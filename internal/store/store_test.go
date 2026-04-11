package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yusefmosiah/go-choir/internal/types"
)

// testStorePath returns a unique temporary database path for each test.
func testStorePath(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(os.TempDir(), "go-choir-m3-store-test")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	return filepath.Join(dir, t.Name()+".db")
}

// openTestStore creates a fresh store for testing.
func openTestStore(t *testing.T) *Store {
	t.Helper()
	path := testStorePath(t)
	// Clean up any previous test database.
	_ = os.Remove(path)

	s, err := Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = s.Close()
		_ = os.Remove(path)
	})
	return s
}

func TestOpenCreatesDatabase(t *testing.T) {
	path := testStorePath(t)
	_ = os.Remove(path)

	s, err := Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() {
		_ = s.Close()
		_ = os.Remove(path)
	}()

	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Error("expected database file to be created")
	}
}

func TestCloseIdempotent(t *testing.T) {
	s := openTestStore(t)
	// Close twice should not panic.
	_ = s.Close()
}

func TestCreateAndGetTask(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)
	rec := types.TaskRecord{
		TaskID:    "task-001",
		OwnerID:   "user-alice",
		SandboxID: "sandbox-dev",
		State:     types.TaskPending,
		Prompt:    "explain closures in Go",
		CreatedAt: now,
		UpdatedAt: now,
		Metadata: map[string]any{
			"model": "claude-3",
		},
	}

	if err := s.CreateTask(ctx, rec); err != nil {
		t.Fatalf("create task: %v", err)
	}

	got, err := s.GetTask(ctx, "task-001")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}

	if got.TaskID != rec.TaskID {
		t.Errorf("task_id: got %q, want %q", got.TaskID, rec.TaskID)
	}
	if got.OwnerID != rec.OwnerID {
		t.Errorf("owner_id: got %q, want %q", got.OwnerID, rec.OwnerID)
	}
	if got.SandboxID != rec.SandboxID {
		t.Errorf("sandbox_id: got %q, want %q", got.SandboxID, rec.SandboxID)
	}
	if got.State != rec.State {
		t.Errorf("state: got %q, want %q", got.State, rec.State)
	}
	if got.Prompt != rec.Prompt {
		t.Errorf("prompt: got %q, want %q", got.Prompt, rec.Prompt)
	}
	if got.FinishedAt != nil {
		t.Errorf("finished_at: got %v, want nil for pending task", got.FinishedAt)
	}
	if got.Metadata["model"] != "claude-3" {
		t.Errorf("metadata model: got %v, want claude-3", got.Metadata["model"])
	}
}

func TestGetTaskNotFound(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	_, err := s.GetTask(ctx, "nonexistent")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestUpdateTask(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)
	rec := types.TaskRecord{
		TaskID:    "task-002",
		OwnerID:   "user-bob",
		SandboxID: "sandbox-dev",
		State:     types.TaskPending,
		Prompt:    "write a hello world",
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := s.CreateTask(ctx, rec); err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Update to running.
	rec.State = types.TaskRunning
	rec.UpdatedAt = now.Add(1 * time.Second)
	if err := s.UpdateTask(ctx, rec); err != nil {
		t.Fatalf("update task to running: %v", err)
	}

	got, err := s.GetTask(ctx, "task-002")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.State != types.TaskRunning {
		t.Errorf("state: got %q, want %q", got.State, types.TaskRunning)
	}

	// Update to completed with result.
	finishedAt := now.Add(10 * time.Second)
	rec.State = types.TaskCompleted
	rec.Result = "Hello, World!"
	rec.UpdatedAt = finishedAt
	rec.FinishedAt = &finishedAt
	if err := s.UpdateTask(ctx, rec); err != nil {
		t.Fatalf("update task to completed: %v", err)
	}

	got, err = s.GetTask(ctx, "task-002")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.State != types.TaskCompleted {
		t.Errorf("state: got %q, want %q", got.State, types.TaskCompleted)
	}
	if got.Result != "Hello, World!" {
		t.Errorf("result: got %q, want %q", got.Result, "Hello, World!")
	}
	if got.FinishedAt == nil {
		t.Fatal("finished_at should not be nil for completed task")
	}
}

func TestUpdateTaskNotFound(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	rec := types.TaskRecord{
		TaskID: "nonexistent",
		State:  types.TaskRunning,
	}

	err := s.UpdateTask(ctx, rec)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestListTasksByOwner(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)

	// Create tasks for two owners.
	for i, owner := range []string{"alice", "bob", "alice"} {
		taskID := fmtTaskID(i)
		rec := types.TaskRecord{
			TaskID:    taskID,
			OwnerID:   owner,
			SandboxID: "sandbox-dev",
			State:     types.TaskPending,
			Prompt:    "prompt " + taskID,
			CreatedAt: now.Add(time.Duration(i) * time.Second),
			UpdatedAt: now.Add(time.Duration(i) * time.Second),
		}
		if err := s.CreateTask(ctx, rec); err != nil {
			t.Fatalf("create task %s: %v", taskID, err)
		}
	}

	aliceTasks, err := s.ListTasksByOwner(ctx, "alice", 10)
	if err != nil {
		t.Fatalf("list tasks by owner: %v", err)
	}
	if len(aliceTasks) != 2 {
		t.Errorf("alice tasks: got %d, want 2", len(aliceTasks))
	}
	for _, task := range aliceTasks {
		if task.OwnerID != "alice" {
			t.Errorf("owner_id: got %q, want alice", task.OwnerID)
		}
	}

	bobTasks, err := s.ListTasksByOwner(ctx, "bob", 10)
	if err != nil {
		t.Fatalf("list tasks by owner: %v", err)
	}
	if len(bobTasks) != 1 {
		t.Errorf("bob tasks: got %d, want 1", len(bobTasks))
	}
}

func TestListTasksByState(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)

	states := []types.TaskState{types.TaskPending, types.TaskRunning, types.TaskCompleted, types.TaskPending}
	for i, state := range states {
		taskID := fmtTaskID(i)
		rec := types.TaskRecord{
			TaskID:    taskID,
			OwnerID:   "user-test",
			SandboxID: "sandbox-dev",
			State:     state,
			Prompt:    "prompt " + taskID,
			CreatedAt: now.Add(time.Duration(i) * time.Second),
			UpdatedAt: now.Add(time.Duration(i) * time.Second),
		}
		if err := s.CreateTask(ctx, rec); err != nil {
			t.Fatalf("create task %s: %v", taskID, err)
		}
	}

	pendingTasks, err := s.ListTasksByState(ctx, types.TaskPending, 10)
	if err != nil {
		t.Fatalf("list tasks by state: %v", err)
	}
	if len(pendingTasks) != 2 {
		t.Errorf("pending tasks: got %d, want 2", len(pendingTasks))
	}
	for _, task := range pendingTasks {
		if task.State != types.TaskPending {
			t.Errorf("state: got %q, want pending", task.State)
		}
	}

	completedTasks, err := s.ListTasksByState(ctx, types.TaskCompleted, 10)
	if err != nil {
		t.Fatalf("list tasks by state: %v", err)
	}
	if len(completedTasks) != 1 {
		t.Errorf("completed tasks: got %d, want 1", len(completedTasks))
	}
}

func TestListTasks(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)

	for i := 0; i < 5; i++ {
		taskID := fmtTaskID(i)
		rec := types.TaskRecord{
			TaskID:    taskID,
			OwnerID:   "user-test",
			SandboxID: "sandbox-dev",
			State:     types.TaskPending,
			Prompt:    "prompt " + taskID,
			CreatedAt: now.Add(time.Duration(i) * time.Second),
			UpdatedAt: now.Add(time.Duration(i) * time.Second),
		}
		if err := s.CreateTask(ctx, rec); err != nil {
			t.Fatalf("create task %s: %v", taskID, err)
		}
	}

	tasks, err := s.ListTasks(ctx, 3)
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(tasks) != 3 {
		t.Errorf("tasks: got %d, want 3 (limited)", len(tasks))
	}

	// Should be ordered by created_at descending (newest first).
	if tasks[0].CreatedAt.Before(tasks[1].CreatedAt) {
		t.Error("expected tasks ordered by created_at descending")
	}
}

func TestTaskStateTransitionFromPendingToRunning(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)
	rec := types.TaskRecord{
		TaskID:    "task-transition",
		OwnerID:   "user-alice",
		SandboxID: "sandbox-dev",
		State:     types.TaskPending,
		Prompt:    "test transition",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.CreateTask(ctx, rec); err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Transition: pending → running
	rec.State = types.TaskRunning
	rec.UpdatedAt = now.Add(1 * time.Second)
	if err := s.UpdateTask(ctx, rec); err != nil {
		t.Fatalf("update task to running: %v", err)
	}

	// Transition: running → failed (provider failure scenario)
	finishedAt := now.Add(5 * time.Second)
	rec.State = types.TaskFailed
	rec.Error = "provider timeout"
	rec.UpdatedAt = finishedAt
	rec.FinishedAt = &finishedAt
	if err := s.UpdateTask(ctx, rec); err != nil {
		t.Fatalf("update task to failed: %v", err)
	}

	got, err := s.GetTask(ctx, "task-transition")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.State != types.TaskFailed {
		t.Errorf("state: got %q, want %q", got.State, types.TaskFailed)
	}
	if got.Error != "provider timeout" {
		t.Errorf("error: got %q, want %q", got.Error, "provider timeout")
	}
	if got.FinishedAt == nil {
		t.Fatal("finished_at should be set for failed task")
	}

	// Runtime should remain available for new tasks after failure.
	nextTask := types.TaskRecord{
		TaskID:    "task-after-failure",
		OwnerID:   "user-alice",
		SandboxID: "sandbox-dev",
		State:     types.TaskPending,
		Prompt:    "next prompt after failure",
		CreatedAt: now.Add(10 * time.Second),
		UpdatedAt: now.Add(10 * time.Second),
	}
	if err := s.CreateTask(ctx, nextTask); err != nil {
		t.Fatalf("create task after failure: %v", err)
	}
}

func TestAppendAndListEvents(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)

	// Append a sequence of events for a task.
	kinds := []types.EventKind{
		types.EventTaskSubmitted,
		types.EventTaskStarted,
		types.EventTaskProgress,
		types.EventTaskDelta,
		types.EventTaskCompleted,
	}

	for i, kind := range kinds {
		rec := &types.EventRecord{
			EventID:   fmtEventID(i),
			TaskID:    "task-001",
			OwnerID:   "user-alice",
			Timestamp: now.Add(time.Duration(i) * time.Second),
			Kind:      kind,
			Phase:     "execution",
			Payload:   json.RawMessage(`{"step":` + string(rune('0'+i)) + `}`),
		}
		if err := s.AppendEvent(ctx, rec); err != nil {
			t.Fatalf("append event %d: %v", i, err)
		}
	}

	events, err := s.ListEvents(ctx, "task-001", 10)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}

	if len(events) != 5 {
		t.Fatalf("events: got %d, want 5", len(events))
	}

	// Events should be ordered by sequence.
	for i, ev := range events {
		if ev.Seq != int64(i+1) {
			t.Errorf("event %d seq: got %d, want %d", i, ev.Seq, i+1)
		}
		expectedKind := kinds[i]
		if ev.Kind != expectedKind {
			t.Errorf("event %d kind: got %q, want %q", i, ev.Kind, expectedKind)
		}
	}
}

func TestListEventsAfter(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)

	for i := 0; i < 5; i++ {
		rec := &types.EventRecord{
			EventID:   fmtEventID(i),
			TaskID:    "task-002",
			OwnerID:   "user-alice",
			Timestamp: now.Add(time.Duration(i) * time.Second),
			Kind:      types.EventTaskProgress,
			Payload:   json.RawMessage(`{}`),
		}
		if err := s.AppendEvent(ctx, rec); err != nil {
			t.Fatalf("append event %d: %v", i, err)
		}
	}

	// Get events after seq 2 (should return seq 3, 4, 5).
	events, err := s.ListEventsAfter(ctx, "task-002", 2, 10)
	if err != nil {
		t.Fatalf("list events after: %v", err)
	}

	if len(events) != 3 {
		t.Fatalf("events after seq 2: got %d, want 3", len(events))
	}
	for i, ev := range events {
		expectedSeq := int64(i + 3)
		if ev.Seq != expectedSeq {
			t.Errorf("event %d seq: got %d, want %d", i, ev.Seq, expectedSeq)
		}
	}
}

func TestListEventsByOwner(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)

	// Events for two different owners.
	for i, owner := range []string{"alice", "bob", "alice"} {
		rec := &types.EventRecord{
			EventID:   fmtEventID(i),
			TaskID:    "task-" + owner,
			OwnerID:   owner,
			Timestamp: now.Add(time.Duration(i) * time.Second),
			Kind:      types.EventTaskSubmitted,
			Payload:   json.RawMessage(`{}`),
		}
		if err := s.AppendEvent(ctx, rec); err != nil {
			t.Fatalf("append event %d: %v", i, err)
		}
	}

	aliceEvents, err := s.ListEventsByOwner(ctx, "alice", 10)
	if err != nil {
		t.Fatalf("list events by owner: %v", err)
	}
	if len(aliceEvents) != 2 {
		t.Errorf("alice events: got %d, want 2", len(aliceEvents))
	}
	for _, ev := range aliceEvents {
		if ev.OwnerID != "alice" {
			t.Errorf("owner_id: got %q, want alice", ev.OwnerID)
		}
	}
}

func TestTaskRecoveryAcrossReopen(t *testing.T) {
	path := testStorePath(t)
	_ = os.Remove(path)

	now := time.Now().UTC().Truncate(time.Microsecond)
	ctx := context.Background()

	// Open store, create a task, and close.
	s1, err := Open(path)
	if err != nil {
		t.Fatalf("open store 1: %v", err)
	}

	rec := types.TaskRecord{
		TaskID:    "task-recovery",
		OwnerID:   "user-alice",
		SandboxID: "sandbox-dev",
		State:     types.TaskRunning,
		Prompt:    "test recovery across restart",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s1.CreateTask(ctx, rec); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("close store 1: %v", err)
	}

	// Reopen the same database and verify the task is still there.
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("open store 2: %v", err)
	}
	defer func() {
		_ = s2.Close()
		_ = os.Remove(path)
	}()

	got, err := s2.GetTask(ctx, "task-recovery")
	if err != nil {
		t.Fatalf("get task after reopen: %v", err)
	}
	if got.TaskID != "task-recovery" {
		t.Errorf("task_id: got %q, want task-recovery", got.TaskID)
	}
	if got.State != types.TaskRunning {
		t.Errorf("state: got %q, want running", got.State)
	}
	if got.Prompt != "test recovery across restart" {
		t.Errorf("prompt: got %q, want original prompt", got.Prompt)
	}
}

func TestEventRecoveryAcrossReopen(t *testing.T) {
	path := testStorePath(t)
	_ = os.Remove(path)

	now := time.Now().UTC().Truncate(time.Microsecond)
	ctx := context.Background()

	// Open store, append events, and close.
	s1, err := Open(path)
	if err != nil {
		t.Fatalf("open store 1: %v", err)
	}

	for i := 0; i < 3; i++ {
		rec := &types.EventRecord{
			EventID:   fmtEventID(i),
			TaskID:    "task-event-recovery",
			OwnerID:   "user-alice",
			Timestamp: now.Add(time.Duration(i) * time.Second),
			Kind:      types.EventTaskProgress,
			Payload:   json.RawMessage(`{"step":` + string(rune('0'+byte(i))) + `}`),
		}
		if err := s1.AppendEvent(ctx, rec); err != nil {
			t.Fatalf("append event %d: %v", i, err)
		}
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("close store 1: %v", err)
	}

	// Reopen and verify events are recoverable.
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("open store 2: %v", err)
	}
	defer func() {
		_ = s2.Close()
		_ = os.Remove(path)
	}()

	events, err := s2.ListEvents(ctx, "task-event-recovery", 10)
	if err != nil {
		t.Fatalf("list events after reopen: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("events: got %d, want 3", len(events))
	}
	// Verify sequence numbers survived.
	for i, ev := range events {
		if ev.Seq != int64(i+1) {
			t.Errorf("event %d seq: got %d, want %d", i, ev.Seq, i+1)
		}
	}
}

func TestAppendEventDefaultPayload(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	rec := &types.EventRecord{
		EventID:   "evt-default-payload",
		TaskID:    "task-001",
		Timestamp: time.Now().UTC(),
		Kind:      types.EventTaskSubmitted,
		Payload:   nil, // should default to {}
	}
	if err := s.AppendEvent(ctx, rec); err != nil {
		t.Fatalf("append event: %v", err)
	}

	events, err := s.ListEvents(ctx, "task-001", 10)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events: got %d, want 1", len(events))
	}
	if string(events[0].Payload) != `{}` {
		t.Errorf("payload: got %q, want {}", string(events[0].Payload))
	}
}

func TestTaskWithFailedStatePersistsError(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)
	finishedAt := now.Add(5 * time.Second)

	rec := types.TaskRecord{
		TaskID:     "task-failed",
		OwnerID:    "user-alice",
		SandboxID:  "sandbox-dev",
		State:      types.TaskFailed,
		Prompt:     "prompt that fails",
		Error:      "provider timeout after 30s",
		CreatedAt:  now,
		UpdatedAt:  finishedAt,
		FinishedAt: &finishedAt,
	}
	if err := s.CreateTask(ctx, rec); err != nil {
		t.Fatalf("create task: %v", err)
	}

	got, err := s.GetTask(ctx, "task-failed")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.State != types.TaskFailed {
		t.Errorf("state: got %q, want %q", got.State, types.TaskFailed)
	}
	if got.Error != "provider timeout after 30s" {
		t.Errorf("error: got %q, want provider timeout after 30s", got.Error)
	}
	if got.Result != "" {
		t.Errorf("result: got %q, want empty for failed task", got.Result)
	}
}

func TestTaskWithBlockedState(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)

	rec := types.TaskRecord{
		TaskID:    "task-blocked",
		OwnerID:   "user-alice",
		SandboxID: "sandbox-dev",
		State:     types.TaskBlocked,
		Prompt:    "prompt that gets blocked",
		Error:     "provider rate limit exceeded",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.CreateTask(ctx, rec); err != nil {
		t.Fatalf("create task: %v", err)
	}

	got, err := s.GetTask(ctx, "task-blocked")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.State != types.TaskBlocked {
		t.Errorf("state: got %q, want %q", got.State, types.TaskBlocked)
	}
	if !got.State.Valid() {
		t.Error("blocked state should be valid")
	}
	if got.State.Terminal() {
		t.Error("blocked state should not be terminal (may be retried)")
	}
}

// Helper functions for generating deterministic IDs in tests.
func fmtTaskID(i int) string {
	return fmtID("task", i)
}

func fmtEventID(i int) string {
	return fmtID("evt", i)
}

func fmtID(prefix string, i int) string {
	return prefix + "-" + string(rune('0'+i))
}
