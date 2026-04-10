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
- `SANDBOX_PORT` — sandbox listen port (default: `8085`)

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
- `PROXY_SANDBOX_URL` — hardcoded placeholder sandbox base URL for Milestone 1

### Placeholder sandbox runtime
- `SANDBOX_ID` — stable identity string returned by the placeholder sandbox for validation

### Route invariants
These should stay constant for this mission:
- protected shell bootstrap route: `GET /api/shell/bootstrap`
- protected shell WebSocket route: `GET /api/ws`

## External Dependencies

- **Node B (OVH)**: NixOS bare metal at 147.135.70.196 (draft.choir-ip.com)
- **GitHub Actions**: CI/CD pipeline, secrets configured on repo
- **Let's Encrypt**: TLS certificates via Caddy (automatic)

## Secrets and persistence

- Keep signing keys and any cookie/session secrets out of git and out of the Nix store
- Node B runtime secrets should live in a writable runtime location (for example `/var/lib/go-choir/...`) or systemd credentials
- **Note on Ed25519 signing keys generated at runtime**: Keys generated via `ssh-keygen` (like in systemd `ExecStartPre`) produce OpenSSH formatted keys rather than raw PKCS8/PEM. Go's `crypto/ed25519` expects PKCS8/PEM, so the auth service must handle parsing this OpenSSH format.
- Milestone 1 auth persistence is SQLite-backed; the DB must live in a writable persistent path, not inside the repo checkout or the Nix store
- Local worker setup may use temporary files under `/tmp/go-choir-m2`, and the local service defaults may resolve there when explicit path env vars are omitted
- Deployed validation must still target `https://draft.choir-ip.com`

## Platform Notes

- Go binaries must cross-compile to `linux/amd64` for Node B (local dev is `darwin/arm64`)
- Nix builds run ON Node B (git-pull-then-rebuild pattern), not pushed as pre-built artifacts
- NixOS configuration comes from the flake in `/opt/go-choir` on Node B
- The real passkey acceptance surface is the deployed HTTPS origin `draft.choir-ip.com`; localhost is useful only for worker smoke checks
