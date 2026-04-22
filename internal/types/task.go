// Package types defines the core runtime types for the go-choir sandbox runtime.
//
// These types represent the foundational vocabulary for Mission 3: run handles,
// lifecycle states, event records, and the minimal type surface needed for stable
// run IDs, persisted state, and later API milestones.
//
// Design decisions:
//   - No adapter-wrapper or native-session model; the runtime loop runs as
//     direct goroutines, not subprocesses.
//   - RunState is a simpler lifecycle than Cogent's JobState because go-choir
//     runs combine session, job, and turn into a single execution handle.
//   - OwnerID links runs to the authenticated user who started them so that
//     status/event surfaces can scope by caller.
//   - Run IDs are UUID strings, generated once at submission and stable across
//     restart, supporting VAL-RUNTIME-003 and VAL-RUNTIME-010.
package types

import (
	"encoding/json"
	"time"
)

// RunState represents the lifecycle state of a runtime run.
type RunState string

const (
	// RunPending means the run was submitted but has not started executing.
	RunPending RunState = "pending"

	// RunRunning means the run is actively executing.
	RunRunning RunState = "running"

	// RunCompleted means the task finished successfully.
	RunCompleted RunState = "completed"

	// RunFailed means the run failed with a structured error outcome.
	// The runtime remains available for later runs (VAL-RUNTIME-008).
	RunFailed RunState = "failed"

	// RunCancelled means the run was cancelled before completion.
	RunCancelled RunState = "cancelled"

	// RunBlocked means the run is blocked (e.g., provider failure)
	// and may be retried or resolved later.
	RunBlocked RunState = "blocked"
)

// Terminal returns true if the state is a terminal state that will not
// transition further.
func (s RunState) Terminal() bool {
	switch s {
	case RunCompleted, RunFailed, RunCancelled:
		return true
	default:
		return false
	}
}

// Valid returns true if the RunState value is a recognized state.
func (s RunState) Valid() bool {
	switch s {
	case RunPending, RunRunning, RunCompleted, RunFailed, RunCancelled, RunBlocked:
		return true
	default:
		return false
	}
}

// RunRecord is the persisted representation of a submitted runtime run.
// It carries the stable run ID, owner identity, lifecycle state, and
// enough context for status lookup, event correlation, and restart recovery.
type RunRecord struct {
	// RunID is the stable unique identifier for this run, generated at
	// submission time and persisted for the lifetime of the record.
	// This is the handle used by status/event surfaces (VAL-RUNTIME-003).
	RunID string `json:"loop_id"`

	// AgentID is the durable agent identity that executed this run.
	// Multiple runs may belong to the same agent over time.
	AgentID string `json:"agent_id"`

	// ChannelID is the shared coordination channel for this run family.
	// Related workers and appagents can share a channel without sharing a run.
	ChannelID string `json:"channel_id,omitempty"`

	// ParentRunID links this run to the run that spawned it, if any.
	ParentRunID string `json:"parent_loop_id,omitempty"`

	// AgentProfile is the profile/tool policy used for this run.
	AgentProfile string `json:"agent_profile,omitempty"`

	// AgentRole is the current role label surfaced to tools and debugging UIs.
	AgentRole string `json:"agent_role,omitempty"`

	// OwnerID is the authenticated user who submitted the run.
	// Status and event surfaces scope by owner (VAL-RUNTIME-006).
	OwnerID string `json:"owner_id"`

	// SandboxID is the sandbox identity that accepted the run.
	SandboxID string `json:"sandbox_id"`

	// State is the current lifecycle state of the run.
	State RunState `json:"state"`

	// Prompt is the user-submitted input that initiated the run.
	Prompt string `json:"prompt"`

	// Result holds the final output text when the run completes.
	// Empty until the run reaches a terminal state with a result.
	Result string `json:"result,omitempty"`

	// Error holds a structured error message when the run fails or is blocked.
	// Empty unless the run is in RunFailed or RunBlocked state.
	Error string `json:"error,omitempty"`

	// CreatedAt is the time the run was submitted.
	CreatedAt time.Time `json:"created_at"`

	// UpdatedAt is the time the run state was last changed.
	UpdatedAt time.Time `json:"updated_at"`

	// FinishedAt is the time the run reached a terminal state, or nil.
	FinishedAt *time.Time `json:"finished_at,omitempty"`

	// Metadata holds extensible key-value data for the run.
	Metadata map[string]any `json:"metadata,omitempty"`
}

// EventKind represents the kind of a runtime event emitted during run execution.
type EventKind string

const (
	// EventRunSubmitted is emitted when a run is submitted and accepted.
	EventRunSubmitted EventKind = "loop.submitted"

	// EventRunStarted is emitted when a run begins executing.
	EventRunStarted EventKind = "loop.started"

	// EventRunProgress is emitted for incremental progress updates during
	// run execution.
	EventRunProgress EventKind = "loop.progress"

	// EventRunDelta is emitted for streaming text deltas from the provider
	// response, supporting incremental event streaming (VAL-RUNTIME-005).
	EventRunDelta EventKind = "loop.delta"

	// EventRunCompleted is emitted when a run finishes successfully.
	EventRunCompleted EventKind = "loop.completed"

	// EventRunFailed is emitted when a run fails with a structured error
	// outcome (VAL-RUNTIME-008).
	EventRunFailed EventKind = "loop.failed"

	// EventRunBlocked is emitted when a run is blocked (e.g., provider failure).
	EventRunBlocked EventKind = "loop.blocked"

	// EventRunCancelled is emitted when a run is cancelled.
	EventRunCancelled EventKind = "loop.cancelled"

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

	// EventVTextDocumentRevisionCreated is emitted when a canonical document
	// revision is created outside the appagent synthesis loop, such as a direct
	// user-authored save through the document API. The payload includes doc_id,
	// revision_id, and current_revision_id so the editor can follow head changes.
	EventVTextDocumentRevisionCreated EventKind = "vtext.document_revision.created"
)

// EventRecord represents a single runtime event emitted during run execution
// or runtime lifecycle changes. Events are ordered by sequence number within
// a run, persisted for restart recovery, and streamed through /api/events
// (VAL-RUNTIME-005).
type EventRecord struct {
	// EventID is the unique identifier for this event.
	EventID string `json:"event_id"`

	// Seq is the per-run sequence number, assigned monotonically on append.
	// Events for the same run can be fetched incrementally using after-seq
	// cursors.
	Seq int64 `json:"seq"`

	// StreamSeq is the owner/global monotonic sequence used for cross-loop
	// catch-up and streaming.
	StreamSeq int64 `json:"stream_seq,omitempty"`

	// Timestamp is when the event occurred.
	Timestamp time.Time `json:"ts"`

	// RunID is the run this event is correlated to. For runtime-level events
	// (health, degraded), this may be empty.
	RunID string `json:"loop_id,omitempty"`

	// AgentID is the durable agent identity that emitted or owns this event.
	AgentID string `json:"agent_id,omitempty"`

	// ChannelID is the shared coordination channel correlated to this event.
	ChannelID string `json:"channel_id,omitempty"`

	// OwnerID is the authenticated user who owns the run, used for
	// caller-scoped event streaming (VAL-RUNTIME-006).
	OwnerID string `json:"owner_id,omitempty"`

	// TrajectoryID ties the event to the broader user-visible workflow.
	TrajectoryID string `json:"trajectory_id,omitempty"`

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
	// ChannelID is the shared coordination channel that owns this message.
	ChannelID string `json:"channel_id,omitempty"`

	// Seq is the durable per-channel sequence number for incremental reads.
	Seq int64 `json:"seq,omitempty"`

	// From identifies the sender (e.g., "appagent", "worker-1", "runtime").
	From string `json:"from"`

	// FromAgentID identifies the durable agent that posted the message.
	FromAgentID string `json:"from_agent_id,omitempty"`

	// FromRunID identifies the run that posted the message.
	FromRunID string `json:"from_loop_id,omitempty"`

	// ToAgentID identifies the addressed recipient agent for directed delivery.
	// Empty means the message is broadcast on the channel.
	ToAgentID string `json:"to_agent_id,omitempty"`

	// ToRunID identifies the addressed recipient run for directed delivery when
	// a specific live execution is the target. Empty means no specific run is
	// required.
	ToRunID string `json:"to_loop_id,omitempty"`

	// TrajectoryID ties the message to the broader user-visible workflow.
	TrajectoryID string `json:"trajectory_id,omitempty"`

	// Role classifies the message (e.g., "coordinator", "worker", "status").
	Role string `json:"role"`

	// Content is the message body.
	Content string `json:"content"`

	// Timestamp is when the message was posted.
	Timestamp time.Time `json:"timestamp"`
}

// InboxDelivery is the runtime-owned delivery queue entry for a directed
// message. Unlike ChannelMessage, which is the audit log / trace surface, inbox
// deliveries are consumed by the runtime and threaded back into agent loops as
// user turns.
type InboxDelivery struct {
	DeliveryID        string     `json:"delivery_id"`
	OwnerID           string     `json:"owner_id"`
	ToAgentID         string     `json:"to_agent_id"`
	ToRunID           string     `json:"to_loop_id,omitempty"`
	FromAgentID       string     `json:"from_agent_id,omitempty"`
	FromRunID         string     `json:"from_loop_id,omitempty"`
	ChannelID         string     `json:"channel_id,omitempty"`
	Role              string     `json:"role,omitempty"`
	Content           string     `json:"content"`
	TrajectoryID      string     `json:"trajectory_id,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	DeliveredToLoopID string     `json:"delivered_to_loop_id,omitempty"`
	DeliveredAt       *time.Time `json:"delivered_at,omitempty"`
}

// AgentRecord is the durable runtime representation of an agent identity.
// Runs are ephemeral executions owned by an agent; channels are the shared
// coordination surface that can outlive any one run.
type AgentRecord struct {
	AgentID   string    `json:"agent_id"`
	OwnerID   string    `json:"owner_id"`
	SandboxID string    `json:"sandbox_id"`
	Profile   string    `json:"profile"`
	Role      string    `json:"role"`
	ChannelID string    `json:"channel_id"`
	CreatedAt time.Time `json:"created_at"`
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
