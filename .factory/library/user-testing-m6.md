# User Testing — Mission 6

## Validation Surface

**Primary surface:** Browser UI at http://localhost:4173 (Svelte SPA)
**Secondary surface:** curl for API endpoint verification

## Testing Tools

- **agent-browser**: Primary tool for all UI assertions. Supports viewport emulation for responsive testing.
- **Playwright**: Automated regression tests in `frontend/tests/`
- **curl**: API endpoint verification (through proxy at localhost:4173)

## Setup Requirements

1. All services must be running: auth(8081), proxy(8082), vmctl(8083), gateway(8084), sandbox(8085), frontend(4173)
2. Auth registration required before any desktop testing (passkey/WebAuthn)
3. The frontend dev server proxies `/auth/*` → 8081 and `/api/*` → 8082

## Validation Concurrency

**Machine:** 16 GB RAM, 8 CPU cores
**Available headroom:** ~6 GB (after macOS + IDE + services)

| Surface | Per-Instance Cost | Max Concurrent |
|---------|------------------|---------------|
| agent-browser (lightweight SPA) | ~300 MB | 5 |

Rationale: 5 × 300 MB = 1.5 GB, well within 6 GB headroom × 0.7 = 4.2 GB budget.

## Viewport Configurations

| Breakpoint | Width | Height | Purpose |
|------------|-------|--------|---------|
| Desktop | 1280 | 800 | Full layout testing |
| Tablet | 900 | 800 | Icon-only rail, constrained windows |
| Mobile | 375 | 812 | Hamburger, focus mode, touch targets |

## Key Test Sequences

**Auth flow:** Register passkey → verify desktop layout → logout → verify guest UI

**Window management:** Open Files → open Terminal → focus Files → minimize → focus Terminal → maximize → restore → close → verify bottom bar indicators

**Responsive round-trip:** Desktop layout → resize to tablet → verify icon-only rail → resize to mobile → verify hamburger → open app → verify focus mode → resize back to desktop → verify layout restored

**Terminal:** Open terminal → type commands → resize window → verify reflow → minimize → restore → verify content preserved → close → verify PTY cleanup → reopen → verify fresh session

**File browser:** Open file browser → navigate into directory → create folder → delete folder → verify breadcrumb navigation → back/forward

**Settings:** Open settings → add provider → edit provider → toggle active → delete provider → verify API responses

## Resource Cost Classification

- **Lightweight:** Desktop shell interactions (click, drag, resize) — minimal resource impact
- **Medium:** Terminal with PTY — spawns a shell process (~5-10 MB)
- **Medium:** Multiple agent-browser instances for parallel validation
- **Heavy:** Concurrent terminal sessions with active command execution

## Isolation Notes

- Desktop state is per-user (SQLite on server). Different test users get independent state.
- Terminal PTY sessions are per-WebSocket connection. Each validator gets independent sessions.
- File browser operates on a sandbox directory. Create test files in isolated subdirectories.
- Settings providers are shared across the user's sessions. Validators should create providers with unique names.
