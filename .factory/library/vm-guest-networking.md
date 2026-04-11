# VM Guest Networking

This document describes the Firecracker VM guest networking architecture
implemented in this feature. Understanding this is essential for any worker
debugging VM guest connectivity issues.

## Architecture Overview

Each Firecracker VM gets its own isolated /30 subnet:

```
Host (tap device)     172.(N+1).0.1/30
Guest (eth0)          172.(N+1).0.2/30
```

Where N = hostPort - VM_HOST_BASE_PORT (e.g., port 9000 → N=0 → subnet 172.1.0.0/30).

## Boot Flow

1. vmctl resolves a user → VM assignment
2. vmctl issues a gateway credential token for the VM sandbox ID
3. vmmanager writes the token to `<state-dir>/<vmID>/persist/gateway-token`
4. vmmanager creates a per-VM writable rootfs copy
5. vmmanager creates a tap device (`vm-<id8>-tap`)
6. vmmanager assigns the host IP to the tap device
7. vmmanager configures iptables (DNAT, MASQUERADE, FORWARD)
8. vmmanager launches Firecracker with the config file
9. The guest kernel boots with `ip=` parameter (CONFIG_IP_PNP=y)
10. The guest init script (`/bin/init`) runs:
    - Mounts /proc, /sys, /dev, /tmp
    - Parses /proc/cmdline for guest_port, vm_id, epoch, ip=
    - Configures eth0 if the kernel didn't (fallback)
    - Reads gateway token from /mnt/persistent/gateway-token
    - Sets RUNTIME_GATEWAY_URL=http://<hostIP>:8084
    - Sets RUNTIME_GATEWAY_TOKEN from the file
    - Executes /bin/sandbox
11. The sandbox process starts, connects to the gateway, and serves /health

## Host-Side iptables Rules

Per VM, the following iptables rules are set up:

```
# Allow forwarding
FORWARD -i <tap> -j ACCEPT
FORWARD -o <tap> -j ACCEPT

# Masquerade guest traffic so replies route back through the tap
POSTROUTING -s <guestIP>/30 -o lo -j MASQUERADE
POSTROUTING -s <guestIP>/30 ! -d <guestIP>/30 -j MASQUERADE

# DNAT: host localhost port → guest (for vmctl/proxy health checks)
OUTPUT -p tcp --dport <hostPort> -d 127.0.0.1 -j DNAT --to-destination <guestIP>:<guestPort>

# DNAT: guest → gateway (gateway only listens on 127.0.0.1:8084)
PREROUTING -p tcp --dport 8084 -d <hostIP> -j DNAT --to-destination 127.0.0.1:8084
```

## Critical sysctl Settings

- `net.ipv4.ip_forward=1` — Required for host↔guest packet forwarding
- `net.ipv4.conf.<tap>.route_localnet=1` — Allows DNAT of 127.0.0.1 traffic on non-loopback interfaces
- `net.ipv4.conf.<tap>.accept_local=1` — Accepts packets from local subnets on the tap

## Common Issues

### Guest sandbox doesn't respond to health checks

1. Check if the guest kernel has CONFIG_IP_PNP=y (for `ip=` parameter)
2. Check if the init script correctly parses /proc/cmdline
3. Check if `init=/bin/init` is in the kernel boot args
4. Check if the tap device exists and has the host IP assigned
5. Check iptables rules (DNAT, MASQUERADE, FORWARD)
6. Check route_localnet on the tap device
7. Check if the sandbox binary is executable in the rootfs

### Guest can't reach the gateway

1. Verify MASQUERADE rules are set (without them, reply packets don't route back)
2. Verify PREROUTING DNAT for port 8084 (gateway only listens on 127.0.0.1)
3. Verify route_localnet=1 on the tap device
4. Verify the gateway token is written to the persistent directory

### init script fails to parse kernel cmdline

The kernel cmdline parameters use `key=value` format. The init script uses
POSIX shell `case` statements to parse them. Make sure the init script
uses the correct heredoc delimiter (single-quoted to prevent expansion).

## Key Files

- `nix/guest-image.nix` — Guest kernel config, rootfs builder, init script
- `internal/vmmanager/manager.go` — Firecracker lifecycle, networking setup
- `internal/vmmanager/config.go` — Config loading from environment
- `internal/vmctl/ownership.go` — VM ownership registry, gateway token issuance
- `cmd/vmctl/main.go` — vmctl service entry, adapter wiring
- `nix/node-b.nix` — Node B systemd service configuration

## Guest Rootfs Contents

The guest rootfs contains:
- `/bin/init` — Init script (parses cmdline, sets up networking, starts sandbox)
- `/bin/sandbox` — The sandbox runtime binary
- `/bin/ip` — iproute2 binary for network configuration
- `/bin/sh` — Bash shell
- `/bin/mount`, `mkdir`, `cat`, `echo`, `cut`, `grep` — Core utilities
- `/etc/resolv.conf` — DNS configuration (8.8.8.8)
- `/mnt/persistent/` — Persistent state mount point
