package proxy

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
	"github.com/yusefmosiah/go-choir/internal/vmctl"
	"golang.org/x/crypto/ssh"
)

// testProxyEnv sets up a proxy Handler with a real backend sandbox and
// Ed25519 key material for JWT validation. The sandbox backend includes
// HTTP bootstrap and WebSocket echo endpoints matching the real sandbox
// surface used in production.
func testProxyEnv(t *testing.T) (*Handler, ed25519.PrivateKey, *httptest.Server) {
	t.Helper()

	// Generate a real Ed25519 key pair for JWT signing/verification.
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate ed25519 key: %v", err)
	}

	// Create a fake sandbox backend that echoes request data and supports WS.
	sandboxMux := http.NewServeMux()
	sandboxMux.HandleFunc("/api/shell/bootstrap", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		user := r.Header.Get("X-Authenticated-User")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"sandbox_id":  "sandbox-test",
			"user":        user,
			"bootstrap":   "placeholder-shell-v1",
			"path":        r.URL.Path,
			"method":      r.Method,
			"query":       r.URL.RawQuery,
			"status_code": 200,
		})
	})
	sandboxMux.HandleFunc("/api/shell/error", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"sandbox_id":  "sandbox-test",
			"status_code": 500,
			"error":       "deliberate sandbox error",
		})
	})
	sandboxMux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "service": "sandbox"})
	})
	// WebSocket echo endpoint matching the real sandbox surface.
	sandboxMux.HandleFunc("/api/ws", func(w http.ResponseWriter, r *http.Request) {
		user := r.Header.Get("X-Authenticated-User")
		upgrader := websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		// Send initial connected message.
		connected := map[string]interface{}{
			"sandbox_id": "sandbox-test",
			"user":       user,
			"type":       "connected",
			"payload":    "websocket channel open",
		}
		if err := conn.WriteJSON(connected); err != nil {
			return
		}

		// Echo loop.
		for {
			mt, msg, err := conn.ReadMessage()
			if err != nil {
				break
			}
			// Parse the incoming JSON to extract payload, then echo back.
			var incoming map[string]interface{}
			if json.Unmarshal(msg, &incoming) == nil {
				echo := map[string]interface{}{
					"sandbox_id": "sandbox-test",
					"user":       user,
					"type":       "echo",
					"payload":    incoming["payload"],
				}
				if err := conn.WriteJSON(echo); err != nil {
					break
				}
			} else {
				// Non-JSON: echo as raw text message.
				if err := conn.WriteMessage(mt, msg); err != nil {
					break
				}
			}
		}
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

// testWSProxyEnv sets up a full proxy server with WS support and returns
// the proxy test server URL for WebSocket dialing, the signing key, and
// a cleanup function.
func testWSProxyEnv(t *testing.T) (*httptest.Server, ed25519.PrivateKey) {
	t.Helper()

	handler, priv, _ := testProxyEnv(t)

	// Build a mux that routes both HTTP and WS through the proxy handler.
	mux := http.NewServeMux()
	mux.HandleFunc("/api/shell/bootstrap", handler.HandleBootstrap)
	mux.HandleFunc("/api/ws", handler.HandleWS)
	mux.HandleFunc("/api/", handler.HandleAPI)

	proxyServer := httptest.NewServer(mux)
	t.Cleanup(func() { proxyServer.Close() })

	return proxyServer, priv
}

// wsDialWithCookie dials the proxy's /api/ws endpoint with a valid access
// JWT cookie. Returns the websocket connection.
func wsDialWithCookie(t *testing.T, proxyURL string, accessToken string) *websocket.Conn {
	t.Helper()

	wsURL := "ws" + strings.TrimPrefix(proxyURL, "http") + "/api/ws"
	header := http.Header{}
	header.Set("Cookie", "choir_access="+accessToken)

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		t.Fatalf("dial proxy WS: %v", err)
	}
	return conn
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
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
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
	_ = json.NewDecoder(w.Body).Decode(&raw) // decode again from already-consumed body
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

// TestHandleAPIForwardsPromptRoutes verifies that /api/prompts and
// /api/prompts/{role} are forwarded to the sandbox through the proxy
// rather than hitting the generic 404 fallback.
func TestHandleAPIForwardsPromptRoutes(t *testing.T) {
	h, priv, _ := testProxyEnv(t)
	accessToken := issueTestAccessJWT(priv, "user-123")

	paths := []string{"/api/prompts", "/api/prompts/conductor"}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.AddCookie(&http.Cookie{
				Name:  "choir_access",
				Value: accessToken,
			})
			w := httptest.NewRecorder()
			h.HandleAPI(w, req)

			// The sandbox mock doesn't handle /api/prompts, so we'll get
			// a 404 or 502 from the sandbox rather than the proxy's own
			// auth-gated 404. The key assertion is that we do NOT get the
			// proxy's 404 JSON body, meaning the request was forwarded.
			body := w.Body.String()
			if w.Code == http.StatusNotFound && strings.Contains(body, `"error":"not found"`) {
				t.Errorf("%s was NOT forwarded to sandbox; proxy returned its own 404", path)
			}
		})
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

// --- VAL-PROXY-004: Missing or invalid auth cannot open GET /api/ws ---

func TestWSDeniesMissingAuth(t *testing.T) {
	h, _, _ := testProxyEnv(t)

	// Use httptest.NewServer so we can attempt a real WS dial.
	mux := http.NewServeMux()
	mux.HandleFunc("/api/ws", h.HandleWS)
	proxyServer := httptest.NewServer(mux)
	defer proxyServer.Close()

	wsURL := "ws" + strings.TrimPrefix(proxyServer.URL, "http") + "/api/ws"
	_, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err == nil {
		t.Fatal("expected WS dial to fail without auth, but it succeeded")
	}
	// The response should be a 401, not a successful upgrade.
	if resp != nil && resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 status, got %d", resp.StatusCode)
	}
}

func TestWSDeniesInvalidAuth(t *testing.T) {
	h, _, _ := testProxyEnv(t)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/ws", h.HandleWS)
	proxyServer := httptest.NewServer(mux)
	defer proxyServer.Close()

	wsURL := "ws" + strings.TrimPrefix(proxyServer.URL, "http") + "/api/ws"
	header := http.Header{}
	header.Set("Cookie", "choir_access=this-is-not-a-jwt")

	_, resp, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err == nil {
		t.Fatal("expected WS dial to fail with invalid auth, but it succeeded")
	}
	if resp != nil && resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 status, got %d", resp.StatusCode)
	}
}

func TestWSDeniesExpiredAuth(t *testing.T) {
	h, priv, _ := testProxyEnv(t)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/ws", h.HandleWS)
	proxyServer := httptest.NewServer(mux)
	defer proxyServer.Close()

	expiredToken := issueTestAccessJWTWithTTL(priv, "user-expired", -1*time.Minute)
	wsURL := "ws" + strings.TrimPrefix(proxyServer.URL, "http") + "/api/ws"
	header := http.Header{}
	header.Set("Cookie", "choir_access="+expiredToken)

	_, resp, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err == nil {
		t.Fatal("expected WS dial to fail with expired auth, but it succeeded")
	}
	if resp != nil && resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 status, got %d", resp.StatusCode)
	}
}

func TestWSDeniesTamperedAuth(t *testing.T) {
	h, priv, _ := testProxyEnv(t)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/ws", h.HandleWS)
	proxyServer := httptest.NewServer(mux)
	defer proxyServer.Close()

	tamperedToken := issueTestAccessJWT(priv, "user-tampered") + "tamper"
	wsURL := "ws" + strings.TrimPrefix(proxyServer.URL, "http") + "/api/ws"
	header := http.Header{}
	header.Set("Cookie", "choir_access="+tamperedToken)

	_, resp, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err == nil {
		t.Fatal("expected WS dial to fail with tampered auth, but it succeeded")
	}
	if resp != nil && resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 status, got %d", resp.StatusCode)
	}
}

func TestWSDeniesNonAccessToken(t *testing.T) {
	h, priv, _ := testProxyEnv(t)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/ws", h.HandleWS)
	proxyServer := httptest.NewServer(mux)
	defer proxyServer.Close()

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

	wsURL := "ws" + strings.TrimPrefix(proxyServer.URL, "http") + "/api/ws"
	header := http.Header{}
	header.Set("Cookie", "choir_access="+refreshToken)

	_, resp, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err == nil {
		t.Fatal("expected WS dial to fail with non-access token, but it succeeded")
	}
	if resp != nil && resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 status, got %d", resp.StatusCode)
	}
}

func TestWSDeniesEmptyCookieValue(t *testing.T) {
	h, _, _ := testProxyEnv(t)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/ws", h.HandleWS)
	proxyServer := httptest.NewServer(mux)
	defer proxyServer.Close()

	wsURL := "ws" + strings.TrimPrefix(proxyServer.URL, "http") + "/api/ws"
	header := http.Header{}
	header.Set("Cookie", "choir_access=")

	_, resp, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err == nil {
		t.Fatal("expected WS dial to fail with empty cookie value, but it succeeded")
	}
	if resp != nil && resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 status, got %d", resp.StatusCode)
	}
}

// --- VAL-PROXY-003: Authenticated WS upgrade succeeds and relays frames bidirectionally ---

func TestWSAuthenticatedUpgradesAndRelays(t *testing.T) {
	proxyServer, priv := testWSProxyEnv(t)

	accessToken := issueTestAccessJWT(priv, "user-ws-relay")
	conn := wsDialWithCookie(t, proxyServer.URL, accessToken)
	defer func() { _ = conn.Close() }()

	// Read the initial connected message from the sandbox (relayed through proxy).
	var connected map[string]interface{}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if err := conn.ReadJSON(&connected); err != nil {
		t.Fatalf("read connected message: %v", err)
	}

	if connected["type"] != "connected" {
		t.Errorf("connected type: got %v, want %q", connected["type"], "connected")
	}
	if connected["sandbox_id"] != "sandbox-test" {
		t.Errorf("connected sandbox_id: got %v, want %q", connected["sandbox_id"], "sandbox-test")
	}

	// Send a message and verify it is echoed back via the proxy relay.
	msg := map[string]interface{}{
		"type":    "test",
		"payload": "hello-through-proxy",
	}
	if err := conn.WriteJSON(msg); err != nil {
		t.Fatalf("write test message: %v", err)
	}

	var echo map[string]interface{}
	if err := conn.ReadJSON(&echo); err != nil {
		t.Fatalf("read echo message: %v", err)
	}

	if echo["type"] != "echo" {
		t.Errorf("echo type: got %v, want %q", echo["type"], "echo")
	}
	if echo["payload"] != "hello-through-proxy" {
		t.Errorf("echo payload: got %v, want %q", echo["payload"], "hello-through-proxy")
	}
	if echo["sandbox_id"] != "sandbox-test" {
		t.Errorf("echo sandbox_id: got %v, want %q", echo["sandbox_id"], "sandbox-test")
	}
}

func TestWSAuthenticatedInjectsUserContext(t *testing.T) {
	proxyServer, priv := testWSProxyEnv(t)

	accessToken := issueTestAccessJWT(priv, "user-ws-context")
	conn := wsDialWithCookie(t, proxyServer.URL, accessToken)
	defer func() { _ = conn.Close() }()

	// The connected message from the sandbox should contain the proxy-injected
	// user context matching the JWT subject.
	var connected map[string]interface{}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if err := conn.ReadJSON(&connected); err != nil {
		t.Fatalf("read connected message: %v", err)
	}

	if connected["user"] != "user-ws-context" {
		t.Errorf("connected user: got %v, want %q", connected["user"], "user-ws-context")
	}

	// Send a message and check user context in the echo.
	msg := map[string]interface{}{
		"type":    "test",
		"payload": "ping",
	}
	if err := conn.WriteJSON(msg); err != nil {
		t.Fatalf("write test message: %v", err)
	}

	var echo map[string]interface{}
	if err := conn.ReadJSON(&echo); err != nil {
		t.Fatalf("read echo message: %v", err)
	}

	if echo["user"] != "user-ws-context" {
		t.Errorf("echo user: got %v, want %q", echo["user"], "user-ws-context")
	}
}

func TestWSIgnoresClientSuppliedUserContext(t *testing.T) {
	proxyServer, priv := testWSProxyEnv(t)

	accessToken := issueTestAccessJWT(priv, "user-real-identity")

	// Attempt to spoof the X-Authenticated-User header via the WS handshake.
	wsURL := "ws" + strings.TrimPrefix(proxyServer.URL, "http") + "/api/ws"
	header := http.Header{}
	header.Set("Cookie", "choir_access="+accessToken)
	header.Set("X-Authenticated-User", "spoofed-attacker-identity")

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		t.Fatalf("dial proxy WS: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// The sandbox should see the JWT-verified user, not the spoofed header.
	var connected map[string]interface{}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if err := conn.ReadJSON(&connected); err != nil {
		t.Fatalf("read connected message: %v", err)
	}

	if connected["user"] != "user-real-identity" {
		t.Errorf("spoofed identity: got %v, want %q (JWT identity)", connected["user"], "user-real-identity")
	}
}

func TestWSRelaysMultipleFramesBidirectionally(t *testing.T) {
	proxyServer, priv := testWSProxyEnv(t)

	accessToken := issueTestAccessJWT(priv, "user-multi-frame")
	conn := wsDialWithCookie(t, proxyServer.URL, accessToken)
	defer func() { _ = conn.Close() }()

	// Read the initial connected message.
	var connected map[string]interface{}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if err := conn.ReadJSON(&connected); err != nil {
		t.Fatalf("read connected message: %v", err)
	}

	// Send multiple messages and verify each echo comes back correctly.
	for i := 0; i < 5; i++ {
		msg := map[string]interface{}{
			"type":    "test",
			"payload": fmt.Sprintf("frame-%d", i),
		}
		if err := conn.WriteJSON(msg); err != nil {
			t.Fatalf("write message %d: %v", i, err)
		}

		var echo map[string]interface{}
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		if err := conn.ReadJSON(&echo); err != nil {
			t.Fatalf("read echo %d: %v", i, err)
		}

		want := fmt.Sprintf("frame-%d", i)
		if echo["payload"] != want {
			t.Errorf("echo %d payload: got %v, want %q", i, echo["payload"], want)
		}
		if echo["type"] != "echo" {
			t.Errorf("echo %d type: got %v, want %q", i, echo["type"], "echo")
		}
	}
}

func TestWSProxyPreservesSinglePublicEntrypoint(t *testing.T) {
	// Verify that /api/ws is the single public entrypoint and that the
	// proxy route registration includes it.
	proxyServer, priv := testWSProxyEnv(t)

	accessToken := issueTestAccessJWT(priv, "user-entrypoint")
	conn := wsDialWithCookie(t, proxyServer.URL, accessToken)
	defer func() { _ = conn.Close() }()

	// The connection should succeed on /api/ws.
	var connected map[string]interface{}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if err := conn.ReadJSON(&connected); err != nil {
		t.Fatalf("read connected message: %v", err)
	}

	if connected["type"] != "connected" {
		t.Errorf("connected type: got %v, want %q", connected["type"], "connected")
	}
}

func TestWSRelaysBinaryFrames(t *testing.T) {
	proxyServer, priv := testWSProxyEnv(t)

	accessToken := issueTestAccessJWT(priv, "user-binary")
	conn := wsDialWithCookie(t, proxyServer.URL, accessToken)
	defer func() { _ = conn.Close() }()

	// Read the initial connected message.
	var connected map[string]interface{}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if err := conn.ReadJSON(&connected); err != nil {
		t.Fatalf("read connected message: %v", err)
	}

	// Send a binary message and verify it's echoed back.
	binaryPayload := []byte{0x01, 0x02, 0x03, 0x04}
	if err := conn.WriteMessage(websocket.BinaryMessage, binaryPayload); err != nil {
		t.Fatalf("write binary message: %v", err)
	}

	mt, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read binary echo: %v", err)
	}

	if mt != websocket.BinaryMessage {
		t.Errorf("binary echo message type: got %d, want %d", mt, websocket.BinaryMessage)
	}

	// The sandbox echoes raw binary back.
	if len(msg) != len(binaryPayload) {
		t.Errorf("binary echo length: got %d, want %d", len(msg), len(binaryPayload))
	}
}

func TestWSClosePropagates(t *testing.T) {
	proxyServer, priv := testWSProxyEnv(t)

	accessToken := issueTestAccessJWT(priv, "user-close")
	conn := wsDialWithCookie(t, proxyServer.URL, accessToken)

	// Read the initial connected message.
	var connected map[string]interface{}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if err := conn.ReadJSON(&connected); err != nil {
		_ = conn.Close()
		t.Fatalf("read connected message: %v", err)
	}

	// Client closes the connection with a normal close message.
	if err := conn.WriteMessage(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")); err != nil {
		_ = conn.Close()
		t.Fatalf("write close message: %v", err)
	}

	// Subsequent reads should indicate the connection is closed.
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, _, err := conn.ReadMessage()
	_ = conn.Close()
	if err == nil {
		t.Error("expected error reading after close, but got none")
	}
}

// testWSDeniesAuthWithHTTPCheck verifies that a plain HTTP request to the WS
// endpoint without valid auth returns 401 JSON without upgrading.
func TestWSAuthDenialReturnsJSONWithoutUpgrade(t *testing.T) {
	h, _, _ := testProxyEnv(t)

	req := httptest.NewRequest(http.MethodGet, "/api/ws", nil)
	// Add WebSocket upgrade headers to simulate a WS handshake attempt.
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")

	w := httptest.NewRecorder()
	h.HandleWS(w, req)

	// Auth denial should return 401 before any upgrade happens.
	if w.Code != http.StatusUnauthorized {
		t.Errorf("WS auth denial: got status %d, want %d", w.Code, http.StatusUnauthorized)
	}

	// Response should be JSON, not a WS upgrade.
	ct := w.Header().Get("Content-Type")
	if ct == "" {
		t.Error("Content-Type header is missing on WS auth failure")
	}

	// Verify the response body is a JSON error, not a WS frame.
	var resp errorResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode WS auth error response: %v", err)
	}
	if resp.Error == "" {
		t.Error("expected non-empty error message for WS auth denial")
	}

	// The Upgrade header should NOT be set (no WS upgrade occurred).
	if upgrade := w.Header().Get("Upgrade"); upgrade == "websocket" {
		t.Error("Upgrade header should not be set on auth denial")
	}
}

// --- VAL-PROXY-005: Spoofed identity headers, same sandbox, distinct user context ---

func TestBootstrapTwoDistinctUsersSameSandboxDifferentContext(t *testing.T) {
	h, priv, _ := testProxyEnv(t)

	// User A requests bootstrap.
	accessTokenA := issueTestAccessJWT(priv, "user-alice")
	reqA := httptest.NewRequest(http.MethodGet, "/api/shell/bootstrap", nil)
	reqA.AddCookie(&http.Cookie{Name: "choir_access", Value: accessTokenA})
	wA := httptest.NewRecorder()
	h.HandleBootstrap(wA, reqA)

	if wA.Code != http.StatusOK {
		t.Fatalf("user A: got status %d, want %d", wA.Code, http.StatusOK)
	}

	var respA map[string]interface{}
	if err := json.NewDecoder(wA.Body).Decode(&respA); err != nil {
		t.Fatalf("decode user A bootstrap: %v", err)
	}

	// User B requests bootstrap.
	accessTokenB := issueTestAccessJWT(priv, "user-bob")
	reqB := httptest.NewRequest(http.MethodGet, "/api/shell/bootstrap", nil)
	reqB.AddCookie(&http.Cookie{Name: "choir_access", Value: accessTokenB})
	wB := httptest.NewRecorder()
	h.HandleBootstrap(wB, reqB)

	if wB.Code != http.StatusOK {
		t.Fatalf("user B: got status %d, want %d", wB.Code, http.StatusOK)
	}

	var respB map[string]interface{}
	if err := json.NewDecoder(wB.Body).Decode(&respB); err != nil {
		t.Fatalf("decode user B bootstrap: %v", err)
	}

	// Both users must reach the same sandbox instance.
	if respA["sandbox_id"] != respB["sandbox_id"] {
		t.Errorf("sandbox identity mismatch: user A saw %v, user B saw %v", respA["sandbox_id"], respB["sandbox_id"])
	}

	// Each user must see their own authenticated context.
	if respA["user"] != "user-alice" {
		t.Errorf("user A context: got %v, want %q", respA["user"], "user-alice")
	}
	if respB["user"] != "user-bob" {
		t.Errorf("user B context: got %v, want %q", respB["user"], "user-bob")
	}

	// The contexts must be distinct.
	if respA["user"] == respB["user"] {
		t.Errorf("user A and user B should have different context, both got %v", respA["user"])
	}
}

func TestBootstrapNoStaleIdentityLeakBetweenUsers(t *testing.T) {
	h, priv, _ := testProxyEnv(t)

	// User A requests bootstrap.
	accessTokenA := issueTestAccessJWT(priv, "user-alice")
	reqA := httptest.NewRequest(http.MethodGet, "/api/shell/bootstrap", nil)
	reqA.AddCookie(&http.Cookie{Name: "choir_access", Value: accessTokenA})
	wA := httptest.NewRecorder()
	h.HandleBootstrap(wA, reqA)

	var respA map[string]interface{}
	if err := json.NewDecoder(wA.Body).Decode(&respA); err != nil {
		t.Fatalf("decode user A: %v", err)
	}

	// Immediately after, user B requests bootstrap on the same handler.
	accessTokenB := issueTestAccessJWT(priv, "user-bob")
	reqB := httptest.NewRequest(http.MethodGet, "/api/shell/bootstrap", nil)
	reqB.AddCookie(&http.Cookie{Name: "choir_access", Value: accessTokenB})
	wB := httptest.NewRecorder()
	h.HandleBootstrap(wB, reqB)

	var respB map[string]interface{}
	if err := json.NewDecoder(wB.Body).Decode(&respB); err != nil {
		t.Fatalf("decode user B: %v", err)
	}

	// User B must NOT see user A's identity.
	if respB["user"] != "user-bob" {
		t.Errorf("user B should see own identity, got %v (possible leak from user A %v)", respB["user"], respA["user"])
	}
}

func TestBootstrapStripsAdditionalSpoofedIdentityHeaders(t *testing.T) {
	// Verify that the proxy strips common identity-spoofing headers
	// beyond just X-Authenticated-User.
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	// Create a sandbox that echoes all received identity headers.
	sandboxMux := http.NewServeMux()
	sandboxMux.HandleFunc("/api/shell/bootstrap", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"sandbox_id":          "sandbox-test",
			"user":                r.Header.Get("X-Authenticated-User"),
			"x_user_id":          r.Header.Get("X-User-Id"),
			"x_forwarded_user":   r.Header.Get("X-Forwarded-User"),
			"x_remote_user":     r.Header.Get("X-Remote-User"),
			"x_auth_user":       r.Header.Get("X-Auth-User"),
			"x_user_name":       r.Header.Get("X-User-Name"),
			"path":              r.URL.Path,
		})
	})
	sandboxServer := httptest.NewServer(sandboxMux)
	defer sandboxServer.Close()

	cfg := &Config{Port: "0", SandboxURL: sandboxServer.URL, AuthPublicKeyPath: "/unused"}
	handler, err := NewHandler(cfg, pub)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	accessToken := issueTestAccessJWT(priv, "user-real-identity")

	req := httptest.NewRequest(http.MethodGet, "/api/shell/bootstrap", nil)
	req.AddCookie(&http.Cookie{Name: "choir_access", Value: accessToken})
	// Spoof multiple identity headers.
	req.Header.Set("X-Authenticated-User", "spoofed-auth-user")
	req.Header.Set("X-User-Id", "spoofed-user-id")
	req.Header.Set("X-Forwarded-User", "spoofed-forwarded-user")
	req.Header.Set("X-Remote-User", "spoofed-remote-user")
	req.Header.Set("X-Auth-User", "spoofed-auth-user-header")
	req.Header.Set("X-User-Name", "spoofed-user-name")

	w := httptest.NewRecorder()
	handler.HandleBootstrap(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("got status %d, want %d", w.Code, http.StatusOK)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	// The trusted X-Authenticated-User must be the JWT-verified identity.
	if resp["user"] != "user-real-identity" {
		t.Errorf("X-Authenticated-User: got %v, want %q", resp["user"], "user-real-identity")
	}

	// All other identity headers must be stripped — not forwarded to sandbox.
	for _, header := range []string{"x_user_id", "x_forwarded_user", "x_remote_user", "x_auth_user", "x_user_name"} {
		if resp[header] != "" {
			t.Errorf("spoofed header %q leaked to sandbox: got %v", header, resp[header])
		}
	}
}

func TestWSAuthenticatedTwoDistinctUsersSameSandboxDifferentContext(t *testing.T) {
	proxyServer, priv := testWSProxyEnv(t)

	// User A connects via WS.
	accessTokenA := issueTestAccessJWT(priv, "user-ws-alice")
	connA := wsDialWithCookie(t, proxyServer.URL, accessTokenA)
	defer func() { _ = connA.Close() }()

	var connectedA map[string]interface{}
	_ = connA.SetReadDeadline(time.Now().Add(3 * time.Second))
	if err := connA.ReadJSON(&connectedA); err != nil {
		t.Fatalf("user A: read connected: %v", err)
	}

	// User B connects via WS.
	accessTokenB := issueTestAccessJWT(priv, "user-ws-bob")
	connB := wsDialWithCookie(t, proxyServer.URL, accessTokenB)
	defer func() { _ = connB.Close() }()

	var connectedB map[string]interface{}
	_ = connB.SetReadDeadline(time.Now().Add(3 * time.Second))
	if err := connB.ReadJSON(&connectedB); err != nil {
		t.Fatalf("user B: read connected: %v", err)
	}

	// Both users must reach the same sandbox instance.
	if connectedA["sandbox_id"] != connectedB["sandbox_id"] {
		t.Errorf("sandbox identity mismatch: user A saw %v, user B saw %v", connectedA["sandbox_id"], connectedB["sandbox_id"])
	}

	// Each user must see their own authenticated context.
	if connectedA["user"] != "user-ws-alice" {
		t.Errorf("user A context: got %v, want %q", connectedA["user"], "user-ws-alice")
	}
	if connectedB["user"] != "user-ws-bob" {
		t.Errorf("user B context: got %v, want %q", connectedB["user"], "user-ws-bob")
	}

	// The contexts must be distinct.
	if connectedA["user"] == connectedB["user"] {
		t.Errorf("user A and user B should have different context, both got %v", connectedA["user"])
	}
}

func TestWSNoStaleIdentityLeakBetweenUsers(t *testing.T) {
	proxyServer, priv := testWSProxyEnv(t)

	// User A connects and receives initial context.
	accessTokenA := issueTestAccessJWT(priv, "user-ws-first")
	connA := wsDialWithCookie(t, proxyServer.URL, accessTokenA)

	var connectedA map[string]interface{}
	_ = connA.SetReadDeadline(time.Now().Add(3 * time.Second))
	if err := connA.ReadJSON(&connectedA); err != nil {
		_ = connA.Close()
		t.Fatalf("user A: read connected: %v", err)
	}

	// Close user A's connection.
	_ = connA.Close()

	// User B connects on the same proxy.
	accessTokenB := issueTestAccessJWT(priv, "user-ws-second")
	connB := wsDialWithCookie(t, proxyServer.URL, accessTokenB)
	defer func() { _ = connB.Close() }()

	var connectedB map[string]interface{}
	_ = connB.SetReadDeadline(time.Now().Add(3 * time.Second))
	if err := connB.ReadJSON(&connectedB); err != nil {
		t.Fatalf("user B: read connected: %v", err)
	}

	// User B must NOT see user A's identity.
	if connectedB["user"] != "user-ws-second" {
		t.Errorf("user B should see own identity, got %v (possible leak from user A %v)", connectedB["user"], connectedA["user"])
	}
}

func TestWSSpoofedIdentityHeadersDoNotReachSandbox(t *testing.T) {
	// Create a sandbox that echoes all received identity headers over WS.
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	sandboxMux := http.NewServeMux()
	sandboxMux.HandleFunc("/api/ws", func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		// Echo all identity headers we received.
		connected := map[string]interface{}{
			"sandbox_id":         "sandbox-test",
			"user":               r.Header.Get("X-Authenticated-User"),
			"x_user_id":         r.Header.Get("X-User-Id"),
			"x_forwarded_user":  r.Header.Get("X-Forwarded-User"),
			"x_remote_user":     r.Header.Get("X-Remote-User"),
			"type":              "connected",
		}
		if err := conn.WriteJSON(connected); err != nil {
			return
		}
	})
	sandboxServer := httptest.NewServer(sandboxMux)
	defer sandboxServer.Close()

	cfg := &Config{Port: "0", SandboxURL: sandboxServer.URL, AuthPublicKeyPath: "/unused"}
	handler, err := NewHandler(cfg, pub)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/ws", handler.HandleWS)
	proxyServer := httptest.NewServer(mux)
	defer proxyServer.Close()

	accessToken := issueTestAccessJWT(priv, "user-real-ws")

	// Dial with spoofed identity headers on the handshake.
	wsURL := "ws" + strings.TrimPrefix(proxyServer.URL, "http") + "/api/ws"
	header := http.Header{}
	header.Set("Cookie", "choir_access="+accessToken)
	header.Set("X-Authenticated-User", "spoofed-auth-user")
	header.Set("X-User-Id", "spoofed-user-id")
	header.Set("X-Forwarded-User", "spoofed-forwarded")
	header.Set("X-Remote-User", "spoofed-remote")

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		t.Fatalf("dial proxy WS: %v", err)
	}
	defer func() { _ = conn.Close() }()

	var connected map[string]interface{}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if err := conn.ReadJSON(&connected); err != nil {
		t.Fatalf("read connected: %v", err)
	}

	// Trusted identity must match JWT.
	if connected["user"] != "user-real-ws" {
		t.Errorf("X-Authenticated-User: got %v, want %q", connected["user"], "user-real-ws")
	}

	// All other identity headers must NOT reach the sandbox.
	for _, header := range []string{"x_user_id", "x_forwarded_user", "x_remote_user"} {
		if connected[header] != "" {
			t.Errorf("spoofed header %q leaked to sandbox via WS: got %v", header, connected[header])
		}
	}
}

func TestWSDeniesWrongSigningKey(t *testing.T) {
	h, _, _ := testProxyEnv(t)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/ws", h.HandleWS)
	proxyServer := httptest.NewServer(mux)
	defer proxyServer.Close()

	// Generate a different key pair.
	_, wrongPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate wrong key: %v", err)
	}

	// Sign a JWT with the wrong key.
	wrongToken := issueTestAccessJWT(wrongPriv, "user-attacker")

	wsURL := "ws" + strings.TrimPrefix(proxyServer.URL, "http") + "/api/ws"
	header := http.Header{}
	header.Set("Cookie", "choir_access="+wrongToken)

	_, resp, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err == nil {
		t.Fatal("expected WS dial to fail with wrong signing key, but it succeeded")
	}
	if resp != nil && resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 status, got %d", resp.StatusCode)
	}
}

// ======================================================================
// VAL-DEPLOY-005: Protected shell routes fail closed when signed out
// ======================================================================

// TestProtectedBootstrapDeniesSignedOut verifies that the shell bootstrap
// route returns a machine-readable 401 JSON denial to signed-out callers
// and does not expose any sandbox payload.
//
// VAL-DEPLOY-005: "Protected shell routes deny signed-out callers before
// shell data or live state are exposed"
func TestProtectedBootstrapDeniesSignedOut(t *testing.T) {
	h, _, _ := testProxyEnv(t)

	req := httptest.NewRequest(http.MethodGet, "/api/shell/bootstrap", nil)
	w := httptest.NewRecorder()
	h.HandleBootstrap(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("signed-out bootstrap: got status %d, want %d", w.Code, http.StatusUnauthorized)
	}

	// Response must be machine-readable JSON.
	ct := w.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("Content-Type: got %q, want %q", ct, "application/json")
	}

	var resp errorResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode denial: %v", err)
	}
	if resp.Error == "" {
		t.Error("denial should have a non-empty error message")
	}

	// Must not contain sandbox payload data.
	body := w.Body.String()
	for _, field := range []string{"sandbox_id", "bootstrap", "user"} {
		if strings.Contains(body, field) {
			t.Errorf("denial response should not contain sandbox field %q", field)
		}
	}
}

// TestProtectedLiveChannelDeniesSignedOut verifies that the live channel
// route returns a machine-readable 401 JSON denial to signed-out callers
// without upgrading the connection.
//
// VAL-DEPLOY-005: "Protected shell routes deny signed-out callers before
// shell data or live state are exposed"
func TestProtectedLiveChannelDeniesSignedOut(t *testing.T) {
	h, _, _ := testProxyEnv(t)

	req := httptest.NewRequest(http.MethodGet, "/api/ws", nil)
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")

	w := httptest.NewRecorder()
	h.HandleWS(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("signed-out WS: got status %d, want %d", w.Code, http.StatusUnauthorized)
	}

	// No WS upgrade should occur.
	if upgrade := w.Header().Get("Upgrade"); upgrade == "websocket" {
		t.Error("Upgrade header should not be set on auth denial — no WS upgrade")
	}

	// Response must be machine-readable JSON.
	var resp errorResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode WS denial: %v", err)
	}
	if resp.Error == "" {
		t.Error("WS denial should have a non-empty error message")
	}
}

// TestAllAPIRoutesDenySignedOutCallers verifies that every /api/* route
// denies signed-out callers with 401. This covers both explicitly-handled
// protected routes (bootstrap, ws) and the default catch-all for unknown
// /api/* paths. No /api/* route should ever return 200 or expose data
// without valid auth.
//
// VAL-DEPLOY-005: "Protected shell routes deny signed-out callers before
// shell data or live state are exposed"
func TestAllAPIRoutesDenySignedOutCallers(t *testing.T) {
	h, _, _ := testProxyEnv(t)

	// Test a variety of /api/* paths — both known protected routes and
	// unknown future routes. All must deny without auth.
	paths := []struct {
		path       string
		method     string
		wantStatus int
	}{
		{"/api/shell/bootstrap", http.MethodGet, http.StatusUnauthorized},
		{"/api/ws", http.MethodGet, http.StatusUnauthorized},
		{"/api/agent/loop", http.MethodPost, http.StatusUnauthorized},
		{"/api/agent/status", http.MethodGet, http.StatusUnauthorized},
		{"/api/events", http.MethodGet, http.StatusUnauthorized},
		{"/api/unknown", http.MethodGet, http.StatusUnauthorized},
		{"/api/shell/some-future-route", http.MethodGet, http.StatusUnauthorized},
	}

	for _, tt := range paths {
		t.Run(tt.path, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			w := httptest.NewRecorder()
			h.HandleAPI(w, req)

			if w.Code != tt.wantStatus {
				t.Errorf("signed-out %s %s: got status %d, want %d", tt.method, tt.path, w.Code, tt.wantStatus)
			}

			// All denials must be machine-readable JSON.
			ct := w.Header().Get("Content-Type")
			if ct != "application/json" {
				t.Errorf("Content-Type: got %q, want %q", ct, "application/json")
			}

			var resp errorResponse
			if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
				t.Fatalf("decode denial for %s: %v", tt.path, err)
			}
			if resp.Error == "" {
				t.Errorf("denial for %s should have a non-empty error message", tt.path)
			}

			// Must not contain sandbox data.
			body := w.Body.String()
			if strings.Contains(body, "sandbox_id") {
				t.Errorf("denial for %s should not contain sandbox_id", tt.path)
			}
		})
	}
}

// TestUnknownAPIRouteReturns404ForAuthenticatedCaller verifies that unknown
// /api/* routes return 404 (not found) for authenticated callers, confirming
// that the proxy doesn't blindly forward everything to the sandbox.
func TestUnknownAPIRouteReturns404ForAuthenticatedCaller(t *testing.T) {
	h, priv, _ := testProxyEnv(t)

	accessToken := issueTestAccessJWT(priv, "user-authenticated")

	req := httptest.NewRequest(http.MethodGet, "/api/nonexistent/route", nil)
	req.AddCookie(&http.Cookie{Name: "choir_access", Value: accessToken})
	w := httptest.NewRecorder()
	h.HandleAPI(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("authenticated unknown route: got status %d, want %d", w.Code, http.StatusNotFound)
	}

	var resp errorResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error == "" {
		t.Error("404 response should have a non-empty error message")
	}
}

func TestAuthenticatedVTextRouteIsForwarded(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate ed25519 key: %v", err)
	}

	gotUser := ""
	gotPath := ""
	sandboxMux := http.NewServeMux()
	sandboxMux.HandleFunc("/api/vtext/documents", func(w http.ResponseWriter, r *http.Request) {
		gotUser = r.Header.Get("X-Authenticated-User")
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"documents": []any{},
		})
	})
	sandbox := httptest.NewServer(sandboxMux)
	t.Cleanup(func() { sandbox.Close() })

	cfg := &Config{
		Port:              "0",
		SandboxURL:        sandbox.URL,
		AuthPublicKeyPath: "/unused/in/test",
	}
	h, err := NewHandler(cfg, pub)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	accessToken := issueTestAccessJWT(priv, "user-authenticated")

	req := httptest.NewRequest(http.MethodGet, "/api/vtext/documents", nil)
	req.AddCookie(&http.Cookie{Name: "choir_access", Value: accessToken})
	w := httptest.NewRecorder()
	h.HandleAPI(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", w.Code, http.StatusOK)
	}

	if gotUser != "user-authenticated" {
		t.Fatalf("forwarded X-Authenticated-User: got %q, want %q", gotUser, "user-authenticated")
	}
	if gotPath != "/api/vtext/documents" {
		t.Fatalf("forwarded path: got %q, want %q", gotPath, "/api/vtext/documents")
	}
}

func TestAuthenticatedTestRouteIsForwarded(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate ed25519 key: %v", err)
	}

	gotUser := ""
	gotPath := ""
	sandboxMux := http.NewServeMux()
	sandboxMux.HandleFunc("/api/test/vtext/research-findings", func(w http.ResponseWriter, r *http.Request) {
		gotUser = r.Header.Get("X-Authenticated-User")
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "submitted",
		})
	})
	sandbox := httptest.NewServer(sandboxMux)
	t.Cleanup(func() { sandbox.Close() })

	cfg := &Config{
		Port:              "0",
		SandboxURL:        sandbox.URL,
		AuthPublicKeyPath: "/unused/in/test",
	}
	h, err := NewHandler(cfg, pub)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	accessToken := issueTestAccessJWT(priv, "user-authenticated")

	req := httptest.NewRequest(http.MethodPost, "/api/test/vtext/research-findings", strings.NewReader(`{"doc_id":"doc-1","finding_id":"finding-1"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "choir_access", Value: accessToken})
	w := httptest.NewRecorder()
	h.HandleAPI(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", w.Code, http.StatusOK)
	}

	if gotUser != "user-authenticated" {
		t.Fatalf("forwarded X-Authenticated-User: got %q, want %q", gotUser, "user-authenticated")
	}
	if gotPath != "/api/test/vtext/research-findings" {
		t.Fatalf("forwarded path: got %q, want %q", gotPath, "/api/test/vtext/research-findings")
	}
}

// TestSignedOutCallersNeverSeeSandboxData is a comprehensive test verifying
// that no proxy response to a signed-out caller ever contains sandbox-origin
// data (sandbox_id, bootstrap payloads, user context from the upstream).
//
// VAL-DEPLOY-005: "Protected shell routes deny signed-out callers before
// shell data or live state are exposed"
func TestSignedOutCallersNeverSeeSandboxData(t *testing.T) {
	h, _, _ := testProxyEnv(t)

	paths := []string{
		"/api/shell/bootstrap",
		"/api/ws",
		"/api/agent/loop",
		"/api/anything",
	}

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			w := httptest.NewRecorder()
			h.HandleAPI(w, req)

			body := w.Body.String()

			// Must not contain any sandbox-origin data.
			for _, field := range []string{"sandbox_id", "placeholder-shell", "websocket channel"} {
				if strings.Contains(body, field) {
					t.Errorf("signed-out response for %s contains sandbox data field %q", path, field)
				}
			}

			// Response must be a denial (401 or 404), never 200.
			if w.Code == http.StatusOK {
				t.Errorf("signed-out caller got 200 for %s — this is a fail-open bug", path)
			}
		})
	}
}

// ======================================================================
// VAL-DEPLOY-008 / VAL-CROSS-118: Proxy health and restart readiness
// ======================================================================

// TestProxyHealthReportsOkWhenUpstreamIsHealthy verifies that the proxy
// /health endpoint returns "ok" status with "ok" upstream when the
// sandbox backend is reachable.
//
// VAL-DEPLOY-008: "protected-request backend health is observable"
func TestProxyHealthReportsOkWhenUpstreamIsHealthy(t *testing.T) {
	h, _, _ := testProxyEnv(t)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	h.HandleHealth(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("health endpoint: got status %d, want %d", w.Code, http.StatusOK)
	}

	var resp proxyHealthResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode health response: %v", err)
	}

	if resp.Status != "ok" {
		t.Errorf("status: got %q, want %q", resp.Status, "ok")
	}
	if resp.Service != "proxy" {
		t.Errorf("service: got %q, want %q", resp.Service, "proxy")
	}
	if resp.Upstream != "ok" {
		t.Errorf("upstream: got %q, want %q", resp.Upstream, "ok")
	}
}

// TestProxyHealthReportsDegradedWhenUpstreamIsUnreachable verifies that
// the proxy /health endpoint returns "degraded" status with "unreachable"
// upstream when the sandbox backend is not available. This makes it
// possible for operators and monitoring to distinguish between a healthy
// proxy and a degraded proxy whose backend is down.
//
// VAL-DEPLOY-008: "protected-request backend health is observable and restartable"
func TestProxyHealthReportsDegradedWhenUpstreamIsUnreachable(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	// Create a sandbox server that we can close to simulate unreachability.
	sandboxMux := http.NewServeMux()
	sandboxMux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "service": "sandbox"})
	})
	sandboxServer := httptest.NewServer(sandboxMux)

	cfg := &Config{Port: "0", SandboxURL: sandboxServer.URL, AuthPublicKeyPath: "/unused"}
	handler, err := NewHandler(cfg, pub)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	// Verify health is ok while sandbox is up.
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	handler.HandleHealth(w, req)

	var respOk proxyHealthResponse
	if err := json.NewDecoder(w.Body).Decode(&respOk); err != nil {
		t.Fatalf("decode healthy response: %v", err)
	}
	if respOk.Status != "ok" {
		t.Errorf("status before shutdown: got %q, want %q", respOk.Status, "ok")
	}
	if respOk.Upstream != "ok" {
		t.Errorf("upstream before shutdown: got %q, want %q", respOk.Upstream, "ok")
	}

	// Shut down the sandbox to simulate an upstream failure.
	sandboxServer.Close()

	// Wait briefly for connections to drain.
	time.Sleep(100 * time.Millisecond)

	// Verify health reports degraded when upstream is unreachable.
	req2 := httptest.NewRequest(http.MethodGet, "/health", nil)
	w2 := httptest.NewRecorder()
	handler.HandleHealth(w2, req2)

	var respDegraded proxyHealthResponse
	if err := json.NewDecoder(w2.Body).Decode(&respDegraded); err != nil {
		t.Fatalf("decode degraded response: %v", err)
	}
	if respDegraded.Status != "degraded" {
		t.Errorf("status after shutdown: got %q, want %q", respDegraded.Status, "degraded")
	}
	if respDegraded.Upstream != "unreachable" {
		t.Errorf("upstream after shutdown: got %q, want %q", respDegraded.Upstream, "unreachable")
	}
}

// TestProxyHealthReportsDegradedWithNoUpstreamAtStartup verifies that
// the proxy health endpoint correctly reports "degraded" when started
// with an unreachable sandbox URL (e.g., sandbox hasn't started yet).
//
// VAL-DEPLOY-008: "protected-request backend health is observable"
func TestProxyHealthReportsDegradedWithNoUpstreamAtStartup(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	// Point proxy at a non-existent upstream.
	cfg := &Config{Port: "0", SandboxURL: "http://127.0.0.1:1", AuthPublicKeyPath: "/unused"}
	handler, err := NewHandler(cfg, pub)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	handler.HandleHealth(w, req)

	var resp proxyHealthResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode health response: %v", err)
	}

	if resp.Status != "degraded" {
		t.Errorf("status with no upstream: got %q, want %q", resp.Status, "degraded")
	}
	if resp.Upstream != "unreachable" {
		t.Errorf("upstream with no upstream: got %q, want %q", resp.Upstream, "unreachable")
	}
}

// TestProxyHealthRejectsNonGet verifies that the health endpoint only
// accepts GET requests.
func TestProxyHealthRejectsNonGet(t *testing.T) {
	h, _, _ := testProxyEnv(t)

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/health", nil)
			w := httptest.NewRecorder()
			h.HandleHealth(w, req)

			if w.Code != http.StatusMethodNotAllowed {
				t.Errorf("health %s: got status %d, want %d", method, w.Code, http.StatusMethodNotAllowed)
			}
		})
	}
}

// TestProxyHealthRecoversAfterUpstreamRestart verifies that the proxy
// health endpoint transitions from "degraded" back to "ok" when the
// upstream sandbox recovers. This simulates the restart recovery path
// required by VAL-DEPLOY-008 and VAL-CROSS-118.
//
// VAL-CROSS-118: "Restarting auth or proxy returns the system to healthy state"
func TestProxyHealthRecoversAfterUpstreamRestart(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	// Use a custom port that's unlikely to conflict.
	sandboxMux := http.NewServeMux()
	sandboxMux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "service": "sandbox"})
	})

	// Start the sandbox, create proxy pointing at it.
	sandboxServer := httptest.NewServer(sandboxMux)
	cfg := &Config{Port: "0", SandboxURL: sandboxServer.URL, AuthPublicKeyPath: "/unused"}
	handler, err := NewHandler(cfg, pub)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	// Verify health is ok.
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	handler.HandleHealth(w, req)
	var resp1 proxyHealthResponse
	_ = json.NewDecoder(w.Body).Decode(&resp1)
	if resp1.Status != "ok" {
		t.Fatalf("initial status: got %q, want %q", resp1.Status, "ok")
	}

	// Stop the sandbox (simulate crash).
	sandboxServer.Close()
	time.Sleep(100 * time.Millisecond)

	// Verify health reports degraded.
	req2 := httptest.NewRequest(http.MethodGet, "/health", nil)
	w2 := httptest.NewRecorder()
	handler.HandleHealth(w2, req2)
	var resp2 proxyHealthResponse
	_ = json.NewDecoder(w2.Body).Decode(&resp2)
	if resp2.Status != "degraded" {
		t.Fatalf("degraded status: got %q, want %q", resp2.Status, "degraded")
	}

	// "Restart" the sandbox on the same address by creating a new test server.
	// Since httptest.Server uses random ports, we need a different approach:
	// create a new sandbox server and update the handler's config to point to it.
	newSandboxMux := http.NewServeMux()
	newSandboxMux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "service": "sandbox"})
	})
	newSandboxServer := httptest.NewServer(newSandboxMux)
	defer newSandboxServer.Close()

	// Re-create the handler pointing to the new sandbox.
	newSandboxURL, _ := url.Parse(newSandboxServer.URL)
	proxy := httputil.NewSingleHostReverseProxy(newSandboxURL)
	handler2, err := NewHandler(&Config{
		Port:              "0",
		SandboxURL:        newSandboxServer.URL,
		AuthPublicKeyPath: "/unused",
	}, pub)
	_ = proxy
	if err != nil {
		t.Fatalf("NewHandler for restart: %v", err)
	}

	// Verify health recovers to ok.
	req3 := httptest.NewRequest(http.MethodGet, "/health", nil)
	w3 := httptest.NewRecorder()
	handler2.HandleHealth(w3, req3)
	var resp3 proxyHealthResponse
	_ = json.NewDecoder(w3.Body).Decode(&resp3)
	if resp3.Status != "ok" {
		t.Fatalf("recovered status: got %q, want %q", resp3.Status, "ok")
	}
	if resp3.Upstream != "ok" {
		t.Fatalf("recovered upstream: got %q, want %q", resp3.Upstream, "ok")
	}
}

// TestProviderRoutesDenied verifies that the proxy denies all browser access
// to /provider/* routes (VAL-GATEWAY-002). Browser callers must never use
// /provider/* as a raw inference bypass around the runtime/proxy boundary.
func TestProviderRoutesDenied(t *testing.T) {
	handler, _, _ := testProxyEnv(t)

	providerPaths := []string{
		"/provider/v1/inference",
		"/provider/v1/credentials/issue",
		"/provider/v1/credentials/revoke",
		"/provider/v1/credentials/rotate",
		"/provider/anything",
	}

	for _, path := range providerPaths {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"messages":[]}`))
			req.Header.Set("Content-Type", "application/json")

			w := httptest.NewRecorder()
			handler.HandleProviderDeny(w, req)

			if w.Code != http.StatusForbidden {
				t.Errorf("status = %d, want %d for path %s", w.Code, http.StatusForbidden, path)
			}

			var errResp errorResponse
			if err := json.NewDecoder(w.Body).Decode(&errResp); err != nil {
				t.Fatalf("decode error response: %v", err)
			}
			if !strings.Contains(errResp.Error, "provider routes") {
				t.Errorf("error = %q, want provider routes denial message", errResp.Error)
			}
		})
	}
}

// TestProviderRouteDeniedWithAuth verifies that even authenticated users
// cannot access /provider/* routes through the proxy (VAL-GATEWAY-002).
func TestProviderRouteDeniedWithAuth(t *testing.T) {
	handler, priv, _ := testProxyEnv(t)

	// Create a valid access JWT.
	token := issueTestAccessJWTWithTTL(priv, "user-1", 5*time.Minute)
	req := httptest.NewRequest(http.MethodPost, "/provider/v1/inference", strings.NewReader(`{"messages":[]}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "choir_access", Value: token})

	w := httptest.NewRecorder()
	handler.HandleProviderDeny(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d (even with auth)", w.Code, http.StatusForbidden)
	}
}

// --- VAL-VM-001, VAL-VM-002: vmctl-backed routing tests ---

// testVMctlProxyEnv sets up a proxy Handler with a vmctl service backend,
// a fake sandbox backend, and Ed25519 key material. Returns the handler,
// signing key, sandbox server, and vmctl test server.
func testVMctlProxyEnv(t *testing.T) (*Handler, ed25519.PrivateKey, *httptest.Server, *httptest.Server) {
	t.Helper()

	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate ed25519 key: %v", err)
	}

	// Create a fake sandbox backend.
	sandboxMux := http.NewServeMux()
	sandboxMux.HandleFunc("/api/shell/bootstrap", func(w http.ResponseWriter, r *http.Request) {
		user := r.Header.Get("X-Authenticated-User")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"sandbox_id":  "sandbox-vmctl-test",
			"user":        user,
			"bootstrap":   "vm-routed",
			"path":        r.URL.Path,
		})
	})
	sandboxMux.HandleFunc("/api/agent/loop", func(w http.ResponseWriter, r *http.Request) {
		user := r.Header.Get("X-Authenticated-User")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"loop_id":  "task-123",
			"owner_id": user,
			"state":    "accepted",
		})
	})
	sandboxMux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	sandboxServer := httptest.NewServer(sandboxMux)
	t.Cleanup(func() { sandboxServer.Close() })

	// Create a vmctl service.
	reg := vmctl.NewOwnershipRegistry(sandboxServer.URL)
	vmctlHandler := vmctl.NewHandler(reg)

	vmctlMux := http.NewServeMux()
	vmctlMux.HandleFunc("/internal/vmctl/resolve", vmctlHandler.HandleResolve)
	vmctlMux.HandleFunc("/internal/vmctl/fork-desktop", vmctlHandler.HandleForkDesktop)
	vmctlMux.HandleFunc("/internal/vmctl/publish-desktop", vmctlHandler.HandlePublishDesktop)
	vmctlMux.HandleFunc("/internal/vmctl/lookup", vmctlHandler.HandleLookup)
	vmctlMux.HandleFunc("/internal/vmctl/list", vmctlHandler.HandleList)

	vmctlServer := httptest.NewServer(vmctlMux)
	t.Cleanup(func() { vmctlServer.Close() })

	// Create proxy config with vmctl routing enabled.
	cfg := &Config{
		Port:              "0",
		SandboxURL:        sandboxServer.URL,
		AuthPublicKeyPath: "/unused/in/test",
		VmctlURL:          vmctlServer.URL,
	}

	if !cfg.VmctlRoutingEnabled() {
		t.Fatal("expected vmctl routing to be enabled")
	}

	handler, err := NewHandler(cfg, pub)
	if err != nil {
		t.Fatalf("NewHandler with vmctl: %v", err)
	}

	return handler, priv, sandboxServer, vmctlServer
}

// TestVMctlRouting_BootstrapRoutesThroughVM tests that protected bootstrap
// routes resolve through vmctl ownership (VAL-VM-001, VAL-VM-002).
func TestVMctlRouting_BootstrapRoutesThroughVM(t *testing.T) {
	handler, priv, _, vmctlSrv := testVMctlProxyEnv(t)
	_ = vmctlSrv

	accessToken := issueTestAccessJWT(priv, "user-vm-1")

	req := httptest.NewRequest(http.MethodGet, "/api/shell/bootstrap", nil)
	req.Header.Set("Cookie", "choir_access="+accessToken)
	w := httptest.NewRecorder()
	handler.HandleBootstrap(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var result map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("decode result: %v", err)
	}

	// The sandbox should have received the user context.
	if result["user"] != "user-vm-1" {
		t.Errorf("expected user user-vm-1, got %v", result["user"])
	}
	if result["bootstrap"] != "vm-routed" {
		t.Errorf("expected vm-routed bootstrap, got %v", result["bootstrap"])
	}
}

// TestVMctlRouting_DifferentUsersGetDifferentVMs tests that different users
// receive distinct VMs (VAL-VM-005, VAL-CROSS-113).
func TestVMctlRouting_DifferentUsersGetDifferentVMs(t *testing.T) {
	handler, priv, _, vmctlSrv := testVMctlProxyEnv(t)
	_ = vmctlSrv

	user1Token := issueTestAccessJWT(priv, "alice")
	user2Token := issueTestAccessJWT(priv, "bob")

	// User 1 request.
	req1 := httptest.NewRequest(http.MethodGet, "/api/shell/bootstrap", nil)
	req1.Header.Set("Cookie", "choir_access="+user1Token)
	w1 := httptest.NewRecorder()
	handler.HandleBootstrap(w1, req1)

	// User 2 request.
	req2 := httptest.NewRequest(http.MethodGet, "/api/shell/bootstrap", nil)
	req2.Header.Set("Cookie", "choir_access="+user2Token)
	w2 := httptest.NewRecorder()
	handler.HandleBootstrap(w2, req2)

	if w1.Code != http.StatusOK || w2.Code != http.StatusOK {
		t.Fatalf("expected both 200, got %d and %d", w1.Code, w2.Code)
	}

	// Verify the vmctl registry created distinct VMs for each user.
	client := vmctl.NewClient(vmctlSrv.URL)
	lookup1, _ := client.Lookup("alice")
	lookup2, _ := client.Lookup("bob")

	if lookup1 == nil || lookup2 == nil {
		t.Fatal("expected both users to have VM ownership")
	}
	if lookup1.VMID == lookup2.VMID {
		t.Error("expected different VM IDs for different users (VAL-VM-005)")
	}
}

func TestVMctlRouting_SameUserDifferentDesktopsGetDifferentVMs(t *testing.T) {
	handler, priv, _, vmctlSrv := testVMctlProxyEnv(t)

	accessToken := issueTestAccessJWT(priv, "alice")

	reqA := httptest.NewRequest(http.MethodGet, "/api/shell/bootstrap?desktop_id=primary", nil)
	reqA.Header.Set("Cookie", "choir_access="+accessToken)
	wA := httptest.NewRecorder()
	handler.HandleBootstrap(wA, reqA)

	reqB := httptest.NewRequest(http.MethodGet, "/api/shell/bootstrap?desktop_id=branch-a", nil)
	reqB.Header.Set("Cookie", "choir_access="+accessToken)
	wB := httptest.NewRecorder()
	handler.HandleBootstrap(wB, reqB)

	if wA.Code != http.StatusOK || wB.Code != http.StatusOK {
		t.Fatalf("expected both 200, got %d and %d", wA.Code, wB.Code)
	}

	client := vmctl.NewClient(vmctlSrv.URL)
	primary, err := client.LookupDesktop("alice", vmctl.PrimaryDesktopID)
	if err != nil {
		t.Fatalf("LookupDesktop primary: %v", err)
	}
	branch, err := client.LookupDesktop("alice", "branch-a")
	if err != nil {
		t.Fatalf("LookupDesktop branch: %v", err)
	}
	if primary == nil || branch == nil {
		t.Fatalf("expected ownerships for both desktops, got primary=%+v branch=%+v", primary, branch)
	}
	if primary.VMID == branch.VMID {
		t.Fatalf("expected different VM IDs per desktop, got %s", primary.VMID)
	}
}

func TestVMctlRouting_UnpublishedDesktopRejected(t *testing.T) {
	handler, priv, _, vmctlSrv := testVMctlProxyEnv(t)

	client := vmctl.NewClient(vmctlSrv.URL)
	if _, err := client.ResolveDesktop("alice", vmctl.PrimaryDesktopID); err != nil {
		t.Fatalf("ResolveDesktop primary: %v", err)
	}
	if _, err := client.ForkDesktop("alice", vmctl.PrimaryDesktopID, "branch-a"); err != nil {
		t.Fatalf("ForkDesktop branch-a: %v", err)
	}

	accessToken := issueTestAccessJWT(priv, "alice")
	req := httptest.NewRequest(http.MethodGet, "/api/shell/bootstrap?desktop_id=branch-a", nil)
	req.Header.Set("Cookie", "choir_access="+accessToken)
	w := httptest.NewRecorder()
	handler.HandleBootstrap(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 for unpublished desktop, got %d", w.Code)
	}
}

// TestVMctlRouting_InvalidAuthDeniedBeforeVMSideEffects tests that invalid
// auth is denied before VM ownership changes or runtime side effects
// (VAL-CROSS-110).
func TestVMctlRouting_InvalidAuthDeniedBeforeVMSideEffects(t *testing.T) {
	handler, priv, _, vmctlSrv := testVMctlProxyEnv(t)
	_ = vmctlSrv

	// Issue a token then expire it.
	expiredToken := issueTestAccessJWTWithTTL(priv, "user-would-be", -1*time.Minute)

	req := httptest.NewRequest(http.MethodGet, "/api/shell/bootstrap", nil)
	req.Header.Set("Cookie", "choir_access="+expiredToken)
	w := httptest.NewRecorder()
	handler.HandleBootstrap(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for expired token, got %d", w.Code)
	}

	// The user should NOT have a VM assignment since auth was denied
	// before the resolve step (VAL-CROSS-110).
	client := vmctl.NewClient(vmctlSrv.URL)
	lookup, _ := client.Lookup("user-would-be")
	if lookup != nil {
		t.Error("expected no VM assignment when auth is denied (VAL-CROSS-110)")
	}
}

// TestVMctlRouting_SameUserPinnedToSameVM tests that repeated requests
// from the same user stay pinned to the same VM (VAL-VM-003).
func TestVMctlRouting_SameUserPinnedToSameVM(t *testing.T) {
	handler, priv, _, vmctlSrv := testVMctlProxyEnv(t)
	_ = vmctlSrv

	accessToken := issueTestAccessJWT(priv, "user-pinned")

	// Make two requests.
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/shell/bootstrap", nil)
		req.Header.Set("Cookie", "choir_access="+accessToken)
		w := httptest.NewRecorder()
		handler.HandleBootstrap(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("request %d: expected 200, got %d", i+1, w.Code)
		}
	}

	// Should still have exactly one VM for this user.
	client := vmctl.NewClient(vmctlSrv.URL)
	lookup, _ := client.Lookup("user-pinned")
	if lookup == nil {
		t.Fatal("expected user to have a VM")
	}
	// The VM ID should be stable.
	vmID := lookup.VMID

	// Make another request and verify the VM hasn't changed.
	req := httptest.NewRequest(http.MethodGet, "/api/shell/bootstrap", nil)
	req.Header.Set("Cookie", "choir_access="+accessToken)
	w := httptest.NewRecorder()
	handler.HandleBootstrap(w, req)

	lookup2, _ := client.Lookup("user-pinned")
	if lookup2.VMID != vmID {
		t.Errorf("expected pinned VM %s, got %s (VAL-VM-003)", vmID, lookup2.VMID)
	}
}

// TestVMctlRouting_ProtectedAPIThroughVM tests that runtime API routes also
// resolve through vmctl ownership (VAL-VM-002).
func TestVMctlRouting_ProtectedAPIThroughVM(t *testing.T) {
	handler, priv, _, vmctlSrv := testVMctlProxyEnv(t)
	_ = vmctlSrv

	accessToken := issueTestAccessJWT(priv, "user-runtime")

	req := httptest.NewRequest(http.MethodPost, "/api/agent/loop", strings.NewReader(`{"prompt":"test"}`))
	req.Header.Set("Cookie", "choir_access="+accessToken)
	w := httptest.NewRecorder()
	handler.HandleProtectedAPI(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var result map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result["owner_id"] != "user-runtime" {
		t.Errorf("expected owner_id user-runtime, got %v", result["owner_id"])
	}
}

// TestVMctlDeny_PublicVMctlBlocked tests that /internal/vmctl/* routes are
// denied to browser callers (VAL-VM-012).
func TestVMctlDeny_PublicVMctlBlocked(t *testing.T) {
	handler, _, _, _ := testVMctlProxyEnv(t)

	req := httptest.NewRequest(http.MethodPost, "/internal/vmctl/resolve", nil)
	w := httptest.NewRecorder()
	handler.HandleVMctlDeny(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", w.Code)
	}

	var result errorResponse
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if !strings.Contains(result.Error, "not publicly accessible") {
		t.Errorf("expected public denial message, got: %s", result.Error)
	}
}

// TestVMctlRouting_HealthReportsVMctlStatus tests that proxy health includes
// vmctl routing status when enabled.
func TestVMctlRouting_HealthReportsVMctlStatus(t *testing.T) {
	handler, _, _, _ := testVMctlProxyEnv(t)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	handler.HandleHealth(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var result proxyHealthResponse
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result.VMctlRouting != "enabled" {
		t.Errorf("expected vmctl_routing=enabled, got %s", result.VMctlRouting)
	}
	if result.VMctlURL == "" {
		t.Error("expected non-empty vmctl_url")
	}
}

// TestVMctlRouting_GracefulDegradation tests that when vmctl is unreachable,
// the proxy falls back to the static sandbox URL.
func TestVMctlRouting_GracefulDegradation(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	// Create a sandbox backend.
	sandboxMux := http.NewServeMux()
	sandboxMux.HandleFunc("/api/shell/bootstrap", func(w http.ResponseWriter, r *http.Request) {
		user := r.Header.Get("X-Authenticated-User")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"sandbox_id": "sandbox-fallback",
			"user":       user,
		})
	})
	sandboxMux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
	sandboxServer := httptest.NewServer(sandboxMux)
	t.Cleanup(func() { sandboxServer.Close() })

	// Create proxy pointing at an unreachable vmctl.
	cfg := &Config{
		Port:              "0",
		SandboxURL:        sandboxServer.URL,
		AuthPublicKeyPath: "/unused",
		VmctlURL:          "http://127.0.0.1:1", // unreachable port
	}

	handler, err := NewHandler(cfg, pub)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	accessToken := issueTestAccessJWT(priv, "user-fallback")

	req := httptest.NewRequest(http.MethodGet, "/api/shell/bootstrap", nil)
	req.Header.Set("Cookie", "choir_access="+accessToken)
	w := httptest.NewRecorder()
	handler.HandleBootstrap(w, req)

	// Multi-desktop routing must fail closed. Falling back to a different sandbox
	// would land the user on the wrong desktop.
	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 when vmctl is unavailable, got %d", w.Code)
	}
}

// TestConfig_VmctlRoutingEnabled tests the vmctl routing config flag.
func TestConfig_VmctlRoutingEnabled(t *testing.T) {
	cfg1 := &Config{VmctlURL: "http://localhost:8083"}
	if !cfg1.VmctlRoutingEnabled() {
		t.Error("expected vmctl routing enabled when URL is set")
	}

	cfg2 := &Config{VmctlURL: ""}
	if cfg2.VmctlRoutingEnabled() {
		t.Error("expected vmctl routing disabled when URL is empty")
	}
}
