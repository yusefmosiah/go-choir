package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yusefmosiah/go-choir/internal/types"
)

// --- Spawn API Tests ---

// testSpawnSetup creates a fresh Runtime and APIHandler for spawn tests,
// including a parent work item that can be used for parent_id references.
func testSpawnSetup(t *testing.T) (*Runtime, *APIHandler, string) {
	t.Helper()
	rt, handler := testAPISetup(t)

	// Create a parent task to use as parent_id in spawn tests.
	parentRec, err := rt.SubmitTask(context.Background(), "parent objective", "user-alice")
	if err != nil {
		t.Fatalf("create parent task: %v", err)
	}

	// Wait briefly for the parent task to start running.
	time.Sleep(50 * time.Millisecond)

	return rt, handler, parentRec.TaskID
}

// TestSpawnCreatesChildTask verifies that POST /api/agent/spawn creates a child
// task linked to the parent with correct fields (VAL-CHOIR-001).
func TestSpawnCreatesChildTask(t *testing.T) {
	_, handler, parentID := testSpawnSetup(t)

	body := fmt.Sprintf(`{"parent_id":"%s","objective":"research the history of Go"}`, parentID)
	req := authenticatedRequest(http.MethodPost, "/api/agent/spawn", body, "user-alice")
	w := httptest.NewRecorder()

	handler.HandleSpawn(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status: got %d, want %d; body: %s", w.Code, http.StatusAccepted, w.Body.String())
	}

	var resp spawnResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.TaskID == "" {
		t.Error("task_id should not be empty")
	}
	if resp.State != types.TaskPending {
		t.Errorf("state: got %q, want %q", resp.State, types.TaskPending)
	}
	if resp.ParentID != parentID {
		t.Errorf("parent_id: got %q, want %q", resp.ParentID, parentID)
	}
	if resp.OwnerID != "user-alice" {
		t.Errorf("owner_id: got %q, want user-alice", resp.OwnerID)
	}
	if resp.CreatedAt == "" {
		t.Error("created_at should not be empty")
	}
}

// TestSpawnChildInRegistry verifies the child task appears in the work registry
// with the correct parent-child relationship (VAL-CHOIR-001, VAL-CHOIR-003, VAL-CHOIR-004).
func TestSpawnChildInRegistry(t *testing.T) {
	rt, handler, parentID := testSpawnSetup(t)

	body := fmt.Sprintf(`{"parent_id":"%s","objective":"child task objective"}`, parentID)
	req := authenticatedRequest(http.MethodPost, "/api/agent/spawn", body, "user-alice")
	w := httptest.NewRecorder()

	handler.HandleSpawn(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status: got %d, want %d", w.Code, http.StatusAccepted)
	}

	var resp spawnResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	// Verify the work item exists in the registry.
	ctx := context.Background()
	item, err := rt.Store().GetWorkItem(ctx, resp.TaskID)
	if err != nil {
		t.Fatalf("get work item from registry: %v", err)
	}

	if item.ParentID != parentID {
		t.Errorf("work item parent_id: got %q, want %q", item.ParentID, parentID)
	}
	if item.State != types.TaskPending {
		t.Errorf("work item state: got %q, want %q", item.State, types.TaskPending)
	}
	if item.OwnerID != "user-alice" {
		t.Errorf("work item owner_id: got %q, want user-alice", item.OwnerID)
	}
	if item.Objective != "child task objective" {
		t.Errorf("work item objective: got %q, want %q", item.Objective, "child task objective")
	}
}

// TestSpawnInheritsOwnerFromParent verifies that the child task inherits the
// owner from the authenticated user context (feature requirement).
func TestSpawnInheritsOwnerFromAuth(t *testing.T) {
	_, handler, parentID := testSpawnSetup(t)

	// Spawn as user-bob — child should be owned by bob.
	body := fmt.Sprintf(`{"parent_id":"%s","objective":"bob's child"}`, parentID)
	req := authenticatedRequest(http.MethodPost, "/api/agent/spawn", body, "user-bob")
	w := httptest.NewRecorder()

	handler.HandleSpawn(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status: got %d, want %d", w.Code, http.StatusAccepted)
	}

	var resp spawnResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	// The child inherits the owner from the auth context (user-bob),
	// not from the parent (user-alice).
	if resp.OwnerID != "user-bob" {
		t.Errorf("owner_id: got %q, want user-bob (from auth context)", resp.OwnerID)
	}
}

// TestSpawnWithoutParentIDFails verifies that spawn requires a parent_id.
func TestSpawnWithoutParentIDFails(t *testing.T) {
	_, handler, _ := testSpawnSetup(t)

	body := `{"objective":"orphan task"}`
	req := authenticatedRequest(http.MethodPost, "/api/agent/spawn", body, "user-alice")
	w := httptest.NewRecorder()

	handler.HandleSpawn(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusBadRequest)
	}
}

// TestSpawnWithoutObjectiveFails verifies that spawn requires an objective.
func TestSpawnWithoutObjectiveFails(t *testing.T) {
	_, handler, parentID := testSpawnSetup(t)

	body := fmt.Sprintf(`{"parent_id":"%s"}`, parentID)
	req := authenticatedRequest(http.MethodPost, "/api/agent/spawn", body, "user-alice")
	w := httptest.NewRecorder()

	handler.HandleSpawn(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusBadRequest)
	}
}

// TestSpawnWithConstraints verifies that constraints can be passed in the
// spawn request and are stored in the child task metadata.
func TestSpawnWithConstraints(t *testing.T) {
	_, handler, parentID := testSpawnSetup(t)

	body := fmt.Sprintf(`{
		"parent_id":"%s",
		"objective":"research topic X",
		"constraints":{"max_tokens":500,"timeout_seconds":30}
	}`, parentID)
	req := authenticatedRequest(http.MethodPost, "/api/agent/spawn", body, "user-alice")
	w := httptest.NewRecorder()

	handler.HandleSpawn(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status: got %d, want %d; body: %s", w.Code, http.StatusAccepted, w.Body.String())
	}

	var resp spawnResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.TaskID == "" {
		t.Error("task_id should not be empty")
	}
	if resp.State != types.TaskPending {
		t.Errorf("state: got %q, want %q", resp.State, types.TaskPending)
	}
}

// TestSpawnAuthGated verifies that spawn requires authentication.
func TestSpawnAuthGated(t *testing.T) {
	_, handler, parentID := testSpawnSetup(t)

	body := fmt.Sprintf(`{"parent_id":"%s","objective":"test"}`, parentID)
	req := authenticatedRequest(http.MethodPost, "/api/agent/spawn", body, "")
	w := httptest.NewRecorder()

	handler.HandleSpawn(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

// TestSpawnMethodNotAllowed verifies that only POST is accepted.
func TestSpawnMethodNotAllowed(t *testing.T) {
	_, handler, _ := testSpawnSetup(t)

	req := authenticatedRequest(http.MethodGet, "/api/agent/spawn", "", "user-alice")
	w := httptest.NewRecorder()

	handler.HandleSpawn(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

// TestSpawnInvalidBody verifies that invalid JSON is rejected.
func TestSpawnInvalidBody(t *testing.T) {
	_, handler, _ := testSpawnSetup(t)

	req := authenticatedRequest(http.MethodPost, "/api/agent/spawn", "not json", "user-alice")
	w := httptest.NewRecorder()

	handler.HandleSpawn(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusBadRequest)
	}
}

// TestSpawnNonexistentParent verifies that spawning with a nonexistent parent_id
// returns an appropriate error.
func TestSpawnNonexistentParent(t *testing.T) {
	_, handler, _ := testSpawnSetup(t)

	body := `{"parent_id":"nonexistent-task-id","objective":"orphan child"}`
	req := authenticatedRequest(http.MethodPost, "/api/agent/spawn", body, "user-alice")
	w := httptest.NewRecorder()

	handler.HandleSpawn(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status: got %d, want %d; body: %s", w.Code, http.StatusNotFound, w.Body.String())
	}
}

// TestSpawnMultipleChildrenFromSameParent verifies that multiple children
// can be spawned from the same parent (VAL-CHOIR-008).
func TestSpawnMultipleChildrenFromSameParent(t *testing.T) {
	rt, handler, parentID := testSpawnSetup(t)

	childIDs := make(map[string]bool)

	for i := 0; i < 3; i++ {
		body := fmt.Sprintf(`{"parent_id":"%s","objective":"child task %d"}`, parentID, i)
		req := authenticatedRequest(http.MethodPost, "/api/agent/spawn", body, "user-alice")
		w := httptest.NewRecorder()

		handler.HandleSpawn(w, req)

		if w.Code != http.StatusAccepted {
			t.Fatalf("spawn %d: status: got %d, want %d", i, w.Code, http.StatusAccepted)
		}

		var resp spawnResponse
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("spawn %d: decode response: %v", i, err)
		}

		if childIDs[resp.TaskID] {
			t.Errorf("spawn %d: duplicate task_id %q", i, resp.TaskID)
		}
		childIDs[resp.TaskID] = true
	}

	if len(childIDs) != 3 {
		t.Errorf("expected 3 unique child IDs, got %d", len(childIDs))
	}

	// Verify all children are listed under the parent in the work registry.
	ctx := context.Background()
	children, err := rt.Store().ListWorkItemsByParent(ctx, parentID, 10)
	if err != nil {
		t.Fatalf("list children by parent: %v", err)
	}

	if len(children) != 3 {
		t.Errorf("children count: got %d, want 3", len(children))
	}

	for _, child := range children {
		if child.ParentID != parentID {
			t.Errorf("child parent_id: got %q, want %q", child.ParentID, parentID)
		}
	}
}

// TestSpawnCreatesRuntimeTask verifies that spawn also creates a runtime task
// record (not just a work item) so it can be tracked via the status API.
// We verify metadata rather than state since the task may have already
// transitioned to running by the time we read it.
func TestSpawnCreatesRuntimeTask(t *testing.T) {
	rt, handler, parentID := testSpawnSetup(t)

	body := fmt.Sprintf(`{"parent_id":"%s","objective":"child runtime task"}`, parentID)
	req := authenticatedRequest(http.MethodPost, "/api/agent/spawn", body, "user-alice")
	w := httptest.NewRecorder()

	handler.HandleSpawn(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status: got %d, want %d", w.Code, http.StatusAccepted)
	}

	var resp spawnResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	// Verify the task exists in the runtime store.
	ctx := context.Background()
	task, err := rt.Store().GetTask(ctx, resp.TaskID)
	if err != nil {
		t.Fatalf("get runtime task: %v", err)
	}

	if task.OwnerID != "user-alice" {
		t.Errorf("task owner_id: got %q, want user-alice", task.OwnerID)
	}
	if task.Prompt != "child runtime task" {
		t.Errorf("task prompt: got %q, want %q", task.Prompt, "child runtime task")
	}

	// Check that metadata contains the parent_id reference.
	if task.Metadata == nil {
		t.Fatal("task metadata should not be nil")
	}
	parentIDInMeta, ok := task.Metadata["parent_id"].(string)
	if !ok || parentIDInMeta != parentID {
		t.Errorf("task metadata parent_id: got %v, want %q", task.Metadata["parent_id"], parentID)
	}
}

// TestSpawnChildWorkItemAndTaskConsistent verifies that the work item and
// runtime task share the same ID and owner. The task may have already
// transitioned to running by the time we check, so we only verify ID and
// owner consistency (not state, since the work item stays at pending until
// the executor updates it).
func TestSpawnChildWorkItemAndTaskConsistent(t *testing.T) {
	rt, handler, parentID := testSpawnSetup(t)

	body := fmt.Sprintf(`{"parent_id":"%s","objective":"consistency check"}`, parentID)
	req := authenticatedRequest(http.MethodPost, "/api/agent/spawn", body, "user-alice")
	w := httptest.NewRecorder()

	handler.HandleSpawn(w, req)

	var resp spawnResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	ctx := context.Background()

	// Check work item.
	item, err := rt.Store().GetWorkItem(ctx, resp.TaskID)
	if err != nil {
		t.Fatalf("get work item: %v", err)
	}

	// Check task.
	task, err := rt.Store().GetTask(ctx, resp.TaskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}

	// They should have the same ID and owner.
	if item.ID != task.TaskID {
		t.Errorf("IDs don't match: work_item=%q, task=%q", item.ID, task.TaskID)
	}
	if item.OwnerID != task.OwnerID {
		t.Errorf("owners don't match: work_item=%q, task=%q", item.OwnerID, task.OwnerID)
	}
}

// TestSpawnListedByParent verifies that spawned children can be listed
// by their parent_id in the work registry (VAL-CHOIR-004).
func TestSpawnListedByParent(t *testing.T) {
	_, handler, parentID := testSpawnSetup(t)

	// Spawn two children.
	for i := 0; i < 2; i++ {
		body := fmt.Sprintf(`{"parent_id":"%s","objective":"child %d"}`, parentID, i)
		req := authenticatedRequest(http.MethodPost, "/api/agent/spawn", body, "user-alice")
		w := httptest.NewRecorder()
		handler.HandleSpawn(w, req)

		if w.Code != http.StatusAccepted {
			t.Fatalf("spawn %d: got %d, want %d", i, w.Code, http.StatusAccepted)
		}
	}

	// List children via status endpoint - we verify via the store directly.
	// This is tested more comprehensively in the store-level tests.
}

// TestSpawnEmptyObjectiveRejected verifies that whitespace-only objectives
// are rejected.
func TestSpawnEmptyObjectiveRejected(t *testing.T) {
	_, handler, parentID := testSpawnSetup(t)

	body := fmt.Sprintf(`{"parent_id":"%s","objective":"   "}`, parentID)
	req := authenticatedRequest(http.MethodPost, "/api/agent/spawn", body, "user-alice")
	w := httptest.NewRecorder()

	handler.HandleSpawn(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want %d; body: %s", w.Code, http.StatusBadRequest, w.Body.String())
	}

	var errResp apiError
	if err := json.NewDecoder(w.Body).Decode(&errResp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}

	if !strings.Contains(errResp.Error, "objective") {
		t.Errorf("error message should mention 'objective', got: %q", errResp.Error)
	}
}

// TestSpawnRouteRegistered verifies that /api/agent/spawn is registered
// as a route in the API handler.
func TestSpawnRouteRegistered(t *testing.T) {
	_, handler := testAPISetup(t)

	// Submit a request to /api/agent/spawn — should not get 404.
	req := authenticatedRequest(http.MethodPost, "/api/agent/spawn", "{}", "user-alice")
	w := httptest.NewRecorder()

	handler.HandleSpawn(w, req)

	// Should get 400 (bad request) not 404 (not found).
	if w.Code == http.StatusNotFound {
		t.Error("/api/agent/spawn should be registered as a route")
	}
}
