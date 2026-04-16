/**
 * Runtime API client for the go-choir shell prompt UI.
 *
 * Communicates with the runtime through the same-origin proxy routes only:
 *   POST /api/agent/run       — submit a prompt
 *   GET  /api/agent/status    — poll run status
 *   GET  /api/events          — SSE event stream
 *
 * All requests use cookie-backed auth via fetchWithRenewal so that:
 *   - expired access tokens are silently renewed before retry
 *   - the shell never falls back to guest auth mid-submission
 *   - renewal/retry does not duplicate run submission (VAL-CROSS-111)
 *
 * Reattachment across reload/new-tab (VAL-CROSS-121):
 *   - The active run ID is stored in sessionStorage as 'go-choir-active-run'
 *   - On mount, the UI checks for an in-flight run and reattaches instead
 *     of resubmitting
 *   - The stored run ID is cleared when the run reaches a terminal state
 */

import { fetchWithRenewal, AuthRequiredError } from './auth.js';

// ---------------------------------------------------------------------------
// Run submission
// ---------------------------------------------------------------------------

/**
 * Submits a prompt to the runtime API.
 *
 * Uses fetchWithRenewal so that if the access JWT is expired, the request
 * is retried after silent renewal rather than failing outright. The run is
 * only submitted once because the server generates the run ID at acceptance
 * time — a renewed retry of the same POST creates a second run only if the
 * first request never reached the server. The client-side guard below
 * prevents this edge case.
 *
 * @param {string} prompt - The user prompt text.
 * @returns {Promise<{run_id: string, state: string, owner_id: string, created_at: string}>}
 * @throws {AuthRequiredError} If auth renewal fails.
 * @throws {Error} If the server rejects the request.
 */
export async function submitRun(prompt) {
  // Guard: refuse to submit if there is already an in-flight active run.
  // This prevents duplicate submission during renewal retry (VAL-CROSS-111).
  const activeRun = getActiveRun();
  if (activeRun && !isTerminalState(activeRun.state)) {
    // There is already an in-flight run — return its info instead of
    // submitting a new one.
    return activeRun;
  }

  const res = await fetchWithRenewal('/api/agent/run', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ prompt }),
  });

  if (!res.ok) {
    const err = await res.json().catch(() => ({}));
    throw new Error(err.error || `Run submission failed (${res.status})`);
  }

  const runInfo = await res.json();

  // Store the active run so we can reattach after reload (VAL-CROSS-121).
  setActiveRun({
    run_id: runInfo.run_id,
    state: runInfo.state,
    owner_id: runInfo.owner_id,
    created_at: runInfo.created_at,
    prompt,
  });

  return runInfo;
}

// ---------------------------------------------------------------------------
// Run status
// ---------------------------------------------------------------------------

/**
 * Fetches the current status of a run.
 *
 * @param {string} runId - The stable run handle.
 * @returns {Promise<object>} The run status record.
 * @throws {AuthRequiredError} If auth renewal fails.
 * @throws {Error} If the run is not found.
 */
export async function fetchRunStatus(runId) {
  const res = await fetchWithRenewal(`/api/agent/status?run_id=${encodeURIComponent(runId)}`, {
    method: 'GET',
  });

  if (!res.ok) {
    const err = await res.json().catch(() => ({}));
    throw new Error(err.error || `Run status fetch failed (${res.status})`);
  }

  const status = await res.json();

  // Update the stored active run state so reattachment picks up the latest.
  const active = getActiveRun();
  if (active && active.run_id === runId) {
    setActiveRun({ ...active, state: status.state, result: status.result, error: status.error });
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
// Active run persistence (reattachment, VAL-CROSS-121)
// ---------------------------------------------------------------------------

const ACTIVE_RUN_KEY = 'go-choir-active-run';

/**
 * Stores the active run info in sessionStorage so it survives page reload
 * but not closing the browser tab entirely. This supports reattachment
 * across reload and new-tab within the same session (VAL-CROSS-121).
 *
 * Note: sessionStorage is used here because the run ID is not a secret —
 * it is a server-generated UUID that is already exposed through the API.
 * Auth tokens are never stored here; only the run handle for reattachment.
 */
export function setActiveRun(runInfo) {
  try {
    sessionStorage.setItem(ACTIVE_RUN_KEY, JSON.stringify(runInfo));
  } catch (_err) {
    // sessionStorage may be unavailable in some contexts.
  }
}

/**
 * Returns the stored active run info, or null if none.
 */
export function getActiveRun() {
  try {
    const raw = sessionStorage.getItem(ACTIVE_RUN_KEY);
    if (!raw) return null;
    return JSON.parse(raw);
  } catch (_err) {
    return null;
  }
}

/**
 * Clears the stored active run info.
 */
export function clearActiveRun() {
  try {
    sessionStorage.removeItem(ACTIVE_RUN_KEY);
  } catch (_err) {
    // Ignore.
  }
}

/**
 * Returns true if the given run state is terminal (completed, failed, cancelled).
 */
export function isTerminalState(state) {
  return state === 'completed' || state === 'failed' || state === 'cancelled';
}

/**
 * Attempts to reattach to an in-flight run.
 *
 * On mount (or reload), the UI should call this to check whether there is
 * an active run that is still in-flight. If so, it returns the run status
 * so the UI can resume showing progress. If the run has already completed,
 * it returns the final status. If there is no active run, returns null.
 *
 * @returns {Promise<object|null>} The run status, or null if no active run.
 */
export async function reattachToActiveRun() {
  const active = getActiveRun();
  if (!active || !active.run_id) {
    return null;
  }

  try {
    const status = await fetchRunStatus(active.run_id);
    if (isTerminalState(status.state)) {
      // Run already finished — clear the stored handle.
      clearActiveRun();
    } else {
      // Run is still in-flight — update the stored state.
      setActiveRun({ ...active, state: status.state });
    }
    return status;
  } catch (_err) {
    // Status fetch failed — the run may have been cleaned up or auth
    // expired. Clear the stored handle so the UI doesn't get stuck.
    clearActiveRun();
    return null;
  }
}
