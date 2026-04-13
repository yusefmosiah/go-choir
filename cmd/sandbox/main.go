package main

import (
	"context"
	"log"
	"os"
	"strings"

	"github.com/yusefmosiah/go-choir/internal/events"
	"github.com/yusefmosiah/go-choir/internal/gateway"
	"github.com/yusefmosiah/go-choir/internal/provider"
	"github.com/yusefmosiah/go-choir/internal/runtime"
	"github.com/yusefmosiah/go-choir/internal/sandbox"
	"github.com/yusefmosiah/go-choir/internal/server"
	"github.com/yusefmosiah/go-choir/internal/store"
)

func main() {
	cfg := sandbox.LoadConfig()

	s := server.NewServer("sandbox", cfg.Port)

	// Initialize the placeholder shell handlers.
	h := sandbox.NewHandler(cfg.SandboxID)
	sandbox.RegisterRoutes(s, h)

	// Initialize the file browser handler with sandbox files root.
	filesRoot := os.Getenv("SANDBOX_FILES_ROOT")
	fileHandler := sandbox.NewFilesHandler(filesRoot)
	sandbox.RegisterFileRoutes(s, fileHandler)

	// Initialize the runtime engine with persisted state.
	rtCfg := runtime.Config{
		SandboxID:           cfg.SandboxID,
		StorePath:           cfg.StorePath,
		ProviderTimeout:     runtime.LoadConfig().ProviderTimeout,
		SupervisionInterval: runtime.LoadConfig().SupervisionInterval,
	}
	if rtCfg.StorePath == "" {
		rtCfg.StorePath = runtime.DefaultStorePath
	}

	// Ensure the store directory exists.
	if err := os.MkdirAll(storeDir(rtCfg.StorePath), 0o755); err != nil {
		log.Fatalf("sandbox: create store directory: %v", err)
	}

	db, err := store.Open(rtCfg.StorePath)
	if err != nil {
		log.Fatalf("sandbox: open runtime store: %v", err)
	}
	defer func() {
		_ = db.Close()
	}()

	bus := events.NewEventBus()

	// Resolve the provider:
	//  1. When RUNTIME_GATEWAY_URL is set (VM mode), use the GatewayClient
	//     to route LLM calls through the host-side gateway. This ensures
	//     provider credentials stay host-side and the sandbox never touches
	//     them directly (VAL-GATEWAY-001, VAL-GATEWAY-004).
	//  2. When direct provider credentials are available (Bedrock, Z.AI,
	//     Fireworks), resolve the provider directly. This is the host-process
	//     path used when there is no gateway between the runtime and the
	//     upstream provider.
	//  3. Otherwise, fall back to the stub provider.
	var rtProvider runtime.Provider
	gatewayURL := os.Getenv("RUNTIME_GATEWAY_URL")
	if gatewayURL == "" {
		// Fallback: also check PROXY_VMCTL_URL which signals VM mode.
		gatewayURL = os.Getenv("PROXY_VMCTL_URL")
	}

	if gatewayURL != "" {
		gatewayToken := os.Getenv("RUNTIME_GATEWAY_TOKEN")
		client := gateway.NewGatewayClient(gatewayURL, gatewayToken)
		rtProvider = provider.NewGatewayBridgeProvider(client)
		log.Printf("sandbox: using gateway provider (url=%s)", gatewayURL)
	} else {
		realProvider, err := provider.ResolveProvider(loadProviderConfig())
		if err != nil {
			log.Printf("sandbox: provider resolution failed, using stub: %v", err)
		}
		if realProvider != nil {
			rtProvider = provider.NewBridgeProvider(realProvider)
			log.Printf("sandbox: using real provider: %s", realProvider.Name())
		} else {
			rtProvider = runtime.NewStubProvider(rtCfg.ProviderTimeout)
			log.Printf("sandbox: using stub provider (no credentials configured)")
		}
	}

	// Build runtime options based on configuration.
	var rtOpts []runtime.RuntimeOption

	// If the environment requests tool support, configure the tool registry
	// with built-in tools. The registry starts empty but is ready for tool
	// registration by future features (e-text appagent, terminal, files, etc.).
	if os.Getenv("RUNTIME_ENABLE_TOOLS") != "" {
		registry := runtime.NewToolRegistry()
		if registry.Size() > 0 {
			rtOpts = append(rtOpts, runtime.WithToolRegistry(registry))
			log.Printf("sandbox: tool registry enabled with %d tools", registry.Size())
		} else {
			log.Printf("sandbox: RUNTIME_ENABLE_TOOLS set but no tools registered yet")
		}
	}

	rt := runtime.New(rtCfg, db, bus, rtProvider, rtOpts...)

	// Register runtime API routes (overrides default /health).
	apiHandler := runtime.NewAPIHandler(rt)
	runtime.RegisterRoutes(s, apiHandler)

	// Start the runtime engine and supervisor.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rt.Start(ctx)

	s.Start()
}

// storeDir extracts the directory portion of a file path.
func storeDir(path string) string {
	if path == "" {
		return "/tmp/go-choir-m3"
	}
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[:i]
		}
	}
	return "."
}

// loadProviderConfig builds a ProviderConfig from environment variables.
// Model selection is a runtime concern resolved here at the sandbox entry
// point, not inside the provider package.
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

	if v := os.Getenv("SANDBOX_BEDROCK_MODELS"); v != "" {
		cfg.BedrockModels = strings.Split(v, ",")
	}
	if v := os.Getenv("SANDBOX_ZAI_MODELS"); v != "" {
		cfg.ZAIModels = strings.Split(v, ",")
	}
	if v := os.Getenv("SANDBOX_FIREWORKS_MODELS"); v != "" {
		cfg.FireworksModels = strings.Split(v, ",")
	}

	return cfg
}
