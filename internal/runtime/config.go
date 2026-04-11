// Package runtime provides the host-process runtime engine for the go-choir
// sandbox. It manages task lifecycle, event emission, supervision, health
// state, and the HTTP API surface consumed through the authenticated proxy.
//
// Design decisions:
//   - Tasks execute as direct goroutines, not subprocess CLI loops or
//     adapter-wrapper processes.
//   - Provider is an interface; the stub provider simulates execution until
//     the Bedrock/Z.AI bridge feature replaces it with a real provider.
//   - All state is persisted through the store package (SQLite-backed) so
//     task handles and events survive sandbox process restarts.
//   - Health, degradation, and recovery are externally visible through the
//     /health endpoint and event stream.
package runtime

import (
	"os"
	"time"
)

const (
	// DefaultStorePath is the default SQLite database path for runtime state.
	DefaultStorePath = "/tmp/go-choir-m3/runtime.db"

	// DefaultProviderTimeout is how long the stub provider simulates work.
	DefaultProviderTimeout = 2 * time.Second

	// DefaultSupervisionInterval is how often the supervisor checks health.
	DefaultSupervisionInterval = 5 * time.Second
)

// Config holds runtime configuration resolved from environment variables.
type Config struct {
	// SandboxID is the stable identity of this sandbox instance.
	SandboxID string

	// StorePath is the path to the SQLite database for task/event persistence.
	StorePath string

	// ProviderTimeout is the simulated work duration for the stub provider.
	ProviderTimeout time.Duration

	// SupervisionInterval is how often the supervisor checks runtime health.
	SupervisionInterval time.Duration
}

// LoadConfig resolves runtime configuration from environment variables.
func LoadConfig() Config {
	return Config{
		SandboxID:           envOr("SANDBOX_ID", "sandbox-dev"),
		StorePath:           envOr("RUNTIME_STORE_PATH", DefaultStorePath),
		ProviderTimeout:     durationOr("RUNTIME_PROVIDER_TIMEOUT", DefaultProviderTimeout),
		SupervisionInterval: durationOr("RUNTIME_SUPERVISION_INTERVAL", DefaultSupervisionInterval),
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func durationOr(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fallback
	}
	return d
}
