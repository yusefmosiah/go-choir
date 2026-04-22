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

func TestCreateAndGetRun(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)
	rec := types.RunRecord{
		RunID:     "task-001",
		OwnerID:   "user-alice",
		SandboxID: "sandbox-dev",
		State:     types.RunPending,
		Prompt:    "explain closures in Go",
		CreatedAt: now,
		UpdatedAt: now,
		Metadata: map[string]any{
			"model": "claude-3",
		},
	}

	if err := s.CreateRun(ctx, rec); err != nil {
		t.Fatalf("create task: %v", err)
	}

	got, err := s.GetRun(ctx, "task-001")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}

	if got.RunID != rec.RunID {
		t.Errorf("loop_id: got %q, want %q", got.RunID, rec.RunID)
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

func TestGetRunNotFound(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	_, err := s.GetRun(ctx, "nonexistent")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestUpdateRun(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)
	rec := types.RunRecord{
		RunID:     "task-002",
		OwnerID:   "user-bob",
		SandboxID: "sandbox-dev",
		State:     types.RunPending,
		Prompt:    "write a hello world",
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := s.CreateRun(ctx, rec); err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Update to running.
	rec.State = types.RunRunning
	rec.UpdatedAt = now.Add(1 * time.Second)
	if err := s.UpdateRun(ctx, rec); err != nil {
		t.Fatalf("update task to running: %v", err)
	}

	got, err := s.GetRun(ctx, "task-002")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.State != types.RunRunning {
		t.Errorf("state: got %q, want %q", got.State, types.RunRunning)
	}

	// Update to completed with result.
	finishedAt := now.Add(10 * time.Second)
	rec.State = types.RunCompleted
	rec.Result = "Hello, World!"
	rec.UpdatedAt = finishedAt
	rec.FinishedAt = &finishedAt
	if err := s.UpdateRun(ctx, rec); err != nil {
		t.Fatalf("update task to completed: %v", err)
	}

	got, err = s.GetRun(ctx, "task-002")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.State != types.RunCompleted {
		t.Errorf("state: got %q, want %q", got.State, types.RunCompleted)
	}
	if got.Result != "Hello, World!" {
		t.Errorf("result: got %q, want %q", got.Result, "Hello, World!")
	}
	if got.FinishedAt == nil {
		t.Fatal("finished_at should not be nil for completed task")
	}
}

func TestUpdateRunNotFound(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	rec := types.RunRecord{
		RunID: "nonexistent",
		State: types.RunRunning,
	}

	err := s.UpdateRun(ctx, rec)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestListRunsByOwner(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)

	// Create runs for two owners.
	for i, owner := range []string{"alice", "bob", "alice"} {
		taskID := fmtRunID(i)
		rec := types.RunRecord{
			RunID:     taskID,
			OwnerID:   owner,
			SandboxID: "sandbox-dev",
			State:     types.RunPending,
			Prompt:    "prompt " + taskID,
			CreatedAt: now.Add(time.Duration(i) * time.Second),
			UpdatedAt: now.Add(time.Duration(i) * time.Second),
		}
		if err := s.CreateRun(ctx, rec); err != nil {
			t.Fatalf("create task %s: %v", taskID, err)
		}
	}

	aliceTasks, err := s.ListRunsByOwner(ctx, "alice", 10)
	if err != nil {
		t.Fatalf("list runs by owner: %v", err)
	}
	if len(aliceTasks) != 2 {
		t.Errorf("alice runs: got %d, want 2", len(aliceTasks))
	}
	for _, task := range aliceTasks {
		if task.OwnerID != "alice" {
			t.Errorf("owner_id: got %q, want alice", task.OwnerID)
		}
	}

	bobTasks, err := s.ListRunsByOwner(ctx, "bob", 10)
	if err != nil {
		t.Fatalf("list runs by owner: %v", err)
	}
	if len(bobTasks) != 1 {
		t.Errorf("bob runs: got %d, want 1", len(bobTasks))
	}
}

func TestListRunsByState(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)

	states := []types.RunState{types.RunPending, types.RunRunning, types.RunCompleted, types.RunPending}
	for i, state := range states {
		taskID := fmtRunID(i)
		rec := types.RunRecord{
			RunID:     taskID,
			OwnerID:   "user-test",
			SandboxID: "sandbox-dev",
			State:     state,
			Prompt:    "prompt " + taskID,
			CreatedAt: now.Add(time.Duration(i) * time.Second),
			UpdatedAt: now.Add(time.Duration(i) * time.Second),
		}
		if err := s.CreateRun(ctx, rec); err != nil {
			t.Fatalf("create task %s: %v", taskID, err)
		}
	}

	pendingTasks, err := s.ListRunsByState(ctx, types.RunPending, 10)
	if err != nil {
		t.Fatalf("list runs by state: %v", err)
	}
	if len(pendingTasks) != 2 {
		t.Errorf("pending runs: got %d, want 2", len(pendingTasks))
	}
	for _, task := range pendingTasks {
		if task.State != types.RunPending {
			t.Errorf("state: got %q, want pending", task.State)
		}
	}

	completedTasks, err := s.ListRunsByState(ctx, types.RunCompleted, 10)
	if err != nil {
		t.Fatalf("list runs by state: %v", err)
	}
	if len(completedTasks) != 1 {
		t.Errorf("completed runs: got %d, want 1", len(completedTasks))
	}
}

func TestListRuns(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)

	for i := 0; i < 5; i++ {
		taskID := fmtRunID(i)
		rec := types.RunRecord{
			RunID:     taskID,
			OwnerID:   "user-test",
			SandboxID: "sandbox-dev",
			State:     types.RunPending,
			Prompt:    "prompt " + taskID,
			CreatedAt: now.Add(time.Duration(i) * time.Second),
			UpdatedAt: now.Add(time.Duration(i) * time.Second),
		}
		if err := s.CreateRun(ctx, rec); err != nil {
			t.Fatalf("create task %s: %v", taskID, err)
		}
	}

	runs, err := s.ListRuns(ctx, 3)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 3 {
		t.Errorf("runs: got %d, want 3 (limited)", len(runs))
	}

	// Should be ordered by created_at descending (newest first).
	if runs[0].CreatedAt.Before(runs[1].CreatedAt) {
		t.Error("expected runs ordered by created_at descending")
	}
}

func TestTaskStateTransitionFromPendingToRunning(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)
	rec := types.RunRecord{
		RunID:     "task-transition",
		OwnerID:   "user-alice",
		SandboxID: "sandbox-dev",
		State:     types.RunPending,
		Prompt:    "test transition",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.CreateRun(ctx, rec); err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Transition: pending → running
	rec.State = types.RunRunning
	rec.UpdatedAt = now.Add(1 * time.Second)
	if err := s.UpdateRun(ctx, rec); err != nil {
		t.Fatalf("update task to running: %v", err)
	}

	// Transition: running → failed (provider failure scenario)
	finishedAt := now.Add(5 * time.Second)
	rec.State = types.RunFailed
	rec.Error = "provider timeout"
	rec.UpdatedAt = finishedAt
	rec.FinishedAt = &finishedAt
	if err := s.UpdateRun(ctx, rec); err != nil {
		t.Fatalf("update task to failed: %v", err)
	}

	got, err := s.GetRun(ctx, "task-transition")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.State != types.RunFailed {
		t.Errorf("state: got %q, want %q", got.State, types.RunFailed)
	}
	if got.Error != "provider timeout" {
		t.Errorf("error: got %q, want %q", got.Error, "provider timeout")
	}
	if got.FinishedAt == nil {
		t.Fatal("finished_at should be set for failed task")
	}

	// Runtime should remain available for new runs after failure.
	nextTask := types.RunRecord{
		RunID:     "task-after-failure",
		OwnerID:   "user-alice",
		SandboxID: "sandbox-dev",
		State:     types.RunPending,
		Prompt:    "next prompt after failure",
		CreatedAt: now.Add(10 * time.Second),
		UpdatedAt: now.Add(10 * time.Second),
	}
	if err := s.CreateRun(ctx, nextTask); err != nil {
		t.Fatalf("create task after failure: %v", err)
	}
}

func TestAppendAndListEvents(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)

	// Append a sequence of events for a task.
	kinds := []types.EventKind{
		types.EventRunSubmitted,
		types.EventRunStarted,
		types.EventRunProgress,
		types.EventRunDelta,
		types.EventRunCompleted,
	}

	for i, kind := range kinds {
		rec := &types.EventRecord{
			EventID:   fmtEventID(i),
			RunID:     "task-001",
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
			RunID:     "task-002",
			OwnerID:   "user-alice",
			Timestamp: now.Add(time.Duration(i) * time.Second),
			Kind:      types.EventRunProgress,
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
			RunID:     "task-" + owner,
			OwnerID:   owner,
			Timestamp: now.Add(time.Duration(i) * time.Second),
			Kind:      types.EventRunSubmitted,
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

	rec := types.RunRecord{
		RunID:     "task-recovery",
		OwnerID:   "user-alice",
		SandboxID: "sandbox-dev",
		State:     types.RunRunning,
		Prompt:    "test recovery across restart",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s1.CreateRun(ctx, rec); err != nil {
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

	got, err := s2.GetRun(ctx, "task-recovery")
	if err != nil {
		t.Fatalf("get task after reopen: %v", err)
	}
	if got.RunID != "task-recovery" {
		t.Errorf("loop_id: got %q, want task-recovery", got.RunID)
	}
	if got.State != types.RunRunning {
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
			RunID:     "task-event-recovery",
			OwnerID:   "user-alice",
			Timestamp: now.Add(time.Duration(i) * time.Second),
			Kind:      types.EventRunProgress,
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
		RunID:     "task-001",
		Timestamp: time.Now().UTC(),
		Kind:      types.EventRunSubmitted,
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

	rec := types.RunRecord{
		RunID:      "task-failed",
		OwnerID:    "user-alice",
		SandboxID:  "sandbox-dev",
		State:      types.RunFailed,
		Prompt:     "prompt that fails",
		Error:      "provider timeout after 30s",
		CreatedAt:  now,
		UpdatedAt:  finishedAt,
		FinishedAt: &finishedAt,
	}
	if err := s.CreateRun(ctx, rec); err != nil {
		t.Fatalf("create task: %v", err)
	}

	got, err := s.GetRun(ctx, "task-failed")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.State != types.RunFailed {
		t.Errorf("state: got %q, want %q", got.State, types.RunFailed)
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

	rec := types.RunRecord{
		RunID:     "task-blocked",
		OwnerID:   "user-alice",
		SandboxID: "sandbox-dev",
		State:     types.RunBlocked,
		Prompt:    "prompt that gets blocked",
		Error:     "provider rate limit exceeded",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.CreateRun(ctx, rec); err != nil {
		t.Fatalf("create task: %v", err)
	}

	got, err := s.GetRun(ctx, "task-blocked")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.State != types.RunBlocked {
		t.Errorf("state: got %q, want %q", got.State, types.RunBlocked)
	}
	if !got.State.Valid() {
		t.Error("blocked state should be valid")
	}
	if got.State.Terminal() {
		t.Error("blocked state should not be terminal (may be retried)")
	}
}

// Helper functions for generating deterministic IDs in tests.
func fmtRunID(i int) string {
	return fmtID("task", i)
}

func fmtEventID(i int) string {
	return fmtID("evt", i)
}

func fmtID(prefix string, i int) string {
	return prefix + "-" + string(rune('0'+i))
}

func TestListEventsByOwnerAfter(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)

	// Create events for alice with increasing sequence numbers.
	for i := 0; i < 5; i++ {
		rec := &types.EventRecord{
			EventID:   fmtEventID(i),
			RunID:     "task-001",
			OwnerID:   "user-alice",
			Timestamp: now.Add(time.Duration(i) * time.Second),
			Kind:      types.EventRunProgress,
			Payload:   json.RawMessage(`{"step":` + string(rune('0'+byte(i))) + `}`),
		}
		if err := s.AppendEvent(ctx, rec); err != nil {
			t.Fatalf("append event %d: %v", i, err)
		}
	}

	// Get events after seq 2 (should return seq 3, 4, 5).
	events, err := s.ListEventsByOwnerAfter(ctx, "user-alice", 2, 10)
	if err != nil {
		t.Fatalf("list events by owner after seq: %v", err)
	}

	if len(events) != 3 {
		t.Fatalf("events after seq 2: got %d, want 3", len(events))
	}

	// Events should be ordered by timestamp ascending.
	for i, ev := range events {
		expectedSeq := int64(i + 3)
		if ev.Seq != expectedSeq {
			t.Errorf("event %d seq: got %d, want %d", i, ev.Seq, expectedSeq)
		}
		if ev.OwnerID != "user-alice" {
			t.Errorf("event %d owner_id: got %q, want user-alice", i, ev.OwnerID)
		}
	}
}

func TestAppendEventAssignsOwnerWideStreamSeqAcrossRuns(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)
	eventsToAppend := []*types.EventRecord{
		{
			EventID:      "evt-stream-1",
			RunID:        "run-a",
			OwnerID:      "user-alice",
			TrajectoryID: "traj-1",
			Timestamp:    now,
			Kind:         types.EventRunSubmitted,
			Payload:      json.RawMessage(`{"step":"a1"}`),
		},
		{
			EventID:      "evt-stream-2",
			RunID:        "run-b",
			OwnerID:      "user-alice",
			TrajectoryID: "traj-2",
			Timestamp:    now.Add(1 * time.Second),
			Kind:         types.EventRunProgress,
			Payload:      json.RawMessage(`{"step":"b1"}`),
		},
		{
			EventID:      "evt-stream-3",
			RunID:        "run-a",
			OwnerID:      "user-alice",
			TrajectoryID: "traj-1",
			Timestamp:    now.Add(2 * time.Second),
			Kind:         types.EventRunCompleted,
			Payload:      json.RawMessage(`{"step":"a2"}`),
		},
	}
	for _, rec := range eventsToAppend {
		if err := s.AppendEvent(ctx, rec); err != nil {
			t.Fatalf("append %s: %v", rec.EventID, err)
		}
	}

	if eventsToAppend[0].Seq != 1 || eventsToAppend[0].StreamSeq != 1 {
		t.Fatalf("first event seqs = (%d,%d), want (1,1)", eventsToAppend[0].Seq, eventsToAppend[0].StreamSeq)
	}
	if eventsToAppend[1].Seq != 1 || eventsToAppend[1].StreamSeq != 2 {
		t.Fatalf("second event seqs = (%d,%d), want (1,2)", eventsToAppend[1].Seq, eventsToAppend[1].StreamSeq)
	}
	if eventsToAppend[2].Seq != 2 || eventsToAppend[2].StreamSeq != 3 {
		t.Fatalf("third event seqs = (%d,%d), want (2,3)", eventsToAppend[2].Seq, eventsToAppend[2].StreamSeq)
	}

	got, err := s.ListEventsByOwnerAfter(ctx, "user-alice", 1, 10)
	if err != nil {
		t.Fatalf("list events by owner after stream seq: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("events after stream seq 1: got %d, want 2", len(got))
	}
	if got[0].EventID != "evt-stream-2" || got[0].TrajectoryID != "traj-2" || got[0].StreamSeq != 2 {
		t.Fatalf("first catch-up event = %+v, want evt-stream-2 traj-2 stream_seq=2", got[0])
	}
	if got[1].EventID != "evt-stream-3" || got[1].TrajectoryID != "traj-1" || got[1].StreamSeq != 3 {
		t.Fatalf("second catch-up event = %+v, want evt-stream-3 traj-1 stream_seq=3", got[1])
	}
}

func TestListEventsByOwnerAfterFiltersByOwner(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)

	// Events for alice and bob.
	for i, owner := range []string{"alice", "bob", "alice"} {
		rec := &types.EventRecord{
			EventID:   fmtEventID(i),
			RunID:     "task-" + owner,
			OwnerID:   owner,
			Timestamp: now.Add(time.Duration(i) * time.Second),
			Kind:      types.EventRunSubmitted,
			Payload:   json.RawMessage(`{}`),
		}
		if err := s.AppendEvent(ctx, rec); err != nil {
			t.Fatalf("append event %d: %v", i, err)
		}
	}

	// Alice's events after seq 0.
	aliceEvents, err := s.ListEventsByOwnerAfter(ctx, "alice", 0, 10)
	if err != nil {
		t.Fatalf("list alice events after seq: %v", err)
	}
	if len(aliceEvents) != 2 {
		t.Errorf("alice events: got %d, want 2", len(aliceEvents))
	}
	for _, ev := range aliceEvents {
		if ev.OwnerID != "alice" {
			t.Errorf("owner_id: got %q, want alice", ev.OwnerID)
		}
	}

	// Bob's events after seq 0.
	bobEvents, err := s.ListEventsByOwnerAfter(ctx, "bob", 0, 10)
	if err != nil {
		t.Fatalf("list bob events after seq: %v", err)
	}
	if len(bobEvents) != 1 {
		t.Errorf("bob events: got %d, want 1", len(bobEvents))
	}
}
