# Etext Agent Revision Real LLM Testing

## Feature: etext-agent-revision-real-llm

End-to-end tests validating the full agent revision flow with real LLM providers.

### Test File
`internal/runtime/etext_real_llm_test.go`

### Architecture Decision: Avoiding Circular Dependencies

The test file implements a lightweight Anthropic Messages API client (`anthropicClient`) directly instead of importing the `provider` package. This avoids a circular dependency:
- `runtime` package imports would create: `runtime` (test) → `provider` → `runtime` (bridge.go)

The `anthropicClient` struct implements the `runtime.Provider` interface by:
1. Making direct HTTP POST requests to the Anthropic Messages API endpoint
2. Parsing SSE streams for real streaming delta events
3. Setting `task.Result` on the TaskRecord (same pattern as BridgeProvider)

### Provider Resolution Order
1. Z.AI (`ZAI_API_KEY`, model `glm-5-turbo`, base URL `https://api.z.ai/api/anthropic`)
2. Fireworks (`FIREWORKS_API_KEY`, model `accounts/fireworks/routers/kimi-k2p5-turbo`, base URL `https://api.fireworks.ai/inference`)
3. Skip if neither credential is configured

### Test Cases

| Test | Validates | Assertion |
|------|-----------|-----------|
| TestEtextAgentRevisionRealLLM | Full flow: create doc → submit prompt → LLM call → revision → history | VAL-LLM-013, VAL-LLM-014 |
| TestEtextAgentRevisionRealLLMCodeGeneration | Code generation produces code-like output | VAL-LLM-015 |
| TestEtextAgentRevisionRealLLMEventsEmitted | Lifecycle events (submitted, started, completed, etext-specific) | VAL-LLM-008 |
| TestEtextAgentRevisionRealLLMMutationIdempotency | Retry returns same task ID, no duplicate revisions | VAL-CROSS-122 |
| TestEtextAgentRevisionRealLLMStreamingDeltas | Streaming delta events contain real text | VAL-LLM-008 |
| TestEtextAgentRevisionRealLLMProviderMetadata | Task metadata has type=etext_agent_revision, doc_id | Task metadata |
| TestEtextAgentRevisionRealLLMFullHistory | Multiple user+agent edits produce correct attribution order | VAL-CROSS-119 |
| TestRealLLMProviderInfo | Reports which provider is available | Info only |

### Running Tests

```bash
# Without credentials (tests skip):
go test ./internal/runtime/... -run "TestEtextAgentRevisionReal" -v

# With Z.AI credentials:
ZAI_API_KEY=your-key go test ./internal/runtime/... -run "TestEtextAgentRevisionReal" -v -timeout 300s

# With Fireworks credentials:
FIREWORKS_API_KEY=your-key go test ./internal/runtime/... -run "TestEtextAgentRevisionReal" -v -timeout 300s
```

### Key Design Patterns
- Tests use `t.Skip()` when no credentials are available, ensuring CI passes without keys
- Each test creates a fresh database in `/tmp/go-choir-m2-etext-real-llm-test/`
- 60-second timeout per test for real LLM calls (can be slow on first request)
- SSE parsing handles `content_block_delta` events for streaming verification
