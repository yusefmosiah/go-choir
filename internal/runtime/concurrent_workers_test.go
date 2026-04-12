package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yusefmosiah/go-choir/internal/events"
	"github.com/yusefmosiah/go-choir/internal/store"
	"github.com/yusefmosiah/go-choir/internal/types"
)

// --- Concurrent Workers Tests (VAL-CHOIR-008) ---
//
// These tests verify that multiple workers can run concurrently from the
// same parent without interference. Feature requirements:
//
//   - Parent can spawn 3+ workers in sequence without waiting
//   - All workers run concurrently (not sequentially)
//   - Each worker has independent channel to parent
//   - Results collected as each worker completes
//   - No interference between sibling workers

// testConcurrentSetup creates a fresh Runtime with a slow provider to
// ensure tasks stay running long enough for concurrent observation.
func testConcurrentSetup(t *testing.T) (*Runtime, *APIHandler, string) {
	t.Helper()

	dir := t.TempDir()
	dbPath := fmt.Sprintf("%s/%s.db", dir, t.Name())

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	bus := events.NewEventBus()
	// Use a slow provider so tasks stay running for concurrent observation.
	provider := NewStubProvider(500 * time.Millisecond)
	cfg := Config{
		SandboxID:           "sandbox-concurrent-test",
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

	// Create a parent task.
	parentRec, err := rt.SubmitTask(context.Background(), "parent objective", "user-alice")
	if err != nil {
		t.Fatalf("create parent task: %v", err)
	}

	// Wait for the parent to start running.
	time.Sleep(50 * time.Millisecond)

	return rt, handler, parentRec.TaskID
}

// TestConcurrentWorkers_Spawn3WithoutWaiting verifies that a parent can
// spawn 3 workers in rapid sequence without waiting for any to complete
// (VAL-CHOIR-008, expected behavior #1).
func TestConcurrentWorkers_Spawn3WithoutWaiting(t *testing.T) {
	_, handler, parentID := testConcurrentSetup(t)

	childIDs := make([]string, 3)

	for i := 0; i < 3; i++ {
		body := fmt.Sprintf(`{"parent_id":"%s","objective":"worker task %d"}`, parentID, i)
		req := authenticatedRequest(http.MethodPost, "/api/agent/spawn", body, "user-alice")
		w := httptest.NewRecorder()
		handler.HandleSpawn(w, req)

		if w.Code != http.StatusAccepted {
			t.Fatalf("spawn %d: status got %d, want %d; body: %s",
				i, w.Code, http.StatusAccepted, w.Body.String())
		}

		var resp spawnResponse
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("spawn %d: decode response: %v", i, err)
		}

		childIDs[i] = resp.TaskID
	}

	// All child IDs should be unique.
	seen := make(map[string]bool)
	for i, id := range childIDs {
		if seen[id] {
			t.Errorf("child %d has duplicate ID %q", i, id)
		}
		seen[id] = true
	}
}

// TestConcurrentWorkers_AllRunningSimultaneously verifies that after spawning
// multiple workers, all are in running state at the same time (not serialized)
// (VAL-CHOIR-008, expected behavior #2).
func TestConcurrentWorkers_AllRunningSimultaneously(t *testing.T) {
	rt, handler, parentID := testConcurrentSetup(t)

	// Spawn 3 workers rapidly.
	childIDs := make([]string, 3)
	for i := 0; i < 3; i++ {
		body := fmt.Sprintf(`{"parent_id":"%s","objective":"concurrent task %d"}`, parentID, i)
		req := authenticatedRequest(http.MethodPost, "/api/agent/spawn", body, "user-alice")
		w := httptest.NewRecorder()
		handler.HandleSpawn(w, req)

		var resp spawnResponse
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("spawn %d: decode: %v", i, err)
		}
		childIDs[i] = resp.TaskID
	}

	// Give a small window for all tasks to transition to running.
	time.Sleep(100 * time.Millisecond)

	// Check that the runtime reports multiple running tasks.
	runningCount := rt.RunningCount()
	if runningCount < 3 {
		t.Errorf("running tasks: got %d, want at least 3 (parent + 3 children or just 3 children)", runningCount)
	}

	// Verify all children are in running state.
	ctx := context.Background()
	runningChildren := 0
	for _, id := range childIDs {
		task, err := rt.Store().GetTask(ctx, id)
		if err != nil {
			t.Fatalf("get task %s: %v", id, err)
		}
		if task.State == types.TaskRunning {
			runningChildren++
		}
	}

	if runningChildren < 3 {
		t.Errorf("running children: got %d, want 3 (all children should be running simultaneously)", runningChildren)
	}
}

// TestConcurrentWorkers_EachCompletesIndependently verifies that each worker
// completes independently and results are collected separately
// (VAL-CHOIR-008, expected behavior #4).
func TestConcurrentWorkers_EachCompletesIndependently(t *testing.T) {
	rt, _, parentID := testConcurrentSetup(t)

	ctx := context.Background()

	// Spawn 3 children.
	childIDs := make([]string, 3)
	for i := 0; i < 3; i++ {
		rec, err := rt.SpawnTask(ctx, parentID, fmt.Sprintf("independent task %d", i), "user-alice", nil)
		if err != nil {
			t.Fatalf("spawn child %d: %v", i, err)
		}
		childIDs[i] = rec.TaskID
	}

	// Wait for all children to complete (with timeout).
	deadline := time.After(10 * time.Second)
	allDone := false

	for !allDone {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for all children to complete")
		default:
		}

		allDone = true
		for _, id := range childIDs {
			task, err := rt.Store().GetTask(ctx, id)
			if err != nil {
				t.Fatalf("get task %s: %v", id, err)
			}
			if !task.State.Terminal() {
				allDone = false
				break
			}
		}
		if !allDone {
			time.Sleep(50 * time.Millisecond)
		}
	}

	// Verify each child has its own result.
	for i, id := range childIDs {
		task, err := rt.Store().GetTask(ctx, id)
		if err != nil {
			t.Fatalf("get completed task %s: %v", id, err)
		}

		if task.State != types.TaskCompleted {
			t.Errorf("child %d (%s): state got %q, want completed", i, id[:8], task.State)
		}
		if task.Result == "" {
			t.Errorf("child %d (%s): result should not be empty", i, id[:8])
		}
		if task.FinishedAt == nil {
			t.Errorf("child %d (%s): finished_at should not be nil", i, id[:8])
		}
	}
}

// TestConcurrentWorkers_IndependentChannels verifies that each worker has
// an independent channel to the parent. Messages from one child don't
// interfere with another (VAL-CHOIR-008, expected behavior #3).
func TestConcurrentWorkers_IndependentChannels(t *testing.T) {
	rt, _, parentID := testConcurrentSetup(t)
	ctx := context.Background()

	// Spawn 3 children.
	childIDs := make([]string, 3)
	for i := 0; i < 3; i++ {
		rec, err := rt.SpawnTask(ctx, parentID, fmt.Sprintf("channel task %d", i), "user-alice", nil)
		if err != nil {
			t.Fatalf("spawn child %d: %v", i, err)
		}
		childIDs[i] = rec.TaskID
	}

	// Each child should have its own channel.
	for i, id := range childIDs {
		ch, err := rt.ChannelManager().Channel(id)
		if err != nil {
			t.Fatalf("child %d channel: %v", i, err)
		}
		if ch == nil {
			t.Fatalf("child %d: channel should not be nil", i)
		}
	}

	// Post messages from each child to the parent channel.
	for i, id := range childIDs {
		_, err := rt.ChannelPost(ctx, parentID, id, "result", fmt.Sprintf("Result from child %d", i))
		if err != nil {
			t.Fatalf("child %d post to parent: %v", i, err)
		}
	}

	// Read all messages on the parent channel.
	msgs, _, err := rt.ChannelRead(parentID, 0)
	if err != nil {
		t.Fatalf("parent channel read: %v", err)
	}

	if len(msgs) != 3 {
		t.Fatalf("parent messages: got %d, want 3", len(msgs))
	}

	// Verify each message comes from the correct child.
	for i, id := range childIDs {
		found := false
		for _, msg := range msgs {
			if msg.From == id && msg.Content == fmt.Sprintf("Result from child %d", i) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no message found from child %d (%s)", i, id[:8])
		}
	}
}

// TestConcurrentWorkers_NoInterferenceBetweenSiblings verifies that sibling
// workers don't interfere with each other. Each child's state, result, and
// error are independent (VAL-CHOIR-008, expected behavior #5).
func TestConcurrentWorkers_NoInterferenceBetweenSiblings(t *testing.T) {
	rt, _, parentID := testConcurrentSetup(t)
	ctx := context.Background()

	// Spawn 5 children with different objectives.
	childIDs := make([]string, 5)
	objectives := make([]string, 5)
	for i := 0; i < 5; i++ {
		objectives[i] = fmt.Sprintf("unique objective %d: analyze feature %c", i, 'A'+i)
		rec, err := rt.SpawnTask(ctx, parentID, objectives[i], "user-alice", nil)
		if err != nil {
			t.Fatalf("spawn child %d: %v", i, err)
		}
		childIDs[i] = rec.TaskID
	}

	// Wait for all to complete.
	deadline := time.After(10 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for children to complete")
		default:
		}

		allDone := true
		for _, id := range childIDs {
			task, _ := rt.Store().GetTask(ctx, id)
			if !task.State.Terminal() {
				allDone = false
				break
			}
		}
		if allDone {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Verify each child has the correct objective.
	for i, id := range childIDs {
		task, err := rt.Store().GetTask(ctx, id)
		if err != nil {
			t.Fatalf("get task %s: %v", id, err)
		}
		if task.Prompt != objectives[i] {
			t.Errorf("child %d: prompt got %q, want %q", i, task.Prompt, objectives[i])
		}
		if task.State != types.TaskCompleted {
			t.Errorf("child %d: state got %q, want completed", i, task.State)
		}
	}
}

// TestConcurrentWorkers_ResultsCollectedViaChannels verifies that results
// from all workers are delivered to the parent via channels and can be
// collected independently as each worker completes
// (VAL-CHOIR-008, expected behavior #4).
func TestConcurrentWorkers_ResultsCollectedViaChannels(t *testing.T) {
	rt, _, parentID := testConcurrentSetup(t)
	ctx := context.Background()

	// Spawn 3 children.
	childIDs := make([]string, 3)
	for i := 0; i < 3; i++ {
		rec, err := rt.SpawnTask(ctx, parentID, fmt.Sprintf("result collection task %d", i), "user-alice", nil)
		if err != nil {
			t.Fatalf("spawn child %d: %v", i, err)
		}
		childIDs[i] = rec.TaskID
	}

	// Wait for all children to complete.
	deadline := time.After(10 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for children to complete")
		default:
		}

		allDone := true
		for _, id := range childIDs {
			task, _ := rt.Store().GetTask(ctx, id)
			if !task.State.Terminal() {
				allDone = false
				break
			}
		}
		if allDone {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Now check that the parent received results via channels for each child.
	// The child tasks should have posted their results to the parent's channel.
	msgs, _, err := rt.ChannelRead(parentID, 0)
	if err != nil {
		t.Fatalf("parent channel read: %v", err)
	}

	// Each child should have posted a result message.
	childResults := make(map[string]string)
	for _, msg := range msgs {
		if msg.Role == "result" {
			childResults[msg.From] = msg.Content
		}
	}

	// All children should have results in the channel.
	for i, id := range childIDs {
		if _, ok := childResults[id]; !ok {
			t.Errorf("child %d (%s): no result message found in parent channel", i, id[:8])
		}
	}
}

// TestConcurrentWorkers_WorkItemsUpdatedOnCompletion verifies that work items
// in the registry are updated when spawned tasks complete
// (VAL-CHOIR-008, related to VAL-CHOIR-001, VAL-CHOIR-003).
func TestConcurrentWorkers_WorkItemsUpdatedOnCompletion(t *testing.T) {
	rt, _, parentID := testConcurrentSetup(t)
	ctx := context.Background()

	// Spawn 3 children.
	childIDs := make([]string, 3)
	for i := 0; i < 3; i++ {
		rec, err := rt.SpawnTask(ctx, parentID, fmt.Sprintf("work item task %d", i), "user-alice", nil)
		if err != nil {
			t.Fatalf("spawn child %d: %v", i, err)
		}
		childIDs[i] = rec.TaskID
	}

	// Wait for all to complete.
	deadline := time.After(10 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for children to complete")
		default:
		}

		allDone := true
		for _, id := range childIDs {
			task, _ := rt.Store().GetTask(ctx, id)
			if !task.State.Terminal() {
				allDone = false
				break
			}
		}
		if allDone {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Verify each work item is updated to completed.
	for i, id := range childIDs {
		item, err := rt.Store().GetWorkItem(ctx, id)
		if err != nil {
			t.Fatalf("get work item %s: %v", id, err)
		}

		if item.State != types.TaskCompleted {
			t.Errorf("child %d work item: state got %q, want completed", i, item.State)
		}
		if item.Result == "" {
			t.Errorf("child %d work item: result should not be empty", i)
		}
		if item.ParentID != parentID {
			t.Errorf("child %d work item: parent_id got %q, want %q", i, item.ParentID, parentID)
		}
	}
}

// TestConcurrentWorkers_HealthReportsRunningCount verifies that the health
// endpoint reports the correct number of running tasks during concurrent
// execution (VAL-CHOIR-008).
func TestConcurrentWorkers_HealthReportsRunningCount(t *testing.T) {
	_, handler, parentID := testConcurrentSetup(t)

	// Spawn 3 children.
	for i := 0; i < 3; i++ {
		body := fmt.Sprintf(`{"parent_id":"%s","objective":"health check task %d"}`, parentID, i)
		req := authenticatedRequest(http.MethodPost, "/api/agent/spawn", body, "user-alice")
		w := httptest.NewRecorder()
		handler.HandleSpawn(w, req)
	}

	// Give time for tasks to transition to running.
	time.Sleep(100 * time.Millisecond)

	// Check the health endpoint reports running tasks.
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	handler.HandleHealth(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("health status: got %d, want %d", w.Code, http.StatusOK)
	}

	var resp runtimeHealthResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode health response: %v", err)
	}

	if resp.RunningTasks < 3 {
		t.Errorf("running_tasks: got %d, want at least 3", resp.RunningTasks)
	}
}

// TestConcurrentWorkers_5ConcurrentWorkers verifies spawning and completing
// 5 workers concurrently. This matches the VAL-CHOIR-008 evidence which
// uses 5 tasks.
func TestConcurrentWorkers_5ConcurrentWorkers(t *testing.T) {
	rt, handler, parentID := testConcurrentSetup(t)
	ctx := context.Background()

	childIDs := make([]string, 5)

	// Submit 5 tasks rapidly.
	for i := 0; i < 5; i++ {
		body := fmt.Sprintf(`{"parent_id":"%s","objective":"Analyze Go features - part %d"}`, parentID, i)
		req := authenticatedRequest(http.MethodPost, "/api/agent/spawn", body, "user-alice")
		w := httptest.NewRecorder()
		handler.HandleSpawn(w, req)

		if w.Code != http.StatusAccepted {
			t.Fatalf("spawn %d: status got %d, want %d", i, w.Code, http.StatusAccepted)
		}

		var resp spawnResponse
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("spawn %d: decode: %v", i, err)
		}
		childIDs[i] = resp.TaskID
	}

	// All 5 should have unique IDs.
	seen := make(map[string]bool)
	for _, id := range childIDs {
		if seen[id] {
			t.Errorf("duplicate task ID: %s", id)
		}
		seen[id] = true
	}

	// Wait for all to complete.
	deadline := time.After(10 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for 5 workers to complete")
		default:
		}

		allDone := true
		for _, id := range childIDs {
			task, _ := rt.Store().GetTask(ctx, id)
			if !task.State.Terminal() {
				allDone = false
				break
			}
		}
		if allDone {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// All 5 should be completed with correct results.
	completedCount := 0
	for i, id := range childIDs {
		task, err := rt.Store().GetTask(ctx, id)
		if err != nil {
			t.Fatalf("get task %s: %v", id, err)
		}
		if task.State == types.TaskCompleted {
			completedCount++
		} else {
			t.Errorf("task %d (%s): state got %q, want completed", i, id[:8], task.State)
		}
	}

	if completedCount != 5 {
		t.Errorf("completed count: got %d, want 5", completedCount)
	}
}

// --- Race condition stress test ---

// TestConcurrentWorkers_ConcurrentSpawnStress verifies that concurrent
// spawn calls don't cause race conditions or data corruption.
func TestConcurrentWorkers_ConcurrentSpawnStress(t *testing.T) {
	rt, _, parentID := testConcurrentSetup(t)
	ctx := context.Background()

	const numWorkers = 10
	var wg sync.WaitGroup
	childIDs := make([]string, numWorkers)
	errors := make([]error, numWorkers)

	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			rec, err := rt.SpawnTask(ctx, parentID, fmt.Sprintf("stress task %d", idx), "user-alice", nil)
			if err != nil {
				errors[idx] = err
				return
			}
			childIDs[idx] = rec.TaskID
		}(i)
	}

	wg.Wait()

	// Check no errors occurred.
	for i, err := range errors {
		if err != nil {
			t.Errorf("concurrent spawn %d: %v", i, err)
		}
	}

	// Verify all IDs are unique.
	seen := make(map[string]bool)
	for i, id := range childIDs {
		if id == "" {
			t.Errorf("child %d has empty ID", i)
			continue
		}
		if seen[id] {
			t.Errorf("duplicate child ID: %s", id)
		}
		seen[id] = true
	}
}

// --- Slow Provider for controlled concurrent testing ---

// slowProvider is a stub provider with a configurable delay per task,
// allowing fine-grained control over task execution duration.
type slowProvider struct {
	StubProvider
	delay time.Duration
}

func newSlowProvider(delay time.Duration) *slowProvider {
	return &slowProvider{
		StubProvider: *NewStubProvider(delay),
		delay:        delay,
	}
}

// TestConcurrentWorkers_TasksActuallyRunConcurrently verifies that tasks
// actually run concurrently, not sequentially. If 3 tasks each take 200ms,
// the total should be closer to 200ms than 600ms.
func TestConcurrentWorkers_TasksActuallyRunConcurrently(t *testing.T) {
	dir := t.TempDir()
	dbPath := fmt.Sprintf("%s/%s.db", dir, t.Name())

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	bus := events.NewEventBus()
	// Each task takes 200ms.
	provider := NewStubProvider(200 * time.Millisecond)
	cfg := Config{
		SandboxID:           "sandbox-concurrent-timing",
		StorePath:           dbPath,
		ProviderTimeout:     200 * time.Millisecond,
		SupervisionInterval: 1 * time.Hour,
	}

	rt := New(cfg, s, bus, provider)

	t.Cleanup(func() {
		rt.Stop()
		_ = s.Close()
	})

	ctx := context.Background()

	// Create parent.
	parentRec, err := rt.SubmitTask(ctx, "parent", "user-alice")
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	// Spawn 3 children and measure total time.
	start := time.Now()
	childIDs := make([]string, 3)
	for i := 0; i < 3; i++ {
		rec, err := rt.SpawnTask(ctx, parentRec.TaskID, fmt.Sprintf("timing task %d", i), "user-alice", nil)
		if err != nil {
			t.Fatalf("spawn child %d: %v", i, err)
		}
		childIDs[i] = rec.TaskID
	}

	// Wait for all to complete.
	deadline := time.After(10 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout")
		default:
		}

		allDone := true
		for _, id := range childIDs {
			task, _ := rt.Store().GetTask(ctx, id)
			if !task.State.Terminal() {
				allDone = false
				break
			}
		}
		if allDone {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	elapsed := time.Since(start)

	// If tasks ran sequentially, total would be ~600ms.
	// With concurrency, should be closer to ~300ms (200ms per task + overhead).
	// We use a generous threshold of 500ms.
	if elapsed > 500*time.Millisecond {
		t.Errorf("tasks appear to run sequentially: elapsed %v (expected < 500ms for 3 concurrent 200ms tasks)", elapsed)
	}
}

// TestConcurrentWorkers_ResultsPostedToParentChannelOnCompletion verifies that
// when a spawned child task completes, the result is automatically posted
// to the parent's channel (VAL-CHOIR-008, related to VAL-CHOIR-006).
func TestConcurrentWorkers_ResultsPostedToParentChannelOnCompletion(t *testing.T) {
	rt, _, parentID := testConcurrentSetup(t)
	ctx := context.Background()

	// Spawn a child.
	rec, err := rt.SpawnTask(ctx, parentID, "auto-post result task", "user-alice", nil)
	if err != nil {
		t.Fatalf("spawn child: %v", err)
	}

	// Wait for the child to complete.
	deadline := time.After(10 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for child to complete")
		default:
		}
		task, _ := rt.Store().GetTask(ctx, rec.TaskID)
		if task.State.Terminal() {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Check the parent channel for the child's result.
	msgs, _, err := rt.ChannelRead(parentID, 0)
	if err != nil {
		t.Fatalf("parent channel read: %v", err)
	}

	// Should find a result message from the child.
	found := false
	for _, msg := range msgs {
		if msg.From == rec.TaskID && msg.Role == "result" {
			found = true
			break
		}
	}

	if !found {
		t.Error("no result message found in parent channel from child after completion")
	}
}

// TestConcurrentWorkers_FailedChildPostsErrorToParentChannel verifies that
// when a spawned child task fails, an error is posted to the parent's channel
// (VAL-CHOIR-008, related to VAL-CHOIR-009).
func TestConcurrentWorkers_FailedChildPostsErrorToParentChannel(t *testing.T) {
	dir := t.TempDir()
	dbPath := fmt.Sprintf("%s/%s.db", dir, t.Name())

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	bus := events.NewEventBus()
	// Provider that always fails.
	provider := &StubProvider{
		Delay:   10 * time.Millisecond,
		FailErr: fmt.Errorf("simulated provider failure"),
	}
	cfg := Config{
		SandboxID:           "sandbox-fail-test",
		StorePath:           dbPath,
		ProviderTimeout:     10 * time.Millisecond,
		SupervisionInterval: 1 * time.Hour,
	}

	rt := New(cfg, s, bus, provider)

	t.Cleanup(func() {
		rt.Stop()
		_ = s.Close()
	})

	ctx := context.Background()

	// Create parent.
	parentRec, err := rt.SubmitTask(ctx, "parent", "user-alice")
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	// Spawn a child that will fail.
	rec, err := rt.SpawnTask(ctx, parentRec.TaskID, "failing task", "user-alice", nil)
	if err != nil {
		t.Fatalf("spawn child: %v", err)
	}

	// Wait for the child to complete (fail).
	deadline := time.After(10 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for child to fail")
		default:
		}
		task, _ := rt.Store().GetTask(ctx, rec.TaskID)
		if task.State.Terminal() {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Verify the child is in failed state.
	task, _ := rt.Store().GetTask(ctx, rec.TaskID)
	if task.State != types.TaskFailed {
		t.Fatalf("child state: got %q, want failed", task.State)
	}

	// Check the parent channel for the error message.
	msgs, _, err := rt.ChannelRead(parentRec.TaskID, 0)
	if err != nil {
		t.Fatalf("parent channel read: %v", err)
	}

	// Should find an error message from the child.
	found := false
	for _, msg := range msgs {
		if msg.From == rec.TaskID && msg.Role == "error" {
			found = true
			break
		}
	}

	if !found {
		t.Error("no error message found in parent channel from failed child")
	}
}

// TestConcurrentWorkers_StatusAPIReturnsCorrectState verifies that the status
// API returns correct state for each concurrent worker
// (VAL-CHOIR-008, verification step).
func TestConcurrentWorkers_StatusAPIReturnsCorrectState(t *testing.T) {
	_, handler, parentID := testConcurrentSetup(t)

	// Spawn 3 workers.
	childIDs := make([]string, 3)
	for i := 0; i < 3; i++ {
		body := fmt.Sprintf(`{"parent_id":"%s","objective":"status check task %d"}`, parentID, i)
		req := authenticatedRequest(http.MethodPost, "/api/agent/spawn", body, "user-alice")
		w := httptest.NewRecorder()
		handler.HandleSpawn(w, req)

		var resp spawnResponse
		json.NewDecoder(w.Body).Decode(&resp)
		childIDs[i] = resp.TaskID
	}

	// Check each child's status via the status API.
	for i, id := range childIDs {
		req := authenticatedRequest(http.MethodGet, "/api/agent/status?task_id="+id, "", "user-alice")
		w := httptest.NewRecorder()
		handler.HandleTaskStatus(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("status for child %d: got %d, want 200", i, w.Code)
		}

		var resp taskStatusResponse
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("decode status response: %v", err)
		}

		if resp.TaskID != id {
			t.Errorf("child %d: task_id got %q, want %q", i, resp.TaskID, id)
		}
		if resp.OwnerID != "user-alice" {
			t.Errorf("child %d: owner_id got %q, want user-alice", i, resp.OwnerID)
		}
		// State should be pending or running (both valid for just-spawned).
		if resp.State != types.TaskPending && resp.State != types.TaskRunning {
			t.Errorf("child %d: state got %q, want pending or running", i, resp.State)
		}
	}
}

// TestConcurrentWorkers_Spawn5WorkersRapidlyThenVerifyAllComplete is the
// comprehensive test that matches the exact VAL-CHOIR-008 verification
// steps: spawn 3+ workers, check all running simultaneously, verify each
// completes independently, confirm all results received by parent.
func TestConcurrentWorkers_Spawn5WorkersRapidlyThenVerifyAllComplete(t *testing.T) {
	rt, _, parentID := testConcurrentSetup(t)
	ctx := context.Background()

	// Step 1: Spawn 5 workers with different objectives.
	objectives := []string{
		"Analyze Go concurrency patterns",
		"Research Go error handling best practices",
		"Summarize Go module system",
		"Investigate Go testing strategies",
		"Review Go performance optimization",
	}

	childIDs := make([]string, len(objectives))
	for i, obj := range objectives {
		rec, err := rt.SpawnTask(ctx, parentID, obj, "user-alice", nil)
		if err != nil {
			t.Fatalf("spawn worker %d: %v", i, err)
		}
		childIDs[i] = rec.TaskID
	}

	// Step 2: Check: all show 'running' state simultaneously.
	time.Sleep(100 * time.Millisecond)

	runningCount := 0
	for _, id := range childIDs {
		task, err := rt.Store().GetTask(ctx, id)
		if err != nil {
			t.Fatalf("get task: %v", err)
		}
		if task.State == types.TaskRunning {
			runningCount++
		}
	}
	if runningCount < 3 {
		t.Errorf("running count: got %d, want at least 3 running simultaneously", runningCount)
	}

	// Step 3: Verify: each completes independently.
	deadline := time.After(10 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for all workers to complete")
		default:
		}

		allDone := true
		for _, id := range childIDs {
			task, _ := rt.Store().GetTask(ctx, id)
			if !task.State.Terminal() {
				allDone = false
				break
			}
		}
		if allDone {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// All should be completed.
	for i, id := range childIDs {
		task, err := rt.Store().GetTask(ctx, id)
		if err != nil {
			t.Fatalf("get completed task: %v", err)
		}
		if task.State != types.TaskCompleted {
			t.Errorf("worker %d: state got %q, want completed", i, task.State)
		}
		if task.Result == "" {
			t.Errorf("worker %d: result is empty", i)
		}
	}

	// Step 4: Confirm: all results received by parent.
	// Results should be posted to the parent channel.
	msgs, _, err := rt.ChannelRead(parentID, 0)
	if err != nil {
		t.Fatalf("parent channel read: %v", err)
	}

	childResults := make(map[string]bool)
	for _, msg := range msgs {
		if msg.Role == "result" {
			childResults[msg.From] = true
		}
	}

	for i, id := range childIDs {
		if !childResults[id] {
			t.Errorf("worker %d (%s): result not found in parent channel", i, id[:8])
		}
	}
}

// TestConcurrentWorkers_SpawnWithSlowProvider_HighConcurrency tests spawning
// 10 workers with a slow provider to stress-test concurrency.
func TestConcurrentWorkers_SpawnWithSlowProvider_HighConcurrency(t *testing.T) {
	dir := t.TempDir()
	dbPath := fmt.Sprintf("%s/%s.db", dir, t.Name())

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	bus := events.NewEventBus()
	provider := NewStubProvider(300 * time.Millisecond)
	cfg := Config{
		SandboxID:           "sandbox-high-concurrency",
		StorePath:           dbPath,
		ProviderTimeout:     300 * time.Millisecond,
		SupervisionInterval: 1 * time.Hour,
	}

	rt := New(cfg, s, bus, provider)

	t.Cleanup(func() {
		rt.Stop()
		_ = s.Close()
	})

	ctx := context.Background()

	// Create parent.
	parentRec, err := rt.SubmitTask(ctx, "parent for 10 workers", "user-alice")
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	// Spawn 10 workers concurrently.
	const numWorkers = 10
	var wg sync.WaitGroup
	var successCount atomic.Int32
	childIDs := make([]string, numWorkers)

	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			rec, err := rt.SpawnTask(ctx, parentRec.TaskID, fmt.Sprintf("worker %d", idx), "user-alice", nil)
			if err != nil {
				t.Errorf("spawn worker %d: %v", idx, err)
				return
			}
			childIDs[idx] = rec.TaskID
			successCount.Add(1)
		}(i)
	}
	wg.Wait()

	if int(successCount.Load()) != numWorkers {
		t.Fatalf("spawned count: got %d, want %d", successCount.Load(), numWorkers)
	}

	// Verify running count.
	time.Sleep(100 * time.Millisecond)
	running := rt.RunningCount()
	if running < numWorkers {
		t.Errorf("running tasks: got %d, want at least %d", running, numWorkers)
	}

	// Wait for all to complete.
	deadline := time.After(15 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for workers to complete")
		default:
		}

		allDone := true
		for _, id := range childIDs {
			if id == "" {
				continue
			}
			task, _ := rt.Store().GetTask(ctx, id)
			if !task.State.Terminal() {
				allDone = false
				break
			}
		}
		if allDone {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// All should be completed.
	completed := 0
	for i, id := range childIDs {
		if id == "" {
			continue
		}
		task, err := rt.Store().GetTask(ctx, id)
		if err != nil {
			t.Errorf("get worker %d: %v", i, err)
			continue
		}
		if task.State == types.TaskCompleted {
			completed++
		}
	}

	if completed != numWorkers {
		t.Errorf("completed: got %d, want %d", completed, numWorkers)
	}
}

// TestConcurrentWorkers_MixedPassFailWorkers verifies that when some workers
// fail and others succeed, results are correctly reported independently
// (VAL-CHOIR-008, VAL-CHOIR-009).
func TestConcurrentWorkers_MixedPassFailWorkers(t *testing.T) {
	dir := t.TempDir()
	dbPath := fmt.Sprintf("%s/%s.db", dir, t.Name())

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	bus := events.NewEventBus()

	// Create a provider that fails for tasks containing "fail" in the objective.
	provider := &conditionalFailProvider{
		delay:      50 * time.Millisecond,
		failPrefix: "fail",
		result:     "Task completed successfully.",
	}

	cfg := Config{
		SandboxID:           "sandbox-mixed-test",
		StorePath:           dbPath,
		ProviderTimeout:     50 * time.Millisecond,
		SupervisionInterval: 1 * time.Hour,
	}

	rt := New(cfg, s, bus, provider)

	t.Cleanup(func() {
		rt.Stop()
		_ = s.Close()
	})

	ctx := context.Background()

	// Create parent.
	parentRec, err := rt.SubmitTask(ctx, "parent for mixed workers", "user-alice")
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	// Spawn workers: 2 succeed, 1 fails.
	rec1, _ := rt.SpawnTask(ctx, parentRec.TaskID, "analyze data", "user-alice", nil)
	rec2, _ := rt.SpawnTask(ctx, parentRec.TaskID, "fail this task", "user-alice", nil)
	rec3, _ := rt.SpawnTask(ctx, parentRec.TaskID, "summarize results", "user-alice", nil)

	childIDs := []string{rec1.TaskID, rec2.TaskID, rec3.TaskID}

	// Wait for all to complete.
	deadline := time.After(10 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout")
		default:
		}
		allDone := true
		for _, id := range childIDs {
			task, _ := rt.Store().GetTask(ctx, id)
			if !task.State.Terminal() {
				allDone = false
				break
			}
		}
		if allDone {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Check task states.
	task1, _ := rt.Store().GetTask(ctx, rec1.TaskID)
	task2, _ := rt.Store().GetTask(ctx, rec2.TaskID)
	task3, _ := rt.Store().GetTask(ctx, rec3.TaskID)

	if task1.State != types.TaskCompleted {
		t.Errorf("task1: got %q, want completed", task1.State)
	}
	if task2.State != types.TaskFailed {
		t.Errorf("task2 (failing): got %q, want failed", task2.State)
	}
	if task3.State != types.TaskCompleted {
		t.Errorf("task3: got %q, want completed", task3.State)
	}

	// Failed task should have error message.
	if task2.Error == "" {
		t.Error("task2 should have an error message")
	}

	// Successful tasks should have results.
	if task1.Result == "" {
		t.Error("task1 should have a result")
	}
	if task3.Result == "" {
		t.Error("task3 should have a result")
	}
}

// conditionalFailProvider is a test provider that fails tasks containing
// a specific prefix in the prompt.
type conditionalFailProvider struct {
	delay      time.Duration
	failPrefix string
	result     string
}

func (p *conditionalFailProvider) ProviderName() string { return "conditional-fail" }

func (p *conditionalFailProvider) Execute(ctx context.Context, task *types.TaskRecord, emit EventEmitFunc) error {
	emit(types.EventTaskProgress, "execution", json.RawMessage(`{"status":"started"}`))

	select {
	case <-time.After(p.delay):
	case <-ctx.Done():
		return ctx.Err()
	}

	if strings.Contains(strings.ToLower(task.Prompt), p.failPrefix) {
		return fmt.Errorf("task failed: prompt contains %q", p.failPrefix)
	}

	emit(types.EventTaskDelta, "execution",
		json.RawMessage(`{"text":"`+p.result+`"}`))
	return nil
}
