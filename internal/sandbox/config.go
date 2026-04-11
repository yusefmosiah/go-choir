// Package sandbox provides the placeholder sandbox service for Mission 2 Milestone 1.
//
// The placeholder sandbox runs as a host-process upstream on port 8085 and provides
// deterministic behavior for proxy validation: shell bootstrap payload, live
// WebSocket echo, stable sandbox identity, current-user-context echo, and a
// deliberate non-2xx path for passthrough tests.
package sandbox

import (
	"os"
)

// Config holds the placeholder sandbox configuration resolved from environment
// variables.
type Config struct {
	// Port is the listen port for the sandbox HTTP server.
	Port string
	// SandboxID is the stable identity string returned in bootstrap and
	// validation responses. It proves which sandbox instance handled a request.
	SandboxID string
}

// LoadConfig resolves sandbox configuration from environment variables.
func LoadConfig() Config {
	port := portFromEnv("SANDBOX_PORT", "8085")
	sandboxID := fromEnv("SANDBOX_ID", "sandbox-dev")
	return Config{
		Port:      port,
		SandboxID: sandboxID,
	}
}

func portFromEnv(envVar, defaultPort string) string {
	if v := os.Getenv(envVar); v != "" {
		return v
	}
	return defaultPort
}

func fromEnv(envVar, defaultVal string) string {
	if v := os.Getenv(envVar); v != "" {
		return v
	}
	return defaultVal
}
