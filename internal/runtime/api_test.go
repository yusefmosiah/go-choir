package runtime

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusefmosiah/go-choir/internal/events"
	"github.com/yusefmosiah/go-choir/internal/store"
	"github.com/yusefmosiah/go-choir/internal/types"
)

// testAPISetup creates a fresh Runtime and APIHandler for HTTP handler tests.
func testAPISetup(t *testing.T) (*Runtime, *APIHandler) {
	t.Helper()

	dir := filepath.Join(os.TempDir(), "go-choir-m3-api-test")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	dbPath := filepath.Join(dir, t.Name()+".db")
	_ = os.Remove(dbPath)

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	bus := events.NewEventBus()
	provider := NewStubProvider(50 * time.Millisecond)
	cfg := Config{
		SandboxID:           "sandbox-test",
		StorePath:           dbPath,
		ProviderTimeout:     50 * time.Millisecond,
		SupervisionInterval: 1 * time.Hour,
	}

	rt := New(cfg, s, bus, provider)
	handler := NewAPIHandler(rt)

	// Stop the runtime before closing the store to avoid "database is
	// closed" log noise from in-flight goroutines.
	t.Cleanup(func() {
		rt.Stop()
		_ = s.Close()
		_ = os.Remove(dbPath)
	})

	return rt, handler
}

// authenticatedRequest creates an HTTP request with the X-Authenticated-User
// header set, simulating the proxy's user-context injection.
func authenticatedRequest(method, path, body, user string) *http.Request {
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	if user != "" {
		req.Header.Set("X-Authenticated-User", user)
	}
	return req
}

// --- Task Submission Tests ---

func TestHandleTaskSubmissionReturnsStableHandle(t *testing.T) {
	// VAL-RUNTIME-003: accepted task submission returns a stable handle.
	_, handler := testAPISetup(t)

	body := `{"prompt":"explain closures in Go"}`
	req := authenticatedRequest(http.MethodPost, "/api/agent/task", body, "user-alice")
	w := httptest.NewRecorder()

	handler.HandleTaskSubmission(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status: got %d, want %d", w.Code, http.StatusAccepted)
	}

	var resp taskSubmitResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.TaskID == "" {
		t.Error("task_id should not be empty")
	}
	if resp.State != types.TaskPending {
		t.Errorf("state: got %q, want %q", resp.State, types.TaskPending)
	}
	if resp.OwnerID != "user-alice" {
		t.Errorf("owner_id: got %q, want user-alice", resp.OwnerID)
	}
}

func TestHandleTaskSubmissionPreservesMetadata(t *testing.T) {
	rt, handler := testAPISetup(t)

	body := `{"prompt":"route this into conductor","metadata":{"agent_profile":"conductor","agent_role":"conductor","input_source":"prompt_bar","requested_app":"vtext"}}`
	req := authenticatedRequest(http.MethodPost, "/api/agent/task", body, "user-alice")
	w := httptest.NewRecorder()

	handler.HandleTaskSubmission(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status: got %d, want %d", w.Code, http.StatusAccepted)
	}

	var resp taskSubmitResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	rec, err := rt.GetTask(context.Background(), resp.TaskID, "user-alice")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}

	if got, _ := rec.Metadata["agent_profile"].(string); got != AgentProfileConductor {
		t.Fatalf("agent_profile: got %q, want %q", got, AgentProfileConductor)
	}
	if got, _ := rec.Metadata["agent_role"].(string); got != AgentProfileConductor {
		t.Fatalf("agent_role: got %q, want %q", got, AgentProfileConductor)
	}
	if got, _ := rec.Metadata["input_source"].(string); got != "prompt_bar" {
		t.Fatalf("input_source: got %q, want prompt_bar", got)
	}
	if got, _ := rec.Metadata["requested_app"].(string); got != AgentProfileVText {
		t.Fatalf("requested_app: got %q, want %q", got, AgentProfileVText)
	}
}

func TestHandleTaskSubmissionAuthGated(t *testing.T) {
	// VAL-RUNTIME-002: task submission is auth-gated.
	_, handler := testAPISetup(t)

	// Request without auth header.
	body := `{"prompt":"test prompt"}`
	req := authenticatedRequest(http.MethodPost, "/api/agent/task", body, "")
	w := httptest.NewRecorder()

	handler.HandleTaskSubmission(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestHandleTaskSubmissionMethodNotAllowed(t *testing.T) {
	_, handler := testAPISetup(t)

	req := authenticatedRequest(http.MethodGet, "/api/agent/task", "", "user-alice")
	w := httptest.NewRecorder()

	handler.HandleTaskSubmission(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

func TestHandleTaskSubmissionEmptyPrompt(t *testing.T) {
	_, handler := testAPISetup(t)

	body := `{"prompt":""}`
	req := authenticatedRequest(http.MethodPost, "/api/agent/task", body, "user-alice")
	w := httptest.NewRecorder()

	handler.HandleTaskSubmission(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestHandleTaskSubmissionInvalidBody(t *testing.T) {
	_, handler := testAPISetup(t)

	req := authenticatedRequest(http.MethodPost, "/api/agent/task", "not json", "user-alice")
	w := httptest.NewRecorder()

	handler.HandleTaskSubmission(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusBadRequest)
	}
}

// --- Task Status Tests ---

func TestHandleTaskStatusReturnsCorrelatedHandle(t *testing.T) {
	// VAL-RUNTIME-004: status is correlated to the submitted handle.
	rt, handler := testAPISetup(t)

	rec, err := rt.SubmitTask(context.Background(), "test prompt", "user-alice")
	if err != nil {
		t.Fatalf("submit task: %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	req := authenticatedRequest(http.MethodGet,
		fmt.Sprintf("/api/agent/status?task_id=%s", rec.TaskID), "", "user-alice")
	w := httptest.NewRecorder()

	handler.HandleTaskStatus(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", w.Code, http.StatusOK)
	}

	var resp taskStatusResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.TaskID != rec.TaskID {
		t.Errorf("task_id: got %q, want %q", resp.TaskID, rec.TaskID)
	}
	if resp.State != types.TaskCompleted {
		t.Errorf("state: got %q, want %q", resp.State, types.TaskCompleted)
	}
	if resp.Result == "" {
		t.Error("result should not be empty for completed task")
	}
}

func TestHandleTaskStatusAuthGated(t *testing.T) {
	// VAL-RUNTIME-006: status is auth-gated.
	_, handler := testAPISetup(t)

	req := authenticatedRequest(http.MethodGet, "/api/agent/status?task_id=test", "", "")
	w := httptest.NewRecorder()

	handler.HandleTaskStatus(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestHandleTaskStatusCallerScoped(t *testing.T) {
	// VAL-RUNTIME-006: status is caller-scoped (user cannot see other users' tasks).
	rt, handler := testAPISetup(t)

	rec, err := rt.SubmitTask(context.Background(), "alice task", "user-alice")
	if err != nil {
		t.Fatalf("submit task: %v", err)
	}

	// Eve tries to see Alice's task.
	req := authenticatedRequest(http.MethodGet,
		fmt.Sprintf("/api/agent/status?task_id=%s", rec.TaskID), "", "user-eve")
	w := httptest.NewRecorder()

	handler.HandleTaskStatus(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status: got %d, want %d (caller-scoped denial)", w.Code, http.StatusNotFound)
	}
}

func TestHandleTaskStatusMissingTaskID(t *testing.T) {
	_, handler := testAPISetup(t)

	req := authenticatedRequest(http.MethodGet, "/api/agent/status", "", "user-alice")
	w := httptest.NewRecorder()

	handler.HandleTaskStatus(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestHandleTaskStatusNotFound(t *testing.T) {
	_, handler := testAPISetup(t)

	req := authenticatedRequest(http.MethodGet, "/api/agent/status?task_id=nonexistent", "", "user-alice")
	w := httptest.NewRecorder()

	handler.HandleTaskStatus(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestHandleTaskStatusFailedOutcome(t *testing.T) {
	// VAL-RUNTIME-004: status exposes non-happy-path outcomes.
	dir := filepath.Join(os.TempDir(), "go-choir-m3-api-test")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	dbPath := filepath.Join(dir, t.Name()+".db")
	_ = os.Remove(dbPath)

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	bus := events.NewEventBus()
	provider := &StubProvider{
		Delay:   10 * time.Millisecond,
		FailErr: errors.New("provider timeout"),
	}

	cfg := Config{
		SandboxID:           "sandbox-test",
		StorePath:           dbPath,
		ProviderTimeout:     10 * time.Millisecond,
		SupervisionInterval: 1 * time.Hour,
	}

	rt := New(cfg, s, bus, provider)
	handler := NewAPIHandler(rt)

	t.Cleanup(func() {
		rt.Stop()
		_ = s.Close()
		_ = os.Remove(dbPath)
	})

	rec, err := rt.SubmitTask(context.Background(), "failing prompt", "user-alice")
	if err != nil {
		t.Fatalf("submit task: %v", err)
	}

	// Wait for the task to fail.
	time.Sleep(200 * time.Millisecond)

	req := authenticatedRequest(http.MethodGet,
		fmt.Sprintf("/api/agent/status?task_id=%s", rec.TaskID), "", "user-alice")
	w := httptest.NewRecorder()

	handler.HandleTaskStatus(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", w.Code, http.StatusOK)
	}

	var resp taskStatusResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.State != types.TaskFailed {
		t.Errorf("state: got %q, want %q", resp.State, types.TaskFailed)
	}
	if resp.Error == "" {
		t.Error("error should not be empty for failed task")
	}
}

// --- Task Status By Path ID Tests (VAL-CHOIR-002, VAL-CHOIR-005) ---
// GET /api/agent/{id}/status

func TestHandleTaskStatusByIDReturnsTaskRecord(t *testing.T) {
	// VAL-CHOIR-002: GET /api/agent/{id}/status returns task record.
	rt, handler := testAPISetup(t)

	rec, err := rt.SubmitTask(context.Background(), "test status by id", "user-alice")
	if err != nil {
		t.Fatalf("submit task: %v", err)
	}

	// Wait for task to complete.
	time.Sleep(200 * time.Millisecond)

	req := authenticatedRequest(http.MethodGet,
		fmt.Sprintf("/api/agent/%s/status", rec.TaskID), "", "user-alice")
	w := httptest.NewRecorder()

	handler.HandleTaskStatusByID(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", w.Code, http.StatusOK)
	}

	var resp taskStatusResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	// Response includes all required fields (VAL-CHOIR-002).
	if resp.TaskID != rec.TaskID {
		t.Errorf("task_id: got %q, want %q", resp.TaskID, rec.TaskID)
	}
	if resp.OwnerID != "user-alice" {
		t.Errorf("owner_id: got %q, want user-alice", resp.OwnerID)
	}
	if resp.State == "" {
		t.Error("state should not be empty")
	}
	if resp.Prompt == "" {
		t.Error("prompt should not be empty")
	}
	if resp.CreatedAt == "" {
		t.Error("created_at should not be empty")
	}
	if resp.UpdatedAt == "" {
		t.Error("updated_at should not be empty")
	}
}

func TestHandleTaskStatusByIDCompletedResult(t *testing.T) {
	// VAL-CHOIR-005: completed task has result and finished_at.
	rt, handler := testAPISetup(t)

	rec, err := rt.SubmitTask(context.Background(), "result check prompt", "user-alice")
	if err != nil {
		t.Fatalf("submit task: %v", err)
	}

	// Wait for task to complete.
	time.Sleep(200 * time.Millisecond)

	req := authenticatedRequest(http.MethodGet,
		fmt.Sprintf("/api/agent/%s/status", rec.TaskID), "", "user-alice")
	w := httptest.NewRecorder()

	handler.HandleTaskStatusByID(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", w.Code, http.StatusOK)
	}

	var resp taskStatusResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.State != types.TaskCompleted {
		t.Errorf("state: got %q, want %q", resp.State, types.TaskCompleted)
	}
	if resp.Result == "" {
		t.Error("result should not be empty for completed task (VAL-CHOIR-005)")
	}
	if resp.FinishedAt == nil || *resp.FinishedAt == "" {
		t.Error("finished_at should be set for completed task (VAL-CHOIR-005)")
	}
}

func TestHandleTaskStatusByIDAuthGated(t *testing.T) {
	// VAL-CHOIR-002: unauthenticated request returns 401.
	_, handler := testAPISetup(t)

	req := authenticatedRequest(http.MethodGet, "/api/agent/some-id/status", "", "")
	w := httptest.NewRecorder()

	handler.HandleTaskStatusByID(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestHandleTaskStatusByIDCallerScoped(t *testing.T) {
	// VAL-CHOIR-002: 404 for task owned by different user (403 in spec,
	// but we use 404 to prevent IDOR probing — same as query-param handler).
	rt, handler := testAPISetup(t)

	rec, err := rt.SubmitTask(context.Background(), "alice private task", "user-alice")
	if err != nil {
		t.Fatalf("submit task: %v", err)
	}

	// Eve tries to see Alice's task.
	req := authenticatedRequest(http.MethodGet,
		fmt.Sprintf("/api/agent/%s/status", rec.TaskID), "", "user-eve")
	w := httptest.NewRecorder()

	handler.HandleTaskStatusByID(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status: got %d, want %d (caller-scoped denial)", w.Code, http.StatusNotFound)
	}
}

func TestHandleTaskStatusByIDNotFound(t *testing.T) {
	// VAL-CHOIR-002: 404 for non-existent task.
	_, handler := testAPISetup(t)

	req := authenticatedRequest(http.MethodGet,
		"/api/agent/nonexistent-task-id/status", "", "user-alice")
	w := httptest.NewRecorder()

	handler.HandleTaskStatusByID(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestHandleTaskStatusByIDFailedOutcome(t *testing.T) {
	// VAL-CHOIR-002: status exposes error information for failed tasks.
	dir := filepath.Join(os.TempDir(), "go-choir-m3-api-test")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	dbPath := filepath.Join(dir, t.Name()+".db")
	_ = os.Remove(dbPath)

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	bus := events.NewEventBus()
	provider := &StubProvider{
		Delay:   10 * time.Millisecond,
		FailErr: errors.New("provider timeout for by-id test"),
	}

	cfg := Config{
		SandboxID:           "sandbox-test",
		StorePath:           dbPath,
		ProviderTimeout:     10 * time.Millisecond,
		SupervisionInterval: 1 * time.Hour,
	}

	rt := New(cfg, s, bus, provider)
	handler := NewAPIHandler(rt)

	t.Cleanup(func() {
		rt.Stop()
		_ = s.Close()
		_ = os.Remove(dbPath)
	})

	rec, err := rt.SubmitTask(context.Background(), "failing by-id prompt", "user-alice")
	if err != nil {
		t.Fatalf("submit task: %v", err)
	}

	// Wait for the task to fail.
	time.Sleep(200 * time.Millisecond)

	req := authenticatedRequest(http.MethodGet,
		fmt.Sprintf("/api/agent/%s/status", rec.TaskID), "", "user-alice")
	w := httptest.NewRecorder()

	handler.HandleTaskStatusByID(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", w.Code, http.StatusOK)
	}

	var resp taskStatusResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.State != types.TaskFailed {
		t.Errorf("state: got %q, want %q", resp.State, types.TaskFailed)
	}
	if resp.Error == "" {
		t.Error("error should not be empty for failed task")
	}
	if resp.FinishedAt == nil || *resp.FinishedAt == "" {
		t.Error("finished_at should be set for failed task")
	}
}

func TestHandleTaskStatusByIDMethodNotAllowed(t *testing.T) {
	// Only GET is allowed.
	_, handler := testAPISetup(t)

	req := authenticatedRequest(http.MethodPost, "/api/agent/some-id/status", "", "user-alice")
	w := httptest.NewRecorder()

	handler.HandleTaskStatusByID(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

func TestHandleTaskStatusByIDSpawnedChildTask(t *testing.T) {
	// VAL-CHOIR-002: status works for spawned child tasks too.
	rt, handler := testAPISetup(t)

	// Create a parent task first.
	parent, err := rt.SubmitTask(context.Background(), "parent task", "user-alice")
	if err != nil {
		t.Fatalf("submit parent task: %v", err)
	}

	// Spawn a child task.
	child, err := rt.SpawnTask(context.Background(), parent.TaskID, "child objective", "user-alice", nil)
	if err != nil {
		t.Fatalf("spawn child task: %v", err)
	}

	// Wait for the child task to complete.
	time.Sleep(200 * time.Millisecond)

	req := authenticatedRequest(http.MethodGet,
		fmt.Sprintf("/api/agent/%s/status", child.TaskID), "", "user-alice")
	w := httptest.NewRecorder()

	handler.HandleTaskStatusByID(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", w.Code, http.StatusOK)
	}

	var resp taskStatusResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.TaskID != child.TaskID {
		t.Errorf("task_id: got %q, want %q", resp.TaskID, child.TaskID)
	}
	if resp.State == "" {
		t.Error("state should not be empty")
	}
	// Verify metadata includes parent_id.
	if resp.Metadata == nil {
		t.Error("metadata should not be nil for spawned task")
	} else if pid, _ := resp.Metadata["parent_id"].(string); pid != parent.TaskID {
		t.Errorf("metadata.parent_id: got %q, want %q", pid, parent.TaskID)
	}
}

func TestHandleTaskStatusByIDStateTransitions(t *testing.T) {
	// VAL-CHOIR-002: state transitions reflected in status.
	// Verify that status shows different states as the task progresses.
	rt, handler := testAPISetup(t)

	rec, err := rt.SubmitTask(context.Background(), "state transition test", "user-alice")
	if err != nil {
		t.Fatalf("submit task: %v", err)
	}

	// Immediately check — should be at least pending (may already be running).
	req := authenticatedRequest(http.MethodGet,
		fmt.Sprintf("/api/agent/%s/status", rec.TaskID), "", "user-alice")
	w := httptest.NewRecorder()
	handler.HandleTaskStatusByID(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("initial status: got %d, want %d", w.Code, http.StatusOK)
	}

	var initialResp taskStatusResponse
	if err := json.NewDecoder(w.Body).Decode(&initialResp); err != nil {
		t.Fatalf("decode initial response: %v", err)
	}

	// The initial state should be pending or running.
	if initialResp.State != types.TaskPending && initialResp.State != types.TaskRunning && initialResp.State != types.TaskCompleted {
		t.Errorf("initial state: got %q, want pending/running/completed", initialResp.State)
	}

	// Wait for task to complete.
	time.Sleep(200 * time.Millisecond)

	req2 := authenticatedRequest(http.MethodGet,
		fmt.Sprintf("/api/agent/%s/status", rec.TaskID), "", "user-alice")
	w2 := httptest.NewRecorder()
	handler.HandleTaskStatusByID(w2, req2)

	var finalResp taskStatusResponse
	if err := json.NewDecoder(w2.Body).Decode(&finalResp); err != nil {
		t.Fatalf("decode final response: %v", err)
	}

	if finalResp.State != types.TaskCompleted {
		t.Errorf("final state: got %q, want %q", finalResp.State, types.TaskCompleted)
	}

	// UpdatedAt should be >= CreatedAt.
	if finalResp.UpdatedAt < finalResp.CreatedAt {
		t.Errorf("updated_at %q should be >= created_at %q", finalResp.UpdatedAt, finalResp.CreatedAt)
	}
}

// --- Events Tests ---

func TestHandleEventsAuthGated(t *testing.T) {
	// VAL-RUNTIME-006: events are auth-gated.
	_, handler := testAPISetup(t)

	req := authenticatedRequest(http.MethodGet, "/api/events", "", "")
	w := httptest.NewRecorder()

	handler.HandleEvents(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestHandleEventsReturnsSSEStream(t *testing.T) {
	// VAL-RUNTIME-005: events stream is long-lived and incremental.
	rt, handler := testAPISetup(t)

	// Start the SSE connection in a goroutine.
	req := authenticatedRequest(http.MethodGet, "/api/events", "", "user-alice")
	req = req.WithContext(context.Background())
	w := httptest.NewRecorder()

	// SSE is a long-lived connection; we need to run it in a goroutine
	// and signal when we're done reading.
	done := make(chan struct{})
	go func() {
		handler.HandleEvents(w, req)
		close(done)
	}()

	// Submit a task to generate events.
	_, err := rt.SubmitTask(context.Background(), "test prompt for events", "user-alice")
	if err != nil {
		t.Fatalf("submit task: %v", err)
	}

	// Wait a bit for events to be emitted.
	time.Sleep(100 * time.Millisecond)

	// Read the response body so far.
	body := w.Body.String()

	// The response should have SSE headers.
	ct := w.Header().Get("Content-Type")
	if ct != "text/event-stream" {
		t.Errorf("content-type: got %q, want text/event-stream", ct)
	}

	// The body should contain at least one SSE data line.
	if !strings.Contains(body, "data: ") {
		t.Errorf("expected SSE data line in body, got: %s", body)
	}

	// Verify the SSE data contains event records.
	lines := strings.Split(body, "\n")
	var foundSubmitted bool
	for _, line := range lines {
		if strings.HasPrefix(line, "data: ") {
			var ev types.EventRecord
			data := strings.TrimPrefix(line, "data: ")
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				continue // skip malformed lines
			}
			if ev.Kind == types.EventTaskSubmitted && ev.OwnerID == "user-alice" {
				foundSubmitted = true
			}
		}
	}
	if !foundSubmitted {
		t.Error("expected task.submitted event in SSE stream")
	}
}

func TestHandleEventsCallerScoped(t *testing.T) {
	// VAL-RUNTIME-006: events are caller-scoped.
	rt, handler := testAPISetup(t)

	// Submit a task for alice.
	_, err := rt.SubmitTask(context.Background(), "alice task", "user-alice")
	if err != nil {
		t.Fatalf("submit task: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	// Connect as bob — should not see alice's events.
	req := authenticatedRequest(http.MethodGet, "/api/events", "", "user-bob")
	req = req.WithContext(context.Background())
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		handler.HandleEvents(w, req)
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)

	body := w.Body.String()

	// Bob should not see any events for alice's task.
	lines := strings.Split(body, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "data: ") {
			var ev types.EventRecord
			data := strings.TrimPrefix(line, "data: ")
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				continue
			}
			if ev.OwnerID == "user-alice" {
				t.Errorf("bob should not see alice's events: %+v", ev)
			}
		}
	}
}

func TestHandleEventsIncremental(t *testing.T) {
	// VAL-RUNTIME-005: events arrive incrementally, not buffered.
	rt, handler := testAPISetup(t)

	req := authenticatedRequest(http.MethodGet, "/api/events", "", "user-alice")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()

	go func() {
		handler.HandleEvents(w, req)
	}()

	// Submit a task — should generate events incrementally.
	_, err := rt.SubmitTask(context.Background(), "incremental test", "user-alice")
	if err != nil {
		t.Fatalf("submit task: %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	body := w.Body.String()

	// Parse SSE events and check that multiple different kinds arrived.
	kinds := make(map[types.EventKind]bool)
	scanner := bufio.NewScanner(strings.NewReader(body))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			var ev types.EventRecord
			data := strings.TrimPrefix(line, "data: ")
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				continue
			}
			kinds[ev.Kind] = true
		}
	}

	// Should see at least submitted + started (incremental, not buffered).
	if !kinds[types.EventTaskSubmitted] {
		t.Error("expected task.submitted event")
	}
	if !kinds[types.EventTaskStarted] {
		t.Error("expected task.started event (arrived incrementally)")
	}
}

// --- Health Tests ---

func TestHandleHealthReady(t *testing.T) {
	// VAL-RUNTIME-001: health reflects runtime readiness.
	_, handler := testAPISetup(t)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()

	handler.HandleHealth(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusOK)
	}

	var resp runtimeHealthResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.Status != "ready" {
		t.Errorf("status: got %q, want ready", resp.Status)
	}
	if resp.RuntimeHealth != types.HealthReady {
		t.Errorf("runtime_health: got %q, want ready", resp.RuntimeHealth)
	}
	if resp.SandboxID != "sandbox-test" {
		t.Errorf("sandbox_id: got %q, want sandbox-test", resp.SandboxID)
	}
	if resp.ActiveProvider != "stub" {
		t.Errorf("active_provider: got %q, want stub (default test provider)", resp.ActiveProvider)
	}
}

func TestHandleHealthDegraded(t *testing.T) {
	// VAL-RUNTIME-001: degraded state is surfaced.
	rt, handler := testAPISetup(t)

	rt.SetHealth(types.HealthDegraded)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()

	handler.HandleHealth(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want %d (degraded is still serving)", w.Code, http.StatusOK)
	}

	var resp runtimeHealthResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.Status != "degraded" {
		t.Errorf("status: got %q, want degraded", resp.Status)
	}
	if resp.RuntimeHealth != types.HealthDegraded {
		t.Errorf("runtime_health: got %q, want degraded", resp.RuntimeHealth)
	}
}

func TestHandleHealthFailed(t *testing.T) {
	// VAL-RUNTIME-001: failed state is surfaced with 503.
	rt, handler := testAPISetup(t)

	rt.SetHealth(types.HealthFailed)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()

	handler.HandleHealth(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusServiceUnavailable)
	}

	var resp runtimeHealthResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.RuntimeHealth != types.HealthFailed {
		t.Errorf("runtime_health: got %q, want failed", resp.RuntimeHealth)
	}
}

func TestHandleHealthReflectsRunningTasks(t *testing.T) {
	_, handler := testAPISetup(t)
	rt := handler.rt

	// No tasks running initially.
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	handler.HandleHealth(w, req)

	var resp runtimeHealthResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.RunningTasks != 0 {
		t.Errorf("running_tasks: got %d, want 0", resp.RunningTasks)
	}

	// Submit a task.
	_, err := rt.SubmitTask(context.Background(), "running task", "user-alice")
	if err != nil {
		t.Fatalf("submit task: %v", err)
	}

	w = httptest.NewRecorder()
	handler.HandleHealth(w, req)

	var resp2 runtimeHealthResponse
	if err := json.NewDecoder(w.Body).Decode(&resp2); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp2.RunningTasks < 1 {
		t.Errorf("running_tasks: got %d, want >= 1", resp2.RunningTasks)
	}
}

func TestHandleTopologyReportsOrchestrationShape(t *testing.T) {
	rt, handler := testAPISetup(t)
	rt.cfg.ResearcherCount = 5

	if _, err := rt.ChannelManager().Channel("parent-1"); err != nil {
		t.Fatalf("create parent channel: %v", err)
	}
	if _, err := rt.ChannelManager().Channel("child-1"); err != nil {
		t.Fatalf("create child channel: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/agent/topology", nil)
	w := httptest.NewRecorder()

	handler.HandleTopology(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", w.Code, http.StatusOK)
	}

	var resp runtimeTopologyResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.ResearcherCount != 5 {
		t.Errorf("researcher_count: got %d, want 5", resp.ResearcherCount)
	}
	if resp.ChannelCount != 2 {
		t.Errorf("channel_count: got %d, want 2", resp.ChannelCount)
	}
	if resp.SupervisionInterval != "1h0m0s" {
		t.Errorf("supervision_interval: got %q, want 1h0m0s", resp.SupervisionInterval)
	}
}

func TestHandleVTextDocumentsRootAliasesEtext(t *testing.T) {
	_, handler := testAPISetup(t)

	createReqBody := `{"title":"vtext alias doc","content":"hello"}`
	createReq := authenticatedRequest(http.MethodPost, "/api/vtext/documents", createReqBody, "user-alice")
	createW := httptest.NewRecorder()
	handler.HandleVTextDocumentsRoot(createW, createReq)

	if createW.Code != http.StatusCreated {
		t.Fatalf("create status: got %d, want %d", createW.Code, http.StatusCreated)
	}

	var createResp etextCreateDocResponse
	if err := json.NewDecoder(createW.Body).Decode(&createResp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if createResp.DocID == "" {
		t.Fatal("doc_id should not be empty")
	}

	listReq := authenticatedRequest(http.MethodGet, "/api/vtext/documents", "", "user-alice")
	listW := httptest.NewRecorder()
	handler.HandleVTextDocumentsRoot(listW, listReq)

	if listW.Code != http.StatusOK {
		t.Fatalf("list status: got %d, want %d", listW.Code, http.StatusOK)
	}

	var listResp etextListDocsResponse
	if err := json.NewDecoder(listW.Body).Decode(&listResp); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(listResp.Documents) != 1 {
		t.Fatalf("documents: got %d, want 1", len(listResp.Documents))
	}
	if listResp.Documents[0].Title != "vtext alias doc" {
		t.Errorf("title: got %q, want %q", listResp.Documents[0].Title, "vtext alias doc")
	}
}

// --- Supervisor Recovery Visibility Tests ---

func TestSupervisorRecoveryVisible(t *testing.T) {
	// VAL-RUNTIME-009: supervisor recovery is externally visible.
	rt, _ := testAPISetup(t)

	// Subscribe to events.
	ch := rt.EventBus().Subscribe()
	defer rt.EventBus().Unsubscribe(ch)

	// Manually degrade and then recover the runtime.
	rt.SetHealth(types.HealthDegraded)

	// Should see degraded event.
	select {
	case ev := <-ch:
		if ev.Record.Kind != types.EventRuntimeDegraded {
			t.Errorf("event kind: got %q, want %q", ev.Record.Kind, types.EventRuntimeDegraded)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for degraded event")
	}

	// Recover to ready.
	rt.SetHealth(types.HealthReady)

	// Should see health event.
	select {
	case ev := <-ch:
		if ev.Record.Kind != types.EventRuntimeHealth {
			t.Errorf("event kind: got %q, want %q", ev.Record.Kind, types.EventRuntimeHealth)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for health event")
	}
}

func TestProviderFailureDoesNotCrashRuntime(t *testing.T) {
	// VAL-RUNTIME-008: provider failures surface without crashing the runtime.
	// Submit a failing task, verify the runtime still accepts new tasks.
	dir := filepath.Join(os.TempDir(), "go-choir-m3-api-test")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	dbPath := filepath.Join(dir, t.Name()+".db")
	_ = os.Remove(dbPath)

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	bus := events.NewEventBus()
	failProvider := &StubProvider{
		Delay:   10 * time.Millisecond,
		FailErr: errors.New("provider connection refused"),
	}

	cfg := Config{
		SandboxID:           "sandbox-test",
		StorePath:           dbPath,
		ProviderTimeout:     10 * time.Millisecond,
		SupervisionInterval: 1 * time.Hour,
	}

	rt := New(cfg, s, bus, failProvider)
	handler := NewAPIHandler(rt)

	t.Cleanup(func() {
		rt.Stop()
		_ = s.Close()
		_ = os.Remove(dbPath)
	})

	// Submit the failing task.
	body := `{"prompt":"will fail"}`
	req := authenticatedRequest(http.MethodPost, "/api/agent/task", body, "user-alice")
	w := httptest.NewRecorder()
	handler.HandleTaskSubmission(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status: got %d, want %d", w.Code, http.StatusAccepted)
	}

	// Wait for failure.
	time.Sleep(200 * time.Millisecond)

	// Check the failed task status.
	var submitResp taskSubmitResponse
	if err := json.NewDecoder(w.Body).Decode(&submitResp); err != nil {
		t.Fatalf("decode submit response: %v", err)
	}

	statusReq := authenticatedRequest(http.MethodGet,
		fmt.Sprintf("/api/agent/status?task_id=%s", submitResp.TaskID), "", "user-alice")
	statusW := httptest.NewRecorder()
	handler.HandleTaskStatus(statusW, statusReq)

	if statusW.Code != http.StatusOK {
		t.Fatalf("status code: got %d, want %d", statusW.Code, http.StatusOK)
	}

	var statusResp taskStatusResponse
	if err := json.NewDecoder(statusW.Body).Decode(&statusResp); err != nil {
		t.Fatalf("decode status response: %v", err)
	}

	if statusResp.State != types.TaskFailed {
		t.Errorf("state: got %q, want %q", statusResp.State, types.TaskFailed)
	}

	// The runtime should still accept new tasks.
	newBody := `{"prompt":"after failure"}`
	newReq := authenticatedRequest(http.MethodPost, "/api/agent/task", newBody, "user-alice")
	newW := httptest.NewRecorder()

	// Replace the provider with a working one for the new task.
	rt.provider = NewStubProvider(50 * time.Millisecond)

	handler.HandleTaskSubmission(newW, newReq)

	if newW.Code != http.StatusAccepted {
		t.Errorf("status after failure: got %d, want %d", newW.Code, http.StatusAccepted)
	}
}

// --- AuthenticateUser Tests ---

func TestAuthenticateUserMissing(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/agent/status", nil)
	_, err := authenticateUser(req)
	if err == nil {
		t.Error("expected error for missing auth header")
	}
}

func TestAuthenticateUserPresent(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/agent/status", nil)
	req.Header.Set("X-Authenticated-User", "user-alice")

	user, err := authenticateUser(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if user != "user-alice" {
		t.Errorf("user: got %q, want user-alice", user)
	}
}

// --- Provider Bridge Health Visibility ---

func TestHandleHealthReportsBridgeProvider(t *testing.T) {
	// When a bridge provider is active, the health endpoint should report
	// its name (e.g., "bedrock" or "zai") instead of "stub", so operators
	// can distinguish real-provider paths from canned responses.

	dir := filepath.Join(os.TempDir(), "go-choir-m3-api-test")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	dbPath := filepath.Join(dir, t.Name()+".db")
	_ = os.Remove(dbPath)

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	bus := events.NewEventBus()

	// Use a mock bridge provider instead of the stub.
	bridge := &mockBridgeProvider{name: "bedrock", result: "test"}

	cfg := Config{
		SandboxID:           "sandbox-test",
		StorePath:           dbPath,
		ProviderTimeout:     50 * time.Millisecond,
		SupervisionInterval: 1 * time.Hour,
	}

	rt := New(cfg, s, bus, bridge)
	handler := NewAPIHandler(rt)

	t.Cleanup(func() {
		rt.Stop()
		_ = s.Close()
		_ = os.Remove(dbPath)
	})

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	handler.HandleHealth(w, req)

	var resp runtimeHealthResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.ActiveProvider != "bedrock" {
		t.Errorf("active_provider: got %q, want bedrock", resp.ActiveProvider)
	}
}
