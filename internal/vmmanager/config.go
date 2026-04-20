// Package vmmanager provides Firecracker VM lifecycle management.
// See manager.go for the main implementation.
package vmmanager

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// LoadConfigFromEnv creates a ManagerConfig from VMCTL_* and VM_*
// environment variables. This is used by the vmctl service to configure
// the VM manager on Node B.
func LoadConfigFromEnv() ManagerConfig {
	cfg := DefaultManagerConfig()

	if v := os.Getenv("VM_FIRECRACKER_BIN"); v != "" {
		cfg.FirecrackerBinPath = v
	}

	if v := os.Getenv("VM_KERNEL_IMAGE"); v != "" {
		cfg.KernelImagePath = v
	}

	if v := os.Getenv("VM_INITRD_IMAGE"); v != "" {
		cfg.InitrdPath = v
	}

	if v := os.Getenv("VM_ROOTFS_IMAGE"); v != "" {
		cfg.RootfsPath = v
	}

	if v := os.Getenv("VM_STORE_DISK_IMAGE"); v != "" {
		cfg.StoreDiskPath = v
	}

	if v := os.Getenv("VM_KERNEL_PARAMS"); v != "" {
		cfg.KernelParams = strings.TrimSpace(v)
	}
	if v := os.Getenv("VM_KERNEL_PARAMS_FILE"); v != "" {
		if data, err := os.ReadFile(v); err == nil {
			cfg.KernelParams = strings.TrimSpace(string(data))
		}
	}

	if v := os.Getenv("VM_GUEST_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.GuestPort = n
		}
	}

	if v := os.Getenv("VM_HOST_BASE_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.HostBasePort = n
		}
	}

	if v := os.Getenv("VM_CPU_COUNT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.MachineCPUCount = n
		}
	}

	if v := os.Getenv("VM_MEM_MIB"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.MachineMemSizeMib = n
		}
	}

	if v := os.Getenv("VM_STATE_DIR"); v != "" {
		cfg.StateDir = v
	}

	if v := os.Getenv("VM_HEALTH_CHECK_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.HealthCheckInterval = d
		}
	}

	if v := os.Getenv("VM_HEALTH_CHECK_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.HealthCheckTimeout = d
		}
	}

	if v := os.Getenv("VM_BOOT_READY_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.BootReadyTimeout = d
		}
	}

	return cfg
}

// Validate checks that the manager configuration is usable for
// launching Firecracker VMs on Node B.
func (c ManagerConfig) Validate() error {
	if c.KernelImagePath == "" {
		return fmt.Errorf("VM_KERNEL_IMAGE is required for Firecracker VM management")
	}
	if c.StateDir == "" {
		return fmt.Errorf("VM_STATE_DIR is required for Firecracker VM management")
	}
	return nil
}

// IsFirecrackerAvailable returns true if the Firecracker binary is
// present on the system (i.e., we are on Node B with KVM).
// On macOS or environments without Firecracker, this returns false
// and the vmctl service falls back to host-process sandbox mode.
func IsFirecrackerAvailable() bool {
	bin := os.Getenv("VM_FIRECRACKER_BIN")
	if bin == "" {
		bin = "firecracker"
	}

	// Check if the binary exists and is executable.
	_, err := os.Stat(bin)
	if err != nil {
		// Try PATH lookup.
		if _, pathErr := findInPath(bin); pathErr != nil {
			return false
		}
	}
	return true
}

func findInPath(name string) (string, error) {
	path := os.Getenv("PATH")
	if path == "" {
		return "", fmt.Errorf("no PATH")
	}

	start := 0
	for i := 0; i <= len(path); i++ {
		if i == len(path) || path[i] == ':' {
			candidate := path[start:i] + "/" + name
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				return candidate, nil
			}
			start = i + 1
		}
	}
	return "", fmt.Errorf("%s not found in PATH", name)
}
