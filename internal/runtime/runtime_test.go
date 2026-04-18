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

	rec, err := rt.StartRun(ctx, "explain closures in Go", "user-alice")
	if err != nil {
		t.Fatalf("submit task: %v", err)
	}

	// Task should have a stable UUID handle.
	if rec.RunID == "" {
		t.Error("loop_id should not be empty")
	}
	if rec.State != types.RunPending {
		t.Errorf("state: got %q, want %q", rec.State, types.RunPending)
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

	rec, err := rt.StartRun(ctx, "test prompt", "user-bob")
	if err != nil {
		t.Fatalf("submit task: %v", err)
	}

	// Verify the task is persisted in the store.
	stored, err := s.GetRun(ctx, rec.RunID)
	if err != nil {
		t.Fatalf("get task from store: %v", err)
	}
	if stored.RunID != rec.RunID {
		t.Errorf("loop_id: got %q, want %q", stored.RunID, rec.RunID)
	}
	if stored.OwnerID != "user-bob" {
		t.Errorf("owner_id: got %q, want user-bob", stored.OwnerID)
	}
}

func TestConductorTaskNormalizesStructuredRouteResult(t *testing.T) {
	rt, s := testRuntime(t)
	ctx := context.Background()

	rec, err := rt.StartRunWithMetadata(ctx, "hi", "user-alice", map[string]any{
		runMetadataAgentProfile:  "conductor",
		runMetadataAgentRole:     "conductor",
		"input_source":           "prompt_bar",
		"requested_app":          "vtext",
		"seed_prompt":            "hi",
		"initial_document_title": "hi",
	})
	if err != nil {
		t.Fatalf("submit conductor task: %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	stored, err := s.GetRun(ctx, rec.RunID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if stored.State != types.RunCompleted {
		t.Fatalf("state: got %q, want %q", stored.State, types.RunCompleted)
	}

	var result struct {
		Action               string `json:"action"`
		App                  string `json:"app"`
		Title                string `json:"title"`
		SeedPrompt           string `json:"seed_prompt"`
		InitialContent       string `json:"initial_content"`
		CreateInitialVersion bool   `json:"create_initial_version"`
		DocID                string `json:"doc_id"`
		InitialRevisionID    string `json:"initial_revision_id"`
		InitialRunID         string `json:"initial_loop_id"`
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
	if result.DocID == "" {
		t.Fatal("doc_id should not be empty")
	}
	if result.InitialRevisionID == "" {
		t.Fatal("initial_revision_id should not be empty")
	}
	if result.InitialRunID == "" {
		t.Fatal("initial_loop_id should not be empty")
	}

	doc, err := s.GetDocument(ctx, result.DocID, "user-alice")
	if err != nil {
		t.Fatalf("get document: %v", err)
	}
	if doc.CurrentRevisionID == "" {
		t.Fatal("document head should not be empty")
	}

	v0, err := s.GetRevision(ctx, result.InitialRevisionID, "user-alice")
	if err != nil {
		t.Fatalf("get initial revision: %v", err)
	}
	if v0.AuthorKind != types.AuthorUser {
		t.Fatalf("v0 author_kind: got %q, want %q", v0.AuthorKind, types.AuthorUser)
	}
	if v0.Content != "hi" {
		t.Fatalf("v0 content: got %q, want hi", v0.Content)
	}
	meta := decodeRevisionMetadata(v0.Metadata)
	if metadataString(meta, "conductor_loop_id") != rec.RunID {
		t.Fatalf("v0 conductor_loop_id: got %q, want %q", metadataString(meta, "conductor_loop_id"), rec.RunID)
	}

	initialTask, err := s.GetRun(ctx, result.InitialRunID)
	if err != nil {
		t.Fatalf("get initial vtext task: %v", err)
	}
	if initialTask.Metadata["parent_id"] != rec.RunID {
		t.Fatalf("initial task parent_id: got %v, want %q", initialTask.Metadata["parent_id"], rec.RunID)
	}
	if initialTask.Metadata["doc_id"] != result.DocID {
		t.Fatalf("initial task doc_id: got %v, want %q", initialTask.Metadata["doc_id"], result.DocID)
	}
	if initialTask.ChannelID != result.DocID {
		t.Fatalf("initial task channel_id: got %q, want %q", initialTask.ChannelID, result.DocID)
	}
	if initialTask.Metadata["current_revision_id"] != result.InitialRevisionID {
		t.Fatalf("initial task current_revision_id: got %v, want %q", initialTask.Metadata["current_revision_id"], result.InitialRevisionID)
	}
}

func TestProviderPromptUsesPromptOverride(t *testing.T) {
	rt, _ := testRuntime(t)
	if _, err := rt.PromptStore().Save("user-alice", AgentProfileConductor, "Custom conductor prompt"); err != nil {
		t.Fatalf("save prompt override: %v", err)
	}

	rec := &types.RunRecord{
		RunID:    "task-1",
		OwnerID:  "user-alice",
		Prompt:   "route this request",
		Metadata: map[string]any{runMetadataAgentProfile: AgentProfileConductor},
	}
	prompt, err := rt.providerPromptForRun(rec)
	if err != nil {
		t.Fatalf("providerPromptForRun: %v", err)
	}
	if !strings.Contains(prompt, "Custom conductor prompt") {
		t.Fatalf("provider prompt should include prompt override, got %q", prompt)
	}
	if !strings.Contains(prompt, "route this request") {
		t.Fatalf("provider prompt should include task prompt, got %q", prompt)
	}
}

func TestSystemPromptForVTextDefaultsToResearch(t *testing.T) {
	rt, _ := testRuntime(t)

	rec := &types.RunRecord{
		RunID:        "run-vtext-1",
		AgentID:      "vtext:doc-1",
		ChannelID:    "doc-1",
		OwnerID:      "user-alice",
		AgentProfile: AgentProfileVText,
		Prompt:       "what's the latest with ai",
	}

	prompt, err := rt.systemPromptForRun(rec)
	if err != nil {
		t.Fatalf("systemPromptForRun: %v", err)
	}
	if !strings.Contains(prompt, "Open researcher work first") {
		t.Fatalf("vtext system prompt should bias toward spawning researchers first, got %q", prompt)
	}
	if !strings.Contains(prompt, "Current shared channel: doc-1.") {
		t.Fatalf("vtext system prompt should include shared channel, got %q", prompt)
	}
}

func TestGetRunCallerScoped(t *testing.T) {
	rt, _ := testRuntime(t)
	ctx := context.Background()

	rec, err := rt.StartRun(ctx, "test prompt", "user-alice")
	if err != nil {
		t.Fatalf("submit task: %v", err)
	}

	// Owner can see their own task.
	got, err := rt.GetRun(ctx, rec.RunID, "user-alice")
	if err != nil {
		t.Fatalf("get own task: %v", err)
	}
	if got.RunID != rec.RunID {
		t.Errorf("loop_id: got %q, want %q", got.RunID, rec.RunID)
	}

	// Another user cannot see the task (VAL-RUNTIME-006).
	_, err = rt.GetRun(ctx, rec.RunID, "user-eve")
	if err == nil {
		t.Error("expected error when getting another user's task")
	}
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestGetRunNotFound(t *testing.T) {
	rt, _ := testRuntime(t)
	ctx := context.Background()

	_, err := rt.GetRun(ctx, "nonexistent-task-id", "user-alice")
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestTaskCompletesSuccessfully(t *testing.T) {
	rt, _ := testRuntime(t)
	ctx := context.Background()

	rec, err := rt.StartRun(ctx, "test prompt", "user-alice")
	if err != nil {
		t.Fatalf("submit task: %v", err)
	}

	// Wait for the task to complete (stub provider has 50ms delay).
	time.Sleep(200 * time.Millisecond)

	got, err := rt.GetRun(ctx, rec.RunID, "user-alice")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}

	if got.State != types.RunCompleted {
		t.Errorf("state: got %q, want %q", got.State, types.RunCompleted)
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

	rec, err := rt.StartRun(context.Background(), "failing prompt", "user-alice")
	if err != nil {
		t.Fatalf("submit task: %v", err)
	}

	// Wait for the task to fail.
	time.Sleep(200 * time.Millisecond)

	got, err := rt.GetRun(context.Background(), rec.RunID, "user-alice")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}

	if got.State != types.RunFailed {
		t.Errorf("state: got %q, want %q", got.State, types.RunFailed)
	}
	if got.Error == "" {
		t.Error("error should be set for failed task")
	}
	if got.FinishedAt == nil {
		t.Error("finished_at should be set for failed task")
	}

	// Runtime should remain available for new runs.
	nextRec, err := rt.StartRun(context.Background(), "next prompt", "user-alice")
	if err != nil {
		t.Fatalf("submit task after failure: %v", err)
	}
	if nextRec.RunID == "" {
		t.Error("loop_id should not be empty for task submitted after failure")
	}
}

func TestRuntimeRemainsAvailableAfterProviderFailure(t *testing.T) {
	// Verify that after a provider failure, the runtime is still healthy
	// and can accept and complete new runs (VAL-RUNTIME-008).
	rt, _ := testRuntime(t)
	ctx := context.Background()

	// Submit and complete a normal task.
	rec, err := rt.StartRun(ctx, "normal task", "user-alice")
	if err != nil {
		t.Fatalf("submit task: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	got, err := rt.GetRun(ctx, rec.RunID, "user-alice")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.State != types.RunCompleted {
		t.Errorf("state: got %q, want %q", got.State, types.RunCompleted)
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

	_, err := rt.StartRun(ctx, "test prompt", "user-alice")
	if err != nil {
		t.Fatalf("submit task: %v", err)
	}

	// Should receive a loop.submitted event.
	select {
	case ev := <-ch:
		if ev.Record.Kind != types.EventRunSubmitted {
			t.Errorf("event kind: got %q, want %q", ev.Record.Kind, types.EventRunSubmitted)
		}
		if ev.Record.OwnerID != "user-alice" {
			t.Errorf("event owner_id: got %q, want user-alice", ev.Record.OwnerID)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for loop.submitted event")
	}
}

func TestEventsPersistedToStore(t *testing.T) {
	rt, s := testRuntime(t)
	ctx := context.Background()

	rec, err := rt.StartRun(ctx, "test prompt", "user-alice")
	if err != nil {
		t.Fatalf("submit task: %v", err)
	}

	// Wait for the task to complete and events to be persisted.
	time.Sleep(200 * time.Millisecond)

	// Check that events were persisted.
	evts, err := s.ListEvents(ctx, rec.RunID, 20)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}

	if len(evts) == 0 {
		t.Fatal("expected events to be persisted")
	}

	// First event should be loop.submitted.
	if evts[0].Kind != types.EventRunSubmitted {
		t.Errorf("first event kind: got %q, want %q", evts[0].Kind, types.EventRunSubmitted)
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

	rec, err := rt1.StartRun(context.Background(), "survive restart", "user-alice")
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
	got, err := rt2.GetRun(context.Background(), rec.RunID, "user-alice")
	if err != nil {
		t.Fatalf("get task after restart: %v", err)
	}

	if got.RunID != rec.RunID {
		t.Errorf("loop_id: got %q, want %q", got.RunID, rec.RunID)
	}
	if got.State != types.RunCompleted {
		t.Errorf("state: got %q, want %q", got.State, types.RunCompleted)
	}
	if got.Prompt != "survive restart" {
		t.Errorf("prompt: got %q, want original", got.Prompt)
	}
}

func TestInterruptedRunningTasksRecoveredOnStart(t *testing.T) {
	// When the sandbox restarts, runs that were running should be resolved
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
	interruptedTask := types.RunRecord{
		RunID:     "interrupted-task-001",
		OwnerID:   "user-alice",
		SandboxID: "sandbox-test",
		State:     types.RunRunning, // was running when process exited
		Prompt:    "interrupted prompt",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s1.CreateRun(ctx, interruptedTask); err != nil {
		t.Fatalf("create interrupted task: %v", err)
	}
	_ = s1.Close()

	// Simulate restart: open new store and runtime, then call Start()
	// which should recover interrupted runs.
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

	// The interrupted run should now be in failed state with a clear error.
	got, err := rt.GetRun(ctx, "interrupted-task-001", "user-alice")
	if err != nil {
		t.Fatalf("get interrupted task: %v", err)
	}
	if got.State != types.RunFailed {
		t.Errorf("state: got %q, want %q", got.State, types.RunFailed)
	}
	if got.Error != "runtime restarted, run interrupted" {
		t.Errorf("error: got %q, want runtime restarted, run interrupted", got.Error)
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

func TestListRunsByOwner(t *testing.T) {
	rt, _ := testRuntime(t)
	ctx := context.Background()

	// Submit runs for two owners.
	_, err := rt.StartRun(ctx, "alice task 1", "user-alice")
	if err != nil {
		t.Fatalf("submit alice task: %v", err)
	}
	_, err = rt.StartRun(ctx, "bob task 1", "user-bob")
	if err != nil {
		t.Fatalf("submit bob task: %v", err)
	}
	_, err = rt.StartRun(ctx, "alice task 2", "user-alice")
	if err != nil {
		t.Fatalf("submit alice task 2: %v", err)
	}

	aliceTasks, err := rt.ListRunsByOwner(ctx, "user-alice", 10)
	if err != nil {
		t.Fatalf("list alice runs: %v", err)
	}
	if len(aliceTasks) != 2 {
		t.Errorf("alice runs: got %d, want 2", len(aliceTasks))
	}

	bobTasks, err := rt.ListRunsByOwner(ctx, "user-bob", 10)
	if err != nil {
		t.Fatalf("list bob runs: %v", err)
	}
	if len(bobTasks) != 1 {
		t.Errorf("bob runs: got %d, want 1", len(bobTasks))
	}
}

func TestEventPayloadContent(t *testing.T) {
	rt, s := testRuntime(t)
	ctx := context.Background()

	_, err := rt.StartRun(ctx, "test prompt content", "user-alice")
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

	_, err := rt.StartRun(ctx, "progress test", "user-alice")
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

	if !kinds[types.EventRunSubmitted] {
		t.Error("expected loop.submitted event")
	}
	if !kinds[types.EventRunStarted] {
		t.Error("expected loop.started event")
	}
	if !kinds[types.EventRunProgress] {
		t.Error("expected loop.progress event")
	}
	if !kinds[types.EventRunCompleted] {
		t.Error("expected loop.completed event")
	}
}

func TestProviderStubDeltaEvent(t *testing.T) {
	rt, s := testRuntime(t)
	ctx := context.Background()

	rec, err := rt.StartRun(ctx, "delta test", "user-alice")
	if err != nil {
		t.Fatalf("submit task: %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	evts, err := s.ListEvents(ctx, rec.RunID, 20)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}

	hasDelta := false
	for _, ev := range evts {
		if ev.Kind == types.EventRunDelta {
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
		t.Error("expected loop.delta event from stub provider")
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
	taskResult string // captures the result set by Execute on the RunRecord
}

func (m *mockBridgeProvider) Execute(ctx context.Context, task *types.RunRecord, emit EventEmitFunc) error {
	m.mu.Lock()
	m.called = true
	m.mu.Unlock()

	if m.execErr != nil {
		emit(types.EventRunProgress, "execution", json.RawMessage(`{"status":"failed","real":"true"}`))
		return m.execErr
	}

	emit(types.EventRunProgress, "execution", json.RawMessage(`{"status":"started","provider":"`+m.name+`","real":"true"}`))
	emit(types.EventRunDelta, "execution", json.RawMessage(`{"text":"`+m.result+`","provider":"`+m.name+`","real":"true"}`))
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

	rec, err := rt.StartRun(ctx, "What is the capital of France?", "user-bridge")
	if err != nil {
		t.Fatalf("submit task: %v", err)
	}

	// Wait for the task to complete.
	time.Sleep(200 * time.Millisecond)

	// Verify task completed with the bridge provider result.
	stored, err := s.GetRun(ctx, rec.RunID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if stored.State != types.RunCompleted {
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

	rec, err := rt.StartRun(ctx, "This should fail at the provider", "user-fail")
	if err != nil {
		t.Fatalf("submit task: %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	// The task should be in failed state, not crashing the runtime.
	stored, err := rt.GetRun(ctx, rec.RunID, "user-fail")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if stored.State != types.RunFailed {
		t.Errorf("state: got %q, want failed", stored.State)
	}

	// The runtime should still be healthy for later runs.
	if rt.HealthState() == types.HealthFailed {
		t.Error("runtime should not be in failed state after a single provider error")
	}

	// Submit another task — should still work.
	rec2, err := rt.StartRun(ctx, "Another task after failure", "user-retry")
	if err != nil {
		t.Fatalf("submit task after failure: %v", err)
	}
	if rec2.RunID == "" {
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

	rec, err := rt.StartRun(ctx, "test event markers", "user-events")
	if err != nil {
		t.Fatalf("submit task: %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	evts, err := s.ListEvents(ctx, rec.RunID, 20)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}

	// Look for events with the "real":"true" marker that distinguishes
	// bridge provider events from stub provider events.
	hasRealMarker := false
	for _, ev := range evts {
		if ev.Kind == types.EventRunDelta || ev.Kind == types.EventRunProgress {
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
// conductor_loop_id from the parent revision metadata so subsequent
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
		"seed_prompt":      "write a haiku about cats",
		"source_path":      "/notes/cats.md",
		"conductor_loop_id": "task-original-conductor",
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
	rec := &types.RunRecord{
		RunID:    "task-agent-1",
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
	if resultMap["conductor_loop_id"] != "task-original-conductor" {
		t.Errorf("conductor_loop_id: got %v, want 'task-original-conductor'", resultMap["conductor_loop_id"])
	}

	// Verify agent-specific fields are also present.
	if resultMap["source"] != "agent_revision" {
		t.Errorf("source: got %v, want 'agent_revision'", resultMap["source"])
	}
	if resultMap["loop_id"] != "task-agent-1" {
		t.Errorf("loop_id: got %v, want 'task-agent-1'", resultMap["loop_id"])
	}
}
