# Runtime API, Status, Events, and Recovery

This document captures the runtime API implementation added by the `runtime-api-status-events-recovery` feature for the sandbox-runtime milestone.

## Package Layout

### `internal/runtime/` — Runtime Engine and API Handlers

- **Config** — Runtime configuration from env vars: `SANDBOX_ID`, `RUNTIME_STORE_PATH`, `RUNTIME_PROVIDER_TIMEOUT`, `RUNTIME_SUPERVISION_INTERVAL`
- **Runtime** — Core engine managing task lifecycle, event emission, health state
  - `SubmitTask(ctx, prompt, ownerID)` — Creates task, persists, starts goroutine execution
  - `GetTask(ctx, taskID, ownerID)` — Caller-scoped task lookup
  - `HealthState()` / `SetHealth()` — Runtime health with external visibility
  - `Start(ctx)` / `Stop()` — Lifecycle including supervisor and recovery
  - `recoverInterruptedTasks()` — Resolves non-terminal tasks on restart (VAL-RUNTIME-010)
  - `executeTask()` — Runs task through Provider interface in goroutine
- **Supervisor** — Periodic health check and recovery
  - Counts failed/blocked tasks to determine health state
  - Publishes health/degraded/recovery events on state transitions
  - `Start()` / `Stop()` — Idempotent lifecycle
- **Provider** — Interface for task execution
  - `Execute(ctx, task, emit)` — Runs task, calls emit for progress events
  - `StubProvider` — Simulates execution with configurable delay and failure
  - `EventEmitFunc` — Callback for incremental event emission
- **APIHandler** — HTTP handlers for the runtime API surface
  - `HandleTaskSubmission` — POST /api/agent/task (auth-gated, returns stable handle)
  - `HandleTaskStatus` — GET /api/agent/status?task_id=X (auth-gated, caller-scoped)
  - `HandleEvents` — GET /api/events (SSE, auth-gated, caller-scoped, incremental)
  - `HandleHealth` — GET /health (reports ready/degraded/failed)
  - `authenticateUser()` — Extracts X-Authenticated-User for defense-in-depth

### Store Addition

- `ListEventsByOwnerAfter(ctx, ownerID, afterSeq, limit)` — SSE catch-up query

### Proxy Updates

- Routes `/api/agent/task`, `/api/agent/status`, `/api/events` through `HandleProtectedAPI`
- `FlushInterval: -1` on reverse proxy for immediate SSE flushing

## API Contract

### POST /api/agent/task
- **Auth**: Required (X-Authenticated-User from proxy)
- **Body**: `{"prompt": "string", "metadata": {...}}`
- **Response**: 202 Accepted, `{"task_id": "uuid", "state": "pending", "owner_id": "...", "created_at": "..."}`
- **Errors**: 401 (no auth), 400 (empty prompt or invalid body)

### GET /api/agent/status?task_id=X
- **Auth**: Required, caller-scoped (owner must match)
- **Response**: 200 OK, full task record with state/result/error
- **Errors**: 401 (no auth), 404 (not found or not owner), 400 (missing task_id)

### GET /api/events
- **Auth**: Required, caller-scoped (filtered by owner)
- **Response**: SSE stream (Content-Type: text/event-stream)
- **Query**: `?after_seq=N` for catch-up
- **Format**: `data: {"event_id":"...","kind":"task.submitted",...}\n\n`
- **Errors**: 401 (no auth)

### GET /health
- **Response**: `{"status":"ready|degraded|failed","runtime_health":"ready|degraded|failed","sandbox_id":"...","running_tasks":N}`
- **HTTP Status**: 200 for ready/degraded, 503 for failed

## Key Design Decisions

1. **Provider is an interface** — StubProvider simulates execution; the real Bedrock/Z.AI bridge will implement the same interface in the next feature.

2. **Auth is defense-in-depth** — The proxy validates JWTs and injects X-Authenticated-User. The runtime handlers also verify this header exists, preventing direct sandbox access from bypassing auth.

3. **Caller scoping is enforced** — GetTask returns ErrNotFound for tasks not owned by the requesting user, preventing cross-user data leakage. Events are filtered by owner in the SSE stream.

4. **SSE is incremental** — Events arrive as they occur during task execution, not buffered until completion. The proxy uses FlushInterval=-1 for immediate flushing.

5. **Restart recovery is explicit** — On Start(), interrupted tasks (pending/running) are resolved to "failed" with error "runtime restarted, task interrupted". Blocked tasks are preserved as-is.

6. **Health state changes are evented** — Transitions between ready/degraded/failed publish RuntimeEvent on the event bus, making supervisor recovery externally visible.

7. **Supervisor is simple threshold-based** — 2+ problem tasks → degraded, 5+ → failed, 0-1 → ready. This will be refined in later milestones.

## What's Not Here Yet

- Real provider execution (Bedrock/Z.AI) → `runtime-bedrock-zai-provider-bridge`
- Shell prompt UI and browser-driven task interaction → `runtime-shell-prompt-ui-and-reattachment`
- Dolt-backed per-user workspaces → `etext-store-history-diff-blame-foundation`
- VM-backed runtime isolation → `vmctl-ownership-proxy-routing-and-isolation`
