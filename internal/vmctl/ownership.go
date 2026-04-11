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
package vmctl

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
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
	VMStateStopped VMState = "stopped"

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
}

// IsReady returns true if the VM is in a state that can serve requests.
func (o *VMOwnership) IsReady() bool {
	return o.State == VMStateActive || o.State == VMStateBooting
}

// OwnershipRegistry manages the mapping of users to VMs. It provides
// thread-safe VM assignment with singleflight semantics so that concurrent
// first requests for the same user collapse onto one VM assignment
// (VAL-VM-004).
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
}

// NewOwnershipRegistry creates a new ownership registry.
func NewOwnershipRegistry(sandboxURLBase string) *OwnershipRegistry {
	if sandboxURLBase == "" {
		sandboxURLBase = "http://127.0.0.1:8085"
	}
	return &OwnershipRegistry{
		ownerships:     make(map[string]*VMOwnership),
		vmByID:         make(map[string]*VMOwnership),
		pendingWaiters: make(map[string][]chan *VMOwnership),
		sandboxURLBase: sandboxURLBase,
	}
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

		// VM exists but is not ready (stopped, failed). Create a new one.
		// Clean up the old mapping.
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
	own := &VMOwnership{
		VMID:        vmID,
		UserID:      userID,
		SandboxURL:  r.sandboxURLForVM(vmID),
		State:       VMStateBooting,
		CreatedAt:   time.Now(),
		LastActiveAt: time.Now(),
	}

	// Register pending waiters map before unlocking so other callers can find it.
	r.pendingWaiters[userID] = nil

	// Store the ownership immediately in booting state.
	r.ownerships[userID] = own
	r.vmByID[vmID] = own

	r.mu.Unlock()

	// Simulate VM boot (in production this would call Firecracker).
	// For now, immediately transition to active.
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
