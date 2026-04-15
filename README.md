# go-choir

Distributed multiagent operating system -- Go rewrite of ChoirOS, unified with Cogent. Five microservices, Caddy edge proxy, Svelte SPA, running on bare-metal NixOS.

## Architecture

```
Browser --> Caddy (TLS, static assets, Svelte SPA)
             |-- /auth/*      --> auth     (8081)
             |-- /api/*       --> proxy    (8082)
             |-- /provider/*  --> gateway  (8084)

vmctl (8083) -- manages VM ownership and lifecycle, using host-process fallback locally and Firecracker on Linux/KVM hosts
sandbox (8085) -- host-process runtime fallback for local development; target runtime moves into per-user microVMs
```

**Five services:**

| Service   | Port | Role |
|-----------|------|------|
| auth      | 8081 | Email/password registration + login, JWT access + refresh token sessions, SQLite persistence |
| proxy     | 8082 | Auth-gated HTTP and WebSocket proxying to the sandbox, user-context injection |
| vmctl     | 8083 | VM ownership + lifecycle control, host-process fallback locally, Firecracker lifecycle on supported hosts |
| gateway   | 8084 | Multi-provider LLM gateway (Fireworks, Z.AI, Bedrock) with SSE streaming |
| sandbox   | 8085 | Placeholder shell bootstrap and WebSocket echo surface for Milestone 1 |

**Frontend:** Svelte SPA with email auth UI, desktop shell, and the versioned document app (`vtext`) plus choir-in-choir controls.

All services expose a `/health` endpoint.

## Status

**Mission 6 COMPLETE: Desktop UX Rewrite.** Converted the web desktop from a top-bar paradigm to a traditional OS-style desktop with floating icons, floating windows, and full responsive support.

Delivered features (10):

1. **Floating desktop icons** -- Freely-draggable app icons on the desktop surface with emoji + labels, position persistence, double-click to launch, Show Desktop button to minimize all windows.
2. **Bottom bar** -- Fixed bottom bar with prompt input, minimized window indicators, user info, logout, and live connection status.
3. **Floating windows** -- Simplified resize (bottom-right handle only), cascade positioning, active highlight, z-index management, minimize/maximize/restore.
4. **Responsive layout** -- Desktop/tablet/mobile breakpoints. Same floating desktop/window model at all sizes, with tighter default geometry on smaller screens.
5. **File browser backend** -- CRUD API endpoints (`/api/files`) with path traversal protection.
6. **File browser frontend** -- Component with breadcrumb navigation, folder creation, inline delete, file download.
7. **Browser app** -- iframe-based web browsing with URL bar, back/forward/reload navigation, graceful error handling for blocked iframes.
8. **Terminal backend** -- PTY WebSocket endpoint (`/api/terminal/ws`) with auth gating, per-session management, cleanup on disconnect, multiple concurrent sessions.
9. **Terminal frontend** -- ghostty-web WASM terminal emulator with dark theme, FitAddon responsive fit, 10000-line scrollback, WebSocket PTY connection.
10. **Cross-area integration** -- Deploy-readiness tests for new layout, all cross-area flows verified.

Deferred to Mission 7:

- **Settings backend** -- Runtime LLM provider CRUD with AES-GCM encrypted API keys, provider reload mechanism.
- **Settings frontend** -- Settings UI for managing providers (add/edit/delete, toggle active, inline validation).
  (Deferred pending the conductor agent, which will provide the orchestration context for provider management.)

Prior milestones still in place:

- Email/password auth with JWT sessions (Mission 4)
- Multi-provider LLM gateway with SSE streaming (Mission 4)
- Auth-gated HTTP and WebSocket proxy with user-context injection
- Desktop state persistence (window positions + icon positions) across reload and tabs
- Playwright e2e test suite (168+ tests passing)
- Go unit tests (all packages passing)

Next: finish the hard-cutover `vtext` + embedded Dolt local path, then return to `vmctl` and microVM deepening.

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

VMCTL_PORT=8083 VMCTL_SANDBOX_URL_BASE="http://127.0.0.1:8085" VMCTL_IDLE_TIMEOUT="30m" go run ./cmd/vmctl

PROXY_PORT=8082 PROXY_SANDBOX_URL="http://127.0.0.1:8085" PROXY_VMCTL_URL="http://127.0.0.1:8083" go run ./cmd/proxy

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
  vmctl/        VM controller entry point
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
  vmmanager/    Firecracker VM management
frontend/       Svelte SPA: email auth UI, desktop shell, versioned document UI (`vtext`), choir-in-choir controls
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
