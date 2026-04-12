# Runtime SSE Streaming

## Overview

SSE streaming flows from provider through gateway, runtime, to browser. The full chain:

```
LLM Provider → provider.Stream() → BridgeProvider.Execute() → EventEmitFunc → EventBus → /api/events SSE → Browser
```

## Key Components

### Provider Layer (`internal/provider/`)
- `Provider.Stream(ctx, req, onChunk)` — streams SSE from upstream providers (Z.AI, Fireworks)
- `BridgeProvider.Execute()` — calls `inner.Stream()` and emits `EventTaskDelta` for each text chunk
- `GatewayBridgeProvider.Execute()` — calls `client.Stream()` through gateway with streaming enabled
- Both bridge providers now set `Stream: true` in the LLM request

### Gateway Layer (`internal/gateway/`)
- `GatewayClient.Stream()` — sends SSE request to gateway, parses SSE response
- `parseGatewaySSE(body, onChunk)` — reads SSE `data:` lines, accumulates response fields
- `handleStreamingInference()` — gateway handler that forwards provider chunks as SSE to HTTP client
- `GatewayCaller` interface now requires `Stream()` method

### Runtime Layer (`internal/runtime/`)
- `executeWithProvider()` and `executeWithToolLoop()` pass through events from providers
- `EventTaskDelta` events are emitted by bridge providers for each streaming chunk
- Events are published on EventBus and streamed via `/api/events` SSE endpoint

## Event Flow

For each text chunk from the provider:
1. Provider calls `onChunk(StreamChunk{Delta: "text"})`
2. Bridge provider's callback emits `EventTaskDelta` via `emit()`
3. Runtime's `emitEvent()` persists and publishes on EventBus
4. `/api/events` handler receives event and writes SSE to browser

## Testing

- `internal/runtime/streaming_test.go` — 8 test cases for streaming provider behavior
- `internal/provider/provider_test.go` — BridgeProvider streaming tests (3 new cases)
- `internal/gateway/gateway_test.go` — GatewayClient.Stream() and parseGatewaySSE tests (5 new cases)

## Important Notes

- The `ToolLoopProvider.CallWithTools()` still uses non-streaming `Call()` for tool-calling iterations (tools need complete responses)
- Only the final text response from `executeWithProvider()` path uses streaming
- Bedrock falls back to non-streaming (binary EventStream, not SSE)
- Z.AI and Fireworks support real SSE streaming
