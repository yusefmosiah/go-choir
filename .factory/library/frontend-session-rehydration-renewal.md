# Frontend Session Rehydration and Renewal

## Overview

This feature implements cookie-backed shell rehydration, silent renewal through refresh rotation, and failed-renewal fallback to guest auth state.

Fulfills: VAL-CROSS-004, VAL-CROSS-005, VAL-CROSS-008.

## Rehydration Flow (VAL-CROSS-005)

On mount (including hard reload and new tab), `App.svelte` calls `checkSession()` which hits `GET /auth/session`. This endpoint:

1. If access JWT is valid → returns `authenticated: true` with user info
2. If access JWT expired but refresh cookie valid → rotates refresh, issues new access JWT + refresh cookie, returns `authenticated: true`
3. If both invalid → returns `authenticated: false`

The frontend transitions to `signed_in` / `signed_out` based on this response. No in-memory state is relied upon — the shell is always rehydrated from cookie-backed server state.

## Silent Renewal Flow (VAL-CROSS-004)

When a protected request (e.g., `GET /api/shell/bootstrap`) returns 401:

1. `fetchWithRenewal()` calls `renewSession()` → `GET /auth/session`
2. If refresh rotation succeeds, new cookies are set automatically by the server
3. The original request is retried with the new cookies
4. No new passkey ceremony is required

The Shell component's `fetchBootstrap()` uses `fetchWithRenewal()`. If renewal fails, it dispatches `authexpired`.

## Live Channel Reconnection

When the WebSocket connection closes or errors (not due to logout):

1. The Shell's `attemptWsReconnection()` runs with exponential backoff (max 5 attempts)
2. Before reconnecting, it calls `renewSession()` to check/refresh auth
3. If renewal succeeds, a new WebSocket connection is created
4. If renewal fails, the Shell dispatches `authexpired`
5. On successful WS open, the reconnection attempt counter resets

## Failed Renewal Fallback (VAL-CROSS-008)

When renewal cannot restore the session (both access and refresh are invalid):

1. The Shell dispatches `authexpired` event
2. `App.svelte` handles it by transitioning to `signed_out` state
3. The guest auth entry UI is shown — no stale shell, no infinite retry loop

## New Functions in `auth.js`

- `renewSession()` — Calls `GET /auth/session` and returns `{ renewed: boolean, user?: object }`
- `fetchWithRenewal(url, options)` — Protected fetch with automatic 401 → renewal → retry
- `AuthRequiredError` — Thrown when renewal fails, signaling fallback to guest state

## Component Events

- Shell dispatches `authexpired` when renewal fails (bootstrap or WS reconnection)
- App.svelte handles `authexpired` by transitioning to `signed_out` state

## Test Data Attributes

No new data attributes were added. The existing attributes are sufficient:
- `data-shell` — shell root (visible = authenticated, hidden = signed out)
- `data-auth-entry` — guest auth entry (visible = signed out)
- `data-shell-bootstrap` — bootstrap data section
- `data-shell-live-status` — live channel status indicator

## Playwright Tests

### `frontend/tests/session-rehydration-renewal.spec.js`

VAL-CROSS-005 tests:
1. Hard reload at `/` rehydrates the shell from cookies
2. New tab at `/` rehydrates the shell from cookies

VAL-CROSS-004 tests:
3. Expired access cookie renews through refresh rotation on reload
4. Live channel reconnects after successful renewal following access expiry
5. Replayed old refresh state cannot restore access after rotation

VAL-CROSS-008 tests:
6. Failed renewal falls back to guest auth state on reload
7. Mounted shell falls back to guest state when protected request fails
8. Failed renewal does not leave stale live channel state

## Key Invariants

- The rehydration checkpoint is always `GET /auth/session` — the frontend never stores or manages tokens
- Auth tokens never appear in the URL, localStorage, or sessionStorage
- The Shell's protected requests use `fetchWithRenewal` which handles renewal transparently
- WS reconnection always checks session validity before attempting to reconnect
- The `authexpired` event is the clean fallback signal — no half-authenticated state
- The Shell marks logout-triggered WS close to prevent reconnection attempts
