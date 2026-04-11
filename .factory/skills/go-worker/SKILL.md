---
name: go-worker
description: Builds Go services, runtime/store packages, gateway/vmctl logic, and backend APIs for Mission 3
---

# Go Worker

NOTE: Startup and cleanup are handled by `worker-base`. This skill defines the WORK PROCEDURE.

## When to Use This Skill

Use for Mission 3 backend features such as:
- Node B deploy-readiness fixes that require Go service changes
- proxy/auth contract changes that preserve the public browser surface
- sandbox runtime APIs, supervision, events, and persistence
- Dolt-backed store/types/scheduler/appagent backend work
- gateway provider access and caller authentication
- vmctl ownership registry and VM lifecycle APIs
- admin/status APIs exposed by Go services

## Required Skills

None

## Work Procedure

1. **Read the feature and contract first**
   - Read the feature description, `mission.md`, `AGENTS.md`, `validation-contract.md`, `.factory/library/architecture.md`, `.factory/library/environment.md`, `.factory/library/user-testing.md`, `.factory/library/mission-3-references.md`, and `.factory/services.yaml`.
   - Identify exactly which contract assertions this feature fulfills.
   - If the current top milestone is still `deploy-readiness`, treat restoring `https://draft.choir-ip.com` as a hard execution gate for later work rather than optional cleanup.

2. **Read the existing Go surface before editing**
   - Inspect the relevant `cmd/*`, `internal/*`, `go.mod`, and Nix/runtime config touched by the feature.
   - Reuse existing `internal/server` patterns where sensible.
   - Preserve browser-facing route invariants unless the feature explicitly owns a validated contract change.
   - For runtime/gateway/vmctl work, keep boundaries explicit: proxy is the browser ingress, gateway owns provider credential use, vmctl owns VM lifecycle, and appagents own canonical writes.

3. **Write failing tests first**
   - Add or extend `_test.go` coverage before implementation.
   - Cover the happy path plus the contract-critical denial, recovery, and boundary cases for the feature.
   - Confirm the new tests fail first before writing production code.

4. **Implement the smallest correct slice**
   - Write the minimum production code to make the tests pass.
   - Match Mission 3 decisions:
     - no CLI subprocess loop inside sandbox
     - no adapter-wrapper process around the runtime loop
     - cookie-backed same-origin auth remains the browser trust model
     - users and appagents are peer canonical editors
     - subordinate workers never become canonical authors
     - Bedrock and/or Z.AI are the first required real-provider paths
   - Reuse already-adopted libraries and patterns where possible; add new dependencies only when the feature requires them and after confirming they are appropriate for the repo.

5. **Verify the contract surface directly**
   - Use `curl` for HTTP/API contracts, including auth denial, same-origin proxy paths, status/event surfaces, and operator APIs.
   - If the feature changes a browser-visible contract, run an `agent-browser` smoke check against the affected public flow and add focused Playwright coverage when the behavior is stateful or regression-prone.
   - When the feature changes runtime/gateway/vmctl behavior, capture evidence that proves the expected boundary: no auth bypass, no client-supplied identity trust, no direct-browser provider calls, no guest-side secret leakage.
   - When the feature touches persisted state, prove the resulting state survives the restart/reload scope named in the feature.
   - When the feature affects task submission or canonical mutation, explicitly check that renewal/retry/recovery does not duplicate the effect.
   - Do not leave long-running processes behind; if you start a service, you must stop it.

6. **Run validators before handoff**
   - `go test ./... -count=1 -p 4`
   - `go vet ./...`
   - `go build ./cmd/...`
   - `cd frontend && pnpm build` when the feature changes routes or payloads consumed by the frontend
   - run any focused additional command required by the feature (for example `nix flake show .` for Nix-coupled backend changes)

7. **Return a concrete handoff**
   - Record the exact tests you added, the focused verification commands you ran, and the curl/log/browser evidence that proves the contract behavior.
   - If anything remained partial, say exactly what and why.

## Example Handoff

```json
{
  "salientSummary": "Implemented authenticated runtime task/status/event APIs with stable task handles and Dolt-backed task recovery. Verified signed-out requests fail closed, signed-in requests receive stable handles, and status remains recoverable after restarting the sandbox process.",
  "whatWasImplemented": "Added the runtime task submission, status lookup, and event-stream backend for the Mission 3 host-process runtime, including stable task IDs, persisted task state, and restart-safe recovery behavior. Wired the proxy-facing same-origin contract so `/api/agent/task`, `/api/agent/status`, and `/api/events` all stay behind cookie-backed auth instead of exposing direct sandbox ports or alternate hosts.",
  "whatWasLeftUndone": "Browser prompt UI and agent-browser verification of the shell prompt surface belong to the frontend feature that consumes these APIs.",
  "verification": {
    "commandsRun": [
      {
        "command": "go test ./... -count=1 -p 4",
        "exitCode": 0,
        "observation": "Runtime API, persistence, and restart-recovery tests pass."
      },
      {
        "command": "go vet ./...",
        "exitCode": 0,
        "observation": "No vet findings."
      },
      {
        "command": "go build ./cmd/...",
        "exitCode": 0,
        "observation": "All service binaries still compile."
      }
    ],
    "interactiveChecks": [
      {
        "action": "POSTed to `/api/agent/task`, polled `/api/agent/status`, restarted the sandbox, and polled status again by the same task handle",
        "observed": "Signed-out submission failed with 401, signed-in submission returned a stable task ID, and post-restart status lookup still returned the accepted task instead of losing it."
      },
      {
        "action": "Opened `/api/events` with valid auth and then repeated with invalid auth",
        "observed": "The signed-in stream emitted incremental task lifecycle events; the invalid-auth attempt failed closed."
      }
    ]
  },
  "tests": {
    "added": [
      {
        "file": "internal/runtime/api_test.go",
        "cases": [
          {
            "name": "TestTaskSubmissionReturnsStableHandle",
            "verifies": "Accepted runtime work returns a stable task ID and initial lifecycle state."
          },
          {
            "name": "TestStatusLookupSurvivesRestart",
            "verifies": "Accepted work remains recoverable after sandbox restart."
          },
          {
            "name": "TestEventsRequireAuth",
            "verifies": "Runtime event streaming fails closed without valid auth."
          }
        ]
      }
    ]
  },
  "discoveredIssues": []
}
```

## When to Return to Orchestrator

- The feature requires Nix/Caddy/systemd or Node B secret-management changes beyond a small local adjustment
- The feature needs a new mission boundary, new public route, or provider/VM exposure model not already captured in shared state
- Real-provider validation is blocked by missing credentials or missing Node B/Linux capabilities
- The feature would require a local auth bypass, direct-browser access to service ports, or guest-side provider credentials
- A contract-critical verification path cannot be exercised with the current services or test setup
