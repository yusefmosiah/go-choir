# Parent-Child Channel Communication

**Feature:** runtime-parent-child-channels (Milestone: choir-in-choir)
**Date:** 2026-04-12

## Overview

Channel-based communication between parent and child tasks, enabling workers to report results, progress, and errors back to their parent through the ChannelManager. This is the foundation for the choir-in-choir pattern where appagents spawn worker agents.

## Architecture

Channels are keyed by task/work ID. The parent task has a channel keyed by its task ID, and each child has its own channel keyed by its task ID. The primary communication pattern is:

1. **Parent spawns child** → Channels auto-created for both parent and child
2. **Child posts result** → Message posted to parent's channel (keyed by parentID)
3. **Parent reads/waits** → Reads messages from its own channel

## Key Methods

### Runtime Convenience Methods

- `rt.PostChildResult(ctx, parentID, childID, result)` — Post result from child to parent
- `rt.PostChildError(ctx, parentID, childID, errMsg)` — Post error from child to parent
- `rt.PostChildProgress(ctx, parentID, childID, progress)` — Post progress update from child to parent
- `rt.WaitForChildResult(ctx, parentID, childID, role)` — Wait for specific child+role message
- `rt.ChannelPost(ctx, workID, from, role, content)` — Low-level channel post
- `rt.ChannelRead(workID, cursor)` — Read messages since cursor
- `rt.ChannelWait(ctx, workID, cursor)` — Block until new messages arrive

### ChannelManager Methods

- `mgr.Channel(workID)` — Get or create channel for work ID
- `mgr.ensureParentChildChannels(parentID, childID)` — Create both channels at once
- `mgr.Close(workID)` — Close and remove channel
- `mgr.ListChannels()` — List all active channel IDs
- `mgr.PostToChannel(workID, message, emit)` — Post with event emission

## Message Format

```go
type ChannelMessage struct {
    From      string    // Sender: "worker-1", "child-task-uuid", "appagent"
    Role      string    // Type: "result", "error", "status", "coordinator"
    Content   string    // Payload: result text, error message, progress info
    Timestamp time.Time // Auto-set on post
}
```

The message format satisfies: `{from, to (via channel key), type (role), payload (content)}`.

## Channel Scoping (VAL-CHOIR-015)

Channels are **isolated per work/task ID**:
- Channel for work ID A does not receive messages from work ID B
- Posting to channel A does not wake waiters on channel B
- Channel closure affects only that channel's waiters
- Other parents never receive messages from unrelated children

## Event Emission

Channel messages emit `channel.message` events through the runtime event bus:
- `EventKind`: `types.EventChannelMessage`
- `Actor`: `events.ActorChannel`
- `Cause`: `events.CauseChannelMessage`
- Payload includes: `work_id`, `from`, `role`, `content_len`

These events are persisted to the store and streamed via `/api/events`.

## Auto-Channel Creation on Spawn

When `SpawnTask()` is called, the runtime automatically ensures channels exist for both the parent and child via `ensureParentChildChannels()`. This means:

1. No explicit channel setup needed before communication
2. Channels are lazily created on first access even if auto-creation fails
3. Both parent and child channels are available immediately after spawn

## Key Files

| File | Purpose |
|------|---------|
| `internal/runtime/channels.go` | AgentChannel, ChannelManager, convenience methods |
| `internal/runtime/runtime.go` | SpawnTask auto-channel, ChannelPost/Read/Wait |
| `internal/runtime/parent_child_channel_test.go` | 22 tests for parent-child channel behavior |
| `internal/types/task.go` | ChannelMessage type, EventChannelMessage kind |

## Test Coverage

22 tests in `internal/runtime/parent_child_channel_test.go`:
- Core: child sends → parent receives (child-to-parent messaging)
- Results: child result delivery on completion
- Scoping: channels isolated per relationship, cross-channel isolation
- Waiting: blocking wait for messages, async result delivery
- Multiple children: same parent receives from all children
- Closure isolation: closing one channel doesn't affect others
- Event emission: channel.message events on the bus
- Spawn integration: channels auto-created on spawn
- Error notification: error messages from child to parent
- Incremental read: cursor-based incremental message reading
- Message format: from/to/type/payload fields verified
- Concurrent access: 10 concurrent children posting safely
- Convenience methods: PostChildResult, PostChildError, PostChildProgress
- Filtered wait: WaitForChildResult filters by child+role
- Async filtered wait: WaitForChildResult with async message arrival

## Downstream Features

This is a foundation for:
- `etext-research-button` — UI triggers worker spawn, results via channel
- `scheduler-concurrent-workers` — Multiple children post to same parent
- `scheduler-worker-failure-isolation` — Error notifications via channel
