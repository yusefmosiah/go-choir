package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
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
	gatewayURL string      // base URL of the gateway service
	token      string      // sandbox credential token
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
