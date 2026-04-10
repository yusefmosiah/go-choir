---
name: go-worker
description: Builds Go services, internal packages, tests, and frontend scaffold
---

# Go Worker

NOTE: Startup and cleanup are handled by `worker-base`. This skill defines the WORK PROCEDURE.

## When to Use This Skill

Features that involve writing Go code (services, handlers, internal packages, tests) or scaffolding the Svelte frontend project.

## Required Skills

None

## Work Procedure

1. **Read the feature description** carefully. Understand preconditions, expected behavior, and verification steps.

2. **Read existing code** before writing anything. Check `internal/` for shared packages, `cmd/` for service entry points, `go.mod` for dependencies. Match existing patterns.

3. **Write tests first** (TDD):
   - Create `_test.go` files alongside the code being tested.
   - Write failing tests that cover the expected behavior described in the feature.
   - Run `go test ./...` to confirm they fail (red).

4. **Implement the code**:
   - Write the minimum code to make tests pass (green).
   - Use stdlib (`net/http`, `encoding/json`, `os/signal`) — no external dependencies for Mission 1.
   - For shared code (e.g., server setup), put it in `internal/server/`.
   - For service-specific code, put it in the service's `cmd/` directory or a dedicated `internal/` subpackage.

5. **For Svelte scaffold features**:
   - Use `pnpm create` or manual scaffolding in a `frontend/` directory.
   - Ensure `pnpm install && pnpm build` produces static output with `index.html`.
   - The placeholder page must contain "go-choir" text.

6. **Run all checks**:
   - `go vet ./...` — must pass with no warnings
   - `go test ./...` — must pass with all tests green
   - `go build ./cmd/...` — must compile all 5 binaries
   - For frontend: `cd frontend && pnpm build` — must produce output

7. **Manual verification**:
   - Start each service binary, curl its `/health` endpoint, confirm the JSON response.
   - Test graceful shutdown: start service, send SIGTERM, verify clean exit.
   - Test port configuration: start with custom `*_PORT` env var, verify it listens on that port.
   - Record each manual check in `interactiveChecks`.

8. **Commit** with a descriptive message.

## Example Handoff

```json
{
  "salientSummary": "Implemented shared internal/server package with health handler and graceful shutdown. All 5 service binaries (auth, proxy, vmctl, gateway, sandbox) now start HTTP servers on configurable ports and respond to GET /health with JSON. Wrote 12 tests covering health response, port config, and shutdown. All pass.",
  "whatWasImplemented": "internal/server/ package with NewServer(), health handler, graceful shutdown. Updated all 5 cmd/*/main.go to use it. Each service responds to GET /health with {\"status\":\"ok\",\"service\":\"<name>\"}. Port configurable via AUTH_PORT, PROXY_PORT, etc. with defaults 8081-8085.",
  "whatWasLeftUndone": "",
  "verification": {
    "commandsRun": [
      {"command": "go vet ./...", "exitCode": 0, "observation": "No warnings"},
      {"command": "go test ./...", "exitCode": 0, "observation": "12 tests pass across 2 packages"},
      {"command": "go build ./cmd/...", "exitCode": 0, "observation": "All 5 binaries compile"}
    ],
    "interactiveChecks": [
      {"action": "Started auth binary, curled localhost:8081/health", "observed": "200 OK, {\"status\":\"ok\",\"service\":\"auth\"}"},
      {"action": "Sent SIGTERM to auth process", "observed": "Process exited with code 0, no error output"},
      {"action": "Started auth with AUTH_PORT=9999, curled localhost:9999/health", "observed": "200 OK on custom port"}
    ]
  },
  "tests": {
    "added": [
      {"file": "internal/server/server_test.go", "cases": [
        {"name": "TestHealthHandler", "verifies": "Returns 200 with correct JSON"},
        {"name": "TestHealthHandlerServiceName", "verifies": "JSON includes the configured service name"},
        {"name": "TestServerStartAndShutdown", "verifies": "Server starts, accepts requests, shuts down cleanly"},
        {"name": "TestPortFromEnv", "verifies": "Server reads port from environment variable"},
        {"name": "TestPortDefault", "verifies": "Server uses default port when env var is unset"}
      ]}
    ]
  },
  "discoveredIssues": []
}
```

## When to Return to Orchestrator

- Feature requires external Go dependencies not yet in go.mod
- Feature requires NixOS configuration changes (use infra-worker)
- Existing code patterns are unclear or contradictory
- Tests reveal a bug in shared infrastructure
