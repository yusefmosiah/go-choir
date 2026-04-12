# Concurrent Workers (VAL-CHOIR-008)

## Overview

The runtime supports multiple concurrent workers spawned from the same parent. Workers run as goroutines, each with independent execution context and channels to the parent. This enables the choir-in-choir pattern where a parent task can spawn multiple child workers and collect their results independently.

## Key Implementation Details

### Auto-notification on Completion/Failure

When a spawned child task completes (or fails), the runtime automatically:

1. **Updates the work item state** in the work registry (`updateWorkItemState`)
2. **Posts a result/error to the parent's channel** (`notifyParent`)

This happens in:
- `executeWithProvider` - legacy provider path, after completion
- `executeWithToolLoop` - tool-calling loop path, after completion
- `handleExecutionError` - on task failure/cancellation

### Work Item State Sync

The `updateWorkItemState(ctx, taskID, state, result, errMsg)` method is called at each state transition:
- Running: when `executeTask` transitions the task to running
- Completed: when the task finishes successfully (with result)
- Failed: when the task encounters an error (with error message)

If the task has no work item (not a spawned child), the method silently returns.

### Channel-based Parent Notification

The `notifyParent(ctx, rec)` method checks if the task has a `parent_id` in its metadata:
- On completion: posts `role="result"` message to parent's channel
- On failure: posts `role="error"` message to parent's channel

### Concurrency Model

- Each spawned child runs as an independent goroutine (`go rt.executeTask(...)`)
- Tasks are tracked in `rt.running` map with cancel functions
- No serialization or queue — all workers run concurrently
- Channel messages are thread-safe via mutex-protected `AgentChannel`
- The store uses SQLite with WAL mode and `SetMaxOpenConns(1)` for safe concurrent writes

## Test Coverage

Tests in `concurrent_workers_test.go` verify:
- Spawning 3+ workers in sequence without waiting
- All workers running simultaneously (not sequentially)
- Each worker completes independently with correct results
- Independent channels per child
- No interference between sibling workers
- Results auto-posted to parent channel on completion
- Errors auto-posted to parent channel on failure
- Work items updated on completion
- Stress test: 10 concurrent spawns
- Timing test: 3 × 200ms tasks complete in < 500ms
- Mixed pass/fail workers

## Files Modified

- `internal/runtime/runtime.go` - Added `updateWorkItemState`, `notifyParent` methods; called from `executeTask`, `executeWithProvider`, `executeWithToolLoop`, `handleExecutionError`
- `internal/runtime/parent_child_channel_test.go` - Updated tests to account for auto-notification
- `internal/runtime/concurrent_workers_test.go` - New test file with 16 test cases

## Key Gotchas

- Auto-notification means existing tests that manually post results to parent channels need to account for the additional auto-posted messages
- The `notifyParent` uses `context.Background()` in error paths since the task's context may be canceled
- Work item state updates are best-effort (errors are logged but don't fail the task)
