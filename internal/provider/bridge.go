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
