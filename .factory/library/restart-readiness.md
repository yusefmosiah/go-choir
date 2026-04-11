# Restart Readiness and Public Exposure Control

This document describes the Node B production-safety measures for
VAL-DEPLOY-007, VAL-DEPLOY-008, and VAL-CROSS-118.

## Public Exposure Control (VAL-DEPLOY-007)

Only the intended public edge is internet-reachable on Node B. Internal
service ports are kept private through two layers of defense:

### Layer 1: Firewall

The NixOS firewall (`networking.firewall`) allows only ports 22, 80, 443:

```nix
networking.firewall = {
  enable = true;
  allowedTCPPorts = [ 22 80 443 ];
};
```

Service ports 8081–8085 are NOT in the allowed list, so they are
blocked at the kernel level from external connections.

### Layer 2: Localhost-only binding (defense in depth)

All Go services bind to `127.0.0.1` only, not `0.0.0.0`. This is
configured in the shared `internal/server` package:

- Default bind host: `127.0.0.1` (set by `defaultBindHost`)
- Override via: `SERVER_HOST` environment variable (e.g., `SERVER_HOST=0.0.0.0`)
- The `/health` endpoint reports the bound address in the `addr` field

Even if the firewall is misconfigured or disabled, services will only
accept connections from localhost. Caddy connects to services via
`127.0.0.1:port` in its reverse proxy configuration.

### Verification

On Node B, verify public exposure:
```bash
# From an external host, scan for open ports
nmap -p 22,80,443,8081,8082,8083,8084,8085 draft.choir-ip.com
# Expected: only 22, 80, 443 are open

# On Node B, verify services bind to localhost only
ss -tlnp | grep -E '808[1-5]'
# Expected: all services show 127.0.0.1:XXXX, not 0.0.0.0:XXXX
```

## Service Health and Restart Paths (VAL-DEPLOY-008)

### Health endpoints

Every Go service exposes `GET /health`:

- **auth**: `http://127.0.0.1:8081/health` — returns `{status, service, addr}`
- **proxy**: `http://127.0.0.1:8082/health` — returns `{status, service, upstream, addr}`
  - `status`: "ok" when proxy and upstream are healthy, "degraded" when upstream is unreachable
  - `upstream`: "ok" or "unreachable" — reflects sandbox health
- **sandbox**: `http://127.0.0.1:8085/health` — returns `{status, service, addr}`
- **vmctl**: `http://127.0.0.1:8083/health` — returns `{status, service, addr}`
- **gateway**: `http://127.0.0.1:8084/health` — returns `{status, service, addr}`

The proxy's enhanced health endpoint makes the protected-request backend
(sandbox) health observable. When the sandbox is unreachable, proxy
reports `status: "degraded"` and `upstream: "unreachable"`, allowing
operators to distinguish between a proxy failure and a backend failure.

### Systemd restart configuration

All services are configured with:

- `Restart = "on-failure"` — automatic restart on crash
- `RestartSec = 3` — 3-second backoff between restart attempts
- `WatchdogSec = 30` — systemd kills and restarts stuck services
- Service hardening (ProtectSystem, PrivateTmp, NoNewPrivileges, etc.)

### Service ordering

- **auth**: starts after `network-online.target`
- **sandbox**: starts after `network-online.target`
- **proxy**: starts after auth and sandbox (`after` + `wants` for sandbox, `requires` for auth)
- **vmctl/gateway**: independent of other go-choir services

The proxy `wants` the sandbox (soft dependency) but does not `require` it.
This means the proxy can start even if the sandbox is down, and its
health endpoint will report "degraded" until the sandbox recovers.

## Browser Recovery After Restart (VAL-CROSS-118)

### Auth restart

When auth restarts, browser users can recover safely because:

1. **Auth sessions persist in SQLite** (`/var/lib/go-choir/auth/auth.db`).
   The database file survives auth restarts because it lives in a
   writable runtime directory, not in the Nix store.

2. **The signing key persists** (`/var/lib/go-choir/auth-signing/ed25519-key`).
   Auth reuses the same key file across restarts, so existing access JWTs
   remain valid until they expire. The proxy can still verify them because
   it reads the same public key file.

3. **Refresh token rotation works after restart**. When a browser user's
   access JWT expires (5-minute TTL), the shell's silent renewal path
   calls `GET /auth/session`, which triggers refresh-token rotation.
   Since the refresh sessions are persisted in SQLite, the rotation
   succeeds even after an auth restart.

4. **Failed renewal falls back safely**. If the refresh token has also
   expired or been invalidated, the auth handler returns
   `{authenticated: false}` and the shell transitions to the guest state
   without looping or leaving the user half-authenticated.

### Proxy restart

When proxy restarts, browser users can recover because:

1. **Existing access JWTs remain valid**. The proxy re-reads the auth
   public key on startup and can immediately verify existing JWTs.

2. **Brief interruption is tolerable**. In-flight requests will receive
   connection errors, but the browser shell can retry or wait for
   automatic recovery. The shell's session-check mechanism detects
   the interruption and either rehydrates or falls back to guest state.

3. **Proxy health reflects upstream state**. After proxy restart, the
   `/health` endpoint checks sandbox reachability and reports "degraded"
   if the sandbox is not yet available, giving operators visibility
   into the recovery process.

### Sandbox restart

When the sandbox (protected-request backend) restarts:

1. **Proxy reports degraded state** via `/health` until the sandbox
   comes back online.

2. **Protected API requests fail gracefully**. The proxy returns
   upstream-unavailable errors to the browser, which can retry.

3. **Auth and public routes are unaffected**. Auth endpoints and
   static frontend files continue to work normally through Caddy,
   even when the sandbox backend is temporarily down.

### Cross-user pollution is prevented

After any restart, there is no risk of cross-user state pollution:

- Auth sessions are keyed by user ID and token hash, not by memory state
- The proxy creates fresh per-request auth validation with no shared
  mutable state between requests
- Cookie attributes (Secure, HttpOnly, SameSite=Lax) prevent cookie
  leakage across origins or users

## Go tests

The following tests verify restart-safe behavior:

| Test | Package | Verifies |
|------|---------|----------|
| `TestSessionDataSurvivesAuthRestart` | `internal/auth` | SQLite-backed user/credential/refresh session data persists across store close+reopen |
| `TestRefreshRotationWorksAfterAuthRestart` | `internal/auth` | Refresh token rotation works correctly after simulated auth restart |
| `TestProxyHealthReportsOkWhenUpstreamIsHealthy` | `internal/proxy` | Proxy /health reports "ok" when sandbox is reachable |
| `TestProxyHealthReportsDegradedWhenUpstreamIsUnreachable` | `internal/proxy` | Proxy /health reports "degraded" when sandbox is down |
| `TestProxyHealthRecoversAfterUpstreamRestart` | `internal/proxy` | Proxy /health transitions from "degraded" back to "ok" after upstream recovery |
| `TestNewServerBindsToLocalhostByDefault` | `internal/server` | Services bind to 127.0.0.1 by default, not 0.0.0.0 |
| `TestBindHostDefault` | `internal/server` | Default bind host is 127.0.0.1 |
| `TestBindHostFromEnv` | `internal/server` | SERVER_HOST env var overrides bind host |

## Remaining Node B verification

Some assertions require direct Node B access to fully verify:

- **VAL-DEPLOY-007 full proof**: Port scan from an external host and
  `ss -tlnp` output on Node B showing localhost-only binding
- **VAL-DEPLOY-008 restart transcript**: `systemctl restart go-choir-auth`
  and `systemctl restart go-choir-proxy` on Node B with before/after
  health checks and service status output
- **VAL-CROSS-118 browser recording**: Playwright or agent-browser recording
  of browser rehydration after auth/proxy restart on draft.choir-ip.com

The Go tests above verify the behavioral invariants that can be checked
without Node B access. The remaining proof requires SSH and browser
validation on the deployed host.
