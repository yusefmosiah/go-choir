// Package runtime provides the host-process runtime engine for the go-choir
// sandbox. It manages task lifecycle, event emission, health state, and the
// HTTP API surface consumed through the authenticated proxy.
//
// Design decisions:
//   - Runs execute as direct goroutines, not subprocess CLI loops or
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
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultStorePath is the default SQLite database path for runtime state.
	DefaultStorePath = "/tmp/go-choir-m3/runtime.db"

	// DefaultProviderTimeout is how long the stub provider simulates work.
	DefaultProviderTimeout = 2 * time.Second

	// DefaultSupervisionInterval is a legacy reserved setting kept only to avoid
	// churning tests and config plumbing during the runtime cleanup.
	DefaultSupervisionInterval = 5 * time.Second

	// DefaultResearcherCount is the default number of researcher workers
	// the microVM topology should assume when none is configured.
	DefaultResearcherCount = 3
)

// Config holds runtime configuration resolved from environment variables.
type Config struct {
	// SandboxID is the stable identity of this sandbox instance.
	SandboxID string

	// StorePath is the path to the SQLite database for task/event persistence.
	StorePath string

	// PromptRoot is the sandbox-owned filesystem root for editable role prompts.
	PromptRoot string

	// ProviderTimeout is the simulated work duration for the stub provider.
	ProviderTimeout time.Duration

	// SupervisionInterval is legacy reserved configuration. The old polling
	// supervisor has been deleted and this value is currently unused.
	SupervisionInterval time.Duration

	// ResearcherCount is the configured researcher worker count for this VM.
	ResearcherCount int
}

// LoadConfig resolves runtime configuration from environment variables.
func LoadConfig() Config {
	storePath := envOr("RUNTIME_STORE_PATH", DefaultStorePath)
	return Config{
		SandboxID:           envOr("SANDBOX_ID", "sandbox-dev"),
		StorePath:           storePath,
		PromptRoot:          envOr("RUNTIME_PROMPT_ROOT", defaultPromptRoot(storePath)),
		ProviderTimeout:     durationOr("RUNTIME_PROVIDER_TIMEOUT", DefaultProviderTimeout),
		SupervisionInterval: durationOr("RUNTIME_SUPERVISION_INTERVAL", DefaultSupervisionInterval),
		ResearcherCount:     intOr("RUNTIME_RESEARCHER_COUNT", DefaultResearcherCount),
	}
}

func normalizeConfig(cfg Config) Config {
	if strings.TrimSpace(cfg.StorePath) == "" {
		cfg.StorePath = DefaultStorePath
	}
	if strings.TrimSpace(cfg.PromptRoot) == "" {
		cfg.PromptRoot = defaultPromptRoot(cfg.StorePath)
	}
	return cfg
}

func defaultPromptRoot(storePath string) string {
	if strings.TrimSpace(storePath) == "" {
		storePath = DefaultStorePath
	}
	return filepath.Join(filepath.Dir(storePath), "prompts")
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

func intOr(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return fallback
	}
	return n
}
