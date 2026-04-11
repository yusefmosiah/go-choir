# Firecracker guest image builder for go-choir Node B.
#
# This Nix expression builds the guest VM artifacts:
#   - A Linux kernel suitable for Firecracker
#   - A root filesystem (ext4) containing the sandbox runtime
#
# The guest image includes ONLY the sandbox runtime binary and its
# minimum runtime dependencies. Provider credentials, gateway secrets,
# auth signing keys, and other host-side material are explicitly
# excluded from the guest (VAL-VM-011).
#
# Built artifacts are what Node B actually boots (VAL-VM-010).
{ pkgs, goChoirPackages }:

let
  # Minimal guest root filesystem containing only the sandbox runtime.
  # This is the set of files that will be packed into the ext4 rootfs image.
  guestRoot = pkgs.runCommand "go-choir-guest-root" {
    # Explicitly do NOT pass any secret-containing environment variables
    # into the derivation. The Nix sandbox prevents access to host
    # environment anyway, but this comment makes the intent clear (VAL-VM-011).
  } ''
    mkdir -p $out/bin
    mkdir -p $out/tmp
    mkdir -p $out/mnt/persistent

    # Copy only the sandbox binary — no auth, no gateway, no proxy.
    # The guest runs ONLY the sandbox runtime.
    cp ${goChoirPackages.sandbox}/bin/sandbox $out/bin/sandbox
    chmod +x $out/bin/sandbox

    # Create a minimal init script that launches the sandbox runtime.
    # The sandbox listens on the configured GUEST_PORT inside the VM.
    # No provider credentials are passed via environment or arguments (VAL-VM-011).
    cat > $out/bin/init << 'EOF'
    #!/bin/sh
    export PATH=/bin:/usr/bin
    export SANDBOX_PORT=''${guest_port:-8085}
    export SANDBOX_ID=''${vm_id:-sandbox-guest}
    export RUNTIME_STORE_PATH=/mnt/persistent/state

    # Wait for the persistent mount to be available.
    # In production, the Firecracker VM config mounts persistent storage
    # at /mnt/persistent for per-user state that survives stop/resume.
    mkdir -p /mnt/persistent/state

    echo "go-choir guest: starting sandbox (port=$SANDBOX_PORT vm=$SANDBOX_ID)"
    exec /bin/sandbox
    EOF
    chmod +x $out/bin/init

    # Create a simple /etc/resolv.conf for DNS inside the guest.
    mkdir -p $out/etc
    echo "nameserver 8.8.8.8" > $out/etc/resolv.conf

    # Create required /dev, /proc, /sys mount points (populated at boot).
    mkdir -p $out/dev $out/proc $out/sys $out/run
  '';

  # Build the ext4 rootfs image from the guest root.
  # Firecracker requires an ext4 filesystem image as the root drive.
  guestRootfs = pkgs.runCommand "go-choir-guest-rootfs.ext4" {
    buildInputs = [ pkgs.e2fsprogs ];
    # 256MB is generous for a single Go binary + runtime state.
    # The persistent user data is on a separate mount, not in this image.
  } ''
    # Create an empty ext4 filesystem image.
    dd if=/dev/zero of=$out bs=1M count=256
    mkfs.ext4 -F $out

    # Mount and copy the guest root files.
    mkdir -p /tmp/mnt
    mount -o loop $out /tmp/mnt
    cp -r ${guestRoot}/* /tmp/mnt/
    umount /tmp/mnt
  '';

  # Firecracker-compatible Linux kernel.
  # Firecracker requires an uncompressed ELF kernel (vmlinux).
  # We use the nixpkgs kernel with the required KVM guest config.
  guestKernel = pkgs.linuxKernel.kernels.linux_6_1.kernel;

in
{
  # The ext4 rootfs image containing the sandbox runtime.
  inherit guestRootfs;

  # The kernel vmlinux ELF binary for Firecracker.
  inherit guestKernel;

  # Convenience attribute that provides both artifacts together.
  guest-image = pkgs.runCommand "go-choir-guest-image" { } ''
    mkdir -p $out
    cp ${guestRootfs} $out/rootfs.ext4
    cp ${guestKernel}/vmlinux $out/vmlinux
  '';
}
