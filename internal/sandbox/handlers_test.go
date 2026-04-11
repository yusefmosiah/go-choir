package sandbox

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestHandleBootstrapReturnsSandboxIdentity(t *testing.T) {
	cfg := Config{Port: "0", SandboxID: "sandbox-test-001"}
	h := NewHandler(cfg.SandboxID)

	req := httptest.NewRequest(http.MethodGet, "/api/shell/bootstrap", nil)
	w := httptest.NewRecorder()
	h.HandleBootstrap(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	var resp BootstrapResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode bootstrap response: %v", err)
	}

	if resp.SandboxID != "sandbox-test-001" {
		t.Errorf("expected sandbox_id %q, got %q", "sandbox-test-001", resp.SandboxID)
	}
	if resp.Bootstrap != "placeholder-shell-v1" {
		t.Errorf("expected bootstrap %q, got %q", "placeholder-shell-v1", resp.Bootstrap)
	}
}

func TestHandleBootstrapEchoesUserContext(t *testing.T) {
	cfg := Config{Port: "0", SandboxID: "sandbox-test-001"}
	h := NewHandler(cfg.SandboxID)

	req := httptest.NewRequest(http.MethodGet, "/api/shell/bootstrap", nil)
	req.Header.Set("X-Authenticated-User", "user-alice@example.com")
	w := httptest.NewRecorder()
	h.HandleBootstrap(w, req)

	var resp BootstrapResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode bootstrap response: %v", err)
	}

	if resp.User != "user-alice@example.com" {
		t.Errorf("expected user %q, got %q", "user-alice@example.com", resp.User)
	}
}

func TestHandleBootstrapEchoesRequestPath(t *testing.T) {
	cfg := Config{Port: "0", SandboxID: "sandbox-test-001"}
	h := NewHandler(cfg.SandboxID)

	req := httptest.NewRequest(http.MethodGet, "/api/shell/bootstrap?detail=full", nil)
	w := httptest.NewRecorder()
	h.HandleBootstrap(w, req)

	var resp BootstrapResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode bootstrap response: %v", err)
	}

	if resp.Path != "/api/shell/bootstrap" {
		t.Errorf("expected path %q, got %q", "/api/shell/bootstrap", resp.Path)
	}
	if resp.Method != "GET" {
		t.Errorf("expected method %q, got %q", "GET", resp.Method)
	}
	if resp.Query != "detail=full" {
		t.Errorf("expected query %q, got %q", "detail=full", resp.Query)
	}
}

func TestHandleBootstrapRejectsNonGet(t *testing.T) {
	cfg := Config{Port: "0", SandboxID: "sandbox-test-001"}
	h := NewHandler(cfg.SandboxID)

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/api/shell/bootstrap", nil)
			w := httptest.NewRecorder()
			h.HandleBootstrap(w, req)

			if w.Code != http.StatusMethodNotAllowed {
				t.Errorf("expected status 405 for %s, got %d", method, w.Code)
			}
		})
	}
}

func TestHandleBootstrapReturnsJSONContentType(t *testing.T) {
	cfg := Config{Port: "0", SandboxID: "sandbox-test-001"}
	h := NewHandler(cfg.SandboxID)

	req := httptest.NewRequest(http.MethodGet, "/api/shell/bootstrap", nil)
	w := httptest.NewRecorder()
	h.HandleBootstrap(w, req)

	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("expected Content-Type to contain application/json, got %q", ct)
	}
}

func TestHandleErrorReturnsNon2xx(t *testing.T) {
	cfg := Config{Port: "0", SandboxID: "sandbox-test-001"}
	h := NewHandler(cfg.SandboxID)

	req := httptest.NewRequest(http.MethodGet, "/api/shell/error", nil)
	w := httptest.NewRecorder()
	h.HandleError(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected status 500, got %d", w.Code)
	}

	var resp ErrorResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}

	if resp.SandboxID != "sandbox-test-001" {
		t.Errorf("expected sandbox_id %q, got %q", "sandbox-test-001", resp.SandboxID)
	}
	if resp.StatusCode != 500 {
		t.Errorf("expected status_code 500, got %d", resp.StatusCode)
	}
	if resp.Error == "" {
		t.Error("expected non-empty error message")
	}
}

func TestHandleErrorReturnsJSONContentType(t *testing.T) {
	cfg := Config{Port: "0", SandboxID: "sandbox-test-001"}
	h := NewHandler(cfg.SandboxID)

	req := httptest.NewRequest(http.MethodGet, "/api/shell/error", nil)
	w := httptest.NewRecorder()
	h.HandleError(w, req)

	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("expected Content-Type to contain application/json, got %q", ct)
	}
}

func TestHandleWSUpgradesAndEchoes(t *testing.T) {
	cfg := Config{Port: "0", SandboxID: "sandbox-ws-test"}
	h := NewHandler(cfg.SandboxID)

	// Create a test HTTP server that routes to the WS handler.
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.HandleWS(w, r)
	}))
	defer s.Close()

	// Connect via WebSocket.
	wsURL := "ws" + strings.TrimPrefix(s.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("failed to dial websocket: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// Read the initial connected message.
	var connected WSMessage
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if err := conn.ReadJSON(&connected); err != nil {
		t.Fatalf("failed to read connected message: %v", err)
	}

	if connected.SandboxID != "sandbox-ws-test" {
		t.Errorf("expected sandbox_id %q, got %q", "sandbox-ws-test", connected.SandboxID)
	}
	if connected.Type != "connected" {
		t.Errorf("expected type %q, got %q", "connected", connected.Type)
	}

	// Send an echo message and verify it comes back.
	msg := WSMessage{
		Type:    "test",
		Payload: "hello-from-shell",
	}
	if err := conn.WriteJSON(msg); err != nil {
		t.Fatalf("failed to write message: %v", err)
	}

	var echo WSMessage
	if err := conn.ReadJSON(&echo); err != nil {
		t.Fatalf("failed to read echo message: %v", err)
	}

	if echo.SandboxID != "sandbox-ws-test" {
		t.Errorf("expected echo sandbox_id %q, got %q", "sandbox-ws-test", echo.SandboxID)
	}
	if echo.Type != "echo" {
		t.Errorf("expected echo type %q, got %q", "echo", echo.Type)
	}
	if echo.Payload != "hello-from-shell" {
		t.Errorf("expected echo payload %q, got %q", "hello-from-shell", echo.Payload)
	}
}

func TestHandleWSEchoesUserContext(t *testing.T) {
	cfg := Config{Port: "0", SandboxID: "sandbox-ws-user"}
	h := NewHandler(cfg.SandboxID)

	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.HandleWS(w, r)
	}))
	defer s.Close()

	wsURL := "ws" + strings.TrimPrefix(s.URL, "http")
	header := http.Header{}
	header.Set("X-Authenticated-User", "user-bob@example.com")

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		t.Fatalf("failed to dial websocket: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// Read the initial connected message and check user context.
	var connected WSMessage
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if err := conn.ReadJSON(&connected); err != nil {
		t.Fatalf("failed to read connected message: %v", err)
	}

	if connected.User != "user-bob@example.com" {
		t.Errorf("expected user %q in connected message, got %q", "user-bob@example.com", connected.User)
	}

	// Send a message and check user context in the echo.
	msg := WSMessage{Type: "test", Payload: "ping"}
	if err := conn.WriteJSON(msg); err != nil {
		t.Fatalf("failed to write message: %v", err)
	}

	var echo WSMessage
	if err := conn.ReadJSON(&echo); err != nil {
		t.Fatalf("failed to read echo message: %v", err)
	}

	if echo.User != "user-bob@example.com" {
		t.Errorf("expected user %q in echo, got %q", "user-bob@example.com", echo.User)
	}
}

func TestBootstrapNoUserContextWithoutHeader(t *testing.T) {
	cfg := Config{Port: "0", SandboxID: "sandbox-test-001"}
	h := NewHandler(cfg.SandboxID)

	req := httptest.NewRequest(http.MethodGet, "/api/shell/bootstrap", nil)
	// No X-Authenticated-User header set.
	w := httptest.NewRecorder()
	h.HandleBootstrap(w, req)

	var resp BootstrapResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode bootstrap response: %v", err)
	}

	if resp.User != "" {
		t.Errorf("expected empty user when no header set, got %q", resp.User)
	}
}

func TestHandleWSNoUserContextWithoutHeader(t *testing.T) {
	cfg := Config{Port: "0", SandboxID: "sandbox-ws-no-user"}
	h := NewHandler(cfg.SandboxID)

	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.HandleWS(w, r)
	}))
	defer s.Close()

	wsURL := "ws" + strings.TrimPrefix(s.URL, "http")
	// No X-Authenticated-User header.
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("failed to dial websocket: %v", err)
	}
	defer func() { _ = conn.Close() }()

	var connected WSMessage
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if err := conn.ReadJSON(&connected); err != nil {
		t.Fatalf("failed to read connected message: %v", err)
	}

	if connected.User != "" {
		t.Errorf("expected empty user in connected message, got %q", connected.User)
	}
}
