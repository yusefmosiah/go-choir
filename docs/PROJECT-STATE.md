# go-choir Project State

**Last Updated:** 2026-04-15  
**Current Mission:** Make local `vtext` + MAS real before `vmctl` deepening

---

## Project Vision

**go-choir** is a distributed multi-agent operating system - unifying **ChoirOS** (web desktop, microVMs) + the strongest runtime patterns we learned while building **Cogent** (hierarchical MAS, long-horizon work, resumability).

### Why Cogent Matters

Cogent remains important as a source of runtime patterns and temporary bootstrap leverage while we finish choir-in-choir locally:

**Cogent's 3-Tier Hierarchy:**
1. **Host** - The user-facing appagent that owns canonical document state (for now, the future `vtext` appagent)
2. **Supervisor** - LLM-driven orchestrator that claims work and delegates
3. **Workers** - Including verifiers that check work before attestation

**Key cogent patterns for choir:**
- **Work graph:** SQLite-backed DAG of work items, edges, attestations
- **Overnight coding runs:** Long-horizon work with resume capability
- **Attestation:** Work isn't "done" until verified with evidence
- **Database:** Should be **Dolt** (not SQLite) for version control

### 3-Tier Architecture (Unified Vision)

**Control Plane (Host/Hypervisor):**
- Auth service (WebAuthn, JWT)
- Proxy (routing to user microVMs)
- Gateway (LLM provider + **web search** proxy)
- VM lifecycle management (vmctl)

**Runtime Plane (Per-User MicroVM):**
- One microVM per user with choir runtime services and appagents
- One `super` agent per microVM coordinates execution work and may fan out via coagent tools
- Worker roles include coding, researcher and verification-style helpers, with configurable researcher count from day one
- **AppAgents** - `vtext` is the primary document appagent
- Database direction: **DoltDB** for version-native document and work state

**Client Plane (Browser):**
- Web desktop UI (Svelte)
- Shows work progress inline in `vtext`
- Communicates via HTTP/WebSocket → Proxy → Sandbox runtime

### Agent Hierarchy (Current Direction)

**`vtext` AppAgent** - User-facing, owns canonical document state and rewrites versions  
**`super` Agent** - LLM-driven execution coordinator, delegates and may spawn coagents  
**Workers** - Execute tasks, report results, may spawn coagents where allowed  
**Verifiers** - Check work quality before attestation where verification matters

**Key principle:** the document is the living state of the work. Users and the `vtext` appagent are canonical editors; workers read and report but do not directly edit canonical text.

### MicroVM + Cogent Lifecycle

- **Per-user microVM:** Contains choir runtime, `super`, workers, and DoltDB-backed state
- **Snapshot-based:** VMs load from snapshots (fast resume)
- **Background forking:** For choir-in-choir, fork VM to build new app in background
- **Dolt database:** Version-controlled work graph survives hibernation

**Key architectural principle:** Agents may always stop, the system may always resume.

---

## Mission History

### Mission 1-3: Foundation
- Deploy pipeline to Node B (OVH)
- Auth service with WebAuthn
- Proxy/gateway/vmctl infrastructure
- Versioned document app (`vtext`)
- Firecracker microVM support

### Mission 4: Core Functionality ✓ COMPLETE
**Status:** 62/62 validation assertions passed

**Delivered:**
1. ✅ **Auth re-login bug fixed** - CredentialFlags now persist (root cause was missing flags in DB)
2. ✅ **Email-based auth migration** - Username dropped, email primary identifier
3. ✅ **Multi-provider LLM gateway** - Fireworks, Z.AI, Bedrock routing with SSE streaming
4. ✅ **Tool calling validation** - file_read tool working end-to-end
5. ✅ **Choir-in-choir (minimal)** - Scheduler, spawn API, parent-child tasks, early `vtext`-based experimentation
6. ✅ **All tests passing** - 127+ tests across 13 packages

**Deployed to Node B:** Code is live but needs verification (see Mission 5)

### Mission 6: Desktop UX Rewrite ✓ COMPLETE
**Status:** 10/13 features completed (3 cancelled/deferred), 168+ Playwright tests passing

**Design pivot mid-mission:** Left rail → floating desktop icons (traditional OS UX)

**Delivered (8 features):**
1. ✅ **Floating desktop icons** - Draggable app icons with position persistence, Show Desktop button
2. ✅ **Bottom bar** - Prompt input, minimized window indicators, user info, connection status
3. ✅ **Floating windows** - Bottom-right resize, cascade positioning, z-index management, minimize/maximize
4. ✅ **Responsive layout** - 3 breakpoints (desktop/tablet/mobile), same floating window model on small screens
5. ✅ **File browser** - Backend CRUD API (`/api/files`), path traversal protection, frontend component
6. ✅ **Browser app** - iframe-based with URL bar, back/forward/reload, error handling
7. ✅ **Terminal backend** - PTY WebSocket (`/api/terminal/ws`), auth gating, session management
8. ✅ **Terminal frontend** - ghostty-web WASM with dark theme, FitAddon, 10000-line scrollback

**Cancelled/Deferred (3 features):**
1. ⏸️ **Settings backend** - Cancelled — deferred to Mission 7. Settings requires conductor agent for per-user model routing preferences. API keys stay in gateway env vars.
2. ⏸️ **Settings frontend** - Cancelled — depends on settings backend (Mission 7)
3. ✅ **Cross-area integration** - Updated all test selectors for M6 rewrite, 168 tests passing

**Committed:** 9c66be3, e9594c3, 50b84e8, 2c1e6cc

---

## External Repositories

### ~/choiros-rs (Rust reference implementation)
**What it is:** Original ChoirOS in Rust with actor model (ractor), BAML, Dioxus UI  
**Why it matters:** UX patterns, architecture reference, conductor/scheduler concepts  
**Key locations:**
- `dioxus-desktop/src/` - UI patterns to port to Svelte
- `sandbox/src/` - Actor runtime patterns
- `docs/` - Architecture decisions (ADR docs)
- `docs/archive/DESKTOP_ARCHITECTURE_DESIGN.md` - Desktop UX philosophy
- `.cogent/cogent-private.db` - **Secrets and deploy credentials** (SSH keys, API keys)

**How to access credentials:**
```bash
cd ~/choiros-rs
# Query private notes for deploy credentials
sqlite3 .cogent/cogent-private.db "SELECT title, content FROM private_notes WHERE title LIKE '%deploy%' OR title LIKE '%ssh%' OR title LIKE '%ovh%';"
```

### ~/cogent (Go work control plane reference)
**What it is:** Local work control plane for governed agent software engineering  
**Why it matters:** Tool loop pattern, work graph, co-agent spawning  
**Key locations:**
- `internal/adapters/native/` - Tool calling loop implementation
- `internal/service/` - Work graph orchestration
- `internal/store/` - SQLite schema patterns
- `docs/architecture.md` - System design

**Key concepts copied to go-choir:**
- ToolRegistry pattern
- Agent session persistence
- Channel-based messaging
- Work item state machine

---

## Critical Information Locations

### Secrets & Credentials
| Secret | Location | Access |
|--------|----------|--------|
| Node B SSH key | `~/.ssh/` or `~/choiros-rs/.cogent/cogent-private.db` | SSH to 147.135.70.196 |
| Fireworks API key | `~/choiros-rs/.cogent/cogent-private.db` | Needed for Mission 5 |
| Z.AI API key | `~/choiros-rs/.cogent/cogent-private.db` | Needed for Mission 5 |
| AWS Bedrock keys | `~/choiros-rs/.cogent/cogent-private.db` | Production LLM |
| Auth signing key | Generated on Node B at `/var/lib/go-choir/auth-signing/` | Auto-generated |

### Node B (Production)
- **IP:** 147.135.70.196
- **URL:** https://draft.choir-ip.com
- **SSH:** `ssh root@147.135.70.196` (key in choiros-rs private notes)
- **OS:** NixOS
- **Services:** auth(8081), proxy(8082), vmctl(8083), gateway(8084), sandbox(8085)

### Repository Structure
```
~/go-choir/
├── cmd/
│   ├── auth/        # WebAuthn + JWT service
│   ├── proxy/       # Auth-gated reverse proxy
│   ├── gateway/     # LLM provider routing
│   ├── vmctl/       # Firecracker VM lifecycle
│   └── sandbox/     # Runtime + scheduler + apps
├── internal/
│   ├── auth/        # Handlers, store, WebAuthn
│   ├── gateway/     # Multi-provider routing
│   ├── runtime/     # Task execution, tool loop, channels
│   ├── scheduler/   # Work registry (in progress)
│   └── store/       # SQLite runtime state + embedded Dolt vtext persistence
├── frontend/        # Svelte SPA
│   ├── src/lib/
│   │   ├── Desktop.svelte
│   │   ├── VTextEditor.svelte
│   │   └── [desktop/runtime components under active refinement]
│   └── tests/       # Playwright tests
├── nix/
│   └── node-b.nix   # NixOS config for Node B
└── docs/
    ├── mission-4-core-functionality-and-choir-in-choir.md
    ├── mission-5-production-hardening-and-polish.md
    └── mission-6-desktop-ux-rewrite.md
```

---

## Current Prompt Flow

This is the live prompt path today, not the intended path:

1. **Bottom prompt bar**
   - `frontend/src/lib/BottomBar.svelte` emits `promptsubmit`
   - `frontend/src/lib/Desktop.svelte` handles it
   - trivial prompts like `hi` are short-circuited to a toast
   - non-trivial prompts call `submitConductorPrompt(...)` in `frontend/src/lib/conductor.js`

2. **Conductor submission**
   - `submitConductorPrompt(...)` posts to `/api/agent/task`
   - metadata includes:
     - `agent_profile=conductor`
     - `agent_role=conductor`
     - `requested_app=vtext`
     - `seed_prompt=<prompt>`
   - this creates a real conductor task in the runtime

3. **But the UI still bypasses conductor results**
   - after submission, `Desktop.svelte` immediately opens a new `vtext` window locally
   - that window is seeded directly with the prompt text as `v0`
   - conductor is therefore not yet the actual owner of app routing in practice

4. **`vtext` prompt/apply**
   - `frontend/src/lib/VTextEditor.svelte` saves a user revision
   - then it calls `/api/vtext/documents/{id}/agent-revision`
   - the backend builds the real provider prompt in `internal/runtime/vtext.go`

5. **Runtime execution**
   - `internal/runtime/runtime.go` runs the task through the tool loop when a tool registry exists
   - `internal/runtime/tool_profiles.go` supplies:
     - `conductor`: coagent tools only
     - `vtext`: coagent tools only
     - `researcher`: research + file + coagent tools
     - `super`: coding + research + file + coagent tools
   - the tool loop system prompt comes from `systemPromptForTask(...)`

6. **Why the behavior still feels fake**
   - conductor is not yet actually driving the desktop/app routing result
   - `vtext` is still mostly instructed by prompt text alone, not by a strong structured orchestration contract
   - the trace surface exists, but it is still too raw to make delegation legible at a glance
   - as a result, the system can spawn agents in principle, but the user experience does not yet make that orchestration visible or reliable

## Prompt Surfaces To Edit

These are the important prompt/system-prompt surfaces right now:

1. **Conductor user prompt payload**
   - `frontend/src/lib/conductor.js`
   - controls what metadata gets attached when the prompt bar submits a conductor task

2. **Desktop routing behavior**
   - `frontend/src/lib/Desktop.svelte`
   - not a prompt, but it currently determines that prompt-bar input opens `vtext` directly instead of waiting for conductor output

3. **`vtext` frontend agent prompt**
   - `frontend/src/lib/VTextEditor.svelte`
   - `buildAgentPrompt()`
   - this is the first editable text telling the `vtext` agent how to behave

4. **`vtext` backend revision prompt**
   - `internal/runtime/vtext.go`
   - `buildAgentRevisionPrompt(...)`
   - this is the canonical backend prompt that wraps the current document and user request

5. **Per-profile system prompts**
   - `internal/runtime/tool_profiles.go`
   - `systemPromptForTask(...)`
   - this is where `conductor`, `vtext`, `researcher`, and `super` get their core role instructions

6. **Tool surfaces**
   - not prompts, but prompt behavior depends heavily on them:
   - `internal/runtime/tools_coagent.go`
   - `internal/runtime/tools_research.go`
   - `internal/runtime/tools_coding.go`

## Current Issues & Blockers

### Production (Node B)
1. ✅ **Auth database schema** - Resolved in Mission 4
2. ✅ **Provider credentials** - Configured in Mission 5
3. **Terminal integration** - ghostty-web loaded but full PTY lifecycle needs verification on Node B

### Codebase (Mission 6 addressed most issues)
1. ✅ **Top bar → floating icons** - Resolved (floating desktop icons, draggable, position persistence)
2. ✅ **Missing bottom bar** - Resolved (prompt input, window indicators, user info)
3. ✅ **Not responsive** - Resolved (3 breakpoints, desktop-parity windowing on mobile-sized screens)
4. ⏸️ **Settings app** - Deferred to Mission 7 (requires conductor agent)
5. ⚠️ **Browser app** - Limited by iframe security (X-Frame-Options). Only sites allowing embedding (like Wikipedia) work. Full proxy solution deferred to Mission 7+ (requires server-side proxy with HTML rewriting).
6. ⚠️ **VText UX + orchestration** - hard cutover is not actually coherent yet; complete locally before `vmctl`
7. ⚠️ **Trace UX** - current trace is useful as raw instrumentation, but not yet readable enough to debug runs comfortably
8. ⚠️ **Prompt flow mismatch** - conductor submits a real task, but the desktop still opens `vtext` directly rather than letting conductor route the work

### Testing
- ✅ Unit tests pass (all Go packages)
- ✅ Playwright tests pass (233 tests)
- ⚠️ Settings app validation deferred to cogent
- ⚠️ Production WebAuthn requires manual testing (can't automate biometric auth)

---

## Next Missions

### Mission 5: Production Hardening (COMPLETED)
**Status:** Verified auth, gateway, proxy operational. Provider credentials configured.

### Mission 6: Desktop UX Rewrite (COMPLETE)
**Delivered:** 10/13 features (see Mission History above)

### Immediate Local Sequence (NEXT)
**Goal:** Finish the local `vtext` core, make the MAS visibly real, then return to `vmctl` / microVM lifecycle.

**Why this comes first:**
1. The current `vtext` UI is still too rough and wastes space.
2. The MAS exists in pieces, but the user cannot clearly see or trust delegation.
3. The trace/debugging surface needs to make worker spawning, tool usage, and channel traffic legible.
4. The sandbox state model should stabilize around embedded Dolt before deeper VM work.
5. `vmctl` work should happen after the sandbox runtime and document model are the ones we actually want to run inside VMs.

**Current local priority order:**
1. Make `vtext` feel right:
   - the window should essentially be the document
   - floating prompt + version controls only
   - no dead space, no misleading status chrome
2. Make prompt routing honest:
   - conductor should become the actual routing owner
   - remove the “submit conductor, then just open `vtext` anyway” mismatch
3. Make the MAS visibly real:
   - `vtext` must reliably spawn researchers for current/external info
   - `super` should appear when execution work is needed
   - runs should produce understandable trace output
4. Improve Trace:
   - make it show runs/families clearly
   - show which agent spawned which child
   - show tool calls, channel messages, and final synthesis in a simpler narrative order
5. Finish embedded Dolt integration:
   - version history should be genuinely native to the sandbox state model
   - document revisions and related metadata should feel like first-class persisted state
6. Then return to `vmctl`:
   - review current Go implementation
   - compare against `choiros-rs`
   - design the right user-VM / worker-VM lifecycle

### Mission 7: MicroVM Architecture + Runtime Deepening (AFTER LOCAL `VTEXT`/MAS/DOLT)
**Goal:** Deepen `vmctl` and per-user microVM architecture once the local `vtext` + MAS loop is real and inspectable.

**Doc:** `docs/mission-7-cogent-integration.md`

**Key Deliverables:**
1. **MicroVM Filesystem** — Per-user isolated filesystem (port from `~/choiros-rs`)
2. **Browser Proxy** — Server-side proxy with HTML rewriting (fix iframe limitations)
3. **Cogent Integration** — Work graph, agent spawning, long-running work
4. **Settings UI** — Per-user preferences in microVM, consumed by conductor

**Architectural Sources:**
- `~/choiros-rs/sandbox/src/` — VM lifecycle patterns
- `~/cogent/internal/adapters/native/` — Tool calling loop
- `~/cogent/internal/service/` — Work graph orchestration

**Implementation note:** Cogent is a reference and bootstrap donor, not the target control plane.

## Technical Debt To Track Explicitly

### Product / Runtime Debt
1. **Conductor is not yet authoritative**
   - desktop still shortcuts into `vtext`
   - routing and execution are split between deterministic UI code and agent tasks
2. **Trace app is not yet readable enough**
   - current version is instrumentation-first, not operator-first
   - needs better grouping, labels, and summaries
3. **`vtext` orchestration is prompt-fragile**
   - delegation currently depends too much on freeform prompt behavior
   - it needs stronger policy and clearer worker plans
4. **Prompt surfaces are scattered**
   - frontend prompt text, backend prompt text, and profile system prompts all shape behavior
   - this should eventually be rationalized into a more intentional prompt/config layout

### Architecture / Infra Debt
1. **`vmctl` is still under-validated**
   - local host-process fallback exists
   - real x86 Firecracker validation remains to be done
2. **Docs are still partly aspirational**
   - several mission docs still describe stale implementation paths
3. **Factory Droid residue still exists**
   - `.factory` bootstrap assumptions
   - old mission artifacts and references
   - stale comments and docs that imply Factory-era workflows
4. **README is stale in a few important ways**
   - it still describes the sandbox/runtime as more placeholder-oriented than the current repo
   - it does not yet explain the current prompt flow / MAS debugging path clearly

### Mission 5 (revisit): Production Hardening (if needed)

**Doc:** `docs/mission-5-production-hardening-and-polish.md`

**Key tasks:**
1. Verify auth schema migration on Node B (or clean reset)
2. Deploy Fireworks + Z.AI API keys
3. Test: register → login → `vtext` → LLM prompt → response

**Blockers:** None (just needs execution)

### Mission 6: VText UX + Choir-in-Choir (PLANNED)
**Goal:** Realize the full vision - single editor UX + background app building

**VText Vision:**
- Single responsive text editor, no sidebars
- User prompt = Version 0
- Agent creates Version 1, spawns workers
- Workers message back, agent creates subsequent versions
- Users "reprompt" by editing text inline anywhere
- Citations, metadata integrated into text (not sidebar)
- Show artifacts (images, videos, audio, interactive elements) inline

Note: the rename is no longer just a docs convention. Completing the active `etext` → `vtext` rename as a hard cutover is part of the immediate local work so the UI, runtime, and storage layers all describe the same app.

**Choir-in-Choir:**
- Fork microVM to build new apps in background
- Stream progress/artifacts to vtext
- Switch to new app when ready

**Reference:** `~/choiros-rs/docs/archive/DESKTOP_ARCHITECTURE_DESIGN.md`

---

## Quick Reference Commands

### Local Development
```bash
cd ~/go-choir
source start-services.sh        # Start all services locally
go test ./... -count=1 -p 4     # Run all tests
cd frontend && pnpm test        # Run Playwright tests
```

### Node B Deploy
```bash
# Check CI status
gh run list --branch main --limit 5

# SSH to Node B (credentials in ~/choiros-rs/.cogent/cogent-private.db)
ssh root@147.135.70.196

# On Node B, check service status
systemctl status go-choir-auth
journalctl -u go-choir-auth -f

# Clean auth database if needed
rm /var/lib/go-choir/auth/auth.db
systemctl restart go-choir-auth

# Full rebuild
nixos-rebuild switch
```

### Access Cogent Private Notes (Secrets)
```bash
cd ~/choiros-rs
# List all private notes
sqlite3 .cogent/cogent-private.db "SELECT title FROM private_notes;"

# Search for deploy-related
sqlite3 .cogent/cogent-private.db "SELECT title, content FROM private_notes WHERE title LIKE '%deploy%' OR title LIKE '%ssh%' OR title LIKE '%ovh%';"
```

---

## Key Decisions Log

| Decision | Date | Rationale |
|----------|------|-----------|
| Hard cutover for auth schema | 2026-04-12 | No real users yet, cleaner than migration |
| Pull Dolt earlier for `vtext` | 2026-04-13 | Version-native document behavior is core to the product, not a late-stage migration |
| Minimal choir-in-choir | Mission 4 | Full Conductor/Scheduler deferred to Mission 6+ |
| UX rewrite as Mission 6 | 2026-04-12 | Current UX fundamentally wrong, needs full rewrite |
| Passkeys with email | Mission 4 | WebAuthn preserved, just changed identifier |

---

## Contacts & Resources

- **ChoirOS-RS reference:** `~/choiros-rs/docs/`
- **Cogent reference:** `~/cogent/docs/architecture.md`
- **Node B:** https://draft.choir-ip.com (147.135.70.196)
- **Mission artifacts:** `~/.factory/missions/0411ccd8-943c-4cab-92ab-9528cccd79b5/`

---

## How to Continue

1. **Read Mission 5 doc:** `docs/mission-5-production-hardening-and-polish.md`
2. **Get Node B credentials:** Query `~/choiros-rs/.cogent/cogent-private.db`
3. **Verify deploy:** Test auth registration on https://draft.choir-ip.com
4. **Add provider keys:** Update `nix/node-b.nix`, rebuild
5. **Test end-to-end:** Full flow from auth → `vtext` → LLM

**For questions:** Reference this doc, then specific mission docs, then external repos (choiros-rs, cogent).
