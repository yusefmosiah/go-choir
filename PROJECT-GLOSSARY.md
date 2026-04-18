# Project Glossary

**Last Updated:** 2026-04-16  
**Purpose:** Canonical terminology for `go-choir`, including prior names and nearby synonyms that still appear in code, docs, or conversation.

---

## Canonical Terms

### `conductor`

**Definition:** The intake/router agent for top-level user or connector input.

**What it does:**
- receives prompt-bar input first
- later receives email/chat/connector input first
- decides what appagent or flow should own the work

**Prior / nearby names:**
- intake router
- top-level router
- conductor task

---

### `vtext`

**Definition:** The primary version-native document app and appagent.

**What it does:**
- owns canonical document state
- turns prompts, edits, and worker messages into new versions
- is the main cumulative work surface for the user

**Prior / nearby names:**
- `etext`
- writer
- writer appagent
- versioned text editor

**Canonical rule:** use `vtext`, not `etext` or writer, for the product concept.

---

### `vtext agent`

**Definition:** The agent that owns the canonical `vtext` document state and writes new versions.

**What it does:**
- writes canonical versions
- spawns workers when needed
- reads worker messages
- synthesizes new document versions

**Prior / nearby names:**
- writer
- writer agent
- appagent for etext

---

### `appagent`

**Definition:** A user-facing agent responsible for an app-level domain and its canonical state.

**Examples:**
- `vtext` appagent
- (future) email, calendar, ebook, pdf reader, youtube or other video player, audio or podcast player, any app
- upgrade trace app to appagent (agentic tracing, reviewing and analyzing and visualizing past trajectories)

**Topology rule:** many appagents may coexist as peers in one user microVM, each with its own durable perspective.

**Prior / nearby names:**
- host agent
- top-level app worker

---

### `super`

**Definition:** The execution-oriented agent with the broadest tool surface in a microVM.

**What it does:**
- handles execution-heavy or tool-heavy work
- can delegate further with coagent tools
- can coordinate with researchers and appagents
- (most agents lack bash tools; least privilege principle)

**Topology rule:**
- there should generally be one singleton `super` per user microVM
- `super` is the privileged orchestration root for execution-heavy concurrency in that VM
- future co-supers or execution descendants should remain under `super` by default rather than becoming free peers immediately

**Prior / nearby names:**
- supervisor
- terminal agent
- terminalagent
- execution coordinator

**Idiomatic:** use `super`, not supervisor, when referring to the intended agent role.

---

### `researcher`

**Definition:** A research-oriented worker agent.

**What it does:**
- gathers current/external information
- reads local context
- persists evidentiary material into embedded Dolt
- sends findings back over channels
- does not own canonical document text

**Topology rule:** researchers should usually come from a shared pool within a user microVM.

**Prior / nearby names:**
- research worker
- research agent

---

### `worker`

**Definition:** A non-canonical agent that performs delegated sub-work for an appagent or `super`.

**What it does:**
- reads context
- performs assigned work
- sends back messages/results/findings

**Important rule:** workers do not directly author canonical `vtext` document text.

**Examples:**
- `researcher`
- `super` when acting as a delegated execution worker
- we will make other types of workers with specific tools, roles and capabilities

---

### `work`

**Definition:** A unit of agentic effort or causal activity inside Choir.

**What it does:**
- gives us a way to talk about what agents are advancing
- may happen sequentially or concurrently with other work
- should preserve causal relationships without forcing a rigid workflow graph

**Important rule:** prefer modeling generic work plus messages, timestamps, actors, and causes over inventing overly specific workflow tables too early.

**Prior / nearby names:**
- task
- subtask
- job
- delegation

---

### `trajectory`

**Definition:** The full causal path started by one user request and continued through conductor routing, appagent ownership, worker delegation, and later revisions.

**What it includes:**
- prompt-bar input
- `conductor`
- the owning appagent, usually `vtext`
- delegated workers and their messages
- later user revisions and agent-authored versions for that same document/work surface

**Important rule:** this is the primary thing Trace should show as one coherent unit.

**Prior / nearby names:**
- workflow
- session thread
- end-to-end request

---

### `loop`

**Definition:** One individual LLM/tool execution record inside a larger trajectory.

**Important rule:** use `loop` for a single execution record; do not use `run` as the primary product term for this concept.

**Prior / nearby names:**
- run
- task record
- execution loop

---

### `task`

**Definition:** Legacy compatibility wording for a runtime handle/record, not a preferred product concept.

**Important rule:** in user-facing behavior and MAS semantics, prefer `trajectory`, `loop`, `work`, `delegation`, `agent`, or `version`. Use `task` only when discussing old code, compatibility layers, or trivia.

**Prior / nearby names:**
- runtime task
- task record
- execution handle

---

### `version`

**Definition:** A canonical document state in `vtext`.

**Examples:**
- `v0` = initial user-prompt-created document
- `v1` = first appagent-produced best-effort completion from the `vtext` agent's current perspective

**Important rule:** versions are the main state transitions, not chat turns.

---

### `user-authored version`

**Definition:** A version created from a batch of user edits when the user hits Revise.

**Prior / nearby names:**
- edit batch
- user edit snapshot

---

### `agent-authored version`

**Definition:** A version authored by the `vtext` agent after synthesis.

**Prior / nearby names:**
- writer revision
- appagent revision

**Important rule:** the first agent-authored version should usually arrive promptly, even before worker evidence comes back. Later evidence can produce further agent-authored versions.

---

### `best-effort completion`

**Definition:** The default mode where the `vtext` agent tries to complete the document objectively from currently available context, without waiting for all delegated work to finish first.

**Important rule:** this is not the same as conversational self-reporting. The agent should generally complete the work as well as it can, then revise when more evidence arrives.

---

### `Revise` button

**Definition:** The explicit control inside `vtext` that finalizes the user’s current edit batch into a new user-authored version and re-engages the `vtext` agent.

**Important rule:** multiple user edits before Revise are one version.

**Prior / nearby names:**
- Prompt button
- Prompt / Version button

---

### `prompt bar`

**Definition:** The bottom-bar input for top-level user requests.

**Important rule:** it should always route through `conductor`.

**Prior / nearby names:**
- bottom prompt bar
- conductor input

---

### `coagent tools`

**Definition:** The tools agents use to spawn peer/child agents and exchange messages over shared channels.

**Examples:**
- `spawn_agent`
- `post_message`
- `read_messages`
- `wait_for_message`
- `close_agent`

**Prior / nearby names:**
- co-agent tools
- channel tools

**Important rule:** role/tool matching should be enforced in code. If a role should not have shell, writable filesystem, or privileged delegation access, those tools should not be present.

---

### `shared work channel`

**Definition:** The message channel used by related agents to exchange updates, findings, and coordination messages.

**Prior / nearby names:**
- channel
- work channel
- shared channel

---

### `dumb data, smart models`

**Definition:** A core modeling principle for Choir.

**What it means:**
- keep stored data structures generic and legible
- store facts, versions, timestamps, actors, messages, and causal relationships
- avoid baking brittle algorithms or overfit workflow logic into the schema
- let models process the data intelligently, with the policy expressible in prompts

**Important implication:**
- we should not feel pressure to encode concepts like `work_edges` just because relationships exist conceptually
- we should still preserve enough information to reconstruct sequential and concurrent causality between pieces of work

**Prior / nearby names:**
- dumb data smart models
- generic data, prompted policy

---

### `Trace`

**Definition:** The app/surface used to inspect trajectories, loops, delegations, tool calls, and message flow in the MAS.

**Important rule:** Trace should center the trajectory as the primary unit and show individual loops as children inside it.

**What it is not:**
- not the same thing as old Rust Trace
- not necessarily a dense graph UI

**Goal:** visual enough to explain what happened quickly, without forcing the user to read every message.

**Design direction:**
- use geometry
- use topology
- use temporality
- use color
- support filtering, querying, and agentic inspection

**Future direction:**
- Trace should likely become an appagent after we find the right visualization model.

---

### `prompt management`

**Definition:** The per-user system for inspecting and editing prompts inside Choir.

**What it does:**
- exposes editable prompts for conductor, `vtext`, and worker roles
- persists them as per-user sandbox state
- eventually becomes a first-class app in the desktop

**Important rule:** prompt configuration is per-user and belongs inside the sandbox, not as a host-global setting.

**Prompting style rule:** prompts should be subtle. Prefer a few strong positive instructions over long negative rule lists.

---

### `MAS`

**Definition:** Multiagent system.

**In this repo:** the interacting set of `conductor`, appagents like `vtext`, and workers such as `researcher` and `super`.

---

### `sandbox`

**Definition:** The runtime service/process that currently hosts the local agent runtime and desktop app APIs.

**Current reality:**
- host-process fallback locally
- target runtime later lives inside per-user microVMs

---

### `vmctl`

**Definition:** The host-side VM lifecycle and ownership service.

**What it should own:**
- user VM lifecycle
- VM ownership/routing support
- later Firecracker orchestration on supported hosts

---

### `user VM`

**Definition:** The per-user microVM that holds the user’s runtime, state, and appagents.

**Prior / nearby names:**
- per-user microVM
- primary sandbox VM

---

### `worker VM`

**Definition:** A microVM used for delegated/background worker execution when the architecture requires separate worker isolation.

**Prior / nearby names:**
- child VM
- worker microVM

---

### embedded Dolt

**Definition:** The per-user in-process Dolt database used inside the sandbox/user runtime.

**Important distinction:**
- embedded Dolt = per-user runtime storage
- platform/server Dolt = possible later shared/published storage

**Important rule:** even when a `vtext` lives canonically in Dolt, it should also have a filesystem manifestation or shortcut so it appears naturally in the file browser and opens into a new `vtext` window.

---

### transclusion

**Definition:** Inline embedding of referenced content or artifacts into the main `vtext` document flow.

**Examples:**
- quoted text snippets
- citations
- images
- video
- audio
- interactive elements

---

### citation

**Definition:** A transclusion reference rendered inline, often as a superscript, which can expand into embedded referenced content.

**Important rule:** citations are not sidebar-native in the target UX.

---

## Terms To Avoid As Primary Names

Use these only when talking about history or compatibility:

- `etext`
- writer
- run
- task
- supervisor
- terminal agent
- Factory Droid / factory workflows as an architectural reference

Preferred replacements:

- `vtext`
- `vtext agent`
- trajectory
- loop
- `super`
- `conductor`

---

## Canonical Short Summary

If we need the shortest consistent language:

- top-level input goes to `conductor`
- one prompt-bar request starts one `trajectory`
- `conductor` usually spawns `vtext`
- the user prompt becomes `v0`
- the `vtext` agent writes `v1`
- `vtext` spawns workers like `researcher` or `super` as needed
- workers send messages back over coagent tools
- those worker and appagent executions are `loops` inside the trajectory
- the `vtext` agent writes new canonical versions
- users can always edit and hit Revise to create a new user-authored version
