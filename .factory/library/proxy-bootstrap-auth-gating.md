# Proxy Auth Gating and User Context

This document describes the `internal/proxy` package implementing protected HTTP and WebSocket proxying for `GET /api/shell/bootstrap` and `GET /api/ws`, with auth gating, reverse proxy, user-context injection, and spoofed-header resistance.

## Package Structure

- `internal/proxy/config.go` — `Config` struct and `LoadConfig()` that resolves PROXY_* env vars; `LoadPublicKey()` for auth public key loading
- `internal/proxy/handlers.go` — HTTP handlers for `/api/shell/bootstrap` and `/api/ws` with auth gating, reverse proxy, WS relay, user-context injection, and spoofed-header stripping

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

## Spoofed-Header Resistance (clientIdentityHeaders)

The proxy strips ALL client-supplied identity headers before forwarding to the sandbox. The `clientIdentityHeaders` list in `handlers.go` defines which headers are stripped:

- `X-Authenticated-User`
- `X-User-Id`
- `X-User-Name`
- `X-Forwarded-User`
- `X-Remote-User`
- `X-Auth-User`

After stripping, only the JWT-verified user ID is injected via `X-Authenticated-User`. This prevents any form of client identity spoofing — even if a client sends multiple identity headers, none reach the sandbox.

For the HTTP (bootstrap) path, the reverse proxy Director strips all these headers before forwarding.

For the WebSocket path, the proxy dials the sandbox with a fresh `http.Header{}`, so no client-supplied headers can leak through at all. Only `X-Authenticated-User` is set from the JWT-verified user ID.

## User Context Injection

The proxy uses a two-step process to prevent client identity spoofing on the HTTP path:
1. The handler stores the JWT-verified user ID in `X-Proxy-Trusted-User` header
2. The reverse proxy Director strips ALL client identity headers, then sets `X-Authenticated-User` from `X-Proxy-Trusted-User`

For the WebSocket path:
1. The handler validates the JWT before upgrading
2. The handler dials the sandbox with a fresh `http.Header{}` containing only the trusted `X-Authenticated-User`

This ensures that even if a client sends spoofed identity headers, the sandbox only sees the proxy-verified value.

## Reverse Proxy Behavior (HTTP)

For authenticated requests to `GET /api/shell/bootstrap`:
1. Validates the access JWT from the `choir_access` cookie
2. Strips ALL client-supplied identity headers (spoofing prevention)
3. Injects `X-Authenticated-User` with the JWT-verified user ID
4. Preserves the original public request path, method, and query string
5. Forwards to the sandbox upstream via `httputil.ReverseProxy`
6. Passes through the upstream status code and response body unchanged

## WebSocket Proxy Behavior

For authenticated requests to `GET /api/ws`:
1. Validates the access JWT BEFORE upgrading (no upgrade on invalid auth)
2. Upgrades the client connection to WebSocket
3. Dials the sandbox `/api/ws` endpoint with a fresh header containing only the trusted `X-Authenticated-User`
4. Relays frames bidirectionally between client and sandbox
5. Closes both connections when either side disconnects

## Route Registration

- `GET /api/shell/bootstrap` — `HandleBootstrap` (auth-gated, GET-only)
- `GET /api/ws` — `HandleWS` (auth-gated, bidirectional WS relay)
- `/api/*` — `HandleAPI` (catch-all, returns 404 for unknown API routes)

## Non-2xx Passthrough

The proxy passes through upstream error status codes and response bodies unchanged. For example, a 500 from the sandbox error endpoint is returned as 500 to the client. This is tested in unit tests using `HandleProtectedAPI`.

## Test Coverage

Tests in `internal/proxy/handlers_test.go` and `internal/proxy/config_test.go`:

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

### VAL-PROXY-003 tests (authenticated WS upgrade and relay):
- `TestWSAuthenticatedUpgradesAndRelays` — authenticated WS upgrades, relays, and echoes
- `TestWSAuthenticatedInjectsUserContext` — WS connected message contains JWT-verified user
- `TestWSRelaysMultipleFramesBidirectionally` — multiple frames relay correctly
- `TestWSProxyPreservesSinglePublicEntrypoint` — single /api/ws entrypoint
- `TestWSRelaysBinaryFrames` — binary frames relay correctly
- `TestWSClosePropagates` — close messages propagate between sides

### VAL-PROXY-004 tests (missing/invalid auth cannot open WS):
- `TestWSDeniesMissingAuth` — no cookie denied before upgrade
- `TestWSDeniesInvalidAuth` — bogus JWT denied before upgrade
- `TestWSDeniesExpiredAuth` — expired JWT denied before upgrade
- `TestWSDeniesTamperedAuth` — tampered JWT denied before upgrade
- `TestWSDeniesNonAccessToken` — wrong-scope JWT denied before upgrade
- `TestWSDeniesEmptyCookieValue` — empty cookie denied before upgrade
- `TestWSDeniesWrongSigningKey` — wrong-key JWT denied before upgrade
- `TestWSAuthDenialReturnsJSONWithoutUpgrade` — 401 JSON without WS upgrade

### VAL-PROXY-005 tests (spoofed-header resistance, same sandbox, distinct user context):
- `TestBootstrapTwoDistinctUsersSameSandboxDifferentContext` — two users reach same sandbox_id with different user context
- `TestBootstrapNoStaleIdentityLeakBetweenUsers` — consecutive requests from different users don't leak identity
- `TestBootstrapStripsAdditionalSpoofedIdentityHeaders` — X-User-Id, X-Forwarded-User, etc. are stripped
- `TestWSAuthenticatedTwoDistinctUsersSameSandboxDifferentContext` — two WS users reach same sandbox_id with different user context
- `TestWSNoStaleIdentityLeakBetweenUsers` — consecutive WS connections from different users don't leak identity
- `TestWSSpoofedIdentityHeadersDoNotReachSandbox` — spoofed headers on WS handshake don't reach sandbox
- `TestWSIgnoresClientSuppliedUserContext` — spoofed X-Authenticated-User on WS replaced with JWT identity

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
