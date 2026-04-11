# Mission 3 Reference Migrations

What belongs here: high-level migration guidance from local reference repositories used for Mission 3 planning.  
What does not belong here: raw secrets, copied code, or implementation details that belong in the feature itself.

## Cogent runtime references

Most relevant source areas in `/Users/wiz/cogent`:

- `internal/adapters/native/session.go`
- `internal/adapters/native/loop.go`
- `internal/adapters/native/tools*.go`
- `internal/adapters/native/client_anthropic.go`
- `internal/adapters/native/client_openai.go`
- `internal/core/types.go`
- `internal/store/store.go`
- `internal/service/events.go`
- `internal/events/translate.go`

Use these as behavior/reference inputs while adapting to go-choir’s constraints:

- no CLI subprocess loop in sandbox
- no adapter-wrapper process around the runtime loop
- goroutine supervision is first-class
- state moves toward per-user Dolt-backed persistence

## Choiros-rs gateway / VM references

Most relevant source areas in `/Users/wiz/choiros-rs`:

- `hypervisor/src/provider_gateway.rs`
- `hypervisor/src/runtime_registry.rs`
- `hypervisor/src/sandbox/mod.rs`
- `hypervisor/src/sandbox/systemd.rs`
- `hypervisor/src/config.rs`
- `hypervisor/src/jobs.rs`
- `hypervisor/src/api/mod.rs`

Use these as behavior/reference inputs while adapting to go-choir’s constraints:

- gateway injects host-side provider credentials
- per-user VM ownership is explicit
- proxy routing consults ownership state rather than static upstream config
- Node B is the only acceptance host in Mission 3

## Decomposition guidance

- Split runtime loop behavior from storage implementation
- Freeze canonical types and event vocabulary before deep store migrations
- Keep gateway identity and credential boundaries narrow and explicit
- Keep vmctl ownership/routing concerns separate from frontend/browser concerns
- Treat operator/admin/load/monitoring surfaces as first-class user-testing surfaces, not just implementation details
