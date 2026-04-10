# Mission 1: Deploy Pipeline — GitHub Actions → OVH Node B

## Goal

Set up the end-to-end deploy pipeline so that every push to `main` builds all go-choir artifacts and deploys them to OVH Node B (draft.choir-ip.com). This is the first mission because there is no point building code that can't be verified on real infrastructure.

## Context

- **Node A** (choir-ip.com) runs choiros-rs — do not touch it
- **Node B** (draft.choir-ip.com) is the go-choir target — replace whatever is on it
- Both nodes are bare-metal OVH servers running NixOS
- OVH credentials and SSH details are in `/Users/wiz/choiros-rs/.cogent/cogent-private.db` (query the `private_notes` table for deploy-related entries)
- The existing choiros-rs deploy uses GitHub Actions + NixOS — reference `/Users/wiz/choiros-rs/.github/workflows/` and `/Users/wiz/choiros-rs/flake.nix` for patterns
- Caddy is already deployed on OVH (verify on node B, configure for go-choir)

## Architecture Reference

See `docs/architecture.md` sections 2.1 (Production Topology), 2.3 (Five Go Binaries), and 8.5 (Deployment Target).

## Deliverables

### 1. NixOS Module Definitions
- One NixOS module per service: auth, proxy, vmctl, gateway
- Systemd unit for each service (socket-activated or simple)
- Caddy NixOS module configuration for go-choir routes
- Sandbox binary packaged for inclusion in VM images (separate from host services)
- All modules in a `nix/` directory in the go-choir repo

### 2. GitHub Actions Workflow
- Build all 5 Go binaries (linux/amd64)
- Build the Svelte frontend (placeholder index.html is fine for now)
- Run `go test ./...` 
- Deploy to Node B via SSH + NixOS rebuild
- Triggered on push to `main`

### 3. Caddy Configuration (Node B)
- TLS via Let's Encrypt for draft.choir-ip.com
- Route `/auth/*` → auth service
- Route `/api/*` → proxy service
- Route `/provider/*` → gateway service
- Serve Svelte static assets at root `/`

### 4. Smoke Test Services
Each of the 4 host services should have a minimal `/health` endpoint that returns 200. The deploy pipeline should verify all 4 health endpoints respond after deploy.

## What's NOT in scope
- The sandbox binary running inside VMs (that's mission 2)
- Firecracker VM setup (mission 2)
- Actual WebAuthn auth (mission 2)
- Any real business logic — just the pipeline and health endpoints

## Acceptance Criteria
1. Push to `main` triggers GitHub Actions
2. Actions build all 5 Go binaries successfully
3. Actions deploy to Node B
4. `curl https://draft.choir-ip.com/auth/health` → 200
5. `curl https://draft.choir-ip.com/api/health` → 200  
6. `curl https://draft.choir-ip.com/provider/health` → 200
7. vmctl health endpoint reachable on internal port
8. `curl https://draft.choir-ip.com/` → serves placeholder HTML

## Key Files to Reference
- `/Users/wiz/choiros-rs/.github/workflows/` — existing deploy patterns
- `/Users/wiz/choiros-rs/flake.nix` — NixOS packaging patterns
- `/Users/wiz/choiros-rs/infra/` — infrastructure configs
- `/Users/wiz/choiros-rs/.cogent/cogent-private.db` — SSH/deploy credentials (private_notes table)
- `/Users/wiz/choiros-rs/nix/` — NixOS module patterns

## Estimated Complexity
Medium. Mostly infrastructure plumbing — NixOS modules, GitHub Actions YAML, Caddy config. The Go code is trivial (health endpoints only). The hard part is getting the deploy pipeline working end-to-end with real SSH to OVH.
