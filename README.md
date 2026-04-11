# go-choir

Distributed multiagent operating system -- Go rewrite of ChoirOS, unified with Cogent. Five microservices, Caddy edge proxy, Svelte SPA, running on bare-metal NixOS.

## Architecture

```
Browser --> Caddy (TLS, static assets)
             |-- /auth/*      --> auth   (8081)
             |-- /api/*       --> proxy  (8082)
             |-- /provider/*  --> gateway (8084)

vmctl (8083) -- manages Firecracker VM lifecycle (stub)
sandbox (8085) -- placeholder host service for Milestone 1 (will move into Firecracker microVMs)
```

**Five services:**

| Service   | Port | Role |
|-----------|------|------|
| auth      | 8081 | WebAuthn passkey registration/login, JWT access + refresh token sessions, SQLite persistence |
| proxy     | 8082 | Auth-gated HTTP and WebSocket proxying to the sandbox, user-context injection |
| vmctl     | 8083 | Firecracker API socket management, VM lifecycle (stub) |
| gateway   | 8084 | LLM provider key injection, rate limiting, multi-provider routing (stub) |
| sandbox   | 8085 | Placeholder shell bootstrap and WebSocket echo surface for Milestone 1 |

All services expose a `/health` endpoint.

## Status

**Mission 2 Milestone 1 complete: auth + proxy + frontend shell working end to end.**

- WebAuthn passkey registration and login on `draft.choir-ip.com`
- JWT access tokens (5 min) with rotating refresh tokens (30 days)
- Cookie-backed same-origin auth (no tokens in localStorage/sessionStorage/URL)
- Auth-gated HTTP proxy for `GET /api/shell/bootstrap`
- Auth-gated WebSocket proxy for `GET /api/ws` with bidirectional frame relay
- Trusted user-context injection; client-spoofed identity headers stripped
- Svelte guest auth UI with register/login views and retryable passkey error states
- Authenticated placeholder desktop shell with bootstrap data, live channel status, logout
- Cookie-backed shell rehydration on hard reload / new tab
- Silent access renewal via refresh rotation without re-authentication
- Clean fallback to guest auth state when renewal fails
- Live-channel teardown on logout; user-switch state isolation
- Playwright Chromium virtual-authenticator e2e test suite (58 tests)
- CI deploys to Node B via NixOS rebuild over SSH

Next: Mission 3 (sandbox runtime, e-text app, VM isolation, polish/promotion).

## Development Setup

- Go 1.25+
- Node.js 22+ and pnpm 10+
- Nix (for NixOS deployment config)

## Run Locally

```sh
# Install dependencies and generate local signing keys
bash .factory/init.sh

# Start services (each in its own terminal)
AUTH_PORT=8081 AUTH_RP_ID="localhost" AUTH_RP_ORIGINS="http://localhost:4173" \
  AUTH_ACCESS_TOKEN_TTL="5m" AUTH_REFRESH_TOKEN_TTL="720h" AUTH_COOKIE_SECURE="false" \
  go run ./cmd/auth

PROXY_PORT=8082 PROXY_SANDBOX_URL="http://127.0.0.1:8085" go run ./cmd/proxy

SANDBOX_PORT=8085 SANDBOX_ID="sandbox-dev" go run ./cmd/sandbox

# Frontend (serves on http://localhost:4173, proxies /auth/* and /api/* to backend)
cd frontend && pnpm install && pnpm dev
```

## Tests

```sh
# Go unit tests
go test ./... -count=1 -p 4

# Playwright e2e tests (requires auth, proxy, sandbox, and frontend running)
cd frontend && pnpm exec playwright test --workers=1
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
  vmctl/        VM controller entry point (stub)
  gateway/      LLM gateway entry point (stub)
  sandbox/      Sandbox entry point
internal/
  server/       Shared HTTP server, health endpoint, graceful shutdown
  auth/         WebAuthn handlers, JWT issuance, SQLite store, session management
  proxy/        Auth-gated HTTP/WS reverse proxy, user-context injection
  sandbox/      Placeholder bootstrap/echo/WS handlers
  gateway/      Gateway domain logic (stub)
  runtime/      Agent runtime (stub)
  store/        Persistence layer (stub)
  types/        Core domain types (stub)
  vmmanager/    Firecracker VM management (stub)
frontend/       Svelte SPA: auth entry UI, placeholder desktop shell
  tests/        Playwright e2e tests (passkey flows, shell, rehydration, logout)
nix/
  node-b.nix    NixOS module: systemd services, Caddy config
  hardware.nix  Hardware configuration for Node B
  disks.nix     Disk layout for Node B
docs/
  architecture.md                    Full architecture spec
  mission-1-deploy-pipeline.md       Mission 1 brief (complete)
  mission-2-build-system.md          Mission 2 brief (Milestone 1 complete)
  mission-3-remaining-system-milestones.md  Remaining milestones
```
