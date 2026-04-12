package gateway

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/yusefmosiah/go-choir/internal/provider"
)

// GatewayClient is a provider.Provider implementation that routes LLM calls
// through the gateway service instead of calling upstream providers directly.
// The sandbox runtime uses this client when the gateway is the configured
// provider boundary.
//
// The client authenticates to the gateway using a sandbox credential issued
// by the gateway's identity registry. The gateway injects host-side provider
// credentials before calling the upstream (VAL-GATEWAY-004).
type GatewayClient struct {
	gatewayURL string // base URL of the gateway service
	token      string // sandbox credential token
	httpClient *http.Client
}

// NewGatewayClient creates a GatewayClient pointing at the given gateway URL
// with the given sandbox credential token.
func NewGatewayClient(gatewayURL, token string) *GatewayClient {
	return &GatewayClient{
		gatewayURL: gatewayURL,
		token:      token,
		httpClient: &http.Client{Timeout: 130 * time.Second},
	}
}

// Name returns "gateway" for the gateway client provider.
func (c *GatewayClient) Name() string { return "gateway" }

// IsReal returns true because the gateway routes to real upstream providers.
func (c *GatewayClient) IsReal() bool { return true }

// Call sends the LLM request through the gateway. The gateway authenticates
// the sandbox caller, injects host-side credentials, calls the upstream
// provider, and returns the response with sanitized errors.
func (c *GatewayClient) Call(ctx context.Context, req provider.LLMRequest) (*provider.LLMResponse, error) {
	endpoint := c.gatewayURL + "/provider/v1/inference"

	// Build the gateway request payload.
	gwReq := ProviderRequest{
		Provider:  "", // let the gateway decide
		Model:     req.Model,
		Messages:  req.Messages,
		System:    req.System,
		Tools:     req.Tools,
		MaxTokens: req.MaxTokens,
		Stream:    false,
	}

	data, err := json.Marshal(gwReq)
	if err != nil {
		return nil, fmt.Errorf("gateway client: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("gateway client: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("gateway client: http call: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("gateway client: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		// Sanitize the error response.
		var errResp ErrorResponse
		if err := json.Unmarshal(body, &errResp); err == nil && errResp.Error != "" {
			return nil, fmt.Errorf("gateway client: %s", errResp.Error)
		}
		return nil, fmt.Errorf("gateway client: status %s (sanitized)", resp.Status)
	}

	var gwResp ProviderResponse
	if err := json.Unmarshal(body, &gwResp); err != nil {
		return nil, fmt.Errorf("gateway client: decode response: %w", err)
	}

	// Convert gateway response to provider LLMResponse.
	result := &provider.LLMResponse{
		ID:           gwResp.ID,
		Text:         gwResp.Text,
		Model:        gwResp.Model,
		StopReason:   gwResp.StopReason,
		Usage:        gwResp.Usage,
		ToolCalls:    gwResp.ToolCalls,
		ProviderName: gwResp.ProviderName,
	}

	log.Printf("gateway client: inference succeeded (provider=%s tokens=%d+%d text_len=%d)",
		result.ProviderName, result.Usage.InputTokens, result.Usage.OutputTokens, len(result.Text))

	return result, nil
}

// Stream sends the LLM request through the gateway with streaming enabled.
// The gateway returns SSE chunks that are forwarded to the onChunk callback.
// Returns the accumulated LLMResponse on completion.
//
// The stream reads SSE events from the gateway response body. Each event is
// parsed and forwarded to the onChunk callback. The stream terminates when
// a "[DONE]" marker is received or the connection closes.
func (c *GatewayClient) Stream(ctx context.Context, req provider.LLMRequest, onChunk func(provider.StreamChunk)) (*provider.LLMResponse, error) {
	endpoint := c.gatewayURL + "/provider/v1/inference"

	// Build the gateway request payload with streaming enabled.
	gwReq := ProviderRequest{
		Provider:  "", // let the gateway decide
		Model:     req.Model,
		Messages:  req.Messages,
		System:    req.System,
		Tools:     req.Tools,
		MaxTokens: req.MaxTokens,
		Stream:    true,
	}

	data, err := json.Marshal(gwReq)
	if err != nil {
		return nil, fmt.Errorf("gateway client: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("gateway client: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.token)
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("gateway client: http call: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		var errResp ErrorResponse
		if err := json.Unmarshal(body, &errResp); err == nil && errResp.Error != "" {
			return nil, fmt.Errorf("gateway client: %s", errResp.Error)
		}
		return nil, fmt.Errorf("gateway client: status %s (sanitized)", resp.Status)
	}

	// Parse the SSE stream from the gateway response.
	return parseGatewaySSE(resp.Body, onChunk)
}

// parseGatewaySSE reads SSE events from the gateway response body and
// forwards parsed StreamChunk values to the onChunk callback. It returns
// the accumulated LLMResponse when the stream completes.
func parseGatewaySSE(body io.Reader, onChunk func(provider.StreamChunk)) (*provider.LLMResponse, error) {
	var accumulated provider.LLMResponse
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()

		// Skip empty lines and comments.
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}

		// Parse SSE data lines.
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")

		// Check for stream termination marker.
		if data == "[DONE]" {
			break
		}

		// Try to parse as an error event.
		var errCheck struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal([]byte(data), &errCheck); err == nil && errCheck.Error != "" {
			return nil, fmt.Errorf("gateway client: stream error: %s", errCheck.Error)
		}

		// Parse as a StreamChunk.
		var chunk provider.StreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			log.Printf("gateway client: unmarshal chunk: %v (data=%q)", err, data)
			continue
		}

		// Accumulate response fields from the stream.
		if chunk.ID != "" {
			accumulated.ID = chunk.ID
		}
		if chunk.Model != "" {
			accumulated.Model = chunk.Model
		}
		if chunk.Delta != "" {
			accumulated.Text += chunk.Delta
		}
		if chunk.StopReason != "" {
			accumulated.StopReason = chunk.StopReason
		}
		if chunk.Usage != nil {
			accumulated.Usage.InputTokens = chunk.Usage.InputTokens
			accumulated.Usage.OutputTokens = chunk.Usage.OutputTokens
		}

		// Forward to the caller.
		onChunk(chunk)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("gateway client: read stream: %w", err)
	}

	return &accumulated, nil
}
