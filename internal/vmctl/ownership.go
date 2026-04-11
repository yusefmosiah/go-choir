// Package vmctl implements the VM ownership registry and lifecycle control
// for Mission 3. The registry maps authenticated users to VM-backed sandbox
// workloads and ensures concurrent first requests for the same user collapse
// onto a single VM assignment (VAL-VM-004).
//
// Key invariants:
//   - Each authenticated user receives exactly one active VM at a time.
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
)

// VMOwnership represents the assignment of a user to a specific VM.
type VMOwnership struct {
	// VMID is the unique identifier for the VM.
	VMID string `json:"vm_id"`

	// UserID is the authenticated user who owns this VM.
	UserID string `json:"user_id"`

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

// IsReady returns true if the VM is in a state that can serve requests.
func (o *VMOwnership) IsReady() bool {
	return o.State == VMStateActive || o.State == VMStateBooting
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

	// ownerships maps user ID to their active VM ownership.
	ownerships map[string]*VMOwnership

	// vmByID maps VM ID to ownership for reverse lookup.
	vmByID map[string]*VMOwnership

	// pendingWaiters maps user IDs to channels that concurrent callers
	// wait on when a VM assignment is already in progress. This collapses
	// concurrent first requests (VAL-VM-004).
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

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("vmctl: gateway token request failed for %s: %v", sandboxID, err)
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("vmctl: gateway token issue returned %d for %s", resp.StatusCode, sandboxID)
		return ""
	}

	var result struct {
		SandboxID string `json:"sandbox_id"`
		RawToken  string `json:"raw_token"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		log.Printf("vmctl: gateway token response decode failed: %v", err)
		return ""
	}

	return result.RawToken
}

// ResolveOrAssign resolves the VM ownership for the given user. If the user
// already has an active or booting VM, it is returned. If not, a new VM is
// assigned. If a VM assignment is already in progress for this user, the
// caller waits and receives the same result (VAL-VM-004).
//
// This method is safe for concurrent use. Multiple goroutines calling
// ResolveOrAssign for the same user simultaneously will all receive the
// same VMOwnership, and exactly one VM is created.
func (r *OwnershipRegistry) ResolveOrAssign(userID string) (*VMOwnership, error) {
	r.mu.Lock()

	// Check if user already has an active ownership.
	if own, ok := r.ownerships[userID]; ok {
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
			log.Printf("vmctl: resumed VM %s for user %s on resolve (epoch=%d)", own.VMID, userID, own.Epoch)
			return own, nil
		}

		// VM exists but failed or is degraded. Create a new one
		// with a fresh epoch. Clean up the old mapping.
		delete(r.vmByID, own.VMID)
	}

	// Check if a VM assignment is already in progress for this user.
	if waiters, ok := r.pendingWaiters[userID]; ok && len(waiters) > 0 {
		// Another goroutine is already assigning a VM for this user.
		// Wait for it to complete.
		ch := make(chan *VMOwnership, 1)
		r.pendingWaiters[userID] = append(waiters, ch)
		r.mu.Unlock()

		// Wait for the assignment to complete.
		own := <-ch
		if own == nil {
			return nil, fmt.Errorf("vm assignment failed for user %s", userID)
		}
		return own, nil
	}

	// We are the first caller for this user. Create a new VM.
	vmID := generateVMID()
	epoch := r.nextEpoch()

	own := &VMOwnership{
		VMID:        vmID,
		UserID:      userID,
		SandboxURL:  r.sandboxURLForVM(vmID),
		State:       VMStateBooting,
		CreatedAt:   time.Now(),
		LastActiveAt: time.Now(),
		Epoch:       epoch,
	}

	// Register pending waiters map before unlocking so other callers can find it.
	r.pendingWaiters[userID] = nil

	// Store the ownership immediately in booting state.
	r.ownerships[userID] = own
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
			waiters := r.pendingWaiters[userID]
			delete(r.pendingWaiters, userID)
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
	waiters := r.pendingWaiters[userID]
	delete(r.pendingWaiters, userID)
	r.mu.Unlock()

	for _, ch := range waiters {
		ch <- own
	}

	log.Printf("vmctl: assigned VM %s to user %s", vmID, userID)

	return own, nil
}

// GetOwnership returns the current ownership for a user, or nil if none exists.
func (r *OwnershipRegistry) GetOwnership(userID string) *VMOwnership {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.ownerships[userID]
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

	result := make([]*VMOwnership, 0, len(r.ownerships))
	for _, own := range r.ownerships {
		result = append(result, own)
	}
	return result
}

// StopVM stops the VM for the given user, transitioning it to stopped state.
// Returns the ownership or an error if no VM exists for the user.
func (r *OwnershipRegistry) StopVM(userID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	own, ok := r.ownerships[userID]
	if !ok {
		return fmt.Errorf("no VM found for user %s", userID)
	}

	// Delegate to the real VM manager if available.
	if r.vmManager != nil && (own.State == VMStateActive || own.State == VMStateDegraded) {
		_ = r.vmManager.StopVM(own.VMID)
	}

	own.State = VMStateStopped
	own.LastActiveAt = time.Now()
	log.Printf("vmctl: stopped VM %s for user %s", own.VMID, userID)
	return nil
}

// RemoveOwnership removes the ownership for a user entirely (e.g., after
// logout). The VM is stopped and the mappings are cleaned up.
func (r *OwnershipRegistry) RemoveOwnership(userID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	own, ok := r.ownerships[userID]
	if !ok {
		return nil // already gone, idempotent
	}

	// Delegate to the real VM manager if available.
	if r.vmManager != nil && (own.State == VMStateActive || own.State == VMStateDegraded) {
		_ = r.vmManager.StopVM(own.VMID)
	}

	own.State = VMStateStopped
	delete(r.ownerships, userID)
	delete(r.vmByID, own.VMID)
	log.Printf("vmctl: removed VM %s ownership for user %s", own.VMID, userID)
	return nil
}

// MarkUnhealthy marks the VM for the given user as degraded/failed.
func (r *OwnershipRegistry) MarkUnhealthy(userID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	own, ok := r.ownerships[userID]
	if !ok {
		return fmt.Errorf("no VM found for user %s", userID)
	}

	own.State = VMStateDegraded
	own.LastActiveAt = time.Now()
	log.Printf("vmctl: marked VM %s unhealthy for user %s", own.VMID, userID)
	return nil
}

// HibernateVM transitions the VM for the given user to hibernated state.
// The VM can be resumed later with ResumeVM, restoring the same user's
// persisted state (VAL-CROSS-116, VAL-VM-008).
//
// The epoch does NOT change on hibernate; it will stay the same on resume,
// allowing callers to distinguish fresh boot from resume (VAL-CROSS-117).
func (r *OwnershipRegistry) HibernateVM(userID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	own, ok := r.ownerships[userID]
	if !ok {
		return fmt.Errorf("no VM found for user %s", userID)
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
	log.Printf("vmctl: hibernated VM %s for user %s (epoch=%d)", own.VMID, userID, own.Epoch)
	return nil
}

// ResumeVM resumes a stopped or hibernated VM for the given user,
// restoring the same user's persisted state (VAL-CROSS-116).
//
// The epoch does NOT increment on resume, so callers can detect that
// this is a resume rather than a fresh boot. This prevents duplicate
// canonical effects (VAL-CROSS-117).
func (r *OwnershipRegistry) ResumeVM(userID string) (*VMOwnership, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	own, ok := r.ownerships[userID]
	if !ok {
		return nil, fmt.Errorf("no VM found for user %s", userID)
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
	log.Printf("vmctl: resumed VM %s for user %s (epoch=%d, same-epoch=resume)", own.VMID, userID, own.Epoch)
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
	r.mu.Lock()
	defer r.mu.Unlock()

	own, ok := r.ownerships[userID]
	if !ok {
		return nil, fmt.Errorf("no VM found for user %s", userID)
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
	log.Printf("vmctl: recovered VM %s for user %s (new_epoch=%d, fresh-boot)", own.VMID, userID, own.Epoch)
	return own, nil
}

// LogoutVM handles VM lifecycle transition on user logout. It transitions
// only the current user's VM to stopped state (VAL-VM-008).
// Other users' VMs are not affected.
func (r *OwnershipRegistry) LogoutVM(userID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	own, ok := r.ownerships[userID]
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
	log.Printf("vmctl: stopped VM %s for user %s (reason=logout)", own.VMID, userID)
	return nil
}

// CheckIdleVMs returns the user IDs whose VMs have exceeded the idle
// timeout and should be stopped or hibernated (VAL-VM-008).
// Only VMs in active state are considered.
func (r *OwnershipRegistry) CheckIdleVMs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.idleTimeout <= 0 {
		return nil
	}

	var idle []string
	now := time.Now()
	for userID, own := range r.ownerships {
		if own.State == VMStateActive && now.Sub(own.LastActiveAt) > r.idleTimeout {
			idle = append(idle, userID)
		}
	}
	return idle
}

// StopIdleVMs transitions all idle VMs to hibernated state.
// Returns the number of VMs that were stopped (VAL-VM-008).
func (r *OwnershipRegistry) StopIdleVMs() int {
	idleUsers := r.CheckIdleVMs()
	stopped := 0
	for _, userID := range idleUsers {
		if err := r.HibernateVM(userID); err == nil {
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
