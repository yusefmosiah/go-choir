package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yusefmosiah/go-choir/internal/store"
	"github.com/yusefmosiah/go-choir/internal/types"
)

// --- file_read Tool Implementation ---
//
// The file_read tool is a real in-process tool that reads a file from the
// local filesystem and returns its contents. It is the first real tool
// registered in the ToolRegistry for tool-calling validation.
//
// It validates:
//   - VAL-LLM-010: LLM request with tools returns tool_use response
//   - VAL-LLM-011: Tool calling loop executes tool, feeds result back to LLM
//   - VAL-LLM-012: Multi-tool loop executes multiple tools sequentially

// fileReadTool returns a Tool that reads a file from the given base directory.
// The tool expects a JSON argument with a "path" field (relative to baseDir).
func fileReadTool(baseDir string) Tool {
	return Tool{
		Name:        "file_read",
		Description: "Read the contents of a file from the sandbox filesystem. Provide the file path as a relative or absolute path.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Path to the file to read",
				},
			},
			"required": []string{"path"},
		},
		Func: func(ctx context.Context, args json.RawMessage) (string, error) {
			var params struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal(args, &params); err != nil {
				return "", fmt.Errorf("file_read: invalid arguments: %w", err)
			}
			if params.Path == "" {
				return "", fmt.Errorf("file_read: path is required")
			}

			// Resolve relative to baseDir for safety.
			fullPath := filepath.Join(baseDir, params.Path)
			data, err := os.ReadFile(fullPath)
			if err != nil {
				return "", fmt.Errorf("file_read: %w", err)
			}
			return string(data), nil
		},
	}
}

// --- TestToolLoop: Comprehensive Tool Calling Validation ---
//
// These tests validate the full tool-calling loop end-to-end, covering:
// 1. Tool registration (file_read)
// 2. LLM invokes file_read during task
// 3. Tool result fed back to LLM
// 4. Tool events emitted (tool.invoked, tool.result)
// 5. Task completes with tool-augmented response
// 6. Multi-tool sequential execution

// TestToolLoopFileReadRegistered validates that the file_read tool can be
// registered in the ToolRegistry and is available for tool-calling.
func TestToolLoopFileReadRegistered(t *testing.T) {
	registry := NewToolRegistry()

	tmpDir := t.TempDir()
	tool := fileReadTool(tmpDir)

	if err := registry.Register(tool); err != nil {
		t.Fatalf("register file_read: %v", err)
	}

	// Verify the tool is registered.
	found, ok := registry.Lookup("file_read")
	if !ok {
		t.Fatal("file_read tool should be registered")
	}
	if found.Name != "file_read" {
		t.Errorf("name: got %q, want file_read", found.Name)
	}
	if found.Description == "" {
		t.Error("file_read tool should have a description")
	}
	if found.Parameters == nil {
		t.Error("file_read tool should have parameters schema")
	}

	// Verify the tool appears in definitions for LLM-facing schema.
	defs := registry.Definitions()
	if len(defs) != 1 {
		t.Fatalf("definitions: got %d, want 1", len(defs))
	}
	if defs[0].Name != "file_read" {
		t.Errorf("definition name: got %q, want file_read", defs[0].Name)
	}

	// Verify the tool appears in catalog for system prompt.
	catalog := registry.Catalog()
	if catalog == "" {
		t.Error("catalog should not be empty")
	}
}

// TestToolLoopFileReadExecution tests that the file_read tool can actually
// read a file and return its contents.
func TestToolLoopFileReadExecution(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a test file.
	testContent := "Hello from the test file!\nLine 2\nLine 3"
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte(testContent), 0o644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	registry := NewToolRegistry()
	if err := registry.Register(fileReadTool(tmpDir)); err != nil {
		t.Fatalf("register: %v", err)
	}

	// Execute the tool with file path.
	result, err := registry.Execute(context.Background(), "file_read",
		json.RawMessage(`{"path":"test.txt"}`))
	if err != nil {
		t.Fatalf("execute file_read: %v", err)
	}

	if result != testContent {
		t.Errorf("file_read result:\ngot:  %q\nwant: %q", result, testContent)
	}
}

// TestToolLoopFileReadError tests that the file_read tool returns an error
// for non-existent files.
func TestToolLoopFileReadError(t *testing.T) {
	tmpDir := t.TempDir()
	registry := NewToolRegistry()
	if err := registry.Register(fileReadTool(tmpDir)); err != nil {
		t.Fatalf("register: %v", err)
	}

	_, err := registry.Execute(context.Background(), "file_read",
		json.RawMessage(`{"path":"nonexistent.txt"}`))
	if err == nil {
		t.Error("expected error for non-existent file")
	}
}

// TestToolLoopFileReadWithRuntime tests the full runtime path: submit a task
// that triggers file_read through the tool-calling loop, and verify the
// file contents are incorporated into the final response.
//
// This validates:
//   - file_read tool registered in ToolRegistry
//   - LLM can invoke file_read tool during task
//   - Tool result fed back to LLM
//   - Tool events emitted (tool.invoked, tool.result)
//   - Task completes with tool-augmented response
func TestToolLoopFileReadWithRuntime(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a test file with recognizable content.
	testContent := "NAME=\"ChoirOS\"\nVERSION=\"1.0\"\nID=choir"
	testFile := filepath.Join(tmpDir, "os-release")
	if err := os.WriteFile(testFile, []byte(testContent), 0o644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	registry := NewToolRegistry()
	if err := registry.Register(fileReadTool(tmpDir)); err != nil {
		t.Fatalf("register file_read: %v", err)
	}

	// Mock provider that simulates LLM calling file_read then producing final answer.
	provider := newMockToolLoopProvider(
		// First response: LLM requests file_read tool.
		&ToolLoopResponse{
			StopReason: "tool_use",
			Text:       "",
			ToolCalls: []types.ToolCall{
				{
					ID:        "call-fr-1",
					Name:      "file_read",
					Arguments: json.RawMessage(`{"path":"os-release"}`),
				},
			},
			Usage: TokenUsage{InputTokens: 50, OutputTokens: 20},
			Model: "test-model",
		},
		// Second response: LLM summarizes the file contents.
		&ToolLoopResponse{
			StopReason: "end_turn",
			Text:       "The system is ChoirOS version 1.0.",
			Usage:      TokenUsage{InputTokens: 80, OutputTokens: 15},
			Model:      "test-model",
		},
	)

	rt, s := testRuntimeWithProviderAndRegistry(t, provider, registry)
	defer rt.Stop()

	// Subscribe to events to capture tool events.
	ch := rt.EventBus().SubscribeWithBuffer(256)
	defer rt.EventBus().Unsubscribe(ch)

	// Submit task that should trigger file_read.
	rec, err := rt.StartRun(context.Background(),
		"Read the file os-release and tell me what OS this is", "user-toolloop-test")
	if err != nil {
		t.Fatalf("submit task: %v", err)
	}

	// Wait for completion with timeout.
	waitForToolLoopTask(t, s, rec.RunID, 5*time.Second)

	// Verify the task completed successfully.
	fetched, err := s.GetRun(context.Background(), rec.RunID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if fetched.State != types.RunCompleted {
		t.Fatalf("task state: got %q, want completed (error: %s)", fetched.State, fetched.Error)
	}

	// Verify the result incorporates file_read output.
	if fetched.Result != "The system is ChoirOS version 1.0." {
		t.Errorf("result: got %q, want response incorporating file_read output", fetched.Result)
	}

	// Verify token usage stored in metadata.
	if fetched.Metadata == nil {
		t.Fatal("metadata should not be nil")
	}
	if inputTok, ok := fetched.Metadata["input_tokens"].(float64); !ok || inputTok != 130 {
		t.Errorf("input_tokens: got %v, want 130", fetched.Metadata["input_tokens"])
	}

	// Verify tool events were emitted and persisted. The task state can become
	// visible a moment before the final event query observes the completed-event
	// row, so poll briefly instead of assuming a single read is enough.
	var (
		evts          []types.EventRecord
		toolInvoked   bool
		toolResult    bool
		taskCompleted bool
	)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		evts, err = s.ListEvents(context.Background(), rec.RunID, 100)
		if err != nil {
			t.Fatalf("list events: %v", err)
		}

		toolInvoked = false
		toolResult = false
		taskCompleted = false
		for _, ev := range evts {
			switch ev.Kind {
			case types.EventToolInvoked:
				toolInvoked = true
				// Verify the payload contains the tool name.
				var payload map[string]string
				if err := json.Unmarshal(ev.Payload, &payload); err != nil {
					t.Errorf("unmarshal tool.invoked payload: %v", err)
				} else if payload["tool"] != "file_read" {
					t.Errorf("tool.invoked tool: got %q, want file_read", payload["tool"])
				}
			case types.EventToolResult:
				toolResult = true
				var payload map[string]any
				if err := json.Unmarshal(ev.Payload, &payload); err != nil {
					t.Errorf("unmarshal tool.result payload: %v", err)
				} else {
					if payload["tool"] != "file_read" {
						t.Errorf("tool.result tool: got %v, want file_read", payload["tool"])
					}
					if isError, _ := payload["is_error"].(bool); isError {
						t.Error("tool.result should not be an error")
					}
				}
			case types.EventRunCompleted:
				taskCompleted = true
			}
		}

		if toolInvoked && toolResult && taskCompleted {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if !toolInvoked {
		t.Error("expected persisted tool.invoked event")
	}
	if !toolResult {
		t.Error("expected persisted tool.result event")
	}
	if !taskCompleted {
		t.Error("expected persisted run.completed event")
	}

	// Also verify tool events were published to the live event bus.
	var liveInvoked, liveResult bool
	timeout := time.After(2 * time.Second)
	for !liveInvoked || !liveResult {
		select {
		case ev := <-ch:
			if ev.Record.RunID != rec.RunID {
				continue
			}
			if ev.Record.Kind == types.EventToolInvoked {
				liveInvoked = true
			}
			if ev.Record.Kind == types.EventToolResult {
				liveResult = true
			}
		case <-timeout:
			t.Fatalf("timed out waiting for live tool events (invoked=%v result=%v)", liveInvoked, liveResult)
		}
	}
}

// TestToolLoopToolUseResponse validates VAL-LLM-010: An LLM request with
// tool definitions can return a tool_use stop reason with tool calls.
func TestToolLoopToolUseResponse(t *testing.T) {
	registry := NewToolRegistry()
	if err := registry.Register(fileReadTool(t.TempDir())); err != nil {
		t.Fatalf("register: %v", err)
	}

	// Provider returns tool_use with a valid tool call.
	provider := newMockToolLoopProvider(
		&ToolLoopResponse{
			ID:         "resp-001",
			StopReason: "tool_use",
			ToolCalls: []types.ToolCall{
				{
					ID:        "call-001",
					Name:      "file_read",
					Arguments: json.RawMessage(`{"path":"/etc/hosts"}`),
				},
			},
			Usage: TokenUsage{InputTokens: 30, OutputTokens: 10},
			Model: "test-model",
		},
		// Second turn: final response after tool result.
		&ToolLoopResponse{
			ID:         "resp-002",
			StopReason: "end_turn",
			Text:       "The hosts file contains localhost mappings.",
			Usage:      TokenUsage{InputTokens: 50, OutputTokens: 20},
			Model:      "test-model",
		},
	)

	var capturedEvents []struct {
		kind    types.EventKind
		payload json.RawMessage
	}
	emit := func(kind types.EventKind, phase string, payload json.RawMessage) {
		capturedEvents = append(capturedEvents, struct {
			kind    types.EventKind
			payload json.RawMessage
		}{kind, payload})
	}

	text, usage, err := RunToolLoop(
		context.Background(),
		provider,
		registry,
		[]json.RawMessage{json.RawMessage(`{"role":"user","content":"Read /etc/hosts and summarize"}`)},
		"You are helpful.",
		4096,
		emit,
	)

	if err != nil {
		t.Fatalf("run tool loop: %v", err)
	}

	// VAL-LLM-010: Response should contain tool-augmented text.
	if text != "The hosts file contains localhost mappings." {
		t.Errorf("text: got %q, want tool-augmented response", text)
	}

	// Token usage should accumulate across iterations.
	if usage.InputTokens != 80 {
		t.Errorf("total input tokens: got %d, want 80", usage.InputTokens)
	}
	if usage.OutputTokens != 30 {
		t.Errorf("total output tokens: got %d, want 30", usage.OutputTokens)
	}

	// Verify tool.invoked and tool.result events were emitted.
	var invokedCount, resultCount int
	for _, ev := range capturedEvents {
		if ev.kind == types.EventToolInvoked {
			invokedCount++
		}
		if ev.kind == types.EventToolResult {
			resultCount++
		}
	}
	if invokedCount == 0 {
		t.Error("VAL-LLM-010: expected tool.invoked events")
	}
	if resultCount == 0 {
		t.Error("VAL-LLM-010: expected tool.result events")
	}
}

// TestToolLoopToolResultFedBack validates VAL-LLM-011: The tool calling loop
// executes requested tools and feeds results back to the LLM for final
// response generation.
func TestToolLoopToolResultFedBack(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a file with specific content.
	expectedContent := "The quick brown fox jumps over the lazy dog."
	testFile := filepath.Join(tmpDir, "story.txt")
	if err := os.WriteFile(testFile, []byte(expectedContent), 0o644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	registry := NewToolRegistry()
	if err := registry.Register(fileReadTool(tmpDir)); err != nil {
		t.Fatalf("register: %v", err)
	}

	// Track the messages sent to the provider to verify tool results
	// are included in the conversation.
	var capturedMessages []json.RawMessage
	provider := &messageCapturingProvider{
		responses: []*ToolLoopResponse{
			// First: LLM requests file_read.
			{
				StopReason: "tool_use",
				ToolCalls: []types.ToolCall{
					{
						ID:        "call-feed-1",
						Name:      "file_read",
						Arguments: json.RawMessage(`{"path":"story.txt"}`),
					},
				},
				Usage: TokenUsage{InputTokens: 20, OutputTokens: 10},
				Model: "test-model",
			},
			// Second: LLM produces final answer incorporating file content.
			{
				StopReason: "end_turn",
				Text:       "The file contains a pangram: The quick brown fox jumps over the lazy dog.",
				Usage:      TokenUsage{InputTokens: 40, OutputTokens: 20},
				Model:      "test-model",
			},
		},
		capturedMessages: &capturedMessages,
	}

	emit := func(kind types.EventKind, phase string, payload json.RawMessage) {}

	text, _, err := RunToolLoop(
		context.Background(),
		provider,
		registry,
		[]json.RawMessage{json.RawMessage(`{"role":"user","content":"Read story.txt and describe it"}`)},
		"You are helpful.",
		4096,
		emit,
	)

	if err != nil {
		t.Fatalf("run tool loop: %v", err)
	}

	// VAL-LLM-011: Final response incorporates the tool result.
	if text != "The file contains a pangram: The quick brown fox jumps over the lazy dog." {
		t.Errorf("text: got %q", text)
	}

	// VAL-LLM-011: Tool result was fed back to the LLM in conversation.
	// The second CallWithTools should have received a conversation that
	// includes the tool result message.
	if len(capturedMessages) < 3 {
		t.Fatalf("expected at least 3 messages (initial + assistant tool_use + user tool_result), got %d", len(capturedMessages))
	}

	// The third message (index 2) should be the tool_result message.
	toolResultMsg := capturedMessages[2]
	var msg struct {
		Role    string `json:"role"`
		Content []struct {
			Type      string `json:"type"`
			ToolUseID string `json:"tool_use_id"`
			Content   string `json:"content"`
		} `json:"content"`
	}
	if err := json.Unmarshal(toolResultMsg, &msg); err != nil {
		t.Fatalf("unmarshal tool result message: %v", err)
	}

	if msg.Role != "user" {
		t.Errorf("tool result message role: got %q, want user", msg.Role)
	}
	if len(msg.Content) == 0 {
		t.Fatal("tool result message should have content blocks")
	}
	if msg.Content[0].Type != "tool_result" {
		t.Errorf("content type: got %q, want tool_result", msg.Content[0].Type)
	}
	if msg.Content[0].Content != expectedContent {
		t.Errorf("tool result content: got %q, want %q", msg.Content[0].Content, expectedContent)
	}
}

// TestToolLoopMultiToolSequential validates VAL-LLM-012: A complex task
// requiring multiple sequential tool calls executes all tools correctly.
func TestToolLoopMultiToolSequential(t *testing.T) {
	tmpDir := t.TempDir()

	// Create multiple test files.
	fileA := filepath.Join(tmpDir, "intro.txt")
	fileB := filepath.Join(tmpDir, "details.txt")
	if err := os.WriteFile(fileA, []byte("Introduction: This is a test document."), 0o644); err != nil {
		t.Fatalf("write file A: %v", err)
	}
	if err := os.WriteFile(fileB, []byte("Details: The system has 3 components."), 0o644); err != nil {
		t.Fatalf("write file B: %v", err)
	}

	registry := NewToolRegistry()
	if err := registry.Register(fileReadTool(tmpDir)); err != nil {
		t.Fatalf("register: %v", err)
	}

	// Provider makes two sequential tool calls then summarizes.
	provider := newMockToolLoopProvider(
		// First response: read intro.txt.
		&ToolLoopResponse{
			StopReason: "tool_use",
			ToolCalls: []types.ToolCall{
				{
					ID:        "call-multi-1",
					Name:      "file_read",
					Arguments: json.RawMessage(`{"path":"intro.txt"}`),
				},
			},
			Usage: TokenUsage{InputTokens: 25, OutputTokens: 10},
			Model: "test-model",
		},
		// Second response: read details.txt.
		&ToolLoopResponse{
			StopReason: "tool_use",
			ToolCalls: []types.ToolCall{
				{
					ID:        "call-multi-2",
					Name:      "file_read",
					Arguments: json.RawMessage(`{"path":"details.txt"}`),
				},
			},
			Usage: TokenUsage{InputTokens: 40, OutputTokens: 10},
			Model: "test-model",
		},
		// Third response: final summary referencing both files.
		&ToolLoopResponse{
			StopReason: "end_turn",
			Text:       "Summary: This is a test document. The system has 3 components.",
			Usage:      TokenUsage{InputTokens: 60, OutputTokens: 20},
			Model:      "test-model",
		},
	)

	var emittedKinds []types.EventKind
	emit := func(kind types.EventKind, phase string, payload json.RawMessage) {
		emittedKinds = append(emittedKinds, kind)
	}

	text, usage, err := RunToolLoop(
		context.Background(),
		provider,
		registry,
		[]json.RawMessage{json.RawMessage(`{"role":"user","content":"Read intro.txt and details.txt, then summarize"}`)},
		"You are helpful.",
		4096,
		emit,
	)

	if err != nil {
		t.Fatalf("run tool loop: %v", err)
	}

	// VAL-LLM-012: Final response references all tool results.
	if text != "Summary: This is a test document. The system has 3 components." {
		t.Errorf("text: got %q", text)
	}

	// VAL-LLM-012: Three LLM calls were made (2 tool_use + 1 end_turn).
	if provider.CallCount() != 3 {
		t.Errorf("call count: got %d, want 3", provider.CallCount())
	}

	// Token usage should accumulate across all 3 iterations.
	if usage.InputTokens != 125 {
		t.Errorf("total input tokens: got %d, want 125", usage.InputTokens)
	}
	if usage.OutputTokens != 40 {
		t.Errorf("total output tokens: got %d, want 40", usage.OutputTokens)
	}

	// Should have 2 tool.invoked and 2 tool.result events (one per tool call).
	invokedCount := 0
	resultCount := 0
	for _, k := range emittedKinds {
		if k == types.EventToolInvoked {
			invokedCount++
		}
		if k == types.EventToolResult {
			resultCount++
		}
	}
	if invokedCount != 2 {
		t.Errorf("tool.invoked events: got %d, want 2", invokedCount)
	}
	if resultCount != 2 {
		t.Errorf("tool.result events: got %d, want 2", resultCount)
	}
}

// TestToolLoopEndToEndWithRuntime validates the complete integration:
// StartRun → tool_use → file_read execution → tool result → final response.
// This is the full end-to-end validation that the feature requires.
func TestToolLoopEndToEndWithRuntime(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a file that the tool will read.
	testContent := "ChoirOS Sandbox Runtime v1.0\nBuild: 2024-04-12\nFeatures: tools, streaming"
	testFile := filepath.Join(tmpDir, "version.txt")
	if err := os.WriteFile(testFile, []byte(testContent), 0o644); err != nil {
		t.Fatalf("write version file: %v", err)
	}

	registry := NewToolRegistry()
	if err := registry.Register(fileReadTool(tmpDir)); err != nil {
		t.Fatalf("register file_read: %v", err)
	}

	// Provider simulates: read file → summarize.
	provider := newMockToolLoopProvider(
		&ToolLoopResponse{
			StopReason: "tool_use",
			ToolCalls: []types.ToolCall{
				{
					ID:        "call-e2e-1",
					Name:      "file_read",
					Arguments: json.RawMessage(`{"path":"version.txt"}`),
				},
			},
			Usage: TokenUsage{InputTokens: 30, OutputTokens: 15},
			Model: "test-model",
		},
		&ToolLoopResponse{
			StopReason: "end_turn",
			Text:       "Based on the version file, this is ChoirOS Sandbox Runtime v1.0, built on 2024-04-12, with tools and streaming features.",
			Usage:      TokenUsage{InputTokens: 60, OutputTokens: 25},
			Model:      "test-model",
		},
	)

	rt, s := testRuntimeWithProviderAndRegistry(t, provider, registry)
	defer rt.Stop()

	// Submit the task.
	rec, err := rt.StartRun(context.Background(),
		"Read file version.txt and summarize what you find", "user-e2e-test")
	if err != nil {
		t.Fatalf("submit task: %v", err)
	}

	// Wait for completion.
	waitForToolLoopTask(t, s, rec.RunID, 5*time.Second)

	// Fetch and validate the task.
	fetched, err := s.GetRun(context.Background(), rec.RunID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}

	if fetched.State != types.RunCompleted {
		t.Fatalf("task state: got %q, want completed (error: %s)", fetched.State, fetched.Error)
	}

	// The result should reference the file content.
	expectedResult := "Based on the version file, this is ChoirOS Sandbox Runtime v1.0, built on 2024-04-12, with tools and streaming features."
	if fetched.Result != expectedResult {
		t.Errorf("result:\ngot:  %q\nwant: %q", fetched.Result, expectedResult)
	}

	// Validate all expected events were emitted.
	evts, err := s.ListEvents(context.Background(), rec.RunID, 100)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}

	expectedKinds := map[types.EventKind]bool{
		types.EventRunSubmitted: false,
		types.EventRunStarted:   false,
		types.EventToolInvoked:   false,
		types.EventToolResult:    false,
		types.EventRunProgress:  false,
		types.EventRunCompleted: false,
	}

	for _, ev := range evts {
		if _, ok := expectedKinds[ev.Kind]; ok {
			expectedKinds[ev.Kind] = true
		}
	}

	for kind, found := range expectedKinds {
		if !found {
			t.Errorf("missing expected event kind: %s", kind)
		}
	}

	// Verify metadata has token usage.
	if fetched.Metadata["input_tokens"] == nil {
		t.Error("metadata should contain input_tokens")
	}
	if fetched.Metadata["output_tokens"] == nil {
		t.Error("metadata should contain output_tokens")
	}
}

// TestToolLoopWithMultipleToolsRegistered validates that multiple tools
// can be registered and the LLM can invoke any of them.
func TestToolLoopWithMultipleToolsRegistered(t *testing.T) {
	tmpDir := t.TempDir()

	registry := NewToolRegistry()

	// Register file_read tool.
	if err := registry.Register(fileReadTool(tmpDir)); err != nil {
		t.Fatalf("register file_read: %v", err)
	}

	// Register a second tool (simple echo tool).
	if err := registry.Register(Tool{
		Name:        "echo",
		Description: "Echo back the input text",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"text": map[string]any{"type": "string"},
			},
			"required": []string{"text"},
		},
		Func: func(ctx context.Context, args json.RawMessage) (string, error) {
			var params struct {
				Text string `json:"text"`
			}
			json.Unmarshal(args, &params)
			return params.Text, nil
		},
	}); err != nil {
		t.Fatalf("register echo: %v", err)
	}

	if registry.Size() != 2 {
		t.Errorf("registry size: got %d, want 2", registry.Size())
	}

	// Both tools should be in definitions.
	defs := registry.Definitions()
	if len(defs) != 2 {
		t.Fatalf("definitions: got %d, want 2", len(defs))
	}

	// Provider invokes both tools in one turn.
	provider := newMockToolLoopProvider(
		&ToolLoopResponse{
			StopReason: "tool_use",
			ToolCalls: []types.ToolCall{
				{
					ID:        "call-multi-tool-1",
					Name:      "echo",
					Arguments: json.RawMessage(`{"text":"Hello world"}`),
				},
			},
			Usage: TokenUsage{InputTokens: 20, OutputTokens: 10},
			Model: "test-model",
		},
		&ToolLoopResponse{
			StopReason: "end_turn",
			Text:       "I echoed: Hello world",
			Usage:      TokenUsage{InputTokens: 30, OutputTokens: 10},
			Model:      "test-model",
		},
	)

	text, _, err := RunToolLoop(
		context.Background(),
		provider,
		registry,
		[]json.RawMessage{json.RawMessage(`{"role":"user","content":"Use the echo tool"}`)},
		"You are helpful.",
		4096,
		func(kind types.EventKind, phase string, payload json.RawMessage) {},
	)

	if err != nil {
		t.Fatalf("run tool loop: %v", err)
	}
	if text != "I echoed: Hello world" {
		t.Errorf("text: got %q", text)
	}
}

// --- Helper types ---

// messageCapturingProvider is a mock ToolLoopProvider that captures all
// messages sent to CallWithTools for verification.
type messageCapturingProvider struct {
	Provider
	responses        []*ToolLoopResponse
	capturedMessages *[]json.RawMessage
	callIdx          int
}

func (m *messageCapturingProvider) CallWithTools(ctx context.Context, req ToolLoopRequest) (*ToolLoopResponse, error) {
	// Capture the messages.
	if m.capturedMessages != nil {
		*m.capturedMessages = req.Messages
	}

	idx := m.callIdx
	m.callIdx++
	if idx >= len(m.responses) {
		idx = len(m.responses) - 1
	}
	if idx < 0 {
		return nil, fmt.Errorf("no responses configured")
	}
	return m.responses[idx], nil
}

// waitForToolLoopTask polls the store until the task reaches a terminal state.
func waitForToolLoopTask(t *testing.T, s *store.Store, taskID string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		rec, err := s.GetRun(context.Background(), taskID)
		if err != nil {
			t.Fatalf("get task during wait: %v", err)
		}
		if rec.State.Terminal() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("task %s did not complete within %v", taskID, timeout)
}
