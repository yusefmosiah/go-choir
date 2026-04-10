package auth

import (
	"bytes"
	"crypto/ed25519"
	"encoding/pem"
	"encoding/json"
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
		name    string
		body    string
	}{
		{"not json", `this is not json`},
		{"missing username field", `{"email": "alice@example.com"}`},
		{"empty username", `{"username": ""}`},
		{"username is number", `{"username": 123}`},
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

	body := `{"username": "alice"}`
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

	body := `{"username": "bob"}`
	req := httptest.NewRequest(http.MethodPost, "/auth/register/begin",
		bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.HandleRegisterBegin(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusOK)
	}

	// Verify the user was created in the store.
	user, err := h.store.GetUserByUsername("bob")
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if user.Username != "bob" {
		t.Errorf("username: got %q, want %q", user.Username, "bob")
	}
}

func TestRegisterBeginIdempotentForExistingUser(t *testing.T) {
	h, _ := testHandlerEnv(t)

	// Create user first.
	if _, err := h.store.CreateUser("existing-id", "charlie"); err != nil {
		t.Fatalf("create user: %v", err)
	}

	body := `{"username": "charlie"}`
	req := httptest.NewRequest(http.MethodPost, "/auth/register/begin",
		bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.HandleRegisterBegin(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusOK)
	}

	// Verify existing user was found, not duplicated.
	user, err := h.store.GetUserByUsername("charlie")
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
		{"missing username", `{"email": "alice@example.com"}`},
		{"empty username", `{"username": ""}`},
		{"username is null", `{"username": null}`},
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

	body := `{"username": "nonexistent"}`
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
	if _, err := h.store.CreateUser("user-no-creds", "nobody"); err != nil {
		t.Fatalf("create user: %v", err)
	}

	body := `{"username": "nobody"}`
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
	user, err := h.store.CreateUser("login-user", "dave")
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
		CreatedAt:       time.Now().UTC(),
	}
	if err := h.store.CreateCredential(cred); err != nil {
		t.Fatalf("create credential: %v", err)
	}

	body := `{"username": "dave"}`
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
	user, err := h.store.CreateUser("jwt-user", "eve")
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
	if resp.User.Username != "eve" {
		t.Errorf("username: got %q, want %q", resp.User.Username, "eve")
	}
	if resp.User.ID != user.ID {
		t.Errorf("user ID: got %q, want %q", resp.User.ID, user.ID)
	}
}

func TestSessionDoesNotLeakSecrets(t *testing.T) {
	h, priv := testHandlerEnv(t)

	// Create a user and a credential (secret).
	user, err := h.store.CreateUser("secret-user", "frank")
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

	user, err := store.CreateUser("wa-user-1", "walter")
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
	if waUser.WebAuthnName() != "walter" {
		t.Errorf("WebAuthnName: got %q, want %q", waUser.WebAuthnName(), "walter")
	}
	if waUser.WebAuthnDisplayName() != "walter" {
		t.Errorf("WebAuthnDisplayName: got %q, want %q", waUser.WebAuthnDisplayName(), "walter")
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

	// Step 1: Register begin for "alice"
	regBody := `{"username": "alice"}`
	regReq := httptest.NewRequest(http.MethodPost, "/auth/register/begin",
		bytes.NewBufferString(regBody))
	regReq.Header.Set("Content-Type", "application/json")
	regRec := httptest.NewRecorder()
	h.HandleRegisterBegin(regRec, regReq)

	if regRec.Code != http.StatusOK {
		t.Fatalf("register begin: got %d, want %d; body: %s", regRec.Code, http.StatusOK, regRec.Body.String())
	}

	// Step 2: Login begin should fail because no credentials yet
	loginBody := `{"username": "alice"}`
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

	body := `{"username": "deployed-alice"}`
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

// pemEncodeBlock encodes a *pem.Block to bytes.
func pemEncodeBlock(block *pem.Block) []byte {
	return pem.EncodeToMemory(block)
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
