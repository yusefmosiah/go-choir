# Firecracker guest image builder for go-choir Node B.
#
# This Nix expression builds the guest VM artifacts:
#   - A Linux kernel with ext4 built-in (not as a module)
#   - A root filesystem (ext4) containing the sandbox runtime
#
# The guest image includes ONLY the sandbox runtime binary and its
# minimum runtime dependencies. Provider credentials, gateway secrets,
# auth signing keys, and other host-side material are explicitly
# excluded from the guest (VAL-VM-011).
#
# Built artifacts are what Node B actually boots (VAL-VM-010).
#
# IMPORTANT: All bash scripts are extracted into separate writeShellScript
# derivations to avoid Nix parser ambiguity with '' indented strings.
# Nix 2.31+ has issues parsing complex bash (parameter expansion, case
# statements) inside '' strings passed to runCommand. By extracting the
# bash into writeShellScript derivations, the runCommand bodies become
# trivial single-line script invocations.
{ pkgs, goChoirPackages }:

let
  # Custom kernel with ext4 built-in (not as a module).
  # The standard nixpkgs kernel builds ext4 as a module, which means
  # the guest can't mount its rootfs without an initrd. By building
  # ext4 into the kernel, we avoid needing an initrd entirely.
  guestKernelPackage = pkgs.linuxKernel.kernels.linux_6_1.override {
    structuredExtraConfig = with pkgs.lib.kernel; {
      EXT4_FS = yes;
      JBD2 = yes;
      CRC16 = yes;
      NET = yes;
      INET = yes;
      # IP_PNP enables kernel-level IP configuration from the ip= boot
      # parameter. Without this, the ip= parameter is silently ignored
      # and the guest has no network connectivity until userspace brings
      # up the interface.
      IP_PNP = yes;
      NETDEVICES = yes;
      VIRTIO_MMIO = yes;
      VIRTIO_MMIO_CMDLINE_DEVICES = yes;
      VIRTIO_BLK = yes;
      VIRTIO_NET = yes;
      DEVTMPFS = yes;
      DEVTMPFS_MOUNT = yes;
      TTY = yes;
      SERIAL_8250 = yes;
      SERIAL_8250_CONSOLE = yes;
      PROC_FS = yes;
      SYSFS = yes;
      TMPFS = yes;
    };
  };

  # Guest init script — extracted into its own derivation to avoid
  # Nix parser ambiguity with '' indented strings containing bash
  # parameter-expansion patterns like ${param#vm_id=}.
  guestInitScript = pkgs.writeShellScript "guest-init" ''
    export PATH=/bin:/usr/bin

    # Mount essential virtual filesystems.
    mount -t proc proc /proc
    mount -t sysfs sysfs /sys
    mount -t devtmpfs devtmpfs /dev
    mount -t tmpfs tmpfs /tmp

    # Parse kernel cmdline to extract guest parameters.
    # Firecracker passes configuration via /proc/cmdline using the format:
    #   guest_port=8085 vm_id=vm-abc epoch=1 ip=172.X.0.2::172.X.0.1:...
    CMDLINE=""
    if [ -f /proc/cmdline ]; then
        CMDLINE=$(cat /proc/cmdline)
    fi

    # Extract individual parameters from cmdline.
    GUEST_PORT=""
    VM_ID=""
    EPOCH=""
    for param in $CMDLINE; do
        case "$param" in
            guest_port=*) GUEST_PORT="''${param#guest_port=}" ;;
            vm_id=*)      VM_ID="''${param#vm_id=}" ;;
            epoch=*)      EPOCH="''${param#epoch=}" ;;
        esac
    done

    # Apply defaults for missing parameters.
    : "''${GUEST_PORT:=8085}"
    : "''${VM_ID:=sandbox-guest}"
    : "''${EPOCH:=0}"

    # Configure guest networking from the ip= kernel parameter.
    # The kernel's ip= parameter configures the interface automatically
    # when CONFIG_IP_PNP is enabled. However, we still need to bring
    # the interface up manually as a fallback in case ip= was not
    # processed by the kernel (e.g., missing CONFIG_IP_PNP).
    #
    # Parse the ip= parameter to extract guest IP, gateway, and mask.
    IP_PARAM=""
    for param in $CMDLINE; do
        case "$param" in
            ip=*) IP_PARAM="''${param#ip=}" ;;
        esac
    done

    if [ -n "$IP_PARAM" ]; then
        # ip= format: <client-ip>::<server-ip>:<netmask>::<device>:<autoconf>
        # Extract client IP (first field).
        GUEST_IP=$(echo "$IP_PARAM" | cut -d: -f1)
        # Extract server/gateway IP (third field).
        HOST_IP=$(echo "$IP_PARAM" | cut -d: -f3)

        # Bring up the network interface if the kernel didn't already.
        if ! ip addr show eth0 2>/dev/null | grep -q "inet "; then
            ip link set eth0 up
            if [ -n "$GUEST_IP" ]; then
                ip addr add "$GUEST_IP/30" dev eth0
            fi
            if [ -n "$HOST_IP" ]; then
                ip route add default via "$HOST_IP" dev eth0 2>/dev/null || true
            fi
        fi

        # Set gateway URL so the sandbox can reach the host-side gateway.
        # The gateway listens on the host at 127.0.0.1:8084, which is
        # reachable from the guest via the host tap IP (172.X.0.1).
        # Provider credentials stay host-side (VAL-VM-011).
        if [ -n "$HOST_IP" ]; then
            export RUNTIME_GATEWAY_URL="http://''${HOST_IP}:8084"
        fi
    else
        # No ip= parameter — try to bring up eth0 anyway.
        ip link set eth0 up 2>/dev/null || true
    fi

    # Set sandbox environment variables.
    export SANDBOX_PORT="$GUEST_PORT"
    export SANDBOX_ID="$VM_ID"
    export RUNTIME_STORE_PATH=/mnt/persistent/state

    # Ensure the persistent state directory exists.
    mkdir -p /mnt/persistent/state

    # Read the gateway token from the persistent directory.
    # The vmctl service writes this token before booting the VM.
    # It authenticates the sandbox to the host-side gateway.
    if [ -f /mnt/persistent/gateway-token ]; then
        export RUNTIME_GATEWAY_TOKEN=$(cat /mnt/persistent/gateway-token)
    fi

    echo "go-choir guest: starting sandbox (port=$SANDBOX_PORT vm=$SANDBOX_ID epoch=$EPOCH)"
    echo "go-choir guest: network configured (ip=$GUEST_IP gateway=$HOST_IP)"
    if [ -n "$RUNTIME_GATEWAY_URL" ]; then
        echo "go-choir guest: gateway=$RUNTIME_GATEWAY_URL"
    fi

    # Execute the sandbox binary (replaces init process).
    exec /bin/sandbox
  '';

  # Script to assemble the guest root filesystem directory tree.
  # Extracted into writeShellScript to avoid '' string parsing issues
  # in Nix 2.31+ when complex bash lives inside runCommand.
  guestRootSetupScript = pkgs.writeShellScript "guest-root-setup" ''
    mkdir -p $out/bin
    mkdir -p $out/tmp
    mkdir -p $out/mnt/persistent
    mkdir -p $out/usr/bin
    mkdir -p $out/sbin

    # Copy only the sandbox binary — no auth, no gateway, no proxy.
    # The guest runs ONLY the sandbox runtime.
    cp ${goChoirPackages.sandbox}/bin/sandbox $out/bin/sandbox
    chmod +x $out/bin/sandbox

    # Copy essential networking utilities for the init script.
    # These are needed for: ip addr/link/route, and shell builtins
    # (mount, mkdir, cat, echo, grep, cut) come from busybox or
    # are statically linked.
    cp ${pkgs.iproute2}/bin/ip $out/bin/ip
    chmod +x $out/bin/ip

    # The init script uses standard POSIX utilities. On NixOS, these
    # come from coreutils and bash. We copy the minimal set needed
    # by the init script: sh (bash), mount, mkdir, cat, echo, grep, cut.
    cp ${pkgs.bash}/bin/sh $out/bin/sh
    chmod +x $out/bin/sh

    # Create symlinks for core utilities used by the init script.
    # busybox would be smaller but coreutils are already in the closure.
    cp ${pkgs.coreutils}/bin/mount $out/bin/mount
    cp ${pkgs.coreutils}/bin/mkdir $out/bin/mkdir
    cp ${pkgs.coreutils}/bin/cat $out/bin/cat
    cp ${pkgs.coreutils}/bin/echo $out/bin/echo
    cp ${pkgs.coreutils}/bin/cut $out/bin/cut
    cp ${pkgs.gnugrep}/bin/grep $out/bin/grep
    chmod +x $out/bin/mount $out/bin/mkdir $out/bin/cat $out/bin/echo $out/bin/cut $out/bin/grep

    # Copy the pre-built guest init script.
    cp ${guestInitScript}/bin/guest-init $out/bin/init

    # Create a simple /etc/resolv.conf for DNS inside the guest.
    mkdir -p $out/etc
    echo "nameserver 8.8.8.8" > $out/etc/resolv.conf

    # Create required /dev, /proc, /sys mount points (populated at boot).
    mkdir -p $out/dev $out/proc $out/sys $out/run
  '';

  # Minimal guest root filesystem containing only the sandbox runtime.
  # This is the set of files that will be packed into the ext4 rootfs image.
  #
  # Includes: sandbox binary, init script, and minimal networking tools
  # (ip from iproute2, plus standard POSIX utilities from coreutils/shadow).
  # Provider credentials, gateway secrets, auth signing keys, and other
  # host-side material are explicitly excluded (VAL-VM-011).
  #
  # The actual bash logic lives in guestRootSetupScript (a writeShellScript
  # derivation) to avoid Nix 2.31+ '' indented-string parsing issues.
  guestRoot = pkgs.runCommand "go-choir-guest-root" {
    # Explicitly do NOT pass any secret-containing environment variables
    # into the derivation. The Nix sandbox prevents access to host
    # environment anyway, but this comment makes the intent clear (VAL-VM-011).
  } ''
    exec ${guestRootSetupScript}
  '';

  # Script to build the ext4 rootfs image using debugfs.
  # Extracted into writeShellScript to avoid '' string parsing issues
  # in Nix 2.31+ when complex bash lives inside runCommand.
  guestRootfsBuildScript = pkgs.writeShellScript "guest-rootfs-build" ''
    # Create an empty ext4 filesystem image.
    dd if=/dev/zero of=$out bs=1M count=512
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
      echo "mkdir usr"
      echo "mkdir usr/bin"
      echo "mkdir sbin"

      # Copy the sandbox binary.
      echo "write ${goChoirPackages.sandbox}/bin/sandbox bin/sandbox"

      # Copy the init script.
      echo "write ${guestRoot}/bin/init bin/init"

      # Copy networking utilities.
      echo "write ${pkgs.iproute2}/bin/ip bin/ip"

      # Copy shell and core utilities.
      echo "write ${pkgs.bash}/bin/sh bin/sh"
      echo "write ${pkgs.coreutils}/bin/mount bin/mount"
      echo "write ${pkgs.coreutils}/bin/mkdir bin/mkdir"
      echo "write ${pkgs.coreutils}/bin/cat bin/cat"
      echo "write ${pkgs.coreutils}/bin/echo bin/echo"
      echo "write ${pkgs.coreutils}/bin/cut bin/cut"
      echo "write ${pkgs.gnugrep}/bin/grep bin/grep"

      # Copy resolv.conf.
      echo "write ${guestRoot}/etc/resolv.conf etc/resolv.conf"
    } > /tmp/debugfs.cmds

    debugfs -w -f /tmp/debugfs.cmds $out

    # Set execute permissions on binaries.
    {
      echo "modify_inode bin/sandbox mode 0755"
      echo "modify_inode bin/init mode 0755"
      echo "modify_inode bin/ip mode 0755"
      echo "modify_inode bin/sh mode 0755"
      echo "modify_inode bin/mount mode 0755"
      echo "modify_inode bin/mkdir mode 0755"
      echo "modify_inode bin/cat mode 0755"
      echo "modify_inode bin/echo mode 0755"
      echo "modify_inode bin/cut mode 0755"
      echo "modify_inode bin/grep mode 0755"
    } > /tmp/debugfs-chmod.cmds

    debugfs -w -f /tmp/debugfs-chmod.cmds $out
  '';

  # Build the ext4 rootfs image from the guest root.
  # Firecracker requires an ext4 filesystem image as the root drive.
  # Uses debugfs to populate the image without requiring mount(8),
  # which is unavailable inside the Nix build sandbox.
  #
  # The actual bash logic lives in guestRootfsBuildScript (a
  # writeShellScript derivation) to avoid Nix 2.31+ '' indented-string
  # parsing issues.
  guestRootfs = pkgs.runCommand "go-choir-guest-rootfs.ext4" {
    buildInputs = [ pkgs.e2fsprogs ];
    # 512MB rootfs to accommodate sandbox binary + networking utilities.
    # The persistent user data is on a separate mount, not in this image.
  } ''
    exec ${guestRootfsBuildScript}
  '';

  # Firecracker-compatible Linux kernel.
  # Firecracker requires an uncompressed ELF kernel (vmlinux).
  # The kernel dev output contains vmlinux.
  # This kernel has ext4 built-in so no initrd is needed.
  guestKernel = guestKernelPackage.dev;

in
{
  # The ext4 rootfs image containing the sandbox runtime.
  inherit guestRootfs;

  # The kernel vmlinux ELF binary for Firecracker.
  inherit guestKernel;

  # Convenience attribute that provides artifacts together.
  guest-image = pkgs.runCommand "go-choir-guest-image" { } ''
    mkdir -p $out
    cp ${guestRootfs} $out/rootfs.ext4
    cp ${guestKernel}/vmlinux $out/vmlinux
  '';
}
