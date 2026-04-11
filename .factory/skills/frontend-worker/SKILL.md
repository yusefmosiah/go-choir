---
name: frontend-worker
description: Builds the real desktop, runtime UI, e-text app, and browser-facing Mission 3 flows
---

# Frontend Worker

NOTE: Startup and cleanup are handled by `worker-base`. This skill defines the WORK PROCEDURE.

## When to Use This Skill

Use for Mission 3 browser-facing features such as:
- Node B deploy-readiness frontend fixes
- real desktop/window-manager UI
- shell prompt/task/status/event UI
- e-text editor and history surfaces
- terminal/files/mind-graph app surfaces
- browser-facing logout, rehydration, user-switch, and in-flight reattachment behavior

## Required Skills

- `agent-browser` — required for deployed/browser-visible verification and screenshot-backed smoke checks

## Work Procedure

1. **Read the contract and route invariants first**
   - Read the feature description, `mission.md`, `AGENTS.md`, `validation-contract.md`, `.factory/library/architecture.md`, `.factory/library/environment.md`, `.factory/library/user-testing.md`, and `.factory/services.yaml`.
   - Keep the public browser contract fixed:
     - `/`
     - `/auth/*`
     - `GET /api/shell/bootstrap`
     - `GET /api/ws`
     - `POST /api/agent/task`
     - `GET /api/agent/status`
     - `GET /api/events`
   - If the current top milestone is still `deploy-readiness`, restoring the real browser experience on `https://draft.choir-ip.com` is a hard gate for later UI work.

2. **Read the existing frontend before editing**
   - Inspect `frontend/src`, `frontend/package.json`, and Vite config first.
   - Match existing style and keep the app same-origin.
   - Never introduce token storage in the URL, `localStorage`, or `sessionStorage`.
   - If the feature adds desktop state, prove it is not browser-only fake persistence when the contract expects server-backed restore.

3. **Write failing tests first**
   - Add or extend Playwright coverage before implementation for stateful desktop/e-text/browser flows.
   - Write failing tests for the UI state transitions, restore semantics, or browser-facing logic you are about to implement.
   - If the feature is mostly integration wiring, still add the smallest meaningful failing test around routing/state behavior before implementation.
   - Keep the Playwright Chromium virtual-authenticator harness current when the feature touches passkey/session flows.

4. **Implement the smallest browser slice that satisfies the feature**
   - Keep signed-out and signed-in states visibly distinct.
   - Use same-origin cookie-backed requests only.
   - Do not hardcode direct service ports, `localhost`, or stripped internal paths into deployed browser behavior.
   - Treat `GET /auth/session` as the rehydration checkpoint for reload/new-tab flows.
   - For desktop/e-text flows, prefer real task/status/history wiring over decorative placeholder UI.

5. **Verify with `agent-browser`**
   - Run browser checks for the exact visible state transitions the feature changes.
   - For stateful desktop/e-text flows, run focused Playwright coverage and use `agent-browser` as the visual/deployed smoke layer.
   - For guest UI features, verify the app does not spam failing protected requests while signed out.
   - For shell/runtime features, prove task/status/event behavior or explicit degraded-state handling.
   - For passkey/session-lifecycle features, run the focused Playwright test you added or the relevant suite slice.
   - When the feature affects task submission or canonical document mutation, explicitly prove that renewal/retry/reload does not create duplicate submissions or duplicate canonical revisions.

6. **Run validators before handoff**
   - `go test ./... -count=1 -p 4`
   - `go vet ./...`
   - `go build ./cmd/...`
   - `cd frontend && pnpm build`
   - `cd frontend && pnpm exec playwright test --workers=1` when the feature changes passkey automation, browser session-lifecycle flows, or stateful desktop/app behavior
   - Run any narrower Playwright command you added during iteration

7. **Return a concrete handoff**
   - Record the browser checks you actually performed, including the route/state transitions observed.
   - Include both deterministic Playwright proof and visual `agent-browser` proof when the feature has meaningful UI state.
   - If manual deployed validation is still needed for full proof, say that explicitly in `whatWasLeftUndone` or `interactiveChecks`.

## Example Handoff

```json
{
  "salientSummary": "Implemented the real desktop window manager plus persisted window restore for the e-text app. Added focused Playwright coverage for focus, drag/resize, minimize/maximize, close/reopen, and restore from a fresh browser context, and verified the deployed desktop surface visually with agent-browser.",
  "whatWasImplemented": "Replaced placeholder shell chrome with a real desktop shell, launcher, and stateful window manager for Mission 3. Added browser-facing restore behavior that reattaches to persisted desktop state for the same user, and wired the e-text app launcher entry so opening the app creates a real focused window instead of a decorative placeholder panel.",
  "whatWasLeftUndone": "Document editing/history behavior and appagent revision flows are handled by later e-text features.",
  "verification": {
    "commandsRun": [
      {
        "command": "go test ./... -count=1 -p 4",
        "exitCode": 0,
        "observation": "Backend packages remain green after the frontend state changes."
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
        "observation": "Frontend builds successfully with the real desktop shell."
      },
      {
        "command": "cd frontend && pnpm exec playwright test --workers=1 tests/desktop-window-manager.spec.js",
        "exitCode": 0,
        "observation": "Desktop window-manager and restore assertions pass serially."
      }
    ],
    "interactiveChecks": [
      {
        "action": "Used agent-browser to load the authenticated desktop on the intended acceptance surface and open the e-text app from the launcher",
        "observed": "The desktop rendered a real launcher and opened a focused e-text window instead of placeholder shell chrome."
      },
      {
        "action": "Reloaded the desktop in a fresh browser context after leaving the e-text window open",
        "observed": "The desktop restored the same window and document context instead of starting from an empty placeholder state."
      }
    ]
  },
  "tests": {
    "added": [
      {
        "file": "frontend/tests/desktop-window-manager.spec.js",
        "cases": [
          {
            "name": "restoresDesktopStateFromFreshContext",
            "verifies": "Open windows and active context restore for the same user from persisted state."
          },
          {
            "name": "supportsWindowFocusAndResize",
            "verifies": "Focus changes and geometry updates are reflected in visible desktop state."
          }
        ]
      }
    ]
  },
  "discoveredIssues": []
}
```

## When to Return to Orchestrator

- The feature needs backend contract changes to `/auth/*`, `GET /api/shell/bootstrap`, `GET /api/ws`, `/api/agent/*`, or `/api/events`
- The browser surface cannot satisfy the contract without an infra/Nix/Caddy or VM-routing change
- The only way to proceed would be storing auth tokens in browser-visible locations, using a local auth bypass, or faking server-backed state with browser-only persistence
- Manual deployed validation is the only missing proof and the feature is otherwise complete
