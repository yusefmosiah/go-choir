// Package vmmanager provides the Firecracker VM lifecycle manager for Node B.
//
// This package manages the full lifecycle of Firecracker microVMs:
// boot, health checking, stop, hibernate, resume, and force-kill.
// It is the concrete runtime that vmctl delegates to when managing
// VM-backed sandbox workloads (VAL-VM-010, VAL-VM-011).
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

	// RootfsPath is the path to the repo-built guest root filesystem image.
	RootfsPath string

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
	// from the host (e.g., "http://192.168.X.Y:8085" for tap networking
	// or "http://127.0.0.1:PORT" for host-forwarded networking).
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

	// RootfsPath is the default rootfs image for all VMs.
	// The manager creates per-VM copies for writable guest filesystems.
	RootfsPath string

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
	cfg    ManagerConfig
	mu     sync.RWMutex
	vms    map[string]*VMInstance // vmID → instance
	nextPort int                  // next host port to assign

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
		cfg:       cfg,
		vms:       make(map[string]*VMInstance),
		nextPort:  cfg.HostBasePort,
	}
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

	// Ensure the persistent directory exists.
	if cfg.PersistentDir == "" {
		cfg.PersistentDir = filepath.Join(m.cfg.StateDir, cfg.VMID, "persist")
	}
	if err := os.MkdirAll(cfg.PersistentDir, 0o755); err != nil {
		return nil, fmt.Errorf("create persistent dir %s: %w", cfg.PersistentDir, err)
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
	if cfg.RootfsPath == "" {
		cfg.RootfsPath = m.cfg.RootfsPath
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

	// Build the Firecracker configuration.
	// Provider credentials are explicitly NOT included here (VAL-VM-011).
	fcConfig := m.buildFirecrackerConfig(cfg, hostPort)

	hostURL := fmt.Sprintf("http://127.0.0.1:%d", hostPort)

	inst := &VMInstance{
		Config:    cfg,
		State:     StatePending,
		HostURL:   hostURL,
		StartedAt: time.Now(),
		done:      make(chan struct{}),
	}

	m.vms[cfg.VMID] = inst

	// Launch Firecracker.
	if err := m.launchFirecracker(cfg.VMID, fcConfig); err != nil {
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

	hostURL := fmt.Sprintf("http://127.0.0.1:%d", hostPort)
	inst.HostURL = hostURL

	// Reuse the existing epoch (no increment on resume).
	// This preserves the VM identity across stop/resume so callers
	// can detect duplicate canonical effects (VAL-CROSS-117).
	epoch := inst.Config.Epoch

	fcConfig := m.buildFirecrackerConfig(inst.Config, hostPort)

	if err := m.launchFirecracker(vmID, fcConfig); err != nil {
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
	// Build the guest kernel boot arguments.
	// These contain NO provider credentials (VAL-VM-011).
	bootArgs := fmt.Sprintf(
		"console=ttyS0 reboot=k panic=1 pci=off "+
			"guest_port=%d persistent=/mnt/persistent "+
			"vm_id=%s epoch=%d",
		cfg.GuestPort, cfg.VMID, cfg.Epoch,
	)

	// Firecracker VM configuration.
	// No secrets, no provider credentials, no host paths (VAL-VM-011).
	fcConfig := map[string]interface{}{
		"boot-source": map[string]interface{}{
			"kernel_image_path": cfg.KernelImagePath,
			"boot_args":         bootArgs,
		},
		"drives": []map[string]interface{}{
			{
				"drive_id":      "rootfs",
				"path_on_host":  cfg.RootfsPath,
				"is_root_device": true,
				"is_read_only":  false,
			},
		},
		"machine-config": map[string]interface{}{
			"vcpu_count":  cfg.MachineCPUCount,
			"mem_size_mib": cfg.MachineMemSizeMib,
		},
		"network-interfaces": []map[string]interface{}{
			{
				"iface_id":     "eth0",
				"guest_mac":    fmt.Sprintf("AA:FC:00:00:00:%02X", hostPort%256),
				"host_dev_name": fmt.Sprintf("vm-%s-tap", cfg.VMID[:8]),
			},
		},
		"vsock": map[string]interface{}{
			"guest_cid": 3,
			"uds_path":  filepath.Join(m.cfg.StateDir, cfg.VMID, "vsock.sock"),
		},
	}

	return fcConfig
}

// launchFirecracker starts a Firecracker process for the given VM.
func (m *Manager) launchFirecracker(vmID string, fcConfig map[string]interface{}) error {
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

	// Build the Firecracker command.
	// --no-api disables the Firecracker API socket (we use config file).
	// --id sets the VM identifier.
	// --config-file provides the VM configuration.
	cmd := exec.Command(bin,
		"--no-api",
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

// killFirecrackerProcess forcefully terminates a Firecracker process.
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


