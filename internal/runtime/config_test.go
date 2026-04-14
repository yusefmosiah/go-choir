package runtime

import (
	"os"
	"testing"
	"time"
)

func TestLoadConfigDefaultsResearcherCount(t *testing.T) {
	t.Setenv("SANDBOX_ID", "")
	t.Setenv("RUNTIME_STORE_PATH", "")
	t.Setenv("RUNTIME_PROVIDER_TIMEOUT", "")
	t.Setenv("RUNTIME_SUPERVISION_INTERVAL", "")
	t.Setenv("RUNTIME_RESEARCHER_COUNT", "")

	cfg := LoadConfig()
	if cfg.ResearcherCount != DefaultResearcherCount {
		t.Fatalf("researcher_count = %d, want %d", cfg.ResearcherCount, DefaultResearcherCount)
	}
}

func TestLoadConfigReadsResearcherCount(t *testing.T) {
	t.Setenv("RUNTIME_RESEARCHER_COUNT", "5")
	t.Setenv("RUNTIME_SUPERVISION_INTERVAL", "7s")
	t.Setenv("RUNTIME_PROVIDER_TIMEOUT", "3s")

	cfg := LoadConfig()
	if cfg.ResearcherCount != 5 {
		t.Fatalf("researcher_count = %d, want 5", cfg.ResearcherCount)
	}
	if cfg.SupervisionInterval != 7*time.Second {
		t.Fatalf("supervision interval = %s, want 7s", cfg.SupervisionInterval)
	}
	if cfg.ProviderTimeout != 3*time.Second {
		t.Fatalf("provider timeout = %s, want 3s", cfg.ProviderTimeout)
	}
}

func TestLoadConfigFallsBackOnInvalidResearcherCount(t *testing.T) {
	_ = os.Setenv("RUNTIME_RESEARCHER_COUNT", "-2")
	t.Cleanup(func() { _ = os.Unsetenv("RUNTIME_RESEARCHER_COUNT") })

	cfg := LoadConfig()
	if cfg.ResearcherCount != DefaultResearcherCount {
		t.Fatalf("researcher_count = %d, want fallback %d", cfg.ResearcherCount, DefaultResearcherCount)
	}
}
