# Gateway Provider Boundary

The gateway service (port 8084) is the host-side provider boundary for Mission 3/4. It is the only component that holds real provider credentials (Bedrock, Z.AI, Fireworks) and makes upstream LLM calls.

## Architecture

```
Browser → Proxy (8082) → Sandbox/Runtime (8085) → Gateway (8084) → Bedrock/Z.AI/Fireworks
                                    ↓
                            Uses GatewayClient
                    (Bearer token auth to gateway)
```

The sandbox runtime no longer calls providers directly when the gateway is active. Instead, it uses a `GatewayClient` (in `internal/gateway/client.go`) that routes LLM requests through the gateway.

## Multi-Provider Routing

The gateway supports multi-provider routing via `provider.MultiProvider`. When multiple providers are available, the gateway routes requests based on:

1. **Explicit provider field**: `provider: "fireworks"` routes to the Fireworks provider
2. **Model-based routing**: Model IDs are matched against `provider.SupportedModels()` to determine the correct provider
   - `accounts/fireworks/routers/kimi-k2p5-turbo` → fireworks
   - `glm-5-turbo`, `glm-5.1` → zai
   - `*.anthropic.claude-*` → bedrock
3. **Heuristic fallback**: If no exact match, model string patterns are checked (contains "fireworks", starts with "glm-", contains "claude")
4. **Default provider**: If no provider or model is specified, the first registered provider is used

The gateway entrypoint (`cmd/gateway/main.go`) uses `provider.ResolveAll()` to initialize all providers with available credentials, and creates a `NewMultiHandlerWithRateLimit` handler.

### Supported Models

| Model ID | Provider | Display Name |
|---|---|---|
| `accounts/fireworks/routers/kimi-k2p5-turbo` | fireworks | Kimi K2.5 |
| `glm-5.1` | zai | GLM-5.1 |
| `glm-5-turbo` | zai | GLM-5-Turbo |
| `us.anthropic.claude-haiku-4-5-20251001-v1:0` | bedrock | Claude Haiku 4.5 |
| `us.anthropic.claude-sonnet-4-6` | bedrock | Claude Sonnet 4.6 |
| `us.anthropic.claude-opus-4-6-v1` | bedrock | Claude Opus 4.6 |

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
- `internal/gateway/handlers.go` — HTTP handlers for inference (multi-provider routing) and credential endpoints
- `internal/gateway/client.go` — GatewayClient (provider.Provider) for sandbox-to-gateway calls
- `internal/gateway/ratelimit.go` — Per-sandbox rate limiter
- `internal/gateway/gateway_test.go` — Comprehensive tests (49 test cases including multi-provider routing)
- `internal/provider/provider.go` — Provider implementations (Bedrock, Z.AI, Fireworks), MultiProvider, ResolveAll
- `internal/provider/bridge.go` — BridgeProvider and GatewayBridgeProvider for runtime integration
- `cmd/gateway/main.go` — Gateway service entrypoint (uses ResolveAll for multi-provider)

## Future Work

- SSE streaming support (VAL-LLM-003, VAL-LLM-004) — currently non-streaming only
- Durable credential storage (currently in-memory; needs persistence for production)
- VM lifecycle integration — vmctl should issue/rotate gateway credentials when VMs start/stop
- The sandbox runtime can use GatewayClient instead of direct provider when the gateway is available
