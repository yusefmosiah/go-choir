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

	"github.com/yusefmosiah/go-choir/internal/events"
	"github.com/yusefmosiah/go-choir/internal/store"
	"github.com/yusefmosiah/go-choir/internal/types"
)

// --- Worker Failure Isolation Tests (VAL-CHOIR-009, VAL-CHOIR-010) ---
//
// These tests verify that worker failures are isolated: a failing child
// worker does not crash the parent task, other sibling workers, or the
// runtime itself. Parents receive error notifications and can continue.
//
// Feature requirements:
//
//   - Worker failure sends error message to parent
//   - Parent task continues running (not crashed)
//   - Error includes task_id and error message
//   - Parent can spawn replacement worker if needed
//   - Other sibling workers unaffected by one failure
//   - Failed task transitions to failed state with error details
//   - task.failed event emitted with error details
//   - Runtime health remains ready or degraded (not failed)
//   - Parent can cancel running child tasks (VAL-CHOIR-010)
//   - Cancelled task transitions to cancelled state
//   - task.cancelled event emitted

// failureIsolationSetup creates a fresh Runtime with a configurable provider
// for testing failure scenarios.
func failureIsolationSetup(t *testing.T, provider Provider) (*Runtime, *APIHandler, string) {
	t.Helper()

	dir := t.TempDir()
	dbPath := fmt.Sprintf("%s/%s.db", dir, t.Name())

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	bus := events.NewEventBus()
	cfg := Config{
		SandboxID:           "sandbox-failure-test",
		StorePath:           dbPath,
		ProviderTimeout:     500 * time.Millisecond,
		SupervisionInterval: 1 * time.Hour,
	}

	rt := New(cfg, s, bus, provider)
	handler := NewAPIHandler(rt)

	t.Cleanup(func() {
		rt.Stop()
		_ = s.Close()
	})

	// Create a parent task that stays running for the duration of the test.
	parentRec, err := rt.SubmitTask(context.Background(), "parent task for isolation tests", "user-alice")
	if err != nil {
		t.Fatalf("create parent task: %v", err)
	}

	// Wait for parent to start running.
	time.Sleep(50 * time.Millisecond)

	return rt, handler, parentRec.TaskID
}

// waitForTaskState polls until the task reaches a terminal state or times out.
func waitForTaskState(t *testing.T, rt *Runtime, taskID string, timeout time.Duration) types.TaskRecord {
	t.Helper()
	ctx := context.Background()
	deadline := time.After(timeout)
	for {
		select {
		case <-deadline:
			task, _ := rt.Store().GetTask(ctx, taskID)
			t.Fatalf("timeout waiting for task %s (state=%s)", taskID[:8], task.State)
		default:
		}
		task, err := rt.Store().GetTask(ctx, taskID)
		if err != nil {
			t.Fatalf("get task %s: %v", taskID, err)
		}
		if task.State.Terminal() {
			return task
		}
		time.Sleep(30 * time.Millisecond)
	}
}

// --- VAL-CHOIR-009: Worker Failure Isolation ---

// TestFailureIsolation_FailedWorkerSendsErrorToParent verifies that when a
// child worker fails, the parent receives an error notification via the
// channel system (VAL-CHOIR-009, expected behavior #1).
func TestFailureIsolation_FailedWorkerSendsErrorToParent(t *testing.T) {
	provider := &StubProvider{
		Delay:   10 * time.Millisecond,
		FailErr: fmt.Errorf("simulated worker failure: invalid tool invocation"),
	}
	rt, _, parentID := failureIsolationSetup(t, provider)
	ctx := context.Background()

	// Spawn a child that will fail.
	child, err := rt.SpawnTask(ctx, parentID, "execute invalid command", "user-alice", nil)
	if err != nil {
		t.Fatalf("spawn child: %v", err)
	}

	// Wait for the child to reach a terminal state.
	task := waitForTaskState(t, rt, child.TaskID, 10*time.Second)

	// Verify the child is in failed state.
	if task.State != types.TaskFailed {
		t.Fatalf("child state: got %q, want failed", task.State)
	}

	// Verify the error message is populated.
	if task.Error == "" {
		t.Fatal("child error should not be empty")
	}
	if !strings.Contains(task.Error, "simulated worker failure") {
		t.Errorf("child error: got %q, want to contain 'simulated worker failure'", task.Error)
	}

	// Verify error message posted to parent channel.
	msgs, _, err := rt.ChannelRead(parentID, 0)
	if err != nil {
		t.Fatalf("parent channel read: %v", err)
	}

	found := false
	for _, msg := range msgs {
		if msg.From == child.TaskID && msg.Role == "error" {
			found = true
			if !strings.Contains(msg.Content, "simulated worker failure") {
				t.Errorf("error message content: got %q, want to contain 'simulated worker failure'", msg.Content)
			}
			break
		}
	}
	if !found {
		t.Error("no error message found in parent channel from failed child")
	}
}

// TestFailureIsolation_ParentContinuesRunning verifies that when a child
// worker fails, the parent task continues running (not crashed)
// (VAL-CHOIR-009, expected behavior #2).
func TestFailureIsolation_ParentContinuesRunning(t *testing.T) {
	provider := &StubProvider{
		Delay:   10 * time.Millisecond,
		FailErr: fmt.Errorf("child failure"),
	}
	rt, _, parentID := failureIsolationSetup(t, provider)
	ctx := context.Background()

	// Spawn a child that will fail.
	child, err := rt.SpawnTask(ctx, parentID, "failing objective", "user-alice", nil)
	if err != nil {
		t.Fatalf("spawn child: %v", err)
	}

	// Wait for child to finish (fail).
	waitForTaskState(t, rt, child.TaskID, 10*time.Second)

	// Verify the parent is still functional by spawning another child.
	child2, err := rt.SpawnTask(ctx, parentID, "second objective after failure", "user-alice", nil)
	if err != nil {
		t.Fatalf("spawn second child after first failure: %v", err)
	}

	// Wait for second child to finish (will also fail, but that's fine).
	waitForTaskState(t, rt, child2.TaskID, 10*time.Second)

	// The fact that we could spawn a second child proves the parent is
	// still operational and the runtime didn't crash.
}

// TestFailureIsolation_ErrorIncludesTaskIDAndMessage verifies that the error
// notification includes both the task_id and the error message
// (VAL-CHOIR-009, expected behavior #3).
func TestFailureIsolation_ErrorIncludesTaskIDAndMessage(t *testing.T) {
	provider := &StubProvider{
		Delay:   10 * time.Millisecond,
		FailErr: fmt.Errorf("detailed error: connection refused to upstream service"),
	}
	rt, _, parentID := failureIsolationSetup(t, provider)
	ctx := context.Background()

	child, err := rt.SpawnTask(ctx, parentID, "task with specific error", "user-alice", nil)
	if err != nil {
		t.Fatalf("spawn child: %v", err)
	}

	task := waitForTaskState(t, rt, child.TaskID, 10*time.Second)

	// Verify task record has both task_id and error.
	if task.TaskID != child.TaskID {
		t.Errorf("task_id: got %q, want %q", task.TaskID, child.TaskID)
	}
	if task.Error == "" {
		t.Fatal("error field should not be empty")
	}
	if !strings.Contains(task.Error, "connection refused") {
		t.Errorf("error field: got %q, want to contain 'connection refused'", task.Error)
	}

	// Verify work item also has the error.
	item, err := rt.Store().GetWorkItem(ctx, child.TaskID)
	if err != nil {
		t.Fatalf("get work item: %v", err)
	}
	if item.Error == "" {
		t.Error("work item error should not be empty")
	}
}

// TestFailureIsolation_ParentCanSpawnReplacementWorker verifies that after a
// child failure, the parent can spawn a replacement worker
// (VAL-CHOIR-009, expected behavior #4).
func TestFailureIsolation_ParentCanSpawnReplacementWorker(t *testing.T) {
	// First child fails, second succeeds. Use the conditionalFailProvider
	// which fails tasks containing "fail" in the prompt.
	provider := &conditionalFailProvider{
		delay:      20 * time.Millisecond,
		failPrefix: "fail",
		result:     "Replacement worker completed successfully.",
	}
	rt, _, parentID := failureIsolationSetup(t, provider)
	ctx := context.Background()

	// Spawn a child that will fail.
	child1, err := rt.SpawnTask(ctx, parentID, "fail: first attempt", "user-alice", nil)
	if err != nil {
		t.Fatalf("spawn first child: %v", err)
	}

	// Wait for it to fail.
	task1 := waitForTaskState(t, rt, child1.TaskID, 10*time.Second)
	if task1.State != types.TaskFailed {
		t.Fatalf("first child state: got %q, want failed", task1.State)
	}

	// Spawn a replacement worker that will succeed.
	child2, err := rt.SpawnTask(ctx, parentID, "replacement attempt", "user-alice", nil)
	if err != nil {
		t.Fatalf("spawn replacement child: %v", err)
	}

	// Wait for replacement to complete (should succeed).
	task2 := waitForTaskState(t, rt, child2.TaskID, 10*time.Second)
	if task2.State != types.TaskCompleted {
		t.Fatalf("replacement child state: got %q, want completed", task2.State)
	}

	// Verify replacement produced a result.
	if task2.Result == "" {
		t.Error("replacement child should have a result")
	}

	// Verify both child notifications in parent channel.
	msgs, _, err := rt.ChannelRead(parentID, 0)
	if err != nil {
		t.Fatalf("parent channel read: %v", err)
	}

	errorFound := false
	resultFound := false
	for _, msg := range msgs {
		if msg.From == child1.TaskID && msg.Role == "error" {
			errorFound = true
		}
		if msg.From == child2.TaskID && msg.Role == "result" {
			resultFound = true
		}
	}

	if !errorFound {
		t.Error("parent should have received error from first child")
	}
	if !resultFound {
		t.Error("parent should have received result from replacement child")
	}
}

// TestFailureIsolation_SiblingWorkersUnaffected verifies that when one child
// worker fails, other sibling workers continue running unaffected
// (VAL-CHOIR-009, expected behavior #5).
func TestFailureIsolation_SiblingWorkersUnaffected(t *testing.T) {
	provider := &conditionalFailProvider{
		delay:      50 * time.Millisecond,
		failPrefix: "fail",
		result:     "Task completed successfully.",
	}
	rt, _, parentID := failureIsolationSetup(t, provider)
	ctx := context.Background()

	// Spawn 3 children: 2 succeed, 1 fails.
	succeed1, _ := rt.SpawnTask(ctx, parentID, "analyze data", "user-alice", nil)
	fail, _ := rt.SpawnTask(ctx, parentID, "fail this task on purpose", "user-alice", nil)
	succeed2, _ := rt.SpawnTask(ctx, parentID, "summarize results", "user-alice", nil)

	// Wait for all to reach terminal state.
	taskS1 := waitForTaskState(t, rt, succeed1.TaskID, 10*time.Second)
	taskF := waitForTaskState(t, rt, fail.TaskID, 10*time.Second)
	taskS2 := waitForTaskState(t, rt, succeed2.TaskID, 10*time.Second)

	// Verify the failing child failed.
	if taskF.State != types.TaskFailed {
		t.Errorf("failing child state: got %q, want failed", taskF.State)
	}

	// Verify the succeeding children completed.
	if taskS1.State != types.TaskCompleted {
		t.Errorf("succeeding child 1 state: got %q, want completed", taskS1.State)
	}
	if taskS2.State != types.TaskCompleted {
		t.Errorf("succeeding child 2 state: got %q, want completed", taskS2.State)
	}

	// Verify results are present for successful children.
	if taskS1.Result == "" {
		t.Error("succeeding child 1 should have a result")
	}
	if taskS2.Result == "" {
		t.Error("succeeding child 2 should have a result")
	}

	// Verify error message for failed child.
	if taskF.Error == "" {
		t.Error("failing child should have an error message")
	}
}

// TestFailureIsolation_RuntimeHealthRemainsReady verifies that the runtime
// health remains ready or degraded (not failed) after a worker failure
// (VAL-CHOIR-009 pass condition).
func TestFailureIsolation_RuntimeHealthRemainsReady(t *testing.T) {
	provider := &StubProvider{
		Delay:   10 * time.Millisecond,
		FailErr: fmt.Errorf("worker failure for health test"),
	}
	rt, _, parentID := failureIsolationSetup(t, provider)
	ctx := context.Background()

	// Spawn a failing child.
	child, _ := rt.SpawnTask(ctx, parentID, "fail task", "user-alice", nil)
	waitForTaskState(t, rt, child.TaskID, 10*time.Second)

	// Check runtime health.
	health := rt.HealthState()
	if health == types.HealthFailed {
		t.Errorf("runtime health should not be 'failed' after a single worker failure, got %q", health)
	}

	// The runtime should still be able to accept new tasks.
	child2, err := rt.SpawnTask(ctx, parentID, "post-failure task", "user-alice", nil)
	if err != nil {
		t.Fatalf("runtime should accept new tasks after worker failure: %v", err)
	}
	waitForTaskState(t, rt, child2.TaskID, 10*time.Second)
}

// TestFailureIsolation_TaskFailedEventEmitted verifies that a task.failed event
// is emitted when a worker fails (VAL-CHOIR-009 pass condition).
func TestFailureIsolation_TaskFailedEventEmitted(t *testing.T) {
	provider := &StubProvider{
		Delay:   10 * time.Millisecond,
		FailErr: fmt.Errorf("emit test failure"),
	}
	rt, _, parentID := failureIsolationSetup(t, provider)
	ctx := context.Background()

	// Subscribe to events before spawning.
	bus := rt.EventBus()
	ch := bus.SubscribeWithBuffer(128)
	defer bus.Unsubscribe(ch)

	// Spawn a failing child.
	child, _ := rt.SpawnTask(ctx, parentID, "fail for event test", "user-alice", nil)
	waitForTaskState(t, rt, child.TaskID, 10*time.Second)

	// Check for task.failed event.
	found := false
	timeout := time.After(3 * time.Second)
	for !found {
		select {
		case <-timeout:
			t.Fatal("timeout waiting for task.failed event")
		case ev := <-ch:
			if ev.Record.Kind == types.EventTaskFailed && ev.Record.TaskID == child.TaskID {
				found = true
				// Verify the event has error details in the payload.
				var payload map[string]string
				if err := json.Unmarshal(ev.Record.Payload, &payload); err == nil {
					if payload["error"] == "" {
						t.Error("task.failed event payload should contain error details")
					}
				}
			}
		default:
			// Drain remaining events.
			select {
			case <-ch:
			default:
				time.Sleep(50 * time.Millisecond)
			}
		}
	}
}

// TestFailureIsolation_WorkItemUpdatedOnFailure verifies that the work item
// in the registry is updated to failed state when the task fails
// (VAL-CHOIR-009, related to VAL-CHOIR-001).
func TestFailureIsolation_WorkItemUpdatedOnFailure(t *testing.T) {
	provider := &StubProvider{
		Delay:   10 * time.Millisecond,
		FailErr: fmt.Errorf("work item failure test"),
	}
	rt, _, parentID := failureIsolationSetup(t, provider)
	ctx := context.Background()

	child, _ := rt.SpawnTask(ctx, parentID, "work item fail task", "user-alice", nil)
	waitForTaskState(t, rt, child.TaskID, 10*time.Second)

	// Check the work item.
	item, err := rt.Store().GetWorkItem(ctx, child.TaskID)
	if err != nil {
		t.Fatalf("get work item: %v", err)
	}

	if item.State != types.TaskFailed {
		t.Errorf("work item state: got %q, want failed", item.State)
	}
	if item.Error == "" {
		t.Error("work item error should not be empty")
	}
	if item.ParentID != parentID {
		t.Errorf("work item parent_id: got %q, want %q", item.ParentID, parentID)
	}
}

// TestFailureIsolation_APIStatusReturnsFailedState verifies that the status
// API returns the failed state with error details for a failed worker
// (VAL-CHOIR-009, verification steps).
func TestFailureIsolation_APIStatusReturnsFailedState(t *testing.T) {
	provider := &StubProvider{
		Delay:   10 * time.Millisecond,
		FailErr: fmt.Errorf("api status test failure"),
	}
	_, handler, parentID := failureIsolationSetup(t, provider)

	// Spawn a child that will fail.
	body := fmt.Sprintf(`{"parent_id":"%s","objective":"execute invalid command"}`, parentID)
	req := authenticatedRequest(http.MethodPost, "/api/agent/spawn", body, "user-alice")
	w := httptest.NewRecorder()
	handler.HandleSpawn(w, req)

	var spawnResp spawnResponse
	if err := json.NewDecoder(w.Body).Decode(&spawnResp); err != nil {
		t.Fatalf("decode spawn response: %v", err)
	}

	// Wait for the task to complete (fail).
	time.Sleep(200 * time.Millisecond)

	// Check status via API.
	req = authenticatedRequest(http.MethodGet, "/api/agent/status?task_id="+spawnResp.TaskID, "", "user-alice")
	w = httptest.NewRecorder()
	handler.HandleTaskStatus(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status API: got %d, want 200", w.Code)
	}

	var statusResp taskStatusResponse
	if err := json.NewDecoder(w.Body).Decode(&statusResp); err != nil {
		t.Fatalf("decode status response: %v", err)
	}

	if statusResp.State != types.TaskFailed {
		t.Errorf("task state: got %q, want failed", statusResp.State)
	}
	if statusResp.Error == "" {
		t.Error("task error should not be empty")
	}
}

// TestFailureIsolation_HealthEndpointRemainsHealthy verifies that the /health
// endpoint reports ready/degraded status after a worker failure, not failed
// (VAL-CHOIR-009 pass condition).
func TestFailureIsolation_HealthEndpointRemainsHealthy(t *testing.T) {
	provider := &StubProvider{
		Delay:   10 * time.Millisecond,
		FailErr: fmt.Errorf("health endpoint failure test"),
	}
	_, handler, parentID := failureIsolationSetup(t, provider)

	// Spawn a failing child via API.
	body := fmt.Sprintf(`{"parent_id":"%s","objective":"fail for health"}`, parentID)
	req := authenticatedRequest(http.MethodPost, "/api/agent/spawn", body, "user-alice")
	w := httptest.NewRecorder()
	handler.HandleSpawn(w, req)

	// Wait for failure.
	time.Sleep(200 * time.Millisecond)

	// Check health endpoint.
	req = httptest.NewRequest(http.MethodGet, "/health", nil)
	w = httptest.NewRecorder()
	handler.HandleHealth(w, req)

	// Health should not be 503 (Service Unavailable).
	if w.Code == http.StatusServiceUnavailable {
		t.Error("health endpoint should not return 503 after a single worker failure")
	}

	var resp runtimeHealthResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode health response: %v", err)
	}

	if resp.Status == "failed" {
		t.Error("runtime status should not be 'failed' after a single worker failure")
	}
}

// TestFailureIsolation_MultipleFailuresDontCrashRuntime verifies that
// multiple consecutive worker failures don't crash the runtime
// (VAL-CHOIR-009 extended).
func TestFailureIsolation_MultipleFailuresDontCrashRuntime(t *testing.T) {
	provider := &StubProvider{
		Delay:   10 * time.Millisecond,
		FailErr: fmt.Errorf("repeated failure"),
	}
	rt, _, parentID := failureIsolationSetup(t, provider)
	ctx := context.Background()

	// Spawn 5 failing children in sequence.
	for i := 0; i < 5; i++ {
		child, err := rt.SpawnTask(ctx, parentID, fmt.Sprintf("failure %d", i), "user-alice", nil)
		if err != nil {
			t.Fatalf("spawn child %d: %v", i, err)
		}
		task := waitForTaskState(t, rt, child.TaskID, 10*time.Second)
		if task.State != types.TaskFailed {
			t.Errorf("child %d: got %q, want failed", i, task.State)
		}
	}

	// The runtime should still be operational.
	// Switch to a succeeding provider and verify new tasks complete.
	// (We can't switch providers mid-test, so verify that the runtime
	// accepted all tasks and they all failed as expected.)
	health := rt.HealthState()
	// Health may be degraded after multiple failures, but not crashed.
	if health == types.HealthFailed {
		t.Error("runtime should not be in 'failed' state even after multiple worker failures")
	}
}

// TestFailureIsolation_ConcurrentFailuresAndSuccesses verifies that when
// multiple workers run concurrently and some fail while others succeed,
// results are correctly separated (VAL-CHOIR-009, VAL-CHOIR-008).
func TestFailureIsolation_ConcurrentFailuresAndSuccesses(t *testing.T) {
	provider := &conditionalFailProvider{
		delay:      50 * time.Millisecond,
		failPrefix: "fail",
		result:     "Completed successfully.",
	}
	rt, _, parentID := failureIsolationSetup(t, provider)
	ctx := context.Background()

	// Spawn 5 children: 3 succeed, 2 fail.
	ids := make([]string, 5)
	objectives := []string{
		"research topic A",
		"fail task B",
		"analyze data C",
		"fail task D",
		"summarize E",
	}

	for i, obj := range objectives {
		rec, err := rt.SpawnTask(ctx, parentID, obj, "user-alice", nil)
		if err != nil {
			t.Fatalf("spawn child %d: %v", i, err)
		}
		ids[i] = rec.TaskID
	}

	// Wait for all to complete.
	for i, id := range ids {
		task := waitForTaskState(t, rt, id, 10*time.Second)
		expectedState := types.TaskCompleted
		if strings.Contains(objectives[i], "fail") {
			expectedState = types.TaskFailed
		}
		if task.State != expectedState {
			t.Errorf("child %d (%q): state got %q, want %q", i, objectives[i], task.State, expectedState)
		}
	}

	// Verify parent channel has both results and errors.
	msgs, _, err := rt.ChannelRead(parentID, 0)
	if err != nil {
		t.Fatalf("parent channel read: %v", err)
	}

	resultCount := 0
	errorCount := 0
	for _, msg := range msgs {
		if msg.Role == "result" {
			resultCount++
		} else if msg.Role == "error" {
			errorCount++
		}
	}

	if resultCount != 3 {
		t.Errorf("result messages: got %d, want 3", resultCount)
	}
	if errorCount != 2 {
		t.Errorf("error messages: got %d, want 2", errorCount)
	}
}

// TestFailureIsolation_ParentResponsiveAfterFailure verifies that the parent
// task remains responsive after a child failure by checking that the parent
// can still receive messages and spawn new tasks
// (VAL-CHOIR-009 verification step #3).
func TestFailureIsolation_ParentResponsiveAfterFailure(t *testing.T) {
	provider := &StubProvider{
		Delay:   10 * time.Millisecond,
		FailErr: fmt.Errorf("responsiveness test failure"),
	}
	rt, _, parentID := failureIsolationSetup(t, provider)
	ctx := context.Background()

	// Spawn and wait for a failing child.
	child1, _ := rt.SpawnTask(ctx, parentID, "fail task", "user-alice", nil)
	waitForTaskState(t, rt, child1.TaskID, 10*time.Second)

	// Parent should still be responsive: check channel read works.
	msgs, cursor, err := rt.ChannelRead(parentID, 0)
	if err != nil {
		t.Fatalf("parent channel read after child failure: %v", err)
	}
	if len(msgs) == 0 {
		t.Error("parent should have at least one message from the failed child")
	}

	// Parent should be able to spawn a new child.
	child2, err := rt.SpawnTask(ctx, parentID, "post-failure task", "user-alice", nil)
	if err != nil {
		t.Fatalf("spawn after failure: %v", err)
	}
	waitForTaskState(t, rt, child2.TaskID, 10*time.Second)

	// Verify parent received messages from both children.
	msgs2, _, err := rt.ChannelRead(parentID, cursor)
	if err != nil {
		t.Fatalf("parent channel read after second child: %v", err)
	}
	if len(msgs2) == 0 {
		t.Error("parent should have messages from the second child")
	}
}

// --- VAL-CHOIR-010: Task Cancellation ---

// TestCancellation_CancelRunningTask verifies that a running task can be
// cancelled and transitions to cancelled state (VAL-CHOIR-010).
func TestCancellation_CancelRunningTask(t *testing.T) {
	// Use a slow provider so the task stays running.
	provider := NewStubProvider(5 * time.Second)
	rt, _, parentID := failureIsolationSetup(t, provider)
	ctx := context.Background()

	// Spawn a child with a long-running task.
	child, err := rt.SpawnTask(ctx, parentID, "long running analysis", "user-alice", nil)
	if err != nil {
		t.Fatalf("spawn child: %v", err)
	}

	// Wait for the task to start running.
	time.Sleep(100 * time.Millisecond)

	// Verify it's running.
	task, _ := rt.Store().GetTask(ctx, child.TaskID)
	if task.State != types.TaskRunning {
		t.Fatalf("child should be running, got %q", task.State)
	}

	// Cancel the task via the runtime.
	err = rt.CancelTask(ctx, child.TaskID, "user-alice")
	if err != nil {
		t.Fatalf("cancel task: %v", err)
	}

	// Wait for the task to reach terminal state.
	task = waitForTaskState(t, rt, child.TaskID, 5*time.Second)

	if task.State != types.TaskCancelled {
		t.Errorf("cancelled task state: got %q, want cancelled", task.State)
	}
}

// TestCancellation_CancelledEventEmitted verifies that a task.cancelled event
// is emitted when a task is cancelled (VAL-CHOIR-010).
func TestCancellation_CancelledEventEmitted(t *testing.T) {
	provider := NewStubProvider(5 * time.Second)
	rt, _, parentID := failureIsolationSetup(t, provider)
	ctx := context.Background()

	// Subscribe to events before spawning.
	bus := rt.EventBus()
	ch := bus.SubscribeWithBuffer(128)
	defer bus.Unsubscribe(ch)

	child, _ := rt.SpawnTask(ctx, parentID, "cancellable task", "user-alice", nil)
	time.Sleep(100 * time.Millisecond)

	// Cancel the task.
	err := rt.CancelTask(ctx, child.TaskID, "user-alice")
	if err != nil {
		t.Fatalf("cancel task: %v", err)
	}

	// Wait for the cancelled event.
	found := false
	timeout := time.After(5 * time.Second)
	for !found {
		select {
		case <-timeout:
			t.Fatal("timeout waiting for task.cancelled event")
		case ev := <-ch:
			if ev.Record.Kind == types.EventTaskCancelled && ev.Record.TaskID == child.TaskID {
				found = true
			}
		default:
			time.Sleep(50 * time.Millisecond)
		}
	}
}

// TestCancellation_CancelledTaskNoResult verifies that a cancelled task does
// not produce a result (VAL-CHOIR-010).
func TestCancellation_CancelledTaskNoResult(t *testing.T) {
	provider := NewStubProvider(5 * time.Second)
	rt, _, parentID := failureIsolationSetup(t, provider)
	ctx := context.Background()

	child, _ := rt.SpawnTask(ctx, parentID, "task to cancel", "user-alice", nil)
	time.Sleep(100 * time.Millisecond)

	err := rt.CancelTask(ctx, child.TaskID, "user-alice")
	if err != nil {
		t.Fatalf("cancel task: %v", err)
	}

	task := waitForTaskState(t, rt, child.TaskID, 5*time.Second)

	if task.State != types.TaskCancelled {
		t.Errorf("state: got %q, want cancelled", task.State)
	}

	// Cancelled task should not have a result.
	if task.Result != "" {
		t.Errorf("cancelled task should not have a result, got %q", task.Result)
	}
}

// TestCancellation_CancelNonExistentTask verifies that cancelling a
// non-existent task returns an appropriate error.
func TestCancellation_CancelNonExistentTask(t *testing.T) {
	provider := NewStubProvider(50 * time.Millisecond)
	rt, _, _ := failureIsolationSetup(t, provider)
	ctx := context.Background()

	err := rt.CancelTask(ctx, "non-existent-task-id", "user-alice")
	if err == nil {
		t.Error("expected error when cancelling non-existent task")
	}
}

// TestCancellation_CancelOtherUsersTask verifies that cancelling another
// user's task returns an error (ownership check).
func TestCancellation_CancelOtherUsersTask(t *testing.T) {
	provider := NewStubProvider(5 * time.Second)
	rt, _, parentID := failureIsolationSetup(t, provider)
	ctx := context.Background()

	child, _ := rt.SpawnTask(ctx, parentID, "task owned by alice", "user-alice", nil)
	time.Sleep(100 * time.Millisecond)

	// Try to cancel as a different user.
	err := rt.CancelTask(ctx, child.TaskID, "user-bob")
	if err == nil {
		t.Error("expected error when cancelling another user's task")
	}
}

// TestCancellation_SiblingUnaffectedByCancel verifies that cancelling one
// running task does not affect other running tasks (VAL-CHOIR-010).
func TestCancellation_SiblingUnaffectedByCancel(t *testing.T) {
	provider := NewStubProvider(500 * time.Millisecond)
	rt, _, parentID := failureIsolationSetup(t, provider)
	ctx := context.Background()

	// Spawn 3 children.
	child1, _ := rt.SpawnTask(ctx, parentID, "task 1", "user-alice", nil)
	child2, _ := rt.SpawnTask(ctx, parentID, "task 2", "user-alice", nil)
	child3, _ := rt.SpawnTask(ctx, parentID, "task 3", "user-alice", nil)

	time.Sleep(100 * time.Millisecond)

	// Cancel only child2.
	err := rt.CancelTask(ctx, child2.TaskID, "user-alice")
	if err != nil {
		t.Fatalf("cancel child2: %v", err)
	}

	// Wait for child2 to be cancelled.
	task2 := waitForTaskState(t, rt, child2.TaskID, 5*time.Second)
	if task2.State != types.TaskCancelled {
		t.Errorf("child2 state: got %q, want cancelled", task2.State)
	}

	// Wait for children 1 and 3 to complete normally.
	task1 := waitForTaskState(t, rt, child1.TaskID, 10*time.Second)
	task3 := waitForTaskState(t, rt, child3.TaskID, 10*time.Second)

	if task1.State != types.TaskCompleted {
		t.Errorf("child1 state: got %q, want completed (should be unaffected)", task1.State)
	}
	if task3.State != types.TaskCompleted {
		t.Errorf("child3 state: got %q, want completed (should be unaffected)", task3.State)
	}

	// Children 1 and 3 should have results.
	if task1.Result == "" {
		t.Error("child1 should have a result")
	}
	if task3.Result == "" {
		t.Error("child3 should have a result")
	}
}

// TestCancellation_CancelViaAPI verifies that the cancel API endpoint works
// correctly (VAL-CHOIR-010).
func TestCancellation_CancelViaAPI(t *testing.T) {
	provider := NewStubProvider(5 * time.Second)
	_, handler, parentID := failureIsolationSetup(t, provider)

	// Spawn a child via API.
	body := fmt.Sprintf(`{"parent_id":"%s","objective":"cancellable via api"}`, parentID)
	req := authenticatedRequest(http.MethodPost, "/api/agent/spawn", body, "user-alice")
	w := httptest.NewRecorder()
	handler.HandleSpawn(w, req)

	var spawnResp spawnResponse
	if err := json.NewDecoder(w.Body).Decode(&spawnResp); err != nil {
		t.Fatalf("decode spawn response: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	// Cancel via API.
	cancelBody := fmt.Sprintf(`{"task_id":"%s"}`, spawnResp.TaskID)
	req = authenticatedRequest(http.MethodPost, "/api/agent/cancel", cancelBody, "user-alice")
	w = httptest.NewRecorder()
	handler.HandleCancel(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("cancel API status: got %d, want 200; body: %s", w.Code, w.Body.String())
	}

	// Verify the task is cancelled via status API.
	time.Sleep(200 * time.Millisecond)
	req = authenticatedRequest(http.MethodGet, "/api/agent/status?task_id="+spawnResp.TaskID, "", "user-alice")
	w = httptest.NewRecorder()
	handler.HandleTaskStatus(w, req)

	var statusResp taskStatusResponse
	if err := json.NewDecoder(w.Body).Decode(&statusResp); err != nil {
		t.Fatalf("decode status response: %v", err)
	}

	if statusResp.State != types.TaskCancelled {
		t.Errorf("task state after cancel: got %q, want cancelled", statusResp.State)
	}
}

// --- VAL-CHOIR-014: Recovery After Sandbox Restart ---

// TestRecovery_InterruptedTasksMarkedFailedOnRestart verifies that tasks in
// non-terminal states when the runtime stops are recovered and marked as
// failed on restart (VAL-CHOIR-014).
func TestRecovery_InterruptedTasksMarkedFailedOnRestart(t *testing.T) {
	dir := t.TempDir()
	dbPath := fmt.Sprintf("%s/%s.db", dir, t.Name())

	// First instance: create tasks in the store directly to simulate tasks
	// that were running when the process crashed. We don't use rt.Stop()
	// because that cleanly cancels tasks before exiting.
	s1, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store 1: %v", err)
	}

	now := time.Now().UTC()

	// Create a parent task in running state (simulating interrupted).
	parent := types.TaskRecord{
		TaskID:    "parent-recovery-test",
		OwnerID:   "user-alice",
		SandboxID: "sandbox-recovery-test",
		State:     types.TaskRunning,
		Prompt:    "parent for recovery test",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s1.CreateTask(context.Background(), parent); err != nil {
		t.Fatalf("create parent: %v", err)
	}

	// Create a child task in running state (simulating interrupted).
	child := types.TaskRecord{
		TaskID:    "child-recovery-test",
		OwnerID:   "user-alice",
		SandboxID: "sandbox-recovery-test",
		State:     types.TaskRunning,
		Prompt:    "task that will be interrupted",
		CreatedAt: now,
		UpdatedAt: now,
		Metadata:  map[string]any{"parent_id": "parent-recovery-test"},
	}
	if err := s1.CreateTask(context.Background(), child); err != nil {
		t.Fatalf("create child: %v", err)
	}

	// Also create a work item for the child.
	workItem := types.WorkItem{
		ID:        "child-recovery-test",
		ParentID:  "parent-recovery-test",
		OwnerID:   "user-alice",
		Objective: "task that will be interrupted",
		State:     types.TaskRunning,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s1.CreateWorkItem(context.Background(), workItem); err != nil {
		t.Fatalf("create work item: %v", err)
	}

	// Simulate crash: close the store without graceful shutdown.
	_ = s1.Close()

	// Second instance: restart with same store.
	s2, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store 2: %v", err)
	}
	bus2 := events.NewEventBus()
	cfg := Config{
		SandboxID:           "sandbox-recovery-test",
		StorePath:           dbPath,
		ProviderTimeout:     500 * time.Millisecond,
		SupervisionInterval: 1 * time.Hour,
	}
	provider := NewStubProvider(50 * time.Millisecond)
	rt2 := New(cfg, s2, bus2, provider)
	rt2.Start(context.Background())

	t.Cleanup(func() {
		rt2.Stop()
		_ = s2.Close()
	})

	// Wait for recovery to process.
	time.Sleep(200 * time.Millisecond)

	ctx := context.Background()

	// Check the parent task is now failed.
	parentTask, err := rt2.Store().GetTask(ctx, "parent-recovery-test")
	if err != nil {
		t.Fatalf("get parent task after recovery: %v", err)
	}
	if parentTask.State != types.TaskFailed {
		t.Errorf("recovered parent state: got %q, want failed", parentTask.State)
	}
	if !strings.Contains(parentTask.Error, "interrupted") {
		t.Errorf("recovered parent error: got %q, want to contain 'interrupted'", parentTask.Error)
	}

	// Check the child task is now failed.
	childTask, err := rt2.Store().GetTask(ctx, "child-recovery-test")
	if err != nil {
		t.Fatalf("get child task after recovery: %v", err)
	}
	if childTask.State != types.TaskFailed {
		t.Errorf("recovered child state: got %q, want failed", childTask.State)
	}
	if !strings.Contains(childTask.Error, "interrupted") {
		t.Errorf("recovered child error: got %q, want to contain 'interrupted'", childTask.Error)
	}
}

// TestRecovery_RecoveredTasksEmitFailedEvents verifies that recovered tasks
// emit task.failed events (VAL-CHOIR-014).
func TestRecovery_RecoveredTasksEmitFailedEvents(t *testing.T) {
	dir := t.TempDir()
	dbPath := fmt.Sprintf("%s/%s.db", dir, t.Name())

	// Create a task directly in the store in running state.
	s1, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	now := time.Now().UTC()
	task := types.TaskRecord{
		TaskID:    "recovery-event-test",
		OwnerID:   "user-alice",
		SandboxID: "sandbox-recovery-events",
		State:     types.TaskRunning,
		Prompt:    "interrupted task for events",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s1.CreateTask(context.Background(), task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	_ = s1.Close()

	// Restart with event subscription.
	s2, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store 2: %v", err)
	}
	bus2 := events.NewEventBus()
	cfg := Config{
		SandboxID:           "sandbox-recovery-events",
		StorePath:           dbPath,
		ProviderTimeout:     50 * time.Millisecond,
		SupervisionInterval: 1 * time.Hour,
	}
	provider := NewStubProvider(50 * time.Millisecond)
	rt2 := New(cfg, s2, bus2, provider)

	// Subscribe before starting to capture recovery events.
	ch := bus2.SubscribeWithBuffer(128)
	defer bus2.Unsubscribe(ch)

	rt2.Start(context.Background())

	t.Cleanup(func() {
		rt2.Stop()
		_ = s2.Close()
	})

	// Wait for recovery.
	time.Sleep(200 * time.Millisecond)

	// Check for task.failed event from recovery.
	found := false
	timeout := time.After(3 * time.Second)
	for !found {
		select {
		case <-timeout:
			t.Fatal("timeout waiting for recovery task.failed event")
		case ev := <-ch:
			if ev.Record.Kind == types.EventTaskFailed && ev.Record.TaskID == "recovery-event-test" {
				found = true
			}
		default:
			time.Sleep(50 * time.Millisecond)
		}
	}
}

// TestRecovery_RuntimeAcceptsNewTasksAfterRecovery verifies that after
// recovering interrupted tasks, the runtime can accept and complete new tasks.
func TestRecovery_RuntimeAcceptsNewTasksAfterRecovery(t *testing.T) {
	dir := t.TempDir()
	dbPath := fmt.Sprintf("%s/%s.db", dir, t.Name())

	// Create a task directly in the store in running state.
	s1, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	now := time.Now().UTC()
	task := types.TaskRecord{
		TaskID:    "old-interrupted-task",
		OwnerID:   "user-alice",
		SandboxID: "sandbox-recovery-accept",
		State:     types.TaskRunning,
		Prompt:    "interrupted task",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s1.CreateTask(context.Background(), task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	_ = s1.Close()

	// Restart with a fast provider.
	s2, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store 2: %v", err)
	}
	bus2 := events.NewEventBus()
	fastProvider := NewStubProvider(50 * time.Millisecond)
	cfg := Config{
		SandboxID:           "sandbox-recovery-accept",
		StorePath:           dbPath,
		ProviderTimeout:     50 * time.Millisecond,
		SupervisionInterval: 1 * time.Hour,
	}
	rt2 := New(cfg, s2, bus2, fastProvider)
	rt2.Start(context.Background())

	t.Cleanup(func() {
		rt2.Stop()
		_ = s2.Close()
	})

	time.Sleep(200 * time.Millisecond) // Let recovery process.

	// Submit a new task and verify it completes.
	newTask, err := rt2.SubmitTask(context.Background(), "post-recovery task", "user-alice")
	if err != nil {
		t.Fatalf("submit post-recovery task: %v", err)
	}

	completedTask := waitForTaskState(t, rt2, newTask.TaskID, 10*time.Second)
	if completedTask.State != types.TaskCompleted {
		t.Errorf("post-recovery task state: got %q, want completed", completedTask.State)
	}
}

// --- Helper types ---
