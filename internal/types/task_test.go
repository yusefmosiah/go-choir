package types

import (
	"encoding/json"
	"testing"
	"time"
)

func TestTaskStateTerminal(t *testing.T) {
	terminalStates := []TaskState{TaskCompleted, TaskFailed, TaskCancelled}
	for _, s := range terminalStates {
		if !s.Terminal() {
			t.Errorf("expected %q to be terminal", s)
		}
	}

	nonTerminalStates := []TaskState{TaskPending, TaskRunning, TaskBlocked}
	for _, s := range nonTerminalStates {
		if s.Terminal() {
			t.Errorf("expected %q to not be terminal", s)
		}
	}
}

func TestTaskStateValid(t *testing.T) {
	validStates := []TaskState{
		TaskPending, TaskRunning, TaskCompleted,
		TaskFailed, TaskCancelled, TaskBlocked,
	}
	for _, s := range validStates {
		if !s.Valid() {
			t.Errorf("expected %q to be valid", s)
		}
	}

	invalidStates := []TaskState{"unknown", "", "created", "starting", "waiting_input"}
	for _, s := range invalidStates {
		if s.Valid() {
			t.Errorf("expected %q to be invalid", s)
		}
	}
}

func TestTaskRecordJSONRoundTrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	finishedAt := now.Add(10 * time.Second)

	rec := TaskRecord{
		TaskID:    "task-001",
		OwnerID:   "user-alice",
		SandboxID: "sandbox-dev",
		State:     TaskCompleted,
		Prompt:    "explain closures in Go",
		Result:    "Closures in Go capture variables...",
		CreatedAt: now,
		UpdatedAt: now.Add(5 * time.Second),
		FinishedAt: &finishedAt,
		Metadata: map[string]any{
			"model":    "claude-3",
			"provider": "bedrock",
		},
	}

	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal task record: %v", err)
	}

	var decoded TaskRecord
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal task record: %v", err)
	}

	if decoded.TaskID != rec.TaskID {
		t.Errorf("task_id: got %q, want %q", decoded.TaskID, rec.TaskID)
	}
	if decoded.OwnerID != rec.OwnerID {
		t.Errorf("owner_id: got %q, want %q", decoded.OwnerID, rec.OwnerID)
	}
	if decoded.State != rec.State {
		t.Errorf("state: got %q, want %q", decoded.State, rec.State)
	}
	if decoded.Prompt != rec.Prompt {
		t.Errorf("prompt: got %q, want %q", decoded.Prompt, rec.Prompt)
	}
	if decoded.Result != rec.Result {
		t.Errorf("result: got %q, want %q", decoded.Result, rec.Result)
	}
	if decoded.FinishedAt == nil {
		t.Fatal("finished_at should not be nil")
	}
	if !decoded.FinishedAt.Equal(finishedAt) {
		t.Errorf("finished_at: got %v, want %v", decoded.FinishedAt, finishedAt)
	}
}

func TestTaskRecordWithoutOptionalFields(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	rec := TaskRecord{
		TaskID:    "task-002",
		OwnerID:   "user-bob",
		SandboxID: "sandbox-dev",
		State:     TaskPending,
		Prompt:    "hello world",
		CreatedAt: now,
		UpdatedAt: now,
	}

	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal task record: %v", err)
	}

	var decoded TaskRecord
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal task record: %v", err)
	}

	if decoded.Result != "" {
		t.Errorf("expected empty result for pending task, got %q", decoded.Result)
	}
	if decoded.Error != "" {
		t.Errorf("expected empty error for pending task, got %q", decoded.Error)
	}
	if decoded.FinishedAt != nil {
		t.Error("expected nil finished_at for pending task")
	}
}

func TestEventRecordJSONRoundTrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	rec := EventRecord{
		EventID:   "evt-001",
		Seq:       1,
		Timestamp: now,
		TaskID:    "task-001",
		OwnerID:   "user-alice",
		Kind:      EventTaskStarted,
		Phase:     "execution",
		Payload:   json.RawMessage(`{"adapter":"host-process"}`),
	}

	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal event record: %v", err)
	}

	var decoded EventRecord
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal event record: %v", err)
	}

	if decoded.EventID != rec.EventID {
		t.Errorf("event_id: got %q, want %q", decoded.EventID, rec.EventID)
	}
	if decoded.Seq != rec.Seq {
		t.Errorf("seq: got %d, want %d", decoded.Seq, rec.Seq)
	}
	if decoded.Kind != rec.Kind {
		t.Errorf("kind: got %q, want %q", decoded.Kind, rec.Kind)
	}
	if decoded.TaskID != rec.TaskID {
		t.Errorf("task_id: got %q, want %q", decoded.TaskID, rec.TaskID)
	}
	if decoded.OwnerID != rec.OwnerID {
		t.Errorf("owner_id: got %q, want %q", decoded.OwnerID, rec.OwnerID)
	}
	if string(decoded.Payload) != string(rec.Payload) {
		t.Errorf("payload: got %q, want %q", string(decoded.Payload), string(rec.Payload))
	}
}

func TestEventRecordWithEmptyPayload(t *testing.T) {
	rec := EventRecord{
		EventID:   "evt-002",
		Seq:       2,
		Timestamp: time.Now().UTC(),
		TaskID:    "task-001",
		Kind:      EventTaskCompleted,
		Payload:   json.RawMessage(`{}`),
	}

	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal event record: %v", err)
	}

	var decoded EventRecord
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal event record: %v", err)
	}

	if string(decoded.Payload) != `{}` {
		t.Errorf("payload: got %q, want {}", string(decoded.Payload))
	}
}

func TestRuntimeHealthStateValues(t *testing.T) {
	states := map[RuntimeHealthState]bool{
		HealthReady:    true,
		HealthDegraded: true,
		HealthFailed:   true,
		"unknown":      false,
	}
	for s, expected := range states {
		valid := s == HealthReady || s == HealthDegraded || s == HealthFailed
		if valid != expected {
			t.Errorf("health state %q: got valid=%v, want %v", s, valid, expected)
		}
	}
}

func TestWorkItemJSONRoundTrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)

	item := WorkItem{
		ID:        "work-001",
		ParentID:  "parent-001",
		OwnerID:   "user-alice",
		Objective: "research topic X",
		State:     TaskCompleted,
		Result:    "found 5 papers",
		Error:     "",
		CreatedAt: now,
		UpdatedAt: now.Add(5 * time.Second),
	}

	data, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("marshal work item: %v", err)
	}

	var decoded WorkItem
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal work item: %v", err)
	}

	if decoded.ID != item.ID {
		t.Errorf("id: got %q, want %q", decoded.ID, item.ID)
	}
	if decoded.ParentID != item.ParentID {
		t.Errorf("parent_id: got %q, want %q", decoded.ParentID, item.ParentID)
	}
	if decoded.OwnerID != item.OwnerID {
		t.Errorf("owner_id: got %q, want %q", decoded.OwnerID, item.OwnerID)
	}
	if decoded.Objective != item.Objective {
		t.Errorf("objective: got %q, want %q", decoded.Objective, item.Objective)
	}
	if decoded.State != item.State {
		t.Errorf("state: got %q, want %q", decoded.State, item.State)
	}
	if decoded.Result != item.Result {
		t.Errorf("result: got %q, want %q", decoded.Result, item.Result)
	}
	if !decoded.CreatedAt.Equal(item.CreatedAt) {
		t.Errorf("created_at: got %v, want %v", decoded.CreatedAt, item.CreatedAt)
	}
	if !decoded.UpdatedAt.Equal(item.UpdatedAt) {
		t.Errorf("updated_at: got %v, want %v", decoded.UpdatedAt, item.UpdatedAt)
	}
}

func TestWorkItemOmitsEmptyFields(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	item := WorkItem{
		ID:        "work-002",
		ParentID:  "",
		OwnerID:   "user-bob",
		Objective: "root task",
		State:     TaskPending,
		CreatedAt: now,
		UpdatedAt: now,
	}

	data, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("marshal work item: %v", err)
	}

	// Verify parent_id is omitted when empty.
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal to map: %v", err)
	}
	if _, ok := raw["parent_id"]; ok {
		t.Error("expected parent_id to be omitted when empty")
	}
	if _, ok := raw["result"]; ok {
		t.Error("expected result to be omitted when empty")
	}
	if _, ok := raw["error"]; ok {
		t.Error("expected error to be omitted when empty")
	}
}

func TestWorkItemUsesTaskState(t *testing.T) {
	// Verify WorkItem uses the same TaskState vocabulary.
	states := []TaskState{TaskPending, TaskRunning, TaskCompleted, TaskFailed, TaskCancelled}
	for _, state := range states {
		item := WorkItem{ID: "test", State: state}
		if !item.State.Valid() {
			t.Errorf("work item state %q should be valid", state)
		}
	}
}

func TestEtextAgentRevisionEventKinds(t *testing.T) {
	// Verify the etext agent revision event kinds exist and are distinct.
	eventKinds := []EventKind{
		EventEtextAgentRevisionStarted,
		EventEtextAgentRevisionProgress,
		EventEtextAgentRevisionCompleted,
		EventEtextAgentRevisionFailed,
	}
	expected := []string{
		"etext.agent_revision.started",
		"etext.agent_revision.progress",
		"etext.agent_revision.completed",
		"etext.agent_revision.failed",
	}
	for i, kind := range eventKinds {
		if string(kind) != expected[i] {
			t.Errorf("event kind %d: got %q, want %q", i, kind, expected[i])
		}
	}

	// Verify they're distinct from each other.
	seen := map[string]bool{}
	for _, kind := range eventKinds {
		if seen[string(kind)] {
			t.Errorf("duplicate event kind: %q", kind)
		}
		seen[string(kind)] = true
	}

	// Verify they're distinct from task lifecycle events.
	taskKinds := []EventKind{
		EventTaskSubmitted, EventTaskStarted, EventTaskCompleted,
		EventTaskFailed, EventTaskCancelled,
	}
	for _, tk := range taskKinds {
		if seen[string(tk)] {
			t.Errorf("etext event kind collides with task event kind: %q", tk)
		}
	}
}
