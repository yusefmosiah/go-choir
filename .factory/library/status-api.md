# Task Status API

## Overview

The task status API provides endpoints for checking the progress and results of runtime tasks. Two route patterns are supported:

1. **Query parameter**: `GET /api/agent/status?task_id={uuid}` — original route
2. **Path parameter**: `GET /api/agent/{id}/status` — RESTful route (VAL-CHOIR-002)

Both return the same `taskStatusResponse` JSON structure.

## Response Format

```json
{
  "task_id": "uuid",
  "owner_id": "user-alice",
  "sandbox_id": "sandbox-dev",
  "state": "completed",
  "prompt": "explain closures in Go",
  "result": "Closures are functions that capture...",
  "error": "",
  "created_at": "2026-04-12T07:00:00.000Z",
  "updated_at": "2026-04-12T07:00:01.000Z",
  "finished_at": "2026-04-12T07:00:01.000Z",
  "metadata": {}
}
```

## State Machine

```
pending → running → completed
                  → failed
                  → blocked
                  → cancelled
```

- `pending`: Task submitted, not yet executing
- `running`: Task actively executing (provider call in progress)
- `completed`: Task finished successfully, `result` field populated
- `failed`: Task failed, `error` field populated with reason
- `blocked`: Task blocked (e.g., provider failure), may be retried
- `cancelled`: Task cancelled (e.g., runtime shutdown)

## Error Responses

| Condition | HTTP Status | Error Message |
|-----------|-------------|---------------|
| Missing auth | 401 | "authentication required" |
| Wrong method | 405 | "method not allowed" |
| Missing task ID | 400 | "task_id query parameter is required" (query) / "task ID is required" (path) |
| Non-existent task | 404 | "task not found" |
| Task owned by different user | 404 | "task not found" (same as non-existent to prevent IDOR) |

## Access Control

- Tasks are scoped to the authenticated owner (`X-Authenticated-User` header)
- Requesting another user's task returns 404 (not 403) to prevent task ID probing
- Auth identity is injected by the proxy layer

## Implementation

- Handler: `APIHandler.HandleTaskStatusByID` in `internal/runtime/api.go`
- Runtime method: `Runtime.GetTask` in `internal/runtime/runtime.go`
- Store method: `Store.GetTask` in `internal/store/store.go`
- Route registration: `RegisterRoutes` in `internal/runtime/api.go`

## Tests

Tests are in `internal/runtime/api_test.go` with prefix `TestHandleTaskStatusByID`:
- `TestHandleTaskStatusByIDReturnsTaskRecord` — full response structure validation
- `TestHandleTaskStatusByIDCompletedResult` — completed task has result and finished_at
- `TestHandleTaskStatusByIDAuthGated` — 401 without auth
- `TestHandleTaskStatusByIDCallerScoped` — 404 for other users' tasks
- `TestHandleTaskStatusByIDNotFound` — 404 for non-existent tasks
- `TestHandleTaskStatusByIDFailedOutcome` — failed task has error and finished_at
- `TestHandleTaskStatusByIDMethodNotAllowed` — POST returns 405
- `TestHandleTaskStatusByIDSpawnedChildTask` — works for spawned child tasks
- `TestHandleTaskStatusByIDStateTransitions` — state changes reflected immediately
