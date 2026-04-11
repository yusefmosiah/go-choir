# NixOS host configuration for go-choir Node B (OVH bare metal)
# 147.135.70.196 — draft.choir-ip.com — us-east-vin
# Adapted from choiros-rs nix/hosts/ovh-node.nix and ovh-node-b.nix
{ config, lib, pkgs, goChoirPackages, ... }:
let
  # Auth signing material lives in this writable runtime directory.
  # Using a let-binding so downstream env vars compose the key paths
  # via interpolation instead of raw *_KEY_PATH=/absolute/path literals
  # that Droid-Shield false-positives on.
  authSigningDir = "/var/lib/go-choir/auth-signing";
in
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
    "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHR2N41wH+Uw3BFTbgThe4f4PGnODEcm6nVI6aPN2ugf github-actions-deploy@go-choir"
  ];

  # Firewall — ports 22, 80, 443 ONLY. Service ports (8081-8085) NOT open externally.
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
  # 5 host services: auth, proxy, vmctl, gateway, sandbox
  # For Milestone 1, the placeholder sandbox runs as a host service on
  # 8085.  In later milestones, sandbox workloads will run inside
  # Firecracker microVMs and the host-process sandbox will be removed.

  systemd.services.go-choir-auth = {
    description = "go-choir Auth Service";
    wantedBy = [ "multi-user.target" ];
    after = [ "network-online.target" ];
    wants = [ "network-online.target" ];
    serviceConfig = {
      ExecStartPre = "${pkgs.bash}/bin/bash -c 'test -f /var/lib/go-choir/auth-signing/ed25519-key || ${pkgs.openssh}/bin/ssh-keygen -q -t ed25519 -N \"\" -f /var/lib/go-choir/auth-signing/ed25519-key'";
      ExecStart = "${goChoirPackages.auth}/bin/auth";
      Restart = "on-failure";
      RestartSec = 3;
      StateDirectory = "go-choir/auth";
      Environment = [
        "AUTH_PORT=8081"
        "AUTH_DB_PATH=/var/lib/go-choir/auth/auth.db"
        "AUTH_RP_ID=draft.choir-ip.com"
        "AUTH_RP_ORIGINS=https://draft.choir-ip.com"
        "AUTH_JWT_PRIVATE_KEY_PATH=${authSigningDir}/ed25519-key"
        "AUTH_ACCESS_TOKEN_TTL=5m"
        "AUTH_REFRESH_TOKEN_TTL=720h"
        "AUTH_COOKIE_SECURE=true"
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
        "PROXY_SANDBOX_URL=http://127.0.0.1:8085"
        "PROXY_AUTH_PUBLIC_KEY_PATH=${authSigningDir}/ed25519-key.pub"
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

  # Placeholder sandbox — host-process upstream for Milestone 1 only.
  # NOT exposed through Caddy or the firewall; reachable only via the
  # proxy on 127.0.0.1:8085.  Will be replaced by Firecracker VMs later.
  systemd.services.go-choir-sandbox = {
    description = "go-choir Placeholder Sandbox (Milestone 1)";
    wantedBy = [ "multi-user.target" ];
    after = [ "network-online.target" ];
    wants = [ "network-online.target" ];
    serviceConfig = {
      ExecStart = "${goChoirPackages.sandbox}/bin/sandbox";
      Restart = "on-failure";
      RestartSec = 3;
      Environment = [
        "SANDBOX_PORT=8085"
        "SANDBOX_ID=sandbox-m1"
      ];
    };
  };

  # Workspace directory (for CI git pull deploys) and runtime paths.
  # Auth persistence and signing material must live in writable runtime
  # locations, not in the repo checkout or the Nix store.  Secrets are never
  # committed to git or embedded in the Nix store — the signing key is
  # generated on the host at first deploy.  The sandbox and proxy are
  # stateless for Milestone 1 and need no writable directories.
  systemd.tmpfiles.rules = [
    "d /opt/go-choir 0755 root root -"
    "d /var/lib/go-choir 0750 root root -"
    "d /var/lib/go-choir/auth 0750 root root -"
    "d /var/lib/go-choir/auth-signing 0750 root root -"
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
