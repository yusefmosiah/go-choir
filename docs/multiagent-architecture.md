# go-choir Multiagent Architecture

> Last updated: 2026-04-20. Reflects the hard cutover state: durable agents, addressed coordination channels, runtime-owned inbox delivery, loop-oriented execution, and backend-owned vtext lifecycle.

---

## System Overview

go-choir is a local-first multiagent writing environment. Users submit prompts through a desktop shell; a **conductor** agent decides what to open; for writing work, **vtext** owns the canonical document and may delegate to **researcher** or **super** workers over durable coordination channels.

All execution happens inside a **sandbox** process. A **proxy** sits between the frontend and the sandbox, forwarding authenticated requests. Agent coordination is message-passing over durable channels plus runtime-owned inbox delivery; there is no shared mutable state between loops.

---

## Agent Role Graph

```
User (desktop shell)
       | prompt
       v
  [conductor]  decides: open vtext / show toast
       |
       | materializeConductorDecision()
       | creates: document, v0, initial vtext run
       v
  [vtext] --spawn_agent("researcher")--> [researcher]  (leaf: search + evidence)
  (owns    --spawn_agent("super")------> [super]
   doc)                                    |
                                  spawn_agent("co-super")
                                           v
                                      [co-super]        (execution helper)
```

### Delegation policy — enforced in tool_profiles.go, not in prompts

| Caller     | Can spawn            | Notes                                       |
|------------|----------------------|---------------------------------------------|
| conductor  | vtext, researcher    | Routing-only; no file/code tools            |
| vtext      | researcher, super    | Owns document; workers cast findings back to the vtext agent |
| super      | researcher, co-super | Privileged execution root                   |
| co-super   | researcher           | Supervised execution helper                 |
| researcher | (none)               | Leaf node; read-only files + search         |

---

## Tool Surfaces by Profile

| Tool group        | conductor | vtext | super | co-super | researcher |
|-------------------|:---------:|:-----:|:-----:|:--------:|:----------:|
| Writable files    |           |       |   Y   |    Y     |            |
| Read-only files   |           |       |       |          |     Y      |
| Coding tools      |           |       |   Y   |    Y     |            |
| Research tools    |           |       |   Y   |    Y     |     Y      |
| Evidence tools    |           |   Y   |   Y   |    Y     |     Y      |
| CoAgent tools     |     Y     |   Y   |   Y   |    Y     |     Y      |
| Role-specific tools |         |       |       |          | submit_research_findings |

CoAgent tools (available to all profiles): `spawn_agent`, `cast_agent`, `cancel_agent`.

Role-specific tools layer semantic handoffs on top of coagent messaging. Today
the researcher gets `submit_research_findings`, which persists evidence and
dispatches one typed findings packet back to the owning agent.

---

## CoAgent Tool Reference

### spawn_agent
Spawn a child run with a specific role. Delegation policy enforced from caller profile.

```json
// input
{ "objective": "find GDP stats", "role": "researcher",
  "channel_id": "doc-abc123", "model": "optional-override" }
// output
{ "agent_id": "...", "loop_id": "...", "channel_id": "doc-abc123",
  "role": "researcher", "profile": "researcher", "state": "pending" }
```

### cast_agent
Queue addressed work for an existing agent. The runtime persists the coordination log entry and separately enqueues inbox delivery for the target agent.

```json
{
  "channel_id": "doc-abc123",
  "agent_id": "vtext:doc-abc123",
  "content": "GDP = $28T (IMF 2025)",
  "role": "result"
}
// -> { "channel_id": "doc-abc123", "agent_id": "vtext:doc-abc123", "cursor": 42, "status": "queued" }
```

### cancel_agent
Cancel the latest active loop owned by an existing agent. The durable agent
identity stays around; this only stops current work.

```json
{ "agent_id": "vtext:doc-abc123" }
// -> { "agent_id": "vtext:doc-abc123", "status": "cancelled" }
```

---

## Data Model

### Core persistence tables — internal/store/store.go (SQLite)

```
agents
  agent_id TEXT (PK)  owner_id  sandbox_id  profile  channel_id
  created_at  updated_at

runs  [agent_id -> agents]
  loop_id TEXT (PK)  agent_id  parent_loop_id  channel_id
  agent_profile  agent_role  owner_id  sandbox_id  state
  prompt  result  error  metadata JSON
  created_at  started_at  finished_at

events  [loop_id -> runs]
  event_id TEXT (PK)  loop_id  agent_id  channel_id
  owner_id  kind  payload JSON  seq INTEGER  ts

channel_messages
  id INTEGER (PK autoincrement)  channel_id
  from_loop_id  from_agent_id  to_loop_id  to_agent_id
  trajectory_id  role  content  ts

inbox_deliveries
  delivery_id TEXT (PK)  owner_id  channel_id  message_id
  from_loop_id  from_agent_id  to_loop_id  to_agent_id
  trajectory_id  role  content  created_at
  delivered_at  delivered_to_loop_id
```

### VText tables — internal/store/vtext.go (Dolt — version-native document storage)

```
vtext_documents
  doc_id TEXT (PK)  owner_id  title  head_revision_id
  created_at  updated_at

vtext_revisions  [doc_id -> vtext_documents]
  revision_id TEXT (PK)  doc_id  parent_revision_id
  content  author_type   -- "user" | "agent"
  metadata JSON  created_at

vtext_agent_mutations  [loop_id PK]
  loop_id  doc_id  state  canonical_revision_id
  created_at  completed_at

agent_evidence
  evidence_id TEXT (PK)  loop_id  doc_id
  kind  content  source  created_at
```

### Identity and channel conventions

| Concept | Value | Set by |
|---------|-------|--------|
| `agent_id` for vtext agents | `vtext:<doc_id>` | `submitVTextAgentRevisionRun()` in vtext.go |
| `agent_id` for other agents | `loop_id` (self) | `agentIDForRun()` in tool_profiles.go |
| `channel_id` for vtext families | `doc_id` | `submitVTextAgentRevisionRun()` in vtext.go |
| `channel_id` for ad-hoc runs | caller `loop_id` unless explicit | `channelIDForRun()` in tool_profiles.go |
| `parent_loop_id` | spawning run's `loop_id` | `StartChildRun()` in runtime.go |

---

## Request / Execution Lifecycle

### 1. Top-level prompt — conductor — vtext bootstrap

```
User types prompt
  -> BottomBar.svelte emits promptsubmit
  -> Desktop.svelte: submitConductorPrompt()
       POST /api/agent/loop { prompt, metadata: { agent_profile: "conductor" } }
  -> Proxy validates auth, forwards to sandbox :8081
  -> Runtime creates RunRecord (profile=conductor, channel_id=loop_id)
  -> executeWithToolLoop() with conductor tool registry
  -> Conductor LLM returns JSON decision:
       { "action": "open_app", "app": "vtext", "title": "Essay on X",
         "seed_prompt": "write about X", "initial_content": "optional draft" }
  -> handleRunCompletion() -> materializeConductorDecision():
       1. CreateDocument(doc_id, title, owner)
       2. CreateRevision(v0, author_type="user", content=initial_content)
       3. submitVTextAgentRevisionRun(doc_id, v0_id, parentRunID=conductor_loop_id)
            -> vtext RunRecord: profile=vtext, agent_id="vtext:<doc_id>",
               channel_id=doc_id, parent_loop_id=conductor_loop_id
       4. Enriches conductor result:
            { ...decision, doc_id, initial_revision_id, initial_loop_id }
  -> Frontend receives enriched result
  -> Desktop.svelte opens VTextEditor({ docId, initialRunId })
  -> VTextEditor polls (document-scoped, not global):
       GET /api/agent/status?loop_id=<initialRunId>
       GET /api/agent/loops?channel_id=<docId>
       GET /api/agent/events?channel_id=<docId>
```

### 2. VText revision run execution

```
System prompt = vtext.md (user override or embedded default)
              + "\nCurrent coordination channel: <doc_id>"
User prompt   = buildAgentRevisionRequest():
  seed_prompt + current_doc_content + diff_summary + revision metadata
  + awareness of whether this document/channel already has grounded worker history

Agent tool loop (up to N turns):
  spawn_agent({ role: "researcher", channel_id: doc_id, ... })
  submit_research_findings({ finding_id: "...", findings: [...], evidence: [...] })

Agent final answer = complete next document version (plain text)

-> handleRunCompletion() vtext side effect:
     If the document has no prior grounded worker history, require actual
     child-worker activity addressed back to the vtext agent before accepting the answer
     as canonical. Follow-up transforms may reuse prior grounded history.
     CreateRevision(content, author_type="agent",
         metadata={ source:"agent_revision", loop_id, seed_prompt, ... })
     UpdateDocument(head_revision_id = new_revision_id)
     CompleteVTextAgentMutation(loop_id)
-> Emits vtext.agent_revision.completed
```

### 3. Manual user revise

```
User edits in VTextEditor -> clicks Revise
-> POST /api/vtext/documents/{id}/agent-revision { content, intent }
-> HandleVTextAgentRevision():
     1. CreateRevision(author_type="user", content=userEdit)   -- user checkpoint
     2. submitVTextAgentRevisionRun(doc_id, new_revision_id)   -- single emitter
(same execution path as step 2)
```

### 4. Researcher delegation (inside a vtext run)

```
VText calls spawn_agent({ role:"researcher", channel_id:doc_id, objective:"find X" })
-> StartChildRun(): profile=researcher, parent_loop_id=vtext_loop_id, channel_id=doc_id

Researcher:
  web_search("X 2025")
  submit_research_findings({
    finding_id:"finding-123",
    findings:["X = 42"],
    evidence:[...],
    questions:["Do we trust source Y?"]
  })

VText:
  runtime injects the addressed delivery as the next user turn for an active loop,
  or wakes a fresh vtext loop if the agent is idle
  -> incorporate findings -> write next canonical revision
```

---

## Event Kinds

| Event kind | When emitted |
|-----------|-------------|
| `loop.started` | Run transitions to running |
| `loop.completed` | Run finishes successfully |
| `loop.failed` | Run fails with error |
| `loop.cancelled` | Run is cancelled |
| `run.streaming_token` | Streaming token from LLM (SSE only) |
| `channel.message` | Coordination log entry, optionally addressed to a specific agent |
| `vtext.agent_revision.started` | VText mutation run created |
| `vtext.agent_revision.completed` | Canonical revision written |
| `vtext.agent_revision.failed` | VText run failed |

---

## HTTP API Surface

All routes registered in `internal/runtime/api.go`, forwarded by `internal/proxy/handlers.go`.

### Agent / run execution

| Method | Path | Description |
|--------|------|-------------|
| POST | `/api/agent/loop` | Submit a new run |
| GET | `/api/agent/status?loop_id=` | Poll run state + result |
| GET | `/api/agent/{id}/status` | Same, path-based |
| GET | `/api/agent/loops?channel_id=&limit=` | List runs (channel-scoped) |
| GET | `/api/agent/events?channel_id=&limit=` | List events (channel-scoped) |
| GET | `/api/agent/topology` | Active run family graph |
| POST | `/api/agent/spawn` | API-level child run spawn |
| POST | `/api/agent/cancel` | Cancel a run |
| GET | `/api/events` | SSE stream of real-time events |

### VText documents

| Method | Path | Description |
|--------|------|-------------|
| GET/POST | `/api/vtext/documents` | List / create documents |
| GET/PUT/DELETE | `/api/vtext/documents/{id}` | Get / update / delete |
| GET/POST | `/api/vtext/documents/{id}/revisions` | List / create revisions |
| POST | `/api/vtext/documents/{id}/agent-revision` | Trigger agent revision run |
| GET | `/api/vtext/documents/{id}/history` | Full revision history |
| GET | `/api/vtext/revisions/{id}` | Revision snapshot |
| GET | `/api/vtext/revisions/{id}/blame` | Blame by author type |
| GET | `/api/vtext/diff?a=&b=` | Diff two revisions |

### Prompts

| Method | Path | Description |
|--------|------|-------------|
| GET/POST | `/api/prompts` | List / create role prompts |
| GET/PUT/DELETE | `/api/prompts/{role}` | Get / update / delete role prompt |

---

## Prompt Store

Default prompts embedded via `//go:embed` from `internal/runtime/prompt_defaults/*.md`, seeded to disk on first run, overridable per user via `/api/prompts` or the Settings UI.

| File | Core instruction |
|------|--------------------|
| `conductor.md` | Route input to apps; return structured JSON decision |
| `vtext.md` | Own document; write canonical versions; delegate to researcher/super for evidence/execution |
| `researcher.md` | Gather evidence; use `submit_research_findings` to persist and hand findings back; no further delegation |
| `super.md` | Broad tool surface; execution-heavy coordination; delegate researcher/co-super |
| `co-super.md` | Supervised helper under super; carry out concrete execution subtasks |

---

## Frontend Component Map

```
Desktop.svelte
  BottomBar.svelte             prompt input -> conductor
  conductor.js                 submitConductorPrompt, waitForConductorDecision
  Window.svelte                window chrome (minimize / maximize / close)
    VTextEditor.svelte         main writing surface
      Activity panel           polls /api/agent/loops?channel_id=<doc_id>
                               polls /api/agent/events?channel_id=<doc_id>
      Version nav              floating vN navigator (v0 to vLatest)
      Revise button            POST .../agent-revision
      vtext.js                 VText CRUD + agent revision API calls
      runtime.js               submitRun, fetchRunStatus, connectEventStream
      trace.js                 getRunsByChannel, getEventsByChannel
  TraceApp.svelte              full run family inspector + event timeline
  PromptManager.svelte         role prompt editing UI (/api/prompts)
  TaskRunner.svelte            generic run submit + status widget
```

---

## Deployment Architecture

```
Browser / Electron frontend  (frontend/dist/)
         | HTTP :8080
         v
  [proxy :8080] --auth check--> [auth server]
  forwards: /api/agent/* /api/vtext/* /api/prompts/* /api/files/* /api/events
         | HTTP :8081 (authenticated)
         v
  [sandbox :8081]
    Runtime
      ToolRegistry (per agent profile)
      ChannelManager (in-memory + durable channel_messages table)
      PromptStore (disk overrides + embedded defaults)
      EventBus (SSE fanout to connected clients)
      SQLite store: agents, runs, events, channel_messages
      Dolt store:   vtext_documents, vtext_revisions,
                    vtext_agent_mutations, agent_evidence
```

---

## Key Design Decisions

1. **Runs vs. agents are separate.** A *run* is a single ephemeral execution. An *agent* is a durable identity that can span multiple runs — `vtext:doc-abc` accumulates revision runs over the lifetime of a document.

2. **`channel_id` is the coordination handle.** For vtext families, `channel_id = doc_id`. All researcher/super workers share the same channel, so message history is document-scoped and survives across revision cycles.

3. **Conductor completion materializes the document.** When conductor completes with `action: open_app`, the runtime creates the document, v0, and initial vtext child run before the frontend receives the result. The frontend opens an already-real document.

4. **Tool access is code policy, not prompt warnings.** Delegation targets and tool surfaces are enforced in Go (`roleSpec()` + `canDelegateTo()` in `tool_profiles.go`). Prompts describe desired behavior; code enforces capability boundaries.

5. **Single-emission vtext revise.** `submitVTextAgentRevisionRun()` is the one site that creates the pending mutation and emits `vtext.agent_revision.started`. `HandleVTextAgentRevision` only calls this helper and does not repeat side effects.

6. **Activity panels are channel-scoped.** `/api/agent/loops` and `/api/agent/events` both accept `?channel_id=` so VTextEditor sees only its document family regardless of unrelated global history volume.
