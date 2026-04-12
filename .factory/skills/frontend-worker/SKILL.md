---
name: frontend-worker
description: Builds the Svelte SPA, desktop UI, e-text app, and browser-facing flows
---

# Frontend Worker

NOTE: Startup and cleanup are handled by `worker-base`. This skill defines the WORK PROCEDURE.

## When to Use This Skill

Use this skill for:
- Auth UI changes (email input, error messages)
- E-text editor integration with backend APIs
- Desktop window management updates
- Task runner and agent spawning UI
- Playwright test updates for browser flows
- Any Svelte component or JavaScript module work

## Required Skills

- **agent-browser**: REQUIRED for all frontend features. Use to verify UI flows, auth ceremonies, and end-to-end interactions. Invoke this skill during verification.

## Work Procedure

### 1. Understand the Feature
Read the feature description. Identify:
- Which Svelte components need changes
- Which API endpoints will be called
- What Playwright tests need updates

### 2. Update Tests First (TDD)
Update or add Playwright tests before implementation:
- Modify existing test files in `frontend/tests/`
- Add new test cases for the feature
- Tests should expect the NEW behavior (will fail initially)

### 3. Implement UI Changes
Update Svelte components:
- Modify inputs, labels, and validation
- Update API calls in `lib/*.js` modules
- Handle loading states and errors
- Ensure accessibility (labels, ARIA attributes)

### 4. Manual Verification with Dev Server
- Start frontend: `cd frontend && pnpm dev`
- Start backend services if needed
- Test the flow manually in browser
- Check browser console for errors

### 5. Run Frontend Validators
- `cd frontend && pnpm run lint`
- `cd frontend && pnpm run typecheck` (if available)
- Fix any issues

### 6. Invoke agent-browser for End-to-End Verification
Invoke the `agent-browser` skill:
- Test the full user flow
- Verify auth works (register → login → use feature)
- Capture screenshots or evidence
- Document any UI quirks or issues

## Example Handoff

```json
{
  "salientSummary": "Updated AuthEntry.svelte to use email input instead of username. Modified auth.js to send email field to backend. Updated Playwright tests to use uniqueEmail() helper. All auth flows verified working in browser.",
  "whatWasImplemented": "Changed username input to type=email with autocomplete=email in AuthEntry.svelte. Updated handleRegister and handleLogin to use email variable. Modified auth.js registerPasskey and loginPasskey functions to send {email} instead of {username}. Updated 5 Playwright test files to use email format.",
  "whatWasLeftUndone": "",
  "verification": {
    "commandsRun": [
      {"command": "cd frontend && pnpm run lint", "exitCode": 0, "observation": "No lint errors"},
      {"command": "cd frontend && pnpm test --grep 'auth'", "exitCode": 0, "observation": "12 auth tests passed"}
    ],
    "interactiveChecks": [
      {
        "action": "agent-browser: Navigate to /auth, register with email user@example.com, complete WebAuthn, verify redirect to desktop",
        "observed": "Registration successful, email displayed in UI, session active"
      },
      {
        "action": "agent-browser: Logout, re-login with same email and passkey",
        "observed": "Login successful, user returned to desktop with previous state"
      }
    ]
  },
  "tests": {
    "added": [
      {"file": "frontend/tests/auth-passkey.spec.js", "cases": [
        {"name": "email input validation", "verifies": "Email input rejects invalid formats"},
        {"name": "registration with email", "verifies": "Full registration flow with email field"}
      ]}
    ]
  },
  "discoveredIssues": []
}
```

## When to Return to Orchestrator

Return if:
- Backend API contract is unclear or not implemented yet
- Playwright tests fail due to infrastructure issues (not code)
- UI design decision needed (layout, UX pattern)
- Browser compatibility issue discovered
