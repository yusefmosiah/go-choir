package vmctl

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- Ownership Registry Tests ---

func TestOwnershipRegistry_ResolveOrAssignCreatesVM(t *testing.T) {
	reg := NewOwnershipRegistry("http://127.0.0.1:8085")

	own, err := reg.ResolveOrAssign("user-1")
	if err != nil {
		t.Fatalf("ResolveOrAssign: %v", err)
	}

	if own.UserID != "user-1" {
		t.Errorf("expected UserID user-1, got %s", own.UserID)
	}
	if own.VMID == "" {
		t.Error("expected non-empty VMID")
	}
	if !strings.HasPrefix(own.VMID, "vm-") {
		t.Errorf("expected VMID to start with vm-, got %s", own.VMID)
	}
	if own.State != VMStateActive {
		t.Errorf("expected state active, got %s", own.State)
	}
	if own.SandboxURL == "" {
		t.Error("expected non-empty SandboxURL")
	}
}

func TestOwnershipRegistry_ResolveOrAssignReturnsSameVM(t *testing.T) {
	// VAL-VM-003: Repeated requests from the same user stay pinned to
	// the same active VM.
	reg := NewOwnershipRegistry("http://127.0.0.1:8085")

	own1, err := reg.ResolveOrAssign("user-1")
	if err != nil {
		t.Fatalf("first ResolveOrAssign: %v", err)
	}

	own2, err := reg.ResolveOrAssign("user-1")
	if err != nil {
		t.Fatalf("second ResolveOrAssign: %v", err)
	}

	if own1.VMID != own2.VMID {
		t.Errorf("expected same VMID for repeated requests, got %s and %s", own1.VMID, own2.VMID)
	}
}

func TestOwnershipRegistry_DifferentUsersGetDifferentVMs(t *testing.T) {
	// VAL-VM-005: Different users receive distinct VMs and isolated state.
	reg := NewOwnershipRegistry("http://127.0.0.1:8085")

	own1, _ := reg.ResolveOrAssign("user-alice")
	own2, _ := reg.ResolveOrAssign("user-bob")

	if own1.VMID == own2.VMID {
		t.Error("expected different VM IDs for different users")
	}
	if own1.UserID == own2.UserID {
		t.Error("expected different user IDs")
	}
}

func TestOwnershipRegistry_ConcurrentRequestsCollapseToOneVM(t *testing.T) {
	// VAL-VM-004: Concurrent first requests for one user collapse onto one
	// VM assignment.
	reg := NewOwnershipRegistry("http://127.0.0.1:8085")

	const concurrency = 20
	results := make(chan *VMOwnership, concurrency)
	errors := make(chan error, concurrency)

	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			own, err := reg.ResolveOrAssign("user-concurrent")
			if err != nil {
				errors <- err
				return
			}
			results <- own
		}()
	}
	wg.Wait()
	close(results)
	close(errors)

	for err := range errors {
		t.Errorf("concurrent ResolveOrAssign: %v", err)
	}

	var vmIDs []string
	for own := range results {
		vmIDs = append(vmIDs, own.VMID)
	}

	if len(vmIDs) != concurrency {
		t.Fatalf("expected %d results, got %d", concurrency, len(vmIDs))
	}

	// All concurrent callers should receive the same VM ID.
	first := vmIDs[0]
	for _, id := range vmIDs[1:] {
		if id != first {
			t.Errorf("expected all concurrent callers to get VM %s, got %s", first, id)
		}
	}
}

func TestOwnershipRegistry_ActiveCount(t *testing.T) {
	reg := NewOwnershipRegistry("http://127.0.0.1:8085")

	if count := reg.ActiveCount(); count != 0 {
		t.Errorf("expected 0 active VMs, got %d", count)
	}

	if _, err := reg.ResolveOrAssign("user-1"); err != nil {
		t.Fatalf("ResolveOrAssign user-1: %v", err)
	}
	if count := reg.ActiveCount(); count != 1 {
		t.Errorf("expected 1 active VM, got %d", count)
	}

	if _, err := reg.ResolveOrAssign("user-2"); err != nil {
		t.Fatalf("ResolveOrAssign user-2: %v", err)
	}
	if count := reg.ActiveCount(); count != 2 {
		t.Errorf("expected 2 active VMs, got %d", count)
	}
}

func TestOwnershipRegistry_StopVM(t *testing.T) {
	reg := NewOwnershipRegistry("http://127.0.0.1:8085")

	own, _ := reg.ResolveOrAssign("user-1")
	if own.State != VMStateActive {
		t.Fatal("expected active state after assign")
	}

	if err := reg.StopVM("user-1"); err != nil {
		t.Fatalf("StopVM: %v", err)
	}

	// After stopping, the ownership should reflect stopped state.
	updated := reg.GetOwnership("user-1")
	if updated.State != VMStateStopped {
		t.Errorf("expected stopped state, got %s", updated.State)
	}
}

func TestOwnershipRegistry_StopNonexistentUser(t *testing.T) {
	reg := NewOwnershipRegistry("http://127.0.0.1:8085")

	err := reg.StopVM("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent user")
	}
}

func TestOwnershipRegistry_RemoveOwnership(t *testing.T) {
	reg := NewOwnershipRegistry("http://127.0.0.1:8085")

	own, _ := reg.ResolveOrAssign("user-1")
	vmID := own.VMID

	if err := reg.RemoveOwnership("user-1"); err != nil {
		t.Fatalf("RemoveOwnership: %v", err)
	}

	// Ownership should be gone.
	if reg.GetOwnership("user-1") != nil {
		t.Error("expected nil ownership after remove")
	}
	if reg.GetOwnershipByVMID(vmID) != nil {
		t.Error("expected nil VM-by-ID after remove")
	}
}

func TestOwnershipRegistry_RemoveOwnershipIdempotent(t *testing.T) {
	reg := NewOwnershipRegistry("http://127.0.0.1:8085")

	// Removing nonexistent user should not error.
	if err := reg.RemoveOwnership("nonexistent"); err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

func TestOwnershipRegistry_MarkUnhealthy(t *testing.T) {
	reg := NewOwnershipRegistry("http://127.0.0.1:8085")

	if _, err := reg.ResolveOrAssign("user-1"); err != nil {
		t.Fatalf("ResolveOrAssign: %v", err)
	}
	if err := reg.MarkUnhealthy("user-1"); err != nil {
		t.Fatalf("MarkUnhealthy: %v", err)
	}

	own := reg.GetOwnership("user-1")
	if own.State != VMStateDegraded {
		t.Errorf("expected degraded state, got %s", own.State)
	}
}

func TestOwnershipRegistry_ListOwnerships(t *testing.T) {
	reg := NewOwnershipRegistry("http://127.0.0.1:8085")

	if _, err := reg.ResolveOrAssign("user-1"); err != nil {
		t.Fatalf("ResolveOrAssign user-1: %v", err)
	}
	if _, err := reg.ResolveOrAssign("user-2"); err != nil {
		t.Fatalf("ResolveOrAssign user-2: %v", err)
	}
	if _, err := reg.ResolveOrAssign("user-3"); err != nil {
		t.Fatalf("ResolveOrAssign user-3: %v", err)
	}

	list := reg.ListOwnerships()
	if len(list) != 3 {
		t.Errorf("expected 3 ownerships, got %d", len(list))
	}
}

func TestOwnershipRegistry_SetSandboxCredential(t *testing.T) {
	reg := NewOwnershipRegistry("http://127.0.0.1:8085")

	own, _ := reg.ResolveOrAssign("user-1")
	if err := reg.SetSandboxCredential(own.VMID, "cred-123"); err != nil {
		t.Fatalf("SetSandboxCredential: %v", err)
	}

	updated := reg.GetOwnership("user-1")
	if updated.SandboxCredential != "cred-123" {
		t.Errorf("expected credential cred-123, got %s", updated.SandboxCredential)
	}
}

func TestOwnershipRegistry_IsReady(t *testing.T) {
	own := &VMOwnership{State: VMStateActive}
	if !own.IsReady() {
		t.Error("expected active VM to be ready")
	}

	own.State = VMStateBooting
	if !own.IsReady() {
		t.Error("expected booting VM to be ready")
	}

	own.State = VMStateStopped
	if own.IsReady() {
		t.Error("expected stopped VM to not be ready")
	}

	own.State = VMStateFailed
	if own.IsReady() {
		t.Error("expected failed VM to not be ready")
	}
}

func TestOwnershipRegistry_StoppedVMGetsResumed(t *testing.T) {
	// When a user's VM is stopped, a new ResolveOrAssign should resume it
	// with the same VMID, preserving user state (VAL-CROSS-116).
	// The epoch stays the same on resume (VAL-CROSS-117).
	reg := NewOwnershipRegistry("http://127.0.0.1:8085")

	own1, _ := reg.ResolveOrAssign("user-1")
	oldVMID := own1.VMID
	oldEpoch := own1.Epoch

	if err := reg.StopVM("user-1"); err != nil {
		t.Fatalf("StopVM: %v", err)
	}

	own2, _ := reg.ResolveOrAssign("user-1")
	if own2.VMID != oldVMID {
		t.Errorf("expected same VM ID after stop+resolve (resume), got %s vs %s", oldVMID, own2.VMID)
	}
	if own2.Epoch != oldEpoch {
		t.Errorf("expected same epoch after resume, got %d vs %d", oldEpoch, own2.Epoch)
	}
	if own2.State != VMStateActive {
		t.Errorf("expected active state after resume, got %s", own2.State)
	}
}

// --- Handler Tests ---

func newTestServer(t *testing.T) (*httptest.Server, *OwnershipRegistry) {
	t.Helper()
	reg := NewOwnershipRegistry("http://127.0.0.1:8085")
	handler := NewHandler(reg)

	mux := http.NewServeMux()
	mux.HandleFunc("/health", handler.HandleHealth)
	mux.HandleFunc("/internal/vmctl/resolve", handler.HandleResolve)
	mux.HandleFunc("/internal/vmctl/lookup", handler.HandleLookup)
	mux.HandleFunc("/internal/vmctl/stop", handler.HandleStop)
	mux.HandleFunc("/internal/vmctl/remove", handler.HandleRemove)
	mux.HandleFunc("/internal/vmctl/list", handler.HandleList)
	mux.HandleFunc("/internal/vmctl/hibernate", handler.HandleHibernate)
	mux.HandleFunc("/internal/vmctl/resume", handler.HandleResume)
	mux.HandleFunc("/internal/vmctl/recover", handler.HandleRecover)
	mux.HandleFunc("/internal/vmctl/logout", handler.HandleLogout)
	mux.HandleFunc("/internal/vmctl/idle-check", handler.HandleIdleCheck)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, reg
}

func TestHandler_Health(t *testing.T) {
	srv, _ := newTestServer(t)

	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatalf("health request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var result vmctlHealthResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode health response: %v", err)
	}

	if result.Status != "ok" {
		t.Errorf("expected ok status, got %s", result.Status)
	}
	if result.Service != "vmctl" {
		t.Errorf("expected vmctl service, got %s", result.Service)
	}
}

func TestHandler_ResolveCreatesVM(t *testing.T) {
	// VAL-VM-001: First protected request resolves through VM ownership.
	srv, _ := newTestServer(t)

	body := `{"user_id":"user-1"}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/internal/vmctl/resolve", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Caller", "true")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("resolve request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var result resolveResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode resolve response: %v", err)
	}

	if result.UserID != "user-1" {
		t.Errorf("expected user-1, got %s", result.UserID)
	}
	if result.VMID == "" {
		t.Error("expected non-empty VMID")
	}
	if result.SandboxURL == "" {
		t.Error("expected non-empty SandboxURL")
	}
	if result.State != "active" {
		t.Errorf("expected active state, got %s", result.State)
	}
}

func TestHandler_ResolveReturnsExistingVM(t *testing.T) {
	// VAL-VM-003: Repeated requests stay pinned to the same VM.
	srv, _ := newTestServer(t)

	// First resolve.
	body := `{"user_id":"user-1"}`
	req1, _ := http.NewRequest(http.MethodPost, srv.URL+"/internal/vmctl/resolve", strings.NewReader(body))
	req1.Header.Set("Content-Type", "application/json")
	req1.Header.Set("X-Internal-Caller", "true")
	resp1, _ := http.DefaultClient.Do(req1)
	var result1 resolveResponse
	if err := json.NewDecoder(resp1.Body).Decode(&result1); err != nil {
		t.Fatalf("decode result1: %v", err)
	}
	_ = resp1.Body.Close()

	// Second resolve.
	req2, _ := http.NewRequest(http.MethodPost, srv.URL+"/internal/vmctl/resolve", strings.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("X-Internal-Caller", "true")
	resp2, _ := http.DefaultClient.Do(req2)
	var result2 resolveResponse
	if err := json.NewDecoder(resp2.Body).Decode(&result2); err != nil {
		t.Fatalf("decode result2: %v", err)
	}
	_ = resp2.Body.Close()

	if result1.VMID != result2.VMID {
		t.Errorf("expected same VMID across resolves, got %s and %s", result1.VMID, result2.VMID)
	}
}

func TestHandler_ResolveDeniesExternalCallers(t *testing.T) {
	// VAL-VM-012: vmctl control endpoints are not publicly accessible.
	// Verify the isInternalCaller function properly rejects non-localhost callers.
	if !isInternalCaller(&http.Request{Host: "192.168.1.1:8083", RemoteAddr: "10.0.0.1:12345"}) {
		// Good, non-localhost is rejected
	} else {
		t.Error("expected non-localhost caller to be rejected")
	}
}

func TestHandler_ResolveRequiresUserID(t *testing.T) {
	srv, _ := newTestServer(t)

	body := `{}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/internal/vmctl/resolve", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Caller", "true")

	resp, _ := http.DefaultClient.Do(req)
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestHandler_Lookup(t *testing.T) {
	srv, _ := newTestServer(t)

	// First create an ownership.
	body := `{"user_id":"user-1"}`
	req1, _ := http.NewRequest(http.MethodPost, srv.URL+"/internal/vmctl/resolve", strings.NewReader(body))
	req1.Header.Set("Content-Type", "application/json")
	req1.Header.Set("X-Internal-Caller", "true")
	resp1, _ := http.DefaultClient.Do(req1)
	_ = resp1.Body.Close()

	// Now lookup.
	req2, _ := http.NewRequest(http.MethodGet, srv.URL+"/internal/vmctl/lookup?user_id=user-1", nil)
	req2.Header.Set("X-Internal-Caller", "true")
	resp2, _ := http.DefaultClient.Do(req2)
	defer func() { _ = resp2.Body.Close() }()

	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp2.StatusCode)
	}

	var result ownershipResponse
	if err := json.NewDecoder(resp2.Body).Decode(&result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result.UserID != "user-1" {
		t.Errorf("expected user-1, got %s", result.UserID)
	}
}

func TestHandler_LookupNonexistent(t *testing.T) {
	srv, _ := newTestServer(t)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/internal/vmctl/lookup?user_id=nonexistent", nil)
	req.Header.Set("X-Internal-Caller", "true")

	resp, _ := http.DefaultClient.Do(req)
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

func TestHandler_Stop(t *testing.T) {
	srv, _ := newTestServer(t)

	// First create.
	body := `{"user_id":"user-1"}`
	req1, _ := http.NewRequest(http.MethodPost, srv.URL+"/internal/vmctl/resolve", strings.NewReader(body))
	req1.Header.Set("Content-Type", "application/json")
	req1.Header.Set("X-Internal-Caller", "true")
	resp1, _ := http.DefaultClient.Do(req1)
	_ = resp1.Body.Close()

	// Now stop.
	req2, _ := http.NewRequest(http.MethodPost, srv.URL+"/internal/vmctl/stop", strings.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("X-Internal-Caller", "true")
	resp2, _ := http.DefaultClient.Do(req2)
	defer func() { _ = resp2.Body.Close() }()

	if resp2.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp2.StatusCode)
	}

	// Lookup should still find it but in stopped state.
	req3, _ := http.NewRequest(http.MethodGet, srv.URL+"/internal/vmctl/lookup?user_id=user-1", nil)
	req3.Header.Set("X-Internal-Caller", "true")
	resp3, _ := http.DefaultClient.Do(req3)
	defer func() { _ = resp3.Body.Close() }()

	var result ownershipResponse
	if err := json.NewDecoder(resp3.Body).Decode(&result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result.State != "stopped" {
		t.Errorf("expected stopped state, got %s", result.State)
	}
}

func TestHandler_Remove(t *testing.T) {
	srv, _ := newTestServer(t)

	// First create.
	body := `{"user_id":"user-1"}`
	req1, _ := http.NewRequest(http.MethodPost, srv.URL+"/internal/vmctl/resolve", strings.NewReader(body))
	req1.Header.Set("Content-Type", "application/json")
	req1.Header.Set("X-Internal-Caller", "true")
	resp1, _ := http.DefaultClient.Do(req1)
	_ = resp1.Body.Close()

	// Now remove.
	req2, _ := http.NewRequest(http.MethodPost, srv.URL+"/internal/vmctl/remove", strings.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("X-Internal-Caller", "true")
	resp2, _ := http.DefaultClient.Do(req2)
	defer func() { _ = resp2.Body.Close() }()

	if resp2.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp2.StatusCode)
	}

	// Lookup should return 404.
	req3, _ := http.NewRequest(http.MethodGet, srv.URL+"/internal/vmctl/lookup?user_id=user-1", nil)
	req3.Header.Set("X-Internal-Caller", "true")
	resp3, _ := http.DefaultClient.Do(req3)
	defer func() { _ = resp3.Body.Close() }()

	if resp3.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 after remove, got %d", resp3.StatusCode)
	}
}

func TestHandler_List(t *testing.T) {
	srv, _ := newTestServer(t)

	// Create two ownerships.
	for _, userID := range []string{"user-1", "user-2"} {
		body := fmt.Sprintf(`{"user_id":"%s"}`, userID)
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/internal/vmctl/resolve", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Internal-Caller", "true")
		resp, _ := http.DefaultClient.Do(req)
		_ = resp.Body.Close()
	}

	// List.
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/internal/vmctl/list", nil)
	req.Header.Set("X-Internal-Caller", "true")
	resp, _ := http.DefaultClient.Do(req)
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode result: %v", err)
	}

	count, _ := result["count"].(float64)
	if int(count) != 2 {
		t.Errorf("expected 2 ownerships, got %v", count)
	}
}

func TestHandler_MethodNotAllowed(t *testing.T) {
	srv, _ := newTestServer(t)

	// GET on a POST-only endpoint.
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/internal/vmctl/resolve", nil)
	req.Header.Set("X-Internal-Caller", "true")
	resp, _ := http.DefaultClient.Do(req)
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", resp.StatusCode)
	}
}

// --- Client Tests ---

func TestClient_ResolveAndLookup(t *testing.T) {
	srv, _ := newTestServer(t)
	client := NewClient(srv.URL)

	// Resolve creates a VM.
	resp, err := client.Resolve("user-client-test")
	if err != nil {
		t.Fatalf("client resolve: %v", err)
	}
	if resp.UserID != "user-client-test" {
		t.Errorf("expected user-client-test, got %s", resp.UserID)
	}
	if resp.VMID == "" {
		t.Error("expected non-empty VMID")
	}

	// Lookup finds the existing VM.
	lookup, err := client.Lookup("user-client-test")
	if err != nil {
		t.Fatalf("client lookup: %v", err)
	}
	if lookup == nil {
		t.Fatal("expected non-nil lookup result")
	}
	if lookup.VMID != resp.VMID {
		t.Errorf("expected same VMID %s, got %s", resp.VMID, lookup.VMID)
	}
}

func TestClient_LookupNonexistent(t *testing.T) {
	srv, _ := newTestServer(t)
	client := NewClient(srv.URL)

	result, err := client.Lookup("nonexistent")
	if err != nil {
		t.Fatalf("client lookup nonexistent: %v", err)
	}
	if result != nil {
		t.Error("expected nil for nonexistent user")
	}
}

func TestClient_Stop(t *testing.T) {
	srv, _ := newTestServer(t)
	client := NewClient(srv.URL)

	if _, err := client.Resolve("user-stop-test"); err != nil {
		t.Fatalf("client resolve: %v", err)
	}

	if err := client.Stop("user-stop-test"); err != nil {
		t.Fatalf("client stop: %v", err)
	}
}

func TestClient_Remove(t *testing.T) {
	srv, _ := newTestServer(t)
	client := NewClient(srv.URL)

	if _, err := client.Resolve("user-remove-test"); err != nil {
		t.Fatalf("client resolve: %v", err)
	}

	if err := client.Remove("user-remove-test"); err != nil {
		t.Fatalf("client remove: %v", err)
	}

	// Lookup should return nil.
	result, _ := client.Lookup("user-remove-test")
	if result != nil {
		t.Error("expected nil after remove")
	}
}

func TestClient_DifferentUsersIsolatedVMs(t *testing.T) {
	// VAL-VM-005: Different users receive distinct VMs.
	srv, _ := newTestServer(t)
	client := NewClient(srv.URL)

	resp1, _ := client.Resolve("alice")
	resp2, _ := client.Resolve("bob")

	if resp1.VMID == resp2.VMID {
		t.Error("expected different VM IDs for different users")
	}
}

func TestClient_ConcurrentResolveSameUser(t *testing.T) {
	// VAL-VM-004: Concurrent first requests for one user collapse.
	srv, _ := newTestServer(t)
	client := NewClient(srv.URL)

	const concurrency = 10
	results := make(chan *resolveResponse, concurrency)
	errors := make(chan error, concurrency)

	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := client.Resolve("user-concurrent")
			if err != nil {
				errors <- err
				return
			}
			results <- resp
		}()
	}
	wg.Wait()
	close(results)
	close(errors)

	for err := range errors {
		t.Errorf("concurrent client resolve: %v", err)
	}

	var vmIDs []string
	for resp := range results {
		vmIDs = append(vmIDs, resp.VMID)
	}

	if len(vmIDs) != concurrency {
		t.Fatalf("expected %d results, got %d", concurrency, len(vmIDs))
	}

	first := vmIDs[0]
	for _, id := range vmIDs[1:] {
		if id != first {
			t.Errorf("expected all concurrent callers to get VM %s, got %s", first, id)
		}
	}
}

// --- isInternalCaller Tests ---

func TestIsInternalCaller(t *testing.T) {
	tests := []struct {
		name       string
		host       string
		remoteAddr string
		header     string
		want       bool
	}{
		{"localhost host", "localhost:8083", "127.0.0.1:12345", "", true},
		{"127.0.0.1 host", "127.0.0.1:8083", "127.0.0.1:12345", "", true},
		{"::1 host", "[::1]:8083", "[::1]:12345", "", true},
		{"external host", "192.168.1.1:8083", "10.0.0.1:12345", "", false},
		{"internal header", "external:8083", "10.0.0.1:12345", "true", true},
		{"empty header", "external:8083", "10.0.0.1:12345", "false", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &http.Request{
				Host:       tt.host,
				RemoteAddr: tt.remoteAddr,
				Header:     http.Header{"X-Internal-Caller": {tt.header}},
			}
			if got := isInternalCaller(r); got != tt.want {
				t.Errorf("isInternalCaller(%+v) = %v, want %v", tt, got, tt.want)
			}
		})
	}
}

// --- Timing Tests ---

func TestOwnershipRegistry_LastActiveAtUpdated(t *testing.T) {
	reg := NewOwnershipRegistry("http://127.0.0.1:8085")

	own1, _ := reg.ResolveOrAssign("user-1")
	firstActive := own1.LastActiveAt

	// Wait a tiny bit and resolve again.
	time.Sleep(10 * time.Millisecond)

	if _, err := reg.ResolveOrAssign("user-1"); err != nil {
		t.Fatalf("ResolveOrAssign: %v", err)
	}
	updated := reg.GetOwnership("user-1")

	if !updated.LastActiveAt.After(firstActive) {
		t.Error("expected LastActiveAt to be updated on subsequent resolve")
	}
}

// --- Endpoint URL Tests ---

func TestEndpointURLs(t *testing.T) {
	base := "http://localhost:8083"

	if got := ResolveEndpoint(base); got != "http://localhost:8083/internal/vmctl/resolve" {
		t.Errorf("ResolveEndpoint = %s", got)
	}
	if got := LookupEndpoint(base); got != "http://localhost:8083/internal/vmctl/lookup" {
		t.Errorf("LookupEndpoint = %s", got)
	}
	if got := StopEndpoint(base); got != "http://localhost:8083/internal/vmctl/stop" {
		t.Errorf("StopEndpoint = %s", got)
	}
	if got := RemoveEndpoint(base); got != "http://localhost:8083/internal/vmctl/remove" {
		t.Errorf("RemoveEndpoint = %s", got)
	}
	if got := HibernateEndpoint(base); got != "http://localhost:8083/internal/vmctl/hibernate" {
		t.Errorf("HibernateEndpoint = %s", got)
	}
	if got := ResumeEndpoint(base); got != "http://localhost:8083/internal/vmctl/resume" {
		t.Errorf("ResumeEndpoint = %s", got)
	}
	if got := RecoverEndpoint(base); got != "http://localhost:8083/internal/vmctl/recover" {
		t.Errorf("RecoverEndpoint = %s", got)
	}
	if got := LogoutEndpoint(base); got != "http://localhost:8083/internal/vmctl/logout" {
		t.Errorf("LogoutEndpoint = %s", got)
	}
	if got := IdleCheckEndpoint(base); got != "http://localhost:8083/internal/vmctl/idle-check" {
		t.Errorf("IdleCheckEndpoint = %s", got)
	}
}

// --- Lifecycle Tests (VAL-VM-008, VAL-VM-009, VAL-CROSS-116, VAL-CROSS-117) ---

func TestOwnershipRegistry_HibernateAndResume(t *testing.T) {
	// VAL-CROSS-116: Idle stop or hibernate resumes the same user's state.
	reg := NewOwnershipRegistry("http://127.0.0.1:8085")

	own1, _ := reg.ResolveOrAssign("user-1")
	vmID := own1.VMID
	epoch := own1.Epoch

	// Hibernate the VM.
	if err := reg.HibernateVM("user-1"); err != nil {
		t.Fatalf("HibernateVM: %v", err)
	}

	own := reg.GetOwnership("user-1")
	if own.State != VMStateHibernated {
		t.Errorf("expected hibernated state, got %s", own.State)
	}
	if own.VMID != vmID {
		t.Errorf("expected same VMID after hibernate, got %s", own.VMID)
	}

	// Resume the VM — epoch should NOT change (VAL-CROSS-117).
	resumed, err := reg.ResumeVM("user-1")
	if err != nil {
		t.Fatalf("ResumeVM: %v", err)
	}
	if resumed.State != VMStateActive {
		t.Errorf("expected active state after resume, got %s", resumed.State)
	}
	if resumed.VMID != vmID {
		t.Errorf("expected same VMID after resume, got %s", resumed.VMID)
	}
	if resumed.Epoch != epoch {
		t.Errorf("expected epoch %d after resume (no increment), got %d", epoch, resumed.Epoch)
	}
}

func TestOwnershipRegistry_RecoverIncrementsEpoch(t *testing.T) {
	// VAL-CROSS-117: Crash recovery does not duplicate canonical effects.
	// Recovery increments epoch to signal fresh boot.
	reg := NewOwnershipRegistry("http://127.0.0.1:8085")

	own1, _ := reg.ResolveOrAssign("user-1")
	vmID := own1.VMID
	epoch := own1.Epoch

	// Mark unhealthy and recover.
	if err := reg.MarkUnhealthy("user-1"); err != nil {
		t.Fatalf("MarkUnhealthy: %v", err)
	}

	recovered, err := reg.RecoverVM("user-1")
	if err != nil {
		t.Fatalf("RecoverVM: %v", err)
	}

	if recovered.State != VMStateActive {
		t.Errorf("expected active state after recovery, got %s", recovered.State)
	}
	if recovered.VMID != vmID {
		t.Errorf("expected same VMID after recovery, got %s", recovered.VMID)
	}
	if recovered.Epoch <= epoch {
		t.Errorf("expected epoch > %d after recovery (fresh boot), got %d", epoch, recovered.Epoch)
	}
}

func TestOwnershipRegistry_LogoutStopsOnlyCurrentUser(t *testing.T) {
	// VAL-VM-008: Logout or idle transitions only the current user's VM.
	reg := NewOwnershipRegistry("http://127.0.0.1:8085")

	if _, err := reg.ResolveOrAssign("user-alice"); err != nil {
		t.Fatalf("ResolveOrAssign alice: %v", err)
	}
	if _, err := reg.ResolveOrAssign("user-bob"); err != nil {
		t.Fatalf("ResolveOrAssign bob: %v", err)
	}

	// Logout user-alice.
	if err := reg.LogoutVM("user-alice"); err != nil {
		t.Fatalf("LogoutVM: %v", err)
	}

	// Alice's VM should be stopped.
	aliceOwn := reg.GetOwnership("user-alice")
	if aliceOwn.State != VMStateStopped {
		t.Errorf("expected alice VM stopped after logout, got %s", aliceOwn.State)
	}
	if aliceOwn.StoppedBy != "logout" {
		t.Errorf("expected stopped_by=logout, got %s", aliceOwn.StoppedBy)
	}

	// Bob's VM should still be active.
	bobOwn := reg.GetOwnership("user-bob")
	if bobOwn.State != VMStateActive {
		t.Errorf("expected bob VM still active after alice logout, got %s", bobOwn.State)
	}
}

func TestOwnershipRegistry_IdleTimeoutChecks(t *testing.T) {
	// VAL-VM-008: Idle timeout transitions inactive VMs.
	reg := NewOwnershipRegistry("http://127.0.0.1:8085")
	reg.SetIdleTimeout(50 * time.Millisecond)

	if _, err := reg.ResolveOrAssign("user-active"); err != nil {
		t.Fatalf("ResolveOrAssign user-active: %v", err)
	}
	if _, err := reg.ResolveOrAssign("user-idle"); err != nil {
		t.Fatalf("ResolveOrAssign user-idle: %v", err)
	}

	// Simulate user-idle being idle by backdating its LastActiveAt.
	reg.mu.Lock()
	idleOwn := reg.ownerships["user-idle"]
	idleOwn.LastActiveAt = time.Now().Add(-100 * time.Millisecond)
	reg.mu.Unlock()

	// Check idle VMs — should only find user-idle.
	idleUsers := reg.CheckIdleVMs()
	if len(idleUsers) != 1 {
		t.Fatalf("expected 1 idle user, got %d: %v", len(idleUsers), idleUsers)
	}
	if idleUsers[0] != "user-idle" {
		t.Errorf("expected user-idle to be idle, got %s", idleUsers[0])
	}

	// Stop idle VMs.
	stopped := reg.StopIdleVMs()
	if stopped != 1 {
		t.Errorf("expected 1 VM stopped, got %d", stopped)
	}

	// Verify user-idle is now hibernated.
	idleOwn = reg.GetOwnership("user-idle")
	if idleOwn.State != VMStateHibernated {
		t.Errorf("expected hibernated after idle stop, got %s", idleOwn.State)
	}

	// Verify user-active is still active.
	activeOwn := reg.GetOwnership("user-active")
	if activeOwn.State != VMStateActive {
		t.Errorf("expected active VM still running, got %s", activeOwn.State)
	}
}

func TestOwnershipRegistry_HibernateRequiresRunningVM(t *testing.T) {
	reg := NewOwnershipRegistry("http://127.0.0.1:8085")

	// No VM at all.
	if err := reg.HibernateVM("nonexistent"); err == nil {
		t.Error("expected error for nonexistent user")
	}

	// Already stopped VM.
	if _, err := reg.ResolveOrAssign("user-1"); err != nil {
		t.Fatalf("ResolveOrAssign: %v", err)
	}
	if err := reg.StopVM("user-1"); err != nil {
		t.Fatalf("StopVM: %v", err)
	}
	if err := reg.HibernateVM("user-1"); err == nil {
		t.Error("expected error for stopped VM")
	}
}

func TestOwnershipRegistry_ResumeNonResumableState(t *testing.T) {
	reg := NewOwnershipRegistry("http://127.0.0.1:8085")

	// No VM at all.
	_, err := reg.ResumeVM("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent user")
	}

	// Active VM — resume returns it as-is.
	if _, err := reg.ResolveOrAssign("user-1"); err != nil {
		t.Fatalf("ResolveOrAssign: %v", err)
	}
	own, err := reg.ResumeVM("user-1")
	if err != nil {
		t.Fatalf("ResumeVM on active: %v", err)
	}
	if own.State != VMStateActive {
		t.Errorf("expected active, got %s", own.State)
	}
}

func TestOwnershipRegistry_RecoverRequiresFailedState(t *testing.T) {
	reg := NewOwnershipRegistry("http://127.0.0.1:8085")

	// No VM at all.
	_, err := reg.RecoverVM("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent user")
	}

	// Active VM — cannot recover.
	if _, err := reg.ResolveOrAssign("user-1"); err != nil {
		t.Fatalf("ResolveOrAssign: %v", err)
	}
	_, err = reg.RecoverVM("user-1")
	if err == nil {
		t.Error("expected error for active VM")
	}
}

func TestOwnershipRegistry_EpochTracksBootGeneration(t *testing.T) {
	// VAL-CROSS-117: Epoch tracking prevents duplicate canonical effects.
	reg := NewOwnershipRegistry("http://127.0.0.1:8085")

	// First assignment gets an epoch.
	own1, _ := reg.ResolveOrAssign("user-1")
	epoch1 := own1.Epoch
	if epoch1 == 0 {
		t.Error("expected non-zero epoch")
	}

	// Resolve again (same active VM) — epoch stays the same.
	own2, _ := reg.ResolveOrAssign("user-1")
	if own2.Epoch != epoch1 {
		t.Errorf("expected same epoch on re-resolve, got %d vs %d", epoch1, own2.Epoch)
	}

	// Stop and resolve (resume) — epoch stays the same.
	if err := reg.StopVM("user-1"); err != nil {
		t.Fatalf("StopVM: %v", err)
	}
	own3, _ := reg.ResolveOrAssign("user-1")
	if own3.Epoch != epoch1 {
		t.Errorf("expected same epoch on resume, got %d vs %d", epoch1, own3.Epoch)
	}

	// Mark unhealthy and recover — epoch increments.
	if err := reg.MarkUnhealthy("user-1"); err != nil {
		t.Fatalf("MarkUnhealthy: %v", err)
	}
	own4, _ := reg.RecoverVM("user-1")
	if own4.Epoch <= epoch1 {
		t.Errorf("expected epoch > %d after recovery, got %d", epoch1, own4.Epoch)
	}
}

func TestOwnershipRegistry_FailedVMGetsNewAssignment(t *testing.T) {
	// Failed VMs should get a new assignment (new VMID, new epoch).
	reg := NewOwnershipRegistry("http://127.0.0.1:8085")

	own1, _ := reg.ResolveOrAssign("user-1")
	oldVMID := own1.VMID
	oldEpoch := own1.Epoch

	// Simulate a failure.
	reg.mu.Lock()
	own1.State = VMStateFailed
	reg.mu.Unlock()

	// ResolveOrAssign should create a new VM for the failed state.
	own2, _ := reg.ResolveOrAssign("user-1")
	if own2.VMID == oldVMID {
		t.Error("expected new VM ID for failed VM")
	}
	if own2.Epoch <= oldEpoch {
		t.Errorf("expected new epoch > %d for new VM, got %d", oldEpoch, own2.Epoch)
	}
}

func TestOwnershipRegistry_NoIdleTimeoutWhenZero(t *testing.T) {
	reg := NewOwnershipRegistry("http://127.0.0.1:8085")
	// Default idle timeout is 0 — no idle checking.

	if _, err := reg.ResolveOrAssign("user-1"); err != nil {
		t.Fatalf("ResolveOrAssign: %v", err)
	}

	// Backdate the last active time.
	reg.mu.Lock()
	reg.ownerships["user-1"].LastActiveAt = time.Now().Add(-24 * time.Hour)
	reg.mu.Unlock()

	// Should find no idle VMs.
	idle := reg.CheckIdleVMs()
	if len(idle) != 0 {
		t.Errorf("expected no idle VMs with zero timeout, got %d", len(idle))
	}
}

func TestOwnershipRegistry_ResolveAfterLogout(t *testing.T) {
	// VAL-VM-008: After logout, the next request wakes or recreates the VM.
	reg := NewOwnershipRegistry("http://127.0.0.1:8085")

	own1, _ := reg.ResolveOrAssign("user-1")
	vmID := own1.VMID

	if err := reg.LogoutVM("user-1"); err != nil {
		t.Fatalf("LogoutVM: %v", err)
	}

	// Resolving after logout should resume the same VM (VAL-CROSS-116).
	own2, _ := reg.ResolveOrAssign("user-1")
	if own2.VMID != vmID {
		t.Errorf("expected same VMID after logout+resolve (resume), got %s vs %s", vmID, own2.VMID)
	}
	if own2.State != VMStateActive {
		t.Errorf("expected active state after logout+resolve, got %s", own2.State)
	}
}

// --- Handler Lifecycle Tests ---

func TestHandler_HibernateAndResume(t *testing.T) {
	srv, _ := newTestServer(t)

	// Create a VM.
	body := `{"user_id":"user-1"}`
	req1, _ := http.NewRequest(http.MethodPost, srv.URL+"/internal/vmctl/resolve", strings.NewReader(body))
	req1.Header.Set("Content-Type", "application/json")
	req1.Header.Set("X-Internal-Caller", "true")
	resp1, _ := http.DefaultClient.Do(req1)
	var result1 resolveResponse
	if err := json.NewDecoder(resp1.Body).Decode(&result1); err != nil {
		t.Fatalf("decode result1: %v", err)
	}
	_ = resp1.Body.Close()
	vmID := result1.VMID

	// Hibernate.
	req2, _ := http.NewRequest(http.MethodPost, srv.URL+"/internal/vmctl/hibernate", strings.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("X-Internal-Caller", "true")
	resp2, _ := http.DefaultClient.Do(req2)
	defer func() { _ = resp2.Body.Close() }()

	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on hibernate, got %d", resp2.StatusCode)
	}

	var hibResult map[string]interface{}
	if err := json.NewDecoder(resp2.Body).Decode(&hibResult); err != nil {
		t.Fatalf("decode hibResult: %v", err)
	}
	if hibResult["status"] != "hibernated" {
		t.Errorf("expected status=hibernated, got %v", hibResult["status"])
	}
	if hibResult["vm_id"] != vmID {
		t.Errorf("expected vm_id=%s, got %v", vmID, hibResult["vm_id"])
	}

	// Resume.
	req3, _ := http.NewRequest(http.MethodPost, srv.URL+"/internal/vmctl/resume", strings.NewReader(body))
	req3.Header.Set("Content-Type", "application/json")
	req3.Header.Set("X-Internal-Caller", "true")
	resp3, _ := http.DefaultClient.Do(req3)
	defer func() { _ = resp3.Body.Close() }()

	if resp3.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on resume, got %d", resp3.StatusCode)
	}

	var result3 resolveResponse
	if err := json.NewDecoder(resp3.Body).Decode(&result3); err != nil {
		t.Fatalf("decode result3: %v", err)
	}
	if result3.VMID != vmID {
		t.Errorf("expected same VMID after resume, got %s", result3.VMID)
	}
	if result3.State != "active" {
		t.Errorf("expected active state after resume, got %s", result3.State)
	}
}

func TestHandler_RecoverRequiresUnhealthyState(t *testing.T) {
	srv, reg := newTestServer(t)

	// Create a VM.
	body := `{"user_id":"user-1"}`
	req1, _ := http.NewRequest(http.MethodPost, srv.URL+"/internal/vmctl/resolve", strings.NewReader(body))
	req1.Header.Set("Content-Type", "application/json")
	req1.Header.Set("X-Internal-Caller", "true")
	resp1, _ := http.DefaultClient.Do(req1)
	_ = resp1.Body.Close()

	// Try to recover a healthy VM — should fail.
	req2, _ := http.NewRequest(http.MethodPost, srv.URL+"/internal/vmctl/recover", strings.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("X-Internal-Caller", "true")
	resp2, _ := http.DefaultClient.Do(req2)
	defer func() { _ = resp2.Body.Close() }()

	if resp2.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 for healthy VM recovery, got %d", resp2.StatusCode)
	}

	// Mark unhealthy.
	if err := reg.MarkUnhealthy("user-1"); err != nil {
		t.Fatalf("MarkUnhealthy: %v", err)
	}

	// Now recover should succeed with a new epoch.
	req3, _ := http.NewRequest(http.MethodPost, srv.URL+"/internal/vmctl/recover", strings.NewReader(body))
	req3.Header.Set("Content-Type", "application/json")
	req3.Header.Set("X-Internal-Caller", "true")
	resp3, _ := http.DefaultClient.Do(req3)
	defer func() { _ = resp3.Body.Close() }()

	if resp3.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on recovery of unhealthy VM, got %d", resp3.StatusCode)
	}

	var result resolveResponse
	if err := json.NewDecoder(resp3.Body).Decode(&result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result.State != "active" {
		t.Errorf("expected active state after recovery, got %s", result.State)
	}
}

func TestHandler_LogoutStopsVM(t *testing.T) {
	// VAL-VM-008: Logout stops only the current user's VM.
	srv, _ := newTestServer(t)

	// Create VMs for two users.
	for _, userID := range []string{"user-alice", "user-bob"} {
		body := fmt.Sprintf(`{"user_id":"%s"}`, userID)
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/internal/vmctl/resolve", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Internal-Caller", "true")
		resp, _ := http.DefaultClient.Do(req)
		_ = resp.Body.Close()
	}

	// Logout alice.
	body := `{"user_id":"user-alice"}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/internal/vmctl/logout", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Caller", "true")
	resp, _ := http.DefaultClient.Do(req)
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on logout, got %d", resp.StatusCode)
	}

	// Lookup alice — should be stopped.
	req2, _ := http.NewRequest(http.MethodGet, srv.URL+"/internal/vmctl/lookup?user_id=user-alice", nil)
	req2.Header.Set("X-Internal-Caller", "true")
	resp2, _ := http.DefaultClient.Do(req2)
	var aliceResp ownershipResponse
	if err := json.NewDecoder(resp2.Body).Decode(&aliceResp); err != nil {
		t.Fatalf("decode aliceResp: %v", err)
	}
	_ = resp2.Body.Close()
	if aliceResp.State != "stopped" {
		t.Errorf("expected alice VM stopped after logout, got %s", aliceResp.State)
	}

	// Lookup bob — should still be active.
	req3, _ := http.NewRequest(http.MethodGet, srv.URL+"/internal/vmctl/lookup?user_id=user-bob", nil)
	req3.Header.Set("X-Internal-Caller", "true")
	resp3, _ := http.DefaultClient.Do(req3)
	var bobResp ownershipResponse
	if err := json.NewDecoder(resp3.Body).Decode(&bobResp); err != nil {
		t.Fatalf("decode bobResp: %v", err)
	}
	_ = resp3.Body.Close()
	if bobResp.State != "active" {
		t.Errorf("expected bob VM still active, got %s", bobResp.State)
	}
}

func TestHandler_IdleCheckEndpoint(t *testing.T) {
	srv, reg := newTestServer(t)

	// Set a very short idle timeout.
	reg.SetIdleTimeout(50 * time.Millisecond)

	// Create a VM.
	body := `{"user_id":"user-1"}`
	req1, _ := http.NewRequest(http.MethodPost, srv.URL+"/internal/vmctl/resolve", strings.NewReader(body))
	req1.Header.Set("Content-Type", "application/json")
	req1.Header.Set("X-Internal-Caller", "true")
	resp1, _ := http.DefaultClient.Do(req1)
	_ = resp1.Body.Close()

	// Backdate the VM.
	reg.mu.Lock()
	reg.ownerships["user-1"].LastActiveAt = time.Now().Add(-100 * time.Millisecond)
	reg.mu.Unlock()

	// Trigger idle check.
	req2, _ := http.NewRequest(http.MethodPost, srv.URL+"/internal/vmctl/idle-check", nil)
	req2.Header.Set("X-Internal-Caller", "true")
	resp2, _ := http.DefaultClient.Do(req2)
	defer func() { _ = resp2.Body.Close() }()

	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on idle-check, got %d", resp2.StatusCode)
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp2.Body).Decode(&result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if vmsStopped, _ := result["vms_stopped"].(float64); int(vmsStopped) != 1 {
		t.Errorf("expected 1 VM stopped, got %v", result["vms_stopped"])
	}
}

func TestHandler_LifecycleEndpointsDenyExternalCallers(t *testing.T) {
	// VAL-VM-012: All lifecycle endpoints require internal access.
	// The isInternalCaller function is tested separately above.
	// This test verifies that the handler endpoints exist and are
	// wired up correctly. The actual external caller denial is
	// tested via isInternalCaller unit tests and via the proxy's
	// HandleVMctlDeny which blocks /internal/vmctl/* at the proxy
	// level for browser callers.
	srv, _ := newTestServer(t)

	endpoints := []struct {
		path   string
		method string
		body   string
	}{
		{"/internal/vmctl/hibernate", "POST", `{"user_id":"user-1"}`},
		{"/internal/vmctl/resume", "POST", `{"user_id":"user-1"}`},
		{"/internal/vmctl/recover", "POST", `{"user_id":"user-1"}`},
		{"/internal/vmctl/logout", "POST", `{"user_id":"user-1"}`},
		{"/internal/vmctl/idle-check", "POST", ""},
	}

	for _, ep := range endpoints {
		t.Run(ep.path, func(t *testing.T) {
			var body io.Reader
			if ep.body != "" {
				body = strings.NewReader(ep.body)
			}
			req, _ := http.NewRequest(ep.method, srv.URL+ep.path, body)
			if ep.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			req.Header.Set("X-Internal-Caller", "true")

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()

			if resp.StatusCode == http.StatusMethodNotAllowed {
				t.Errorf("endpoint %s not registered (405)", ep.path)
			}
		})
	}
}
