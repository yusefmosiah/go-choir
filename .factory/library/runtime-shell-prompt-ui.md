# Runtime Shell Prompt UI

## Overview

The shell prompt UI is the first real runtime-facing browser surface in go-choir. It replaces the placeholder "Desktop" panel in the authenticated Shell with a TaskRunner component that submits prompts through the runtime API, renders task/status/event progress, and returns a real provider-backed answer.

## Components

### `frontend/src/lib/runtime.js` — Runtime API client

Provides the browser-side runtime API surface:
- `submitTask(prompt)` — POST /api/agent/task with renewal safety via `fetchWithRenewal`
- `fetchTaskStatus(taskId)` — GET /api/agent/status?task_id=...
- `connectEventStream(onEvent, afterSeq?)` — SSE connection to GET /api/events
- `reattachToActiveTask()` — checks for in-flight task on mount, resumes without resubmitting
- `setActiveTask()`/`getActiveTask()`/`clearActiveTask()` — sessionStorage-based task handle persistence

### `frontend/src/lib/TaskRunner.svelte` — Task UI component

The TaskRunner component manages:
- Prompt input with Enter-to-submit
- Task lifecycle: submit → pending → running → completed/failed
- SSE event stream for live progress
- Status polling (2-second interval) for terminal state detection
- Reattachment on mount via `reattachToActiveTask()`
- Prompt input disabled during in-flight task, re-enabled on completion

### Shell.svelte integration

The TaskRunner replaces the old `desktop-placeholder` panel. The Shell passes `authexpired` events from TaskRunner up to the root App component.

## Validation Assertions

This feature fulfills:
- **VAL-RUNTIME-007**: Browser prompt flow returns a real provider-backed answer
- **VAL-CROSS-109**: First useful runtime action completes end to end
- **VAL-CROSS-111**: Renewal and retries do not duplicate runtime task submission
- **VAL-CROSS-121**: Reload/new-tab during in-flight work reattaches without resubmitting

## Design Decisions

### Task handle in sessionStorage

The active task ID is stored in `sessionStorage` (key: `go-choir-active-task`) rather than `localStorage` because:
- The task handle is not a secret — it's a server-generated UUID already exposed through the API
- sessionStorage survives page reload within the same tab but not across browser restart
- Auth tokens are NEVER stored here; only the task handle for reattachment
- This is consistent with the "no auth tokens in browser storage" contract

### Deduplication guard in submitTask()

`submitTask()` checks for an existing in-flight task before submitting. If one exists, it returns the existing task info instead of creating a duplicate. This prevents duplicate submission during:
- Renewal retry (fetchWithRenewal retries with new cookies after 401)
- Double-click on the submit button (input is disabled, but the guard adds defense-in-depth)
- Race conditions between status polling and user actions

### SSE for live progress, polling for terminal state

The TaskRunner uses both SSE (EventSource) for live event streaming and periodic status polling:
- SSE provides real-time task events (submitted, started, progress, tool invocations)
- Status polling (every 2s) provides authoritative terminal state detection
- This dual approach ensures terminal states are always detected even if SSE events are missed

### EventSource for SSE

The runtime uses standard SSE (`text/event-stream`) which the browser handles natively via `EventSource`. This is simpler than WebSocket for the event stream use case because:
- SSE is unidirectional (server → client), which matches the event stream semantics
- EventSource auto-reconnects on connection loss
- The `after_seq` parameter supports catch-up after reconnection

## Data Attributes for Test Targeting

| Attribute | Element |
|-----------|---------|
| `data-task-runner` | TaskRunner root container |
| `data-prompt-input` | Prompt text input |
| `data-prompt-submit` | Submit button |
| `data-task-status` | Task status section |
| `data-task-id` | Task ID display |
| `data-task-state` | Task state badge |
| `data-task-result` | Result text container |
| `data-task-error` | Error message container |
| `data-task-events` | Event log container |
| `data-event-item` | Individual event entry |

## Future Work

- Desktop window manager will replace the current grid layout with real windows
- E-text app will add document-centric prompt surfaces
- Agent revision flows will extend the task UI with in-document progress
- Rate limiting feedback will surface throttling state in the UI
