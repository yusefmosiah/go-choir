// Package auth provides the Mission 2 auth foundation: configuration loading,
// SQLite-backed persistence for users/credentials/challenge-state/refresh-session
// records, and test helpers used by later auth features.
//
// No local auth bypass is provided. All config is loaded from AUTH_* environment
// variables at runtime; secrets are never hardcoded.
package auth

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config holds all runtime configuration for the auth service, resolved from
// AUTH_* environment variables. It does not contain any secrets—only paths
// and flags that point to where secrets live at runtime.
type Config struct {
	// Port is the TCP port the auth service listens on.
	Port string

	// DBPath is the filesystem path to the SQLite database file.
	DBPath string

	// RPID is the WebAuthn relying-party ID (e.g. "draft.choir-ip.com").
	RPID string

	// RPOrigins is the list of allowed WebAuthn origins
	// (e.g. ["https://draft.choir-ip.com"]).
	RPOrigins []string

	// JWTPrivateKeyPath is the path to the Ed25519 private key used to sign
	// access JWTs.
	JWTPrivateKeyPath string

	// AccessTokenTTL is the lifetime of short-lived access JWTs.
	AccessTokenTTL time.Duration

	// RefreshTokenTTL is the lifetime of refresh tokens / session records.
	RefreshTokenTTL time.Duration

	// CookieSecure controls whether auth cookies set the Secure flag.
	// Should be true on deployed HTTPS origins, false only for localhost.
	CookieSecure bool
}

const (
	// defaultLocalDir is the base directory for local worker defaults when
	// explicit path env vars are omitted.
	defaultLocalDir = "/tmp/go-choir-m2"

	// DefaultDBPath is the default SQLite database path.
	DefaultDBPath = defaultLocalDir + "/auth/auth.db"

	// DefaultJWTPrivateKeyPath is the default Ed25519 private key path.
	DefaultJWTPrivateKeyPath = defaultLocalDir + "/auth-signing-key"

	// DefaultRPID is the default WebAuthn relying-party ID.
	DefaultRPID = "localhost"

	// DefaultRPOrigins is the default comma-separated WebAuthn origins.
	DefaultRPOrigins = "http://localhost:4173"

	// DefaultAccessTokenTTL is the default short-lived access token TTL.
	DefaultAccessTokenTTL = 5 * time.Minute

	// DefaultRefreshTokenTTL is the default refresh-state TTL.
	DefaultRefreshTokenTTL = 720 * time.Hour

	// DefaultCookieSecure is the default Secure cookie flag.
	DefaultCookieSecure = false

	// DefaultAuthPort is the default auth service port.
	DefaultAuthPort = "8081"
)

// LoadConfig resolves a Config from AUTH_* environment variables.
// When explicit path env vars are omitted, local worker defaults resolve
// under /tmp/go-choir-m2.
//
// No secrets are hardcoded; signing keys and the database path are read from
// the environment or defaulted to writable local paths.
func LoadConfig() (*Config, error) {
	cfg := &Config{
		Port:              envOr("AUTH_PORT", DefaultAuthPort),
		DBPath:            envOr("AUTH_DB_PATH", DefaultDBPath),
		RPID:              envOr("AUTH_RP_ID", DefaultRPID),
		JWTPrivateKeyPath:  envOr("AUTH_JWT_PRIVATE_KEY_PATH", DefaultJWTPrivateKeyPath),
		AccessTokenTTL:    envDuration("AUTH_ACCESS_TOKEN_TTL", DefaultAccessTokenTTL),
		RefreshTokenTTL:   envDuration("AUTH_REFRESH_TOKEN_TTL", DefaultRefreshTokenTTL),
		CookieSecure:      envBool("AUTH_COOKIE_SECURE", DefaultCookieSecure),
	}

	originsStr := envOr("AUTH_RP_ORIGINS", DefaultRPOrigins)
	cfg.RPOrigins = splitComma(originsStr)

	if err := cfg.validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

// validate checks that the config is consistent and usable.
func (c *Config) validate() error {
	if c.Port == "" {
		return fmt.Errorf("auth config: AUTH_PORT must not be empty")
	}
	if c.DBPath == "" {
		return fmt.Errorf("auth config: AUTH_DB_PATH must not be empty")
	}
	if c.RPID == "" {
		return fmt.Errorf("auth config: AUTH_RP_ID must not be empty")
	}
	if len(c.RPOrigins) == 0 {
		return fmt.Errorf("auth config: AUTH_RP_ORIGINS must not be empty")
	}
	if c.JWTPrivateKeyPath == "" {
		return fmt.Errorf("auth config: AUTH_JWT_PRIVATE_KEY_PATH must not be empty")
	}
	if c.AccessTokenTTL <= 0 {
		return fmt.Errorf("auth config: AUTH_ACCESS_TOKEN_TTL must be positive")
	}
	if c.RefreshTokenTTL <= 0 {
		return fmt.Errorf("auth config: AUTH_REFRESH_TOKEN_TTL must be positive")
	}
	return nil
}

// EnsureDirs creates the parent directories for the DB and JWT key paths
// so that SQLite and key loading can succeed.
func (c *Config) EnsureDirs() error {
	if err := os.MkdirAll(filepath.Dir(c.DBPath), 0o755); err != nil {
		return fmt.Errorf("auth config: cannot create DB directory %s: %w", filepath.Dir(c.DBPath), err)
	}
	if err := os.MkdirAll(filepath.Dir(c.JWTPrivateKeyPath), 0o755); err != nil {
		return fmt.Errorf("auth config: cannot create JWT key directory %s: %w", filepath.Dir(c.JWTPrivateKeyPath), err)
	}
	return nil
}

// envOr returns the value of the environment variable named key, or fallback
// if the variable is unset or empty.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// envBool returns the boolean value of the environment variable named key,
// or fallback if the variable is unset or empty.
func envBool(key string, fallback bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return b
}

// envDuration returns the duration value of the environment variable named key,
// or fallback if the variable is unset, empty, or cannot be parsed.
func envDuration(key string, fallback time.Duration) time.Duration {
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

// splitComma splits a comma-separated string into trimmed, non-empty parts.
func splitComma(s string) []string {
	var result []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}
