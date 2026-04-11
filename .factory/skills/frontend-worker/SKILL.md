---
name: frontend-worker
description: Builds the Svelte auth UI, placeholder shell, and browser-facing session flows for Mission 2 Milestone 1
---

# Frontend Worker

NOTE: Startup and cleanup are handled by `worker-base`. This skill defines the WORK PROCEDURE.

## When to Use This Skill

Use for browser-facing features in Mission 2 Milestone 1:
- guest auth UI
- register/login view transitions
- passkey flow wiring in the Svelte app
- placeholder desktop shell UI
- cookie-backed shell bootstrap, reload/new-tab rehydration, logout teardown, and user-switch behavior

## Required Skills

- `agent-browser` — required for browser verification of guest UI, route transitions, shell state, reload/new-tab behavior, and logout flows

## Work Procedure

1. **Read the contract and route invariants first**
   - Read the feature description, `validation-contract.md`, `.factory/library/architecture.md`, and `.factory/library/user-testing.md`.
   - Keep the public browser contract fixed:
     - `/`
     - `/auth/*`
     - `GET /api/shell/bootstrap`
     - `GET /api/ws`

2. **Read the existing frontend before editing**
   - Inspect `frontend/src`, `frontend/package.json`, and Vite config first.
   - Match existing style and keep the app same-origin.
   - Never introduce token storage in the URL, `localStorage`, or `sessionStorage`.

3. **Write failing tests first**
   - Add or extend a minimal frontend test harness if the feature needs one.
   - Write failing tests for the UI state transitions or browser-facing logic you are about to implement.
   - If the feature is mostly integration wiring, still add the smallest meaningful failing test around routing/state behavior before implementation.
   - When the feature touches passkey/browser-auth validation, keep the Playwright Chromium virtual-authenticator harness current.

4. **Implement the smallest browser slice that satisfies the feature**
   - Keep signed-out and signed-in states visibly distinct.
   - Use same-origin cookie-backed requests only.
   - Do not hardcode direct service ports, `localhost`, or stripped internal paths into deployed browser behavior.
   - Treat `GET /auth/session` as the rehydration checkpoint for reload/new-tab flows.

5. **Verify with `agent-browser`**
   - Run browser checks for the exact state transitions the feature changes.
   - For guest UI features, verify the app does not spam failing protected requests while signed out.
   - For shell features, verify the UI is not just static chrome; prove bootstrap/live-channel behavior or explicit degraded-state handling.
   - For passkey/session-lifecycle features, run the Playwright Chromium suite or the focused Playwright test you added.

6. **Run validators before handoff**
   - `go test ./... -count=1 -p 4`
   - `go vet ./...`
   - `go build ./cmd/...`
   - `cd frontend && pnpm build`
   - `cd frontend && pnpm exec playwright test` when the feature changes passkey automation or browser session-lifecycle flows
   - Run any new frontend test command you introduced

7. **Return a concrete handoff**
   - Record the browser checks you actually performed, including the route/state transitions observed.
   - If manual passkey validation is still needed for full proof, say that explicitly in `whatWasLeftUndone` or `interactiveChecks`.

## Example Handoff

```json
{
  "salientSummary": "Implemented the signed-out auth entry UI with distinct register/login views and wired it to the existing auth route contract. Added focused frontend tests for view switching and verified in a browser that the guest root no longer renders the placeholder-only page or eagerly boots protected shell traffic.",
  "whatWasImplemented": "Updated the Svelte app to render signed-out auth entry state at `/`, added register/login view toggles with clear primary actions, and kept the browser on same-origin route usage only. Also added browser-state guards so the guest UI does not continuously issue failing protected bootstrap or WebSocket requests before login.",
  "whatWasLeftUndone": "Passkey ceremony success/failure handling and authenticated shell behavior are handled by later frontend integration features.",
  "verification": {
    "commandsRun": [
      {
        "command": "go test ./... -count=1 -p 4",
        "exitCode": 0,
        "observation": "Go packages remain green after frontend route/state changes."
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
      },
      {
        "command": "cd frontend && pnpm build",
        "exitCode": 0,
        "observation": "Frontend builds successfully with the new auth-entry UI."
      }
    ],
    "interactiveChecks": [
      {
        "action": "Used agent-browser to load `/` in a clean session and switch between register and login views",
        "observed": "Both views rendered distinct headings and CTAs without exposing the authenticated shell."
      },
      {
        "action": "Inspected the signed-out network activity in the browser",
        "observed": "The guest UI did not repeatedly issue failing `GET /api/shell/bootstrap` or `GET /api/ws` requests."
      }
    ]
  },
  "tests": {
    "added": [
      {
        "file": "frontend/src/App.test.js",
        "cases": [
          {
            "name": "rendersGuestAuthEntryByDefault",
            "verifies": "Signed-out root shows auth UI instead of shell UI."
          },
          {
            "name": "switchesBetweenRegisterAndLoginViews",
            "verifies": "Guest users can reach both register and login views."
          }
        ]
      }
    ]
  },
  "discoveredIssues": []
}
```

## When to Return to Orchestrator

- The feature needs backend contract changes to `/auth/*`, `GET /api/shell/bootstrap`, or `GET /api/ws`
- The browser surface cannot satisfy the contract without an infra/Nix/Caddy change
- The only way to proceed would be storing auth tokens in browser-visible locations or using a local auth bypass
- Manual passkey validation is the only missing proof and the feature is otherwise complete
