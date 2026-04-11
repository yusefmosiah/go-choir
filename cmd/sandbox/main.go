package main

import (
	"context"
	"log"
	"os"

	"github.com/yusefmosiah/go-choir/internal/events"
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
	provider := runtime.NewStubProvider(rtCfg.ProviderTimeout)
	rt := runtime.New(rtCfg, db, bus, provider)

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
