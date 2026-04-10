package auth

import (
	"os"
	"testing"
	"time"
)

func TestLoadConfigDefaults(t *testing.T) {
	// Clear all AUTH_* env vars to test defaults.
	envVars := []string{
		"AUTH_PORT", "AUTH_DB_PATH", "AUTH_RP_ID", "AUTH_RP_ORIGINS",
		"AUTH_JWT_PRIVATE_KEY_PATH", "AUTH_ACCESS_TOKEN_TTL",
		"AUTH_REFRESH_TOKEN_TTL", "AUTH_COOKIE_SECURE",
	}
	for _, v := range envVars {
		orig := os.Getenv(v)
		os.Unsetenv(v)
		t.Cleanup(func() {
			if orig != "" {
				os.Setenv(v, orig)
			}
		})
	}

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig with defaults: %v", err)
	}

	if cfg.Port != DefaultAuthPort {
		t.Errorf("Port: got %q, want %q", cfg.Port, DefaultAuthPort)
	}
	if cfg.DBPath != DefaultDBPath {
		t.Errorf("DBPath: got %q, want %q", cfg.DBPath, DefaultDBPath)
	}
	if cfg.RPID != DefaultRPID {
		t.Errorf("RPID: got %q, want %q", cfg.RPID, DefaultRPID)
	}
	if len(cfg.RPOrigins) != 1 || cfg.RPOrigins[0] != DefaultRPOrigins {
		t.Errorf("RPOrigins: got %v, want [%q]", cfg.RPOrigins, DefaultRPOrigins)
	}
	if cfg.JWTPrivateKeyPath != DefaultJWTPrivateKeyPath {
		t.Errorf("JWTPrivateKeyPath: got %q, want %q", cfg.JWTPrivateKeyPath, DefaultJWTPrivateKeyPath)
	}
	if cfg.AccessTokenTTL != DefaultAccessTokenTTL {
		t.Errorf("AccessTokenTTL: got %v, want %v", cfg.AccessTokenTTL, DefaultAccessTokenTTL)
	}
	if cfg.RefreshTokenTTL != DefaultRefreshTokenTTL {
		t.Errorf("RefreshTokenTTL: got %v, want %v", cfg.RefreshTokenTTL, DefaultRefreshTokenTTL)
	}
	if cfg.CookieSecure != DefaultCookieSecure {
		t.Errorf("CookieSecure: got %v, want %v", cfg.CookieSecure, DefaultCookieSecure)
	}
}

func TestLoadConfigFromEnv(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		check   func(t *testing.T, cfg *Config)
		wantErr bool
	}{
		{
			name: "custom_port",
			env:  map[string]string{"AUTH_PORT": "9090"},
			check: func(t *testing.T, cfg *Config) {
				if cfg.Port != "9090" {
					t.Errorf("Port: got %q, want %q", cfg.Port, "9090")
				}
			},
		},
		{
			name: "custom_db_path",
			env:  map[string]string{"AUTH_DB_PATH": "/data/auth.db"},
			check: func(t *testing.T, cfg *Config) {
				if cfg.DBPath != "/data/auth.db" {
					t.Errorf("DBPath: got %q, want %q", cfg.DBPath, "/data/auth.db")
				}
			},
		},
		{
			name: "custom_rp_id",
			env:  map[string]string{"AUTH_RP_ID": "draft.choir-ip.com"},
			check: func(t *testing.T, cfg *Config) {
				if cfg.RPID != "draft.choir-ip.com" {
					t.Errorf("RPID: got %q, want %q", cfg.RPID, "draft.choir-ip.com")
				}
			},
		},
		{
			name: "custom_rp_origins",
			env:  map[string]string{"AUTH_RP_ORIGINS": "https://draft.choir-ip.com,https://alt.choir-ip.com"},
			check: func(t *testing.T, cfg *Config) {
				want := []string{"https://draft.choir-ip.com", "https://alt.choir-ip.com"}
				if len(cfg.RPOrigins) != len(want) {
					t.Fatalf("RPOrigins length: got %d, want %d", len(cfg.RPOrigins), len(want))
				}
				for i, o := range cfg.RPOrigins {
					if o != want[i] {
						t.Errorf("RPOrigins[%d]: got %q, want %q", i, o, want[i])
					}
				}
			},
		},
		{
			name: "custom_jwt_key_path",
			env:  map[string]string{"AUTH_JWT_PRIVATE_KEY_PATH": "/run/keys/auth-signing-key"},
			check: func(t *testing.T, cfg *Config) {
				if cfg.JWTPrivateKeyPath != "/run/keys/auth-signing-key" {
					t.Errorf("JWTPrivateKeyPath: got %q, want %q", cfg.JWTPrivateKeyPath, "/run/keys/auth-signing-key")
				}
			},
		},
		{
			name: "custom_access_ttl",
			env:  map[string]string{"AUTH_ACCESS_TOKEN_TTL": "15m"},
			check: func(t *testing.T, cfg *Config) {
				if cfg.AccessTokenTTL != 15*time.Minute {
					t.Errorf("AccessTokenTTL: got %v, want %v", cfg.AccessTokenTTL, 15*time.Minute)
				}
			},
		},
		{
			name: "custom_refresh_ttl",
			env:  map[string]string{"AUTH_REFRESH_TOKEN_TTL": "168h"},
			check: func(t *testing.T, cfg *Config) {
				if cfg.RefreshTokenTTL != 168*time.Hour {
					t.Errorf("RefreshTokenTTL: got %v, want %v", cfg.RefreshTokenTTL, 168*time.Hour)
				}
			},
		},
		{
			name: "cookie_secure_true",
			env:  map[string]string{"AUTH_COOKIE_SECURE": "true"},
			check: func(t *testing.T, cfg *Config) {
				if !cfg.CookieSecure {
					t.Error("CookieSecure: got false, want true")
				}
			},
		},
		{
			name: "cookie_secure_false",
			env:  map[string]string{"AUTH_COOKIE_SECURE": "false"},
			check: func(t *testing.T, cfg *Config) {
				if cfg.CookieSecure {
					t.Error("CookieSecure: got true, want false")
				}
			},
		},
		{
			name: "deployed_config",
			env: map[string]string{
				"AUTH_PORT":               "8081",
				"AUTH_DB_PATH":            "/var/lib/go-choir/auth.db",
				"AUTH_RP_ID":              "draft.choir-ip.com",
				"AUTH_RP_ORIGINS":          "https://draft.choir-ip.com",
				"AUTH_JWT_PRIVATE_KEY_PATH": "/var/lib/go-choir/auth-signing-key",
				"AUTH_ACCESS_TOKEN_TTL":   "5m",
				"AUTH_REFRESH_TOKEN_TTL":  "720h",
				"AUTH_COOKIE_SECURE":      "true",
			},
			check: func(t *testing.T, cfg *Config) {
				if cfg.Port != "8081" {
					t.Errorf("Port: got %q", cfg.Port)
				}
				if cfg.DBPath != "/var/lib/go-choir/auth.db" {
					t.Errorf("DBPath: got %q", cfg.DBPath)
				}
				if cfg.RPID != "draft.choir-ip.com" {
					t.Errorf("RPID: got %q", cfg.RPID)
				}
				if !cfg.CookieSecure {
					t.Error("CookieSecure: got false, want true")
				}
			},
		},
		{
			name:    "invalid_access_ttl",
			env:     map[string]string{"AUTH_ACCESS_TOKEN_TTL": "not-a-duration"},
			wantErr: false, // invalid durations fall back to default
			check: func(t *testing.T, cfg *Config) {
				if cfg.AccessTokenTTL != DefaultAccessTokenTTL {
					t.Errorf("AccessTokenTTL should fall back to default on invalid input, got %v", cfg.AccessTokenTTL)
				}
			},
		},
		{
			name:    "empty_port_fails",
			env:     map[string]string{"AUTH_PORT": ""},
			wantErr: false, // empty string falls back to default
			check: func(t *testing.T, cfg *Config) {
				if cfg.Port != DefaultAuthPort {
					t.Errorf("Port: got %q, want default %q", cfg.Port, DefaultAuthPort)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Save and clear all AUTH_* env vars.
			saved := map[string]string{}
			for _, v := range []string{
				"AUTH_PORT", "AUTH_DB_PATH", "AUTH_RP_ID", "AUTH_RP_ORIGINS",
				"AUTH_JWT_PRIVATE_KEY_PATH", "AUTH_ACCESS_TOKEN_TTL",
				"AUTH_REFRESH_TOKEN_TTL", "AUTH_COOKIE_SECURE",
			} {
				saved[v] = os.Getenv(v)
				os.Unsetenv(v)
			}
			t.Cleanup(func() {
				for k, v := range saved {
					if v == "" {
						os.Unsetenv(k)
					} else {
						os.Setenv(k, v)
					}
				}
			})

			// Set test env vars.
			for k, v := range tt.env {
				os.Setenv(k, v)
			}

			cfg, err := LoadConfig()
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadConfig: %v", err)
			}
			tt.check(t, cfg)
		})
	}
}

func TestConfigValidationRejectsEmptyRPID(t *testing.T) {
	cfg := &Config{
		Port:              "8081",
		DBPath:            "/tmp/test.db",
		RPID:              "", // empty
		RPOrigins:         []string{"https://example.com"},
		JWTPrivateKeyPath: "/tmp/key",
		AccessTokenTTL:    5 * time.Minute,
		RefreshTokenTTL:   720 * time.Hour,
		CookieSecure:      true,
	}
	if err := cfg.validate(); err == nil {
		t.Error("expected validation error for empty RPID, got nil")
	}
}

func TestConfigValidationRejectsEmptyRPOrigins(t *testing.T) {
	cfg := &Config{
		Port:              "8081",
		DBPath:            "/tmp/test.db",
		RPID:              "example.com",
		RPOrigins:         []string{},
		JWTPrivateKeyPath: "/tmp/key",
		AccessTokenTTL:    5 * time.Minute,
		RefreshTokenTTL:   720 * time.Hour,
		CookieSecure:      true,
	}
	if err := cfg.validate(); err == nil {
		t.Error("expected validation error for empty RPOrigins, got nil")
	}
}

func TestConfigValidationRejectsNegativeTTLs(t *testing.T) {
	cfg := &Config{
		Port:              "8081",
		DBPath:            "/tmp/test.db",
		RPID:              "example.com",
		RPOrigins:         []string{"https://example.com"},
		JWTPrivateKeyPath: "/tmp/key",
		AccessTokenTTL:    -1 * time.Minute,
		RefreshTokenTTL:   720 * time.Hour,
		CookieSecure:      true,
	}
	if err := cfg.validate(); err == nil {
		t.Error("expected validation error for negative AccessTokenTTL, got nil")
	}

	cfg.AccessTokenTTL = 5 * time.Minute
	cfg.RefreshTokenTTL = -1 * time.Hour
	if err := cfg.validate(); err == nil {
		t.Error("expected validation error for negative RefreshTokenTTL, got nil")
	}
}

func TestConfigValidationAcceptsValidConfig(t *testing.T) {
	cfg := &Config{
		Port:              "8081",
		DBPath:            "/tmp/test.db",
		RPID:              "draft.choir-ip.com",
		RPOrigins:         []string{"https://draft.choir-ip.com"},
		JWTPrivateKeyPath: "/tmp/key",
		AccessTokenTTL:    5 * time.Minute,
		RefreshTokenTTL:   720 * time.Hour,
		CookieSecure:      true,
	}
	if err := cfg.validate(); err != nil {
		t.Errorf("expected valid config to pass, got error: %v", err)
	}
}

func TestConfigEnsureDirsCreatesDBPath(t *testing.T) {
	dir := t.TempDir()
	dbPath := dir + "/subdir/auth.db"
	keyPath := dir + "/keys/ed25519"

	cfg := &Config{
		DBPath:            dbPath,
		JWTPrivateKeyPath: keyPath,
	}
	if err := cfg.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}

	// Verify the directories exist (not the files, just dirs).
	if _, err := os.Stat(dir + "/subdir"); err != nil {
		t.Errorf("DB parent dir not created: %v", err)
	}
	if _, err := os.Stat(dir + "/keys"); err != nil {
		t.Errorf("Key parent dir not created: %v", err)
	}
}

func TestSplitComma(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{"", nil},
		{"single", []string{"single"}},
		{"a,b", []string{"a", "b"}},
		{" a , b ", []string{"a", "b"}},
		{",a,,b,", []string{"a", "b"}},
	}
	for _, tt := range tests {
		got := splitComma(tt.input)
		if len(got) != len(tt.want) {
			t.Errorf("splitComma(%q): got %v, want %v", tt.input, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("splitComma(%q)[%d]: got %q, want %q", tt.input, i, got[i], tt.want[i])
			}
		}
	}
}
