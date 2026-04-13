package sandbox

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/exec"
	"sync"

	"github.com/creack/pty/v2"
	"github.com/gorilla/websocket"
)

// TerminalMessage represents a JSON message exchanged over the terminal WebSocket.
// Client-to-server messages:
//   - {"type":"input","data":"..."} — keystrokes/stdin
//   - {"type":"resize","cols":N,"rows":N} — resize the PTY
//
// Server-to-client messages:
//   - {"type":"output","data":"..."} — PTY stdout
//   - {"type":"error","data":"..."} — error message
type TerminalMessage struct {
	Type  string `json:"type"`           // "input", "resize", "output", "error"
	Data  string `json:"data,omitempty"` // payload for input/output/error
	Cols  uint16 `json:"cols,omitempty"` // columns for resize
	Rows  uint16 `json:"rows,omitempty"` // rows for resize
}

// TerminalSession represents a single PTY session associated with a WebSocket
// connection. Each session has its own shell process and PTY.
type TerminalSession struct {
	id   string
	cmd  *exec.Cmd
	ptmx *os.File
	conn *websocket.Conn
}

// TerminalManager manages active terminal sessions. It provides per-session
// PTY lifecycle management, cleanup on disconnect, and prevents zombie
// processes.
type TerminalManager struct {
	mu       sync.Mutex
	sessions map[string]*TerminalSession
}

// NewTerminalManager creates a new terminal session manager.
func NewTerminalManager() *TerminalManager {
	return &TerminalManager{
		sessions: make(map[string]*TerminalSession),
	}
}

// Sessions returns the number of active sessions.
func (tm *TerminalManager) Sessions() int {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	return len(tm.sessions)
}

// TerminalHandler provides the WebSocket handler for terminal PTY sessions.
type TerminalHandler struct {
	manager  *TerminalManager
	upgrader websocket.Upgrader
	shell    string // path to the shell binary
}

// NewTerminalHandler creates a new terminal handler. It looks for bash first,
// falling back to sh.
func NewTerminalHandler() *TerminalHandler {
	shell := findShell()
	return &TerminalHandler{
		manager: NewTerminalManager(),
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				// Allow all origins; the proxy is the trust boundary for
				// origin validation.
				return true
			},
		},
		shell: shell,
	}
}

// Manager returns the terminal session manager.
func (th *TerminalHandler) Manager() *TerminalManager {
	return th.manager
}

// HandleTerminalWS handles the WebSocket upgrade for /api/terminal/ws.
// On connection it spawns a new PTY shell session and relays data
// bidirectionally between the WebSocket client and the PTY.
func (th *TerminalHandler) HandleTerminalWS(w http.ResponseWriter, r *http.Request) {
	// Check that the request has been authenticated by the proxy.
	// The proxy sets X-Authenticated-User for valid sessions.
	user := r.Header.Get("X-Authenticated-User")
	if user == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"authentication required"}`))
		return
	}

	// Upgrade to WebSocket.
	conn, err := th.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("terminal: WebSocket upgrade failed: %v", err)
		return
	}

	session, err := th.newSession(conn)
	if err != nil {
		log.Printf("terminal: failed to create PTY session: %v", err)
		th.sendError(conn, "failed to start shell: "+err.Error())
		_ = conn.Close()
		return
	}

	log.Printf("terminal: session %s started for user %s (pid=%d)", session.id, user, session.cmd.Process.Pid)

	// Start PTY output reader in a goroutine.
	ptyDone := make(chan struct{})
	go func() {
		defer close(ptyDone)
		session.readPTY()
	}()

	// Read from WebSocket (client input) and write to PTY.
	// This blocks until the WebSocket closes or an error occurs.
	session.readWS()

	// WebSocket closed. Shut down the PTY and clean up.
	// Close the PTY first to unblock the readPTY goroutine.
	_ = session.ptmx.Close()

	// Kill the shell process.
	if session.cmd.Process != nil {
		_ = session.cmd.Process.Kill()
	}

	// Wait for the PTY reader to finish.
	<-ptyDone

	// Wait for the process to exit (prevent zombies).
	if session.cmd.Process != nil {
		_ = session.cmd.Wait()
	}

	// Unregister from manager.
	th.manager.mu.Lock()
	delete(th.manager.sessions, session.id)
	th.manager.mu.Unlock()

	log.Printf("terminal: session %s cleaned up", session.id)
}

// newSession creates a new PTY session and registers it with the manager.
func (th *TerminalHandler) newSession(conn *websocket.Conn) (*TerminalSession, error) {
	// Create the shell command.
	cmd := exec.Command(th.shell)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")

	// Start the command with a PTY.
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return nil, err
	}

	id := generateSessionID()
	session := &TerminalSession{
		id:   id,
		cmd:  cmd,
		ptmx: ptmx,
		conn: conn,
	}

	// Register the session.
	th.manager.mu.Lock()
	th.manager.sessions[id] = session
	th.manager.mu.Unlock()

	return session, nil
}

// readPTY reads output from the PTY and sends it to the WebSocket client.
func (s *TerminalSession) readPTY() {
	buf := make([]byte, 4096)
	for {
		n, err := s.ptmx.Read(buf)
		if err != nil {
			// PTY closed (process exited or PTY closed).
			return
		}
		if n > 0 {
			msg := TerminalMessage{
				Type: "output",
				Data: string(buf[:n]),
			}
			if err := s.conn.WriteJSON(msg); err != nil {
				return
			}
		}
	}
}

// readWS reads messages from the WebSocket client and processes them.
// It handles "input" messages (keystrokes to PTY) and "resize" messages.
// Returns when the WebSocket is closed or an error occurs.
func (s *TerminalSession) readWS() {
	for {
		mt, msgBytes, err := s.conn.ReadMessage()
		if err != nil {
			// WebSocket closed or error.
			return
		}
		if mt != websocket.TextMessage {
			continue
		}

		var msg TerminalMessage
		if err := json.Unmarshal(msgBytes, &msg); err != nil {
			continue
		}

		switch msg.Type {
		case "input":
			if msg.Data != "" {
				_, _ = s.ptmx.Write([]byte(msg.Data))
			}
		case "resize":
			if msg.Cols > 0 && msg.Rows > 0 {
				_ = pty.Setsize(s.ptmx, &pty.Winsize{
					Cols: msg.Cols,
					Rows: msg.Rows,
				})
			}
		}
	}
}

// sendError sends an error message to the WebSocket client.
func (th *TerminalHandler) sendError(conn *websocket.Conn, message string) {
	msg := TerminalMessage{
		Type: "error",
		Data: message,
	}
	_ = conn.WriteJSON(msg)
}

// findShell returns the path to the shell binary, preferring bash over sh.
func findShell() string {
	for _, shell := range []string{"/bin/bash", "/bin/sh"} {
		if _, err := os.Stat(shell); err == nil {
			return shell
		}
	}
	// Fallback.
	return "sh"
}

// generateSessionID creates a simple unique session identifier.
func generateSessionID() string {
	return nextSessionID()
}

var sessionCounter uint64
var sessionMu sync.Mutex

func nextSessionID() string {
	sessionMu.Lock()
	defer sessionMu.Unlock()
	sessionCounter++
	return formatSessionID(sessionCounter)
}

func formatSessionID(n uint64) string {
	return "term-" + uint64ToDec(n)
}

func uint64ToDec(n uint64) string {
	if n == 0 {
		return "0"
	}
	const digits = "0123456789"
	var buf [20]byte
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = digits[n%10]
		n /= 10
	}
	return string(buf[pos:])
}

// RegisterTerminalRoutes registers the terminal WebSocket route on the given
// server.
func RegisterTerminalRoutes(s interface{ HandleFunc(string, http.HandlerFunc) }, th *TerminalHandler) {
	s.HandleFunc("/api/terminal/ws", th.HandleTerminalWS)
}
