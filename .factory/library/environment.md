# Environment

Environment variables, external dependencies, and setup notes.

**What belongs here:** Required env vars, external API keys/services, dependency quirks, platform-specific notes.
**What does NOT belong here:** Service ports/commands (use `.factory/services.yaml`).

---

## Environment Variables

### Service Ports (defaults if unset)
- `AUTH_PORT` — auth service listen port (default: 8081)
- `PROXY_PORT` — proxy service listen port (default: 8082)
- `VMCTL_PORT` — vmctl service listen port (default: 8083)
- `GATEWAY_PORT` — gateway service listen port (default: 8084)
- `SANDBOX_PORT` — sandbox listen port (default: 8085)

## External Dependencies

- **Node B (OVH)**: NixOS bare metal at 147.135.70.196 (draft.choir-ip.com)
- **GitHub Actions**: CI/CD pipeline, secrets configured on repo
- **Let's Encrypt**: TLS certificates via Caddy (automatic)

## Platform Notes

- Go binaries must cross-compile to `linux/amd64` for Node B (local dev is `darwin/arm64`)
- Nix builds run ON Node B (git-pull-then-rebuild pattern), not pushed as pre-built artifacts
- NixOS configuration comes from the flake in `/opt/go-choir` on Node B
