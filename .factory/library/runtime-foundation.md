# Runtime Types, Events, and Store Foundation

This document captures the foundational packages added by the `runtime-types-store-foundation` feature for the sandbox-runtime milestone.

## Package Layout

### `internal/types` — Core Runtime Types

Defines the foundational vocabulary for the Mission 3 runtime:

- **TaskState** — Lifecycle states: `pending`, `running`, `completed`, `failed`, `cancelled`, `blocked`
  - `Terminal()` returns true for completed/failed/cancelled
  - `Valid()` returns true for all recognized states
  - `blocked` is NOT terminal (may be retried)

- **TaskRecord** — Persisted task representation
  - `TaskID` — Stable UUID, generated at submission, survives restart
  - `OwnerID` — Authenticated user who submitted the task (for caller scoping)
  - `SandboxID` — Which sandbox accepted the task
  - `State`, `Prompt`, `Result`, `Error` — Lifecycle data
  - `FinishedAt` — Set when task reaches terminal state
  - `Metadata` — Extensible JSON map

- **EventKind** — Runtime event vocabulary: `task.submitted`, `task.started`, `task.progress`, `task.delta`, `task.completed`, `task.failed`, `task.blocked`, `task.cancelled`, `runtime.health`, `runtime.degraded`

- **EventRecord** — Persisted event with per-task sequence numbers

- **RuntimeHealthState** — `ready`, `degraded`, `failed`

### `internal/events` — Event Bus and Vocabulary

In-process pub/sub adapted from Cogent's pattern:

- **EventActor** — `runtime`, `supervisor`, `provider`, `host`
- **EventCause** — `task_lifecycle`, `provider_progress`, `provider_failure`, `supervisor_recovery`, `host_action`
- **RuntimeEvent** — Wraps `types.EventRecord` with actor/cause context
- **EventBus** — Thread-safe pub/sub with buffered channels and drop counting
- `RequiresSupervisorAttention()` — Filters events that should wake the supervisor
- `IsTerminal()` — Checks if an event kind is terminal

### `internal/store` — Durable Runtime Storage

SQLite-backed persistence for task and event records:

- **Task CRUD**: `CreateTask`, `GetTask`, `UpdateTask`
- **Task queries**: `ListTasks`, `ListTasksByOwner`, `ListTasksByState`
- **Event append**: `AppendEvent` (auto-assigns per-task sequence numbers)
- **Event queries**: `ListEvents`, `ListEventsAfter`, `ListEventsByOwner`
- `ErrNotFound` — Sentinel for missing records
- WAL mode for concurrent reads, single connection for writes
- Restart recovery: close and reopen preserves all task/event state

## Key Design Decisions

1. **No adapter-wrapper or native-session model** — The runtime loop runs as direct goroutines, not subprocesses or adapter processes. Cogent's `NativeSessionRecord`, `TransferPacket`, and adapter concepts are not ported.

2. **Task combines session+job+turn** — Cogent separates Sessions, Jobs, and Turns. Go-choir's TaskRecord is simpler: one task = one unit of work from submission to completion.

3. **SQLite first, Dolt later** — The store uses SQLite (already in go.mod) for the host-process milestone. The schema is designed to migrate to per-user Dolt-backed workspaces in the e-text milestone.

4. **Owner-based scoping** — `OwnerID` on TaskRecord and EventRecord enables caller-scoped status/event surfaces needed by VAL-RUNTIME-006.

5. **Blocked is non-terminal** — `TaskBlocked` is intentionally non-terminal because blocked tasks may be retried or resolved when the provider recovers.

6. **Event sequence per task** — Event records have per-task sequence numbers enabling incremental cursors (`ListEventsAfter`) for the `/api/events` streaming surface.

## What's Not Here Yet

The following are intentionally out of scope for this foundation feature and belong to later features:

- HTTP API handlers (`/api/agent/task`, `/api/agent/status`, `/api/events`) → `runtime-api-status-events-recovery`
- Runtime loop and goroutine supervision → `runtime-api-status-events-recovery`
- Provider bridge (Bedrock/Z.AI) → `runtime-bedrock-zai-provider-bridge`
- Dolt-backed per-user workspaces → `etext-store-history-diff-blame-foundation`
- `internal/runtime/` remains empty → will be populated by the API feature
