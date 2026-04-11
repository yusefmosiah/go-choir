/**
 * Runtime API client for the go-choir shell prompt UI.
 *
 * Communicates with the runtime through the same-origin proxy routes only:
 *   POST /api/agent/task      — submit a prompt
 *   GET  /api/agent/status    — poll task status
 *   GET  /api/events          — SSE event stream
 *
 * All requests use cookie-backed auth via fetchWithRenewal so that:
 *   - expired access tokens are silently renewed before retry
 *   - the shell never falls back to guest auth mid-submission
 *   - renewal/retry does not duplicate task submission (VAL-CROSS-111)
 *
 * Reattachment across reload/new-tab (VAL-CROSS-121):
 *   - The active task ID is stored in sessionStorage as 'go-choir-active-task'
 *   - On mount, the UI checks for an in-flight task and reattaches instead
 *     of resubmitting
 *   - The stored task ID is cleared when the task reaches a terminal state
 */

import { fetchWithRenewal, AuthRequiredError } from './auth.js';

// ---------------------------------------------------------------------------
// Task submission
// ---------------------------------------------------------------------------

/**
 * Submits a prompt to the runtime API.
 *
 * Uses fetchWithRenewal so that if the access JWT is expired, the request
 * is retried after silent renewal rather than failing outright. The task is
 * only submitted once because the server generates the task ID at acceptance
 * time — a renewed retry of the same POST creates a second task only if the
 * first request never reached the server. The client-side guard below
 * prevents this edge case.
 *
 * @param {string} prompt - The user prompt text.
 * @returns {Promise<{task_id: string, state: string, owner_id: string, created_at: string}>}
 * @throws {AuthRequiredError} If auth renewal fails.
 * @throws {Error} If the server rejects the request.
 */
export async function submitTask(prompt) {
  // Guard: refuse to submit if there is already an in-flight active task.
  // This prevents duplicate submission during renewal retry (VAL-CROSS-111).
  const activeTask = getActiveTask();
  if (activeTask && !isTerminalState(activeTask.state)) {
    // There is already an in-flight task — return its info instead of
    // submitting a new one.
    return activeTask;
  }

  const res = await fetchWithRenewal('/api/agent/task', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ prompt }),
  });

  if (!res.ok) {
    const err = await res.json().catch(() => ({}));
    throw new Error(err.error || `Task submission failed (${res.status})`);
  }

  const taskInfo = await res.json();

  // Store the active task so we can reattach after reload (VAL-CROSS-121).
  setActiveTask({
    task_id: taskInfo.task_id,
    state: taskInfo.state,
    owner_id: taskInfo.owner_id,
    created_at: taskInfo.created_at,
    prompt,
  });

  return taskInfo;
}

// ---------------------------------------------------------------------------
// Task status
// ---------------------------------------------------------------------------

/**
 * Fetches the current status of a task.
 *
 * @param {string} taskId - The stable task handle.
 * @returns {Promise<object>} The task status record.
 * @throws {AuthRequiredError} If auth renewal fails.
 * @throws {Error} If the task is not found.
 */
export async function fetchTaskStatus(taskId) {
  const res = await fetchWithRenewal(`/api/agent/status?task_id=${encodeURIComponent(taskId)}`, {
    method: 'GET',
  });

  if (!res.ok) {
    const err = await res.json().catch(() => ({}));
    throw new Error(err.error || `Status fetch failed (${res.status})`);
  }

  const status = await res.json();

  // Update the stored active task state so reattachment picks up the latest.
  const active = getActiveTask();
  if (active && active.task_id === taskId) {
    setActiveTask({ ...active, state: status.state, result: status.result, error: status.error });
  }

  return status;
}

// ---------------------------------------------------------------------------
// Event stream (SSE)
// ---------------------------------------------------------------------------

/**
 * Connects to the runtime event stream and calls the callback for each event.
 *
 * The SSE connection uses the same cookie-backed auth. If the connection
 * drops, the caller should attempt reconnection with an after_seq cursor
 * so missed events are replayed.
 *
 * @param {function(object): void} onEvent - Called for each SSE event.
 * @param {object} [options]
 * @param {number} [options.afterSeq] - Replay events after this sequence number.
 * @returns {{ close: () => void }} A handle to close the stream.
 */
export function connectEventStream(onEvent, options = {}) {
  const { afterSeq } = options;
  let url = '/api/events';
  if (afterSeq && afterSeq > 0) {
    url += `?after_seq=${afterSeq}`;
  }

  const eventSource = new EventSource(url);

  eventSource.onmessage = (event) => {
    try {
      const data = JSON.parse(event.data);
      onEvent(data);
    } catch (_err) {
      // Malformed SSE data — skip.
    }
  };

  eventSource.onerror = () => {
    // The browser will auto-reconnect EventSource. The caller can
    // also destroy this handle and create a new one with after_seq.
  };

  return {
    close() {
      eventSource.close();
    },
  };
}

// ---------------------------------------------------------------------------
// Active task persistence (reattachment, VAL-CROSS-121)
// ---------------------------------------------------------------------------

const ACTIVE_TASK_KEY = 'go-choir-active-task';

/**
 * Stores the active task info in sessionStorage so it survives page reload
 * but not closing the browser tab entirely. This supports reattachment
 * across reload and new-tab within the same session (VAL-CROSS-121).
 *
 * Note: sessionStorage is used here because the task ID is not a secret —
 * it is a server-generated UUID that is already exposed through the API.
 * Auth tokens are never stored here; only the task handle for reattachment.
 */
export function setActiveTask(taskInfo) {
  try {
    sessionStorage.setItem(ACTIVE_TASK_KEY, JSON.stringify(taskInfo));
  } catch (_err) {
    // sessionStorage may be unavailable in some contexts.
  }
}

/**
 * Returns the stored active task info, or null if none.
 */
export function getActiveTask() {
  try {
    const raw = sessionStorage.getItem(ACTIVE_TASK_KEY);
    if (!raw) return null;
    return JSON.parse(raw);
  } catch (_err) {
    return null;
  }
}

/**
 * Clears the stored active task info.
 */
export function clearActiveTask() {
  try {
    sessionStorage.removeItem(ACTIVE_TASK_KEY);
  } catch (_err) {
    // Ignore.
  }
}

/**
 * Returns true if the given task state is terminal (completed, failed, cancelled).
 */
export function isTerminalState(state) {
  return state === 'completed' || state === 'failed' || state === 'cancelled';
}

/**
 * Attempts to reattach to an in-flight task.
 *
 * On mount (or reload), the UI should call this to check whether there is
 * an active task that is still in-flight. If so, it returns the task status
 * so the UI can resume showing progress. If the task has already completed,
 * it returns the final status. If there is no active task, returns null.
 *
 * @returns {Promise<object|null>} The task status, or null if no active task.
 */
export async function reattachToActiveTask() {
  const active = getActiveTask();
  if (!active || !active.task_id) {
    return null;
  }

  try {
    const status = await fetchTaskStatus(active.task_id);
    if (isTerminalState(status.state)) {
      // Task already finished — clear the stored handle.
      clearActiveTask();
    } else {
      // Task is still in-flight — update the stored state.
      setActiveTask({ ...active, state: status.state });
    }
    return status;
  } catch (_err) {
    // Status fetch failed — the task may have been cleaned up or auth
    // expired. Clear the stored handle so the UI doesn't get stuck.
    clearActiveTask();
    return null;
  }
}
