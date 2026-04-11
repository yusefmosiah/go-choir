package sandbox

import (
	"os"
	"testing"
)

func TestLoadConfigDefaults(t *testing.T) {
	_ = os.Unsetenv("SANDBOX_PORT")
	_ = os.Unsetenv("SANDBOX_ID")
	_ = os.Unsetenv("RUNTIME_STORE_PATH")

	cfg := LoadConfig()

	if cfg.Port != "8085" {
		t.Errorf("expected default port 8085, got %q", cfg.Port)
	}
	if cfg.SandboxID != "sandbox-dev" {
		t.Errorf("expected default sandbox_id sandbox-dev, got %q", cfg.SandboxID)
	}
	if cfg.StorePath != "" {
		t.Errorf("expected empty default store_path, got %q", cfg.StorePath)
	}
}

func TestLoadConfigFromEnv(t *testing.T) {
	_ = os.Setenv("SANDBOX_PORT", "9090")
	_ = os.Setenv("SANDBOX_ID", "custom-sandbox-42")
	defer func() { _ = os.Unsetenv("SANDBOX_PORT") }()
	defer func() { _ = os.Unsetenv("SANDBOX_ID") }()

	cfg := LoadConfig()

	if cfg.Port != "9090" {
		t.Errorf("expected port 9090, got %q", cfg.Port)
	}
	if cfg.SandboxID != "custom-sandbox-42" {
		t.Errorf("expected sandbox_id custom-sandbox-42, got %q", cfg.SandboxID)
	}
}
