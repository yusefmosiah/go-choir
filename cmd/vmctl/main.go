package main

import (
	"log"
	"os"
	"time"

	"github.com/yusefmosiah/go-choir/internal/server"
	"github.com/yusefmosiah/go-choir/internal/vmctl"
	"github.com/yusefmosiah/go-choir/internal/vmmanager"
)

func main() {
	port := server.PortFromEnv("VMCTL_PORT", "8083")

	// The sandbox URL base is where VM-backed sandbox runtimes are
	// reachable. In host-process mode this is the local sandbox.
	// In production with Firecracker, vmctl will return per-VM URLs.
	sandboxURLBase := envOr("VMCTL_SANDBOX_URL_BASE", "http://127.0.0.1:8085")

	registry := vmctl.NewOwnershipRegistry(sandboxURLBase)

	// Configure the gateway URL for issuing sandbox credentials to VM guests.
	// When Firecracker VMs are active, each guest sandbox needs a token to
	// authenticate to the host-side gateway for provider access.
	if gwURL := os.Getenv("VMCTL_GATEWAY_URL"); gwURL != "" {
		registry.SetGatewayURL(gwURL)
		log.Printf("vmctl: gateway URL configured for VM token issuance")
	}

	// Configure idle timeout for automatic VM lifecycle management.
	// After this duration of inactivity, VMs transition to hibernated
	// state (VAL-VM-008, VAL-CROSS-116).
	if v := os.Getenv("VMCTL_IDLE_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			registry.SetIdleTimeout(d)
			log.Printf("vmctl: idle timeout set to %s", d)
		}
	}

	// Check if Firecracker is available on this host.
	// If so, create a VM manager for real Firecracker lifecycle management
	// and wire it to the ownership registry so that VM boot/stop/resume
	// operations are delegated to real Firecracker VMs (VAL-VM-010).
	// If not, vmctl can still run in host-process mode for local development,
	// but deployed environments should disable that fallback explicitly.
	if vmmanager.IsFirecrackerAvailable() {
		mgrCfg := vmmanager.LoadConfigFromEnv()
		if err := mgrCfg.Validate(); err != nil {
			log.Fatalf("vmctl: Firecracker config validation failed: %v", err)
		}
		mgr := vmmanager.NewManager(mgrCfg)
		mgr.Start()
		defer mgr.Stop()

		// Wire the manager to the registry via an adapter that
		// translates between the vmctl and vmmanager interfaces.
		registry.SetVMManager(&vmManagerAdapter{mgr: mgr})

		log.Printf("vmctl: Firecracker VM manager started (kernel=%s rootfs=%s)", mgrCfg.KernelImagePath, mgrCfg.RootfsPath)
	} else {
		if !vmmanager.HostProcessFallbackEnabled() {
			log.Fatal("vmctl: Firecracker not available and host-process fallback is disabled")
		}
		log.Printf("vmctl: Firecracker not available, using host-process sandbox mode")
	}

	handler := vmctl.NewHandler(registry)

	s := server.NewServer("vmctl", port)
	vmctl.RegisterRoutes(s, handler)

	log.Printf("vmctl: ownership registry initialized (sandbox_url_base=%s)", sandboxURLBase)
	s.Start()
}

// vmManagerAdapter adapts the vmmanager.Manager to the vmctl.VMManager
// interface. This adapter translates between the vmctl ownership types
// and the vmmanager VM lifecycle types.
type vmManagerAdapter struct {
	mgr *vmmanager.Manager
}

func (a *vmManagerAdapter) BootVM(cfg vmctl.VMManagerConfig) (*vmctl.VMInstanceInfo, error) {
	inst, err := a.mgr.BootVM(vmmanager.VMConfig{
		VMID:              cfg.VMID,
		KernelImagePath:   cfg.KernelImagePath,
		RootfsPath:        cfg.RootfsPath,
		GuestPort:         cfg.GuestPort,
		MachineCPUCount:   cfg.MachineCPUCount,
		MachineMemSizeMib: cfg.MachineMemSizeMib,
		PersistentDir:     cfg.PersistentDir,
		GatewayToken:      cfg.GatewayToken,
	})
	if err != nil {
		return nil, err
	}
	return &vmctl.VMInstanceInfo{
		HostURL: inst.HostURL,
		Epoch:   inst.Config.Epoch,
		Healthy: inst.Healthy,
		State:   string(inst.State),
	}, nil
}

func (a *vmManagerAdapter) StopVM(vmID string) error {
	return a.mgr.StopVM(vmID)
}

func (a *vmManagerAdapter) HibernateVM(vmID string) error {
	return a.mgr.HibernateVM(vmID)
}

func (a *vmManagerAdapter) ResumeVM(vmID string) (*vmctl.VMInstanceInfo, error) {
	inst, err := a.mgr.ResumeVM(vmID)
	if err != nil {
		return nil, err
	}
	return &vmctl.VMInstanceInfo{
		HostURL: inst.HostURL,
		Epoch:   inst.Config.Epoch,
		Healthy: inst.Healthy,
		State:   string(inst.State),
	}, nil
}

func (a *vmManagerAdapter) RecoverVM(vmID string) (*vmctl.VMInstanceInfo, error) {
	inst, err := a.mgr.RecoverVM(vmID)
	if err != nil {
		return nil, err
	}
	return &vmctl.VMInstanceInfo{
		HostURL: inst.HostURL,
		Epoch:   inst.Config.Epoch,
		Healthy: inst.Healthy,
		State:   string(inst.State),
	}, nil
}

func (a *vmManagerAdapter) GetVM(vmID string) *vmctl.VMInstanceInfo {
	inst := a.mgr.GetVM(vmID)
	if inst == nil {
		return nil
	}
	return &vmctl.VMInstanceInfo{
		HostURL: inst.HostURL,
		Epoch:   inst.Config.Epoch,
		Healthy: inst.Healthy,
		State:   string(inst.State),
	}
}

func (a *vmManagerAdapter) CheckHealth(vmID string) (bool, error) {
	return a.mgr.CheckHealth(vmID)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
