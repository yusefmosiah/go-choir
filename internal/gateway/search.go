// Package gateway implements web search functionality with multi-provider
// rotation and fallback. Supports Tavily, Brave, Exa, and Serper search APIs.
//
// The SearchClient uses round-robin rotation across available providers,
// automatically falling back to the next provider if one fails.
package gateway

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

// SearchResult represents a single search result item.
type SearchResult struct {
	// Title is the page title.
	Title string `json:"title"`

	// URL is the result URL.
	URL string `json:"url"`

	// Snippet is a text excerpt or description.
	Snippet string `json:"snippet"`

	// PublishedAt is the optional publication date (ISO 8601 format).
	PublishedAt string `json:"published_at,omitempty"`

	// Score is the optional relevance score.
	Score float64 `json:"score,omitempty"`
}

// SearchResponse is the unified response from the search endpoint.
type SearchResponse struct {
	// Results is the list of search results.
	Results []SearchResult `json:"results"`

	// Provider identifies which search provider served this request.
	Provider string `json:"provider"`

	// Query is the original search query.
	Query string `json:"query"`
}

// SearchRequest is the incoming search request payload.
type SearchRequest struct {
	// Query is the search query string (required).
	Query string `json:"query"`

	// MaxResults is the maximum number of results (default 10, max 50).
	MaxResults int `json:"max_results,omitempty"`
}

// SearchProvider is the interface for search API implementations.
type SearchProvider interface {
	// Name returns the provider identifier (e.g., "tavily", "brave").
	Name() string

	// Search executes a search query and returns normalized results.
	// Returns an error if the search fails or the API key is invalid.
	Search(ctx context.Context, query string, maxResults int) ([]SearchResult, error)

	// IsAvailable returns true if the provider has credentials configured.
	IsAvailable() bool
}

// SearchClient provides round-robin rotation across multiple search providers
// with automatic fallback on failure.
type SearchClient struct {
	providers []SearchProvider
	counter   atomic.Int64
}

// NewSearchClient creates a SearchClient with all available providers.
// Providers are registered in priority order; the client uses round-robin
// rotation starting from the current position, falling back to subsequent
// providers if one fails.
func NewSearchClient() *SearchClient {
	providers := []SearchProvider{
		&TavilyProvider{},
		&BraveProvider{},
		&ExaProvider{},
		&SerperProvider{},
	}

	// Filter to only available providers.
	var available []SearchProvider
	for _, p := range providers {
		if p.IsAvailable() {
			available = append(available, p)
		}
	}

	return &SearchClient{
		providers: available,
	}
}

// Search executes a search query using round-robin rotation across providers.
// It tries each available provider in sequence until one succeeds.
// Returns an error if all providers fail.
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

	// Round-robin: get next starting position atomically.
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
		// Continue to next provider (fallback).
	}

	return nil, fmt.Errorf("all search providers failed: %w", lastErr)
}

// AvailableProviders returns the names of configured search providers.
func (c *SearchClient) AvailableProviders() []string {
	names := make([]string, len(c.providers))
	for i, p := range c.providers {
		names[i] = p.Name()
	}
	return names
}

// --- Tavily Provider ---

// TavilyProvider implements search using the Tavily API.
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
		"query":       query,
		"max_results": maxResults,
		"search_depth": "basic",
		"include_answer": false,
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

// --- Brave Provider ---

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

// --- Exa Provider ---

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
			Title         string  `json:"title"`
			URL           string  `json:"url"`
			Text          string  `json:"text"`
			PublishedDate string  `json:"publishedDate"`
			Score         float64 `json:"score"`
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
		
		// Use highlights if available, otherwise text.
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

// --- Serper Provider ---

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

// --- Helpers ---

// truncateError limits error message length to avoid leaking large responses.
func truncateError(data []byte) string {
	s := string(data)
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}
