// Package provider implements real LLM provider bridges for the go-choir
// sandbox runtime. It supports Bedrock (Anthropic Messages API over AWS
// Bedrock invoke endpoint), Z.AI (Anthropic-compatible API), and Fireworks AI
// (Anthropic-compatible API) as the first required real-provider paths for
// Mission 3.
//
// Design decisions:
//   - Provider credentials are read from environment variables only; they are
//     never committed to the repo, baked into Nix store, or exposed to the
//     browser/guest.
//   - The Provider interface is consumed by the runtime engine; the runtime
//     does not know which specific backend is active.
//   - All provider interactions are logged with redacted credentials so
//     operators can distinguish real upstream work from canned stub responses.
//   - Bedrock uses Bearer auth (AWS_BEARER_TOKEN_BEDROCK) rather than SigV4,
//     matching the pattern established in choiros-rs.
//   - Z.AI uses an Anthropic-compatible API at https://api.z.ai/api/anthropic
//     with bearer auth via ZAI_API_KEY.
//   - Fireworks AI uses an Anthropic-compatible API at
//     https://api.fireworks.ai/inference with bearer auth via FIREWORKS_API_KEY.
package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// LLMRequest is the unified request shape for all provider backends.
type LLMRequest struct {
	// Model is the model identifier (provider-specific).
	Model string `json:"model"`

	// System is the optional system prompt.
	System string `json:"system,omitempty"`

	// Messages is the conversation history in Anthropic Messages format.
	Messages []Message `json:"messages"`

	// Tools is the list of tool definitions to include in the request.
	// When non-empty, the provider may return tool_use content blocks.
	Tools []ToolDef `json:"tools,omitempty"`

	// MaxTokens is the maximum number of output tokens.
	MaxTokens int `json:"max_tokens"`

	// Stream controls whether to use streaming (SSE) or non-streaming.
	// Bedrock forces this to false because it uses binary EventStream.
	Stream bool `json:"stream,omitempty"`
}

// Message is a single message in the conversation history.
type Message struct {
	Role    string  `json:"role"`
	Content []Block `json:"content"`
}

// ToolDef is a tool definition for inclusion in LLM requests. The
// provider package uses this instead of importing from the runtime
// package to avoid circular dependencies.
type ToolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema,omitempty"` // Anthropic uses "input_schema"
}

// Block is a content block within a message. It supports all Anthropic
// Messages API content block types: text, tool_use, and tool_result.
// The fields are structured so that each block type uses its relevant
// subset:
//   - text: Type="text", Text=content
//   - tool_use: Type="tool_use", ID=call_id, Name=tool_name, Input=args
//   - tool_result: Type="tool_result", ToolUseID=call_id, Text=result_content
type Block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`         // tool_use: provider-assigned call ID
	Name      string          `json:"name,omitempty"`       // tool_use: tool name to invoke
	Input     json.RawMessage `json:"input,omitempty"`      // tool_use: tool arguments as JSON
	ToolUseID string          `json:"tool_use_id,omitempty"` // tool_result: ID of the originating tool call
	IsError   bool            `json:"is_error,omitempty"`   // tool_result: true if result is an error
}

// LLMResponse is the unified response shape from all provider backends.
type LLMResponse struct {
	// ID is the provider-assigned response identifier.
	ID string `json:"id"`

	// Text is the concatenated text content from the response.
	Text string `json:"text"`

	// Model is the model that produced the response (may differ from request
	// if the provider resolved an alias).
	Model string `json:"model"`

	// StopReason is why the model stopped generating.
	StopReason string `json:"stop_reason"`

	// Usage contains token usage information.
	Usage Usage `json:"usage"`

	// ToolCalls contains structured tool invocation requests extracted from
	// tool_use content blocks in the provider response. Non-empty only when
	// StopReason is "tool_use". This field bridges the gap between the
	// Anthropic Messages API response format (where tool calls appear as
	// content blocks with type "tool_use") and the runtime's ToolCall type.
	ToolCalls []ContentToolCall `json:"tool_calls,omitempty"`

	// ProviderName identifies which provider handled this request, for
	// observability and redacted logging.
	ProviderName string `json:"provider_name"`
}

// Usage tracks token counts for a provider response.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// ContentToolCall represents a tool invocation extracted from a tool_use
// content block in an Anthropic Messages API response. This is the
// provider-package-level representation; the bridge layer converts these
// into runtime.ToolCall values consumed by the tool-calling loop.
type ContentToolCall struct {
	// ID is the provider-assigned tool call identifier (e.g., "toolu_01").
	ID string `json:"id"`

	// Name is the tool name to invoke (must match a registered tool).
	Name string `json:"name"`

	// Arguments is the raw JSON arguments object for the tool call.
	Arguments json.RawMessage `json:"arguments"`
}

// Provider is the interface for executing LLM requests. Implementations
// must not leak credentials in error messages or logs.
type Provider interface {
	// Call executes the LLM request and returns the response.
	// Implementations should return structured errors that the runtime
	// can surface as task failures without crashing (VAL-RUNTIME-008).
	Call(ctx context.Context, req LLMRequest) (*LLMResponse, error)

	// Name returns the provider name for observability.
	Name() string

	// IsReal returns true for providers that reach a real upstream
	// backend, as opposed to stub/canned providers.
	IsReal() bool
}

// BedrockProvider implements the Provider interface for AWS Bedrock using
// the Anthropic Messages API format over the Bedrock invoke endpoint.
// Auth uses a bearer identity token (AWS_BEARER_TOKEN_BEDROCK) rather
// than SigV4 signing, matching the pattern from choiros-rs.
type BedrockProvider struct {
	region      string
	modelID     string
	authToken   string // loaded at init time, never logged
	httpClient  *http.Client
	anthropicV  string // anthropic version header
}

// BedrockConfig holds configuration for creating a BedrockProvider.
type BedrockConfig struct {
	Region    string
	ModelID   string
	AuthToken string
}

// NewBedrockProvider creates a Bedrock provider from the given config.
// Returns an error if required fields are missing.
func NewBedrockProvider(cfg BedrockConfig) (*BedrockProvider, error) {
	if cfg.Region == "" {
		return nil, fmt.Errorf("bedrock provider requires region")
	}
	if cfg.ModelID == "" {
		return nil, fmt.Errorf("bedrock provider requires model_id")
	}
	if cfg.AuthToken == "" {
		return nil, fmt.Errorf("bedrock provider requires auth token")
	}

	return &BedrockProvider{
		region:     cfg.Region,
		modelID:   cfg.ModelID,
		authToken:  cfg.AuthToken,
		httpClient: &http.Client{Timeout: 120 * time.Second},
		anthropicV: "bedrock-2023-05-31",
	}, nil
}

// NewBedrockProviderFromEnv creates a Bedrock provider from environment
// variables: AWS_REGION, AWS_BEARER_TOKEN_BEDROCK, and RUNTIME_BEDROCK_MODEL.
func NewBedrockProviderFromEnv() (*BedrockProvider, error) {
	region := os.Getenv("AWS_REGION")
	token := os.Getenv("AWS_BEARER_TOKEN_BEDROCK")
	modelID := os.Getenv("RUNTIME_BEDROCK_MODEL")
	if modelID == "" {
		modelID = "us.anthropic.claude-sonnet-4-5-20250514-v1:0"
	}

	p, err := NewBedrockProvider(BedrockConfig{
		Region:    region,
		ModelID:   modelID,
		AuthToken: token,
	})
	if err != nil {
		return nil, fmt.Errorf("bedrock from env: %w", err)
	}
	return p, nil
}

func (p *BedrockProvider) Name() string { return "bedrock" }
func (p *BedrockProvider) IsReal() bool { return true }

// Call sends the request to Bedrock's invoke endpoint.
// Bedrock uses the model ID in the URL path and forces non-streaming
// because streaming uses binary EventStream, not SSE.
func (p *BedrockProvider) Call(ctx context.Context, req LLMRequest) (*LLMResponse, error) {
	endpoint := fmt.Sprintf(
		"https://bedrock-runtime.%s.amazonaws.com/model/%s/invoke",
		p.region, pathEscape(p.modelID),
	)

	// Build Anthropic Messages API request body.
	body := p.buildRequestBody(req)

	httpReq, err := newJSONRequest(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return nil, fmt.Errorf("bedrock: build request: %w", err)
	}

	httpReq.Header.Set("Authorization", "Bearer "+p.authToken)
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("anthropic-version", p.anthropicV)

	log.Printf("provider: bedrock call model=%s region=%s", redactModel(p.modelID), p.region)

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("bedrock: http call: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	return parseBedrockResponse(resp, p.modelID)
}

func (p *BedrockProvider) buildRequestBody(req LLMRequest) anthropicRequest {
	ar := anthropicRequest{
		MaxTokens:        defaultMaxTokens(req.MaxTokens),
		AnthropicVersion: p.anthropicV,
	}

	// Bedrock: system prompt with cache_control for prompt caching.
	if req.System != "" {
		ar.System = []anthropicSystemBlock{{
			Type:         "text",
			Text:         req.System,
			CacheControl: map[string]string{"type": "ephemeral"},
		}}
	}

	ar.Messages = convertMessages(req.Messages)
	ar.Tools = convertToolDefs(req.Tools)
	return ar
}

// ZAIProvider implements the Provider interface for Z.AI using the
// Anthropic-compatible API at https://api.z.ai/api/anthropic.
type ZAIProvider struct {
	apiKey     string // loaded at init time, never logged
	modelID   string
	httpClient *http.Client
	baseURL    string
}

// ZAIConfig holds configuration for creating a ZAIProvider.
type ZAIConfig struct {
	APIKey  string
	ModelID string
	BaseURL string // defaults to https://api.z.ai/api/anthropic
}

// NewZAIProvider creates a Z.AI provider from the given config.
func NewZAIProvider(cfg ZAIConfig) (*ZAIProvider, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("zai provider requires api key")
	}
	if cfg.ModelID == "" {
		return nil, fmt.Errorf("zai provider requires model_id")
	}

	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://api.z.ai/api/anthropic"
	}

	return &ZAIProvider{
		apiKey:     cfg.APIKey,
		modelID:   cfg.ModelID,
		httpClient: &http.Client{Timeout: 120 * time.Second},
		baseURL:   strings.TrimRight(baseURL, "/"),
	}, nil
}

// NewZAIProviderFromEnv creates a Z.AI provider from environment variables:
// ZAI_API_KEY and RUNTIME_ZAI_MODEL.
func NewZAIProviderFromEnv() (*ZAIProvider, error) {
	apiKey := os.Getenv("ZAI_API_KEY")
	modelID := os.Getenv("RUNTIME_ZAI_MODEL")
	if modelID == "" {
		modelID = "glm-4.7"
	}

	p, err := NewZAIProvider(ZAIConfig{
		APIKey:  apiKey,
		ModelID: modelID,
	})
	if err != nil {
		return nil, fmt.Errorf("zai from env: %w", err)
	}
	return p, nil
}

func (p *ZAIProvider) Name() string { return "zai" }
func (p *ZAIProvider) IsReal() bool { return true }

// Call sends the request to Z.AI's Anthropic-compatible endpoint.
func (p *ZAIProvider) Call(ctx context.Context, req LLMRequest) (*LLMResponse, error) {
	endpoint := p.baseURL + "/v1/messages"

	body := p.buildRequestBody(req)

	httpReq, err := newJSONRequest(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return nil, fmt.Errorf("zai: build request: %w", err)
	}

	httpReq.Header.Set("x-api-key", p.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")

	log.Printf("provider: zai call model=%s", p.modelID)

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("zai: http call: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	return parseAnthropicResponse(resp, p.modelID, "zai")
}

func (p *ZAIProvider) buildRequestBody(req LLMRequest) anthropicRequest {
	ar := anthropicRequest{
		Model:            p.modelID,
		MaxTokens:        defaultMaxTokens(req.MaxTokens),
		AnthropicVersion: "2023-06-01",
		Stream:           false,
	}

	if req.System != "" {
		ar.System = []anthropicSystemBlock{{
			Type: "text",
			Text: req.System,
		}}
	}

	ar.Messages = convertMessages(req.Messages)
	ar.Tools = convertToolDefs(req.Tools)
	return ar
}

// FireworksProvider implements the Provider interface for Fireworks AI
// using the Anthropic-compatible API at https://api.fireworks.ai/inference.
type FireworksProvider struct {
	apiKey     string // loaded at init time, never logged
	modelID   string
	httpClient *http.Client
	baseURL    string
}

// FireworksConfig holds configuration for creating a FireworksProvider.
type FireworksConfig struct {
	APIKey  string
	ModelID string
	BaseURL string // defaults to https://api.fireworks.ai/inference
}

// NewFireworksProvider creates a Fireworks provider from the given config.
func NewFireworksProvider(cfg FireworksConfig) (*FireworksProvider, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("fireworks provider requires api key")
	}
	if cfg.ModelID == "" {
		return nil, fmt.Errorf("fireworks provider requires model_id")
	}

	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://api.fireworks.ai/inference"
	}

	return &FireworksProvider{
		apiKey:     cfg.APIKey,
		modelID:   cfg.ModelID,
		httpClient: &http.Client{Timeout: 120 * time.Second},
		baseURL:   strings.TrimRight(baseURL, "/"),
	}, nil
}

// NewFireworksProviderFromEnv creates a Fireworks provider from environment
// variables: FIREWORKS_API_KEY, RUNTIME_FIREWORKS_MODEL, and optionally
// FIREWORKS_BASE_URL.
func NewFireworksProviderFromEnv() (*FireworksProvider, error) {
	apiKey := os.Getenv("FIREWORKS_API_KEY")
	modelID := os.Getenv("RUNTIME_FIREWORKS_MODEL")
	if modelID == "" {
		modelID = "accounts/fireworks/models/llama4-maverick-instruct-basic"
	}
	baseURL := os.Getenv("FIREWORKS_BASE_URL")

	p, err := NewFireworksProvider(FireworksConfig{
		APIKey:  apiKey,
		ModelID: modelID,
		BaseURL: baseURL,
	})
	if err != nil {
		return nil, fmt.Errorf("fireworks from env: %w", err)
	}
	return p, nil
}

func (p *FireworksProvider) Name() string { return "fireworks" }
func (p *FireworksProvider) IsReal() bool { return true }

// Call sends the request to Fireworks AI's Anthropic-compatible endpoint.
func (p *FireworksProvider) Call(ctx context.Context, req LLMRequest) (*LLMResponse, error) {
	endpoint := p.baseURL + "/v1/messages"

	body := p.buildRequestBody(req)

	httpReq, err := newJSONRequest(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return nil, fmt.Errorf("fireworks: build request: %w", err)
	}

	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")

	log.Printf("provider: fireworks call model=%s", p.modelID)

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("fireworks: http call: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	return parseAnthropicResponse(resp, p.modelID, "fireworks")
}

func (p *FireworksProvider) buildRequestBody(req LLMRequest) anthropicRequest {
	ar := anthropicRequest{
		Model:            p.modelID,
		MaxTokens:        defaultMaxTokens(req.MaxTokens),
		AnthropicVersion: "2023-06-01",
		Stream:           false,
	}

	if req.System != "" {
		ar.System = []anthropicSystemBlock{{
			Type: "text",
			Text: req.System,
		}}
	}

	ar.Messages = convertMessages(req.Messages)
	ar.Tools = convertToolDefs(req.Tools)
	return ar
}

// --- Shared types and helpers ---

type anthropicRequest struct {
	Model            string                 `json:"model,omitempty"` // empty for Bedrock (model in URL)
	System           any                    `json:"system,omitempty"`
	Messages         []anthropicMessage     `json:"messages"`
	Tools            []anthropicTool        `json:"tools,omitempty"`
	MaxTokens        int                    `json:"max_tokens"`
	Stream           bool                   `json:"stream,omitempty"`
	AnthropicVersion string                 `json:"anthropic_version,omitempty"`
}

type anthropicSystemBlock struct {
	Type         string            `json:"type"`
	Text         string            `json:"text"`
	CacheControl map[string]string `json:"cache_control,omitempty"`
}

// anthropicTool is the tool definition format for the Anthropic Messages API.
type anthropicTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema,omitempty"`
}

type anthropicMessage struct {
	Role    string              `json:"role"`
	Content []anthropicContent  `json:"content"`
}

type anthropicContent struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`          // tool_use: call ID
	Name      string          `json:"name,omitempty"`         // tool_use: tool name
	Input     json.RawMessage `json:"input,omitempty"`        // tool_use: arguments
	ToolUseID string          `json:"tool_use_id,omitempty"`  // tool_result: originating call ID
	Content   string          `json:"content,omitempty"`      // tool_result: result content (when string)
	IsError   bool            `json:"is_error,omitempty"`      // tool_result: error flag
}

type anthropicResponse struct {
	ID         string                   `json:"id"`
	Content    []anthropicResponseBlock `json:"content"`
	StopReason string                   `json:"stop_reason"`
	Usage      anthropicUsage           `json:"usage"`
	Model      string                   `json:"model,omitempty"`
}

type anthropicResponseBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text,omitempty"`
	ID    string          `json:"id,omitempty"`    // tool_use: provider-assigned call ID
	Name  string          `json:"name,omitempty"`  // tool_use: tool name
	Input json.RawMessage `json:"input,omitempty"` // tool_use: tool arguments
}

type anthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

func convertMessages(msgs []Message) []anthropicMessage {
	out := make([]anthropicMessage, 0, len(msgs))
	for _, msg := range msgs {
		item := anthropicMessage{
			Role:    msg.Role,
			Content: make([]anthropicContent, 0, len(msg.Content)),
		}
		for _, block := range msg.Content {
			ac := anthropicContent{
				Type:      block.Type,
				Text:      block.Text,
				ID:        block.ID,
				Name:      block.Name,
				Input:     block.Input,
				ToolUseID: block.ToolUseID,
				IsError:   block.IsError,
			}
			// For tool_result blocks, the result content goes in the
			// "content" field (not "text") per Anthropic Messages API.
			if block.Type == "tool_result" {
				ac.Content = block.Text
				ac.Text = "" // clear text field for tool_result
			}
			item.Content = append(item.Content, ac)
		}
		out = append(out, item)
	}
	return out
}

// convertToolDefs converts provider ToolDef values to the Anthropic
// tool definition format. Empty input returns nil (omitted from JSON).
func convertToolDefs(tools []ToolDef) []anthropicTool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]anthropicTool, 0, len(tools))
	for _, tool := range tools {
		out = append(out, anthropicTool(tool))
	}
	return out
}

func defaultMaxTokens(n int) int {
	if n <= 0 {
		return 4096
	}
	return n
}

func pathEscape(s string) string {
	return strings.ReplaceAll(s, "/", "~1")
}

func redactModel(modelID string) string {
	// Show first and last segment of Bedrock model IDs for logging.
	// Bedrock IDs look like "us.anthropic.claude-sonnet-4-5-20250514-v1:0"
	// which splits into 3 dot-separated parts: us, anthropic, <long model>.
	// We want to redact the middle portion of the model string.
	parts := strings.Split(modelID, ".")
	if len(parts) >= 3 {
		// Redact the middle of the last part (the actual model identifier).
		lastPart := parts[len(parts)-1]
		redacted := redactMiddle(lastPart)
		return parts[0] + "." + parts[1] + "." + redacted
	}
	if len(parts) == 2 {
		return parts[0] + ".***"
	}
	return modelID
}

// redactMiddle shows the first and last few characters of a string,
// replacing the middle with "***".
func redactMiddle(s string) string {
	if len(s) <= 8 {
		return "***"
	}
	return s[:4] + "***" + s[len(s)-4:]
}

func newJSONRequest(ctx context.Context, method, url string, body interface{}) (*http.Request, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request body: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, strings.NewReader(string(data)))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

func parseBedrockResponse(resp *http.Response, modelID string) (*LLMResponse, error) {
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Drain and discard the body to allow connection reuse, but do not
		// include it in the error (may contain provider details or credentials).
		_, _ = io.ReadAll(resp.Body)
		return nil, fmt.Errorf("bedrock: status %s (sanitized)", resp.Status)
	}

	var payload anthropicResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("bedrock: decode response: %w", err)
	}

	result := &LLMResponse{
		ID:          payload.ID,
		Model:       coalesce(payload.Model, modelID),
		StopReason:  payload.StopReason,
		Usage:       Usage{InputTokens: payload.Usage.InputTokens, OutputTokens: payload.Usage.OutputTokens},
		ProviderName: "bedrock",
	}

	for _, block := range payload.Content {
		switch block.Type {
		case "text":
			if block.Text != "" {
				result.Text += block.Text
			}
		case "tool_use":
			result.ToolCalls = append(result.ToolCalls, ContentToolCall{
				ID:        block.ID,
				Name:      block.Name,
				Arguments: canonicalJSON(block.Input),
			})
		}
	}

	log.Printf("provider: bedrock response model=%s stop=%s tokens_in=%d tokens_out=%d text_len=%d tool_calls=%d",
		redactModel(result.Model), result.StopReason,
		result.Usage.InputTokens, result.Usage.OutputTokens, len(result.Text), len(result.ToolCalls))

	return result, nil
}

func parseAnthropicResponse(resp *http.Response, modelID string, providerName string) (*LLMResponse, error) {
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Drain and discard the body to allow connection reuse, but do not
		// include it in the error (may contain provider details or credentials).
		_, _ = io.ReadAll(resp.Body)
		return nil, fmt.Errorf("%s: status %s (sanitized)", providerName, resp.Status)
	}

	var payload anthropicResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("%s: decode response: %w", providerName, err)
	}

	result := &LLMResponse{
		ID:           payload.ID,
		Model:        coalesce(payload.Model, modelID),
		StopReason:   payload.StopReason,
		Usage:        Usage{InputTokens: payload.Usage.InputTokens, OutputTokens: payload.Usage.OutputTokens},
		ProviderName: providerName,
	}

	for _, block := range payload.Content {
		switch block.Type {
		case "text":
			if block.Text != "" {
				result.Text += block.Text
			}
		case "tool_use":
			result.ToolCalls = append(result.ToolCalls, ContentToolCall{
				ID:        block.ID,
				Name:      block.Name,
				Arguments: canonicalJSON(block.Input),
			})
		}
	}

	log.Printf("provider: %s response model=%s stop=%s tokens_in=%d tokens_out=%d text_len=%d tool_calls=%d",
		providerName, result.Model, result.StopReason,
		result.Usage.InputTokens, result.Usage.OutputTokens, len(result.Text), len(result.ToolCalls))

	return result, nil
}

func coalesce(s, fallback string) string {
	if s != "" {
		return s
	}
	return fallback
}

// canonicalJSON normalizes a JSON raw message: trims whitespace,
// unmarshals and re-marshals to ensure consistent formatting, and
// returns "{}" for empty or null input. This matches the Cogent
// reference behavior for tool_use input arguments.
func canonicalJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage([]byte("{}"))
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return json.RawMessage([]byte("{}"))
	}
	var v any
	if err := json.Unmarshal(trimmed, &v); err != nil {
		return raw
	}
	b, err := json.Marshal(v)
	if err != nil {
		return raw
	}
	return b
}

// ResolveProvider creates the configured real provider from environment
// variables. It tries Bedrock first (if AWS_BEARER_TOKEN_BEDROCK is set),
// then Z.AI (if ZAI_API_KEY is set), then Fireworks (if FIREWORKS_API_KEY
// is set). If none is configured, it returns nil, indicating that the stub
// provider should be used instead.
//
// This preserves the later gateway boundary: the runtime does not directly
// expose provider credentials to the browser; it only uses them internally.
// The full gateway milestone will move this credential injection behind
// an explicit gateway service boundary.
func ResolveProvider() (Provider, error) {
	// Try Bedrock first.
	if os.Getenv("AWS_BEARER_TOKEN_BEDROCK") != "" {
		p, err := NewBedrockProviderFromEnv()
		if err != nil {
			return nil, fmt.Errorf("resolve provider: %w", err)
		}
		log.Printf("provider: resolved bedrock (region=%s model=%s)", p.region, redactModel(p.modelID))
		return p, nil
	}

	// Try Z.AI.
	if os.Getenv("ZAI_API_KEY") != "" {
		p, err := NewZAIProviderFromEnv()
		if err != nil {
			return nil, fmt.Errorf("resolve provider: %w", err)
		}
		log.Printf("provider: resolved zai (model=%s)", p.modelID)
		return p, nil
	}

	// Try Fireworks.
	if os.Getenv("FIREWORKS_API_KEY") != "" {
		p, err := NewFireworksProviderFromEnv()
		if err != nil {
			return nil, fmt.Errorf("resolve provider: %w", err)
		}
		log.Printf("provider: resolved fireworks (model=%s)", p.modelID)
		return p, nil
	}

	return nil, nil
}
