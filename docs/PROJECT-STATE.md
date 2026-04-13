# go-choir Project State

**Last Updated:** 2026-04-13  
**Current Mission:** 6 Partially Complete → Cogent for remaining work

---

## Project Vision

**go-choir** is a distributed multi-agent operating system - unifying **ChoirOS** (web desktop, microVMs) + **Cogent** (hierarchical MAS, overnight runs).

### Why Cogent Matters

Cogent provides the **hierarchical multi-agent system** that enables "choir in choir":

**Cogent's 3-Tier Hierarchy:**
1. **Host** - The agent that calls cogent (e.g., E-Text AppAgent)
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
- One microVM per user with **full cogent system inside**
- **Cogent Supervisor** - Claims work, delegates to workers
- **Cogent Workers** - Terminal (bash), Researcher (web search via gateway), Verifiers
- **AppAgents** - E-Text (orchestrates via cogent supervisor)
- Database: **Dolt** (version-controlled work graph)

**Client Plane (Browser):**
- Web desktop UI (Svelte)
- Shows cogent work progress inline in etext
- Communicates via HTTP/WebSocket → Proxy → Sandbox → Cogent

### Agent Hierarchy (Cogent-Powered)

**Host (E-Text AppAgent)** - User-facing, owns document state, calls cogent  
**Cogent Supervisor** - LLM-driven, claims ready work from work graph, delegates  
**Cogent Workers** - Execute tasks, report results, may spawn coagents  
**Verifiers** - Check work quality before attestation

**Key principle:** Cogent enables long-horizon work. User can say "build me an app" and come back tomorrow to find it done with full history.

### MicroVM + Cogent Lifecycle

- **Per-user microVM:** Contains full cogent system (supervisor, workers, Dolt DB)
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
- E-text app with versioned storage
- Firecracker microVM support

### Mission 4: Core Functionality ✓ COMPLETE
**Status:** 62/62 validation assertions passed

**Delivered:**
1. ✅ **Auth re-login bug fixed** - CredentialFlags now persist (root cause was missing flags in DB)
2. ✅ **Email-based auth migration** - Username dropped, email primary identifier
3. ✅ **Multi-provider LLM gateway** - Fireworks, Z.AI, Bedrock routing with SSE streaming
4. ✅ **Tool calling validation** - file_read tool working end-to-end
5. ✅ **Choir-in-choir (minimal)** - Scheduler, spawn API, parent-child tasks, etext research button
6. ✅ **All tests passing** - 127+ tests across 13 packages

**Deployed to Node B:** Code is live but needs verification (see Mission 5)

### Mission 6: Desktop UX Rewrite ⚠️ PARTIALLY COMPLETE
**Status:** 8/13 features completed, 233 Playwright tests passing, all Go tests passing

**Design pivot mid-mission:** Left rail → floating desktop icons (traditional OS UX)

**Delivered (8 features):**
1. ✅ **Floating desktop icons** - Draggable app icons with position persistence, Show Desktop button
2. ✅ **Bottom bar** - Prompt input, minimized window indicators, user info, connection status
3. ✅ **Floating windows** - Bottom-right resize, cascade positioning, z-index management, minimize/maximize
4. ✅ **Responsive layout** - 3 breakpoints (desktop/tablet/mobile), single-focus mobile mode
5. ✅ **File browser** - Backend CRUD API (`/api/files`), path traversal protection, frontend component
6. ✅ **Browser app** - iframe-based with URL bar, back/forward/reload, error handling
7. ✅ **Terminal backend** - PTY WebSocket (`/api/terminal/ws`), auth gating, session management
8. ✅ **Terminal frontend** - ghostty-web WASM with dark theme, FitAddon, 10000-line scrollback

**Not completed (3 features):**
1. ❌ **Settings backend** - Worker spawn crashes prevented implementation (runtime LLM provider CRUD)
2. ❌ **Settings frontend** - Depends on settings backend
3. ❌ **Cross-area integration** - Deploy-readiness tests, cross-area flows

**Blocker:** settings-backend worker crashed repeatedly (10+ spawn attempts, exit code 0). User decided to switch to cogent for remaining work.

**Committed:** 9c66be3, e9594c3, 50b84e8

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
│   └── store/       # SQLite persistence
├── frontend/        # Svelte SPA
│   ├── src/lib/
│   │   ├── Desktop.svelte
│   │   ├── ETextEditor.svelte
│   │   └── [components to rewrite in Mission 6]
│   └── tests/       # Playwright tests
├── nix/
│   └── node-b.nix   # NixOS config for Node B
└── docs/
    ├── mission-4-core-functionality-and-choir-in-choir.md
    ├── mission-5-production-hardening-and-polish.md
    └── mission-6-desktop-ux-rewrite.md
```

---

## Current Issues & Blockers

### Production (Node B)
1. ✅ **Auth database schema** - Resolved in Mission 4
2. ✅ **Provider credentials** - Configured in Mission 5
3. **Terminal integration** - ghostty-web loaded but full PTY lifecycle needs verification on Node B

### Codebase (Mission 6 addressed most issues)
1. ✅ **Top bar → floating icons** - Resolved (floating desktop icons, draggable, position persistence)
2. ✅ **Missing bottom bar** - Resolved (prompt input, window indicators, user info)
3. ✅ **Not responsive** - Resolved (3 breakpoints, mobile single-focus mode)
4. ❌ **Settings app** - Not implemented (blocked by worker spawn issues)
5. ❌ **E-text UX** - Not addressed (Mission 7 scope)

### Testing
- ✅ Unit tests pass (all Go packages)
- ✅ Playwright tests pass (233 tests)
- ⚠️ Settings app validation deferred to cogent
- ⚠️ Production WebAuthn requires manual testing (can't automate biometric auth)

---

## Next Missions

### Mission 5: Production Hardening (COMPLETED)
**Status:** Verified auth, gateway, proxy operational. Provider credentials configured.

### Mission 6: Desktop UX Rewrite (PARTIALLY COMPLETE)
**Delivered:** 8/13 features (see Mission History above)
**Remaining for Cogent:**
- Settings backend (runtime LLM provider CRUD, encrypted API keys)
- Settings frontend (provider management UI)
- Cross-area integration (deploy-readiness tests)

### Mission 7: E-Text + Choir-in-Choir (NEXT)
**Goal:** Realize full vision - single editor UX + background app building

**E-Text Vision:**
- Single responsive text editor, no sidebars
- User prompt = Version 0
- Agent creates Version 1, spawns workers
- Workers message back, agent creates subsequent versions
- Users "reprompt" by editing text inline anywhere

**Choir-in-Choir:**
- Fork microVM to build new apps in background
- Stream progress/artifacts to etext
- Switch to new app when ready

**Reference:** `~/choiros-rs/docs/archive/DESKTOP_ARCHITECTURE_DESIGN.md`

### Mission 5 (revisit): Production Hardening (if needed)

**Doc:** `docs/mission-5-production-hardening-and-polish.md`

**Key tasks:**
1. Verify auth schema migration on Node B (or clean reset)
2. Deploy Fireworks + Z.AI API keys
3. Test: register → login → etext → LLM prompt → response

**Blockers:** None (just needs execution)

### Mission 6: E-Text UX + Choir-in-Choir (PLANNED)
**Goal:** Realize the full vision - single editor UX + background app building

**E-Text Vision:**
- Single responsive text editor, no sidebars
- User prompt = Version 0
- Agent creates Version 1, spawns workers
- Workers message back, agent creates subsequent versions
- Users "reprompt" by editing text inline anywhere
- Citations, metadata integrated into text (not sidebar)
- Show artifacts (images, videos) inline

**Choir-in-Choir:**
- Fork microVM to build new apps in background
- Stream progress/artifacts to etext
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
| Defer Dolt migration | Mission 4 | SQLite sufficient for now, Dolt in future |
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
5. **Test end-to-end:** Full flow from auth → etext → LLM

**For questions:** Reference this doc, then specific mission docs, then external repos (choiros-rs, cogent).
