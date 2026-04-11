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
