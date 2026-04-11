# Provider Bridge

The `internal/provider` package implements the Bedrock and Z.AI provider bridges that connect the runtime engine to real LLM backends. This is the first real-provider path for Mission 3.

## Architecture

```
Runtime Engine (internal/runtime)
    ↓ uses runtime.Provider interface
Bridge Provider (internal/provider.BridgeProvider)
    ↓ adapts to LLM provider.Provider interface
Concrete Provider (BedrockProvider or ZAIProvider)
    ↓ makes HTTP calls
Upstream LLM API (Bedrock invoke / Z.AI /v1/messages)
```

The BridgeProvider sits between the runtime engine and the LLM provider, translating between the runtime's TaskRecord/EventEmitFunc model and the provider package's LLMRequest/LLMResponse model.

## Credential Resolution

`provider.ResolveProvider()` checks environment variables in order:
1. If `AWS_BEARER_TOKEN_BEDROCK` is set → create BedrockProvider
2. Else if `ZAI_API_KEY` is set → create ZAIProvider
3. Else → return nil (runtime falls back to StubProvider)

The sandbox `cmd/sandbox/main.go` wires this up: if `ResolveProvider()` returns a real provider, it wraps it in a `BridgeProvider`; otherwise it uses the `StubProvider`.

## Bedrock Details

- Endpoint: `https://bedrock-runtime.{region}.amazonaws.com/model/{model}/invoke`
- Auth: Bearer token via `Authorization: Bearer {AWS_BEARER_TOKEN_BEDROCK}`
- No SigV4 signing; uses the bearer token pattern from choiros-rs
- Forces non-streaming (Bedrock streaming uses binary EventStream, not SSE)
- Model ID goes in the URL path, not the body
- Anthropic version header: `bedrock-2023-05-31`
- System prompt includes `cache_control: {"type": "ephemeral"}` for prompt caching

## Z.AI Details

- Endpoint: `https://api.z.ai/api/anthropic/v1/messages`
- Auth: API key via `x-api-key: {ZAI_API_KEY}`
- Anthropic-compatible Messages API format
- Anthropic version header: `2023-06-01`
- Default model: `glm-4.7`

## Observability

All provider events include a `"real": "true"` marker that distinguishes them from stub provider events. The health endpoint (`GET /health`) reports `active_provider` as "bedrock", "zai", or "stub".

Provider interactions are logged with redacted model IDs (e.g., `us.anthropic.clau***v1:0`) to provide enough context for debugging without revealing full model identifiers.

## Error Sanitization

When providers return HTTP errors, the response body is drained and discarded but never included in the error message. This prevents leaking provider details, credentials, or internal error messages. Error messages use the format `{provider}: status {code} (sanitized)`.

## Gateway Boundary

This feature preserves the later gateway boundary: the runtime uses provider credentials internally but never exposes them to the browser or sandbox-visible APIs. The full gateway milestone will move credential injection behind an explicit gateway service boundary with per-sandbox identity, rate limiting, and credential rotation.

## Future Work

- Streaming support (SSE for Z.AI, EventStream for Bedrock)
- Tool calling / agentic loop support
- Per-sandbox identity for gateway authentication
- Gateway-mediated credential injection instead of direct env vars
- Provider fallback chain (e.g., try Z.AI if Bedrock fails)
