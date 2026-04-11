// Package sandbox provides the sandbox service that hosts the placeholder
// shell handlers and the runtime engine for Mission 3.
//
// The sandbox service runs as a host process on port 8085 (during the
// host-process milestone) and provides both the legacy placeholder endpoints
// and the real runtime API endpoints for task submission, status lookup,
// and event streaming.
package sandbox

import (
	"os"
)

// Config holds the sandbox service configuration resolved from environment
// variables.
type Config struct {
	// Port is the listen port for the sandbox HTTP server.
	Port string

	// SandboxID is the stable identity string returned in bootstrap and
	// validation responses. It proves which sandbox instance handled a request.
	SandboxID string

	// StorePath is the path to the SQLite database for runtime state.
	// If empty, the runtime package default is used.
	StorePath string
}

// LoadConfig resolves sandbox configuration from environment variables.
func LoadConfig() Config {
	port := portFromEnv("SANDBOX_PORT", "8085")
	sandboxID := fromEnv("SANDBOX_ID", "sandbox-dev")
	storePath := fromEnv("RUNTIME_STORE_PATH", "")
	return Config{
		Port:      port,
		SandboxID: sandboxID,
		StorePath: storePath,
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
