---
name: go-worker
description: Implements Go backend services, APIs, store layer, and infrastructure for go-choir
---

# Go Worker

NOTE: Startup and cleanup are handled by `worker-base`. This skill defines the WORK PROCEDURE.

## When to Use This Skill

Features that involve Go backend code: new API endpoints, database schema changes, store layer extensions, WebSocket handlers, PTY management, or service configuration changes.

## Required Skills

- `agent-browser` — for verifying API endpoints via curl through the proxy. Not always needed for pure backend features.

## Work Procedure

1. **Read context:** Read `mission.md` and `AGENTS.md` for mission boundaries and coding conventions. Read `.factory/library/architecture.md` for system design. Read relevant `.factory/library/*.md` files for the specific area.

2. **Write tests first (red):** Create Go test functions covering the feature's expected behavior. Tests must fail before implementation begins. Follow existing test patterns in `internal/`.

3. **Implement (green):** Build the Go code to make tests pass. Follow existing patterns:
   - Store methods in `internal/store/store.go` (extend existing schema)
   - API handlers in `cmd/sandbox/` (following existing handler patterns)
   - Auth gating via proxy (all `/api/*` endpoints are proxied)
   - Use `crypto/aes` and `crypto/cipher` for AES-GCM encryption (stdlib only)

4. **Run validators:**
   ```bash
   go test ./... -count=1 -p 4
   go vet ./...
   ```

5. **API verification:** If the feature adds HTTP endpoints, verify them via curl through the proxy:
   ```bash
   # Example: verify new endpoint through proxy
   curl -sf http://localhost:4173/api/files
   ```

6. **Schema migration:** If adding new database tables, ensure the schema is created in the store initialization (existing pattern: `InitDB` in `internal/store/store.go`). Do NOT use a migration framework — the existing pattern creates tables on startup.

## Example Handoff

```json
{
  "salientSummary": "Implemented file browser backend with 4 CRUD endpoints (GET/POST/DELETE /api/files, GET /api/files/{path}). Added file browsing with sandbox root directory, breadcrumb navigation support, and proper auth gating. All 12 Go tests passing.",
  "whatWasImplemented": "File handler in cmd/sandbox/files.go: HandleListFiles (GET /api/files), HandleGetFile (GET /api/files/{path}), HandleCreateDirectory (POST /api/files/{path}), HandleDeleteFile (DELETE /api/files/{path}). Store methods in internal/store/store.go for file operations. Routes registered in cmd/sandbox/main.go.",
  "whatWasLeftUndone": "",
  "verification": {
    "commandsRun": [
      {"command": "go test ./internal/store/... -count=1 -v -run TestFile", "exitCode": 0, "observation": "8 file store tests passing"},
      {"command": "go test ./... -count=1 -p 4", "exitCode": 0, "observation": "All tests passing including new file tests"},
      {"command": "go vet ./...", "exitCode": 0, "observation": "No vet warnings"}
    ],
    "interactiveChecks": [
      {"action": "curl -sf http://localhost:4173/api/files (with auth cookies)", "observed": "200 response with JSON array of files"},
      {"action": "curl -X POST http://localhost:4173/api/files/test-folder", "observed": "201 response, folder created"},
      {"action": "curl -sf http://localhost:4173/api/files again", "observed": "200 response includes test-folder"}
    ],
    "tests": {
      "added": [
        {"file": "internal/store/files_test.go", "cases": [
          {"name": "TestListRootDirectory", "verifies": "File listing returns entries"},
          {"name": "TestCreateDirectory", "verifies": "Directory creation persists"},
          {"name": "TestDeleteFile", "verifies": "File deletion works"},
          {"name": "TestNonexistentPathReturns404", "verifies": "Error handling for missing paths"}
        ]}
      ]
    }
  },
  "discoveredIssues": []
}
```

## When to Return to Orchestrator

- Feature requires frontend components that don't exist yet
- Database schema change would break existing functionality
- External dependency needed that isn't in go.mod
