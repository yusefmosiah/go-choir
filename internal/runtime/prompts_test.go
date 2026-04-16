package runtime

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func testPromptAPISetup(t *testing.T) (*Runtime, *APIHandler) {
	t.Helper()
	rt, handler := testAPISetup(t)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get cwd: %v", err)
	}
	if err := rt.InstallDefaultAgentTools(cwd); err != nil {
		t.Fatalf("install default agent tools: %v", err)
	}
	return rt, handler
}

func TestHandlePromptListReturnsEffectivePrompts(t *testing.T) {
	_, handler := testPromptAPISetup(t)

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
	for _, prompt := range resp.Prompts {
		if strings.TrimSpace(prompt.SourceLabel) == "" {
			t.Fatalf("prompt %s missing source label", prompt.Role)
		}
		if strings.TrimSpace(prompt.EffectiveSystemPrompt) == "" {
			t.Fatalf("prompt %s missing effective system prompt", prompt.Role)
		}
		if len(prompt.Tools) == 0 {
			t.Fatalf("prompt %s missing tool metadata", prompt.Role)
		}
		if strings.TrimSpace(prompt.ProviderPolicy.ActiveProvider) == "" {
			t.Fatalf("prompt %s missing provider policy", prompt.Role)
		}
	}
}

func TestHandlePromptRoleSupportsSaveAndReset(t *testing.T) {
	_, handler := testPromptAPISetup(t)

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
	if !strings.Contains(getResp.EffectiveSystemPrompt, "Available tools:") {
		t.Fatalf("effective system prompt should include tool catalog, got %q", getResp.EffectiveSystemPrompt)
	}
	if len(getResp.Tools) == 0 {
		t.Fatal("expected tools in prompt response")
	}
	if getResp.RolePolicy.Profile != AgentProfileVText {
		t.Fatalf("role policy profile = %q, want %q", getResp.RolePolicy.Profile, AgentProfileVText)
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
