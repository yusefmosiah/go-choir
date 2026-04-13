---
name: frontend-worker
description: Builds Svelte SPA components, responsive layouts, and browser-facing flows for go-choir desktop UX rewrite
---

# Frontend Worker

NOTE: Startup and cleanup are handled by `worker-base`. This skill defines the WORK PROCEDURE.

## When to Use This Skill

Features that involve creating or modifying Svelte components, CSS layouts, responsive design, client-side state management, or browser-facing UI flows in the go-choir frontend.

## Required Skills

- `agent-browser` — for manual verification of UI components and interactions. Invoke after implementing each component to verify rendering, interactions, and responsive behavior.

## Work Procedure

1. **Read context:** Read `mission.md` and `AGENTS.md` for mission boundaries and coding conventions. Read `.factory/library/architecture.md` for system design.

2. **Write tests first (red):** Create Playwright test file(s) covering the feature's validation assertions. Tests must fail before implementation begins. Place new test files in `frontend/tests/` following the naming convention.

3. **Implement (green):** Build the Svelte component(s) to make tests pass. Follow existing patterns:
   - Scoped `<style>` blocks with dark theme colors
   - `data-*` attributes for test targeting
   - Svelte stores for state management
   - `fetchWithRenewal` from `./lib/auth.js` for API calls

4. **Responsive verification:** If the feature involves layout, use `agent-browser` with viewport emulation at three breakpoints:
   - Desktop: 1280x800
   - Tablet: 900x800
   - Mobile: 375x812

5. **Accessibility check:** Verify keyboard navigation (Tab, Enter, Escape) and ARIA labels on all interactive elements.

6. **Run validators:**
   ```bash
   cd frontend && pnpm build
   cd frontend && pnpm e2e
   ```

7. **Manual verification with agent-browser:** For each user-facing interaction, take a screenshot showing the expected state. Each flow tested = one `interactiveChecks` entry.

## Example Handoff

```json
{
  "salientSummary": "Implemented DesktopIcons left rail component with 4 app icons (Files, Browser, Terminal, Settings), active indicator highlighting, and scrollable overflow. Created FloatingWindow rewrite with simplified bottom-right-only resize handle. Desktop.svelte rewritten with new layout (no top bar, left rail + bottom bar). All 8 new Playwright tests passing.",
  "whatWasImplemented": "DesktopIcons.svelte (left rail with icon+label, active indicator, scroll), BottomBar.svelte (minimized indicators + prompt input + user info + connection status), FloatingWindow.svelte (rewrite of Window.svelte with single resize handle), Desktop.svelte rewrite (removed top bar, bootstrap accordion, runtime panel; integrated left rail + bottom bar layout). New stores in desktop-stores.js.",
  "whatWasLeftUndone": "",
  "verification": {
    "commandsRun": [
      {"command": "cd frontend && pnpm build", "exitCode": 0, "observation": "Build succeeded with no errors"},
      {"command": "cd frontend && pnpm e2e --grep 'desktop shell'", "exitCode": 0, "observation": "8 tests passing"}
    ],
    "interactiveChecks": [
      {"action": "Navigate to http://localhost:4173, register passkey, verify desktop layout", "observed": "Left rail visible with 4 icons, bottom bar with prompt input, no top bar"},
      {"action": "Click File Browser icon in left rail", "observed": "Floating window opened with Files title, active indicator on rail icon"},
      {"action": "Click same icon again", "observed": "Existing window focused, no duplicate opened"},
      {"action": "Minimize window, click indicator in bottom bar", "observed": "Window restored to previous geometry"}
    ],
    "tests": {
      "added": [
        {"file": "frontend/tests/desktop-shell-core.spec.js", "cases": [
          {"name": "left rail renders with all app icons", "verifies": "VAL-SHELL-002"},
          {"name": "clicking rail icon opens single-instance window", "verifies": "VAL-SHELL-003"},
          {"name": "bottom bar always visible with prompt input", "verifies": "VAL-SHELL-006"},
          {"name": "floating window drag via title bar", "verifies": "VAL-SHELL-017"}
        ]}
      ]
    }
  },
  "discoveredIssues": []
}
```

## When to Return to Orchestrator

- Feature requires a backend API endpoint that doesn't exist yet
- Existing component structure prevents the required implementation
- Data attributes from prior mission tests conflict with new component structure
