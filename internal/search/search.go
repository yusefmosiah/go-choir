package search

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

type SearchResult struct {
	Title       string  `json:"title"`
	URL         string  `json:"url"`
	Snippet     string  `json:"snippet"`
	PublishedAt string  `json:"published_at,omitempty"`
	Score       float64 `json:"score,omitempty"`
}

type SearchResponse struct {
	Results  []SearchResult `json:"results"`
	Provider string         `json:"provider"`
	Query    string         `json:"query"`
}

type SearchRequest struct {
	Query      string `json:"query"`
	MaxResults int    `json:"max_results,omitempty"`
}

type SearchProvider interface {
	Name() string
	Search(ctx context.Context, query string, maxResults int) ([]SearchResult, error)
	IsAvailable() bool
}

type SearchClient struct {
	providers []SearchProvider
	counter   atomic.Int64
}

func NewSearchClient() *SearchClient {
	providers := []SearchProvider{
		&TavilyProvider{},
		&BraveProvider{},
		&ExaProvider{},
		&SerperProvider{},
	}
	var available []SearchProvider
	for _, p := range providers {
		if p.IsAvailable() {
			available = append(available, p)
		}
	}
	return &SearchClient{providers: available}
}

func (c *SearchClient) Search(ctx context.Context, req SearchRequest) (*SearchResponse, error) {
	if req.Query == "" {
		return nil, fmt.Errorf("query is required")
	}
	maxResults := req.MaxResults
	if maxResults <= 0 {
		maxResults = 10
	}
	if maxResults > 50 {
		maxResults = 50
	}
	if len(c.providers) == 0 {
		return nil, fmt.Errorf("no search providers available (set TAVILY_API_KEY, BRAVE_API_KEY, EXA_API_KEY, or SERPER_API_KEY)")
	}
	start := int(c.counter.Add(1)-1) % len(c.providers)
	var lastErr error
	for i := range c.providers {
		idx := (start + i) % len(c.providers)
		provider := c.providers[idx]
		results, err := provider.Search(ctx, req.Query, maxResults)
		if err == nil {
			return &SearchResponse{
				Results:  results,
				Provider: provider.Name(),
				Query:    req.Query,
			}, nil
		}
		lastErr = fmt.Errorf("%s: %w", provider.Name(), err)
	}
	return nil, fmt.Errorf("all search providers failed: %w", lastErr)
}

func (c *SearchClient) AvailableProviders() []string {
	names := make([]string, len(c.providers))
	for i, p := range c.providers {
		names[i] = p.Name()
	}
	return names
}

type TavilyProvider struct {
	httpClient *http.Client
}

func (p *TavilyProvider) Name() string { return "tavily" }
func (p *TavilyProvider) IsAvailable() bool {
	return os.Getenv("TAVILY_API_KEY") != ""
}
func (p *TavilyProvider) http() *http.Client {
	if p.httpClient != nil {
		return p.httpClient
	}
	return &http.Client{Timeout: 30 * time.Second}
}
func (p *TavilyProvider) Search(ctx context.Context, query string, maxResults int) ([]SearchResult, error) {
	apiKey := os.Getenv("TAVILY_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("TAVILY_API_KEY not set")
	}
	body := map[string]any{
		"query":               query,
		"max_results":         maxResults,
		"search_depth":        "basic",
		"include_answer":      false,
		"include_raw_content": false,
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.tavily.com/search", strings.NewReader(string(bodyBytes)))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := p.http().Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	bodyBytes, err = io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("status %s: %s", resp.Status, truncateError(bodyBytes))
	}
	return parseTavilyResults(bodyBytes)
}

func parseTavilyResults(data []byte) ([]SearchResult, error) {
	var result struct {
		Results []struct {
			Title         string  `json:"title"`
			URL           string  `json:"url"`
			Content       string  `json:"content"`
			PublishedDate string  `json:"published_date"`
			Score         float64 `json:"score"`
		} `json:"results"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	results := make([]SearchResult, 0, len(result.Results))
	for _, r := range result.Results {
		if r.URL == "" {
			continue
		}
		results = append(results, SearchResult{
			Title:       r.Title,
			URL:         r.URL,
			Snippet:     r.Content,
			PublishedAt: r.PublishedDate,
			Score:       r.Score,
		})
	}
	return results, nil
}

type BraveProvider struct {
	httpClient *http.Client
}

func (p *BraveProvider) Name() string { return "brave" }
func (p *BraveProvider) IsAvailable() bool {
	return os.Getenv("BRAVE_API_KEY") != ""
}
func (p *BraveProvider) http() *http.Client {
	if p.httpClient != nil {
		return p.httpClient
	}
	return &http.Client{Timeout: 30 * time.Second}
}
func (p *BraveProvider) Search(ctx context.Context, query string, maxResults int) ([]SearchResult, error) {
	apiKey := os.Getenv("BRAVE_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("BRAVE_API_KEY not set")
	}
	u := fmt.Sprintf("https://api.search.brave.com/res/v1/web/search?q=%s&count=%d", url.QueryEscape(query), maxResults)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", apiKey)
	resp, err := p.http().Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("status %s: %s", resp.Status, truncateError(bodyBytes))
	}
	return parseBraveResults(bodyBytes)
}

func parseBraveResults(data []byte) ([]SearchResult, error) {
	var result struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
				Age         string `json:"age"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	results := make([]SearchResult, 0, len(result.Web.Results))
	for _, r := range result.Web.Results {
		if r.URL == "" {
			continue
		}
		results = append(results, SearchResult{
			Title:       r.Title,
			URL:         r.URL,
			Snippet:     r.Description,
			PublishedAt: r.Age,
		})
	}
	return results, nil
}

type ExaProvider struct {
	httpClient *http.Client
}

func (p *ExaProvider) Name() string { return "exa" }
func (p *ExaProvider) IsAvailable() bool {
	return os.Getenv("EXA_API_KEY") != ""
}
func (p *ExaProvider) http() *http.Client {
	if p.httpClient != nil {
		return p.httpClient
	}
	return &http.Client{Timeout: 30 * time.Second}
}
func (p *ExaProvider) Search(ctx context.Context, query string, maxResults int) ([]SearchResult, error) {
	apiKey := os.Getenv("EXA_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("EXA_API_KEY not set")
	}
	body := map[string]any{
		"query":      query,
		"numResults": maxResults,
		"type":       "auto",
		"contents": map[string]any{
			"text": true,
		},
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.exa.ai/search", strings.NewReader(string(bodyBytes)))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	resp, err := p.http().Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	bodyBytes, err = io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("status %s: %s", resp.Status, truncateError(bodyBytes))
	}
	return parseExaResults(bodyBytes)
}

func parseExaResults(data []byte) ([]SearchResult, error) {
	var result struct {
		Results []struct {
			Title         string   `json:"title"`
			URL           string   `json:"url"`
			Text          string   `json:"text"`
			PublishedDate string   `json:"publishedDate"`
			Score         float64  `json:"score"`
			Highlights    []string `json:"highlights"`
		} `json:"results"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	results := make([]SearchResult, 0, len(result.Results))
	for _, r := range result.Results {
		if r.URL == "" {
			continue
		}
		snippet := r.Text
		if len(r.Highlights) > 0 {
			snippet = strings.Join(r.Highlights, " ")
		}
		results = append(results, SearchResult{
			Title:       r.Title,
			URL:         r.URL,
			Snippet:     snippet,
			PublishedAt: r.PublishedDate,
			Score:       r.Score,
		})
	}
	return results, nil
}

type SerperProvider struct {
	httpClient *http.Client
}

func (p *SerperProvider) Name() string { return "serper" }
func (p *SerperProvider) IsAvailable() bool {
	return os.Getenv("SERPER_API_KEY") != ""
}
func (p *SerperProvider) http() *http.Client {
	if p.httpClient != nil {
		return p.httpClient
	}
	return &http.Client{Timeout: 30 * time.Second}
}
func (p *SerperProvider) Search(ctx context.Context, query string, maxResults int) ([]SearchResult, error) {
	apiKey := os.Getenv("SERPER_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("SERPER_API_KEY not set")
	}
	body := map[string]any{
		"q":   query,
		"num": maxResults,
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://google.serper.dev/search", strings.NewReader(string(bodyBytes)))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-KEY", apiKey)
	resp, err := p.http().Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	bodyBytes, err = io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("status %s: %s", resp.Status, truncateError(bodyBytes))
	}
	return parseSerperResults(bodyBytes)
}

func parseSerperResults(data []byte) ([]SearchResult, error) {
	var result struct {
		Organic []struct {
			Title   string `json:"title"`
			Link    string `json:"link"`
			Snippet string `json:"snippet"`
			Date    string `json:"date"`
		} `json:"organic"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	results := make([]SearchResult, 0, len(result.Organic))
	for _, r := range result.Organic {
		if r.Link == "" {
			continue
		}
		results = append(results, SearchResult{
			Title:       r.Title,
			URL:         r.Link,
			Snippet:     r.Snippet,
			PublishedAt: r.Date,
		})
	}
	return results, nil
}

func truncateError(data []byte) string {
	s := string(data)
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}
