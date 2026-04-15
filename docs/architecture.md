# Unified Multiagent System — Spec Sketch

> **Date**: 2026-04-10
> **Status**: Draft — design sketch for review (revised: microservices architecture)
> **Sources**: choiros-rs architecture analysis, cogent architecture analysis, Dolt integration research, Go agent runtime ecosystem research

---

## 1. System Identity

**go-choir** is a **distributed multiagent operating system** composed of **five Go microservices**, a **Caddy** reverse proxy at the edge, and a **Svelte** single-page application. The name signals the Go rewrite of the ChoirOS lineage, now unified with Cogent's capabilities into one coherent platform. The host side provides thin, focused services (authentication, request routing, VM lifecycle management, and LLM provider proxying), while the **sandbox** binary running inside each Firecracker microVM contains the bulk of the product: conductor (input routing), scheduler (work registry), agent runtime, apps, persistence, and tools. Users interact through a web desktop served by Caddy and rendered by the Svelte SPA. Agents interact through the same API surface and through direct Go interfaces (in-process within a sandbox) or HTTP calls (cross-service). The system runs on bare-metal Linux hosts, isolates untrusted code execution in Firecracker microVMs, and manages LLM API access through a dedicated provider gateway service that holds all secrets.

All five Go binaries are built in the **`go-choir`** repository, with a clean module structure designed for the 5-binary architecture: `go-choir/cmd/auth/`, `go-choir/cmd/proxy/`, `go-choir/cmd/vmctl/`, `go-choir/cmd/gateway/`, `go-choir/cmd/sandbox/`. Shared internal packages live under `go-choir/internal/`: `runtime/` (agent loop, tools, channels), `store/` (persistence), `types/` (core domain types), `gateway/` (provider routing), `auth/` (WebAuthn), `vmmanager/` (Firecracker), `proxy/` (request routing). Valuable internals are copied from the cogent repository: LLM streaming clients (`client_anthropic.go`, `client_openai.go`), tool-calling loop (`loop.go`), ToolRegistry and tool implementations (`tools*.go`), co-agent messaging (`channel.go`), core types and ID generation (`internal/core`), store schema and CRUD patterns (`internal/store`, adapted from SQLite to Dolt), EventBus pattern (`events.go`). The cogent repo is preserved as a reference.

---

## 2. Architecture Overview

### 2.1 Production Topology

```
Browser → Caddy (edge, TLS termination, serves Svelte static assets)
           ├── /auth/*      → auth service (Go)
           ├── /api/*       → proxy service (Go) → sandbox VM (vsock/virtio-net)
           └── /provider/*  → gateway service (Go)

vmctl (Go) runs independently, manages VM lifecycle via Firecracker API socket
sandbox (Go, inside each microVM) — conductor, scheduler, agent runtime, apps, vtext (Dolt), tools, persistence
```

### 2.2 High-Level Component Diagram

```
┌────────────────────────────────────────────────────────────────────────────────┐
│                              WEB DESKTOP (Browser)                             │
│                                                                                │
│   Svelte SPA (reactive UI)  │  Pretext (text layout)  │  WebSocket / SSE      │
└───────────────────────────┬────────────────────────────────────────────────────┘
                            │  HTTPS
                            ▼
┌────────────────────────────────────────────────────────────────────────────────┐
│                         CADDY (Edge / Reverse Proxy)                           │
│                                                                                │
│   TLS termination  │  Static asset serving (Svelte build)  │  Route dispatch   │
│                                                                                │
│   /auth/*  ──────────────┐                                                     │
│   /api/*   ──────────┐   │                                                     │
│   /provider/* ───┐   │   │                                                     │
└──────────────────┼───┼───┼─────────────────────────────────────────────────────┘
                   │   │   │
         ┌─────────┘   │   └──────────┐
         ▼             ▼              ▼
┌────────────┐  ┌────────────┐  ┌────────────┐       ┌─────────────────────┐
│  GATEWAY   │  │   PROXY    │  │    AUTH     │       │       VMCTL         │
│  SERVICE   │  │  SERVICE   │  │  SERVICE    │       │      SERVICE        │
│            │  │            │  │             │       │                     │
│ LLM key   │  │ Route reqs │  │ WebAuthn    │       │ Firecracker API     │
│ injection, │  │ to correct │  │ registration│       │ socket management,  │
│ rate limit,│  │ sandbox VM │  │ /login/     │       │ boot/stop/hibernate │
│ multi-     │  │ based on   │  │ logout/     │       │ idle watchdog,      │
│ provider   │  │ user       │  │ recovery,   │       │ memory pressure,    │
│ routing    │  │ session,   │  │ session     │       │ VM registry         │
│            │  │ WebSocket  │  │ mgmt, user  │       │                     │
│            │  │ upgrade &  │  │ identity    │       │ Internal API for    │
│            │  │ proxying   │  │             │       │ proxy/auth queries  │
└─────▲──────┘  └──────┬─────┘  └─────────────┘       └─────────────────────┘
      │                │
      │     vsock / virtio-net
      │                │
      │                ▼
┌─────┴──────────────────────────────────────────────────────────────────────────┐
│                   SANDBOX (inside each Firecracker microVM)                     │
│                                                                                │
│  ┌──────────────────────────────────────────────────────────────────────────┐  │
│  │                           OS LAYER                                       │  │
│  │                                                                          │  │
│  │  ┌──────────────┐ ┌──────────────┐ ┌─────────────┐ ┌───────────────────┐│  │
│  │  │  Conductor   │ │  Scheduler   │ │  Agent      │ │  Persistence      ││  │
│  │  │  (multi-ch   │ │  (cross-app  │ │  Runtime    │ │  (Dolt — all      ││  │
│  │  │   input      │ │   work       │ │  (tool loop,│ │   sandbox state)  ││  │
│  │  │   gateway,   │ │   registry,  │ │   LLM       │ ├───────────────────┤│  │
│  │  │   routing)   │ │   dispatch)  │ │   clients)  │ │  Authority & Lease││  │
│  │  │              │ │              │ │             │ │  (cap tkns, scope)││  │
│  │  └──────┬───────┘ └──────┬───────┘ └──────┬──────┘ └────────┬──────────┘│  │
│  │         │                │                │                 │           │  │
│  │  ┌──────┴────────────────┴────────────────┴─────────────────┴────────┐  │  │
│  │  │               Goroutine Supervisor                                │  │  │
│  │  │      (agent lifecycle, health checks, restart strategies)         │  │  │
│  │  └──────────────────────────────────────────────────────────────────┘  │  │
│  └──────────────────────────────────────────────────────────────────────────┘  │
│                                                                                │
│  ┌──────────────────────────────────────────────────────────────────────────┐  │
│  │                           APP LAYER                                      │  │
│  │                                                                          │  │
│  │  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐  ┌───────────────┐  │  │
│  │  │  E-Text App │  │ Terminal App│  │  Files App  │  │  Future Apps  │  │  │
│  │  │             │  │             │  │             │  │               │  │  │
│  │  │ ┌─────────┐ │  │ ┌─────────┐ │  │ ┌─────────┐ │  │               │  │  │
│  │  │ │AppAgent │ │  │ │AppAgent │ │  │ │AppAgent │ │  │               │  │  │
│  │  │ │(writer) │ │  │ │(exec)   │ │  │ │(fs mgr) │ │  │               │  │  │
│  │  │ └────┬────┘ │  │ └────┬────┘ │  │ └────┬────┘ │  │               │  │  │
│  │  │      │      │  │      │      │  │      │      │  │               │  │  │
│  │  │ ┌────┴────┐ │  │ ┌────┴────┐ │  │             │  │               │  │  │
│  │  │ │Workers  │ │  │ │Workers  │ │  │             │  │               │  │  │
│  │  │ │(research│ │  │ │(sandbox │ │  │             │  │               │  │  │
│  │  │ │ draft)  │ │  │ │ cmds)   │ │  │             │  │               │  │  │
│  │  │ └─────────┘ │  │ └─────────┘ │  │             │  │               │  │  │
│  │  └─────────────┘  └─────────────┘  └─────────────┘  └───────────────┘  │  │
│  └──────────────────────────────────────────────────────────────────────────┘  │
│                                                                                │
│  Persistence: Dolt (all sandbox state) + Filesystem                            │
└────────────────────────────────────────────────────────────────────────────────┘

Bare Metal Host (NixOS, OVH)
```

### 2.3 The Five Go Binaries

All built from the same Go module in `go-choir`, different `cmd/` entry points:

| Binary | Where it runs | Responsibility | Port/Transport |
|--------|--------------|----------------|----------------|
| `auth` | Host | WebAuthn registration/login/logout/recovery, session management, user identity | HTTP (behind Caddy) |
| `proxy` | Host | Routes authenticated API requests to the correct sandbox VM based on user session. Handles WebSocket upgrade and proxying. | HTTP (behind Caddy), vsock/virtio-net to VMs |
| `vmctl` | Host | VM lifecycle management via firecracker-go-sdk. Boot, stop, hibernate, idle watchdog, memory pressure checks. Exposes internal API for proxy/auth to query VM status. | Internal API (not exposed to browser) |
| `gateway` | Host | Provider gateway. Receives LLM API calls from sandboxes, injects real API keys, proxies to upstream providers (Anthropic, OpenAI, Bedrock, etc.), rate limiting per sandbox. | HTTP (called by sandboxes, not directly by browser) |
| `sandbox` | Inside each microVM | The full product: conductor (input routing), scheduler (work registry), agent runtime, apps, appagents, workers, vtext, tools, persistence (Dolt — all sandbox state). | HTTP API on internal port (reached via proxy) |

### 2.4 Caddy (Edge)

- Serves the Svelte frontend as static assets
- Reverse-proxies to auth, proxy, and gateway services
- TLS termination
- Already deployed in the current infrastructure
- Not a custom binary — standard Caddy with a Caddyfile

### 2.5 Svelte Frontend

- Single SPA build artifact, served by Caddy as static files
- Handles both auth flows (talks to auth service) and desktop/app flows (talks to proxy → sandbox)
- Pretext (`@chenglou/pretext`) used for vtext editor component
- Window management (drag, resize, minimize, maximize, z-ordering) implemented client-side
- Real-time: SSE for status streams, WebSocket for interactive sessions
- Communicates exclusively via JSON API and WebSocket

### 2.6 Mapping Summary: Current → Unified

**From choiros-rs (Rust)**:
- Hypervisor auth → **auth** service (WebAuthn, session management)
- Hypervisor proxy/routing → **proxy** service (request routing to sandbox VMs)
- Hypervisor provider gateway → **gateway** service (LLM key injection, multi-provider routing)
- Hypervisor VM management → **vmctl** service (Firecracker lifecycle)
- ConductorActor (input routing) → **sandbox**: Conductor subsystem (multi-channel input gateway — §3.2)
- ConductorActor (work tracking, dispatch) → **sandbox**: Scheduler subsystem (cross-app work registry — §3.3)
- Sandbox (actor system, event store, agents, apps) → **sandbox** binary (agent runtime, persistence, app layer)
- Dioxus WASM frontend → **Svelte SPA** (served by Caddy) + **Pretext** for vtext editor
- BAML contracts → Go-native structured output via LLM clients (copied from cogent, in sandbox)
- ractor actor system → goroutine supervisor (in sandbox)
- shared-types → eliminated (Go types within the sandbox binary, shared via Go module for host services)

**From cogent (Go)**:
- Work graph (state machine) → **sandbox**: Scheduler subsystem (central cross-app work registry — §3.3, tables adapted from SQLite to Dolt)
- Native LLM tool loop, co-agents, channels → **sandbox**: core execution engine (internals copied to `go-choir`)
- Serve runtime (HTTP, WebSocket, supervisor) → split: **proxy** handles routing, **sandbox** handles app API
- Attestation model → **sandbox**: Scheduler internal quality gate (preserved)
- Mind-graph UI → **Svelte SPA**: one app among many (preserved)
- Capability tokens (Ed25519) → **sandbox**: agent identity and authorization (preserved)
- EventBus → **sandbox**: event distribution (preserved, extended to serve app events)
- Hand-rolled LLM clients (Anthropic, OpenAI) → **sandbox** calls **gateway** for LLM access
- ToolRegistry pattern → **sandbox**: tool system (preserved)

**What gets dropped**:
- Dioxus WASM frontend (replaced by Svelte SPA with Pretext, served by Caddy)
- ractor dependency (replaced by plain goroutines + channels + custom supervisor in sandbox)
- BAML code generation (replaced by Go-native structured output in sandbox)
- shared-types crate (unnecessary — single language)
- The choiros-rs/cogent boundary (two processes, gateway token dance)
- Adapter abstraction layer (Claude adapter, BaseAdapter, subprocess-based adapters — replaced by direct in-process agent execution)
- `cogent` CLI (agents interact via direct Go function calls, operator management via HTTP admin API)
- `cogent serve` as a monolithic dev mode (the real distributed architecture is the only architecture)
- `.qwy` file format (replaced by Dolt-backed relational storage in sandbox)
- `embed.FS` for frontend assets (Caddy serves the Svelte build as static files)

---

## 3. OS Layer Specification

The OS layer is **distributed across the host services and the sandbox binary**. Host services are thin and focused; the sandbox contains the bulk of the OS runtime.

### 3.1 Agent Runtime (sandbox binary)

**Purpose**: The one standardized contract that all agents — local goroutines within a sandbox and remote agents in other sandboxes — implement. This is the system's most important abstraction. It lives entirely within the **sandbox** binary.

**What it subsumes**:
- choiros-rs: `AgentHarness`, `WorkerPort` trait, `ALM` harness, actor message types, BAML function contracts
- cogent: native tool-calling loop (`loop.go`), ToolRegistry and tool implementations, co-agent manager, channel manager

**Key interfaces**:

```go
// AgentCard describes an agent's capabilities.
type AgentCard struct {
    ID          string            `json:"id"`
    Name        string            `json:"name"`
    Description string            `json:"description"`
    Skills      []Skill           `json:"skills"`
    Endpoint    string            `json:"endpoint,omitempty"` // empty for local agents
    AuthScheme  string            `json:"auth_scheme,omitempty"`
}

// Agent is the universal contract. Every agent — appagent, worker, local, remote — implements this.
type Agent interface {
    Card() AgentCard

    // HandleTask processes a task and returns a result.
    // For local agents: direct Go function call within the sandbox process.
    // For remote agents: HTTP call over vsock via proxy (transport hidden by the runtime).
    HandleTask(ctx context.Context, task Task) (TaskResult, error)

    // HandleMessage processes an inter-agent message (fire-and-forget or request-response).
    HandleMessage(ctx context.Context, msg Message) error

    // Status returns the agent's current operational status.
    Status(ctx context.Context) (AgentStatus, error)
}

// Task is the unit of work delegated between agents.
type Task struct {
    ID          string            `json:"id"`
    ParentID    string            `json:"parent_id,omitempty"`
    Objective   string            `json:"objective"`
    Input       []Part            `json:"input"`
    Constraints TaskConstraints   `json:"constraints,omitempty"`
}

// TaskResult is returned when a task completes or fails.
type TaskResult struct {
    TaskID  string     `json:"task_id"`
    Status  TaskStatus `json:"status"` // completed, failed, blocked
    Output  []Part     `json:"output"`
    Error   string     `json:"error,omitempty"`
}

// Part is a typed content chunk (text, file reference, structured data).
type Part struct {
    Kind    string          `json:"kind"` // "text", "file", "data", "artifact"
    Content json.RawMessage `json:"content"`
}

// Message is the coagent messaging primitive (inter-agent communication).
type Message struct {
    ID        string   `json:"id"`
    From      string   `json:"from"`       // sender agent ID
    To        string   `json:"to"`         // recipient agent ID
    Channel   string   `json:"channel"`    // optional named channel
    Parts     []Part   `json:"parts"`
    ReplyTo   string   `json:"reply_to,omitempty"`
}
```

**Tool access via ToolRegistry**:

Every agent gets access to tools through the `ToolRegistry` pattern (copied from cogent) — plain Go functions registered in a map. The tool surface depends on the agent's role (appagent vs. worker):

```go
// ToolSet defines what tools an agent has access to.
type ToolSet struct {
    FileRead     bool
    FileWrite    bool
    FileEdit     bool
    Bash         bool   // sandboxed execution within the VM
    WebSearch    bool
    WebFetch     bool
    MessageAgent bool   // send messages to other agents
    Custom       []Tool // app-specific tools registered by the appagent
}
```

Workers get `MessageAgent` but NOT direct write access to canonical app state. They propose changes by messaging the appagent.

**Persistence model**: Agent sessions, turns, and events are persisted in Dolt within the sandbox (schema adapted from cogent). The agent runtime does NOT own app-layer state — that belongs to each app's persistence layer. All sandbox state lives in a single Dolt database (see §3.5).

**Local vs. remote**: The `Agent` interface is the same. For local agents (goroutines within the sandbox), `HandleTask` is a direct Go function call. For remote agents (in other sandboxes), the runtime routes the call through the host proxy service: sandbox → host proxy → target sandbox. The caller never knows the difference.

### 3.2 Conductor (sandbox binary)

**Purpose**: Multi-channel input gateway. The Conductor receives user inputs from any channel — web UI prompt bar, email, chat app integrations (Slack, etc.), API calls, future integrations — normalizes them into a common format, and routes to the correct appagent. Think of it as the "mail room" or "receptionist."

The Conductor does NOT manage work items or track background tasks — that's the Scheduler's job (§3.3). It does NOT care about time scale — whether a request is handled instantly or kicks off a multi-hour background task is the appagent's problem. The Conductor just delivers the message.

**What it subsumes**:
- choiros-rs: `ConductorActor` (input routing, capability dispatch — the routing half, NOT the work tracking half)
- cogent: request routing from `serve.go` (the "which agent handles this" decision)

**Key interfaces**:

```go
// Conductor is the multi-channel input gateway.
// It normalizes inputs from any channel and routes to the correct appagent.
type Conductor struct {
    apps    AppRegistry
    gateway GatewayClient
}

// InboundMessage is the normalized input from any channel.
type InboundMessage struct {
    Channel  string         // "web_ui", "email", "chat_slack", "api"
    UserID   string
    Content  string
    Metadata map[string]any
}

// RouteDecision is the Conductor's output: which app handles this input.
type RouteDecision struct {
    AppID     string
    Objective string
    Context   map[string]any
}

// Route normalizes an inbound message and routes it to the correct appagent.
func (c *Conductor) Route(ctx context.Context, msg InboundMessage) (RouteDecision, error) { ... }
```

**Routing logic**:
1. Conductor receives a normalized `InboundMessage` (the proxy service or channel adapters deliver it)
2. Inspects the message content and metadata to determine the target app
3. For explicit app-targeted messages, routes directly
4. For generic prompt-bar input, focus is not a routing hint. The default local behavior is to open a new `vtext` unless the conductor has a better explicit route
5. For ambiguous messages (e.g., an email that could be handled by multiple apps), uses an LLM call via the gateway to classify
6. Very lightweight prompts may resolve to a tiny UI action such as a toast rather than opening a heavier app workflow
7. Returns a `RouteDecision` — the sandbox's app router invokes the appagent's `HandleUserAction`

**Multi-channel input flow**:
```
Input channels:
  Web UI prompt bar  ──→ proxy → sandbox HTTP API ──→ Conductor
  Email              ──→ inbound webhook → sandbox ──→ Conductor
  Chat (Slack, etc.) ──→ integration webhook ──────→ Conductor
  API calls          ──→ proxy → sandbox HTTP API ──→ Conductor
                                                        │
                                                        ▼
                                                   RouteDecision
                                                        │
                                                        ▼
                                                   AppAgent.HandleUserAction()
```

**What the Conductor is NOT**:
- Not a scheduler — it doesn't track work or dispatch workers
- Not an agent — it doesn't run a tool-calling loop or make autonomous decisions
- Not a queue — messages are routed synchronously (the appagent decides if async work is needed)

### 3.3 Scheduler (sandbox binary)

**Purpose**: Central cross-app work registry. The Scheduler is a service that appagents USE when they need background worker execution. It tracks ALL work across ALL apps in one registry for observability, dispatches workers on the Agent Runtime, monitors completion, and handles retry/resume. Think of it as the "job board."

Appagents submit work TO the Scheduler. The Scheduler dispatches workers and reports results BACK to the appagent. The Scheduler does NOT route user inputs — that's the Conductor's job (§3.2).

**What it subsumes**:
- choiros-rs: `ConductorActor` (the work-tracking and dispatch half — NOT the input routing half), `self_directed_dispatch.rs`, run state machine
- cogent: work graph (state machine, tables adapted to Dolt), claim/lease model, supervisor agent, auto-dispatch, rotation config, briefing/hydration, attestation gating

**The flow** (Conductor → AppAgent → Scheduler):
```
Input channels (web UI, email, chat, API)
    │
    ▼
Conductor (normalizes, routes)              ← §3.2
    │
    ▼
AppAgent (domain authority)                 ← §4.2
    ├── handles directly (any time scale)
    └── submits background work → Scheduler ← §3.3 (this section)
                                    │
                                    └── dispatches workers (on Agent Runtime)
                                          │
                                          └── results → AppAgent → updates canonical state
```

**Key interfaces**:

```go
// Scheduler is the central cross-app work registry within the sandbox.
type Scheduler struct {
    store    *DoltStore
    eventBus *EventBus
}

// SubmitWork registers a new work item. Called by appagents when they need background execution.
func (s *Scheduler) SubmitWork(ctx context.Context, req WorkRequest) (string, error) { ... }

// WorkStatus returns the current status of a work item.
func (s *Scheduler) WorkStatus(ctx context.Context, workID string) (WorkStatus, error) { ... }

// ListWork lists work items matching a filter. Provides cross-app observability.
func (s *Scheduler) ListWork(ctx context.Context, filter WorkFilter) ([]WorkRecord, error) { ... }

// OnComplete registers a callback for when a work item finishes.
func (s *Scheduler) OnComplete(ctx context.Context, workID string, cb func(WorkResult)) { ... }

// WorkRequest is submitted by an appagent to request background execution.
type WorkRequest struct {
    AppID       string          `json:"app_id"`       // which app owns this work
    Objective   string          `json:"objective"`
    Input       []Part          `json:"input"`
    Constraints TaskConstraints `json:"constraints"`
    Priority    int             `json:"priority"`
}

// WorkRecord is a tracked work item in the central registry.
type WorkRecord struct {
    ID              string              `json:"id"`
    AppID           string              `json:"app_id"`           // which app owns this
    AgentID         string              `json:"agent_id"`         // which worker is assigned
    Objective       string              `json:"objective"`
    State           ExecutionState      `json:"state"`            // queued → running → completed/failed
    Priority        int                 `json:"priority"`
    Constraints     TaskConstraints     `json:"constraints"`
    AttemptEpoch    int                 `json:"attempt_epoch"`
    ClaimedBy       string              `json:"claimed_by,omitempty"`
    ClaimedUntil    *time.Time          `json:"claimed_until,omitempty"`
    CreatedAt       time.Time           `json:"created_at"`
    UpdatedAt       time.Time           `json:"updated_at"`
}

// ExecutionState tracks work lifecycle.
type ExecutionState string

const (
    StateQueued    ExecutionState = "queued"
    StateRunning   ExecutionState = "running"
    StateBlocked   ExecutionState = "blocked"
    StateCompleted ExecutionState = "completed"
    StateFailed    ExecutionState = "failed"
    StateCancelled ExecutionState = "cancelled"
)
```

**Cross-app observability**: Because ALL apps submit work to the same Scheduler, operators can query the full work registry to see everything happening across the entire sandbox — which apps have pending work, which workers are running, what's stalled, etc. This is the single source of truth for "what is the system doing right now."

**Dispatch logic** (from cogent's supervisor, preserved):
1. Scheduler monitors for queued work items across all apps
2. Selects model/provider using rotation pool (round-robin with history-aware avoidance)
3. Hydrates briefing context via `ProjectHydrate()`
4. Dispatches to a worker via the Agent Runtime (in-process)
5. Monitors for stalls, handles completion/failure
6. Reports results back to the originating appagent
7. Attestation gating: work is only `completed` when verification evidence satisfies policy

**Persistence**: Dolt within the sandbox (`work_items`, `work_edges`, `attestation_records`, `jobs`, `sessions`, `turns`, `events` tables — schema adapted from cogent). All scheduler state lives in the single per-sandbox Dolt database (see §3.5).

**What disappears**: Normal users never see work items — they interact with apps. The Scheduler is internal machinery used by appagents. Agents interact with the Scheduler via direct Go function calls (in-process), not through any CLI.

### 3.4 Desktop Shell (distributed: Caddy + proxy + Svelte SPA)

**Purpose**: Session management, window management, app lifecycle, and the UI experience. Unlike the previous monolithic design, the "desktop shell" is now distributed across three components.

**What it subsumes**:
- choiros-rs: `DesktopActor` (window state), Dioxus frontend (replaced), hypervisor HTTP server routing, sandbox HTTP API (desktop, files, writer endpoints), WebSocket protocols
- cogent: `serve.go` HTTP server (split), WebSocket hub (split), embedded web UI (mind-graph becomes one app)

**Distribution of responsibilities**:

| Concern | Component | Details |
|---------|-----------|---------|
| Static asset serving | **Caddy** | Serves the compiled Svelte SPA as static files |
| TLS termination | **Caddy** | Handles HTTPS certificates |
| Route dispatch | **Caddy** | Routes `/auth/*`, `/api/*`, `/provider/*` to respective services |
| Session validation | **proxy** service | Validates auth tokens on every API request before forwarding to sandbox |
| WebSocket proxying | **proxy** service | Upgrades HTTP to WebSocket and proxies bidirectionally to sandbox VMs |
| Request routing to VMs | **proxy** service | Maps authenticated user → correct sandbox VM via vmctl registry |
| Window management | **Svelte SPA** (browser) | Drag, resize, minimize, maximize, z-ordering — all client-side |
| App rendering | **Svelte SPA** (browser) | Reactive UI, component rendering |
| Real-time updates | **Svelte SPA** (browser) ↔ **sandbox** (via proxy) | SSE/WebSocket through proxy to sandbox |
| Desktop state persistence | **sandbox** | Window state, app state stored in sandbox's Dolt database |

**Key interfaces** (within the sandbox):

```go
// App is a registered application in the desktop.
type App struct {
    ID          string   `json:"id"`
    Name        string   `json:"name"`
    Icon        string   `json:"icon"`
    Description string   `json:"description"`
    AgentID     string   `json:"agent_id,omitempty"` // optional appagent
    Routes      []Route  `json:"routes"`             // HTTP routes this app owns
    HasUI       bool     `json:"has_ui"`             // renders in the desktop
}

// WindowState represents a window in the desktop.
type WindowState struct {
    ID       string  `json:"id"`
    AppID    string  `json:"app_id"`
    Title    string  `json:"title"`
    X, Y     float64 `json:"x,y"`
    W, H     float64 `json:"w,h"`
    ZIndex   int     `json:"z_index"`
    State    string  `json:"state"` // normal, minimized, maximized
}

// DesktopSession represents a user's active session.
type DesktopSession struct {
    ID       string        `json:"id"`
    UserID   string        `json:"user_id"`
    Windows  []WindowState `json:"windows"`
    ActiveWin string       `json:"active_window"`
}
```

**Request flow** (browser → sandbox):
```
Browser (Svelte SPA)
  → HTTPS → Caddy
  → /api/* → proxy service (validates session token)
  → proxy looks up user's sandbox VM via vmctl internal API
  → proxy forwards request via vsock/virtio-net to sandbox
  → sandbox processes request (app API, desktop state, etc.)
  → response flows back the same path
```

**The sandbox's HTTP server** serves app-specific JSON APIs and desktop state APIs. The proxy service is a thin authenticated pass-through — it does not interpret request bodies.

**Persistence**: Desktop state (windows, sessions) in the sandbox's Dolt database. Auth state (users, credentials) in the auth service's own SQLite DB (see §3.8).

### 3.5 Persistence Substrate (distributed)

**Purpose**: Provide the appropriate storage backend for each kind of state, distributed across the services that own that state. The guiding principle is **consolidation**: one Dolt database per sandbox VM for all sandbox state, plus minimal SQLite on the host side.

**What it subsumes**:
- choiros-rs: `events.db` (event store), `hypervisor.db` (auth, routes, jobs), `memory store` (symbolic memory), `.qwy` files (document storage)
- cogent: `cogent.db` (work graph, sessions, jobs, events, artifacts), `cogent-private.db` (private notes, credentials)

**Consolidated persistence model: 1 Dolt per sandbox + 2 tiny SQLite on host = 3 databases total**

**Per sandbox VM: ONE Dolt database (embedded)**
- Everything goes in Dolt: vtext content, work graph (scheduler records, attestations, edges), app state, sessions, events (append-only table), locks, runtime state, job records, artifacts metadata, desktop state, private notes
- Version-controlled commits for meaningful state changes (user edits, work state transitions, etc.)
- Operational/ephemeral state (sessions, locks, events) lives in Dolt tables but doesn't need to be committed after every write — just committed periodically or on significant state changes
- This is simpler to manage: one database, one backup story, one replication story

**Host side: minimal SQLite**
- **auth** service: SQLite for users, credentials, sessions (small, simple, doesn't need versioning)
- **vmctl** service: SQLite for VM registry (small, simple)
- proxy and gateway: stateless (or minimal config in memory)

**Storage tiers** (by service):

| Service | Engine | Purpose | Schema Source |
|---------|--------|---------|--------------|
| **sandbox** (All State) | Dolt (embedded, per-user) | ALL sandbox state: vtext content, work graph, agent sessions/jobs/turns, events, desktop state, private notes, artifacts metadata | Schema adapted from cogent's 21-table schema + vtext schema (see §5) |
| **sandbox** (Filesystem) | Local disk (VM data.img) | Agent artifacts (binary files), raw outputs, native session history, config | Layout adapted from cogent's `.cogent/` directory structure |
| **auth** (Auth DB) | SQLite | User accounts, WebAuthn credentials, session tokens | choiros hypervisor schema (users, credentials, sessions) |
| **vmctl** (VM Registry) | SQLite | VM metadata, user→VM mappings, VM health status | New schema |
| **gateway** (Config) | TOML / env vars | Provider API keys, rate limit config | No persistent state beyond config |

**Dolt tables (all in one database per sandbox):**

*Versioned (committed on meaningful changes):*
- `documents`, `content`, `citations`, `metadata` — vtext app
- `work_items`, `work_edges`, `attestation_records`, `approval_records` — scheduler/work graph
- `doc_content`, `work_notes`, `work_proposals` — work documentation
- App-specific tables as apps are added

*Operational (in Dolt but committed periodically, not per-write):*
- `sessions`, `jobs`, `turns` — agent session tracking
- `events` — append-only canonical event log
- `locks`, `job_runtime` — ephemeral runtime state
- `artifacts` — artifact metadata
- `native_sessions` — agent session persistence

Note: Dolt supports normal SQL writes without committing. You can INSERT/UPDATE freely. `CALL dolt_commit()` captures a versioned snapshot when you want one. So operational tables work fine in Dolt — they just don't get committed as often.

**Key decisions**:
- **Dolt as the sole sandbox database**: Embedded via `dolthub/driver`. Per-user database directory within the sandbox VM (`/data/sandbox/.dolt/`). Full version control via SQL for content that needs it; normal SQL access for operational state.
- **SQLite only on the host side**: `modernc.org/sqlite` (pure Go, no CGo) for the auth and vmctl services. WAL mode, `_txlock=immediate`, `MaxOpenConns=1`. Same configuration pattern as cogent. Not used inside the sandbox.
- **Each service owns its own database**: No shared database across services. Services communicate via internal HTTP APIs, not shared state.
- **No event store actor**: The choiros-rs pattern of an `EventStoreActor` wrapping a database is unnecessary — Go's `database/sql` with proper transaction handling provides the same sequential write guarantee.
- **Event log preserved**: Append-only `events` table in the sandbox's Dolt is the canonical audit trail. All significant state changes emit events.

**Dolt schema within sandbox**: Merges the table schemas adapted from cogent (sessions, jobs, turns, events, work_items, work_edges, attestation_records, etc.) with vtext tables (documents, content, citations, metadata) and desktop state tables (windows, app registrations). The SQLite CRUD patterns from cogent are adapted to Dolt's MySQL-compatible SQL dialect (driver changes from `modernc.org/sqlite` to `dolthub/driver`). Migration: additive schema evolution (CREATE TABLE IF NOT EXISTS).

#### Future: Published E-Texts (not in v1 scope)

The architecture supports a future publishing path for vtexts:
- Users publish vtexts to choir-ip.com where they are viewable without login
- Publishing uses Dolt's native push/pull: the sandbox's embedded Dolt pushes a branch/snapshot to a platform-level Dolt instance on choir-ip.com
- The platform-level Dolt serves as a read-only public database for published content
- This is a natural fit because Dolt is literally Git-for-data — push/pull between instances is a first-class operation
- Architecture implication: the per-sandbox Dolt schema must be designed so that publishable content can be cleanly extracted (the current table design with `documents` + `content` + `citations` supports this)
- Not specced in detail — this is a future mission

### 3.5a Store API: SQLite → Dolt Migration

**The Dolt embedded driver implements Go's `database/sql` interface.** This means the cogent store patterns carry over almost 1:1.

#### Opening the database

SQLite (cogent current):
```go
db, err := sql.Open("sqlite", dsn)
db.SetMaxOpenConns(1)
```

Dolt (go-choir):
```go
import embedded "github.com/dolthub/driver"

cfg, _ := embedded.ParseDSN("file:///path/to/db?commitname=System&commitemail=system@go-choir.local&database=sandbox")
connector, _ := embedded.NewConnector(cfg)
db := sql.OpenDB(connector)
```

#### CRUD operations — identical interface

```go
// Insert (same db.ExecContext pattern)
_, err := db.ExecContext(ctx,
    `INSERT INTO work_items (work_id, title, objective, execution_state, created_at)
     VALUES (?, ?, ?, ?, ?)`,
    workID, title, objective, "queued", now)

// Query (same db.QueryContext pattern)
rows, err := db.QueryContext(ctx,
    `SELECT work_id, title, execution_state FROM work_items WHERE execution_state = ?`,
    "queued")

// Transaction (same db.BeginTx pattern)
tx, err := db.BeginTx(ctx, nil)
defer tx.Rollback()
tx.ExecContext(ctx, `UPDATE work_items SET execution_state = ? WHERE work_id = ?`, "running", workID)
tx.Commit()
```

#### Three SQL dialect changes from SQLite

| SQLite | Dolt (MySQL-compatible) | Sites affected |
|--------|----------------------|----------------|
| `INSERT ... ON CONFLICT(col) DO UPDATE SET ...` | `INSERT ... ON DUPLICATE KEY UPDATE ...` | ~3 upsert sites |
| `TEXT PRIMARY KEY` | `VARCHAR(36) PRIMARY KEY` | All DDL (use UUIDs) |
| `PRAGMA journal_mode=WAL; PRAGMA busy_timeout=...` | Remove (not applicable) | Store bootstrap |

#### Version control (additive — new capabilities)

```go
// Commit after meaningful state changes
db.ExecContext(ctx, `CALL dolt_add('.')`)
db.ExecContext(ctx, `CALL dolt_commit('-m', ?, '--author', ?)`,
    "Work item claimed: "+workID,
    "Scheduler <scheduler@go-choir.local>")

// Query history
rows, _ := db.QueryContext(ctx, `SELECT * FROM dolt_log ORDER BY date DESC LIMIT 20`)

// Query state at a point in time
rows, _ := db.QueryContext(ctx,
    `SELECT * FROM work_items AS OF ? WHERE work_id = ?`, commitHash, workID)

// Diff between versions
rows, _ := db.QueryContext(ctx,
    `SELECT * FROM dolt_diff_work_items WHERE from_commit = ? AND to_commit = ?`,
    fromHash, toHash)

// Blame
rows, _ := db.QueryContext(ctx, `SELECT * FROM dolt_blame_work_items`)
```

#### Commit strategy

- **User edits** (vtext): commit immediately with user as author
- **AppAgent edits**: commit immediately with agent as author
- **Work state transitions** (claimed, completed, failed): commit on transition
- **Operational state** (sessions, events, locks): commit periodically (every 5 minutes) or on graceful shutdown
- **Batch operations**: commit once at end of batch

#### What this means

The work graph that was in cogent's SQLite (`work_items`, `work_edges`, `attestation_records`, etc.) moves to Dolt with:
- Identical CRUD patterns (same `database/sql` interface)
- 3 mechanical SQL syntax changes
- Free versioning, provenance, and audit trail on top

### 3.6 Authority & Lease Model (distributed)

**Purpose**: Security boundary enforcement across the distributed system. Who can do what, where, and for how long.

**What it subsumes**:
- choiros-rs: keyless sandbox policy, gateway token, provider gateway auth, VM lifecycle/isolation, non-root sandbox user, route pointers, machine classes
- cogent: Ed25519 CA, capability tokens, agent credentials, session locks

**Distribution of security concerns**:

| Concern | Service | Details |
|---------|---------|---------|
| User authentication (WebAuthn) | **auth** | Passkey registration, login, session token issuance |
| Session token validation | **proxy** | Every API request validated before forwarding to sandbox |
| VM isolation boundary | **vmctl** | Firecracker microVM creation, one user per VM, resource limits |
| Agent capability tokens (Ed25519) | **sandbox** | Agent identity, role-scoped authorization within the sandbox |
| LLM API key management | **gateway** | Keys never exposed to sandboxes; gateway injects them |
| Worker permission enforcement | **sandbox** | ToolSet restrictions, MessageAgent-only for workers |

**Key invariants**:
1. **MicroVMs never hold LLM API keys** (choiros invariant, preserved). The gateway service on the host injects secrets.
2. **Agents authenticate via Ed25519 capability tokens** (cogent invariant, preserved). Tokens are time-limited, role-scoped, and signed by the sandbox's CA. These are internal to each sandbox.
3. **One user per sandbox VM** — singular authority. No multi-tenant VMs.
4. **Workers cannot directly mutate canonical app state** — enforced by the agent runtime's tool set (workers get `MessageAgent`, not direct state write).
5. **Auth tokens propagate through the chain** — auth service issues session tokens, proxy validates them, sandbox trusts the proxy's forwarded identity header.

**Capability token model** (design from cogent, copied to `go-choir` — within sandbox):

```go
// CapabilityToken authorizes an agent for specific actions within a sandbox.
type CapabilityToken struct {
    TokenID   string    `json:"token_id"`
    AgentID   string    `json:"agent_id"`
    Role      string    `json:"role"`     // "appagent", "worker", "supervisor"
    Scope     []string  `json:"scope"`    // allowed actions
    IssuedAt  time.Time `json:"issued_at"`
    ExpiresAt time.Time `json:"expires_at"`
    Signature []byte    `json:"signature"` // Ed25519 signature
}
```

**MicroVM lifecycle** (managed by vmctl service via firecracker-go-sdk):

| VM Type | Purpose | Guest Profile | Management |
|---------|---------|---------------|------------|
| User Sandbox (live) | Per-user agent execution | Minimal (2 vCPU, 1GB) | vmctl → firecracker-go-sdk → API socket |
| User Sandbox (dev) | Dev/branch sandbox | Minimal | vmctl → firecracker-go-sdk → API socket |
| Worker VM | Shared pool, thick tooling | Worker (more resources) | vmctl → firecracker-go-sdk → API socket |

The vmctl service IS the hypervisor. VM lifecycle (boot, stop, hibernate, idle watchdog, memory pressure) is managed **directly via firecracker-go-sdk API socket calls** — no systemd templates, no shell scripts. Nix builds the VM images (NixOS guest configs, kernel, disk images via microvm.nix), but vmctl manages everything at runtime.

VM lifecycle: boot → running → (idle timeout) → hibernated/stopped. Idle watchdog (30s scan, configurable timeout). Memory pressure check before spawn.

**Host ↔ Guest IPC**: vsock (preferred — no network config) or virtio-net (for compatibility). The sandbox binary inside VMs communicates with host services via HTTP over vsock — the proxy service mediates all browser-facing traffic.

### 3.7 Provider Gateway (gateway service)

**Purpose**: Centralized LLM API key management and multi-provider routing as a dedicated microservice. The one place where secrets live.

**What it subsumes**:
- choiros-rs: `provider_gateway.rs` (Anthropic, OpenAI, Z.AI, Kimi, Inception, OpenRouter, Tavily, Brave, Exa, AWS Bedrock proxying, per-sandbox rate limiting, Bedrock request rewriting)
- cogent: provider configuration (ZAI, Bedrock, ChatGPT, direct Anthropic, direct OpenAI), web search API key management (Exa, Tavily, Brave, Serper)

**Design**: The gateway is a standalone host service. Sandbox binaries send LLM requests to the gateway via HTTP (over vsock or virtio-net). The gateway injects the real API key and proxies to the upstream provider. The gateway is NOT directly accessible from the browser — Caddy routes `/provider/*` to it, but this is for administrative endpoints only.

```go
// ProviderGateway routes LLM API calls to upstream providers,
// injecting API keys and enforcing rate limits.
type ProviderGateway struct {
    providers   map[string]ProviderConfig
    rateLimiter *RateLimiter
}

// ProviderConfig defines an upstream LLM provider.
type ProviderConfig struct {
    Name       string   // "anthropic", "openai", "bedrock", "zai", etc.
    BaseURL    string
    AuthHeader string   // e.g., "x-api-key", "Authorization"
    APIKey     string   // loaded from env/config, NEVER exposed to sandboxes
    Models     []string // allowed models
    RateLimit  RateLimit
}
```

**For sandbox agents**: LLM calls go sandbox → gateway service (HTTP over vsock/virtio-net). The gateway authenticates the sandbox (via a per-VM token injected at boot by vmctl), injects the real API key, and proxies to the upstream provider. The hand-rolled streaming clients (copied from cogent's `client_anthropic.go`, `client_openai.go`) are used within the sandbox, configured to point at the gateway endpoint instead of the real upstream URL.

**Unified provider list** (merged from both systems):
- Anthropic (Claude Opus, Sonnet, Haiku)
- OpenAI (GPT-5.x)
- AWS Bedrock (Claude variants)
- Z.AI (GLM models)
- OpenRouter (aggregator)
- Inception (Mercury)
- Kimi (Moonshot)
- Google (Gemini)
- Web search: Exa, Tavily, Brave, Serper (round-robin rotation, pattern copied from cogent)

### 3.8 Auth Service (auth binary)

**Purpose**: User identity and authentication as a dedicated host-side microservice. Handles WebAuthn passkey flows and session management.

**What it subsumes**:
- choiros-rs: hypervisor auth routes (WebAuthn registration, login, logout, recovery), user table, credentials table, session/cookie management

**Key interfaces**:

```go
// AuthService manages user identity and authentication.
type AuthService struct {
    webAuthn  *webauthn.WebAuthn
    store     *AuthStore   // SQLite: users, credentials, sessions
    vmctl     VMCtlClient  // to query/trigger VM state on login/logout
}

// User represents a registered user.
type User struct {
    ID          string    `json:"id"`
    DisplayName string    `json:"display_name"`
    Email       string    `json:"email,omitempty"`
    CreatedAt   time.Time `json:"created_at"`
}

// SessionToken is issued on successful login.
type SessionToken struct {
    Token     string    `json:"token"`
    UserID    string    `json:"user_id"`
    ExpiresAt time.Time `json:"expires_at"`
}
```

**HTTP routes** (behind Caddy at `/auth/*`):
```
POST   /auth/register/begin     → start WebAuthn registration
POST   /auth/register/finish    → complete WebAuthn registration
POST   /auth/login/begin        → start WebAuthn login
POST   /auth/login/finish       → complete WebAuthn login (returns session token)
POST   /auth/logout             → invalidate session token
POST   /auth/recovery/begin     → start account recovery
POST   /auth/recovery/finish    → complete account recovery
GET    /auth/session            → validate current session, return user info
```

**Session propagation**: On successful login, the auth service issues a session token (JWT or opaque token stored in its SQLite DB). The Svelte frontend includes this token in all subsequent requests (as a cookie or Authorization header). The proxy service validates the token against the auth service before forwarding requests to sandboxes.

**Persistence**: Own SQLite database with users, credentials, and sessions tables.

### 3.9 Proxy Service (proxy binary)

**Purpose**: Authenticated request routing from the browser to the correct sandbox VM. A thin, stateless pass-through that validates sessions and maps users to VMs.

**What it subsumes**:
- choiros-rs: hypervisor's proxy/routing logic (mapping user sessions to sandbox VMs), WebSocket forwarding
- cogent: `serve.go` HTTP routing (split out)

**Key interfaces**:

```go
// ProxyService routes authenticated requests to sandbox VMs.
type ProxyService struct {
    auth       AuthClient    // to validate session tokens
    vmctl      VMCtlClient   // to look up user→VM mappings
    transport  TransportPool // pool of vsock/virtio-net connections to VMs
}
```

**HTTP routes** (behind Caddy at `/api/*`):
```
/api/*     → validate session token → look up user's sandbox VM → forward request
/ws/*      → validate session token → WebSocket upgrade → bidirectional proxy to sandbox
/sse/*     → validate session token → SSE proxy from sandbox
```

**Request flow**:
1. Browser sends request to Caddy
2. Caddy routes `/api/*` to proxy service
3. Proxy extracts session token, calls auth service to validate
4. Proxy calls vmctl to find the user's sandbox VM (or trigger boot if not running)
5. Proxy forwards the request to the sandbox via vsock/virtio-net
6. Sandbox processes the request and returns a response
7. Proxy forwards the response back to the browser

**WebSocket proxying**: The proxy upgrades the HTTP connection to WebSocket and maintains a bidirectional tunnel between the browser and the sandbox. This is critical for real-time features (terminal, agent chat, live document updates).

**Stateless**: The proxy holds no persistent state. It queries auth and vmctl on every request (with appropriate caching for performance).

### 3.10 VM Controller (vmctl binary)

**Purpose**: Firecracker microVM lifecycle management as a dedicated host-side service. Boots, stops, hibernates, and monitors sandbox VMs.

**What it subsumes**:
- choiros-rs: `SandboxRegistry` (VM lifecycle, idle watchdog, memory pressure), Firecracker API socket management, machine class configuration

**Key interfaces**:

```go
// VMController manages the lifecycle of Firecracker microVMs.
type VMController struct {
    registry  *VMRegistry   // SQLite: VM metadata, user→VM mappings
    machines  map[string]*firecracker.Machine // active VMs
    config    VMConfig      // machine classes, resource limits
}

// VMEntry represents a registered VM in the registry.
type VMEntry struct {
    VMID       string     `json:"vm_id"`
    UserID     string     `json:"user_id"`
    MachineClass string   `json:"machine_class"` // "minimal", "worker", "dev"
    Status     VMStatus   `json:"status"`         // booting, running, hibernated, stopped
    VsockCID   uint32     `json:"vsock_cid"`
    BootedAt   *time.Time `json:"booted_at,omitempty"`
    LastActive *time.Time `json:"last_active,omitempty"`
}

// VMStatus tracks VM lifecycle state.
type VMStatus string

const (
    VMBooting    VMStatus = "booting"
    VMRunning    VMStatus = "running"
    VMHibernated VMStatus = "hibernated"
    VMStopped    VMStatus = "stopped"
    VMError      VMStatus = "error"
)
```

**Internal API** (not exposed to browser, only to proxy and auth services):
```
GET    /internal/vms                   → list all VMs
GET    /internal/vms/by-user/{uid}     → get VM for a specific user
POST   /internal/vms/boot              → boot a VM for a user
POST   /internal/vms/{vmid}/stop       → stop a VM
POST   /internal/vms/{vmid}/hibernate  → hibernate a VM
GET    /internal/vms/{vmid}/status     → health status of a VM
```

**VM lifecycle management**:
- **Boot**: On first request for a user (triggered by proxy), vmctl boots a new VM via firecracker-go-sdk API socket. Injects gateway token, user identity, and vsock CID.
- **Idle watchdog**: Scans every 30s (configurable). VMs idle beyond the timeout are hibernated or stopped.
- **Memory pressure**: Before booting a new VM, checks host memory. If insufficient, hibernates idle VMs to make room.
- **Health monitoring**: Periodic health checks on running VMs. Detects crashed sandboxes and cleans up.

**Machine classes** (from choiros, preserved):

| Class | vCPU | Memory | Disk | Use Case |
|-------|------|--------|------|----------|
| `minimal` | 2 | 1 GB | 4 GB | Standard user sandbox |
| `worker` | 4 | 4 GB | 16 GB | Worker pool VMs with thick tooling |
| `dev` | 2 | 2 GB | 8 GB | Dev/branch sandboxes |

**Persistence**: Own SQLite database with VM registry (metadata, user mappings, health status).

### 3.11 Custom Goroutine Supervisor (sandbox binary)

**Purpose**: OTP-like supervision for agent goroutines within the sandbox binary without a framework dependency. The supervision quality comes from the pattern, not from an external library.

**Design**: A custom lightweight supervisor built on plain goroutines, Go channels, and context cancellation. This runs inside each sandbox process.

```go
// Supervisor manages a group of child goroutines with restart strategies.
type Supervisor struct {
    name       string
    strategy   RestartStrategy
    children   []*Child
    healthTick time.Duration
    ctx        context.Context
    cancel     context.CancelFunc
}

// RestartStrategy determines how failures are handled.
type RestartStrategy string

const (
    RestartOne RestartStrategy = "restart_one" // restart only the failed child
    RestartAll RestartStrategy = "restart_all" // restart all children when one fails
)

// Child represents a supervised goroutine.
type Child struct {
    Name      string
    Start     func(ctx context.Context) error // the goroutine's main function
    Health    chan struct{}                    // heartbeat channel
    done      chan error                       // signals completion/failure
}
```

**Key patterns**:
- **Parent monitors children via channels**: Each child goroutine sends on its `done` channel when it exits (with nil for clean shutdown, error for failure). The parent `select`s on all children's channels.
- **Restart strategies**: `restart_one` restarts only the failed child (appropriate for independent agents). `restart_all` restarts the entire supervision group (appropriate for agents with shared state).
- **Health checks**: Children periodically send on a heartbeat channel. The supervisor detects stalls when heartbeats stop (configurable timeout).
- **Graceful shutdown via context cancellation**: The supervisor's context is derived from the parent context. Cancelling the parent context cascades to all children. Children check `ctx.Done()` and clean up.
- **Supervision tree**: Supervisors can be children of other supervisors, forming a tree. The root supervisor is the sandbox's main process.

```go
// Example: supervisor for vtext app agents within a sandbox
func NewETextSupervisor(ctx context.Context) *Supervisor {
    sup := &Supervisor{
        name:       "vtext-supervisor",
        strategy:   RestartOne,
        healthTick: 10 * time.Second,
    }
    sup.AddChild(&Child{
        Name:  "vtext-appagent",
        Start: runETextAppAgent,
    })
    sup.AddChild(&Child{
        Name:  "vtext-worker-pool",
        Start: runETextWorkerPool,
    })
    return sup
}

func (s *Supervisor) Run(ctx context.Context) error {
    s.ctx, s.cancel = context.WithCancel(ctx)
    defer s.cancel()

    // Start all children
    for _, child := range s.children {
        s.startChild(child)
    }

    // Monitor loop
    for {
        select {
        case <-s.ctx.Done():
            return s.shutdownAll()
        case err := <-s.anyChildDone():
            child := s.identifyFailedChild(err)
            if s.strategy == RestartAll {
                s.restartAll()
            } else {
                s.restartChild(child)
            }
        }
    }
}
```

This is simple Go — no framework needed. The OTP-like quality comes from the disciplined application of the supervision pattern.

---

## 4. App Layer Specification

The app layer lives entirely within the **sandbox** binary. Apps don't know about the host microservices — they interact with the sandbox's OS layer (agent runtime, scheduler, persistence, event bus) via direct Go interfaces. Requests from the browser reach apps through the chain: Svelte SPA → Caddy → proxy → sandbox → app.

### 4.1 App Lifecycle and Registration

An **app** is a named unit of functionality with an optional UI, optional appagent, and a set of API routes. Apps are registered within the sandbox at boot.

```go
// AppDefinition is the static declaration of an app.
type AppDefinition struct {
    ID          string        `json:"id"`          // unique identifier, e.g., "vtext"
    Name        string        `json:"name"`        // display name, e.g., "E-Text"
    Icon        string        `json:"icon"`
    Description string        `json:"description"`
    HasAgent    bool          `json:"has_agent"`   // whether this app has an appagent
    AgentConfig *AgentConfig  `json:"agent_config,omitempty"`
    APIPrefix   string        `json:"api_prefix"`  // e.g., "/app/vtext"
}

// AppInstance is a running instance of an app for a specific user.
type AppInstance struct {
    AppID    string        `json:"app_id"`
    UserID   string        `json:"user_id"`
    AgentRef *AgentRef     `json:"agent_ref,omitempty"` // reference to the live appagent
    State    AppState      `json:"state"`               // starting, running, stopped
}
```

**Lifecycle**:
1. **Register**: App provides its `AppDefinition` to the sandbox at boot
2. **Instantiate**: When the user opens the app (request arrives via proxy), the sandbox creates an `AppInstance`, optionally spawning the appagent
3. **Run**: The Svelte SPA renders the app's UI client-side, the appagent handles agentic requests, the JSON API routes are live within the sandbox
4. **Stop**: The app instance is torn down when the user closes it (or on session end); appagent is stopped, resources released

**App registry** (built-in apps for v1):

| App ID | Name | Has Agent | Description |
|--------|------|-----------|-------------|
| `vtext` | E-Text | Yes | Versioned document editor (reference implementation) |
| `terminal` | Terminal | Yes | Interactive terminal / code execution |
| `files` | Files | No (or minimal) | File browser for sandbox filesystem |
| `mindgraph` | Mind Graph | No | Work graph Poincaré disk visualization (from cogent) |
| `settings` | Settings | No | User preferences, model config |
| `logs` | Logs | No | Event log viewer |

### 4.2 AppAgent Contract

An appagent is an `Agent` (§3.1) with elevated privileges: it is a **canonical editor** of its app's state. The appagent is the sole agent-side authority over the app's data. AppAgents run as goroutines within the sandbox process.

**Dual interaction**: AppAgents interact with two OS-layer subsystems:
- **Receives from Conductor** (§3.2): The Conductor routes normalized user inputs to the appagent via `HandleUserAction`
- **Submits to Scheduler** (§3.3): When the appagent needs background work, it submits a `WorkRequest` to the Scheduler, which dispatches workers and reports results back

```go
// AppAgent extends Agent with app-specific lifecycle methods.
type AppAgent interface {
    Agent

    // Init is called when the app instance starts.
    Init(ctx context.Context, appCtx AppContext) error

    // HandleUserAction processes a user's request to the app.
    HandleUserAction(ctx context.Context, action UserAction) (ActionResult, error)

    // Shutdown is called when the app instance stops.
    Shutdown(ctx context.Context) error
}

// AppContext provides the appagent with access to sandbox OS services.
type AppContext struct {
    AppID       string
    UserID      string
    Conductor   ConductorClient   // to receive routed user inputs (§3.2)
    Scheduler   SchedulerClient   // to submit background work (§3.3)
    Runtime     RuntimeClient     // to spawn/manage workers (within the sandbox)
    Gateway     GatewayClient     // to make LLM calls (via gateway service)
    Persistence PersistenceClient // to access the app's storage (sandbox-local)
    EventBus    EventBusClient    // to publish/subscribe events (within the sandbox)
}

// UserAction represents a user's request to the app via the agent.
type UserAction struct {
    ID        string `json:"id"`
    Kind      string `json:"kind"`    // "prompt", "edit", "command", etc.
    Payload   any    `json:"payload"`
}
```

**What an appagent can do**:
- Read and write its app's canonical state (e.g., vtext documents in Dolt) within the sandbox
- Delegate subtasks to workers via the Scheduler (within the sandbox)
- Spawn worker agents (goroutines within the sandbox)
- Make LLM calls via the gateway service (sandbox → gateway over vsock/virtio-net)
- Publish events to the EventBus (within the sandbox, forwarded to browser via proxy)
- Register custom tools for its workers via ToolRegistry

**What an appagent cannot do**:
- Access another app's state directly (app isolation within the sandbox)
- Bypass the gateway service (no direct LLM API keys — keys live on the host)
- Create user accounts or modify auth state (auth service is on the host)

### 4.3 Worker Agent Contract

A worker is an `Agent` with restricted privileges: it is a **subordinate non-canonical executor**. Workers run as goroutines within the sandbox process.

```go
// WorkerConfig defines how a worker is spawned.
type WorkerConfig struct {
    AgentCard   AgentCard        `json:"agent_card"`
    ToolSet     ToolSet          `json:"tool_set"`
    Budget      WorkerBudget     `json:"budget"`
}

// WorkerBudget constrains worker execution.
type WorkerBudget struct {
    MaxSteps    int           `json:"max_steps"`
    MaxTokens   int           `json:"max_tokens"`
    MaxDuration time.Duration `json:"max_duration"`
}
```

**What a worker can do**:
- Execute tools from its authorized `ToolSet` (file read, bash within the VM, web search, etc.)
- Send messages/proposals to its appagent via `MessageAgent` tool
- Read (but not write) canonical app state if the appagent exposes it as a read-only resource

**What a worker cannot do**:
- Directly write to canonical app state (enforced by ToolSet — no direct Dolt/DB access)
- Spawn other agents (only appagents can delegate)
- Access the gateway directly (worker LLM calls are mediated by the agent runtime, which routes through the gateway service)

### 4.4 Canonical Editing: Users and AppAgents as Peers

This is a core design principle. Both users and appagents are **canonical editors** — their edits have equal authority and create canonical new versions.

**How it works for vtext** (the pattern all apps should follow):

```
User (via Svelte UI → Caddy → proxy → sandbox) ──┐
                                                   ├──→ VText Canonical State (Dolt, in sandbox) ──→ Version N+1
AppAgent (goroutine within sandbox) ──────────────┘

Workers ──→ Messages/Results ──→ AppAgent ──→ VText Canonical State ──→ Version N+1
```

1. **User edits**: User may make multiple edits in the Svelte editor before explicitly prompting. That batch of changes becomes the next canonical user-authored version when the user submits.

2. **AppAgent edits**: AppAgent processes a user prompt, decides on changes, writes to Dolt within the sandbox. Canonical. Equivalent authority to user edits.

3. **Worker messages**: Workers cannot commit directly. They send structured messages/results to the appagent via `MessageAgent`. The appagent reviews them and decides whether and how to rewrite the canonical document.

**Concurrency model (v1)**: Single-user, serialized writes on the `main` branch. The user and the appagent take turns — there is no concurrent editing. Real-time collaborative editing (CRDT/OT) and branch-based isolation for concurrent user+agent edits are deferred.

### 4.5 API Exposure Pattern

Every app exposes a JSON API within the sandbox that the Svelte frontend (via proxy) and external agents consume:

```
/app/{app_id}/api/...     → App-specific JSON API (served by sandbox)
/app/{app_id}/ws          → App-specific WebSocket (proxied by proxy service)
/app/{app_id}/sse         → App-specific SSE stream (proxied by proxy service)
```

Full request path: `Browser → Caddy → proxy service → sandbox → /app/{app_id}/api/...`

All endpoints return JSON. The Svelte SPA handles all rendering client-side.

Example for `vtext`:
```
GET    /app/vtext/api/documents                    → list documents (JSON)
POST   /app/vtext/api/documents                    → create document (JSON)
GET    /app/vtext/api/documents/{id}               → get document (JSON, current version)
GET    /app/vtext/api/documents/{id}?at={commit}   → get document (JSON, historical version)
PUT    /app/vtext/api/documents/{id}               → update document (user edit → Dolt commit)
GET    /app/vtext/api/documents/{id}/history       → version history (Dolt log, JSON)
GET    /app/vtext/api/documents/{id}/diff?from=&to= → diff between versions (JSON)
POST   /app/vtext/api/documents/{id}/prompt        → submit prompt to appagent
GET    /app/vtext/api/documents/{id}/blame         → blame (who edited what, JSON)
```

---

## 5. VText App (Reference Implementation)

### 5.1 Overview

The primary document app is **`vtext`**. Historical `vtext` compatibility routes may exist temporarily during migration, but the active product, runtime, and storage direction should all converge on `vtext`. It is a version-native living document system, not a chat pane and not a sidebar-heavy editor. The core UI is one primary editable document surface. User prompts, user edits, and appagent rewrites all materialize as document versions.

`vtext` demonstrates the full app model: Dolt-backed versioned storage, canonical editing by users and appagent, worker delegation through messages, and a rich JSON API. The app logic and Dolt database run **inside the sandbox binary**.

The editor UI is built with **Svelte** and uses **Pretext** (`@chenglou/pretext`) for custom layout in the browser. Pretext matters here not just for text measurement, but for document-native rendering features such as:
- inline superscript citation markers
- click-to-expand inline transclusions
- transcluded blocks that can render at a narrower measure than the main prose
- embedded non-text artifacts such as images, audio, video, and interactive elements

The important rule is that the document remains the primary organizing surface. Citations, transclusions, and artifacts appear inline in the flow of the document, not as the primary UI in sidebars or marginalia panes.

### 5.2 Dolt-Backed Versioned Storage

Dolt runs **in-process inside the sandbox binary** via the embedded driver. Each sandbox VM has its own Dolt database.

**Storage location**: Within the sandbox VM filesystem, e.g., `/data/vtext/.dolt/` — one Dolt database per sandbox (per user). Completing this embedded Dolt `vtext` store is an architectural prerequisite for the deeper `vmctl` / microVM lifecycle pass, because we want to validate the VM path against the real version-native sandbox state model rather than a temporary SQLite-backed document store.

**Connection** (embedded mode):

```go
import (
    "database/sql"
    embedded "github.com/dolthub/driver"
)

func OpenETextDB() (*sql.DB, error) {
    dbPath := "/data/vtext"
    dsn := fmt.Sprintf("file://%s?commitname=System&commitemail=system@choiros.local&database=vtext",
        dbPath)
    cfg, err := embedded.ParseDSN(dsn)
    if err != nil {
        return nil, err
    }
    connector, err := embedded.NewConnector(cfg)
    if err != nil {
        return nil, err
    }
    return sql.OpenDB(connector), nil
}
```

**Schema**:

```sql
CREATE TABLE documents (
    doc_id      VARCHAR(36) DEFAULT (UUID()) PRIMARY KEY,
    title       VARCHAR(512) NOT NULL,
    doc_type    VARCHAR(64) NOT NULL DEFAULT 'text',
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE content (
    doc_id        VARCHAR(36) NOT NULL,
    section_id    VARCHAR(36) DEFAULT (UUID()),
    section_order INT NOT NULL,
    heading       VARCHAR(256),
    body          LONGTEXT NOT NULL,
    content_hash  VARCHAR(64),  -- SHA-256 for quick equality checks
    PRIMARY KEY (doc_id, section_id),
    INDEX idx_doc_order (doc_id, section_order),
    FOREIGN KEY (doc_id) REFERENCES documents(doc_id)
);

CREATE TABLE citations (
    citation_id   VARCHAR(36) DEFAULT (UUID()) PRIMARY KEY,
    doc_id        VARCHAR(36) NOT NULL,
    section_id    VARCHAR(36),
    source_url    VARCHAR(2048),
    source_title  VARCHAR(512),
    citation_kind VARCHAR(64) NOT NULL, -- 'retrieved', 'inline_ref', 'builds_on', 'contradicts'
    context_text  TEXT,
    created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (doc_id) REFERENCES documents(doc_id)
);

CREATE TABLE metadata (
    doc_id     VARCHAR(36) NOT NULL,
    meta_key   VARCHAR(128) NOT NULL,
    meta_value TEXT,
    PRIMARY KEY (doc_id, meta_key),
    FOREIGN KEY (doc_id) REFERENCES documents(doc_id)
);
```

**UUID primary keys** throughout (recommended by Dolt for merge-friendliness — no auto-increment conflicts across branches).

**Content split into sections** for granular cell-level diffs. Each section is independently diffable and mergeable by Dolt.

### 5.3 Version Creation

Every meaningful edit creates a Dolt commit with explicit author attribution:

```go
// SaveUserEdit creates a canonical version attributed to the user.
func (s *ETextStore) SaveUserEdit(ctx context.Context, req UserEditRequest) (string, error) {
    tx, err := s.db.BeginTx(ctx, nil)
    if err != nil {
        return "", err
    }
    defer tx.Rollback()

    // Apply the edit
    _, err = tx.ExecContext(ctx,
        `UPDATE content SET body = ?, content_hash = SHA2(?, 256), updated_at = NOW()
         WHERE doc_id = ? AND section_id = ?`,
        req.NewContent, req.NewContent, req.DocID, req.SectionID)
    if err != nil {
        return "", err
    }

    // Update document timestamp
    _, err = tx.ExecContext(ctx,
        `UPDATE documents SET updated_at = NOW() WHERE doc_id = ?`, req.DocID)
    if err != nil {
        return "", err
    }

    // Dolt: stage and commit
    _, err = tx.ExecContext(ctx, `CALL dolt_add('.')`)
    if err != nil {
        return "", err
    }

    var commitHash string
    err = tx.QueryRowContext(ctx,
        `CALL dolt_commit('-m', ?, '--author', ?)`,
        fmt.Sprintf("User edit: %s §%s", req.DocID, req.SectionID),
        fmt.Sprintf("%s <%s>", req.UserName, req.UserEmail),
    ).Scan(&commitHash)
    if err != nil {
        return "", err
    }

    return commitHash, tx.Commit()
}

// SaveAppAgentEdit creates a canonical version attributed to the appagent.
func (s *ETextStore) SaveAppAgentEdit(ctx context.Context, req AgentEditRequest) (string, error) {
    // Same pattern, but author is the appagent:
    // --author "ETextAgent <vtext-agent@choiros.local>"
    // ... (analogous to SaveUserEdit)
}
```

### 5.4 AppAgent Behavior

The `vtext` appagent runs as a goroutine within the sandbox and is the **sole agent-side canonical writer**. It:

1. Receives user prompts via `HandleUserAction` (request arrived via Caddy → proxy → sandbox)
2. Plans what changes are needed (LLM call via gateway service over vsock)
3. Optionally delegates research/analysis/execution to workers (goroutines within the sandbox)
4. Rewrites the document into a new version when appropriate, creating a new Dolt commit as the appagent author
5. Publishes events so the Svelte UI updates in real-time (events flow: sandbox → proxy → browser via SSE/WebSocket)

`vtext` is not a request/response chat surface. The appagent owns the cumulative document state for an ongoing process or project. Prompts and edits are interpreted as changes against the current version, not as isolated conversational turns.

```go
type ETextAppAgent struct {
    store     *ETextStore       // Dolt-backed storage (in-process within sandbox)
    scheduler SchedulerClient   // for delegating to workers (within sandbox)
    runtime   RuntimeClient     // for spawning workers (within sandbox)
    gateway   GatewayClient     // for LLM calls (sandbox → gateway service)
    eventBus  EventBusClient    // for real-time updates (within sandbox, proxied to browser)
}

func (a *ETextAppAgent) HandleUserAction(ctx context.Context, action UserAction) (ActionResult, error) {
    switch action.Kind {
    case "prompt":
        return a.handlePrompt(ctx, action.Payload)
    case "edit":
        // User direct edit — goes straight to Dolt, bypasses agent
        return a.handleDirectEdit(ctx, action.Payload)
    default:
        return ActionResult{}, fmt.Errorf("unknown action kind: %s", action.Kind)
    }
}

func (a *ETextAppAgent) handlePrompt(ctx context.Context, payload any) (ActionResult, error) {
    prompt := payload.(PromptPayload)

    // 1. Decide what to do (LLM call via gateway service)
    decision, err := a.decideAction(ctx, prompt)
    if err != nil {
        return ActionResult{}, err
    }

    // 2. If research needed, submit work to Scheduler (§3.3)
    if decision.NeedsResearch {
        a.scheduler.SubmitWork(ctx, WorkRequest{
            AppID:     "vtext",
            Objective: decision.ResearchObjective,
            Input:     []Part{{Kind: "text", Content: json.RawMessage(prompt.Text)}},
        })
    }

    // 3. Apply edits to Dolt (canonical appagent edit, in-process)
    for _, edit := range decision.Edits {
        _, err := a.store.SaveAppAgentEdit(ctx, edit)
        if err != nil {
            return ActionResult{}, err
        }
        // Publish real-time update (flows through proxy to browser)
        a.eventBus.Publish(ctx, Event{
            Kind: "vtext.section.updated",
            Data: edit,
        })
    }

    return ActionResult{Status: "completed"}, nil
}
```

### 5.5 Worker Interaction Model

Workers run as goroutines within the sandbox and cannot commit to Dolt directly. They may read canonical `vtext` state if exposed as a read-only resource, but they never directly edit the document.

The intended first-cut topology is:
- one `super` agent per microVM, responsible for execution orchestration
- a configurable number of researcher workers per microVM
- additional coagents only when spawned intentionally by `super` or another allowed worker

```go
// Worker tool: message_appagent
// Workers send findings, results, questions, and artifact references to the
// appagent. The appagent decides whether and how to rewrite the document.
type WorkerMessage struct {
    DocID         string       `json:"doc_id"`
    MessageType   string       `json:"message_type"` // "finding" | "result" | "question" | "artifact" | "error" | "status"
    Summary       string       `json:"summary"`
    Body          string       `json:"body,omitempty"`
    ArtifactRefs  []ArtifactRef `json:"artifact_refs,omitempty"`
    RelatedRange  string       `json:"related_range,omitempty"`
}
```

The worker sends a structured message to the appagent via Go channel (in-process). The appagent receives it, evaluates it (possibly with an LLM call via gateway), and may:
- incorporate it into a new appagent-authored document version
- hold it as context for a later rewrite
- ignore/reject it

Workers do not send canonical diffs as their primary contract. They send findings and artifacts; the appagent decides how those affect the document.

### 5.6 User Interaction Model

Users interact with `vtext` through the Svelte SPA (browser → Caddy → proxy → sandbox):

1. **Blank document or prompt bar input creates the first version**. A new `vtext` starts at `v0`, seeded by the user prompt or initial user-authored content.
2. **Direct editing is local until the user prompts**: the user may make multiple edits anywhere in the document before pressing the prompt/action button. That edit batch becomes the next canonical user-authored version.
3. **User edits also inform the appagent**: when the user submits after editing, the appagent should receive structured edit/diff context describing the batch of changes and any accompanying instruction.
4. **Prompting**: User submits a natural language prompt through the prompt bar or within `vtext` → conductor routes to `vtext` appagent by default unless there is a better explicit route → appagent processes it, may delegate, and rewrites the document into a new version. User sees changes in real-time via SSE/WebSocket (sandbox → proxy → browser).
5. **Version history**: User browses commit history (Dolt log), views diffs between versions, and reverts to previous versions.
6. **Transclusion and citation UX**: citations and transcluded artifacts are rendered inline in the document flow, not as the primary representation in sidebars.
7. **Desktop/file-browser unity**: ordinary textual files should open in `vtext` so the file browser and the document workflow converge rather than splitting into separate editing surfaces.

### 5.7 API Surface

All routes served by the sandbox, reached via Caddy → proxy:

```
# Documents
GET    /app/vtext/api/documents                          → list all documents
POST   /app/vtext/api/documents                          → create new document
GET    /app/vtext/api/documents/{id}                     → get document with all sections
PUT    /app/vtext/api/documents/{id}                     → update document metadata
DELETE /app/vtext/api/documents/{id}                     → delete document

# Content (sections)
GET    /app/vtext/api/documents/{id}/sections            → list sections
PUT    /app/vtext/api/documents/{id}/sections/{sid}      → update section (user edit → Dolt commit)
POST   /app/vtext/api/documents/{id}/sections            → add section
DELETE /app/vtext/api/documents/{id}/sections/{sid}      → delete section

# Versioning
GET    /app/vtext/api/documents/{id}/history             → commit log (dolt_log)
GET    /app/vtext/api/documents/{id}/at/{commit}         → document at specific version
GET    /app/vtext/api/documents/{id}/diff?from=X&to=Y    → diff between versions
GET    /app/vtext/api/documents/{id}/blame               → blame per section
POST   /app/vtext/api/documents/{id}/revert/{commit}     → revert to specific version

# Agent interaction
POST   /app/vtext/api/documents/{id}/prompt              → submit prompt to appagent
GET    /app/vtext/api/documents/{id}/proposals            → list pending worker proposals

# Real-time
GET    /app/vtext/sse/documents/{id}                     → SSE stream for document changes
```

---

## 6. Agent Runtime Contract

### 6.1 Agent Identity and Addressing

Every agent has a globally unique ID (ULID-based, same ID generation pattern as cogent). Agent identities are scoped to their sandbox:

```go
// AgentID format: "agent_" + ULID
// Examples:
//   agent_01HXYZ... (an vtext appagent within a sandbox)
//   agent_01HABC... (a research worker within a sandbox)

func GenerateAgentID() string {
    return "agent_" + ulid.Make().String()
}
```

Agents are addressable by ID within their sandbox. The sandbox's runtime maintains a registry of all local agents.

```go
// AgentRegistry tracks all live agents within a sandbox.
type AgentRegistry struct {
    mu     sync.RWMutex
    agents map[string]AgentLocation
}

type AgentLocation struct {
    AgentID  string
    Kind     string // "local_goroutine" (within sandbox), "remote_sandbox" (another VM)
    Address  string // for remote: proxy route to other sandbox
}
```

### 6.2 Messaging (Coagent Communication)

Inter-agent messaging within a sandbox uses **Go channels**. Cross-sandbox communication routes through the host proxy: sandbox A → host proxy → sandbox B.

**Within a sandbox** (the primary case):

**Task-based delegation** (structured, tracked):
```go
// Appagent delegates to worker (both goroutines within the same sandbox)
result, err := runtime.DelegateTask(ctx, DelegateRequest{
    From:     appagentID,
    To:       workerID,          // specific worker
    // OR
    ToSkill:  "web_research",    // runtime picks a suitable agent
    Task:     task,
    Budget:   budget,
})
```

**Message-based communication** (lightweight, fire-and-forget or request-response):
```go
// Worker sends proposal to appagent (Go channel within the sandbox)
err := runtime.SendMessage(ctx, Message{
    From:    workerID,
    To:      appagentID,
    Channel: "proposals",
    Parts:   []Part{{Kind: "data", Content: proposalJSON}},
})

// Appagent subscribes to messages
ch := runtime.Subscribe(ctx, appagentID, "proposals")
for msg := range ch {
    // process proposal
}
```

**Cross-sandbox** (remote agents in other VMs):
Workers don't talk directly to other sandboxes. Cross-sandbox communication routes through the host proxy: sandbox → host proxy → other sandbox VM. This is mediated and authenticated.

**Channel model** (design from cogent's `ChannelManager`, copied to `go-choir`):
Named channels for pub/sub between agents within a sandbox. In-process channels are native Go channels.

### 6.3 Tool Calling

Tools are the agent's hands. They are defined and registered via the `ToolRegistry` pattern (copied from cogent) within the sandbox — plain Go functions registered in a map. Agents call tools as direct Go function calls (in-process). There is no subprocess spawning for tool execution (except for the `bash` tool which spawns actual shell commands).

For scheduler interaction, agents have tools like `work_list`, `work_claim`, `work_complete` that internally call the scheduler's Go API — no CLI subprocess involved.

```go
// ToolRegistry manages available tools for agents within a sandbox.
type ToolRegistry struct {
    tools map[string]ToolFunc
}

// ToolFunc is the signature for a tool implementation.
type ToolFunc func(ctx context.Context, params json.RawMessage) (json.RawMessage, error)

// Built-in tools (copied from cogent's tool implementations):
// - read_file, write_file, edit_file — file I/O (within VM filesystem)
// - glob, grep — file search (within VM filesystem)
// - bash — command execution (within the VM, the ONLY tool that spawns subprocesses)
// - web_search — multi-provider web search (routed via gateway for API keys)
// - web_fetch — URL content fetching
// - git_status, git_diff, git_commit — git operations (within VM filesystem)
// - message_agent — send message to another agent (within the sandbox)
// - work_list, work_claim, work_complete — scheduler interaction (direct Go API calls)
// - finished — signal task completion
```

**Tool authorization**: The sandbox's agent runtime filters the tool registry based on the agent's `ToolSet` (§3.1). Workers don't get `write_file` on canonical state; they get `message_agent` instead.

**LLM tool calls**: When an agent needs to call an LLM (e.g., during the tool-calling loop), the sandbox uses the hand-rolled streaming clients (copied from cogent) configured to point at the gateway service endpoint (reachable via vsock from the VM). The gateway injects the real API key. The tool-calling loop from cogent IS the agent runtime — running directly as goroutines within the sandbox process, without any adapter wrapper.

### 6.4 File Access

Agents access files through tool calls within the sandbox VM's filesystem:

- **AppAgents**: access to their app's data directory plus project files within the VM
- **Workers**: access to a temporary working directory within the VM; output goes via messages to appagent
- All file access is sandboxed by the VM boundary — agents cannot access host filesystem

### 6.5 Communication Architecture

```
Within a Sandbox (Go channels — the primary model)
├── Conductor → AppAgent: routes normalized user inputs to the correct appagent
├── AppAgent → Scheduler: submits background work, receives results
├── Agent ↔ Agent: direct Go method calls via Agent interface
├── Agent ↔ Tools: direct Go function calls via ToolRegistry (no subprocess spawning except bash)
├── Agent ↔ Scheduler: direct Go function calls via scheduler tools (work_list, work_claim, etc.)
├── Agent ↔ EventBus: direct Go channel pub/sub
└── Used for: all goroutine-based agents within a sandbox (zero overhead)

Sandbox → Host Services (HTTP over vsock/virtio-net)
├── Sandbox → Gateway: LLM API calls (gateway injects keys, proxies upstream)
├── Sandbox → Proxy → Other Sandbox: cross-sandbox agent communication (clean HTTP API)
└── Used for: LLM access, cross-VM agent messaging (same interface the browser uses)

Browser → Sandbox (HTTP via Caddy → Proxy → Sandbox)
├── Svelte SPA → Caddy → Proxy → Sandbox: JSON API, app routes
├── Svelte SPA → Caddy → Proxy → Sandbox: WebSocket (terminal, agent chat)
├── Svelte SPA → Caddy → Proxy → Sandbox: SSE (status, document updates)
└── Used for: all user-facing interactions
```

### 6.6 Local vs Remote: Same Interface, Transport Hidden

```go
// Within a sandbox, the caller doesn't know or care where the agent runs.
// Most agents are local goroutines. Remote agents are in other sandboxes.

// This works the same whether workerID is a local goroutine or a remote sandbox agent:
result, err := runtime.DelegateTask(ctx, DelegateRequest{
    From: appagentID,
    To:   workerID,
    Task: task,
})
```

**Local dispatch** (primary): Direct Go method call on the agent's `HandleTask` within the sandbox process.

**Remote dispatch** (cross-sandbox): Serialize the `Task` to JSON, send via HTTP through the host proxy to the target sandbox VM.

```go
func (r *AgentRuntime) DelegateTask(ctx context.Context, req DelegateRequest) (TaskResult, error) {
    loc, ok := r.registry.Lookup(req.To)
    if !ok {
        return TaskResult{}, fmt.Errorf("agent not found: %s", req.To)
    }

    switch loc.Kind {
    case "local_goroutine":
        agent := r.localAgents[req.To]
        return agent.HandleTask(ctx, req.Task)
    case "remote_sandbox":
        // Route through host proxy to other sandbox VM
        return r.httpClient.SendTask(ctx, loc.Address, req.Task)
    default:
        return TaskResult{}, fmt.Errorf("unknown agent location kind: %s", loc.Kind)
    }
}
```

### 6.7 Session/Lifecycle Model

Agent sessions follow the session model (copied from cogent), running within the sandbox:

```go
// AgentSession tracks an agent's execution context across turns.
type AgentSession struct {
    SessionID     string        `json:"session_id"`
    AgentID       string        `json:"agent_id"`
    ParentSession string        `json:"parent_session,omitempty"` // for worker sessions
    Status        SessionStatus `json:"status"`                   // active, paused, completed, failed
    CreatedAt     time.Time     `json:"created_at"`
    LastTurnAt    time.Time     `json:"last_turn_at"`
}

// Turn represents one agent reasoning step (LLM call + tool executions).
type Turn struct {
    TurnID      string    `json:"turn_id"`
    SessionID   string    `json:"session_id"`
    Input       string    `json:"input"`
    ToolCalls   []ToolCall `json:"tool_calls"`
    Output      string    `json:"output"`
    TokenUsage  Usage     `json:"token_usage"`
    CreatedAt   time.Time `json:"created_at"`
}
```

**Crash recovery**: The "agents may always stop, the system may always resume" invariant (from cogent) is preserved. Sessions are persisted in the sandbox's Dolt database. The sandbox can resume a session from the last persisted state. If the VM itself crashes, vmctl detects the failure and can reboot the sandbox — the sandbox restores from its persisted state.

**History compression**: Proactive history compression (LLM-based summarization of old turns approaching context limits, pattern copied from cogent) is preserved within the sandbox.

---

## 7. Technology Stack

### 7.1 Concrete Recommendations

| Layer | Technology | Service(s) | Rationale |
|-------|-----------|-----------|-----------|
| **Language** | Go 1.25+ | All 5 binaries | Whole Go rewrite |
| **Frontend Framework** | Svelte | Svelte SPA (served by Caddy) | Reactive SPA, compiled, small runtime |
| **Text Layout** | Pretext (`@chenglou/pretext`) | Svelte SPA (browser) | DOM-free text measurement for vtext editor |
| **Edge/Reverse Proxy** | Caddy | Caddy (edge) | TLS, static assets, route dispatch |
| **Agent Comms (local)** | Go channels | sandbox | Direct, typed, zero overhead |
| **Agent Comms (remote)** | HTTP API + vsock | proxy + sandbox | Same interface shape, serialized |
| **Tool System** | ToolRegistry (from cogent) | sandbox | Battle-tested, plain Go functions |
| **LLM Clients** | Streaming clients (from cogent) | sandbox (→ gateway) | Hand-rolled Anthropic + OpenAI streaming |
| **Supervision** | Custom goroutine supervisor | sandbox | Lightweight, no framework dependency |
| **Host DB** | `modernc.org/sqlite` | auth, vmctl | Pure Go, already used by cogent. Host services only. |
| **Sandbox DB** | Dolt embedded (`dolthub/driver`) | sandbox | In-process versioned SQL. Sole database engine inside the sandbox — all state (vtext, work graph, sessions, events, desktop state). |
| **ID Generation** | `oklog/ulid` | sandbox | Already used by cogent |
| **Config** | `BurntSushi/toml` | All services | Already used by cogent |
| **MicroVM Lifecycle** | `firecracker-go-sdk` | vmctl | Direct API socket management |
| **VM Images** | Nix (`microvm.nix`) | Build system | NixOS guest builds |
| **Auth** | `go-webauthn` | auth | WebAuthn passkey auth |
| **Crypto** | stdlib `crypto/ed25519` | sandbox | Capability tokens |
| **WebSocket** | `gorilla/websocket` or `coder/websocket` | proxy, sandbox | Real-time frontend comms |
| **HTTP Router** | `net/http` (Go 1.22+) | All services | Standard library |
| **Observability** | OpenTelemetry Go SDK | All services | Structured tracing across services |

### 7.2 What's NOT in the Stack

- **No A2A protocol SDK** — too heavyweight; plain Go interfaces + HTTP replace it
- **No MCP protocol SDK** — too heavyweight; ToolRegistry pattern (from cogent) replaces it
- **No ADK-Go** (Google Agent Development Kit) — was only justified by A2A/MCP
- **No actor framework** (Proto.Actor, Ergo, Hollywood) — plain goroutines + channels + custom supervisor in sandbox
- **No LLM abstraction library** (go-llm, langchaingo, Genkit) — keeping cogent's hand-rolled clients (copied to `go-choir`)
- **No CLI framework** (cobra, etc.) — no CLI ships with the system; binaries are Go `main()` functions that start HTTP servers (or in vmctl's case, a Firecracker manager); operator management is via HTTP admin API
- **No server-side HTML templating** (htmx, templ) — Svelte SPA replaces server-rendered HTML
- **No JavaScript frameworks besides Svelte** (no React, Vue, Alpine.js)
- **No BAML** — Go-native structured output replaces BAML
- **No ractor** — no Rust actor framework
- **No Cargo/Rust toolchain** — pure Go build
- **No `embed.FS` for frontend assets** — Caddy serves the Svelte build as static files
- **No `cogent serve` monolithic dev mode** — the distributed architecture is the only architecture
- **No `cogent` CLI** — agents interact via direct Go function calls, not CLI subprocesses
- **No shared database across services** — each service owns its own storage

---

## 8. Migration Path

### 8.1 New Repository with Internals Copied from Cogent

The unified system is built in **`go-choir`** — a new repository, not an extension of the cogent repo. `go-choir` has a clean module structure designed for the 5-binary architecture. Valuable internal packages are copied from cogent and adapted to the new structure. The cogent repo is preserved as a reference.

**What to copy from cogent** (and where it goes in `go-choir`):

| Cogent Source | `go-choir` Target | What to Adapt |
|--------------|----------------|---------------|
| `client_anthropic.go` | `go-choir/internal/runtime/llm/anthropic.go` | Point at gateway endpoint instead of direct upstream |
| `client_openai.go` | `go-choir/internal/runtime/llm/openai.go` | Point at gateway endpoint instead of direct upstream |
| `internal/adapters/native/loop.go` | `go-choir/internal/runtime/loop.go` | Remove adapter wrapper; this IS the agent runtime |
| `internal/adapters/native/tools*.go` | `go-choir/internal/runtime/tools/` | ToolRegistry and tool implementations; add scheduler tools (`work_list`, `work_claim`, etc.) |
| `internal/adapters/native/channel.go` | `go-choir/internal/runtime/channel.go` | Co-agent messaging channels |
| `internal/core/` | `go-choir/internal/types/` | Core domain types, ID generation (ULID), config types |
| `internal/store/` | `go-choir/internal/store/` | Table schemas and CRUD patterns; adapted from SQLite to Dolt's MySQL-compatible SQL dialect (driver changes from `modernc.org/sqlite` to `dolthub/driver`). All tables go into the single per-sandbox Dolt database. |
| `internal/events/events.go` | `go-choir/internal/runtime/events.go` | EventBus pattern |
| `internal/catalog/` | `go-choir/internal/gateway/catalog/` | Provider catalog |
| `internal/pricing/` | `go-choir/internal/gateway/pricing/` | Pricing registry |
| `internal/notify/` | `go-choir/internal/notify/` | Email notifications |
| `skills/` | `go-choir/internal/runtime/skills/` | Skill definitions |
| `mind-graph/` | (separate Svelte app) | Migrated to Svelte SPA |

**What is NOT copied** (eliminated):

| Cogent Source | Reason for Elimination |
|--------------|----------------------|
| `internal/cli/` | No CLI in the new system |
| `internal/adapterapi/` | No adapter abstraction layer |
| `internal/adapters/claude/` | External/subprocess adapters eliminated |
| `internal/web/` | Replaced by Svelte SPA served by Caddy |
| `internal/service/` (serve runtime) | Split: proxy handles routing, sandbox handles app API |
| `internal/channelmeta/` | Evaluate if still needed |

### 8.2 What Choiros-rs Functionality is Reimplemented in Go

| Choiros-rs Component | Go Implementation | Target Binary | Complexity |
|---------------------|-------------------|--------------|------------|
| Provider Gateway (`provider_gateway.rs`, 625 lines) | New `internal/gateway` package | **gateway** | Medium |
| WebAuthn Auth (`auth/`, ~500 lines) | New `internal/auth` package using `go-webauthn/webauthn` | **auth** | Medium |
| Proxy/Routing (hypervisor routing) | New `internal/proxy` package | **proxy** | Medium |
| VM Management (sandbox registry) | New `internal/vmctl` package using `firecracker-go-sdk` | **vmctl** | High |
| Desktop Actor (`desktop.rs`, 2108 lines) | New `internal/desktop` package (simpler service with mutex) | **sandbox** | Medium |
| Writer Actor → E-Text AppAgent (`writer/mod.rs`, 2477 lines) | New `internal/apps/vtext` package, Dolt instead of `.qwy` | **sandbox** | High |
| Terminal Actor (`terminal.rs`, 2361 lines) | New `internal/apps/terminal` package, Go PTY via `creack/pty` | **sandbox** | High |
| Conductor → Conductor + Scheduler (`conductor/`, ~3000 lines) | Split: input routing → Conductor (§3.2), work tracking → Scheduler (§3.3, merged with cogent's work graph) | **sandbox** | High |
| Event Store Actor (`event_store.rs`) | Direct Dolt access via `internal/store` | **sandbox** | Eliminated |
| Event Bus Actor | EventBus pattern (copied from cogent) extended | **sandbox** | Low |
| Supervisor Tree (`supervisor/mod.rs`, 1570 lines) | Custom goroutine supervisor (§3.11) | **sandbox** | Medium |
| Agent Harness (`agent_harness/`, 3120 lines) | Tool-calling loop (copied from cogent) extended | **sandbox** | Medium |
| Shared Types (`shared-types/src/lib.rs`, 2230 lines) | Go types in relevant packages | Shared module | Low |
| Dioxus Frontend (`dioxus-desktop/`, ~6000 lines) | Svelte SPA + Pretext | Svelte build (served by Caddy) | High |

### 8.3 Concrete Phasing

Each service can be built and tested incrementally. The real distributed architecture is the only architecture — no monolithic dev mode.

#### Phase 1: New Repo Scaffolding + Auth + Proxy + Basic Svelte Shell (Weeks 1-4)

**Goal**: Create `go-choir`, scaffold all 5 binaries, prove the distributed architecture works.

1. **Create `go-choir`** with clean Go module structure:
   - `go-choir/cmd/auth/`, `go-choir/cmd/proxy/`, `go-choir/cmd/vmctl/`, `go-choir/cmd/gateway/`, `go-choir/cmd/sandbox/` (each a `main.go` that starts an HTTP server)
   - `go-choir/internal/` packages: `runtime/`, `store/`, `types/`, `gateway/`, `auth/`, `vmmanager/`, `proxy/`
2. **Copy valuable internals from cogent** into `go-choir`: LLM streaming clients, tool-calling loop, ToolRegistry, co-agent messaging, core types/ID generation, store schema (adapted from SQLite to Dolt), EventBus pattern. Copy comprehensive tests alongside the code.
3. Build the **auth** service: WebAuthn registration/login, session tokens, user SQLite DB
4. Build the **proxy** service: session validation, static VM routing (hardcoded for dev)
5. Build a basic **Svelte SPA**: login flow, empty desktop shell
6. Configure **Caddy**: route `/auth/*` to auth, `/api/*` to proxy, serve Svelte static assets

**Deliverable**: `go-choir` scaffolded with 5 binary entry points. User can register, login, and see an empty desktop. Auth → proxy pipeline validated.

#### Phase 2: Sandbox Binary with Agent Runtime (Weeks 5-8)

**Goal**: Prove the VM boundary works. Proxy routes requests to a sandbox binary.

1. Build the **sandbox** binary: HTTP server with app registry, desktop state API
2. Wire the copied agent runtime internals (tool-calling loop, ToolRegistry, conductor, scheduler) into the sandbox binary
3. Wire proxy → sandbox communication (vsock for production, TCP for dev)
4. Port desktop state management from choiros DesktopActor
5. Basic apps: mind-graph, settings, logs (no appagents yet)

**Deliverable**: Proxy forwards requests to sandbox. Desktop renders apps. Agent runtime runs within sandbox.

#### Phase 3: E-Text App with Dolt (Weeks 9-12)

**Goal**: Prove the product works end-to-end through the distributed architecture.

1. Integrate Dolt embedded driver into sandbox
2. Build vtext AppAgent (handles prompts, delegates to workers, commits to Dolt)
3. Build vtext Svelte UI with Pretext for text measurement/layout (in browser)
4. Implement version history, diff, blame via Dolt system tables
5. Worker proposal flow (within sandbox: worker → Go channel → appagent → Dolt commit)
6. Full JSON API surface for vtext

**Deliverable**: Users can create documents, edit them, prompt the agent, see version history. Full Caddy → proxy → sandbox → Dolt pipeline.

#### Phase 4: vmctl with Firecracker (Weeks 13-16)

**Goal**: Prove VM lifecycle management works.

1. Build the **vmctl** service: Firecracker API socket management, VM registry, boot/stop/hibernate
2. Replace hardcoded proxy routing with dynamic vmctl lookups
3. Idle watchdog, memory pressure checks, health monitoring
4. Machine class support (minimal, worker, dev profiles)
5. Gateway token injection at VM boot

**Deliverable**: VMs boot on demand, idle VMs hibernate, proxy dynamically routes to correct VM.

#### Phase 5: Gateway + Security Boundary (Weeks 17-20)

**Goal**: Prove the security boundary works. LLM keys never reach sandboxes.

1. Build the **gateway** service: multi-provider LLM proxying, API key injection, rate limiting per sandbox
2. Bedrock request rewriting (ported from choiros)
3. Configure sandbox LLM clients to point at gateway endpoint
4. Capability token enforcement at VM boundary
5. Web search API key management (Exa, Tavily, Brave, Serper rotation)

**Deliverable**: LLM calls route sandbox → gateway → upstream. API keys never in VMs. Rate limiting per sandbox.

#### Phase 6: Polish and Migration (Weeks 21-24)

**Goal**: Production readiness.

1. Branch/dev sandboxes
2. Admin API (sandbox management, system stats)
3. Migration tooling for existing choiros-rs users
4. NixOS deployment configuration for all 5 services + Caddy
5. E2E testing across the full distributed stack
6. Performance optimization (proxy latency, WebSocket throughput)
7. Observability (OpenTelemetry tracing across services)

**Deliverable**: Production-ready distributed system.

### 8.4 What Can Be Dropped Entirely

| Component | Why It Can Be Dropped |
|-----------|----------------------|
| `shared-types` crate | Single-language system — no need for cross-language type sharing |
| BAML code generation | Go-native structured output replaces BAML |
| ractor dependency | Replaced by plain goroutines + channels + custom supervisor in sandbox |
| `.qwy` file format | Replaced by Dolt relational storage in sandbox |
| Overlay/pending system | v1 uses serialized writes; no overlay/branch isolation needed |
| Event Store Actor | Direct Dolt access within sandbox |
| Gateway token dance (kernel cmdline injection) | Simplified — vmctl injects token at boot |
| `cogent serve` monolithic mode | No monolithic dev mode. The distributed architecture is the only architecture. |
| `cogent` CLI (cobra) | Eliminated. No CLI ships with the system. Agents use direct Go function calls. Operator management via HTTP admin API. |
| Adapter abstraction (`adapterapi`) | Eliminated. One execution model: in-process agent execution via the native tool-calling loop. |
| Claude adapter (`internal/adapters/claude`) | Eliminated. No external/subprocess-based adapters. |
| `embed.FS` for frontend | Caddy serves the Svelte build as static files |
| `dioxus-desktop` WASM frontend | Replaced by Svelte SPA + Pretext |
| `ts-rs` TypeScript generation | Not applicable |
| `SQLX_OFFLINE` / `.sqlx/` directory | Not applicable to Go |
| `baml_src/` definitions | Replaced by Go-native LLM function contracts |
| Cargo workspace, Cargo.lock | Go modules replace Cargo |
| systemd template layer | vmctl manages VMs directly via firecracker-go-sdk |
| vfkit-runtime-ctl scripts | vmctl manages VMs directly via firecracker-go-sdk |

### 8.5 Deployment Target: OVH Node B (draft.choir-ip.com)

go-choir deploys to **OVH node B** (draft.choir-ip.com) while choiros-rs stays on node A (choir-ip.com). After both systems are validated, go-choir promotes to node A.

OVH deployment credentials and SSH details are stored in the choiros-rs cogent private notes database at `/Users/wiz/choiros-rs/.cogent/cogent-private.db`. Access via: `cogent work private-note` CLI in the choiros-rs repo, or directly query the `private_notes` table. Deploy mission workers will need to extract: SSH endpoints, deploy procedures, node B NixOS configuration, Caddy setup.

#### Deployment Architecture on Node B

```
draft.choir-ip.com (OVH Node B, NixOS)
├── Caddy (TLS, static assets, reverse proxy)
├── auth service (Go binary, systemd unit)
├── proxy service (Go binary, systemd unit)
├── vmctl service (Go binary, systemd unit)
├── gateway service (Go binary, systemd unit)
├── Firecracker microVMs
│   └── sandbox instances (Go binary inside VM)
└── NixOS configuration (flake-based)
```

- GitHub Actions builds all 5 Go binaries + Svelte frontend
- Deploy pushes to node B via SSH (or NixOS deploy tooling)
- Each service runs as a systemd unit
- Caddy config deployed as part of the NixOS configuration

---

## 9. Mapping Table

| Current Component | Source | Target Service/Binary | Fate | Notes |
|---|---|---|---|---|
| **Hypervisor auth routes** | choiros-rs | **auth** | Reimplemented | WebAuthn flows, session management |
| **Hypervisor proxy/routing** | choiros-rs | **proxy** | Reimplemented | Request routing to sandbox VMs |
| **Hypervisor provider gateway** | choiros-rs | **gateway** | Reimplemented | LLM key injection, multi-provider routing |
| **Hypervisor VM management** | choiros-rs | **vmctl** | Reimplemented | Firecracker lifecycle via API socket |
| **Sandbox HTTP server** | choiros-rs | **sandbox** | Reimplemented | App APIs, desktop state |
| **Provider Gateway** | choiros-rs | **gateway** | Reimplemented | Multi-provider routing, key injection, rate limiting |
| **WebAuthn auth** | choiros-rs | **auth** | Reimplemented | Using `go-webauthn/webauthn` |
| **SandboxRegistry** | choiros-rs | **vmctl** | Reimplemented | Uses `firecracker-go-sdk` direct API socket calls |
| **Machine Classes** | choiros-rs | **vmctl** | Preserved concept | TOML config, user preference, admin override |
| **Route Pointers** | choiros-rs | **proxy** | Simplified | Dynamic VM lookup via vmctl API |
| **Session Store** | choiros-rs | **auth** | Merged into auth DB | SQLite sessions table |
| **EventStoreActor** | choiros-rs | — | Eliminated | Direct Dolt writes in sandbox |
| **EventBusActor** | choiros-rs | **sandbox** | Merged with EventBus (from cogent) | In-process pub/sub |
| **EventRelayActor** | choiros-rs | — | Eliminated | Events flow sandbox → proxy → browser |
| **ConductorActor** (input routing) | choiros-rs | **sandbox** | Split into Conductor (§3.2) | Multi-channel input gateway, routes to appagents |
| **ConductorActor** (work tracking) | choiros-rs | **sandbox** | Split into Scheduler (§3.3) | Cross-app work registry, dispatch logic |
| **WriterActor** | choiros-rs | **sandbox** | Reimplemented as E-Text AppAgent | Dolt-backed |
| **TerminalActor** | choiros-rs | **sandbox** | Reimplemented as Terminal AppAgent | Go PTY via `creack/pty` |
| **ResearcherActor** | choiros-rs | **sandbox** | Subsumed by worker agents | Workers are goroutines in sandbox |
| **DesktopActor** | choiros-rs | **sandbox** | Reimplemented | Simpler Go service with mutex |
| **MemoryActor** | choiros-rs | **sandbox** | Reimplemented | Per-user symbolic memory, Dolt |
| **AgentHarness** | choiros-rs | **sandbox** | Merged with tool-calling loop (from cogent) | Cogent's loop internals are canonical |
| **ALM Harness** | choiros-rs | — | Deferred | Future enhancement |
| **ApplicationSupervisor** | choiros-rs | **sandbox** | Replaced by goroutine supervisor (§3.11) | Plain goroutines + channels |
| **WriterDelegationAdapter** | choiros-rs | **sandbox** | Replaced by Go channels | In-process within sandbox |
| **BAML contracts** | choiros-rs | **sandbox** | Replaced by Go-native structured output | JSON schema support |
| **shared-types** | choiros-rs | — | Eliminated | Go types in relevant packages |
| **Dioxus WASM frontend** | choiros-rs | **Svelte SPA** (Caddy) | Replaced | Client-side rendered |
| **Nix build (Crane)** | choiros-rs | Nix | Replaced by Nix Go builder | Per-binary build derivations |
| **`cogent.db` schema (21 tables)** | cogent | **sandbox** | Adapted to `go-choir` | Table schemas adapted from SQLite to Dolt's MySQL-compatible dialect. All tables go into the single per-sandbox Dolt database. |
| **`cogent-private.db`** | cogent | **sandbox** | Merged into Dolt | Private data tables merged into the single per-sandbox Dolt database |
| **Work graph state machine** | cogent | **sandbox** | Copied as Scheduler internals (§3.3) | Central cross-app work registry, same state machine |
| **Attestation model** | cogent | **sandbox** | Copied to `go-choir` | Quality gate for work completion |
| **Adapter abstraction (`adapterapi`)** | cogent | — | **Eliminated** | No adapter layer; one execution model (in-process) |
| **Native adapter internals** | cogent | **sandbox** | Internals copied (LLM clients, tool loop) | Core agent loop, ToolRegistry — without adapter wrapper |
| **Claude adapter** | cogent | — | **Eliminated** | No external/subprocess-based adapters |
| **EventBus** | cogent | **sandbox** | Copied + extended | App-layer events |
| **WebSocket hub** | cogent | **proxy** + **sandbox** | Split | Proxy tunnels WS to sandbox |
| **Serve runtime** | cogent | **sandbox** + **proxy** | Split | JSON API in sandbox, routing in proxy |
| **CLI commands** | cogent | — | **Eliminated** | No CLI; agents use direct Go function calls; operator management via HTTP admin API |
| **Capability tokens** | cogent | **sandbox** | Copied to `go-choir` | Ed25519 CA, agent auth |
| **Rotation config** | cogent | **sandbox** | Copied to `go-choir` | Model/provider rotation |
| **Briefing/hydration** | cogent | **sandbox** | Copied to `go-choir` | ProjectHydrate |
| **Email notifications** | cogent | **sandbox** | Copied to `go-choir` | Digest emails via Resend |
| **Mind-graph UI** | cogent | **Svelte SPA** (Caddy) | Migrated | One app in the desktop |
| **Co-agent tools** | cogent | **sandbox** | Copied + extended | Go channels (local), HTTP via proxy (remote) |
| **Channel manager** | cogent | **sandbox** | Copied to `go-choir` | Inter-agent message channels |
| **History compression** | cogent | **sandbox** | Copied to `go-choir` | LLM-based context compression |
| **Session persistence** | cogent | **sandbox** | Copied to `go-choir` | Persisted to VM filesystem |
| **Catalog/pricing** | cogent | **sandbox** + **gateway** | Split | Discovery in sandbox, routing in gateway |
| **LLM clients** | cogent | **sandbox** (→ **gateway**) | Copied + extended | Clients in sandbox, keys in gateway |
| **cogent repository** | cogent | — | Referenced for design | Valuable internals copied to `go-choir`; cogent repo preserved as reference |

---

## 10. Open Questions

### 10.1 Architecture

1. **Inter-service communication model**: Should host services communicate via HTTP, gRPC, or Unix sockets? HTTP is simplest and sufficient for the expected traffic. gRPC adds complexity. Unix sockets avoid network overhead for co-located services. Recommendation: HTTP for simplicity, Unix sockets for latency-sensitive paths (proxy → vmctl lookups).

2. **Service discovery**: How does the proxy know which VMs are available? Current design: proxy queries vmctl's internal API. Alternative: vmctl pushes updates to proxy via WebSocket or shared state. Decision needed — polling vs. push.

3. **Auth session propagation**: Auth issues a session token, proxy validates it, sandbox trusts it. What's the token format? JWT (self-contained, proxy can validate without calling auth) vs. opaque token (proxy must call auth on every request). JWT recommended for performance.

4. **Shared state across host services**: Should there be a shared state store (e.g., Redis, etcd) across host services, or is per-service SQLite sufficient? Per-service SQLite is simpler and sufficient for the expected scale. Redis/etcd adds operational complexity.

5. **Dolt binary size impact**: The embedded Dolt driver pulls in the entire Dolt engine, potentially adding 100MB+ to the sandbox binary size. Now more critical since Dolt is the sole database inside the sandbox. Is this acceptable given that it only affects the sandbox binary (not host services)? Alternative: use Dolt as a sidecar process with MySQL protocol inside the VM.

6. **Pretext integration depth** — just for the vtext editor, or used more broadly for text measurement across all apps in the desktop? Pretext is purpose-built for high-performance text layout without DOM reflow, which could benefit other text-heavy UI components.

### 10.2 Migration

7. **Existing user data migration**: Users on the current choiros-rs system have data in `events.db` and `.qwy` files. What's the migration path? Export + import tooling needed.

8. **Parallel operation period**: Can the old (choiros-rs) and new (`go-choir`) systems run side-by-side during migration? Or is it a hard cutover?

9. **What's the Dolt commit strategy for operational tables?** — Versioned tables (vtext content, work graph) get committed on meaningful state changes. But operational tables (sessions, events, locks) don't need per-write commits. What triggers periodic commits? Options: periodic timer (every N minutes), on graceful shutdown, on significant state transitions, or some combination. Need to balance durability vs. commit noise in the Dolt log.

### 10.3 Design

10. **App isolation model**: How strongly are apps isolated from each other within a sandbox? Can the vtext appagent access the terminal app's resources? The current design says "no" but this needs enforcement details.

11. **Multi-user**: The system supports multiple users on one host with per-user VMs. How does the proxy handle multiple simultaneous users? (Likely: separate session tokens, separate VM lookups, no shared state.)

12. **Appagent model selection**: Each appagent makes LLM calls via the gateway. Should appagents use a single model or have model rotation? The vtext appagent probably needs a strong model (Claude Opus) while research workers can use cheaper models.

13. **Worker lifecycle**: Are workers persistent (long-running sessions) or ephemeral (spawn per task, die after)? The sandbox should probably support both.

14. **Dev environment parity**: How do developers run `go-choir` locally? Recommendation: a `docker-compose.yml` or `just dev` command that starts all 5 services + Caddy locally with TCP transport instead of vsock.

---

## 11. Risk Register

### 11.1 Critical Risks

| # | Risk | Likelihood | Impact | Mitigation |
|---|------|-----------|--------|------------|
| R1 | **Microservices operational complexity** — 5 Go binaries + Caddy is significantly more complex to deploy, monitor, and debug than a monolith. Distributed failures are harder to diagnose. | High | Critical | Invest in observability early (OpenTelemetry tracing across all services). Structured logging with correlation IDs. Health check endpoints on every service. NixOS deployment modules for reproducible deployment. Docker-compose for local dev. |
| R2 | **Hidden orchestration complexity** — the distributed system becomes as complex as the two separate systems combined, defeating the purpose | High | Critical | Ruthless simplification. Host services are THIN (auth, proxy, vmctl, gateway). The sandbox is where complexity lives. Resist the temptation to add orchestration layers between host services. |
| R3 | **Inter-service latency** — every user request traverses Browser → Caddy → proxy → sandbox (and back). WebSocket proxying adds latency to real-time features. | Medium | High | Measure latency early. Proxy should be stateless and fast (no DB queries on hot path — cache vmctl lookups). Co-locate all host services on the same machine. Use Unix sockets between co-located services where possible. |
| R4 | **Feature regression during rewrite** — the Go system doesn't reach feature parity with choiros-rs before the old system degrades | High | High | Phased migration (§8.3). Each phase delivers standalone value. Don't deprecate choiros-rs until Phase 5 is complete. |

### 11.2 Significant Risks

| # | Risk | Likelihood | Impact | Mitigation |
|---|------|-----------|--------|------------|
| R5 | **Session management across services** — auth issues tokens, proxy validates them, sandbox trusts them. Token revocation, expiry, and refresh across services is a known hard problem. | Medium | High | Use JWT with short expiry (15 min) + refresh tokens. Proxy caches JWT validation (no auth service call on hot path). Sandbox trusts the proxy's forwarded identity header. |
| R6 | **Dolt embedded driver instability** — the embedded Go driver is less battle-tested than Dolt server mode. Only affects the sandbox binary. | Medium | High | Extensive integration testing. Fallback plan: Dolt as sidecar process inside the VM (MySQL protocol). Keep `database/sql` interface so the switch is easy. |
| R7 | **Writer/E-Text complexity underestimated** — the choiros WriterActor is 2477 lines of complex state management. Reimplementing on Dolt may not be simpler. | Medium | Medium | Dolt handles versioning natively (no custom version tree). Start with minimal vtext appagent. The appagent delegation logic is the real complexity. |
| R8 | **Svelte + Pretext integration risk** — combining Svelte's reactive model with Pretext's DOM-free text measurement is a novel integration. | Medium | Medium | Prototype the Svelte + Pretext editor early (Phase 3). Start minimal. Have a fallback to a simpler editor. |
| R9 | **Loss of battle-tested cogent code during copy/refactor** — copying internals from cogent to `go-choir` risks losing subtle behaviors, breaking edge cases, or introducing regressions in the process | Medium | High | Copy comprehensive tests alongside the code. Keep cogent repo intact as a reference. Run cogent's test suite against the copied code in `go-choir`. Systematic comparison of behavior before and after the copy. |
| R10 | **Sandbox binary size** — Dolt engine + all Go dependencies → sandbox binary may be large. Now more critical since Dolt is THE database inside the sandbox (no SQLite fallback). Only affects the VM guest image, not the host services. | Medium | High | Monitor binary size. Host services stay small (they use SQLite). If sandbox binary exceeds targets: strip debug symbols, consider Dolt as sidecar within VM. |
| R10a | **Single database engine dependency in sandbox** — if Dolt has issues, there's no fallback database inside the sandbox. All sandbox state depends on Dolt. | Low | High | Dolt is MySQL-compatible; in emergency, could swap to a MySQL server process inside the VM. The `database/sql` interface makes the driver swappable. Extensive integration testing against Dolt. Keep host services on SQLite as a natural hedge. |

### 11.3 Low Risks (Monitor)

| # | Risk | Likelihood | Impact | Mitigation |
|---|------|-----------|--------|------------|
| R11 | **Go 1.25+ dependency** — requires recent Go version | Low | Low | NixOS pins the Go version. Not a concern for deployment. |
| R12 | **WebAuthn library differences** — `go-webauthn` may differ from `webauthn-rs` | Low | Low | Test with existing credentials. Worst case: users re-register. |
| R13 | **Dolt MySQL compatibility gaps** — edge cases in SQL syntax | Low | Low | E-text uses simple CRUD — well within Dolt's compatibility. |
| R14 | **Vsock complexity on macOS dev** — vsock is Linux-only; macOS development requires TCP | Medium | Low | Use TCP as the dev transport; vsock as the production transport. HTTP interface is transport-agnostic. |

---

*End of spec sketch. This document should be reviewed and iterated before implementation begins.*
