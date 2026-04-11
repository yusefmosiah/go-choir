# Frontend Mounted-Shell Renewal Follow-Up

## Overview

This feature completes the mounted-shell silent-renewal path so protected shell activity can trigger refresh rotation without depending only on reload/new-tab or WebSocket reconnect flows.

## What Was Added

### In-Shell "Refresh" Button (`data-shell-refresh`)

An explicit protected action button in the Shell's bootstrap panel that re-fetches `GET /api/shell/bootstrap` via `fetchWithRenewal`. This is the first user-triggered in-shell protected action that proves renewal works mid-session.

### Renewal Status Indicator (`data-shell-refresh-status`)

Shows "Session renewed" after successful renewal or "Refresh failed" for non-auth errors. When renewal fails completely (both access and refresh invalid), the Shell dispatches `authexpired` and App.svelte transitions to guest auth state.

## Flow

1. Shell is already mounted and active
2. Access JWT expires (refresh cookie still valid)
3. User clicks "Refresh" button
4. `handleRefresh()` calls `fetchWithRenewal('/api/shell/bootstrap')`
5. `fetchWithRenewal` gets 401, calls `renewSession()` → `GET /auth/session`
6. Server rotates refresh and issues new access JWT (cookies set automatically)
7. `fetchWithRenewal` retries the bootstrap request with new cookies
8. Bootstrap data updates, status shows "Session renewed"
9. Shell remains stable — no page reload, no new passkey ceremony

### Failed Renewal

1. Both access and refresh cookies invalid
2. User clicks "Refresh"
3. `fetchWithRenewal` gets 401, calls `renewSession()`
4. `renewSession()` returns `{ renewed: false }`
5. `fetchWithRenewal` throws `AuthRequiredError`
6. `handleRefresh()` dispatches `authexpired`
7. App.svelte transitions to guest auth state — no stale shell

## Key Invariants

- No new auth mechanism: renewal still goes through `GET /auth/session` only
- Auth tokens never appear in URL, localStorage, or sessionStorage
- The refresh button uses `fetchWithRenewal` which is the same helper used by initial bootstrap fetch and WS reconnection
- Shell stability: successful renewal does not cause a page reload or shell unmount
- Clean fallback: failed renewal transitions to guest auth UI without a half-authenticated state

## Test Data Attributes

- `data-shell-refresh` — the Refresh button in the bootstrap panel
- `data-shell-refresh-status` — the renewal status span next to the button

## Playwright Tests

`frontend/tests/mounted-shell-renewal.spec.js` covers:

1. In-shell refresh renews expired access through refresh rotation
2. Successful renewal keeps the shell stable without a full reload
3. Failed renewal falls back cleanly to guest state
4. The refresh action uses GET /auth/session (no new auth mechanism)
5. Refresh works while the live channel is already connected
