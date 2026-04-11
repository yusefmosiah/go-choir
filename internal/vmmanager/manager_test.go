package vmmanager

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewManager(t *testing.T) {
	cfg := DefaultManagerConfig()
	mgr := NewManager(cfg)

	if mgr == nil {
		t.Fatal("expected non-nil manager")
	}
	if mgr.nextPort != cfg.HostBasePort {
		t.Errorf("expected nextPort=%d, got %d", cfg.HostBasePort, mgr.nextPort)
	}
}

func TestManagerDefaultConfig(t *testing.T) {
	cfg := DefaultManagerConfig()

	if cfg.GuestPort != 8085 {
		t.Errorf("expected GuestPort=8085, got %d", cfg.GuestPort)
	}
	if cfg.HostBasePort != 9000 {
		t.Errorf("expected HostBasePort=9000, got %d", cfg.HostBasePort)
	}
	if cfg.MachineCPUCount != 2 {
		t.Errorf("expected MachineCPUCount=2, got %d", cfg.MachineCPUCount)
	}
	if cfg.MachineMemSizeMib != 512 {
		t.Errorf("expected MachineMemSizeMib=512, got %d", cfg.MachineMemSizeMib)
	}
	if cfg.HealthCheckInterval != 15*time.Second {
		t.Errorf("expected HealthCheckInterval=15s, got %s", cfg.HealthCheckInterval)
	}
}

func TestManagerBootVMRequiresKernelAndRootfs(t *testing.T) {
	// BootVM should fail when no kernel/rootfs is configured.
	tmpDir := t.TempDir()
	cfg := DefaultManagerConfig()
	cfg.StateDir = tmpDir
	// Deliberately leave KernelImagePath and RootfsPath empty.

	mgr := NewManager(cfg)
	_, err := mgr.BootVM(VMConfig{
		VMID:          "test-vm-1",
		PersistentDir: filepath.Join(tmpDir, "persist"),
	})

	if err == nil {
		t.Error("expected error when kernel/rootfs not configured")
	}
}

func TestManagerBuildFirecrackerConfig_NoSecrets(t *testing.T) {
	// VAL-VM-011: The Firecracker config must NOT contain provider
	// credentials or any secret material.
	cfg := DefaultManagerConfig()
	cfg.StateDir = t.TempDir()
	cfg.KernelImagePath = "/opt/go-choir/guest/vmlinux"
	cfg.RootfsPath = "/opt/go-choir/guest/rootfs.ext4"

	mgr := NewManager(cfg)

	vmCfg := VMConfig{
		VMID:             "vm-test-123",
		KernelImagePath:  cfg.KernelImagePath,
		RootfsPath:       cfg.RootfsPath,
		GuestPort:        8085,
		MachineCPUCount:  2,
		MachineMemSizeMib: 512,
		Epoch:            1,
	}

	fcConfig := mgr.buildFirecrackerConfig(vmCfg, 9001)

	// Verify the config is a valid map.
	if fcConfig == nil {
		t.Fatal("expected non-nil config")
	}

	// Verify boot-source exists.
	bootSource, ok := fcConfig["boot-source"].(map[string]interface{})
	if !ok {
		t.Fatal("expected boot-source in config")
	}

	// Check boot args contain the VM ID and epoch but NO secrets.
	bootArgs, _ := bootSource["boot_args"].(string)
	if bootArgs == "" {
		t.Error("expected non-empty boot_args")
	}

	// VAL-VM-011: Verify NO secret patterns in the config.
	forbidden := []string{
		"Bearer", "AWS_", "SECRET", "PASSWORD", "TOKEN",
		"api_key", "apiKey", "api-key",
		"ZAI_API_KEY", "AWS_BEARER_TOKEN_BEDROCK", "FIREWORKS_API_KEY",
	}
	for _, pattern := range forbidden {
		if contains(fcConfig, pattern) {
			t.Errorf("VAL-VM-011: firecracker config contains forbidden pattern: %s", pattern)
		}
	}

	// Verify VM ID and epoch are in boot args.
	if !containsStr(bootArgs, "vm_id=vm-test-123") {
		t.Errorf("expected vm_id in boot args: %s", bootArgs)
	}
	if !containsStr(bootArgs, "epoch=1") {
		t.Errorf("expected epoch in boot args: %s", bootArgs)
	}

	// Verify machine config.
	machineCfg, ok := fcConfig["machine-config"].(map[string]interface{})
	if !ok {
		t.Fatal("expected machine-config in config")
	}
	if machineCfg["vcpu_count"] != 2 {
		t.Errorf("expected vcpu_count=2, got %v", machineCfg["vcpu_count"])
	}
	if machineCfg["mem_size_mib"] != 512 {
		t.Errorf("expected mem_size_mib=512, got %v", machineCfg["mem_size_mib"])
	}
}

func TestManagerEpochPersistence(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := DefaultManagerConfig()
	cfg.StateDir = tmpDir

	mgr := NewManager(cfg)

	// Save an epoch.
	if err := mgr.saveEpoch("test-vm-1", 42); err != nil {
		t.Fatalf("saveEpoch: %v", err)
	}

	// Load it back.
	epoch, err := mgr.loadEpoch("test-vm-1")
	if err != nil {
		t.Fatalf("loadEpoch: %v", err)
	}
	if epoch != 42 {
		t.Errorf("expected epoch=42, got %d", epoch)
	}

	// Nonexistent VM returns error.
	_, err = mgr.loadEpoch("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent epoch")
	}
}

func TestManagerGetListRemoveVM(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := DefaultManagerConfig()
	cfg.StateDir = tmpDir

	mgr := NewManager(cfg)

	// Get nonexistent VM.
	if v := mgr.GetVM("nonexistent"); v != nil {
		t.Error("expected nil for nonexistent VM")
	}

	// List empty.
	if vms := mgr.ListVMs(); len(vms) != 0 {
		t.Errorf("expected 0 VMs, got %d", len(vms))
	}

	// Add a VM manually.
	inst := &VMInstance{
		Config: VMConfig{VMID: "test-vm-1"},
		State:  StateStopped,
	}
	mgr.mu.Lock()
	mgr.vms["test-vm-1"] = inst
	mgr.mu.Unlock()

	// Get it back.
	if v := mgr.GetVM("test-vm-1"); v == nil || v.Config.VMID != "test-vm-1" {
		t.Error("expected to find test-vm-1")
	}

	// List should have 1.
	if vms := mgr.ListVMs(); len(vms) != 1 {
		t.Errorf("expected 1 VM, got %d", len(vms))
	}

	// Remove running VM should fail.
	inst.State = StateRunning
	if err := mgr.RemoveVM("test-vm-1"); err == nil {
		t.Error("expected error removing running VM")
	}

	// Remove stopped VM should succeed.
	inst.State = StateStopped
	if err := mgr.RemoveVM("test-vm-1"); err != nil {
		t.Errorf("RemoveVM: %v", err)
	}

	// Verify it's gone.
	if v := mgr.GetVM("test-vm-1"); v != nil {
		t.Error("expected nil after remove")
	}
}

func TestManagerMarkFailed(t *testing.T) {
	mgr := NewManager(DefaultManagerConfig())

	inst := &VMInstance{
		Config: VMConfig{VMID: "test-vm-1"},
		State:  StateRunning,
		Healthy: true,
	}
	mgr.mu.Lock()
	mgr.vms["test-vm-1"] = inst
	mgr.mu.Unlock()

	mgr.MarkFailed("test-vm-1")

	if inst.State != StateFailed {
		t.Errorf("expected failed state, got %s", inst.State)
	}
	if inst.Healthy {
		t.Error("expected unhealthy after MarkFailed")
	}
}

func TestManagerForceKillVM(t *testing.T) {
	mgr := NewManager(DefaultManagerConfig())

	inst := &VMInstance{
		Config: VMConfig{VMID: "test-vm-1"},
		State:  StateRunning,
		Healthy: true,
		done:   make(chan struct{}),
	}
	mgr.mu.Lock()
	mgr.vms["test-vm-1"] = inst
	mgr.mu.Unlock()

	if err := mgr.ForceKillVM("test-vm-1"); err != nil {
		t.Fatalf("ForceKillVM: %v", err)
	}

	if inst.State != StateFailed {
		t.Errorf("expected failed state, got %s", inst.State)
	}
}

func TestManagerStopVM(t *testing.T) {
	mgr := NewManager(DefaultManagerConfig())

	inst := &VMInstance{
		Config: VMConfig{VMID: "test-vm-1"},
		State:  StateRunning,
		Healthy: true,
		done:   make(chan struct{}),
	}
	mgr.mu.Lock()
	mgr.vms["test-vm-1"] = inst
	mgr.mu.Unlock()

	if err := mgr.StopVM("test-vm-1"); err != nil {
		t.Fatalf("StopVM: %v", err)
	}

	if inst.State != StateStopped {
		t.Errorf("expected stopped state, got %s", inst.State)
	}
	if inst.Healthy {
		t.Error("expected unhealthy after stop")
	}

	// Stop nonexistent VM.
	if err := mgr.StopVM("nonexistent"); err == nil {
		t.Error("expected error for nonexistent VM")
	}
}

func TestManagerHibernateVM(t *testing.T) {
	mgr := NewManager(DefaultManagerConfig())

	inst := &VMInstance{
		Config: VMConfig{VMID: "test-vm-1"},
		State:  StateRunning,
		Healthy: true,
		done:   make(chan struct{}),
	}
	mgr.mu.Lock()
	mgr.vms["test-vm-1"] = inst
	mgr.mu.Unlock()

	if err := mgr.HibernateVM("test-vm-1"); err != nil {
		t.Fatalf("HibernateVM: %v", err)
	}

	if inst.State != StateHibernated {
		t.Errorf("expected hibernated state, got %s", inst.State)
	}

	// Hibernate non-running VM should fail.
	inst2 := &VMInstance{
		Config: VMConfig{VMID: "test-vm-2"},
		State:  StateStopped,
	}
	mgr.mu.Lock()
	mgr.vms["test-vm-2"] = inst2
	mgr.mu.Unlock()

	if err := mgr.HibernateVM("test-vm-2"); err == nil {
		t.Error("expected error hibernating non-running VM")
	}
}

// --- Config Tests ---

func TestLoadConfigFromEnv(t *testing.T) {
	// Test with no env vars.
	cfg := LoadConfigFromEnv()
	if cfg.KernelImagePath != "" {
		t.Errorf("expected empty KernelImagePath, got %s", cfg.KernelImagePath)
	}
}

func TestConfigValidate(t *testing.T) {
	cfg := ManagerConfig{} // empty config

	// Missing kernel.
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for missing kernel")
	}

	cfg.KernelImagePath = "/path/to/kernel"

	// Missing rootfs.
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for missing rootfs")
	}

	cfg.RootfsPath = "/path/to/rootfs"

	// Missing state dir.
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for missing state dir")
	}

	cfg.StateDir = "/path/to/state"

	// Valid config.
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

func TestIsFirecrackerAvailable(t *testing.T) {
	// On macOS, Firecracker is not available.
	// This test just verifies the function doesn't panic.
	_ = IsFirecrackerAvailable()
}

func TestManagerPersistentDirCreation(t *testing.T) {
	// Verify that BootVM creates the persistent directory.
	tmpDir := t.TempDir()
	cfg := DefaultManagerConfig()
	cfg.StateDir = tmpDir
	cfg.KernelImagePath = "/nonexistent/kernel"
	cfg.RootfsPath = "/nonexistent/rootfs"

	mgr := NewManager(cfg)

	persistDir := filepath.Join(tmpDir, "test-vm-1", "persist")

	// BootVM will fail because Firecracker is not available, but it
	// should still create the persistent directory.
	_, _ = mgr.BootVM(VMConfig{
		VMID:          "test-vm-1",
		PersistentDir: persistDir,
	})

	// The persistent directory should have been created.
	if _, err := os.Stat(persistDir); os.IsNotExist(err) {
		t.Errorf("expected persistent directory to be created at %s", persistDir)
	}
}

// --- Helper functions ---

func contains(m map[string]interface{}, pattern string) bool {
	for k, v := range m {
		if containsStr(k, pattern) || containsStr(fmtVal(v), pattern) {
			return true
		}
	}
	return false
}

func fmtVal(v interface{}) string {
	switch val := v.(type) {
	case string:
		return val
	case []interface{}:
		s := ""
		for _, item := range val {
			s += fmtVal(item)
		}
		return s
	case map[string]interface{}:
		s := ""
		for k, v := range val {
			s += k + "=" + fmtVal(v)
		}
		return s
	default:
		return ""
	}
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		(len(s) > 0 && len(sub) > 0 && findSubstr(s, sub)))
}

func findSubstr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
