# Firecracker guest image builder for go-choir Node B.
#
# This Nix expression builds the guest VM artifacts:
#   - A Linux kernel suitable for Firecracker
#   - An initramfs that loads ext4 and mounts the root filesystem
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
  # The nixpkgs kernel to use for the guest. We use linux_6_1 because
  # it is well-tested with Firecracker.
  guestKernelPackage = pkgs.linuxKernel.kernels.linux_6_1;

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

    # Mount essential filesystems.
    mount -t proc proc /proc
    mount -t sysfs sysfs /sys
    mount -t devtmpfs devtmpfs /dev

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
  # Uses debugfs to populate the image without requiring mount(8),
  # which is unavailable inside the Nix build sandbox.
  guestRootfs = pkgs.runCommand "go-choir-guest-rootfs.ext4" {
    buildInputs = [ pkgs.e2fsprogs ];
    # 256MB is generous for a single Go binary + runtime state.
    # The persistent user data is on a separate mount, not in this image.
  } ''
    # Create an empty ext4 filesystem image.
    dd if=/dev/zero of=$out bs=1M count=256
    mkfs.ext4 -F $out

    # Populate the image using debugfs (no mount needed).
    # debugfs -w opens the image in write mode; -f reads commands from a script.
    # The -R flag passes a single command; we build a command file for
    # bulk directory and file creation.
    {
      # Create directory structure.
      echo "mkdir bin"
      echo "mkdir tmp"
      echo "mkdir mnt"
      echo "mkdir mnt/persistent"
      echo "mkdir etc"
      echo "mkdir dev"
      echo "mkdir proc"
      echo "mkdir sys"
      echo "mkdir run"

      # Copy the sandbox binary.
      echo "write ${goChoirPackages.sandbox}/bin/sandbox bin/sandbox"

      # Copy the init script.
      echo "write ${guestRoot}/bin/init bin/init"

      # Copy resolv.conf.
      echo "write ${guestRoot}/etc/resolv.conf etc/resolv.conf"
    } > /tmp/debugfs.cmds

    debugfs -w -f /tmp/debugfs.cmds $out

    # Set execute permissions on binaries.
    {
      echo "modify_inode bin/sandbox mode 0755"
      echo "modify_inode bin/init mode 0755"
    } > /tmp/debugfs-chmod.cmds

    debugfs -w -f /tmp/debugfs-chmod.cmds $out
  '';

  # Minimal initramfs that loads the ext4 kernel module and mounts
  # the root filesystem. The nixpkgs kernel builds ext4 as a module,
  # so we need an initrd to load it before mounting root.
  #
  # The initramfs init script:
  # 1. Mounts /proc and /sys
  # 2. Loads the ext4 module from the kernel modules directory
  # 3. Mounts /dev/vda (the Firecracker rootfs drive) to /root
  # 4. Switches root to /root and execs /bin/init
  #
  # No provider credentials are included (VAL-VM-011).
  guestInitrd = pkgs.runCommand "go-choir-guest-initrd.cpio.gz" {
    buildInputs = [ pkgs.cpio pkgs.gzip guestKernelPackage ];
  } ''
    mkdir -p $out_dir
    outdir=$TMPDIR/initrd

    mkdir -p $outdir/{bin,proc,sys,dev,root,lib/modules}

    # Copy the ext4 kernel module and its dependencies.
    # The kernel modules are in the kernel package's /lib/modules/ directory.
    KMOD_DIR=$(find ${guestKernelPackage}/lib/modules -mindepth 1 -maxdepth 1 -type d | head -1)

    # Copy ext4 module and its dependencies (jbd2, crc16, mbcache, etc.)
    for mod in ext4 jbd2 mbcache crc16 crc32_generic; do
      find "$KMOD_DIR" -name "$mod.ko*" -exec cp --parents {} $outdir \; 2>/dev/null || true
    done
    # Copy all of kernel/lib/modules to ensure dependencies are available
    cp -r "$KMOD_DIR"/* $outdir/lib/modules/$(basename "$KMOD_DIR")/ 2>/dev/null || true

    # Create a minimal init for the initramfs.
    cat > $outdir/init << 'INITEOF'
    #!/bin/sh
    export PATH=/bin

    # Mount essential filesystems.
    mount -t proc proc /proc
    mount -t sysfs sysfs /sys
    mount -t devtmpfs devtmpfs /dev

    # Load kernel modules needed for ext4 root.
    KVER=$(uname -r)
    MODDIR=/lib/modules/$KVER
    if [ -d "$MODDIR" ]; then
      for mod in crc16 crc32_generic mbcache jbd2 ext4; do
        mod=$(find "$MODDIR" -name "$mod.ko*" 2>/dev/null | head -1)
        if [ -n "$mod" ]; then
          insmod "$mod" 2>/dev/null || true
        fi
      done
    fi

    # Wait briefly for the block device to appear.
    for i in 1 2 3 4 5; do
      if [ -b /dev/vda ]; then
        break
      fi
      sleep 0.1
    done

    # Mount the root filesystem.
    mount -t ext4 /dev/vda /root

    # Unmount initramfs filesystems before switch_root.
    umount /proc /sys /dev 2>/dev/null || true

    # Switch to the real root and exec init.
    exec switch_root /root /bin/init
    INITEOF
    chmod +x $outdir/init

    # Create the cpio archive.
    cd $outdir
    find . | cpio -o -H newc 2>/dev/null | gzip > $out
  '';

  # Firecracker-compatible Linux kernel.
  # Firecracker requires an uncompressed ELF kernel (vmlinux).
  # The nixpkgs kernel dev output contains vmlinux.
  guestKernel = guestKernelPackage.dev;

in
{
  # The ext4 rootfs image containing the sandbox runtime.
  inherit guestRootfs;

  # The kernel vmlinux ELF binary for Firecracker.
  inherit guestKernel;

  # The initramfs for loading ext4 module before root mount.
  inherit guestInitrd;

  # Convenience attribute that provides all artifacts together.
  guest-image = pkgs.runCommand "go-choir-guest-image" { } ''
    mkdir -p $out
    cp ${guestRootfs} $out/rootfs.ext4
    cp ${guestKernel}/vmlinux $out/vmlinux
    cp ${guestInitrd} $out/initrd.cpio.gz
  '';
}
