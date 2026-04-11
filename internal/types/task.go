// Package types defines the core runtime types for the go-choir sandbox runtime.
//
// These types represent the foundational vocabulary for Mission 3: task handles,
// lifecycle states, event records, and the minimal type surface needed for stable
// task IDs, persisted state, and later API milestones.
//
// Design decisions:
//   - No adapter-wrapper or native-session model; the runtime loop runs as
//     direct goroutines, not subprocesses.
//   - TaskState is a simpler lifecycle than Cogent's JobState because go-choir
//     tasks combine session, job, and turn into a single unit of work.
//   - OwnerID links tasks to the authenticated user who submitted them so that
//     status/event surfaces can scope by caller.
//   - Task IDs are UUID strings, generated once at submission and stable across
//     restart, supporting VAL-RUNTIME-003 and VAL-RUNTIME-010.
package types

import (
	"encoding/json"
	"time"
)

// TaskState represents the lifecycle state of a runtime task.
type TaskState string

const (
	// TaskPending means the task was submitted but has not started executing.
	TaskPending TaskState = "pending"

	// TaskRunning means the task is actively executing.
	TaskRunning TaskState = "running"

	// TaskCompleted means the task finished successfully.
	TaskCompleted TaskState = "completed"

	// TaskFailed means the task failed with a structured error outcome.
	// The runtime remains available for later tasks (VAL-RUNTIME-008).
	TaskFailed TaskState = "failed"

	// TaskCancelled means the task was cancelled before completion.
	TaskCancelled TaskState = "cancelled"

	// TaskBlocked means the task is blocked (e.g., provider failure)
	// and may be retried or resolved later.
	TaskBlocked TaskState = "blocked"
)

// Terminal returns true if the state is a terminal state that will not
// transition further.
func (s TaskState) Terminal() bool {
	switch s {
	case TaskCompleted, TaskFailed, TaskCancelled:
		return true
	default:
		return false
	}
}

// Valid returns true if the TaskState value is a recognized state.
func (s TaskState) Valid() bool {
	switch s {
	case TaskPending, TaskRunning, TaskCompleted, TaskFailed, TaskCancelled, TaskBlocked:
		return true
	default:
		return false
	}
}

// TaskRecord is the persisted representation of a submitted runtime task.
// It carries the stable task ID, owner identity, lifecycle state, and
// enough context for status lookup, event correlation, and restart recovery.
type TaskRecord struct {
	// TaskID is the stable unique identifier for this task, generated at
	// submission time and persisted for the lifetime of the record.
	// This is the handle used by status/event surfaces (VAL-RUNTIME-003).
	TaskID string `json:"task_id"`

	// OwnerID is the authenticated user who submitted the task.
	// Status and event surfaces scope by owner (VAL-RUNTIME-006).
	OwnerID string `json:"owner_id"`

	// SandboxID is the sandbox identity that accepted the task.
	SandboxID string `json:"sandbox_id"`

	// State is the current lifecycle state of the task.
	State TaskState `json:"state"`

	// Prompt is the user-submitted input that initiated the task.
	Prompt string `json:"prompt"`

	// Result holds the final output text when the task completes.
	// Empty until the task reaches a terminal state with a result.
	Result string `json:"result,omitempty"`

	// Error holds a structured error message when the task fails or is blocked.
	// Empty unless the task is in TaskFailed or TaskBlocked state.
	Error string `json:"error,omitempty"`

	// CreatedAt is the time the task was submitted.
	CreatedAt time.Time `json:"created_at"`

	// UpdatedAt is the time the task state was last changed.
	UpdatedAt time.Time `json:"updated_at"`

	// FinishedAt is the time the task reached a terminal state, or nil.
	FinishedAt *time.Time `json:"finished_at,omitempty"`

	// Metadata holds extensible key-value data for the task.
	Metadata map[string]any `json:"metadata,omitempty"`
}

// EventKind represents the kind of a runtime event emitted during task execution.
type EventKind string

const (
	// EventTaskSubmitted is emitted when a task is submitted and accepted.
	EventTaskSubmitted EventKind = "task.submitted"

	// EventTaskStarted is emitted when a task begins executing.
	EventTaskStarted EventKind = "task.started"

	// EventTaskProgress is emitted for incremental progress updates during
	// task execution.
	EventTaskProgress EventKind = "task.progress"

	// EventTaskDelta is emitted for streaming text deltas from the provider
	// response, supporting incremental event streaming (VAL-RUNTIME-005).
	EventTaskDelta EventKind = "task.delta"

	// EventTaskCompleted is emitted when a task finishes successfully.
	EventTaskCompleted EventKind = "task.completed"

	// EventTaskFailed is emitted when a task fails with a structured error
	// outcome (VAL-RUNTIME-008).
	EventTaskFailed EventKind = "task.failed"

	// EventTaskBlocked is emitted when a task is blocked (e.g., provider failure).
	EventTaskBlocked EventKind = "task.blocked"

	// EventTaskCancelled is emitted when a task is cancelled.
	EventTaskCancelled EventKind = "task.cancelled"

	// EventRuntimeHealth is emitted when the runtime health state changes.
	EventRuntimeHealth EventKind = "runtime.health"

	// EventRuntimeDegraded is emitted when the runtime enters a degraded state.
	EventRuntimeDegraded EventKind = "runtime.degraded"
)

// EventRecord represents a single runtime event emitted during task execution
// or runtime lifecycle changes. Events are ordered by sequence number within
// a task, persisted for restart recovery, and streamed through /api/events
// (VAL-RUNTIME-005).
type EventRecord struct {
	// EventID is the unique identifier for this event.
	EventID string `json:"event_id"`

	// Seq is the per-task sequence number, assigned monotonically on append.
	// Events for the same task can be fetched incrementally using after-seq
	// cursors.
	Seq int64 `json:"seq"`

	// Timestamp is when the event occurred.
	Timestamp time.Time `json:"ts"`

	// TaskID is the task this event is correlated to. For runtime-level events
	// (health, degraded), this may be empty.
	TaskID string `json:"task_id,omitempty"`

	// OwnerID is the authenticated user who owns the task, used for
	// caller-scoped event streaming (VAL-RUNTIME-006).
	OwnerID string `json:"owner_id,omitempty"`

	// Kind is the event kind from the vocabulary above.
	Kind EventKind `json:"kind"`

	// Phase provides additional phase context for the event (e.g., "execution",
	// "translation", "recovery").
	Phase string `json:"phase,omitempty"`

	// Payload carries the event-specific data as a JSON blob.
	Payload json.RawMessage `json:"payload"`
}

// RuntimeHealthState represents the health state of the runtime.
type RuntimeHealthState string

const (
	// HealthReady means the runtime is ready for task handling.
	HealthReady RuntimeHealthState = "ready"

	// HealthDegraded means the runtime is degraded but partially functional.
	// This is surfaced as degraded rather than hidden behind a generic healthy
	// response (VAL-RUNTIME-001).
	HealthDegraded RuntimeHealthState = "degraded"

	// HealthFailed means the runtime is not functional.
	HealthFailed RuntimeHealthState = "failed"
)
