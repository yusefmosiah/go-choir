# Guest Image Deployment

## Building Guest Images

Guest images are built via `nix build .#guest-image` on Node B (x86_64-linux only).

The build produces:
- `vmlinux` — uncompressed ELF kernel (from `linuxKernel.kernels.linux_6_1.dev`)
- `rootfs.ext4` — ext4 filesystem image containing the sandbox binary

### Key implementation notes

1. **Kernel**: The nixpkgs kernel package (`linux_6_1`) only ships `bzImage` in the main output. The uncompressed `vmlinux` required by Firecracker is in the **dev** output (`linux_6_1.dev`).

2. **Rootfs**: The ext4 image is populated using `debugfs` from `e2fsprogs`, not `mount`. The Nix build sandbox does not have `mount(8)` available, so debugfs write commands are used to create directories and copy files.

3. **Deployment path**: Built images are deployed to `/var/lib/go-choir/guest/` on Node B. The vmctl service references `VM_KERNEL_IMAGE=/var/lib/go-choir/guest/vmlinux` and `VM_ROOTFS_IMAGE=/var/lib/go-choir/guest/rootfs.ext4`.

## Deploying to Node B

```bash
# Pull latest code
ssh node-b "cd /opt/go-choir && git pull origin main"

# Build guest images
ssh node-b "cd /opt/go-choir && nix build .#guest-image --no-link --print-out-paths"

# Deploy images to runtime path
ssh node-b "GUEST_PATH=\$(nix build .#guest-image --no-link --print-out-paths 2>/dev/null) && \
  cp \$GUEST_PATH/vmlinux /var/lib/go-choir/guest/vmlinux && \
  cp \$GUEST_PATH/rootfs.ext4 /var/lib/go-choir/guest/rootfs.ext4"

# Rebuild NixOS (rebuilds Go services, frontend, Caddy config)
ssh node-b "cd /opt/go-choir && sudo nixos-rebuild switch --flake .#go-choir-b"
```

## Caddy Public Health Route

The `/health` route on the public origin (`https://draft.choir-ip.com/health`) is proxied to the proxy service on port 8082. This was added in the Caddy config in `nix/node-b.nix` to make proxy health reachable for monitoring and validation.
