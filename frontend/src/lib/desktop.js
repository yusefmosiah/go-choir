/**
 * Desktop state API client for the go-choir desktop shell.
 *
 * Communicates with the desktop state API through the same-origin proxy
 * routes only:
 *   GET  /api/desktop/state  — fetch persisted desktop state
 *   PUT  /api/desktop/state  — save desktop state
 *
 * All requests use cookie-backed auth via fetchWithRenewal so that:
 *   - expired access tokens are silently renewed before retry
 *   - the desktop never falls back to guest auth mid-operation
 *
 * Desktop state is persisted server-side so that restore works across
 * fresh browser contexts for the same user (VAL-DESKTOP-007).
 */

import { fetchWithRenewal, AuthRequiredError } from './auth.js';

// ---------------------------------------------------------------------------
// Desktop state fetch
// ---------------------------------------------------------------------------

/**
 * Fetches the persisted desktop state for the current authenticated user.
 *
 * Returns the full desktop state including open windows, active window,
 * geometry, and app context. If no state exists, the server returns an
 * empty default state.
 *
 * @returns {Promise<{owner_id: string, windows: Array, active_window_id: string, updated_at: string}>}
 * @throws {AuthRequiredError} If auth renewal fails.
 */
export async function fetchDesktopState() {
  const res = await fetchWithRenewal('/api/desktop/state', {
    method: 'GET',
  });

  if (!res.ok) {
    const err = await res.json().catch(() => ({}));
    throw new Error(err.error || `Desktop state fetch failed (${res.status})`);
  }

  return res.json();
}

// ---------------------------------------------------------------------------
// Desktop state save
// ---------------------------------------------------------------------------

/**
 * Saves the desktop state for the current authenticated user.
 *
 * The state is persisted server-side and survives fresh browser contexts
 * (VAL-DESKTOP-007). Includes window identities, geometry, mode, active
 * window, and app context.
 *
 * @param {object} state - The desktop state to save.
 * @param {Array} state.windows - Open windows.
 * @param {string} [state.active_window_id] - Currently focused window ID.
 * @returns {Promise<{ok: boolean, updated_at: string}>}
 * @throws {AuthRequiredError} If auth renewal fails.
 */
export async function saveDesktopState(state) {
  const res = await fetchWithRenewal('/api/desktop/state', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      windows: state.windows,
      active_window_id: state.active_window_id || '',
    }),
  });

  if (!res.ok) {
    const err = await res.json().catch(() => ({}));
    throw new Error(err.error || `Desktop state save failed (${res.status})`);
  }

  return res.json();
}
