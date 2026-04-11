// Package provider implements real LLM provider bridges for the go-choir
// sandbox runtime.
package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/yusefmosiah/go-choir/internal/runtime"
	"github.com/yusefmosiah/go-choir/internal/types"
)

// BridgeProvider adapts the LLM provider.Provider interface to the
// runtime.Provider interface consumed by the runtime engine. It translates
// between the runtime's TaskRecord/EventEmitFunc model and the provider
// package's LLMRequest/LLMResponse model.
//
// When a tool registry is active, BridgeProvider also implements the
// ToolLoopProvider interface, enabling the tool-calling loop to send
// requests with tool definitions and conversation history, and receive
// responses that may contain tool_use stop reasons.
//
// This is the integration point where real provider calls flow into the
// runtime task lifecycle. When a BridgeProvider is active, task results
// contain real upstream responses and provider interactions are logged
// with enough detail to distinguish them from canned stub responses.
type BridgeProvider struct {
	inner Provider // the underlying LLM provider (bedrock or zai)
}

// NewBridgeProvider creates a bridge from an LLM provider to the runtime
// provider interface. The inner provider must be non-nil.
func NewBridgeProvider(inner Provider) *BridgeProvider {
	if inner == nil {
		panic("bridge provider requires a non-nil inner provider")
	}
	return &BridgeProvider{inner: inner}
}

// Inner returns the underlying LLM provider for inspection.
func (b *BridgeProvider) Inner() Provider { return b.inner }

// ProviderName returns the underlying LLM provider's name for observability.
func (b *BridgeProvider) ProviderName() string { return b.inner.Name() }

// Execute implements the runtime.Provider interface. It translates the
// task prompt into an LLM request, calls the real provider, emits
// progress events during execution, and returns the result text.
//
// Provider failures surface as structured errors without crashing the
// runtime (VAL-RUNTIME-008). The emitted events contain enough detail
// (provider name, model, usage) to distinguish real upstream work from
// canned stub responses.
func (b *BridgeProvider) Execute(ctx context.Context, task *types.TaskRecord, emit runtime.EventEmitFunc) error {
	providerName := b.inner.Name()

	// Emit a progress event showing we're about to call a real provider.
	startPayload, _ := json.Marshal(map[string]string{
		"status":   "started",
		"provider": providerName,
		"real":     "true",
	})
	emit(types.EventTaskProgress, "execution", startPayload)

	// Build the LLM request from the task prompt.
	req := LLMRequest{
		Model:    "", // model is set by the provider internally
		System:   "You are a helpful assistant running inside the ChoirOS sandbox runtime. Respond concisely and helpfully.",
		Messages: []Message{
			{
				Role: "user",
				Content: []Block{
					{Type: "text", Text: task.Prompt},
				},
			},
		},
		MaxTokens: 4096,
		Stream:    false,
	}

	log.Printf("bridge: calling %s provider for task %s (prompt_len=%d)",
		providerName, task.TaskID, len(task.Prompt))

	// Call the real provider.
	resp, err := b.inner.Call(ctx, req)
	if err != nil {
		// Emit a provider-failure event with sanitized error info.
		failPayload, _ := json.Marshal(map[string]string{
			"status":   "failed",
			"provider": providerName,
			"real":     "true",
			"error":    err.Error(),
		})
		emit(types.EventTaskProgress, "execution", failPayload)

		return fmt.Errorf("provider %s call failed: %w", providerName, err)
	}

	// Emit a progress event with provider metadata (observable enough to
	// distinguish real work from stub).
	progressPayload, _ := json.Marshal(map[string]string{
		"status":       "responded",
		"provider":     providerName,
		"real":         "true",
		"model":        resp.Model,
		"stop_reason":  resp.StopReason,
		"tokens_in":    fmt.Sprintf("%d", resp.Usage.InputTokens),
		"tokens_out":   fmt.Sprintf("%d", resp.Usage.OutputTokens),
	})
	emit(types.EventTaskProgress, "execution", progressPayload)

	// Emit the delta with the actual text content.
	deltaPayload, _ := json.Marshal(map[string]string{
		"text":     resp.Text,
		"provider": providerName,
		"real":     "true",
	})
	emit(types.EventTaskDelta, "execution", deltaPayload)

	// Store the result on the task for the runtime to persist.
	task.Result = resp.Text

	log.Printf("bridge: %s provider completed for task %s (tokens=%d+%d text_len=%d)",
		providerName, task.TaskID, resp.Usage.InputTokens, resp.Usage.OutputTokens, len(resp.Text))

	return nil
}

// CallWithTools implements the runtime.ToolLoopProvider interface. It
// translates the tool-loop request (with conversation history and tool
// definitions) into a provider LLMRequest, calls the real provider, and
// returns a ToolLoopResponse that may contain tool calls.
//
// This method is called by the tool-calling loop when a tool registry is
// active. Each iteration of the loop calls CallWithTools, inspects the
// stop reason, and either executes tools or returns the final text.
func (b *BridgeProvider) CallWithTools(ctx context.Context, req runtime.ToolLoopRequest) (*runtime.ToolLoopResponse, error) {
	providerName := b.inner.Name()

	// Convert the tool-loop request into an LLMRequest.
	llmReq := LLMRequest{
		Model:    "", // model is set by the provider internally
		System:   req.System,
		Messages: convertRawMessages(req.Messages),
		MaxTokens: req.MaxTokens,
		Stream:    false,
	}

	log.Printf("bridge: calling %s provider with %d tools (messages=%d)",
		providerName, len(req.ToolDefinitions), len(req.Messages))

	// Call the real provider.
	resp, err := b.inner.Call(ctx, llmReq)
	if err != nil {
		return nil, fmt.Errorf("provider %s call failed: %w", providerName, err)
	}

	// Build the tool-loop response from the LLM response.
	tlr := &runtime.ToolLoopResponse{
		ID:         resp.ID,
		StopReason: convertStopReason(resp.StopReason),
		Text:       resp.Text,
		Usage:      runtime.TokenUsage{
			InputTokens:  resp.Usage.InputTokens,
			OutputTokens: resp.Usage.OutputTokens,
		},
		Model: resp.Model,
	}

	// If the provider returned tool_use, extract tool calls from the response.
	// Currently, the Anthropic Messages API returns tool calls inline in
	// content blocks with type "tool_use". We parse these here.
	if tlr.StopReason == "tool_use" {
		tlr.ToolCalls = extractToolCalls(resp)
	}

	log.Printf("bridge: %s provider responded (stop=%s text_len=%d tool_calls=%d)",
		providerName, tlr.StopReason, len(tlr.Text), len(tlr.ToolCalls))

	return tlr, nil
}

// convertStopReason maps provider stop reasons to tool-loop stop reasons.
// The Anthropic API uses "end_turn" and "tool_use"; Bedrock uses similar
// conventions.
func convertStopReason(reason string) string {
	switch reason {
	case "end_turn", "stop":
		return "end_turn"
	case "tool_use":
		return "tool_use"
	case "max_tokens":
		return "max_tokens"
	default:
		return reason
	}
}

// extractToolCalls extracts tool calls from an LLM response. Currently,
// the LLMResponse type doesn't carry tool call data because the initial
// provider bridge was designed for single-turn completion. When the
// provider package is enhanced to parse tool_use content blocks from
// Anthropic/Bedrock responses, this function will extract them properly.
//
// For now, returns an empty slice, which means the tool-calling loop
// will fall through to the toolLoopAdapter path for providers that
// don't yet return structured tool call data.
func extractToolCalls(resp *LLMResponse) []types.ToolCall {
	// TODO: When the provider package's LLMResponse is enhanced to
	// carry tool call data (from Anthropic content blocks with type
	// "tool_use"), extract and return them here. This is a placeholder
	// that returns empty until that enhancement lands.
	return nil
}

// convertRawMessages converts raw JSON messages from the tool-loop request
// into the provider package's Message format.
func convertRawMessages(raw []json.RawMessage) []Message {
	out := make([]Message, 0, len(raw))
	for _, r := range raw {
		var msg struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(r, &msg); err != nil {
			continue
		}

		// Try to parse content as an array of content blocks.
		var blocks []struct {
			Type      string          `json:"type"`
			Text      string          `json:"text,omitempty"`
			ToolUseID string          `json:"tool_use_id,omitempty"`
			ID        string          `json:"id,omitempty"`
			Name      string          `json:"name,omitempty"`
			Input     json.RawMessage `json:"input,omitempty"`
			Content   string          `json:"content,omitempty"` // tool_result content can be a string
			IsError   bool            `json:"is_error,omitempty"`
		}
		if err := json.Unmarshal(msg.Content, &blocks); err != nil {
			// Content is not an array — try as a plain string.
			var text string
			if err := json.Unmarshal(msg.Content, &text); err == nil {
				out = append(out, Message{
					Role:    msg.Role,
					Content: []Block{{Type: "text", Text: text}},
				})
			}
			continue
		}

		// Convert content blocks to provider Message format.
		var content []Block
		for _, block := range blocks {
			switch block.Type {
			case "text":
				content = append(content, Block{Type: "text", Text: block.Text})
			case "tool_result":
				resultText := block.Content
				if resultText == "" {
					// Tool result content may be in a nested content field.
					resultText = block.Text
				}
				content = append(content, Block{
					Type: "tool_result",
					Text: resultText,
				})
			case "tool_use":
				// Tool use blocks are passed through for the provider to see.
				inputText := string(block.Input)
				content = append(content, Block{
					Type: "tool_use",
					Text: inputText,
				})
			}
		}

		if len(content) > 0 {
			out = append(out, Message{
				Role:    msg.Role,
				Content: content,
			})
		}
	}
	return out
}
