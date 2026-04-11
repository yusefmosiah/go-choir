# Placeholder Sandbox Surface

The placeholder sandbox (`cmd/sandbox`, `internal/sandbox`) provides the deterministic upstream needed by the proxy for Milestone 1 validation.

## Endpoints

| Route | Method | Purpose |
| --- | --- | --- |
| `/health` | GET | Standard health check (from shared `internal/server`) |
| `/api/shell/bootstrap` | GET | Shell bootstrap payload with sandbox identity and user context echo |
| `/api/shell/error` | GET | Deliberate 500 response for non-2xx passthrough testing |
| `/api/ws` | GET | WebSocket upgrade with echo and connected message |

## Bootstrap Response (`GET /api/shell/bootstrap`)

Returns JSON with:
- `sandbox_id` — stable identity (from `SANDBOX_ID` env var)
- `user` — value of `X-Authenticated-User` header (injected by proxy, not client)
- `bootstrap` — `"placeholder-shell-v1"` constant
- `path` — echo of request URL path (proves prefix preservation)
- `method` — echo of request method
- `query` — echo of raw query string
- `status_code` — 200

## Error Response (`GET /api/shell/error`)

Returns 500 JSON with `sandbox_id`, `status_code: 500`, and error message. Used by proxy tests to verify non-2xx passthrough behavior.

## WebSocket (`GET /api/ws`)

On upgrade, immediately sends a `connected` message:
```json
{"sandbox_id":"sandbox-dev","user":"...","type":"connected","payload":"websocket channel open"}
```

Then echoes messages with `type: "echo"`:
```json
{"sandbox_id":"sandbox-dev","user":"...","type":"echo","payload":"<original payload>"}
```

The `user` field comes from `X-Authenticated-User` header on the initial HTTP request.

## Configuration

- `SANDBOX_PORT` — listen port (default: 8085)
- `SANDBOX_ID` — stable sandbox identity string (default: `sandbox-dev`)

## Key Design Decisions

- The sandbox trusts the `X-Authenticated-User` header because the proxy (not the client) is the trust boundary for user identity
- The sandbox does NOT validate auth itself; that's the proxy's job
- The WS upgrader allows all origins for the placeholder; the proxy enforces origin policy
- The non-2xx path is separate from bootstrap so proxy tests can exercise error passthrough without affecting normal bootstrap behavior
