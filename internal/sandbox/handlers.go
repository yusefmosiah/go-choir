package sandbox

import (
	"encoding/json"
	"net/http"

	"github.com/gorilla/websocket"
	"github.com/yusefmosiah/go-choir/internal/server"
)

// BootstrapResponse is the JSON payload returned by GET /api/shell/bootstrap.
// It includes the sandbox identity, the authenticated user context forwarded by
// the proxy, and a bootstrap payload for the shell.
type BootstrapResponse struct {
	SandboxID  string `json:"sandbox_id"`
	User       string `json:"user,omitempty"`
	Bootstrap  string `json:"bootstrap"`
	Path       string `json:"path"`
	Method     string `json:"method"`
	Query      string `json:"query,omitempty"`
	StatusCode int    `json:"status_code"`
}

// ErrorResponse is the JSON payload returned by deliberate non-2xx paths.
type ErrorResponse struct {
	SandboxID  string `json:"sandbox_id"`
	StatusCode int    `json:"status_code"`
	Error      string `json:"error"`
}

// WSMessage is a simple JSON message echoed over the WebSocket channel.
type WSMessage struct {
	SandboxID string `json:"sandbox_id"`
	User      string `json:"user,omitempty"`
	Type      string `json:"type"`
	Payload   string `json:"payload"`
}

// Handler provides the placeholder sandbox HTTP and WebSocket handlers.
type Handler struct {
	cfg    Config
	upgrader websocket.Upgrader
}

// NewHandler creates a sandbox handler with the given configuration.
func NewHandler(cfg Config) *Handler {
	return &Handler{
		cfg: cfg,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				// Allow all origins for the placeholder sandbox; the proxy
				// is the trust boundary for origin validation.
				return true
			},
		},
	}
}

// HandleBootstrap handles GET /api/shell/bootstrap.
// It returns the shell bootstrap payload including sandbox identity,
// authenticated user context, and request echo data.
func (h *Handler) HandleBootstrap(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	user := r.Header.Get("X-Authenticated-User")

	resp := BootstrapResponse{
		SandboxID:  h.cfg.SandboxID,
		User:       user,
		Bootstrap:  "placeholder-shell-v1",
		Path:       r.URL.Path,
		Method:     r.Method,
		Query:      r.URL.RawQuery,
		StatusCode: 200,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp)
}

// HandleError returns a deliberate 500 response for proxy passthrough testing.
func (h *Handler) HandleError(w http.ResponseWriter, r *http.Request) {
	resp := ErrorResponse{
		SandboxID:  h.cfg.SandboxID,
		StatusCode: 500,
		Error:      "deliberate sandbox error for passthrough testing",
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	json.NewEncoder(w).Encode(resp)
}

// HandleWS upgrades the connection to WebSocket and echoes messages back.
// It also sends an initial connected message with the sandbox identity and
// user context on successful upgrade.
func (h *Handler) HandleWS(w http.ResponseWriter, r *http.Request) {
	user := r.Header.Get("X-Authenticated-User")

	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	// Send initial connected message with sandbox identity and user context.
	connected := WSMessage{
		SandboxID: h.cfg.SandboxID,
		User:      user,
		Type:      "connected",
		Payload:   "websocket channel open",
	}
	if err := conn.WriteJSON(connected); err != nil {
		return
	}

	// Echo loop: read messages and echo them back with sandbox context.
	for {
		var msg WSMessage
		if err := conn.ReadJSON(&msg); err != nil {
			break
		}
		echo := WSMessage{
			SandboxID: h.cfg.SandboxID,
			User:      user,
			Type:      "echo",
			Payload:   msg.Payload,
		}
		if err := conn.WriteJSON(echo); err != nil {
			break
		}
	}
}

// RegisterRoutes registers all sandbox routes on the given server.
func RegisterRoutes(s *server.Server, h *Handler) {
	s.HandleFunc("/api/shell/bootstrap", h.HandleBootstrap)
	s.HandleFunc("/api/shell/error", h.HandleError)
	s.HandleFunc("/api/ws", h.HandleWS)
}
