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

	// EventToolInvoked is emitted when the tool-calling loop invokes a
	// registered tool. The payload includes the tool name, call ID, and
	// argument summary (VAL-RUNTIME-005: tool-driven progress is observable).
	EventToolInvoked EventKind = "tool.invoked"

	// EventToolResult is emitted when a tool invocation completes. The
	// payload includes the tool name, call ID, and result summary.
	EventToolResult EventKind = "tool.result"

	// EventChannelMessage is emitted when a message is posted to an agent
	// channel, making inter-agent coordination observable through the
	// event stream.
	EventChannelMessage EventKind = "channel.message"

	// EventVTextAgentRevisionStarted is emitted when an appagent-driven
	// document revision starts executing. The payload includes the doc_id
	// so the frontend can correlate the revision to the open document
	// (VAL-ETEXT-004).
	EventVTextAgentRevisionStarted EventKind = "vtext.agent_revision.started"

	// EventVTextAgentRevisionProgress is emitted during appagent revision
	// execution, carrying incremental progress that the open document
	// can display without manual refresh (VAL-ETEXT-004).
	EventVTextAgentRevisionProgress EventKind = "vtext.agent_revision.progress"

	// EventVTextAgentRevisionCompleted is emitted when an appagent-driven
	// revision completes and the canonical revision is created. The payload
	// includes the doc_id and revision_id (VAL-ETEXT-003, VAL-ETEXT-004).
	EventVTextAgentRevisionCompleted EventKind = "vtext.agent_revision.completed"

	// EventVTextAgentRevisionFailed is emitted when an appagent-driven
	// revision fails. The payload includes the doc_id and error message.
	EventVTextAgentRevisionFailed EventKind = "vtext.agent_revision.failed"
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

// ToolCall represents a single tool invocation request from the LLM provider.
// When the provider returns a tool_use stop reason, each call specifies which
// tool to invoke and with what arguments. The tool-calling loop executes these
// calls and returns the results to the provider for the next turn.
type ToolCall struct {
	// ID is the provider-assigned call identifier, used to correlate the
	// result back to the provider's conversation history.
	ID string `json:"id"`

	// Name is the registered tool name to invoke.
	Name string `json:"name"`

	// Arguments is the raw JSON arguments object from the provider.
	Arguments json.RawMessage `json:"arguments"`
}

// ToolResult represents the output of a tool invocation, sent back to the
// provider as a tool_result content block in the conversation history.
type ToolResult struct {
	// CallID is the ID from the originating ToolCall.
	CallID string `json:"call_id"`

	// Output is the text result from the tool execution.
	Output string `json:"output"`

	// IsError is true if the tool execution returned an error.
	IsError bool `json:"is_error,omitempty"`
}

// ChannelMessage represents a message posted to an agent channel for
// inter-agent coordination. Channels support appagent and worker
// communication without going through the LLM provider loop.
type ChannelMessage struct {
	// From identifies the sender (e.g., "appagent", "worker-1", "runtime").
	From string `json:"from"`

	// Role classifies the message (e.g., "coordinator", "worker", "status").
	Role string `json:"role"`

	// Content is the message body.
	Content string `json:"content"`

	// Timestamp is when the message was posted.
	Timestamp time.Time `json:"timestamp"`
}

// WorkItem represents a tracked unit of work in the scheduler's work registry.
// Work items track spawned tasks with parent-child relationships, enabling the
// choir-in-choir pattern where appagents spawn worker agents and track their
// progress. The work registry provides persistent tracking across restarts
// (VAL-CHOIR-001, VAL-CHOIR-003).
type WorkItem struct {
	// ID is the stable unique identifier for this work item, matching the
	// task_id of the associated task.
	ID string `json:"id"`

	// ParentID references the parent work item (or task) that spawned this
	// item. Empty for root tasks. Enables parent-child relationships for
	// worker spawning (VAL-CHOIR-004).
	ParentID string `json:"parent_id,omitempty"`

	// OwnerID is the authenticated user who owns this work item.
	// Used for access scoping (VAL-CHOIR-002).
	OwnerID string `json:"owner_id"`

	// Objective is the goal or prompt for this work item.
	Objective string `json:"objective"`

	// State is the current lifecycle state of the work item, using the
	// same TaskState vocabulary as tasks (pending, running, completed, etc.).
	State TaskState `json:"state"`

	// Result holds the final output when the work item completes.
	// Empty until the work item reaches a terminal state with a result.
	Result string `json:"result,omitempty"`

	// Error holds an error message when the work item fails.
	// Empty unless the work item is in TaskFailed or TaskBlocked state.
	Error string `json:"error,omitempty"`

	// CreatedAt is the time the work item was created.
	CreatedAt time.Time `json:"created_at"`

	// UpdatedAt is the time the work item state was last changed.
	UpdatedAt time.Time `json:"updated_at"`
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
