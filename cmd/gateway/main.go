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

	// Resolve the real provider from environment credentials and runtime
	// model config. Provider credentials remain host-side and are never
	// exposed to sandbox callers or browsers (VAL-GATEWAY-004).
	var p provider.Provider
	realProvider, err := provider.ResolveProvider(loadProviderConfig())
	if err != nil {
		log.Printf("gateway: provider resolution failed: %v", err)
	}
	if realProvider != nil {
		p = realProvider
		log.Printf("gateway: using real provider: %s", realProvider.Name())
	} else {
		log.Printf("gateway: no real provider configured; inference requests will fail")
	}

	// Initialize per-sandbox rate limiting (VAL-GATEWAY-005).
	rlCfg := gateway.LoadRateLimiterConfig()
	rl := gateway.NewPerSandboxRateLimiter(rlCfg.MaxRequests, rlCfg.WindowSize)
	log.Printf("gateway: rate limiter enabled: %s", rl)

	// Create the gateway handler with registry, provider, and rate limiter.
	handler := gateway.NewHandlerWithRateLimit(registry, p, rl)
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

	return cfg
}
