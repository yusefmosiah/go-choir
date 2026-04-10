# NixOS Configuration Notes

## Flake Structure

- `flake.nix` — Root flake with `buildGoModule` for each Go service and a `stdenv.mkDerivation` for the frontend
- `nix/hardware.nix` — OVH bare metal hardware config (shared between Node A and B patterns)
- `nix/disks.nix` — Node B disk layout with UUID-based mounts
- `nix/node-b.nix` — Full Node B system configuration

## Key Patterns

### Go Packages
Each Go service is built with `buildGoModule` using `subPackages` to select the specific `cmd/` directory. The `commonGoArgs` attrset provides shared configuration (src filter, vendorHash, doCheck).

### vendorHash
Currently set to `null` because go.mod has no external dependencies. When dependencies are added, the first build will fail with a hash mismatch error — the correct hash will be shown in the error message and should be pasted into `vendorHash`.

### Frontend Package
Uses `pkgs.runCommand` to generate a placeholder `index.html` with "go-choir" text. This avoids network access in the Nix sandbox (pnpm install would fail). The real Svelte build pipeline with pnpm will be added in Mission 2 when the frontend has real content.

### nixosConfigurations.go-choir-b
The NixOS system config is built from 3 modules passed as `specialArgs.goChoirPackages` so systemd services and Caddy can reference the built packages.

## Caddy Configuration
- Virtual host: `draft.choir-ip.com`
- Routes: `/auth/*` → :8081, `/api/*` → :8082, `/provider/*` → :8084
- Root `/` serves frontend static assets via `file_server`
- vmctl (:8083) is NOT exposed through Caddy (internal-only)
- TLS is automatic via Caddy/Let's Encrypt

## Firewall
Only ports 22, 80, 443 are open. Service ports 8081-8084 are localhost-only.

## SSH Keys
Both the human operator key and the GitHub Actions deploy key are configured from choiros-rs patterns.
