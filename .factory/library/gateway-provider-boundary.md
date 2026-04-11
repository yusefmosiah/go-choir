# Gateway Provider Boundary

The gateway service (port 8084) is the host-side provider boundary for Mission 3. It is the only component that holds real provider credentials (Bedrock, Z.AI) and makes upstream LLM calls.

## Architecture

```
Browser → Proxy (8082) → Sandbox/Runtime (8085) → Gateway (8084) → Bedrock/Z.AI
                                    ↓
                            Uses GatewayClient
                    (Bearer token auth to gateway)
```

The sandbox runtime no longer calls providers directly when the gateway is active. Instead, it uses a `GatewayClient` (in `internal/gateway/client.go`) that routes LLM requests through the gateway.

## Key Invariants

1. **Provider credentials stay host-side** — never in browser responses, sandbox-visible APIs, guest environment, guest files, or process args (VAL-GATEWAY-004).
2. **Browser callers denied** — the proxy denies all `/provider/*` routes with 403 (VAL-GATEWAY-002).
3. **Gateway denies unauthenticated callers** — requires valid sandbox Bearer token (VAL-GATEWAY-003).
4. **Errors sanitized** — upstream failures return bounded, sanitized messages without credentials, auth headers, or stack traces (VAL-GATEWAY-007).
5. **Stale credentials invalidated** — revocation, rotation, and expiry all prevent further provider access (VAL-GATEWAY-008).

## Sandbox Identity and Credentials

The gateway manages per-sandbox credentials through an in-process `IdentityRegistry`:

- **Issue**: `POST /provider/v1/credentials/issue` (localhost only) — issues a `sandboxID:token` credential
- **Revoke**: `POST /provider/v1/credentials/revoke` (localhost only) — revokes a credential
- **Rotate**: `POST /provider/v1/credentials/rotate` (localhost only) — revokes old and issues new

Tokens are SHA-256 hashed at rest. The raw token is returned only once at issuance time.

## Gateway Routes

| Route | Method | Auth | Purpose |
|---|---|---|---|
| `/health` | GET | None | Service health |
| `/provider/v1/inference` | POST | Sandbox Bearer | LLM inference |
| `/provider/v1/credentials/issue` | POST | Localhost | Issue sandbox credential |
| `/provider/v1/credentials/revoke` | POST | Localhost | Revoke credential |
| `/provider/v1/credentials/rotate` | POST | Localhost | Rotate credential |

## Files

- `internal/gateway/config.go` — Config, IdentityRegistry, credential management
- `internal/gateway/handlers.go` — HTTP handlers for inference and credential endpoints
- `internal/gateway/client.go` — GatewayClient (provider.Provider) for sandbox-to-gateway calls
- `internal/gateway/gateway_test.go` — Comprehensive tests (35 test cases)
- `cmd/gateway/main.go` — Gateway service entrypoint

## Future Work

- Per-sandbox rate limiting (VAL-GATEWAY-005) — next feature
- Durable credential storage (currently in-memory; needs persistence for production)
- VM lifecycle integration — vmctl should issue/rotate gateway credentials when VMs start/stop
- The sandbox runtime can use GatewayClient instead of direct provider when the gateway is available
