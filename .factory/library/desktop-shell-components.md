# Desktop Shell Components (M6 Desktop-Shell-Core)

## Overview

The desktop shell was rewritten from a top-bar + launcher paradigm to the ChoirOS desktop pattern with a left rail, floating window area, and bottom bar.

## New Components

### DesktopIcons.svelte (Left Rail)
- Fixed position on left edge, 80px wide (56px tablet, hidden mobile)
- 4 hardcoded apps: Files (📁), Browser (🌐), Terminal (💻), Settings (⚙️)
- Active indicator (blue dot) on icon when app window is focused
- Scrollable when viewport height insufficient
- `data-desktop-rail`, `data-rail-item`, `data-rail-icon`, `data-rail-label`
- `data-app-id` for each app (files, browser, terminal, settings)

### BottomBar.svelte (Bottom Bar)
- Fixed position at viewport bottom, 56px height
- Left section: hamburger button (mobile) + minimized window indicators
- Center: prompt input with "Ask anything..." placeholder
- Right: connection status dot (green/yellow/red), user email, logout button
- `data-bottom-bar`, `data-prompt-input`, `data-minimized-indicator`
- `data-bottom-logout`, `data-bottom-user`, `data-connection-status`

### stores/desktop.js (Desktop Stores)
- Svelte writable stores: `windows`, `activeWindowId`, `nextZIndex`, `liveStatus`
- Derived stores: `minimizedWindows`, `visibleWindows`
- Actions: `openApp`, `closeWindow`, `focusWindow`, `minimizeWindow`, `maximizeWindow`, `restoreWindow`, `moveWindow`, `resizeWindow`, `setWindows`
- App registry: `APP_REGISTRY` with 5 apps (files, browser, terminal, settings, etext)

## Modified Components

### Desktop.svelte
- Removed: top bar (`data-desktop-bar`), launcher, bootstrap accordion, runtime panel
- Added: left rail (`<DesktopIcons>`), bottom bar (`<BottomBar>`)
- Window area offset by rail width (80px desktop, 56px tablet, 0px mobile)
- Bootstrap data still fetched (for session renewal) but not displayed
- Live channel WebSocket connection preserved
- Desktop state persistence (GET/PUT /api/desktop/state) preserved via store subscriptions

### Window.svelte
- Added `aria-label` attributes to window control buttons (VAL-SHELL-031)

## Data Attributes (Test Selectors)

| Selector | Component | Description |
|----------|-----------|-------------|
| `[data-desktop-rail]` | DesktopIcons | Left rail container |
| `[data-rail-item]` | DesktopIcons | Individual rail icon button |
| `[data-rail-icon]` | DesktopIcons | Emoji icon span |
| `[data-rail-label]` | DesktopIcons | Text label span |
| `[data-bottom-bar]` | BottomBar | Bottom bar container |
| `[data-prompt-input]` | BottomBar | Prompt text input |
| `[data-minimized-indicator]` | BottomBar | Minimized window indicator button |
| `[data-bottom-logout]` | BottomBar | Logout button |
| `[data-bottom-user]` | BottomBar | User info container |
| `[data-connection-status]` | BottomBar | Connection status dot container |

**Backward compat selectors preserved:**
- `[data-desktop-logout]` / `[data-shell-logout]` → on BottomBar logout button
- `[data-desktop-user]` / `[data-shell-user]` → on BottomBar user info
- `[data-desktop-live-status]` / `[data-shell-live-status]` → on BottomBar connection status
- `[data-desktop]` / `[data-shell]` → on Desktop root container

## Removed Selectors
- `[data-desktop-bar]` — top bar removed (VAL-SHELL-001)
- `[data-launcher-toggle]` / `[data-launcher-menu]` — launcher removed
- `[data-desktop-taskbar]` — old taskbar removed (replaced by bottom bar indicators)
- `[data-shell-bootstrap]` — no longer displayed (data still fetched internally)

## Responsive Breakpoints

| Breakpoint | Left Rail | Bottom Bar |
|------------|-----------|------------|
| Desktop >1024px | 80px, icon+label | Full 56px |
| Tablet 768-1024px | 56px, icon-only | Full 56px |
| Mobile <768px | Hidden (hamburger) | Compact, prompt full-width |

## Test Coverage

- `desktop-shell-core.spec.js`: 14 tests covering VAL-SHELL-001 through VAL-SHELL-031
- `desktop-window-manager.spec.js`: Updated for new layout (9 passing, 1 skip)
- `logout-user-switch.spec.js`: Updated for new selectors (9 passing)
- `shell-ui.spec.js`: Updated for new layout (6 passing)

## Known Issues

- Desktop state persistence (GET/PUT /api/desktop/state) returns 500 in test environment — backend issue, not frontend
- WebSocket live channel may not connect in test environment due to proxy auth flow for WS upgrades
