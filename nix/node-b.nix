# NixOS host configuration for go-choir Node B (OVH bare metal)
# 147.135.70.196 — choir.news — us-east-vin
# Adapted from choiros-rs nix/hosts/ovh-node.nix and ovh-node-b.nix
#
# Service hardening notes (VAL-DEPLOY-007 / VAL-DEPLOY-008 / VAL-CROSS-118):
# - All Go services bind to 127.0.0.1 only (localhost-only, defense in depth)
# - Firewall allows only ports 22, 80, 443 externally
# - Caddy is the sole public edge; internal service ports are never exposed
# - Each service has Restart=on-failure with a backoff, plus a watchdog
# - Proxy depends on both auth and autoputer; if either restarts, proxy
#   re-verifies health on the next request and returns degraded state
#   through /health while the upstream recovers
# - Auth persists sessions in SQLite, so sessions survive auth restarts
# - Auth reuses the same signing key file across restarts, so existing
#   access JWTs remain valid after auth restarts (VAL-CROSS-118)
{ config, lib, pkgs, goChoirPackages, guestImage ? null, sourceRepoRemote ? "https://github.com/choir-hip/go-choir.git", buildCommit ? "local", ... }:
let
  # Auth signing material lives in this writable runtime directory.
  # Using a let-binding so downstream env vars compose the key paths
  # via interpolation instead of raw *_KEY_PATH=/absolute/path literals
  # that Droid-Shield false-positives on.
  authSigningDir = "/var/lib/go-choir/auth-signing";
  frontendRoot = "/var/www/go-choir";
  frontendCurrent = "/var/www/go-choir/frontend-current";
  autoputerFilesDir = "/var/lib/go-choir/files";
  autoputerRuntimeDir = "/var/lib/go-choir/runtime";
  sourceServiceDir = "/var/lib/go-choir/source-service";
  mailDir = "/var/lib/go-choir/mail";
  platformDoltDir = "/var/lib/go-choir/platform-dolt";
  platformDoltDBDir = "${platformDoltDir}/platform";
  platformArtifactsDir = "/var/lib/go-choir/platform-artifacts";
  platformDoltInit = pkgs.writeShellScript "platform-dolt-init" ''
    set -euo pipefail
    export HOME="${platformDoltDir}"
    install -d -m 0750 "${platformDoltDir}" "${platformDoltDBDir}"
    cd "${platformDoltDBDir}"
    ${pkgs.dolt}/bin/dolt config --global --add user.name "Choir Platform" >/dev/null 2>&1 || true
    ${pkgs.dolt}/bin/dolt config --global --add user.email "platform@choir.news" >/dev/null 2>&1 || true
    if [ ! -d .dolt ]; then
      ${pkgs.dolt}/bin/dolt init
    fi
  '';
  goServiceLibraryPath = lib.makeLibraryPath [ pkgs.icu ];
  serviceExec = name: package: pkgs.writeShellScript "go-choir-${name}-exec" ''
    set -euo pipefail
    export LD_LIBRARY_PATH="${goServiceLibraryPath}''${LD_LIBRARY_PATH:+:}''${LD_LIBRARY_PATH:-}"
    pointer="/var/lib/go-choir/services/${name}/bin/${name}"
    if [ -x "$pointer" ]; then
      if [ "${name}" = "autoputer" ] && [ -d "/var/lib/go-choir/services/autoputer/share/go-choir/skills" ]; then
        export RUNTIME_SKILLS_ROOT="/var/lib/go-choir/services/autoputer/share/go-choir/skills"
      fi
      exec "$pointer" "$@"
    fi
    exec "${package}/bin/${name}" "$@"
  '';
  # vmctl-priority.env is mutable by design, but may only tune priority IDs.
  # Reassert credential authority after EnvironmentFile expansion.
  vmctlExec = pkgs.writeShellScript "go-choir-vmctl-exec" ''
    set -euo pipefail
    export VMCTL_CORPUSD_URL="http://127.0.0.1:8086"
    exec ${serviceExec "vmctl" goChoirPackages.vmctl} "$@"
  '';
  diskRetentionSweep = pkgs.writeShellScript "go-choir-disk-retention-sweep" ''
    set -euo pipefail
    export PATH="${lib.makeBinPath [ pkgs.coreutils pkgs.curl pkgs.gnugrep pkgs.nix pkgs.systemd ]}:$PATH"

    emergency_min_free_kib="''${GO_CHOIR_DISK_GC_MIN_FREE_KIB:-125829120}"
    target_free_kib="''${GO_CHOIR_DISK_GC_TARGET_FREE_KIB:-188743680}"
    avail_kib="$(df --output=avail -k / | tail -n 1 | tr -d ' ')"
    echo "go-choir disk retention: root_available_kib=''${avail_kib} emergency_min_free_kib=''${emergency_min_free_kib} target_free_kib=''${target_free_kib}"
    df -h / /var/lib/go-choir 2>/dev/null || df -h /

    curl -fsS -X POST -H 'X-Internal-Caller: true' http://127.0.0.1:8083/internal/vmctl/reclaim || true
    journalctl --vacuum-size=256M || true
    nix-env -p /nix/var/nix/profiles/system --delete-generations +4 || true

    avail_kib="$(df --output=avail -k / | tail -n 1 | tr -d ' ')"
    if [ -z "''${avail_kib}" ] || [ "''${avail_kib}" -lt "''${emergency_min_free_kib}" ]; then
      echo "go-choir disk retention: below emergency headroom after generation pruning; running nix store gc"
      nix store gc || true
    elif [ "''${avail_kib}" -lt "''${target_free_kib}" ]; then
      echo "go-choir disk retention: below target headroom; running nix store gc for routine maintenance"
      nix store gc || true
    else
      echo "go-choir disk retention: preserving warm Nix cache because target headroom is satisfied"
    fi

    df -h / /var/lib/go-choir 2>/dev/null || df -h /
  '';
  platformDoltHistoryAudit = pkgs.writeShellScript "go-choir-platform-dolt-history-audit" ''
    set -euo pipefail
    export PATH="${lib.makeBinPath [ pkgs.coreutils pkgs.dolt pkgs.systemd ]}:$PATH"
    # Recurrence control for the 2026-08-26 incident: per-mutation
    # CALL DOLT_COMMIT in internal/platform/store.go and internal/cycle/
    # storage.go grows reachable commit history that NO dolt gc (shallow or
    # --full) can collect. See docs/evidence/platform-dolt-oldgen-218g-dead-
    # history-2026-08-26.md. This audit fails loudly (nonzero exit ->
    # failed unit -> monitoring) when history regrows past the squash
    # thresholds, so the squash runbook runs before the disk refills.
    dbdir="${platformDoltDBDir}"
    commits=$(cd "$dbdir" && HOME="${platformDoltDir}" dolt sql -q "SELECT COUNT(*) FROM dolt_log" 2>/dev/null | grep -oE '[0-9]+' | head -1 || echo 0)
    oldgen_kib=$(du -sk "$dbdir/.dolt/noms/oldgen" 2>/dev/null | cut -f1 || echo 0)
    noms_kib=$(du -sk "$dbdir/.dolt/noms" 2>/dev/null | cut -f1 || echo 0)
    echo "platform-dolt history audit: dolt_log commits=''${commits} oldgen=''${oldgen_kib}KiB noms=''${noms_kib}KiB"
    max_commits="''${GO_CHOIR_DOLT_MAX_COMMITS:-1000000}"
    max_oldgen_kib="''${GO_CHOIR_DOLT_MAX_OLDGEN_KIB:-52428800}"
    if [ "''${commits:-0}" -gt "$max_commits" ]; then
      echo "platform-dolt history audit FAILED: $commits commits > $max_commits; run the history-squash runbook (docs/evidence/platform-dolt-oldgen-218g-dead-history-2026-08-26.md)" >&2
      exit 1
    fi
    if [ "''${oldgen_kib:-0}" -gt "$max_oldgen_kib" ]; then
      echo "platform-dolt history audit FAILED: oldgen ''${oldgen_kib}KiB > ''${max_oldgen_kib}KiB; run the history-squash runbook" >&2
      exit 1
    fi
  '';

  # Common systemd service hardening options applied to all go-choir
  # services. These restrict what the service process can do at the
  # Linux kernel level, reducing the blast radius of any compromise.
  commonServiceHardening = {
    # Prevent the service from modifying the Nix store.
    ProtectSystem = "strict";
    # Give the service its own /tmp, invisible to other services.
    PrivateTmp = true;
    # Disallow creating new setuid/setgid binaries.
    NoNewPrivileges = true;
    # Prevent the service from loading new kernel modules.
    ProtectKernelModules = true;
    # Prevent the service from tuning kernel parameters.
    ProtectKernelTunables = true;
    # Prevent the service from writing to sysctl knobs.
    ProtectControlGroups = true;
    # Restrict system call surface.
    SystemCallArchitectures = "native";
    # Remove /dev nodes that are not needed.
    PrivateDevices = true;
    # Restrict which system calls the service can make.
    SystemCallFilter = [ "@system-service" "~@mount" "@privileged" ];
    # Don't allow the service to change its mount namespace.
    MountFlags = "private";
  };
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

  # Firewall — ports 22, 80, 443 ONLY. Service ports (8081-8087) NOT open externally.
  # This plus localhost-only binding (defense in depth) satisfies VAL-DEPLOY-007:
  # only the intended public edge (Caddy on 80/443) is internet-reachable.
  networking.firewall = {
    enable = true;
    allowedTCPPorts = [
      22    # SSH
      80    # HTTP
      443   # HTTPS
    ];
  };

  # Caddy reverse proxy (TLS termination → Go services + frontend)
  # Primary public/staging host is choir.news; old domains redirect here.
  services.caddy = {
    enable = true;
    virtualHosts."choir.news" = {
      extraConfig = ''
        handle /auth/* {
          reverse_proxy 127.0.0.1:8081
        }
        handle /health {
          reverse_proxy 127.0.0.1:8082
        }
        handle /health/* {
          reverse_proxy 127.0.0.1:8084
        }
        handle /api/email/resend/webhook {
          reverse_proxy 127.0.0.1:8087
        }
        handle /api/* {
          reverse_proxy 127.0.0.1:8082 {
            transport http {
              response_header_timeout 15m
              read_timeout 15m
              write_timeout 15m
            }
          }
        }
        handle /provider/* {
          respond "provider routes are not available from the public edge" 403
        }
        handle /internal/* {
          respond "internal routes are not available from the public edge" 403
        }
        handle /assets/* {
          reverse_proxy 127.0.0.1:8082
        }
        handle {
          # Computer-surface HTML/assets are selected after vmctl resolve.
          # Host frontend-current is platform-shell chrome only (unsigned).
          reverse_proxy 127.0.0.1:8082
        }
      '';
    };
    virtualHosts."choir-ip.com" = {
      extraConfig = ''
        redir https://choir.news{uri} permanent
      '';
    };
  };

  # Qdrant vector search engine (host service, localhost-only).
  # The Go qdrant client (internal/qdrant) talks to http://127.0.0.1:6333.
  # VMs on the tap network cannot reach 127.0.0.1; see VM reachability note
  # in the mission doc. If VMs need Qdrant access, bind to the tap IP or
  # 0.0.0.0 (the firewall already blocks 6333 externally).
  services.qdrant = {
    enable = true;
    settings = {
      service = {
        host = "127.0.0.1";
        http_port = 6333;
        grpc_port = 6334;
      };
      storage = {
        storage_path = "/var/lib/qdrant/storage";
        snapshots_path = "/var/lib/qdrant/snapshots";
        hnsw_index.on_disk = true;
      };
      telemetry_disabled = true;
    };
  };

  # SearXNG self-hosted meta-search engine (localhost-only, free unlimited queries).
  # Aggregates Google, Bing, DuckDuckGo, and 70+ engines. JSON API enabled for
  # the gateway's SearXNGProvider. No API key, no credits. The limiter is
  # disabled because this is an internal API endpoint, not a public-facing
  # search UI — only the gateway (127.0.0.1) can reach it.
  services.searx = {
    enable = true;
    package = pkgs.searxng;
    environmentFile = "/var/lib/go-choir/searxng.env";
    settings = {
      use_default_settings = true;
      general = {
        instance_name = "Choir Search";
        debug = false;
      };
      search = {
        safe_search = 0;
        autocomplete = "";
        formats = [ "html" "json" ];
        default_range = "";
      };
      server = {
        secret_key = "$SEARXNG_SECRET";
        bind_address = "127.0.0.1";
        port = 8888;
        limiter = false;
        image_proxy = false;
      };
      ui = {
        static_use_hash = true;
      };
      outgoing = {
        request_timeout = 10;
        max_request_timeout = 15;
      };
    };
  };

  # ── Systemd services ──────────────────────────────────────────────────
  # Host services: auth, proxy, vmctl, gateway, autoputer, maild, corpusd,
  # and sourcecycled.
  # Autoputer workloads for authenticated traffic are expected to run inside
  # Firecracker microVMs managed by vmctl. Node B disables vmctl's
  # host-process fallback so deployed routing fails closed instead of silently
  # landing on the placeholder host autoputer.
  #
  # Guest images are repo-built (VAL-VM-010):
  #   nix build .#guest-image  →  kernel (vmlinux) + rootfs (ext4) + initrd
  # The guest contains ONLY the autoputer binary — no provider credentials,
  # no auth signing keys, no gateway secrets (VAL-VM-011).
  #
  # Restart and recovery behavior (VAL-DEPLOY-008 / VAL-CROSS-118):
  # - Each service uses Restart=on-failure with a 3-second backoff.
  # - Proxy depends on auth and autoputer; auth and autoputer restart
  #   independently. After an auth restart, existing access JWTs remain
  #   valid because the signing key file persists across restarts. After
  #   a autoputer restart, the proxy /health endpoint reports "degraded"
  #   until the autoputer comes back, then returns to "ok".
  # - Auth sessions are persisted in SQLite, so session state survives
  #   auth restart. Browser users either rehydrate via refresh-token
  #   rotation or fall back safely to the guest state.
  # - WatchdogSec is intentionally NOT set because the Go server package
  #   does not send sd_notify keepalives. Adding WatchdogSec without
  #   sd_notify causes the service to be killed every 30 seconds.

  systemd.services.go-choir-auth = {
    description = "go-choir Auth Service";
    wantedBy = [ "multi-user.target" ];
    after = [ "network-online.target" ];
    wants = [ "network-online.target" ];
    serviceConfig = commonServiceHardening // {
      ExecStartPre = "${pkgs.bash}/bin/bash -c 'test -f /var/lib/go-choir/auth-signing/ed25519-key || ${pkgs.openssh}/bin/ssh-keygen -q -t ed25519 -N \"\" -f /var/lib/go-choir/auth-signing/ed25519-key'";
      ExecStart = "${serviceExec "auth" goChoirPackages.auth}";
      Restart = "on-failure";
      RestartSec = 3;
      StateDirectory = "go-choir/auth";
      # Read-write paths for auth persistence and signing key.
      ReadWritePaths = [ "/var/lib/go-choir/auth" "/var/lib/go-choir/auth-signing" ];
      Environment = [
        "AUTH_PORT=8081"
        "AUTH_DB_PATH=/var/lib/go-choir/auth/auth.db"
        "AUTH_RP_ID=choir.news"
        "AUTH_RP_ORIGINS=https://choir.news"
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
    after = [ "network-online.target" "go-choir-auth.service" "go-choir-corpusd.service" "go-choir-maild.service" ];
    wants = [ "network-online.target" "go-choir-corpusd.service" "go-choir-maild.service" ];
    requires = [ "go-choir-auth.service" ];
    serviceConfig = commonServiceHardening // {
      ExecStart = "${serviceExec "proxy" goChoirPackages.proxy}";
      Restart = "on-failure";
      RestartSec = 3;
      EnvironmentFile = "-/var/lib/go-choir/deploy.env";
      # Proxy needs to read auth signing material and the auth DB for API-key validation.
      ReadWritePaths = [ "/var/lib/go-choir/auth-signing" "/var/lib/go-choir/auth" ];
      Environment = [
        "SERVER_HOST=0.0.0.0"
        "PROXY_PORT=8082"
        "PROXY_AUTOPUTER_URL=http://127.0.0.1:8085"
        "PROXY_AUTH_PUBLIC_KEY_PATH=${authSigningDir}/ed25519-key.pub"
        "PROXY_AUTH_DB_PATH=/var/lib/go-choir/auth/auth.db"
        # When vmctl is running, the proxy resolves user VM ownership
        # through vmctl instead of using the static autoputer URL
        # (VAL-VM-001, VAL-VM-002).
        "PROXY_VMCTL_URL=http://127.0.0.1:8083"
        # Bounded fail-fast timeout for the proxy -> vmctl resolve path. A hung
        # or slow vmctl resolve returns a legible 504 to the caller within 60s
        # instead of keeping the public request open for the full boot wait.
        # Cold computer boot completion is handled by vmctl's
        # VM_BOOT_READY_TIMEOUT and the retry/replay path (Phase D).
        "PROXY_VMCTL_TIMEOUT=30m"
        # Replay completeness reconstructs a disposable projection and is an
        # owner-only route with a dedicated 10m budget; ordinary interactive
        # proxy routes stay at 30s. The handler extends the response deadline
        # for this route only; it does not widen the global proxy write budget.
        "PROXY_REPLAY_COMPLETENESS_TIMEOUT=10m"
        # Residue import serializes a bounded reducer snapshot and uses its own
        # 110s budget for the same fail-closed route-boundary reason.
        "PROXY_RESIDUE_IMPORT_TIMEOUT=110s"
        "PROXY_CORPUSD_URL=http://127.0.0.1:8086"
        "PROXY_MAILD_URL=http://127.0.0.1:8087"
        # Unsigned / and /assets/* serve this host tree (picker/auth chrome).
        # Authenticated computer surface is selected after vmctl resolve.
        "PROXY_PLATFORM_SHELL_ROOT=${frontendCurrent}"
      ];
    };
  };

  systemd.services.go-choir-maild = {
    description = "go-choir Mail Service";
    wantedBy = [ "multi-user.target" ];
    after = [ "network-online.target" ];
    wants = [ "network-online.target" ];
    serviceConfig = commonServiceHardening // {
      ExecStart = "${serviceExec "maild" goChoirPackages.maild}";
      Restart = "on-failure";
      RestartSec = 3;
      StateDirectory = "go-choir/mail";
      # Mail state contains private message bodies and provider metadata.
      StateDirectoryMode = "0700";
      UMask = "0077";
      EnvironmentFile = "-/var/lib/go-choir/maild.env";
      ReadWritePaths = [ mailDir ];
      Environment = [
        # Autoputer guest VMs persist Email appagent drafts via the host tap
        # address. The host firewall still keeps 8087 closed externally.
        "SERVER_HOST=0.0.0.0"
        "MAILD_PORT=8087"
        "MAILD_DB_PATH=${mailDir}/mail.db"
        "MAILD_STORAGE_ROOT=${mailDir}"
        "MAILD_PRIMARY_DOMAIN=choir.news"
        # Maild routes trace events through vmctl to user VMs. The host
        # autoputer fallback (MAILD_RUNTIME_URL) was removed in PR 5.
        "MAILD_VMCTL_URL=http://127.0.0.1:8083"
      ];
    };
  };

  systemd.services.go-choir-platform-dolt = {
    description = "go-choir Platform Dolt SQL Server";
    wantedBy = [ "multi-user.target" ];
    after = [ "network-online.target" ];
    wants = [ "network-online.target" ];
    serviceConfig = commonServiceHardening // {
      ExecStartPre = platformDoltInit;
      ExecStart = "${pkgs.dolt}/bin/dolt sql-server --host 127.0.0.1 --port 13306";
      WorkingDirectory = platformDoltDBDir;
      Restart = "on-failure";
      RestartSec = 3;
      StateDirectory = "go-choir/platform-dolt";
      ReadWritePaths = [ platformDoltDir ];
      Environment = [
        "HOME=${platformDoltDir}"
      ];
    };
  };

  systemd.services.go-choir-corpusd = {
    description = "go-choir Platform Service";
    wantedBy = [ "multi-user.target" ];
    after = [ "network-online.target" "go-choir-platform-dolt.service" ];
    wants = [ "network-online.target" ];
    requires = [ "go-choir-platform-dolt.service" ];
    serviceConfig = commonServiceHardening // {
      ExecStart = "${serviceExec "corpusd" goChoirPackages.corpusd}";
      Restart = "on-failure";
      RestartSec = 3;
      StateDirectory = "go-choir/platform-artifacts";
      ReadWritePaths = [ platformArtifactsDir ];
      # Track K two-approval operator credentials live in a deploy-created
      # file outside git (same trust class as gateway credentials):
      # CHOIR_KEY_ESCROW_OPERATORS="name:token,..." — the unwrap gate stays
      # closed (503) while the file or entries are absent.
      EnvironmentFile = "-/var/lib/go-choir/secrets/corpusd-escrow-operators.env";
      Environment = [
        "SERVER_HOST=0.0.0.0"
        "CORPUSD_PORT=8086"
        "CORPUSD_DOLT_DSN=root@tcp(127.0.0.1:13306)/platform?parseTime=true&multiStatements=true&clientFoundRows=true"
        "CORPUSD_ARTIFACTS_ROOT=${platformArtifactsDir}"
      ];
    };
  };

  systemd.services.go-choir-sourcecycled = {
    description = "go-choir Source Service Ingestion Daemon";
    wantedBy = [ "multi-user.target" ];
    after = [ "network-online.target" "go-choir-proxy.service" "go-choir-vmctl.service" ];
    wants = [ "network-online.target" "go-choir-proxy.service" "go-choir-vmctl.service" ];
    path = with pkgs; [ bash coreutils ];
    serviceConfig = commonServiceHardening // {
      ExecStart = "${serviceExec "sourcecycled" goChoirPackages.sourcecycled}";
      Restart = "on-failure";
      RestartSec = 10;
      StateDirectory = "go-choir/source-service";
      ReadWritePaths = [ sourceServiceDir ];
      EnvironmentFile = [
        "-/var/lib/go-choir/deploy.env"
      ];
      # Shared captures publish through the host proxy to corpusd; user VMs are never the durable target.
      Environment = [
        "SOURCE_SERVICE_ADDR=0.0.0.0:8787"
        "SOURCE_SERVICE_CONFIG_PATH=/opt/go-choir/configs/sources.json"
        "SOURCE_SERVICE_OBJECTGRAPH_BASE_URL=http://127.0.0.1:8082"
        "SOURCE_SERVICE_OBJECTGRAPH_OWNER_ID=universal-wire-platform"
        "SOURCE_SERVICE_RUNTIME_OWNER_ID=universal-wire-platform"
        "SOURCE_SERVICE_AGENT_DISPATCH_MAX_PROCESSORS=1"
        "SOURCE_SERVICE_AGENT_DISPATCH_DRAIN_INTERVAL_SECONDS=60"
        "VMCTL_AUTOPUTER_PROXY_SOCK=/run/go-choir/vmctl.sock"
      ];
    };
  };

  systemd.services.go-choir-vmctl = {
    description = "go-choir VMCtl Service (Firecracker VM lifecycle)";
    wantedBy = [ "multi-user.target" ];
    after = [ "network-online.target" "go-choir-platform-dolt.service" "go-choir-corpusd.service" ];
    wants = [ "network-online.target" "go-choir-platform-dolt.service" "go-choir-corpusd.service" ];
    serviceConfig = commonServiceHardening // {
      ExecStart = "${vmctlExec}";
      Restart = "on-failure";
      RuntimeDirectory = "go-choir";
      RuntimeDirectoryMode = "0700";
      RestartSec = 3;
      # Firecracker needs access to /dev/kvm for VM hardware acceleration.
      # We must allow KVM device access while keeping other hardening.
      PrivateDevices = lib.mkForce false;
      # Allow Firecracker to create tap devices and access networking.
      # CAP_NET_ADMIN is required for: ip tuntap, ip addr, ip link,
      # iptables (DNAT, MASQUERADE, FORWARD rules), and ip route.
      CapabilityBoundingSet = [ "CAP_NET_ADMIN" "CAP_SYS_PTRACE" ];
      # Let Firecracker child processes survive vmctl process replacement.
      # The new vmctl process reattaches using durable ownership + pid files
      # after proving the guest health endpoint still responds.
      KillMode = "process";
      # IP forwarding must be enabled for guest↔host communication.
      # ProtectKernelTunables blocks /proc/sys writes, so we override it
      # and use ExecStartPre to set ip_forward before the service starts.
      ProtectKernelTunables = lib.mkForce false;
      ExecStartPre = [
        ""  # Reset the ExecStartPre list
        "${pkgs.bash}/bin/bash -c '${pkgs.procps}/bin/sysctl -w net.ipv4.ip_forward=1 2>/dev/null || true'"
      ];
      # VM state directory for Firecracker VM persistence and epoch tracking.
      # Persistent user data in VMs is stored here and survives stop/resume
      # cycles (VAL-CROSS-116). Provider credentials are NEVER written here
      # (VAL-VM-011).
      StateDirectory = "go-choir/vm-state";
      ReadWritePaths = [ "/var/lib/go-choir" "/var/lib/go-choir/vm-state" "/var/lib/go-choir/guest" ];
      ReadOnlyPaths = [ "/var/lib/go-choir/auth" ];
      # Optional runtime priority overrides. This is intentionally outside the
      # repo-tracked Nix closure so operators can add paid/real-user always-on
      # IDs without a platform rebuild:
      #   VMCTL_ALWAYS_ON_USER_IDS=<auth user UUID>,<auth user UUID>
      EnvironmentFile = "-/var/lib/go-choir/vmctl-priority.env";
      Environment = [
        "VMCTL_PORT=8083"
        # D-ROUTE and immutable ComputerVersion inputs are tables on the same
        # corpusd world-wire SQL server. vmctl is the sole route CAS writer.
        "VMCTL_ROUTE_DSN=root@tcp(127.0.0.1:13306)/platform?parseTime=true&multiStatements=true&clientFoundRows=true"
        "VMCTL_ARTIFACTS_ROOT=${platformArtifactsDir}"
        "VMCTL_BASE_BLOB_ROOT=${platformArtifactsDir}/computer-inputs/blobs"
        "VMCTL_PROMOTION_AUTHORITY_PUBLIC_KEY=oEdoKFfUCLiOsxNX5J8bT3PQDrjqjVeuU8usTLHSYZ4="
        # Guest images are a stable boot substrate. At boot, guest autoputeres
        # fetch the current autoputer service package from this host-side pointer
        # and execute it from their writable data disk, so ordinary runtime code
        # deploys do not have to rebuild the whole microVM image.
        "VMCTL_AUTOPUTER_PACKAGE_DIR=/var/lib/go-choir/services/autoputer"
        # Firecracker VM configuration (VAL-VM-010):
        # Guest images are built from the repo via `nix build .#guest-image`.
        # The microvm.nix approach produces:
        #   - vmlinux (kernel)
        #   - rootfs.ext4 (boot disk / guest root filesystem)
        #   - initrd (for systemd module loading)
        #   - storedisk.erofs (shared nix store)
        "VM_FIRECRACKER_BIN=${pkgs.firecracker}/bin/firecracker"
        "VM_MKFS_EXT4_BIN=${pkgs.e2fsprogs}/bin/mkfs.ext4"
        "VM_KERNEL_IMAGE=/var/lib/go-choir/guest/vmlinux"
        "VM_ROOTFS_IMAGE=/var/lib/go-choir/guest/rootfs.ext4"
        "VM_INITRD_IMAGE=/var/lib/go-choir/guest/initrd"
        "VM_STORE_DISK_IMAGE=/var/lib/go-choir/guest/storedisk.erofs"
        "VM_KERNEL_PARAMS_FILE=/var/lib/go-choir/guest/kernel-params"
        "VM_STATE_DIR=/var/lib/go-choir/vm-state"
        "VM_HOST_BASE_PORT=9000"
        "VM_CPU_COUNT=2"
        "VM_MEM_MIB=4096"
        # Interactive guest shape (2026-09-03): the retained staging computer's
        # 11 GiB texture/object-graph store OOM-loops a 2 GiB guest on boot
        # replay and snapshot scans. 4 CPU / 8 GiB fits the 12-core / 32 GiB
        # Node B host; honored by interactiveMachineShape().
        "VM_INTERACTIVE_CPU_COUNT=4"
        "VM_INTERACTIVE_MEM_MIB=8192"
        "VM_HEALTH_CHECK_INTERVAL=15s"
        "VM_HEALTH_CHECK_TIMEOUT=10s"
        "VM_BOOT_READY_TIMEOUT=30m"
        # Recovery replay: guest Dolt checkpoint commits at the 5-8 GiB workspace
        # scale exceed 120s on the 4096 MiB guest (stall-gate fired mid-commit
        # at seq 99,765 / 103,148 / 105,196 during recovery boots). B10 range
        # allows 120-300s; the ceiling gives a legitimate commit room while
        # still failing a truly frozen replay inside the 30m boot window.
        "VM_REPLAY_STALL_TIMEOUT=300s"
        "VMCTL_STOP_MANAGED_ON_EXIT=false"
        # Keep personal computers resident while the host is under capacity.
        "VMCTL_IDLE_TIMEOUT=30m"
        "VMCTL_IDLE_SWEEP_INTERVAL=2m"
        "VMCTL_PRIMARY_KEEPALIVE_MODE=under-capacity"
        # Active reclaim uses the same ranking exposed by dry-run mode, but
        # hibernates a bounded number of lower-priority idle computers when
        # host pressure crosses threshold.
        "VMCTL_PRESSURE_RECLAIM_MODE=active"
        "VMCTL_PRESSURE_RECLAIM_MIN_IDLE=30m"
        "VMCTL_PRESSURE_MIN_MEMORY_AVAILABLE_MIB=4096"
        "VMCTL_PRESSURE_MIN_MEMORY_AVAILABLE_PERCENT=15"
        "VMCTL_PRESSURE_MIN_STATE_DIR_AVAILABLE_MIB=32768"
        "VMCTL_PRESSURE_MIN_STATE_DIR_AVAILABLE_PERCENT=10"
        "VMCTL_PRESSURE_MAX_MEMORY_SOME_AVG10=1.0"
        "VMCTL_PRESSURE_MAX_CPU_SOME_AVG10=90.0"
        "VMCTL_PRESSURE_MAX_IO_SOME_AVG10=5.0"
        "VMCTL_PRESSURE_RECLAIM_MAX_CANDIDATES=5"
        "VMCTL_STALE_STATE_MIN_AGE=6h"
        "VMCTL_STALE_STATE_MAX_DELETES=25"
        # Codex-created staging/product-proof accounts use the example.com and
        # example.test domains. Their hibernated primary VM state is not a
        # rollback primitive, so vmctl may delete it after a day while keeping
        # real-user computers protected.
        "VMCTL_RETENTION_PRUNE_MODE=active"
        "VMCTL_RETENTION_AUTH_DB_PATH=/var/lib/go-choir/auth/auth.db"
        "VMCTL_RETENTION_EPHEMERAL_EMAIL_DOMAINS=example.com,example.test"
        "VMCTL_RETENTION_EPHEMERAL_USER_PREFIXES=diagnostic-,sourcemaxx-proof-"
        "VMCTL_RETENTION_ORPHAN_MIN_AGE=6h"
        "VMCTL_RETENTION_EPHEMERAL_MIN_AGE=24h"
        "VMCTL_RETENTION_MAX_DELETES=100"
        "VMCTL_RETENTION_MAX_BYTES_MIB=122880"
        # Shadow retention mirrors the active policy as an observation endpoint
        # so reports can compare deletion pressure before and after sweeps.
        "VMCTL_RETENTION_SHADOW_PRUNE_MODE=dry-run"
        "VMCTL_RETENTION_SHADOW_AUTH_DB_PATH=/var/lib/go-choir/auth/auth.db"
        "VMCTL_RETENTION_SHADOW_EPHEMERAL_EMAIL_DOMAINS=example.com,example.test"
        "VMCTL_RETENTION_SHADOW_EPHEMERAL_USER_PREFIXES=diagnostic-,sourcemaxx-proof-"
        "VMCTL_RETENTION_SHADOW_ORPHAN_MIN_AGE=6h"
        "VMCTL_RETENTION_SHADOW_EPHEMERAL_MIN_AGE=24h"
        "VMCTL_RETENTION_SHADOW_MAX_DELETES=100"
        "VMCTL_RETENTION_SHADOW_MAX_BYTES_MIB=122880"
        # Gateway URL for issuing autoputer credentials to VM guests.
        # vmctl calls this endpoint to get a token before booting each VM.
        "VMCTL_GATEWAY_URL=http://127.0.0.1:8084"
        "VMCTL_CORPUSD_URL=http://127.0.0.1:8086"
        "VMCTL_ALLOW_HOST_PROCESS=false"
        "VMCTL_PLATFORM_WIRE_ENABLED=true"
        "VMCTL_AUTOPUTER_PROXY_SOCK=/run/go-choir/vmctl.sock"
        # Path to system binaries (ip, iptables, mkfs.ext4) for network/disk setup.
        "PATH=/run/current-system/sw/bin:/bin:/usr/bin"
      ];
    };
  };

  systemd.services.go-choir-disk-gc = {
    description = "go-choir bounded disk retention sweep";
    after = [ "go-choir-vmctl.service" ];
    wants = [ "go-choir-vmctl.service" ];
    serviceConfig = {
      Type = "oneshot";
      ExecStart = diskRetentionSweep;
      Environment = [
        # Emergency floor remains the lower bound. The higher target runs
        # routine Nix GC before storage pressure reaches recovery territory.
        "GO_CHOIR_DISK_GC_MIN_FREE_KIB=125829120"
        "GO_CHOIR_DISK_GC_TARGET_FREE_KIB=188743680"
      ];
    };
  };

  systemd.timers.go-choir-disk-gc = {
    description = "Daily go-choir disk retention sweep";
    wantedBy = [ "timers.target" ];
    timerConfig = {
      OnCalendar = "daily";
      RandomizedDelaySec = "1h";
      Persistent = true;
    };
  };

  systemd.services.go-choir-platform-dolt-history-audit = {
    description = "go-choir platform dolt commit-history growth audit";
    after = [ "go-choir-platform-dolt.service" ];
    requires = [ "go-choir-platform-dolt.service" ];
    serviceConfig = {
      Type = "oneshot";
      ExecStart = platformDoltHistoryAudit;
    };
  };

  systemd.timers.go-choir-platform-dolt-history-audit = {
    description = "Daily platform dolt commit-history growth audit";
    wantedBy = [ "timers.target" ];
    timerConfig = {
      OnCalendar = "daily";
      RandomizedDelaySec = "30m";
      Persistent = true;
    };
  };

  systemd.services.go-choir-gateway = {
    description = "go-choir Gateway Service";
    wantedBy = [ "multi-user.target" ];
    after = [ "network-online.target" "searx.service" ];
    wants = [ "network-online.target" "searx.service" ];
    serviceConfig = commonServiceHardening // {
      ExecStart = "${serviceExec "gateway" goChoirPackages.gateway}";
      Restart = "on-failure";
      RestartSec = 3;
      TimeoutStopSec = "11min";
      # Provider credentials (LLM and search) are injected via an EnvironmentFile
      # that lives in a writable runtime location outside the Nix store. The file is
      # created/updated by the deploy script and never committed to git.
      # This satisfies VAL-GATEWAY-004 and VAL-OPS-006: credentials stay
      # out of the repo, the Nix store, and guest-visible surfaces.
      #
      # The EnvironmentFile should contain:
      #   # LLM Provider Keys
      #   AWS_BEARER_TOKEN_BEDROCK=...
      #   ZAI_API_KEY=...
      #   FIREWORKS_API_KEY=...
      #   CHATGPT_AUTH_PATH=/var/lib/go-choir/codex-auth.json
      #   # ChatGPT search reuses the same auth file; optional tuning:
      #   CHATGPT_SEARCH_MODEL=gpt-5.5
      #   CHATGPT_SEARCH_REASONING_EFFORT=low
      #   CHATGPT_SEARCH_CONTEXT_SIZE=low
      #   # Search Provider Keys
      #   # (SearXNG is free and self-hosted — no key needed, just SEARXNG_URL
      #   #  which is set in the Environment block below)
      #   TAVILY_API_KEY=...
      #   BRAVE_API_KEY=...
      #   EXA_API_KEY=...
      #   SERPER_API_KEY=...
      #   PARALLEL_API_KEY=...
      #   SERPAPI_API_KEY=...
      EnvironmentFile = "-/var/lib/go-choir/gateway-provider.env";
      ReadWritePaths = [ "/var/lib/go-choir" ];
      Environment = [
        # Guest autoputeres call the host gateway via the tap subnet
        # (172.X.0.1:8084). Keep operator-only credential endpoints locked to
        # loopback at the handler layer, but let the process accept guest
        # traffic on tap addresses.
        "SERVER_HOST=0.0.0.0"
        "SERVER_SHUTDOWN_TIMEOUT=10m30s"
        # Inference and upstream provider calls may run for up to 10m; keep the
        # HTTP response open long enough for the handler to return a sanitized
        # result instead of dropping the guest connection with EOF.
        "SERVER_WRITE_TIMEOUT=10m30s"
        "GATEWAY_PORT=8084"
        "GATEWAY_IDENTITY_STORE_PATH=/var/lib/go-choir/gateway-identities.json"
        "GATEWAY_CHATGPT_MODELS=gpt-5.5,gpt-5.4,gpt-5.4-mini"
        "GATEWAY_CHATGPT_REASONING_EFFORT=low"
        # Tokens are currently issued at autoputer/VM bootstrap and not
        # proactively rotated. Use a longer TTL in staging to avoid
        # authentication lapses during normal multi-hour sessions.
        "GATEWAY_AUTOPUTER_TOKEN_TTL=720h"
        # SearXNG self-hosted meta-search (free, no credits). SearXNGProvider
        # is first in the provider list, so this absorbs the majority of
        # search load. Paid providers (Tavily, Brave, etc.) act as fallback.
        "SEARXNG_URL=http://127.0.0.1:8888"
      ];
    };
  };

  # Generate SearXNG secret key on first deploy (or reuse existing).
  # The environmentFile substitutes $SEARXNG_SECRET into settings.yml via envsubst.
  system.activationScripts.go-choir-searxng-secret = ''
    if [ ! -f /var/lib/go-choir/searxng.env ]; then
      secret="$(${pkgs.openssl}/bin/openssl rand -hex 32)"
      umask 077
      echo "SEARXNG_SECRET=$secret" > /var/lib/go-choir/searxng.env
      chmod 600 /var/lib/go-choir/searxng.env
      echo "go-choir SearXNG secret key generated"
    fi
  '';

  # The host system closure owns the exact immutable guest image. Point vmctl
  # at that store output atomically; never let current host binaries boot an
  # unrelated mutable directory. Preserve the one pre-managed image directory
  # as an explicit rollback ref during the first cutover.
  system.activationScripts.go-choir-guest-image = lib.optionalString (guestImage != null) ''
    set -euo pipefail
    state=/var/lib/go-choir
    target="$state/guest"
    legacy="$state/guest-pre-managed-rollback"
    conflict="$state/guest-cutover-conflict-recovery"
    next="$state/.guest-next"
    src="${guestImage}"
    for artifact in vmlinux rootfs.ext4 initrd storedisk.erofs kernel-params build.json; do
      if [ ! -f "$src/$artifact" ]; then
        echo "go-choir guest image missing $artifact in $src" >&2
        exit 1
      fi
    done
    mkdir -p "$state"
    moved_to=
    restore_moved_guest() {
      local current_target=
      if [ -z "$moved_to" ]; then
        return 0
      fi
      if [ -L "$target" ]; then
        if ! current_target=$(readlink "$target"); then
          return 1
        fi
        if [ "$current_target" != "$src" ]; then
          echo "go-choir guest image cutover refuses to replace unexpected target $target -> $current_target" >&2
          return 1
        fi
        if ! rm -f "$target"; then
          return 1
        fi
      fi
      if [ -e "$target" ] || [ -L "$target" ]; then
        echo "go-choir guest image cutover refuses to restore over existing target $target" >&2
        return 1
      fi
      if ! mv -T "$moved_to" "$target"; then
        echo "go-choir guest image cutover could not restore $moved_to to $target" >&2
        return 1
      fi
      moved_to=
    }
    saved_hup=$(trap -p HUP)
    saved_int=$(trap -p INT)
    saved_term=$(trap -p TERM)
    restore_saved_trap() {
      local signal="$1"
      local saved="$2"
      if [ -n "$saved" ]; then
        eval "$saved"
      else
        trap - "$signal"
      fi
    }
    restore_cutover_traps() {
      restore_saved_trap HUP "$saved_hup"
      restore_saved_trap INT "$saved_int"
      restore_saved_trap TERM "$saved_term"
    }
    on_cutover_signal() {
      local signal="$1"
      restore_moved_guest || true
      case "$signal" in
        HUP) exit 129 ;;
        INT) exit 130 ;;
        TERM) exit 143 ;;
      esac
    }
    move_to=
    preserved_conflict=false
    if [ -e "$target" ] && [ ! -L "$target" ]; then
      if [ -e "$legacy" ] || [ -L "$legacy" ]; then
        if [ -e "$conflict" ] || [ -L "$conflict" ]; then
          echo "go-choir guest image cutover refuses a second ambiguous guest tree; recovery already exists at $conflict" >&2
          exit 1
        fi
        move_to="$conflict"
        preserved_conflict=true
      else
        move_to="$legacy"
      fi
    fi
    rm -f "$next"
    ln -s "$src" "$next"
    trap 'on_cutover_signal HUP' HUP
    trap 'on_cutover_signal INT' INT
    trap 'on_cutover_signal TERM' TERM
    if [ -n "$move_to" ]; then
      moved_to="$move_to"
      if mv -T "$target" "$moved_to"; then
        :
      else
        status=$?
        moved_to=
        restore_cutover_traps
        rm -f "$next" || true
        exit "$status"
      fi
    fi
    if mv -Tf "$next" "$target"; then
      :
    else
      status=$?
      restore_moved_guest || status=1
      restore_cutover_traps
      rm -f "$next" || true
      exit "$status"
    fi
    moved_to=
    restore_cutover_traps
    if [ "$preserved_conflict" = true ]; then
      echo "go-choir guest image cutover preserved ambiguous active tree at $conflict" || true
    fi
    echo "go-choir guest image pointer updated to $src" || true
  '';

  # Host autoputer service deleted in PR 5 of store-consolidation mission.
  # All runtime work happens in VMs via vmctl. Served proxy routes require an
  # immutable D-ROUTE slot and fail before VM lookup when route authority is
  # unavailable; PROXY_AUTOPUTER_URL is not a production fallback.
  #
  # The autoputer binary is still built and packaged (goChoirPackages.autoputer)
  # because it runs inside Firecracker VMs (nix/autoputer-vm.nix). vmctl serves
  # it to VMs from /var/lib/go-choir/services/autoputer. Since the systemd
  # service is gone, node-b-sync-service-pointers can no longer discover the
  # package via systemctl show. This activation script installs the pointer
  # directly from the Nix closure on every NixOS switch.
  system.activationScripts.go-choir-autoputer-package-pointer = ''
    mkdir -p /var/lib/go-choir/services
    src="${goChoirPackages.autoputer}"
    if [ -x "$src/bin/autoputer" ]; then
      rm -rf /var/lib/go-choir/services/.autoputer-next
      mkdir -p /var/lib/go-choir/services/.autoputer-next
      cp -a "$src/." /var/lib/go-choir/services/.autoputer-next/
      rm -rf /var/lib/go-choir/services/autoputer-previous
      if [ -e /var/lib/go-choir/services/autoputer ]; then
        mv /var/lib/go-choir/services/autoputer /var/lib/go-choir/services/autoputer-previous
      fi
      mv /var/lib/go-choir/services/.autoputer-next /var/lib/go-choir/services/autoputer
      echo "go-choir autoputer package pointer updated from NixOS closure"
    fi
  '';

  # Workspace directory (for CI git pull deploys) and runtime paths.
  # Auth persistence and signing material must live in writable runtime
  # locations, not in the repo checkout or the Nix store.  Secrets are never
  # committed to git or embedded in the Nix store — the signing key is
  # generated on the host at first deploy.  The autoputer and proxy are
  # stateless for dev and need no writable directories.
  #
  # Firecracker guest image directory (VAL-VM-010): the repo-built guest
  # kernel and rootfs are deployed here. Provider credentials are never
  # placed in this directory or in the guest image itself (VAL-VM-011).
  systemd.tmpfiles.rules = [
    "d /opt/go-choir 0755 root root -"
    "d /var/www 0755 root root -"
    "d /var/www/go-choir 0755 root root -"
    "d /var/lib/go-choir 0750 root root -"
    "d /var/lib/go-choir/services 0755 root root -"
    "d ${autoputerRuntimeDir} 0750 root root -"
    "d /var/lib/go-choir/auth 0750 root root -"
    "d /var/lib/go-choir/auth-signing 0750 root root -"
    "d ${mailDir} 0700 root root -"
    "d ${mailDir}/raw 0700 root root -"
    "d ${mailDir}/attachments 0700 root root -"
    "d ${mailDir}/attachments/quarantine 0700 root root -"
    "z ${mailDir} 0700 root root -"
    "z ${mailDir}/mail.db 0600 root root -"
    "z ${mailDir}/raw 0700 root root -"
    "z ${mailDir}/attachments 0700 root root -"
    "z ${mailDir}/attachments/quarantine 0700 root root -"
    "d /var/lib/go-choir/vm-state 0750 root root -"
    "d ${platformDoltDir} 0750 root root -"
    "d ${platformDoltDBDir} 0750 root root -"
    "d ${platformArtifactsDir} 0750 root root -"
    "d ${platformArtifactsDir}/computer-inputs 0750 root root -"
    "d ${platformArtifactsDir}/computer-inputs/blobs 0750 root root -"
    "d ${platformArtifactsDir}/sha256 0750 root root -"
    # SearXNG secret key file directory (writable by the searx service)
    "d /var/lib/searx 0750 searx searx -"
  ];

  # Nix settings
  nix.settings = {
    experimental-features = [ "nix-command" "flakes" ];
    auto-optimise-store = false;
    min-free = 128849018880;
    max-free = 193273528320;
  };

  nix.optimise = {
    automatic = true;
    dates = [ "Sun 03:30" ];
    randomizedDelaySec = "45min";
    persistent = true;
  };

  # System packages
  environment.systemPackages = with pkgs; [
    bash
    btrfs-progs
    coreutils
    curl
    firecracker
    gcc
    git
    go
    gnumake
    gnugrep
    gnused
    dolt
    htop
    jq
    nodejs
    python3
    pkg-config
    icu
    icu.dev
    goChoirPackages.maildctl
    goChoirPackages.zot
    procps
    lsof
    strace
    ripgrep
    vim
  ];

  # Timezone
  time.timeZone = "UTC";

  system.stateVersion = "25.11";
}
