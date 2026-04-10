# CI/CD Deploy Pipeline

## Workflow: `.github/workflows/ci.yml`

Three-job GitHub Actions workflow, adapted from the choiros-rs deploy pattern.

### Jobs

1. **check** — Go vet, test, build (linux/amd64 on ubuntu-latest)
2. **build-frontend** — Node.js 22 + pnpm 10, install and build Svelte frontend
3. **deploy-staging** — SSH to Node B, runs only on main after check + build-frontend pass

### Deploy Flow

1. SSH to Node B using deploy key
2. Clone repo to `/opt/go-choir` if not present
3. `git fetch origin && git reset --hard origin/main`
4. `nix build .#nixosConfigurations.go-choir-b.config.system.build.toplevel` (pre-build OOM check)
5. `nixos-rebuild switch --flake .#go-choir-b`
6. Wait 5 seconds for services to start
7. Smoke test: curl health endpoints on 127.0.0.1:8081, 8082, 8083, 8084
8. Always clean up SSH key in post step

### Required GitHub Secrets

| Secret | Description |
|--------|-------------|
| `OVH_DEPLOY_SSH_KEY` | SSH private key for deploying to Node B (must match the `github-actions-deploy@choiros` authorized key in nix/node-b.nix) |
| `OVH_NODE_B_HOST` | IP address or hostname of Node B (147.135.70.196) |

**These secrets are NOT yet configured on the repo.** The user needs to set them before the deploy job can work.

### Node B Workspace

- `/opt/go-choir` — git clone of https://github.com/yusefmosiah/go-choir.git
- Currently cloned and verified working (git pull succeeds)
- The tmpfiles rule in nix/node-b.nix ensures the directory exists

### Concurrency

- Group: `ci-${{ github.ref }}`
- Cancel-in-progress: true (newer pushes cancel older runs)

### Path Ignore

Pushes to `docs/**` and `*.md` files do not trigger CI.
