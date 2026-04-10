---
name: infra-worker
description: Builds NixOS configuration, Caddy config, GitHub Actions CI/CD, and Node B infrastructure
---

# Infra Worker

NOTE: Startup and cleanup are handled by `worker-base`. This skill defines the WORK PROCEDURE.

## When to Use This Skill

Features that involve NixOS configuration (flake.nix, modules, systemd units), Caddy configuration, GitHub Actions CI/CD workflows, or Node B infrastructure setup.

## Required Skills

None

## Work Procedure

1. **Read the feature description** carefully. Understand preconditions, expected behavior, and verification steps.

2. **Read existing infrastructure files** before writing anything:
   - Check `flake.nix` if it exists
   - Check `nix/` directory for existing modules
   - Check `.github/workflows/` for existing CI configs
   - Read `.factory/library/architecture.md` for system topology
   - Read `.factory/library/environment.md` for env vars and credentials

3. **For NixOS features**:
   - Reference the choiros-rs patterns at `/Users/wiz/choiros-rs/` for proven NixOS module patterns:
     - `flake.nix` — flake structure, buildGoModule pattern (see the `cogent` package), nixosConfigurations
     - `nix/ovh-node.nix` — base node config (Caddy, SSH, firewall, systemd services)
     - `nix/ovh-node-b.nix` — Node B overrides (hostname, domain)
     - `nix/ovh-node-b-disks.nix` — disk layout (btrfs RAID, UUIDs)
     - `nix/ovh-node-hardware.nix` — hardware config
   - Adapt these patterns for Go services (not Rust). Key differences:
     - Use `pkgs.buildGoModule` instead of crane
     - 4 separate systemd services instead of 1 monolithic hypervisor
     - Caddy routes to 3 backends (auth, proxy, gateway) instead of 1
   - Node B hardware facts: 12 cores, 32GB RAM, 2x512GB NVMe in RAID, root btrfs UUID `3b71f2a6-7820-47a1-ba22-c44c65e31ea1`
   - SSH authorized keys: preserve BOTH the human operator key AND the GitHub Actions deploy key from the choiros-rs config

4. **For Caddy configuration**:
   - Use NixOS `services.caddy` module (not a raw Caddyfile)
   - Virtual host: `draft.choir-ip.com`
   - Routes: `/auth/*` → 127.0.0.1:8081, `/api/*` → 127.0.0.1:8082, `/provider/*` → 127.0.0.1:8084
   - Root `/` serves Svelte static assets (file_server with root pointing to the frontend Nix package)
   - vmctl (:8083) is NOT exposed through Caddy
   - TLS is automatic via Let's Encrypt (Caddy default)

5. **For GitHub Actions**:
   - Reference `/Users/wiz/choiros-rs/.github/workflows/ci.yml` for the deploy pattern
   - The deploy pattern: SSH → git pull → nix build (pre-check) → nixos-rebuild switch → health check
   - Workspace on Node B: `/opt/go-choir`
   - Secrets: `OVH_DEPLOY_SSH_KEY`, `OVH_NODE_B_HOST`
   - The workflow must include: Go vet, Go test, Go build (linux/amd64), frontend build, deploy, smoke test

6. **For Node B setup**:
   - Create workspace directory, clone the repo
   - This can be done via SSH from the CI workflow or as a one-time setup step
   - Ensure the deploy user can git pull (HTTPS clone, no auth needed for public repo)

7. **Verify your work**:
   - For Nix: Run `nix flake check` or `nix flake show` locally if possible. If the flake is linux-only, verify syntax and structure.
   - For CI: Verify YAML syntax. Cross-reference with the choiros-rs workflow for correctness.
   - For modules: Ensure all systemd units have `Restart=on-failure`, correct ports, and proper `ExecStart` paths.
   - Record all verification in `commandsRun`.

8. **Commit** with a descriptive message.

## Example Handoff

```json
{
  "salientSummary": "Created flake.nix with buildGoModule for all 5 Go binaries and a frontend package. Added NixOS configuration for Node B with hardware config, disk mounts, SSH, firewall (ports 22/80/443), Caddy routing for draft.choir-ip.com, and 4 systemd services. Verified flake evaluates without errors.",
  "whatWasImplemented": "flake.nix with inputs (nixpkgs unstable), 6 packages (auth, proxy, vmctl, gateway, sandbox, frontend), and nixosConfigurations.go-choir-b. NixOS modules: nix/hardware.nix (OVH bare metal), nix/disks.nix (btrfs RAID), nix/node-b.nix (full system: SSH with 2 authorized keys, firewall ports 22/80/443, Caddy with TLS for draft.choir-ip.com routing /auth→8081 /api→8082 /provider→8084 /→frontend, 4 systemd services with Restart=on-failure and EnvironmentFile for port config).",
  "whatWasLeftUndone": "",
  "verification": {
    "commandsRun": [
      {"command": "nix flake show", "exitCode": 0, "observation": "All packages and nixosConfigurations listed correctly"},
      {"command": "yamllint .github/workflows/ci.yml", "exitCode": 0, "observation": "Valid YAML"},
      {"command": "grep -c Restart=on-failure nix/node-b.nix", "exitCode": 0, "observation": "4 occurrences, one per service"}
    ],
    "interactiveChecks": [
      {"action": "Reviewed flake.nix buildGoModule config", "observed": "Uses subPackages for each cmd/, vendorHash = null (will need updating after first build)"},
      {"action": "Reviewed Caddy NixOS config", "observed": "3 reverse_proxy routes + file_server for frontend, vmctl NOT exposed"},
      {"action": "Reviewed firewall config", "observed": "allowedTCPPorts = [22 80 443], no 8081-8084"}
    ]
  },
  "tests": {
    "added": []
  },
  "discoveredIssues": []
}
```

## When to Return to Orchestrator

- SSH access to Node B fails or credentials are incorrect
- NixOS build fails with dependency issues requiring user input
- Go binary packages fail to build with Nix (vendorHash mismatch, missing dependencies)
- GitHub secrets need to be configured (cannot be done programmatically without user authorization)
- Hardware configuration details are unclear (disk layout, network interfaces)
