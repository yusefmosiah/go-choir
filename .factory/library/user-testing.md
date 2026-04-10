# User Testing

## Validation Surface

Primary acceptance surface for this mission is the deployed HTTPS origin:

- `https://draft.choir-ip.com/`

Milestone 1 browser/auth/proxy validation is primarily Node B–based, not local-fidelity based.

### Browser UI surface

**Primary tool:** `agent-browser` for smoke/navigation checks  
**Manual tool:** real browser on `https://draft.choir-ip.com` for passkey/WebAuthn ceremonies

Expected browser-visible routes and states:
- `/` — guest auth UI when signed out; placeholder desktop shell when signed in
- `/auth/*` — auth API traffic only; browser should not need direct service ports
- `GET /api/shell/bootstrap` — protected shell bootstrap request
- `GET /api/ws` — protected live channel

Manual-validation limitation:
- passkey automation is not currently available in this environment
- real WebAuthn registration/login must be manually validated on the deployed HTTPS origin

### Direct HTTP/API surface

**Primary tool:** `curl`

Key deployed routes to exercise:
- `POST https://draft.choir-ip.com/auth/register/begin`
- `POST https://draft.choir-ip.com/auth/register/finish`
- `POST https://draft.choir-ip.com/auth/login/begin`
- `POST https://draft.choir-ip.com/auth/login/finish`
- `GET https://draft.choir-ip.com/auth/session`
- `POST https://draft.choir-ip.com/auth/logout`
- `GET https://draft.choir-ip.com/api/shell/bootstrap`

### Live-channel surface

**Primary tool:** browser DevTools / browser-based validation

Key deployed route:
- `GET https://draft.choir-ip.com/api/ws`

Validators should confirm:
- signed-out websocket denial
- signed-in websocket success
- live-channel teardown on logout / failed renewal / user switch

### Remote system surface

**Primary tools:** `curl`, SSH only when infrastructure verification is required

SSH access:
- `ssh node-b` (alias for root@147.135.70.196)
- use only for deployment/runtime verification that cannot be observed via the public origin

Do not rely on local direct-port browser flows for acceptance.

## Validation Concurrency

### curl-only deployed checks
- Max concurrent validators: **5**
- Rationale: low CPU/memory cost; these are lightweight HTTP checks against the deployed origin

### agent-browser deployed smoke/navigation checks
- Max concurrent validators: **5**
- Rationale: dry run showed lightweight browser automation on this machine (8 CPU / 16 GB RAM) with comfortable headroom for a light Svelte app

### manual browser / real passkey validation
- Max concurrent validators: **1**
- Rationale: requires a human-driven real browser flow on the deployed HTTPS origin

### remote Node B mutable verification
- Max concurrent validators: **1**
- Rationale: shared remote system state; avoid concurrent deploy/systemd/runtime mutation checks

## Flow Validator Guidance: shell

- Treat the local repository checkout as shared read-mostly state.
- If starting local services for smoke checks, own the full lifecycle for the ports you use and clean them up before exiting.
- Prefer deployed-origin validation for acceptance; local service startup is secondary and should not replace Node B validation.

## Flow Validator Guidance: curl

- Public `https://draft.choir-ip.com` HTTP checks are safe to run concurrently.
- Use separate cookie jars/browser profiles when validating multiple users or replay/rotation scenarios.
- Node B SSH-based validation touches shared remote system state; keep it single-threaded.
- Do not access Node A or any host other than `node-b`.

## Notes

- Browser validations must prove the frontend uses same-origin cookies only; no bearer-token injection, `localhost`, or direct service ports
- Validate cookie-backed rehydration on hard reload/new tab and fallback to signed-out state when renewal can no longer succeed
- Provider/gateway and VM routing are out of scope for this mission’s user testing
- TLS certificates are auto-provisioned by Caddy/Let's Encrypt
- Deploy happens via GitHub Actions / NixOS rebuild flow, not by ad hoc manual process
- This repo's flake exports Linux packages only; deployed runtime validation on Node B is the source of truth for acceptance
