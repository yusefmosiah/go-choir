# Sandbox guest NixOS config for Firecracker microVMs on Node B.
#
# This module defines the guest VM configuration using the upstream
# microvm.nix module (https://github.com/microvm-nix/microvm.nix).
# The upstream module handles kernel building, initrd generation,
# rootfs image creation, and Firecracker runner script generation.
#
# Key design choices (aligned with choiros-rs approach):
#   - Uses upstream microvm.nix (not the fork) for stability
#   - virtio-blk for data volumes (storeDiskInterface = "blk")
#   - erofs for the shared nix store disk (automatic when shares = [])
#   - systemd as init (proper NixOS boot instead of custom init script)
#   - Go control plane (vmmanager/vmctl) manages VM lifecycle externally
#
# Guest contains ONLY the sandbox runtime binary — no provider credentials,
# no auth signing keys, no gateway secrets (VAL-VM-011).
#
# The vmmanager package (internal/vmmanager) launches Firecracker with
# the kernel, rootfs, and store disk from the microvm runner outputs.
# It does NOT use the microvm runner scripts directly because vmmanager
# needs per-VM networking, port assignment, and lifecycle control.
{ config, lib, pkgs, goChoirPackages, ... }:

{
  networking.hostName = "go-choir-sandbox";

  # ── microvm configuration ────────────────────────────────────────────
  microvm = {
    # Firecracker as the hypervisor. The actual hypervisor binary is not
    # used from the microvm runner — vmmanager launches firecracker directly.
    # But this tells microvm.nix to generate Firecracker-compatible artifacts.
    hypervisor = "firecracker";

    # Guest resources (overridden by vmmanager at launch time via
    # Firecracker config, but used for the build-time artifact generation).
    vcpu = 2;
    mem = 512;

    # No tap interfaces defined here — vmmanager creates per-VM tap
    # devices and networking at runtime. The guest uses DHCP or
    # kernel ip= parameter for network config.
    interfaces = [];

    # Mutable sandbox state on a virtio-blk volume (/dev/vdb).
    # vmmanager creates the actual data.img per-VM at runtime from
    # the VM state directory. This declaration tells microvm.nix to
    # include virtio-blk support in the guest kernel/initrd.
    volumes = [{
      image = "data.img";
      mountPoint = "/mnt/persistent";
      size = 2048;
    }];

    # Use upstream microvm.nix API for the nix store disk.
    # erofs provides a shared nix store that can be shared
    # across VMs with KSM deduplication on the host.
    storeOnDisk = true;
    storeDiskType = "erofs";

    # No virtiofs or 9p shares. With shares = [], microvm.nix
    # automatically generates an erofs disk for the nix store closure.
    # This is more efficient than virtiofs for our use case because:
    # - No virtiofsd daemon needed on the host
    # - erofs disk is a single shared file referenced by all VMs
    # - Combined with KSM (shared=off), identical pages are deduplicated
    shares = [];
  };

  # ── Guest services ───────────────────────────────────────────────────

  # Extract per-VM bootstrap settings into an env file before the sandbox
  # service starts. Runtime parameters come from kernel cmdline, while the
  # gateway token is read from the persistent data volume vmmanager owns.
  systemd.services.go-choir-extract-cmdline = {
    description = "Extract go-choir secrets from kernel cmdline";
    wantedBy = [ "multi-user.target" ];
    before = [ "go-choir-sandbox.service" ];
    serviceConfig = {
      Type = "oneshot";
      RemainAfterExit = true;
    };
    script = ''
      set -euo pipefail
      ENV_FILE="/run/go-choir-sandbox.env"
      : > "$ENV_FILE"

      # Parse kernel cmdline parameters from vmmanager.
      for param in $(cat /proc/cmdline); do
        case "$param" in
          guest_port=*)
            echo "SANDBOX_PORT=''${param#guest_port=}" >> "$ENV_FILE"
            ;;
          vm_id=*)
            echo "SANDBOX_ID=''${param#vm_id=}" >> "$ENV_FILE"
            ;;
          epoch=*)
            echo "VM_EPOCH=''${param#epoch=}" >> "$ENV_FILE"
            ;;
          choir.gateway_url=*)
            echo "RUNTIME_GATEWAY_URL=''${param#choir.gateway_url=}" >> "$ENV_FILE"
            ;;
        esac
      done

      if [ -f /mnt/persistent/gateway-token ]; then
        printf 'RUNTIME_GATEWAY_TOKEN=%s\n' "$(cat /mnt/persistent/gateway-token)" >> "$ENV_FILE"
      fi

      chmod 0640 "$ENV_FILE"
    '';
  };

  # Sandbox runtime service.
  # Runs the Go sandbox binary which listens for runtime API requests
  # inside the VM. Provider credentials are never in the guest (VAL-VM-011).
  # LLM calls route through the host-side gateway using the extracted token.
  systemd.services.go-choir-sandbox = {
    description = "go-choir Sandbox Runtime (VM guest)";
    wantedBy = [ "multi-user.target" ];
    after = [ "network-online.target" "go-choir-extract-cmdline.service" ];
    wants = [ "network-online.target" ];
    requires = [ "go-choir-extract-cmdline.service" ];
    environment = {
      # VM health checks and host forwarding reach the guest via its tap IP,
      # so the sandbox must listen on all guest interfaces, not loopback only.
      SERVER_HOST = "0.0.0.0";
      # Default port; overridden by guest_port= in kernel cmdline.
      SANDBOX_PORT = "8085";
      SANDBOX_ID = "sandbox-guest";
      # Persistent state directory on the virtio-blk data volume.
      RUNTIME_STORE_PATH = "/mnt/persistent/state";
    };
    serviceConfig = {
      ExecStart = "${goChoirPackages.sandbox}/bin/sandbox";
      Restart = "on-failure";
      RestartSec = 1;
      StandardOutput = "journal+console";
      StandardError = "journal+console";
      EnvironmentFile = [ "-/run/go-choir-sandbox.env" ];
    };
  };

  # Allow sandbox port through firewall
  networking.firewall.allowedTCPPorts = [ 8085 ];

  # ── Networking ───────────────────────────────────────────────────────
  # Use systemd-networkd for DHCP on virtio-net interfaces.
  # vmmanager creates the tap device and assigns IPs via the ip= kernel
  # parameter. The guest configures networking through systemd-networkd.
  networking.useDHCP = false;
  systemd.network = {
    enable = true;
    networks."10-vm" = {
      matchConfig.Driver = "virtio_net";
      networkConfig = {
        DHCP = "ipv4";
      };
      dhcpV4Config = {
        UseDNS = true;
        UseRoutes = true;
      };
    };
  };

  # ── System packages ──────────────────────────────────────────────────
  # Minimal set for debugging and runtime support.
  environment.systemPackages = with pkgs; [
    coreutils
    curl
    procps
    iproute2
    bash
  ];

  system.stateVersion = "25.11";
}
