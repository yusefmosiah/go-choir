// Package vmmanager provides the Firecracker VM lifecycle manager for Node B.
//
// This package manages the full lifecycle of Firecracker microVMs:
// boot, health checking, stop, hibernate, resume, and force-kill.
// It is the concrete runtime that vmctl delegates to when managing
// VM-backed sandbox workloads (VAL-VM-010, VAL-VM-011).
//
// Build: 2026-04-12-01 - Force rebuild after Nix cache issue
//
// The VMManager is designed to run only on Linux with KVM available.
// On non-Linux platforms, the stub implementations return graceful
// errors so that local development and testing remain possible.
//
// Key invariants:
//   - Guest images are repo-built artifacts; the manager never generates
//     or modifies guest images at runtime (VAL-VM-010).
//   - Provider credentials are never injected into guest environment,
//     config files, mounted secrets, or process arguments (VAL-VM-011).
//   - VM IDs are stable identifiers that survive stop/hibernate/resume
//     cycles for the same user, preserving user state (VAL-CROSS-116).
//   - Crash recovery does not duplicate canonical effects; each VM
//     carries a monotonically increasing epoch counter that increments
//     on fresh boot but stays the same on resume (VAL-CROSS-117).
package vmmanager

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// VMState represents the lifecycle state of a Firecracker VM.
type VMState string

const (
	// StatePending means the VM is being prepared but not yet launched.
	StatePending VMState = "pending"

	// StateRunning means the VM is actively running and healthy.
	StateRunning VMState = "running"

	// StateStopped means the VM has been cleanly shut down.
	StateStopped VMState = "stopped"

	// StateHibernated means the VM's memory and state have been saved to disk
	// and can be resumed. This is a future capability; for now the manager
	// uses stop/resume semantics with persistent state.
	StateHibernated VMState = "hibernated"

	// StateFailed means the VM has crashed or failed to start.
	StateFailed VMState = "failed"
)

// VMConfig holds the configuration for launching a single Firecracker VM.
type VMConfig struct {
	// VMID is the unique identifier for this VM instance.
	VMID string

	// KernelImagePath is the path to the repo-built guest kernel image.
	KernelImagePath string

	// InitrdPath is the path to the guest initramfs (optional).
	// If set, the initrd is passed to Firecracker for loading ext4
	// and other modules before mounting the root filesystem.
	InitrdPath string

	// RootfsPath is the path to the repo-built guest root filesystem image.
	// With the upstream microvm.nix approach, this is the boot disk used
	// as the guest root filesystem.
	RootfsPath string

	// StoreDiskPath is the path to the erofs store disk image built by
	// microvm.nix. When set, the Firecracker config includes this as a
	// read-only virtio-blk drive that the NixOS init mounts at /nix/store.
	StoreDiskPath string

	// GuestPort is the port the guest sandbox runtime listens on inside
	// the VM. The host-side vsock or tap networking maps this to a
	// host-accessible URL.
	GuestPort int

	// VsockPort is the vsock port for host-guest communication.
	VsockPort uint32

	// MachineCPUCount is the number of vCPUs for the guest.
	MachineCPUCount int

	// MachineMemSizeMib is the memory size in MiB for the guest.
	MachineMemSizeMib int

	// PersistentDir is the host directory where per-VM persistent state
	// (user data, task state) is stored. This directory is mounted into
	// the guest and survives stop/resume cycles (VAL-CROSS-116).
	PersistentDir string

	// GatewayToken is the sandbox credential token for authenticating to
	// the host-side gateway. Written to a file in the persistent directory
	// so the guest init script can read and set RUNTIME_GATEWAY_TOKEN.
	// This is NOT a provider credential — it's a sandbox identity token
	// that allows the guest sandbox to call the gateway (VAL-VM-011
	// still holds: no provider credentials in the guest).
	GatewayToken string

	// Epoch is the monotonically increasing boot counter for this VM.
	// On fresh boot, the epoch increments. On resume from hibernate,
	// the epoch stays the same. Callers can use epoch to detect whether
	// a VM went through a fresh boot vs. a resume (VAL-CROSS-117).
	Epoch int64
}

// VMInstance represents a running or stopped Firecracker VM.
type VMInstance struct {
	// Config is the configuration used to launch this VM.
	Config VMConfig

	// State is the current lifecycle state.
	State VMState

	// HostURL is the URL where this VM's sandbox runtime is reachable
	// from the host. On the Firecracker tap path this is the guest IP on the
	// per-VM /30 subnet (for example "http://172.X.0.2:8085").
	HostURL string

	// PID is the Firecracker process ID (0 if not running).
	PID int

	// StartedAt is when the VM was last started.
	StartedAt time.Time

	// LastHealthCheck is when the VM was last health-checked.
	LastHealthCheck time.Time

	// Healthy is the result of the last health check.
	Healthy bool

	// cmd is the running Firecracker process (nil when stopped).
	cmd *exec.Cmd

	// cancel is a function to clean up the process (nil when stopped).
	done chan struct{}
}

// ManagerConfig holds the global configuration for the VM manager.
type ManagerConfig struct {
	// FirecrackerBinPath is the path to the Firecracker binary.
	// If empty, the manager searches $PATH.
	FirecrackerBinPath string

	// KernelImagePath is the default kernel image for all VMs.
	KernelImagePath string

	// InitrdPath is the path to the guest initramfs (optional).
	// If set, the initrd is passed to Firecracker for module loading.
	InitrdPath string

	// RootfsPath is the default rootfs image for all VMs. With the
	// upstream microvm.nix approach, this is the boot disk used as /.
	RootfsPath string

	// StoreDiskPath is the path to the erofs store disk image built by
	// microvm.nix. This disk contains the shared read-only nix store
	// closure. All VMs reference the same store disk, and KSM on the
	// host deduplicates identical pages across VMs.
	// When set, the Firecracker config includes this as a virtio-blk drive
	// mounted read-only at /nix/store by the guest init.
	StoreDiskPath string

	// KernelParams is the upstream microvm.nix kernel parameter set
	// (for example root=fstab, init=/nix/store/.../init, regInfo=...).
	// vmmanager appends its per-VM runtime parameters to this base cmdline.
	KernelParams string

	// GuestPort is the port the guest sandbox listens on.
	GuestPort int

	// BaseVsockPort is the starting vsock port for VM assignment.
	BaseVsockPort uint32

	// MachineCPUCount is the default number of vCPUs per VM.
	MachineCPUCount int

	// MachineMemSizeMib is the default memory per VM.
	MachineMemSizeMib int

	// HostBasePort is the starting host port for VM sandbox listeners.
	// Each VM gets a unique port starting from this value.
	HostBasePort int

	// StateDir is the directory where VM state (pid files, persistent
	// data, epoch counters) is stored.
	StateDir string

	// HealthCheckInterval is how often the manager checks VM health.
	HealthCheckInterval time.Duration

	// HealthCheckTimeout is the per-check HTTP timeout.
	HealthCheckTimeout time.Duration
}

// DefaultManagerConfig returns a sensible default configuration.
func DefaultManagerConfig() ManagerConfig {
	return ManagerConfig{
		FirecrackerBinPath:  "firecracker",
		GuestPort:           8085,
		BaseVsockPort:       6000,
		MachineCPUCount:     2,
		MachineMemSizeMib:   512,
		HostBasePort:        9000,
		StateDir:            "/var/lib/go-choir/vm-state",
		HealthCheckInterval: 15 * time.Second,
		HealthCheckTimeout:  3 * time.Second,
	}
}

// Manager manages Firecracker VM lifecycles on Node B.
// It provides thread-safe VM boot, stop, resume, and health-check
// operations that vmctl delegates to.
type Manager struct {
	cfg      ManagerConfig
	mu       sync.RWMutex
	vms      map[string]*VMInstance // vmID → instance
	nextPort int                    // next host port to assign

	// healthCancel is used to stop the background health checker.
	healthCancel chan struct{}
	healthDone   chan struct{}
}

// NewManager creates a new VM manager with the given configuration.
func NewManager(cfg ManagerConfig) *Manager {
	if cfg.HostBasePort <= 0 {
		cfg.HostBasePort = 9000
	}
	return &Manager{
		cfg:      cfg,
		vms:      make(map[string]*VMInstance),
		nextPort: cfg.HostBasePort,
	}
}

func (m *Manager) guestAndHostIP(hostPort int) (guestIP, hostIP string) {
	subnetIndex := hostPort - m.cfg.HostBasePort + 1
	return fmt.Sprintf("172.%d.0.2", subnetIndex), fmt.Sprintf("172.%d.0.1", subnetIndex)
}

// Start begins the manager's background health checking loop.
func (m *Manager) Start() {
	m.mu.Lock()
	m.healthCancel = make(chan struct{})
	m.healthDone = make(chan struct{})
	m.mu.Unlock()

	go m.healthCheckLoop()
	log.Printf("vmmanager: started with state_dir=%s base_port=%d", m.cfg.StateDir, m.cfg.HostBasePort)
}

// Stop shuts down all managed VMs and stops the health checker.
func (m *Manager) Stop() {
	m.mu.Lock()
	cancel := m.healthCancel
	done := m.healthDone
	m.mu.Unlock()

	// Stop the health checker.
	if cancel != nil {
		close(cancel)
	}
	if done != nil {
		<-done
	}

	// Stop all VMs.
	m.mu.Lock()
	var vmIDs []string
	for id := range m.vms {
		vmIDs = append(vmIDs, id)
	}
	m.mu.Unlock()

	for _, id := range vmIDs {
		if err := m.StopVM(id); err != nil {
			log.Printf("vmmanager: stop VM %s during shutdown: %v", id, err)
		}
	}
	log.Printf("vmmanager: stopped")
}

// BootVM launches a new Firecracker VM with the given configuration.
// Returns the VM instance with its host-accessible URL populated.
//
// The guest receives only the minimum bootstrap material:
//   - The repo-built kernel and rootfs (read-only overlay)
//   - A persistent state directory for user data (read-write)
//   - The guest port to listen on
//
// Provider credentials are never included in the guest environment,
// config files, or process arguments (VAL-VM-011).
func (m *Manager) BootVM(cfg VMConfig) (*VMInstance, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if the VM already exists.
	if inst, ok := m.vms[cfg.VMID]; ok {
		if inst.State == StateRunning {
			return inst, nil
		}
		// VM exists but is not running; clean up before relaunching.
		m.forceCleanup(cfg.VMID)
	}

	// Assign a host port for this VM.
	hostPort := m.nextPort
	m.nextPort++

	// Ensure VM state directory exists before creating data image.
	vmStateDir := filepath.Join(m.cfg.StateDir, cfg.VMID)
	if err := os.MkdirAll(vmStateDir, 0750); err != nil {
		return nil, fmt.Errorf("failed to create VM state directory: %w", err)
	}

	// Ensure the persistent directory exists.
	if cfg.PersistentDir == "" {
		cfg.PersistentDir = filepath.Join(m.cfg.StateDir, cfg.VMID, "persist")
	}
	if err := os.MkdirAll(cfg.PersistentDir, 0o755); err != nil {
		return nil, fmt.Errorf("create persistent dir %s: %w", cfg.PersistentDir, err)
	}

	// Write the gateway token to the persistent directory if provided.
	// The guest init script reads this file and sets RUNTIME_GATEWAY_TOKEN.
	// This is a sandbox identity token (not a provider credential) so
	// VAL-VM-011 is not violated — the token only authenticates the
	// sandbox to the host-side gateway, which holds the actual provider
	// credentials (VAL-GATEWAY-004).
	if cfg.GatewayToken != "" {
		tokenPath := filepath.Join(cfg.PersistentDir, "gateway-token")
		if err := os.WriteFile(tokenPath, []byte(cfg.GatewayToken), 0o600); err != nil {
			log.Printf("vmmanager: warning: could not write gateway token for VM %s: %v", cfg.VMID, err)
		}
	}

	// Load or initialize the epoch counter for this VM.
	epoch, err := m.loadEpoch(cfg.VMID)
	if err != nil {
		log.Printf("vmmanager: could not load epoch for %s, starting at 1: %v", cfg.VMID, err)
		epoch = 1
	} else {
		epoch++ // increment on fresh boot
	}
	cfg.Epoch = epoch

	// Save the updated epoch.
	_ = m.saveEpoch(cfg.VMID, epoch)

	// Apply defaults from manager config.
	if cfg.KernelImagePath == "" {
		cfg.KernelImagePath = m.cfg.KernelImagePath
	}
	if cfg.InitrdPath == "" {
		cfg.InitrdPath = m.cfg.InitrdPath
	}
	if cfg.RootfsPath == "" {
		cfg.RootfsPath = m.cfg.RootfsPath
	}
	if cfg.StoreDiskPath == "" {
		cfg.StoreDiskPath = m.cfg.StoreDiskPath
	}
	if cfg.GuestPort == 0 {
		cfg.GuestPort = m.cfg.GuestPort
	}
	if cfg.MachineCPUCount == 0 {
		cfg.MachineCPUCount = m.cfg.MachineCPUCount
	}
	if cfg.MachineMemSizeMib == 0 {
		cfg.MachineMemSizeMib = m.cfg.MachineMemSizeMib
	}

	// Prepare per-VM disk images.
	if cfg.StoreDiskPath != "" {
		// The erofs nix-store disk is shared and read-only. Only the mutable
		// per-VM data image is created here.
		vmDataDir := filepath.Join(m.cfg.StateDir, cfg.VMID)
		if err := os.MkdirAll(vmDataDir, 0o755); err != nil {
			return nil, fmt.Errorf("create VM data dir %s: %w", vmDataDir, err)
		}
		dataImg := filepath.Join(vmDataDir, "data.img")
		if _, err := os.Stat(dataImg); os.IsNotExist(err) {
			// Create a 64MB sparse ext4 data image for mutable state.
			if err := m.createDataImage(dataImg, 64); err != nil {
				return nil, fmt.Errorf("create data image for VM %s: %w", cfg.VMID, err)
			}
		}
	} else if cfg.RootfsPath != "" {
		// Legacy approach: create a per-VM writable copy of the rootfs.
		// The base rootfs image is read-only (from the Nix store or
		// /var/lib/go-choir/guest/), but Firecracker needs write access.
		// Each VM gets its own copy so state is isolated per-user (VAL-VM-005).
		vmRootfs := filepath.Join(m.cfg.StateDir, cfg.VMID, "rootfs.ext4")
		if err := m.copyFile(cfg.RootfsPath, vmRootfs); err != nil {
			return nil, fmt.Errorf("copy rootfs for VM %s: %w", cfg.VMID, err)
		}
		cfg.RootfsPath = vmRootfs
	}

	// Build the Firecracker configuration.
	// Provider credentials are explicitly NOT included here (VAL-VM-011).
	fcConfig := m.buildFirecrackerConfig(cfg, hostPort)

	guestIP, _ := m.guestAndHostIP(hostPort)
	hostURL := fmt.Sprintf("http://%s:%d", guestIP, cfg.GuestPort)

	inst := &VMInstance{
		Config:    cfg,
		State:     StatePending,
		HostURL:   hostURL,
		StartedAt: time.Now(),
		done:      make(chan struct{}),
	}

	m.vms[cfg.VMID] = inst

	// Launch Firecracker.
	if err := m.launchFirecracker(cfg.VMID, fcConfig, hostPort, cfg.GuestPort); err != nil {
		inst.State = StateFailed
		return nil, fmt.Errorf("launch firecracker for VM %s: %w", cfg.VMID, err)
	}

	inst.State = StateRunning
	inst.Healthy = true
	inst.LastHealthCheck = time.Now()

	log.Printf("vmmanager: booted VM %s (host=%s epoch=%d)", cfg.VMID, hostURL, epoch)

	return inst, nil
}

// StopVM cleanly stops a running VM.
func (m *Manager) StopVM(vmID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	inst, ok := m.vms[vmID]
	if !ok {
		return fmt.Errorf("vm %s not found", vmID)
	}

	if inst.State != StateRunning && inst.State != StateFailed {
		return nil // already stopped or hibernated
	}

	m.killFirecrackerProcess(inst)
	inst.State = StateStopped
	inst.Healthy = false

	log.Printf("vmmanager: stopped VM %s", vmID)
	return nil
}

// HibernateVM saves the VM state and stops it. The VM can be resumed
// later, restoring the same user state from the persistent directory
// (VAL-CROSS-116).
//
// Note: Full memory snapshot/restore is a future Firecracker feature.
// For now, hibernate is semantically equivalent to stop with the
// persistent directory preserved, and resume reboots the VM with the
// same persistent data.
func (m *Manager) HibernateVM(vmID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	inst, ok := m.vms[vmID]
	if !ok {
		return fmt.Errorf("vm %s not found", vmID)
	}

	if inst.State != StateRunning {
		return fmt.Errorf("vm %s is not running (state=%s)", vmID, inst.State)
	}

	m.killFirecrackerProcess(inst)
	inst.State = StateHibernated
	inst.Healthy = false

	log.Printf("vmmanager: hibernated VM %s (persistent data preserved at %s)", vmID, inst.Config.PersistentDir)
	return nil
}

// ResumeVM resumes a stopped or hibernated VM, restoring the user's
// persisted state from the VM's persistent directory (VAL-CROSS-116).
// The epoch counter is NOT incremented on resume, so callers can
// distinguish fresh boot from resume (VAL-CROSS-117).
func (m *Manager) ResumeVM(vmID string) (*VMInstance, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	inst, ok := m.vms[vmID]
	if !ok {
		return nil, fmt.Errorf("vm %s not found", vmID)
	}

	if inst.State != StateStopped && inst.State != StateHibernated {
		if inst.State == StateRunning {
			return inst, nil
		}
		return nil, fmt.Errorf("vm %s cannot be resumed (state=%s)", vmID, inst.State)
	}

	// Clean up the old process.
	m.forceCleanup(vmID)

	// Assign a new host port (old one may be reused).
	hostPort := m.nextPort
	m.nextPort++

	guestIP, _ := m.guestAndHostIP(hostPort)
	hostURL := fmt.Sprintf("http://%s:%d", guestIP, inst.Config.GuestPort)
	inst.HostURL = hostURL

	// Reuse the existing epoch (no increment on resume).
	// This preserves the VM identity across stop/resume so callers
	// can detect duplicate canonical effects (VAL-CROSS-117).
	epoch := inst.Config.Epoch

	fcConfig := m.buildFirecrackerConfig(inst.Config, hostPort)

	if err := m.launchFirecracker(vmID, fcConfig, hostPort, inst.Config.GuestPort); err != nil {
		inst.State = StateFailed
		return nil, fmt.Errorf("resume firecracker for VM %s: %w", vmID, err)
	}

	inst.State = StateRunning
	inst.Healthy = true
	inst.StartedAt = time.Now()
	inst.LastHealthCheck = time.Now()

	log.Printf("vmmanager: resumed VM %s (host=%s epoch=%d)", vmID, hostURL, epoch)
	return inst, nil
}

// ForceKillVM forcefully terminates a VM process. Use for unhealthy
// guests that do not respond to clean shutdown.
func (m *Manager) ForceKillVM(vmID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	inst, ok := m.vms[vmID]
	if !ok {
		return fmt.Errorf("vm %s not found", vmID)
	}

	m.killFirecrackerProcess(inst)
	inst.State = StateFailed
	inst.Healthy = false

	log.Printf("vmmanager: force-killed VM %s", vmID)
	return nil
}

// GetVM returns the VM instance for the given ID, or nil if not found.
func (m *Manager) GetVM(vmID string) *VMInstance {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.vms[vmID]
}

// ListVMs returns all managed VM instances.
func (m *Manager) ListVMs() []*VMInstance {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]*VMInstance, 0, len(m.vms))
	for _, inst := range m.vms {
		result = append(result, inst)
	}
	return result
}

// RemoveVM removes a VM from management. The VM must be stopped first.
// The persistent directory is preserved so the user's data survives
// across stop/remove/recreate cycles (VAL-CROSS-116).
func (m *Manager) RemoveVM(vmID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	inst, ok := m.vms[vmID]
	if !ok {
		return nil // idempotent
	}

	if inst.State == StateRunning {
		return fmt.Errorf("vm %s is still running; stop it first", vmID)
	}

	delete(m.vms, vmID)
	log.Printf("vmmanager: removed VM %s from management (persistent data preserved)", vmID)
	return nil
}

// CheckHealth probes the VM's guest health endpoint.
func (m *Manager) CheckHealth(vmID string) (bool, error) {
	m.mu.RLock()
	inst, ok := m.vms[vmID]
	m.mu.RUnlock()

	if !ok {
		return false, fmt.Errorf("vm %s not found", vmID)
	}

	if inst.State != StateRunning {
		return false, nil
	}

	// Use HTTP client to probe the guest's /health endpoint.
	healthy := m.probeGuestHealth(inst.HostURL)

	m.mu.Lock()
	inst.Healthy = healthy
	inst.LastHealthCheck = time.Now()
	if !healthy {
		log.Printf("vmmanager: health check failed for VM %s at %s", vmID, inst.HostURL)
	}
	m.mu.Unlock()

	return healthy, nil
}

// MarkFailed marks a VM as failed (e.g., after detecting an unhealthy guest).
func (m *Manager) MarkFailed(vmID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if inst, ok := m.vms[vmID]; ok {
		inst.State = StateFailed
		inst.Healthy = false
		log.Printf("vmmanager: marked VM %s as failed", vmID)
	}
}

// RecoverVM attempts to recover a failed VM by force-killing and
// rebooting it. The persistent data is preserved so the user's state
// survives recovery (VAL-VM-009, VAL-CROSS-116).
//
// The epoch IS incremented on recovery because this is a fresh boot,
// not a resume. This prevents duplicate canonical effects (VAL-CROSS-117).
func (m *Manager) RecoverVM(vmID string) (*VMInstance, error) {
	m.mu.Lock()
	inst, ok := m.vms[vmID]
	if !ok {
		m.mu.Unlock()
		return nil, fmt.Errorf("vm %s not found", vmID)
	}

	// Force kill the old process.
	m.killFirecrackerProcess(inst)
	inst.State = StateFailed
	m.mu.Unlock()

	// Increment the epoch for recovery (fresh boot, not resume).
	m.mu.Lock()
	inst.Config.Epoch++
	_ = m.saveEpoch(vmID, inst.Config.Epoch)
	m.mu.Unlock()

	// Boot with the same config but new epoch.
	return m.BootVM(inst.Config)
}

// buildFirecrackerConfig generates the JSON configuration for a
// Firecracker VM launch. This config is written to a temp file and
// passed to the Firecracker binary via --config-file.
//
// IMPORTANT: Provider credentials are explicitly NOT included in
// this configuration. The guest environment, boot_args, and drives
// contain only the minimum bootstrap material (VAL-VM-011).
func (m *Manager) buildFirecrackerConfig(cfg VMConfig, hostPort int) map[string]interface{} {
	// Calculate the /30 subnet for this VM based on host port.
	// hostPort is 9000+N, so subnetIndex = N.
	// Host gets 172.(N+1).0.1, guest gets 172.(N+1).0.2.
	guestIP, hostIP := m.guestAndHostIP(hostPort)

	// Build drives list.
	// With the upstream microvm.nix approach:
	//   - Shared nix-store disk (erofs) as a read-only virtio-blk drive
	//   - Per-VM data volume for mutable sandbox state
	// With legacy rootfs (old approach):
	//   - Rootfs ext4 as writable virtio-blk at /dev/vda
	var drives []map[string]interface{}

	if cfg.StoreDiskPath != "" {
		// Shared nix-store disk mounted read-only by the guest init.
		drives = append(drives, map[string]interface{}{
			"drive_id":       "store",
			"path_on_host":   cfg.StoreDiskPath,
			"is_root_device": false,
			"is_read_only":   true,
		})
	} else if cfg.RootfsPath != "" {
		// Legacy approach: ext4 rootfs as the writable root device.
		// This is the old custom init script approach with init=/bin/init.
		drives = append(drives, map[string]interface{}{
			"drive_id":       "rootfs",
			"path_on_host":   cfg.RootfsPath,
			"is_root_device": true,
			"is_read_only":   false,
		})
	}

	// Per-VM data volume for mutable sandbox state (always present).
	// vmmanager creates a data.img per-VM in the state directory.
	dataImgPath := filepath.Join(m.cfg.StateDir, cfg.VMID, "data.img")
	drives = append(drives, map[string]interface{}{
		"drive_id":       "data",
		"path_on_host":   dataImgPath,
		"is_root_device": false,
		"is_read_only":   false,
	})

	// Build the guest kernel boot arguments.
	// These contain NO provider credentials (VAL-VM-011).
	// Network config is passed via ip= kernel parameter so the guest
	// can configure its network interface at boot time.
	//
	// With the upstream microvm.nix approach:
	//   - start from the microvm-provided kernel params (`root=fstab`,
	//     `/nix/store/.../init`, `regInfo=...`)
	//   - append per-VM runtime parameters used by go-choir
	//
	// With legacy rootfs:
	//   - init=/bin/init tells the kernel to run our custom init script
	//   - root=/dev/vda points to the ext4 root drive
	var bootArgs string
	if cfg.StoreDiskPath != "" && strings.TrimSpace(m.cfg.KernelParams) != "" {
		runtimeArgs := []string{
			"console=ttyS0,115200",
			"reboot=k",
			"panic=1",
			"i8042.noaux",
			"i8042.nomux",
			"i8042.nopnp",
			"i8042.dumbkbd",
			fmt.Sprintf("guest_port=%d", cfg.GuestPort),
			"persistent=/mnt/persistent",
			fmt.Sprintf("vm_id=%s", cfg.VMID),
			fmt.Sprintf("epoch=%d", cfg.Epoch),
			fmt.Sprintf("choir.gateway_url=http://%s:8084", hostIP),
			fmt.Sprintf("ip=%s::%s:255.255.255.252::eth0:off", guestIP, hostIP),
		}
		bootArgs = strings.Join(append([]string{strings.TrimSpace(m.cfg.KernelParams)}, runtimeArgs...), " ")
	} else {
		// Legacy approach with custom init script.
		bootArgs = fmt.Sprintf(
			"console=ttyS0 reboot=k panic=1 pci=off "+
				"root=/dev/vda rw init=/bin/init "+
				"guest_port=%d persistent=/mnt/persistent "+
				"vm_id=%s epoch=%d "+
				"ip=%s::%s:255.255.255.252::eth0:off",
			cfg.GuestPort, cfg.VMID, cfg.Epoch,
			guestIP, hostIP,
		)
	}

	// Build boot-source config. If an initrd is available, include it
	// so the kernel can load ext4/erofs/virtio modules before mounting.
	// With microvm.nix, the initrd is essential for systemd boot.
	bootSource := map[string]interface{}{
		"kernel_image_path": cfg.KernelImagePath,
		"boot_args":         bootArgs,
	}
	if cfg.InitrdPath != "" {
		bootSource["initrd_path"] = cfg.InitrdPath
	}

	// Firecracker VM configuration.
	// No secrets, no provider credentials, no host paths (VAL-VM-011).
	fcConfig := map[string]interface{}{
		"boot-source": bootSource,
		"drives":      drives,
		"machine-config": map[string]interface{}{
			"vcpu_count":   cfg.MachineCPUCount,
			"mem_size_mib": cfg.MachineMemSizeMib,
		},
		"network-interfaces": []map[string]interface{}{
			{
				"iface_id":      "eth0",
				"guest_mac":     fmt.Sprintf("AA:FC:00:00:00:%02X", hostPort%256),
				"host_dev_name": fmt.Sprintf("vm-%s-tap", cfg.VMID[:8]),
			},
		},
	}

	return fcConfig
}

// launchFirecracker starts a Firecracker process for the given VM.
// hostPort is the host port assigned for this VM, used for setting up
// networking. guestPort is the port the guest sandbox listens on.
func (m *Manager) launchFirecracker(vmID string, fcConfig map[string]interface{}, hostPort int, guestPort int) error {
	bin := m.cfg.FirecrackerBinPath
	if bin == "" {
		bin = "firecracker"
	}

	// Write the config to a temp file.
	configData, err := json.Marshal(fcConfig)
	if err != nil {
		return fmt.Errorf("marshal firecracker config: %w", err)
	}

	configDir := filepath.Join(m.cfg.StateDir, vmID)
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return fmt.Errorf("create config dir %s: %w", configDir, err)
	}

	configPath := filepath.Join(configDir, "fc-config.json")
	if err := os.WriteFile(configPath, configData, 0o644); err != nil {
		return fmt.Errorf("write firecracker config: %w", err)
	}

	// Create the tap device for VM networking.
	// Firecracker requires the tap device to exist before launching.
	// The tap name comes from the config's host_dev_name field.
	tapName := fmt.Sprintf("vm-%s-tap", vmID[:8])
	if err := m.createTapDevice(tapName); err != nil {
		log.Printf("vmmanager: warning: could not create tap device %s: %v (may already exist)", tapName, err)
	}

	// Configure host-side networking for the tap device.
	// Assign the host IP in the /30 subnet and set up DNAT
	// so traffic to the host port reaches the guest.
	guestIP, hostIP := m.guestAndHostIP(hostPort)
	if err := m.setupHostNetworking(tapName, hostIP, hostPort, guestIP, guestPort); err != nil {
		log.Printf("vmmanager: warning: host networking setup failed for %s: %v", tapName, err)
	}

	// Build the Firecracker command.
	// --no-api disables the Firecracker API socket (we use config file).
	// --enable-pci matches the upstream microvm runner so the guest sees the
	// expected Firecracker device model.
	// --id sets the VM identifier.
	// --config-file provides the VM configuration.
	cmd := exec.Command(bin,
		"--no-api",
		"--enable-pci",
		"--id", vmID,
		"--config-file", configPath,
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start firecracker: %w", err)
	}

	// Store the process info.
	if inst, ok := m.vms[vmID]; ok {
		inst.PID = cmd.Process.Pid
		inst.cmd = cmd
		inst.done = make(chan struct{})

		// Monitor the process in the background.
		go func() {
			err := cmd.Wait()
			close(inst.done)
			if err != nil {
				log.Printf("vmmanager: firecracker process for VM %s exited with error: %v", vmID, err)
				m.MarkFailed(vmID)
			}
		}()
	}

	return nil
}

// killFirecrackerProcess forcefully terminates a Firecracker process
// and cleans up the associated tap device.
func (m *Manager) killFirecrackerProcess(inst *VMInstance) {
	if inst.cmd != nil && inst.cmd.Process != nil {
		_ = inst.cmd.Process.Kill()
		// Wait for the process to exit.
		if inst.done != nil {
			select {
			case <-inst.done:
			case <-time.After(5 * time.Second):
				log.Printf("vmmanager: timeout waiting for VM process %d to exit", inst.PID)
			}
		}
	}
	inst.PID = 0
	inst.cmd = nil

	// Clean up the tap device.
	if inst.Config.VMID != "" && len(inst.Config.VMID) >= 8 {
		tapName := fmt.Sprintf("vm-%s-tap", inst.Config.VMID[:8])
		m.deleteTapDevice(tapName)
	}
}

// forceCleanup removes any leftover state for a VM before relaunching.
func (m *Manager) forceCleanup(vmID string) {
	if inst, ok := m.vms[vmID]; ok {
		m.killFirecrackerProcess(inst)
	}
}

// probeGuestHealth attempts to reach the guest's /health endpoint.
// On Linux/Node B with real Firecracker VMs, this uses HTTP to probe
// the guest sandbox runtime. On macOS or environments without Firecracker,
// this returns true by default (the VM isn't actually running).
func (m *Manager) probeGuestHealth(hostURL string) bool {
	client := &http.Client{
		Timeout: m.cfg.HealthCheckTimeout,
		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout: m.cfg.HealthCheckTimeout,
			}).DialContext,
		},
	}
	resp, err := client.Get(hostURL + "/health")
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// loadEpoch loads the epoch counter for a VM from persistent storage.
func (m *Manager) loadEpoch(vmID string) (int64, error) {
	path := filepath.Join(m.cfg.StateDir, vmID, "epoch")
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var epoch int64
	for _, b := range data {
		if b >= '0' && b <= '9' {
			epoch = epoch*10 + int64(b-'0')
		}
	}
	if epoch == 0 {
		return 0, fmt.Errorf("invalid epoch data")
	}
	return epoch, nil
}

// saveEpoch persists the epoch counter for a VM.
func (m *Manager) saveEpoch(vmID string, epoch int64) error {
	path := filepath.Join(m.cfg.StateDir, vmID, "epoch")
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(fmt.Sprintf("%d", epoch)), 0o644)
}

// copyFile copies a file from src to dst. It creates the destination
// directory if needed and sets the output file to be writable by the
// owner. This is used to create per-VM writable copies of the rootfs
// image so each VM has its own isolated filesystem (VAL-VM-005).
func (m *Manager) copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("create dir %s: %w", filepath.Dir(dst), err)
	}

	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open src %s: %w", src, err)
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("create dst %s: %w", dst, err)
	}
	defer out.Close()

	if _, err := out.ReadFrom(in); err != nil {
		return fmt.Errorf("copy %s to %s: %w", src, dst, err)
	}
	return nil
}

// createDataImage creates a sparse ext4 filesystem image for per-VM
// mutable state. The image size is specified in megabytes.
// This is used by the microvm.nix approach where the rootfs is read-only
// (erofs store disk) and per-VM mutable state goes on a separate volume.
func (m *Manager) createDataImage(path string, sizeMB int) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create data image dir %s: %w", filepath.Dir(path), err)
	}

	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return fmt.Errorf("create data image %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close data image %s: %w", path, err)
	}

	// Create a sparse file of the desired size.
	if err := os.Truncate(path, int64(sizeMB)*1024*1024); err != nil {
		return fmt.Errorf("truncate data image %s: %w", path, err)
	}

	// Format as ext4 using mkfs.ext4.
	// The -F flag forces creation without confirmation.
	// The -L flag sets a filesystem label for easy identification.
	mkfsBin := findBinary("mkfs.ext4", "/run/current-system/sw/bin/mkfs.ext4")
	cmd := exec.Command(mkfsBin, "-F", "-L", "go-choir-data", path)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("mkfs.ext4 data image %s: %w (%s)", path, err, string(output))
	}

	return nil
}

// createTapDevice creates a TAP network device for Firecracker VM
// networking. Firecracker requires the TAP device to pre-exist on the
// host before the VM can use it. The vmctl service needs CAP_NET_ADMIN
// to create TAP devices, which is configured in node-b.nix.
//
// The tap device is given a host-side IP in the 172.X.0.1/30 range,
// and the guest is expected to configure 172.X.0.2 as its IP. NAT
// masquerading is set up so the guest can reach the host's localhost
// services (gateway, etc.).
func (m *Manager) createTapDevice(name string) error {
	ipBin := findBinary("ip", "/run/current-system/sw/bin/ip")

	// Check if the tap device already exists.
	checkCmd := exec.Command(ipBin, "link", "show", name)
	if err := checkCmd.Run(); err == nil {
		return nil // already exists
	}

	// Create the tap device.
	createCmd := exec.Command(ipBin, "tuntap", "add", "dev", name, "mode", "tap")
	if err := createCmd.Run(); err != nil {
		return fmt.Errorf("create tap device %s: %w", name, err)
	}

	// Bring the interface up.
	upCmd := exec.Command(ipBin, "link", "set", name, "up")
	if err := upCmd.Run(); err != nil {
		return fmt.Errorf("bring up tap device %s: %w", name, err)
	}

	log.Printf("vmmanager: created tap device %s", name)
	return nil
}

// deleteTapDevice removes a TAP network device and its associated
// deleteTapDevice removes a TAP network device and its associated
// iptables rules after a VM stops.
func (m *Manager) deleteTapDevice(name string) {
	ipBin := findBinary("ip", "/run/current-system/sw/bin/ip")
	iptBin := findBinary("iptables", "/run/current-system/sw/bin/iptables")

	comment := fmt.Sprintf("go-choir-vm-%s", name)

	// Remove all iptables rules associated with this VM's comment tag.
	// We clean up PREROUTING, OUTPUT, POSTROUTING, and FORWARD chains
	// to ensure no stale rules leak between VM lifecycles.
	for _, chain := range []string{"PREROUTING", "OUTPUT", "POSTROUTING"} {
		_ = exec.Command("sh", "-c",
			fmt.Sprintf("%s -t nat -S %s | grep '%s' | cut -d' ' -f2- | while read rule; do %s -t nat -D %s $rule 2>/dev/null; done", iptBin, chain, comment, iptBin, chain)).Run()
	}
	for _, chain := range []string{"FORWARD"} {
		_ = exec.Command("sh", "-c",
			fmt.Sprintf("%s -S %s | grep '%s' | cut -d' ' -f2- | while read rule; do %s -D %s $rule 2>/dev/null; done", iptBin, chain, comment, iptBin, chain)).Run()
	}
	// Also remove rules by tap device name for older rules that may not
	// have the comment tag.
	for _, chain := range []string{"PREROUTING", "OUTPUT", "POSTROUTING"} {
		_ = exec.Command("sh", "-c",
			fmt.Sprintf("%s -t nat -S %s | grep '%s' | cut -d' ' -f2- | while read rule; do %s -t nat -D %s $rule 2>/dev/null; done", iptBin, chain, name, iptBin, chain)).Run()
	}
	_ = exec.Command("sh", "-c",
		fmt.Sprintf("%s -S FORWARD | grep '%s' | cut -d' ' -f2- | while read rule; do %s -D FORWARD $rule 2>/dev/null; done", iptBin, name, iptBin)).Run()

	cmd := exec.Command(ipBin, "link", "del", name)
	if err := cmd.Run(); err != nil {
		log.Printf("vmmanager: warning: could not delete tap device %s: %v", name, err)
	}
}

// setupHostNetworking configures the host-side networking for a VM's
// tap device. It assigns the host IP address to the tap interface and
// sets up iptables rules to:
//   - Forward traffic from the assigned host port to the guest (DNAT)
//   - Masquerade guest traffic so the guest can reach host services
//     like the gateway at 127.0.0.1:8084 (SNAT/MASQUERADE)
//   - Allow forwarding between the tap device and the host
//
// Without masquerading, the guest can send packets to the host but the
// host's reply packets don't route back to the guest's /30 subnet.
// The MASQUERADE rule makes host replies go back through the tap device.
func (m *Manager) setupHostNetworking(tapName, hostIP string, hostPort int, guestIP string, guestPort int) error {
	ipBin := findBinary("ip", "/run/current-system/sw/bin/ip")
	iptBin := findBinary("iptables", "/run/current-system/sw/bin/iptables")

	// Assign the host IP to the tap device.
	addrCmd := exec.Command(ipBin, "addr", "add", hostIP+"/30", "dev", tapName)
	if err := addrCmd.Run(); err != nil {
		log.Printf("vmmanager: note: ip addr add %s/30 on %s: %v (may already be set)", hostIP, tapName, err)
	}

	// Enable IP forwarding globally.
	_ = os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1"), 0o644)

	// Enable route_localnet on the tap device. This allows the host to
	// route packets between the guest's subnet and 127.0.0.1 (localhost).
	// Without this, the DNAT rules that redirect guest→gateway traffic
	// (172.X.0.1:8084 → 127.0.0.1:8084) would be silently dropped because
	// the kernel treats 127.0.0.0/8 as a martian source on non-loopback
	// interfaces. This is critical for the guest to reach the gateway.
	routeLocalnet := fmt.Sprintf("/proc/sys/net/ipv4/conf/%s/route_localnet", tapName)
	if err := os.WriteFile(routeLocalnet, []byte("1"), 0o644); err != nil {
		log.Printf("vmmanager: warning: could not enable route_localnet on %s: %v", tapName, err)
	}

	// Also enable accept_local so the host accepts packets from local
	// subnets on the tap interface.
	acceptLocal := fmt.Sprintf("/proc/sys/net/ipv4/conf/%s/accept_local", tapName)
	_ = os.WriteFile(acceptLocal, []byte("1"), 0o644)

	// Allow forwarding to/from the tap device.
	// These ACCEPT rules ensure packets can flow between the tap device
	// and the rest of the host networking stack.
	_ = exec.Command(iptBin, "-A", "FORWARD",
		"-i", tapName, "-j", "ACCEPT").Run()
	_ = exec.Command(iptBin, "-A", "FORWARD",
		"-o", tapName, "-j", "ACCEPT").Run()

	// Set up MASQUERADE (SNAT) for outbound guest traffic.
	// This is critical: without it, the guest can send packets to the
	// host (e.g., to 127.0.0.1:8084 for gateway) but the host's reply
	// packets don't route back to the guest. MASQUERADE rewrites the
	// source IP to the host's IP so replies come back through the tap.
	comment := fmt.Sprintf("go-choir-vm-%s", tapName)
	_ = exec.Command(iptBin, "-t", "nat", "-A", "POSTROUTING",
		"-s", guestIP+"/30",
		"-o", "lo",
		"-j", "MASQUERADE",
		"-m", "comment", "--comment", comment).Run()

	// Also masquerade traffic going out through the default interface
	// (in case the guest needs internet access for any reason).
	_ = exec.Command(iptBin, "-t", "nat", "-A", "POSTROUTING",
		"-s", guestIP+"/30",
		"!", "-d", guestIP+"/30",
		"-j", "MASQUERADE",
		"-m", "comment", "--comment", comment).Run()

	// Set up DNAT: traffic to 127.0.0.1:hostPort → guestIP:guestPort.
	// This lets vmctl and other host processes reach the guest sandbox
	// via localhost on the assigned host port.
	_ = exec.Command(iptBin, "-t", "nat", "-A", "OUTPUT",
		"-p", "tcp", "--dport", fmt.Sprintf("%d", hostPort),
		"-d", "127.0.0.1",
		"-j", "DNAT", "--to-destination", fmt.Sprintf("%s:%d", guestIP, guestPort),
		"-m", "comment", "--comment", comment).Run()

	// Set up DNAT: guest→host traffic for the gateway port.
	// The guest sends packets to hostIP:8084 (the gateway port) but the
	// gateway only listens on 127.0.0.1:8084. This PREROUTING rule
	// rewrites the destination so the guest can reach the gateway.
	// Without this, the guest cannot route LLM calls through the host-side
	// gateway, and provider-backed responses would be impossible.
	_ = exec.Command(iptBin, "-t", "nat", "-A", "PREROUTING",
		"-p", "tcp", "--dport", "8084",
		"-d", hostIP,
		"-j", "DNAT", "--to-destination", "127.0.0.1:8084",
		"-m", "comment", "--comment", comment).Run()

	return nil
}

// findBinary locates a binary by name, falling back to common NixOS paths.
func findBinary(name string, fallback string) string {
	// Try PATH lookup first.
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	// Try the fallback path.
	if _, err := os.Stat(fallback); err == nil {
		return fallback
	}
	// Try common NixOS system paths.
	for _, dir := range []string{"/run/current-system/sw/bin", "/usr/bin", "/bin"} {
		candidate := dir + "/" + name
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	// Last resort: just use the name and hope for the best.
	return name
}

// healthCheckLoop periodically checks the health of all running VMs.
func (m *Manager) healthCheckLoop() {
	defer close(m.healthDone)

	ticker := time.NewTicker(m.cfg.HealthCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-m.healthCancel:
			return
		case <-ticker.C:
			m.checkAllHealth()
		}
	}
}

// checkAllHealth checks the health of all running VMs.
func (m *Manager) checkAllHealth() {
	m.mu.RLock()
	var vmIDs []string
	for id, inst := range m.vms {
		if inst.State == StateRunning {
			vmIDs = append(vmIDs, id)
		}
	}
	m.mu.RUnlock()

	for _, id := range vmIDs {
		healthy, _ := m.CheckHealth(id)
		if !healthy {
			log.Printf("vmmanager: VM %s is unhealthy", id)
		}
	}
}
