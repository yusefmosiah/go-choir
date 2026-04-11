package proxy

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

// errorResponse is a generic JSON error envelope.
type errorResponse struct {
	Error string `json:"error"`
}

// AuthResult holds the result of access JWT validation.
type AuthResult struct {
	UserID  string
	Valid   bool
}

// Handler provides HTTP handlers for the proxy routes.
type Handler struct {
	cfg      *Config
	pubKey   ed25519.PublicKey
	reverseProxy *httputil.ReverseProxy
}

// NewHandler creates a proxy Handler with the given config and auth public key.
// It initializes the reverse proxy pointing at the configured sandbox URL.
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
	// The director also handles user-context injection: it strips any
	// client-supplied X-Authenticated-User header (to prevent spoofing),
	// then sets it from the trusted X-Proxy-Trusted-User header that the
	// proxy handler sets after JWT validation.
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

		// Inject trusted user context: strip any client-supplied value,
		// then set from the proxy-validated trusted user header.
		trustedUser := req.Header.Get("X-Proxy-Trusted-User")
		req.Header.Del("X-Authenticated-User")
		if trustedUser != "" {
			req.Header.Set("X-Authenticated-User", trustedUser)
		}

		// Clean up internal proxy headers before forwarding.
		req.Header.Del("X-Proxy-Trusted-User")
		req.Header.Del("X-Original-Path")
		req.Header.Del("X-Original-RawQuery")
	}

	return &Handler{
		cfg:         cfg,
		pubKey:      pubKey,
		reverseProxy: proxy,
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

// HandleAPI routes /api/* traffic. It applies auth gating for protected
// routes and returns 404 for unknown /api/* paths.
func (h *Handler) HandleAPI(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	// Route specific protected paths.
	switch {
	case path == "/api/shell/bootstrap":
		h.HandleBootstrap(w, r)
		return
	case strings.HasPrefix(path, "/api/"):
		// For Milestone 1, other /api/* routes are not yet protected through
		// specific handlers. Return 404 for unknown API routes.
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "not found"})
		return
	default:
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "not found"})
		return
	}
}

// RegisterRoutes registers all proxy routes on the given server.
func RegisterRoutes(s interface{ HandleFunc(string, http.HandlerFunc) }, h *Handler) {
	s.HandleFunc("/api/shell/bootstrap", h.HandleBootstrap)
	s.HandleFunc("/api/", h.HandleAPI)
}
