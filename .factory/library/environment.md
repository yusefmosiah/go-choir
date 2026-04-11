# Environment

Environment variables, external dependencies, and setup notes.

**What belongs here:** Required env vars, external API keys/services, dependency quirks, platform-specific notes.
**What does NOT belong here:** Service ports/commands (use `.factory/services.yaml`).

---

## Environment Variables

### Service ports
- `AUTH_PORT` — auth service listen port (default: `8081`)
- `PROXY_PORT` — proxy service listen port (default: `8082`)
- `VMCTL_PORT` — vmctl service listen port (default: `8083`)
- `GATEWAY_PORT` — gateway service listen port (default: `8084`)
- `SANDBOX_PORT` — host-process sandbox/runtime listen port during local/dev-host milestones (default: `8085`)

### Auth runtime
- `AUTH_DB_PATH` — SQLite database path for users, credentials, challenge/session state, and refresh/session records
- `AUTH_RP_ID` — WebAuthn RP ID
- `AUTH_RP_ORIGINS` — comma-separated allowed WebAuthn origins
- `AUTH_JWT_PRIVATE_KEY_PATH` — Ed25519 private key used to sign access JWTs
- `AUTH_ACCESS_TOKEN_TTL` — short-lived access token TTL
- `AUTH_REFRESH_TOKEN_TTL` — refresh-state TTL
- `AUTH_COOKIE_SECURE` — `true` on deployed HTTPS origin, `false` only for localhost development

### Proxy runtime
- `PROXY_AUTH_PUBLIC_KEY_PATH` — Ed25519 public key used to verify auth-issued access JWTs
- `PROXY_SANDBOX_URL` — current upstream sandbox base URL used before vmctl-backed routing fully replaces static routing
- local proxy startup may omit `PROXY_AUTH_PUBLIC_KEY_PATH` because `internal/proxy/config.go` defaults it to `/tmp/go-choir-m2/auth-signing-key.pub`, which `.factory/init.sh` generates

### Sandbox/runtime
- `SANDBOX_ID` — stable sandbox identity for local or host-process runtime validation
- expect new runtime/store variables to be introduced for Dolt workspace paths, task/event persistence, and supervisor config as Mission 3 lands

### Gateway/provider runtime
- `AWS_BEARER_TOKEN_BEDROCK` — Bearer auth token for Bedrock invoke endpoint (not SigV4; matches choiros-rs pattern)
- `AWS_REGION` — AWS region for Bedrock (e.g., `us-east-1`)
- `RUNTIME_BEDROCK_MODEL` — Bedrock model ID override (defaults to `us.anthropic.claude-sonnet-4-5-20250514-v1:0`)
- `ZAI_API_KEY` — API key for Z.AI's Anthropic-compatible endpoint at `https://api.z.ai/api/anthropic`
- `RUNTIME_ZAI_MODEL` — Z.AI model ID override (defaults to `glm-4.7`)
- Provider resolution: Bedrock is preferred if `AWS_BEARER_TOKEN_BEDROCK` is set; Z.AI is used if only `ZAI_API_KEY` is set; otherwise the stub provider is used
- provider secrets must be injected from host runtime configuration, not committed to the repo and not baked into guest images
- Error responses from providers are sanitized: raw response bodies are never included in error messages to avoid leaking credentials or provider details

### VM runtime
- expect Firecracker/vmctl env vars for image paths, kernel paths, ownership registry storage, and lifecycle settings once the VM milestone lands

### Route invariants
These browser-facing routes remain the stable contract:
- `GET /auth/session`
- `GET /api/shell/bootstrap`
- `GET /api/ws`

Mission 3 adds proxy-routed runtime routes:
- `POST /api/agent/task`
- `GET /api/agent/status`
- `GET /api/events`

## External Dependencies

- **Node B (OVH)**: NixOS bare metal at `147.135.70.196` (`draft.choir-ip.com`)
- **GitHub Actions**: CI/CD pipeline and deploy path
- **Let's Encrypt / Caddy**: TLS at the public edge
- **Real provider credentials**:
  - Bedrock and/or Z.AI are the first required real-provider paths
  - the user noted local reference configuration exists outside this repo (for example `~/choiros_rs/.env`); workers may inspect naming patterns carefully if needed, but must never print or commit secret values

## Secrets and persistence

- Keep signing keys and any cookie/session secrets out of git and out of the Nix store
- Keep provider credentials out of git, out of the Nix store, and out of guest VM files/env/process args
- Node B runtime secrets should live in writable runtime locations (for example `/var/lib/go-choir/...`) or systemd-managed credentials
- **Note on Ed25519 signing keys generated at runtime**: keys generated via `ssh-keygen` are OpenSSH formatted, so auth parsing must keep handling that format correctly
- Mission 2 auth persistence remains SQLite-backed until explicitly replaced; the DB must live in a writable persistent path, not inside the repo checkout or Nix store
- Mission 3 moves user/app/runtime state toward per-user Dolt-backed workspace directories; workers should keep these outside git and outside the Nix store
- Local worker setup may use temporary files under `/tmp/go-choir-m2` and `/tmp/go-choir-m3`

## Platform Notes

- Local planning/development here is `darwin/arm64`; deployed runtime acceptance is `linux/amd64` on Node B
- Real Firecracker/KVM validation is Linux-only and must happen on Node B, not on macOS
- Nix builds run on Node B in the deploy flow (git pull then `nixos-rebuild switch`)
- NixOS configuration comes from the flake in `/opt/go-choir` on Node B
- The real passkey/browser acceptance surface is `https://draft.choir-ip.com`; localhost is for worker smoke checks and Playwright-driven local verification
