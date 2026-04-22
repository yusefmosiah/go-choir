package runtime

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/yusefmosiah/go-choir/internal/types"
)

// bytesReader is a convenience wrapper for creating an io.Reader from a byte slice.
func bytesReader(b []byte) *bytes.Reader {
	return bytes.NewReader(b)
}

func TestDesktopStateGetUnauthenticated(t *testing.T) {
	_, h := testAPISetup(t)

	req := httptest.NewRequest(http.MethodGet, "/api/desktop/state", nil)
	// No X-Authenticated-User header — should be denied.
	w := httptest.NewRecorder()
	h.HandleDesktopStateGet(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestDesktopStateGetEmpty(t *testing.T) {
	_, h := testAPISetup(t)

	req := httptest.NewRequest(http.MethodGet, "/api/desktop/state", nil)
	req.Header.Set("X-Authenticated-User", "user-1")
	w := httptest.NewRecorder()
	h.HandleDesktopStateGet(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp desktopStateGetResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.OwnerID != "user-1" {
		t.Errorf("OwnerID = %q, want %q", resp.OwnerID, "user-1")
	}
	if len(resp.Windows) != 0 {
		t.Errorf("Windows count = %d, want 0", len(resp.Windows))
	}
}

func TestDesktopStateSaveAndGet(t *testing.T) {
	_, h := testAPISetup(t)

	// Save desktop state.
	saveReq := desktopStateSaveRequest{
		Windows: []types.WindowState{
			{
				WindowID: "win-1",
				AppID:    "vtext",
				Title:    "E-Text Editor",
				Geometry: types.WindowGeometry{X: 100, Y: 100, Width: 600, Height: 400},
				Mode:     types.WindowNormal,
				ZIndex:   1,
			},
		},
		ActiveWindowID: "win-1",
	}

	body, _ := json.Marshal(saveReq)
	req := httptest.NewRequest(http.MethodPut, "/api/desktop/state", bytesReader(body))
	req.Header.Set("X-Authenticated-User", "user-1")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleDesktopStateSave(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("save status = %d, want %d", w.Code, http.StatusOK)
	}

	// Get the saved state.
	getReq := httptest.NewRequest(http.MethodGet, "/api/desktop/state", nil)
	getReq.Header.Set("X-Authenticated-User", "user-1")
	getW := httptest.NewRecorder()
	h.HandleDesktopStateGet(getW, getReq)

	if getW.Code != http.StatusOK {
		t.Fatalf("get status = %d, want %d", getW.Code, http.StatusOK)
	}

	var resp desktopStateGetResponse
	if err := json.NewDecoder(getW.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.OwnerID != "user-1" {
		t.Errorf("OwnerID = %q, want %q", resp.OwnerID, "user-1")
	}
	if len(resp.Windows) != 1 {
		t.Fatalf("Windows count = %d, want 1", len(resp.Windows))
	}
	if resp.Windows[0].WindowID != "win-1" {
		t.Errorf("Window[0].WindowID = %q, want %q", resp.Windows[0].WindowID, "win-1")
	}
	if resp.Windows[0].AppID != "vtext" {
		t.Errorf("Window[0].AppID = %q, want %q", resp.Windows[0].AppID, "vtext")
	}
	if resp.ActiveWindowID != "win-1" {
		t.Errorf("ActiveWindowID = %q, want %q", resp.ActiveWindowID, "win-1")
	}
}

func TestDesktopStateSaveUnauthenticated(t *testing.T) {
	_, h := testAPISetup(t)

	saveReq := desktopStateSaveRequest{
		Windows:        []types.WindowState{},
		ActiveWindowID: "",
	}

	body, _ := json.Marshal(saveReq)
	req := httptest.NewRequest(http.MethodPut, "/api/desktop/state", bytesReader(body))
	// No X-Authenticated-User header — should be denied.
	w := httptest.NewRecorder()
	h.HandleDesktopStateSave(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestDesktopStateUserIsolation(t *testing.T) {
	_, h := testAPISetup(t)

	// Save state for user-1.
	saveReq1 := desktopStateSaveRequest{
		Windows: []types.WindowState{
			{WindowID: "win-a", AppID: "vtext", Title: "User 1 Doc", Geometry: types.WindowGeometry{X: 10, Y: 10, Width: 400, Height: 300}, Mode: types.WindowNormal, ZIndex: 1},
		},
		ActiveWindowID: "win-a",
	}
	body1, _ := json.Marshal(saveReq1)
	req1 := httptest.NewRequest(http.MethodPut, "/api/desktop/state", bytesReader(body1))
	req1.Header.Set("X-Authenticated-User", "user-1")
	req1.Header.Set("Content-Type", "application/json")
	w1 := httptest.NewRecorder()
	h.HandleDesktopStateSave(w1, req1)

	if w1.Code != http.StatusOK {
		t.Fatalf("save user-1 status = %d, want %d", w1.Code, http.StatusOK)
	}

	// Save state for user-2.
	saveReq2 := desktopStateSaveRequest{
		Windows: []types.WindowState{
			{WindowID: "win-b", AppID: "terminal", Title: "User 2 Terminal", Geometry: types.WindowGeometry{X: 20, Y: 20, Width: 500, Height: 400}, Mode: types.WindowNormal, ZIndex: 1},
		},
		ActiveWindowID: "win-b",
	}
	body2, _ := json.Marshal(saveReq2)
	req2 := httptest.NewRequest(http.MethodPut, "/api/desktop/state", bytesReader(body2))
	req2.Header.Set("X-Authenticated-User", "user-2")
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	h.HandleDesktopStateSave(w2, req2)

	if w2.Code != http.StatusOK {
		t.Fatalf("save user-2 status = %d, want %d", w2.Code, http.StatusOK)
	}

	// Verify user-1's state is independent.
	getReq1 := httptest.NewRequest(http.MethodGet, "/api/desktop/state", nil)
	getReq1.Header.Set("X-Authenticated-User", "user-1")
	getW1 := httptest.NewRecorder()
	h.HandleDesktopStateGet(getW1, getReq1)

	var resp1 desktopStateGetResponse
	if err := json.NewDecoder(getW1.Body).Decode(&resp1); err != nil {
		t.Fatalf("decode user-1 response: %v", err)
	}
	if len(resp1.Windows) != 1 || resp1.Windows[0].AppID != "vtext" {
		t.Errorf("user-1 desktop state was affected by user-2 save")
	}

	// Verify user-2's state is independent.
	getReq2 := httptest.NewRequest(http.MethodGet, "/api/desktop/state", nil)
	getReq2.Header.Set("X-Authenticated-User", "user-2")
	getW2 := httptest.NewRecorder()
	h.HandleDesktopStateGet(getW2, getReq2)

	var resp2 desktopStateGetResponse
	if err := json.NewDecoder(getW2.Body).Decode(&resp2); err != nil {
		t.Fatalf("decode user-2 response: %v", err)
	}
	if len(resp2.Windows) != 1 || resp2.Windows[0].AppID != "terminal" {
		t.Errorf("user-2 desktop state incorrect")
	}
}

func TestDesktopStateRouterMethodDispatch(t *testing.T) {
	_, h := testAPISetup(t)

	// POST should be method not allowed.
	req := httptest.NewRequest(http.MethodPost, "/api/desktop/state", nil)
	req.Header.Set("X-Authenticated-User", "user-1")
	w := httptest.NewRecorder()
	h.HandleDesktopState(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}

	// DELETE should be method not allowed.
	req2 := httptest.NewRequest(http.MethodDelete, "/api/desktop/state", nil)
	req2.Header.Set("X-Authenticated-User", "user-1")
	w2 := httptest.NewRecorder()
	h.HandleDesktopState(w2, req2)

	if w2.Code != http.StatusMethodNotAllowed {
		t.Errorf("DELETE status = %d, want %d", w2.Code, http.StatusMethodNotAllowed)
	}

	// GET should work.
	req3 := httptest.NewRequest(http.MethodGet, "/api/desktop/state", nil)
	req3.Header.Set("X-Authenticated-User", "user-1")
	w3 := httptest.NewRecorder()
	h.HandleDesktopState(w3, req3)

	if w3.Code != http.StatusOK {
		t.Errorf("GET status = %d, want %d", w3.Code, http.StatusOK)
	}
}

func TestDesktopStateSaveAndGetByDesktopSelector(t *testing.T) {
	_, h := testAPISetup(t)

	saveReq := desktopStateSaveRequest{
		Windows: []types.WindowState{
			{
				WindowID: "win-branch",
				AppID:    "vtext",
				Title:    "Branch desktop",
				Geometry: types.WindowGeometry{X: 50, Y: 60, Width: 700, Height: 500},
				Mode:     types.WindowNormal,
				ZIndex:   1,
			},
		},
		ActiveWindowID: "win-branch",
	}

	body, _ := json.Marshal(saveReq)
	save := httptest.NewRequest(http.MethodPut, "/api/desktop/state?desktop_id=branch-a", bytesReader(body))
	save.Header.Set("X-Authenticated-User", "user-1")
	save.Header.Set("Content-Type", "application/json")
	saveW := httptest.NewRecorder()
	h.HandleDesktopStateSave(saveW, save)
	if saveW.Code != http.StatusOK {
		t.Fatalf("save status = %d, want %d", saveW.Code, http.StatusOK)
	}

	getBranch := httptest.NewRequest(http.MethodGet, "/api/desktop/state?desktop_id=branch-a", nil)
	getBranch.Header.Set("X-Authenticated-User", "user-1")
	getBranchW := httptest.NewRecorder()
	h.HandleDesktopStateGet(getBranchW, getBranch)
	if getBranchW.Code != http.StatusOK {
		t.Fatalf("branch get status = %d, want %d", getBranchW.Code, http.StatusOK)
	}
	var branchResp desktopStateGetResponse
	if err := json.NewDecoder(getBranchW.Body).Decode(&branchResp); err != nil {
		t.Fatalf("decode branch response: %v", err)
	}
	if branchResp.DesktopID != "branch-a" {
		t.Errorf("branch DesktopID = %q, want %q", branchResp.DesktopID, "branch-a")
	}
	if len(branchResp.Windows) != 1 || branchResp.Windows[0].WindowID != "win-branch" {
		t.Fatalf("branch desktop windows mismatch: %+v", branchResp.Windows)
	}

	getPrimary := httptest.NewRequest(http.MethodGet, "/api/desktop/state", nil)
	getPrimary.Header.Set("X-Authenticated-User", "user-1")
	getPrimaryW := httptest.NewRecorder()
	h.HandleDesktopStateGet(getPrimaryW, getPrimary)
	if getPrimaryW.Code != http.StatusOK {
		t.Fatalf("primary get status = %d, want %d", getPrimaryW.Code, http.StatusOK)
	}
	var primaryResp desktopStateGetResponse
	if err := json.NewDecoder(getPrimaryW.Body).Decode(&primaryResp); err != nil {
		t.Fatalf("decode primary response: %v", err)
	}
	if primaryResp.DesktopID != types.PrimaryDesktopID {
		t.Errorf("primary DesktopID = %q, want %q", primaryResp.DesktopID, types.PrimaryDesktopID)
	}
	if len(primaryResp.Windows) != 0 {
		t.Fatalf("expected empty primary desktop state, got %+v", primaryResp.Windows)
	}
}
