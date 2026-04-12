# go-choir Project State

**Last Updated:** 2026-04-12  
**Current Mission:** 4 Complete → Starting Mission 5

---

## Project Vision

**go-choir** is a distributed multi-agent operating system - a Go rewrite of ChoirOS (Rust), unified with Cogent's capabilities. The goal is a web desktop where:

- **Each user has their own microVM** containing their data, apps, and agent runtime
- **Conductor (prompt bar)** receives user input and routes to AppAgents within the user's microVM
- **AppAgents** (like etext) handle domain-specific work within the user's microVM
- **Workers** spawn as separate microVMs that autoscale for code execution (not terminal agents themselves)
- **Web desktop UX** follows ChoirOS pattern: desktop icons, floating windows, bottom prompt bar

**Key architectural principle:** Agents may always stop, the system may always resume.

**Correction from choiros-rs:** User data lives in their dedicated microVM. The system autoscales worker execution to separate microVMs, but terminal agents and AppAgents run within the user's primary microVM.

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
1. **Auth database schema** - Old `username` column may still exist; migration should fix but needs verification
2. **Provider credentials** - Fireworks/Z.AI keys not deployed; LLM calls fail

### Codebase
1. **UX is wrong** - Current implementation has:
   - Top bar with apps (should be desktop icons)
   - E-text has research button + sidebar (should be simple editor)
   - Missing prompt bar at bottom (conductor)
   - Not responsive for mobile

### Testing
- ✅ Unit tests pass
- ✅ Playwright tests pass (virtual authenticator for WebAuthn)
- ⚠️ Production WebAuthn requires manual testing (can't automate biometric auth)

---

## Next Missions

### Mission 5: Production Hardening (IN PROGRESS)
**Goal:** Fix Node B deployment, add provider credentials, verify end-to-end flow

**Doc:** `docs/mission-5-production-hardening-and-polish.md`

**Key tasks:**
1. Verify auth schema migration on Node B (or clean reset)
2. Deploy Fireworks + Z.AI API keys
3. Test: register → login → etext → LLM prompt → response

**Blockers:** None (just needs execution)

### Mission 6: Desktop UX Rewrite (PLANNED)
**Goal:** Rewrite frontend to match ChoirOS desktop paradigm

**Doc:** `docs/mission-6-desktop-ux-rewrite.md`

**Key tasks:**
1. Desktop icons on left rail (not top bar)
2. Floating, draggable, resizable windows
3. Prompt bar at bottom (conductor)
4. Simple e-text editor (no research button, no sidebar)
5. Responsive for mobile/tablet/desktop

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
