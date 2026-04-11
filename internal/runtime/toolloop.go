package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/yusefmosiah/go-choir/internal/types"
)

// ToolLoopProvider extends the Provider interface with tool-calling
// capabilities. When the LLM returns a tool_use stop reason, the
// tool-calling loop needs to:
//  1. Parse the tool calls from the response
//  2. Execute them via the ToolRegistry
//  3. Feed the results back into the next LLM call
//
// This interface separates the tool-loop orchestration (owned by the
// runtime) from the LLM API mechanics (owned by the provider). The
// BridgeProvider implements this interface when wrapping a real LLM
// provider; the StubProvider implements it with optional tool simulation.
type ToolLoopProvider interface {
	Provider

	// CallWithTools sends a request with tool definitions and conversation
	// history, returning a response that may contain tool calls. This is the
	// primitive used by the tool-calling loop: each iteration calls
	// CallWithTools, inspects the stop reason, and either executes tools
	// or returns the final text.
	CallWithTools(ctx context.Context, req ToolLoopRequest) (*ToolLoopResponse, error)
}

// ToolLoopRequest is the request shape for the tool-calling loop. It carries
// the full conversation history including prior tool results, the available
// tool definitions, and the system prompt.
type ToolLoopRequest struct {
	// System is the system prompt (potentially including the tool catalog).
	System string `json:"system"`

	// Messages is the conversation history in Anthropic Messages format.
	// Each entry is a raw JSON message object with role and content fields.
	Messages []json.RawMessage `json:"messages"`

	// ToolDefinitions is the list of available tool schemas.
	ToolDefinitions []ToolDefinition `json:"tool_definitions"`

	// MaxTokens is the maximum output tokens for this call.
	MaxTokens int `json:"max_tokens"`
}

// ToolLoopResponse is the response from a single LLM call in the tool-calling
// loop. It may contain text output, tool calls, or both, depending on the
// stop reason.
type ToolLoopResponse struct {
	// ID is the provider-assigned response identifier.
	ID string `json:"id"`

	// StopReason is why the model stopped: "tool_use", "end_turn", "max_tokens",
	// or other provider-specific reasons.
	StopReason string `json:"stop_reason"`

	// Text is the concatenated text content from the response. May be empty
	// if the model only produced tool calls.
	Text string `json:"text"`

	// ToolCalls contains the tool invocation requests from the provider.
	// Non-empty only when StopReason is "tool_use".
	ToolCalls []types.ToolCall `json:"tool_calls,omitempty"`

	// Usage contains token usage information.
	Usage TokenUsage `json:"usage"`

	// Model is the model that produced the response.
	Model string `json:"model"`
}

// TokenUsage tracks token counts for a tool-loop response.
type TokenUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// maxToolLoopIterations prevents infinite tool-calling loops. If the LLM
// keeps requesting tool use without reaching an end_turn, we bail out
// after this many iterations.
const maxToolLoopIterations = 25

// RunToolLoop executes the tool-calling loop: call the LLM, execute any
// requested tools, feed results back, and repeat until the model returns
// end_turn or the context is cancelled.
//
// This is adapted from Cogent's runToolLoop but simplified for go-choir:
//   - No session history management (the runtime loop manages conversation
//     state per task).
//   - No steer/interrupt mechanism (tasks are atomic from the runtime's
//     perspective; steering belongs in the appagent layer).
//   - No history compression (context window management is the provider's
//     concern in go-choir's model).
//   - Tool execution emits observable events through the event bus.
//
// Returns the final text result, total token usage, and any error.
func RunToolLoop(ctx context.Context, provider ToolLoopProvider, registry *ToolRegistry, initialMessages []json.RawMessage, systemPrompt string, maxTokens int, emit EventEmitFunc) (string, TokenUsage, error) {
	var totalUsage TokenUsage
	messages := make([]json.RawMessage, len(initialMessages))
	copy(messages, initialMessages)

	toolDefs := []ToolDefinition{}
	if registry != nil {
		toolDefs = registry.Definitions()
		systemPrompt = buildSystemPromptWithTools(systemPrompt, registry)
	}

	for i := 0; i < maxToolLoopIterations; i++ {
		// Call the LLM with current conversation state.
		resp, err := provider.CallWithTools(ctx, ToolLoopRequest{
			System:          systemPrompt,
			Messages:        messages,
			ToolDefinitions: toolDefs,
			MaxTokens:       maxTokens,
		})
		if err != nil {
			return "", totalUsage, fmt.Errorf("tool loop iteration %d: %w", i, err)
		}

		// Accumulate token usage.
		totalUsage.InputTokens += resp.Usage.InputTokens
		totalUsage.OutputTokens += resp.Usage.OutputTokens

		// Emit progress event for this iteration.
		progressPayload, _ := json.Marshal(map[string]any{
			"iteration":   i + 1,
			"stop_reason": resp.StopReason,
			"tool_calls":  len(resp.ToolCalls),
			"model":       resp.Model,
		})
		emit(types.EventTaskProgress, "tool_loop", progressPayload)

		switch resp.StopReason {
		case "tool_use":
			if len(resp.ToolCalls) == 0 {
				return "", totalUsage, fmt.Errorf("tool loop: provider returned tool_use without tool calls")
			}

			// Append the assistant's response (with tool calls) to conversation.
			assistantMsg, _ := json.Marshal(map[string]any{
				"role":       "assistant",
				"content":    buildAssistantContent(resp.Text, resp.ToolCalls),
			})
			messages = append(messages, assistantMsg)

			// Execute tools and collect results.
			toolResults := executeTools(ctx, registry, resp.ToolCalls, emit)

			// Append tool results as a user message (per Anthropic Messages API convention).
			toolResultMsg, _ := json.Marshal(map[string]any{
				"role":    "user",
				"content": buildToolResultContent(toolResults),
			})
			messages = append(messages, toolResultMsg)

			log.Printf("tool loop: iteration %d, executed %d tools, continuing", i+1, len(resp.ToolCalls))

		case "end_turn", "":
			// Normal completion — return the text.
			return resp.Text, totalUsage, nil

		case "max_tokens":
			return resp.Text, totalUsage, fmt.Errorf("tool loop: model stopped at max_tokens (iteration %d)", i+1)

		default:
			return "", totalUsage, fmt.Errorf("tool loop: unsupported stop reason %q (iteration %d)", resp.StopReason, i+1)
		}
	}

	return "", totalUsage, fmt.Errorf("tool loop: exceeded %d iterations without end_turn", maxToolLoopIterations)
}

// buildAssistantContent constructs the content blocks for an assistant
// message that may contain text and tool calls.
func buildAssistantContent(text string, toolCalls []types.ToolCall) []any {
	var content []any

	// Add text content if present.
	if text != "" {
		content = append(content, map[string]string{
			"type": "text",
			"text": text,
		})
	}

	// Add tool_use blocks for each tool call.
	for _, call := range toolCalls {
		content = append(content, map[string]any{
			"type":  "tool_use",
			"id":    call.ID,
			"name":  call.Name,
			"input": json.RawMessage(call.Arguments),
		})
	}

	return content
}

// buildToolResultContent constructs the content blocks for a user message
// containing tool results, following the Anthropic Messages API convention.
func buildToolResultContent(results []types.ToolResult) []any {
	content := make([]any, 0, len(results))
	for _, result := range results {
		entry := map[string]any{
			"type":      "tool_result",
			"tool_use_id": result.CallID,
			"content":   result.Output,
		}
		if result.IsError {
			entry["is_error"] = true
		}
		content = append(content, entry)
	}
	return content
}

// --- ToolLoopProvider adapter for providers that don't natively support it ---

// toolLoopAdapter wraps a basic Provider to implement ToolLoopProvider by
// converting tool-loop calls into the simpler Provider.Execute interface.
// This is used when a provider (like the StubProvider or BridgeProvider)
// doesn't directly implement CallWithTools.
//
// The adapter converts the tool-loop request into a TaskRecord-like call
// through the Provider.Execute method. It does NOT support actual tool-calling
// (it ignores tool definitions and always returns end_turn), so it should
// only be used when the runtime wants the executeTask path without the
// tool-calling loop.
type toolLoopAdapter struct {
	Provider
}

// CallWithTools implements ToolLoopProvider by delegating to the underlying
// Provider's Execute method. The adapter extracts the last user message as
// the prompt and returns a single-turn end_turn response.
func (a *toolLoopAdapter) CallWithTools(ctx context.Context, req ToolLoopRequest) (*ToolLoopResponse, error) {
	// Extract the last user message as the prompt for the simple provider.
	prompt := extractLastUserMessage(req.Messages)

	task := &types.TaskRecord{
		Prompt: prompt,
	}

	var capturedText string
	emit := func(kind types.EventKind, phase string, payload json.RawMessage) {
		// Capture delta text for the response.
		if kind == types.EventTaskDelta {
			var delta struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(payload, &delta); err == nil && delta.Text != "" {
				capturedText += delta.Text
			}
		}
	}

	err := a.Execute(ctx, task, emit)
	if err != nil {
		return nil, err
	}

	result := capturedText
	if result == "" {
		result = task.Result
	}

	return &ToolLoopResponse{
		StopReason: "end_turn",
		Text:       result,
		Usage:      TokenUsage{},
	}, nil
}

// extractLastUserMessage finds the last user-role message in the conversation
// history and returns its text content. Falls back to an empty string.
func extractLastUserMessage(messages []json.RawMessage) string {
	for i := len(messages) - 1; i >= 0; i-- {
		var msg struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		}
		if err := json.Unmarshal(messages[i], &msg); err != nil {
			continue
		}
		if msg.Role == "user" {
			return extractTextFromContent(msg.Content)
		}
	}
	return ""
}

// extractTextFromContent extracts text from a message content field, which
// may be a string, an array of content blocks, or null.
func extractTextFromContent(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		var text string
		for _, item := range v {
			if block, ok := item.(map[string]any); ok {
				if blockType, _ := block["type"].(string); blockType == "text" {
					if t, _ := block["text"].(string); t != "" {
						text += t
					}
				}
				// Skip tool_result blocks when extracting text.
			}
		}
		return text
	default:
		return ""
	}
}

// asToolLoopProvider converts a Provider to a ToolLoopProvider. If the
// provider already implements ToolLoopProvider, it is returned directly.
// Otherwise, it is wrapped in a toolLoopAdapter that converts tool-loop
// calls into simple provider calls.
func asToolLoopProvider(p Provider) ToolLoopProvider {
	if tlp, ok := p.(ToolLoopProvider); ok {
		return tlp
	}
	return &toolLoopAdapter{Provider: p}
}
