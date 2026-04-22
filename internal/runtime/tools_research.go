package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type webSearchClient interface {
	Search(ctx context.Context, query string, maxResults int) (*webSearchResponse, error)
}

type webSearchResponse struct {
	Query    string           `json:"query"`
	Provider string           `json:"provider"`
	Results  []map[string]any `json:"results"`
}

func RegisterResearchTools(registry *ToolRegistry, searchClient webSearchClient, httpClient *http.Client) error {
	for _, tool := range []Tool{
		newWebSearchTool(searchClient),
		newFetchURLTool(httpClient),
	} {
		if err := registry.Register(tool); err != nil {
			return err
		}
	}
	return nil
}

func newWebSearchTool(searchClient webSearchClient) Tool {
	type args struct {
		Query      string `json:"query"`
		MaxResults int    `json:"max_results,omitempty"`
	}
	return Tool{
		Name:        "web_search",
		Description: "Search the web using the configured multi-provider search client.",
		Parameters: jsonSchemaObject(map[string]any{
			"query":       map[string]any{"type": "string"},
			"max_results": map[string]any{"type": "integer", "minimum": 1, "maximum": 50},
		}, []string{"query"}, false),
		Func: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var in args
			if err := json.Unmarshal(raw, &in); err != nil {
				return "", fmt.Errorf("decode web_search args: %w", err)
			}
			if searchClient == nil {
				return "", fmt.Errorf("search client not configured")
			}
			if strings.TrimSpace(in.Query) == "" {
				return "", fmt.Errorf("query must not be empty")
			}
			resp, err := searchClient.Search(ctx, strings.TrimSpace(in.Query), in.MaxResults)
			if err != nil {
				return "", err
			}
			return toolResultJSON(map[string]any{
				"query":    resp.Query,
				"provider": resp.Provider,
				"results":  resp.Results,
			})
		},
	}
}

func newFetchURLTool(httpClient *http.Client) Tool {
	type args struct {
		URL      string `json:"url"`
		MaxChars int    `json:"max_chars,omitempty"`
	}
	return Tool{
		Name:        "fetch_url",
		Description: "Fetch a URL and return response metadata plus a truncated content excerpt.",
		Parameters: jsonSchemaObject(map[string]any{
			"url":       map[string]any{"type": "string"},
			"max_chars": map[string]any{"type": "integer", "minimum": 1},
		}, []string{"url"}, false),
		Func: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var in args
			if err := json.Unmarshal(raw, &in); err != nil {
				return "", fmt.Errorf("decode fetch_url args: %w", err)
			}
			target := strings.TrimSpace(in.URL)
			if target == "" {
				return "", fmt.Errorf("url must not be empty")
			}
			client := httpClient
			if client == nil {
				client = &http.Client{Timeout: 30 * time.Second}
			}
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
			if err != nil {
				return "", err
			}
			resp, err := client.Do(req)
			if err != nil {
				return "", err
			}
			defer func() { _ = resp.Body.Close() }()

			data, err := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
			if err != nil {
				return "", err
			}
			maxChars := in.MaxChars
			if maxChars <= 0 {
				maxChars = 12000
			}
			content := strings.TrimSpace(string(data))
			if len(content) > maxChars {
				content = content[:maxChars]
			}
			return toolResultJSON(map[string]any{
				"url":            target,
				"status_code":    resp.StatusCode,
				"content_type":   resp.Header.Get("Content-Type"),
				"content_length": len(data),
				"content":        content,
			})
		},
	}
}
