# Architecture

Mission 3 extends the stable Mission 2 auth/proxy/shell slice into the full Node B system. This document describes the intended high-level system behavior workers should preserve while building the remaining milestones.

## Scope and sequencing

Mission 3 starts with a **deploy-readiness prerequisite**:

1. restore `https://draft.choir-ip.com` so it serves the real auth/shell SPA again
2. keep the public auth/proxy/session contract healthy on Node B

Only after that prerequisite is stable do the four core Mission 3 milestones land:

1. sandbox runtime on the host
2. e-text + real desktop
3. gateway + VM isolation
4. polish on Node B

## Public topology

### Public origin

There is one public browser origin for this mission:

- `https://draft.choir-ip.com`

The browser talks only to this origin. No browser-visible flow should depend on direct service ports or alternate hosts.

### Edge routing

On Node B, Caddy terminates TLS and preserves public prefixes:

- `/auth/*` → auth service
- `/api/*` → proxy service
- `/provider/*` → gateway service when that milestone is active
- `/` → built Svelte frontend

Prefix preservation matters. Services should receive the same public path prefix that the browser uses.

## Service responsibilities

| Service | Port | High-level role in Mission 3 |
| --- | --- | --- |
| auth | 8081 | WebAuthn, cookie-backed session lifecycle, access/refresh renewal, logout |
| proxy | 8082 | Same-origin authenticated ingress for shell, runtime, live streams, and VM-backed routing |
| vmctl | 8083 | Internal VM ownership registry and Firecracker lifecycle control |
| gateway | 8084 | Host-side provider access, credential injection, per-sandbox rate limiting |
| sandbox runtime | 8085 locally first, then inside VMs | Runtime loop, events, app execution, per-user state |

## Stable external contract from Mission 2

These behaviors remain the browser-facing foundation for Mission 3:

- passkey registration and login on `draft.choir-ip.com`
- cookie-backed auth only
- `GET /auth/session` as the rehydration and silent-renewal checkpoint
- protected shell access through proxy-owned same-origin routes
- logout and renewal failure tear down protected state cleanly

Mission 3 builds on this contract; it does not replace it with bearer tokens, direct service ports, or alternate browser origins.

## Runtime architecture

### Sandbox runtime

The sandbox evolves from a placeholder process into the first real runtime surface. During the first Mission 3 milestone it still runs as a **host process**, not yet inside a VM.

The runtime owns:

- task submission
- task status
- live event streaming
- app/runtime execution loops
- per-user persisted state

The runtime loop should be **direct goroutines**, not a subprocess CLI loop and not an external adapter-wrapper process.

### Runtime APIs

The browser-visible runtime surface is reached through proxy-routed same-origin endpoints:

- `POST /api/agent/task`
- `GET /api/agent/status`
- `GET /api/events`

Runtime health remains an internal/service health surface used for service and operator checks:

- `GET /health`

The runtime should return stable task or run identifiers so later status/event lookups correlate to the same accepted work.

### Supervision and durability

The runtime must not rely on one long fragile goroutine. Health, restart behavior, and degraded state need to be externally visible.

Workers should assume:

- supervised runtime components can fail independently
- a degraded runtime must be observable through health/status/events
- accepted work must not disappear silently across restart

## Persistence model

### Per-user state

Mission 3 moves toward **one Dolt database per sandbox/user workspace**. That database becomes the durable source for:

- desktop session state
- app state
- e-text documents and revision history
- scheduler/work graph data
- event or runtime state that must survive restart

### Canonical editing model

Users and appagents are peer canonical editors.

- direct user edits create canonical user-authored revisions
- appagent actions create canonical appagent-authored revisions
- subordinate workers may help produce changes, but they do not become canonical authors

This is a core system invariant and must remain visible in history/blame/provenance surfaces.

## Desktop and app model

### Real desktop shell

The placeholder authenticated shell is replaced by a real desktop that owns:

- launcher
- window lifecycle
- focus/z-order
- drag/resize
- minimize/maximize/restore
- restore of desktop state from persisted sandbox data

Desktop state is not meant to be browser-only ephemeral UI state. It should restore from persisted user state strongly enough to survive page reloads and fresh browser contexts.

### E-Text app

E-text is the first real app and the reference application model for Mission 3.

It spans:

- document creation/open
- direct editing
- appagent-driven revision requests
- revision history
- historical snapshot viewing
- diff
- blame / user-vs-agent attribution

The underlying schema includes:

- `documents`
- `content`
- `citations`
- `metadata`

## Conductor and scheduler

These stay separate.

### Conductor

The conductor owns routing user-facing input to the correct app/runtime destination.

### Scheduler

The scheduler owns background work tracking and graph state:

- queued/running/blocked/completed work
- relationships between work items
- operator/debug visibility into current system activity

Do not collapse conductor and scheduler into one component just because they are both inside the sandbox runtime boundary.

## Gateway and provider access

Provider credentials are a host-side concern.

The architecture goal is:

- browser never sees provider credentials
- guest VM never holds provider credentials
- gateway authenticates sandbox callers
- gateway injects host-side Bedrock/Z.AI credentials first
- gateway later expands to the wider provider matrix
- `/provider/*` is not intended to become a raw browser inference bypass around runtime/proxy boundaries

Mission 3’s staged validation only requires **Bedrock and/or Z.AI first**. The rest of the provider matrix is later gateway work, not an excuse to skip real-provider proof entirely.

## VM architecture

### Ownership

By the VM milestone, each authenticated user routes to their own VM-backed sandbox workspace.

vmctl owns:

- user → VM assignment
- boot/resume
- idle stop or hibernate (VAL-VM-008, VAL-CROSS-116)
- unhealthy guest detection and recovery (VAL-VM-009)
- logout teardown (VAL-VM-008)
- epoch tracking for crash dedup (VAL-CROSS-117)

The vmmanager package (`internal/vmmanager`) provides the concrete Firecracker lifecycle:

- BootVM: launches a Firecracker process with repo-built guest images
- StopVM/HibernateVM: clean shutdown preserving persistent state
- ResumeVM: reboots with same epoch (preserves user state, no dedup concerns)
- RecoverVM: force-kills and reboots with new epoch (prevents duplicate canonical effects)
- CheckHealth: periodic guest health probes

Guest images are Nix-built (`nix build .#guest-image`) and contain only the sandbox binary. Provider credentials are never in guest environment, config, or process args (VAL-VM-011).

### Routing

Once VM routing is active, protected shell/runtime surfaces should resolve through vm ownership rather than a static host sandbox fallback.

The sandbox listener on `8085` is a local/internal runtime listener during host-process milestones, not a browser-facing contract surface.

### Isolation

The VM boundary exists to preserve:

- per-user state isolation
- provider credential isolation
- host control-plane isolation

Guest workloads should not be able to reach host-only control-plane surfaces, sensitive sockets, or host secret paths.

## Operations and admin surfaces

Mission 3 polish adds operator-facing HTTP surfaces for:

- cross-service status
- per-user sandbox/VM ownership
- work/debug visibility
- bounded lifecycle actions

These surfaces are operator-only. They are not part of the ordinary browser-user contract.

Monitoring/alerting/load-testing should describe the full Node B system, not just static asset health.

## Core flows

### Auth to useful work

1. Guest opens `/`
2. User authenticates with WebAuthn
3. Browser receives same-origin cookies
4. Shell rehydrates through `GET /auth/session`
5. Browser accesses protected shell/runtime surfaces through proxy
6. Runtime accepts work and emits status/events
7. A real provider-backed result returns to the UI

### E-text authoring

1. User opens E-text from the desktop
2. User creates or opens a document
3. User edits directly or prompts the appagent
4. Canonical revisions land with correct authorship
5. History/diff/blame expose the provenance

### VM-backed execution

1. Authenticated request reaches proxy
2. Proxy resolves the user’s VM through vmctl
3. Sandbox runtime in that VM handles work
4. Gateway injects host-side provider credentials
5. Result returns through the same browser origin

## Invariants

- `draft.choir-ip.com` is the only public acceptance host in this mission
- Node A / `choir-ip.com` is out of scope
- cookie-backed same-origin auth remains the browser trust model
- no browser-visible auth tokens in URL, localStorage, or sessionStorage
- no CLI subprocess loop inside the sandbox runtime
- no adapter-wrapper process around the main runtime loop
- one Dolt-backed user workspace per sandbox
- conductor and scheduler remain separate concerns
- users and appagents are peer canonical editors
- subordinate workers do not directly author canonical state
- provider credentials stay outside the guest VM and outside git/Nix store
