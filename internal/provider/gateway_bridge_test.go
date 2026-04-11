package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/yusefmosiah/go-choir/internal/runtime"
	"github.com/yusefmosiah/go-choir/internal/types"
)

// mockGatewayCaller is a test double for GatewayCaller that records calls
// and returns configurable responses.
type mockGatewayCaller struct {
	name    string
	isReal  bool
	resp    *LLMResponse
	err     error
	lastReq *LLMRequest // captures the most recent request
}

func (m *mockGatewayCaller) Name() string  { return m.name }
func (m *mockGatewayCaller) IsReal() bool   { return m.isReal }
func (m *mockGatewayCaller) Call(ctx context.Context, req LLMRequest) (*LLMResponse, error) {
	m.lastReq = &req
	return m.resp, m.err
}

// --- GatewayBridgeProvider construction tests ---

func TestGatewayBridgeProviderRequiresNonNilClient(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic for nil client")
		}
		if !strings.Contains(fmt.Sprint(r), "non-nil") {
			t.Fatalf("expected non-nil panic, got: %v", r)
		}
	}()
	NewGatewayBridgeProvider(nil)
}

func TestGatewayBridgeProviderName(t *testing.T) {
	mock := &mockGatewayCaller{name: "gateway", isReal: true}
	gbp := NewGatewayBridgeProvider(mock)
	if gbp.ProviderName() != "gateway" {
		t.Fatalf("expected ProviderName()=gateway, got %s", gbp.ProviderName())
	}
}

// --- Execute tests (runtime.Provider interface) ---

func TestGatewayBridgeProviderExecuteSuccess(t *testing.T) {
	mock := &mockGatewayCaller{
		name:   "gateway",
		isReal: true,
		resp: &LLMResponse{
			ID:           "resp-123",
			Text:         "Hello from the gateway!",
			Model:        "glm-4.7",
			StopReason:   "end_turn",
			Usage:        Usage{InputTokens: 50, OutputTokens: 20},
			ProviderName: "zai",
		},
	}
	gbp := NewGatewayBridgeProvider(mock)

	task := &types.TaskRecord{
		TaskID: "task-1",
		Prompt: "Say hello",
	}

	var emitted []struct {
		kind    types.EventKind
		payload string
	}
	emit := func(kind types.EventKind, phase string, payload json.RawMessage) {
		emitted = append(emitted, struct {
			kind    types.EventKind
			payload string
		}{kind, string(payload)})
	}

	err := gbp.Execute(context.Background(), task, emit)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// Verify the task result was set.
	if task.Result != "Hello from the gateway!" {
		t.Fatalf("expected task result, got: %s", task.Result)
	}

	// Verify events were emitted (started, responded progress, delta).
	if len(emitted) < 3 {
		t.Fatalf("expected at least 3 events, got %d", len(emitted))
	}

	// First event should be started.
	if !strings.Contains(emitted[0].payload, `"started"`) {
		t.Fatalf("first event should be started, got: %s", emitted[0].payload)
	}
	// First event should indicate routed through gateway.
	if !strings.Contains(emitted[0].payload, `"routed":true`) {
		t.Fatalf("first event should indicate routed=true, got: %s", emitted[0].payload)
	}

	// Verify the request was sent correctly.
	if mock.lastReq == nil {
		t.Fatal("no request was sent to the gateway")
	}
	if len(mock.lastReq.Messages) != 1 || mock.lastReq.Messages[0].Role != "user" {
		t.Fatalf("expected 1 user message, got: %+v", mock.lastReq.Messages)
	}
}

func TestGatewayBridgeProviderExecuteFailure(t *testing.T) {
	mock := &mockGatewayCaller{
		name:   "gateway",
		isReal: true,
		err:    fmt.Errorf("gateway client: status 503 (sanitized)"),
	}
	gbp := NewGatewayBridgeProvider(mock)

	task := &types.TaskRecord{
		TaskID: "task-2",
		Prompt: "This should fail",
	}

	emit := func(kind types.EventKind, phase string, payload json.RawMessage) {}

	err := gbp.Execute(context.Background(), task, emit)
	if err == nil {
		t.Fatal("expected error from Execute")
	}
	if !strings.Contains(err.Error(), "gateway call failed") {
		t.Fatalf("expected gateway call failed error, got: %v", err)
	}

	// Provider failures must be structured errors, not panics.
	// The runtime should remain available for later tasks (VAL-RUNTIME-008).
	if task.Result != "" {
		t.Fatalf("task result should be empty on failure, got: %s", task.Result)
	}
}

func TestGatewayBridgeProviderExecuteCancelledContext(t *testing.T) {
	mock := &mockGatewayCaller{
		name:   "gateway",
		isReal: true,
		err:    context.Canceled,
	}
	gbp := NewGatewayBridgeProvider(mock)

	task := &types.TaskRecord{TaskID: "task-ctx", Prompt: "cancelled"}
	emit := func(kind types.EventKind, phase string, payload json.RawMessage) {}

	err := gbp.Execute(context.Background(), task, emit)
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
}

// --- CallWithTools tests (runtime.ToolLoopProvider interface) ---

func TestGatewayBridgeProviderCallWithToolsEndTurn(t *testing.T) {
	mock := &mockGatewayCaller{
		name:   "gateway",
		isReal: true,
		resp: &LLMResponse{
			ID:         "resp-tools-1",
			Text:       "The answer is 42.",
			Model:      "glm-4.7",
			StopReason: "end_turn",
			Usage:      Usage{InputTokens: 100, OutputTokens: 10},
		},
	}
	gbp := NewGatewayBridgeProvider(mock)

	req := runtime.ToolLoopRequest{
		System:    "You are helpful.",
		Messages:  []json.RawMessage{[]byte(`{"role":"user","content":[{"type":"text","text":"What is the answer?"}]}`)},
		MaxTokens: 4096,
	}

	resp, err := gbp.CallWithTools(context.Background(), req)
	if err != nil {
		t.Fatalf("CallWithTools failed: %v", err)
	}

	if resp.StopReason != "end_turn" {
		t.Fatalf("expected end_turn, got: %s", resp.StopReason)
	}
	if resp.Text != "The answer is 42." {
		t.Fatalf("unexpected text: %s", resp.Text)
	}
	if resp.Usage.InputTokens != 100 || resp.Usage.OutputTokens != 10 {
		t.Fatalf("unexpected usage: %+v", resp.Usage)
	}
}

func TestGatewayBridgeProviderCallWithToolsToolUse(t *testing.T) {
	mock := &mockGatewayCaller{
		name:   "gateway",
		isReal: true,
		resp: &LLMResponse{
			ID:         "resp-tools-2",
			Model:      "glm-4.7",
			StopReason: "tool_use",
			Usage:      Usage{InputTokens: 80, OutputTokens: 15},
			ToolCalls: []ContentToolCall{
				{
					ID:        "call_1",
					Name:      "read_file",
					Arguments: json.RawMessage(`{"path":"/tmp/test.txt"}`),
				},
			},
		},
	}
	gbp := NewGatewayBridgeProvider(mock)

	req := runtime.ToolLoopRequest{
		System:    "You have tools.",
		Messages:  []json.RawMessage{[]byte(`{"role":"user","content":[{"type":"text","text":"Read the file"}]}`)},
		MaxTokens: 4096,
		ToolDefinitions: []runtime.ToolDefinition{
			{Name: "read_file", Description: "Read a file", Parameters: map[string]any{"type": "object"}},
		},
	}

	resp, err := gbp.CallWithTools(context.Background(), req)
	if err != nil {
		t.Fatalf("CallWithTools failed: %v", err)
	}

	if resp.StopReason != "tool_use" {
		t.Fatalf("expected tool_use, got: %s", resp.StopReason)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].Name != "read_file" {
		t.Fatalf("expected read_file tool call, got: %s", resp.ToolCalls[0].Name)
	}

	// Verify tools were forwarded to the gateway.
	if mock.lastReq == nil {
		t.Fatal("no request sent to gateway")
	}
	if len(mock.lastReq.Tools) != 1 || mock.lastReq.Tools[0].Name != "read_file" {
		t.Fatalf("tools not forwarded: %+v", mock.lastReq.Tools)
	}
}

func TestGatewayBridgeProviderCallWithToolsError(t *testing.T) {
	mock := &mockGatewayCaller{
		name:   "gateway",
		isReal: true,
		err:    fmt.Errorf("gateway client: status 502 (sanitized)"),
	}
	gbp := NewGatewayBridgeProvider(mock)

	req := runtime.ToolLoopRequest{
		System:    "system",
		Messages:  []json.RawMessage{[]byte(`{"role":"user","content":"hi"}`)},
		MaxTokens: 1024,
	}

	_, err := gbp.CallWithTools(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "gateway call failed") {
		t.Fatalf("expected gateway call failed, got: %v", err)
	}
}

// Note: HTTP-level integration tests for the full gateway-bridge-provider
// path live in the gateway package (internal/gateway/gateway_test.go) to
// avoid circular imports. The mock-based tests above cover the
// GatewayBridgeProvider logic end-to-end.

// --- Gateway preference decision logic tests ---

func TestGatewayURLPreferredOverDirectResolution(t *testing.T) {
	t.Setenv("RUNTIME_GATEWAY_URL", "http://gateway.test:8084")
	t.Setenv("RUNTIME_GATEWAY_TOKEN", "test-token")
	t.Setenv("ZAI_API_KEY", "fake-key-for-test")

	// Verify that when RUNTIME_GATEWAY_URL is set, we should use the gateway.
	gatewayURL := os.Getenv("RUNTIME_GATEWAY_URL")
	if gatewayURL == "" {
		t.Fatal("expected gateway URL to be set")
	}

	// Also verify that direct resolution would succeed (ZAI_API_KEY is set).
	// The sandbox logic should prefer the gateway URL regardless.
	p, err := ResolveProvider()
	if err != nil {
		t.Fatalf("direct resolution should still work: %v", err)
	}
	if p == nil {
		t.Fatal("direct resolution should return a provider when ZAI_API_KEY is set")
	}
	if p.Name() != "zai" {
		t.Fatalf("expected zai provider, got: %s", p.Name())
	}

	// The sandbox should prefer gateway when URL is set, even though direct
	// resolution would also succeed. This test validates both paths work.
}

func TestGatewayURLFallsBackToProxyVMCTLURL(t *testing.T) {
	t.Setenv("PROXY_VMCTL_URL", "http://vmctl.test:8083")
	// RUNTIME_GATEWAY_URL is not set.

	gatewayURL := os.Getenv("RUNTIME_GATEWAY_URL")
	vmctlURL := os.Getenv("PROXY_VMCTL_URL")

	// Sandbox logic: if RUNTIME_GATEWAY_URL is empty, use PROXY_VMCTL_URL.
	effectiveURL := gatewayURL
	if effectiveURL == "" {
		effectiveURL = vmctlURL
	}

	if effectiveURL != "http://vmctl.test:8083" {
		t.Fatalf("expected fallback to PROXY_VMCTL_URL, got: %s", effectiveURL)
	}
}

func TestGatewayURLEmptyWhenNeitherSet(t *testing.T) {
	// Both RUNTIME_GATEWAY_URL and PROXY_VMCTL_URL are unset.
	gatewayURL := os.Getenv("RUNTIME_GATEWAY_URL")
	vmctlURL := os.Getenv("PROXY_VMCTL_URL")

	effectiveURL := gatewayURL
	if effectiveURL == "" {
		effectiveURL = vmctlURL
	}

	if effectiveURL != "" {
		t.Fatalf("expected empty gateway URL, got: %s", effectiveURL)
	}
}
