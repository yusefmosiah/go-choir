# go-choir

Distributed multiagent operating system -- Go rewrite of ChoirOS, unified with Cogent. Five microservices, Caddy edge proxy, Svelte SPA, running on bare-metal NixOS.

## Architecture

```
Browser --> Caddy (TLS, static assets, Svelte SPA)
             |-- /auth/*      --> auth     (8081)
             |-- /api/*       --> proxy    (8082)
             |-- /provider/*  --> gateway  (8084)

vmctl (8083) -- manages Firecracker VM lifecycle (stub)
sandbox (8085) -- placeholder host service for Milestone 1 (will move into Firecracker microVMs)
```

**Five services:**

| Service   | Port | Role |
|-----------|------|------|
| auth      | 8081 | Email/password registration + login, JWT access + refresh token sessions, SQLite persistence |
| proxy     | 8082 | Auth-gated HTTP and WebSocket proxying to the sandbox, user-context injection |
| vmctl     | 8083 | Firecracker API socket management, VM lifecycle (stub) |
| gateway   | 8084 | Multi-provider LLM gateway (Fireworks, Z.AI, Bedrock) with SSE streaming |
| sandbox   | 8085 | Placeholder shell bootstrap and WebSocket echo surface for Milestone 1 |

**Frontend:** Svelte SPA with email auth UI, desktop shell, e-text editor, and choir-in-choir controls.

All services expose a `/health` endpoint.

## Status

**Mission 4 complete: auth fix, email auth, multi-provider LLM gateway, SSE streaming, choir-in-choir.**

Mission 4 delivered five major features:

1. **Auth re-login bug fix** -- CredentialFlags now persist across re-logins, eliminating the stale-credential crash loop.
2. **Email-based authentication** -- Migrated from WebAuthn passkeys to email/password with bcrypt hashing and password reset flow.
3. **Multi-provider LLM gateway** -- Unified `/provider/*` endpoint routes requests to Fireworks, Z.AI, and Amazon Bedrock with automatic provider selection.
4. **SSE streaming** -- Gateway streams LLM completions to the frontend via Server-Sent Events for real-time token delivery.
5. **Choir-in-choir (minimal)** -- Scheduler service, worker spawn API, e-text research button, and support for multiple concurrent workers with auto-notification and work item sync.

Prior milestones still in place:

- JWT access tokens (5 min) with rotating refresh tokens (30 days)
- Cookie-backed same-origin auth (no tokens in localStorage/sessionStorage/URL)
- Auth-gated HTTP and WebSocket proxy with user-context injection
- Svelte SPA with auth UI, desktop shell, and e-text editor
- Playwright e2e test suite
- CI deploys to Node B via NixOS rebuild over SSH

Next: Mission 5 (VM isolation, production hardening, full choir orchestration).

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

GATEWAY_PORT=8084 \
  FIREWORKS_API_KEY="your-key" ZAI_API_KEY="your-key" BEDROCK_ACCESS_KEY="your-key" BEDROCK_SECRET_KEY="your-key" BEDROCK_REGION="us-east-1" \
  go run ./cmd/gateway

SANDBOX_PORT=8085 SANDBOX_ID="sandbox-dev" go run ./cmd/sandbox

# Frontend (serves on http://localhost:4173, proxies /auth/*, /api/*, /provider/* to backend)
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
  gateway/      LLM gateway entry point (Fireworks, Z.AI, Bedrock, SSE streaming)
  sandbox/      Sandbox entry point
internal/
  server/       Shared HTTP server, health endpoint, graceful shutdown
  auth/         Email/password handlers, JWT issuance, SQLite store, session management
  proxy/        Auth-gated HTTP/WS reverse proxy, user-context injection
  sandbox/      Placeholder bootstrap/echo/WS handlers
  gateway/      Multi-provider LLM routing, SSE streaming, provider key management
  runtime/      Agent runtime (stub)
  store/        Persistence layer (stub)
  types/        Core domain types (stub)
  vmmanager/    Firecracker VM management (stub)
frontend/       Svelte SPA: email auth UI, desktop shell, e-text editor, choir-in-choir controls
  tests/        Playwright e2e tests (auth flows, shell, rehydration, logout, re-login)
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
# CI trigger: Sat Apr 11 21:33:07 EDT 2026
