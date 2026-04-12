package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yusefmosiah/go-choir/internal/runtime"
	"github.com/yusefmosiah/go-choir/internal/types"
)

// --- Bedrock Provider Tests ---

func TestBedrockProviderRequiresRegion(t *testing.T) {
	_, err := NewBedrockProvider(BedrockConfig{
		Region:    "",
		ModelID:   "test-model",
		AuthToken: "test-token",
	})
	if err == nil || !strings.Contains(err.Error(), "region") {
		t.Fatalf("expected region error, got: %v", err)
	}
}

func TestBedrockProviderRequiresModelID(t *testing.T) {
	_, err := NewBedrockProvider(BedrockConfig{
		Region:    "us-east-1",
		ModelID:   "",
		AuthToken: "test-token",
	})
	if err == nil || !strings.Contains(err.Error(), "model_id") {
		t.Fatalf("expected model_id error, got: %v", err)
	}
}

func TestBedrockProviderRequiresAuthToken(t *testing.T) {
	_, err := NewBedrockProvider(BedrockConfig{
		Region:    "us-east-1",
		ModelID:   "test-model",
		AuthToken: "",
	})
	if err == nil || !strings.Contains(err.Error(), "auth token") {
		t.Fatalf("expected auth token error, got: %v", err)
	}
}

func TestBedrockProviderCallSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify the request targets the Bedrock invoke endpoint.
		if !strings.Contains(r.URL.Path, "/model/") || !strings.Contains(r.URL.Path, "/invoke") {
			t.Errorf("unexpected URL path: %s", r.URL.Path)
		}

		// Verify Authorization header uses Bearer token.
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			t.Errorf("expected Bearer auth, got: %s", auth)
		}

		// Verify anthropic-version header.
		if v := r.Header.Get("anthropic-version"); v != "bedrock-2023-05-31" {
			t.Errorf("expected anthropic-version bedrock-2023-05-31, got: %s", v)
		}

		// Return a successful Anthropic Messages API response.
		resp := anthropicResponse{
			ID: "msg_test123",
			Content: []anthropicResponseBlock{
				{Type: "text", Text: "Hello from Bedrock!"},
			},
			StopReason: "end_turn",
			Usage:      anthropicUsage{InputTokens: 10, OutputTokens: 5},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := &BedrockProvider{
		region:     "us-east-1",
		modelID:   "us.anthropic.claude-sonnet-4-6",
		authToken:  "test-bearer-token",
		httpClient: server.Client(),
		anthropicV: "bedrock-2023-05-31",
	}
	// Override the httpClient base URL by constructing a custom transport.
	p.httpClient = &http.Client{
		Timeout:   120 * time.Second,
		Transport: &rewriteTransport{target: server.URL, original: "https://bedrock-runtime.us-east-1.amazonaws.com"},
	}

	resp, err := p.Call(context.Background(), LLMRequest{
		System: "You are helpful.",
		Messages: []Message{
			{Role: "user", Content: []Block{{Type: "text", Text: "Hello"}}},
		},
		MaxTokens: 1024,
	})
	if err != nil {
		t.Fatalf("bedrock call: %v", err)
	}

	if resp.Text != "Hello from Bedrock!" {
		t.Errorf("expected response text 'Hello from Bedrock!', got: %s", resp.Text)
	}
	if resp.ProviderName != "bedrock" {
		t.Errorf("expected provider name 'bedrock', got: %s", resp.ProviderName)
	}
	if resp.Usage.InputTokens != 10 || resp.Usage.OutputTokens != 5 {
		t.Errorf("unexpected usage: %+v", resp.Usage)
	}
	if resp.StopReason != "end_turn" {
		t.Errorf("expected stop_reason 'end_turn', got: %s", resp.StopReason)
	}
}

func TestBedrockProviderCallError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"message":"Service unavailable"}`))
	}))
	defer server.Close()

	p := &BedrockProvider{
		region:     "us-east-1",
		modelID:   "test-model",
		authToken:  "test-token",
		httpClient: &http.Client{
			Timeout:   120 * time.Second,
			Transport: &rewriteTransport{target: server.URL, original: "https://bedrock-runtime.us-east-1.amazonaws.com"},
		},
		anthropicV: "bedrock-2023-05-31",
	}

	_, err := p.Call(context.Background(), LLMRequest{
		Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "test"}}}},
		MaxTokens: 1024,
	})
	if err == nil {
		t.Fatal("expected error for 503 response")
	}
	// Error should be sanitized (no raw response body leaked).
	if strings.Contains(err.Error(), "Service unavailable") {
		t.Errorf("error message should be sanitized, got: %v", err)
	}
	if !strings.Contains(err.Error(), "sanitized") {
		t.Errorf("error should mention sanitized, got: %v", err)
	}
}

func TestBedrockProviderNameAndReal(t *testing.T) {
	p := &BedrockProvider{region: "us-east-1", modelID: "test", authToken: "tok"}
	if p.Name() != "bedrock" {
		t.Errorf("expected name 'bedrock', got: %s", p.Name())
	}
	if !p.IsReal() {
		t.Error("bedrock provider should report IsReal() = true")
	}
}

// --- Z.AI Provider Tests ---

func TestZAIProviderRequiresAPIKey(t *testing.T) {
	_, err := NewZAIProvider(ZAIConfig{
		APIKey:  "",
		ModelID: "glm-4.7",
	})
	if err == nil || !strings.Contains(err.Error(), "api key") {
		t.Fatalf("expected api key error, got: %v", err)
	}
}

func TestZAIProviderRequiresModelID(t *testing.T) {
	_, err := NewZAIProvider(ZAIConfig{
		APIKey:  "test-key",
		ModelID: "",
	})
	if err == nil || !strings.Contains(err.Error(), "model_id") {
		t.Fatalf("expected model_id error, got: %v", err)
	}
}

func TestZAIProviderCallSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify the request targets the /v1/messages endpoint.
		if r.URL.Path != "/v1/messages" {
			t.Errorf("unexpected URL path: %s", r.URL.Path)
		}

		// Verify x-api-key header.
		if v := r.Header.Get("x-api-key"); v != "test-zai-key" {
			t.Errorf("expected x-api-key 'test-zai-key', got: %s", v)
		}

		// Verify anthropic-version header.
		if v := r.Header.Get("anthropic-version"); v != "2023-06-01" {
			t.Errorf("expected anthropic-version 2023-06-01, got: %s", v)
		}

		// Return a successful Anthropic Messages API response.
		resp := anthropicResponse{
			ID:    "msg_zai_test",
			Model: "glm-4.7",
			Content: []anthropicResponseBlock{
				{Type: "text", Text: "Hello from Z.AI!"},
			},
			StopReason: "end_turn",
			Usage:      anthropicUsage{InputTokens: 8, OutputTokens: 4},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := &ZAIProvider{
		apiKey:     "test-zai-key",
		modelID:   "glm-4.7",
		httpClient: server.Client(),
		baseURL:    server.URL,
	}

	resp, err := p.Call(context.Background(), LLMRequest{
		System: "You are helpful.",
		Messages: []Message{
			{Role: "user", Content: []Block{{Type: "text", Text: "Hi"}}},
		},
		MaxTokens: 1024,
	})
	if err != nil {
		t.Fatalf("zai call: %v", err)
	}

	if resp.Text != "Hello from Z.AI!" {
		t.Errorf("expected 'Hello from Z.AI!', got: %s", resp.Text)
	}
	if resp.ProviderName != "zai" {
		t.Errorf("expected provider name 'zai', got: %s", resp.ProviderName)
	}
	if resp.Model != "glm-4.7" {
		t.Errorf("expected model 'glm-4.7', got: %s", resp.Model)
	}
}

func TestZAIProviderCallError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid api key"}`))
	}))
	defer server.Close()

	p := &ZAIProvider{
		apiKey:     "bad-key",
		modelID:   "glm-4.7",
		httpClient: server.Client(),
		baseURL:    server.URL,
	}

	_, err := p.Call(context.Background(), LLMRequest{
		Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "test"}}}},
		MaxTokens: 1024,
	})
	if err == nil {
		t.Fatal("expected error for 401 response")
	}
	// Error should be sanitized.
	if strings.Contains(err.Error(), "invalid api key") {
		t.Errorf("error message should be sanitized, got: %v", err)
	}
}

func TestZAIProviderNameAndReal(t *testing.T) {
	p := &ZAIProvider{apiKey: "key", modelID: "model", baseURL: "http://test"}
	if p.Name() != "zai" {
		t.Errorf("expected name 'zai', got: %s", p.Name())
	}
	if !p.IsReal() {
		t.Error("zai provider should report IsReal() = true")
	}
}

func TestZAIProviderDefaultBaseURL(t *testing.T) {
	p, err := NewZAIProvider(ZAIConfig{
		APIKey:  "test-key",
		ModelID: "glm-4.7",
	})
	if err != nil {
		t.Fatalf("create zai provider: %v", err)
	}
	if p.baseURL != "https://api.z.ai/api/anthropic" {
		t.Errorf("expected default base URL, got: %s", p.baseURL)
	}
}

// --- Resolve Provider Tests ---

func TestResolveProviderPrefersBedrock(t *testing.T) {
	t.Setenv("AWS_BEARER_TOKEN_BEDROCK", "test-bedrock-token")
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("ZAI_API_KEY", "test-zai-key")

	p, err := ResolveProvider(ProviderConfig{
		BedrockModels: []string{"us.anthropic.claude-sonnet-4-6"},
		ZAIModels:     []string{"glm-5.1"},
	})
	if err != nil {
		t.Fatalf("resolve provider: %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil provider")
	}
	if p.Name() != "bedrock" {
		t.Errorf("expected bedrock, got: %s", p.Name())
	}
}

func TestResolveProviderFallsBackToZAI(t *testing.T) {
	t.Setenv("ZAI_API_KEY", "test-zai-key")

	p, err := ResolveProvider(ProviderConfig{
		ZAIModels: []string{"glm-5.1"},
	})
	if err != nil {
		t.Fatalf("resolve provider: %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil provider")
	}
	if p.Name() != "zai" {
		t.Errorf("expected zai, got: %s", p.Name())
	}
}

func TestResolveProviderReturnsNilWhenNoCredentials(t *testing.T) {
	t.Setenv("AWS_BEARER_TOKEN_BEDROCK", "")
	t.Setenv("ZAI_API_KEY", "")
	t.Setenv("FIREWORKS_API_KEY", "")

	p, err := ResolveProvider(ProviderConfig{
		BedrockModels:   []string{"us.anthropic.claude-sonnet-4-6"},
		ZAIModels:       []string{"glm-5.1"},
		FireworksModels: []string{"accounts/fireworks/routers/kimi-k2p5-turbo"},
	})
	if err != nil {
		t.Fatalf("resolve provider: %v", err)
	}
	if p != nil {
		t.Errorf("expected nil provider when no credentials, got: %s", p.Name())
	}
}

func TestResolveProviderFallsBackToFireworks(t *testing.T) {
	t.Setenv("FIREWORKS_API_KEY", "fw_test-key")

	p, err := ResolveProvider(ProviderConfig{
		FireworksModels: []string{"accounts/fireworks/models/test-model"},
	})
	if err != nil {
		t.Fatalf("resolve provider: %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil provider")
	}
	if p.Name() != "fireworks" {
		t.Errorf("expected fireworks, got: %s", p.Name())
	}
}

func TestFireworksProviderFromEnvMissingKey(t *testing.T) {
	t.Setenv("FIREWORKS_API_KEY", "")

	_, err := NewFireworksProviderFromEnv("accounts/fireworks/routers/kimi-k2p5-turbo")
	if err == nil || !strings.Contains(err.Error(), "api key") {
		t.Errorf("expected api key error, got: %v", err)
	}
}

func TestFireworksProviderFromEnvPassesModel(t *testing.T) {
	t.Setenv("FIREWORKS_API_KEY", "fw_test-key")

	p, err := NewFireworksProviderFromEnv("accounts/fireworks/routers/kimi-k2p5-turbo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.modelID != "accounts/fireworks/routers/kimi-k2p5-turbo" {
		t.Errorf("expected passed model, got: %s", p.modelID)
	}
}

func TestFireworksProviderFromEnvCustomBaseURL(t *testing.T) {
	t.Setenv("FIREWORKS_API_KEY", "fw_test-key")
	t.Setenv("FIREWORKS_BASE_URL", "https://custom.example.com/api")

	p, err := NewFireworksProviderFromEnv("test-model")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.baseURL != "https://custom.example.com/api" {
		t.Errorf("expected custom base URL, got: %s", p.baseURL)
	}
}

// --- Bridge Provider Tests ---

type mockLLMProvider struct {
	name    string
	isReal  bool
	resp    *LLMResponse
	err     error
	called  bool
	lastReq *LLMRequest
}

func (m *mockLLMProvider) Call(ctx context.Context, req LLMRequest) (*LLMResponse, error) {
	m.called = true
	m.lastReq = &req
	if m.err != nil {
		return nil, m.err
	}
	return m.resp, nil
}

func (m *mockLLMProvider) Name() string  { return m.name }
func (m *mockLLMProvider) IsReal() bool { return m.isReal }

func TestBridgeProviderExecuteSuccess(t *testing.T) {
	mock := &mockLLMProvider{
		name:   "test-provider",
		isReal: true,
		resp: &LLMResponse{
			ID:           "msg_123",
			Text:         "Real provider response!",
			Model:        "test-model",
			StopReason:   "end_turn",
			Usage:        Usage{InputTokens: 10, OutputTokens: 20},
			ProviderName: "test-provider",
		},
	}

	bridge := NewBridgeProvider(mock)

	task := &types.TaskRecord{
		TaskID:  "task-1",
		OwnerID: "user-1",
		Prompt:  "What is the meaning of life?",
	}

	var events []struct {
		kind    types.EventKind
		phase   string
		payload json.RawMessage
	}
	emit := func(kind types.EventKind, phase string, payload json.RawMessage) {
		events = append(events, struct {
			kind    types.EventKind
			phase   string
			payload json.RawMessage
		}{kind, phase, payload})
	}

	err := bridge.Execute(context.Background(), task, emit)
	if err != nil {
		t.Fatalf("bridge execute: %v", err)
	}

	if !mock.called {
		t.Error("expected inner provider to be called")
	}

	// Verify the result was set on the task.
	if task.Result != "Real provider response!" {
		t.Errorf("expected result 'Real provider response!', got: %s", task.Result)
	}

	// Verify events were emitted.
	if len(events) < 3 {
		t.Fatalf("expected at least 3 events, got %d", len(events))
	}

	// First event should be a progress with "started" status and "real" flag.
	var firstPayload map[string]string
	if err := json.Unmarshal(events[0].payload, &firstPayload); err != nil {
		t.Fatalf("unmarshal first event: %v", err)
	}
	if firstPayload["status"] != "started" {
		t.Errorf("expected first event status 'started', got: %s", firstPayload["status"])
	}
	if firstPayload["real"] != "true" {
		t.Errorf("expected first event real=true, got: %s", firstPayload["real"])
	}
	if firstPayload["provider"] != "test-provider" {
		t.Errorf("expected first event provider 'test-provider', got: %s", firstPayload["provider"])
	}

	// Delta event should contain the response text.
	deltaIdx := len(events) - 1
	var deltaPayload map[string]string
	if err := json.Unmarshal(events[deltaIdx].payload, &deltaPayload); err != nil {
		t.Fatalf("unmarshal delta event: %v", err)
	}
	if deltaPayload["real"] != "true" {
		t.Errorf("expected delta event real=true, got: %s", deltaPayload["real"])
	}
	if deltaPayload["text"] != "Real provider response!" {
		t.Errorf("expected delta text 'Real provider response!', got: %s", deltaPayload["text"])
	}
}

func TestBridgeProviderExecuteFailure(t *testing.T) {
	mock := &mockLLMProvider{
		name:   "failing-provider",
		isReal: true,
		err:    fmt.Errorf("upstream timeout"),
	}

	bridge := NewBridgeProvider(mock)

	task := &types.TaskRecord{
		TaskID:  "task-2",
		OwnerID: "user-1",
		Prompt:  "This should fail",
	}

	var events []struct {
		kind    types.EventKind
		phase   string
		payload json.RawMessage
	}
	emit := func(kind types.EventKind, phase string, payload json.RawMessage) {
		events = append(events, struct {
			kind    types.EventKind
			phase   string
			payload json.RawMessage
		}{kind, phase, payload})
	}

	err := bridge.Execute(context.Background(), task, emit)
	if err == nil {
		t.Fatal("expected error from failed provider call")
	}
	if !strings.Contains(err.Error(), "failing-provider") {
		t.Errorf("error should mention provider name, got: %v", err)
	}
	if !strings.Contains(err.Error(), "upstream timeout") {
		t.Errorf("error should wrap original error, got: %v", err)
	}

	// Should have emitted a failure event.
	var lastPayload map[string]string
	if err := json.Unmarshal(events[len(events)-1].payload, &lastPayload); err != nil {
		t.Fatalf("unmarshal last event: %v", err)
	}
	if lastPayload["status"] != "failed" {
		t.Errorf("expected last event status 'failed', got: %s", lastPayload["status"])
	}
}

func TestBridgeProviderEventsDistinguishRealFromStub(t *testing.T) {
	// This test verifies that the events emitted by the bridge provider
	// contain a "real":"true" marker that distinguishes them from the
	// stub provider's "provider":"stub" marker.
	mock := &mockLLMProvider{
		name:   "bedrock",
		isReal: true,
		resp: &LLMResponse{
			Text:         "real response",
			Model:        "claude-sonnet",
			StopReason:   "end_turn",
			Usage:        Usage{InputTokens: 5, OutputTokens: 3},
			ProviderName: "bedrock",
		},
	}

	bridge := NewBridgeProvider(mock)
	task := &types.TaskRecord{TaskID: "t1", Prompt: "test"}
	var collected []map[string]string
	emit := func(kind types.EventKind, phase string, payload json.RawMessage) {
		var m map[string]string
		_ = json.Unmarshal(payload, &m)
		collected = append(collected, m)
	}

	_ = bridge.Execute(context.Background(), task, emit)

	// Every event should have real="true" and a non-stub provider name.
	for i, ev := range collected {
		if ev["real"] != "true" {
			t.Errorf("event %d: expected real=true, got %s", i, ev["real"])
		}
		if ev["provider"] == "stub" {
			t.Errorf("event %d: provider should not be 'stub'", i)
		}
	}
}

// --- Helper types ---

// rewriteTransport redirects requests from the original URL to the test
// server URL, preserving the path and headers.
type rewriteTransport struct {
	target  string
	original string
}

func (t *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	newURL := strings.Replace(req.URL.String(), t.original, t.target, 1)
	newReq, err := http.NewRequest(req.Method, newURL, req.Body)
	if err != nil {
		return nil, err
	}
	newReq = newReq.WithContext(req.Context())
	// Copy headers.
	for k, vs := range req.Header {
		for _, v := range vs {
			newReq.Header.Add(k, v)
		}
	}
	return http.DefaultClient.Do(newReq)
}

// --- Bedrock from Env Tests ---

func TestBedrockProviderFromEnvMissingRegion(t *testing.T) {
	t.Setenv("AWS_REGION", "")
	t.Setenv("AWS_BEARER_TOKEN_BEDROCK", "test-token")

	_, err := NewBedrockProviderFromEnv("us.anthropic.claude-sonnet-4-6")
	if err == nil || !strings.Contains(err.Error(), "region") {
		t.Fatalf("expected region error, got: %v", err)
	}
}

func TestBedrockProviderFromEnvMissingToken(t *testing.T) {
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_BEARER_TOKEN_BEDROCK", "")

	_, err := NewBedrockProviderFromEnv("us.anthropic.claude-sonnet-4-6")
	if err == nil || !strings.Contains(err.Error(), "auth token") {
		t.Fatalf("expected auth token error, got: %v", err)
	}
}

func TestBedrockProviderFromEnvPassesModel(t *testing.T) {
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_BEARER_TOKEN_BEDROCK", "test-token")

	p, err := NewBedrockProviderFromEnv("us.anthropic.claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("create from env: %v", err)
	}
	if p.modelID != "us.anthropic.claude-sonnet-4-6" {
		t.Errorf("expected passed model, got: %s", p.modelID)
	}
}

// --- Z.AI from Env Tests ---

func TestZAIProviderFromEnvMissingKey(t *testing.T) {
	t.Setenv("ZAI_API_KEY", "")

	_, err := NewZAIProviderFromEnv("glm-5.1")
	if err == nil || !strings.Contains(err.Error(), "api key") {
		t.Fatalf("expected api key error, got: %v", err)
	}
}

func TestZAIProviderFromEnvPassesModel(t *testing.T) {
	t.Setenv("ZAI_API_KEY", "test-key")

	p, err := NewZAIProviderFromEnv("glm-5.1")
	if err != nil {
		t.Fatalf("create from env: %v", err)
	}
	if p.modelID != "glm-5.1" {
		t.Errorf("expected passed model 'glm-5.1', got: %s", p.modelID)
	}
}

// --- Redaction Tests ---

func TestRedactModel(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		// Bedrock-style IDs: "us.anthropic.<long-model>"
		{"us.anthropic.claude-sonnet-4-6", "us.anthropic.clau***-4-6"},
		// Simple model name without dots
		{"simple-model", "simple-model"},
		// 4-part model ID: 3+ parts → first.second.<redacted last>
		{"a.b.c.d", "a.b.***"},
		// 2-part
		{"a.b", "a.***"},
	}
	for _, tc := range tests {
		got := redactModel(tc.input)
		if got != tc.expected {
			t.Errorf("redactModel(%q) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

func TestRedactMiddle(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"claude-sonnet-4-6", "clau***-4-6"},
		{"short", "***"},
		{"123456789", "1234***6789"},
	}
	for _, tc := range tests {
		got := redactMiddle(tc.input)
		if got != tc.expected {
			t.Errorf("redactMiddle(%q) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

func TestErrorSanitization(t *testing.T) {
	// Verify that HTTP errors from providers do not include raw response bodies.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"internal server error with secret=abc123"}`))
	}))
	defer server.Close()

	p := &BedrockProvider{
		region:     "us-east-1",
		modelID:   "test-model",
		authToken:  "test-token",
		httpClient: &http.Client{
			Timeout:   120 * time.Second,
			Transport: &rewriteTransport{target: server.URL, original: "https://bedrock-runtime.us-east-1.amazonaws.com"},
		},
		anthropicV: "bedrock-2023-05-31",
	}

	_, err := p.Call(context.Background(), LLMRequest{
		Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "test"}}}},
		MaxTokens: 1024,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "secret=abc123") {
		t.Errorf("error should not contain raw response body: %v", err)
	}
}

// --- Tool Use Content Block Tests ---

func TestBedrockProviderCallWithToolUse(t *testing.T) {
	// When the provider returns a tool_use content block in the response,
	// the LLMResponse should preserve it as a ToolCall.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify the request includes tools if present.
		var body map[string]json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&body)
		// Not strictly required for this test, but verify we can parse it.

		resp := anthropicResponse{
			ID: "msg_tool_test",
			Content: []anthropicResponseBlock{
				{Type: "text", Text: "Let me look that up."},
				{Type: "tool_use", ID: "toolu_01", Name: "read_file", Input: json.RawMessage(`{"path":"/tmp/test.txt"}`)},
			},
			StopReason: "tool_use",
			Usage:      anthropicUsage{InputTokens: 50, OutputTokens: 30},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := &BedrockProvider{
		region:     "us-east-1",
		modelID:   "us.anthropic.claude-sonnet-4-6",
		authToken:  "test-bearer-token",
		httpClient: &http.Client{
			Timeout:   120 * time.Second,
			Transport: &rewriteTransport{target: server.URL, original: "https://bedrock-runtime.us-east-1.amazonaws.com"},
		},
		anthropicV: "bedrock-2023-05-31",
	}

	resp, err := p.Call(context.Background(), LLMRequest{
		System:   "You are helpful.",
		Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "Read the test file"}}}},
		MaxTokens: 4096,
	})
	if err != nil {
		t.Fatalf("bedrock call: %v", err)
	}

	// Verify text content is preserved.
	if resp.Text != "Let me look that up." {
		t.Errorf("text: got %q, want 'Let me look that up.'", resp.Text)
	}

	// Verify stop reason is tool_use.
	if resp.StopReason != "tool_use" {
		t.Errorf("stop_reason: got %q, want tool_use", resp.StopReason)
	}

	// Verify tool calls are extracted from content blocks.
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("tool_calls: got %d, want 1", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].ID != "toolu_01" {
		t.Errorf("tool call id: got %q, want toolu_01", resp.ToolCalls[0].ID)
	}
	if resp.ToolCalls[0].Name != "read_file" {
		t.Errorf("tool call name: got %q, want read_file", resp.ToolCalls[0].Name)
	}
	if string(resp.ToolCalls[0].Arguments) != `{"path":"/tmp/test.txt"}` {
		t.Errorf("tool call arguments: got %q, want {\"path\":\"/tmp/test.txt\"}", string(resp.ToolCalls[0].Arguments))
	}
}

func TestZAIProviderCallWithToolUse(t *testing.T) {
	// Same test for Z.AI provider.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := anthropicResponse{
			ID:    "msg_zai_tool",
			Model: "glm-4.7",
			Content: []anthropicResponseBlock{
				{Type: "tool_use", ID: "toolu_02", Name: "search", Input: json.RawMessage(`{"query":"golang testing"}`)},
			},
			StopReason: "tool_use",
			Usage:      anthropicUsage{InputTokens: 40, OutputTokens: 20},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := &ZAIProvider{
		apiKey:     "test-zai-key",
		modelID:   "glm-4.7",
		httpClient: server.Client(),
		baseURL:    server.URL,
	}

	resp, err := p.Call(context.Background(), LLMRequest{
		Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "Search for golang testing"}}}},
		MaxTokens: 4096,
	})
	if err != nil {
		t.Fatalf("zai call: %v", err)
	}

	if resp.StopReason != "tool_use" {
		t.Errorf("stop_reason: got %q, want tool_use", resp.StopReason)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("tool_calls: got %d, want 1", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].ID != "toolu_02" {
		t.Errorf("tool call id: got %q, want toolu_02", resp.ToolCalls[0].ID)
	}
	if resp.ToolCalls[0].Name != "search" {
		t.Errorf("tool call name: got %q, want search", resp.ToolCalls[0].Name)
	}
}

func TestBedrockProviderCallWithMultipleToolUse(t *testing.T) {
	// Provider returns multiple tool_use blocks in one response.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := anthropicResponse{
			ID: "msg_multi_tool",
			Content: []anthropicResponseBlock{
				{Type: "tool_use", ID: "toolu_10", Name: "read_file", Input: json.RawMessage(`{"path":"/a"}`)},
				{Type: "tool_use", ID: "toolu_11", Name: "read_file", Input: json.RawMessage(`{"path":"/b"}`)},
				{Type: "tool_use", ID: "toolu_12", Name: "search", Input: json.RawMessage(`{"q":"test"}`)},
			},
			StopReason: "tool_use",
			Usage:      anthropicUsage{InputTokens: 30, OutputTokens: 60},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := &BedrockProvider{
		region:     "us-east-1",
		modelID:   "test-model",
		authToken:  "test-token",
		httpClient: &http.Client{
			Timeout:   120 * time.Second,
			Transport: &rewriteTransport{target: server.URL, original: "https://bedrock-runtime.us-east-1.amazonaws.com"},
		},
		anthropicV: "bedrock-2023-05-31",
	}

	resp, err := p.Call(context.Background(), LLMRequest{
		Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "read both files"}}}},
		MaxTokens: 4096,
	})
	if err != nil {
		t.Fatalf("bedrock call: %v", err)
	}

	if len(resp.ToolCalls) != 3 {
		t.Fatalf("tool_calls: got %d, want 3", len(resp.ToolCalls))
	}

	// Verify each tool call is preserved in order.
	names := []string{"read_file", "read_file", "search"}
	for i, tc := range resp.ToolCalls {
		if tc.Name != names[i] {
			t.Errorf("tool_calls[%d].name: got %q, want %q", i, tc.Name, names[i])
		}
	}
}

// --- extractToolCalls Tests ---

func TestExtractToolCallsFromResponse(t *testing.T) {
	// extractToolCalls should return tool calls from an LLMResponse that
	// has ToolCalls populated (from parsed tool_use content blocks).
	resp := &LLMResponse{
		ID:         "msg_test",
		StopReason: "tool_use",
		ToolCalls: []ContentToolCall{
			{ID: "toolu_1", Name: "read_file", Arguments: json.RawMessage(`{"path":"/tmp/test"}`)},
			{ID: "toolu_2", Name: "search", Arguments: json.RawMessage(`{"q":"hello"}`)},
		},
	}

	calls := extractToolCalls(resp)
	if len(calls) != 2 {
		t.Fatalf("tool calls: got %d, want 2", len(calls))
	}
	if calls[0].ID != "toolu_1" {
		t.Errorf("call[0].id: got %q, want toolu_1", calls[0].ID)
	}
	if calls[0].Name != "read_file" {
		t.Errorf("call[0].name: got %q, want read_file", calls[0].Name)
	}
	if string(calls[0].Arguments) != `{"path":"/tmp/test"}` {
		t.Errorf("call[0].arguments: got %q", string(calls[0].Arguments))
	}
	if calls[1].ID != "toolu_2" {
		t.Errorf("call[1].id: got %q, want toolu_2", calls[1].ID)
	}
}

func TestExtractToolCallsEmptyResponse(t *testing.T) {
	// extractToolCalls should return nil when there are no tool calls.
	resp := &LLMResponse{
		ID:         "msg_test",
		StopReason: "end_turn",
		Text:       "Hello!",
		ToolCalls:  nil,
	}

	calls := extractToolCalls(resp)
	if calls != nil {
		t.Errorf("expected nil for response without tool calls, got %d", len(calls))
	}
}

// --- Bridge CallWithTools Integration Tests ---

func TestBridgeProviderCallWithToolsReturnsToolCalls(t *testing.T) {
	// When the inner provider returns a response with tool_use content blocks,
	// CallWithTools should extract and return the tool calls.
	mock := &mockLLMProvider{
		name:   "test-provider",
		isReal: true,
		resp: &LLMResponse{
			ID:         "msg_tool_bridge",
			Text:       "Let me check that.",
			StopReason: "tool_use",
			Usage:      Usage{InputTokens: 50, OutputTokens: 30},
			ToolCalls: []ContentToolCall{
				{ID: "toolu_bridge_1", Name: "read_file", Arguments: json.RawMessage(`{"path":"/etc/hosts"}`)},
			},
		},
	}

	bridge := NewBridgeProvider(mock)

	req := runtime.ToolLoopRequest{
		System:     "You are helpful.",
		Messages:   []json.RawMessage{json.RawMessage(`{"role":"user","content":[{"type":"text","text":"Read the hosts file"}]}`)},
		MaxTokens:  4096,
	}

	resp, err := bridge.CallWithTools(context.Background(), req)
	if err != nil {
		t.Fatalf("call with tools: %v", err)
	}

	if resp.StopReason != "tool_use" {
		t.Errorf("stop_reason: got %q, want tool_use", resp.StopReason)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("tool_calls: got %d, want 1", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].ID != "toolu_bridge_1" {
		t.Errorf("tool call id: got %q, want toolu_bridge_1", resp.ToolCalls[0].ID)
	}
	if resp.ToolCalls[0].Name != "read_file" {
		t.Errorf("tool call name: got %q, want read_file", resp.ToolCalls[0].Name)
	}
	if string(resp.ToolCalls[0].Arguments) != `{"path":"/etc/hosts"}` {
		t.Errorf("tool call arguments: got %q", string(resp.ToolCalls[0].Arguments))
	}
}

func TestBridgeProviderCallWithToolsEndTurn(t *testing.T) {
	// When the inner provider returns end_turn, CallWithTools should
	// return an end_turn response without tool calls.
	mock := &mockLLMProvider{
		name:   "test-provider",
		isReal: true,
		resp: &LLMResponse{
			ID:         "msg_end",
			Text:       "The answer is 42.",
			StopReason: "end_turn",
			Usage:      Usage{InputTokens: 10, OutputTokens: 20},
			ToolCalls:  nil,
		},
	}

	bridge := NewBridgeProvider(mock)

	req := runtime.ToolLoopRequest{
		System:     "You are helpful.",
		Messages:   []json.RawMessage{json.RawMessage(`{"role":"user","content":[{"type":"text","text":"What is the answer?"}]}`)},
		MaxTokens:  4096,
	}

	resp, err := bridge.CallWithTools(context.Background(), req)
	if err != nil {
		t.Fatalf("call with tools: %v", err)
	}

	if resp.StopReason != "end_turn" {
		t.Errorf("stop_reason: got %q, want end_turn", resp.StopReason)
	}
	if resp.Text != "The answer is 42." {
		t.Errorf("text: got %q, want 'The answer is 42.'", resp.Text)
	}
	if len(resp.ToolCalls) != 0 {
		t.Errorf("tool_calls: got %d, want 0", len(resp.ToolCalls))
	}
}

// --- convertRawMessages Tests ---

func TestConvertRawMessagesToolUseBlock(t *testing.T) {
	// tool_use content blocks should preserve id, name, and input fields
	// when converting from raw messages to provider Message format.
	raw := []json.RawMessage{
		json.RawMessage(`{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"read_file","input":{"path":"/tmp/test"}}]}`),
	}

	msgs := convertRawMessages(raw)
	if len(msgs) != 1 {
		t.Fatalf("messages: got %d, want 1", len(msgs))
	}
	if msgs[0].Role != "assistant" {
		t.Errorf("role: got %q, want assistant", msgs[0].Role)
	}
	if len(msgs[0].Content) != 1 {
		t.Fatalf("content blocks: got %d, want 1", len(msgs[0].Content))
	}
	block := msgs[0].Content[0]
	if block.Type != "tool_use" {
		t.Errorf("block type: got %q, want tool_use", block.Type)
	}
	if block.ID != "toolu_1" {
		t.Errorf("block id: got %q, want toolu_1", block.ID)
	}
	if block.Name != "read_file" {
		t.Errorf("block name: got %q, want read_file", block.Name)
	}
	if block.Input == nil || string(block.Input) != `{"path":"/tmp/test"}` {
		t.Errorf("block input: got %q, want {\"path\":\"/tmp/test\"}", block.Input)
	}
}

func TestConvertRawMessagesToolResultBlock(t *testing.T) {
	// tool_result content blocks should preserve tool_use_id and content fields.
	raw := []json.RawMessage{
		json.RawMessage(`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"file contents here"}]}`),
	}

	msgs := convertRawMessages(raw)
	if len(msgs) != 1 {
		t.Fatalf("messages: got %d, want 1", len(msgs))
	}
	if len(msgs[0].Content) != 1 {
		t.Fatalf("content blocks: got %d, want 1", len(msgs[0].Content))
	}
	block := msgs[0].Content[0]
	if block.Type != "tool_result" {
		t.Errorf("block type: got %q, want tool_result", block.Type)
	}
	if block.ToolUseID != "toolu_1" {
		t.Errorf("block tool_use_id: got %q, want toolu_1", block.ToolUseID)
	}
	if block.Text != "file contents here" {
		t.Errorf("block text/content: got %q, want 'file contents here'", block.Text)
	}
}

func TestConvertRawMessagesMixedBlocks(t *testing.T) {
	// A single message may contain text, tool_use, and tool_result blocks.
	raw := []json.RawMessage{
		json.RawMessage(`{"role":"assistant","content":[{"type":"text","text":"I'll help"},{"type":"tool_use","id":"toolu_1","name":"search","input":{"q":"go"}}]}`),
	}

	msgs := convertRawMessages(raw)
	if len(msgs) != 1 {
		t.Fatalf("messages: got %d, want 1", len(msgs))
	}
	if len(msgs[0].Content) != 2 {
		t.Fatalf("content blocks: got %d, want 2", len(msgs[0].Content))
	}

	// First block: text.
	if msgs[0].Content[0].Type != "text" {
		t.Errorf("block[0] type: got %q, want text", msgs[0].Content[0].Type)
	}
	if msgs[0].Content[0].Text != "I'll help" {
		t.Errorf("block[0] text: got %q, want 'I'll help'", msgs[0].Content[0].Text)
	}

	// Second block: tool_use.
	if msgs[0].Content[1].Type != "tool_use" {
		t.Errorf("block[1] type: got %q, want tool_use", msgs[0].Content[1].Type)
	}
	if msgs[0].Content[1].ID != "toolu_1" {
		t.Errorf("block[1] id: got %q, want toolu_1", msgs[0].Content[1].ID)
	}
}

func TestConvertRawMessagesPlainTextContent(t *testing.T) {
	// Plain string content should still work.
	raw := []json.RawMessage{
		json.RawMessage(`{"role":"user","content":"hello world"}`),
	}

	msgs := convertRawMessages(raw)
	if len(msgs) != 1 {
		t.Fatalf("messages: got %d, want 1", len(msgs))
	}
	if len(msgs[0].Content) != 1 {
		t.Fatalf("content blocks: got %d, want 1", len(msgs[0].Content))
	}
	if msgs[0].Content[0].Type != "text" {
		t.Errorf("block type: got %q, want text", msgs[0].Content[0].Type)
	}
	if msgs[0].Content[0].Text != "hello world" {
		t.Errorf("block text: got %q, want 'hello world'", msgs[0].Content[0].Text)
	}
}

// --- convertMessages Tests ---

func TestConvertMessagesPreservesToolUseBlocks(t *testing.T) {
	// When converting Message with tool_use content blocks for the API
	// request, the id, name, and input fields must be preserved.
	msgs := []Message{
		{
			Role: "assistant",
			Content: []Block{
				{Type: "text", Text: "Let me check."},
				{Type: "tool_use", ID: "toolu_1", Name: "read_file", Input: json.RawMessage(`{"path":"/tmp/test"}`)},
			},
		},
	}

	result := convertMessages(msgs)
	if len(result) != 1 {
		t.Fatalf("messages: got %d, want 1", len(result))
	}
	if len(result[0].Content) != 2 {
		t.Fatalf("content blocks: got %d, want 2", len(result[0].Content))
	}

	// Second block should be tool_use with preserved fields.
	toolBlock := result[0].Content[1]
	if toolBlock.Type != "tool_use" {
		t.Errorf("block type: got %q, want tool_use", toolBlock.Type)
	}
	if toolBlock.ID != "toolu_1" {
		t.Errorf("block id: got %q, want toolu_1", toolBlock.ID)
	}
	if toolBlock.Name != "read_file" {
		t.Errorf("block name: got %q, want read_file", toolBlock.Name)
	}
	if toolBlock.Input == nil || string(toolBlock.Input) != `{"path":"/tmp/test"}` {
		t.Errorf("block input: got %q, want {\"path\":\"/tmp/test\"}", toolBlock.Input)
	}
}

func TestConvertMessagesPreservesToolResultBlocks(t *testing.T) {
	// When converting Message with tool_result content blocks for the API
	// request, the tool_use_id must be preserved.
	msgs := []Message{
		{
			Role: "user",
			Content: []Block{
				{Type: "tool_result", ToolUseID: "toolu_1", Text: "file contents"},
			},
		},
	}

	result := convertMessages(msgs)
	if len(result) != 1 {
		t.Fatalf("messages: got %d, want 1", len(result))
	}
	if len(result[0].Content) != 1 {
		t.Fatalf("content blocks: got %d, want 1", len(result[0].Content))
	}

	block := result[0].Content[0]
	if block.Type != "tool_result" {
		t.Errorf("block type: got %q, want tool_result", block.Type)
	}
	if block.ToolUseID != "toolu_1" {
		t.Errorf("block tool_use_id: got %q, want toolu_1", block.ToolUseID)
	}
}

// --- CallWithTools with Tool Definitions Tests ---

func TestBridgeProviderCallWithToolsPassesToolDefinitions(t *testing.T) {
	// When the bridge provider sends a CallWithTools request with tool
	// definitions, the underlying LLM request should include tools.
	var capturedReq *LLMRequest
	mock := &capturingLLMProvider{
		name: "test-provider",
		resp: &LLMResponse{
			ID:         "msg_tools",
			StopReason: "end_turn",
			Text:       "Done.",
			Usage:      Usage{InputTokens: 10, OutputTokens: 5},
		},
		capture: func(req LLMRequest) { capturedReq = &req },
	}

	bridge := NewBridgeProvider(mock)

	req := runtime.ToolLoopRequest{
		System: "You are helpful.",
		Messages: []json.RawMessage{
			json.RawMessage(`{"role":"user","content":[{"type":"text","text":"Read a file"}]}`),
		},
		ToolDefinitions: []runtime.ToolDefinition{
			{Name: "read_file", Description: "Read a file", Parameters: map[string]any{"type": "object"}},
		},
		MaxTokens: 4096,
	}

	_, err := bridge.CallWithTools(context.Background(), req)
	if err != nil {
		t.Fatalf("call with tools: %v", err)
	}

	if capturedReq == nil {
		t.Fatal("expected captured request")
	}
	if len(capturedReq.Tools) != 1 {
		t.Fatalf("tools: got %d, want 1", len(capturedReq.Tools))
	}
	if capturedReq.Tools[0].Name != "read_file" {
		t.Errorf("tool name: got %q, want read_file", capturedReq.Tools[0].Name)
	}
}

// --- Helper: capturing LLM provider ---

// capturingLLMProvider captures the LLMRequest before returning a canned response.
type capturingLLMProvider struct {
	name    string
	resp    *LLMResponse
	capture func(LLMRequest)
}

func (c *capturingLLMProvider) Call(ctx context.Context, req LLMRequest) (*LLMResponse, error) {
	if c.capture != nil {
		c.capture(req)
	}
	return c.resp, nil
}
func (c *capturingLLMProvider) Name() string  { return c.name }
func (c *capturingLLMProvider) IsReal() bool { return true }
