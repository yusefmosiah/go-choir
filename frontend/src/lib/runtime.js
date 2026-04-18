/**
 * Runtime API client for the go-choir shell prompt UI.
 *
 * Communicates with the runtime through the same-origin proxy routes only:
 *   POST /api/agent/loop       — submit a prompt
 *   GET  /api/agent/status    — poll loop status
 *   GET  /api/events          — SSE event stream
 *
 * All requests use cookie-backed auth via fetchWithRenewal so that:
 *   - expired access tokens are silently renewed before retry
 *   - the shell never falls back to guest auth mid-submission
 *   - renewal/retry does not duplicate loop submission (VAL-CROSS-111)
 *
 * Reattachment across reload/new-tab (VAL-CROSS-121):
 *   - The active loop ID is stored in sessionStorage as 'go-choir-active-loop'
 *   - On mount, the UI checks for an in-flight loop and reattaches instead
 *     of resubmitting
 *   - The stored loop ID is cleared when the loop reaches a terminal state
 */

import { fetchWithRenewal, AuthRequiredError } from './auth.js';

// ---------------------------------------------------------------------------
// Loop submission
// ---------------------------------------------------------------------------

/**
 * Submits a prompt to the runtime API.
 *
 * Uses fetchWithRenewal so that if the access JWT is expired, the request
 * is retried after silent renewal rather than failing outright. The loop is
 * only submitted once because the server generates the loop handle at acceptance
 * time — a renewed retry of the same POST creates a second loop only if the
 * first request never reached the server. The client-side guard below
 * prevents this edge case.
 *
 * @param {string} prompt - The user prompt text.
 * @returns {Promise<{loop_id: string, state: string, owner_id: string, created_at: string}>}
 * @throws {AuthRequiredError} If auth renewal fails.
 * @throws {Error} If the server rejects the request.
 */
export async function submitLoop(prompt) {
  // Guard: refuse to submit if there is already an in-flight active loop.
  // This prevents duplicate submission during renewal retry (VAL-CROSS-111).
  const activeLoop = getActiveLoop();
  if (activeLoop && !isTerminalState(activeLoop.state)) {
    // There is already an in-flight loop — return its info instead of
    // submitting a new one.
    return activeLoop;
  }

  const res = await fetchWithRenewal('/api/agent/loop', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ prompt }),
  });

  if (!res.ok) {
    const err = await res.json().catch(() => ({}));
    throw new Error(err.error || `Loop submission failed (${res.status})`);
  }

  const loopInfo = await res.json();

  // Store the active loop so we can reattach after reload (VAL-CROSS-121).
  setActiveLoop({
    loop_id: loopInfo.loop_id,
    state: loopInfo.state,
    owner_id: loopInfo.owner_id,
    created_at: loopInfo.created_at,
    prompt,
  });

  return loopInfo;
}

// ---------------------------------------------------------------------------
// Loop status
// ---------------------------------------------------------------------------

/**
 * Fetches the current status of a loop.
 *
 * @param {string} loopId - The stable loop handle.
 * @returns {Promise<object>} The loop status record.
 * @throws {AuthRequiredError} If auth renewal fails.
 * @throws {Error} If the loop is not found.
 */
export async function fetchLoopStatus(loopId) {
  const res = await fetchWithRenewal(`/api/agent/status?loop_id=${encodeURIComponent(loopId)}`, {
    method: 'GET',
  });

  if (!res.ok) {
    const err = await res.json().catch(() => ({}));
    throw new Error(err.error || `Loop status fetch failed (${res.status})`);
  }

  const status = await res.json();

  // Update the stored active loop state so reattachment picks up the latest.
  const active = getActiveLoop();
  if (active && active.loop_id === loopId) {
    setActiveLoop({ ...active, state: status.state, result: status.result, error: status.error });
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
// Active loop persistence (reattachment, VAL-CROSS-121)
// ---------------------------------------------------------------------------

const ACTIVE_LOOP_KEY = 'go-choir-active-loop';

/**
 * Stores the active loop info in sessionStorage so it survives page reload
 * but not closing the browser tab entirely. This supports reattachment
 * across reload and new-tab within the same session (VAL-CROSS-121).
 *
 * Note: sessionStorage is used here because the loop ID is not a secret —
 * it is a server-generated UUID that is already exposed through the API.
 * Auth tokens are never stored here; only the loop handle for reattachment.
 */
export function setActiveLoop(loopInfo) {
  try {
    sessionStorage.setItem(ACTIVE_LOOP_KEY, JSON.stringify(loopInfo));
  } catch (_err) {
    // sessionStorage may be unavailable in some contexts.
  }
}

/**
 * Returns the stored active loop info, or null if none.
 */
export function getActiveLoop() {
  try {
    const raw = sessionStorage.getItem(ACTIVE_LOOP_KEY);
    if (!raw) return null;
    return JSON.parse(raw);
  } catch (_err) {
    return null;
  }
}

/**
 * Clears the stored active loop info.
 */
export function clearActiveLoop() {
  try {
    sessionStorage.removeItem(ACTIVE_LOOP_KEY);
  } catch (_err) {
    // Ignore.
  }
}

/**
 * Returns true if the given loop state is terminal (completed, failed, cancelled).
 */
export function isTerminalState(state) {
  return state === 'completed' || state === 'failed' || state === 'cancelled';
}

/**
 * Attempts to reattach to an in-flight loop.
 *
 * On mount (or reload), the UI should call this to check whether there is
 * an active loop that is still in-flight. If so, it returns the loop status
 * so the UI can resume showing progress. If the loop has already completed,
 * it returns the final status. If there is no active loop, returns null.
 *
 * @returns {Promise<object|null>} The loop status, or null if no active loop.
 */
export async function reattachToActiveLoop() {
  const active = getActiveLoop();
  if (!active || !active.loop_id) {
    return null;
  }

  try {
    const status = await fetchLoopStatus(active.loop_id);
    if (isTerminalState(status.state)) {
      // Loop already finished — clear the stored handle.
      clearActiveLoop();
    } else {
      // Loop is still in-flight — update the stored state.
      setActiveLoop({ ...active, state: status.state });
    }
    return status;
  } catch (_err) {
    // Status fetch failed — the loop may have been cleaned up or auth
    // expired. Clear the stored handle so the UI doesn't get stuck.
    clearActiveLoop();
    return null;
  }
}
