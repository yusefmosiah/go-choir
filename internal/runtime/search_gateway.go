package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type gatewaySearchClient struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

func newGatewaySearchClientFromEnv() webSearchClient {
	baseURL := strings.TrimSpace(os.Getenv("RUNTIME_GATEWAY_URL"))
	if baseURL == "" {
		baseURL = strings.TrimSpace(os.Getenv("PROXY_VMCTL_URL"))
	}
	token := strings.TrimSpace(os.Getenv("RUNTIME_GATEWAY_TOKEN"))
	if baseURL == "" || token == "" {
		return nil
	}
	return &gatewaySearchClient{
		baseURL:    baseURL,
		token:      token,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *gatewaySearchClient) Search(ctx context.Context, query string, maxResults int) (*webSearchResponse, error) {
	payload := map[string]any{
		"query": query,
	}
	if maxResults > 0 {
		payload["max_results"] = maxResults
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("gateway search: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/provider/v1/search", bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("gateway search: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gateway search: http call: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("gateway search: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		var errResp struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(body, &errResp); err == nil && strings.TrimSpace(errResp.Error) != "" {
			return nil, fmt.Errorf("gateway search: %s", errResp.Error)
		}
		return nil, fmt.Errorf("gateway search: status %s", resp.Status)
	}

	var result webSearchResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("gateway search: decode response: %w", err)
	}
	return &result, nil
}
