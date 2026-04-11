# Frontend Shell UI and Passkey Errors

## Component Structure

- `frontend/src/App.svelte` — Root component that checks auth state on mount via `GET /auth/session` and renders either `AuthEntry` (signed out) or `Shell` (signed in). Handles passkey ceremony wiring and error display.
- `frontend/src/lib/AuthEntry.svelte` — Guest auth entry with distinct register/login views, toggle tabs, username input, "Register with Passkey" / "Sign In with Passkey" primary action buttons, and passkey error display.
- `frontend/src/lib/Shell.svelte` — Authenticated placeholder desktop shell with header bar, logout control, session-aware current-user display, bootstrap data section, live channel status, and placeholder desktop chrome.
- `frontend/src/lib/auth.js` — WebAuthn ceremony helpers (`registerPasskey`, `loginPasskey`, `getSession`, `logout`, `passkeyErrorMessage`) that perform the full passkey flow using same-origin `/auth/*` routes only.

## Auth State Flow

1. On mount, `App.svelte` calls `GET /auth/session` with `credentials: 'include'`.
2. If the response indicates `authenticated: true`, the app sets `authState = 'signed_in'` and renders the Shell.
3. If the response indicates `authenticated: false` or the request fails, the app sets `authState = 'signed_out'` and renders the AuthEntry.
4. The `AuthEntry` component dispatches an `authbegin` event with `{ username, type }` when the user clicks the primary action button.
5. `App.svelte` handles the `authbegin` event by calling `registerPasskey(username)` or `loginPasskey(username)` from `auth.js`.
6. On ceremony success, `App.svelte` calls `checkSession()` to transition to the authenticated shell state.
7. On ceremony cancellation (NotAllowedError) or failure, `App.svelte` sets `passkeyError` which is displayed in the `AuthEntry` component. The user stays in the guest auth state and can retry.

## Passkey Error Handling (VAL-FRONTEND-004)

When a passkey ceremony is cancelled or fails:
- The `passkeyErrorMessage(err)` function returns a user-friendly error message.
- `NotAllowedError` (user cancelled) maps to: "Passkey ceremony was cancelled. Please try again."
- Server-side begin/finish errors surface the server's error message.
- Other errors map to: "Passkey authentication failed. Please try again."
- The error is displayed in the `AuthEntry` component via the `data-passkey-error` element.
- Switching between register and login views clears the passkey error via the `clearpasskeyerror` event.
- The authenticated shell is NEVER exposed after a cancelled or failed ceremony.

## Shell Component (VAL-FRONTEND-003)

The Shell component is visually distinct from the guest auth UI:
- **Header bar** with app name "go-choir" + "Shell" badge, current user display (username), and "Sign Out" button.
- **Bootstrap panel** — calls `GET /api/shell/bootstrap` on mount and displays the response data.
- **Live channel panel** — opens a WebSocket to `GET /api/ws` on mount and shows connection status (disconnected, connecting, connected, error).
- **Desktop panel** — placeholder desktop chrome with "agents and tools will appear here" text.

The shell only boots protected traffic (bootstrap + WS) when the component mounts in the authenticated state.

## Key Invariants

- No protected routes (`/api/shell/bootstrap`, `/api/ws`) are called while signed out.
- Auth is cookie-backed and same-origin only; no tokens in URL, localStorage, or sessionStorage.
- The `authbegin` event handler in `App.svelte` performs the real WebAuthn flow via `auth.js`.
- Cancelled or failed passkey ceremonies keep the user in a retryable guest auth state.
- The authenticated shell is never exposed from a failed or cancelled passkey ceremony.
- The Vite dev server proxies `/auth/*` to port 8081 and `/api/*` (including WS) to port 8082.

## Test Data Attributes

### AuthEntry component
- `data-auth-entry` — root container
- `data-register-toggle` — tab button to switch to register view
- `data-login-toggle` — tab button to switch to login view
- `data-register-view` — register view container (hidden when login view is active)
- `data-login-view` — login view container (hidden when register view is active)
- `data-passkey-error` — passkey ceremony error message area

### Shell component
- `data-shell` — root container
- `data-shell-header` — top bar with app name, user display, logout
- `data-shell-logout` — logout button
- `data-shell-user` — current user display
- `data-shell-bootstrap` — bootstrap data section
- `data-shell-live-status` — live channel status indicator

## Playwright Tests

### `frontend/tests/shell-ui.spec.js` (VAL-FRONTEND-003)
1. Authenticated shell is visible and distinct from guest auth UI
2. Authenticated shell includes a visible logout control
3. Authenticated shell exposes session-aware current user display
4. Authenticated shell calls GET /api/shell/bootstrap on mount
5. Authenticated shell shows bootstrap data section
6. Authenticated shell shows live channel status indicator
7. Clicking logout returns to guest auth UI

### `frontend/tests/passkey-errors.spec.js` (VAL-FRONTEND-004)
1. Cancelled passkey ceremony shows error and stays in guest auth UI
2. Cancelled login passkey ceremony shows error and stays in guest auth UI
3. Failed passkey ceremony shows error and stays in guest auth UI
4. User can retry after a cancelled passkey ceremony
5. Switching between register and login views clears the passkey error
6. Auth begin endpoint failure shows error and stays in guest auth UI

### `frontend/tests/auth-entry-ui.spec.js` (VAL-FRONTEND-001, VAL-FRONTEND-002)
1. Signed-out root shows guest auth UI instead of placeholder
2. Guest users can reach both register and login views
3. Register view has a clear primary action to begin passkey flow
4. Login view has a clear primary action to begin passkey flow
5. Signed-out root does not show the authenticated shell
6. Signed-out render does not repeatedly fire failing protected requests
