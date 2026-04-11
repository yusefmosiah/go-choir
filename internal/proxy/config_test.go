package proxy

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigDefaults(t *testing.T) {
	// Clear all PROXY_* env vars to test defaults.
	os.Unsetenv("PROXY_PORT")
	os.Unsetenv("PROXY_SANDBOX_URL")
	os.Unsetenv("PROXY_AUTH_PUBLIC_KEY_PATH")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	if cfg.Port != "8082" {
		t.Errorf("Port: got %q, want %q", cfg.Port, "8082")
	}
	if cfg.SandboxURL != "http://127.0.0.1:8085" {
		t.Errorf("SandboxURL: got %q, want %q", cfg.SandboxURL, "http://127.0.0.1:8085")
	}
	if cfg.AuthPublicKeyPath != "/tmp/go-choir-m2/auth-signing-key.pub" {
		t.Errorf("AuthPublicKeyPath: got %q, want %q", cfg.AuthPublicKeyPath, "/tmp/go-choir-m2/auth-signing-key.pub")
	}
}

func TestLoadConfigFromEnv(t *testing.T) {
	os.Setenv("PROXY_PORT", "9999")
	os.Setenv("PROXY_SANDBOX_URL", "http://example.com:8085")
	os.Setenv("PROXY_AUTH_PUBLIC_KEY_PATH", "/tmp/test-pub.key")
	defer func() {
		os.Unsetenv("PROXY_PORT")
		os.Unsetenv("PROXY_SANDBOX_URL")
		os.Unsetenv("PROXY_AUTH_PUBLIC_KEY_PATH")
	}()

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	if cfg.Port != "9999" {
		t.Errorf("Port: got %q, want %q", cfg.Port, "9999")
	}
	if cfg.SandboxURL != "http://example.com:8085" {
		t.Errorf("SandboxURL: got %q, want %q", cfg.SandboxURL, "http://example.com:8085")
	}
	if cfg.AuthPublicKeyPath != "/tmp/test-pub.key" {
		t.Errorf("AuthPublicKeyPath: got %q, want %q", cfg.AuthPublicKeyPath, "/tmp/test-pub.key")
	}
}

func TestLoadConfigRejectsEmptyPort(t *testing.T) {
	os.Setenv("PROXY_PORT", "")
	defer os.Unsetenv("PROXY_PORT")

	// When PROXY_PORT is empty, the default should be used, not rejected.
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig with empty env should use default: %v", err)
	}
	if cfg.Port != "8082" {
		t.Errorf("Port: got %q, want default %q", cfg.Port, "8082")
	}
}

func TestLoadPublicKey(t *testing.T) {
	// Generate a test key pair and write the public key to a temp file.
	dir := t.TempDir()
	pubKeyPath := filepath.Join(dir, "test-pub.key")

	// Use the same key that init.sh generates for the test environment.
	if _, err := os.Stat("/tmp/go-choir-m2/auth-signing-key.pub"); err == nil {
		// The init.sh key exists — copy it.
		data, err := os.ReadFile("/tmp/go-choir-m2/auth-signing-key.pub")
		if err != nil {
			t.Fatalf("read init.sh public key: %v", err)
		}
		if err := os.WriteFile(pubKeyPath, data, 0o644); err != nil {
			t.Fatalf("write test public key: %v", err)
		}
	} else {
		// No init.sh key — skip this test gracefully.
		t.Skip("No test key available from init.sh; skipping LoadPublicKey test")
	}

	pubKey, err := LoadPublicKey(pubKeyPath)
	if err != nil {
		t.Fatalf("LoadPublicKey: %v", err)
	}

	if len(pubKey) != ed25519PublicKeySize {
		t.Errorf("public key size: got %d, want %d", len(pubKey), ed25519PublicKeySize)
	}
}

const ed25519PublicKeySize = 32
