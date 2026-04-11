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

func (m *mockProvider) Name() string  { return m.name }
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
	req.Host = "localhost:8084"

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
	req.Host = "10.0.0.1:8084"

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
	req.Host = "localhost:8084"

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
	req.Host = "localhost:8084"

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
