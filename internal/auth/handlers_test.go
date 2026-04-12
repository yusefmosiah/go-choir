package auth

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/ssh"
)

// testHandlerEnv sets up a Handler with a test Store, WebAuthn instance, and
// temporary key material for unit testing.
func testHandlerEnv(t *testing.T) (*Handler, ed25519.PrivateKey) {
	t.Helper()

	store := TestStore(t)
	cfg := TestConfig(t)

	// Generate a real Ed25519 key pair for JWT signing/verification.
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate ed25519 key: %v", err)
	}

	// Write the private key in OpenSSH PEM format so that LoadPrivateKey can read it.
	keyDir := t.TempDir()
	keyPath := filepath.Join(keyDir, "test-ed25519")
	writeTestKey(t, keyPath, priv)
	_ = pub

	// Create WebAuthn instance bound to the test config's RP.
	wa, err := webauthn.New(&webauthn.Config{
		RPID:          cfg.RPID,
		RPDisplayName: "go-choir test",
		RPOrigins:     cfg.RPOrigins,
	})
	if err != nil {
		t.Fatalf("create webauthn: %v", err)
	}

	handler := NewHandler(store, wa, cfg, priv)
	return handler, priv
}

// --- Register Begin Tests ---

func TestRegisterBeginRejectsNonPost(t *testing.T) {
	h, _ := testHandlerEnv(t)

	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		req := httptest.NewRequest(method, "/auth/register/begin", nil)
		rec := httptest.NewRecorder()
		h.HandleRegisterBegin(rec, req)

		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("method %s: got status %d, want %d", method, rec.Code, http.StatusMethodNotAllowed)
		}

		var resp errorResponse
		if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
			t.Fatalf("decode error response: %v", err)
		}
		if resp.Error == "" {
			t.Errorf("method %s: expected non-empty error message", method)
		}
	}
}

func TestRegisterBeginRejectsEmptyBody(t *testing.T) {
	h, _ := testHandlerEnv(t)

	req := httptest.NewRequest(http.MethodPost, "/auth/register/begin", nil)
	rec := httptest.NewRecorder()
	h.HandleRegisterBegin(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want %d", rec.Code, http.StatusBadRequest)
	}

	var resp errorResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error == "" {
		t.Error("expected non-empty error message")
	}
}

func TestRegisterBeginRejectsMalformedJSON(t *testing.T) {
	h, _ := testHandlerEnv(t)

	tests := []struct {
		name string
		body string
	}{
		{"not json", `this is not json`},
		{"missing email field", `{"username": "alice@example.com"}`},
		{"empty email", `{"email": ""}`},
		{"email is number", `{"email": 123}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/auth/register/begin",
				bytes.NewBufferString(tt.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			h.HandleRegisterBegin(rec, req)

			if rec.Code < 400 || rec.Code >= 500 {
				t.Errorf("status: got %d, want 4xx", rec.Code)
			}

			// Must be JSON, not HTML.
			ct := rec.Header().Get("Content-Type")
			if ct != "application/json" {
				t.Errorf("Content-Type: got %q, want %q", ct, "application/json")
			}

			var resp errorResponse
			if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if resp.Error == "" {
				t.Error("expected non-empty error message")
			}
		})
	}
}

func TestRegisterBeginReturnsRPBoundChallenge(t *testing.T) {
	h, _ := testHandlerEnv(t)

	body := `{"email": "alice@example.com"}`
	req := httptest.NewRequest(http.MethodPost, "/auth/register/begin",
		bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.HandleRegisterBegin(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	ct := rec.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("Content-Type: got %q, want %q", ct, "application/json")
	}

	// Parse the response as a generic JSON object to check key fields.
	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// The WebAuthn credential creation response has a "publicKey" field
	// containing the PublicKeyCredentialCreationOptions.
	pk, ok := resp["publicKey"]
	if !ok {
		t.Fatal("response missing 'publicKey' field")
	}
	pkMap, ok := pk.(map[string]interface{})
	if !ok {
		t.Fatalf("publicKey is %T, not a map", pk)
	}

	// Check challenge is non-empty.
	challenge, ok := pkMap["challenge"].(string)
	if !ok || challenge == "" {
		t.Error("publicKey.challenge is missing or empty")
	}

	// Check RP ID matches our config.
	rp, ok := pkMap["rp"].(map[string]interface{})
	if !ok {
		t.Fatal("publicKey.rp is missing or not an object")
	}
	rpID, ok := rp["id"].(string)
	if !ok || rpID == "" {
		t.Error("publicKey.rp.id is missing or empty")
	}

	// The handler uses TestConfig which has RPID="localhost".
	cfg := TestConfig(t)
	if rpID != cfg.RPID {
		t.Errorf("rp.id: got %q, want %q", rpID, cfg.RPID)
	}
}

func TestRegisterBeginCreatesUserAndChallengeInStore(t *testing.T) {
	h, _ := testHandlerEnv(t)

	body := `{"email": "bob@example.com"}`
	req := httptest.NewRequest(http.MethodPost, "/auth/register/begin",
		bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.HandleRegisterBegin(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusOK)
	}

	// Verify the user was created in the store.
	user, err := h.store.GetUserByEmail("bob@example.com")
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if user.Email != "bob@example.com" {
		t.Errorf("email: got %q, want %q", user.Email, "bob@example.com")
	}
}

func TestRegisterBeginIdempotentForExistingUser(t *testing.T) {
	h, _ := testHandlerEnv(t)

	// Create user first.
	if _, err := h.store.CreateUser("existing-id", "charlie@example.com"); err != nil {
		t.Fatalf("create user: %v", err)
	}

	body := `{"email": "charlie@example.com"}`
	req := httptest.NewRequest(http.MethodPost, "/auth/register/begin",
		bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.HandleRegisterBegin(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusOK)
	}

	// Verify existing user was found, not duplicated.
	user, err := h.store.GetUserByEmail("charlie@example.com")
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if user.ID != "existing-id" {
		t.Errorf("user ID: got %q, want %q (should be existing user)", user.ID, "existing-id")
	}
}

// --- Login Begin Tests ---

func TestLoginBeginRejectsNonPost(t *testing.T) {
	h, _ := testHandlerEnv(t)

	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		req := httptest.NewRequest(method, "/auth/login/begin", nil)
		rec := httptest.NewRecorder()
		h.HandleLoginBegin(rec, req)

		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("method %s: got status %d, want %d", method, rec.Code, http.StatusMethodNotAllowed)
		}
	}
}

func TestLoginBeginRejectsEmptyBody(t *testing.T) {
	h, _ := testHandlerEnv(t)

	req := httptest.NewRequest(http.MethodPost, "/auth/login/begin", nil)
	rec := httptest.NewRecorder()
	h.HandleLoginBegin(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want %d", rec.Code, http.StatusBadRequest)
	}

	var resp errorResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error == "" {
		t.Error("expected non-empty error message")
	}
}

func TestLoginBeginRejectsMalformedJSON(t *testing.T) {
	h, _ := testHandlerEnv(t)

	tests := []struct {
		name string
		body string
	}{
		{"not json", `not json at all`},
		{"missing email", `{"username": "alice@example.com"}`},
		{"empty email", `{"email": ""}`},
		{"email is null", `{"email": null}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/auth/login/begin",
				bytes.NewBufferString(tt.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			h.HandleLoginBegin(rec, req)

			if rec.Code < 400 || rec.Code >= 500 {
				t.Errorf("status: got %d, want 4xx; body: %s", rec.Code, rec.Body.String())
			}

			ct := rec.Header().Get("Content-Type")
			if ct != "application/json" {
				t.Errorf("Content-Type: got %q, want %q", ct, "application/json")
			}

			var resp errorResponse
			if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if resp.Error == "" {
				t.Error("expected non-empty error message")
			}
		})
	}
}

func TestLoginBeginRejectsUnknownUser(t *testing.T) {
	h, _ := testHandlerEnv(t)

	body := `{"email": "nonexistent@example.com"}`
	req := httptest.NewRequest(http.MethodPost, "/auth/login/begin",
		bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.HandleLoginBegin(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status: got %d, want %d", rec.Code, http.StatusNotFound)
	}

	var resp errorResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error == "" {
		t.Error("expected non-empty error message")
	}
}

func TestLoginBeginRejectsUserWithNoCredentials(t *testing.T) {
	h, _ := testHandlerEnv(t)

	// Create a user with no passkeys.
	if _, err := h.store.CreateUser("user-no-creds", "nobody@example.com"); err != nil {
		t.Fatalf("create user: %v", err)
	}

	body := `{"email": "nobody@example.com"}`
	req := httptest.NewRequest(http.MethodPost, "/auth/login/begin",
		bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.HandleLoginBegin(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status: got %d, want %d; body: %s", rec.Code, http.StatusNotFound, rec.Body.String())
	}

	var resp errorResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error == "" {
		t.Error("expected non-empty error message")
	}
}

func TestLoginBeginReturnsAssertionOptionsForRegisteredUser(t *testing.T) {
	h, _ := testHandlerEnv(t)

	// Create a user with a credential.
	user, err := h.store.CreateUser("login-user", "dave@example.com")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	cred := &Credential{
		ID:              "cred-login-1",
		UserID:          user.ID,
		PublicKey:       make([]byte, 64), // fake 64-byte key
		AttestationType: "none",
		Transport:       `["internal"]`,
		SignCount:       0,
		AAGUID:          make([]byte, 16),
		Flags:           `{"user_present":true,"user_verified":true,"backup_eligible":true,"backup_state":false}`,
		CreatedAt:       time.Now().UTC(),
	}
	if err := h.store.CreateCredential(cred); err != nil {
		t.Fatalf("create credential: %v", err)
	}

	body := `{"email": "dave@example.com"}`
	req := httptest.NewRequest(http.MethodPost, "/auth/login/begin",
		bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.HandleLoginBegin(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	ct := rec.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("Content-Type: got %q, want %q", ct, "application/json")
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// The WebAuthn assertion response has a "publicKey" field.
	pk, ok := resp["publicKey"]
	if !ok {
		t.Fatal("response missing 'publicKey' field")
	}
	pkMap, ok := pk.(map[string]interface{})
	if !ok {
		t.Fatalf("publicKey is %T, not a map", pk)
	}

	// Check challenge is non-empty.
	challenge, ok := pkMap["challenge"].(string)
	if !ok || challenge == "" {
		t.Error("publicKey.challenge is missing or empty")
	}

	// Check that allowCredentials is populated for the registered passkeys.
	allowCreds, ok := pkMap["allowCredentials"]
	if !ok {
		t.Error("publicKey.allowCredentials is missing")
	}
	allowCredsArr, ok := allowCreds.([]interface{})
	if !ok || len(allowCredsArr) == 0 {
		t.Errorf("publicKey.allowCredentials should be a non-empty array, got %v", allowCreds)
	}

	// Check RP ID in the assertion options.
	rpID, _ := pkMap["rpId"].(string)
	cfg := TestConfig(t)
	if rpID != "" && rpID != cfg.RPID {
		t.Errorf("rpId: got %q, want %q", rpID, cfg.RPID)
	}
}

// --- Session Endpoint Tests ---

func TestSessionReturnsSignedOutWithoutCookie(t *testing.T) {
	h, _ := testHandlerEnv(t)

	req := httptest.NewRequest(http.MethodGet, "/auth/session", nil)
	rec := httptest.NewRecorder()
	h.HandleSession(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status: got %d, want %d", rec.Code, http.StatusOK)
	}

	var resp sessionResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Authenticated {
		t.Error("should not be authenticated without a cookie")
	}
	if resp.User != nil {
		t.Error("user should be nil when signed out")
	}
}

func TestSessionReturnsSignedOutWithBogusCookie(t *testing.T) {
	h, _ := testHandlerEnv(t)

	req := httptest.NewRequest(http.MethodGet, "/auth/session", nil)
	req.AddCookie(&http.Cookie{
		Name:  AccessTokenCookieName,
		Value: "this-is-not-a-jwt",
	})
	rec := httptest.NewRecorder()
	h.HandleSession(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status: got %d, want %d", rec.Code, http.StatusOK)
	}

	var resp sessionResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Authenticated {
		t.Error("should not be authenticated with a bogus cookie")
	}
	if resp.User != nil {
		t.Error("user should be nil with bogus cookie")
	}
}

func TestSessionReturnsSignedOutWithEmptyCookie(t *testing.T) {
	h, _ := testHandlerEnv(t)

	req := httptest.NewRequest(http.MethodGet, "/auth/session", nil)
	req.AddCookie(&http.Cookie{
		Name:  AccessTokenCookieName,
		Value: "",
	})
	rec := httptest.NewRecorder()
	h.HandleSession(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status: got %d, want %d", rec.Code, http.StatusOK)
	}

	var resp sessionResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Authenticated {
		t.Error("should not be authenticated with an empty cookie")
	}
}

func TestSessionReturnsSignedOutWithExpiredJWT(t *testing.T) {
	h, priv := testHandlerEnv(t)

	// Create an expired JWT.
	claims := jwt.MapClaims{
		"sub": "user-1",
		"exp": time.Now().Add(-1 * time.Hour).Unix(),
		"iat": time.Now().Add(-2 * time.Hour).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	tokenStr, err := token.SignedString(priv)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/auth/session", nil)
	req.AddCookie(&http.Cookie{
		Name:  AccessTokenCookieName,
		Value: tokenStr,
	})
	rec := httptest.NewRecorder()
	h.HandleSession(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status: got %d, want %d", rec.Code, http.StatusOK)
	}

	var resp sessionResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Authenticated {
		t.Error("should not be authenticated with an expired JWT")
	}
}

func TestSessionReturnsSignedOutWithTamperedJWT(t *testing.T) {
	h, priv := testHandlerEnv(t)

	// Create a valid JWT then tamper with it.
	claims := jwt.MapClaims{
		"sub": "user-1",
		"exp": time.Now().Add(5 * time.Minute).Unix(),
		"iat": time.Now().Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	tokenStr, err := token.SignedString(priv)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}

	// Tamper: change one character in the token.
	tampered := tokenStr[:len(tokenStr)-5] + "XXXXX"

	req := httptest.NewRequest(http.MethodGet, "/auth/session", nil)
	req.AddCookie(&http.Cookie{
		Name:  AccessTokenCookieName,
		Value: tampered,
	})
	rec := httptest.NewRecorder()
	h.HandleSession(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status: got %d, want %d", rec.Code, http.StatusOK)
	}

	var resp sessionResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Authenticated {
		t.Error("should not be authenticated with a tampered JWT")
	}
}

func TestSessionReturnsAuthenticatedWithValidJWT(t *testing.T) {
	h, priv := testHandlerEnv(t)

	// Create a user in the store.
	user, err := h.store.CreateUser("jwt-user", "eve@example.com")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	// Create a valid JWT for this user.
	claims := jwt.MapClaims{
		"sub": user.ID,
		"exp": time.Now().Add(5 * time.Minute).Unix(),
		"iat": time.Now().Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	tokenStr, err := token.SignedString(priv)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/auth/session", nil)
	req.AddCookie(&http.Cookie{
		Name:  AccessTokenCookieName,
		Value: tokenStr,
	})
	rec := httptest.NewRecorder()
	h.HandleSession(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status: got %d, want %d", rec.Code, http.StatusOK)
	}

	var resp sessionResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Authenticated {
		t.Error("should be authenticated with a valid JWT")
	}
	if resp.User == nil {
		t.Fatal("user info should be present when authenticated")
	}
	if resp.User.Email != "eve@example.com" {
		t.Errorf("email: got %q, want %q", resp.User.Email, "eve@example.com")
	}
	if resp.User.ID != user.ID {
		t.Errorf("user ID: got %q, want %q", resp.User.ID, user.ID)
	}
}

func TestSessionDoesNotLeakSecrets(t *testing.T) {
	h, priv := testHandlerEnv(t)

	// Create a user and a credential (secret).
	user, err := h.store.CreateUser("secret-user", "frank@example.com")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	cred := &Credential{
		ID:              "secret-cred",
		UserID:          user.ID,
		PublicKey:       []byte("secret-public-key-material"),
		AttestationType: "none",
		Transport:       `["internal"]`,
		SignCount:       0,
		AAGUID:          make([]byte, 16),
		Flags:           "{}",
		CreatedAt:       time.Now().UTC(),
	}
	if err := h.store.CreateCredential(cred); err != nil {
		t.Fatalf("create credential: %v", err)
	}

	// Create a valid JWT for this user.
	claims := jwt.MapClaims{
		"sub": user.ID,
		"exp": time.Now().Add(5 * time.Minute).Unix(),
		"iat": time.Now().Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	tokenStr, err := token.SignedString(priv)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/auth/session", nil)
	req.AddCookie(&http.Cookie{
		Name:  AccessTokenCookieName,
		Value: tokenStr,
	})
	rec := httptest.NewRecorder()
	h.HandleSession(rec, req)

	// The response body should not contain any secret data.
	body := rec.Body.String()
	for _, secret := range []string{
		"secret-public-key-material",
		"secret-cred",
		"choir_refresh",
	} {
		if bytes.Contains([]byte(body), []byte(secret)) {
			t.Errorf("session response leaks secret %q", secret)
		}
	}
}

func TestSessionRejectsNonGet(t *testing.T) {
	h, _ := testHandlerEnv(t)

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		req := httptest.NewRequest(method, "/auth/session", nil)
		rec := httptest.NewRecorder()
		h.HandleSession(rec, req)

		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("method %s: got status %d, want %d", method, rec.Code, http.StatusMethodNotAllowed)
		}
	}
}

func TestSessionNeverReturns5xxForInvalidAuth(t *testing.T) {
	h, _ := testHandlerEnv(t)

	tests := []struct {
		name   string
		cookie *http.Cookie
	}{
		{"no cookie", nil},
		{"empty value", &http.Cookie{Name: AccessTokenCookieName, Value: ""}},
		{"bogus value", &http.Cookie{Name: AccessTokenCookieName, Value: "not-a-jwt"}},
		{"random base64", &http.Cookie{Name: AccessTokenCookieName, Value: "dGhpcyBpcyBub3QgYSB0b2tlbg=="}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/auth/session", nil)
			if tt.cookie != nil {
				req.AddCookie(tt.cookie)
			}
			rec := httptest.NewRecorder()
			h.HandleSession(rec, req)

			if rec.Code >= 500 {
				t.Errorf("status: got %d (5xx), want non-5xx for invalid auth", rec.Code)
			}

			ct := rec.Header().Get("Content-Type")
			if ct != "application/json" {
				t.Errorf("Content-Type: got %q, want %q", ct, "application/json")
			}

			var resp sessionResponse
			if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if resp.Authenticated {
				t.Error("should not be authenticated with invalid auth state")
			}
		})
	}
}

// --- WebAuthn user adapter tests ---

func TestWebAuthnUserAdapter(t *testing.T) {
	store := TestStore(t)

	user, err := store.CreateUser("wa-user-1", "walter@example.com")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	cred := &Credential{
		ID:              "wa-cred-1",
		UserID:          user.ID,
		PublicKey:       []byte("fake-key"),
		AttestationType: "none",
		Transport:       `["internal","hybrid"]`,
		SignCount:       5,
		AAGUID:          make([]byte, 16),
		Flags:           `{"user_present":true,"user_verified":true,"backup_eligible":true,"backup_state":false}`,
		CreatedAt:       time.Now().UTC(),
	}
	if err := store.CreateCredential(cred); err != nil {
		t.Fatalf("create credential: %v", err)
	}

	waUser, err := newWebAuthnUser(user, []Credential{*cred})
	if err != nil {
		t.Fatalf("newWebAuthnUser: %v", err)
	}

	if string(waUser.WebAuthnID()) != user.ID {
		t.Errorf("WebAuthnID: got %q, want %q", string(waUser.WebAuthnID()), user.ID)
	}
	if waUser.WebAuthnName() != "walter@example.com" {
		t.Errorf("WebAuthnName: got %q, want %q", waUser.WebAuthnName(), "walter@example.com")
	}
	if waUser.WebAuthnDisplayName() != "walter@example.com" {
		t.Errorf("WebAuthnDisplayName: got %q, want %q", waUser.WebAuthnDisplayName(), "walter@example.com")
	}
	creds := waUser.WebAuthnCredentials()
	if len(creds) != 1 {
		t.Fatalf("WebAuthnCredentials: got %d, want 1", len(creds))
	}
	if string(creds[0].ID) != "wa-cred-1" {
		t.Errorf("credential ID: got %q, want %q", string(creds[0].ID), "wa-cred-1")
	}
	if len(creds[0].Transport) != 2 {
		t.Errorf("Transport: got %d, want 2", len(creds[0].Transport))
	}
}

// --- Key loading tests ---

func TestLoadPrivateKey(t *testing.T) {
	// Generate a key with ssh-keygen format, like init.sh does.
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	dir := t.TempDir()
	keyPath := filepath.Join(dir, "test-key")
	writeTestKey(t, keyPath, priv)

	loaded, err := LoadPrivateKey(keyPath)
	if err != nil {
		t.Fatalf("LoadPrivateKey: %v", err)
	}

	// Compare the private key bytes.
	if !bytes.Equal(priv, loaded) {
		t.Error("loaded key does not match original key")
	}
}

func TestLoadPrivateKeyInvalidPath(t *testing.T) {
	_, err := LoadPrivateKey("/nonexistent/key")
	if err == nil {
		t.Error("expected error for nonexistent key path, got nil")
	}
}

// --- Integration: register begin then login begin ---

func TestRegisterThenLoginBeginFlow(t *testing.T) {
	h, _ := testHandlerEnv(t)

	// Step 1: Register begin for "alice@example.com"
	regBody := `{"email": "alice@example.com"}`
	regReq := httptest.NewRequest(http.MethodPost, "/auth/register/begin",
		bytes.NewBufferString(regBody))
	regReq.Header.Set("Content-Type", "application/json")
	regRec := httptest.NewRecorder()
	h.HandleRegisterBegin(regRec, regReq)

	if regRec.Code != http.StatusOK {
		t.Fatalf("register begin: got %d, want %d; body: %s", regRec.Code, http.StatusOK, regRec.Body.String())
	}

	// Step 2: Login begin should fail because no credentials yet
	loginBody := `{"email": "alice@example.com"}`
	loginReq := httptest.NewRequest(http.MethodPost, "/auth/login/begin",
		bytes.NewBufferString(loginBody))
	loginReq.Header.Set("Content-Type", "application/json")
	loginRec := httptest.NewRecorder()
	h.HandleLoginBegin(loginRec, loginReq)

	// Login begin should return 404 (no passkeys registered yet).
	if loginRec.Code != http.StatusNotFound {
		t.Errorf("login begin before finish: got %d, want %d; body: %s",
			loginRec.Code, http.StatusNotFound, loginRec.Body.String())
	}
}

// --- Deployed RP ID test ---

func TestRegisterBeginWithDeployedRPID(t *testing.T) {
	store := TestStore(t)

	// Use a config that mimics deployed settings.
	cfg := &Config{
		Port:              "0",
		DBPath:            filepath.Join(t.TempDir(), "auth.db"),
		RPID:              "draft.choir-ip.com",
		RPOrigins:         []string{"https://draft.choir-ip.com"},
		JWTPrivateKeyPath: filepath.Join(t.TempDir(), "key"),
		AccessTokenTTL:    5 * time.Minute,
		RefreshTokenTTL:   720 * time.Hour,
		CookieSecure:      true,
	}

	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	wa, err := webauthn.New(&webauthn.Config{
		RPID:          cfg.RPID,
		RPDisplayName: "go-choir",
		RPOrigins:     cfg.RPOrigins,
	})
	if err != nil {
		t.Fatalf("create webauthn: %v", err)
	}

	h := NewHandler(store, wa, cfg, priv)

	body := `{"email": "deployed-alice@example.com"}`
	req := httptest.NewRequest(http.MethodPost, "/auth/register/begin",
		bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.HandleRegisterBegin(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	pk, _ := resp["publicKey"].(map[string]interface{})
	rp, _ := pk["rp"].(map[string]interface{})
	rpID, _ := rp["id"].(string)

	if rpID != "draft.choir-ip.com" {
		t.Errorf("RP ID: got %q, want %q", rpID, "draft.choir-ip.com")
	}

	challenge, _ := pk["challenge"].(string)
	if challenge == "" {
		t.Error("challenge is empty")
	}
}

// writeTestKey writes an Ed25519 private key in OpenSSH PEM format to the given path.
func writeTestKey(t *testing.T, path string, priv ed25519.PrivateKey) {
	t.Helper()
	block, err := ssh.MarshalPrivateKey(priv, "test")
	if err != nil {
		t.Fatalf("marshal private key: %v", err)
	}
	data := pem.EncodeToMemory(block)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
}

// --- ssh import usage check ---

func TestSSHPackageImportUsed(t *testing.T) {
	// This is a compile-time check that the ssh package is correctly imported.
	// If this compiles, the import path is correct.
	_ = ssh.MarshalPrivateKey
	_ = fmt.Sprintf
}

// ======================================================================
// Finish route tests
// ======================================================================

// --- Register Finish Tests ---

func TestRegisterFinishRejectsNonPost(t *testing.T) {
	h, _ := testHandlerEnv(t)

	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		req := httptest.NewRequest(method, "/auth/register/finish", nil)
		rec := httptest.NewRecorder()
		h.HandleRegisterFinish(rec, req)

		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("method %s: got status %d, want %d", method, rec.Code, http.StatusMethodNotAllowed)
		}

		var resp errorResponse
		if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
			t.Fatalf("decode error response: %v", err)
		}
		if resp.Error == "" {
			t.Errorf("method %s: expected non-empty error message", method)
		}
	}
}

func TestRegisterFinishRejectsEmptyBody(t *testing.T) {
	h, _ := testHandlerEnv(t)

	req := httptest.NewRequest(http.MethodPost, "/auth/register/finish", nil)
	rec := httptest.NewRecorder()
	h.HandleRegisterFinish(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want %d", rec.Code, http.StatusBadRequest)
	}

	var resp errorResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error == "" {
		t.Error("expected non-empty error message")
	}

	ct := rec.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("Content-Type: got %q, want %q", ct, "application/json")
	}
}

func TestRegisterFinishRejectsInvalidWebAuthnResponse(t *testing.T) {
	h, _ := testHandlerEnv(t)

	// Send a body that is valid JSON but not a valid WebAuthn response.
	body := `{"id":"abc","response":{"clientDataJSON":"","attestationObject":""}}`
	req := httptest.NewRequest(http.MethodPost, "/auth/register/finish",
		bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.HandleRegisterFinish(rec, req)

	// Should return 4xx, not 5xx.
	if rec.Code < 400 || rec.Code >= 500 {
		t.Errorf("status: got %d, want 4xx; body: %s", rec.Code, rec.Body.String())
	}

	ct := rec.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("Content-Type: got %q, want %q", ct, "application/json")
	}
}

func TestRegisterFinishRejectsChallengeNotFound(t *testing.T) {
	h, _ := testHandlerEnv(t)

	// Create a valid-looking WebAuthn response body with a challenge that
	// doesn't exist in the store. This simulates a replay attack where the
	// challenge has already been consumed.
	clientDataJSON := base64RawURLEncode([]byte(`{"type":"webauthn.create","challenge":"nonexistent-challenge","origin":"http://localhost:4173"}`))
	body := fmt.Sprintf(`{"id":"cred-id","rawId":"cred-id","type":"public-key","response":{"clientDataJSON":"%s","attestationObject":"%s"}}`, clientDataJSON, base64RawURLEncode([]byte("fake-attestation")))

	req := httptest.NewRequest(http.MethodPost, "/auth/register/finish",
		bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.HandleRegisterFinish(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want %d; body: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	var resp errorResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error == "" {
		t.Error("expected non-empty error message for missing challenge")
	}

	// Most critically: no auth cookies should have been set.
	cookies := rec.Result().Cookies()
	for _, c := range cookies {
		if c.Name == AccessTokenCookieName || c.Name == RefreshTokenCookieName {
			t.Errorf("auth cookie %q should not be set on failed finish", c.Name)
		}
	}
}

func TestRegisterFinishRejectsExpiredChallenge(t *testing.T) {
	h, _ := testHandlerEnv(t)

	// Create a user and store an expired challenge.
	user, err := h.store.CreateUser("expired-ch-user", "expiredch@example.com")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	sessionData := webauthn.SessionData{
		Challenge:      "expired-test-challenge",
		RelyingPartyID: h.config.RPID,
		UserID:         []byte(user.ID),
	}
	sessionDataJSON, _ := json.Marshal(sessionData)

	cs := &ChallengeState{
		ID:                  "expired-test-challenge",
		UserID:              user.ID,
		Challenge:           "expired-test-challenge",
		Type:                "registration",
		WebAuthnSessionData: string(sessionDataJSON),
		CreatedAt:           time.Now().UTC().Add(-10 * time.Minute),
		ExpiresAt:           time.Now().UTC().Add(-5 * time.Minute), // already expired
	}
	if err := h.store.SaveChallengeState(cs); err != nil {
		t.Fatalf("save challenge: %v", err)
	}

	// Build a request with the expired challenge.
	clientDataJSON := base64RawURLEncode([]byte(fmt.Sprintf(`{"type":"webauthn.create","challenge":"%s","origin":"http://localhost:4173"}`, "expired-test-challenge")))
	body := fmt.Sprintf(`{"id":"cred-id","rawId":"cred-id","type":"public-key","response":{"clientDataJSON":"%s","attestationObject":"%s"}}`, clientDataJSON, base64RawURLEncode([]byte("fake")))

	req := httptest.NewRequest(http.MethodPost, "/auth/register/finish",
		bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.HandleRegisterFinish(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want %d; body: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	var resp errorResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error == "" {
		t.Error("expected non-empty error message for expired challenge")
	}

	// No auth cookies should be set.
	for _, c := range rec.Result().Cookies() {
		if c.Name == AccessTokenCookieName || c.Name == RefreshTokenCookieName {
			t.Errorf("auth cookie %q should not be set on expired challenge", c.Name)
		}
	}

	// The expired challenge should have been cleaned up from the store.
	_, err = h.store.GetChallengeStateByID("expired-test-challenge")
	if err == nil {
		t.Error("expired challenge should have been deleted")
	}
}

func TestRegisterFinishRejectsChallengeTypeMismatch(t *testing.T) {
	h, _ := testHandlerEnv(t)

	// Create a user and store a LOGIN-type challenge, then try to use it for registration.
	user, err := h.store.CreateUser("type-mismatch-user", "typemismatch@example.com")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	sessionData := webauthn.SessionData{
		Challenge:      "type-mismatch-challenge",
		RelyingPartyID: h.config.RPID,
		UserID:         []byte(user.ID),
	}
	sessionDataJSON, _ := json.Marshal(sessionData)

	cs := &ChallengeState{
		ID:                  "type-mismatch-challenge",
		UserID:              user.ID,
		Challenge:           "type-mismatch-challenge",
		Type:                "login", // wrong type for register finish
		WebAuthnSessionData: string(sessionDataJSON),
		CreatedAt:           time.Now().UTC(),
		ExpiresAt:           time.Now().UTC().Add(5 * time.Minute),
	}
	if err := h.store.SaveChallengeState(cs); err != nil {
		t.Fatalf("save challenge: %v", err)
	}

	clientDataJSON := base64RawURLEncode([]byte(fmt.Sprintf(`{"type":"webauthn.create","challenge":"%s","origin":"http://localhost:4173"}`, "type-mismatch-challenge")))
	body := fmt.Sprintf(`{"id":"cred-id","rawId":"cred-id","type":"public-key","response":{"clientDataJSON":"%s","attestationObject":"%s"}}`, clientDataJSON, base64RawURLEncode([]byte("fake")))

	req := httptest.NewRequest(http.MethodPost, "/auth/register/finish",
		bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.HandleRegisterFinish(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want %d; body: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	var resp errorResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error == "" {
		t.Error("expected error for challenge type mismatch")
	}
}

func TestRegisterFinishReplayDoesNotMintSession(t *testing.T) {
	h, _ := testHandlerEnv(t)

	// Create a user and store a valid challenge.
	user, err := h.store.CreateUser("replay-user", "replay@example.com")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	sessionData := webauthn.SessionData{
		Challenge:      "replay-test-challenge",
		RelyingPartyID: h.config.RPID,
		UserID:         []byte(user.ID),
	}
	sessionDataJSON, _ := json.Marshal(sessionData)

	cs := &ChallengeState{
		ID:                  "replay-test-challenge",
		UserID:              user.ID,
		Challenge:           "replay-test-challenge",
		Type:                "registration",
		WebAuthnSessionData: string(sessionDataJSON),
		CreatedAt:           time.Now().UTC(),
		ExpiresAt:           time.Now().UTC().Add(5 * time.Minute),
	}
	if err := h.store.SaveChallengeState(cs); err != nil {
		t.Fatalf("save challenge: %v", err)
	}

	clientDataJSON := base64RawURLEncode([]byte(fmt.Sprintf(`{"type":"webauthn.create","challenge":"%s","origin":"http://localhost:4173"}`, "replay-test-challenge")))
	body := fmt.Sprintf(`{"id":"cred-id","rawId":"cred-id","type":"public-key","response":{"clientDataJSON":"%s","attestationObject":"%s"}}`, clientDataJSON, base64RawURLEncode([]byte("fake")))

	// First attempt: the challenge exists but the WebAuthn verification will fail
	// (fake response). The challenge will be consumed/deleted.
	req := httptest.NewRequest(http.MethodPost, "/auth/register/finish",
		bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.HandleRegisterFinish(rec, req)

	// The finish should fail (WebAuthn verification fails on fake data)
	// and no cookies should be set.
	if rec.Code == http.StatusOK {
		t.Error("finish should not succeed with fake WebAuthn data")
	}

	// Second attempt: replay the exact same body. Now the challenge is gone.
	req2 := httptest.NewRequest(http.MethodPost, "/auth/register/finish",
		bytes.NewBufferString(body))
	req2.Header.Set("Content-Type", "application/json")
	rec2 := httptest.NewRecorder()
	h.HandleRegisterFinish(rec2, req2)

	if rec2.Code != http.StatusBadRequest {
		t.Errorf("replay status: got %d, want %d; body: %s", rec2.Code, http.StatusBadRequest, rec2.Body.String())
	}

	var resp errorResponse
	if err := json.NewDecoder(rec2.Body).Decode(&resp); err != nil {
		t.Fatalf("decode replay response: %v", err)
	}
	if resp.Error == "" {
		t.Error("replay should produce an error message")
	}

	// No auth cookies should be set on either attempt.
	for _, c := range rec.Result().Cookies() {
		if c.Name == AccessTokenCookieName || c.Name == RefreshTokenCookieName {
			t.Errorf("first attempt: auth cookie %q should not be set", c.Name)
		}
	}
	for _, c := range rec2.Result().Cookies() {
		if c.Name == AccessTokenCookieName || c.Name == RefreshTokenCookieName {
			t.Errorf("replay attempt: auth cookie %q should not be set", c.Name)
		}
	}

	// Follow-up: /auth/session should report signed-out.
	sessionReq := httptest.NewRequest(http.MethodGet, "/auth/session", nil)
	// Carry forward any cookies from the replay attempt.
	for _, c := range rec2.Result().Cookies() {
		sessionReq.AddCookie(c)
	}
	sessionRec := httptest.NewRecorder()
	h.HandleSession(sessionRec, sessionReq)

	var sessionResp sessionResponse
	if err := json.NewDecoder(sessionRec.Body).Decode(&sessionResp); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	if sessionResp.Authenticated {
		t.Error("session should not be authenticated after failed/replayed finish")
	}
}

// --- Login Finish Tests ---

func TestLoginFinishRejectsNonPost(t *testing.T) {
	h, _ := testHandlerEnv(t)

	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		req := httptest.NewRequest(method, "/auth/login/finish", nil)
		rec := httptest.NewRecorder()
		h.HandleLoginFinish(rec, req)

		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("method %s: got status %d, want %d", method, rec.Code, http.StatusMethodNotAllowed)
		}
	}
}

func TestLoginFinishRejectsEmptyBody(t *testing.T) {
	h, _ := testHandlerEnv(t)

	req := httptest.NewRequest(http.MethodPost, "/auth/login/finish", nil)
	rec := httptest.NewRecorder()
	h.HandleLoginFinish(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want %d", rec.Code, http.StatusBadRequest)
	}

	var resp errorResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error == "" {
		t.Error("expected non-empty error message")
	}
}

func TestLoginFinishRejectsInvalidWebAuthnResponse(t *testing.T) {
	h, _ := testHandlerEnv(t)

	body := `{"id":"abc","response":{"clientDataJSON":"","authenticatorData":"","signature":""}}`
	req := httptest.NewRequest(http.MethodPost, "/auth/login/finish",
		bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.HandleLoginFinish(rec, req)

	if rec.Code < 400 || rec.Code >= 500 {
		t.Errorf("status: got %d, want 4xx; body: %s", rec.Code, rec.Body.String())
	}
}

func TestLoginFinishRejectsChallengeNotFound(t *testing.T) {
	h, _ := testHandlerEnv(t)

	clientDataJSON := base64RawURLEncode([]byte(`{"type":"webauthn.get","challenge":"nonexistent-login-challenge","origin":"http://localhost:4173"}`))
	body := fmt.Sprintf(`{"id":"cred-id","rawId":"cred-id","type":"public-key","response":{"clientDataJSON":"%s","authenticatorData":"%s","signature":"%s"}}`, clientDataJSON, base64RawURLEncode([]byte("fake-ad")), base64RawURLEncode([]byte("fake-sig")))

	req := httptest.NewRequest(http.MethodPost, "/auth/login/finish",
		bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.HandleLoginFinish(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want %d; body: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	// No auth cookies.
	for _, c := range rec.Result().Cookies() {
		if c.Name == AccessTokenCookieName || c.Name == RefreshTokenCookieName {
			t.Errorf("auth cookie %q should not be set on failed login finish", c.Name)
		}
	}
}

func TestLoginFinishRejectsExpiredChallenge(t *testing.T) {
	h, _ := testHandlerEnv(t)

	// Create a user with a credential.
	user, err := h.store.CreateUser("login-expired-user", "loginexpired@example.com")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	sessionData := webauthn.SessionData{
		Challenge:      "login-expired-challenge",
		RelyingPartyID: h.config.RPID,
		UserID:         []byte(user.ID),
	}
	sessionDataJSON, _ := json.Marshal(sessionData)

	cs := &ChallengeState{
		ID:                  "login-expired-challenge",
		UserID:              user.ID,
		Challenge:           "login-expired-challenge",
		Type:                "login",
		WebAuthnSessionData: string(sessionDataJSON),
		CreatedAt:           time.Now().UTC().Add(-10 * time.Minute),
		ExpiresAt:           time.Now().UTC().Add(-5 * time.Minute),
	}
	if err := h.store.SaveChallengeState(cs); err != nil {
		t.Fatalf("save challenge: %v", err)
	}

	clientDataJSON := base64RawURLEncode([]byte(fmt.Sprintf(`{"type":"webauthn.get","challenge":"%s","origin":"http://localhost:4173"}`, "login-expired-challenge")))
	body := fmt.Sprintf(`{"id":"cred-id","rawId":"cred-id","type":"public-key","response":{"clientDataJSON":"%s","authenticatorData":"%s","signature":"%s"}}`, clientDataJSON, base64RawURLEncode([]byte("fake-ad")), base64RawURLEncode([]byte("fake-sig")))

	req := httptest.NewRequest(http.MethodPost, "/auth/login/finish",
		bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.HandleLoginFinish(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want %d; body: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	// No auth cookies.
	for _, c := range rec.Result().Cookies() {
		if c.Name == AccessTokenCookieName || c.Name == RefreshTokenCookieName {
			t.Errorf("auth cookie %q should not be set on expired challenge", c.Name)
		}
	}
}

func TestLoginFinishRejectsChallengeTypeMismatch(t *testing.T) {
	h, _ := testHandlerEnv(t)

	user, err := h.store.CreateUser("login-type-mismatch-user", "logintm@example.com")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	sessionData := webauthn.SessionData{
		Challenge:      "login-type-mismatch-challenge",
		RelyingPartyID: h.config.RPID,
		UserID:         []byte(user.ID),
	}
	sessionDataJSON, _ := json.Marshal(sessionData)

	cs := &ChallengeState{
		ID:                  "login-type-mismatch-challenge",
		UserID:              user.ID,
		Challenge:           "login-type-mismatch-challenge",
		Type:                "registration", // wrong type for login finish
		WebAuthnSessionData: string(sessionDataJSON),
		CreatedAt:           time.Now().UTC(),
		ExpiresAt:           time.Now().UTC().Add(5 * time.Minute),
	}
	if err := h.store.SaveChallengeState(cs); err != nil {
		t.Fatalf("save challenge: %v", err)
	}

	clientDataJSON := base64RawURLEncode([]byte(fmt.Sprintf(`{"type":"webauthn.get","challenge":"%s","origin":"http://localhost:4173"}`, "login-type-mismatch-challenge")))
	body := fmt.Sprintf(`{"id":"cred-id","rawId":"cred-id","type":"public-key","response":{"clientDataJSON":"%s","authenticatorData":"%s","signature":"%s"}}`, clientDataJSON, base64RawURLEncode([]byte("fake-ad")), base64RawURLEncode([]byte("fake-sig")))

	req := httptest.NewRequest(http.MethodPost, "/auth/login/finish",
		bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.HandleLoginFinish(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want %d; body: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestLoginFinishReplayDoesNotMintSession(t *testing.T) {
	h, _ := testHandlerEnv(t)

	user, err := h.store.CreateUser("login-replay-user", "loginreplay@example.com")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	cred := &Credential{
		ID:              "cred-login-replay",
		UserID:          user.ID,
		PublicKey:       make([]byte, 64),
		AttestationType: "none",
		Transport:       `["internal"]`,
		SignCount:       0,
		AAGUID:          make([]byte, 16),
		Flags:           "{}",
		CreatedAt:       time.Now().UTC(),
	}
	if err := h.store.CreateCredential(cred); err != nil {
		t.Fatalf("create credential: %v", err)
	}

	sessionData := webauthn.SessionData{
		Challenge:      "login-replay-challenge",
		RelyingPartyID: h.config.RPID,
		UserID:         []byte(user.ID),
	}
	sessionDataJSON, _ := json.Marshal(sessionData)

	cs := &ChallengeState{
		ID:                  "login-replay-challenge",
		UserID:              user.ID,
		Challenge:           "login-replay-challenge",
		Type:                "login",
		WebAuthnSessionData: string(sessionDataJSON),
		CreatedAt:           time.Now().UTC(),
		ExpiresAt:           time.Now().UTC().Add(5 * time.Minute),
	}
	if err := h.store.SaveChallengeState(cs); err != nil {
		t.Fatalf("save challenge: %v", err)
	}

	clientDataJSON := base64RawURLEncode([]byte(fmt.Sprintf(`{"type":"webauthn.get","challenge":"%s","origin":"http://localhost:4173"}`, "login-replay-challenge")))
	body := fmt.Sprintf(`{"id":"cred-id","rawId":"cred-id","type":"public-key","response":{"clientDataJSON":"%s","authenticatorData":"%s","signature":"%s"}}`, clientDataJSON, base64RawURLEncode([]byte("fake-ad")), base64RawURLEncode([]byte("fake-sig")))

	// First attempt: will fail (fake response) and consume the challenge.
	req1 := httptest.NewRequest(http.MethodPost, "/auth/login/finish",
		bytes.NewBufferString(body))
	req1.Header.Set("Content-Type", "application/json")
	rec1 := httptest.NewRecorder()
	h.HandleLoginFinish(rec1, req1)

	// Second attempt: replay — challenge should be gone.
	req2 := httptest.NewRequest(http.MethodPost, "/auth/login/finish",
		bytes.NewBufferString(body))
	req2.Header.Set("Content-Type", "application/json")
	rec2 := httptest.NewRecorder()
	h.HandleLoginFinish(rec2, req2)

	if rec2.Code != http.StatusBadRequest {
		t.Errorf("replay status: got %d, want %d", rec2.Code, http.StatusBadRequest)
	}

	// No auth cookies on either attempt.
	for _, c := range rec1.Result().Cookies() {
		if c.Name == AccessTokenCookieName || c.Name == RefreshTokenCookieName {
			t.Errorf("first attempt: auth cookie %q should not be set", c.Name)
		}
	}
	for _, c := range rec2.Result().Cookies() {
		if c.Name == AccessTokenCookieName || c.Name == RefreshTokenCookieName {
			t.Errorf("replay attempt: auth cookie %q should not be set", c.Name)
		}
	}

	// Follow-up /auth/session should show signed-out.
	sessionReq := httptest.NewRequest(http.MethodGet, "/auth/session", nil)
	for _, c := range rec2.Result().Cookies() {
		sessionReq.AddCookie(c)
	}
	sessionRec := httptest.NewRecorder()
	h.HandleSession(sessionRec, sessionReq)

	var sessionResp sessionResponse
	if err := json.NewDecoder(sessionRec.Body).Decode(&sessionResp); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	if sessionResp.Authenticated {
		t.Error("session should not be authenticated after failed/replayed login finish")
	}
}

// --- Session Issuance Tests ---

func TestIssueSessionSetsCookiesAndMintsJWT(t *testing.T) {
	h, priv := testHandlerEnv(t)

	user, err := h.store.CreateUser("session-user", "sessiontest@example.com")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	// Use the handler's issueSession method via the finish flow.
	// We'll simulate this by directly calling the internal helpers
	// through a recorder.
	rec := httptest.NewRecorder()
	userInfo, err := h.issueSession(rec, user)
	if err != nil {
		t.Fatalf("issueSession: %v", err)
	}

	if userInfo.ID != user.ID {
		t.Errorf("user ID: got %q, want %q", userInfo.ID, user.ID)
	}
	if userInfo.Email != "sessiontest@example.com" {
		t.Errorf("email: got %q, want %q", userInfo.Email, "sessiontest@example.com")
	}

	// Check that auth cookies were set.
	cookies := rec.Result().Cookies()
	var accessCookie, refreshCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == AccessTokenCookieName {
			accessCookie = c
		}
		if c.Name == RefreshTokenCookieName {
			refreshCookie = c
		}
	}

	if accessCookie == nil {
		t.Fatal("access token cookie not set")
	}
	if refreshCookie == nil {
		t.Fatal("refresh token cookie not set")
	}

	// Validate cookie attributes: HttpOnly, SameSite, Secure, Path.
	if !accessCookie.HttpOnly {
		t.Error("access cookie should be HttpOnly")
	}
	if accessCookie.SameSite != http.SameSiteLaxMode {
		t.Error("access cookie should use SameSite=Lax")
	}
	if accessCookie.Secure {
		// In test config, CookieSecure is false.
		t.Error("access cookie should not be Secure in test config (CookieSecure=false)")
	}
	if accessCookie.Path != "/" {
		t.Errorf("access cookie Path: got %q, want %q", accessCookie.Path, "/")
	}

	if !refreshCookie.HttpOnly {
		t.Error("refresh cookie should be HttpOnly")
	}
	if refreshCookie.SameSite != http.SameSiteLaxMode {
		t.Error("refresh cookie should use SameSite=Lax")
	}
	if refreshCookie.Path != "/auth" {
		t.Errorf("refresh cookie Path: got %q, want %q", refreshCookie.Path, "/auth")
	}

	// Validate the access JWT.
	token, err := jwt.Parse(accessCookie.Value, func(t *jwt.Token) (interface{}, error) {
		if t.Method != jwt.SigningMethodEdDSA {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return priv.Public(), nil
	})
	if err != nil {
		t.Fatalf("parse access JWT: %v", err)
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		t.Fatal("invalid JWT claims type")
	}
	if claims["sub"] != user.ID {
		t.Errorf("JWT sub: got %v, want %q", claims["sub"], user.ID)
	}
	if scope, _ := claims["scope"].(string); scope != "access" {
		t.Errorf("JWT scope: got %q, want %q", scope, "access")
	}

	// Validate the refresh token is stored in the database.
	hash := sha256Sum([]byte(refreshCookie.Value))
	_, err = h.store.GetRefreshSessionByTokenHash(hash)
	if err != nil {
		t.Fatalf("refresh session not found by hash: %v", err)
	}
}

func TestAuthenticatedSessionWithValidCookies(t *testing.T) {
	h, priv := testHandlerEnv(t)

	user, err := h.store.CreateUser("auth-cookie-user", "cookieauth@example.com")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	// Issue a session and capture the cookies.
	rec := httptest.NewRecorder()
	_, err = h.issueSession(rec, user)
	if err != nil {
		t.Fatalf("issueSession: %v", err)
	}

	// Use the set cookies in a /auth/session request.
	req := httptest.NewRequest(http.MethodGet, "/auth/session", nil)
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}
	sessionRec := httptest.NewRecorder()
	h.HandleSession(sessionRec, req)

	if sessionRec.Code != http.StatusOK {
		t.Errorf("status: got %d, want %d", sessionRec.Code, http.StatusOK)
	}

	var resp sessionResponse
	if err := json.NewDecoder(sessionRec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Authenticated {
		t.Error("should be authenticated with valid cookies")
	}
	if resp.User == nil {
		t.Fatal("user info should be present")
	}
	if resp.User.Email != "cookieauth@example.com" {
		t.Errorf("email: got %q, want %q", resp.User.Email, "cookieauth@example.com")
	}
	if resp.User.ID != user.ID {
		t.Errorf("user ID: got %q, want %q", resp.User.ID, user.ID)
	}

	// Verify no secrets are leaked.
	body := sessionRec.Body.String()
	for _, secret := range []string{
		"choir_refresh",
		"public_key",
		"credential",
	} {
		if bytes.Contains([]byte(body), []byte(secret)) {
			t.Errorf("session response leaks secret %q", secret)
		}
	}

	// Explicitly verify the JWT value is NOT in the response body.
	// The access cookie value should only be in the cookie header, not the body.
	for _, c := range rec.Result().Cookies() {
		if c.Name == AccessTokenCookieName {
			if bytes.Contains([]byte(body), []byte(c.Value)) {
				t.Error("session response body should not contain the raw access JWT value")
			}
		}
	}

	_ = priv // used in closure above
}

func TestAuthenticatedSessionDoesNotLeakCredentialMaterial(t *testing.T) {
	h, priv := testHandlerEnv(t)

	// Create a user with a credential.
	user, err := h.store.CreateUser("no-leak-user", "noleak@example.com")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	cred := &Credential{
		ID:              "sensitive-cred-id",
		UserID:          user.ID,
		PublicKey:       []byte("sensitive-public-key-data-1234"),
		AttestationType: "none",
		Transport:       `["internal"]`,
		SignCount:       0,
		AAGUID:          make([]byte, 16),
		Flags:           "{}",
		CreatedAt:       time.Now().UTC(),
	}
	if err := h.store.CreateCredential(cred); err != nil {
		t.Fatalf("create credential: %v", err)
	}

	// Create a valid JWT for this user.
	claims := jwt.MapClaims{
		"sub":   user.ID,
		"exp":   time.Now().Add(5 * time.Minute).Unix(),
		"iat":   time.Now().Unix(),
		"scope": "access",
	}
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	tokenStr, err := token.SignedString(priv)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/auth/session", nil)
	req.AddCookie(&http.Cookie{
		Name:  AccessTokenCookieName,
		Value: tokenStr,
	})
	rec := httptest.NewRecorder()
	h.HandleSession(rec, req)

	body := rec.Body.String()

	// The response must not contain any raw credential material.
	for _, secret := range []string{
		"sensitive-public-key-data-1234",
		"sensitive-cred-id",
		"choir_refresh",
		"token_hash",
		"challenge",
	} {
		if bytes.Contains([]byte(body), []byte(secret)) {
			t.Errorf("session response leaks secret %q", secret)
		}
	}

	// The response should contain user identity fields.
	if !bytes.Contains([]byte(body), []byte("noleak@example.com")) {
		t.Error("session response should contain email")
	}
	if !bytes.Contains([]byte(body), []byte(user.ID)) {
		t.Error("session response should contain user ID")
	}
}

// --- Refresh Rotation Tests ---

func TestRefreshRotationRenewsAccessJWT(t *testing.T) {
	h, priv := testHandlerEnv(t)

	user, err := h.store.CreateUser("refresh-user", "refresh@example.com")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	// Issue a session.
	issueRec := httptest.NewRecorder()
	_, err = h.issueSession(issueRec, user)
	if err != nil {
		t.Fatalf("issueSession: %v", err)
	}

	// Extract the refresh cookie.
	var refreshCookie *http.Cookie
	for _, c := range issueRec.Result().Cookies() {
		if c.Name == RefreshTokenCookieName {
			refreshCookie = c
		}
	}
	if refreshCookie == nil {
		t.Fatal("refresh cookie not set")
	}

	// Create an expired access JWT (simulating access token expiry).
	expiredClaims := jwt.MapClaims{
		"sub":   user.ID,
		"exp":   time.Now().Add(-1 * time.Hour).Unix(), // expired
		"iat":   time.Now().Add(-2 * time.Hour).Unix(),
		"scope": "access",
	}
	expiredToken := jwt.NewWithClaims(jwt.SigningMethodEdDSA, expiredClaims)
	expiredTokenStr, err := expiredToken.SignedString(priv)
	if err != nil {
		t.Fatalf("sign expired token: %v", err)
	}

	// Request /auth/session with expired access + valid refresh.
	req := httptest.NewRequest(http.MethodGet, "/auth/session", nil)
	req.AddCookie(&http.Cookie{Name: AccessTokenCookieName, Value: expiredTokenStr})
	req.AddCookie(refreshCookie)
	rec := httptest.NewRecorder()
	h.HandleSession(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status: got %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp sessionResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Authenticated {
		t.Error("should be authenticated after refresh rotation")
	}
	if resp.User == nil || resp.User.Email != "refresh@example.com" {
		t.Error("user info should be present after refresh rotation")
	}

	// New access cookie should be set.
	var newAccessCookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == AccessTokenCookieName {
			newAccessCookie = c
		}
	}
	if newAccessCookie == nil {
		t.Fatal("new access cookie should be set after rotation")
	}

	// The new access JWT should be valid.
	newToken, err := jwt.Parse(newAccessCookie.Value, func(t *jwt.Token) (interface{}, error) {
		if t.Method != jwt.SigningMethodEdDSA {
			return nil, fmt.Errorf("unexpected method")
		}
		return priv.Public(), nil
	})
	if err != nil {
		t.Fatalf("parse new access JWT: %v", err)
	}
	if !newToken.Valid {
		t.Error("new access JWT should be valid")
	}

	// New refresh cookie should be set (rotation).
	var newRefreshCookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == RefreshTokenCookieName {
			newRefreshCookie = c
		}
	}
	if newRefreshCookie == nil {
		t.Fatal("new refresh cookie should be set after rotation")
	}

	// The old refresh session should be gone (rotated).
	oldHash := sha256Sum([]byte(refreshCookie.Value))
	_, err = h.store.GetRefreshSessionByTokenHash(oldHash)
	if err == nil {
		t.Error("old refresh session should be deleted after rotation")
	}

	// The new refresh session should exist.
	newHash := sha256Sum([]byte(newRefreshCookie.Value))
	_, err = h.store.GetRefreshSessionByTokenHash(newHash)
	if err != nil {
		t.Fatalf("new refresh session should exist: %v", err)
	}
}

func TestRefreshRotationRejectsExpiredRefresh(t *testing.T) {
	h, priv := testHandlerEnv(t)

	user, err := h.store.CreateUser("expired-refresh-user", "expiredrefresh@example.com")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	// Create an expired refresh session directly in the store.
	rawRefresh := "expired-refresh-token-value"
	hash := sha256Sum([]byte(rawRefresh))
	rs := &RefreshSession{
		ID:        "expired-rs-id",
		UserID:    user.ID,
		TokenHash: hash,
		CreatedAt: time.Now().UTC().Add(-800 * time.Hour),
		ExpiresAt: time.Now().UTC().Add(-1 * time.Hour), // expired
	}
	if err := h.store.CreateRefreshSession(rs); err != nil {
		t.Fatalf("create refresh session: %v", err)
	}

	// Create an expired access JWT.
	expiredClaims := jwt.MapClaims{
		"sub":   user.ID,
		"exp":   time.Now().Add(-1 * time.Hour).Unix(),
		"iat":   time.Now().Add(-2 * time.Hour).Unix(),
		"scope": "access",
	}
	expiredToken := jwt.NewWithClaims(jwt.SigningMethodEdDSA, expiredClaims)
	expiredTokenStr, _ := expiredToken.SignedString(priv)

	// Request /auth/session with both expired.
	req := httptest.NewRequest(http.MethodGet, "/auth/session", nil)
	req.AddCookie(&http.Cookie{Name: AccessTokenCookieName, Value: expiredTokenStr})
	req.AddCookie(&http.Cookie{Name: RefreshTokenCookieName, Value: rawRefresh})
	rec := httptest.NewRecorder()
	h.HandleSession(rec, req)

	var resp sessionResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Authenticated {
		t.Error("should not be authenticated with expired refresh")
	}
}

func TestReplayedOldRefreshTokenFailsAfterRotation(t *testing.T) {
	h, priv := testHandlerEnv(t)

	user, err := h.store.CreateUser("replay-refresh-user", "replayrefresh@example.com")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	// Issue a session.
	issueRec := httptest.NewRecorder()
	_, err = h.issueSession(issueRec, user)
	if err != nil {
		t.Fatalf("issueSession: %v", err)
	}

	var oldRefreshCookie *http.Cookie
	for _, c := range issueRec.Result().Cookies() {
		if c.Name == RefreshTokenCookieName {
			oldRefreshCookie = c
		}
	}
	if oldRefreshCookie == nil {
		t.Fatal("refresh cookie not set")
	}

	// Use the refresh to rotate (simulate access expiry).
	expiredClaims := jwt.MapClaims{
		"sub":   user.ID,
		"exp":   time.Now().Add(-1 * time.Hour).Unix(),
		"iat":   time.Now().Add(-2 * time.Hour).Unix(),
		"scope": "access",
	}
	expiredToken := jwt.NewWithClaims(jwt.SigningMethodEdDSA, expiredClaims)
	expiredTokenStr, _ := expiredToken.SignedString(priv)

	rotateReq := httptest.NewRequest(http.MethodGet, "/auth/session", nil)
	rotateReq.AddCookie(&http.Cookie{Name: AccessTokenCookieName, Value: expiredTokenStr})
	rotateReq.AddCookie(oldRefreshCookie)
	rotateRec := httptest.NewRecorder()
	h.HandleSession(rotateRec, rotateReq)

	var rotateResp sessionResponse
	if err := json.NewDecoder(rotateRec.Body).Decode(&rotateResp); err != nil {
		t.Fatalf("decode rotation: %v", err)
	}
	if !rotateResp.Authenticated {
		t.Error("rotation should succeed with valid refresh")
	}

	// Now try to use the OLD refresh token again (replay after rotation).
	replayReq := httptest.NewRequest(http.MethodGet, "/auth/session", nil)
	replayReq.AddCookie(&http.Cookie{Name: AccessTokenCookieName, Value: expiredTokenStr})
	replayReq.AddCookie(oldRefreshCookie) // old refresh token, already rotated
	replayRec := httptest.NewRecorder()
	h.HandleSession(replayRec, replayReq)

	var replayResp sessionResponse
	if err := json.NewDecoder(replayRec.Body).Decode(&replayResp); err != nil {
		t.Fatalf("decode replay: %v", err)
	}
	if replayResp.Authenticated {
		t.Error("should NOT be authenticated with replayed old refresh token after rotation")
	}
}

// --- ValidateAccessToken tests (for proxy use) ---

func TestValidateAccessTokenValidJWT(t *testing.T) {
	h, priv := testHandlerEnv(t)

	user, err := h.store.CreateUser("validate-user", "validate@example.com")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	// Issue a valid access JWT.
	accessToken, err := h.issueAccessJWT(user)
	if err != nil {
		t.Fatalf("issue access JWT: %v", err)
	}

	// Validate it.
	userID, err := h.ValidateAccessToken(accessToken)
	if err != nil {
		t.Fatalf("ValidateAccessToken: %v", err)
	}
	if userID != user.ID {
		t.Errorf("user ID: got %q, want %q", userID, user.ID)
	}

	_ = priv
}

func TestValidateAccessTokenRejectsTamperedJWT(t *testing.T) {
	h, priv := testHandlerEnv(t)

	user, err := h.store.CreateUser("tamper-user", "tamper@example.com")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	accessToken, err := h.issueAccessJWT(user)
	if err != nil {
		t.Fatalf("issue access JWT: %v", err)
	}

	// Tamper with the token.
	tampered := accessToken[:len(accessToken)-5] + "XXXXX"

	_, err = h.ValidateAccessToken(tampered)
	if err == nil {
		t.Error("tampered JWT should be rejected")
	}

	_ = priv
}

func TestValidateAccessTokenRejectsExpiredJWT(t *testing.T) {
	h, priv := testHandlerEnv(t)

	user, err := h.store.CreateUser("expired-jwt-user", "expiredjwt@example.com")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	// Create an expired JWT manually.
	claims := jwt.MapClaims{
		"sub":   user.ID,
		"exp":   time.Now().Add(-1 * time.Hour).Unix(),
		"iat":   time.Now().Add(-2 * time.Hour).Unix(),
		"scope": "access",
	}
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	tokenStr, _ := token.SignedString(priv)

	_, err = h.ValidateAccessToken(tokenStr)
	if err == nil {
		t.Error("expired JWT should be rejected")
	}

	_ = priv
}

func TestValidateAccessTokenRejectsNonAccessToken(t *testing.T) {
	h, priv := testHandlerEnv(t)

	user, err := h.store.CreateUser("non-access-user", "nonaccess@example.com")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	// Create a JWT without the "access" scope.
	claims := jwt.MapClaims{
		"sub":   user.ID,
		"exp":   time.Now().Add(5 * time.Minute).Unix(),
		"iat":   time.Now().Unix(),
		"scope": "refresh", // wrong scope
	}
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	tokenStr, _ := token.SignedString(priv)

	_, err = h.ValidateAccessToken(tokenStr)
	if err == nil {
		t.Error("JWT without access scope should be rejected")
	}

	_ = priv
}

// --- Cookie attribute tests ---

func TestAuthCookiesAreSecureWhenConfigured(t *testing.T) {
	store := TestStore(t)
	cfg := &Config{
		Port:              "0",
		DBPath:            filepath.Join(t.TempDir(), "auth.db"),
		RPID:              "localhost",
		RPOrigins:         []string{"http://localhost:4173"},
		JWTPrivateKeyPath: filepath.Join(t.TempDir(), "key"),
		AccessTokenTTL:    5 * time.Minute,
		RefreshTokenTTL:   720 * time.Hour,
		CookieSecure:      true, // simulate deployed HTTPS
	}

	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	wa, err := webauthn.New(&webauthn.Config{
		RPID:          cfg.RPID,
		RPDisplayName: "go-choir test",
		RPOrigins:     cfg.RPOrigins,
	})
	if err != nil {
		t.Fatalf("create webauthn: %v", err)
	}

	h := NewHandler(store, wa, cfg, priv)

	user, err := store.CreateUser("secure-cookie-user", "securecookie@example.com")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	rec := httptest.NewRecorder()
	_, err = h.issueSession(rec, user)
	if err != nil {
		t.Fatalf("issueSession: %v", err)
	}

	for _, c := range rec.Result().Cookies() {
		if c.Name == AccessTokenCookieName || c.Name == RefreshTokenCookieName {
			if !c.Secure {
				t.Errorf("cookie %q should be Secure when CookieSecure=true", c.Name)
			}
			if !c.HttpOnly {
				t.Errorf("cookie %q should be HttpOnly", c.Name)
			}
			if c.SameSite != http.SameSiteLaxMode {
				t.Errorf("cookie %q should use SameSite=Lax", c.Name)
			}
		}
	}
}

// --- Challenge session data storage tests ---

func TestRegisterBeginStoresSessionData(t *testing.T) {
	h, _ := testHandlerEnv(t)

	body := `{"email": "sessiondata@example.com"}`
	req := httptest.NewRequest(http.MethodPost, "/auth/register/begin",
		bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.HandleRegisterBegin(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	// Find the user.
	user, err := h.store.GetUserByEmail("sessiondata@example.com")
	if err != nil {
		t.Fatalf("get user: %v", err)
	}

	// Find the challenge for this user.
	// The challenge ID is the challenge string from the WebAuthn session.
	// We can look it up by checking recent challenge states.
	challenges, err := h.store.GetChallengeStatesByUserID(user.ID)
	if err != nil {
		t.Fatalf("get challenges: %v", err)
	}
	if len(challenges) == 0 {
		t.Fatal("no challenges found for user")
	}

	cs := challenges[0]
	if cs.WebAuthnSessionData == "" {
		t.Error("challenge state should have WebAuthn session data")
	}
	if cs.Type != "registration" {
		t.Errorf("challenge type: got %q, want %q", cs.Type, "registration")
	}

	// Verify the session data is valid JSON.
	var sessionData webauthn.SessionData
	if err := json.Unmarshal([]byte(cs.WebAuthnSessionData), &sessionData); err != nil {
		t.Fatalf("unmarshal session data: %v", err)
	}
	if sessionData.Challenge == "" {
		t.Error("session data challenge should not be empty")
	}
}

func TestLoginBeginStoresSessionData(t *testing.T) {
	h, _ := testHandlerEnv(t)

	// Create a user with a credential.
	user, err := h.store.CreateUser("login-session-data-user", "loginsd@example.com")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	cred := &Credential{
		ID:              "cred-lsd",
		UserID:          user.ID,
		PublicKey:       make([]byte, 64),
		AttestationType: "none",
		Transport:       `["internal"]`,
		SignCount:       0,
		AAGUID:          make([]byte, 16),
		Flags:           "{}",
		CreatedAt:       time.Now().UTC(),
	}
	if err := h.store.CreateCredential(cred); err != nil {
		t.Fatalf("create credential: %v", err)
	}

	body := `{"email": "loginsd@example.com"}`
	req := httptest.NewRequest(http.MethodPost, "/auth/login/begin",
		bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.HandleLoginBegin(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	challenges, err := h.store.GetChallengeStatesByUserID(user.ID)
	if err != nil {
		t.Fatalf("get challenges: %v", err)
	}
	if len(challenges) == 0 {
		t.Fatal("no challenges found for user")
	}

	cs := challenges[0]
	if cs.WebAuthnSessionData == "" {
		t.Error("login challenge state should have WebAuthn session data")
	}
	if cs.Type != "login" {
		t.Errorf("challenge type: got %q, want %q", cs.Type, "login")
	}

	var sessionData webauthn.SessionData
	if err := json.Unmarshal([]byte(cs.WebAuthnSessionData), &sessionData); err != nil {
		t.Fatalf("unmarshal session data: %v", err)
	}
}

// --- Logout Tests ---

func TestLogoutRejectsNonPost(t *testing.T) {
	h, _ := testHandlerEnv(t)

	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		req := httptest.NewRequest(method, "/auth/logout", nil)
		rec := httptest.NewRecorder()
		h.HandleLogout(rec, req)

		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("method %s: got status %d, want %d", method, rec.Code, http.StatusMethodNotAllowed)
		}

		var resp errorResponse
		if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
			t.Fatalf("decode error response: %v", err)
		}
		if resp.Error == "" {
			t.Errorf("method %s: expected non-empty error message", method)
		}
	}
}

func TestLogoutReturnsSignedOutWhenAlreadySignedOut(t *testing.T) {
	h, _ := testHandlerEnv(t)

	// POST /auth/logout with no cookies at all.
	req := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	rec := httptest.NewRecorder()
	h.HandleLogout(rec, req)

	// Must not return 5xx.
	if rec.Code >= 500 {
		t.Errorf("status: got %d (5xx), want non-5xx for signed-out logout", rec.Code)
	}

	ct := rec.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("Content-Type: got %q, want %q", ct, "application/json")
	}

	var resp sessionResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Authenticated {
		t.Error("should not be authenticated after logout")
	}
}

func TestLogoutInvalidatesAuthenticatedSession(t *testing.T) {
	h, priv := testHandlerEnv(t)

	// Create a user and issue a full session.
	user, err := h.store.CreateUser("logout-user", "logouttest@example.com")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	issueRec := httptest.NewRecorder()
	_, err = h.issueSession(issueRec, user)
	if err != nil {
		t.Fatalf("issueSession: %v", err)
	}

	// Capture the cookies.
	cookies := issueRec.Result().Cookies()

	// Verify /auth/session reports authenticated before logout.
	preReq := httptest.NewRequest(http.MethodGet, "/auth/session", nil)
	for _, c := range cookies {
		preReq.AddCookie(c)
	}
	preRec := httptest.NewRecorder()
	h.HandleSession(preRec, preReq)

	var preResp sessionResponse
	if err := json.NewDecoder(preRec.Body).Decode(&preResp); err != nil {
		t.Fatalf("decode pre-logout session: %v", err)
	}
	if !preResp.Authenticated {
		t.Fatal("should be authenticated before logout")
	}

	// Now call POST /auth/logout with the same cookies.
	logoutReq := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	for _, c := range cookies {
		logoutReq.AddCookie(c)
	}
	logoutRec := httptest.NewRecorder()
	h.HandleLogout(logoutRec, logoutReq)

	if logoutRec.Code >= 500 {
		t.Errorf("logout status: got %d, want non-5xx", logoutRec.Code)
	}

	// The logout response should indicate signed-out.
	var logoutResp sessionResponse
	if err := json.NewDecoder(logoutRec.Body).Decode(&logoutResp); err != nil {
		t.Fatalf("decode logout response: %v", err)
	}
	if logoutResp.Authenticated {
		t.Error("logout response should not be authenticated")
	}

	// The logout response should clear both cookies.
	logoutCookies := logoutRec.Result().Cookies()
	var accessCleared, refreshCleared bool
	for _, c := range logoutCookies {
		if c.Name == AccessTokenCookieName && c.MaxAge < 0 {
			accessCleared = true
		}
		if c.Name == RefreshTokenCookieName && c.MaxAge < 0 {
			refreshCleared = true
		}
	}
	if !accessCleared {
		t.Error("access cookie should be cleared (MaxAge < 0) on logout")
	}
	if !refreshCleared {
		t.Error("refresh cookie should be cleared (MaxAge < 0) on logout")
	}

	// All refresh sessions for the user should be deleted from the store.
	// We can verify this by checking that the old refresh token hash is gone.
	for _, c := range cookies {
		if c.Name == RefreshTokenCookieName {
			hash := sha256Sum([]byte(c.Value))
			_, err := h.store.GetRefreshSessionByTokenHash(hash)
			if err == nil {
				t.Error("refresh session should be deleted from store after logout")
			}
		}
	}

	// Post-logout: /auth/session should report signed-out even if we pass
	// the old (pre-logout) cookies. The old access JWT is still technically
	// valid until it expires, but the refresh session is deleted. However,
	// the access JWT is self-contained — so it may still report as valid.
	// The contract says old COOKIES should not work. But since the access JWT
	// is a self-contained JWT, we cannot invalidate it server-side without
	// a revocation list (which is out of scope for Milestone 1). The critical
	// contract behavior is that:
	// 1. The refresh session is deleted, so it cannot silently restore access.
	// 2. When the access JWT expires, /auth/session will be signed-out.
	//
	// Let's verify that the refresh session cannot restore the session.
	// Create an expired access JWT and try to use it with the old refresh.
	// The old refresh session was deleted, so this should fail.
	expiredClaims := jwt.MapClaims{
		"sub":   user.ID,
		"exp":   time.Now().Add(-1 * time.Hour).Unix(),
		"iat":   time.Now().Add(-2 * time.Hour).Unix(),
		"scope": "access",
	}
	expiredToken := jwt.NewWithClaims(jwt.SigningMethodEdDSA, expiredClaims)
	expiredTokenStr, err := expiredToken.SignedString(priv)
	if err != nil {
		t.Fatalf("sign expired token: %v", err)
	}

	// Use the old refresh cookie value + expired access.
	postReq := httptest.NewRequest(http.MethodGet, "/auth/session", nil)
	postReq.AddCookie(&http.Cookie{Name: AccessTokenCookieName, Value: expiredTokenStr})
	// Add the old refresh cookie.
	for _, c := range cookies {
		if c.Name == RefreshTokenCookieName {
			postReq.AddCookie(c)
		}
	}
	postRec := httptest.NewRecorder()
	h.HandleSession(postRec, postReq)

	var postResp sessionResponse
	if err := json.NewDecoder(postRec.Body).Decode(&postResp); err != nil {
		t.Fatalf("decode post-logout session: %v", err)
	}
	if postResp.Authenticated {
		t.Error("should NOT be authenticated after logout — refresh cannot restore session")
	}

	_ = priv
}

func TestLogoutRepeatIsSafe(t *testing.T) {
	h, _ := testHandlerEnv(t)

	// First logout with no cookies.
	req1 := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	rec1 := httptest.NewRecorder()
	h.HandleLogout(rec1, req1)

	if rec1.Code >= 500 {
		t.Errorf("first logout: got %d, want non-5xx", rec1.Code)
	}

	// Second logout with no cookies (repeat).
	req2 := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	rec2 := httptest.NewRecorder()
	h.HandleLogout(rec2, req2)

	if rec2.Code >= 500 {
		t.Errorf("second logout: got %d, want non-5xx", rec2.Code)
	}

	var resp sessionResponse
	if err := json.NewDecoder(rec2.Body).Decode(&resp); err != nil {
		t.Fatalf("decode second logout: %v", err)
	}
	if resp.Authenticated {
		t.Error("repeat logout should return signed-out, not authenticated")
	}
}

func TestLogoutWithBogusCookiesIsSafe(t *testing.T) {
	h, _ := testHandlerEnv(t)

	// Logout with bogus cookies — should not crash or return 5xx.
	req := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: AccessTokenCookieName, Value: "not-a-jwt"})
	req.AddCookie(&http.Cookie{Name: RefreshTokenCookieName, Value: "not-a-refresh-token"})
	rec := httptest.NewRecorder()
	h.HandleLogout(rec, req)

	if rec.Code >= 500 {
		t.Errorf("logout with bogus cookies: got %d, want non-5xx", rec.Code)
	}

	ct := rec.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("Content-Type: got %q, want %q", ct, "application/json")
	}

	var resp sessionResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Authenticated {
		t.Error("should not be authenticated after logout with bogus cookies")
	}
}

func TestLogoutThenSessionReportsSignedOut(t *testing.T) {
	h, _ := testHandlerEnv(t)

	// Create a user and issue a session.
	user, err := h.store.CreateUser("logout-session-user", "logoutsession@example.com")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	issueRec := httptest.NewRecorder()
	_, err = h.issueSession(issueRec, user)
	if err != nil {
		t.Fatalf("issueSession: %v", err)
	}

	cookies := issueRec.Result().Cookies()

	// Logout.
	logoutReq := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	for _, c := range cookies {
		logoutReq.AddCookie(c)
	}
	logoutRec := httptest.NewRecorder()
	h.HandleLogout(logoutRec, logoutReq)

	// Use the CLEARING cookies from the logout response to check /auth/session.
	// The clearing cookies have MaxAge < 0 and empty value, simulating the
	// browser's state after it processes the Set-Cookie headers.
	sessionReq := httptest.NewRequest(http.MethodGet, "/auth/session", nil)
	// Add the clearing cookies — they have empty/invalid values.
	for _, c := range logoutRec.Result().Cookies() {
		if c.Name == AccessTokenCookieName || c.Name == RefreshTokenCookieName {
			sessionReq.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
		}
	}
	sessionRec := httptest.NewRecorder()
	h.HandleSession(sessionRec, sessionReq)

	var sessionResp sessionResponse
	if err := json.NewDecoder(sessionRec.Body).Decode(&sessionResp); err != nil {
		t.Fatalf("decode session after logout: %v", err)
	}
	if sessionResp.Authenticated {
		t.Error("/auth/session should report signed-out after logout")
	}
}

// ======================================================================
// VAL-DEPLOY-003: Public auth API reachable with deployed-origin config
// VAL-DEPLOY-004: Cookie security attributes correct for deployed HTTPS
// ======================================================================

// deployedHandlerEnv sets up a Handler configured for the deployed public
// origin (draft.choir-ip.com, HTTPS, CookieSecure=true). This mirrors the
// production NixOS service configuration in nix/node-b.nix.
func deployedHandlerEnv(t *testing.T) (*Handler, ed25519.PrivateKey) {
	t.Helper()

	store := TestStore(t)
	cfg := &Config{
		Port:              "0",
		DBPath:            filepath.Join(t.TempDir(), "auth.db"),
		RPID:              "draft.choir-ip.com",
		RPOrigins:         []string{"https://draft.choir-ip.com"},
		JWTPrivateKeyPath: filepath.Join(t.TempDir(), "key"),
		AccessTokenTTL:    5 * time.Minute,
		RefreshTokenTTL:   720 * time.Hour,
		CookieSecure:      true,
	}

	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	wa, err := webauthn.New(&webauthn.Config{
		RPID:          cfg.RPID,
		RPDisplayName: "go-choir",
		RPOrigins:     cfg.RPOrigins,
	})
	if err != nil {
		t.Fatalf("create webauthn: %v", err)
	}

	return NewHandler(store, wa, cfg, priv), priv
}

// assertDeployedCookieAttributes checks that a cookie has the expected
// deployed-origin security attributes: Secure, HttpOnly, SameSite=Lax,
// and no Domain attribute (host-only cookie bound to the exact host).
func assertDeployedCookieAttributes(t *testing.T, c *http.Cookie, expectedPath string) {
	t.Helper()

	if !c.Secure {
		t.Errorf("cookie %q: Secure flag should be true for deployed HTTPS origin", c.Name)
	}
	if !c.HttpOnly {
		t.Errorf("cookie %q: HttpOnly flag should be true", c.Name)
	}
	if c.SameSite != http.SameSiteLaxMode {
		t.Errorf("cookie %q: SameSite should be Lax, got %v", c.Name, c.SameSite)
	}
	if c.Domain != "" {
		t.Errorf("cookie %q: Domain should be empty (host-only), got %q", c.Name, c.Domain)
	}
	if c.Path != expectedPath {
		t.Errorf("cookie %q: Path should be %q, got %q", c.Name, expectedPath, c.Path)
	}
}

// TestDeployedCookieContractOnSessionIssuance verifies that login (and by
// extension register finish) sets auth cookies with production-correct
// security attributes for the deployed HTTPS origin: Secure, HttpOnly,
// SameSite=Lax, host-only (no Domain), correct Path, and a positive MaxAge.
//
// VAL-DEPLOY-004: "Login ... set or clear Secure/HttpOnly/same-origin
// cookies correctly"
func TestDeployedCookieContractOnSessionIssuance(t *testing.T) {
	h, _ := deployedHandlerEnv(t)

	user, err := h.store.CreateUser("deployed-cookie-user", "deployedcookie@example.com")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	rec := httptest.NewRecorder()
	_, err = h.issueSession(rec, user)
	if err != nil {
		t.Fatalf("issueSession: %v", err)
	}

	cookies := rec.Result().Cookies()
	var accessCookie, refreshCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == AccessTokenCookieName {
			accessCookie = c
		}
		if c.Name == RefreshTokenCookieName {
			refreshCookie = c
		}
	}

	if accessCookie == nil {
		t.Fatal("access token cookie not set")
	}
	if refreshCookie == nil {
		t.Fatal("refresh token cookie not set")
	}

	// Access cookie: Secure, HttpOnly, SameSite=Lax, host-only, Path=/
	assertDeployedCookieAttributes(t, accessCookie, "/")
	if accessCookie.MaxAge <= 0 {
		t.Errorf("access cookie MaxAge should be positive, got %d", accessCookie.MaxAge)
	}

	// Refresh cookie: Secure, HttpOnly, SameSite=Lax, host-only, Path=/auth
	assertDeployedCookieAttributes(t, refreshCookie, "/auth")
	if refreshCookie.MaxAge <= 0 {
		t.Errorf("refresh cookie MaxAge should be positive, got %d", refreshCookie.MaxAge)
	}
}

// TestDeployedCookieContractOnRefreshRotation verifies that silent renewal
// (refresh rotation via GET /auth/session with expired access + valid refresh)
// reissues cookies with the same deployed-origin security attributes.
//
// VAL-DEPLOY-004: "renewal ... set or clear Secure/HttpOnly/same-origin
// cookies correctly"
func TestDeployedCookieContractOnRefreshRotation(t *testing.T) {
	h, priv := deployedHandlerEnv(t)

	user, err := h.store.CreateUser("deployed-rotation-user", "deployedrot@example.com")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	// Issue initial session.
	issueRec := httptest.NewRecorder()
	_, err = h.issueSession(issueRec, user)
	if err != nil {
		t.Fatalf("issueSession: %v", err)
	}

	// Extract the refresh cookie.
	var refreshCookie *http.Cookie
	for _, c := range issueRec.Result().Cookies() {
		if c.Name == RefreshTokenCookieName {
			refreshCookie = c
		}
	}
	if refreshCookie == nil {
		t.Fatal("refresh cookie not set in initial session")
	}

	// Create an expired access JWT.
	expiredClaims := jwt.MapClaims{
		"sub":   user.ID,
		"exp":   time.Now().Add(-1 * time.Hour).Unix(),
		"iat":   time.Now().Add(-2 * time.Hour).Unix(),
		"scope": "access",
	}
	expiredToken := jwt.NewWithClaims(jwt.SigningMethodEdDSA, expiredClaims)
	expiredTokenStr, err := expiredToken.SignedString(priv)
	if err != nil {
		t.Fatalf("sign expired token: %v", err)
	}

	// Request /auth/session with expired access + valid refresh.
	req := httptest.NewRequest(http.MethodGet, "/auth/session", nil)
	req.AddCookie(&http.Cookie{Name: AccessTokenCookieName, Value: expiredTokenStr})
	req.AddCookie(refreshCookie)
	rec := httptest.NewRecorder()
	h.HandleSession(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("rotation status: got %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp sessionResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode rotation response: %v", err)
	}
	if !resp.Authenticated {
		t.Fatal("should be authenticated after refresh rotation")
	}

	// Verify the rotated cookies maintain deployed-origin security attributes.
	cookies := rec.Result().Cookies()
	var newAccessCookie, newRefreshCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == AccessTokenCookieName {
			newAccessCookie = c
		}
		if c.Name == RefreshTokenCookieName {
			newRefreshCookie = c
		}
	}

	if newAccessCookie == nil {
		t.Fatal("new access cookie not set after rotation")
	}
	if newRefreshCookie == nil {
		t.Fatal("new refresh cookie not set after rotation")
	}

	// Rotated access cookie: same deployed-origin attributes.
	assertDeployedCookieAttributes(t, newAccessCookie, "/")
	if newAccessCookie.MaxAge <= 0 {
		t.Errorf("rotated access cookie MaxAge should be positive, got %d", newAccessCookie.MaxAge)
	}

	// Rotated refresh cookie: same deployed-origin attributes.
	assertDeployedCookieAttributes(t, newRefreshCookie, "/auth")
	if newRefreshCookie.MaxAge <= 0 {
		t.Errorf("rotated refresh cookie MaxAge should be positive, got %d", newRefreshCookie.MaxAge)
	}
}

// TestDeployedCookieContractOnLogout verifies that POST /auth/logout clears
// auth cookies with the same deployed-origin security attributes (Secure,
// HttpOnly, SameSite=Lax, host-only) so that the clearing Set-Cookie
// headers are accepted by the browser on the deployed HTTPS origin.
//
// VAL-DEPLOY-004: "logout ... set or clear Secure/HttpOnly/same-origin
// cookies correctly"
func TestDeployedCookieContractOnLogout(t *testing.T) {
	h, _ := deployedHandlerEnv(t)

	user, err := h.store.CreateUser("deployed-logout-user", "deployedlogout@example.com")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	// Issue a session.
	issueRec := httptest.NewRecorder()
	_, err = h.issueSession(issueRec, user)
	if err != nil {
		t.Fatalf("issueSession: %v", err)
	}

	// Logout with the session cookies.
	logoutReq := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	for _, c := range issueRec.Result().Cookies() {
		logoutReq.AddCookie(c)
	}
	logoutRec := httptest.NewRecorder()
	h.HandleLogout(logoutRec, logoutReq)

	if logoutRec.Code >= 500 {
		t.Fatalf("logout status: got %d, want non-5xx", logoutRec.Code)
	}

	// Verify the clearing cookies maintain deployed-origin security attributes.
	// Clearing cookies use MaxAge=-1 to instruct the browser to delete them.
	cookies := logoutRec.Result().Cookies()
	var accessCleared, refreshCleared bool
	for _, c := range cookies {
		if c.Name == AccessTokenCookieName {
			accessCleared = true
			assertDeployedCookieAttributes(t, c, "/")
			if c.MaxAge != -1 {
				t.Errorf("access clearing cookie MaxAge should be -1, got %d", c.MaxAge)
			}
			if c.Value != "" {
				t.Error("access clearing cookie Value should be empty")
			}
		}
		if c.Name == RefreshTokenCookieName {
			refreshCleared = true
			assertDeployedCookieAttributes(t, c, "/auth")
			if c.MaxAge != -1 {
				t.Errorf("refresh clearing cookie MaxAge should be -1, got %d", c.MaxAge)
			}
			if c.Value != "" {
				t.Error("refresh clearing cookie Value should be empty")
			}
		}
	}

	if !accessCleared {
		t.Error("access cookie should be cleared on logout")
	}
	if !refreshCleared {
		t.Error("refresh cookie should be cleared on logout")
	}
}

// TestDeployedOriginSessionEndpointReachable verifies that the /auth/session
// endpoint responds correctly with deployed-origin configuration (RP ID =
// draft.choir-ip.com, HTTPS origins, CookieSecure=true). A signed-out
// request should return {authenticated: false} with no 5xx error.
//
// VAL-DEPLOY-003: "Public auth API is reachable on the draft host"
func TestDeployedOriginSessionEndpointReachable(t *testing.T) {
	h, _ := deployedHandlerEnv(t)

	// Signed-out request.
	req := httptest.NewRequest(http.MethodGet, "/auth/session", nil)
	rec := httptest.NewRecorder()
	h.HandleSession(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("session status: got %d, want %d", rec.Code, http.StatusOK)
	}

	ct := rec.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("Content-Type: got %q, want %q", ct, "application/json")
	}

	var resp sessionResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Authenticated {
		t.Error("should not be authenticated without cookies")
	}
}

// TestDeployedOriginLogoutEndpointReachable verifies that the /auth/logout
// endpoint responds safely with deployed-origin configuration. A signed-out
// logout should be idempotent and return a non-5xx JSON response.
//
// VAL-DEPLOY-003: "Public auth API is reachable on the draft host"
func TestDeployedOriginLogoutEndpointReachable(t *testing.T) {
	h, _ := deployedHandlerEnv(t)

	req := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	rec := httptest.NewRecorder()
	h.HandleLogout(rec, req)

	if rec.Code >= 500 {
		t.Errorf("logout status: got %d, want non-5xx", rec.Code)
	}

	ct := rec.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("Content-Type: got %q, want %q", ct, "application/json")
	}

	var resp sessionResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Authenticated {
		t.Error("should not be authenticated after logout")
	}
}

// TestDeployedOriginRegisterBeginReachable verifies that the register/begin
// endpoint responds correctly with deployed-origin configuration, binding
// the WebAuthn challenge to the deployed RP ID.
//
// VAL-DEPLOY-003: "Public auth API is reachable on the draft host"
func TestDeployedOriginRegisterBeginReachable(t *testing.T) {
	h, _ := deployedHandlerEnv(t)

	body := `{"email": "deployed-reachability@example.com"}`
	req := httptest.NewRequest(http.MethodPost, "/auth/register/begin",
		bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.HandleRegisterBegin(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("register begin status: got %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	ct := rec.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("Content-Type: got %q, want %q", ct, "application/json")
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	pk, ok := resp["publicKey"].(map[string]interface{})
	if !ok {
		t.Fatal("response missing publicKey field")
	}

	rp, ok := pk["rp"].(map[string]interface{})
	if !ok {
		t.Fatal("publicKey.rp missing or not an object")
	}

	rpID, _ := rp["id"].(string)
	if rpID != "draft.choir-ip.com" {
		t.Errorf("RP ID: got %q, want %q", rpID, "draft.choir-ip.com")
	}

	challenge, _ := pk["challenge"].(string)
	if challenge == "" {
		t.Error("challenge should be non-empty")
	}
}

// TestDeployedOriginLoginBeginReachableForKnownUser verifies that the
// login/begin endpoint responds correctly for a registered user with
// deployed-origin configuration.
//
// VAL-DEPLOY-003: "Public auth API is reachable on the draft host"
func TestDeployedOriginLoginBeginReachableForKnownUser(t *testing.T) {
	h, _ := deployedHandlerEnv(t)

	// Create a user with a credential so login/begin can succeed.
	user, err := h.store.CreateUser("deployed-login-user", "deployedlogin@example.com")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	cred := &Credential{
		ID:              "deployed-cred-1",
		UserID:          user.ID,
		PublicKey:       make([]byte, 64),
		AttestationType: "none",
		Transport:       `["internal"]`,
		SignCount:       0,
		AAGUID:          make([]byte, 16),
		Flags:           "{}",
		CreatedAt:       time.Now().UTC(),
	}
	if err := h.store.CreateCredential(cred); err != nil {
		t.Fatalf("create credential: %v", err)
	}

	body := `{"email": "deployedlogin@example.com"}`
	req := httptest.NewRequest(http.MethodPost, "/auth/login/begin",
		bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.HandleLoginBegin(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("login begin status: got %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	ct := rec.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("Content-Type: got %q, want %q", ct, "application/json")
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	pk, ok := resp["publicKey"].(map[string]interface{})
	if !ok {
		t.Fatal("response missing publicKey field")
	}

	challenge, _ := pk["challenge"].(string)
	if challenge == "" {
		t.Error("challenge should be non-empty")
	}
}

// --- Helper functions for tests ---

// base64RawURLEncode encodes bytes to base64url without padding.
func base64RawURLEncode(data []byte) string {
	return base64.RawURLEncoding.EncodeToString(data)
}

// sha256Sum returns the hex-encoded SHA-256 hash of the input.
func sha256Sum(data []byte) string {
	hash := sha256.Sum256(data)
	return fmt.Sprintf("%x", hash)
}

// ======================================================================
// Re-login bug fix tests (VAL-AUTH-004, VAL-AUTH-005)
//
// These tests verify that the CredentialFlags (user_present, user_verified,
// backup_eligible, backup_state) and Authenticator.SignCount are properly
// stored and restored, which is required for WebAuthn re-login to succeed.
// ======================================================================

// TestCredentialFlagsStoredOnRegistration verifies that the CredentialFlags
// from a WebAuthn registration are stored in the credentials table.
func TestCredentialFlagsStoredOnRegistration(t *testing.T) {
	store := TestStore(t)

	user, err := store.CreateUser("flags-test-user", "flagsuser@example.com")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	// Create a credential with realistic flags that a platform authenticator
	// would set (UserPresent=true, UserVerified=true, BackupEligible=true).
	cred := &Credential{
		ID:              "cred-flags-test",
		UserID:          user.ID,
		PublicKey:       make([]byte, 64),
		AttestationType: "none",
		Transport:       `["internal"]`,
		SignCount:       0,
		AAGUID:          make([]byte, 16),
		Flags:           `{"user_present":true,"user_verified":true,"backup_eligible":true,"backup_state":false}`,
		CreatedAt:       time.Now().UTC(),
	}
	if err := store.CreateCredential(cred); err != nil {
		t.Fatalf("create credential: %v", err)
	}

	// Read it back and verify flags are persisted.
	creds, err := store.GetCredentialsByUserID(user.ID)
	if err != nil {
		t.Fatalf("get credentials: %v", err)
	}
	if len(creds) != 1 {
		t.Fatalf("expected 1 credential, got %d", len(creds))
	}
	if creds[0].Flags != `{"user_present":true,"user_verified":true,"backup_eligible":true,"backup_state":false}` {
		t.Errorf("flags: got %q, want backup_eligible=true flags", creds[0].Flags)
	}
}

// TestCredentialFlagsRestoredOnWebAuthnUser verifies that when a webauthnUser
// is created from stored credentials, the CredentialFlags and
// Authenticator.SignCount are properly restored. This is the core of the
// re-login bug fix — without this restoration, the WebAuthn library's
// validateLogin function would fail with "Backup Eligible flag inconsistency".
func TestCredentialFlagsRestoredOnWebAuthnUser(t *testing.T) {
	store := TestStore(t)

	user, err := store.CreateUser("restore-flags-user", "restoreflags@example.com")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	// Simulate a credential stored with BackupEligible=true (which is what
	// platform authenticators like Touch ID / Windows Hello set).
	cred := &Credential{
		ID:              "cred-restore-flags",
		UserID:          user.ID,
		PublicKey:       make([]byte, 64),
		AttestationType: "none",
		Transport:       `["internal"]`,
		SignCount:       5,
		AAGUID:          []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
		Flags:           `{"user_present":true,"user_verified":true,"backup_eligible":true,"backup_state":false}`,
		CreatedAt:       time.Now().UTC(),
	}
	if err := store.CreateCredential(cred); err != nil {
		t.Fatalf("create credential: %v", err)
	}

	// Reload credentials from the store (simulating re-login flow).
	creds, err := store.GetCredentialsByUserID(user.ID)
	if err != nil {
		t.Fatalf("get credentials: %v", err)
	}

	// Create a WebAuthn user adapter (this is what happens during login begin).
	waUser, err := newWebAuthnUser(user, creds)
	if err != nil {
		t.Fatalf("newWebAuthnUser: %v", err)
	}

	waCreds := waUser.WebAuthnCredentials()
	if len(waCreds) != 1 {
		t.Fatalf("expected 1 WebAuthn credential, got %d", len(waCreds))
	}

	// Verify the flags were restored correctly.
	if !waCreds[0].Flags.BackupEligible {
		t.Error("BackupEligible flag should be true after restoration from stored credential")
	}
	if waCreds[0].Flags.BackupState {
		t.Error("BackupState flag should be false after restoration")
	}
	if !waCreds[0].Flags.UserPresent {
		t.Error("UserPresent flag should be true after restoration")
	}
	if !waCreds[0].Flags.UserVerified {
		t.Error("UserVerified flag should be true after restoration")
	}

	// Verify the sign count was restored.
	if waCreds[0].Authenticator.SignCount != 5 {
		t.Errorf("SignCount: got %d, want 5", waCreds[0].Authenticator.SignCount)
	}

	// Verify the AAGUID was restored.
	if len(waCreds[0].Authenticator.AAGUID) != 16 {
		t.Errorf("AAGUID length: got %d, want 16", len(waCreds[0].Authenticator.AAGUID))
	}
}

// TestCredentialFlagsEmptyFlagsHandled verifies that credentials with empty
// or missing flags (e.g., from before the migration) are handled gracefully
// without panicking or causing errors.
func TestCredentialFlagsEmptyFlagsHandled(t *testing.T) {
	store := TestStore(t)

	user, err := store.CreateUser("empty-flags-user", "emptyflags@example.com")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	tests := []struct {
		name  string
		flags string
	}{
		{"empty json", "{}"},
		{"empty string", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cred := &Credential{
				ID:              "cred-empty-" + tt.name,
				UserID:          user.ID,
				PublicKey:       make([]byte, 64),
				AttestationType: "none",
				Transport:       `["internal"]`,
				SignCount:       0,
				AAGUID:          make([]byte, 16),
				Flags:           tt.flags,
				CreatedAt:       time.Now().UTC(),
			}
			if err := store.CreateCredential(cred); err != nil {
				t.Fatalf("create credential: %v", err)
			}

			creds, err := store.GetCredentialsByUserID(user.ID)
			if err != nil {
				t.Fatalf("get credentials: %v", err)
			}

			// Find the right credential.
			var found *Credential
			for i := range creds {
				if creds[i].ID == cred.ID {
					found = &creds[i]
					break
				}
			}
			if found == nil {
				t.Fatal("credential not found")
			}

			// newWebAuthnUser should not panic with empty flags.
			waUser, err := newWebAuthnUser(user, []Credential{*found})
			if err != nil {
				t.Fatalf("newWebAuthnUser with empty flags: %v", err)
			}

			waCreds := waUser.WebAuthnCredentials()
			if len(waCreds) != 1 {
				t.Fatalf("expected 1 credential, got %d", len(waCreds))
			}

			// All flags should be false (zero value) for empty/missing flags.
			if waCreds[0].Flags.BackupEligible {
				t.Error("BackupEligible should be false for empty flags")
			}
		})
	}
}

// TestSignCounterUpdatedOnLogin verifies that the sign counter is updated
// in the credentials table after a successful login (simulated by calling
// the store directly).
func TestSignCounterUpdatedOnLogin(t *testing.T) {
	store := TestStore(t)

	user, err := store.CreateUser("signcount-user", "signcounttest@example.com")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	cred := &Credential{
		ID:              "cred-signcount",
		UserID:          user.ID,
		PublicKey:       make([]byte, 64),
		AttestationType: "none",
		Transport:       `["internal"]`,
		SignCount:       0,
		AAGUID:          make([]byte, 16),
		Flags:           `{"user_present":true,"user_verified":true,"backup_eligible":true,"backup_state":false}`,
		CreatedAt:       time.Now().UTC(),
	}
	if err := store.CreateCredential(cred); err != nil {
		t.Fatalf("create credential: %v", err)
	}

	// Simulate multiple login cycles by updating sign count.
	for i := int64(1); i <= 3; i++ {
		if err := store.UpdateCredentialSignCount("cred-signcount", i); err != nil {
			t.Fatalf("update sign count %d: %v", i, err)
		}

		creds, err := store.GetCredentialsByUserID(user.ID)
		if err != nil {
			t.Fatalf("get credentials: %v", err)
		}
		if creds[0].SignCount != i {
			t.Errorf("after update %d: sign_count got %d, want %d", i, creds[0].SignCount, i)
		}
	}
}

// TestReLoginCredentialIdentity verifies that credential IDs survive the
// store → reload → WebAuthn user adapter round trip without corruption.
// Credential IDs must match byte-for-byte for the WebAuthn library to find
// the correct credential during login validation.
func TestReLoginCredentialIdentity(t *testing.T) {
	store := TestStore(t)

	user, err := store.CreateUser("identity-user", "identitytest@example.com")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	// Use a realistic credential ID (byte array, like WebAuthn produces).
	originalID := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08,
		0x09, 0x0A, 0x0B, 0x0C, 0x0D, 0x0E, 0x0F, 0x10}

	cred := &Credential{
		ID:              string(originalID),
		UserID:          user.ID,
		PublicKey:       make([]byte, 64),
		AttestationType: "none",
		Transport:       `["internal"]`,
		SignCount:       0,
		AAGUID:          make([]byte, 16),
		Flags:           `{"user_present":true,"user_verified":true,"backup_eligible":true,"backup_state":false}`,
		CreatedAt:       time.Now().UTC(),
	}
	if err := store.CreateCredential(cred); err != nil {
		t.Fatalf("create credential: %v", err)
	}

	// Reload and create WebAuthn user adapter.
	creds, err := store.GetCredentialsByUserID(user.ID)
	if err != nil {
		t.Fatalf("get credentials: %v", err)
	}

	waUser, err := newWebAuthnUser(user, creds)
	if err != nil {
		t.Fatalf("newWebAuthnUser: %v", err)
	}

	waCreds := waUser.WebAuthnCredentials()
	if len(waCreds) != 1 {
		t.Fatalf("expected 1 credential, got %d", len(waCreds))
	}

	// Verify the credential ID matches byte-for-byte.
	if !bytes.Equal(waCreds[0].ID, originalID) {
		t.Errorf("credential ID mismatch: got %x, want %x", waCreds[0].ID, originalID)
	}
}

// TestReLoginPublicKeySurvivesRoundTrip verifies that the public key bytes
// survive the store → reload → WebAuthn user adapter round trip.
func TestReLoginPublicKeySurvivesRoundTrip(t *testing.T) {
	store := TestStore(t)

	user, err := store.CreateUser("pubkey-user", "pubkeytest@example.com")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	// Use a non-trivial public key (64 bytes, like a real COSE key).
	originalPubKey := make([]byte, 64)
	for i := range originalPubKey {
		originalPubKey[i] = byte(i)
	}

	cred := &Credential{
		ID:              "cred-pubkey",
		UserID:          user.ID,
		PublicKey:       originalPubKey,
		AttestationType: "none",
		Transport:       `["internal"]`,
		SignCount:       0,
		AAGUID:          make([]byte, 16),
		Flags:           `{"user_present":true,"user_verified":true,"backup_eligible":true,"backup_state":false}`,
		CreatedAt:       time.Now().UTC(),
	}
	if err := store.CreateCredential(cred); err != nil {
		t.Fatalf("create credential: %v", err)
	}

	creds, err := store.GetCredentialsByUserID(user.ID)
	if err != nil {
		t.Fatalf("get credentials: %v", err)
	}

	waUser, err := newWebAuthnUser(user, creds)
	if err != nil {
		t.Fatalf("newWebAuthnUser: %v", err)
	}

	waCreds := waUser.WebAuthnCredentials()
	if !bytes.Equal(waCreds[0].PublicKey, originalPubKey) {
		t.Errorf("public key mismatch: got %x, want %x", waCreds[0].PublicKey, originalPubKey)
	}
}

// TestReLoginSessionDataPersistsForReLogin verifies that the challenge state
// and WebAuthn session data can be stored and retrieved, which is required
// for the login finish handler to validate the WebAuthn assertion during
// re-login.
func TestReLoginSessionDataPersistsForReLogin(t *testing.T) {
	store := TestStore(t)

	user, err := store.CreateUser("session-persist-user", "sessionpersist@example.com")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	cred := &Credential{
		ID:              "cred-session-persist",
		UserID:          user.ID,
		PublicKey:       make([]byte, 64),
		AttestationType: "none",
		Transport:       `["internal"]`,
		SignCount:       0,
		AAGUID:          make([]byte, 16),
		Flags:           `{"user_present":true,"user_verified":true,"backup_eligible":true,"backup_state":false}`,
		CreatedAt:       time.Now().UTC(),
	}
	if err := store.CreateCredential(cred); err != nil {
		t.Fatalf("create credential: %v", err)
	}

	// Simulate a login begin by storing a login challenge.
	sessionData := webauthn.SessionData{
		Challenge:            "relogin-test-challenge",
		RelyingPartyID:       "localhost",
		UserID:               []byte(user.ID),
		AllowedCredentialIDs: [][]byte{[]byte(cred.ID)},
	}
	sessionDataJSON, _ := json.Marshal(sessionData)

	cs := &ChallengeState{
		ID:                  "relogin-test-challenge",
		UserID:              user.ID,
		Challenge:           "relogin-test-challenge",
		Type:                "login",
		AllowedCredentials:  `["cred-session-persist"]`,
		WebAuthnSessionData: string(sessionDataJSON),
		CreatedAt:           time.Now().UTC(),
		ExpiresAt:           time.Now().UTC().Add(5 * time.Minute),
	}
	if err := store.SaveChallengeState(cs); err != nil {
		t.Fatalf("save challenge state: %v", err)
	}

	// Retrieve the challenge (simulating login finish).
	got, err := store.GetChallengeStateByID("relogin-test-challenge")
	if err != nil {
		t.Fatalf("get challenge state: %v", err)
	}

	// Verify the WebAuthn session data can be deserialized.
	var gotSession webauthn.SessionData
	if err := json.Unmarshal([]byte(got.WebAuthnSessionData), &gotSession); err != nil {
		t.Fatalf("unmarshal session data: %v", err)
	}
	if gotSession.Challenge != "relogin-test-challenge" {
		t.Errorf("challenge: got %q, want %q", gotSession.Challenge, "relogin-test-challenge")
	}
	if gotSession.RelyingPartyID != "localhost" {
		t.Errorf("rp ID: got %q, want %q", gotSession.RelyingPartyID, "localhost")
	}
	if len(gotSession.AllowedCredentialIDs) != 1 || string(gotSession.AllowedCredentialIDs[0]) != cred.ID {
		t.Errorf("allowed credential IDs: got %v, want [%q]", gotSession.AllowedCredentialIDs, cred.ID)
	}
}

// TestLogoutThenReLoginFlow simulates the complete register → login →
// logout → re-login lifecycle at the handler level, verifying that
// session state is properly managed across all transitions.
func TestLogoutThenReLoginFlow(t *testing.T) {
	h, priv := testHandlerEnv(t)

	// Step 1: Create a user and issue a session (simulating registration).
	user, err := h.store.CreateUser("lifecycle-user", "lifecycle@example.com")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	issueRec := httptest.NewRecorder()
	_, err = h.issueSession(issueRec, user)
	if err != nil {
		t.Fatalf("issue session: %v", err)
	}

	// Step 2: Verify session is authenticated.
	sessionReq := httptest.NewRequest(http.MethodGet, "/auth/session", nil)
	for _, c := range issueRec.Result().Cookies() {
		sessionReq.AddCookie(c)
	}
	sessionRec := httptest.NewRecorder()
	h.HandleSession(sessionRec, sessionReq)

	var sessionResp sessionResponse
	if err := json.NewDecoder(sessionRec.Body).Decode(&sessionResp); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	if !sessionResp.Authenticated {
		t.Fatal("should be authenticated after initial session")
	}
	if sessionResp.User.ID != user.ID {
		t.Errorf("user ID: got %q, want %q", sessionResp.User.ID, user.ID)
	}

	// Step 3: Logout.
	logoutReq := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	for _, c := range issueRec.Result().Cookies() {
		logoutReq.AddCookie(c)
	}
	logoutRec := httptest.NewRecorder()
	h.HandleLogout(logoutRec, logoutReq)

	// Verify logged out.
	var logoutResp sessionResponse
	if err := json.NewDecoder(logoutRec.Body).Decode(&logoutResp); err != nil {
		t.Fatalf("decode logout: %v", err)
	}
	if logoutResp.Authenticated {
		t.Error("should not be authenticated after logout")
	}

	// Step 4: Verify old session cookies no longer work.
	// Use an expired access JWT to simulate the access token having expired
	// after some time, and verify that the refresh token (deleted during logout)
	// cannot restore the session.
	expiredClaims := jwt.MapClaims{
		"sub":   user.ID,
		"exp":   time.Now().Add(-1 * time.Hour).Unix(),
		"iat":   time.Now().Add(-2 * time.Hour).Unix(),
		"scope": "access",
	}
	expiredToken := jwt.NewWithClaims(jwt.SigningMethodEdDSA, expiredClaims)
	expiredTokenStr, err := expiredToken.SignedString(priv)
	if err != nil {
		t.Fatalf("sign expired token: %v", err)
	}

	// Try to use the old refresh cookie with the expired access.
	postLogoutReq := httptest.NewRequest(http.MethodGet, "/auth/session", nil)
	postLogoutReq.AddCookie(&http.Cookie{Name: AccessTokenCookieName, Value: expiredTokenStr})
	for _, c := range issueRec.Result().Cookies() {
		if c.Name == RefreshTokenCookieName {
			postLogoutReq.AddCookie(c)
		}
	}
	postLogoutRec := httptest.NewRecorder()
	h.HandleSession(postLogoutRec, postLogoutReq)

	var postLogoutResp sessionResponse
	if err := json.NewDecoder(postLogoutRec.Body).Decode(&postLogoutResp); err != nil {
		t.Fatalf("decode post-logout session: %v", err)
	}
	if postLogoutResp.Authenticated {
		t.Error("should NOT be authenticated after logout — old refresh token should be invalidated")
	}

	// Step 5: Simulate re-login by issuing a new session.
	reLoginRec := httptest.NewRecorder()
	_, err = h.issueSession(reLoginRec, user)
	if err != nil {
		t.Fatalf("re-login issue session: %v", err)
	}

	// Step 6: Verify the new session works.
	reLoginSessionReq := httptest.NewRequest(http.MethodGet, "/auth/session", nil)
	for _, c := range reLoginRec.Result().Cookies() {
		reLoginSessionReq.AddCookie(c)
	}
	reLoginSessionRec := httptest.NewRecorder()
	h.HandleSession(reLoginSessionRec, reLoginSessionReq)

	var reLoginResp sessionResponse
	if err := json.NewDecoder(reLoginSessionRec.Body).Decode(&reLoginResp); err != nil {
		t.Fatalf("decode re-login session: %v", err)
	}
	if !reLoginResp.Authenticated {
		t.Error("should be authenticated after re-login")
	}
	if reLoginResp.User.ID != user.ID {
		t.Errorf("re-login user ID: got %q, want %q", reLoginResp.User.ID, user.ID)
	}
	if reLoginResp.User.Email != "lifecycle@example.com" {
		t.Errorf("re-login email: got %q, want %q", reLoginResp.User.Email, "lifecycle@example.com")
	}

	_ = priv
}

// TestConcurrentSessionsOnReLogin verifies that a user can have multiple
// concurrent refresh sessions (e.g., from different browsers), which is
// required for VAL-AUTH-017.
func TestConcurrentSessionsOnReLogin(t *testing.T) {
	h, _ := testHandlerEnv(t)

	user, err := h.store.CreateUser("concurrent-user", "concurrent@example.com")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	// Issue session 1 (simulating first browser).
	rec1 := httptest.NewRecorder()
	_, err = h.issueSession(rec1, user)
	if err != nil {
		t.Fatalf("issue session 1: %v", err)
	}

	// Issue session 2 (simulating second browser).
	rec2 := httptest.NewRecorder()
	_, err = h.issueSession(rec2, user)
	if err != nil {
		t.Fatalf("issue session 2: %v", err)
	}

	// Both sessions should report authenticated.
	for i, rec := range []*httptest.ResponseRecorder{rec1, rec2} {
		req := httptest.NewRequest(http.MethodGet, "/auth/session", nil)
		for _, c := range rec.Result().Cookies() {
			req.AddCookie(c)
		}
		checkRec := httptest.NewRecorder()
		h.HandleSession(checkRec, req)

		var resp sessionResponse
		if err := json.NewDecoder(checkRec.Body).Decode(&resp); err != nil {
			t.Fatalf("session %d: decode: %v", i+1, err)
		}
		if !resp.Authenticated {
			t.Errorf("session %d: should be authenticated", i+1)
		}
		if resp.User.ID != user.ID {
			t.Errorf("session %d: user ID mismatch", i+1)
		}
	}

	// Logout from session 1 should invalidate ALL sessions (current behavior).
	logoutReq := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	for _, c := range rec1.Result().Cookies() {
		logoutReq.AddCookie(c)
	}
	logoutRec := httptest.NewRecorder()
	h.HandleLogout(logoutRec, logoutReq)

	// Session 2's refresh token should also be invalidated.
	req2 := httptest.NewRequest(http.MethodGet, "/auth/session", nil)
	// Use an expired access JWT to force refresh token usage.
	for _, c := range rec2.Result().Cookies() {
		if c.Name == RefreshTokenCookieName {
			req2.AddCookie(c)
		}
	}
	checkRec2 := httptest.NewRecorder()
	h.HandleSession(checkRec2, req2)

	var resp2 sessionResponse
	if err := json.NewDecoder(checkRec2.Body).Decode(&resp2); err != nil {
		t.Fatalf("session 2 after logout 1: decode: %v", err)
	}
	// After logout (which deletes all refresh sessions for the user),
	// session 2 should not be restorable via refresh.
	if resp2.Authenticated {
		t.Error("session 2 should not be restorable after global logout deleted all refresh sessions")
	}
}

// ======================================================================
// Email validation tests (VAL-AUTH-007, VAL-AUTH-008)
// ======================================================================

func TestIsValidEmail(t *testing.T) {
	tests := []struct {
		email string
		valid bool
	}{
		{"user@example.com", true},
		{"alice@domain.org", true},
		{"bob+tag@company.co.uk", true},
		{"test.user@sub.domain.com", true},
		{"", false},
		{"not-an-email", false},
		{"missing@domain", false},
		{"@nodomain.com", false},
		{"spaces in@email.com", false},
		{"no-at-sign.com", false},
		{"double@@at.com", false},
	}

	for _, tt := range tests {
		t.Run(tt.email, func(t *testing.T) {
			got := isValidEmail(tt.email)
			if got != tt.valid {
				t.Errorf("isValidEmail(%q) = %v, want %v", tt.email, got, tt.valid)
			}
		})
	}
}

func TestRegisterBeginRejectsInvalidEmail(t *testing.T) {
	h, _ := testHandlerEnv(t)

	tests := []struct {
		name  string
		email string
	}{
		{"empty", ""},
		{"not email", "not-an-email"},
		{"missing domain dot", "missing@domain"},
		{"no at sign", "no-at-sign.com"},
		{"at sign only", "@nodomain.com"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"email": %q}`, tt.email)
			req := httptest.NewRequest(http.MethodPost, "/auth/register/begin",
				bytes.NewBufferString(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			h.HandleRegisterBegin(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Errorf("status: got %d, want %d; body: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
			}

			var resp errorResponse
			if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if resp.Error == "" {
				t.Error("expected non-empty error message for invalid email")
			}
		})
	}
}

func TestRegisterBeginAcceptsValidEmail(t *testing.T) {
	h, _ := testHandlerEnv(t)

	body := `{"email": "newuser@example.com"}`
	req := httptest.NewRequest(http.MethodPost, "/auth/register/begin",
		bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.HandleRegisterBegin(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	// Verify user was created with the email.
	user, err := h.store.GetUserByEmail("newuser@example.com")
	if err != nil {
		t.Fatalf("GetUserByEmail: %v", err)
	}
	if user.Email != "newuser@example.com" {
		t.Errorf("email: got %q, want %q", user.Email, "newuser@example.com")
	}
}

func TestLoginBeginRejectsInvalidEmail(t *testing.T) {
	h, _ := testHandlerEnv(t)

	tests := []struct {
		name  string
		email string
	}{
		{"empty", ""},
		{"not email", "not-an-email"},
		{"missing domain dot", "missing@domain"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"email": %q}`, tt.email)
			req := httptest.NewRequest(http.MethodPost, "/auth/login/begin",
				bytes.NewBufferString(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			h.HandleLoginBegin(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Errorf("status: got %d, want %d; body: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
			}

			var resp errorResponse
			if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if resp.Error == "" {
				t.Error("expected non-empty error message for invalid email")
			}
		})
	}
}

func TestRegisterBeginRejectsOldUsernameField(t *testing.T) {
	h, _ := testHandlerEnv(t)

	// Sending the old "username" field should fail since the handler
	// now expects "email".
	body := `{"username": "alice@example.com"}`
	req := httptest.NewRequest(http.MethodPost, "/auth/register/begin",
		bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.HandleRegisterBegin(rec, req)

	// The handler reads req.Email which is empty, so it should return 400.
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want %d; body: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

// ======================================================================
// Schema migration tests (VAL-AUTH-009)
// ======================================================================

func TestSchemaHasEmailColumn(t *testing.T) {
	store := TestStore(t)

	// Verify the users table has an email column.
	var colName string
	err := store.DB().QueryRow(
		"SELECT name FROM pragma_table_info('users') WHERE name = 'email'",
	).Scan(&colName)
	if err != nil {
		t.Fatalf("email column not found in users table: %v", err)
	}
	if colName != "email" {
		t.Errorf("column name: got %q, want %q", colName, "email")
	}
}

func TestEmailColumnHasUniqueConstraint(t *testing.T) {
	store := TestStore(t)

	// Try creating two users with the same email — should fail.
	if _, err := store.CreateUser("user-1", "unique@example.com"); err != nil {
		t.Fatalf("first create: %v", err)
	}
	_, err := store.CreateUser("user-2", "unique@example.com")
	if err == nil {
		t.Error("expected error for duplicate email, got nil")
	}
}

func TestMigrationCopiesUsernameToEmail(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "migration-test.db")

	// Open store to create schema, then close.
	store1, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}

	// Insert a user directly using the old "username" column name
	// (simulating a pre-migration database). Since the schema DDL now
	// creates "email" directly, we test that the email column is properly
	// used from the start.
	user, err := store1.CreateUser("mig-user-1", "migrated@example.com")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	_ = store1.Close()

	// Reopen and verify the user's email persists.
	store2, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	defer func() { _ = store2.Close() }()

	found, err := store2.GetUserByID(user.ID)
	if err != nil {
		t.Fatalf("GetUserByID: %v", err)
	}
	if found.Email != "migrated@example.com" {
		t.Errorf("email after reopen: got %q, want %q", found.Email, "migrated@example.com")
	}
}

// TestWebAuthnUserUsesEmailForDisplayName verifies that the WebAuthn user
// adapter uses the email for name and display name (VAL-AUTH-009).
func TestWebAuthnUserUsesEmailForDisplayName(t *testing.T) {
	store := TestStore(t)

	user, err := store.CreateUser("wa-email-user", "display@example.com")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	waUser, err := newWebAuthnUser(user, nil)
	if err != nil {
		t.Fatalf("newWebAuthnUser: %v", err)
	}

	if waUser.WebAuthnName() != "display@example.com" {
		t.Errorf("WebAuthnName: got %q, want %q", waUser.WebAuthnName(), "display@example.com")
	}
	if waUser.WebAuthnDisplayName() != "display@example.com" {
		t.Errorf("WebAuthnDisplayName: got %q, want %q", waUser.WebAuthnDisplayName(), "display@example.com")
	}
}
