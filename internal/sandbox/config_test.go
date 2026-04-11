package sandbox

import (
	"os"
	"testing"
)

func TestLoadConfigDefaults(t *testing.T) {
	os.Unsetenv("SANDBOX_PORT")
	os.Unsetenv("SANDBOX_ID")

	cfg := LoadConfig()

	if cfg.Port != "8085" {
		t.Errorf("expected default port 8085, got %q", cfg.Port)
	}
	if cfg.SandboxID != "sandbox-dev" {
		t.Errorf("expected default sandbox_id sandbox-dev, got %q", cfg.SandboxID)
	}
}

func TestLoadConfigFromEnv(t *testing.T) {
	os.Setenv("SANDBOX_PORT", "9090")
	os.Setenv("SANDBOX_ID", "custom-sandbox-42")
	defer os.Unsetenv("SANDBOX_PORT")
	defer os.Unsetenv("SANDBOX_ID")

	cfg := LoadConfig()

	if cfg.Port != "9090" {
		t.Errorf("expected port 9090, got %q", cfg.Port)
	}
	if cfg.SandboxID != "custom-sandbox-42" {
		t.Errorf("expected sandbox_id custom-sandbox-42, got %q", cfg.SandboxID)
	}
}
