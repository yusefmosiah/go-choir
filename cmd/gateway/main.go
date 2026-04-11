package main

import (
	"log"

	"github.com/yusefmosiah/go-choir/internal/gateway"
	"github.com/yusefmosiah/go-choir/internal/provider"
	"github.com/yusefmosiah/go-choir/internal/server"
)

func main() {
	cfg := gateway.LoadConfig()

	s := server.NewServer("gateway", cfg.Port)

	// Initialize the identity registry for sandbox credential management.
	registry := gateway.NewIdentityRegistry(cfg.SandboxTokenTTL)

	// Resolve the real provider (Bedrock or Z.AI) from environment variables.
	// The provider credentials remain host-side and are never exposed to
	// sandbox callers or browsers (VAL-GATEWAY-004).
	var p provider.Provider
	realProvider, err := provider.ResolveProvider()
	if err != nil {
		log.Printf("gateway: provider resolution failed: %v", err)
	}
	if realProvider != nil {
		p = realProvider
		log.Printf("gateway: using real provider: %s", realProvider.Name())
	} else {
		log.Printf("gateway: no real provider configured; inference requests will fail")
	}

	// Create the gateway handler with registry and provider.
	handler := gateway.NewHandler(registry, p)
	gateway.RegisterRoutes(s, handler)

	s.Start()
}
