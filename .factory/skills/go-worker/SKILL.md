---
name: go-worker
description: Builds Go auth/proxy/sandbox services, internal packages, and tests for Mission 2 Milestone 1
---

# Go Worker

NOTE: Startup and cleanup are handled by `worker-base`. This skill defines the WORK PROCEDURE.

## When to Use This Skill

Use for Go features in Mission 2 Milestone 1:
- auth handlers, WebAuthn flows, session/refresh logic, SQLite-backed storage
- proxy auth gating, HTTP/WS forwarding, user-context injection
- placeholder sandbox HTTP/WS endpoints used by the shell
- shared internal packages, config loading, and Go tests

## Required Skills

None

## Work Procedure

1. **Read the feature and contract first**
   - Read the feature description, `validation-contract.md`, `.factory/library/architecture.md`, `.factory/library/environment.md`, and `.factory/services.yaml`.
   - Identify exactly which contract assertions this feature fulfills.

2. **Read the existing Go surface before editing**
   - Inspect the relevant `cmd/*`, `internal/*`, and `go.mod` files.
   - Reuse the shared `internal/server` patterns where sensible.
   - Keep route names aligned with the mission contract: `/auth/*`, `GET /api/shell/bootstrap`, `GET /api/ws`.

3. **Write failing tests first**
   - Add or extend `_test.go` coverage before implementation.
   - Cover happy path plus the contract-critical failure cases for the feature.
   - Confirm the new tests fail first before writing production code.

4. **Implement the smallest correct slice**
   - Write the minimum production code to make the tests pass.
   - Match Mission 2 decisions:
     - WebAuthn bound to `draft.choir-ip.com` for deployed acceptance
     - no local auth bypass
     - cookie-backed auth state
     - short-lived access + rotating refresh state
     - proxy trusts its own verified auth context, not client-supplied identity headers
   - Use the current planned stack when needed: stdlib `net/http`, `github.com/go-webauthn/webauthn`, `github.com/golang-jwt/jwt/v5`, and `modernc.org/sqlite`.

5. **Verify the contract surface directly**
   - For auth routes, use `curl` to check JSON shape, cookies, and status codes.
   - For proxy/bootstrap routes, verify the concrete public paths in the contract.
   - For WebSocket features, prove the live channel opens/closes as required and record the observed behavior.
   - Do not leave long-running processes behind; if you start a service, you must stop it.

6. **Run validators before handoff**
   - `go test ./... -count=1 -p 4`
   - `go vet ./...`
   - `go build ./cmd/...`
   - If the feature changes public route contracts consumed by the frontend, also run `cd frontend && pnpm build`

7. **Return a concrete handoff**
   - Record the exact failing tests you added, the focused verification commands you ran, and the interactive or curl checks that prove the contract behavior.
   - If anything remained partial, say exactly what and why.

## Example Handoff

```json
{
  "salientSummary": "Implemented auth register/login begin routes plus signed-out and signed-in `/auth/session` behavior. Added SQLite-backed challenge/session storage scaffolding and wired WebAuthn option generation for the configured RP. Verified malformed begin payloads return JSON 4xx and valid begin payloads return challenge data bound to the expected RP.",
  "whatWasImplemented": "Added `internal/auth` configuration and storage primitives, auth HTTP handlers for `/auth/register/begin`, `/auth/login/begin`, and `/auth/session`, and tests covering malformed payload rejection, RP-bound WebAuthn option generation, and signed-out vs signed-in session inspection behavior. Kept route names aligned with the public `/auth/*` contract and matched the cookie-backed session model expected by the frontend.",
  "whatWasLeftUndone": "Register/login finish handlers and logout/refresh rotation are not part of this feature and remain for later features.",
  "verification": {
    "commandsRun": [
      {
        "command": "go test ./... -count=1 -p 4",
        "exitCode": 0,
        "observation": "New auth handler and store tests pass; begin-route negative cases fail closed."
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
        "action": "Started local auth service with `.factory/services.yaml` auth command and POSTed malformed JSON to `/auth/register/begin` and `/auth/login/begin`",
        "observed": "Both endpoints returned JSON 4xx responses instead of HTML or 5xx."
      },
      {
        "action": "POSTed valid begin payloads and fetched `/auth/session` with and without cookies",
        "observed": "Begin routes returned RP-bound challenges; `/auth/session` cleanly distinguished signed-out and signed-in state."
      }
    ]
  },
  "tests": {
    "added": [
      {
        "file": "internal/auth/handlers_test.go",
        "cases": [
          {
            "name": "TestRegisterBeginRejectsMalformedInput",
            "verifies": "Invalid begin payloads return JSON 4xx instead of 5xx."
          },
          {
            "name": "TestRegisterBeginReturnsRPBoundChallenge",
            "verifies": "Register begin response includes a challenge bound to the configured RP."
          },
          {
            "name": "TestSessionReportsSignedOutWithoutCookies",
            "verifies": "Session inspection returns a signed-out result when auth state is missing."
          }
        ]
      }
    ]
  },
  "discoveredIssues": []
}
```

## When to Return to Orchestrator

- The feature needs Nix/Caddy/systemd or Node B secret-management changes
- The feature depends on a public route or acceptance invariant that conflicts with `validation-contract.md`
- The feature would require a local auth bypass or direct-browser access to service ports
- A contract-critical verification path cannot be exercised with the current services or test setup
