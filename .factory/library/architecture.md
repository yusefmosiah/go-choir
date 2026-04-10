# Architecture

This file captures how Mission 2 Milestone 1 is expected to work at a high level.

## Mission 2 Milestone 1 Scope

This mission delivers the first end-to-end vertical slice on `https://draft.choir-ip.com`:

- WebAuthn registration and login
- cookie-backed auth/session lifecycle
- protected proxy routing to a single hardcoded placeholder sandbox
- a minimal Svelte desktop shell

Out of scope here: provider/gateway behavior, real VM routing, recovery flows, and later desktop applications.

## Runtime Topology

### Public edge

**Caddy on Node B** terminates TLS and serves one public origin: `https://draft.choir-ip.com`.

For this mission, public routes are:

- `/auth/*` → auth service on `127.0.0.1:8081`
- `/api/*` → proxy service on `127.0.0.1:8082`
- `/provider/*` → gateway service on `127.0.0.1:8084` (not a Milestone 1 acceptance surface)
- `/` → built Svelte frontend

Use `handle`, not `handle_path`, so services receive the full public prefix.

### Host services

| Service | Binary | Port | Mission 2 Milestone 1 role |
| --- | --- | --- | --- |
| auth | `cmd/auth` | 8081 | WebAuthn ceremonies, session inspection, logout, access+refresh issuance/rotation |
| proxy | `cmd/proxy` | 8082 | Validates auth state and forwards protected HTTP/WS traffic |
| vmctl | `cmd/vmctl` | 8083 | Out of scope for this mission; remains non-user-facing |
| gateway | `cmd/gateway` | 8084 | Out of scope for this mission; route stays configured but behavior is not part of acceptance |
| sandbox | `cmd/sandbox` | 8085 | Placeholder upstream used by proxy for Milestone 1 bootstrap + WebSocket flows |

## Component Responsibilities

### Auth service

Auth owns identity and auth-state truth for this mission.

Expected public routes:

- `POST /auth/register/begin`
- `POST /auth/register/finish`
- `POST /auth/login/begin`
- `POST /auth/login/finish`
- `GET /auth/session`
- `POST /auth/logout`

High-level responsibilities:

- store users and WebAuthn credentials in SQLite
- persist WebAuthn challenge/session data, refresh/session records, and replay-protection state needed by the finish routes
- bind WebAuthn flows to RP ID `draft.choir-ip.com` for deployed validation
- issue short-lived access JWTs plus rotating refresh state
- expose current signed-in/signed-out session state to the frontend
- invalidate access/refresh state on logout

Renewal model for this mission:

- `GET /auth/session` is the canonical session-rehydration and silent-renewal route
- if access state is expired but refresh state is still valid, `/auth/session` rotates refresh state and sets renewed auth cookies
- the frontend then proceeds with `GET /api/shell/bootstrap` and `GET /api/ws`

Cookie invariants for this mission:

- auth cookies are same-origin and cookie-backed
- auth cookies are `Secure` and `HttpOnly`
- auth cookies must use `SameSite` or an equivalent CSRF/origin defense
- auth tokens never appear in the URL or browser web storage

### Proxy service

Proxy owns protected public shell traffic.

Deterministic protected routes for this mission:

- `GET /api/shell/bootstrap`
- `GET /api/ws`

High-level responsibilities:

- deny missing, invalid, expired, or tampered auth state
- verify auth-issued access JWTs locally with auth-owned verification material
- forward protected traffic to the single hardcoded placeholder sandbox
- forward the public `/api/*` path unchanged for Milestone 1 placeholder routes
- strip or ignore client-supplied identity headers
- inject the current authenticated user context for the placeholder sandbox
- reject reconnects when auth is no longer valid
- treat live channels as invalid after logout/user-switch teardown and after auth renewal failure

### Placeholder sandbox

The placeholder sandbox exists only to make the Milestone 1 shell real and testable.

High-level responsibilities:

- serve the protected bootstrap payload used by the shell
- expose a protected WebSocket endpoint that can prove live connectivity
- surface sandbox identity and current-user context for validation

Every authenticated user reaches the same placeholder sandbox instance in this mission.

### Frontend

The Svelte frontend serves two states:

- **guest auth UI** when no valid same-origin auth cookies exist
- **placeholder desktop shell** when a valid session exists

High-level responsibilities:

- expose clear register/login flows
- use same-origin `/auth/*` and `/api/*` routes only
- never store auth tokens in the URL or web storage
- bootstrap the shell from cookie-backed state
- rehydrate the shell on hard reload/new tab while the session is still valid
- fall back cleanly to the guest auth UI when renewal can no longer succeed

## Primary User/Data Flows

### Registration flow

1. Guest opens `/`
2. Frontend shows register UI
3. Frontend calls `POST /auth/register/begin`
4. Browser completes WebAuthn ceremony on `draft.choir-ip.com`
5. Frontend calls `POST /auth/register/finish`
6. Auth sets same-origin auth cookies
7. Frontend loads the placeholder shell
8. Shell calls `GET /api/shell/bootstrap` and opens `GET /api/ws`

### Login flow

1. Guest opens `/`
2. Frontend shows login UI
3. Frontend calls `POST /auth/login/begin`
4. Browser completes WebAuthn login
5. Frontend calls `POST /auth/login/finish`
6. Auth sets same-origin auth cookies
7. Shell bootstraps through proxy routes

### Session lifecycle flow

1. Shell is reloaded or reopened at `/`
2. Frontend calls `GET /auth/session` to rehydrate cookie-backed auth state
3. If access state is expired but refresh is still valid, auth renews access and rotates refresh state on that request
4. Frontend continues with `GET /api/shell/bootstrap` and `GET /api/ws`
5. Shell remains usable and live connectivity is restored or reconnected
6. If refresh can no longer renew, the frontend falls back to guest auth state

### Logout and user-switch flow

1. Authenticated user clicks logout in the shell
2. Frontend calls `POST /auth/logout`
3. Auth invalidates refresh/session state and returns signed-out cookie state
4. Frontend tears down any open live channel
5. Existing protected shell HTTP/WS access stops working and reconnects are denied
6. Browser returns to guest auth UI
7. A second user can authenticate without inheriting stale state from the first

## Invariants

- The deployed acceptance surface is `https://draft.choir-ip.com`
- No local auth bypass mode is allowed
- No browser-visible flow may depend on direct service ports, `localhost`, or stripped internal paths
- Auth state is cookie-backed and same-origin; tokens are not exposed in URLs or web storage
- `GET /auth/session` is the rehydration and silent-renewal checkpoint for the frontend
- The proxy, not the browser, is the trust boundary for sandbox user context
- The proxy validates access JWTs locally; refresh renewal remains auth-owned
- Milestone 1 placeholder routes stay public-path-stable as `GET /api/shell/bootstrap` and `GET /api/ws`
- Prefix-preserving routing matters at the public edge
- Milestone 1 uses one hardcoded placeholder sandbox for all users
