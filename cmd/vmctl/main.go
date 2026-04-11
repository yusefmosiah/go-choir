package main

import (
	"log"
	"os"

	"github.com/yusefmosiah/go-choir/internal/server"
	"github.com/yusefmosiah/go-choir/internal/vmctl"
)

func main() {
	port := server.PortFromEnv("VMCTL_PORT", "8083")

	// The sandbox URL base is where VM-backed sandbox runtimes are
	// reachable. In host-process mode this is the local sandbox.
	// In production with Firecracker, vmctl will return per-VM URLs.
	sandboxURLBase := envOr("VMCTL_SANDBOX_URL_BASE", "http://127.0.0.1:8085")

	registry := vmctl.NewOwnershipRegistry(sandboxURLBase)
	handler := vmctl.NewHandler(registry)

	s := server.NewServer("vmctl", port)
	vmctl.RegisterRoutes(s, handler)

	log.Printf("vmctl: ownership registry initialized (sandbox_url_base=%s)", sandboxURLBase)
	s.Start()
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
