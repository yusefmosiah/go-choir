// Package provider implements real LLM provider bridges for the go-choir
// sandbox runtime. It supports Bedrock (Anthropic Messages API over AWS
// Bedrock invoke endpoint), Z.AI (Anthropic-compatible API), and Fireworks AI
// (Anthropic-compatible API) as the first required real-provider paths for
// Mission 3.
//
// Supported models (matching Droid settings.json customModels):
//   - GLM-5.1 (Z.AI, provider "zai") — default Z.AI model
//   - GLM-5-Turbo (Z.AI, provider "zai") — faster Z.AI variant, same provider
//   - Kimi K2.5 Turbo (Fireworks AI, provider "fireworks") — Fireworks router model
//   - Claude Sonnet 4.5 (Bedrock, provider "bedrock") — default Bedrock model
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
//   - MultiProvider holds multiple provider backends for the upcoming
//     multiagent system where multiple models run simultaneously. Model
//     routing is its own scope and not implemented here.
package provider

import (
	"bufio"
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
	ID        string          `json:"id,omitempty"`          // tool_use: provider-assigned call ID
	Name      string          `json:"name,omitempty"`        // tool_use: tool name to invoke
	Input     json.RawMessage `json:"input,omitempty"`       // tool_use: tool arguments as JSON
	ToolUseID string          `json:"tool_use_id,omitempty"` // tool_result: ID of the originating tool call
	IsError   bool            `json:"is_error,omitempty"`    // tool_result: true if result is an error
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

// StreamChunk represents an incremental SSE chunk from a streaming LLM
// response. The gateway and runtime use these to forward streaming data
// to callers in real-time.
type StreamChunk struct {
	// Type is the SSE event type (e.g., "content_block_delta", "message_start",
	// "message_delta", "message_stop").
	Type string `json:"type"`

	// Delta is the incremental text content from text_delta events.
	Delta string `json:"delta,omitempty"`

	// ToolCallDelta is partial JSON for tool_use input_json_delta events.
	ToolCallDelta string `json:"tool_call_delta,omitempty"`

	// ToolCallID is the tool call ID from content_block_start for tool_use blocks.
	ToolCallID string `json:"tool_call_id,omitempty"`

	// ToolCallName is the tool name from content_block_start for tool_use blocks.
	ToolCallName string `json:"tool_call_name,omitempty"`

	// StopReason is set on message_delta events (e.g., "end_turn", "tool_use").
	StopReason string `json:"stop_reason,omitempty"`

	// Usage is the cumulative token usage from message_delta events.
	Usage *StreamUsage `json:"usage,omitempty"`

	// Index is the content block index for multi-block responses.
	Index int `json:"index,omitempty"`

	// Model is set on message_start events.
	Model string `json:"model,omitempty"`

	// ID is the response ID from message_start events.
	ID string `json:"id,omitempty"`
}

// StreamUsage carries token counts for streaming responses.
type StreamUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Provider is the interface for executing LLM requests. Implementations
// must not leak credentials in error messages or logs.
type Provider interface {
	// Call executes the LLM request and returns the response.
	// Implementations should return structured errors that the runtime
	// can surface as task failures without crashing (VAL-RUNTIME-008).
	Call(ctx context.Context, req LLMRequest) (*LLMResponse, error)

	// Stream executes the LLM request with streaming (SSE) and calls
	// onChunk for each incremental chunk. If the provider does not support
	// streaming, it should fall back to Call and emit a single chunk.
	// Returns the accumulated LLMResponse on completion.
	Stream(ctx context.Context, req LLMRequest, onChunk func(StreamChunk)) (*LLMResponse, error)

	// Name returns the provider name for observability.
	Name() string

	// IsReal returns true for providers that reach a real upstream
	// backend, as opposed to stub/canned providers.
	IsReal() bool
}

// ProviderConfig holds runtime configuration for which model each provider
// backend should use. This is the configuration surface that the host-side
// caller (gateway, direct sandbox) uses to select models. It is NOT resolved
// from environment variables — model selection is a runtime concern, not an
// infrastructure one.
//
// Provider credentials (API keys, tokens) are still resolved from env vars
// by the FromEnv functions. This struct only governs model selection.
type ProviderConfig struct {
	// BedrockModels lists Bedrock model IDs (e.g.,
	// "us.anthropic.claude-sonnet-4-5-20250514-v1:0"). The first entry is
	// the default. If empty, Bedrock is not initialized even if credentials
	// are available.
	BedrockModels []string

	// ZAIModels lists Z.AI model IDs (e.g., "glm-5.1", "glm-5-turbo").
	// The first entry is the default. If empty, Z.AI is not initialized
	// even if ZAI_API_KEY is set.
	ZAIModels []string

	// FireworksModels lists Fireworks model IDs (e.g.,
	// "accounts/fireworks/routers/kimi-k2p5-turbo"). The first entry is
	// the default. If empty, Fireworks is not initialized even if
	// FIREWORKS_API_KEY is set.
	FireworksModels []string
}

// BedrockProvider implements the Provider interface for AWS Bedrock using
// the Anthropic Messages API format over the Bedrock invoke endpoint.
// Auth uses a bearer identity token (AWS_BEARER_TOKEN_BEDROCK) rather
// than SigV4 signing, matching the pattern from choiros-rs.
type BedrockProvider struct {
	region     string
	modelID    string
	authToken  string // loaded at init time, never logged
	httpClient *http.Client
	anthropicV string // anthropic version header
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
		modelID:    cfg.ModelID,
		authToken:  cfg.AuthToken,
		httpClient: &http.Client{Timeout: 120 * time.Second},
		anthropicV: "bedrock-2023-05-31",
	}, nil
}

// NewBedrockProviderFromEnv creates a Bedrock provider using credentials
// from environment variables (AWS_REGION, AWS_BEARER_TOKEN_BEDROCK) and
// the given model ID. The model is a runtime concern, not an env var.
func NewBedrockProviderFromEnv(modelID string) (*BedrockProvider, error) {
	region := os.Getenv("AWS_REGION")
	token := os.Getenv("AWS_BEARER_TOKEN_BEDROCK")

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

// Stream falls back to non-streaming Call because Bedrock uses binary
// EventStream (not SSE). The response is emitted as a single chunk.
func (p *BedrockProvider) Stream(ctx context.Context, req LLMRequest, onChunk func(StreamChunk)) (*LLMResponse, error) {
	resp, err := p.Call(ctx, req)
	if err != nil {
		return nil, err
	}
	// Emit the complete response as a single chunk for consistency.
	onChunk(StreamChunk{
		Type:       "message_start",
		ID:         resp.ID,
		Model:      resp.Model,
		StopReason: resp.StopReason,
		Usage:      &StreamUsage{InputTokens: resp.Usage.InputTokens, OutputTokens: resp.Usage.OutputTokens},
	})
	if resp.Text != "" {
		onChunk(StreamChunk{
			Type:  "content_block_delta",
			Delta: resp.Text,
			Index: 0,
		})
	}
	onChunk(StreamChunk{
		Type:       "message_stop",
		StopReason: resp.StopReason,
	})
	return resp, nil
}

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
	modelID    string
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
		modelID:    cfg.ModelID,
		httpClient: &http.Client{Timeout: 120 * time.Second},
		baseURL:    strings.TrimRight(baseURL, "/"),
	}, nil
}

// NewZAIProviderFromEnv creates a Z.AI provider using credentials from
// environment variables (ZAI_API_KEY) and the given model ID. The model
// is a runtime concern, not an env var. The base URL defaults to
// https://api.z.ai/api/anthropic unless ZAI_BASE_URL is set.
func NewZAIProviderFromEnv(modelID string) (*ZAIProvider, error) {
	apiKey := os.Getenv("ZAI_API_KEY")
	baseURL := os.Getenv("ZAI_BASE_URL")

	p, err := NewZAIProvider(ZAIConfig{
		APIKey:  apiKey,
		ModelID: modelID,
		BaseURL: baseURL,
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

// Stream sends the request to Z.AI with stream=true and processes the
// SSE response. It parses each event and calls onChunk for incremental
// text deltas, tool use, and lifecycle events. Returns the accumulated
// LLMResponse on completion.
func (p *ZAIProvider) Stream(ctx context.Context, req LLMRequest, onChunk func(StreamChunk)) (*LLMResponse, error) {
	endpoint := p.baseURL + "/v1/messages"

	// Build streaming request.
	req.Stream = true
	body := p.buildRequestBody(req)
	body.Stream = true

	httpReq, err := newJSONRequest(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return nil, fmt.Errorf("zai: build stream request: %w", err)
	}

	httpReq.Header.Set("x-api-key", p.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")

	log.Printf("provider: zai stream model=%s", p.modelID)

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("zai: stream http call: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.ReadAll(resp.Body)
		return nil, fmt.Errorf("zai: status %s (sanitized)", resp.Status)
	}

	return parseSSEStream(resp.Body, p.modelID, "zai", onChunk)
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
	modelID    string
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
		modelID:    cfg.ModelID,
		httpClient: &http.Client{Timeout: 120 * time.Second},
		baseURL:    strings.TrimRight(baseURL, "/"),
	}, nil
}

// NewFireworksProviderFromEnv creates a Fireworks provider using credentials
// from environment variables (FIREWORKS_API_KEY) and the given model ID.
// The model is a runtime concern, not an env var. The base URL defaults to
// https://api.fireworks.ai/inference unless FIREWORKS_BASE_URL is set.
func NewFireworksProviderFromEnv(modelID string) (*FireworksProvider, error) {
	apiKey := os.Getenv("FIREWORKS_API_KEY")
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

// Stream sends the request to Fireworks AI with stream=true and processes
// the SSE response using the same Anthropic-compatible SSE format.
func (p *FireworksProvider) Stream(ctx context.Context, req LLMRequest, onChunk func(StreamChunk)) (*LLMResponse, error) {
	endpoint := p.baseURL + "/v1/messages"

	req.Stream = true
	body := p.buildRequestBody(req)
	body.Stream = true

	httpReq, err := newJSONRequest(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return nil, fmt.Errorf("fireworks: build stream request: %w", err)
	}

	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")

	log.Printf("provider: fireworks stream model=%s", p.modelID)

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("fireworks: stream http call: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.ReadAll(resp.Body)
		return nil, fmt.Errorf("fireworks: status %s (sanitized)", resp.Status)
	}

	return parseSSEStream(resp.Body, p.modelID, "fireworks", onChunk)
}

func (p *FireworksProvider) buildRequestBody(req LLMRequest) anthropicRequest {
	ar := anthropicRequest{
		Model:     p.modelID,
		MaxTokens: defaultMaxTokens(req.MaxTokens),
		Stream:    false,
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
	Model            string             `json:"model,omitempty"` // empty for Bedrock (model in URL)
	System           any                `json:"system,omitempty"`
	Messages         []anthropicMessage `json:"messages"`
	Tools            []anthropicTool    `json:"tools,omitempty"`
	MaxTokens        int                `json:"max_tokens"`
	Stream           bool               `json:"stream,omitempty"`
	AnthropicVersion string             `json:"anthropic_version,omitempty"`
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
	Role    string             `json:"role"`
	Content []anthropicContent `json:"content"`
}

type anthropicContent struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`          // tool_use: call ID
	Name      string          `json:"name,omitempty"`        // tool_use: tool name
	Input     json.RawMessage `json:"input,omitempty"`       // tool_use: arguments
	ToolUseID string          `json:"tool_use_id,omitempty"` // tool_result: originating call ID
	Content   string          `json:"content,omitempty"`     // tool_result: result content (when string)
	IsError   bool            `json:"is_error,omitempty"`    // tool_result: error flag
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
		ID:           payload.ID,
		Model:        coalesce(payload.Model, modelID),
		StopReason:   payload.StopReason,
		Usage:        Usage{InputTokens: payload.Usage.InputTokens, OutputTokens: payload.Usage.OutputTokens},
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

// parseSSEStream reads an Anthropic-compatible SSE stream from the response
// body, calling onChunk for each relevant event, and returns the accumulated
// LLMResponse. This handles the standard Anthropic Messages API streaming
// format used by both Z.AI and Fireworks AI.
//
// Event flow:
//  1. message_start — contains response ID, model, initial usage
//  2. content_block_start — indicates a new content block (text or tool_use)
//  3. content_block_delta — incremental text (text_delta) or tool input (input_json_delta)
//  4. content_block_stop — end of content block
//  5. message_delta — final stop_reason and cumulative output token count
//  6. message_stop — stream complete
//
// Unknown events (ping, error, etc.) are silently skipped.
func parseSSEStream(body io.Reader, modelID string, providerName string, onChunk func(StreamChunk)) (*LLMResponse, error) {
	result := &LLMResponse{
		Model:        modelID,
		ProviderName: providerName,
	}

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024) // 1MB max line

	var toolCallInputs map[int]*struct { // index → accumulated JSON
		id   string
		name string
		json strings.Builder
	}
	toolCallInputs = make(map[int]*struct {
		id   string
		name string
		json strings.Builder
	})

	for scanner.Scan() {
		line := scanner.Text()

		// SSE lines starting with "event: " set the event type.
		// We don't need to track this — the event type is in the JSON data.
		if strings.HasPrefix(line, "event: ") {
			continue
		}

		// SSE lines starting with "data: " contain the payload.
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")

		// "[DONE]" signals stream completion.
		if data == "[DONE]" {
			break
		}

		var event struct {
			Type         string          `json:"type"`
			Index        int             `json:"index"`
			Model        string          `json:"model"`
			ID           string          `json:"id"`
			Delta        json.RawMessage `json:"delta"`
			Message      json.RawMessage `json:"message"`
			ContentBlock json.RawMessage `json:"content_block"`
			Usage        struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		}

		if err := json.Unmarshal([]byte(data), &event); err != nil {
			// Skip malformed events.
			continue
		}

		switch event.Type {
		case "message_start":
			// Extract message fields.
			var msg struct {
				ID    string `json:"id"`
				Model string `json:"model"`
				Usage struct {
					InputTokens int `json:"input_tokens"`
				} `json:"usage"`
			}
			if err := json.Unmarshal(event.Message, &msg); err == nil {
				result.ID = msg.ID
				if msg.Model != "" {
					result.Model = msg.Model
				}
				result.Usage.InputTokens = msg.Usage.InputTokens
			}
			onChunk(StreamChunk{
				Type:  "message_start",
				ID:    result.ID,
				Model: result.Model,
				Usage: &StreamUsage{InputTokens: result.Usage.InputTokens},
			})

		case "content_block_start":
			// Check if this is a tool_use block.
			var cb struct {
				Type  string          `json:"type"`
				ID    string          `json:"id"`
				Name  string          `json:"name"`
				Input json.RawMessage `json:"input"`
			}
			if err := json.Unmarshal(event.ContentBlock, &cb); err == nil {
				if cb.Type == "tool_use" {
					toolCallInputs[event.Index] = &struct {
						id   string
						name string
						json strings.Builder
					}{id: cb.ID, name: cb.Name}
					onChunk(StreamChunk{
						Type:         "content_block_start",
						Index:        event.Index,
						ToolCallID:   cb.ID,
						ToolCallName: cb.Name,
					})
				} else {
					onChunk(StreamChunk{
						Type:  "content_block_start",
						Index: event.Index,
					})
				}
			}

		case "content_block_delta":
			// Parse delta based on type.
			var delta struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				PartialJSON string `json:"partial_json"`
			}
			if err := json.Unmarshal(event.Delta, &delta); err == nil {
				switch delta.Type {
				case "text_delta":
					result.Text += delta.Text
					onChunk(StreamChunk{
						Type:  "content_block_delta",
						Delta: delta.Text,
						Index: event.Index,
					})
				case "input_json_delta":
					// Accumulate tool input JSON.
					if tc, ok := toolCallInputs[event.Index]; ok {
						tc.json.WriteString(delta.PartialJSON)
					}
					onChunk(StreamChunk{
						Type:          "content_block_delta",
						Index:         event.Index,
						ToolCallDelta: delta.PartialJSON,
					})
				}
			}

		case "content_block_stop":
			// Finalize any tool_use blocks.
			if tc, ok := toolCallInputs[event.Index]; ok {
				result.ToolCalls = append(result.ToolCalls, ContentToolCall{
					ID:        tc.id,
					Name:      tc.name,
					Arguments: canonicalJSON(json.RawMessage(tc.json.String())),
				})
			}
			onChunk(StreamChunk{
				Type:  "content_block_stop",
				Index: event.Index,
			})

		case "message_delta":
			var delta struct {
				StopReason string `json:"stop_reason"`
			}
			if err := json.Unmarshal(event.Delta, &delta); err == nil {
				result.StopReason = delta.StopReason
			}
			result.Usage.OutputTokens = event.Usage.OutputTokens
			onChunk(StreamChunk{
				Type:       "message_delta",
				StopReason: result.StopReason,
				Usage:      &StreamUsage{OutputTokens: result.Usage.OutputTokens},
			})

		case "message_stop":
			onChunk(StreamChunk{
				Type:       "message_stop",
				StopReason: result.StopReason,
			})

		case "ping":
			// Ignore ping events.

		case "error":
			var errData struct {
				Error struct {
					Type    string `json:"type"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal([]byte(data), &errData); err == nil {
				log.Printf("provider: %s stream error: %s (sanitized)", providerName, errData.Error.Type)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("%s: read stream: %w", providerName, err)
	}

	log.Printf("provider: %s stream complete model=%s stop=%s tokens_in=%d tokens_out=%d text_len=%d tool_calls=%d",
		providerName, result.Model, result.StopReason,
		result.Usage.InputTokens, result.Usage.OutputTokens, len(result.Text), len(result.ToolCalls))

	return result, nil
}

// ModelInfo describes a supported model and its associated provider.
type ModelInfo struct {
	// ID is the model identifier used in API requests (provider-specific).
	ID string `json:"id"`

	// DisplayName is a human-readable name for logging and UI.
	DisplayName string `json:"display_name"`

	// Provider is the provider name that serves this model (e.g., "zai",
	// "fireworks", "bedrock").
	Provider string `json:"provider"`

	// MaxOutputTokens is the maximum output tokens for this model.
	MaxOutputTokens int `json:"max_output_tokens"`
}

// SupportedModels returns the list of models that the provider package
// can serve. These are derived from the Droid settings.json customModels
// configuration.
func SupportedModels() []ModelInfo {
	return []ModelInfo{
		// Bedrock models (cross-region inference IDs)
		{
			ID:              "us.anthropic.claude-haiku-4-5-20251001-v1:0",
			DisplayName:     "Claude Haiku 4.5",
			Provider:        "bedrock",
			MaxOutputTokens: 8192,
		},
		{
			ID:              "us.anthropic.claude-sonnet-4-6",
			DisplayName:     "Claude Sonnet 4.6",
			Provider:        "bedrock",
			MaxOutputTokens: 65536,
		},
		{
			ID:              "us.anthropic.claude-opus-4-6-v1",
			DisplayName:     "Claude Opus 4.6",
			Provider:        "bedrock",
			MaxOutputTokens: 32768,
		},
		// Z.AI models
		{
			ID:              "glm-5.1",
			DisplayName:     "GLM-5.1",
			Provider:        "zai",
			MaxOutputTokens: 131072,
		},
		{
			ID:              "glm-5-turbo",
			DisplayName:     "GLM-5-Turbo",
			Provider:        "zai",
			MaxOutputTokens: 131072,
		},
		// Fireworks models
		{
			ID:              "accounts/fireworks/routers/kimi-k2p5-turbo",
			DisplayName:     "Kimi K2.5",
			Provider:        "fireworks",
			MaxOutputTokens: 131072,
		},
	}
}

// MultiProvider holds multiple provider backends keyed by name. It enables
// the runtime to reference providers for different models simultaneously
// without requiring model routing logic (which is its own scope).
//
// Usage: callers initialize specific providers and register them; the
// MultiProvider simply stores them for later retrieval. Model routing is
// explicitly out of scope for this type.
type MultiProvider struct {
	providers map[string]Provider
}

// NewMultiProvider creates an empty MultiProvider.
func NewMultiProvider() *MultiProvider {
	return &MultiProvider{
		providers: make(map[string]Provider),
	}
}

// Register adds a provider under the given name. If a provider with the
// same name already exists it is replaced.
func (mp *MultiProvider) Register(name string, p Provider) {
	mp.providers[name] = p
}

// Get returns the provider registered under the given name, or nil.
func (mp *MultiProvider) Get(name string) Provider {
	return mp.providers[name]
}

// Names returns all registered provider names.
func (mp *MultiProvider) Names() []string {
	names := make([]string, 0, len(mp.providers))
	for name := range mp.providers {
		names = append(names, name)
	}
	return names
}

// ResolveAll creates and registers all providers for which credentials are
// available in the environment, using the given ProviderConfig for model
// selection. It returns the MultiProvider with whatever providers could be
// initialized (may be empty if no credentials are set).
//
// Model selection is a runtime concern passed via cfg; credentials are
// resolved from environment variables. Model routing is out of scope.
func ResolveAll(cfg ProviderConfig) *MultiProvider {
	mp := NewMultiProvider()

	// Try Bedrock — one provider per model.
	if os.Getenv("AWS_BEARER_TOKEN_BEDROCK") != "" {
		for i, modelID := range cfg.BedrockModels {
			if p, err := NewBedrockProviderFromEnv(modelID); err == nil {
				name := "bedrock"
				if i > 0 {
					name = "bedrock-" + modelID
				}
				mp.Register(name, p)
				log.Printf("provider: resolved %s (region=%s model=%s)", name, p.region, redactModel(p.modelID))
			}
		}
	}

	// Try Z.AI — one provider per model.
	if os.Getenv("ZAI_API_KEY") != "" {
		for i, modelID := range cfg.ZAIModels {
			if p, err := NewZAIProviderFromEnv(modelID); err == nil {
				name := "zai"
				if i > 0 {
					name = "zai-" + modelID
				}
				mp.Register(name, p)
				log.Printf("provider: resolved %s (model=%s)", name, p.modelID)
			}
		}
	}

	// Try Fireworks — one provider per model.
	if os.Getenv("FIREWORKS_API_KEY") != "" {
		for i, modelID := range cfg.FireworksModels {
			if p, err := NewFireworksProviderFromEnv(modelID); err == nil {
				name := "fireworks"
				if i > 0 {
					name = "fireworks-" + modelID
				}
				mp.Register(name, p)
				log.Printf("provider: resolved %s (model=%s)", name, p.modelID)
			}
		}
	}

	return mp
}

// ResolveProvider creates the first available real provider from environment
// credentials using the given ProviderConfig for model selection. It tries
// Bedrock first, then Z.AI (first model), then Fireworks. If none is
// configured, it returns nil, indicating that the stub provider should be
// used instead.
//
// This preserves the gateway boundary: the runtime does not directly expose
// provider credentials to the browser; it only uses them internally.
func ResolveProvider(cfg ProviderConfig) (Provider, error) {
	// Try Bedrock first (first model).
	if os.Getenv("AWS_BEARER_TOKEN_BEDROCK") != "" && len(cfg.BedrockModels) > 0 {
		p, err := NewBedrockProviderFromEnv(cfg.BedrockModels[0])
		if err != nil {
			return nil, fmt.Errorf("resolve provider: %w", err)
		}
		log.Printf("provider: resolved bedrock (region=%s model=%s)", p.region, redactModel(p.modelID))
		return p, nil
	}

	// Try Z.AI (first model).
	if os.Getenv("ZAI_API_KEY") != "" && len(cfg.ZAIModels) > 0 {
		p, err := NewZAIProviderFromEnv(cfg.ZAIModels[0])
		if err != nil {
			return nil, fmt.Errorf("resolve provider: %w", err)
		}
		log.Printf("provider: resolved zai (model=%s)", p.modelID)
		return p, nil
	}

	// Try Fireworks (first model).
	if os.Getenv("FIREWORKS_API_KEY") != "" && len(cfg.FireworksModels) > 0 {
		p, err := NewFireworksProviderFromEnv(cfg.FireworksModels[0])
		if err != nil {
			return nil, fmt.Errorf("resolve provider: %w", err)
		}
		log.Printf("provider: resolved fireworks (model=%s)", p.modelID)
		return p, nil
	}

	return nil, nil
}
