# Mission 7: Cogent Integration + MicroVM Architecture

**Goal:** Properly integrate ChoirOS microVM patterns from `~/choiros-rs` with Cogent work graph from `~/cogent`, replacing the stub implementations with production-ready architecture.

---

## Mission 6 Post-Mortem: What Actually Works

### ✅ Delivered (Working)
1. **Desktop Shell** — Floating icons, bottom bar, floating windows, responsive layout
2. **Terminal** — ghostty-web WASM, PTY WebSocket (fixed protocol issues)
3. **Browser** — iframe-based (limited by X-Frame-Options, documented)
4. **Cross-area integration** — All test selectors updated for M6 rewrite

### ⚠️ Stub/Incorrect Implementations
1. **File Browser** — Uses host directory (`/tmp/go-choir-files`). Should use per-user microVM filesystem.
2. **Settings** — Not implemented. Should be per-user preferences in microVM, consumed by conductor agent.
3. **Browser** — iframe approach hits X-Frame-Options. Needs server-side proxy with HTML rewriting.

---

## Architecture Source of Truth

### `~/choiros-rs` (Rust reference — working microVMs)
**Key patterns to port:**
- Firecracker VM lifecycle management (vmctl equivalent)
- Per-user microVM with isolated filesystem
- Read-only shared filesystem mounting
- Snapshot/restore for fast VM resume
- VM-to-VM networking isolation

**File locations to study:**
- `dioxus-desktop/src/` — UI patterns already ported to Svelte
- `sandbox/src/` — Actor runtime patterns (vm management, channels)
- Internal VM spawn logic

### `~/cogent` (Go work control plane)
**Key patterns to integrate:**
- Work graph (SQLite-backed DAG)
- Agent session persistence
- Tool calling loop
- Work item state machine
- Co-agent spawning

**File locations to study:**
- `internal/adapters/native/` — Tool loop implementation
- `internal/service/` — Work graph orchestration
- `internal/store/` — SQLite schema patterns

---

## Mission 7 Deliverables

### 1. MicroVM Filesystem (File Browser Done Right)

**Current (Wrong):**
```
Host: /tmp/go-choir-files/ (shared across all users)
  ↓
Sandbox API: GET /api/files → returns host directory listing
```

**Target (Correct):**
```
Per-user microVM: /home/user/ (isolated filesystem)
  ↓
vmctl spawns Firecracker VM per user
  ↓
Read-only shared mount: /shared/ (common tools, etc.)
  ↓
Sandbox inside VM: GET /api/files → returns VM filesystem
```

**Implementation:**
- Port VM lifecycle from `~/choiros-rs`
- Create base VM image with sandbox + filesystem
- Per-user VM spawn on first request
- File browser backend runs inside VM (not host)
- Proxy routes file API to user's VM

### 2. Browser Proxy (Browser Done Right)

**Current (Limited):**
```
BrowserApp.svelte: <iframe src="https://example.com">
  ↓
Blocked by X-Frame-Options on most sites
```

**Target (Working):**
```
BrowserApp.svelte: <iframe src="/api/browser/proxy?url=example.com">
  ↓
Proxy endpoint: fetches URL, rewrites HTML
  ↓
Rewrites: URLs → /api/browser/proxy?url=..., cookies isolated per user
  ↓
Returns modified HTML that loads in iframe
```

**Implementation:**
- New endpoint: `GET /api/browser/proxy?url=...`
- HTML parsing/rewriting (goquery or similar)
- URL rewriting: relative → absolute → proxy path
- Cookie jar isolation per user
- CSP header stripping

### 3. Cogent Integration

**Pattern:** Cogent is the work control plane, not inside Choir.

```
User request → Choir proxy → Cogent supervisor
                    ↓
              [Work graph: SQLite]
                    ↓
            Spawns workers in microVMs
                    ↓
              Results stream back
```

**Integration points:**
1. **Proxy routing** — `/api/cogent/*` → Cogent supervisor
2. **Auth sharing** — JWT tokens shared between Choir and Cogent
3. **Work graph UI** — E-text shows work progress inline
4. **Agent spawning** — Conductor agent creates work items, spawns workers

**Key differences from stub scheduler:**
- Real work graph persistence (Dolt/SQLite)
- Real co-agent spawning with verification
- Real attestation before work marked complete
- Long-running work survives VM hibernation

### 4. Settings (Per-User Preferences)

**Location:** Inside microVM, persisted across hibernation.

```
User Settings (SQLite in microVM):
- preferred_models: ["claude-sonnet", "gpt-4"]
- budget_constraints: {monthly_limit: 100}
- policy_text: "Use cheapest model that can handle the task"

Conductor agent reads → routes to appropriate model
```

**Integration:**
- Settings UI in desktop (reads/writes VM-local SQLite)
- Conductor agent queries settings before routing
- Gateway respects user model preferences

---

## Technical Debt from M6 to Clean Up

1. **File browser API** — Remove host-based implementation
2. **Terminal protocol** — Verify fixed protocol is stable
3. **Browser** — Remove iframe-direct approach, implement proxy
4. **VMctl** — Replace stub with real Firecracker management

---

## Verification Approach

1. **Unit tests** — Go tests for VM lifecycle, proxy rewriting
2. **E2E tests** — Playwright: spawn VM, open file browser, see isolated FS
3. **Integration tests** — Cogent work graph → spawn worker → verify result
4. **Manual verification** — Browser proxy loads Google, GitHub, etc.

---

## Migration Path

### Phase 1: MicroVM Foundation
- Port `~/choiros-rs` VM lifecycle to go-choir
- Create base VM image with sandbox
- Verify VM spawn/destroy works

### Phase 2: File Browser Fix
- Move file browser backend into VM
- Update proxy routing to user's VM
- E2E test: isolated filesystem per user

### Phase 3: Browser Proxy
- Implement `/api/browser/proxy`
- HTML rewriting logic
- E2E test: Google loads in iframe via proxy

### Phase 4: Cogent Integration
- Add Cogent supervisor endpoint
- Connect work graph to microVM workers
- E2E test: long-running work survives VM hibernation

---

## Reference Material

- `~/choiros-rs/docs/` — Architecture decisions
- `~/choiros-rs/sandbox/src/` — VM spawn patterns
- `~/cogent/docs/architecture.md` — Work graph design
- `~/cogent/internal/adapters/native/` — Tool loop

---

## Success Criteria

1. ✅ File browser shows per-user isolated filesystem (microVM)
2. ✅ Browser loads any site via proxy (not just Wikipedia)
3. ✅ Cogent work graph spawns workers in microVMs
4. ✅ Settings UI manages per-user preferences
5. ✅ All prior M6 functionality preserved

---

**Status:** Planned, ready for Cogent implementation
