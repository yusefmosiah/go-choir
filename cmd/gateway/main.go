package main

import (
	"log"
	"os"
	"strings"

	"github.com/yusefmosiah/go-choir/internal/gateway"
	"github.com/yusefmosiah/go-choir/internal/provider"
	"github.com/yusefmosiah/go-choir/internal/server"
)

func main() {
	cfg := gateway.LoadConfig()

	s := server.NewServer("gateway", cfg.Port)

	// Initialize the identity registry for sandbox credential management.
	registry := gateway.NewIdentityRegistry(cfg.SandboxTokenTTL)

	// Resolve all available real providers from environment credentials
	// using the MultiProvider for multi-provider routing. The gateway
	// routes requests to the correct provider based on the provider field
	// or model parameter (VAL-LLM-001, VAL-LLM-005).
	// Provider credentials remain host-side and are never exposed to
	// sandbox callers or browsers (VAL-GATEWAY-004).
	providerCfg := loadProviderConfig()
	mp := provider.ResolveAll(providerCfg)
	providerNames := mp.Names()

	var handler *gateway.Handler

	if len(providerNames) > 0 {
		log.Printf("gateway: resolved %d provider(s): %v", len(providerNames), providerNames)

		// Initialize per-sandbox rate limiting (VAL-GATEWAY-005).
		rlCfg := gateway.LoadRateLimiterConfig()
		rl := gateway.NewPerSandboxRateLimiter(rlCfg.MaxRequests, rlCfg.WindowSize)
		log.Printf("gateway: rate limiter enabled: %s", rl)

		handler = gateway.NewMultiHandlerWithRateLimit(registry, mp, rl)
	} else {
		log.Printf("gateway: no real provider configured; inference requests will fail")

		// Fall back to single-provider mode with nil provider.
		rlCfg := gateway.LoadRateLimiterConfig()
		rl := gateway.NewPerSandboxRateLimiter(rlCfg.MaxRequests, rlCfg.WindowSize)
		handler = gateway.NewHandlerWithRateLimit(registry, nil, rl)
	}

	gateway.RegisterRoutes(s, handler)

	s.Start()
}

// loadProviderConfig builds a ProviderConfig from environment variables.
// Model selection is a runtime concern resolved here at the gateway entry
// point, not inside the provider package. Credentials remain in env vars
// and are resolved by the provider FromEnv functions.
func loadProviderConfig() provider.ProviderConfig {
	cfg := provider.ProviderConfig{
		BedrockModels: []string{
			"us.anthropic.claude-haiku-4-5-20251001-v1:0",
			"us.anthropic.claude-sonnet-4-6",
			"us.anthropic.claude-opus-4-6-v1",
		},
		ZAIModels: []string{"glm-5.1", "glm-5-turbo"},
		FireworksModels: []string{
			"accounts/fireworks/routers/kimi-k2p5-turbo",
		},
		ChatGPTModels:          []string{"gpt-5.5", "gpt-5.4", "gpt-5.4-mini"},
		ChatGPTReasoningEffort: "low",
	}

	// Allow overrides for non-default setups.
	if v := os.Getenv("GATEWAY_BEDROCK_MODELS"); v != "" {
		cfg.BedrockModels = strings.Split(v, ",")
	}
	if v := os.Getenv("GATEWAY_ZAI_MODELS"); v != "" {
		cfg.ZAIModels = strings.Split(v, ",")
	}
	if v := os.Getenv("GATEWAY_FIREWORKS_MODELS"); v != "" {
		cfg.FireworksModels = strings.Split(v, ",")
	}
	if v := os.Getenv("GATEWAY_CHATGPT_MODELS"); v != "" {
		cfg.ChatGPTModels = strings.Split(v, ",")
	}
	if v := os.Getenv("GATEWAY_CHATGPT_REASONING_EFFORT"); v != "" {
		cfg.ChatGPTReasoningEffort = v
	}

	return cfg
}
