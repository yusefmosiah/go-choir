---
name: infra-worker
description: Builds NixOS/Caddy/deploy/runtime configuration for Mission 2 Milestone 1
---

# Infra Worker

NOTE: Startup and cleanup are handled by `worker-base`. This skill defines the WORK PROCEDURE.

## When to Use This Skill

Use for features that change:
- NixOS modules or systemd units on Node B
- Caddy routing and public route preservation
- deploy/runtime configuration for auth/proxy/sandbox
- secret or persistent-path wiring needed for deployed validation

## Required Skills

None

## Work Procedure

1. **Read the mission contract before editing infra**
   - Read the feature, `validation-contract.md`, `.factory/library/architecture.md`, `.factory/library/environment.md`, and `.factory/services.yaml`.
   - Preserve the public route contract and Mission 2 boundaries.

2. **Inspect the existing infra first**
   - Read `flake.nix`, `nix/*.nix`, and any relevant workflow/runtime files before changing them.
   - Keep ports on `8081-8085` unless the feature explicitly requires otherwise.

3. **Apply Mission 2 Milestone 1 infra rules**
   - Caddy must use `handle`, not `handle_path`, for the public prefixes this mission depends on.
   - Do not expose internal service ports externally.
   - Keep secrets out of git and out of the Nix store.
   - If auth needs runtime keys or DB paths on Node B, wire them through runtime files or systemd credentials.
   - For Milestone 1, it is acceptable for the placeholder sandbox to run as a host service on `8085`; do not introduce VM routing yet.

4. **Verify the Node B runtime story**
   - Make sure the deployed auth/proxy/sandbox services have the files, directories, and environment they need.
   - Keep `draft.choir-ip.com` as the deployed acceptance origin.
   - Do not broaden scope into provider/gateway or VM-isolation features.

5. **Run infra validation**
   - Prefer syntax/evaluation checks that are safe on this machine.
   - Verify that service ports, routes, and runtime files line up with the mission contract.
   - If you cannot fully run a Nix check locally, record the strongest safe verification you performed.

6. **Return a precise handoff**
   - Name the exact files changed, the public routes affected, the runtime secrets/paths introduced, and how you verified they stay outside the repo/Nix store.

## Example Handoff

```json
{
  "salientSummary": "Updated Node B routing for Mission 2 Milestone 1 by switching Caddy from `handle_path` to `handle`, wiring the placeholder sandbox as a host service on 8085, and adding runtime paths for auth state outside the Nix store. Verified route definitions and service wiring still align with the deploy contract.",
  "whatWasImplemented": "Updated `nix/node-b.nix` so public `/auth/*`, `/api/*`, and `/provider/*` routes keep their prefixes, added the placeholder sandbox systemd service on port 8085 for the Milestone 1 proxy target, and introduced runtime file/directory wiring for auth persistence and signing material under writable Node B paths instead of embedding secrets in the Nix store.",
  "whatWasLeftUndone": "Live deployed passkey/browser validation remains for milestone validation after implementation features complete.",
  "verification": {
    "commandsRun": [
      {
        "command": "nix flake show",
        "exitCode": 0,
        "observation": "The flake still evaluates and exposes the expected packages/configuration."
      },
      {
        "command": "go build ./cmd/...",
        "exitCode": 0,
        "observation": "Service binaries referenced by the NixOS modules still compile."
      }
    ],
    "interactiveChecks": [
      {
        "action": "Reviewed Node B Caddy route definitions after the change",
        "observed": "Public prefixes are preserved with `handle`; no service ports are exposed directly."
      },
      {
        "action": "Reviewed runtime secret/persistence paths",
        "observed": "Auth DB and signing material are expected in writable runtime locations, not in git or the Nix store."
      }
    ]
  },
  "tests": {
    "added": []
  },
  "discoveredIssues": []
}
```

## When to Return to Orchestrator

- The feature requires a mission-boundary change (ports, hosts, or exposing internal services publicly)
- Runtime secrets or deploy credentials are missing and cannot be created safely from within the repo
- The only way forward would place secrets in git or the Nix store
- Node B runtime behavior cannot be verified without user intervention or unavailable external access
