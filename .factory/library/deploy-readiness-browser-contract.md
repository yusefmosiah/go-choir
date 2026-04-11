# Deploy-Readiness Browser Auth/Shell Contract

This document records the verified state of the public browser auth/shell journey on `draft.choir-ip.com` as of the deploy-readiness milestone.

## Verified Assertions

All 9 validation contract assertions assigned to `deploy-public-auth-shell-browser-contract` have been verified:

### VAL-DEPLOY-002: Signed-out visitors see guest auth on the public origin
- **Verified via**: agent-browser against `https://draft.choir-ip.com` + Playwright `deployed-origin-auth-shell.spec.js`
- **Observable behavior**: Guest auth entry UI visible with Register/Sign In toggles; no shell visible; no protected requests fired while signed out

### VAL-CROSS-101: First-time registration lands in the authenticated shell
- **Verified via**: Playwright `deploy-readiness-browser-contract.spec.js` against localhost
- **Observable behavior**: Passkey registration → cookie-backed session → shell with user display, bootstrap data, and live channel connected

### VAL-CROSS-102: Returning login lands in the authenticated shell
- **Verified via**: Playwright `deploy-readiness-browser-contract.spec.js` against localhost
- **Observable behavior**: Logout → login from signed-out state → shell with correct user, bootstrap data, live channel

### VAL-CROSS-103: Protected shell transport uses cookie-backed auth only
- **Verified via**: Playwright `deploy-readiness-browser-contract.spec.js` and `deployed-origin-auth-shell.spec.js`
- **Observable behavior**: No auth tokens in localStorage/sessionStorage; no Authorization header on protected requests; no direct service port calls; all requests same-origin

### VAL-CROSS-104: Expired access renews without a new passkey ceremony
- **Verified via**: Playwright `deploy-readiness-browser-contract.spec.js` against localhost
- **Observable behavior**: Access cookie removed → reload triggers refresh rotation via GET /auth/session → shell rehydrates with same user, bootstrap data, and live channel

### VAL-CROSS-105: Reload and new-tab restart rehydrate from server-backed auth state
- **Verified via**: Playwright `deploy-readiness-browser-contract.spec.js` against localhost
- **Observable behavior**: Hard reload → shell rehydrates with user, bootstrap, live channel; new tab → same rehydration

### VAL-CROSS-106: Logout revokes shell and all protected live surfaces
- **Verified via**: Playwright `deploy-readiness-browser-contract.spec.js` against localhost
- **Observable behavior**: Logout → live channel torn down; protected bootstrap returns 401; WebSocket cannot reconnect; refresh does not resurrect shell

### VAL-CROSS-107: User switch does not leak stale shell state
- **Verified via**: Playwright `deploy-readiness-browser-contract.spec.js` against localhost
- **Observable behavior**: User A logout → User B login shows only User B's state; no User A bootstrap data or live channel leakage

### VAL-CROSS-108: Failed renewal falls back cleanly to guest state
- **Verified via**: Playwright `deploy-readiness-browser-contract.spec.js` against localhost
- **Observable behavior**: Both cookies removed → reload shows guest auth UI; in-shell refresh with no cookies → falls back to guest state; no stale live channel

## Deployed Origin State

As of the commit that adds these tests:
- `https://draft.choir-ip.com/` serves the real Svelte SPA with built-asset references
- `/auth/session` returns `{"authenticated":false}` for signed-out visitors
- `/api/shell/bootstrap` returns 401 with `{"error":"authentication required"}` for signed-out visitors
- No internal service ports are publicly reachable
- Guest auth UI is interactive (register/login toggles work)

## Test Files

| File | Scope | Assertions |
| --- | --- | --- |
| `deploy-readiness-browser-contract.spec.js` | Local service stack (localhost:4173) | VAL-DEPLOY-002, VAL-CROSS-101–108 |
| `deployed-origin-auth-shell.spec.js` | Deployed origin (draft.choir-ip.com) | VAL-DEPLOY-002, VAL-CROSS-103 |

## Notes for Future Workers

- The deployed-origin tests only cover signed-out behavior (no passkey ceremonies) because the Playwright virtual authenticator cannot automate WebAuthn against `https://draft.choir-ip.com`. Full passkey ceremony coverage remains at the localhost level.
- If the deployed origin starts serving a placeholder or goes offline, the `deployed-origin-auth-shell.spec.js` tests will fail. This is by design — they serve as a deployed-surface health check.
- Cookie Secure flag is `false` on localhost and `true` on the deployed origin (controlled by `AUTH_COOKIE_SECURE` env var). The Playwright tests at localhost verify HttpOnly and SameSite attributes; deployed Secure flag verification requires curl or agent-browser inspection of Set-Cookie headers.
