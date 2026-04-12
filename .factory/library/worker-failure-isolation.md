# Worker Failure Isolation

**Feature:** scheduler-worker-failure-isolation  
**Assertions:** VAL-CHOIR-009 (failure isolation), VAL-CHOIR-010 (task cancellation), VAL-CHOIR-014 (recovery after restart)  
**Date:** 2026-04-12

## Summary

Worker failures are fully isolated — a failing child worker does not crash the parent task, other sibling workers, or the runtime itself. Parents receive error notifications via channels and can continue spawning new tasks. Tasks can also be explicitly cancelled.

## Key Implementation Details

### CancelTask Method (runtime.go)

```go
func (rt *Runtime) CancelTask(ctx context.Context, taskID, ownerID string) error
```

- Validates task exists and belongs to owner
- Only running or pending tasks can be cancelled
- Cancels the task's execution context via `context.CancelFunc`
- Transitions task to `TaskCancelled` state
- Emits `EventTaskCancelled` event
- Updates work item state if spawned child

### POST /api/agent/cancel Endpoint

```json
// Request
{"task_id": "uuid"}

// Response
{"task_id": "uuid", "state": "cancelled"}
```

- Owner-scoped: returns 404 for tasks owned by other users
- Returns 409 if task is already in terminal state
- Returns 404 for non-existent tasks

### Context Persistence Fix

`handleExecutionError` now uses `context.Background()` for all persistence operations (store updates, event emission, channel notifications) instead of the task's potentially-cancelled context. This ensures:

- Cancelled task state is properly persisted
- task.failed/task.cancelled events are emitted
- Parent channel receives error notifications
- Work items are updated

### Failure Notification Flow

1. Child task fails → `handleExecutionError` called
2. Task transitions to `TaskFailed` state
3. Error message stored in task record and work item
4. `EventTaskFailed` event emitted with error payload
5. Error message posted to parent's channel via `PostChildError`
6. Parent can read error via `ChannelRead` or `WaitForChildResult`

### Recovery After Restart

- Tasks in `running` or `pending` state when the runtime stops are recovered by `recoverInterruptedTasks` on restart
- Recovered tasks transition to `TaskFailed` with error "runtime restarted, task interrupted"
- Recovery events (`EventTaskFailed`) are emitted
- Runtime remains operational after recovery

## Test Coverage

24 tests in `internal/runtime/failure_isolation_test.go`:

**VAL-CHOIR-009 (Failure Isolation):**
- FailedWorkerSendsErrorToParent
- ParentContinuesRunning
- ErrorIncludesTaskIDAndMessage
- ParentCanSpawnReplacementWorker
- SiblingWorkersUnaffected
- RuntimeHealthRemainsReady
- TaskFailedEventEmitted
- WorkItemUpdatedOnFailure
- APIStatusReturnsFailedState
- HealthEndpointRemainsHealthy
- MultipleFailuresDontCrashRuntime
- ConcurrentFailuresAndSuccesses
- ParentResponsiveAfterFailure

**VAL-CHOIR-010 (Task Cancellation):**
- CancelRunningTask
- CancelledEventEmitted
- CancelledTaskNoResult
- CancelNonExistentTask
- CancelOtherUsersTask
- SiblingUnaffectedByCancel
- CancelViaAPI

**VAL-CHOIR-014 (Recovery After Restart):**
- InterruptedTasksMarkedFailedOnRestart
- RecoveredTasksEmitFailedEvents
- RuntimeAcceptsNewTasksAfterRecovery
