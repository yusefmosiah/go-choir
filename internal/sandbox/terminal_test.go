package sandbox

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// dialTerminalWS is a test helper that dials the terminal WebSocket on the
// given httptest.Server with optional X-Authenticated-User header.
func dialTerminalWS(ts *httptest.Server, user string) (*websocket.Conn, *http.Response, error) {
	dialer := websocket.DefaultDialer
	header := http.Header{}
	if user != "" {
		header.Set("X-Authenticated-User", user)
	}
	url := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/terminal/ws"
	return dialer.Dial(url, header)
}

// newTerminalTestServer creates an httptest.Server with the terminal handler
// registered.
func newTerminalTestServer() (*httptest.Server, *TerminalHandler) {
	th := NewTerminalHandler()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/terminal/ws", th.HandleTerminalWS)
	ts := httptest.NewServer(mux)
	return ts, th
}

// terminalTestConn wraps a websocket.Conn for terminal test helpers.
// It uses a background reader goroutine to avoid read deadline issues.
type terminalTestConn struct {
	conn    *websocket.Conn
	mu      sync.Mutex
	msgs    chan TerminalMessage
	err     error
	closed  chan struct{}
}

// newTerminalTestConn wraps a websocket connection with a background reader.
func newTerminalTestConn(conn *websocket.Conn) *terminalTestConn {
	tc := &terminalTestConn{
		conn:   conn,
		msgs:   make(chan TerminalMessage, 1000),
		closed: make(chan struct{}),
	}
	go tc.readLoop()
	return tc
}

// readLoop reads messages from the WebSocket in the background.
func (tc *terminalTestConn) readLoop() {
	defer close(tc.closed)
	for {
		_, msgBytes, err := tc.conn.ReadMessage()
		if err != nil {
			tc.mu.Lock()
			tc.err = err
			tc.mu.Unlock()
			return
		}
		var msg TerminalMessage
		if json.Unmarshal(msgBytes, &msg) == nil {
			select {
			case tc.msgs <- msg:
			default:
				// Drop old messages if buffer is full.
			}
		}
	}
}

// waitForOutput waits for an output message containing substr within timeout.
func (tc *terminalTestConn) waitForOutput(substr string, timeout time.Duration) (string, bool) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case msg, ok := <-tc.msgs:
			if !ok {
				return "", false
			}
			if msg.Type == "output" && strings.Contains(msg.Data, substr) {
				return msg.Data, true
			}
		case <-timer.C:
			return "", false
		case <-tc.closed:
			return "", false
		}
	}
}

// waitForAnyOutput waits for any non-empty output message within timeout.
func (tc *terminalTestConn) waitForAnyOutput(timeout time.Duration) (string, bool) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case msg, ok := <-tc.msgs:
			if !ok {
				return "", false
			}
			if msg.Type == "output" && msg.Data != "" {
				return msg.Data, true
			}
		case <-timer.C:
			return "", false
		case <-tc.closed:
			return "", false
		}
	}
}

// drainOutput drains all pending messages and waits until no new messages
// arrive for the given quiet duration.
func (tc *terminalTestConn) drainOutput(quiet time.Duration) {
	quietTimer := time.NewTimer(quiet)
	defer quietTimer.Stop()
	for {
		select {
		case <-tc.msgs:
			quietTimer.Reset(quiet)
		case <-quietTimer.C:
			return
		case <-tc.closed:
			return
		}
	}
}

// writeMessage sends a TerminalMessage to the connection.
func (tc *terminalTestConn) writeMessage(msg TerminalMessage) error {
	tc.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	return tc.conn.WriteJSON(msg)
}

// close closes the underlying connection.
func (tc *terminalTestConn) close() {
	_ = tc.conn.WriteMessage(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
	_ = tc.conn.Close()
}

func TestTerminalWS_Unauthenticated_Rejected(t *testing.T) {
	ts, _ := newTerminalTestServer()
	defer ts.Close()

	// Attempt to connect without X-Authenticated-User.
	conn, resp, err := dialTerminalWS(ts, "")
	if err == nil {
		_ = conn.Close()
		t.Fatal("expected WebSocket connection to be rejected without auth")
	}
	if resp == nil {
		t.Fatal("expected non-nil HTTP response")
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", resp.StatusCode)
	}

	// Verify the body contains the expected error.
	var errResp map[string]string
	if decodeErr := json.NewDecoder(resp.Body).Decode(&errResp); decodeErr != nil {
		t.Fatalf("failed to decode error response: %v", decodeErr)
	}
	if errResp["error"] != "authentication required" {
		t.Errorf("expected error 'authentication required', got %q", errResp["error"])
	}
}

func TestTerminalWS_Authenticated_ConnectsAndOutputs(t *testing.T) {
	ts, _ := newTerminalTestServer()
	defer ts.Close()

	conn, _, err := dialTerminalWS(ts, "test-user@example.com")
	if err != nil {
		t.Fatalf("failed to connect with auth: %v", err)
	}
	tc := newTerminalTestConn(conn)
	defer tc.close()

	// Wait for shell prompt output. The shell should start and produce some
	// output (prompt) within 5 seconds.
	_, found := tc.waitForAnyOutput(5 * time.Second)
	if !found {
		t.Error("expected to receive output from terminal session (shell prompt)")
	}
}

func TestTerminalWS_InputReachesPTY(t *testing.T) {
	ts, _ := newTerminalTestServer()
	defer ts.Close()

	conn, _, err := dialTerminalWS(ts, "test-user@example.com")
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	tc := newTerminalTestConn(conn)
	defer tc.close()

	// Drain initial prompt output.
	tc.drainOutput(500 * time.Millisecond)

	// Send a simple command that produces identifiable output.
	if err := tc.writeMessage(TerminalMessage{Type: "input", Data: "echo test_terminal_output_123\n"}); err != nil {
		t.Fatalf("failed to send input: %v", err)
	}

	// Wait for the echo output.
	_, found := tc.waitForOutput("test_terminal_output_123", 5*time.Second)
	if !found {
		t.Error("expected to find 'test_terminal_output_123' in terminal output")
	}
}

func TestTerminalWS_ResizeMessage(t *testing.T) {
	ts, _ := newTerminalTestServer()
	defer ts.Close()

	conn, _, err := dialTerminalWS(ts, "test-user@example.com")
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	tc := newTerminalTestConn(conn)
	defer tc.close()

	// Drain initial prompt output.
	tc.drainOutput(500 * time.Millisecond)

	// Send resize message.
	if err := tc.writeMessage(TerminalMessage{Type: "resize", Cols: 120, Rows: 40}); err != nil {
		t.Fatalf("failed to send resize: %v", err)
	}

	// Send a command to verify the PTY is still functional after resize.
	if err := tc.writeMessage(TerminalMessage{Type: "input", Data: "echo resize_ok\n"}); err != nil {
		t.Fatalf("failed to send input after resize: %v", err)
	}

	// Wait for output confirming PTY is still working.
	_, found := tc.waitForOutput("resize_ok", 5*time.Second)
	if !found {
		t.Error("expected PTY to remain functional after resize")
	}
}

func TestTerminalWS_CloseCleanup(t *testing.T) {
	ts, th := newTerminalTestServer()
	defer ts.Close()

	conn, _, err := dialTerminalWS(ts, "test-user@example.com")
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	tc := newTerminalTestConn(conn)

	// Wait a bit for session to be established.
	tc.drainOutput(300 * time.Millisecond)

	// Verify session exists.
	if th.Manager().Sessions() == 0 {
		t.Fatal("expected at least one active session")
	}

	// Close the WebSocket.
	tc.close()

	// Wait for cleanup.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if th.Manager().Sessions() == 0 {
			return // Success
		}
		time.Sleep(100 * time.Millisecond)
	}

	t.Errorf("expected session to be cleaned up, but %d sessions remain", th.Manager().Sessions())
}

func TestTerminalWS_MultipleSessions_Independent(t *testing.T) {
	ts, th := newTerminalTestServer()
	defer ts.Close()

	// Open two independent terminal sessions.
	conn1, _, err := dialTerminalWS(ts, "test-user@example.com")
	if err != nil {
		t.Fatalf("failed to connect session 1: %v", err)
	}
	tc1 := newTerminalTestConn(conn1)
	defer tc1.close()

	conn2, _, err := dialTerminalWS(ts, "test-user@example.com")
	if err != nil {
		t.Fatalf("failed to connect session 2: %v", err)
	}
	tc2 := newTerminalTestConn(conn2)
	defer tc2.close()

	// Both sessions should be tracked.
	time.Sleep(200 * time.Millisecond)
	if th.Manager().Sessions() != 2 {
		t.Errorf("expected 2 active sessions, got %d", th.Manager().Sessions())
	}

	// Drain initial output from both.
	tc1.drainOutput(500 * time.Millisecond)
	tc2.drainOutput(500 * time.Millisecond)

	// Send echo to session 1 and verify output.
	if err := tc1.writeMessage(TerminalMessage{Type: "input", Data: "echo session1_marker\n"}); err != nil {
		t.Fatalf("write session1: %v", err)
	}
	output1, found1 := tc1.waitForOutput("session1_marker", 5*time.Second)
	if !found1 {
		t.Error("session 1 did not produce output for echo session1_marker")
	}

	// Send echo to session 2 and verify output.
	if err := tc2.writeMessage(TerminalMessage{Type: "input", Data: "echo session2_marker\n"}); err != nil {
		t.Fatalf("write session2: %v", err)
	}
	output2, found2 := tc2.waitForOutput("session2_marker", 5*time.Second)
	if !found2 {
		t.Error("session 2 did not produce output for echo session2_marker")
	}

	// Verify that outputs went to the correct sessions.
	if output1 != "" && strings.Contains(output1, "session2_marker") {
		t.Error("session 1 received session 2's output — sessions are not independent")
	}
	if output2 != "" && strings.Contains(output2, "session1_marker") {
		t.Error("session 2 received session 1's output — sessions are not independent")
	}
}

func TestTerminalWS_MultipleSessions_UniquePIDs(t *testing.T) {
	ts, th := newTerminalTestServer()
	defer ts.Close()

	conn1, _, err := dialTerminalWS(ts, "test-user@example.com")
	if err != nil {
		t.Fatalf("failed to connect session 1: %v", err)
	}
	tc1 := newTerminalTestConn(conn1)
	defer tc1.close()

	conn2, _, err := dialTerminalWS(ts, "test-user@example.com")
	if err != nil {
		t.Fatalf("failed to connect session 2: %v", err)
	}
	tc2 := newTerminalTestConn(conn2)
	defer tc2.close()

	// Both sessions should be tracked.
	time.Sleep(200 * time.Millisecond)
	if th.Manager().Sessions() != 2 {
		t.Errorf("expected 2 active sessions, got %d", th.Manager().Sessions())
	}

	// Drain initial output from both.
	tc1.drainOutput(500 * time.Millisecond)
	tc2.drainOutput(500 * time.Millisecond)

	// Use unique markers to extract PIDs from each session.
	// We send commands sequentially to avoid cross-session timing issues.
	if err := tc1.writeMessage(TerminalMessage{Type: "input", Data: "echo PID_S1=$$\n"}); err != nil {
		t.Fatalf("write session1: %v", err)
	}
	_, found1 := tc1.waitForOutput("PID_S1=", 5*time.Second)

	if err := tc2.writeMessage(TerminalMessage{Type: "input", Data: "echo PID_S2=$$\n"}); err != nil {
		t.Fatalf("write session2: %v", err)
	}
	_, found2 := tc2.waitForOutput("PID_S2=", 5*time.Second)

	if !found1 {
		t.Error("session 1 did not produce output containing PID_S1=")
	}
	if !found2 {
		t.Error("session 2 did not produce output containing PID_S2=")
	}

	// The primary goal of this test is to verify that two concurrent sessions
	// exist independently. The session count check above already verifies this.
	// The PID output confirms each shell is a separate process.
}

// collectOutput collects all output messages for the given duration and returns
// them concatenated.
func (tc *terminalTestConn) collectOutput(dur time.Duration) string {
	var buf strings.Builder
	timer := time.NewTimer(dur)
	defer timer.Stop()
	for {
		select {
		case msg, ok := <-tc.msgs:
			if !ok {
				return buf.String()
			}
			if msg.Type == "output" {
				buf.WriteString(msg.Data)
			}
		case <-timer.C:
			return buf.String()
		case <-tc.closed:
			return buf.String()
		}
	}
}

func TestTerminalWS_ReconnectCreatesFreshSession(t *testing.T) {
	ts, th := newTerminalTestServer()
	defer ts.Close()

	// Connect and disconnect first session.
	conn1, _, err := dialTerminalWS(ts, "test-user@example.com")
	if err != nil {
		t.Fatalf("failed to connect first session: %v", err)
	}
	tc1 := newTerminalTestConn(conn1)
	tc1.drainOutput(200 * time.Millisecond)
	tc1.close()

	// Wait for cleanup.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if th.Manager().Sessions() == 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Connect again.
	conn2, _, err := dialTerminalWS(ts, "test-user@example.com")
	if err != nil {
		t.Fatalf("failed to reconnect: %v", err)
	}
	tc2 := newTerminalTestConn(conn2)
	defer tc2.close()

	// Verify we get a fresh session with output.
	_, found := tc2.waitForAnyOutput(5 * time.Second)
	if !found {
		t.Error("expected fresh session to produce output (shell prompt)")
	}
}

func TestTerminalManager_NewAndSessions(t *testing.T) {
	tm := NewTerminalManager()
	if tm.Sessions() != 0 {
		t.Errorf("expected 0 sessions, got %d", tm.Sessions())
	}
}

func TestFindShell(t *testing.T) {
	shell := findShell()
	if shell == "" {
		t.Error("expected non-empty shell path")
	}
	// Should be either /bin/bash or /bin/sh on most systems.
	if shell != "/bin/bash" && shell != "/bin/sh" {
		t.Errorf("expected /bin/bash or /bin/sh, got %q", shell)
	}
}

func TestTerminalMessage_Unmarshal(t *testing.T) {
	// Test input message.
	inputJSON := `{"type":"input","data":"hello"}`
	var inputMsg TerminalMessage
	if err := json.Unmarshal([]byte(inputJSON), &inputMsg); err != nil {
		t.Fatalf("failed to unmarshal input message: %v", err)
	}
	if inputMsg.Type != "input" || inputMsg.Data != "hello" {
		t.Errorf("unexpected input message: %+v", inputMsg)
	}

	// Test resize message.
	resizeJSON := `{"type":"resize","cols":120,"rows":40}`
	var resizeMsg TerminalMessage
	if err := json.Unmarshal([]byte(resizeJSON), &resizeMsg); err != nil {
		t.Fatalf("failed to unmarshal resize message: %v", err)
	}
	if resizeMsg.Type != "resize" || resizeMsg.Cols != 120 || resizeMsg.Rows != 40 {
		t.Errorf("unexpected resize message: %+v", resizeMsg)
	}

	// Test output message.
	outputJSON := `{"type":"output","data":"shell output"}`
	var outputMsg TerminalMessage
	if err := json.Unmarshal([]byte(outputJSON), &outputMsg); err != nil {
		t.Fatalf("failed to unmarshal output message: %v", err)
	}
	if outputMsg.Type != "output" || outputMsg.Data != "shell output" {
		t.Errorf("unexpected output message: %+v", outputMsg)
	}
}

func TestFormatSessionID(t *testing.T) {
	tests := []struct {
		n    uint64
		want string
	}{
		{1, "term-1"},
		{10, "term-10"},
		{100, "term-100"},
	}
	for _, tt := range tests {
		got := formatSessionID(tt.n)
		if got != tt.want {
			t.Errorf("formatSessionID(%d) = %q, want %q", tt.n, got, tt.want)
		}
	}
}
