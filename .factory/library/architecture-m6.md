# Architecture — Mission 6: Desktop UX Rewrite

## System Overview

go-choir is a distributed multi-agent operating system with a web desktop UI (Svelte SPA) backed by Go microservices.

## Desktop Shell Architecture (New for M6)

```
┌──────────────────────────────────────────────────────────┐
│ Browser (Svelte SPA)                                     │
│                                                          │
│ ┌──────────────────────────────────────────────────────┐ │
│ │ Desktop Surface (floating icons + windows)           │ │
│ │                                                      │ │
│ │  📁        🌐        💻        ⚙️                    │ │
│ │ Files     Browser   Terminal  Settings               │ │
│ │                                                      │ │
│ │  ┌──────┐  ┌──────┐                                 │ │
│ │  │Files │  │Term  │  (floating windows)              │ │
│ │  └──────┘  └──────┘                                 │ │
│ └──────────────────────────────────────────────────────┘ │
│ ┌──────────────────────────────────────────────────────┐ │
│ │ Bottom Bar (56px fixed)                              │ │
│ │ [Show Desktop] [minimized] [prompt: Ask anything...] │ │
│ │                        [user@email] [Sign Out]       │ │
│ └──────────────────────────────────────────────────────┘ │
└──────────────────────────────────────────────────────────┘
        │ fetchWithRenewal (cookie auth)
        ▼
┌──────────────────────────────────────────────────────────┐
│ Proxy (8082) — auth-gated reverse proxy                  │
│ /auth/* → auth:8081                                     │
│ /api/*  → sandbox:8085                                  │
└──────────────────────────────────────────────────────────┘
        │
        ▼
┌──────────────────────────────────────────────────────────┐
│ Sandbox (8085) — runtime + app APIs                       │
│ ├─ /api/desktop/state (GET/PUT) — window layout persist  │
│ ├─ /api/files/* — file browser CRUD                     │
│ ├─ /api/terminal/ws — PTY WebSocket                     │
│ ├─ /api/settings/providers — LLM provider CRUD           │
│ ├─ /api/etext/* — document APIs (unchanged, M7 scope)   │
│ └─ /api/agent/* — task APIs (unchanged)                  │
└──────────────────────────────────────────────────────────┘
        │
        ▼
┌──────────────────────────────────────────────────────────┐
│ Gateway (8084) — LLM provider routing                     │
│ MultiProvider map, reloadable at runtime (M6 addition)   │
└──────────────────────────────────────────────────────────┘
```

## Responsive Breakpoints

| Breakpoint | Desktop Icons | Windows | Bottom Bar |
|------------|---------------|---------|------------|
| Desktop >1024px | Floating, draggable, full-size | Floating, draggable, resizable | Full 56px |
| Tablet 768-1024px | Same as desktop | Floating, max-width constrained | Full 56px |
| Mobile <768px | Same as desktop | Single focus, full-width, no drag | Compact |

## Component Structure (Frontend)

```
frontend/src/lib/
├── App.svelte              — Root: auth check → Desktop or AuthEntry
├── Desktop.svelte           — Shell: desktop surface + windows + bottom bar
├── DesktopIcons.svelte      — Floating desktop icons (draggable, grid layout)
├── BottomBar.svelte         — Show Desktop + minimized indicators + prompt + user info
├── PromptBar.svelte         — "Ask anything..." input
├── FloatingWindow.svelte    — Window chrome (drag, resize, minimize/maximize/close)
├── FileBrowser.svelte       — File browser app
├── BrowserApp.svelte        — Simple iframe browser app
├── TerminalApp.svelte       — ghostty-web terminal app
├── SettingsApp.svelte       — LLM provider settings app
├── stores/
│   └── desktop.js           — Svelte stores (windows, activeWindow, etc.)
├── auth.js                  — fetchWithRenewal, session management
├── desktop.js               — Desktop state persistence API
├── files.js                 — File browser API helpers
├── terminal.js              — Terminal WebSocket helper
├── settings.js              — Settings API helpers
└── (deprecated)
    ├── Window.svelte        — Old 8-handle window (replaced by FloatingWindow)
    ├── Launcher.svelte      — Old dropdown launcher (replaced by DesktopIcons)
    ├── Shell.svelte         — Old shell (replaced by Desktop)
    └── ETextEditor.svelte   — E-text (unchanged, Mission 7)
```

## State Management

- **Svelte writable stores** in `stores/desktop.js`: windows, activeWindow, minimizedWindows
- **Server persistence**: GET/PUT /api/desktop/state (debounced 500ms save)
- **Per-app state**: Each app manages its own internal state (e.g., terminal PTY connection, file browser current path)

## Key Invariants

1. Auth is cookie-based (access + refresh JWT). All API calls use `fetchWithRenewal`.
2. Desktop state survives page reload and new tabs (server-side SQLite).
3. Single-instance apps: double-clicking a desktop icon for an already-open app focuses/restores it.
4. No top bar, no left rail, no bootstrap accordion, no runtime panel visible to users.
5. Desktop icons are freely-draggable on the desktop surface with positions persisted.
6. "Show Desktop" button minimizes all windows; clicking again restores them.
7. Terminal PTY processes are cleaned up on window close (no zombies).
8. Settings API keys are AES-GCM encrypted at rest, never exposed in GET responses.
