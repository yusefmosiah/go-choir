package runtime

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlePromptListReturnsEffectivePrompts(t *testing.T) {
	_, handler := testAPISetup(t)

	req := authenticatedRequest(http.MethodGet, "/api/prompts", "", "user-alice")
	w := httptest.NewRecorder()
	handler.HandlePromptList(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", w.Code, http.StatusOK)
	}

	var resp promptListResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Prompts) != len(promptRoles()) {
		t.Fatalf("prompt count = %d, want %d", len(resp.Prompts), len(promptRoles()))
	}
}

func TestHandlePromptRoleSupportsSaveAndReset(t *testing.T) {
	_, handler := testAPISetup(t)

	putReq := authenticatedRequest(http.MethodPut, "/api/prompts/vtext", `{"content":"Custom prompt"}`, "user-alice")
	putW := httptest.NewRecorder()
	handler.HandlePromptRole(putW, putReq)

	if putW.Code != http.StatusOK {
		t.Fatalf("put status: got %d, want %d", putW.Code, http.StatusOK)
	}

	var putResp promptDescriptorResponse
	if err := json.NewDecoder(putW.Body).Decode(&putResp); err != nil {
		t.Fatalf("decode put response: %v", err)
	}
	if putResp.Source != "user" {
		t.Fatalf("put source = %q, want user", putResp.Source)
	}

	getReq := authenticatedRequest(http.MethodGet, "/api/prompts/vtext", "", "user-alice")
	getW := httptest.NewRecorder()
	handler.HandlePromptRole(getW, getReq)
	if getW.Code != http.StatusOK {
		t.Fatalf("get status: got %d, want %d", getW.Code, http.StatusOK)
	}
	var getResp promptDescriptorResponse
	if err := json.NewDecoder(getW.Body).Decode(&getResp); err != nil {
		t.Fatalf("decode get response: %v", err)
	}
	if getResp.Content != "Custom prompt" {
		t.Fatalf("get content = %q, want custom override", getResp.Content)
	}

	deleteReq := authenticatedRequest(http.MethodDelete, "/api/prompts/vtext", "", "user-alice")
	deleteW := httptest.NewRecorder()
	handler.HandlePromptRole(deleteW, deleteReq)
	if deleteW.Code != http.StatusOK {
		t.Fatalf("delete status: got %d, want %d", deleteW.Code, http.StatusOK)
	}
	var deleteResp promptDescriptorResponse
	if err := json.NewDecoder(deleteW.Body).Decode(&deleteResp); err != nil {
		t.Fatalf("decode delete response: %v", err)
	}
	if deleteResp.Source != "default" {
		t.Fatalf("delete source = %q, want default", deleteResp.Source)
	}
	if strings.TrimSpace(deleteResp.Content) == "" || deleteResp.Content == "Custom prompt" {
		t.Fatalf("delete should restore default prompt, got %q", deleteResp.Content)
	}
}
