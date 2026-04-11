# Deploy Cookie Contract

This document captures the deployed-origin cookie security contract and the proxy fail-closed routing behavior, as validated by the `deploy-auth-proxy-cookie-contract` feature.

## Cookie Security Attributes (VAL-DEPLOY-004)

On the deployed HTTPS origin (`https://draft.choir-ip.com`), the auth service sets cookies with `AUTH_COOKIE_SECURE=true`, which produces these attributes:

| Cookie | Secure | HttpOnly | SameSite | Domain | Path | MaxAge |
|--------|--------|----------|----------|--------|------|--------|
| `choir_access` | true | true | Lax | (empty/host-only) | `/` | access TTL seconds |
| `choir_refresh` | true | true | Lax | (empty/host-only) | `/auth` | refresh TTL seconds |

### Login and Register Finish

Both `HandleRegisterFinish` and `HandleLoginFinish` call `issueSession`, which calls `setAuthCookies`. The cookies inherit these attributes from the config.

### Renewal (Refresh Rotation)

`HandleSession` calls `tryRefreshRotation` → `rotateRefreshSession` → `setAuthCookies`. The rotated cookies have the same security attributes.

### Logout

`HandleLogout` clears both cookies with `MaxAge=-1`, empty `Value`, and the same `Secure`, `HttpOnly`, `SameSite` attributes. This ensures the clearing `Set-Cookie` headers are accepted by browsers on the HTTPS origin (browsers reject non-Secure clearing cookies on HTTPS).

### Key Design Choices

- **SameSite=Lax** (not Strict): Allows session rehydration from top-level navigation links while preventing CSRF via cross-origin subrequests.
- **No Domain attribute**: Cookies are host-only (bound to the exact host `draft.choir-ip.com`), not shared with subdomains.
- **Refresh cookie Path=/auth**: The refresh token is only sent to `/auth/*` routes, limiting its exposure.

## Proxy Fail-Closed Routing (VAL-DEPLOY-005)

The proxy's `HandleAPI` method enforces auth gating for all `/api/*` routes:

1. **Explicitly-handled protected routes** (`/api/shell/bootstrap`, `/api/ws`): Each has its own handler that validates the access JWT before proceeding.
2. **Catch-all `/api/*` routes**: Auth is checked first. Signed-out callers get 401 JSON; signed-in callers get 404 JSON for unknown routes.
3. **No route returns sandbox data without auth**: Denial responses are plain JSON `{error: "..."}` with no sandbox fields.

### Adding New Protected Routes

When adding a new `/api/*` route (e.g., `/api/agent/task`), add a case to the `HandleAPI` switch statement that calls `HandleProtectedAPI` or a dedicated handler. The catch-all will still auth-gate unknown routes as a safety net.

## Test Coverage

Tests for these contracts live in:
- `internal/auth/handlers_test.go`: `TestDeployedCookieContractOnSessionIssuance`, `TestDeployedCookieContractOnRefreshRotation`, `TestDeployedCookieContractOnLogout`, `TestDeployedOrigin*`
- `internal/proxy/handlers_test.go`: `TestProtectedBootstrapDeniesSignedOut`, `TestProtectedLiveChannelDeniesSignedOut`, `TestAllAPIRoutesDenySignedOutCallers`, `TestUnknownAPIRouteReturns404ForAuthenticatedCaller`, `TestSignedOutCallersNeverSeeSandboxData`

All deployed-origin tests use `deployedHandlerEnv` which mirrors the NixOS production config from `nix/node-b.nix`.
