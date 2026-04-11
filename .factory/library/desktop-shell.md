# Desktop Shell Window Manager

## Overview

The desktop shell replaces the placeholder authenticated shell with a real desktop environment featuring:

- **Launcher** — App launcher with E-Text as the first real app
- **Window lifecycle** — Open, focus, minimize, maximize, restore, close, reopen
- **Focus/z-order** — Clicking a window brings it to front; higher zIndex is on top
- **Drag/resize** — Title bar drag for moving; edge/corner handles for resizing
- **Taskbar** — Minimized windows appear in the bottom taskbar for easy restore
- **Server-backed state** — Desktop state is persisted through `/api/desktop/state` and survives fresh browser contexts

## Components

### Desktop.svelte
Root authenticated component. Manages window array, focus/z-order, and state persistence. Dispatches `logout` and `authexpired` events to App.svelte. Preserves Shell behaviors: bootstrap data fetch, live channel, refresh/renewal.

### Window.svelte
Desktop window with title bar (drag handle), window controls (close, minimize, maximize/restore), resize handles (8 directions), and content slot for app rendering. Modes: normal, minimized, maximized. RestoredGeometry saves pre-maximize geometry for restore.

### Launcher.svelte
App launcher dropdown with E-Text enabled and placeholder entries for terminal, files, and mind-graph. Dispatches `launchapp` events to Desktop.

### desktop.js
API client for `GET /api/desktop/state` and `PUT /api/desktop/state`. Uses `fetchWithRenewal` for auth-gated cookie-backed requests.

## Backend API

### GET /api/desktop/state
Returns persisted desktop state for the authenticated user. If no state exists, returns empty default state. Auth-gated through proxy.

### PUT /api/desktop/state
Saves desktop state including windows, active window, geometry, mode, and app context. Auth-gated through proxy.

## Persistence Schema

The `desktop_state` table stores per-user desktop state as JSON in SQLite:
- `owner_id` (primary key) — authenticated user ID
- `windows_json` — JSON array of window states
- `active_window` — currently focused window ID
- `updated_at` — last modification time

## Test Selectors

- `[data-desktop]` — desktop root container (also has `[data-shell]` for backward compat)
- `[data-desktop-bar]` — top bar
- `[data-desktop-logout]` / `[data-shell-logout]` — logout button
- `[data-desktop-user]` / `[data-shell-user]` — current user display
- `[data-desktop-windows]` — window container area
- `[data-desktop-taskbar]` — minimized windows taskbar
- `[data-desktop-live-status]` / `[data-shell-live-status]` — live channel status
- `[data-launcher-toggle]` — launcher open/close button
- `[data-launcher-menu]` — launcher dropdown menu
- `[data-launcher-app]` — launcher app entry
- `[data-app-id="etext"]` — E-Text launcher entry
- `[data-window]` — window container
- `[data-window-id]` — window identifier attribute
- `[data-window-titlebar]` — window title bar
- `[data-window-close]` — close button
- `[data-window-minimize]` — minimize button
- `[data-window-maximize]` — maximize/restore button
- `[data-window-content]` — window content area

## Backward Compatibility

The Desktop component maintains `data-shell` attributes alongside `data-desktop` attributes for backward compatibility with existing Playwright tests from the deploy-readiness milestone. The authenticated surface is still detected as `[data-shell]` by existing tests.
