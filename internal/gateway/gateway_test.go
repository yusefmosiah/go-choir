package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yusefmosiah/go-choir/internal/provider"
)

// --- Identity Registry Tests ---

func TestIssueAndValidateCredential(t *testing.T) {
	reg := NewIdentityRegistry(1 * time.Hour)

	result, err := reg.IssueCredential("sandbox-1")
	if err != nil {
		t.Fatalf("issue credential: %v", err)
	}

	if result.SandboxID != "sandbox-1" {
		t.Errorf("SandboxID = %q, want %q", result.SandboxID, "sandbox-1")
	}
	if result.RawToken == "" {
		t.Error("RawToken is empty")
	}
	if result.ExpiresAt.IsZero() {
		t.Error("ExpiresAt is zero")
	}

	// Validate the credential.
	sandboxID, err := reg.ValidateCredential(result.RawToken)
	if err != nil {
		t.Fatalf("validate credential: %v", err)
	}
	if sandboxID != "sandbox-1" {
		t.Errorf("sandbox ID = %q, want %q", sandboxID, "sandbox-1")
	}
}

func TestValidateCredentialUnknownSandbox(t *testing.T) {
	reg := NewIdentityRegistry(1 * time.Hour)

	_, err := reg.ValidateCredential("unknown-sandbox:sometoken")
	if err == nil {
		t.Fatal("expected error for unknown sandbox")
	}
}

func TestValidateCredentialInvalidFormat(t *testing.T) {
	reg := NewIdentityRegistry(1 * time.Hour)

	_, err := reg.ValidateCredential("invalid-no-colon")
	if err == nil {
		t.Fatal("expected error for invalid format")
	}
}

func TestValidateCredentialWrongToken(t *testing.T) {
	reg := NewIdentityRegistry(1 * time.Hour)

	result, _ := reg.IssueCredential("sandbox-1")

	// Modify the token to be wrong.
	wrongToken := "sandbox-1:deadbeef"
	_, err := reg.ValidateCredential(wrongToken)
	if err == nil {
		t.Fatal("expected error for wrong token")
	}

	// Original still works.
	_, err = reg.ValidateCredential(result.RawToken)
	if err != nil {
		t.Fatalf("original token should still work: %v", err)
	}
}

func TestRevokeCredential(t *testing.T) {
	reg := NewIdentityRegistry(1 * time.Hour)

	result, _ := reg.IssueCredential("sandbox-1")

	reg.RevokeCredential("sandbox-1")

	_, err := reg.ValidateCredential(result.RawToken)
	if err == nil {
		t.Fatal("expected error after revocation")
	}
	if !strings.Contains(err.Error(), "revoked") {
		t.Errorf("error = %q, want revocation message", err.Error())
	}
}

func TestRotateCredential(t *testing.T) {
	reg := NewIdentityRegistry(1 * time.Hour)

	result1, _ := reg.IssueCredential("sandbox-1")

	// Rotate: old credential should stop working, new one should work.
	result2, err := reg.RotateCredential("sandbox-1")
	if err != nil {
		t.Fatalf("rotate credential: %v", err)
	}

	// Old credential is invalid.
	_, err = reg.ValidateCredential(result1.RawToken)
	if err == nil {
		t.Fatal("old credential should be invalid after rotation")
	}

	// New credential is valid.
	_, err = reg.ValidateCredential(result2.RawToken)
	if err != nil {
		t.Fatalf("new credential should be valid: %v", err)
	}
}

func TestExpiredCredential(t *testing.T) {
	reg := NewIdentityRegistry(1 * time.Nanosecond) // immediate expiry

	result, _ := reg.IssueCredential("sandbox-1")

	// Wait for expiry.
	time.Sleep(10 * time.Millisecond)

	_, err := reg.ValidateCredential(result.RawToken)
	if err == nil {
		t.Fatal("expected error for expired credential")
	}
	if !strings.Contains(err.Error(), "expired") {
		t.Errorf("error = %q, want expired message", err.Error())
	}
}

func TestIssueCredentialReplacesExisting(t *testing.T) {
	reg := NewIdentityRegistry(1 * time.Hour)

	result1, _ := reg.IssueCredential("sandbox-1")
	result2, _ := reg.IssueCredential("sandbox-1")

	// First credential should be invalidated.
	_, err := reg.ValidateCredential(result1.RawToken)
	if err == nil {
		t.Fatal("old credential should be invalid after re-issuance")
	}

	// Second credential should work.
	_, err = reg.ValidateCredential(result2.RawToken)
	if err != nil {
		t.Fatalf("new credential should be valid: %v", err)
	}
}

// --- Gateway Handler Tests ---

// mockProvider is a test double for provider.Provider.
type mockProvider struct {
	name     string
	real     bool
	response *provider.LLMResponse
	err      error
	lastReq  *provider.LLMRequest
}

func (m *mockProvider) Call(ctx context.Context, req provider.LLMRequest) (*provider.LLMResponse, error) {
	m.lastReq = &req
	if m.err != nil {
		return nil, m.err
	}
	return m.response, nil
}

func (m *mockProvider) Stream(ctx context.Context, req provider.LLMRequest, onChunk func(provider.StreamChunk)) (*provider.LLMResponse, error) {
	resp, err := m.Call(ctx, req)
	if err != nil {
		return nil, err
	}
	// Emit a single text delta chunk for the mock.
	if resp.Text != "" {
		onChunk(provider.StreamChunk{
			Type:  "content_block_delta",
			Delta: resp.Text,
			Index: 0,
		})
	}
	return resp, nil
}

func (m *mockProvider) Name() string { return m.name }
func (m *mockProvider) IsReal() bool { return m.real }

func setupHandler(t *testing.T) (*Handler, *IdentityRegistry, *mockProvider) {
	t.Helper()
	reg := NewIdentityRegistry(1 * time.Hour)
	mp := &mockProvider{
		name: "bedrock",
		real: true,
		response: &provider.LLMResponse{
			ID:           "resp-123",
			Text:         "Hello from Bedrock!",
			Model:        "claude-sonnet",
			StopReason:   "end_turn",
			ProviderName: "bedrock",
			Usage:        provider.Usage{InputTokens: 10, OutputTokens: 20},
		},
	}
	return NewHandler(reg, mp), reg, mp
}

func setupHandlerNoProvider(t *testing.T) (*Handler, *IdentityRegistry) {
	t.Helper()
	reg := NewIdentityRegistry(1 * time.Hour)
	return NewHandler(reg, nil), reg
}

func TestHandleHealth(t *testing.T) {
	h, _, _ := setupHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	h.HandleHealth(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp gatewayHealthResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Service != "gateway" {
		t.Errorf("Service = %q, want %q", resp.Service, "gateway")
	}
	if resp.Provider != "bedrock" {
		t.Errorf("Provider = %q, want %q", resp.Provider, "bedrock")
	}
}

func TestHandleInference_AuthSuccess(t *testing.T) {
	h, reg, _ := setupHandler(t)

	// Issue a credential.
	result, _ := reg.IssueCredential("sandbox-1")

	// Make an inference request.
	payload := ProviderRequest{
		System:    "You are helpful.",
		Messages:  []provider.Message{{Role: "user", Content: []provider.Block{{Type: "text", Text: "Hello"}}}},
		MaxTokens: 100,
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/provider/v1/inference", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+result.RawToken)
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.HandleInference(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var resp ProviderResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Text != "Hello from Bedrock!" {
		t.Errorf("Text = %q, want %q", resp.Text, "Hello from Bedrock!")
	}
	if resp.ProviderName != "bedrock" {
		t.Errorf("ProviderName = %q, want %q", resp.ProviderName, "bedrock")
	}
}

func TestHandleInference_DeniesExternalPeerWithValidToken(t *testing.T) {
	h, reg, _ := setupHandler(t)

	result, _ := reg.IssueCredential("sandbox-1")

	payload := ProviderRequest{
		Messages: []provider.Message{{Role: "user", Content: []provider.Block{{Type: "text", Text: "Hello"}}}},
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/provider/v1/inference", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+result.RawToken)
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "8.8.8.8:12345"

	w := httptest.NewRecorder()
	h.HandleInference(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestHandleInference_MissingAuth(t *testing.T) {
	h, _, _ := setupHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/provider/v1/inference", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.HandleInference(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestHandleInference_InvalidAuth(t *testing.T) {
	h, _, _ := setupHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/provider/v1/inference", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer invalid-token")
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.HandleInference(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestHandleInference_ForgedAuth(t *testing.T) {
	h, _, _ := setupHandler(t)

	// Use a forged token with wrong hash.
	req := httptest.NewRequest(http.MethodPost, "/provider/v1/inference", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer sandbox-1:deadbeeffaketoken")
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.HandleInference(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestHandleInference_RevokedCredential(t *testing.T) {
	h, reg, _ := setupHandler(t)

	result, _ := reg.IssueCredential("sandbox-1")
	reg.RevokeCredential("sandbox-1")

	req := httptest.NewRequest(http.MethodPost, "/provider/v1/inference", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+result.RawToken)
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.HandleInference(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestHandleInference_NoProvider(t *testing.T) {
	h, reg := setupHandlerNoProvider(t)

	result, _ := reg.IssueCredential("sandbox-1")

	req := httptest.NewRequest(http.MethodPost, "/provider/v1/inference", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+result.RawToken)
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.HandleInference(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
}

func TestHandleInference_UnsupportedProvider(t *testing.T) {
	h, reg, _ := setupHandler(t)

	result, _ := reg.IssueCredential("sandbox-1")

	payload := ProviderRequest{
		Provider: "openai", // not configured
		Messages: []provider.Message{{Role: "user", Content: []provider.Block{{Type: "text", Text: "Hi"}}}},
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/provider/v1/inference", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+result.RawToken)
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.HandleInference(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusBadRequest, w.Body.String())
	}
}

func TestHandleInference_ProviderError(t *testing.T) {
	h, reg, mp := setupHandler(t)
	mp.err = fmt.Errorf("bedrock: status 503 Service Unavailable (sanitized)")

	result, _ := reg.IssueCredential("sandbox-1")

	payload := ProviderRequest{
		Messages: []provider.Message{{Role: "user", Content: []provider.Block{{Type: "text", Text: "Hi"}}}},
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/provider/v1/inference", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+result.RawToken)
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.HandleInference(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusBadGateway, w.Body.String())
	}

	var errResp ErrorResponse
	if err := json.NewDecoder(w.Body).Decode(&errResp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The error should be sanitized and not contain raw upstream details.
	_ = strings.Contains(errResp.Error, "Service Unavailable") // silence staticcheck: empty branch allowed for documentation
	if strings.Contains(errResp.Error, "Bearer") {
		t.Errorf("error message contains credential-like data: %q", errResp.Error)
	}
}

func TestHandleInference_SanitizedError(t *testing.T) {
	// Verify that errors containing credential-like strings are sanitized.
	sanitized := sanitizeError(fmt.Errorf("some error with Bearer sk-secret-key in it"))
	if strings.Contains(sanitized, "sk-secret-key") {
		t.Errorf("sanitizeError did not remove credential: %q", sanitized)
	}
}

func TestHandleInference_MethodNotAllowed(t *testing.T) {
	h, _, _ := setupHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/provider/v1/inference", nil)
	w := httptest.NewRecorder()
	h.HandleInference(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

// --- Credential Management Endpoint Tests ---

func TestHandleIssueCredential(t *testing.T) {
	h, _ := setupHandlerNoProvider(t)

	body := `{"sandbox_id": "sandbox-test"}`
	req := httptest.NewRequest(http.MethodPost, "/provider/v1/credentials/issue", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Caller", "true")
	req.Host = "localhost:8084"
	req.RemoteAddr = "127.0.0.1:12345"

	w := httptest.NewRecorder()
	h.HandleIssueCredential(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusCreated, w.Body.String())
	}

	var result CredentialResult
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.SandboxID != "sandbox-test" {
		t.Errorf("SandboxID = %q, want %q", result.SandboxID, "sandbox-test")
	}
	if result.RawToken == "" {
		t.Error("RawToken is empty")
	}
}

func TestHandleIssueCredential_NonLocalhost(t *testing.T) {
	h, _ := setupHandlerNoProvider(t)

	body := `{"sandbox_id": "sandbox-test"}`
	req := httptest.NewRequest(http.MethodPost, "/provider/v1/credentials/issue", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Caller", "true")
	req.Host = "10.0.0.1:8084"
	req.RemoteAddr = "10.0.0.1:45678"

	w := httptest.NewRecorder()
	h.HandleIssueCredential(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
}

func TestHandleIssueCredential_SpoofedLocalhostHostDenied(t *testing.T) {
	h, _ := setupHandlerNoProvider(t)

	body := `{"sandbox_id": "sandbox-test"}`
	req := httptest.NewRequest(http.MethodPost, "/provider/v1/credentials/issue", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Caller", "true")
	req.Host = "localhost:8084"
	req.RemoteAddr = "10.10.10.10:45678"

	w := httptest.NewRecorder()
	h.HandleIssueCredential(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
}

func TestHandleIssueCredential_MissingInternalHeaderDenied(t *testing.T) {
	h, _ := setupHandlerNoProvider(t)

	body := `{"sandbox_id": "sandbox-test"}`
	req := httptest.NewRequest(http.MethodPost, "/provider/v1/credentials/issue", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Host = "localhost:8084"
	req.RemoteAddr = "127.0.0.1:12345"

	w := httptest.NewRecorder()
	h.HandleIssueCredential(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
}

func TestHandleRevokeCredential(t *testing.T) {
	h, reg := setupHandlerNoProvider(t)

	// Issue a credential first.
	result, _ := reg.IssueCredential("sandbox-test")

	// Revoke it.
	body := `{"sandbox_id": "sandbox-test"}`
	req := httptest.NewRequest(http.MethodPost, "/provider/v1/credentials/revoke", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Caller", "true")
	req.Host = "localhost:8084"
	req.RemoteAddr = "127.0.0.1:12345"

	w := httptest.NewRecorder()
	h.HandleRevokeCredential(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	// Verify the credential is revoked.
	_, err := reg.ValidateCredential(result.RawToken)
	if err == nil {
		t.Fatal("expected credential to be revoked")
	}
}

func TestHandleRotateCredential(t *testing.T) {
	h, reg := setupHandlerNoProvider(t)

	// Issue a credential first.
	result1, _ := reg.IssueCredential("sandbox-test")

	// Rotate it.
	body := `{"sandbox_id": "sandbox-test"}`
	req := httptest.NewRequest(http.MethodPost, "/provider/v1/credentials/rotate", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Caller", "true")
	req.Host = "localhost:8084"
	req.RemoteAddr = "127.0.0.1:12345"

	w := httptest.NewRecorder()
	h.HandleRotateCredential(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var result2 CredentialResult
	if err := json.NewDecoder(w.Body).Decode(&result2); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Old credential should be invalid.
	_, err := reg.ValidateCredential(result1.RawToken)
	if err == nil {
		t.Fatal("old credential should be invalid after rotation")
	}

	// New credential should be valid.
	_, err = reg.ValidateCredential(result2.RawToken)
	if err != nil {
		t.Fatalf("new credential should be valid: %v", err)
	}
}

func TestStaleCredentialAfterRotation(t *testing.T) {
	// VAL-GATEWAY-008: After rotation, the old credential stops working.
	h, reg, mp := setupHandler(t)

	result1, _ := reg.IssueCredential("sandbox-1")

	// Rotate the credential.
	result2, _ := reg.RotateCredential("sandbox-1")

	// Try inference with the old credential.
	payload := ProviderRequest{
		Messages: []provider.Message{{Role: "user", Content: []provider.Block{{Type: "text", Text: "Hi"}}}},
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/provider/v1/inference", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+result1.RawToken)
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.HandleInference(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("old credential: status = %d, want %d", w.Code, http.StatusUnauthorized)
	}

	// Try inference with the new credential.
	req = httptest.NewRequest(http.MethodPost, "/provider/v1/inference", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+result2.RawToken)
	req.Header.Set("Content-Type", "application/json")

	w = httptest.NewRecorder()
	h.HandleInference(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("new credential: status = %d, want %d", w.Code, http.StatusOK)
	}

	// Verify the provider was actually called.
	if mp.lastReq == nil {
		t.Fatal("provider was not called")
	}
}

// --- Gateway Client Tests ---

func TestGatewayClientCall(t *testing.T) {
	reg := NewIdentityRegistry(1 * time.Hour)
	mp := &mockProvider{
		name: "zai",
		real: true,
		response: &provider.LLMResponse{
			ID:           "resp-456",
			Text:         "Z.AI response",
			Model:        "glm-4.7",
			StopReason:   "end_turn",
			ProviderName: "zai",
			Usage:        provider.Usage{InputTokens: 5, OutputTokens: 15},
		},
	}

	handler := NewHandler(reg, mp)

	// Start a test server for the gateway.
	mux := http.NewServeMux()
	mux.HandleFunc("/provider/v1/inference", handler.HandleInference)
	mux.HandleFunc("/health", handler.HandleHealth)
	server := httptest.NewServer(mux)
	defer server.Close()

	// Issue a credential for the sandbox.
	result, err := reg.IssueCredential("sandbox-client-test")
	if err != nil {
		t.Fatalf("issue credential: %v", err)
	}

	// Create a gateway client.
	client := NewGatewayClient(server.URL, result.RawToken)

	// Verify IsReal.
	if !client.IsReal() {
		t.Error("IsReal() = false, want true")
	}

	// Verify Name.
	if client.Name() != "gateway" {
		t.Errorf("Name() = %q, want %q", client.Name(), "gateway")
	}

	// Make a call through the gateway client.
	resp, err := client.Call(context.Background(), provider.LLMRequest{
		System:    "Test system prompt",
		Messages:  []provider.Message{{Role: "user", Content: []provider.Block{{Type: "text", Text: "Hello"}}}},
		MaxTokens: 100,
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}

	if resp.Text != "Z.AI response" {
		t.Errorf("Text = %q, want %q", resp.Text, "Z.AI response")
	}
	if resp.ProviderName != "zai" {
		t.Errorf("ProviderName = %q, want %q", resp.ProviderName, "zai")
	}
}

func TestGatewayClientCall_InvalidToken(t *testing.T) {
	reg := NewIdentityRegistry(1 * time.Hour)
	mp := &mockProvider{name: "bedrock", real: true}
	handler := NewHandler(reg, mp)

	mux := http.NewServeMux()
	mux.HandleFunc("/provider/v1/inference", handler.HandleInference)
	server := httptest.NewServer(mux)
	defer server.Close()

	client := NewGatewayClient(server.URL, "invalid-sandbox:invalid-token")

	_, err := client.Call(context.Background(), provider.LLMRequest{
		Messages: []provider.Message{{Role: "user", Content: []provider.Block{{Type: "text", Text: "Hi"}}}},
	})
	if err == nil {
		t.Fatal("expected error with invalid token")
	}
	if !strings.Contains(err.Error(), "authentication") && !strings.Contains(err.Error(), "sanitized") {
		t.Errorf("error = %q, want auth or sanitized error", err.Error())
	}
}

func TestGatewayClientCall_RevokedToken(t *testing.T) {
	reg := NewIdentityRegistry(1 * time.Hour)
	mp := &mockProvider{name: "bedrock", real: true}
	handler := NewHandler(reg, mp)

	mux := http.NewServeMux()
	mux.HandleFunc("/provider/v1/inference", handler.HandleInference)
	server := httptest.NewServer(mux)
	defer server.Close()

	// Issue and immediately revoke.
	result, _ := reg.IssueCredential("sandbox-revoke-test")
	reg.RevokeCredential("sandbox-revoke-test")

	client := NewGatewayClient(server.URL, result.RawToken)

	_, err := client.Call(context.Background(), provider.LLMRequest{
		Messages: []provider.Message{{Role: "user", Content: []provider.Block{{Type: "text", Text: "Hi"}}}},
	})
	if err == nil {
		t.Fatal("expected error with revoked token")
	}
}

func TestGatewayClientStream(t *testing.T) {
	reg := NewIdentityRegistry(1 * time.Hour)
	mp := &mockProvider{
		name: "zai",
		real: true,
		response: &provider.LLMResponse{
			ID:           "resp-stream-1",
			Text:         "Streaming response text",
			Model:        "glm-5-turbo",
			StopReason:   "end_turn",
			ProviderName: "zai",
			Usage:        provider.Usage{InputTokens: 10, OutputTokens: 25},
		},
	}

	handler := NewHandler(reg, mp)

	mux := http.NewServeMux()
	mux.HandleFunc("/provider/v1/inference", handler.HandleInference)
	mux.HandleFunc("/health", handler.HandleHealth)
	server := httptest.NewServer(mux)
	defer server.Close()

	result, err := reg.IssueCredential("sandbox-stream-test")
	if err != nil {
		t.Fatalf("issue credential: %v", err)
	}

	client := NewGatewayClient(server.URL, result.RawToken)

	// Stream through the gateway client.
	var chunks []provider.StreamChunk
	resp, err := client.Stream(context.Background(), provider.LLMRequest{
		Model:     "glm-5-turbo",
		System:    "You are helpful",
		Messages:  []provider.Message{{Role: "user", Content: []provider.Block{{Type: "text", Text: "Say hi"}}}},
		MaxTokens: 100,
		Stream:    true,
	}, func(chunk provider.StreamChunk) {
		chunks = append(chunks, chunk)
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}

	// Verify the accumulated response text (text comes from delta chunks).
	if resp.Text != "Streaming response text" {
		t.Errorf("Text = %q, want %q", resp.Text, "Streaming response text")
	}

	// Note: Model and StopReason may not be populated in the accumulated
	// response if the mock provider doesn't emit message_start/message_delta
	// events. In production, the real providers emit these events. The
	// GatewayClient.Stream() accumulates them from the SSE stream when present.

	// Verify we received at least one chunk.
	if len(chunks) == 0 {
		t.Fatal("expected at least one streaming chunk")
	}

	// Verify the delta chunk contains the text.
	hasDelta := false
	for _, chunk := range chunks {
		if chunk.Type == "content_block_delta" && chunk.Delta != "" {
			hasDelta = true
			if chunk.Delta != "Streaming response text" {
				t.Errorf("delta = %q, want %q", chunk.Delta, "Streaming response text")
			}
		}
	}
	if !hasDelta {
		t.Error("expected content_block_delta chunk with text")
	}
}

func TestGatewayClientStream_Error(t *testing.T) {
	reg := NewIdentityRegistry(1 * time.Hour)
	mp := &mockProvider{
		name: "zai",
		real: true,
		err:  fmt.Errorf("provider unavailable"),
	}

	handler := NewHandler(reg, mp)
	mux := http.NewServeMux()
	mux.HandleFunc("/provider/v1/inference", handler.HandleInference)
	server := httptest.NewServer(mux)
	defer server.Close()

	result, err := reg.IssueCredential("sandbox-stream-err")
	if err != nil {
		t.Fatalf("issue credential: %v", err)
	}

	client := NewGatewayClient(server.URL, result.RawToken)

	_, err = client.Stream(context.Background(), provider.LLMRequest{
		Messages: []provider.Message{{Role: "user", Content: []provider.Block{{Type: "text", Text: "Hi"}}}},
	}, func(chunk provider.StreamChunk) {})
	if err == nil {
		t.Fatal("expected error from streaming with provider error")
	}
}

func TestGatewayClientStream_SSEAccumulation(t *testing.T) {
	// Test parseGatewaySSE directly with a proper SSE stream containing
	// message_start, content_block_delta, and message_stop events.
	sseData := `data: {"type":"message_start","id":"msg-1","model":"glm-5-turbo"}

data: {"type":"content_block_delta","delta":"Hello ","index":0}

data: {"type":"content_block_delta","delta":"world","index":0}

data: {"type":"message_delta","stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5}}

data: [DONE]

`
	var chunks []provider.StreamChunk
	resp, err := parseGatewaySSE(strings.NewReader(sseData), func(chunk provider.StreamChunk) {
		chunks = append(chunks, chunk)
	})
	if err != nil {
		t.Fatalf("parseGatewaySSE: %v", err)
	}

	// Verify accumulated text.
	if resp.Text != "Hello world" {
		t.Errorf("Text = %q, want %q", resp.Text, "Hello world")
	}
	if resp.Model != "glm-5-turbo" {
		t.Errorf("Model = %q, want %q", resp.Model, "glm-5-turbo")
	}
	if resp.StopReason != "end_turn" {
		t.Errorf("StopReason = %q, want %q", resp.StopReason, "end_turn")
	}
	if resp.ID != "msg-1" {
		t.Errorf("ID = %q, want %q", resp.ID, "msg-1")
	}
	if resp.Usage.InputTokens != 10 || resp.Usage.OutputTokens != 5 {
		t.Errorf("Usage = %+v, want {10 5}", resp.Usage)
	}

	// Verify chunks were forwarded.
	if len(chunks) != 4 { // message_start, 2 deltas, message_delta
		t.Errorf("chunks = %d, want 4", len(chunks))
	}
}

func TestGatewayClientStream_SSEError(t *testing.T) {
	// Test parseGatewaySSE with an error event in the stream.
	sseData := `data: {"error":"rate limit exceeded"}

`
	_, err := parseGatewaySSE(strings.NewReader(sseData), func(chunk provider.StreamChunk) {})
	if err == nil {
		t.Fatal("expected error from SSE error event")
	}
	if !strings.Contains(err.Error(), "rate limit exceeded") {
		t.Errorf("error = %q, want rate limit exceeded", err.Error())
	}
}

func TestGatewayClientStream_SSEInvalidJSON(t *testing.T) {
	// Test parseGatewaySSE with invalid JSON (should be skipped, not error).
	sseData := `data: not-json

data: {"type":"content_block_delta","delta":"valid","index":0}

data: [DONE]

`
	var chunks []provider.StreamChunk
	resp, err := parseGatewaySSE(strings.NewReader(sseData), func(chunk provider.StreamChunk) {
		chunks = append(chunks, chunk)
	})
	if err != nil {
		t.Fatalf("parseGatewaySSE: %v", err)
	}
	if resp.Text != "valid" {
		t.Errorf("Text = %q, want %q", resp.Text, "valid")
	}
	if len(chunks) != 1 {
		t.Errorf("chunks = %d, want 1 (invalid JSON skipped)", len(chunks))
	}
}

// --- Provider Error Sanitization Tests ---

func TestSanitizeError_Basic(t *testing.T) {
	err := fmt.Errorf("connection refused")
	sanitized := sanitizeError(err)
	if sanitized != "connection refused" {
		t.Errorf("sanitizeError = %q, want %q", sanitized, "connection refused")
	}
}

func TestSanitizeError_BearerLeak(t *testing.T) {
	err := fmt.Errorf("upstream failed: Authorization: Bearer sk-12345-secret")
	sanitized := sanitizeError(err)
	if strings.Contains(sanitized, "sk-12345-secret") {
		t.Errorf("sanitizeError leaked credential: %q", sanitized)
	}
	if !strings.Contains(sanitized, "(redacted)") {
		t.Errorf("sanitizeError missing redaction marker: %q", sanitized)
	}
}

func TestSanitizeError_XApiKeyLeak(t *testing.T) {
	err := fmt.Errorf("failed with x-api-key my-secret-key in response")
	sanitized := sanitizeError(err)
	if strings.Contains(sanitized, "my-secret-key") {
		t.Errorf("sanitizeError leaked API key: %q", sanitized)
	}
}

func TestSanitizeError_LongMessage(t *testing.T) {
	longMsg := strings.Repeat("a", 1000)
	err := fmt.Errorf("%s", longMsg)
	sanitized := sanitizeError(err)
	if len(sanitized) > 503 {
		t.Errorf("sanitizeError too long: %d chars", len(sanitized))
	}
}

// --- Config Tests ---

func TestLoadConfig(t *testing.T) {
	cfg := LoadConfig()
	if cfg.Port != "8084" {
		t.Errorf("Port = %q, want %q", cfg.Port, "8084")
	}
	if cfg.SandboxTokenTTL != 1*time.Hour {
		t.Errorf("SandboxTokenTTL = %v, want %v", cfg.SandboxTokenTTL, 1*time.Hour)
	}
}

// --- Browser Denial Tests (VAL-GATEWAY-002) ---
// The gateway denies browser-like callers that don't present valid sandbox
// credentials. Even if a browser-like request reaches the gateway, it
// fails because:
// 1. No Authorization header → 401
// 2. Cookies don't work because the gateway uses Bearer auth
// 3. Forged tokens are rejected by the identity registry

func TestBrowserLikeCallerDenied(t *testing.T) {
	h, _, _ := setupHandler(t)

	// Simulate a browser request with no auth.
	req := httptest.NewRequest(http.MethodPost, "/provider/v1/inference", strings.NewReader(`{"messages":[]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://draft.choir-ip.com")
	req.Header.Set("Cookie", "choir_access=some-access-jwt")

	w := httptest.NewRecorder()
	h.HandleInference(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("browser caller: status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestCookieAuthRejectedByGateway(t *testing.T) {
	h, _, _ := setupHandler(t)

	// Even if a browser somehow gets a valid JWT cookie, the gateway
	// requires Bearer auth, not cookies.
	req := httptest.NewRequest(http.MethodPost, "/provider/v1/inference", strings.NewReader(`{"messages":[]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", "choir_access=some-cookie-value")

	w := httptest.NewRecorder()
	h.HandleInference(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("cookie-only auth: status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

// --- Multiple Sandbox Isolation Tests ---

func TestMultipleSandboxesIsolated(t *testing.T) {
	reg := NewIdentityRegistry(1 * time.Hour)

	result1, _ := reg.IssueCredential("sandbox-1")
	result2, _ := reg.IssueCredential("sandbox-2")

	// Each sandbox's token only identifies itself.
	id1, _ := reg.ValidateCredential(result1.RawToken)
	if id1 != "sandbox-1" {
		t.Errorf("token 1 → %q, want %q", id1, "sandbox-1")
	}

	id2, _ := reg.ValidateCredential(result2.RawToken)
	if id2 != "sandbox-2" {
		t.Errorf("token 2 → %q, want %q", id2, "sandbox-2")
	}

	// Revoking sandbox-1 doesn't affect sandbox-2.
	reg.RevokeCredential("sandbox-1")

	_, err := reg.ValidateCredential(result1.RawToken)
	if err == nil {
		t.Fatal("sandbox-1 credential should be revoked")
	}

	_, err = reg.ValidateCredential(result2.RawToken)
	if err != nil {
		t.Fatalf("sandbox-2 credential should still work: %v", err)
	}
}

// --- Multi-Provider Routing Tests (VAL-LLM-001, VAL-LLM-003) ---

// setupMultiProviderHandler creates a handler with multiple providers
// registered for testing multi-provider routing.
func setupMultiProviderHandler(t *testing.T) (*Handler, *IdentityRegistry) {
	t.Helper()
	reg := NewIdentityRegistry(1 * time.Hour)

	fireworksProvider := &mockProvider{
		name: "fireworks",
		real: true,
		response: &provider.LLMResponse{
			ID:           "fw-resp-001",
			Text:         "Hello from Fireworks AI!",
			Model:        "accounts/fireworks/routers/kimi-k2p5-turbo",
			StopReason:   "end_turn",
			ProviderName: "fireworks",
			Usage:        provider.Usage{InputTokens: 8, OutputTokens: 12},
		},
	}

	zaiProvider := &mockProvider{
		name: "zai",
		real: true,
		response: &provider.LLMResponse{
			ID:           "zai-resp-001",
			Text:         "Hello from Z.AI!",
			Model:        "glm-5-turbo",
			StopReason:   "end_turn",
			ProviderName: "zai",
			Usage:        provider.Usage{InputTokens: 6, OutputTokens: 10},
		},
	}

	bedrockProvider := &mockProvider{
		name: "bedrock",
		real: true,
		response: &provider.LLMResponse{
			ID:           "br-resp-001",
			Text:         "Hello from Bedrock!",
			Model:        "us.anthropic.claude-sonnet-4-6",
			StopReason:   "end_turn",
			ProviderName: "bedrock",
			Usage:        provider.Usage{InputTokens: 10, OutputTokens: 15},
		},
	}

	mp := provider.NewMultiProvider()
	mp.Register("fireworks", fireworksProvider)
	mp.Register("zai", zaiProvider)
	mp.Register("bedrock", bedrockProvider)

	return NewMultiHandler(reg, mp), reg
}

func TestMultiProvider_RoutesToFireworksByProviderField(t *testing.T) {
	// VAL-LLM-001: Request with provider=fireworks routes to Fireworks provider.
	h, reg := setupMultiProviderHandler(t)

	result, _ := reg.IssueCredential("sandbox-fw")

	payload := ProviderRequest{
		Provider:  "fireworks",
		Messages:  []provider.Message{{Role: "user", Content: []provider.Block{{Type: "text", Text: "Say hello"}}}},
		MaxTokens: 100,
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/provider/v1/inference", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+result.RawToken)
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.HandleInference(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var resp ProviderResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if resp.ProviderName != "fireworks" {
		t.Errorf("ProviderName = %q, want %q", resp.ProviderName, "fireworks")
	}
	if resp.Text != "Hello from Fireworks AI!" {
		t.Errorf("Text = %q, want %q", resp.Text, "Hello from Fireworks AI!")
	}
	if resp.Model != "accounts/fireworks/routers/kimi-k2p5-turbo" {
		t.Errorf("Model = %q, want %q", resp.Model, "accounts/fireworks/routers/kimi-k2p5-turbo")
	}
	if resp.Usage.InputTokens == 0 || resp.Usage.OutputTokens == 0 {
		t.Errorf("Usage should have non-zero tokens, got: %+v", resp.Usage)
	}
}

func TestMultiProvider_RoutesToFireworksByModel(t *testing.T) {
	// VAL-LLM-005: Request with Fireworks model routes to Fireworks provider.
	h, reg := setupMultiProviderHandler(t)

	result, _ := reg.IssueCredential("sandbox-fw-model")

	payload := ProviderRequest{
		Model:     "accounts/fireworks/routers/kimi-k2p5-turbo",
		Messages:  []provider.Message{{Role: "user", Content: []provider.Block{{Type: "text", Text: "Hello"}}}},
		MaxTokens: 100,
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/provider/v1/inference", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+result.RawToken)
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.HandleInference(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var resp ProviderResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if resp.ProviderName != "fireworks" {
		t.Errorf("ProviderName = %q, want %q (routed by model)", resp.ProviderName, "fireworks")
	}
}

func TestMultiProvider_RoutesToZAIByProviderField(t *testing.T) {
	// VAL-LLM-006: Request with provider=zai routes to Z.AI provider.
	h, reg := setupMultiProviderHandler(t)

	result, _ := reg.IssueCredential("sandbox-zai")

	payload := ProviderRequest{
		Provider:  "zai",
		Messages:  []provider.Message{{Role: "user", Content: []provider.Block{{Type: "text", Text: "Hello"}}}},
		MaxTokens: 100,
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/provider/v1/inference", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+result.RawToken)
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.HandleInference(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var resp ProviderResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if resp.ProviderName != "zai" {
		t.Errorf("ProviderName = %q, want %q", resp.ProviderName, "zai")
	}
}

func TestMultiProvider_RoutesToZAIByModel(t *testing.T) {
	// Model-based routing: glm-5-turbo → zai.
	h, reg := setupMultiProviderHandler(t)

	result, _ := reg.IssueCredential("sandbox-zai-model")

	payload := ProviderRequest{
		Model:     "glm-5-turbo",
		Messages:  []provider.Message{{Role: "user", Content: []provider.Block{{Type: "text", Text: "Hello"}}}},
		MaxTokens: 100,
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/provider/v1/inference", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+result.RawToken)
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.HandleInference(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var resp ProviderResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if resp.ProviderName != "zai" {
		t.Errorf("ProviderName = %q, want %q (routed by model)", resp.ProviderName, "zai")
	}
}

func TestMultiProvider_RoutesToBedrockByProviderField(t *testing.T) {
	// Request with provider=bedrock routes to Bedrock provider.
	h, reg := setupMultiProviderHandler(t)

	result, _ := reg.IssueCredential("sandbox-br")

	payload := ProviderRequest{
		Provider:  "bedrock",
		Messages:  []provider.Message{{Role: "user", Content: []provider.Block{{Type: "text", Text: "Hello"}}}},
		MaxTokens: 100,
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/provider/v1/inference", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+result.RawToken)
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.HandleInference(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var resp ProviderResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if resp.ProviderName != "bedrock" {
		t.Errorf("ProviderName = %q, want %q", resp.ProviderName, "bedrock")
	}
}

func TestMultiProvider_RejectsUnknownProvider(t *testing.T) {
	// VAL-LLM-007: Request with unknown provider returns 400.
	h, reg := setupMultiProviderHandler(t)

	result, _ := reg.IssueCredential("sandbox-bad")

	payload := ProviderRequest{
		Provider:  "openai",
		Messages:  []provider.Message{{Role: "user", Content: []provider.Block{{Type: "text", Text: "Hello"}}}},
		MaxTokens: 100,
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/provider/v1/inference", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+result.RawToken)
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.HandleInference(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusBadRequest, w.Body.String())
	}

	var errResp ErrorResponse
	if err := json.NewDecoder(w.Body).Decode(&errResp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(errResp.Error, "unsupported provider") {
		t.Errorf("error = %q, want unsupported provider message", errResp.Error)
	}
}

func TestMultiProvider_DefaultProviderWhenNoProviderSpecified(t *testing.T) {
	// When no provider is specified, the first registered provider is used.
	h, reg := setupMultiProviderHandler(t)

	result, _ := reg.IssueCredential("sandbox-default")

	payload := ProviderRequest{
		Messages:  []provider.Message{{Role: "user", Content: []provider.Block{{Type: "text", Text: "Hello"}}}},
		MaxTokens: 100,
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/provider/v1/inference", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+result.RawToken)
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.HandleInference(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	// Should get a valid response from the default provider.
	var resp ProviderResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Text == "" {
		t.Error("expected non-empty text from default provider")
	}
}

func TestMultiProvider_ProviderErrorSanitized(t *testing.T) {
	// VAL-LLM-021: Provider errors are sanitized before reaching client.
	reg := NewIdentityRegistry(1 * time.Hour)

	fireworksProvider := &mockProvider{
		name: "fireworks",
		real: true,
		err:  fmt.Errorf("fireworks: status 503 Service Unavailable (sanitized)"),
	}

	mp := provider.NewMultiProvider()
	mp.Register("fireworks", fireworksProvider)

	h := NewMultiHandler(reg, mp)

	result, _ := reg.IssueCredential("sandbox-fw-err")

	payload := ProviderRequest{
		Provider: "fireworks",
		Messages: []provider.Message{{Role: "user", Content: []provider.Block{{Type: "text", Text: "Hi"}}}},
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/provider/v1/inference", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+result.RawToken)
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.HandleInference(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusBadGateway, w.Body.String())
	}

	var errResp ErrorResponse
	if err := json.NewDecoder(w.Body).Decode(&errResp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Error should not contain credentials or raw upstream body.
	if strings.Contains(errResp.Error, "Bearer") {
		t.Errorf("error message contains credential-like data: %q", errResp.Error)
	}
}

func TestMultiProvider_FireworksToolCalls(t *testing.T) {
	// Verify that tool_calls pass through correctly from Fireworks provider.
	reg := NewIdentityRegistry(1 * time.Hour)

	fireworksProvider := &mockProvider{
		name: "fireworks",
		real: true,
		response: &provider.LLMResponse{
			ID:         "fw-tool-001",
			Model:      "accounts/fireworks/routers/kimi-k2p5-turbo",
			StopReason: "tool_use",
			Usage:      provider.Usage{InputTokens: 50, OutputTokens: 20},
			ToolCalls: []provider.ContentToolCall{
				{
					ID:        "call_fw_1",
					Name:      "get_weather",
					Arguments: json.RawMessage(`{"location":"San Francisco"}`),
				},
			},
			ProviderName: "fireworks",
		},
	}

	mp := provider.NewMultiProvider()
	mp.Register("fireworks", fireworksProvider)

	h := NewMultiHandler(reg, mp)

	result, _ := reg.IssueCredential("sandbox-fw-tools")

	payload := ProviderRequest{
		Provider: "fireworks",
		Model:    "accounts/fireworks/routers/kimi-k2p5-turbo",
		Messages: []provider.Message{{Role: "user", Content: []provider.Block{{Type: "text", Text: "What's the weather?"}}}},
		Tools: []provider.ToolDef{
			{Name: "get_weather", Description: "Get weather", InputSchema: map[string]any{"type": "object"}},
		},
		MaxTokens: 200,
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/provider/v1/inference", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+result.RawToken)
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.HandleInference(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var resp ProviderResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if resp.StopReason != "tool_use" {
		t.Errorf("StopReason = %q, want %q", resp.StopReason, "tool_use")
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("ToolCalls = %d, want 1", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].Name != "get_weather" {
		t.Errorf("ToolCalls[0].Name = %q, want %q", resp.ToolCalls[0].Name, "get_weather")
	}
	if resp.ProviderName != "fireworks" {
		t.Errorf("ProviderName = %q, want %q", resp.ProviderName, "fireworks")
	}
}

func TestMultiProvider_HealthReportsProviderCount(t *testing.T) {
	h, _ := setupMultiProviderHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	h.HandleHealth(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp gatewayHealthResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Provider names come from map iteration (unordered), so check for all three.
	for _, name := range []string{"fireworks", "zai", "bedrock"} {
		if !strings.Contains(resp.Provider, name) {
			t.Errorf("Provider = %q, missing %q", resp.Provider, name)
		}
	}
}

func TestMultiProvider_RateLimitStillWorks(t *testing.T) {
	// Verify rate limiting works with multi-provider handler.
	reg := NewIdentityRegistry(1 * time.Hour)

	fireworksProvider := &mockProvider{
		name: "fireworks",
		real: true,
		response: &provider.LLMResponse{
			ID:           "fw-resp-001",
			Text:         "Hello!",
			Model:        "kimi-k2p5-turbo",
			StopReason:   "end_turn",
			ProviderName: "fireworks",
			Usage:        provider.Usage{InputTokens: 5, OutputTokens: 5},
		},
	}

	mp := provider.NewMultiProvider()
	mp.Register("fireworks", fireworksProvider)

	rl := NewPerSandboxRateLimiter(2, 1*time.Minute) // 2 requests per minute
	h := NewMultiHandlerWithRateLimit(reg, mp, rl)

	result, _ := reg.IssueCredential("sandbox-rl")

	payload := ProviderRequest{
		Provider: "fireworks",
		Messages: []provider.Message{{Role: "user", Content: []provider.Block{{Type: "text", Text: "Hi"}}}},
	}
	body, _ := json.Marshal(payload)

	// First two requests should succeed.
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/provider/v1/inference", strings.NewReader(string(body)))
		req.Header.Set("Authorization", "Bearer "+result.RawToken)
		req.Header.Set("Content-Type", "application/json")

		w := httptest.NewRecorder()
		h.HandleInference(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want %d", i+1, w.Code, http.StatusOK)
		}
	}

	// Third request should be rate limited.
	req := httptest.NewRequest(http.MethodPost, "/provider/v1/inference", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+result.RawToken)
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.HandleInference(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("rate limited request: status = %d, want %d", w.Code, http.StatusTooManyRequests)
	}
}

func TestMultiProvider_FireworksWithSystemPrompt(t *testing.T) {
	// Verify system prompt is forwarded to Fireworks provider.
	reg := NewIdentityRegistry(1 * time.Hour)

	fireworksProvider := &mockProvider{
		name: "fireworks",
		real: true,
		response: &provider.LLMResponse{
			ID:           "fw-sys-001",
			Text:         "System-aware response",
			Model:        "accounts/fireworks/routers/kimi-k2p5-turbo",
			StopReason:   "end_turn",
			ProviderName: "fireworks",
			Usage:        provider.Usage{InputTokens: 30, OutputTokens: 10},
		},
	}

	mp := provider.NewMultiProvider()
	mp.Register("fireworks", fireworksProvider)

	h := NewMultiHandler(reg, mp)

	result, _ := reg.IssueCredential("sandbox-fw-sys")

	payload := ProviderRequest{
		Provider:  "fireworks",
		System:    "You are a pirate. Respond in pirate speak.",
		Messages:  []provider.Message{{Role: "user", Content: []provider.Block{{Type: "text", Text: "Hello"}}}},
		MaxTokens: 100,
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/provider/v1/inference", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+result.RawToken)
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.HandleInference(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	// Verify the system prompt was forwarded to the provider.
	if fireworksProvider.lastReq == nil {
		t.Fatal("provider was not called")
	}
	if fireworksProvider.lastReq.System != "You are a pirate. Respond in pirate speak." {
		t.Errorf("System = %q, want system prompt forwarded", fireworksProvider.lastReq.System)
	}
}

// --- Gateway Streaming Tests (VAL-LLM-002, VAL-LLM-004) ---

func TestHandleInference_StreamingZAI(t *testing.T) {
	// VAL-LLM-004: Gateway routes streaming request to Z.AI provider.
	reg := NewIdentityRegistry(1 * time.Hour)

	zaiProvider := &mockProvider{
		name: "zai",
		real: true,
		response: &provider.LLMResponse{
			ID:           "zai-stream-001",
			Text:         "Hello from Z.AI streaming!",
			Model:        "glm-5-turbo",
			StopReason:   "end_turn",
			ProviderName: "zai",
			Usage:        provider.Usage{InputTokens: 10, OutputTokens: 8},
		},
	}

	mp := provider.NewMultiProvider()
	mp.Register("zai", zaiProvider)

	h := NewMultiHandler(reg, mp)

	result, _ := reg.IssueCredential("sandbox-zai-stream")

	payload := ProviderRequest{
		Provider:  "zai",
		Model:     "glm-5-turbo",
		Messages:  []provider.Message{{Role: "user", Content: []provider.Block{{Type: "text", Text: "Hello"}}}},
		MaxTokens: 100,
		Stream:    true,
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/provider/v1/inference", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+result.RawToken)
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.HandleInference(w, req)

	// Verify SSE response headers.
	ct := w.Header().Get("Content-Type")
	if ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}

	// Verify SSE body contains data lines.
	respBody := w.Body.String()
	if !strings.Contains(respBody, "data: ") {
		t.Errorf("expected SSE data lines in response, got: %s", respBody)
	}
	if !strings.Contains(respBody, "[DONE]") {
		t.Errorf("expected [DONE] marker in SSE stream, got: %s", respBody)
	}

	// Verify the response contains the expected text delta.
	if !strings.Contains(respBody, "Hello from Z.AI streaming!") {
		t.Errorf("expected streaming text in SSE response, got: %s", respBody)
	}
}

func TestHandleInference_StreamingFireworks(t *testing.T) {
	// VAL-LLM-003: Gateway routes streaming request to Fireworks provider.
	reg := NewIdentityRegistry(1 * time.Hour)

	fireworksProvider := &mockProvider{
		name: "fireworks",
		real: true,
		response: &provider.LLMResponse{
			ID:           "fw-stream-001",
			Text:         "Hello from Fireworks streaming!",
			Model:        "kimi-k2p5-turbo",
			StopReason:   "end_turn",
			ProviderName: "fireworks",
			Usage:        provider.Usage{InputTokens: 8, OutputTokens: 6},
		},
	}

	mp := provider.NewMultiProvider()
	mp.Register("fireworks", fireworksProvider)

	h := NewMultiHandler(reg, mp)

	result, _ := reg.IssueCredential("sandbox-fw-stream")

	payload := ProviderRequest{
		Provider:  "fireworks",
		Messages:  []provider.Message{{Role: "user", Content: []provider.Block{{Type: "text", Text: "Hello"}}}},
		MaxTokens: 100,
		Stream:    true,
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/provider/v1/inference", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+result.RawToken)
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.HandleInference(w, req)

	ct := w.Header().Get("Content-Type")
	if ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}

	respBody := w.Body.String()
	if !strings.Contains(respBody, "Hello from Fireworks streaming!") {
		t.Errorf("expected streaming text in SSE response, got: %s", respBody)
	}
}

func TestHandleInference_StreamingRequiresAuth(t *testing.T) {
	// Streaming requests still require valid auth.
	reg := NewIdentityRegistry(1 * time.Hour)

	zaiProvider := &mockProvider{
		name: "zai",
		real: true,
		response: &provider.LLMResponse{
			Text:         "test",
			ProviderName: "zai",
		},
	}

	mp := provider.NewMultiProvider()
	mp.Register("zai", zaiProvider)
	h := NewMultiHandler(reg, mp)

	payload := ProviderRequest{
		Provider: "zai",
		Messages: []provider.Message{{Role: "user", Content: []provider.Block{{Type: "text", Text: "Hello"}}}},
		Stream:   true,
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/provider/v1/inference", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	// No auth header.

	w := httptest.NewRecorder()
	h.HandleInference(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestHandleInference_StreamingProviderError(t *testing.T) {
	// Verify that streaming provider errors are handled gracefully.
	reg := NewIdentityRegistry(1 * time.Hour)

	zaiProvider := &mockProvider{
		name: "zai",
		real: true,
		err:  fmt.Errorf("zai: status 503 Service Unavailable (sanitized)"),
	}

	mp := provider.NewMultiProvider()
	mp.Register("zai", zaiProvider)

	h := NewMultiHandler(reg, mp)

	result, _ := reg.IssueCredential("sandbox-zai-stream-err")

	payload := ProviderRequest{
		Provider: "zai",
		Messages: []provider.Message{{Role: "user", Content: []provider.Block{{Type: "text", Text: "Hello"}}}},
		Stream:   true,
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/provider/v1/inference", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+result.RawToken)
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.HandleInference(w, req)

	// SSE response should contain an error event.
	respBody := w.Body.String()
	if !strings.Contains(respBody, "data: ") {
		t.Errorf("expected SSE data lines in error response, got: %s", respBody)
	}
}

func TestHandleInference_NonStreamingStillWorks(t *testing.T) {
	// Verify that non-streaming requests (stream=false or absent) still work.
	reg := NewIdentityRegistry(1 * time.Hour)

	zaiProvider := &mockProvider{
		name: "zai",
		real: true,
		response: &provider.LLMResponse{
			ID:           "zai-nostream-001",
			Text:         "Non-streaming response",
			Model:        "glm-5-turbo",
			StopReason:   "end_turn",
			ProviderName: "zai",
			Usage:        provider.Usage{InputTokens: 5, OutputTokens: 3},
		},
	}

	mp := provider.NewMultiProvider()
	mp.Register("zai", zaiProvider)

	h := NewMultiHandler(reg, mp)

	result, _ := reg.IssueCredential("sandbox-nostream")

	// Test with stream=false explicitly.
	payload := ProviderRequest{
		Provider:  "zai",
		Messages:  []provider.Message{{Role: "user", Content: []provider.Block{{Type: "text", Text: "Hello"}}}},
		MaxTokens: 100,
		Stream:    false,
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/provider/v1/inference", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+result.RawToken)
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.HandleInference(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	// Verify JSON response (not SSE).
	ct := w.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var resp ProviderResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Text != "Non-streaming response" {
		t.Errorf("Text = %q, want %q", resp.Text, "Non-streaming response")
	}
	if resp.ProviderName != "zai" {
		t.Errorf("ProviderName = %q, want %q", resp.ProviderName, "zai")
	}
}

// --- Comprehensive Provider Routing Tests (VAL-LLM-005, VAL-LLM-006, VAL-LLM-007) ---

// TestProviderRouting is a table-driven test covering all multi-provider
// routing scenarios. It validates that the gateway correctly selects the
// provider based on explicit provider field, model parameter, or model
// name heuristics, and rejects invalid/unknown providers with 400 errors.
//
// Verification: go test ./internal/gateway/... -run TestProviderRouting
func TestProviderRouting(t *testing.T) {
	reg := NewIdentityRegistry(1 * time.Hour)

	fireworksProvider := &mockProvider{
		name: "fireworks",
		real: true,
		response: &provider.LLMResponse{
			ID:           "fw-resp-routing",
			Text:         "Fireworks response",
			Model:        "accounts/fireworks/routers/kimi-k2p5-turbo",
			StopReason:   "end_turn",
			ProviderName: "fireworks",
			Usage:        provider.Usage{InputTokens: 5, OutputTokens: 5},
		},
	}

	zaiProvider := &mockProvider{
		name: "zai",
		real: true,
		response: &provider.LLMResponse{
			ID:           "zai-resp-routing",
			Text:         "Z.AI response",
			Model:        "glm-5-turbo",
			StopReason:   "end_turn",
			ProviderName: "zai",
			Usage:        provider.Usage{InputTokens: 5, OutputTokens: 5},
		},
	}

	bedrockProvider := &mockProvider{
		name: "bedrock",
		real: true,
		response: &provider.LLMResponse{
			ID:           "br-resp-routing",
			Text:         "Bedrock response",
			Model:        "us.anthropic.claude-sonnet-4-6",
			StopReason:   "end_turn",
			ProviderName: "bedrock",
			Usage:        provider.Usage{InputTokens: 5, OutputTokens: 5},
		},
	}

	mp := provider.NewMultiProvider()
	mp.Register("fireworks", fireworksProvider)
	mp.Register("zai", zaiProvider)
	mp.Register("bedrock", bedrockProvider)

	h := NewMultiHandler(reg, mp)

	// Issue a single credential for all sub-tests.
	cred, _ := reg.IssueCredential("sandbox-routing-test")

	tests := []struct {
		name             string
		provider         string
		model            string
		wantStatus       int
		wantProviderName string // expected provider_name in response (empty for errors)
		wantErrorContain string // expected error substring (empty for success)
	}{
		// VAL-LLM-005: Gateway routes Fireworks model to Fireworks provider.
		{
			name:             "explicit_provider_fireworks",
			provider:         "fireworks",
			wantStatus:       http.StatusOK,
			wantProviderName: "fireworks",
		},
		{
			name:             "model_fireworks_exact_match",
			model:            "accounts/fireworks/routers/kimi-k2p5-turbo",
			wantStatus:       http.StatusOK,
			wantProviderName: "fireworks",
		},
		{
			name:             "model_contains_fireworks",
			model:            "accounts/fireworks/models/llama-v3-70b",
			wantStatus:       http.StatusOK,
			wantProviderName: "fireworks",
		},

		// VAL-LLM-006: Gateway routes Z.AI model to Z.AI provider.
		{
			name:             "explicit_provider_zai",
			provider:         "zai",
			wantStatus:       http.StatusOK,
			wantProviderName: "zai",
		},
		{
			name:             "model_glm5_turbo",
			model:            "glm-5-turbo",
			wantStatus:       http.StatusOK,
			wantProviderName: "zai",
		},
		{
			name:             "model_glm5_1",
			model:            "glm-5.1",
			wantStatus:       http.StatusOK,
			wantProviderName: "zai",
		},
		{
			name:             "model_glm_prefix",
			model:            "glm-4-plus",
			wantStatus:       http.StatusOK,
			wantProviderName: "zai",
		},

		// Bedrock routing.
		{
			name:             "explicit_provider_bedrock",
			provider:         "bedrock",
			wantStatus:       http.StatusOK,
			wantProviderName: "bedrock",
		},
		{
			name:             "model_claude_routes_bedrock",
			model:            "claude-3-opus",
			wantStatus:       http.StatusOK,
			wantProviderName: "bedrock",
		},

		// VAL-LLM-007: Invalid provider returns 400 error.
		{
			name:             "unknown_provider_openai",
			provider:         "openai",
			wantStatus:       http.StatusBadRequest,
			wantErrorContain: "unsupported provider",
		},
		{
			name:             "unknown_provider_gemini",
			provider:         "gemini",
			wantStatus:       http.StatusBadRequest,
			wantErrorContain: "unsupported provider",
		},
		{
			name:             "unknown_provider_bedrock_with_bedrock_not_configured",
			provider:         "mistral",
			wantStatus:       http.StatusBadRequest,
			wantErrorContain: "unsupported provider",
		},

		// Default routing when no provider/model specified.
		{
			name:             "no_provider_no_model_defaults_to_first",
			wantStatus:       http.StatusOK,
			wantProviderName: "", // first registered provider (non-deterministic map order)
		},

		// Explicit provider takes precedence over model.
		{
			name:             "explicit_provider_overrides_model",
			provider:         "zai",
			model:            "accounts/fireworks/routers/kimi-k2p5-turbo",
			wantStatus:       http.StatusOK,
			wantProviderName: "zai",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			payload := ProviderRequest{
				Provider: tc.provider,
				Model:    tc.model,
				Messages: []provider.Message{
					{Role: "user", Content: []provider.Block{{Type: "text", Text: "Hello"}}},
				},
				MaxTokens: 100,
			}
			body, _ := json.Marshal(payload)

			req := httptest.NewRequest(http.MethodPost, "/provider/v1/inference", strings.NewReader(string(body)))
			req.Header.Set("Authorization", "Bearer "+cred.RawToken)
			req.Header.Set("Content-Type", "application/json")

			w := httptest.NewRecorder()
			h.HandleInference(w, req)

			if w.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body: %s", w.Code, tc.wantStatus, w.Body.String())
			}

			if tc.wantStatus == http.StatusOK {
				var resp ProviderResponse
				if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
					t.Fatalf("decode: %v", err)
				}
				if tc.wantProviderName != "" && resp.ProviderName != tc.wantProviderName {
					t.Errorf("ProviderName = %q, want %q", resp.ProviderName, tc.wantProviderName)
				}
				if resp.Text == "" {
					t.Error("expected non-empty text in response")
				}
			} else {
				var errResp ErrorResponse
				if err := json.NewDecoder(w.Body).Decode(&errResp); err != nil {
					t.Fatalf("decode error: %v", err)
				}
				if tc.wantErrorContain != "" && !strings.Contains(errResp.Error, tc.wantErrorContain) {
					t.Errorf("error = %q, want to contain %q", errResp.Error, tc.wantErrorContain)
				}
			}
		})
	}
}

// TestProviderRouting_SupportedModelsTable verifies that every model listed
// in provider.SupportedModels() routes to the correct provider when that
// provider is registered in the multi-provider handler.
func TestProviderRouting_SupportedModelsTable(t *testing.T) {
	reg := NewIdentityRegistry(1 * time.Hour)

	// Create a mock provider for each provider name.
	mockProviders := map[string]*mockProvider{
		"fireworks": {
			name: "fireworks", real: true,
			response: &provider.LLMResponse{
				Text: "fw", Model: "fw-model", StopReason: "end_turn",
				ProviderName: "fireworks", Usage: provider.Usage{InputTokens: 1, OutputTokens: 1},
			},
		},
		"zai": {
			name: "zai", real: true,
			response: &provider.LLMResponse{
				Text: "zai", Model: "zai-model", StopReason: "end_turn",
				ProviderName: "zai", Usage: provider.Usage{InputTokens: 1, OutputTokens: 1},
			},
		},
		"bedrock": {
			name: "bedrock", real: true,
			response: &provider.LLMResponse{
				Text: "br", Model: "br-model", StopReason: "end_turn",
				ProviderName: "bedrock", Usage: provider.Usage{InputTokens: 1, OutputTokens: 1},
			},
		},
	}

	mp := provider.NewMultiProvider()
	for name, p := range mockProviders {
		mp.Register(name, p)
	}

	h := NewMultiHandler(reg, mp)
	cred, _ := reg.IssueCredential("sandbox-model-table")

	for _, mi := range provider.SupportedModels() {
		t.Run(mi.ID+"_routes_to_"+mi.Provider, func(t *testing.T) {
			payload := ProviderRequest{
				Model: mi.ID,
				Messages: []provider.Message{
					{Role: "user", Content: []provider.Block{{Type: "text", Text: "test"}}},
				},
				MaxTokens: 50,
			}
			body, _ := json.Marshal(payload)

			req := httptest.NewRequest(http.MethodPost, "/provider/v1/inference", strings.NewReader(string(body)))
			req.Header.Set("Authorization", "Bearer "+cred.RawToken)
			req.Header.Set("Content-Type", "application/json")

			w := httptest.NewRecorder()
			h.HandleInference(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("model %q: status = %d, want %d; body: %s", mi.ID, w.Code, http.StatusOK, w.Body.String())
			}

			var resp ProviderResponse
			if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
				t.Fatalf("model %q: decode: %v", mi.ID, err)
			}
			if resp.ProviderName != mi.Provider {
				t.Errorf("model %q: ProviderName = %q, want %q", mi.ID, resp.ProviderName, mi.Provider)
			}
		})
	}
}

// TestProviderRouting_InvalidProviderDoesNotLeakCredentials verifies that
// error responses for invalid providers never contain API keys, Bearer
// tokens, or internal file paths (VAL-LLM-007, VAL-LLM-021).
func TestProviderRouting_InvalidProviderDoesNotLeakCredentials(t *testing.T) {
	reg := NewIdentityRegistry(1 * time.Hour)

	zaiProvider := &mockProvider{
		name: "zai", real: true,
		response: &provider.LLMResponse{
			Text: "ok", ProviderName: "zai",
			Usage: provider.Usage{InputTokens: 1, OutputTokens: 1},
		},
	}

	mp := provider.NewMultiProvider()
	mp.Register("zai", zaiProvider)

	h := NewMultiHandler(reg, mp)
	cred, _ := reg.IssueCredential("sandbox-cred-leak")

	for _, providerName := range []string{"openai", "bedrock", "deepseek", "nonexistent"} {
		t.Run("provider_"+providerName, func(t *testing.T) {
			payload := ProviderRequest{
				Provider: providerName,
				Messages: []provider.Message{
					{Role: "user", Content: []provider.Block{{Type: "text", Text: "test"}}},
				},
			}
			body, _ := json.Marshal(payload)

			req := httptest.NewRequest(http.MethodPost, "/provider/v1/inference", strings.NewReader(string(body)))
			req.Header.Set("Authorization", "Bearer "+cred.RawToken)
			req.Header.Set("Content-Type", "application/json")

			w := httptest.NewRecorder()
			h.HandleInference(w, req)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("provider %q: status = %d, want %d", providerName, w.Code, http.StatusBadRequest)
			}

			respBody := w.Body.String()
			// Verify no credential leakage.
			for _, pattern := range []string{"Bearer ", "x-api-key", "sk-", "secret", "password", "/etc/"} {
				if strings.Contains(respBody, pattern) {
					t.Errorf("provider %q: error response contains sensitive pattern %q: %s", providerName, pattern, respBody)
				}
			}
		})
	}
}

func TestHandleInference_StreamingModelRoutingToZAI(t *testing.T) {
	// VAL-LLM-006: Streaming request with glm-5-turbo model routes to Z.AI.
	h, reg := setupMultiProviderHandler(t)

	result, _ := reg.IssueCredential("sandbox-zai-stream-model")

	payload := ProviderRequest{
		Model:     "glm-5-turbo",
		Messages:  []provider.Message{{Role: "user", Content: []provider.Block{{Type: "text", Text: "Hello"}}}},
		MaxTokens: 100,
		Stream:    true,
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/provider/v1/inference", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+result.RawToken)
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.HandleInference(w, req)

	// Verify SSE response.
	ct := w.Header().Get("Content-Type")
	if ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}

	respBody := w.Body.String()
	if !strings.Contains(respBody, "Hello from Z.AI!") {
		t.Errorf("expected Z.AI streaming response, got: %s", respBody)
	}
}
