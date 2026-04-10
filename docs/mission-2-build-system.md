# Mission 2: Build the System — Incremental Service Implementation

## Goal

Build go-choir's 5 services incrementally, deploying and verifying each on Node B (draft.choir-ip.com) as they're completed. Each milestone adds real functionality that can be tested on the deployed infrastructure from Mission 1.

## Context

- Mission 1 established the deploy pipeline: push to `main` → build → deploy to Node B
- The architecture spec is at `docs/architecture.md` (2000+ lines, comprehensive)
- Dolt integration research is at `docs/research-dolt.md`
- Valuable code to copy from cogent: `/Users/wiz/cogent/internal/` (LLM clients, tool loop, store patterns, types)
- Valuable patterns from choiros-rs: `/Users/wiz/choiros-rs/` (actor hierarchy, provider gateway, VM management, WebAuthn)
- OVH deploy details in `/Users/wiz/choiros-rs/.cogent/cogent-private.db`

## Architecture Reference

See `docs/architecture.md` for the full spec. Key sections:
- Section 3: OS Layer (conductor, scheduler, agent runtime, persistence, authority, provider gateway, auth, proxy, vmctl)
- Section 4: App Layer (appagent contract, worker contract, canonical editing model)
- Section 5: E-Text App (Dolt schema, appagent behavior, API surface)
- Section 6: Agent Runtime Contract (Go channels local, HTTP remote, tool registry)
- Section 3.5a: Store API migration guide (SQLite → Dolt)

## Milestones

### Milestone 1: Auth + Proxy (Foundation)

**Auth service:**
- WebAuthn registration and login (port from choiros-rs hypervisor's `webauthn-rs` logic, reimplement with `go-webauthn/webauthn`)
- Session management (cookie-based, SQLite backend)
- User CRUD

**Proxy service:**
- Session validation (check auth cookies)
- Route requests to sandbox VMs based on user identity
- WebSocket upgrade and proxying
- For now, proxy to a single hardcoded sandbox (real VM routing comes later)

**Svelte frontend:**
- Auth pages (login, register)
- Placeholder desktop shell (empty window manager)
- WebSocket connection to proxy

**Verification:** Register a user, log in, see a placeholder desktop on draft.choir-ip.com.

### Milestone 2: Sandbox Core (Agent Runtime)

**Copy from cogent:**
- `internal/adapters/native/client_anthropic.go` → `internal/runtime/client_anthropic.go`
- `internal/adapters/native/client_openai.go` → `internal/runtime/client_openai.go`
- `internal/adapters/native/loop.go` → `internal/runtime/loop.go`
- `internal/adapters/native/tools*.go` → `internal/runtime/tools*.go`
- `internal/adapters/native/channel.go` → `internal/runtime/channel.go`
- `internal/core/` → `internal/types/`
- `internal/service/events.go` → `internal/store/events.go`

**Adapt for go-choir:**
- Remove adapter abstraction wrapper — the tool-calling loop runs directly as goroutines
- Replace SQLite store with Dolt embedded (`dolthub/driver`)
- Implement the goroutine supervisor (restart strategies, health checks)
- Agent runtime serves an HTTP API (for proxy to route to)

**Sandbox HTTP API:**
- `/health` — health check
- `/api/agent/task` — submit a task to an agent
- `/api/agent/status` — agent status
- `/api/events` — SSE event stream

**Note:** In this milestone, sandbox runs as a regular process on the host (no VM yet). VM isolation comes in Milestone 4.

**Verification:** Send a prompt via the Svelte UI → proxy → sandbox → LLM response appears in the UI.

### Milestone 3: E-Text App + Desktop

**Dolt embedded setup:**
- Initialize per-user Dolt database
- E-text schema: `documents`, `content`, `citations`, `metadata` tables
- Store API: create, read, update, version history, diff, blame

**E-text appagent:**
- Receives user prompts and direct edits
- Delegates research to workers via scheduler
- Commits to Dolt with author attribution (user vs agent)

**Scheduler:**
- Central work registry (Dolt tables: `work_items`, `work_edges`, `attestation_records`)
- Submit work, track status, dispatch workers
- Cross-app observability API

**Conductor:**
- Multi-channel input routing (web UI for now, email/chat later)
- Routes to correct appagent

**Svelte desktop:**
- Window manager (drag, resize, minimize, maximize)
- E-text editor using Pretext for text layout
- Version history viewer
- App launcher

**Verification:** Create an e-text document, edit it, prompt the agent to revise it, view version history with user vs agent attribution — all on draft.choir-ip.com.

### Milestone 4: Gateway + VM Isolation

**Gateway service:**
- Port provider gateway from choiros-rs (`provider_gateway.rs`)
- Multi-provider routing: Anthropic, OpenAI, Bedrock, Z.AI, OpenRouter
- API key injection (sandbox never holds keys)
- Per-sandbox rate limiting

**vmctl service:**
- Firecracker VM lifecycle via `firecracker-go-sdk`
- Boot, stop, hibernate, idle watchdog, memory pressure checks
- VM registry (which VM belongs to which user)
- Internal API for proxy to query VM status

**VM integration:**
- Sandbox binary runs inside Firecracker VMs (not on host)
- Proxy routes to VMs via vsock/virtio-net
- Sandbox calls gateway for LLM requests (no keys in VM)
- NixOS VM images built by Nix (microvm.nix patterns from choiros-rs)

**Verification:** Full end-to-end with VM isolation: user logs in → proxy routes to user's VM → sandbox handles request → gateway proxies LLM call with key injection → response flows back.

### Milestone 5: Polish + Promotion

- Terminal app (PTY inside VMs)
- Files app (sandbox filesystem browser)
- Mind-graph app (port from cogent, work graph visualization)
- Admin API for all host services
- Monitoring and alerting
- Load testing
- Promote go-choir from Node B (draft.choir-ip.com) to Node A (choir-ip.com)

## Key Design Decisions (from architecture spec)

- **No CLI inside sandbox** — agents use Go function calls via ToolRegistry, not CLI subprocesses
- **No external adapters** — all agent execution is in-process via the native tool-calling loop
- **One Dolt per sandbox** — all state (e-text, work graph, sessions, events) in one embedded Dolt database
- **Conductor and scheduler are independent** — conductor routes inputs, scheduler tracks background work
- **Users and appagents are peer canonical editors** — both create canonical versions in Dolt
- **Workers are subordinate** — they send messages/proposals to appagents, never write canonical state directly

## Estimated Complexity

High. This is a multi-month effort spanning 5 milestones. Each milestone should be its own Droid mission (or set of missions) once the deploy pipeline from Mission 1 is operational.
