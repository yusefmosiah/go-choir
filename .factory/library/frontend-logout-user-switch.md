# Frontend Logout and User-Switch Live-Channel Teardown

## Overview

This feature implements the browser session-lifecycle flows for VAL-CROSS-006
and VAL-CROSS-007: logout must tear down or invalidate the live channel and
revoke protected shell access, and switching from user A to user B must not
leak stale shell/bootstrap/live-channel state from user A.

## Changes

### Shell.svelte — onDestroy cleanup

Added an `onDestroy` lifecycle handler that calls `teardownLiveChannel()` when
the Shell component is destroyed. This is defense in depth: even if
`handleLogout()` was not called explicitly (e.g., the App transitions away
from the shell due to `authexpired`), the WebSocket is properly closed and
reconnection is prevented.

`teardownLiveChannel()` sets `wsClosedByLogout = true` and closes any open
WebSocket. This prevents stale user-A live channels from surviving into a
user-B session when the Shell is destroyed and recreated.

### App.svelte — bfcache and back-navigation protection

Added two event listeners on mount:

1. **`pageshow` event** — Detects when the page is restored from browser
   back/forward cache (bfcache). After logout, if the user presses the
   browser's back button, the page might be restored from bfcache with
   the old JavaScript state still showing the shell. The `pageshow`
   handler re-checks the session via `checkSession()`, which calls
   `GET /auth/session`. Since the server cleared the cookies on logout,
   the session check returns `authenticated: false` and the app
   transitions to the guest auth UI.

2. **`focus` event** — Secondary guard that re-checks the session when
   the window gains focus, if the app currently thinks it's signed in.
   This catches cases where the user logged out in another tab and then
   switches back to this tab.

Both listeners are cleaned up when the component is destroyed (the `onMount`
return function removes them).

## Flow Details

### Logout Flow (VAL-CROSS-006)

1. User clicks "Sign Out" in the Shell header
2. Shell's `handleLogout()`:
   - Sets `wsClosedByLogout = true`
   - Closes the WebSocket (`ws.close()`)
   - Dispatches `logout` event
3. App's `handleLogout()`:
   - Calls `POST /auth/logout` (server clears cookies and invalidates session)
   - Sets `authState = 'signed_out'`
   - Sets `currentUser = null`
4. Svelte reactively unmounts the Shell component
5. Shell's `onDestroy()` runs `teardownLiveChannel()` (defense in depth)
6. AuthEntry component renders (guest auth UI)

After logout:
- `GET /auth/session` returns `authenticated: false` (cookies cleared)
- `GET /api/shell/bootstrap` returns 401 (no valid auth)
- `GET /api/ws` cannot upgrade (no valid auth)
- Browser back/refresh shows guest auth UI (bfcache protection)
- No stale protected requests are fired while signed out

### User-Switch Flow (VAL-CROSS-007)

1. User A authenticates → Shell renders → bootstrap + WS work for A
2. User A clicks "Sign Out" → Shell closes WS, App sets signed_out
3. Shell component is destroyed (all A's state is garbage collected)
4. User B registers/logs in → new Shell component created
5. New Shell boots with user B's `currentUser` prop
6. Bootstrap and WS work for user B only
7. No stale user-A identity, shell state, or live channel leaks into B's session

Key guarantee: The Shell component is fully destroyed between users. Since
Svelte creates a fresh component instance, there is no in-memory state
leakage. The server-side session is also fully invalidated by logout.

## Test Data Attributes

No new data attributes were added. The existing attributes are sufficient:
- `data-shell-logout` — logout button (click triggers teardown)
- `data-shell-live-status` — live channel status indicator
- `data-shell-user` — current user display
- `data-shell-bootstrap` — bootstrap data section
- `data-auth-entry` — guest auth entry (visible after logout)

## Playwright Tests

`frontend/tests/logout-user-switch.spec.js` covers:

VAL-CROSS-006:
1. Logout tears down the open live channel
2. After logout, GET /api/shell/bootstrap fails (401/403)
3. After logout, GET /api/ws cannot reconnect
4. Back navigation after logout does not resurrect the shell
5. Refresh after logout does not resurrect the shell

VAL-CROSS-007:
6. User A -> logout -> user B produces only user-B shell state
   (checks user display, session, bootstrap data by user ID)
7. User A live channel does not leak into user B session
8. User A -> logout -> user B in separate browser contexts has no stale state
9. Repeated logout does not cause errors and keeps user in guest state

## Key Invariants

- The Shell's `onDestroy` handler always tears down the live channel,
  regardless of why the component was destroyed
- The `pageshow` event handler prevents bfcache from resurrecting the
  shell after logout
- Auth state is always re-verified from the server (`GET /auth/session`),
  never trusted from in-memory state alone
- No protected routes are called while signed out
- The Shell component is fully destroyed between user sessions, preventing
  any in-memory state leakage
- Logout closes the WebSocket before dispatching the logout event,
  and `wsClosedByLogout` prevents reconnection attempts
