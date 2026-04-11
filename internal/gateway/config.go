// Package gateway implements the host-side provider gateway for Mission 3.
//
// The gateway is the only component that holds real provider credentials and
// makes upstream LLM calls. Sandboxes authenticate to the gateway using
// per-sandbox credentials that the gateway issues and manages. Browser callers
// are denied at the proxy level.
//
// Key invariants:
//   - Provider credentials remain host-side (VAL-GATEWAY-004).
//   - Browser callers cannot use /provider/* as a raw inference bypass
//     (VAL-GATEWAY-002).
//   - Gateway denies unauthenticated or forged callers (VAL-GATEWAY-003).
//   - Upstream failures are sanitized before returning to callers
//     (VAL-GATEWAY-007).
//   - Stale sandbox credentials are invalidated after lifecycle changes
//     (VAL-GATEWAY-008).
package gateway

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"time"
)

// Config holds gateway service configuration resolved from environment variables.
type Config struct {
	// Port is the gateway listen port.
	Port string

	// SandboxTokenTTL is how long issued sandbox credentials remain valid.
	SandboxTokenTTL time.Duration
}

const (
	// DefaultGatewayPort is the default gateway service port.
	DefaultGatewayPort = "8084"

	// DefaultSandboxTokenTTL is the default TTL for sandbox credentials.
	DefaultSandboxTokenTTL = 1 * time.Hour
)

// LoadConfig resolves gateway configuration from environment variables.
func LoadConfig() Config {
	port := envOr("GATEWAY_PORT", DefaultGatewayPort)

	ttl := DefaultSandboxTokenTTL
	if v := os.Getenv("GATEWAY_SANDBOX_TOKEN_TTL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			ttl = d
		}
	}

	return Config{
		Port:           port,
		SandboxTokenTTL: ttl,
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// SandboxIdentity represents a registered sandbox caller with its
// authentication credential.
type SandboxIdentity struct {
	// SandboxID is the unique sandbox identifier.
	SandboxID string

	// TokenHash is the SHA-256 hash of the issued credential. The raw
	// credential is only returned once at issuance time and is never stored.
	TokenHash string

	// IssuedAt is when the credential was issued.
	IssuedAt time.Time

	// ExpiresAt is when the credential expires.
	ExpiresAt time.Time

	// Active indicates whether the credential is currently valid.
	// Revoked or replaced credentials have Active=false.
	Active bool
}

// IdentityRegistry manages sandbox identities and their credentials.
// It supports issuance, validation, revocation, and invalidation of
// stale credentials (VAL-GATEWAY-008).
type IdentityRegistry struct {
	identities map[string]*SandboxIdentity // sandbox_id → identity
	tokenTTL   time.Duration
}

// NewIdentityRegistry creates a new identity registry with the given
// credential TTL.
func NewIdentityRegistry(tokenTTL time.Duration) *IdentityRegistry {
	return &IdentityRegistry{
		identities: make(map[string]*SandboxIdentity),
		tokenTTL:   tokenTTL,
	}
}

// CredentialResult is returned when a new credential is issued.
// The RawToken is shown once and must be communicated to the sandbox
// out-of-band (e.g., via VM bootstrap material).
type CredentialResult struct {
	SandboxID string
	RawToken  string
	ExpiresAt time.Time
}

// IssueCredential creates a new credential for the given sandbox ID.
// If an existing credential exists, it is revoked and replaced.
// Returns the raw token (shown once) and an error if generation fails.
func (r *IdentityRegistry) IssueCredential(sandboxID string) (*CredentialResult, error) {
	token, err := generateSecureToken(32)
	if err != nil {
		return nil, fmt.Errorf("generate token: %w", err)
	}

	now := time.Now()
	hash := sha256.Sum256([]byte(token))

	// Revoke any existing credential for this sandbox.
	if existing, ok := r.identities[sandboxID]; ok {
		existing.Active = false
	}

	identity := &SandboxIdentity{
		SandboxID: sandboxID,
		TokenHash: hex.EncodeToString(hash[:]),
		IssuedAt:  now,
		ExpiresAt: now.Add(r.tokenTTL),
		Active:    true,
	}
	r.identities[sandboxID] = identity

	return &CredentialResult{
		SandboxID: sandboxID,
		RawToken:  sandboxID + ":" + token,
		ExpiresAt: identity.ExpiresAt,
	}, nil
}

// ValidateCredential checks whether a sandbox credential is valid.
// Returns the sandbox ID if valid, or an error explaining why not.
func (r *IdentityRegistry) ValidateCredential(rawToken string) (string, error) {
	sandboxID, token, ok := splitCredential(rawToken)
	if !ok {
		return "", fmt.Errorf("invalid credential format")
	}

	identity, ok := r.identities[sandboxID]
	if !ok {
		return "", fmt.Errorf("unknown sandbox identity")
	}

	if !identity.Active {
		return "", fmt.Errorf("credential revoked")
	}

	if time.Now().After(identity.ExpiresAt) {
		return "", fmt.Errorf("credential expired")
	}

	// Verify the token hash.
	hash := sha256.Sum256([]byte(token))
	if hex.EncodeToString(hash[:]) != identity.TokenHash {
		return "", fmt.Errorf("invalid credential")
	}

	return sandboxID, nil
}

// RevokeCredential revokes the credential for the given sandbox ID.
// After revocation, the old credential no longer authorizes provider
// requests (VAL-GATEWAY-008).
func (r *IdentityRegistry) RevokeCredential(sandboxID string) {
	if identity, ok := r.identities[sandboxID]; ok {
		identity.Active = false
	}
}

// RotateCredential revokes the existing credential and issues a new one.
// This is used when sandbox credentials are rotated for security or
// after lifecycle changes.
func (r *IdentityRegistry) RotateCredential(sandboxID string) (*CredentialResult, error) {
	r.RevokeCredential(sandboxID)
	return r.IssueCredential(sandboxID)
}

// generateSecureToken generates a cryptographically secure random token
// of the given byte length, hex-encoded.
func generateSecureToken(byteLen int) (string, error) {
	b := make([]byte, byteLen)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// splitCredential splits a "sandboxID:token" credential into its parts.
func splitCredential(raw string) (sandboxID, token string, ok bool) {
	for i := 0; i < len(raw); i++ {
		if raw[i] == ':' {
			return raw[:i], raw[i+1:], true
		}
	}
	return "", "", false
}
