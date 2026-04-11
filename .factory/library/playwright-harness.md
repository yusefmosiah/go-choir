# Playwright Virtual-Authenticator Harness

## Overview

The Playwright Chromium virtual-authenticator harness lives under `frontend/` and enables automated WebAuthn (passkey) registration, login, session, and logout testing against the local service stack on `http://localhost:4173`.

## Architecture

- **Chromium-only** — WebAuthn virtual authenticator requires CDP (Chrome DevTools Protocol), available only in Chromium-based browsers.
- **Serial execution** — Tests run with `fullyParallel: false` because they share a mutable local auth DB and virtual-authenticator state.
- **Same-origin only** — All /auth/* calls go through `http://localhost:4173` (Vite dev server proxying to `127.0.0.1:8081`). No direct service-port calls, no auth bypass, no token injection.
- **No local auth bypass** — The harness exercises the real /auth/* API routes with real WebAuthn ceremonies. It does not add any test-only escape hatch or weaker auth path.

## File Structure

```
frontend/
  playwright.config.js          # Chromium-only, serial, baseURL=localhost:4173
  tests/
    helpers/
      webauthn.js               # CDP virtual-authenticator setup/teardown
      auth.js                   # registerPasskey, loginPasskey, getSession, logout
      fixtures.js               # Custom Playwright fixtures with authenticator
    auth-passkey.spec.js        # Passkey registration, login, session, logout, replay tests
```

## How It Works

### Virtual Authenticator

The `setupVirtualAuthenticator()` helper:
1. Creates a CDP session via `page.context().newCDPSession(page)`
2. Enables the WebAuthn domain with `WebAuthn.enable`
3. Adds a virtual authenticator with `WebAuthn.addVirtualAuthenticator` (CTAP2, internal transport, resident key, user verification, automatic presence simulation)

The virtual authenticator automatically handles `navigator.credentials.create()` and `navigator.credentials.get()` calls without any manual user interaction.

### Auth Flow Helpers

The auth helpers (`registerPasskey`, `loginPasskey`, `getSession`, `logout`) run inside `page.evaluate()` to execute real browser WebAuthn API calls:
1. `fetch()` to `/auth/register/begin` → get creation options
2. `navigator.credentials.create()` → virtual authenticator handles ceremony
3. `fetch()` to `/auth/register/finish` → complete registration, get cookies
4. Similar flow for login (`/auth/login/begin` → `navigator.credentials.get()` → `/auth/login/finish`)

All fetch calls use `credentials: 'include'` to send/receive same-origin auth cookies.

### Vite Proxy

The Vite dev server proxies `/auth/*` to `127.0.0.1:8081` so the Playwright tests (and the dev frontend) can call same-origin auth routes without hitting direct service ports.

## Running the Tests

Prerequisites per `.factory/services.yaml`:
1. Auth service running: `AUTH_RP_ID="localhost" AUTH_RP_ORIGINS="http://localhost:4173" AUTH_COOKIE_SECURE="false" go run ./cmd/auth`
2. Frontend dev server running: `cd frontend && pnpm dev --host localhost --port 4173`
3. Playwright Chromium installed: `cd frontend && pnpm exec playwright install chromium`

Run tests:
```sh
cd frontend && pnpm exec playwright test
# or via the manifest command:
# .factory/services.yaml -> commands.frontend-e2e
```

## Test Coverage

| Test | Validates |
|------|-----------|
| passkey registration creates a credential and authenticated session | VAL-AUTH-004 (register begin returns WebAuthn options), VAL-AUTH-006 (valid finish creates session) |
| passkey login returns assertion options and creates session | VAL-AUTH-005 (login begin returns assertion options), VAL-AUTH-007 (authenticated session returns identity) |
| authenticated /auth/session returns user identity without secrets | VAL-AUTH-007 (no secrets in session response) |
| logout invalidates session and is safe to repeat | VAL-AUTH-008 (logout invalidation + safe repeat) |
| replayed registration finish payload does not create a session | VAL-AUTH-006 (replay fails closed) |
| GET /auth/session returns signed-out for missing auth cookies | VAL-AUTH-002 (signed-out for missing state) |

## Gotchas

- The virtual authenticator's resident key must be enabled (`hasResidentKey: true`) for passkey login flows that use `allowCredentials: []` (discoverable credentials).
- The virtual authenticator must be set up BEFORE any `navigator.credentials` call on the page.
- Tests must run serially (`fullyParallel: false`) because they share the auth service's SQLite DB.
- Each test generates a unique username (`pw-test-<timestamp>-<random>`) to avoid DB collisions.
- The auth service must be started with `AUTH_COOKIE_SECURE="false"` for localhost testing.
