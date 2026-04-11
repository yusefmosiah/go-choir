# Frontend Auth Entry UI

## Component Structure

- `frontend/src/App.svelte` — Root component that checks auth state on mount via `GET /auth/session` and renders either `AuthEntry` (signed out) or a placeholder shell (signed in).
- `frontend/src/lib/AuthEntry.svelte` — Guest auth entry with distinct register/login views, toggle tabs, username input, and "Register with Passkey" / "Sign In with Passkey" primary action buttons.

## Auth State Flow

1. On mount, `App.svelte` calls `GET /auth/session` with `credentials: 'include'`.
2. If the response indicates `authenticated: true`, the app sets `authState = 'signed_in'` and renders the placeholder shell.
3. If the response indicates `authenticated: false` or the request fails, the app sets `authState = 'signed_out'` and renders the guest auth entry.
4. The `AuthEntry` component dispatches an `authbegin` event with `{ username, type }` when the user clicks the primary action button.

## Key Invariants

- No protected routes (`/api/shell/bootstrap`, `/api/ws`) are called while signed out.
- Auth is cookie-backed and same-origin only; no tokens in URL, localStorage, or sessionStorage.
- The `authbegin` event handler in `App.svelte` is a no-op placeholder — actual WebAuthn flow wiring is handled by the passkey-integration feature.
- The Vite dev server proxies `/auth/*` to port 8081 and `/api/*` (including WS) to port 8082.

## Test Data Attributes

The `AuthEntry` component uses data attributes for Playwright targeting:
- `data-auth-entry` — root container
- `data-register-toggle` — tab button to switch to register view
- `data-login-toggle` — tab button to switch to login view
- `data-register-view` — register view container (hidden when login view is active)
- `data-login-view` — login view container (hidden when register view is active)

## Playwright Tests

`frontend/tests/auth-entry-ui.spec.js` covers:
1. Signed-out root shows guest auth UI instead of the old placeholder
2. Guest users can reach both register and login views
3. Register view has a clear primary action to begin passkey flow
4. Login view has a clear primary action to begin passkey flow
5. Signed-out render does not repeatedly fire failing protected requests
