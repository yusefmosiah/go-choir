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
- sends findings back over channels
- does not own canonical document text

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

### `version`

**Definition:** A canonical document state in `vtext`.

**Examples:**
- `v0` = initial user-prompt-created document
- `v1` = first appagent-produced version

**Important rule:** versions are the main state transitions, not chat turns.

---

### `user-authored version`

**Definition:** A version created from a batch of user edits when the user hits Prompt.

**Prior / nearby names:**
- edit batch
- user edit snapshot

---

### `agent-authored version`

**Definition:** A version authored by the `vtext` agent after synthesis.

**Prior / nearby names:**
- writer revision
- appagent revision

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

---

### `shared work channel`

**Definition:** The message channel used by related agents to exchange updates, findings, and coordination messages.

**Prior / nearby names:**
- channel
- work channel
- shared channel

---

### `Trace`

**Definition:** The app/surface used to inspect runs, delegations, tool calls, and message flow in the MAS.

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
- supervisor
- terminal agent
- Factory Droid / factory workflows as an architectural reference

Preferred replacements:

- `vtext`
- `vtext agent`
- `super`
- `conductor`

---

## Canonical Short Summary

If we need the shortest consistent language:

- top-level input goes to `conductor`
- `conductor` usually spawns `vtext`
- the user prompt becomes `v0`
- the `vtext` agent writes `v1`
- `vtext` spawns workers like `researcher` or `super` as needed
- workers send messages back over coagent tools
- the `vtext` agent writes new canonical versions
- users can always edit and hit Prompt to create a new user-authored version
