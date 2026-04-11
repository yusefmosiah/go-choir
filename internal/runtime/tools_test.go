package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/yusefmosiah/go-choir/internal/types"
)

// --- Tool Registry Tests ---

func TestToolRegistryRegister(t *testing.T) {
	registry := NewToolRegistry()

	tool := Tool{
		Name:        "read_file",
		Description: "Read a file from the sandbox filesystem",
		Func: func(ctx context.Context, args json.RawMessage) (string, error) {
			return "file contents", nil
		},
	}

	if err := registry.Register(tool); err != nil {
		t.Fatalf("register: %v", err)
	}

	if registry.Size() != 1 {
		t.Errorf("size: got %d, want 1", registry.Size())
	}
}

func TestToolRegistryRegisterDuplicate(t *testing.T) {
	registry := NewToolRegistry()

	tool := Tool{
		Name: "duplicate",
		Func: func(ctx context.Context, args json.RawMessage) (string, error) {
			return "", nil
		},
	}

	if err := registry.Register(tool); err != nil {
		t.Fatalf("first register: %v", err)
	}

	if err := registry.Register(tool); err == nil {
		t.Error("expected error for duplicate registration")
	}
}

func TestToolRegistryRegisterValidateNoName(t *testing.T) {
	registry := NewToolRegistry()

	tool := Tool{
		Func: func(ctx context.Context, args json.RawMessage) (string, error) {
			return "", nil
		},
	}

	if err := registry.Register(tool); err == nil {
		t.Error("expected error for tool with no name")
	}
}

func TestToolRegistryRegisterValidateNilFunc(t *testing.T) {
	registry := NewToolRegistry()

	tool := Tool{
		Name: "nil_func",
	}

	if err := registry.Register(tool); err == nil {
		t.Error("expected error for tool with nil func")
	}
}

func TestToolRegistryExecute(t *testing.T) {
	registry := NewToolRegistry()

	echoTool := Tool{
		Name:        "echo",
		Description: "Echo back the input arguments",
		Func: func(ctx context.Context, args json.RawMessage) (string, error) {
			return string(args), nil
		},
	}

	if err := registry.Register(echoTool); err != nil {
		t.Fatalf("register: %v", err)
	}

	result, err := registry.Execute(context.Background(), "echo", json.RawMessage(`{"message":"hello"}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	if result != `{"message":"hello"}` {
		t.Errorf("result: got %q, want %q", result, `{"message":"hello"}`)
	}
}

func TestToolRegistryExecuteNotFound(t *testing.T) {
	registry := NewToolRegistry()

	_, err := registry.Execute(context.Background(), "nonexistent", nil)
	if err == nil {
		t.Error("expected error for nonexistent tool")
	}
}

func TestToolRegistryExecuteError(t *testing.T) {
	registry := NewToolRegistry()

	failTool := Tool{
		Name: "fail",
		Func: func(ctx context.Context, args json.RawMessage) (string, error) {
			return "", fmt.Errorf("tool failure")
		},
	}

	if err := registry.Register(failTool); err != nil {
		t.Fatalf("register: %v", err)
	}

	_, err := registry.Execute(context.Background(), "fail", nil)
	if err == nil {
		t.Error("expected error from failing tool")
	}
}

func TestToolRegistryLookup(t *testing.T) {
	registry := NewToolRegistry()

	tool := Tool{
		Name: "findme",
		Func: func(ctx context.Context, args json.RawMessage) (string, error) {
			return "found", nil
		},
	}

	if err := registry.Register(tool); err != nil {
		t.Fatalf("register: %v", err)
	}

	found, ok := registry.Lookup("findme")
	if !ok {
		t.Fatal("expected to find tool")
	}
	if found.Name != "findme" {
		t.Errorf("name: got %q, want findme", found.Name)
	}

	_, ok = registry.Lookup("nonexistent")
	if ok {
		t.Error("should not find nonexistent tool")
	}
}

func TestToolRegistryCatalog(t *testing.T) {
	registry := NewToolRegistry()

	tools := []Tool{
		{
			Name:        "read_file",
			Description: "Read a file from the sandbox filesystem",
			Func:        func(ctx context.Context, args json.RawMessage) (string, error) { return "", nil },
		},
		{
			Name:        "list_files",
			Description: "List files in a directory within the sandbox",
			Func:        func(ctx context.Context, args json.RawMessage) (string, error) { return "", nil },
		},
	}

	for _, tool := range tools {
		if err := registry.Register(tool); err != nil {
			t.Fatalf("register %s: %v", tool.Name, err)
		}
	}

	catalog := registry.Catalog()
	if catalog == "" {
		t.Error("catalog should not be empty")
	}

	// Catalog should list both tools.
	if !contains(catalog, "read_file") {
		t.Error("catalog should contain read_file")
	}
	if !contains(catalog, "list_files") {
		t.Error("catalog should contain list_files")
	}
}

func TestToolRegistryDefinitions(t *testing.T) {
	registry := NewToolRegistry()

	tool := Tool{
		Name:        "test_tool",
		Description: "A test tool",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string"},
			},
		},
		Func: func(ctx context.Context, args json.RawMessage) (string, error) { return "", nil },
	}

	if err := registry.Register(tool); err != nil {
		t.Fatalf("register: %v", err)
	}

	defs := registry.Definitions()
	if len(defs) != 1 {
		t.Fatalf("definitions: got %d, want 1", len(defs))
	}
	if defs[0].Name != "test_tool" {
		t.Errorf("name: got %q, want test_tool", defs[0].Name)
	}
	if defs[0].Description != "A test tool" {
		t.Errorf("description: got %q, want A test tool", defs[0].Description)
	}
}

func TestToolRegistryDefaultParameters(t *testing.T) {
	registry := NewToolRegistry()

	// Tool without parameters should get a default empty object schema.
	tool := Tool{
		Name: "no_params",
		Func: func(ctx context.Context, args json.RawMessage) (string, error) { return "", nil },
	}

	if err := registry.Register(tool); err != nil {
		t.Fatalf("register: %v", err)
	}

	found, ok := registry.Lookup("no_params")
	if !ok {
		t.Fatal("expected to find tool")
	}
	if found.Parameters == nil {
		t.Error("parameters should not be nil (should have default empty object)")
	}
}

func TestNewToolRegistryWithTools(t *testing.T) {
	tools := []Tool{
		{
			Name: "tool_a",
			Func: func(ctx context.Context, args json.RawMessage) (string, error) { return "a", nil },
		},
		{
			Name: "tool_b",
			Func: func(ctx context.Context, args json.RawMessage) (string, error) { return "b", nil },
		},
	}

	registry, err := NewToolRegistryWithTools(tools...)
	if err != nil {
		t.Fatalf("new with tools: %v", err)
	}

	if registry.Size() != 2 {
		t.Errorf("size: got %d, want 2", registry.Size())
	}
}

func TestMustNewToolRegistryPanics(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Error("expected panic for invalid tool")
		}
	}()

	MustNewToolRegistry(Tool{Name: ""})
}

func TestToolRegistryToolsSorted(t *testing.T) {
	registry := NewToolRegistry()

	// Register in non-alphabetical order.
	names := []string{"z_tool", "a_tool", "m_tool"}
	for _, name := range names {
		tool := Tool{
			Name: name,
			Func: func(ctx context.Context, args json.RawMessage) (string, error) { return "", nil },
		}
		if err := registry.Register(tool); err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
	}

	tools := registry.Tools()
	if len(tools) != 3 {
		t.Fatalf("tools: got %d, want 3", len(tools))
	}

	// Should be sorted alphabetically.
	if tools[0].Name != "a_tool" || tools[1].Name != "m_tool" || tools[2].Name != "z_tool" {
		t.Errorf("tools not sorted: got %s, %s, %s", tools[0].Name, tools[1].Name, tools[2].Name)
	}
}

// --- Tool Struct Tests ---

func TestToolValidate(t *testing.T) {
	tests := []struct {
		name    string
		tool    Tool
		wantErr bool
	}{
		{
			name: "valid tool",
			tool: Tool{Name: "valid", Func: func(ctx context.Context, args json.RawMessage) (string, error) { return "", nil }},
			wantErr: false,
		},
		{
			name: "empty name",
			tool: Tool{Name: "", Func: func(ctx context.Context, args json.RawMessage) (string, error) { return "", nil }},
			wantErr: true,
		},
		{
			name: "nil func",
			tool: Tool{Name: "nil_func"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.tool.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestToolDefinition(t *testing.T) {
	tool := Tool{
		Name:        "test",
		Description: "desc",
		Parameters:  map[string]any{"type": "object"},
		Func:        func(ctx context.Context, args json.RawMessage) (string, error) { return "", nil },
	}

	def := tool.Definition()
	if def.Name != "test" {
		t.Errorf("name: got %q, want test", def.Name)
	}
	if def.Description != "desc" {
		t.Errorf("description: got %q, want desc", def.Description)
	}
	if def.Parameters["type"] != "object" {
		t.Errorf("parameters: got %v, want object", def.Parameters["type"])
	}
}

// --- executeTools Tests ---

func TestExecuteTools(t *testing.T) {
	registry := NewToolRegistry()

	echoTool := Tool{
		Name: "echo",
		Func: func(ctx context.Context, args json.RawMessage) (string, error) {
			return string(args), nil
		},
	}
	if err := registry.Register(echoTool); err != nil {
		t.Fatalf("register: %v", err)
	}

	calls := []types.ToolCall{
		{ID: "call-1", Name: "echo", Arguments: json.RawMessage(`{"msg":"hello"}`)},
	}

	var emittedKinds []types.EventKind
	emit := func(kind types.EventKind, phase string, payload json.RawMessage) {
		emittedKinds = append(emittedKinds, kind)
	}

	results := executeTools(context.Background(), registry, calls, emit)

	if len(results) != 1 {
		t.Fatalf("results: got %d, want 1", len(results))
	}
	if results[0].CallID != "call-1" {
		t.Errorf("call_id: got %q, want call-1", results[0].CallID)
	}
	if results[0].Output != `{"msg":"hello"}` {
		t.Errorf("output: got %q, want echo result", results[0].Output)
	}
	if results[0].IsError {
		t.Error("should not be error")
	}

	// Should emit tool.invoked and tool.result events.
	if len(emittedKinds) != 2 {
		t.Fatalf("emitted events: got %d, want 2", len(emittedKinds))
	}
	if emittedKinds[0] != types.EventToolInvoked {
		t.Errorf("first event: got %q, want tool.invoked", emittedKinds[0])
	}
	if emittedKinds[1] != types.EventToolResult {
		t.Errorf("second event: got %q, want tool.result", emittedKinds[1])
	}
}

func TestExecuteToolsParallel(t *testing.T) {
	registry := NewToolRegistry()

	slowTool := Tool{
		Name: "slow",
		Func: func(ctx context.Context, args json.RawMessage) (string, error) {
			return "slow-result", nil
		},
	}
	fastTool := Tool{
		Name: "fast",
		Func: func(ctx context.Context, args json.RawMessage) (string, error) {
			return "fast-result", nil
		},
	}
	if err := registry.Register(slowTool); err != nil {
		t.Fatalf("register slow: %v", err)
	}
	if err := registry.Register(fastTool); err != nil {
		t.Fatalf("register fast: %v", err)
	}

	calls := []types.ToolCall{
		{ID: "call-1", Name: "slow", Arguments: json.RawMessage(`{}`)},
		{ID: "call-2", Name: "fast", Arguments: json.RawMessage(`{}`)},
	}

	emit := func(kind types.EventKind, phase string, payload json.RawMessage) {}

	results := executeTools(context.Background(), registry, calls, emit)

	// Results should be in the same order as the calls.
	if results[0].CallID != "call-1" {
		t.Errorf("result[0] call_id: got %q, want call-1", results[0].CallID)
	}
	if results[0].Output != "slow-result" {
		t.Errorf("result[0] output: got %q, want slow-result", results[0].Output)
	}
	if results[1].CallID != "call-2" {
		t.Errorf("result[1] call_id: got %q, want call-2", results[1].CallID)
	}
	if results[1].Output != "fast-result" {
		t.Errorf("result[1] output: got %q, want fast-result", results[1].Output)
	}
}

func TestExecuteToolsError(t *testing.T) {
	registry := NewToolRegistry()

	failTool := Tool{
		Name: "fail",
		Func: func(ctx context.Context, args json.RawMessage) (string, error) {
			return "", fmt.Errorf("tool failure")
		},
	}
	if err := registry.Register(failTool); err != nil {
		t.Fatalf("register: %v", err)
	}

	calls := []types.ToolCall{
		{ID: "call-1", Name: "fail", Arguments: json.RawMessage(`{}`)},
	}

	emit := func(kind types.EventKind, phase string, payload json.RawMessage) {}

	results := executeTools(context.Background(), registry, calls, emit)

	if !results[0].IsError {
		t.Error("expected error result")
	}
	if results[0].Output == "" {
		t.Error("error output should contain error message")
	}
}

func TestExecuteToolsOutputTruncation(t *testing.T) {
	registry := NewToolRegistry()

	bigTool := Tool{
		Name: "big_output",
		Func: func(ctx context.Context, args json.RawMessage) (string, error) {
			// Return output larger than 100KB.
			result := make([]byte, 150*1024)
			for i := range result {
				result[i] = 'x'
			}
			return string(result), nil
		},
	}
	if err := registry.Register(bigTool); err != nil {
		t.Fatalf("register: %v", err)
	}

	calls := []types.ToolCall{
		{ID: "call-1", Name: "big_output", Arguments: json.RawMessage(`{}`)},
	}

	emit := func(kind types.EventKind, phase string, payload json.RawMessage) {}

	results := executeTools(context.Background(), registry, calls, emit)

	// Output should be truncated to ~100KB + truncation notice.
	if len(results[0].Output) > 110*1024 {
		t.Errorf("output should be truncated, got %d bytes", len(results[0].Output))
	}
}

// --- buildSystemPromptWithTools Tests ---

func TestBuildSystemPromptWithTools(t *testing.T) {
	registry := NewToolRegistry()
	if err := registry.Register(Tool{
		Name:        "test_tool",
		Description: "A test tool for the prompt",
		Func:        func(ctx context.Context, args json.RawMessage) (string, error) { return "", nil },
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	result := buildSystemPromptWithTools("Base prompt.", registry)
	if result == "Base prompt." {
		t.Error("should append tool catalog to base prompt")
	}
	if !contains(result, "test_tool") {
		t.Error("should include tool name in catalog")
	}
}

func TestBuildSystemPromptWithNilRegistry(t *testing.T) {
	result := buildSystemPromptWithTools("Base prompt.", nil)
	if result != "Base prompt." {
		t.Errorf("nil registry should return base prompt unchanged, got %q", result)
	}
}

func TestBuildSystemPromptWithEmptyRegistry(t *testing.T) {
	registry := NewToolRegistry()
	result := buildSystemPromptWithTools("Base prompt.", registry)
	if result != "Base prompt." {
		t.Errorf("empty registry should return base prompt unchanged, got %q", result)
	}
}

// --- Helper ---

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstr(s, substr))
}

func containsSubstr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
