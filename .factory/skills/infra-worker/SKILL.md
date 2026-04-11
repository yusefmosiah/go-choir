---
name: infra-worker
description: Builds deploy, NixOS, Caddy, VM-image, and Node B operational configuration for Mission 3
---

# Infra Worker

NOTE: Startup and cleanup are handled by `worker-base`. This skill defines the WORK PROCEDURE.

## When to Use This Skill

Use for features that change:
- NixOS modules or systemd units on Node B
- Caddy routing and public route preservation
- flake/frontend packaging used by deploy
- Firecracker guest image wiring and runtime configuration
- deploy/runtime configuration for auth/proxy/gateway/vmctl/sandbox
- monitoring, alerting, and load-harness setup
- secret or persistent-path wiring needed for deployed validation

## Required Skills

None

## Work Procedure

1. **Read the mission contract before editing infra**
   - Read the feature, `mission.md`, `AGENTS.md`, `validation-contract.md`, `.factory/library/architecture.md`, `.factory/library/environment.md`, `.factory/library/user-testing.md`, and `.factory/services.yaml`.
   - Preserve the public route contract and Mission 3 boundaries.
   - If `deploy-readiness` is not complete yet, treat public-origin correctness on `https://draft.choir-ip.com` as the hard gate before later milestones.

2. **Inspect the existing infra first**
   - Read `flake.nix`, `nix/*.nix`, and any relevant workflow/runtime files before changing them.
   - Keep ports on `8081-8085` unless the feature explicitly requires otherwise.
   - Treat Node B as the only deploy/acceptance host for this mission.

3. **Apply Mission 3 infra rules**
   - Caddy must use `handle`, not `handle_path`, for the public prefixes this mission depends on.
   - Do not expose internal service ports externally.
   - Keep secrets out of git and out of the Nix store.
   - If auth, gateway, or vmctl need runtime keys/paths on Node B, wire them through runtime files or systemd credentials.
   - The deploy-readiness milestone must restore the real SPA at `draft.choir-ip.com`, not just pass internal health checks.
   - VM-isolation acceptance is Linux/Node B only; do not claim macOS Firecracker proof.

4. **Verify the Node B runtime story**
   - Make sure the deployed services have the files, directories, and environment they need.
   - Keep `draft.choir-ip.com` as the deployed acceptance origin.
   - If the feature changes runtime backing for protected requests, verify the public path still lands on the intended backend.
   - Do not broaden scope to Node A promotion.

5. **Run infra validation**
   - Prefer syntax/evaluation checks that are safe on this machine.
   - Verify that service ports, routes, runtime files, and secret paths line up with the mission contract.
   - If the feature changes a browser-visible deploy/runtime path, include a public-origin smoke check rather than only local/Nix evaluation.
   - If Linux-only validation cannot be run locally, record the strongest safe local verification and the exact remaining Node B proof required.

6. **Return a precise handoff**
   - Name the exact files changed, the public routes affected, the runtime secrets/paths introduced, and how you verified they stay outside the repo/Nix store.
   - If the feature changes deploy artifacts, state how you proved the public root or runtime now serves the intended build/product surface.

## Example Handoff

```json
{
  "salientSummary": "Restored the real frontend deploy artifact on Node B and aligned Caddy/systemd wiring so `https://draft.choir-ip.com` serves the Mission 2 auth shell instead of the placeholder page. Verified the public root now serves built assets and that the protected-request backend restarts cleanly under the configured systemd units.",
  "whatWasImplemented": "Updated the flake/frontend packaging and Node B runtime wiring so the deployed root serves the built Svelte frontend bundle instead of a handwritten placeholder artifact. Preserved prefix-stable `/auth/*` and `/api/*` routing, kept internal service ports private, and kept runtime secrets and persistence paths in writable runtime locations outside git and the Nix store.",
  "whatWasLeftUndone": "Full public-origin browser validation remains for milestone validation after implementation features complete.",
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
      },
      {
        "command": "cd frontend && pnpm build",
        "exitCode": 0,
        "observation": "The frontend build artifact required by the deploy path is generated successfully."
      }
    ],
    "interactiveChecks": [
      {
        "action": "Reviewed the public root artifact path and Caddy route definitions after the change",
        "observed": "The public root serves the built frontend artifact and public prefixes remain preserved with `handle`."
      },
      {
        "action": "Reviewed runtime secret/persistence paths",
        "observed": "Auth DB, signing material, and future runtime secret paths remain in writable runtime locations, not in git or the Nix store."
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

- The feature requires a mission-boundary change (ports, hosts, public exposure, or Node A work)
- Runtime secrets, provider credentials, or deploy credentials are missing and cannot be created safely from within the repo
- The only way forward would place secrets in git, the Nix store, or guest-visible VM state
- Linux-only VM/runtime behavior cannot be safely verified from this environment and the remaining proof requires Node B access or user intervention
