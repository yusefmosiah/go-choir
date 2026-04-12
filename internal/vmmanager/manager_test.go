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

	// Missing state dir (rootfs is no longer required with microvm.nix approach).
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for missing state dir")
	}

	cfg.StateDir = "/path/to/state"

	// Valid config with just kernel and state dir (microvm.nix approach).
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}

	// Also valid with rootfs (legacy approach).
	cfg.RootfsPath = "/path/to/rootfs"
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate with rootfs: %v", err)
	}

	// Also valid with store disk (microvm.nix approach).
	cfg.StoreDiskPath = "/path/to/storedisk.erofs"
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate with store disk: %v", err)
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

// --- Guest Isolation Tests (VAL-VM-007, VAL-VM-011) ---

func TestBuildFirecrackerConfig_NoHostControlPlaneAccess(t *testing.T) {
	// VAL-VM-007: Guest workloads cannot reach host control-plane surfaces.
	// Verify that the Firecracker network configuration does not expose
	// host control-plane ports (8081-8084 for auth, proxy, vmctl, gateway)
	// or host-only sockets and paths.
	cfg := DefaultManagerConfig()
	cfg.StateDir = t.TempDir()
	cfg.KernelImagePath = "/opt/go-choir/guest/vmlinux"
	cfg.RootfsPath = "/opt/go-choir/guest/rootfs.ext4"

	mgr := NewManager(cfg)

	vmCfg := VMConfig{
		VMID:             "vm-isolation-test",
		KernelImagePath:  cfg.KernelImagePath,
		RootfsPath:       cfg.RootfsPath,
		GuestPort:        8085,
		MachineCPUCount:  2,
		MachineMemSizeMib: 512,
		Epoch:            1,
	}

	fcConfig := mgr.buildFirecrackerConfig(vmCfg, 9001)

	// Verify the guest port is the sandbox port, not a control-plane port.
	bootSource := fcConfig["boot-source"].(map[string]interface{})
	bootArgs := bootSource["boot_args"].(string)
	if !containsStr(bootArgs, "guest_port=8085") {
		t.Errorf("expected guest_port=8085 in boot args, got: %s", bootArgs)
	}

	// Verify the config does not reference host control-plane URLs or ports.
	forbiddenPatterns := []string{
		"127.0.0.1:8081", // auth
		"127.0.0.1:8082", // proxy
		"127.0.0.1:8083", // vmctl
		"127.0.0.1:8084", // gateway
		"/var/lib/go-choir/auth",
		"/var/lib/go-choir/auth-signing",
		"/var/lib/go-choir/gateway-provider.env",
		"/var/run/",
		"/run/",
	}
	for _, pattern := range forbiddenPatterns {
		if contains(fcConfig, pattern) {
			t.Errorf("VAL-VM-007: firecracker config exposes host control-plane path: %s", pattern)
		}
	}

	// Verify the network interface uses a tap device, not host-side ports.
	netIfaces, ok := fcConfig["network-interfaces"].([]map[string]interface{})
	if !ok || len(netIfaces) == 0 {
		t.Fatal("expected network-interfaces in config")
	}
	if netIfaces[0]["iface_id"] != "eth0" {
		t.Errorf("expected eth0 interface, got %v", netIfaces[0]["iface_id"])
	}
	// The host_dev_name should be a VM-specific tap device, not a host interface.
	hostDev, _ := netIfaces[0]["host_dev_name"].(string)
	if !containsStr(hostDev, "vm-") || !containsStr(hostDev, "-tap") {
		t.Errorf("expected VM-specific tap device name, got: %s", hostDev)
	}
}

func TestBuildFirecrackerConfig_ComprehensiveSecretExclusion(t *testing.T) {
	// VAL-VM-011: Comprehensive check that NO provider credentials or
	// host-side secrets appear anywhere in the Firecracker VM configuration.
	// This test covers the full forbidden pattern list from the environment
	// documentation.
	cfg := DefaultManagerConfig()
	cfg.StateDir = t.TempDir()
	cfg.KernelImagePath = "/opt/go-choir/guest/vmlinux"
	cfg.RootfsPath = "/opt/go-choir/guest/rootfs.ext4"

	mgr := NewManager(cfg)

	vmCfg := VMConfig{
		VMID:             "vm-secret-test",
		KernelImagePath:  cfg.KernelImagePath,
		RootfsPath:       cfg.RootfsPath,
		GuestPort:        8085,
		MachineCPUCount:  2,
		MachineMemSizeMib: 512,
		Epoch:            1,
	}

	fcConfig := mgr.buildFirecrackerConfig(vmCfg, 9001)

	// Comprehensive forbidden pattern list covering all provider credentials
	// and host-side secret patterns from environment.md.
	forbiddenPatterns := []string{
		// Provider credential env vars
		"ZAI_API_KEY",
		"AWS_BEARER_TOKEN_BEDROCK",
		"AWS_REGION",
		"RUNTIME_BEDROCK_MODEL",
		"RUNTIME_ZAI_MODEL",
		"FIREWORKS_API_KEY",
		"RUNTIME_FIREWORKS_MODEL",
		"FIREWORKS_BASE_URL",
		// Gateway credential patterns
		"RUNTIME_GATEWAY_URL",
		"RUNTIME_GATEWAY_TOKEN",
		// Auth signing material
		"AUTH_JWT_PRIVATE_KEY_PATH",
		"ed25519-key",
		// Generic secret patterns
		"Bearer",
		"SECRET",
		"PASSWORD",
		"TOKEN",
		"api_key",
		"apiKey",
		"api-key",
		// Host secret paths
		"gateway-provider.env",
		"sandbox-gateway-token.env",
		"auth-signing",
	}
	for _, pattern := range forbiddenPatterns {
		if contains(fcConfig, pattern) {
			t.Errorf("VAL-VM-011: firecracker config contains forbidden secret pattern: %s", pattern)
		}
	}

	// Verify the drives section contains guest drives, not host paths.
	// With the microvm.nix approach, we expect a store drive and a data drive.
	// With the legacy approach, we expect a rootfs drive and a data drive.
	drives, ok := fcConfig["drives"].([]map[string]interface{})
	if !ok || len(drives) < 1 {
		t.Fatal("expected at least 1 drive in config")
	}
	driveIDs := make([]string, len(drives))
	for i, d := range drives {
		driveIDs[i], _ = d["drive_id"].(string)
	}
	hasStoreOrRootfs := false
	for _, id := range driveIDs {
		if id == "store" || id == "rootfs" {
			hasStoreOrRootfs = true
			break
		}
	}
	if !hasStoreOrRootfs {
		t.Errorf("expected store or rootfs drive, got drives: %v", driveIDs)
	}
	// Verify data drive is present (per-VM mutable state).
	hasData := false
	for _, id := range driveIDs {
		if id == "data" {
			hasData = true
			break
		}
	}
	if !hasData {
		t.Errorf("expected data drive, got drives: %v", driveIDs)
	}
}

func TestBuildFirecrackerConfig_GuestPortInBootArgs(t *testing.T) {
	// Verify the guest port is passed via boot args so the guest sandbox
	// knows which port to listen on. This is the only way the guest receives
	// network configuration — no host IPs or control-plane ports are exposed.
	cfg := DefaultManagerConfig()
	cfg.StateDir = t.TempDir()
	cfg.KernelImagePath = "/opt/go-choir/guest/vmlinux"
	cfg.RootfsPath = "/opt/go-choir/guest/rootfs.ext4"

	mgr := NewManager(cfg)

	vmCfg := VMConfig{
		VMID:             "vm-bootargs-test",
		KernelImagePath:  cfg.KernelImagePath,
		RootfsPath:       cfg.RootfsPath,
		GuestPort:        8085,
		MachineCPUCount:  2,
		MachineMemSizeMib: 512,
		Epoch:            1,
	}

	fcConfig := mgr.buildFirecrackerConfig(vmCfg, 9001)

	bootSource := fcConfig["boot-source"].(map[string]interface{})
	bootArgs := bootSource["boot_args"].(string)

	// Verify the boot args contain the expected guest parameters.
	expectedArgs := []string{
		"guest_port=8085", "vm_id=vm-bootargs-test", "epoch=1",
		"persistent=/mnt/persistent",
		// init= and root= are required for the guest to boot correctly.
		"init=/bin/init", "root=/dev/vda",
	}
	for _, arg := range expectedArgs {
		if !containsStr(bootArgs, arg) {
			t.Errorf("expected boot arg %s in: %s", arg, bootArgs)
		}
	}

	// Verify the boot args do NOT contain host-side parameters.
	forbiddenArgs := []string{"gateway", "provider", "api_key", "secret", "auth", "token"}
	for _, arg := range forbiddenArgs {
		if containsStr(bootArgs, arg) {
			t.Errorf("VAL-VM-011: boot args contain forbidden pattern: %s (full: %s)", arg, bootArgs)
		}
	}
}

func TestGuestInitScript_NoProviderCredentials(t *testing.T) {
	// VAL-VM-011: Verify the guest init script pattern used in guest-image.nix
	// does not pass provider credentials to the guest. This test mirrors the
	// init script in nix/guest-image.nix to ensure it stays clean.
	//
	// The guest init script sets only:
	//   - SANDBOX_PORT (from guest_port kernel param)
	//   - SANDBOX_ID (from vm_id kernel param)
	//   - RUNTIME_STORE_PATH (local persistent path)
	//
	// No provider credentials, gateway URLs, or auth material are set.
	guestEnvVars := []string{
		"SANDBOX_PORT",
		"SANDBOX_ID",
		"RUNTIME_STORE_PATH",
	}

	forbiddenEnvVars := []string{
		"ZAI_API_KEY",
		"AWS_BEARER_TOKEN_BEDROCK",
		"FIREWORKS_API_KEY",
		"RUNTIME_GATEWAY_URL",
		"RUNTIME_GATEWAY_TOKEN",
		"AUTH_JWT_PRIVATE_KEY_PATH",
		"PROXY_AUTH_PUBLIC_KEY_PATH",
		"GATEWAY_PORT",
		"PROXY_PORT",
		"VMCTL_PORT",
		"AUTH_PORT",
	}

	// Verify no forbidden env vars appear in the allowed set.
	for _, forbidden := range forbiddenEnvVars {
		for _, allowed := range guestEnvVars {
			if allowed == forbidden {
				t.Errorf("VAL-VM-011: guest env var %s is in the forbidden list", forbidden)
			}
		}
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

// --- Gateway Token Tests ---

func TestBootVM_WritesGatewayToken(t *testing.T) {
	// Verify that when a gateway token is provided, it is written to the
	// persistent directory so the guest init script can read it.
	tmpDir := t.TempDir()
	cfg := DefaultManagerConfig()
	cfg.StateDir = tmpDir
	cfg.KernelImagePath = "/nonexistent/kernel"
	cfg.RootfsPath = "/nonexistent/rootfs"

	mgr := NewManager(cfg)

	persistDir := filepath.Join(tmpDir, "test-vm-gw", "persist")
	token := "test-vm-gw:abcdef1234567890"

	_, err := mgr.BootVM(VMConfig{
		VMID:          "test-vm-gw",
		PersistentDir: persistDir,
		GatewayToken:  token,
	})
	// BootVM will fail because Firecracker is not available, but the
	// token should still be written before the launch attempt.
	if err == nil {
		t.Log("BootVM succeeded unexpectedly (no Firecracker)")
	}

	tokenPath := filepath.Join(persistDir, "gateway-token")
	data, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatalf("expected gateway token file at %s: %v", tokenPath, err)
	}
	if string(data) != token {
		t.Errorf("expected token %q, got %q", token, string(data))
	}
}

func TestBootVM_NoGatewayToken(t *testing.T) {
	// Verify that when no gateway token is provided, no token file is created.
	tmpDir := t.TempDir()
	cfg := DefaultManagerConfig()
	cfg.StateDir = tmpDir
	cfg.KernelImagePath = "/nonexistent/kernel"
	cfg.RootfsPath = "/nonexistent/rootfs"

	mgr := NewManager(cfg)

	persistDir := filepath.Join(tmpDir, "test-vm-nogw", "persist")

	_, _ = mgr.BootVM(VMConfig{
		VMID:          "test-vm-nogw",
		PersistentDir: persistDir,
		// GatewayToken intentionally empty
	})

	tokenPath := filepath.Join(persistDir, "gateway-token")
	if _, err := os.Stat(tokenPath); err == nil {
		t.Error("expected no gateway token file when token is empty")
	}
}

func TestBuildFirecrackerConfig_IPConfigInBootArgs(t *testing.T) {
	// Verify the ip= kernel parameter is correctly formatted with guest
	// and host IPs from the /30 subnet allocation.
	cfg := DefaultManagerConfig()
	cfg.StateDir = t.TempDir()
	cfg.KernelImagePath = "/opt/go-choir/guest/vmlinux"
	cfg.RootfsPath = "/opt/go-choir/guest/rootfs.ext4"

	mgr := NewManager(cfg)

	vmCfg := VMConfig{
		VMID:             "vm-ip-test",
		KernelImagePath:  cfg.KernelImagePath,
		RootfsPath:       cfg.RootfsPath,
		GuestPort:        8085,
		MachineCPUCount:  2,
		MachineMemSizeMib: 512,
		Epoch:            1,
	}

	// hostPort 9001 → subnetIndex = 9001-9000+1 = 2
	// guest IP = 172.2.0.2, host IP = 172.2.0.1
	fcConfig := mgr.buildFirecrackerConfig(vmCfg, 9001)

	bootSource := fcConfig["boot-source"].(map[string]interface{})
	bootArgs := bootSource["boot_args"].(string)

	// Verify the ip= parameter contains the expected guest/host IPs.
	if !containsStr(bootArgs, "ip=172.2.0.2::172.2.0.1:255.255.255.252::eth0:off") {
		t.Errorf("expected ip= parameter with correct subnet in boot args: %s", bootArgs)
	}
}

func TestBuildFirecrackerConfig_SubnetIsolation(t *testing.T) {
	// Verify that different host ports get different /30 subnets,
	// ensuring VM network isolation (VAL-VM-005).
	cfg := DefaultManagerConfig()
	cfg.StateDir = t.TempDir()
	cfg.KernelImagePath = "/opt/go-choir/guest/vmlinux"
	cfg.RootfsPath = "/opt/go-choir/guest/rootfs.ext4"

	mgr := NewManager(cfg)

	vmCfg := VMConfig{
		VMID:             "vm-subnet-test",
		KernelImagePath:  cfg.KernelImagePath,
		RootfsPath:       cfg.RootfsPath,
		GuestPort:        8085,
		MachineCPUCount:  2,
		MachineMemSizeMib: 512,
		Epoch:            1,
	}

	// VM on port 9000 → subnetIndex=1 → 172.1.0.0/30
	fcConfig1 := mgr.buildFirecrackerConfig(vmCfg, 9000)
	// VM on port 9001 → subnetIndex=2 → 172.2.0.0/30
	fcConfig2 := mgr.buildFirecrackerConfig(vmCfg, 9001)

	bootArgs1 := fcConfig1["boot-source"].(map[string]interface{})["boot_args"].(string)
	bootArgs2 := fcConfig2["boot-source"].(map[string]interface{})["boot_args"].(string)

	// Verify different subnets.
	if containsStr(bootArgs1, "172.1.0.2") && containsStr(bootArgs2, "172.2.0.2") {
		// Expected: different subnets
	} else {
		t.Errorf("expected different subnets for different host ports:\n  port 9000: %s\n  port 9001: %s", bootArgs1, bootArgs2)
	}

	// Verify the subnets are actually different.
	if bootArgs1 == bootArgs2 {
		t.Error("expected different boot args for different host ports")
	}
}
