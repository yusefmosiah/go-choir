---
name: go-worker
description: Implements Go backend services, runtime, store, and APIs for go-choir
---

# Go Worker

NOTE: Startup and cleanup are handled by `worker-base`. This skill defines the WORK PROCEDURE.

## When to Use This Skill

Use this skill for:
- Auth service fixes and email migration
- Provider gateway integration (Fireworks, Z.AI)
- Scheduler and work registry implementation
- Task spawning and parent-child relationships
- Store/DB schema changes
- Runtime tool loop and channel updates
- Any Go backend API or service work

## Required Skills

- **agent-browser** (if feature involves browser-facing flows): Use to verify auth flows, etext interactions, and task spawning UI. Invoke after implementation to test end-to-end.

## Work Procedure

### 1. Understand the Feature
Read the feature description in features.json. Identify:
- What files need to change (auth handlers, store, runtime, gateway, etc.)
- What tests already exist vs what needs to be added
- What the verification steps require

### 2. Write Tests First (TDD)
Write failing tests before implementation:
- Add table-driven tests in `*_test.go` files
- Test both success and error cases
- Test edge cases (empty inputs, boundary conditions, concurrent access)
- Tests must FAIL before implementation (red phase)

### 3. Implement the Feature
Implement to make tests pass:
- Follow existing code patterns in the codebase
- Use strong typing, proper error handling
- Add structured logging for debugging
- Update schemas with migrations if needed

### 4. Manual Verification
Run verification steps from the feature:
- Start services: `source start-services.sh` or manual start
- Test with curl: `curl -X POST http://localhost:8081/auth/...`
- Check logs: `tail -f auth.log`, `tail -f sandbox.log`
- Verify on Node B if deployment-related

### 5. Run Validators
- `go test ./internal/auth/...` (or relevant package)
- `go vet ./...`
- `gofmt -l .`

### 6. Browser Verification (if applicable)
If the feature has browser-facing aspects:
- Invoke `agent-browser` skill
- Test the full flow: registration → login → feature usage
- Document observations in handoff

## Example Handoff

```json
{
  "salientSummary": "Fixed auth re-login bug by correcting sign counter persistence in credentials table. Migrated username to email with unique constraint enforcement. Added 12 new test cases covering re-login scenarios.",
  "whatWasImplemented": "Updated internal/auth/handlers.go to properly update sign counter after each login. Modified internal/auth/store.go to add email column with UNIQUE constraint. Updated internal/auth/webauthn_user.go to use email for display name. Changed beginRequest struct to use Email field. All registration/login flows now email-based.",
  "whatWasLeftUndone": "",
  "verification": {
    "commandsRun": [
      {"command": "go test ./internal/auth/... -v", "exitCode": 0, "observation": "14 tests passed including 3 new re-login tests"},
      {"command": "curl -s http://localhost:8081/auth/register/begin -d '{\"email\":\"test@example.com\"}'", "exitCode": 0, "observation": "Returned valid WebAuthn creation options"},
      {"command": "curl -s http://localhost:8081/auth/login/begin -d '{\"email\":\"test@example.com\"}'", "exitCode": 0, "observation": "Returned valid WebAuthn assertion options"}
    ],
    "interactiveChecks": [
      {
        "action": "Register with email test@example.com, complete WebAuthn, logout, wait 5 minutes, re-login",
        "observed": "Re-login successful with same passkey, sign counter incremented correctly"
      }
    ]
  },
  "tests": {
    "added": [
      {"file": "internal/auth/handlers_test.go", "cases": [
        {"name": "TestReLoginWithSamePasskey", "verifies": "User can re-login after logout with same credential"},
        {"name": "TestSignCounterIncrement", "verifies": "Sign counter increments on each successful login"},
        {"name": "TestDuplicateEmailRejected", "verifies": "Registration with duplicate email returns 409"}
      ]}
    ]
  },
  "discoveredIssues": [
    {
      "severity": "low",
      "description": "Sign counter was not being updated in the credentials table after login",
      "suggestedFix": "Added tx.Exec to update sign_count in HandleLoginFinish"
    }
  ]
}
```

## When to Return to Orchestrator

Return if:
- The bug root cause is deeper than expected (e.g., WebAuthn library issue, not handler logic)
- Feature requires schema changes that conflict with existing data requiring migration strategy
- Provider API behaves unexpectedly (rate limits, auth failures)
- Need user decision on design tradeoff (e.g., how to handle existing username-based users)
