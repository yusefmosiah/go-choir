package vmctl

import (
	"encoding/json"
	"fmt"
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

	reg.ResolveOrAssign("user-1")
	if count := reg.ActiveCount(); count != 1 {
		t.Errorf("expected 1 active VM, got %d", count)
	}

	reg.ResolveOrAssign("user-2")
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

	reg.RemoveOwnership("user-1")

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

	reg.ResolveOrAssign("user-1")
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

	reg.ResolveOrAssign("user-1")
	reg.ResolveOrAssign("user-2")
	reg.ResolveOrAssign("user-3")

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

func TestOwnershipRegistry_StoppedVMGetsNewAssignment(t *testing.T) {
	// When a user's VM is stopped, a new ResolveOrAssign should create a new VM.
	reg := NewOwnershipRegistry("http://127.0.0.1:8085")

	own1, _ := reg.ResolveOrAssign("user-1")
	oldVMID := own1.VMID

	reg.StopVM("user-1")

	own2, _ := reg.ResolveOrAssign("user-1")
	if own2.VMID == oldVMID {
		t.Error("expected new VM ID after stop, got same ID")
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
	json.NewDecoder(resp1.Body).Decode(&result1)
	_ = resp1.Body.Close()

	// Second resolve.
	req2, _ := http.NewRequest(http.MethodPost, srv.URL+"/internal/vmctl/resolve", strings.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("X-Internal-Caller", "true")
	resp2, _ := http.DefaultClient.Do(req2)
	var result2 resolveResponse
	json.NewDecoder(resp2.Body).Decode(&result2)
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
	json.NewDecoder(resp2.Body).Decode(&result)
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
	json.NewDecoder(resp3.Body).Decode(&result)
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
	json.NewDecoder(resp.Body).Decode(&result)

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

	client.Resolve("user-stop-test")

	if err := client.Stop("user-stop-test"); err != nil {
		t.Fatalf("client stop: %v", err)
	}
}

func TestClient_Remove(t *testing.T) {
	srv, _ := newTestServer(t)
	client := NewClient(srv.URL)

	client.Resolve("user-remove-test")

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

	reg.ResolveOrAssign("user-1")
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
}
