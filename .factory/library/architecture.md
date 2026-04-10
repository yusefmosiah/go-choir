# Architecture: Deploy Pipeline

## System Overview

go-choir is a distributed multiagent OS composed of 5 Go microservices, a Caddy reverse proxy, and a Svelte SPA. This mission (Mission 1) builds the deploy pipeline — health-endpoint-only services, frontend placeholder, NixOS modules, and CI/CD.

## Components (Mission 1 scope)

### Host Services (4 Go binaries, run on Node B as systemd units)

| Service | Binary | Port | Caddy Route | Purpose (future) |
|---------|--------|------|-------------|-------------------|
| auth | `cmd/auth` | 8081 | `/auth/*` | WebAuthn, sessions |
| proxy | `cmd/proxy` | 8082 | `/api/*` | Request routing to VMs |
| vmctl | `cmd/vmctl` | 8083 | (internal only) | VM lifecycle management |
| gateway | `cmd/gateway` | 8084 | `/provider/*` | LLM key injection |

### Sandbox (1 Go binary, built but NOT deployed as host service)

| Binary | Port | Purpose (future) |
|--------|------|-------------------|
| `cmd/sandbox` | 8085 (default) | Runs inside Firecracker microVMs |

### Frontend (Svelte SPA)

Static build artifact served by Caddy at `/`. SvelteKit with adapter-static or Vite+Svelte.

### Edge (Caddy)

Already running on Node B. Reconfigured via NixOS module for go-choir routes. Handles TLS (Let's Encrypt), static asset serving, and reverse proxying.

## Shared Code

`internal/server/` — Common HTTP server setup shared by all 5 binaries:
- `net/http` server with configurable port via environment variable
- Graceful shutdown on SIGTERM/SIGINT
- Health endpoint handler returning `{"status":"ok","service":"<name>"}`

## Deploy Architecture

```
Developer pushes to main
  → GitHub Actions CI
    → go vet, go test, go build (linux/amd64)
    → pnpm build (Svelte frontend)
    → SSH to Node B
      → git pull in /opt/go-choir
      → nix build (pre-check for OOM)
      → nixos-rebuild switch --flake .#go-choir-b
      → Smoke test: curl health endpoints
```

## Node B Infrastructure

- **Host**: OVH bare metal, NixOS 26.05, 12 cores, 32GB RAM, 2x512GB NVMe
- **Disk**: Root btrfs on md RAID, UUID `3b71f2a6-7820-47a1-ba22-c44c65e31ea1`
- **IP**: 147.135.70.196 (draft.choir-ip.com)
- **SSH**: root access via `ssh node-b`, authorized keys for human + deploy
- **Workspace**: `/opt/go-choir` (git clone of the repo)
- **Firewall**: Only ports 22, 80, 443 open externally

## NixOS Configuration Structure

```
flake.nix
  inputs: nixpkgs (unstable)
  packages: auth, proxy, vmctl, gateway, sandbox, frontend
  nixosConfigurations:
    go-choir-b → Node B full system config
      ├── hardware config (OVH bare metal)
      ├── disk config (btrfs RAID)
      ├── SSH + firewall
      ├── Caddy (TLS, routes, static assets)
      └── 4 systemd services (auth, proxy, vmctl, gateway)
```

## Key Invariants

- Service ports are configured via environment variables with defaults
- All services respond to `/health` with JSON
- Sandbox is built but not deployed on host
- vmctl is internal-only (no Caddy route)
- Firewall blocks external access to service ports (8081-8084)
- Deploy is atomic via nixos-rebuild switch
