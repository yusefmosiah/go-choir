// Package provider implements real LLM provider bridges for the go-choir
// sandbox runtime. It supports Bedrock (Anthropic Messages API over AWS
// Bedrock invoke endpoint), Z.AI (Anthropic-compatible API), and Fireworks AI
// (Anthropic-compatible API) as the first required real-provider paths for
// Mission 3.
//
// Supported models (matching Droid settings.json customModels):
//   - GLM-5.1 (Z.AI, provider "zai") — Z.AI model
//   - GLM-5-Turbo (Z.AI, provider "zai") — faster Z.AI variant, same provider
//   - Kimi K2.5 Turbo (Fireworks AI, provider "fireworks") — Fireworks router model
//   - Claude Sonnet 4.5 (Bedrock, provider "bedrock") — Bedrock model
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
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// LLMRequest is the unified request shape for all provider backends.
type LLMRequest struct {
	// Provider is the provider identifier (e.g. "chatgpt", "zai",
	// "fireworks", "bedrock") for gateway-routed requests.
	Provider string `json:"provider,omitempty"`

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

	// ReasoningEffort is an optional provider-specific reasoning control.
	// For ChatGPT/OpenAI Responses this maps to reasoning.effort.
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
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
	// "us.anthropic.claude-sonnet-4-5-20250514-v1:0"). The first entry seeds
	// the provider instance; request.Model still controls per-call selection.
	// If empty, Bedrock is not initialized even if credentials are available.
	BedrockModels []string

	// ZAIModels lists Z.AI model IDs (e.g., "glm-5.1", "glm-5-turbo").
	// The first entry seeds the provider instance; request.Model still
	// controls per-call selection. If empty, Z.AI is not initialized even if
	// ZAI_API_KEY is set.
	ZAIModels []string

	// FireworksModels lists Fireworks model IDs (e.g.,
	// "accounts/fireworks/routers/kimi-k2p5-turbo"). The first entry seeds
	// the provider instance; request.Model still controls per-call selection.
	// If empty, Fireworks is not initialized even if FIREWORKS_API_KEY is set.
	FireworksModels []string

	// ChatGPTModels lists ChatGPT/Codex Responses model IDs. The first entry
	// seeds the provider instance; request.Model still controls per-call
	// selection. If empty, ChatGPT is not initialized even if Codex OAuth auth
	// is available.
	ChatGPTModels []string

	// ChatGPTReasoningEffort seeds the ChatGPT provider's reasoning effort for
	// requests that do not carry a per-request value.
	ChatGPTReasoningEffort string

	// SelectedProvider is the explicitly selected provider for direct sandbox
	// runtime calls. Empty means no direct provider is selected.
	SelectedProvider string
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
	modelID := effectiveModel(req.Model, p.modelID)
	endpoint := fmt.Sprintf(
		"https://bedrock-runtime.%s.amazonaws.com/model/%s/invoke",
		p.region, pathEscape(modelID),
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

	log.Printf("provider: bedrock call model=%s region=%s", redactModel(modelID), p.region)

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("bedrock: http call: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	return parseBedrockResponse(resp, modelID)
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
	modelID := effectiveModel(req.Model, p.modelID)

	body := p.buildRequestBody(req, modelID)

	httpReq, err := newJSONRequest(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return nil, fmt.Errorf("zai: build request: %w", err)
	}

	httpReq.Header.Set("x-api-key", p.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")

	log.Printf("provider: zai call model=%s", modelID)

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("zai: http call: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	return parseAnthropicResponse(resp, modelID, "zai")
}

// Stream sends the request to Z.AI with stream=true and processes the
// SSE response. It parses each event and calls onChunk for incremental
// text deltas, tool use, and lifecycle events. Returns the accumulated
// LLMResponse on completion.
func (p *ZAIProvider) Stream(ctx context.Context, req LLMRequest, onChunk func(StreamChunk)) (*LLMResponse, error) {
	endpoint := p.baseURL + "/v1/messages"
	modelID := effectiveModel(req.Model, p.modelID)

	// Build streaming request.
	req.Stream = true
	body := p.buildRequestBody(req, modelID)
	body.Stream = true

	httpReq, err := newJSONRequest(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return nil, fmt.Errorf("zai: build stream request: %w", err)
	}

	httpReq.Header.Set("x-api-key", p.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")

	log.Printf("provider: zai stream model=%s", modelID)

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("zai: stream http call: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.ReadAll(resp.Body)
		return nil, fmt.Errorf("zai: status %s (sanitized)", resp.Status)
	}

	return parseSSEStream(resp.Body, modelID, "zai", onChunk)
}

func (p *ZAIProvider) buildRequestBody(req LLMRequest, modelID string) anthropicRequest {
	ar := anthropicRequest{
		Model:            modelID,
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
	modelID := effectiveModel(req.Model, p.modelID)

	body := p.buildRequestBody(req, modelID)

	httpReq, err := newJSONRequest(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return nil, fmt.Errorf("fireworks: build request: %w", err)
	}

	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")

	log.Printf("provider: fireworks call model=%s", modelID)

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("fireworks: http call: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	return parseAnthropicResponse(resp, modelID, "fireworks")
}

// Stream sends the request to Fireworks AI with stream=true and processes
// the SSE response using the same Anthropic-compatible SSE format.
func (p *FireworksProvider) Stream(ctx context.Context, req LLMRequest, onChunk func(StreamChunk)) (*LLMResponse, error) {
	endpoint := p.baseURL + "/v1/messages"
	modelID := effectiveModel(req.Model, p.modelID)

	req.Stream = true
	body := p.buildRequestBody(req, modelID)
	body.Stream = true

	httpReq, err := newJSONRequest(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return nil, fmt.Errorf("fireworks: build stream request: %w", err)
	}

	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")

	log.Printf("provider: fireworks stream model=%s", modelID)

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("fireworks: stream http call: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.ReadAll(resp.Body)
		return nil, fmt.Errorf("fireworks: status %s (sanitized)", resp.Status)
	}

	return parseSSEStream(resp.Body, modelID, "fireworks", onChunk)
}

func (p *FireworksProvider) buildRequestBody(req LLMRequest, modelID string) anthropicRequest {
	ar := anthropicRequest{
		Model:     modelID,
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

// ChatGPTProvider implements the Provider interface for ChatGPT subscription
// billing through Codex OAuth, using the OpenAI Responses-compatible endpoint
// exposed by ChatGPT.
type ChatGPTProvider struct {
	auth       *ChatGPTAuth
	modelID    string
	httpClient *http.Client
	baseURL    string
	reasoning  string
}

// ChatGPTConfig holds configuration for creating a ChatGPTProvider.
type ChatGPTConfig struct {
	ModelID         string
	BaseURL         string
	AuthPath        string
	ReasoningEffort string
}

const defaultChatGPTResponsesURL = "https://chatgpt.com/backend-api/codex/responses"

// NewChatGPTProvider creates a ChatGPT provider using Codex OAuth auth.
func NewChatGPTProvider(cfg ChatGPTConfig) (*ChatGPTProvider, error) {
	if cfg.ModelID == "" {
		return nil, fmt.Errorf("chatgpt provider requires model_id")
	}
	baseURL := strings.TrimRight(cfg.BaseURL, "/")
	if baseURL == "" {
		baseURL = defaultChatGPTResponsesURL
	}
	auth := NewChatGPTAuth(ChatGPTAuthOptions{Path: cfg.AuthPath})
	if _, err := auth.Read(); err != nil {
		return nil, fmt.Errorf("chatgpt provider requires codex auth: %w", err)
	}
	reasoning := strings.TrimSpace(cfg.ReasoningEffort)
	if reasoning == "" {
		reasoning = "low"
	}
	return &ChatGPTProvider{
		auth:       auth,
		modelID:    cfg.ModelID,
		httpClient: &http.Client{Timeout: 120 * time.Second},
		baseURL:    baseURL,
		reasoning:  reasoning,
	}, nil
}

// NewChatGPTProviderFromEnv creates a ChatGPT provider using Codex OAuth from
// CHATGPT_AUTH_PATH or ~/.codex/auth.json. CHATGPT_BASE_URL can override the
// Responses endpoint for tests or custom deployments.
func NewChatGPTProviderFromEnv(modelID, reasoningEffort string) (*ChatGPTProvider, error) {
	p, err := NewChatGPTProvider(ChatGPTConfig{
		ModelID:         modelID,
		BaseURL:         os.Getenv("CHATGPT_BASE_URL"),
		AuthPath:        os.Getenv("CHATGPT_AUTH_PATH"),
		ReasoningEffort: reasoningEffort,
	})
	if err != nil {
		return nil, fmt.Errorf("chatgpt from env: %w", err)
	}
	return p, nil
}

func (p *ChatGPTProvider) Name() string { return "chatgpt" }
func (p *ChatGPTProvider) IsReal() bool { return true }

func (p *ChatGPTProvider) Call(ctx context.Context, req LLMRequest) (*LLMResponse, error) {
	req.Stream = true
	modelID := effectiveModel(req.Model, p.modelID)
	body := p.buildRequestBody(req, modelID)
	httpReq, err := newJSONRequest(ctx, http.MethodPost, p.baseURL, body)
	if err != nil {
		return nil, fmt.Errorf("chatgpt: build request: %w", err)
	}
	authHeader, err := p.auth.Header(ctx)
	if err != nil {
		return nil, fmt.Errorf("chatgpt: auth: %w", err)
	}
	httpReq.Header.Set("Authorization", authHeader)
	httpReq.Header.Set("Accept", "text/event-stream")

	log.Printf("provider: chatgpt call model=%s reasoning=%s", modelID, effectiveReasoning(req.ReasoningEffort, p.reasoning))

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("chatgpt: http call: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.ReadAll(resp.Body)
		return nil, fmt.Errorf("chatgpt: status %s (sanitized)", resp.Status)
	}
	return parseOpenAIStream(resp.Body, modelID, "chatgpt", func(StreamChunk) {})
}

func (p *ChatGPTProvider) Stream(ctx context.Context, req LLMRequest, onChunk func(StreamChunk)) (*LLMResponse, error) {
	req.Stream = true
	modelID := effectiveModel(req.Model, p.modelID)
	body := p.buildRequestBody(req, modelID)
	body.Stream = true

	httpReq, err := newJSONRequest(ctx, http.MethodPost, p.baseURL, body)
	if err != nil {
		return nil, fmt.Errorf("chatgpt: build stream request: %w", err)
	}
	authHeader, err := p.auth.Header(ctx)
	if err != nil {
		return nil, fmt.Errorf("chatgpt: auth: %w", err)
	}
	httpReq.Header.Set("Authorization", authHeader)
	httpReq.Header.Set("Accept", "text/event-stream")

	log.Printf("provider: chatgpt stream model=%s reasoning=%s", modelID, effectiveReasoning(req.ReasoningEffort, p.reasoning))

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("chatgpt: stream http call: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.ReadAll(resp.Body)
		return nil, fmt.Errorf("chatgpt: status %s (sanitized)", resp.Status)
	}
	return parseOpenAIStream(resp.Body, modelID, "chatgpt", onChunk)
}

func (p *ChatGPTProvider) buildRequestBody(req LLMRequest, modelID string) openAIRequest {
	payload := openAIRequest{
		Model:        modelID,
		Instructions: req.System,
		Input:        convertOpenAIInput(req.Messages),
		Tools:        convertOpenAITools(req.Tools),
		Store:        false,
		Stream:       req.Stream,
	}
	if effort := effectiveReasoning(req.ReasoningEffort, p.reasoning); effort != "" && effort != "none" && effort != "off" {
		payload.Reasoning = &openAIReasoning{Effort: effort}
	}
	return payload
}

func effectiveReasoning(requested, fallback string) string {
	if strings.TrimSpace(requested) != "" {
		return strings.TrimSpace(requested)
	}
	return strings.TrimSpace(fallback)
}

func effectiveModel(requested, fallback string) string {
	if strings.TrimSpace(requested) != "" {
		return strings.TrimSpace(requested)
	}
	return fallback
}

type openAIRequest struct {
	Model        string           `json:"model"`
	Instructions string           `json:"instructions,omitempty"`
	Input        []openAIItem     `json:"input,omitempty"`
	Tools        []openAITool     `json:"tools,omitempty"`
	Store        bool             `json:"store"`
	Stream       bool             `json:"stream,omitempty"`
	Reasoning    *openAIReasoning `json:"reasoning,omitempty"`
}

type openAIReasoning struct {
	Effort string `json:"effort"`
}

type openAIItem struct {
	Type      string `json:"type,omitempty"`
	Role      string `json:"role,omitempty"`
	Content   any    `json:"content,omitempty"`
	Name      string `json:"name,omitempty"`
	CallID    string `json:"call_id,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	Output    string `json:"output,omitempty"`
}

type openAITool struct {
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type openAIResponse struct {
	ID     string               `json:"id"`
	Model  string               `json:"model,omitempty"`
	Output []openAIResponseItem `json:"output"`
	Usage  openAIUsage          `json:"usage"`
}

type openAIUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

type openAIResponseItem struct {
	Type      string                  `json:"type"`
	ID        string                  `json:"id,omitempty"`
	CallID    string                  `json:"call_id,omitempty"`
	Name      string                  `json:"name,omitempty"`
	Arguments json.RawMessage         `json:"arguments,omitempty"`
	Content   []openAIResponseContent `json:"content,omitempty"`
	Role      string                  `json:"role,omitempty"`
	Status    string                  `json:"status,omitempty"`
}

type openAIResponseContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

func convertOpenAIInput(messages []Message) []openAIItem {
	out := make([]openAIItem, 0, len(messages))
	for _, msg := range messages {
		var parts []map[string]string
		flushText := func() {
			if len(parts) == 0 {
				return
			}
			out = append(out, openAIItem{Role: msg.Role, Content: parts})
			parts = nil
		}
		contentType := "input_text"
		if msg.Role == "assistant" {
			contentType = "output_text"
		}
		for _, block := range msg.Content {
			switch block.Type {
			case "text":
				if block.Text != "" {
					parts = append(parts, map[string]string{"type": contentType, "text": block.Text})
				}
			case "tool_use":
				flushText()
				out = append(out, openAIItem{
					Type:      "function_call",
					CallID:    block.ID,
					Name:      block.Name,
					Arguments: string(canonicalJSON(block.Input)),
				})
			case "tool_result":
				flushText()
				out = append(out, openAIItem{
					Type:   "function_call_output",
					CallID: block.ToolUseID,
					Output: block.Text,
				})
			}
		}
		flushText()
	}
	return out
}

func convertOpenAITools(tools []ToolDef) []openAITool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]openAITool, 0, len(tools))
	for _, tool := range tools {
		out = append(out, openAITool{
			Type:        "function",
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  tool.InputSchema,
		})
	}
	return out
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

func parseOpenAIResponse(resp *http.Response, modelID string, providerName string) (*LLMResponse, error) {
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.ReadAll(resp.Body)
		return nil, fmt.Errorf("%s: status %s (sanitized)", providerName, resp.Status)
	}

	var payload openAIResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("%s: decode response: %w", providerName, err)
	}

	result := &LLMResponse{
		ID:           payload.ID,
		Model:        coalesce(payload.Model, modelID),
		Usage:        Usage{InputTokens: payload.Usage.InputTokens, OutputTokens: payload.Usage.OutputTokens},
		ProviderName: providerName,
	}

	for _, item := range payload.Output {
		switch item.Type {
		case "message":
			for _, part := range item.Content {
				if part.Text != "" {
					result.Text += part.Text
				}
			}
			if result.StopReason == "" {
				result.StopReason = "end_turn"
			}
		case "function_call":
			result.ToolCalls = append(result.ToolCalls, ContentToolCall{
				ID:        item.CallID,
				Name:      item.Name,
				Arguments: canonicalJSON(item.Arguments),
			})
			result.StopReason = "tool_use"
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

func parseOpenAIStream(body io.Reader, modelID string, providerName string, onChunk func(StreamChunk)) (*LLMResponse, error) {
	result := &LLMResponse{
		Model:        modelID,
		ProviderName: providerName,
	}
	type toolBuffer struct {
		callID string
		name   string
		args   string
	}
	tools := map[string]*toolBuffer{}

	if err := readOpenAISSE(body, func(data []byte) error {
		if len(data) == 0 || strings.TrimSpace(string(data)) == "[DONE]" {
			return nil
		}
		var payload map[string]any
		if err := json.Unmarshal(data, &payload); err != nil {
			return nil
		}
		typ := stringValue(payload["type"])
		switch typ {
		case "response.created":
			if response, ok := payload["response"].(map[string]any); ok {
				result.ID = stringValue(response["id"])
				if model := stringValue(response["model"]); model != "" {
					result.Model = model
				}
				onChunk(StreamChunk{Type: "message_start", ID: result.ID, Model: result.Model})
			}
		case "response.output_text.delta":
			text := stringValue(payload["delta"])
			if text != "" {
				result.Text += text
				onChunk(StreamChunk{Type: "content_block_delta", Delta: text})
			}
		case "response.output_item.added":
			item, _ := payload["item"].(map[string]any)
			if stringValue(item["type"]) == "function_call" {
				key := stringValue(item["id"])
				tools[key] = &toolBuffer{
					callID: stringValue(item["call_id"]),
					name:   stringValue(item["name"]),
					args:   stringValue(item["arguments"]),
				}
				onChunk(StreamChunk{Type: "content_block_start", ToolCallID: tools[key].callID, ToolCallName: tools[key].name})
			}
		case "response.function_call_arguments.delta":
			key := stringValue(payload["item_id"])
			if tools[key] == nil {
				tools[key] = &toolBuffer{}
			}
			tools[key].args += stringValue(payload["delta"])
			if tools[key].callID == "" {
				tools[key].callID = stringValue(payload["call_id"])
			}
			if tools[key].name == "" {
				tools[key].name = stringValue(payload["name"])
			}
			onChunk(StreamChunk{Type: "content_block_delta", ToolCallDelta: stringValue(payload["delta"]), ToolCallID: tools[key].callID, ToolCallName: tools[key].name})
		case "response.function_call_arguments.done":
			key := stringValue(payload["item_id"])
			if tools[key] == nil {
				tools[key] = &toolBuffer{}
			}
			if arguments := stringValue(payload["arguments"]); arguments != "" {
				tools[key].args = arguments
			}
			if tools[key].callID == "" {
				tools[key].callID = stringValue(payload["call_id"])
			}
			if tools[key].name == "" {
				tools[key].name = stringValue(payload["name"])
			}
			result.ToolCalls = append(result.ToolCalls, ContentToolCall{
				ID:        tools[key].callID,
				Name:      tools[key].name,
				Arguments: canonicalJSON(json.RawMessage(tools[key].args)),
			})
			result.StopReason = "tool_use"
			onChunk(StreamChunk{Type: "content_block_stop", ToolCallID: tools[key].callID, ToolCallName: tools[key].name})
			delete(tools, key)
		case "response.output_item.done":
			item, _ := payload["item"].(map[string]any)
			if stringValue(item["type"]) == "function_call" {
				args := stringValue(item["arguments"])
				result.ToolCalls = append(result.ToolCalls, ContentToolCall{
					ID:        stringValue(item["call_id"]),
					Name:      stringValue(item["name"]),
					Arguments: canonicalJSON(json.RawMessage(args)),
				})
				result.StopReason = "tool_use"
			}
		case "response.completed":
			response, _ := payload["response"].(map[string]any)
			if response != nil {
				result.ID = stringValue(response["id"])
				if model := stringValue(response["model"]); model != "" {
					result.Model = model
				}
				if usage, ok := response["usage"].(map[string]any); ok {
					result.Usage.InputTokens = int(numberValue(usage["input_tokens"]))
					result.Usage.OutputTokens = int(numberValue(usage["output_tokens"]))
				}
			}
			if result.StopReason == "" {
				result.StopReason = "end_turn"
			}
			onChunk(StreamChunk{
				Type:       "message_delta",
				StopReason: result.StopReason,
				Usage:      &StreamUsage{InputTokens: result.Usage.InputTokens, OutputTokens: result.Usage.OutputTokens},
			})
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("%s: read stream: %w", providerName, err)
	}

	onChunk(StreamChunk{Type: "message_stop", StopReason: result.StopReason})
	log.Printf("provider: %s stream complete model=%s stop=%s tokens_in=%d tokens_out=%d text_len=%d tool_calls=%d",
		providerName, result.Model, result.StopReason, result.Usage.InputTokens, result.Usage.OutputTokens, len(result.Text), len(result.ToolCalls))
	return result, nil
}

type ChatGPTAuthOptions struct {
	Path          string
	RefreshURL    string
	RefreshBefore time.Duration
	HTTPClient    *http.Client
	Now           func() time.Time
}

type ChatGPTAuth struct {
	path          string
	refreshURL    string
	refreshBefore time.Duration
	httpClient    *http.Client
	now           func() time.Time
	mu            sync.Mutex
}

type codexAuthFile struct {
	AuthMode     string          `json:"auth_mode"`
	OpenAIAPIKey string          `json:"OPENAI_API_KEY"`
	Tokens       codexAuthTokens `json:"tokens"`
	LastRefresh  string          `json:"last_refresh"`
}

type codexAuthTokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	AccountID    string `json:"account_id"`
}

type oauthRefreshResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
}

func NewChatGPTAuth(opts ChatGPTAuthOptions) *ChatGPTAuth {
	auth := &ChatGPTAuth{
		path:          opts.Path,
		refreshURL:    opts.RefreshURL,
		refreshBefore: opts.RefreshBefore,
		httpClient:    opts.HTTPClient,
		now:           opts.Now,
	}
	if auth.path == "" {
		auth.path = filepath.Join(userHomeDir(), ".codex", "auth.json")
	}
	if auth.refreshURL == "" {
		auth.refreshURL = "https://auth.openai.com/oauth/token"
	}
	if auth.refreshBefore == 0 {
		auth.refreshBefore = 45 * time.Minute
	}
	if auth.httpClient == nil {
		auth.httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	if auth.now == nil {
		auth.now = time.Now
	}
	return auth
}

func (a *ChatGPTAuth) Header(ctx context.Context) (string, error) {
	record, err := a.AccessToken(ctx)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(record.Tokens.AccessToken)
	if token == "" {
		return "", fmt.Errorf("chatgpt auth missing access token")
	}
	return "Bearer " + token, nil
}

func (a *ChatGPTAuth) AccessToken(ctx context.Context) (*codexAuthFile, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	record, err := a.Read()
	if err != nil {
		return nil, err
	}
	if !a.needsRefresh(record) {
		return record, nil
	}
	refreshed, err := a.refresh(ctx, record)
	if err == nil {
		return refreshed, nil
	}
	if strings.TrimSpace(record.Tokens.AccessToken) != "" {
		return record, nil
	}
	return nil, err
}

func (a *ChatGPTAuth) Read() (*codexAuthFile, error) {
	data, err := os.ReadFile(a.path)
	if err != nil {
		return nil, fmt.Errorf("read chatgpt auth file: %w", err)
	}
	var record codexAuthFile
	if err := json.Unmarshal(data, &record); err != nil {
		return nil, fmt.Errorf("decode chatgpt auth file: %w", err)
	}
	return &record, nil
}

func (a *ChatGPTAuth) needsRefresh(record *codexAuthFile) bool {
	if record == nil || strings.TrimSpace(record.Tokens.AccessToken) == "" {
		return true
	}
	if strings.TrimSpace(record.LastRefresh) == "" {
		return false
	}
	lastRefresh, err := time.Parse(time.RFC3339, record.LastRefresh)
	if err != nil {
		return false
	}
	return a.now().UTC().After(lastRefresh.UTC().Add(a.refreshBefore))
}

func (a *ChatGPTAuth) refresh(ctx context.Context, record *codexAuthFile) (*codexAuthFile, error) {
	if record == nil {
		return nil, fmt.Errorf("refresh chatgpt auth: missing auth record")
	}
	if strings.TrimSpace(record.Tokens.RefreshToken) == "" {
		return nil, fmt.Errorf("refresh chatgpt auth: missing refresh token")
	}
	refreshed, err := a.refreshViaHTTP(ctx, record)
	if err == nil {
		return refreshed, nil
	}
	if cliErr := a.refreshViaCodexCLI(ctx); cliErr == nil {
		return a.Read()
	}
	return nil, err
}

func (a *ChatGPTAuth) refreshViaHTTP(ctx context.Context, record *codexAuthFile) (*codexAuthFile, error) {
	values := url.Values{}
	values.Set("grant_type", "refresh_token")
	values.Set("refresh_token", record.Tokens.RefreshToken)
	if record.Tokens.AccountID != "" {
		values.Set("account_id", record.Tokens.AccountID)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.refreshURL, strings.NewReader(values.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("refresh chatgpt auth via http: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 32*1024))
		return nil, fmt.Errorf("refresh chatgpt auth via http: status %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var payload oauthRefreshResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode chatgpt refresh response: %w", err)
	}
	if strings.TrimSpace(payload.AccessToken) == "" {
		return nil, fmt.Errorf("chatgpt refresh response missing access_token")
	}

	record.Tokens.AccessToken = payload.AccessToken
	if payload.RefreshToken != "" {
		record.Tokens.RefreshToken = payload.RefreshToken
	}
	if payload.IDToken != "" {
		record.Tokens.IDToken = payload.IDToken
	}
	record.LastRefresh = a.now().UTC().Format(time.RFC3339)
	if err := a.write(record); err != nil {
		return nil, err
	}
	return record, nil
}

func (a *ChatGPTAuth) refreshViaCodexCLI(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "codex", "login", "status")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("refresh chatgpt auth via codex cli: %w", err)
	}
	return nil
}

func (a *ChatGPTAuth) write(record *codexAuthFile) error {
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("encode refreshed chatgpt auth file: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(a.path), 0o700); err != nil {
		return fmt.Errorf("create chatgpt auth dir: %w", err)
	}
	if err := os.WriteFile(a.path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write chatgpt auth file: %w", err)
	}
	return nil
}

func userHomeDir() string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return home
	}
	return "."
}

func readOpenAISSE(body io.Reader, fn func([]byte) error) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var data []byte
	dispatch := func() error {
		if len(data) == 0 {
			return nil
		}
		payload := bytes.TrimSpace(data)
		data = nil
		return fn(payload)
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := dispatch(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") || strings.HasPrefix(line, "event:") {
			continue
		}
		if strings.HasPrefix(line, "data:") {
			data = append(data, []byte(strings.TrimSpace(strings.TrimPrefix(line, "data:")))...)
			data = append(data, '\n')
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return dispatch()
}

func stringValue(v any) string {
	s, _ := v.(string)
	return s
}

func numberValue(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case float32:
		return float64(n)
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case json.Number:
		f, _ := n.Float64()
		return f
	case string:
		f, _ := strconv.ParseFloat(n, 64)
		return f
	default:
		return 0
	}
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
		// ChatGPT/Codex subscription models
		{
			ID:              "gpt-5.5",
			DisplayName:     "GPT-5.5",
			Provider:        "chatgpt",
			MaxOutputTokens: 65536,
		},
		{
			ID:              "gpt-5.4",
			DisplayName:     "GPT-5.4",
			Provider:        "chatgpt",
			MaxOutputTokens: 65536,
		},
		{
			ID:              "gpt-5.4-mini",
			DisplayName:     "GPT-5.4 Mini",
			Provider:        "chatgpt",
			MaxOutputTokens: 65536,
		},
	}
}

// MultiProvider holds provider backends keyed by provider name. Model
// selection stays on LLMRequest.Model; provider names must not encode model
// names.
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

	// Try ChatGPT. Register one provider; req.Model selects the model.
	if len(cfg.ChatGPTModels) > 0 {
		if p, err := NewChatGPTProviderFromEnv(cfg.ChatGPTModels[0], cfg.ChatGPTReasoningEffort); err == nil {
			mp.Register("chatgpt", p)
			log.Printf("provider: resolved chatgpt (seed_model=%s reasoning=%s)", p.modelID, p.reasoning)
		}
	}

	// Try Bedrock. Register one provider; req.Model selects the model.
	if os.Getenv("AWS_BEARER_TOKEN_BEDROCK") != "" && len(cfg.BedrockModels) > 0 {
		if p, err := NewBedrockProviderFromEnv(cfg.BedrockModels[0]); err == nil {
			mp.Register("bedrock", p)
			log.Printf("provider: resolved bedrock (region=%s seed_model=%s)", p.region, redactModel(p.modelID))
		}
	}

	// Try Z.AI. Register one provider; req.Model selects the model.
	if os.Getenv("ZAI_API_KEY") != "" && len(cfg.ZAIModels) > 0 {
		if p, err := NewZAIProviderFromEnv(cfg.ZAIModels[0]); err == nil {
			mp.Register("zai", p)
			log.Printf("provider: resolved zai (seed_model=%s)", p.modelID)
		}
	}

	// Try Fireworks. Register one provider; req.Model selects the model.
	if os.Getenv("FIREWORKS_API_KEY") != "" && len(cfg.FireworksModels) > 0 {
		if p, err := NewFireworksProviderFromEnv(cfg.FireworksModels[0]); err == nil {
			mp.Register("fireworks", p)
			log.Printf("provider: resolved fireworks (seed_model=%s)", p.modelID)
		}
	}

	return mp
}

// ResolveProvider resolves the explicitly selected provider for direct
// sandbox mode. It does not guess or fall back; callers that want a provider
// must set ProviderConfig.SelectedProvider.
func ResolveProvider(cfg ProviderConfig) (Provider, error) {
	selected := strings.TrimSpace(cfg.SelectedProvider)
	if selected == "" {
		return nil, nil
	}
	mp := ResolveAll(cfg)
	p := mp.Get(selected)
	if p == nil {
		return nil, fmt.Errorf("resolve provider: selected provider %q is not configured", selected)
	}
	return p, nil
}
