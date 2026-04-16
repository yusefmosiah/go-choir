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

	// Initialize the terminal PTY WebSocket handler.
	terminalHandler := sandbox.NewTerminalHandler()
	sandbox.RegisterTerminalRoutes(s, terminalHandler)

	// Initialize the runtime engine with persisted state.
	rtRuntimeCfg := runtime.LoadConfig()
	rtCfg := runtime.Config{
		SandboxID:           cfg.SandboxID,
		StorePath:           cfg.StorePath,
		PromptRoot:          rtRuntimeCfg.PromptRoot,
		ProviderTimeout:     rtRuntimeCfg.ProviderTimeout,
		SupervisionInterval: rtRuntimeCfg.SupervisionInterval,
		ResearcherCount:     rtRuntimeCfg.ResearcherCount,
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

	rt := runtime.New(rtCfg, db, bus, rtProvider, rtOpts...)
	// Default-on: install the full per-profile tool registry. Set
	// RUNTIME_DISABLE_TOOLS=1 to opt out (for stub-only tests where no tools
	// should run). RUNTIME_ENABLE_TOOLS is still honored for back-compat but
	// is no longer required.
	if os.Getenv("RUNTIME_DISABLE_TOOLS") == "" {
		toolCWD := os.Getenv("RUNTIME_TOOL_CWD")
		if err := rt.InstallDefaultAgentTools(toolCWD); err != nil {
			log.Fatalf("sandbox: install default agent tools: %v", err)
		}
		superTools := 0
		if registry := rt.ToolRegistryForProfile(runtime.AgentProfileSuper); registry != nil {
			superTools = registry.Size()
		}
		log.Printf("sandbox: tool profiles enabled (conductor=%d super=%d researcher=%d vtext=%d)",
			sizeOfRegistry(rt.ToolRegistryForProfile(runtime.AgentProfileConductor)),
			superTools,
			sizeOfRegistry(rt.ToolRegistryForProfile(runtime.AgentProfileResearcher)),
			sizeOfRegistry(rt.ToolRegistryForProfile(runtime.AgentProfileVText)),
		)
	} else {
		log.Printf("sandbox: tool profiles DISABLED via RUNTIME_DISABLE_TOOLS (stub-only mode)")
	}

	// Register runtime API routes (overrides default /health).
	apiHandler := runtime.NewAPIHandler(rt)
	runtime.RegisterRoutes(s, apiHandler)

	// Start the runtime engine and supervisor.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log.Printf("sandbox: orchestration topology (super=1, researchers=%d)", rtCfg.ResearcherCount)
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

func sizeOfRegistry(registry *runtime.ToolRegistry) int {
	if registry == nil {
		return 0
	}
	return registry.Size()
}
