package proxy

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
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
		json.NewEncoder(w).Encode(map[string]interface{}{
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
		defer conn.Close()

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
	defer conn.Close()

	// Read the initial connected message from the sandbox (relayed through proxy).
	var connected map[string]interface{}
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
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
	defer conn.Close()

	// The connected message from the sandbox should contain the proxy-injected
	// user context matching the JWT subject.
	var connected map[string]interface{}
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
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
	defer conn.Close()

	// The sandbox should see the JWT-verified user, not the spoofed header.
	var connected map[string]interface{}
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
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
	defer conn.Close()

	// Read the initial connected message.
	var connected map[string]interface{}
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
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
		conn.SetReadDeadline(time.Now().Add(3 * time.Second))
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
	defer conn.Close()

	// The connection should succeed on /api/ws.
	var connected map[string]interface{}
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
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
	defer conn.Close()

	// Read the initial connected message.
	var connected map[string]interface{}
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
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
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if err := conn.ReadJSON(&connected); err != nil {
		conn.Close()
		t.Fatalf("read connected message: %v", err)
	}

	// Client closes the connection with a normal close message.
	if err := conn.WriteMessage(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")); err != nil {
		conn.Close()
		t.Fatalf("write close message: %v", err)
	}

	// Subsequent reads should indicate the connection is closed.
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, _, err := conn.ReadMessage()
	conn.Close()
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



