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

## Notes

- vmctl is NOT exposed through Caddy — test only via SSH
- Sandbox is not deployed as a host service — test binary directly (local build)
- TLS certificates are auto-provisioned by Caddy/Let's Encrypt
- Deploy happens via GitHub Actions, not manually
