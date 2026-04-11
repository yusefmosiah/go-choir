package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/yusefmosiah/go-choir/internal/types"
)

// ToolFunc is the execution contract for in-process tools. Tools are Go
// function calls, not CLI subprocesses (mission constraint: no CLI loop).
// The function receives the raw JSON arguments from the provider and returns
// a text result or an error.
type ToolFunc func(ctx context.Context, args json.RawMessage) (string, error)

// Tool describes a callable tool plus its LLM-facing schema metadata.
// Adapted from Cogent's Tool struct but simplified for go-choir: no core/tool
// distinction, no Anthropic/OpenAI schema variants (those belong in the
// provider bridge), and no native-session profile tracking.
type Tool struct {
	// Name is the unique tool identifier used in LLM tool_use responses.
	Name string `json:"name"`

	// Description is a human-readable summary of what the tool does,
	// included in the system prompt for LLM tool discovery.
	Description string `json:"description,omitempty"`

	// Parameters is the JSON Schema object describing the tool's input
	// parameters. If nil, defaults to an empty object schema.
	Parameters map[string]any `json:"parameters,omitempty"`

	// Func is the Go function that executes the tool. Must be non-nil.
	Func ToolFunc `json:"-"`
}

// Validate checks that the tool has a name and a non-nil function.
func (t Tool) Validate() error {
	if t.Name == "" {
		return fmt.Errorf("tool name must not be empty")
	}
	if t.Func == nil {
		return fmt.Errorf("tool %q has nil func", t.Name)
	}
	return nil
}

// ToolDefinition is the LLM-facing schema for a tool, without the Go
// function. This is what gets included in API requests and system prompts.
type ToolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

// Definition returns the LLM-facing definition for this tool.
func (t Tool) Definition() ToolDefinition {
	return ToolDefinition{
		Name:        t.Name,
		Description: t.Description,
		Parameters:  cloneSchemaMap(t.Parameters),
	}
}

// ToolRegistry manages the set of available tools for the runtime loop.
// Tools are registered once at startup and looked up by name during the
// tool-calling loop when the LLM returns tool_use stop reasons.
//
// Adapted from Cogent's ToolRegistry but simplified:
//   - No core/activated tool distinction (go-choir sends all tool schemas
//     up front; LLM tool discovery happens through the system prompt catalog).
//   - No Anthropic/OpenAI schema methods (those belong in the provider bridge).
//   - Thread-safe for concurrent lookup during parallel tool execution.
type ToolRegistry struct {
	mu    sync.RWMutex
	tools map[string]Tool
	order []string // sorted names for deterministic catalog output
}

// NewToolRegistry creates an empty tool registry.
func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{
		tools: make(map[string]Tool),
	}
}

// NewToolRegistryWithTools creates a tool registry with the given tools
// pre-registered. Returns an error if any tool fails validation.
func NewToolRegistryWithTools(tools ...Tool) (*ToolRegistry, error) {
	r := NewToolRegistry()
	for _, tool := range tools {
		if err := r.Register(tool); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// MustNewToolRegistry creates a tool registry with the given tools or panics.
func MustNewToolRegistry(tools ...Tool) *ToolRegistry {
	r, err := NewToolRegistryWithTools(tools...)
	if err != nil {
		panic(err)
	}
	return r
}

// Register adds a tool to the registry. Returns an error if the tool fails
// validation or a tool with the same name is already registered.
func (r *ToolRegistry) Register(tool Tool) error {
	if err := tool.Validate(); err != nil {
		return err
	}

	// Default to empty object schema if no parameters specified.
	if len(tool.Parameters) == 0 {
		tool.Parameters = jsonSchemaObject(nil, nil, false)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.tools[tool.Name]; exists {
		return fmt.Errorf("tool %q already registered", tool.Name)
	}
	r.tools[tool.Name] = tool
	r.order = append(r.order, tool.Name)
	sort.Strings(r.order)
	return nil
}

// Lookup returns the tool with the given name, or false if not found.
func (r *ToolRegistry) Lookup(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	tool, ok := r.tools[name]
	return tool, ok
}

// Execute runs the named tool with the given arguments. Returns an error
// if the tool is not found or if execution fails.
func (r *ToolRegistry) Execute(ctx context.Context, name string, args json.RawMessage) (string, error) {
	r.mu.RLock()
	tool, ok := r.tools[name]
	r.mu.RUnlock()

	if !ok {
		return "", fmt.Errorf("tool %q not found", name)
	}
	return tool.Func(ctx, args)
}

// Tools returns all registered tools in sorted order.
func (r *ToolRegistry) Tools() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]Tool, 0, len(r.order))
	for _, name := range r.order {
		out = append(out, r.tools[name])
	}
	return out
}

// Definitions returns the LLM-facing definitions for all registered tools.
func (r *ToolRegistry) Definitions() []ToolDefinition {
	tools := r.Tools()
	out := make([]ToolDefinition, 0, len(tools))
	for _, tool := range tools {
		out = append(out, tool.Definition())
	}
	return out
}

// Catalog returns a compact one-line-per-tool description suitable for
// inclusion in the system prompt. The LLM reads this to know what tools
// are available and calls them by name. Adapted from Cogent's Catalog()
// but without the core/activated distinction.
func (r *ToolRegistry) Catalog() string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var b strings.Builder
	b.WriteString("Available tools:\n")
	for _, name := range r.order {
		tool := r.tools[name]
		desc := tool.Description
		if len(desc) > 80 {
			desc = desc[:80] + "..."
		}
		fmt.Fprintf(&b, "- %s — %s\n", name, desc)
	}
	return b.String()
}

// Size returns the number of registered tools.
func (r *ToolRegistry) Size() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.tools)
}

// --- Schema helpers ---

// jsonSchemaObject creates a JSON Schema object with the given properties,
// required fields, and additionalProperties setting.
func jsonSchemaObject(properties map[string]any, required []string, additionalProperties bool) map[string]any {
	if properties == nil {
		properties = map[string]any{}
	}
	schema := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": additionalProperties,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

// cloneSchemaMap deep-clones a JSON Schema map.
func cloneSchemaMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = cloneSchemaValue(v)
	}
	return out
}

func cloneSchemaValue(v any) any {
	switch vv := v.(type) {
	case map[string]any:
		return cloneSchemaMap(vv)
	case []any:
		out := make([]any, len(vv))
		for i, item := range vv {
			out[i] = cloneSchemaValue(item)
		}
		return out
	default:
		return v
	}
}

// buildSystemPromptWithTools constructs the system prompt for the tool-calling
// loop by appending the tool catalog to the base system prompt. This gives
// the LLM visibility into available tools without requiring separate tool
// schema negotiation on each turn.
func buildSystemPromptWithTools(basePrompt string, registry *ToolRegistry) string {
	if registry == nil || registry.Size() == 0 {
		return basePrompt
	}
	return basePrompt + "\n\n" + registry.Catalog()
}

// executeTools runs a batch of tool calls from the LLM response in parallel,
// emitting events for each invocation. Returns the tool results for feeding
// back into the LLM conversation. Adapted from Cogent's executeTools but
// simplified for go-choir: no steer draining, no consecutive-error tracking,
// and no tool activation (all tools are always available).
func executeTools(ctx context.Context, registry *ToolRegistry, calls []types.ToolCall, emit EventEmitFunc) []types.ToolResult {
	results := make([]types.ToolResult, len(calls))

	// Execute tool calls in parallel — results collected in order.
	var wg sync.WaitGroup
	for i, call := range calls {
		wg.Add(1)
		go func(idx int, c types.ToolCall) {
			defer wg.Done()

			// Emit tool.invoked event before execution.
			invokedPayload, _ := json.Marshal(map[string]string{
				"tool":   c.Name,
				"call_id": c.ID,
			})
			emit(types.EventToolInvoked, "tool_call", invokedPayload)

			output, err := registry.Execute(ctx, c.Name, c.Arguments)
			isError := false
			if err != nil {
				output = fmt.Sprintf("tool_error: %v", err)
				isError = true
			}

			// Cap tool output to prevent context overflow.
			const maxToolOutput = 100 * 1024 // 100KB
			if len(output) > maxToolOutput {
				output = output[:maxToolOutput] + fmt.Sprintf(
					"\n\n[output truncated — %d bytes total, showing first %d bytes]",
					len(output), maxToolOutput)
			}

			// Emit tool.result event after execution.
			resultPayload, _ := json.Marshal(map[string]any{
				"tool":    c.Name,
				"call_id": c.ID,
				"is_error": isError,
				"output_len": len(output),
			})
			emit(types.EventToolResult, "tool_call", resultPayload)

			results[idx] = types.ToolResult{
				CallID:  c.ID,
				Output:  output,
				IsError: isError,
			}
		}(i, call)
	}
	wg.Wait()

	return results
}
