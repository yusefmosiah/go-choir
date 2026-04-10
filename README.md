# go-choir

Distributed multiagent operating system -- Go rewrite of ChoirOS, unified with Cogent. Five microservices, Caddy edge proxy, Svelte SPA, running on bare-metal NixOS.

## Architecture

```
Browser --> Caddy (TLS, static assets)
             |-- /auth/*      --> auth   (8081)
             |-- /api/*       --> proxy  (8082)
             |-- /provider/*  --> gateway (8084)

vmctl (8083) -- manages Firecracker VM lifecycle
sandbox (8085) -- runs inside each microVM (not deployed to host)
```

**Five services:**

| Service   | Port | Role |
|-----------|------|------|
| auth      | 8081 | WebAuthn registration, login, session management |
| proxy     | 8082 | Routes requests to the correct sandbox VM |
| vmctl     | 8083 | Firecracker API socket management, VM lifecycle |
| gateway   | 8084 | LLM provider key injection, rate limiting, multi-provider routing |
| sandbox   | 8085 | Agent runtime, conductor, scheduler, persistence (runs inside Firecracker microVMs) |

All services expose a `/health` endpoint.

## Status

**Mission 1 complete: deploy pipeline working.**

- CI builds all Go binaries and the Svelte frontend on every push to `main`
- Deploys to Node B (draft.choir-ip.com) via NixOS rebuild over SSH
- Four host services (auth, proxy, vmctl, gateway) running as systemd units behind Caddy
- Health endpoints verified post-deploy

Next: Mission 2 (build system, Firecracker VM setup, real service logic).

## Development Setup

- Go 1.24+
- Node.js 22+ and pnpm 10+
- Nix (for NixOS deployment config)

## Run Locally

Each service reads its port from an environment variable, falling back to the default shown above.

```sh
go run ./cmd/auth
go run ./cmd/proxy
go run ./cmd/vmctl
go run ./cmd/gateway
go run ./cmd/sandbox
```

Frontend:

```sh
cd frontend
pnpm install
pnpm dev
```

## Tests

```sh
go test ./...
```

## Deploy

Push to `main`. GitHub Actions will:

1. Run `go vet` and `go test`
2. Build all Go binaries and the frontend
3. SSH into Node B, pull latest code, run `nixos-rebuild switch`
4. Smoke-test all health endpoints

Target: `https://draft.choir-ip.com`

## Project Structure

```
cmd/
  auth/         Auth service entry point
  proxy/        Proxy service entry point
  vmctl/        VM controller entry point
  gateway/      LLM gateway entry point
  sandbox/      Sandbox entry point (runs inside VMs)
internal/
  server/       Shared HTTP server, health endpoint, graceful shutdown
  auth/         Auth domain logic (stub)
  proxy/        Proxy domain logic (stub)
  gateway/      Gateway domain logic (stub)
  runtime/      Agent runtime (stub)
  store/        Persistence layer (stub)
  types/        Core domain types (stub)
  vmmanager/    Firecracker VM management (stub)
frontend/       Svelte SPA (placeholder)
nix/
  node-b.nix    NixOS module: systemd services, Caddy config
  hardware.nix  Hardware configuration for Node B
  disks.nix     Disk layout for Node B
docs/
  architecture.md          Full architecture spec
  mission-1-deploy-pipeline.md   Mission 1 brief (complete)
  mission-2-build-system.md      Mission 2 brief (next)
  research-dolt.md               Dolt integration research
```
