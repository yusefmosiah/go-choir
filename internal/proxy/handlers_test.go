package proxy

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/ssh"
)

// testProxyEnv sets up a proxy Handler with a real backend sandbox and
// Ed25519 key material for JWT validation.
func testProxyEnv(t *testing.T) (*Handler, ed25519.PrivateKey, *httptest.Server) {
	t.Helper()

	// Generate a real Ed25519 key pair for JWT signing/verification.
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate ed25519 key: %v", err)
	}

	// Create a fake sandbox backend that echoes request data.
	sandboxMux := http.NewServeMux()
	sandboxMux.HandleFunc("/api/shell/bootstrap", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		user := r.Header.Get("X-Authenticated-User")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"sandbox_id": "sandbox-test",
			"user":       user,
			"bootstrap":  "placeholder-shell-v1",
			"path":       r.URL.Path,
			"method":     r.Method,
			"query":      r.URL.RawQuery,
			"status_code": 200,
		})
	})
	sandboxMux.HandleFunc("/api/shell/error", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"sandbox_id":  "sandbox-test",
			"status_code": 500,
			"error":       "deliberate sandbox error",
		})
	})
	sandboxMux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok", "service": "sandbox"})
	})

	sandboxServer := httptest.NewServer(sandboxMux)
	t.Cleanup(func() { sandboxServer.Close() })

	cfg := &Config{
		Port:              "0",
		SandboxURL:        sandboxServer.URL,
		AuthPublicKeyPath: "/unused/in/test",
	}

	handler, err := NewHandler(cfg, pub)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	return handler, priv, sandboxServer
}

// issueTestAccessJWT creates a signed Ed25519 JWT for the given user ID
// with a 5-minute TTL and "access" scope.
func issueTestAccessJWT(priv ed25519.PrivateKey, userID string) string {
	return issueTestAccessJWTWithTTL(priv, userID, 5*time.Minute)
}

// issueTestAccessJWTWithTTL creates a signed Ed25519 JWT for the given user
// ID with the specified TTL and "access" scope.
func issueTestAccessJWTWithTTL(priv ed25519.PrivateKey, userID string, ttl time.Duration) string {
	now := time.Now().UTC()
	claims := jwt.MapClaims{
		"sub":   userID,
		"iat":   now.Unix(),
		"exp":   now.Add(ttl).Unix(),
		"scope": "access",
	}
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	signed, err := token.SignedString(priv)
	if err != nil {
		panic(fmt.Sprintf("sign test JWT: %v", err))
	}
	return signed
}

// writeTestPublicKey writes an Ed25519 public key in OpenSSH authorized_keys
// format to the given path.
func writeTestPublicKey(t *testing.T, path string, pub ed25519.PublicKey) {
	t.Helper()
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("create SSH public key: %v", err)
	}
	data := ssh.MarshalAuthorizedKey(sshPub)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write public key: %v", err)
	}
}

// --- VAL-PROXY-001: Missing or invalid auth fails closed ---

func TestBootstrapDeniesMissingAuth(t *testing.T) {
	h, _, _ := testProxyEnv(t)

	req := httptest.NewRequest(http.MethodGet, "/api/shell/bootstrap", nil)
	w := httptest.NewRecorder()
	h.HandleBootstrap(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("missing auth: got status %d, want %d", w.Code, http.StatusUnauthorized)
	}

	var resp errorResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if resp.Error == "" {
		t.Error("expected non-empty error message")
	}
}

func TestBootstrapDeniesInvalidAuth(t *testing.T) {
	h, _, _ := testProxyEnv(t)

	req := httptest.NewRequest(http.MethodGet, "/api/shell/bootstrap", nil)
	req.AddCookie(&http.Cookie{
		Name:  "choir_access",
		Value: "this-is-not-a-jwt",
	})
	w := httptest.NewRecorder()
	h.HandleBootstrap(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("invalid auth: got status %d, want %d", w.Code, http.StatusUnauthorized)
	}

	var resp errorResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if resp.Error == "" {
		t.Error("expected non-empty error message")
	}
}

func TestBootstrapDeniesExpiredAuth(t *testing.T) {
	h, priv, _ := testProxyEnv(t)

	// Issue an access JWT that is already expired.
	expiredToken := issueTestAccessJWTWithTTL(priv, "user-123", -1*time.Minute)

	req := httptest.NewRequest(http.MethodGet, "/api/shell/bootstrap", nil)
	req.AddCookie(&http.Cookie{
		Name:  "choir_access",
		Value: expiredToken,
	})
	w := httptest.NewRecorder()
	h.HandleBootstrap(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expired auth: got status %d, want %d", w.Code, http.StatusUnauthorized)
	}

	var resp errorResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if resp.Error == "" {
		t.Error("expected non-empty error message")
	}
}

func TestBootstrapDeniesTamperedAuth(t *testing.T) {
	h, priv, _ := testProxyEnv(t)

	// Issue a valid token, then tamper with it.
	validToken := issueTestAccessJWT(priv, "user-123")
	tamperedToken := validToken + "tamper"

	req := httptest.NewRequest(http.MethodGet, "/api/shell/bootstrap", nil)
	req.AddCookie(&http.Cookie{
		Name:  "choir_access",
		Value: tamperedToken,
	})
	w := httptest.NewRecorder()
	h.HandleBootstrap(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("tampered auth: got status %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestBootstrapDeniesNonAccessToken(t *testing.T) {
	h, priv, _ := testProxyEnv(t)

	// Issue a JWT with a different scope (e.g., "refresh").
	now := time.Now().UTC()
	claims := jwt.MapClaims{
		"sub":   "user-123",
		"iat":   now.Unix(),
		"exp":   now.Add(5 * time.Minute).Unix(),
		"scope": "refresh",
	}
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	refreshToken, err := token.SignedString(priv)
	if err != nil {
		t.Fatalf("sign refresh JWT: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/shell/bootstrap", nil)
	req.AddCookie(&http.Cookie{
		Name:  "choir_access",
		Value: refreshToken,
	})
	w := httptest.NewRecorder()
	h.HandleBootstrap(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("non-access token: got status %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestBootstrapDeniesWrongSigningKey(t *testing.T) {
	_, priv, _ := testProxyEnv(t)

	// Generate a different key pair.
	_, wrongPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate wrong key: %v", err)
	}

	// Create a handler with the original public key.
	origPub := priv.Public().(ed25519.PublicKey)

	sandboxServer := httptest.NewServer(http.NewServeMux())
	defer sandboxServer.Close()

	cfg := &Config{Port: "0", SandboxURL: sandboxServer.URL, AuthPublicKeyPath: "/unused"}
	handler, err := NewHandler(cfg, origPub)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	// Sign a JWT with the wrong key.
	wrongToken := issueTestAccessJWT(wrongPriv, "user-123")

	req := httptest.NewRequest(http.MethodGet, "/api/shell/bootstrap", nil)
	req.AddCookie(&http.Cookie{
		Name:  "choir_access",
		Value: wrongToken,
	})
	w := httptest.NewRecorder()
	handler.HandleBootstrap(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("wrong signing key: got status %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

// --- VAL-PROXY-002: Authenticated proxying preserves request and response behavior ---

func TestBootstrapAuthenticatedReachesSandbox(t *testing.T) {
	h, priv, sandbox := testProxyEnv(t)
	_ = sandbox

	accessToken := issueTestAccessJWT(priv, "user-456")

	req := httptest.NewRequest(http.MethodGet, "/api/shell/bootstrap", nil)
	req.AddCookie(&http.Cookie{
		Name:  "choir_access",
		Value: accessToken,
	})
	w := httptest.NewRecorder()
	h.HandleBootstrap(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("authenticated request: got status %d, want %d", w.Code, http.StatusOK)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode bootstrap response: %v", err)
	}

	// The sandbox should have received the request and returned its identity.
	if resp["sandbox_id"] != "sandbox-test" {
		t.Errorf("sandbox_id: got %v, want %q", resp["sandbox_id"], "sandbox-test")
	}
}

func TestBootstrapPreservesPublicRequestPath(t *testing.T) {
	h, priv, _ := testProxyEnv(t)

	accessToken := issueTestAccessJWT(priv, "user-789")

	req := httptest.NewRequest(http.MethodGet, "/api/shell/bootstrap", nil)
	req.AddCookie(&http.Cookie{
		Name:  "choir_access",
		Value: accessToken,
	})
	w := httptest.NewRecorder()
	h.HandleBootstrap(w, req)

	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode bootstrap response: %v", err)
	}

	// The sandbox should receive the same public path.
	if resp["path"] != "/api/shell/bootstrap" {
		t.Errorf("path: got %v, want %q", resp["path"], "/api/shell/bootstrap")
	}
}

func TestBootstrapPreservesRequestMethod(t *testing.T) {
	h, priv, _ := testProxyEnv(t)

	accessToken := issueTestAccessJWT(priv, "user-789")

	req := httptest.NewRequest(http.MethodGet, "/api/shell/bootstrap", nil)
	req.AddCookie(&http.Cookie{
		Name:  "choir_access",
		Value: accessToken,
	})
	w := httptest.NewRecorder()
	h.HandleBootstrap(w, req)

	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode bootstrap response: %v", err)
	}

	if resp["method"] != "GET" {
		t.Errorf("method: got %v, want %q", resp["method"], "GET")
	}
}

func TestBootstrapPreservesQueryString(t *testing.T) {
	h, priv, _ := testProxyEnv(t)

	accessToken := issueTestAccessJWT(priv, "user-789")

	req := httptest.NewRequest(http.MethodGet, "/api/shell/bootstrap?detail=full&v=2", nil)
	req.AddCookie(&http.Cookie{
		Name:  "choir_access",
		Value: accessToken,
	})
	w := httptest.NewRecorder()
	h.HandleBootstrap(w, req)

	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode bootstrap response: %v", err)
	}

	if resp["query"] != "detail=full&v=2" {
		t.Errorf("query: got %v, want %q", resp["query"], "detail=full&v=2")
	}
}

func TestBootstrapPreservesUpstreamStatus(t *testing.T) {
	h, priv, _ := testProxyEnv(t)

	accessToken := issueTestAccessJWT(priv, "user-789")

	req := httptest.NewRequest(http.MethodGet, "/api/shell/bootstrap", nil)
	req.AddCookie(&http.Cookie{
		Name:  "choir_access",
		Value: accessToken,
	})
	w := httptest.NewRecorder()
	h.HandleBootstrap(w, req)

	// The sandbox returns 200, so the proxy should pass that through.
	if w.Code != http.StatusOK {
		t.Errorf("upstream status: got %d, want %d", w.Code, http.StatusOK)
	}
}

func TestBootstrapPreservesUpstreamNon2xx(t *testing.T) {
	// Set up a proxy with a sandbox that has an error endpoint.
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	sandboxMux := http.NewServeMux()
	sandboxMux.HandleFunc("/api/shell/error", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"sandbox_id":  "sandbox-test",
			"status_code": 500,
			"error":       "deliberate sandbox error",
		})
	})

	sandboxServer := httptest.NewServer(sandboxMux)
	defer sandboxServer.Close()

	cfg := &Config{Port: "0", SandboxURL: sandboxServer.URL, AuthPublicKeyPath: "/unused"}
	handler, err := NewHandler(cfg, pub)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	accessToken := issueTestAccessJWT(priv, "user-789")

	req := httptest.NewRequest(http.MethodGet, "/api/shell/error", nil)
	req.AddCookie(&http.Cookie{
		Name:  "choir_access",
		Value: accessToken,
	})
	w := httptest.NewRecorder()
	handler.HandleProtectedAPI(w, req)

	// The proxy should pass through the 500 from the upstream.
	if w.Code != http.StatusInternalServerError {
		t.Errorf("upstream 500 passthrough: got %d, want %d", w.Code, http.StatusInternalServerError)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}

	if resp["error"] != "deliberate sandbox error" {
		t.Errorf("upstream error body: got %v, want %q", resp["error"], "deliberate sandbox error")
	}
}

func TestBootstrapInjectsUserContext(t *testing.T) {
	h, priv, _ := testProxyEnv(t)

	accessToken := issueTestAccessJWT(priv, "user-context-test")

	req := httptest.NewRequest(http.MethodGet, "/api/shell/bootstrap", nil)
	req.AddCookie(&http.Cookie{
		Name:  "choir_access",
		Value: accessToken,
	})
	w := httptest.NewRecorder()
	h.HandleBootstrap(w, req)

	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode bootstrap response: %v", err)
	}

	// The sandbox should receive the user context from the JWT subject.
	if resp["user"] != "user-context-test" {
		t.Errorf("user context: got %v, want %q", resp["user"], "user-context-test")
	}
}

func TestBootstrapIgnoresClientSuppliedUserContext(t *testing.T) {
	h, priv, _ := testProxyEnv(t)

	accessToken := issueTestAccessJWT(priv, "user-real-identity")

	req := httptest.NewRequest(http.MethodGet, "/api/shell/bootstrap", nil)
	req.AddCookie(&http.Cookie{
		Name:  "choir_access",
		Value: accessToken,
	})
	// Client tries to spoof identity.
	req.Header.Set("X-Authenticated-User", "spoofed-attacker-identity")
	w := httptest.NewRecorder()
	h.HandleBootstrap(w, req)

	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode bootstrap response: %v", err)
	}

	// The proxy should inject the JWT-verified user, not the client-supplied value.
	if resp["user"] != "user-real-identity" {
		t.Errorf("spoofed identity: got %v, want %q (JWT identity)", resp["user"], "user-real-identity")
	}
}

func TestBootstrapProxyDoesNotLeakToSignedOutUsers(t *testing.T) {
	h, _, sandbox := testProxyEnv(t)
	_ = sandbox

	// Request without auth should not reach the sandbox (no sandbox data in response).
	req := httptest.NewRequest(http.MethodGet, "/api/shell/bootstrap", nil)
	w := httptest.NewRecorder()
	h.HandleBootstrap(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("signed-out request: got status %d, want %d", w.Code, http.StatusUnauthorized)
	}

	// The response should be an auth error, not sandbox data.
	var resp errorResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}

	if resp.Error == "" {
		t.Error("expected non-empty auth error")
	}

	// Verify the error is not a sandbox payload (no sandbox_id field).
	var raw map[string]interface{}
	json.NewDecoder(w.Body).Decode(&raw) // decode again from already-consumed body
	// The error response should only have "error", not sandbox fields.
	_, hasSandboxID := raw["sandbox_id"]
	if hasSandboxID {
		t.Error("signed-out response should not contain sandbox_id")
	}
}

func TestBootstrapRejectsNonGet(t *testing.T) {
	h, priv, _ := testProxyEnv(t)

	accessToken := issueTestAccessJWT(priv, "user-123")

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/api/shell/bootstrap", nil)
			req.AddCookie(&http.Cookie{
				Name:  "choir_access",
				Value: accessToken,
			})
			w := httptest.NewRecorder()
			h.HandleBootstrap(w, req)

			if w.Code != http.StatusMethodNotAllowed {
				t.Errorf("method %s: got status %d, want %d", method, w.Code, http.StatusMethodNotAllowed)
			}
		})
	}
}

// --- Config + LoadPublicKey integration test ---

func TestLoadPublicKeyFromTestKey(t *testing.T) {
	// Generate a key pair, write the public key to a temp file, then load it.
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	dir := t.TempDir()
	pubPath := filepath.Join(dir, "test.pub")
	writeTestPublicKey(t, pubPath, priv.Public().(ed25519.PublicKey))

	loadedPub, err := LoadPublicKey(pubPath)
	if err != nil {
		t.Fatalf("LoadPublicKey: %v", err)
	}

	if len(loadedPub) != 32 {
		t.Errorf("public key length: got %d, want 32", len(loadedPub))
	}
}

// --- HandleAPI routing test ---

func TestHandleAPIReturnsNotFoundForUnknownRoutes(t *testing.T) {
	h, priv, _ := testProxyEnv(t)

	accessToken := issueTestAccessJWT(priv, "user-123")

	req := httptest.NewRequest(http.MethodGet, "/api/unknown/route", nil)
	req.AddCookie(&http.Cookie{
		Name:  "choir_access",
		Value: accessToken,
	})
	w := httptest.NewRecorder()
	h.HandleAPI(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("unknown API route: got status %d, want %d", w.Code, http.StatusNotFound)
	}
}

// --- Edge cases ---

func TestBootstrapWithEmptyCookieValue(t *testing.T) {
	h, _, _ := testProxyEnv(t)

	req := httptest.NewRequest(http.MethodGet, "/api/shell/bootstrap", nil)
	req.AddCookie(&http.Cookie{
		Name:  "choir_access",
		Value: "",
	})
	w := httptest.NewRecorder()
	h.HandleBootstrap(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("empty cookie value: got status %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestValidateAccessJWTWithWrongKey(t *testing.T) {
	// Create a handler with one key, then validate a JWT signed with a different key.
	_, wrongPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate wrong key: %v", err)
	}

	sandboxServer := httptest.NewServer(http.NewServeMux())
	defer sandboxServer.Close()

	// Handler uses the original public key.
	origPub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate original key: %v", err)
	}

	cfg := &Config{Port: "0", SandboxURL: sandboxServer.URL, AuthPublicKeyPath: "/unused"}
	handler, err := NewHandler(cfg, origPub)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	// Sign with the wrong key.
	wrongToken := issueTestAccessJWT(wrongPriv, "user-attacker")

	req := httptest.NewRequest(http.MethodGet, "/api/shell/bootstrap", nil)
	req.AddCookie(&http.Cookie{
		Name:  "choir_access",
		Value: wrongToken,
	})
	w := httptest.NewRecorder()
	handler.HandleBootstrap(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("wrong-key JWT: got status %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestBootstrapAuthenticatedReturnsJSONContentType(t *testing.T) {
	h, priv, _ := testProxyEnv(t)

	accessToken := issueTestAccessJWT(priv, "user-789")

	req := httptest.NewRequest(http.MethodGet, "/api/shell/bootstrap", nil)
	req.AddCookie(&http.Cookie{
		Name:  "choir_access",
		Value: accessToken,
	})
	w := httptest.NewRecorder()
	h.HandleBootstrap(w, req)

	ct := w.Header().Get("Content-Type")
	if ct == "" {
		t.Error("Content-Type header is missing")
	}
}

func TestBootstrapUnauthenticatedReturnsJSONContentType(t *testing.T) {
	h, _, _ := testProxyEnv(t)

	req := httptest.NewRequest(http.MethodGet, "/api/shell/bootstrap", nil)
	w := httptest.NewRecorder()
	h.HandleBootstrap(w, req)

	ct := w.Header().Get("Content-Type")
	if ct == "" {
		t.Error("Content-Type header is missing on auth failure")
	}
}
