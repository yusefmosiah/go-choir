// Package vmctl implements the VM ownership registry and lifecycle control
// for Mission 3. The registry maps authenticated users to VM-backed sandbox
// workloads and ensures concurrent first requests for the same user collapse
// onto a single VM assignment (VAL-VM-004).
//
// Key invariants:
//   - Each authenticated user/desktop pair receives exactly one active
//     interactive VM at a time.
//   - Different users receive distinct VMs with isolated state (VAL-VM-005).
//   - Concurrent first requests for one user converge on one assignment (VAL-VM-004).
//   - VM control endpoints are internal-only (VAL-VM-012).
//   - Invalid auth is denied before VM or gateway side effects (VAL-CROSS-110).
//   - Idle/logout lifecycle transitions only the current user's VM (VAL-VM-008).
//   - vmctl detects unhealthy guests and recovers safely (VAL-VM-009).
//   - Guest VM files/env/process args remain free of provider credentials (VAL-VM-011).
//   - Crash recovery does not duplicate canonical effects (VAL-CROSS-117).
//   - Idle stop or hibernate resumes the same user's state (VAL-CROSS-116).
package vmctl

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// VMState represents the lifecycle state of a VM.
type VMState string

const (
	// VMStateBooting means the VM is being created or started.
	VMStateBooting VMState = "booting"

	// VMStateActive means the VM is running and healthy.
	VMStateActive VMState = "active"

	// VMStateDegraded means the VM is running but unhealthy.
	VMStateDegraded VMState = "degraded"

	// VMStateStopping means the VM is being shut down.
	VMStateStopping VMState = "stopping"

	// VMStateStopped means the VM has been stopped and is not running.
	// The VM can be resumed, restoring the same user's persisted state
	// (VAL-CROSS-116, VAL-VM-008).
	VMStateStopped VMState = "stopped"

	// VMStateHibernated means the VM's persistent state has been preserved
	// and the VM is not running. Resume restores the same user state.
	// The epoch does NOT increment on resume, so callers can distinguish
	// fresh boot from resume (VAL-CROSS-117).
	VMStateHibernated VMState = "hibernated"

	// VMStateFailed means the VM failed to start or has crashed.
	VMStateFailed VMState = "failed"

	// PrimaryDesktopID is the default desktop/workspace selector used when the
	// caller does not explicitly target a branch desktop.
	PrimaryDesktopID = "primary"
)

// VMKind distinguishes interactive desktop VMs from headless worker VMs.
type VMKind string

const (
	VMKindInteractive VMKind = "interactive"
	VMKindWorker      VMKind = "worker"
)

// VMOwnership represents the assignment of a user to a specific VM.
type VMOwnership struct {
	// VMID is the unique identifier for the VM.
	VMID string `json:"vm_id"`

	// UserID is the authenticated user who owns this VM.
	UserID string `json:"user_id"`

	// DesktopID is the desktop/workspace selector this interactive VM belongs to.
	// For worker VMs, this is the parent desktop selector the worker belongs to.
	DesktopID string `json:"desktop_id"`

	// Kind distinguishes interactive desktops from headless worker VMs.
	Kind VMKind `json:"kind,omitempty"`

	// ParentDesktopID records the source desktop when this desktop was forked
	// from another interactive desktop.
	ParentDesktopID string `json:"parent_desktop_id,omitempty"`

	// WorkerID is the typed handle identifier for worker VMs. Empty for
	// interactive desktop VMs.
	WorkerID string `json:"worker_id,omitempty"`

	// ParentAgentID is the durable super/agent identity that requested a worker.
	ParentAgentID string `json:"parent_agent_id,omitempty"`

	// TrajectoryID ties a worker request back to the user-visible workflow.
	TrajectoryID string `json:"trajectory_id,omitempty"`

	// Purpose is the caller-provided reason for this worker VM.
	Purpose string `json:"purpose,omitempty"`

	// MachineClass is the requested resource envelope for this VM.
	MachineClass string `json:"machine_class,omitempty"`

	// Published indicates whether this desktop is user-switchable through the
	// normal browser/proxy routing path. Background candidate desktops stay
	// unpublished until explicitly published by the control plane.
	Published bool `json:"published"`

	// SandboxURL is the URL where this VM's sandbox runtime is reachable.
	SandboxURL string `json:"sandbox_url"`

	// State is the current lifecycle state of the VM.
	State VMState `json:"state"`

	// CreatedAt is when the VM was first created.
	CreatedAt time.Time `json:"created_at"`

	// LastActiveAt is when the VM was last used.
	LastActiveAt time.Time `json:"last_active_at"`

	// SandboxCredential is the credential issued by the gateway for this VM.
	// It is used to authenticate sandbox-to-gateway provider requests.
	SandboxCredential string `json:"-"`

	// Epoch is the monotonically increasing boot counter for this VM.
	// On fresh boot or recovery, the epoch increments. On resume from
	// hibernate, the epoch stays the same. Callers can use epoch to
	// detect whether a VM went through a fresh boot vs. a resume,
	// which prevents duplicate canonical effects (VAL-CROSS-117).
	Epoch int64 `json:"epoch"`

	// StoppedBy indicates why the VM was stopped. Empty if running.
	// Valid values: "idle", "logout", "recovery", "manual".
	StoppedBy string `json:"stopped_by,omitempty"`
}

// WorkerRequest is the typed internal vmctl request for a background worker VM.
type WorkerRequest struct {
	UserID        string `json:"user_id"`
	DesktopID     string `json:"desktop_id,omitempty"`
	ParentAgentID string `json:"parent_agent_id"`
	TrajectoryID  string `json:"trajectory_id,omitempty"`
	Purpose       string `json:"purpose"`
	MachineClass  string `json:"machine_class,omitempty"`
}

// WorkerVMHandle is the typed result returned when vmctl provisions a worker VM.
type WorkerVMHandle struct {
	Kind          VMKind  `json:"kind"`
	WorkerID      string  `json:"worker_id"`
	VMID          string  `json:"vm_id"`
	UserID        string  `json:"user_id"`
	DesktopID     string  `json:"desktop_id"`
	ParentAgentID string  `json:"parent_agent_id,omitempty"`
	TrajectoryID  string  `json:"trajectory_id,omitempty"`
	Purpose       string  `json:"purpose"`
	MachineClass  string  `json:"machine_class"`
	SandboxURL    string  `json:"sandbox_url"`
	State         VMState `json:"state"`
}

// IsReady returns true if the VM is in a state that can serve routed requests.
func (o *VMOwnership) IsReady() bool {
	return o.State == VMStateActive
}

// VMManager is the interface the OwnershipRegistry uses to manage real
// Firecracker VM lifecycles. When Firecracker is available on the host,
// the registry delegates VM boot/stop/resume/recover operations to the
// concrete vmmanager.Manager. When Firecracker is not available, the
// registry runs in host-process mode with no-op VM lifecycle calls.
type VMManager interface {
	// BootVM launches a new Firecracker VM and returns its instance info.
	BootVM(cfg VMManagerConfig) (*VMInstanceInfo, error)

	// StopVM cleanly stops a running VM.
	StopVM(vmID string) error

	// HibernateVM saves VM state and stops it (persistent data preserved).
	HibernateVM(vmID string) error

	// ResumeVM resumes a stopped or hibernated VM (same epoch, same state).
	ResumeVM(vmID string) (*VMInstanceInfo, error)

	// RecoverVM force-kills and reboots a failed VM (new epoch).
	RecoverVM(vmID string) (*VMInstanceInfo, error)

	// GetVM returns the VM instance info, or nil if not found.
	GetVM(vmID string) *VMInstanceInfo

	// CheckHealth probes the VM's guest health endpoint.
	CheckHealth(vmID string) (bool, error)
}

// VMManagerConfig holds the configuration for launching a single VM,
// mirroring the vmmanager.VMConfig fields that the registry controls.
type VMManagerConfig struct {
	VMID              string
	KernelImagePath   string
	RootfsPath        string
	GuestPort         int
	MachineCPUCount   int
	MachineMemSizeMib int
	PersistentDir     string
	// GatewayToken is the credential token for the sandbox to authenticate
	// to the host-side gateway. Written to the persistent directory so the
	// guest init script can read it and set RUNTIME_GATEWAY_TOKEN.
	GatewayToken string
}

// VMInstanceInfo holds the information returned by the VM manager
// after a VM lifecycle operation.
type VMInstanceInfo struct {
	HostURL string
	Epoch   int64
	Healthy bool
	State   string
}

// OwnershipRegistry manages the mapping of users to VMs. It provides
// thread-safe VM assignment with singleflight semantics so that concurrent
// first requests for the same user collapse onto one VM assignment
// (VAL-VM-004).
//
// The registry also manages lifecycle transitions:
//   - Idle timeout: VMs idle beyond a configurable threshold transition
//     to stopped/hibernated state (VAL-VM-008, VAL-CROSS-116).
//   - Logout teardown: removing ownership on logout (VAL-VM-008).
//   - Unhealthy recovery: detecting and recovering failed VMs (VAL-VM-009).
//   - Crash dedup: epoch tracking prevents duplicate canonical effects
//     across recovery (VAL-CROSS-117).
type OwnershipRegistry struct {
	mu sync.RWMutex

	// ownerships maps user/desktop composite keys to their active VM ownership.
	ownerships map[string]*VMOwnership

	// workerVMs maps typed worker handles to their active headless child VMs.
	workerVMs map[string]*VMOwnership

	// vmByID maps VM ID to ownership for reverse lookup.
	vmByID map[string]*VMOwnership

	// pendingWaiters maps user/desktop composite keys to channels that concurrent
	// callers wait on when a VM assignment is already in progress. This
	// collapses concurrent first requests (VAL-VM-004).
	pendingWaiters map[string][]chan *VMOwnership

	// sandboxURLBase is the base URL pattern for sandbox runtimes.
	// The VM ID is appended as a path component: base + "/" + vmID
	sandboxURLBase string

	// idleTimeout is the duration after which a VM with no activity
	// is eligible for stop/hibernate. Zero means no idle timeout.
	idleTimeout time.Duration

	// epochCounter tracks the global epoch counter for VM boot tracking.
	// Each fresh boot or recovery increments this counter, providing a
	// mechanism to prevent duplicate canonical effects (VAL-CROSS-117).
	epochCounter int64

	// vmManager is the optional Firecracker VM lifecycle manager.
	// When nil, the registry operates in host-process sandbox mode where
	// all VMs share the same sandbox URL. When set, the registry delegates
	// VM lifecycle operations to this manager for real Firecracker VMs.
	vmManager VMManager

	// gatewayURL is the URL of the host-side gateway service. When set,
	// the registry issues gateway tokens for VM sandboxes before booting
	// so the guest sandbox can authenticate to the gateway.
	gatewayURL string
}

// NewOwnershipRegistry creates a new ownership registry.
// The idleTimeout parameter configures automatic VM stop after inactivity.
// Zero means no idle timeout (VMs stay active indefinitely).
func NewOwnershipRegistry(sandboxURLBase string) *OwnershipRegistry {
	if sandboxURLBase == "" {
		sandboxURLBase = "http://127.0.0.1:8085"
	}
	return &OwnershipRegistry{
		ownerships:     make(map[string]*VMOwnership),
		workerVMs:      make(map[string]*VMOwnership),
		vmByID:         make(map[string]*VMOwnership),
		pendingWaiters: make(map[string][]chan *VMOwnership),
		sandboxURLBase: sandboxURLBase,
		idleTimeout:    0, // no idle timeout by default
		epochCounter:   1,
	}
}

// SetVMManager sets the Firecracker VM lifecycle manager. When set, the
// registry delegates VM lifecycle operations to the manager instead of
// running in host-process sandbox mode. This activates real Firecracker
// VM lifecycle on Node B.
func (r *OwnershipRegistry) SetVMManager(mgr VMManager) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.vmManager = mgr
}

// SetGatewayURL configures the gateway URL for issuing sandbox tokens.
// When set, the registry will issue a gateway token for each VM before
// booting so the guest sandbox can authenticate to the gateway.
func (r *OwnershipRegistry) SetGatewayURL(url string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gatewayURL = url
}

// SetIdleTimeout configures the idle timeout for automatic VM lifecycle
// management. After this duration of inactivity, VMs are eligible for
// stop/hibernate (VAL-VM-008, VAL-CROSS-116).
func (r *OwnershipRegistry) SetIdleTimeout(d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.idleTimeout = d
}

// nextEpoch returns the next epoch value and increments the counter.
// This is used to track VM boot/recovery generations for crash dedup
// (VAL-CROSS-117).
func (r *OwnershipRegistry) nextEpoch() int64 {
	r.epochCounter++
	return r.epochCounter
}

func normalizeDesktopID(desktopID string) string {
	desktopID = strings.TrimSpace(desktopID)
	if desktopID == "" {
		return PrimaryDesktopID
	}
	return desktopID
}

func ownershipKey(userID, desktopID string) string {
	return strings.TrimSpace(userID) + "|" + normalizeDesktopID(desktopID)
}

func workerHandleFromOwnership(own *VMOwnership) *WorkerVMHandle {
	if own == nil {
		return nil
	}
	return &WorkerVMHandle{
		Kind:          VMKindWorker,
		WorkerID:      own.WorkerID,
		VMID:          own.VMID,
		UserID:        own.UserID,
		DesktopID:     own.DesktopID,
		ParentAgentID: own.ParentAgentID,
		TrajectoryID:  own.TrajectoryID,
		Purpose:       own.Purpose,
		MachineClass:  own.MachineClass,
		SandboxURL:    own.SandboxURL,
		State:         own.State,
	}
}

func normalizeWorkerMachineClass(raw string) (string, int, int, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "worker-small", "small":
		return "worker-small", 1, 256, nil
	case "worker-medium", "medium":
		return "worker-medium", 2, 512, nil
	case "worker-large", "large":
		return "worker-large", 4, 1024, nil
	default:
		return "", 0, 0, fmt.Errorf("unsupported machine_class %q", strings.TrimSpace(raw))
	}
}

// issueGatewayToken requests a gateway credential token for the given
// sandbox ID by calling the gateway's credential issuance endpoint.
// Returns the raw token string or an empty string on failure.
// Failures are logged but not fatal — the VM will still boot but
// won't be able to authenticate to the gateway until a token is provided.
func (r *OwnershipRegistry) issueGatewayToken(sandboxID string) string {
	r.mu.RLock()
	gwURL := r.gatewayURL
	r.mu.RUnlock()

	if gwURL == "" {
		return ""
	}

	// Call the gateway's credential issuance endpoint.
	// This is the same endpoint used by the host sandbox's ExecStartPre.
	body := fmt.Sprintf(`{"sandbox_id":"%s"}`, sandboxID)
	url := strings.TrimRight(gwURL, "/") + "/provider/v1/credentials/issue"

	req, err := http.NewRequestWithContext(context.Background(),
		http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		log.Printf("vmctl: gateway token request creation failed for %s: %v", sandboxID, err)
		return ""
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Caller", "true")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("vmctl: gateway token request failed for %s: %v", sandboxID, err)
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		log.Printf("vmctl: gateway token issue returned %d for %s", resp.StatusCode, sandboxID)
		return ""
	}

	var result struct {
		SandboxID       string `json:"sandbox_id"`
		SandboxIDCompat string `json:"SandboxID"`
		RawToken        string `json:"raw_token"`
		RawTokenCompat  string `json:"RawToken"`
		ExpiresAt       string `json:"expires_at"`
		ExpiresAtCompat string `json:"ExpiresAt"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		log.Printf("vmctl: gateway token response decode failed: %v", err)
		return ""
	}
	if result.RawToken == "" {
		result.RawToken = result.RawTokenCompat
	}

	return result.RawToken
}

// ResolveOrAssign resolves the VM ownership for the primary desktop of the
// given user.
func (r *OwnershipRegistry) ResolveOrAssign(userID string) (*VMOwnership, error) {
	return r.ResolveOrAssignDesktop(userID, PrimaryDesktopID)
}

// ResolveOrAssignDesktop resolves the VM ownership for the given user/desktop
// pair. If the desktop already has an active VM, it is returned. If the VM is
// still booting, concurrent callers wait for that same boot to finish instead
// of routing to a placeholder sandbox URL. If no VM exists, a new VM is
// assigned. Concurrent first requests for the same
// user/desktop pair collapse onto one assignment.
func (r *OwnershipRegistry) ResolveOrAssignDesktop(userID, desktopID string) (*VMOwnership, error) {
	desktopID = normalizeDesktopID(desktopID)
	key := ownershipKey(userID, desktopID)
	r.mu.Lock()

	// Check if the desktop already has an active ownership.
	if own, ok := r.ownerships[key]; ok {
		if own.IsReady() {
			own.LastActiveAt = time.Now()
			r.mu.Unlock()
			return own, nil
		}

		// VM exists but is stopped or hibernated. Resume it instead
		// of creating a new VM, preserving the user's state and epoch
		// (VAL-CROSS-116, VAL-CROSS-117).
		if own.State == VMStateStopped || own.State == VMStateHibernated {
			mgr := r.vmManager
			r.mu.Unlock()

			// Delegate to the real VM manager if available.
			if mgr != nil {
				info, err := mgr.ResumeVM(own.VMID)
				if err != nil {
					log.Printf("vmctl: resume failed for VM %s: %v", own.VMID, err)
					return nil, fmt.Errorf("failed to resume VM %s: %w", own.VMID, err)
				}
				r.mu.Lock()
				own.SandboxURL = info.HostURL
				r.mu.Unlock()
			}

			r.mu.Lock()
			own.State = VMStateActive
			own.LastActiveAt = time.Now()
			own.StoppedBy = ""
			r.mu.Unlock()
			log.Printf("vmctl: resumed VM %s for user %s desktop %s on resolve (epoch=%d)", own.VMID, userID, desktopID, own.Epoch)
			return own, nil
		}

		// VM exists but failed or is degraded. Create a new one
		// with a fresh epoch. Clean up the old mapping.
		delete(r.vmByID, own.VMID)
	}

	// Check if a VM assignment is already in progress for this user/desktop.
	// The zero-waiter case still means a first caller is actively booting the VM,
	// so later callers must join that in-flight boot rather than minting a second
	// VM or routing to the placeholder sandbox URL.
	if waiters, ok := r.pendingWaiters[key]; ok {
		ch := make(chan *VMOwnership, 1)
		r.pendingWaiters[key] = append(waiters, ch)
		r.mu.Unlock()

		own := <-ch
		if own == nil {
			return nil, fmt.Errorf("vm assignment failed for user %s desktop %s", userID, desktopID)
		}
		return own, nil
	}

	// We are the first caller for this user/desktop pair. Create a new VM.
	vmID := generateVMID()
	epoch := r.nextEpoch()

	own := &VMOwnership{
		VMID:         vmID,
		UserID:       userID,
		DesktopID:    desktopID,
		Kind:         VMKindInteractive,
		SandboxURL:   r.sandboxURLForVM(vmID),
		State:        VMStateBooting,
		CreatedAt:    time.Now(),
		LastActiveAt: time.Now(),
		Epoch:        epoch,
		Published:    true,
	}

	// Register pending waiters map before unlocking so other callers can find it.
	r.pendingWaiters[key] = nil

	// Store the ownership immediately in booting state.
	r.ownerships[key] = own
	r.vmByID[vmID] = own

	// Check if we have a real Firecracker VM manager.
	mgr := r.vmManager

	r.mu.Unlock()

	// Boot the real Firecracker VM if a manager is configured.
	if mgr != nil {
		// Issue a gateway token for the VM sandbox before booting.
		// The token is written to the persistent directory by the vmmanager
		// and read by the guest init script to authenticate to the gateway.
		gwToken := r.issueGatewayToken(vmID)

		info, err := mgr.BootVM(VMManagerConfig{
			VMID:              vmID,
			GuestPort:         8085,
			MachineCPUCount:   2,
			MachineMemSizeMib: 512,
			GatewayToken:      gwToken,
		})
		if err != nil {
			log.Printf("vmctl: Firecracker boot failed for VM %s: %v", vmID, err)
			r.mu.Lock()
			own.State = VMStateFailed
			waiters := r.pendingWaiters[key]
			delete(r.pendingWaiters, key)
			r.mu.Unlock()
			for _, ch := range waiters {
				ch <- nil
			}
			return nil, fmt.Errorf("failed to boot VM %s: %w", vmID, err)
		}
		r.mu.Lock()
		own.SandboxURL = info.HostURL
		own.Epoch = info.Epoch
		r.mu.Unlock()
		log.Printf("vmctl: booted Firecracker VM %s for user %s at %s (epoch=%d)", vmID, userID, info.HostURL, info.Epoch)
	}

	// Transition to active.
	r.transitionVM(vmID, VMStateActive)

	// Notify any waiters.
	r.mu.Lock()
	waiters := r.pendingWaiters[key]
	delete(r.pendingWaiters, key)
	r.mu.Unlock()

	for _, ch := range waiters {
		ch <- own
	}

	log.Printf("vmctl: assigned VM %s to user %s desktop %s", vmID, userID, desktopID)

	return own, nil
}

// ForkDesktop creates or resumes a distinct interactive VM for a target desktop
// derived from an existing source desktop. The target desktop must differ from
// the source desktop and the source desktop must already exist.
func (r *OwnershipRegistry) ForkDesktop(userID, sourceDesktopID, targetDesktopID string) (*VMOwnership, error) {
	sourceDesktopID = normalizeDesktopID(sourceDesktopID)
	targetDesktopID = normalizeDesktopID(targetDesktopID)
	if sourceDesktopID == targetDesktopID {
		return nil, fmt.Errorf("target desktop must differ from source desktop")
	}

	r.mu.RLock()
	source := r.ownerships[ownershipKey(userID, sourceDesktopID)]
	r.mu.RUnlock()
	if source == nil {
		return nil, fmt.Errorf("no source VM found for user %s desktop %s", userID, sourceDesktopID)
	}

	own, err := r.ResolveOrAssignDesktop(userID, targetDesktopID)
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	own.ParentDesktopID = sourceDesktopID
	own.LastActiveAt = time.Now()
	own.Published = false
	r.mu.Unlock()

	log.Printf("vmctl: forked desktop %s from %s for user %s onto VM %s", targetDesktopID, sourceDesktopID, userID, own.VMID)
	return own, nil
}

// RequestWorker provisions a headless child VM under an existing desktop and
// returns a typed worker handle. Workers are keyed by worker_id, not by desktop
// routing state, because multiple workers may belong to one desktop.
func (r *OwnershipRegistry) RequestWorker(req WorkerRequest) (*VMOwnership, error) {
	req.UserID = strings.TrimSpace(req.UserID)
	req.DesktopID = normalizeDesktopID(req.DesktopID)
	req.ParentAgentID = strings.TrimSpace(req.ParentAgentID)
	req.TrajectoryID = strings.TrimSpace(req.TrajectoryID)
	req.Purpose = strings.TrimSpace(req.Purpose)
	if req.UserID == "" {
		return nil, fmt.Errorf("user_id is required")
	}
	if req.ParentAgentID == "" {
		return nil, fmt.Errorf("parent_agent_id is required")
	}
	if req.Purpose == "" {
		return nil, fmt.Errorf("purpose is required")
	}
	machineClass, cpuCount, memSizeMib, err := normalizeWorkerMachineClass(req.MachineClass)
	if err != nil {
		return nil, err
	}

	r.mu.RLock()
	parent := r.ownerships[ownershipKey(req.UserID, req.DesktopID)]
	r.mu.RUnlock()
	if parent == nil {
		return nil, fmt.Errorf("no parent desktop VM found for user %s desktop %s", req.UserID, req.DesktopID)
	}

	now := time.Now()
	vmID := generateVMID()
	workerID := generateWorkerID()
	own := &VMOwnership{
		VMID:            vmID,
		UserID:          req.UserID,
		DesktopID:       req.DesktopID,
		Kind:            VMKindWorker,
		WorkerID:        workerID,
		ParentAgentID:   req.ParentAgentID,
		TrajectoryID:    req.TrajectoryID,
		Purpose:         req.Purpose,
		MachineClass:    machineClass,
		SandboxURL:      r.sandboxURLForVM(vmID),
		State:           VMStateBooting,
		CreatedAt:       now,
		LastActiveAt:    now,
		Published:       false,
		ParentDesktopID: "",
	}

	r.mu.Lock()
	own.Epoch = r.nextEpoch()
	r.workerVMs[workerID] = own
	r.vmByID[vmID] = own
	mgr := r.vmManager
	r.mu.Unlock()

	if mgr != nil {
		gwToken := r.issueGatewayToken(vmID)
		info, err := mgr.BootVM(VMManagerConfig{
			VMID:              vmID,
			GuestPort:         8085,
			MachineCPUCount:   cpuCount,
			MachineMemSizeMib: memSizeMib,
			GatewayToken:      gwToken,
		})
		if err != nil {
			log.Printf("vmctl: Firecracker boot failed for worker VM %s: %v", vmID, err)
			r.mu.Lock()
			own.State = VMStateFailed
			r.mu.Unlock()
			return nil, fmt.Errorf("failed to boot worker VM %s: %w", vmID, err)
		}
		r.mu.Lock()
		own.SandboxURL = info.HostURL
		own.Epoch = info.Epoch
		r.mu.Unlock()
		log.Printf("vmctl: booted worker VM %s for user %s desktop %s (worker_id=%s class=%s epoch=%d)", vmID, req.UserID, req.DesktopID, workerID, machineClass, own.Epoch)
	}

	r.transitionVM(vmID, VMStateActive)
	log.Printf("vmctl: assigned worker VM %s for user %s desktop %s (worker_id=%s purpose=%q)", vmID, req.UserID, req.DesktopID, workerID, req.Purpose)
	return own, nil
}

// PublishDesktop marks a background candidate desktop as user-switchable.
func (r *OwnershipRegistry) PublishDesktop(userID, desktopID string) (*VMOwnership, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	own, ok := r.ownerships[ownershipKey(userID, desktopID)]
	if !ok {
		return nil, fmt.Errorf("no VM found for user %s desktop %s", userID, normalizeDesktopID(desktopID))
	}
	own.Published = true
	own.LastActiveAt = time.Now()
	log.Printf("vmctl: published desktop %s for user %s on VM %s", own.DesktopID, userID, own.VMID)
	return own, nil
}

// GetOwnership returns the current ownership for a user's primary desktop, or
// nil if none exists.
func (r *OwnershipRegistry) GetOwnership(userID string) *VMOwnership {
	return r.GetOwnershipForDesktop(userID, PrimaryDesktopID)
}

// GetOwnershipForDesktop returns the current ownership for a specific
// user/desktop pair, or nil if none exists.
func (r *OwnershipRegistry) GetOwnershipForDesktop(userID, desktopID string) *VMOwnership {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.ownerships[ownershipKey(userID, desktopID)]
}

// GetOwnershipByVMID returns the ownership for a specific VM ID, or nil.
func (r *OwnershipRegistry) GetOwnershipByVMID(vmID string) *VMOwnership {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.vmByID[vmID]
}

// ListOwnerships returns all current ownerships.
func (r *OwnershipRegistry) ListOwnerships() []*VMOwnership {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]*VMOwnership, 0, len(r.ownerships)+len(r.workerVMs))
	for _, own := range r.ownerships {
		result = append(result, own)
	}
	for _, own := range r.workerVMs {
		result = append(result, own)
	}
	return result
}

// StopVM stops the VM for the given user's primary desktop.
func (r *OwnershipRegistry) StopVM(userID string) error {
	return r.StopVMForDesktop(userID, PrimaryDesktopID)
}

// StopVMForDesktop stops the VM for the given user/desktop pair,
// transitioning it to stopped state.
func (r *OwnershipRegistry) StopVMForDesktop(userID, desktopID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := ownershipKey(userID, desktopID)
	own, ok := r.ownerships[key]
	if !ok {
		return fmt.Errorf("no VM found for user %s desktop %s", userID, normalizeDesktopID(desktopID))
	}

	// Delegate to the real VM manager if available.
	if r.vmManager != nil && (own.State == VMStateActive || own.State == VMStateDegraded) {
		_ = r.vmManager.StopVM(own.VMID)
	}

	own.State = VMStateStopped
	own.LastActiveAt = time.Now()
	log.Printf("vmctl: stopped VM %s for user %s desktop %s", own.VMID, userID, own.DesktopID)
	return nil
}

// RemoveOwnership removes the ownership for a user's primary desktop.
func (r *OwnershipRegistry) RemoveOwnership(userID string) error {
	return r.RemoveOwnershipForDesktop(userID, PrimaryDesktopID)
}

// RemoveOwnershipForDesktop removes the ownership for a specific user/desktop
// pair entirely (e.g. after logout). The VM is stopped and the mappings are
// cleaned up.
func (r *OwnershipRegistry) RemoveOwnershipForDesktop(userID, desktopID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := ownershipKey(userID, desktopID)
	own, ok := r.ownerships[key]
	if !ok {
		return nil // already gone, idempotent
	}

	// Delegate to the real VM manager if available.
	if r.vmManager != nil && (own.State == VMStateActive || own.State == VMStateDegraded) {
		_ = r.vmManager.StopVM(own.VMID)
	}

	own.State = VMStateStopped
	delete(r.ownerships, key)
	delete(r.vmByID, own.VMID)
	for workerID, worker := range r.workerVMs {
		if worker.UserID != userID || worker.DesktopID != normalizeDesktopID(desktopID) {
			continue
		}
		if r.vmManager != nil && (worker.State == VMStateActive || worker.State == VMStateDegraded) {
			_ = r.vmManager.StopVM(worker.VMID)
		}
		worker.State = VMStateStopped
		delete(r.workerVMs, workerID)
		delete(r.vmByID, worker.VMID)
	}
	log.Printf("vmctl: removed VM %s ownership for user %s desktop %s", own.VMID, userID, own.DesktopID)
	return nil
}

// MarkUnhealthy marks the VM for the given user's primary desktop as degraded.
func (r *OwnershipRegistry) MarkUnhealthy(userID string) error {
	return r.MarkUnhealthyForDesktop(userID, PrimaryDesktopID)
}

// MarkUnhealthyForDesktop marks the VM for the given user/desktop pair as
// degraded.
func (r *OwnershipRegistry) MarkUnhealthyForDesktop(userID, desktopID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	own, ok := r.ownerships[ownershipKey(userID, desktopID)]
	if !ok {
		return fmt.Errorf("no VM found for user %s desktop %s", userID, normalizeDesktopID(desktopID))
	}

	own.State = VMStateDegraded
	own.LastActiveAt = time.Now()
	log.Printf("vmctl: marked VM %s unhealthy for user %s desktop %s", own.VMID, userID, own.DesktopID)
	return nil
}

// HibernateVM transitions the VM for the given user to hibernated state.
// The VM can be resumed later with ResumeVM, restoring the same user's
// persisted state (VAL-CROSS-116, VAL-VM-008).
//
// The epoch does NOT change on hibernate; it will stay the same on resume,
// allowing callers to distinguish fresh boot from resume (VAL-CROSS-117).
func (r *OwnershipRegistry) HibernateVM(userID string) error {
	return r.HibernateVMForDesktop(userID, PrimaryDesktopID)
}

func (r *OwnershipRegistry) HibernateVMForDesktop(userID, desktopID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	own, ok := r.ownerships[ownershipKey(userID, desktopID)]
	if !ok {
		return fmt.Errorf("no VM found for user %s desktop %s", userID, normalizeDesktopID(desktopID))
	}

	if own.State != VMStateActive && own.State != VMStateDegraded {
		return fmt.Errorf("VM %s cannot be hibernated (state=%s)", own.VMID, own.State)
	}

	// Delegate to the real VM manager if available.
	if r.vmManager != nil {
		_ = r.vmManager.HibernateVM(own.VMID)
	}

	own.State = VMStateHibernated
	own.LastActiveAt = time.Now()
	own.StoppedBy = "idle"
	log.Printf("vmctl: hibernated VM %s for user %s desktop %s (epoch=%d)", own.VMID, userID, own.DesktopID, own.Epoch)
	return nil
}

// HibernateWorker transitions the worker VM with the given typed handle to
// hibernated state.
func (r *OwnershipRegistry) HibernateWorker(workerID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	own, ok := r.workerVMs[strings.TrimSpace(workerID)]
	if !ok {
		return fmt.Errorf("no worker VM found for worker_id %s", strings.TrimSpace(workerID))
	}
	if own.State != VMStateActive && own.State != VMStateDegraded {
		return fmt.Errorf("worker VM %s cannot be hibernated (state=%s)", own.VMID, own.State)
	}
	if r.vmManager != nil {
		_ = r.vmManager.HibernateVM(own.VMID)
	}
	own.State = VMStateHibernated
	own.LastActiveAt = time.Now()
	own.StoppedBy = "idle"
	log.Printf("vmctl: hibernated worker VM %s for user %s desktop %s worker_id %s", own.VMID, own.UserID, own.DesktopID, own.WorkerID)
	return nil
}

// ResumeVM resumes a stopped or hibernated VM for the given user,
// restoring the same user's persisted state (VAL-CROSS-116).
//
// The epoch does NOT increment on resume, so callers can detect that
// this is a resume rather than a fresh boot. This prevents duplicate
// canonical effects (VAL-CROSS-117).
func (r *OwnershipRegistry) ResumeVM(userID string) (*VMOwnership, error) {
	return r.ResumeVMForDesktop(userID, PrimaryDesktopID)
}

func (r *OwnershipRegistry) ResumeVMForDesktop(userID, desktopID string) (*VMOwnership, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	own, ok := r.ownerships[ownershipKey(userID, desktopID)]
	if !ok {
		return nil, fmt.Errorf("no VM found for user %s desktop %s", userID, normalizeDesktopID(desktopID))
	}

	if own.State != VMStateStopped && own.State != VMStateHibernated {
		if own.State == VMStateActive || own.State == VMStateBooting {
			own.LastActiveAt = time.Now()
			return own, nil
		}
		return nil, fmt.Errorf("VM %s cannot be resumed (state=%s)", own.VMID, own.State)
	}

	// Delegate to the real VM manager if available.
	if r.vmManager != nil {
		info, err := r.vmManager.ResumeVM(own.VMID)
		if err != nil {
			return nil, fmt.Errorf("failed to resume VM %s: %w", own.VMID, err)
		}
		own.SandboxURL = info.HostURL
		// Epoch stays the same for resume (VAL-CROSS-117).
	}

	// Transition to active. Epoch stays the same for resume (VAL-CROSS-117).
	// A fresh boot would increment the epoch.
	own.State = VMStateActive
	own.LastActiveAt = time.Now()
	own.StoppedBy = ""
	log.Printf("vmctl: resumed VM %s for user %s desktop %s (epoch=%d, same-epoch=resume)", own.VMID, userID, own.DesktopID, own.Epoch)
	return own, nil
}

// RecoverVM recovers an unhealthy or failed VM for the given user.
// Unlike ResumeVM, RecoverVM creates a fresh boot by incrementing the
// epoch counter. This signals to callers that any in-flight work from
// the previous boot should not be retried (VAL-CROSS-117, VAL-VM-009).
//
// The persistent user data is preserved across recovery so the user's
// state survives the crash (VAL-CROSS-116).
func (r *OwnershipRegistry) RecoverVM(userID string) (*VMOwnership, error) {
	return r.RecoverVMForDesktop(userID, PrimaryDesktopID)
}

func (r *OwnershipRegistry) RecoverVMForDesktop(userID, desktopID string) (*VMOwnership, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	own, ok := r.ownerships[ownershipKey(userID, desktopID)]
	if !ok {
		return nil, fmt.Errorf("no VM found for user %s desktop %s", userID, normalizeDesktopID(desktopID))
	}

	if own.State != VMStateDegraded && own.State != VMStateFailed {
		return nil, fmt.Errorf("VM %s is not in a recoverable state (state=%s)", own.VMID, own.State)
	}

	// Delegate to the real VM manager if available.
	if r.vmManager != nil {
		info, err := r.vmManager.RecoverVM(own.VMID)
		if err != nil {
			return nil, fmt.Errorf("failed to recover VM %s: %w", own.VMID, err)
		}
		own.SandboxURL = info.HostURL
		own.Epoch = info.Epoch
	} else {
		// Increment epoch on recovery — this is a fresh boot, not a resume.
		// The epoch change prevents duplicate canonical effects (VAL-CROSS-117).
		own.Epoch = r.nextEpoch()
	}

	own.State = VMStateActive
	own.LastActiveAt = time.Now()
	own.StoppedBy = ""
	log.Printf("vmctl: recovered VM %s for user %s desktop %s (new_epoch=%d, fresh-boot)", own.VMID, userID, own.DesktopID, own.Epoch)
	return own, nil
}

// LogoutVM handles VM lifecycle transition on user logout. It transitions
// only the current user's VM to stopped state (VAL-VM-008).
// Other users' VMs are not affected.
func (r *OwnershipRegistry) LogoutVM(userID string) error {
	return r.LogoutVMForDesktop(userID, PrimaryDesktopID)
}

func (r *OwnershipRegistry) LogoutVMForDesktop(userID, desktopID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	own, ok := r.ownerships[ownershipKey(userID, desktopID)]
	if !ok {
		return nil // no VM for this user, idempotent
	}

	// Delegate to the real VM manager if available.
	if r.vmManager != nil && (own.State == VMStateActive || own.State == VMStateDegraded) {
		_ = r.vmManager.StopVM(own.VMID)
	}

	own.State = VMStateStopped
	own.LastActiveAt = time.Now()
	own.StoppedBy = "logout"
	log.Printf("vmctl: stopped VM %s for user %s desktop %s (reason=logout)", own.VMID, userID, own.DesktopID)
	return nil
}

// CheckIdleVMs returns legacy user IDs for idle primary desktops only.
// Multi-desktop callers should use CheckIdleOwnerships.
func (r *OwnershipRegistry) CheckIdleVMs() []string {
	owns := r.CheckIdleOwnerships()
	idle := make([]string, 0, len(owns))
	for _, own := range owns {
		if own != nil && own.Kind == VMKindInteractive && own.DesktopID == PrimaryDesktopID {
			idle = append(idle, own.UserID)
		}
	}
	return idle
}

// CheckIdleOwnerships returns idle ownership records whose VMs have exceeded
// the idle timeout and should be stopped or hibernated.
func (r *OwnershipRegistry) CheckIdleOwnerships() []*VMOwnership {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.idleTimeout <= 0 {
		return nil
	}

	var idle []*VMOwnership
	now := time.Now()
	for _, own := range r.ownerships {
		if own.State == VMStateActive && now.Sub(own.LastActiveAt) > r.idleTimeout {
			idle = append(idle, own)
		}
	}
	for _, own := range r.workerVMs {
		if own.State == VMStateActive && now.Sub(own.LastActiveAt) > r.idleTimeout {
			idle = append(idle, own)
		}
	}
	return idle
}

// StopIdleVMs transitions all idle VMs to hibernated state.
// Returns the number of VMs that were stopped (VAL-VM-008).
func (r *OwnershipRegistry) StopIdleVMs() int {
	idleOwnerships := r.CheckIdleOwnerships()
	stopped := 0
	for _, own := range idleOwnerships {
		if own == nil {
			continue
		}
		var err error
		if own.Kind == VMKindWorker {
			err = r.HibernateWorker(own.WorkerID)
		} else {
			err = r.HibernateVMForDesktop(own.UserID, own.DesktopID)
		}
		if err == nil {
			stopped++
		}
	}
	return stopped
}

// SetSandboxCredential stores the gateway credential for a VM's sandbox.
func (r *OwnershipRegistry) SetSandboxCredential(vmID, credential string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	own, ok := r.vmByID[vmID]
	if !ok {
		return fmt.Errorf("no VM found with ID %s", vmID)
	}

	own.SandboxCredential = credential
	return nil
}

// ActiveCount returns the number of active (booting or active) VMs.
func (r *OwnershipRegistry) ActiveCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	count := 0
	for _, own := range r.ownerships {
		if own.IsReady() {
			count++
		}
	}
	for _, own := range r.workerVMs {
		if own.IsReady() {
			count++
		}
	}
	return count
}

// transitionVM transitions a VM to a new state.
func (r *OwnershipRegistry) transitionVM(vmID string, state VMState) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if own, ok := r.vmByID[vmID]; ok {
		own.State = state
		own.LastActiveAt = time.Now()
	}
}

// sandboxURLForVM generates the sandbox URL for a given VM ID.
// In production, this would resolve to the actual VM's network address.
// For host-process mode during development, all VMs route to the same
// host sandbox at the configured base URL.
func (r *OwnershipRegistry) sandboxURLForVM(vmID string) string {
	// In the current host-process mode, all VMs share the same sandbox
	// URL. When Firecracker is integrated, this will return per-VM URLs
	// based on the VM's assigned network address.
	return r.sandboxURLBase
}

// generateVMID creates a unique VM identifier.
func generateVMID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return "vm-" + hex.EncodeToString(b)
}

func generateWorkerID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "worker-" + hex.EncodeToString(b)
}
