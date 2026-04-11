package main

import (
	"context"
	"log"
	"os"

	"github.com/yusefmosiah/go-choir/internal/events"
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

	// Resolve the provider: use a real Bedrock or Z.AI provider when
	// credentials are available, otherwise fall back to the stub provider.
	var rtProvider runtime.Provider
	realProvider, err := provider.ResolveProvider()
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
