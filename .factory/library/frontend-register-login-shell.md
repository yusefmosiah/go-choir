# Frontend Register/Login Shell Integration

## Overview

This feature wires the real browser registration and login flows end to end:
passkey success → cookie-backed transition into the placeholder shell →
immediate shell bootstrap through `GET /api/shell/bootstrap` → successful
live-channel connection through `GET /api/ws` — all without manual token
injection or direct-port calls.

Fulfills: VAL-CROSS-001, VAL-CROSS-002, VAL-CROSS-003.

## End-to-End Flow

### Registration Flow (VAL-CROSS-001)

1. Guest opens `/`
2. Frontend shows register view (default in AuthEntry)
3. User fills in username, clicks "Register with Passkey"
4. `auth.js:registerPasskey()` calls `POST /auth/register/begin`
5. Browser completes WebAuthn ceremony (`navigator.credentials.create()`)
6. `auth.js:registerPasskey()` calls `POST /auth/register/finish` with credentials
7. Auth service validates, creates user, sets `choir_access` + `choir_refresh` cookies
8. `App.svelte:handleAuthBegin()` calls `checkSession()` → `GET /auth/session`
9. Session confirms authenticated → `authState = 'signed_in'`
10. Shell component renders → on mount calls `fetchBootstrap()` + `connectLiveChannel()`
11. Shell bootstrap `GET /api/shell/bootstrap` uses cookie auth → reaches sandbox via proxy
12. Live channel `GET /api/ws` uses cookie auth → WebSocket upgrade through proxy

**No page reload is required** — the transition happens reactively in Svelte.

### Login Flow (VAL-CROSS-002)

1. Returning user opens `/` from a fresh signed-out state
2. Frontend shows AuthEntry, user switches to login view
3. User fills in username, clicks "Sign In with Passkey"
4. `auth.js:loginPasskey()` calls `POST /auth/login/begin`
5. Browser completes WebAuthn login (`navigator.credentials.get()`)
6. `auth.js:loginPasskey()` calls `POST /auth/login/finish`
7. Auth service validates, issues cookies
8. Same shell transition as registration (steps 8-12 above)

### Cookie-Backed Auth (VAL-CROSS-003)

- `choir_access` cookie: `Path=/`, `HttpOnly=true`, `SameSite=Lax`, `Secure` (on deployed HTTPS)
- `choir_refresh` cookie: `Path=/auth`, `HttpOnly=true`, `SameSite=Lax`, `Secure` (on deployed HTTPS)
- No auth tokens in URL, `localStorage`, or `sessionStorage`
- All protected requests use `credentials: 'include'` (same-origin cookie auth)
- No `Authorization: Bearer` headers, no manual token injection
- No direct service port calls (8081, 8082, 8085) in browser traffic
- All browser requests go through the Vite dev server (localhost:4173) or deployed origin

## Ceremony In-Progress State

The `ceremonyInProgress` flag in `App.svelte` is wired to `AuthEntry` to:
- Disable the username input during ceremony
- Disable the primary action button during ceremony
- Show loading text ("Creating passkey…" / "Signing in…") during ceremony
- Disable the Register/Sign In tab toggle during ceremony

This prevents double-submits and provides visual feedback during the
passkey ceremony + session check.

## Component Data Attributes

### AuthEntry (existing + new)
- `data-auth-entry` — root container
- `data-register-toggle` — tab button to switch to register view
- `data-login-toggle` — tab button to switch to login view
- `data-register-view` — register view container
- `data-login-view` — login view container
- `data-passkey-error` — passkey ceremony error message area
- `data-auth-submit` — primary action button (register/login submit)

### Shell (existing)
- `data-shell` — root container
- `data-shell-header` — top bar with app name, user, logout
- `data-shell-logout` — logout button
- `data-shell-user` — current user display
- `data-shell-bootstrap` — bootstrap data section
- `data-shell-live-status` — live channel status indicator

## Playwright Tests

### `frontend/tests/register-login-shell.spec.js`

VAL-CROSS-001 tests:
1. First-time user registers and lands in shell without page reload
2. Registered user sees bootstrap data after shell mount
3. Registered user has live channel connected in the shell

VAL-CROSS-002 tests:
4. Returning user logs in from signed-out state and lands in the shell
5. Returning user sees bootstrap data after login
6. Returning user has live channel connected after login

VAL-CROSS-003 tests:
7. Auth cookies are HttpOnly and have SameSite attribute
8. No auth tokens in localStorage or sessionStorage
9. No direct service port calls in browser traffic
10. Shell bootstrap and WS work with cookie auth only (no bearer token)

Additional integration:
11. Auth form controls are disabled during passkey ceremony

## Key Invariants

- The passkey ceremony → shell transition works WITHOUT page reload
- Protected routes (`/api/shell/bootstrap`, `/api/ws`) are only called after auth
- Auth is same-origin cookie-backed; no tokens in URL or web storage
- The shell boots bootstrap and live channel immediately on mount after auth
- No deployed browser flow requires direct service ports, localhost, or manual bearer tokens
- The `ceremonyInProgress` state prevents double-submits during passkey ceremonies
