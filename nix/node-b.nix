# NixOS host configuration for go-choir Node B (OVH bare metal)
# 147.135.70.196 — draft.choir-ip.com — us-east-vin
# Adapted from choiros-rs nix/hosts/ovh-node.nix and ovh-node-b.nix
{ config, lib, pkgs, goChoirPackages, ... }:
{
  # Boot
  boot.loader.efi.canTouchEfiVariables = true;
  boot.loader.efi.efiSysMountPoint = "/boot/efi";
  boot.loader.grub = {
    enable = true;
    efiSupport = true;
    devices = [ "nodev" ];
  };

  # Network
  networking.useDHCP = true;
  networking.hostName = "go-choir-b";

  # SSH access
  services.openssh = {
    enable = true;
    openFirewall = true;
    settings = {
      PermitRootLogin = "prohibit-password";
      PasswordAuthentication = false;
      KbdInteractiveAuthentication = false;
    };
  };

  # SSH authorized keys — copied EXACTLY from choiros-rs nix/hosts/ovh-node.nix
  users.users.root.openssh.authorizedKeys.keys = [
    "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAILN3IIn6TzBBExWiJTJ7aDlA/LlEMXvjFlSfkKkV02TZ wiz@choiros-ovh"
    "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAfYv0qn1XjuKuddQqmDEk/nS3NUP/6+1pG9/DRq4NUS github-actions-deploy@choiros"
  ];

  # Firewall — ports 22, 80, 443 ONLY. Service ports (8081-8084) NOT open externally.
  networking.firewall = {
    enable = true;
    allowedTCPPorts = [
      22    # SSH
      80    # HTTP
      443   # HTTPS
    ];
  };

  # Caddy reverse proxy (TLS termination → Go services + frontend)
  services.caddy = {
    enable = true;
    virtualHosts."draft.choir-ip.com" = {
      extraConfig = ''
        handle /auth/* {
          reverse_proxy 127.0.0.1:8081
        }
        handle /api/* {
          reverse_proxy 127.0.0.1:8082
        }
        handle /provider/* {
          reverse_proxy 127.0.0.1:8084
        }
        handle {
          root * ${goChoirPackages.frontend}
          file_server
        }
      '';
    };
  };

  # ── Systemd services ──────────────────────────────────────────────────
  # 4 host services: auth, proxy, vmctl, gateway
  # sandbox is NOT a host service — it runs inside Firecracker microVMs.

  systemd.services.go-choir-auth = {
    description = "go-choir Auth Service";
    wantedBy = [ "multi-user.target" ];
    after = [ "network-online.target" ];
    wants = [ "network-online.target" ];
    serviceConfig = {
      ExecStart = "${goChoirPackages.auth}/bin/auth";
      Restart = "on-failure";
      RestartSec = 3;
      Environment = [
        "AUTH_PORT=8081"
      ];
    };
  };

  systemd.services.go-choir-proxy = {
    description = "go-choir Proxy Service";
    wantedBy = [ "multi-user.target" ];
    after = [ "network-online.target" ];
    wants = [ "network-online.target" ];
    serviceConfig = {
      ExecStart = "${goChoirPackages.proxy}/bin/proxy";
      Restart = "on-failure";
      RestartSec = 3;
      Environment = [
        "PROXY_PORT=8082"
      ];
    };
  };

  systemd.services.go-choir-vmctl = {
    description = "go-choir VMCtl Service";
    wantedBy = [ "multi-user.target" ];
    after = [ "network-online.target" ];
    wants = [ "network-online.target" ];
    serviceConfig = {
      ExecStart = "${goChoirPackages.vmctl}/bin/vmctl";
      Restart = "on-failure";
      RestartSec = 3;
      Environment = [
        "VMCTL_PORT=8083"
      ];
    };
  };

  systemd.services.go-choir-gateway = {
    description = "go-choir Gateway Service";
    wantedBy = [ "multi-user.target" ];
    after = [ "network-online.target" ];
    wants = [ "network-online.target" ];
    serviceConfig = {
      ExecStart = "${goChoirPackages.gateway}/bin/gateway";
      Restart = "on-failure";
      RestartSec = 3;
      Environment = [
        "GATEWAY_PORT=8084"
      ];
    };
  };

  # Workspace directory (for CI git pull deploys)
  systemd.tmpfiles.rules = [
    "d /opt/go-choir 0755 root root -"
  ];

  # Nix settings
  nix.settings = {
    experimental-features = [ "nix-command" "flakes" ];
    auto-optimise-store = true;
  };

  # System packages
  environment.systemPackages = with pkgs; [
    bash
    btrfs-progs
    coreutils
    curl
    git
    gnugrep
    gnused
    htop
    jq
    procps
    ripgrep
    vim
  ];

  # Timezone
  time.timeZone = "UTC";

  system.stateVersion = "25.11";
}
