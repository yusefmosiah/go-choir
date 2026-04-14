# Mission 3: Build the Remaining System — Runtime, Apps, Isolation, and Promotion

## Goal

Continue after Mission 2 completes the original Milestone 1 foundation. Mission 3 covers the remaining original build-system milestones: sandbox runtime, the first real desktop app, VM isolation, and final polish/promotion.

## Context

- Mission 2 is now scoped to the original **Milestone 1** only:
  - WebAuthn auth
  - cookie-backed session lifecycle
  - proxy plumbing
  - placeholder sandbox
  - minimal Svelte shell
- This document captures the **remaining original milestones** from `docs/mission-2-build-system.md`
- Mission 3 should begin only after Mission 2 leaves a stable deployed auth/proxy/shell slice on `draft.choir-ip.com`

## Remaining Milestones

### Milestone 1: Sandbox Core (original Mission 2 Milestone 2)

**Copy/adapt from cogent:**
- `internal/adapters/native/client_anthropic.go` → `internal/runtime/client_anthropic.go`
- `internal/adapters/native/client_openai.go` → `internal/runtime/client_openai.go`
- `internal/adapters/native/loop.go` → `internal/runtime/loop.go`
- `internal/adapters/native/tools*.go` → `internal/runtime/tools*.go`
- `internal/adapters/native/channel.go` → `internal/runtime/channel.go`
- `internal/core/` → `internal/types/`
- `internal/service/events.go` → `internal/store/events.go`

**Adapt for go-choir:**
- remove the adapter abstraction wrapper
- run the tool-calling loop directly as goroutines
- replace SQLite runtime state with Dolt embedded
- add goroutine supervision, restart strategy, and health checks
- expose sandbox HTTP API for proxy routing

**Sandbox HTTP API:**
- `/health`
- `/api/agent/task`
- `/api/agent/status`
- `/api/events`

**Verification:**
- prompt from the Svelte UI reaches proxy → sandbox → LLM client and the response returns to the UI on `draft.choir-ip.com`

### Milestone 2: VText App + Real Desktop (original Mission 2 Milestone 3)

**Dolt embedded setup:**
- initialize per-user Dolt database
- create version-native document schema for `documents`, `content`, `citations`, `metadata`, and transclusions
- implement store API for create/read/update/history/diff/blame

**App/runtime work:**
- build the `vtext` appagent
- add scheduler tables and work dispatch
- add conductor routing for web UI input

**Desktop work:**
- real window manager
- `vtext` document surface using Pretext
- version history viewer
- app launcher

**Verification:**
- create a document, edit it, request an agent revision, and inspect version history with user-vs-agent attribution on `draft.choir-ip.com`

### Milestone 3: Gateway + VM Isolation (original Mission 2 Milestone 4)

**Gateway service:**
- port provider-gateway behavior from choiros-rs
- support Anthropic, OpenAI, Bedrock, Z.AI, OpenRouter
- inject API keys outside the sandbox
- add per-sandbox rate limiting

**vmctl service:**
- manage Firecracker VM lifecycle with `firecracker-go-sdk`
- boot/stop/hibernate/idle watchdog
- maintain VM ownership registry
- expose internal API for proxy routing

**VM integration:**
- run sandbox inside Firecracker VMs
- route proxy traffic into VMs
- keep API keys out of the VM
- build NixOS VM images from the repo

**Verification:**
- full deployed path with VM isolation: login → proxy → user VM → gateway-backed LLM call → response back to UI

### Milestone 4: Polish + Promotion (original Mission 2 Milestone 5)

- terminal app
- files app
- mind-graph app
- admin API across host services
- monitoring and alerting
- load testing
- promotion from Node B (`draft.choir-ip.com`) to Node A (`choir-ip.com`)

**Verification:**
- production-promotion checklist passes and the system works on the promoted host

## Key Design Decisions That Still Carry Forward

- no CLI subprocess loop inside sandbox; tools are Go function calls
- no external adapter wrapper around the main runtime loop
- one Dolt per sandbox
- conductor and scheduler stay separate
- users and appagents are peer canonical editors
- workers remain subordinate to appagents

## Suggested Mission 3 Planning Split

Mission 3 is still too large for one implementation push. The likely next planning split is:

1. sandbox runtime
2. `vtext` + real desktop
3. gateway + VM isolation
4. polish/promotion

That keeps validation meaningful at each boundary while preserving the original system direction.
