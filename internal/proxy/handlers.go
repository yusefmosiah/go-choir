package proxy

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
	"github.com/yusefmosiah/go-choir/internal/server"
)

// clientIdentityHeaders is the list of HTTP headers that must be stripped from
// client requests before forwarding to the sandbox. These headers could be used
// to impersonate or spoof user identity, so the proxy removes them all and
// only injects the JWT-verified user context via X-Authenticated-User.
var clientIdentityHeaders = []string{
	"X-Authenticated-User",
	"X-User-Id",
	"X-User-Name",
	"X-Forwarded-User",
	"X-Remote-User",
	"X-Auth-User",
}

// errorResponse is a generic JSON error envelope.
type errorResponse struct {
	Error string `json:"error"`
}

// proxyHealthResponse is the JSON structure returned by the proxy /health
// endpoint. It includes the proxy status and the upstream sandbox
// reachability status, making the protected-request backend health
// observable for VAL-DEPLOY-008.
type proxyHealthResponse struct {
	Status   string `json:"status"`
	Service  string `json:"service"`
	Upstream string `json:"upstream"`
}

// AuthResult holds the result of access JWT validation.
type AuthResult struct {
	UserID string
	Valid  bool
}

// Handler provides HTTP and WebSocket handlers for the proxy routes.
type Handler struct {
	cfg          *Config
	pubKey       ed25519.PublicKey
	reverseProxy *httputil.ReverseProxy
	upgrader     websocket.Upgrader
	dialer       *websocket.Dialer
	sandboxURL   *url.URL // parsed sandbox URL for WS dial derivation
}

// NewHandler creates a proxy Handler with the given config and auth public key.
// It initializes the reverse proxy pointing at the configured sandbox URL and
// the WebSocket upgrader/dialer for live-channel proxying.
func NewHandler(cfg *Config, pubKey ed25519.PublicKey) (*Handler, error) {
	sandboxURL, err := url.Parse(cfg.SandboxURL)
	if err != nil {
		return nil, fmt.Errorf("parse sandbox URL %s: %w", cfg.SandboxURL, err)
	}

	proxy := httputil.NewSingleHostReverseProxy(sandboxURL)

	// Customize the director to preserve the original request path and query
	// without rewriting. The default NewSingleHostReverseProxy director
	// replaces the path, but we want the sandbox to receive the same public
	// path (e.g., /api/shell/bootstrap) so that prefix preservation is
	// observable end to end.
	//
	// The director also handles user-context injection: it strips all
	// client-supplied identity headers (to prevent spoofing), then sets
	// X-Authenticated-User from the trusted X-Proxy-Trusted-User header
	// that the proxy handler sets after JWT validation.
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)

		// Override the path and query to preserve the original public request.
		req.URL.Path = req.Header.Get("X-Original-Path")
		req.URL.RawPath = ""
		req.URL.RawQuery = req.Header.Get("X-Original-RawQuery")

		// Set the Host header to the sandbox host so the upstream receives
		// the correct Host.
		req.Host = sandboxURL.Host

		// Strip ALL client-supplied identity headers to prevent spoofing.
		// Only the proxy-verified user context is allowed through.
		for _, hdr := range clientIdentityHeaders {
			req.Header.Del(hdr)
		}

		// Inject trusted user context from the proxy-validated JWT.
		trustedUser := req.Header.Get("X-Proxy-Trusted-User")
		if trustedUser != "" {
			req.Header.Set("X-Authenticated-User", trustedUser)
		}

		// Clean up internal proxy headers before forwarding.
		req.Header.Del("X-Proxy-Trusted-User")
		req.Header.Del("X-Original-Path")
		req.Header.Del("X-Original-RawQuery")
	}

	return &Handler{
		cfg:          cfg,
		pubKey:       pubKey,
		reverseProxy: proxy,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				// The proxy is the trust boundary for origin validation.
				// Accept all origins here; the deployed Caddy layer and
				// same-origin cookie policy enforce origin checks.
				return true
			},
		},
		dialer:     websocket.DefaultDialer,
		sandboxURL: sandboxURL,
	}, nil
}

// writeJSON writes a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("proxy handler: json encode error: %v", err)
	}
}

// validateAccessJWT validates the access JWT from the choir_access cookie.
// It returns the user ID if valid, or an error if the token is missing,
// invalid, expired, tampered, or not an access-scoped token.
func (h *Handler) validateAccessJWT(r *http.Request) (*AuthResult, error) {
	cookie, err := r.Cookie("choir_access")
	if err != nil {
		if errors.Is(err, http.ErrNoCookie) {
			return nil, errors.New("missing access token cookie")
		}
		return nil, fmt.Errorf("read access cookie: %w", err)
	}

	if cookie.Value == "" {
		return nil, errors.New("empty access token cookie")
	}

	token, err := jwt.Parse(cookie.Value, func(t *jwt.Token) (interface{}, error) {
		if t.Method != jwt.SigningMethodEdDSA {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return h.pubKey, nil
	})
	if err != nil {
		return nil, fmt.Errorf("invalid access token: %w", err)
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return nil, errors.New("invalid token claims")
	}

	userID, ok := claims["sub"].(string)
	if !ok || userID == "" {
		return nil, errors.New("invalid token subject")
	}

	scope, _ := claims["scope"].(string)
	if scope != "access" {
		return nil, errors.New("token is not an access token")
	}

	return &AuthResult{UserID: userID, Valid: true}, nil
}

// HandleBootstrap handles GET /api/shell/bootstrap.
// It validates the access JWT cookie, denies requests with missing or invalid
// auth, and forwards authenticated requests to the sandbox upstream.
// The proxy injects the authenticated user context via the
// X-Authenticated-User header and preserves the original request path, method,
// query string, and upstream status/body.
func (h *Handler) HandleBootstrap(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "method not allowed"})
		return
	}

	// Validate auth.
	authResult, err := h.validateAccessJWT(r)
	if err != nil {
		// Missing or invalid auth — deny with a machine-readable auth failure.
		// Do NOT reach the upstream.
		writeJSON(w, http.StatusUnauthorized, errorResponse{Error: "authentication required"})
		return
	}

	// Auth is valid. Store the trusted user context for the director to inject.
	// Use X-Proxy-Trusted-User as an internal carrier; the director will
	// strip any client-supplied X-Authenticated-User and replace it with
	// this trusted value before forwarding to the upstream.
	r.Header.Set("X-Proxy-Trusted-User", authResult.UserID)

	// Preserve the original path and query for the director to use.
	r.Header.Set("X-Original-Path", r.URL.Path)
	r.Header.Set("X-Original-RawQuery", r.URL.RawQuery)

	h.reverseProxy.ServeHTTP(w, r)
}

// HandleProtectedAPI is a generic handler for /api/* routes that require auth.
// It validates the access JWT and forwards authenticated requests to the
// sandbox. This is used for routes other than the specific bootstrap route
// (e.g., future API routes).
func (h *Handler) HandleProtectedAPI(w http.ResponseWriter, r *http.Request) {
	// Validate auth.
	authResult, err := h.validateAccessJWT(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, errorResponse{Error: "authentication required"})
		return
	}

	// Auth is valid. Store the trusted user context for the director.
	r.Header.Set("X-Proxy-Trusted-User", authResult.UserID)
	r.Header.Set("X-Original-Path", r.URL.Path)
	r.Header.Set("X-Original-RawQuery", r.URL.RawQuery)

	h.reverseProxy.ServeHTTP(w, r)
}

// HandleAPI routes /api/* traffic. It applies auth gating for all /api/*
// routes and dispatches to specific handlers where they exist. Unknown /api/*
// paths are denied with 401 for unauthenticated callers and 404 for
// authenticated callers, so the proxy is consistently fail-closed: no /api/*
// route ever exposes data without auth, and signed-out callers always see a
// 401 denial rather than a 404 that might suggest the route doesn't exist.
func (h *Handler) HandleAPI(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	// Route specific protected paths.
	switch {
	case path == "/api/shell/bootstrap":
		h.HandleBootstrap(w, r)
		return
	case path == "/api/ws":
		h.HandleWS(w, r)
		return
	case strings.HasPrefix(path, "/api/"):
		// All /api/* routes require auth by default. Check auth before
		// returning 404 so signed-out callers consistently receive 401
		// instead of accidentally learning which routes exist.
		if _, err := h.validateAccessJWT(r); err != nil {
			writeJSON(w, http.StatusUnauthorized, errorResponse{Error: "authentication required"})
			return
		}
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "not found"})
		return
	default:
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "not found"})
		return
	}
}

// HandleWS handles GET /api/ws. It validates the access JWT cookie, denies
// requests with missing or invalid auth without upgrading the connection, and
// relays WebSocket frames bidirectionally between the client and the hardcoded
// placeholder sandbox. The proxy injects the authenticated user context via
// the X-Authenticated-User header on the sandbox dial and strips any
// client-supplied identity headers.
func (h *Handler) HandleWS(w http.ResponseWriter, r *http.Request) {
	// Step 1: Validate auth BEFORE upgrading. Missing or invalid auth is
	// denied with a machine-readable 401 JSON response and no WS upgrade.
	authResult, err := h.validateAccessJWT(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, errorResponse{Error: "authentication required"})
		return
	}

	// Step 2: Upgrade the client connection to WebSocket.
	clientConn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade failed — nothing to relay. The upgrader already wrote
		// an HTTP error response.
		return
	}
	defer func() { _ = clientConn.Close() }()

	// Step 3: Dial the sandbox WebSocket endpoint.
	sandboxWSURL := h.sandboxWSURL()
	sandboxHeader := http.Header{}
	// Inject the trusted user context; strip any client-supplied value.
	// The proxy is the trust boundary — only JWT-verified identity flows.
	sandboxHeader.Set("X-Authenticated-User", authResult.UserID)

	sandboxConn, _, err := h.dialer.Dial(sandboxWSURL, sandboxHeader)
	if err != nil {
		log.Printf("proxy WS: dial sandbox %s: %v", sandboxWSURL, err)
		// Close the client connection since we can't reach the sandbox.
		_ = clientConn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseTryAgainLater, "upstream unavailable"))
		return
	}
	defer func() { _ = sandboxConn.Close() }()

	// Step 4: Relay frames bidirectionally until either side closes or errors.
	relayDone := make(chan struct{}, 2)

	// Client -> Sandbox relay.
	go func() {
		defer func() { relayDone <- struct{}{} }()
		h.relayFrames(clientConn, sandboxConn, "client->sandbox")
	}()

	// Sandbox -> Client relay.
	go func() {
		defer func() { relayDone <- struct{}{} }()
		h.relayFrames(sandboxConn, clientConn, "sandbox->client")
	}()

	// Wait for one direction to finish, then close both connections.
	<-relayDone

	// Send close messages to both sides to unblock the other relay goroutine.
	_ = clientConn.WriteMessage(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
	_ = sandboxConn.WriteMessage(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))

	// Wait briefly for the second goroutine to finish.
	<-relayDone
}

// sandboxWSURL derives the WebSocket URL for the sandbox /api/ws endpoint
// from the configured HTTP sandbox URL.
func (h *Handler) sandboxWSURL() string {
	u := *h.sandboxURL // shallow copy
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	}
	u.Path = "/api/ws"
	return u.String()
}

// relayFrames copies WebSocket messages from src to dst until an error occurs
// or the connection is closed. It preserves the message type (text or binary).
func (h *Handler) relayFrames(src, dst *websocket.Conn, direction string) {
	for {
		mt, msg, err := src.ReadMessage()
		if err != nil {
			// Normal close or expected error — stop relaying silently.
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				return
			}
			// Abnormal closure or EOF is normal teardown when the other side
			// drops; no need to log noisily.
			if errors.Is(err, io.EOF) || websocket.IsCloseError(err, websocket.CloseAbnormalClosure) {
				return
			}
			// Unexpected errors are worth logging for debugging.
			log.Printf("proxy WS relay %s: read error: %v", direction, err)
			return
		}
		if err := dst.WriteMessage(mt, msg); err != nil {
			// Write error means the other side is gone; stop relaying silently.
			return
		}
	}
}

// HandleHealth handles GET /health for the proxy service. It checks the
// upstream sandbox reachability in addition to the proxy's own health,
// making the protected-request backend health observable (VAL-DEPLOY-008).
// The response includes:
//   - status: "ok" when the proxy and upstream are healthy, "degraded" when
//     the proxy is up but the upstream is unreachable
//   - upstream: "ok" or "unreachable"
func (h *Handler) HandleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "method not allowed"})
		return
	}

	// Check upstream sandbox health.
	upstreamStatus := "ok"
	upstreamHealthy := h.checkUpstreamHealth()
	if !upstreamHealthy {
		upstreamStatus = "unreachable"
	}

	status := "ok"
	if !upstreamHealthy {
		status = "degraded"
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(proxyHealthResponse{
		Status:   status,
		Service:  "proxy",
		Upstream: upstreamStatus,
	})
}

// checkUpstreamHealth probes the upstream sandbox's /health endpoint
// with a short timeout. Returns true if the upstream responds with a
// 2xx status within 2 seconds, false otherwise.
func (h *Handler) checkUpstreamHealth() bool {
	client := &http.Client{Timeout: 2 * time.Second}
	url := h.sandboxURL.String() + "/health"
	resp, err := client.Get(url)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

// RegisterRoutes registers all proxy routes on the given server.
// The proxy /health handler is registered via SetHealthHandler to
// override the default server health handler with one that reports
// upstream sandbox reachability.
func RegisterRoutes(s *server.Server, h *Handler) {
	s.SetHealthHandler(h.HandleHealth)
	s.HandleFunc("/api/shell/bootstrap", h.HandleBootstrap)
	s.HandleFunc("/api/ws", h.HandleWS)
	s.HandleFunc("/api/", h.HandleAPI)
}
