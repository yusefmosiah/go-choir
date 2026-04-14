# Mission 6: Desktop UX Rewrite - ChoirOS Pattern

**Goal:** Rewrite the web desktop UI to match the ChoirOS desktop paradigm: dock + prompt bar at bottom, floating windows, desktop icons, no top bar.

**Reference:** `/Users/wiz/choiros-rs/docs/archive/DESKTOP_ARCHITECTURE_DESIGN.md`

---

## Current Problems

1. **Top bar with apps** - Wrong paradigm. Should be desktop icons.
2. **"Bootstrap" accordion** - Confusing, should be automatic or eliminated.
3. **No prompt bar** - The conductor input should be always visible at bottom.
4. **`vtext` drifted into the wrong UX** - The document app should be a single primary editing surface, not a research-button-plus-sidebar UI.
5. **Not responsive** - Doesn't adapt to mobile/tablet/desktop.

---

## Target Architecture (ChoirOS Pattern)

```
┌─────────────────────────────────────────────────────────────┐
│                      BROWSER (Mobile/Desktop)                │
│                                                              │
│  ┌────────────────────────────────────────────────────────┐ │
│  │ Floating Windows (draggable, resizable, overlapping)   │ │
│  │  ┌──────────┐  ┌──────────┐  ┌──────────┐               │ │
│  │  │ VText    │  │ Terminal│  │ Chat     │               │ │
│  │  │ (drag)   │  │ (drag)  │  │ (drag)   │               │ │
│  │  └──────────┘  └──────────┘  └──────────┘               │ │
│  └────────────────────────────────────────────────────────┘ │
│                                                              │
│  ┌────────────────────────────────────────────────────────┐ │
│  │ Left Rail (Desktop Icons)                               │ │
│  │  📄 VText                                                │ │
│  │  💻 Terminal                                             │ │
│  │  💬 Chat                                                 │ │
│  │  🌐 Browser                                              │ │
│  └────────────────────────────────────────────────────────┘ │
│                                                              │
│  ┌────────────────────────────────────────────────────────┐ │
│  │ Bottom Bar (Always Visible)                             │ │
│  │  ┌──────────────┐  ┌──────────────────────────┐       │ │
│  │  │ Minimized    │  │ 🎤 Prompt Bar (Conductor)│       │ │
│  │  │ [E] [T] [C]  │  │ "Type your request..."   │       │ │
│  │  └──────────────┘  └──────────────────────────┘       │ │
│  └────────────────────────────────────────────────────────┘ │
└─────────────────────────────────────────────────────────────┘
```

---

## Components to Build

### 1. Left Rail - Desktop Icons
**Files:** `frontend/src/lib/DesktopIcons.svelte`

- Vertical stack of app icons on left side
- Each icon has: emoji/icon, label below
- Click to open/launch app
- Scrollable if overflow
- Collapsible on mobile (hamburger menu)

**Apps (hardcoded for now):**
- 📄 VText (versioned document)
- 💻 Terminal (bash shell)
- 💬 Chat (conductor chat interface)
- 🌐 Browser (simple web view)

### 2. Bottom Bar - Dock + Prompt Bar
**Files:** `frontend/src/lib/BottomBar.svelte`

**Left side:** Minimized app indicators
- Small icons showing minimized windows
- Click to restore window
- Same icons as left rail but smaller

**Right side:** Prompt Bar (Conductor input)
- Always visible text input
- Microphone icon for voice (future)
- Submit button or Enter key
- Placeholder: "Type your request..."
- Height: ~60px fixed

### 3. Floating Windows
**Files:** `frontend/src/lib/FloatingWindow.svelte` (rewrite existing Window)

**Features:**
- Draggable (title bar drag)
- Resizable (bottom-right corner handle)
- Title bar with: icon, title, minimize, maximize, close buttons
- Z-index management (click to focus)
- Minimized state (hidden, appears in bottom bar)
- Responsive sizing (mobile: full width, desktop: floating)

**Window State:**
- Position (x, y)
- Size (width, height)
- Z-index
- Minimized/maximized flags
- App ID and instance ID

### 4. VText - Version-Native Document Surface
**Files:** `frontend/src/lib/ETextSimple.svelte` (rewrite)

**Features:**
- One large primary editing surface
- Prompt bar input or blank document creates `v0`
- User may make multiple local edits before explicitly creating the next version
- Inline superscript citations and inline transclusions
- No research button
- No citations sidebar
- No metadata panel as the primary UI
- Current code/routes may still use `etext`; the architectural name is `vtext`

**Simple API:**
- `GET /app/etext/api/documents` - list
- `POST /app/etext/api/documents` - create
- `GET /app/etext/api/documents/:id` - load
- `PUT /app/etext/api/documents/:id` - save

### 5. Conductor Integration
**Files:** `frontend/src/lib/PromptBar.svelte`, update `App.svelte`

**How it works:**
1. User types in prompt bar
2. Submit sends to backend: `POST /api/conductor/route`
3. Conductor decides which appagent should handle it
4. Appagent opens appropriate window or updates existing
5. User sees result in relevant app window

**Target behavior:**
- Focus is not a routing hint for now. By default, prompt bar input opens a new `vtext` rather than assuming the focused window is the target
- A blank `vtext` or prompt submission creates `v0`
- Very simple prompts may resolve to lightweight UI responses such as a toast instead of opening a heavy workflow
- `vtext` appagent responds promptly, may spawn workers, and rewrites the document into later versions
- Conductor should eventually dispatch intelligently across subsystems, but browser/terminal launching is not the first target

---

## Responsive Behavior

### Desktop (>1024px)
- Left rail: full width (~200px)
- Windows: floating, draggable, resizable
- Bottom bar: full height (~60px)

### Tablet (768-1024px)
- Left rail: icon-only mode (~60px), expands on hover
- Windows: floating but max-width constrained
- Bottom bar: full height

### Mobile (<768px)
- Left rail: hidden, hamburger menu button opens slide-out
- Windows: same floating-window model as desktop, with tighter sizing constraints for smaller screens
- Bottom bar: compact mode, prompt bar full width

---

## State Management

### Frontend State (Svelte Stores)
```javascript
// desktop.js
export const windows = writable([]); // Array of window states
export const activeWindow = writable(null); // ID of focused window
export const minimizedWindows = writable([]); // IDs of minimized
export const apps = writable([ // Available apps
  { id: 'etext', name: 'VText', icon: '📄' },
  { id: 'terminal', name: 'Terminal', icon: '💻' },
  { id: 'chat', name: 'Chat', icon: '💬' },
]);
```

Note: current runtime IDs still use `etext`. Product-facing naming should move toward `vtext`.

### Backend Sync
- Window positions saved to backend periodically
- On reload, restore previous window layout
- Per-user desktop state in SQLite

---

## Files to Create/Modify

### New Files:
- `frontend/src/lib/DesktopIcons.svelte` - Left rail
- `frontend/src/lib/BottomBar.svelte` - Dock + prompt bar
- `frontend/src/lib/FloatingWindow.svelte` - Window rewrite
- `frontend/src/lib/ETextSimple.svelte` - Transitional document editor to evolve into `vtext`
- `frontend/src/lib/PromptBar.svelte` - Conductor input
- `frontend/src/lib/AppLauncher.svelte` - App launching logic

### Modify:
- `frontend/src/lib/Desktop.svelte` - Remove top bar, add new layout
- `frontend/src/App.svelte` - Integrate prompt bar, remove bootstrap
- `frontend/src/lib/Window.svelte` - Deprecate, use FloatingWindow
- `frontend/src/lib/ETextEditor.svelte` - Deprecate, use ETextSimple / future `vtext`

### Delete/Deprecate:
- Remove "bootstrap" accordion
- Remove top app bar
- Remove research button from etext/vtext
- Remove citations sidebar
- Make ordinary textual files open in `vtext` for a unified desktop/file-browser workflow

---

## API Changes

### New Backend Endpoints:
```
GET    /api/desktop/state          - Get user's desktop state (windows, positions)
POST   /api/desktop/state          - Save desktop state
POST   /api/conductor/route        - Route prompt to appropriate appagent
```

### Transitional VText API:
```
GET    /app/etext/api/documents              - List documents
POST   /app/etext/api/documents              - Create new document
GET    /app/etext/api/documents/:id          - Get document content
PUT    /app/etext/api/documents/:id          - Save document content
DELETE /app/etext/api/documents/:id          - Delete document
```

---

## Migration Strategy

1. **Phase 1:** Create new components (FloatingWindow, DesktopIcons, BottomBar)
2. **Phase 2:** Rewrite Desktop.svelte with new layout
3. **Phase 3:** Rewrite ETextSimple, remove old ETextEditor
4. **Phase 4:** Add conductor/prompt bar integration
5. **Phase 5:** Responsive polish, mobile testing
6. **Phase 6:** Remove deprecated components, clean up

---

## Success Criteria

1. ✅ No top bar - apps are desktop icons on left rail
2. ✅ Bottom bar always visible with prompt bar
3. ✅ Floating windows - draggable, resizable, minimizable
4. ✅ `vtext` is a focused versioned document surface (no research button, no sidebar-first UX)
5. ✅ Responsive - works on mobile, tablet, desktop
6. ✅ No "bootstrap" accordion - automatic or eliminated
7. ✅ Minimized apps appear in bottom bar

---

## Notes

- **`vtext` as canonical document surface:** The document app is where canonical versions live. The conductor is still the cross-app entry point, but prompt-bar requests should commonly route into `vtext`, where the `vtext` appagent can spawn workers and rewrite the document.

- **Worker authority:** workers may read the document and report findings/results/artifacts back, but they should not directly edit canonical `vtext` content.

- **Reference screenshots:** Look at `/Users/wiz/choiros-rs/dioxus-desktop/` for UI patterns, but adapt to Svelte (not Dioxus).
