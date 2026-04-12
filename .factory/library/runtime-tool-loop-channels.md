# Runtime Tool-Calling Loop and Channels

## Overview

The tool-calling loop and channel plumbing implement the real Mission 3 runtime execution model, adapted from the Cogent native loop/tools/channel references. These components enable:

1. **Tool-calling loop** — Runtime tasks execute through a real loop that handles `tool_use` stop reasons from the LLM, invokes registered Go function-call tools, and feeds results back for the next iteration.
2. **Tool registry** — Tools are registered as Go function calls (not CLI subprocesses), with LLM-facing schema metadata for discovery.
3. **Channel/message plumbing** — In-process agent channels for appagent and worker coordination, with cursor-based read and blocking wait semantics.

## Key Files

| File | Package | Purpose |
|------|---------|---------|
| `internal/runtime/tools.go` | runtime | ToolFunc, Tool, ToolRegistry, executeTools |
| `internal/runtime/toolloop.go` | runtime | ToolLoopProvider, RunToolLoop, conversation builders |
| `internal/runtime/channels.go` | runtime | AgentChannel, ChannelManager, Runtime channel methods |
| `internal/types/task.go` | types | ToolCall, ToolResult, ChannelMessage, new EventKinds |
| `internal/events/bus.go` | events | ActorTool, ActorChannel, CauseToolExecution, CauseChannelMessage |
| `internal/provider/bridge.go` | provider | BridgeProvider.CallWithTools (ToolLoopProvider impl) |

## Design Decisions

### No CLI subprocess loop
Tools are Go function calls (`ToolFunc = func(ctx, args) (string, error)`), not CLI subprocess invocations. This matches the mission constraint that the runtime loop runs as direct goroutines.

### No adapter-wrapper process
The `ToolLoopProvider` interface separates tool-loop orchestration (owned by the runtime) from LLM API mechanics (owned by the provider). The `toolLoopAdapter` wraps basic `Provider` implementations for compatibility, but it's not an external process wrapper.

### Tool discovery through system prompt
The `ToolRegistry.Catalog()` method generates a compact tool listing for inclusion in the system prompt. The LLM discovers tools by reading the catalog, not through separate schema negotiation. This simplifies the loop compared to Cogent's core/activated tool distinction.

### Observable tool execution
Tool invocations emit `tool.invoked` and `tool.result` events through the existing event bus. These events are persisted and streamed via `/api/events`, making tool-driven task progress observable by the appagent and browser features.

### Channel cursor model
Agent channels use a cursor-based read model adapted from Cogent. Messages are append-only with sequential cursor positions. Consumers read incrementally with `ReadSince(cursor)` or block with `Wait(ctx, cursor)`.

### Iteration limit
The tool-calling loop has a hard limit of 25 iterations to prevent infinite loops. If the LLM keeps requesting tool use without reaching `end_turn`, the loop exits with an error.

## Runtime Integration

When a `ToolRegistry` is configured on the Runtime (via `WithToolRegistry` option), the `executeTask` method uses the tool-calling loop path (`executeWithToolLoop`). When no registry is configured, it falls back to the simple `Provider.Execute` path (`executeWithProvider`).

The `BridgeProvider` now implements `ToolLoopProvider` via `CallWithTools`, enabling the tool-calling loop to work with real Bedrock/Z.AI providers.

## Built-in Tools

### file_read (Mission 4, LLM Validation milestone)

The `file_read` tool is defined in `internal/runtime/toolloopvalidation_test.go` as `fileReadTool(baseDir)`. It reads a file from a base directory and returns its contents. This was the first real tool validated end-to-end through the tool-calling loop.

**Usage in tests:**
```go
registry := NewToolRegistry()
registry.Register(fileReadTool(t.TempDir()))
```

**Validates:** VAL-LLM-010 (tool_use response), VAL-LLM-011 (tool result fed back), VAL-LLM-012 (multi-tool sequential).

**Key test file:** `internal/runtime/toolloopvalidation_test.go` — contains `TestToolLoop*` tests covering:
- Tool registration and schema generation
- Single tool execution with file I/O
- Tool error handling (non-existent files)
- Full runtime integration (SubmitTask → tool_use → file_read → result)
- Tool result message verification (conversation history inspection)
- Multi-tool sequential execution (read multiple files)
- Event emission verification (tool.invoked, tool.result, task.progress)

## Future Work

- The `extractToolCalls` function in `bridge.go` returns nil because the provider package's `LLMResponse` type doesn't yet carry structured tool call data. When the provider package is enhanced to parse `tool_use` content blocks from Anthropic/Bedrock responses, this function should extract them.
- Per-tool rate limiting and access control belong in the gateway milestone.
- The channel manager is in-process only; durability comes from the runtime's event persistence if needed.
