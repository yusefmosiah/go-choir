# Spawn API (POST /api/agent/spawn)

## Overview

The spawn API enables creating child tasks linked to parent tasks, supporting the choir-in-choir pattern where appagents spawn worker agents and track their progress.

## Endpoint

`POST /api/agent/spawn`

### Request

```json
{
  "parent_id": "uuid-of-parent-task",
  "objective": "research topic X",
  "constraints": {
    "max_tokens": 500,
    "timeout_seconds": 30
  }
}
```

### Response (202 Accepted)

```json
{
  "task_id": "uuid-of-child-task",
  "parent_id": "uuid-of-parent-task",
  "state": "pending",
  "owner_id": "user-id-from-auth",
  "created_at": "2026-04-12T11:15:58.072Z"
}
```

## Implementation Details

### Files
- `internal/runtime/api.go` — HandleSpawn handler, spawnRequest/spawnResponse types
- `internal/runtime/runtime.go` — SpawnTask method on Runtime
- `internal/runtime/api_spawn_test.go` — 16 test cases covering all scenarios

### What happens on spawn

1. **Validate parent exists** — checks the `tasks` table for the parent_id
2. **Create TaskRecord** — standard runtime task with pending state
3. **Create WorkItem** — in the `work_items` table with parent_id linkage
4. **Store metadata** — parent_id, spawned_by, and any constraints in task metadata
5. **Emit event** — task.submitted event with parent_id in payload
6. **Execute async** — goroutine runs through executeTask (same as SubmitTask)

### Owner inheritance

The child task inherits owner from the authenticated user context (X-Authenticated-User header), NOT from the parent task. This means user-bob can spawn a child of user-alice's task, and the child will be owned by bob.

### Work registry vs Task table

Spawned children create records in BOTH:
- **tasks table** — for runtime execution tracking (lifecycle, result, events)
- **work_items table** — for scheduler/registry tracking (parent-child relationships)

Both share the same ID, owner, and initial state. The work_item stays at `pending` and is not automatically updated during execution (the task record tracks execution state).

### Error responses

| Code | Condition |
|------|-----------|
| 400 | Missing parent_id or objective |
| 401 | Missing authentication |
| 404 | Parent task not found |
| 405 | Non-POST method |

## Testing

```bash
go test ./internal/runtime/... -run TestSpawn -v
```

## Related Features

- `scheduler-status-api` — GET /api/agent/status for checking child task progress
- `runtime-parent-child-channels` — Channel-based communication between parent and child
- `scheduler-work-registry` — Work items table and parent-child tracking (completed)
