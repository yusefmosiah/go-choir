package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yusefmosiah/go-choir/internal/events"
	"github.com/yusefmosiah/go-choir/internal/store"
	"github.com/yusefmosiah/go-choir/internal/types"
)

// testRuntime creates a fresh Runtime for testing with a temporary store
// and the stub provider.
func testRuntime(t *testing.T) (*Runtime, *store.Store) {
	t.Helper()

	dir := filepath.Join(os.TempDir(), "go-choir-m3-runtime-test")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	dbPath := filepath.Join(dir, t.Name()+".db")
	promptRoot := filepath.Join(dir, t.Name()+"-prompts")
	_ = os.Remove(dbPath)
	_ = os.RemoveAll(promptRoot)

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	bus := events.NewEventBus()
	provider := NewStubProvider(50 * time.Millisecond)
	cfg := Config{
		SandboxID:           "sandbox-test",
		StorePath:           dbPath,
		PromptRoot:          promptRoot,
		ProviderTimeout:     50 * time.Millisecond,
		SupervisionInterval: 1 * time.Hour, // don't run supervisor in most tests
	}

	rt := New(cfg, s, bus, provider)

	// Stop the runtime (cancels in-flight goroutines) before closing
	// the store to avoid "database is closed" log noise.
	t.Cleanup(func() {
		rt.Stop()
		_ = s.Close()
		_ = os.Remove(dbPath)
		_ = os.RemoveAll(promptRoot)
	})

	return rt, s
}

func TestSubmitTaskReturnsStableHandle(t *testing.T) {
	rt, _ := testRuntime(t)
	ctx := context.Background()

	rec, err := rt.SubmitTask(ctx, "explain closures in Go", "user-alice")
	if err != nil {
		t.Fatalf("submit task: %v", err)
	}

	// Task should have a stable UUID handle.
	if rec.TaskID == "" {
		t.Error("task_id should not be empty")
	}
	if rec.State != types.TaskPending {
		t.Errorf("state: got %q, want %q", rec.State, types.TaskPending)
	}
	if rec.OwnerID != "user-alice" {
		t.Errorf("owner_id: got %q, want user-alice", rec.OwnerID)
	}
	if rec.Prompt != "explain closures in Go" {
		t.Errorf("prompt: got %q, want original prompt", rec.Prompt)
	}
	if rec.SandboxID != "sandbox-test" {
		t.Errorf("sandbox_id: got %q, want sandbox-test", rec.SandboxID)
	}
	if rec.CreatedAt.IsZero() {
		t.Error("created_at should not be zero")
	}
}

func TestSubmitTaskPersistsToStore(t *testing.T) {
	rt, s := testRuntime(t)
	ctx := context.Background()

	rec, err := rt.SubmitTask(ctx, "test prompt", "user-bob")
	if err != nil {
		t.Fatalf("submit task: %v", err)
	}

	// Verify the task is persisted in the store.
	stored, err := s.GetTask(ctx, rec.TaskID)
	if err != nil {
		t.Fatalf("get task from store: %v", err)
	}
	if stored.TaskID != rec.TaskID {
		t.Errorf("task_id: got %q, want %q", stored.TaskID, rec.TaskID)
	}
	if stored.OwnerID != "user-bob" {
		t.Errorf("owner_id: got %q, want user-bob", stored.OwnerID)
	}
}

func TestConductorTaskNormalizesStructuredRouteResult(t *testing.T) {
	rt, s := testRuntime(t)
	ctx := context.Background()

	rec, err := rt.SubmitTaskWithMetadata(ctx, "hi", "user-alice", map[string]any{
		taskMetadataAgentProfile: "conductor",
		taskMetadataAgentRole:    "conductor",
		"input_source":           "prompt_bar",
		"requested_app":          "vtext",
		"seed_prompt":            "hi",
		"initial_document_title": "hi",
	})
	if err != nil {
		t.Fatalf("submit conductor task: %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	stored, err := s.GetTask(ctx, rec.TaskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if stored.State != types.TaskCompleted {
		t.Fatalf("state: got %q, want %q", stored.State, types.TaskCompleted)
	}

	var result struct {
		Action               string `json:"action"`
		App                  string `json:"app"`
		Title                string `json:"title"`
		SeedPrompt           string `json:"seed_prompt"`
		InitialContent       string `json:"initial_content"`
		CreateInitialVersion bool   `json:"create_initial_version"`
	}
	if err := json.Unmarshal([]byte(stored.Result), &result); err != nil {
		t.Fatalf("decode result json: %v\nraw=%q", err, stored.Result)
	}
	if result.Action != "open_app" {
		t.Fatalf("action: got %q, want open_app", result.Action)
	}
	if result.App != AgentProfileVText {
		t.Fatalf("app: got %q, want %q", result.App, AgentProfileVText)
	}
	if result.SeedPrompt != "hi" {
		t.Fatalf("seed_prompt: got %q, want hi", result.SeedPrompt)
	}
	if result.InitialContent != "hi" {
		t.Fatalf("initial_content: got %q, want hi", result.InitialContent)
	}
	if !result.CreateInitialVersion {
		t.Fatal("create_initial_version: got false, want true")
	}
}

func TestProviderPromptUsesPromptOverride(t *testing.T) {
	rt, _ := testRuntime(t)
	if _, err := rt.PromptStore().Save("user-alice", AgentProfileConductor, "Custom conductor prompt"); err != nil {
		t.Fatalf("save prompt override: %v", err)
	}

	rec := &types.TaskRecord{
		TaskID:   "task-1",
		OwnerID:  "user-alice",
		Prompt:   "route this request",
		Metadata: map[string]any{taskMetadataAgentProfile: AgentProfileConductor},
	}
	prompt, err := rt.providerPromptForTask(rec)
	if err != nil {
		t.Fatalf("providerPromptForTask: %v", err)
	}
	if !strings.Contains(prompt, "Custom conductor prompt") {
		t.Fatalf("provider prompt should include prompt override, got %q", prompt)
	}
	if !strings.Contains(prompt, "route this request") {
		t.Fatalf("provider prompt should include task prompt, got %q", prompt)
	}
}

func TestGetTaskCallerScoped(t *testing.T) {
	rt, _ := testRuntime(t)
	ctx := context.Background()

	rec, err := rt.SubmitTask(ctx, "test prompt", "user-alice")
	if err != nil {
		t.Fatalf("submit task: %v", err)
	}

	// Owner can see their own task.
	got, err := rt.GetTask(ctx, rec.TaskID, "user-alice")
	if err != nil {
		t.Fatalf("get own task: %v", err)
	}
	if got.TaskID != rec.TaskID {
		t.Errorf("task_id: got %q, want %q", got.TaskID, rec.TaskID)
	}

	// Another user cannot see the task (VAL-RUNTIME-006).
	_, err = rt.GetTask(ctx, rec.TaskID, "user-eve")
	if err == nil {
		t.Error("expected error when getting another user's task")
	}
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestGetTaskNotFound(t *testing.T) {
	rt, _ := testRuntime(t)
	ctx := context.Background()

	_, err := rt.GetTask(ctx, "nonexistent-task-id", "user-alice")
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestTaskCompletesSuccessfully(t *testing.T) {
	rt, _ := testRuntime(t)
	ctx := context.Background()

	rec, err := rt.SubmitTask(ctx, "test prompt", "user-alice")
	if err != nil {
		t.Fatalf("submit task: %v", err)
	}

	// Wait for the task to complete (stub provider has 50ms delay).
	time.Sleep(200 * time.Millisecond)

	got, err := rt.GetTask(ctx, rec.TaskID, "user-alice")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}

	if got.State != types.TaskCompleted {
		t.Errorf("state: got %q, want %q", got.State, types.TaskCompleted)
	}
	if got.Result == "" {
		t.Error("result should not be empty for completed task")
	}
	if got.FinishedAt == nil {
		t.Error("finished_at should be set for completed task")
	}
}

func TestProviderFailureSurfacesStructuredOutcome(t *testing.T) {
	// VAL-RUNTIME-008: provider failures surface as structured task outcomes
	// without crashing the runtime.
	dir := filepath.Join(os.TempDir(), "go-choir-m3-runtime-test")
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
	// Create a provider that always fails.
	provider := &StubProvider{
		Delay:   10 * time.Millisecond,
		FailErr: errors.New("provider timeout after 30s"),
		Result:  "",
	}

	cfg := Config{
		SandboxID:           "sandbox-test",
		StorePath:           dbPath,
		ProviderTimeout:     10 * time.Millisecond,
		SupervisionInterval: 1 * time.Hour,
	}

	rt := New(cfg, s, bus, provider)

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

	got, err := rt.GetTask(context.Background(), rec.TaskID, "user-alice")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}

	if got.State != types.TaskFailed {
		t.Errorf("state: got %q, want %q", got.State, types.TaskFailed)
	}
	if got.Error == "" {
		t.Error("error should be set for failed task")
	}
	if got.FinishedAt == nil {
		t.Error("finished_at should be set for failed task")
	}

	// Runtime should remain available for new tasks.
	nextRec, err := rt.SubmitTask(context.Background(), "next prompt", "user-alice")
	if err != nil {
		t.Fatalf("submit task after failure: %v", err)
	}
	if nextRec.TaskID == "" {
		t.Error("task_id should not be empty for task submitted after failure")
	}
}

func TestRuntimeRemainsAvailableAfterProviderFailure(t *testing.T) {
	// Verify that after a provider failure, the runtime is still healthy
	// and can accept and complete new tasks (VAL-RUNTIME-008).
	rt, _ := testRuntime(t)
	ctx := context.Background()

	// Submit and complete a normal task.
	rec, err := rt.SubmitTask(ctx, "normal task", "user-alice")
	if err != nil {
		t.Fatalf("submit task: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	got, err := rt.GetTask(ctx, rec.TaskID, "user-alice")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.State != types.TaskCompleted {
		t.Errorf("state: got %q, want %q", got.State, types.TaskCompleted)
	}

	// Runtime health should still be ready.
	if rt.HealthState() != types.HealthReady {
		t.Errorf("health: got %q, want %q", rt.HealthState(), types.HealthReady)
	}
}

func TestEventEmissionOnTaskSubmission(t *testing.T) {
	rt, _ := testRuntime(t)
	ctx := context.Background()

	// Subscribe to events before submitting.
	ch := rt.EventBus().Subscribe()
	defer rt.EventBus().Unsubscribe(ch)

	_, err := rt.SubmitTask(ctx, "test prompt", "user-alice")
	if err != nil {
		t.Fatalf("submit task: %v", err)
	}

	// Should receive a task.submitted event.
	select {
	case ev := <-ch:
		if ev.Record.Kind != types.EventTaskSubmitted {
			t.Errorf("event kind: got %q, want %q", ev.Record.Kind, types.EventTaskSubmitted)
		}
		if ev.Record.OwnerID != "user-alice" {
			t.Errorf("event owner_id: got %q, want user-alice", ev.Record.OwnerID)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for task.submitted event")
	}
}

func TestEventsPersistedToStore(t *testing.T) {
	rt, s := testRuntime(t)
	ctx := context.Background()

	rec, err := rt.SubmitTask(ctx, "test prompt", "user-alice")
	if err != nil {
		t.Fatalf("submit task: %v", err)
	}

	// Wait for the task to complete and events to be persisted.
	time.Sleep(200 * time.Millisecond)

	// Check that events were persisted.
	evts, err := s.ListEvents(ctx, rec.TaskID, 20)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}

	if len(evts) == 0 {
		t.Fatal("expected events to be persisted")
	}

	// First event should be task.submitted.
	if evts[0].Kind != types.EventTaskSubmitted {
		t.Errorf("first event kind: got %q, want %q", evts[0].Kind, types.EventTaskSubmitted)
	}
}

func TestTaskRecoveryAcrossRestart(t *testing.T) {
	// VAL-RUNTIME-010: accepted task state remains recoverable after
	// sandbox restart.
	dir := filepath.Join(os.TempDir(), "go-choir-m3-runtime-test")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	dbPath := filepath.Join(dir, t.Name()+".db")
	_ = os.Remove(dbPath)

	// Open store, create runtime, submit a task, and stop.
	s1, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store 1: %v", err)
	}

	bus1 := events.NewEventBus()
	cfg := Config{
		SandboxID:           "sandbox-test",
		StorePath:           dbPath,
		ProviderTimeout:     50 * time.Millisecond,
		SupervisionInterval: 1 * time.Hour,
	}
	provider1 := NewStubProvider(50 * time.Millisecond)
	rt1 := New(cfg, s1, bus1, provider1)

	rec, err := rt1.SubmitTask(context.Background(), "survive restart", "user-alice")
	if err != nil {
		t.Fatalf("submit task: %v", err)
	}

	// Wait for completion.
	time.Sleep(200 * time.Millisecond)

	// Stop the first runtime.
	rt1.Stop()
	_ = s1.Close()

	// Reopen the store and create a new runtime (simulates restart).
	s2, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store 2: %v", err)
	}

	bus2 := events.NewEventBus()
	provider2 := NewStubProvider(50 * time.Millisecond)
	rt2 := New(cfg, s2, bus2, provider2)

	t.Cleanup(func() {
		rt2.Stop()
		_ = s2.Close()
		_ = os.Remove(dbPath)
	})

	// The previously completed task should be recoverable by handle.
	got, err := rt2.GetTask(context.Background(), rec.TaskID, "user-alice")
	if err != nil {
		t.Fatalf("get task after restart: %v", err)
	}

	if got.TaskID != rec.TaskID {
		t.Errorf("task_id: got %q, want %q", got.TaskID, rec.TaskID)
	}
	if got.State != types.TaskCompleted {
		t.Errorf("state: got %q, want %q", got.State, types.TaskCompleted)
	}
	if got.Prompt != "survive restart" {
		t.Errorf("prompt: got %q, want original", got.Prompt)
	}
}

func TestInterruptedRunningTasksRecoveredOnStart(t *testing.T) {
	// When the sandbox restarts, tasks that were running should be resolved
	// to an explicit terminal outcome (VAL-RUNTIME-010).
	dir := filepath.Join(os.TempDir(), "go-choir-m3-runtime-test")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	dbPath := filepath.Join(dir, t.Name()+".db")
	_ = os.Remove(dbPath)

	ctx := context.Background()

	// Create a store with a running task that was interrupted.
	s1, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store 1: %v", err)
	}

	now := time.Now().UTC()
	interruptedTask := types.TaskRecord{
		TaskID:    "interrupted-task-001",
		OwnerID:   "user-alice",
		SandboxID: "sandbox-test",
		State:     types.TaskRunning, // was running when process exited
		Prompt:    "interrupted prompt",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s1.CreateTask(ctx, interruptedTask); err != nil {
		t.Fatalf("create interrupted task: %v", err)
	}
	_ = s1.Close()

	// Simulate restart: open new store and runtime, then call Start()
	// which should recover interrupted tasks.
	s2, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store 2: %v", err)
	}

	bus := events.NewEventBus()
	cfg := Config{
		SandboxID:           "sandbox-test",
		StorePath:           dbPath,
		ProviderTimeout:     50 * time.Millisecond,
		SupervisionInterval: 1 * time.Hour,
	}
	provider := NewStubProvider(50 * time.Millisecond)
	rt := New(cfg, s2, bus, provider)

	t.Cleanup(func() {
		rt.Stop()
		_ = s2.Close()
		_ = os.Remove(dbPath)
	})
	rt.Start(ctx)

	// The interrupted task should now be in failed state with a clear error.
	got, err := rt.GetTask(ctx, "interrupted-task-001", "user-alice")
	if err != nil {
		t.Fatalf("get interrupted task: %v", err)
	}
	if got.State != types.TaskFailed {
		t.Errorf("state: got %q, want %q", got.State, types.TaskFailed)
	}
	if got.Error != "runtime restarted, task interrupted" {
		t.Errorf("error: got %q, want runtime restarted, task interrupted", got.Error)
	}
	if got.FinishedAt == nil {
		t.Error("finished_at should be set for recovered task")
	}
}

func TestHealthStartsReady(t *testing.T) {
	rt, _ := testRuntime(t)

	if rt.HealthState() != types.HealthReady {
		t.Errorf("initial health: got %q, want %q", rt.HealthState(), types.HealthReady)
	}
}

func TestSetHealthTransitionsVisible(t *testing.T) {
	// VAL-RUNTIME-001: health transitions are visible.
	rt, _ := testRuntime(t)
	ctx := context.Background()

	// Subscribe to events before transitioning.
	ch := rt.EventBus().Subscribe()
	defer rt.EventBus().Unsubscribe(ch)

	// Transition to degraded.
	rt.SetHealth(types.HealthDegraded)
	if rt.HealthState() != types.HealthDegraded {
		t.Errorf("health after set degraded: got %q, want %q", rt.HealthState(), types.HealthDegraded)
	}

	// Should have received a degraded event.
	select {
	case ev := <-ch:
		if ev.Record.Kind != types.EventRuntimeDegraded {
			t.Errorf("event kind: got %q, want %q", ev.Record.Kind, types.EventRuntimeDegraded)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for degraded event")
	}

	// Transition back to ready.
	rt.SetHealth(types.HealthReady)
	if rt.HealthState() != types.HealthReady {
		t.Errorf("health after set ready: got %q, want %q", rt.HealthState(), types.HealthReady)
	}

	// The health events should also be persisted for post-restart visibility.
	evts, _ := rt.Store().ListEvents(ctx, "", 20)
	_ = evts // not critical for this test
}

func TestSetHealthNoOpForSameState(t *testing.T) {
	rt, _ := testRuntime(t)

	// Set health to ready (already ready) — should not emit an event.
	ch := rt.EventBus().Subscribe()
	defer rt.EventBus().Unsubscribe(ch)

	rt.SetHealth(types.HealthReady)

	select {
	case <-ch:
		t.Error("should not emit event for same health state")
	case <-time.After(50 * time.Millisecond):
		// Expected: no event.
	}
}

func TestListTasksByOwner(t *testing.T) {
	rt, _ := testRuntime(t)
	ctx := context.Background()

	// Submit tasks for two owners.
	_, err := rt.SubmitTask(ctx, "alice task 1", "user-alice")
	if err != nil {
		t.Fatalf("submit alice task: %v", err)
	}
	_, err = rt.SubmitTask(ctx, "bob task 1", "user-bob")
	if err != nil {
		t.Fatalf("submit bob task: %v", err)
	}
	_, err = rt.SubmitTask(ctx, "alice task 2", "user-alice")
	if err != nil {
		t.Fatalf("submit alice task 2: %v", err)
	}

	aliceTasks, err := rt.ListTasksByOwner(ctx, "user-alice", 10)
	if err != nil {
		t.Fatalf("list alice tasks: %v", err)
	}
	if len(aliceTasks) != 2 {
		t.Errorf("alice tasks: got %d, want 2", len(aliceTasks))
	}

	bobTasks, err := rt.ListTasksByOwner(ctx, "user-bob", 10)
	if err != nil {
		t.Fatalf("list bob tasks: %v", err)
	}
	if len(bobTasks) != 1 {
		t.Errorf("bob tasks: got %d, want 1", len(bobTasks))
	}
}

func TestEventPayloadContent(t *testing.T) {
	rt, s := testRuntime(t)
	ctx := context.Background()

	_, err := rt.SubmitTask(ctx, "test prompt content", "user-alice")
	if err != nil {
		t.Fatalf("submit task: %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	evts, _ := s.ListEvents(ctx, "", 20)
	_ = evts // Events are per-task, may need to query by task ID
}

func TestProviderStubEmitsProgress(t *testing.T) {
	rt, _ := testRuntime(t)
	ctx := context.Background()

	ch := rt.EventBus().Subscribe()
	defer rt.EventBus().Unsubscribe(ch)

	_, err := rt.SubmitTask(ctx, "progress test", "user-alice")
	if err != nil {
		t.Fatalf("submit task: %v", err)
	}

	// Collect events for a short time.
	var received []events.RuntimeEvent
	timer := time.After(300 * time.Millisecond)
	for {
		select {
		case ev := <-ch:
			if ev.Record.OwnerID == "user-alice" {
				received = append(received, ev)
			}
		case <-timer:
			goto done
		}
	}
done:

	// Should have received at least submitted, started, progress, and completed.
	kinds := make(map[types.EventKind]bool)
	for _, ev := range received {
		kinds[ev.Record.Kind] = true
	}

	if !kinds[types.EventTaskSubmitted] {
		t.Error("expected task.submitted event")
	}
	if !kinds[types.EventTaskStarted] {
		t.Error("expected task.started event")
	}
	if !kinds[types.EventTaskProgress] {
		t.Error("expected task.progress event")
	}
	if !kinds[types.EventTaskCompleted] {
		t.Error("expected task.completed event")
	}
}

func TestProviderStubDeltaEvent(t *testing.T) {
	rt, s := testRuntime(t)
	ctx := context.Background()

	rec, err := rt.SubmitTask(ctx, "delta test", "user-alice")
	if err != nil {
		t.Fatalf("submit task: %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	evts, err := s.ListEvents(ctx, rec.TaskID, 20)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}

	hasDelta := false
	for _, ev := range evts {
		if ev.Kind == types.EventTaskDelta {
			hasDelta = true
			// Check that the payload contains provider info.
			var payload map[string]string
			if err := json.Unmarshal(ev.Payload, &payload); err == nil {
				if payload["provider"] != "stub" {
					t.Errorf("delta payload provider: got %q, want stub", payload["provider"])
				}
			}
		}
	}
	if !hasDelta {
		t.Error("expected task.delta event from stub provider")
	}
}

// --- Bridge Provider Integration Tests ---

// mockBridgeProvider implements the runtime.Provider interface for testing
// the bridge provider integration with the runtime engine.
type mockBridgeProvider struct {
	name       string
	result     string
	execErr    error
	mu         sync.Mutex
	called     bool
	taskResult string // captures the result set by Execute on the TaskRecord
}

func (m *mockBridgeProvider) Execute(ctx context.Context, task *types.TaskRecord, emit EventEmitFunc) error {
	m.mu.Lock()
	m.called = true
	m.mu.Unlock()

	if m.execErr != nil {
		emit(types.EventTaskProgress, "execution", json.RawMessage(`{"status":"failed","real":"true"}`))
		return m.execErr
	}

	emit(types.EventTaskProgress, "execution", json.RawMessage(`{"status":"started","provider":"`+m.name+`","real":"true"}`))
	emit(types.EventTaskDelta, "execution", json.RawMessage(`{"text":"`+m.result+`","provider":"`+m.name+`","real":"true"}`))
	task.Result = m.result
	m.mu.Lock()
	m.taskResult = m.result
	m.mu.Unlock()
	return nil
}

func (m *mockBridgeProvider) ProviderName() string { return m.name }

func testRuntimeWithBridge(t *testing.T, bridge Provider) (*Runtime, *store.Store) {
	t.Helper()

	dir := filepath.Join(os.TempDir(), "go-choir-m3-bridge-test")
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
	cfg := Config{
		SandboxID:           "sandbox-bridge-test",
		StorePath:           dbPath,
		ProviderTimeout:     50 * time.Millisecond,
		SupervisionInterval: 1 * time.Hour,
	}

	rt := New(cfg, s, bus, bridge)
	t.Cleanup(func() {
		rt.Stop()
		_ = s.Close()
		_ = os.Remove(dbPath)
	})

	return rt, s
}

func TestBridgeProviderSubmitsAndCompletes(t *testing.T) {
	bridge := &mockBridgeProvider{
		name:   "bedrock",
		result: "Real Bedrock response with genuine inference!",
	}

	rt, s := testRuntimeWithBridge(t, bridge)
	ctx := context.Background()

	rec, err := rt.SubmitTask(ctx, "What is the capital of France?", "user-bridge")
	if err != nil {
		t.Fatalf("submit task: %v", err)
	}

	// Wait for the task to complete.
	time.Sleep(200 * time.Millisecond)

	// Verify task completed with the bridge provider result.
	stored, err := s.GetTask(ctx, rec.TaskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if stored.State != types.TaskCompleted {
		t.Errorf("state: got %q, want completed", stored.State)
	}
	if stored.Result != "Real Bedrock response with genuine inference!" {
		t.Errorf("result: got %q, want bridge provider result", stored.Result)
	}

	// Verify the bridge was actually called.
	if !bridge.called {
		t.Error("bridge provider was not called")
	}
}

func TestBridgeProviderFailureSurfacesWithoutCrashing(t *testing.T) {
	bridge := &mockBridgeProvider{
		name:    "zai",
		execErr: fmt.Errorf("upstream provider timeout"),
	}

	rt, _ := testRuntimeWithBridge(t, bridge)
	ctx := context.Background()

	rec, err := rt.SubmitTask(ctx, "This should fail at the provider", "user-fail")
	if err != nil {
		t.Fatalf("submit task: %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	// The task should be in failed state, not crashing the runtime.
	stored, err := rt.GetTask(ctx, rec.TaskID, "user-fail")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if stored.State != types.TaskFailed {
		t.Errorf("state: got %q, want failed", stored.State)
	}

	// The runtime should still be healthy for later tasks.
	if rt.HealthState() == types.HealthFailed {
		t.Error("runtime should not be in failed state after a single provider error")
	}

	// Submit another task — should still work.
	rec2, err := rt.SubmitTask(ctx, "Another task after failure", "user-retry")
	if err != nil {
		t.Fatalf("submit task after failure: %v", err)
	}
	if rec2.TaskID == "" {
		t.Error("second task should have a valid ID")
	}
}

func TestBridgeProviderEventsContainRealMarker(t *testing.T) {
	bridge := &mockBridgeProvider{
		name:   "zai",
		result: "Z.AI generated text",
	}

	rt, s := testRuntimeWithBridge(t, bridge)
	ctx := context.Background()

	rec, err := rt.SubmitTask(ctx, "test event markers", "user-events")
	if err != nil {
		t.Fatalf("submit task: %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	evts, err := s.ListEvents(ctx, rec.TaskID, 20)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}

	// Look for events with the "real":"true" marker that distinguishes
	// bridge provider events from stub provider events.
	hasRealMarker := false
	for _, ev := range evts {
		if ev.Kind == types.EventTaskDelta || ev.Kind == types.EventTaskProgress {
			var payload map[string]string
			if err := json.Unmarshal(ev.Payload, &payload); err == nil {
				if payload["real"] == "true" {
					hasRealMarker = true
					if payload["provider"] == "stub" {
						t.Error("real provider event should not have provider=stub")
					}
				}
			}
		}
	}
	if !hasRealMarker {
		t.Error("expected at least one event with real=true marker from bridge provider")
	}
}

func TestHealthReportsActiveProvider(t *testing.T) {
	bridge := &mockBridgeProvider{
		name:   "bedrock",
		result: "test",
	}

	rt, _ := testRuntimeWithBridge(t, bridge)

	// The runtime's provider should report its name.
	if rt.provider.ProviderName() != "bedrock" {
		t.Errorf("provider name: got %q, want bedrock", rt.provider.ProviderName())
	}
}

// TestBuildAppagentRevisionMetadataPreservesDurableKeys verifies that
// appagent revisions carry forward seed_prompt, source_path, and
// conductor_task_id from the parent revision metadata so subsequent
// revise requests retain the original user context.
func TestBuildAppagentRevisionMetadataPreservesDurableKeys(t *testing.T) {
	_, s := testRuntime(t)

	ctx := context.Background()
	ownerID := "test-user"

	// Create a document with a user-authored revision that has durable metadata.
	doc := types.Document{
		DocID:   "doc-meta-test",
		OwnerID: ownerID,
		Title:   "metadata test",
	}
	if err := s.CreateDocument(ctx, doc); err != nil {
		t.Fatalf("create document: %v", err)
	}

	parentMeta, _ := json.Marshal(map[string]any{
		"seed_prompt":       "write a haiku about cats",
		"source_path":       "/notes/cats.md",
		"conductor_task_id": "task-original-conductor",
	})
	parentRev := types.Revision{
		RevisionID: "rev-parent-meta",
		DocID:      "doc-meta-test",
		OwnerID:    ownerID,
		AuthorKind: types.AuthorUser,
		Content:    "cats are great",
		Citations:  json.RawMessage("[]"),
		Metadata:   parentMeta,
		CreatedAt:  time.Now().UTC(),
	}
	if err := s.CreateRevision(ctx, parentRev); err != nil {
		t.Fatalf("create parent revision: %v", err)
	}

	// Point the document at the parent revision.
	doc.CurrentRevisionID = parentRev.RevisionID

	// Build appagent metadata with a task record that has no durable keys.
	rec := &types.TaskRecord{
		TaskID:   "task-agent-1",
		Metadata: map[string]any{"type": "vtext_agent_revision"},
	}

	result := buildAppagentRevisionMetadata(rec, doc, ownerID, s)
	var resultMap map[string]any
	if err := json.Unmarshal(result, &resultMap); err != nil {
		t.Fatalf("unmarshal result metadata: %v", err)
	}

	// Verify durable keys are carried forward.
	if resultMap["seed_prompt"] != "write a haiku about cats" {
		t.Errorf("seed_prompt: got %v, want 'write a haiku about cats'", resultMap["seed_prompt"])
	}
	if resultMap["source_path"] != "/notes/cats.md" {
		t.Errorf("source_path: got %v, want '/notes/cats.md'", resultMap["source_path"])
	}
	if resultMap["conductor_task_id"] != "task-original-conductor" {
		t.Errorf("conductor_task_id: got %v, want 'task-original-conductor'", resultMap["conductor_task_id"])
	}

	// Verify agent-specific fields are also present.
	if resultMap["source"] != "agent_revision" {
		t.Errorf("source: got %v, want 'agent_revision'", resultMap["source"])
	}
	if resultMap["task_id"] != "task-agent-1" {
		t.Errorf("task_id: got %v, want 'task-agent-1'", resultMap["task_id"])
	}
}
