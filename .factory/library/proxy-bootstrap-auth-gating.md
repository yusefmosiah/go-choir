# Proxy Bootstrap Auth Gating

This document describes the `internal/proxy` package implementing protected HTTP proxying for `GET /api/shell/bootstrap`.

## Package Structure

- `internal/proxy/config.go` — `Config` struct and `LoadConfig()` that resolves PROXY_* env vars; `LoadPublicKey()` for auth public key loading
- `internal/proxy/handlers.go` — HTTP handlers for `/api/shell/bootstrap` with auth gating, reverse proxy, and user-context injection

## Configuration (PROXY_* env vars)

| Variable | Default | Description |
|---|---|---|
| `PROXY_PORT` | `8082` | Listen port |
| `PROXY_SANDBOX_URL` | `http://127.0.0.1:8085` | Hardcoded placeholder sandbox URL |
| `PROXY_AUTH_PUBLIC_KEY_PATH` | `/tmp/go-choir-m2/auth-signing-key.pub` | Ed25519 public key for verifying auth-issued access JWTs |

## Auth Gating

The proxy validates the `choir_access` cookie as an Ed25519 JWT signed by the auth service:
- Missing cookie → 401 JSON `{"error":"authentication required"}`
- Invalid/expired/tampered JWT → 401 JSON
- JWT with wrong scope (not "access") → 401 JSON
- JWT signed with wrong key → 401 JSON

The proxy never reaches the sandbox upstream for unauthenticated requests.

## Reverse Proxy Behavior

For authenticated requests to `GET /api/shell/bootstrap`:
1. Validates the access JWT from the `choir_access` cookie
2. Strips any client-supplied `X-Authenticated-User` header (spoofing prevention)
3. Injects `X-Authenticated-User` with the JWT-verified user ID
4. Preserves the original public request path, method, and query string
5. Forwards to the sandbox upstream via `httputil.ReverseProxy`
6. Passes through the upstream status code and response body unchanged

## User Context Injection

The proxy uses a two-step process to prevent client identity spoofing:
1. The handler stores the JWT-verified user ID in `X-Proxy-Trusted-User` header
2. The reverse proxy Director strips any `X-Authenticated-User` from the client, then sets it from `X-Proxy-Trusted-User`

This ensures that even if a client sends `X-Authenticated-User: attacker`, the sandbox only sees the proxy-verified value.

## Route Registration

- `GET /api/shell/bootstrap` — `HandleBootstrap` (auth-gated, GET-only)
- `/api/*` — `HandleAPI` (catch-all, returns 404 for unknown API routes)

## Non-2xx Passthrough

The proxy passes through upstream error status codes and response bodies unchanged. For example, a 500 from the sandbox error endpoint is returned as 500 to the client. This is tested in unit tests using `HandleProtectedAPI`.

## Test Coverage

22 test cases in `internal/proxy/handlers_test.go` and 4 in `internal/proxy/config_test.go`:

### VAL-PROXY-001 tests (missing/invalid auth fails closed):
- `TestBootstrapDeniesMissingAuth` — no cookie returns 401
- `TestBootstrapDeniesInvalidAuth` — bogus JWT returns 401
- `TestBootstrapDeniesExpiredAuth` — expired JWT returns 401
- `TestBootstrapDeniesTamperedAuth` — tampered JWT returns 401
- `TestBootstrapDeniesNonAccessToken` — wrong-scope JWT returns 401
- `TestBootstrapDeniesWrongSigningKey` — wrong-key JWT returns 401
- `TestBootstrapWithEmptyCookieValue` — empty cookie returns 401
- `TestBootstrapProxyDoesNotLeakToSignedOutUsers` — signed-out response has no sandbox data

### VAL-PROXY-002 tests (authenticated passthrough preserves request/response):
- `TestBootstrapAuthenticatedReachesSandbox` — authenticated request reaches sandbox upstream
- `TestBootstrapPreservesPublicRequestPath` — public path `/api/shell/bootstrap` is preserved
- `TestBootstrapPreservesRequestMethod` — GET method is preserved
- `TestBootstrapPreservesQueryString` — query string is preserved
- `TestBootstrapPreservesUpstreamStatus` — upstream 200 passes through
- `TestBootstrapPreservesUpstreamNon2xx` — upstream 500 passes through
- `TestBootstrapInjectsUserContext` — JWT user ID injected as X-Authenticated-User
- `TestBootstrapIgnoresClientSuppliedUserContext` — spoofed header is replaced with JWT identity

### Additional tests:
- `TestBootstrapRejectsNonGet` — POST/PUT/DELETE/PATCH return 405
- `TestHandleAPIReturnsNotFoundForUnknownRoutes` — unknown /api/* returns 404
- `TestBootstrapAuthenticatedReturnsJSONContentType` — content type check
- `TestBootstrapUnauthenticatedReturnsJSONContentType` — auth failure is JSON
- `TestValidateAccessJWTWithWrongKey` — cross-key validation check
- `TestLoadConfigDefaults`, `TestLoadConfigFromEnv`, `TestLoadConfigRejectsEmptyPort` — config tests
- `TestLoadPublicKey`, `TestLoadPublicKeyFromTestKey` — public key loading tests

## cmd/proxy Integration

`cmd/proxy/main.go` loads config, ensures dirs, loads the auth public key, creates a `Handler`, and registers routes on the shared server.
