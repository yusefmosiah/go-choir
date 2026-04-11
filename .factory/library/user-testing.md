# User Testing

## Validation Surface

Primary acceptance surface for Mission 3 remains the deployed HTTPS origin:

- `https://draft.choir-ip.com/`

Mission 3 starts with a deploy-readiness prerequisite because the deployed root currently serves a static placeholder page instead of the real auth SPA. Local validation tooling is available now, but deployed acceptance must be restored before the main milestones can be considered complete.

### Browser UI surface

**Primary tool:** `agent-browser` for deployed smoke/navigation checks  
**Deterministic browser harness:** Playwright Chromium against the local stack for passkey/session flows and stateful desktop/e-text flows  
**Manual tool:** real browser on `https://draft.choir-ip.com` when the deployed origin itself is the thing being validated

Expected browser-visible routes and states over time:
- `/` — guest auth UI when signed out; real desktop shell when signed in
- `/auth/*` — auth API traffic only; browser should not need direct service ports
- `GET /api/shell/bootstrap` — protected shell bootstrap request
- `GET /api/ws` — protected live channel
- `POST /api/agent/task` — runtime work submission (through shell/app flows)
- `GET /api/agent/status` — runtime status
- `GET /api/events` — runtime/app event stream
- app-level desktop surfaces for e-text, terminal, files, and mind-graph once those milestones land

Manual-validation limitation:
- automated passkey validation should use the Playwright Chromium harness against `http://localhost:4173`
- deployed manual browser validation is still useful for final origin/TLS confirmation on `https://draft.choir-ip.com`

### Direct HTTP/API surface

**Primary tool:** `curl`

Key public routes to exercise:
- `POST https://draft.choir-ip.com/auth/register/begin`
- `POST https://draft.choir-ip.com/auth/register/finish`
- `POST https://draft.choir-ip.com/auth/login/begin`
- `POST https://draft.choir-ip.com/auth/login/finish`
- `GET https://draft.choir-ip.com/auth/session`
- `POST https://draft.choir-ip.com/auth/logout`
- `GET https://draft.choir-ip.com/api/shell/bootstrap`
- `POST https://draft.choir-ip.com/api/agent/task`
- `GET https://draft.choir-ip.com/api/agent/status`
- `GET https://draft.choir-ip.com/api/events`

### Live-channel surface

**Primary tools:** browser automation plus `curl`/DevTools inspection where needed

Key deployed routes:
- `GET https://draft.choir-ip.com/api/ws`
- `GET https://draft.choir-ip.com/api/events`

Validators should confirm:
- signed-out websocket denial
- signed-in websocket success
- task/event-stream teardown on logout, renewal failure, and user switch
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
- Rationale: dry run showed lightweight browser automation on this machine (8 CPU / 16 GB RAM) with comfortable headroom for a light Svelte app; keep high concurrency only for smoke/navigation, not heavy desktop flows

### Playwright Chromium passkey and stateful desktop flows
- Max concurrent validators: **1**
- Rationale: these flows share a mutable local auth DB/service stack, require virtual-authenticator state, and later desktop/e-text flows will add even more mutable per-user state; keep them serialized unless the harness later proves reliable under isolated parallel runs
- Keep the repo-level `frontend-e2e` command serialized (`--workers=1`) because parallel Playwright workers can hit SQLite write contention in shared auth state and create flaky stateful browser tests

### manual browser / real passkey validation
- Max concurrent validators: **1**
- Rationale: requires a human-driven real browser flow on the deployed HTTPS origin

### remote Node B mutable verification
- Max concurrent validators: **1**
- Rationale: shared remote system state; avoid concurrent deploy/systemd/runtime mutation checks, especially once VM ownership and provider credentials are involved

### Node B VM/gateway/operator checks
- Max concurrent validators: **1**
- Rationale: these checks touch shared VM ownership state, gateway quotas, and host-side operational surfaces and should stay serialized

## Flow Validator Guidance: shell

- Treat the local repository checkout as shared read-mostly state.
- If starting local services for smoke checks, own the full lifecycle for the ports you use and clean them up before exiting.
- Prefer deployed-origin validation for acceptance whenever the public surface exists; local service startup is secondary and should not replace Node B validation.
- For stateful desktop/e-text/browser assertions, prefer Playwright over screenshot-only validation.

## Flow Validator Guidance: curl

- Public `https://draft.choir-ip.com` HTTP checks are safe to run concurrently.
- Local Playwright passkey automation should use `http://localhost:4173` and isolated browser contexts/cookie jars.
- Use separate cookie jars/browser profiles when validating multiple users or replay/rotation scenarios.
- Node B SSH-based validation touches shared remote system state; keep it single-threaded.
- Do not access Node A or any host other than `node-b`.

## Flow Validator Guidance: gateway-vm

**Surface:** Node B deployed origin `https://draft.choir-ip.com` plus SSH access for VM/guest inspection

**Concurrency limit:** 1 for Node B SSH-based validation (VM ownership, guest isolation, lifecycle)

**Isolation boundaries:**
- Use separate user accounts for multi-user isolation tests (create test users via auth API)
- VM ownership state is global on Node B - serialize all VM-related assertions
- Guest VM state is ephemeral - document VM IDs used for cross-assertion correlation

**Testing approach by assertion:**
- **Gateway HTTP assertions (VAL-GATEWAY-002 through 008):** Use `curl` against deployed public routes `/provider/*`, `/auth/*`, `/api/*`
- **Gateway end-to-end (VAL-GATEWAY-001):** Requires `agent-browser` with authenticated session to verify real provider response flows through `login → proxy → runtime → gateway → Bedrock/Z.AI → UI`
- **VM assertions (VAL-VM-001 through 012):** Requires SSH to Node B for `vmctl` state inspection, VM lifecycle management, and guest isolation verification
- **Cross assertions (VAL-CROSS-110, 112-117, 123):** Mix of `curl` (auth denial) and SSH (VM isolation, crash recovery)

**Required credentials/data for validation:**
- Node B SSH access available via `ssh node-b` alias
- Provider credentials are host-side only (validate absence from guest VMs via SSH)
- Test users must be created via auth registration API (WebAuthn requires Playwright virtual authenticator)

**Guest VM inspection commands:**
- `ssh node-b "cat /var/lib/go-choir/vmctl/ownership.json"` - VM ownership state
- `ssh node-b "ls -la /var/lib/go-choir/vms/"` - VM image directory
- Guest inspection requires VM boot completion and SSH into guest (if available)

**Debugging VM guest health issues:**
If VM health checks fail, check for kernel panic:
```bash
ssh node-b "journalctl -u go-choir-vmctl --since '5 minutes ago' --no-pager"
```
Common issues:
- **Kernel panic "VFS: Unable to mount root fs on unknown-block(0,0)"**: Missing `VIRTIO_MMIO_CMDLINE_DEVICES=y` in kernel config. Firecracker specifies virtio-mmio device via kernel cmdline but kernel ignores it without this option.
- **No /dev/vda created**: Same root cause - virtio-mmio block device not instantiated
- Check kernel boot args in vmctl logs for `virtio_mmio.device=4K@0xc0001000:6`

**Kernel config requirements for Firecracker:**
Required options in `nix/guest-image.nix`:
- `VIRTIO_MMIO = yes` (base virtio-mmio support)
- `VIRTIO_MMIO_CMDLINE_DEVICES = yes` (REQUIRED for cmdline device spec)
- `VIRTIO_BLK = yes` (block device support)
- `VIRTIO_NET = yes` (network device support)
- `EXT4_FS = yes` (root filesystem)
- `IP_PNP = yes` (IP autoconfiguration)

## Notes

- Browser validations must prove the frontend uses same-origin cookies only; no bearer-token injection, `localhost`, or direct service ports
- Local Playwright passkey automation is the preferred way to validate WebAuthn registration/login/session-lifecycle assertions without manual ceremonies
- Validate cookie-backed rehydration on hard reload/new tab and fallback to signed-out state when renewal can no longer succeed
- Mission 3 expands user testing to provider-backed runtime flows, desktop/e-text flows, VM-backed routing, and later operator/admin surfaces
- Bedrock and/or Z.AI are the first required real-provider validation targets
- Real Firecracker/KVM proof is Linux/Node B only
- TLS certificates are auto-provisioned by Caddy/Let's Encrypt
- Deploy happens via GitHub Actions / NixOS rebuild flow, not by ad hoc manual process
- This repo's flake exports Linux packages only; deployed runtime validation on Node B is the source of truth for VM-isolated acceptance
