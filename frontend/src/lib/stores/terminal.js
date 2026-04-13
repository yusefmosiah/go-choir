/**
 * Terminal store for managing multiple independent terminal sessions.
 *
 * Each session tracks its ghostty-web Terminal instance, FitAddon,
 * WebSocket connection, and lifecycle state.
 *
 * Sessions are keyed by windowId so each floating terminal window
 * has its own independent PTY on the backend.
 */

import { writable } from 'svelte/store';

/** @type {import('svelte/store').Writable<Object>} Map of windowId -> session state */
export const terminalSessions = writable({});

/**
 * Create a new terminal session record.
 * @param {string} windowId
 * @returns {Object} session state
 */
export function createTerminalSession(windowId) {
  const session = {
    windowId,
    term: null,         // ghostty-web Terminal instance
    fitAddon: null,     // FitAddon instance
    ws: null,           // WebSocket connection
    initialized: false, // WASM init + terminal.open() done
    error: null,        // last error message
  };
  terminalSessions.update((sessions) => ({
    ...sessions,
    [windowId]: session,
  }));
  return session;
}

/**
 * Update a terminal session's state.
 * @param {string} windowId
 * @param {Object} updates - partial session state to merge
 */
export function updateTerminalSession(windowId, updates) {
  terminalSessions.update((sessions) => {
    if (!sessions[windowId]) return sessions;
    return {
      ...sessions,
      [windowId]: { ...sessions[windowId], ...updates },
    };
  });
}

/**
 * Get a terminal session by windowId (non-reactive snapshot).
 * @param {string} windowId
 * @returns {Object|null}
 */
export function getTerminalSession(windowId) {
  let result = null;
  terminalSessions.subscribe((sessions) => {
    result = sessions[windowId] || null;
  })();
  return result;
}

/**
 * Destroy a terminal session: dispose terminal, close WebSocket.
 * @param {string} windowId
 */
export function destroyTerminalSession(windowId) {
  const session = getTerminalSession(windowId);
  if (session) {
    if (session.term) {
      try { session.term.dispose(); } catch (_e) { /* ignore */ }
    }
    if (session.ws) {
      try {
        if (session.ws.readyState === WebSocket.OPEN || session.ws.readyState === WebSocket.CONNECTING) {
          session.ws.close();
        }
      } catch (_e) { /* ignore */ }
    }
  }
  terminalSessions.update((sessions) => {
    const { [windowId]: _removed, ...rest } = sessions;
    return rest;
  });
}
