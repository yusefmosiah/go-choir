package auth

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestStore creates a Store backed by a temporary SQLite database.
// The database is cleaned up automatically when the test finishes.
func TestStore(t *testing.T) *Store {
	t.Helper()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test-auth.db")

	store, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("TestStore: open store: %v", err)
	}

	t.Cleanup(func() {
		_ = store.Close()
	})

	return store
}

// TestConfig returns a Config suitable for local/unit testing.
// It uses a temporary directory for the DB path and sets the RP ID to
// "localhost" with localhost origins. CookieSecure is false.
//
// The Config's DBPath points to a temp directory that is cleaned up when the
// test finishes. The caller should call cfg.EnsureDirs() before using the
// config if path creation is needed.
func TestConfig(t *testing.T) *Config {
	t.Helper()

	dir := t.TempDir()

	cfg := &Config{
		Port:              "0", // OS picks a free port
		DBPath:            filepath.Join(dir, "auth.db"),
		RPID:              "localhost",
		RPOrigins:         []string{"http://localhost:4173"},
		JWTPrivateKeyPath: filepath.Join(dir, "test-jwt-ed25519"),
		AccessTokenTTL:    5 * time.Minute,
		RefreshTokenTTL:   720 * time.Hour,
		CookieSecure:      false,
	}

	// Create a dummy private key file so that key-loading code can find it.
	// Real key generation is handled by init.sh for deployed/local workers;
	// the test key is just a placeholder for path validation.
	keyDir := filepath.Dir(cfg.JWTPrivateKeyPath)
	if err := os.MkdirAll(keyDir, 0o755); err != nil {
		t.Fatalf("TestConfig: create key dir: %v", err)
	}
	// Write a placeholder file; actual key material is loaded at runtime
	// by the auth service, not by the config package.
	if err := os.WriteFile(cfg.JWTPrivateKeyPath, []byte("test-key-placeholder"), 0o600); err != nil {
		t.Fatalf("TestConfig: write test key: %v", err)
	}

	return cfg
}
