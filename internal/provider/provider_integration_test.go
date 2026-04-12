package provider

import (
	"context"
	"os"
	"testing"
)

// TestIntegrationAllModelsLive calls each supported model with real
// credentials from the environment. It is skipped unless the env var
// RUN_INTEGRATION_TESTS is set.
func TestIntegrationAllModelsLive(t *testing.T) {
	if os.Getenv("RUN_INTEGRATION_TESTS") == "" {
		t.Skip("skipping integration test (set RUN_INTEGRATION_TESTS=1)")
	}

	cfg := ProviderConfig{
		BedrockModels: []string{
			"us.anthropic.claude-haiku-4-5-20251001-v1:0",
			"us.anthropic.claude-sonnet-4-6",
			"us.anthropic.claude-opus-4-6-v1",
		},
		ZAIModels: []string{"glm-5.1", "glm-5-turbo"},
		FireworksModels: []string{
			"accounts/fireworks/routers/kimi-k2p5-turbo",
		},
	}

	mp := ResolveAll(cfg)
	names := mp.Names()
	if len(names) == 0 {
		t.Fatal("expected at least one provider to resolve from env credentials")
	}
	t.Logf("resolved providers: %v", names)

	req := LLMRequest{
		System:    "Respond with exactly: hello",
		Messages:  []Message{{Role: "user", Content: []Block{{Type: "text", Text: "Say hello"}}}},
		MaxTokens: 64,
	}

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			p := mp.Get(name)
			if p == nil {
				t.Fatalf("provider %q not found", name)
			}

			resp, err := p.Call(context.Background(), req)
			if err != nil {
				// For debugging: make a raw request to see the full error.
				t.Fatalf("call failed: %v", err)
			}

			if resp.Text == "" {
				t.Error("expected non-empty response text")
			}
			if resp.StopReason != "end_turn" && resp.StopReason != "stop" {
				t.Errorf("unexpected stop_reason: %s", resp.StopReason)
			}

			t.Logf("provider=%s model=%s stop=%s tokens=%d+%d text=%q",
				name, resp.Model, resp.StopReason,
				resp.Usage.InputTokens, resp.Usage.OutputTokens, resp.Text)
		})
	}
}
