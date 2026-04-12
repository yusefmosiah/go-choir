# Work Registry

**Feature:** scheduler-work-registry (Milestone: choir-in-choir)
**Date:** 2026-04-12

## Overview

The work registry is a minimal SQLite-backed tracking system for spawned tasks with parent-child relationships. It enables the "choir-in-choir" pattern where appagents spawn worker agents and track their progress.

## Schema

The `work_items` table in the runtime SQLite database:

```sql
CREATE TABLE work_items (
    id         TEXT PRIMARY KEY,      -- Stable UUID matching task_id
    parent_id  TEXT NOT NULL DEFAULT '', -- Parent work item (empty for root)
    owner_id   TEXT NOT NULL,         -- Authenticated user who owns the item
    objective  TEXT NOT NULL DEFAULT '', -- Goal or prompt for the work item
    state      TEXT NOT NULL DEFAULT 'pending', -- TaskState: pending/running/completed/failed/cancelled/blocked
    result     TEXT NOT NULL DEFAULT '', -- Output when completed
    error      TEXT NOT NULL DEFAULT '', -- Error message when failed
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL
);

CREATE INDEX idx_work_items_owner_id  ON work_items(owner_id);
CREATE INDEX idx_work_items_parent_id ON work_items(parent_id);
CREATE INDEX idx_work_items_state     ON work_items(state);
```

## Type

`types.WorkItem` in `internal/types/task.go`:
- `ID` - Stable unique identifier
- `ParentID` - Parent work item reference (empty for root)
- `OwnerID` - Owner for access scoping
- `Objective` - Goal/prompt text
- `State` - Uses `TaskState` vocabulary (pending, running, completed, failed, etc.)
- `Result` - Output text on completion
- `Error` - Error message on failure
- `CreatedAt`, `UpdatedAt` - Timestamps

## Store Methods

All methods are on `*store.Store` in `internal/store/store.go`:

- `CreateWorkItem(ctx, WorkItem) error` - Insert new work item
- `GetWorkItem(ctx, id) (WorkItem, error)` - Get by ID (returns `ErrNotFound`)
- `UpdateWorkItem(ctx, WorkItem) error` - Update state/result/error (returns `ErrNotFound`)
- `ListWorkItemsByOwner(ctx, ownerID, limit) ([]WorkItem, error)` - Owner's items, newest first
- `ListWorkItemsByParent(ctx, parentID, limit) ([]WorkItem, error)` - Children of a parent
- `ListWorkItemsByState(ctx, state, limit) ([]WorkItem, error)` - Items in given state

## Key Patterns

1. **State transitions**: pending → running → completed/failed (same as TaskState)
2. **Parent-child**: ParentID links child workers to their parent task
3. **Owner scoping**: All queries scope by owner for security
4. **Persistence**: Items survive store close/reopen (SQLite WAL mode)
5. **Ordering**: Lists ordered by created_at DESC (newest first)

## Downstream Features

The work registry is a foundation for:
- `scheduler-spawn-api` - POST /api/agent/spawn creates child work items
- `scheduler-status-api` - GET /api/agent/:id/status reads work items
- `runtime-parent-child-channels` - Workers report results via channels
- `etext-research-button` - UI spawns workers via spawn API

## Test Coverage

17 store tests in `internal/store/work_registry_test.go` + 3 type tests:
- CRUD operations (create, get, update)
- Not found errors (get, update)
- Parent-child relationships (create with parent, list by parent)
- State transitions (pending → running → completed, pending → running → failed)
- Owner scoping (list by owner, empty owner)
- Ordering (newest first)
- Limit support
- Persistence across store reopen
- Concurrent parent-child pattern simulation
- Table schema verification (all fields round-trip)
