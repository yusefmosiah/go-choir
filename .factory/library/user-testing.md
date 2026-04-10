# User Testing

## Validation Surface

**Primary surface**: HTTP health endpoints on draft.choir-ip.com (post-deploy)
**Tool**: curl
**Setup**: Services must be deployed to Node B via CI/CD pipeline

### Endpoints to test (external, via Caddy)
- `https://draft.choir-ip.com/auth/health` → 200 JSON
- `https://draft.choir-ip.com/api/health` → 200 JSON
- `https://draft.choir-ip.com/provider/health` → 200 JSON
- `https://draft.choir-ip.com/` → 200 HTML (frontend)

### Endpoints to test (internal, via SSH to Node B)
- `http://127.0.0.1:8081/health` → auth
- `http://127.0.0.1:8082/health` → proxy
- `http://127.0.0.1:8083/health` → vmctl (internal only)
- `http://127.0.0.1:8084/health` → gateway

### SSH access
- `ssh node-b` (alias for root@147.135.70.196)
- SSH key at default location

## Validation Concurrency

Max concurrent validators: **5**
Rationale: curl-based validation is trivially cheap. Each validator runs a few curl commands. No significant resource usage.

## Flow Validator Guidance: shell

- Treat the local repository checkout as shared read-mostly state.
- Do not run multiple validators that start services on ports `8081`-`8085` at the same time.
- Validators that send signals to local service processes must own the full service lifecycle for their assigned ports and clean up before exiting.
- Local build/test validators may run concurrently with remote Node B validators, but avoid mutating tracked files outside assigned report/evidence paths.

## Flow Validator Guidance: curl

- External HTTP checks against `https://draft.choir-ip.com` are safe to run concurrently.
- Node B SSH-based validation touches shared remote system state; keep all remote deploy/systemd/firewall checks within a single validator.
- Do not access Node A or any host other than `node-b`.
- Keep evidence under the assigned milestone/group directory only.

## Notes

- vmctl is NOT exposed through Caddy — test only via SSH
- Sandbox is not deployed as a host service — test binary directly (local build)
- TLS certificates are auto-provisioned by Caddy/Let's Encrypt
- Deploy happens via GitHub Actions, not manually
- For local signal-handling checks, prefer temporary built binaries (for example under `/tmp`) over `go run`; the `go run` wrapper can leave a child process running and make exit-code assertions unreliable.
- This repo's flake exports buildable packages only for `x86_64-linux`, so package-build assertions should be validated on Node B or another compatible Linux builder when running from macOS.
