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
		Model:     "", // model is set by the provider internally
		System:    req.System,
		Messages:  convertRawMessages(req.Messages),
		Tools:     convertToolLoopDefs(req.ToolDefinitions),
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

// extractToolCalls extracts tool calls from an LLM response that has
// structured tool_use content blocks already parsed into ToolCalls.
// This is the bridge between the provider package's ContentToolCall
// representation and the runtime's types.ToolCall consumed by the
// tool-calling loop.
func extractToolCalls(resp *LLMResponse) []types.ToolCall {
	if len(resp.ToolCalls) == 0 {
		return nil
	}
	calls := make([]types.ToolCall, 0, len(resp.ToolCalls))
	for _, tc := range resp.ToolCalls {
		calls = append(calls, types.ToolCall{
			ID:        tc.ID,
			Name:      tc.Name,
			Arguments: canonicalJSON(tc.Arguments),
		})
	}
	return calls
}

// convertRawMessages converts raw JSON messages from the tool-loop request
// into the provider package's Message format. This preserves all structured
// content block fields (id, name, input for tool_use; tool_use_id, content,
// is_error for tool_result) so that the conversation history round-trips
// correctly through the Anthropic Messages API.
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
				content = append(content, Block{
					Type: "text",
					Text: block.Text,
				})
			case "tool_result":
				resultText := block.Content
				if resultText == "" {
					resultText = block.Text
				}
				content = append(content, Block{
					Type:      "tool_result",
					Text:      resultText,
					ToolUseID: block.ToolUseID,
					IsError:   block.IsError,
				})
			case "tool_use":
				content = append(content, Block{
					Type:  "tool_use",
					ID:    block.ID,
					Name:  block.Name,
					Input: canonicalJSON(block.Input),
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

// GatewayBridgeProvider adapts the gateway.GatewayClient to both the
// runtime.Provider and runtime.ToolLoopProvider interfaces. When the
// sandbox runtime is configured with a gateway URL (PROXY_VMCTL_URL /
// RUNTIME_GATEWAY_URL), LLM calls route through the host-side gateway
// instead of resolving providers directly. This ensures provider
// credentials stay host-side and sandbox processes never touch them
// directly (VAL-GATEWAY-001, VAL-GATEWAY-004).
//
// The GatewayBridgeProvider delegates all LLM calls to the underlying
// gateway.GatewayClient, which authenticates to the gateway using a
// sandbox credential token.
type GatewayBridgeProvider struct {
	client GatewayCaller
}

// GatewayCaller is the interface satisfied by gateway.GatewayClient.
// Extracted as a minimal interface so the provider package does not
// import the gateway package directly (avoiding potential circular
// dependencies).
type GatewayCaller interface {
	// Name returns the provider name for observability.
	Name() string
	// IsReal returns true because the gateway routes to real upstream providers.
	IsReal() bool
	// Call sends the LLM request through the gateway.
	Call(ctx context.Context, req LLMRequest) (*LLMResponse, error)
}

// NewGatewayBridgeProvider creates a GatewayBridgeProvider from a
// GatewayCaller. The caller must be non-nil.
func NewGatewayBridgeProvider(client GatewayCaller) *GatewayBridgeProvider {
	if client == nil {
		panic("gateway bridge provider requires a non-nil client")
	}
	return &GatewayBridgeProvider{client: client}
}

// ProviderName returns the underlying gateway client name for observability.
func (g *GatewayBridgeProvider) ProviderName() string { return g.client.Name() }

// Execute implements the runtime.Provider interface. It translates the
// task prompt into an LLM request and routes it through the gateway.
func (g *GatewayBridgeProvider) Execute(ctx context.Context, task *types.TaskRecord, emit runtime.EventEmitFunc) error {
	emit(types.EventTaskProgress, "execution", json.RawMessage(`{"status":"started","provider":"gateway","routed":true}`))

	req := LLMRequest{
		Model:  "",
		System: "You are a helpful assistant running inside the ChoirOS sandbox runtime. Respond concisely and helpfully.",
		Messages: []Message{
			{Role: "user", Content: []Block{{Type: "text", Text: task.Prompt}}},
		},
		MaxTokens: 4096,
		Stream:    false,
	}

	log.Printf("gateway-bridge: calling gateway for task %s (prompt_len=%d)", task.TaskID, len(task.Prompt))

	resp, err := g.client.Call(ctx, req)
	if err != nil {
		failPayload, _ := json.Marshal(map[string]string{
			"status":   "failed",
			"provider": "gateway",
			"routed":   "true",
			"error":    err.Error(),
		})
		emit(types.EventTaskProgress, "execution", failPayload)
		return fmt.Errorf("gateway call failed: %w", err)
	}

	progressPayload, _ := json.Marshal(map[string]string{
		"status":       "responded",
		"provider":     resp.ProviderName,
		"routed":       "true",
		"model":        resp.Model,
		"stop_reason":  resp.StopReason,
		"tokens_in":    fmt.Sprintf("%d", resp.Usage.InputTokens),
		"tokens_out":   fmt.Sprintf("%d", resp.Usage.OutputTokens),
	})
	emit(types.EventTaskProgress, "execution", progressPayload)

	deltaPayload, _ := json.Marshal(map[string]string{
		"text":     resp.Text,
		"provider": resp.ProviderName,
		"routed":   "true",
	})
	emit(types.EventTaskDelta, "execution", deltaPayload)

	task.Result = resp.Text

	log.Printf("gateway-bridge: gateway completed for task %s (provider=%s tokens=%d+%d text_len=%d)",
		task.TaskID, resp.ProviderName, resp.Usage.InputTokens, resp.Usage.OutputTokens, len(resp.Text))

	return nil
}

// CallWithTools implements the runtime.ToolLoopProvider interface.
// It translates the tool-loop request into a gateway LLM request and
// returns a response that may contain tool calls.
func (g *GatewayBridgeProvider) CallWithTools(ctx context.Context, req runtime.ToolLoopRequest) (*runtime.ToolLoopResponse, error) {
	llmReq := LLMRequest{
		Model:     "",
		System:    req.System,
		Messages:  convertRawMessages(req.Messages),
		Tools:     convertToolLoopDefs(req.ToolDefinitions),
		MaxTokens: req.MaxTokens,
		Stream:    false,
	}

	log.Printf("gateway-bridge: calling gateway with %d tools (messages=%d)", len(req.ToolDefinitions), len(req.Messages))

	resp, err := g.client.Call(ctx, llmReq)
	if err != nil {
		return nil, fmt.Errorf("gateway call failed: %w", err)
	}

	tlr := &runtime.ToolLoopResponse{
		ID:         resp.ID,
		StopReason: convertStopReason(resp.StopReason),
		Text:       resp.Text,
		Usage: runtime.TokenUsage{
			InputTokens:  resp.Usage.InputTokens,
			OutputTokens: resp.Usage.OutputTokens,
		},
		Model: resp.Model,
	}

	if tlr.StopReason == "tool_use" {
		tlr.ToolCalls = extractToolCalls(resp)
	}

	log.Printf("gateway-bridge: gateway responded (stop=%s text_len=%d tool_calls=%d)", tlr.StopReason, len(tlr.Text), len(tlr.ToolCalls))

	return tlr, nil
}

// convertToolLoopDefs converts runtime.ToolDefinition values to provider
// ToolDef values for inclusion in the LLM request. This bridges the gap
// between the runtime's tool schema format and the Anthropic Messages
// API tool definition format.
func convertToolLoopDefs(defs []runtime.ToolDefinition) []ToolDef {
	if len(defs) == 0 {
		return nil
	}
	out := make([]ToolDef, 0, len(defs))
	for _, def := range defs {
		out = append(out, ToolDef{
			Name:        def.Name,
			Description: def.Description,
			InputSchema: def.Parameters,
		})
	}
	return out
}
