# Mission 7: VM Runtime Deepening After Local `vtext`

**Goal:** Finish the local `vtext` + MAS loop first, then deepen `vmctl` and per-user microVM architecture using `~/choiros-rs` and `~/cogent` as references.

This doc replaces the older “Cogent as external control plane” framing. That is not the target architecture.

---

## Current Reality

Before `vmctl` becomes the main focus again, these local problems still dominate:

1. `vtext` is not yet a trustworthy product surface
   - the generated content often feels generic or placeholder-like
   - the MAS behavior is not yet legible to the user

2. Conductor is not yet the real owner of routing
   - prompt-bar input submits a conductor task
   - but the desktop still opens `vtext` directly rather than letting conductor drive the handoff

3. Trace exists but is still too raw
   - enough to expose tasks/events/messages
   - not yet good enough for comfortable debugging of delegation behavior

4. Embedded Dolt is now part of the local critical path
   - sandbox state should stabilize around the storage model we actually want before deeper VM work

---

## What Mission 7 Is Now

Mission 7 is no longer “integrate Cogent as an external harness.”

Mission 7 is:
- deepen the microVM architecture
- get `vmctl` right
- structure user VM and worker VM patterns well
- keep borrowing strong ideas from `~/choiros-rs` and `~/cogent` without inheriting their bad boundaries

Cogent matters as:
- a reference for tool loops
- a reference for coagent tools
- a reference for work graph/session patterns
- a temporary bootstrap donor

It does **not** matter as:
- a permanent external supervisor process
- a separate control plane that Choir delegates to forever

---

## Work That Must Happen Before Mission 7 Becomes Primary

### 1. Make `vtext` honest
- tighten the single-surface UX
- ensure prompt/apply behavior is clear and reliable
- improve revision navigation and document-state visibility

### 2. Make the MAS visible
- `vtext` should clearly spawn researchers for current/external info
- `super` should appear when execution work is needed
- trace should make delegation legible

### 3. Make conductor authoritative
- stop mixing real conductor tasks with deterministic desktop shortcuts
- let routing truth come from the agent path

### 4. Stabilize embedded Dolt
- use the storage model we actually want before pushing it into microVM lifecycle work

---

## Inputs For Mission 7

### `~/go-choir`
Study:
- `internal/vmctl/`
- `internal/vmmanager/`
- `internal/proxy/`
- `nix/microvm.nix`
- `nix/guest.nix`
- `nix/storedisk.nix`
- `docs/architecture.md`
- `docs/PROJECT-STATE.md`

### `~/choiros-rs`
Study:
- `hypervisor/src/`
- `sandbox/src/`
- user VM / worker VM lifecycle patterns
- routing, ownership, snapshot, resume, and networking boundaries

Important caveat:
- `choiros-rs` hibernates too aggressively after idle
- that hurts login/startup latency when there is no capacity pressure
- copy the good parts, not that policy

### `~/cogent`
Study:
- tool loop design
- coagent tools
- session persistence patterns
- work graph ideas

Important caveat:
- do not recreate Cogent as an external forever-harness

---

## Questions Mission 7 Should Answer

1. What should the user VM lifecycle be?
   - warm
   - cold boot
   - snapshot resume
   - some hybrid

2. What should the worker VM lifecycle be?
   - per-task
   - pooled
   - spawned on demand from a warm base

3. When should hibernation happen?
   - only under pressure
   - only after longer idle windows
   - probably not as an aggressive default for the primary user VM

4. What belongs in `vmctl` versus proxy/runtime?
   - ownership resolution
   - lifecycle operations
   - capacity decisions
   - routing hints

5. How should user VM vs worker VM isolation work?
   - filesystem
   - network
   - model/tool privileges
   - persistence boundaries

---

## Success Criteria

Mission 7 is ready to start in earnest when:

1. `vtext` feels coherent as the main local product surface
2. conductor, `vtext`, researchers, and `super` are visibly interacting
3. trace makes it easy to tell what actually happened in a run
4. embedded Dolt is stable enough that we are not changing the sandbox state model underneath the VM work

Then the next major question becomes:

**How should `go-choir` run fast per-user microVMs without repeating `choiros-rs`’s slow-login / over-hibernate mistakes?**
