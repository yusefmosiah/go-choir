package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/yusefmosiah/go-choir/internal/gateway/searchplane"
)

// --- Mock Search Provider for Testing ---

type mockSearchProvider struct {
	name        string
	available   bool
	searchFunc  func(ctx context.Context, query string, maxResults int) ([]SearchResult, error)
	searchCount int
}

func (m *mockSearchProvider) Name() string      { return m.name }
func (m *mockSearchProvider) IsAvailable() bool { return m.available }
func (m *mockSearchProvider) Search(ctx context.Context, query string, maxResults int) ([]SearchResult, error) {
	m.searchCount++
	if m.searchFunc != nil {
		return m.searchFunc(ctx, query, maxResults)
	}
	return nil, errors.New("mock: no search func")
}

// --- SearchClient Tests ---

func testSearchClient(providers []SearchProvider, providersPerQuery int) *SearchClient {
	if providersPerQuery <= 0 {
		providersPerQuery = 1
	}
	return &SearchClient{
		providers:         providers,
		providersPerQuery: providersPerQuery,
		healthStore:       searchplane.NewMemoryHealthStore(),
		planeConfig: searchplane.Config{
			ProvidersPerQuery: providersPerQuery,
			MinMergedResults:  1,
			MaxWaves:          1,
			RequestTimeout:    5 * time.Second,
		},
	}
}

func TestSearchClient_NoProviders(t *testing.T) {
	client := testSearchClient([]SearchProvider{}, 1)
	req := SearchRequest{Query: "test", MaxResults: 5}

	_, err := client.Search(context.Background(), req)
	if err == nil {
		t.Fatal("expected error with no providers")
	}
	if !strings.Contains(err.Error(), "no search providers available") {
		t.Errorf("expected 'no search providers available' error, got: %v", err)
	}
}

func TestSearchClient_EmptyQuery(t *testing.T) {
	mock := &mockSearchProvider{
		name:      "mock",
		available: true,
		searchFunc: func(ctx context.Context, query string, maxResults int) ([]SearchResult, error) {
			return []SearchResult{{Title: "Test", URL: "http://example.com", Snippet: "test"}}, nil
		},
	}
	client := testSearchClient([]SearchProvider{mock}, 1)

	req := SearchRequest{Query: "", MaxResults: 5}
	_, err := client.Search(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for empty query")
	}
	if !strings.Contains(err.Error(), "query is required") {
		t.Errorf("expected 'query is required' error, got: %v", err)
	}
}

func TestSearchClient_Rotation(t *testing.T) {
	mock1 := &mockSearchProvider{
		name:      "mock1",
		available: true,
		searchFunc: func(ctx context.Context, query string, maxResults int) ([]SearchResult, error) {
			return []SearchResult{{Title: "Result1", URL: "http://example.com/1", Snippet: "result1"}}, nil
		},
	}
	mock2 := &mockSearchProvider{
		name:      "mock2",
		available: true,
		searchFunc: func(ctx context.Context, query string, maxResults int) ([]SearchResult, error) {
			return []SearchResult{{Title: "Result2", URL: "http://example.com/2", Snippet: "result2"}}, nil
		},
	}

	client := testSearchClient([]SearchProvider{mock1, mock2}, 1)

	// First request should go to mock1 (counter starts at 0, so start=0)
	resp1, err := client.Search(context.Background(), SearchRequest{Query: "test", MaxResults: 5})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp1.Provider != "mock1" {
		t.Errorf("first request: expected provider mock1, got %s", resp1.Provider)
	}

	// Second request should go to mock2 (counter now 1, so start=1)
	resp2, err := client.Search(context.Background(), SearchRequest{Query: "test", MaxResults: 5})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp2.Provider != "mock2" {
		t.Errorf("second request: expected provider mock2, got %s", resp2.Provider)
	}

	// Third request should wrap around to mock1 (counter now 2, so start=0)
	resp3, err := client.Search(context.Background(), SearchRequest{Query: "test", MaxResults: 5})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp3.Provider != "mock1" {
		t.Errorf("third request: expected provider mock1, got %s", resp3.Provider)
	}
}

func TestSearchClient_DiversifiesAcrossProviders(t *testing.T) {
	mock1 := &mockSearchProvider{
		name:      "mock1",
		available: true,
		searchFunc: func(ctx context.Context, query string, maxResults int) ([]SearchResult, error) {
			return []SearchResult{{Title: "Result1", URL: "http://example.com/1", Snippet: "result1"}}, nil
		},
	}
	mock2 := &mockSearchProvider{
		name:      "mock2",
		available: true,
		searchFunc: func(ctx context.Context, query string, maxResults int) ([]SearchResult, error) {
			return []SearchResult{{Title: "Result2", URL: "http://example.com/2", Snippet: "result2"}}, nil
		},
	}
	mock3 := &mockSearchProvider{
		name:      "mock3",
		available: true,
		searchFunc: func(ctx context.Context, query string, maxResults int) ([]SearchResult, error) {
			return []SearchResult{{Title: "Result3", URL: "http://example.com/3", Snippet: "result3"}}, nil
		},
	}

	client := testSearchClient([]SearchProvider{mock1, mock2, mock3}, 2)

	resp1, err := client.Search(context.Background(), SearchRequest{Query: "test", MaxResults: 10})
	if err != nil {
		t.Fatalf("first search: %v", err)
	}
	if got, want := strings.Join(resp1.Providers, ","), "mock1,mock2"; got != want {
		t.Fatalf("first providers = %q, want %q", got, want)
	}
	if len(resp1.Results) != 2 {
		t.Fatalf("first results = %d, want 2", len(resp1.Results))
	}
	if len(resp1.Attempts) != 2 {
		t.Fatalf("first attempts = %d, want 2", len(resp1.Attempts))
	}
	if resp1.Results[0].Provider != "mock1" || resp1.Results[1].Provider != "mock2" {
		t.Fatalf("result providers = %#v, want mock1/mock2", resp1.Results)
	}

	resp2, err := client.Search(context.Background(), SearchRequest{Query: "test", MaxResults: 10})
	if err != nil {
		t.Fatalf("second search: %v", err)
	}
	if got, want := strings.Join(resp2.Providers, ","), "mock2,mock3"; got != want {
		t.Fatalf("second providers = %q, want %q", got, want)
	}
}

func TestSearchClient_MergesMaxResultsAcrossProviderFanout(t *testing.T) {
	mock1 := &mockSearchProvider{
		name:      "mock1",
		available: true,
		searchFunc: func(ctx context.Context, query string, maxResults int) ([]SearchResult, error) {
			return []SearchResult{
				{Title: "Result 1A", URL: "http://example.com/1a", Snippet: "result1a"},
				{Title: "Result 1B", URL: "http://example.com/1b", Snippet: "result1b"},
				{Title: "Result 1C", URL: "http://example.com/1c", Snippet: "result1c"},
				{Title: "Result 1D", URL: "http://example.com/1d", Snippet: "result1d"},
			}, nil
		},
	}
	mock2 := &mockSearchProvider{
		name:      "mock2",
		available: true,
		searchFunc: func(ctx context.Context, query string, maxResults int) ([]SearchResult, error) {
			return []SearchResult{
				{Title: "Result 2A", URL: "http://example.com/2a", Snippet: "result2a"},
				{Title: "Result 2B", URL: "http://example.com/2b", Snippet: "result2b"},
				{Title: "Result 2C", URL: "http://example.com/2c", Snippet: "result2c"},
				{Title: "Result 2D", URL: "http://example.com/2d", Snippet: "result2d"},
			}, nil
		},
	}

	client := testSearchClient([]SearchProvider{mock1, mock2}, 2)
	resp, err := client.Search(context.Background(), SearchRequest{Query: "test", MaxResults: 4})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if got, want := strings.Join(resp.Providers, ","), "mock1,mock2"; got != want {
		t.Fatalf("providers = %q, want %q", got, want)
	}
	if len(resp.Results) != 4 {
		t.Fatalf("results = %d, want 4", len(resp.Results))
	}
	gotProviders := []string{
		resp.Results[0].Provider,
		resp.Results[1].Provider,
		resp.Results[2].Provider,
		resp.Results[3].Provider,
	}
	if strings.Join(gotProviders, ",") != "mock1,mock2,mock1,mock2" {
		t.Fatalf("result providers = %v, want interleaved mock1/mock2", gotProviders)
	}
}

func TestSearchClient_Fallback(t *testing.T) {
	failProvider := &mockSearchProvider{
		name:      "fail",
		available: true,
		searchFunc: func(ctx context.Context, query string, maxResults int) ([]SearchResult, error) {
			return nil, errors.New("provider failed")
		},
	}
	successProvider := &mockSearchProvider{
		name:      "success",
		available: true,
		searchFunc: func(ctx context.Context, query string, maxResults int) ([]SearchResult, error) {
			return []SearchResult{{Title: "Success", URL: "http://example.com", Snippet: "success"}}, nil
		},
	}

	client := testSearchClient([]SearchProvider{failProvider, successProvider}, 2)

	resp, err := client.Search(context.Background(), SearchRequest{Query: "test", MaxResults: 5})
	if err != nil {
		t.Fatalf("expected success through fallback, got error: %v", err)
	}
	if resp.Provider != "success" {
		t.Errorf("expected provider 'success' after fallback, got %s", resp.Provider)
	}
	if len(resp.Attempts) != 2 {
		t.Fatalf("attempts = %d, want failed provider plus success provider", len(resp.Attempts))
	}
	var sawFail, sawSuccess bool
	for _, attempt := range resp.Attempts {
		switch {
		case attempt.Provider == "fail" && attempt.Status == "error":
			sawFail = true
		case attempt.Provider == "success" && attempt.Status == "success":
			sawSuccess = true
		}
	}
	if !sawFail || !sawSuccess {
		t.Fatalf("attempts = %+v, want fail/error and success/success", resp.Attempts)
	}
	if len(resp.Results) != 1 {
		t.Errorf("expected 1 result, got %d", len(resp.Results))
	}
}

func TestSearchClient_CoolsDownQuotaLimitedProviders(t *testing.T) {
	quotaProvider := &mockSearchProvider{
		name:      "quota",
		available: true,
		searchFunc: func(ctx context.Context, query string, maxResults int) ([]SearchResult, error) {
			return nil, errors.New("status 402 Payment Required: NO_MORE_CREDITS")
		},
	}
	successProvider := &mockSearchProvider{
		name:      "success",
		available: true,
		searchFunc: func(ctx context.Context, query string, maxResults int) ([]SearchResult, error) {
			return []SearchResult{{Title: "Success", URL: "http://example.com", Snippet: "success"}}, nil
		},
	}
	client := testSearchClient([]SearchProvider{quotaProvider, successProvider}, 2)
	client.planeConfig.MaxWaves = 2

	first, err := client.Search(context.Background(), SearchRequest{Query: "test", MaxResults: 5})
	if err != nil {
		t.Fatalf("first search: %v", err)
	}
	if first.Attempts[0].Status != "quota_limited" {
		t.Fatalf("first attempt status = %q, want quota_limited", first.Attempts[0].Status)
	}
	if quotaProvider.searchCount != 1 {
		t.Fatalf("quota provider search count after first = %d, want 1", quotaProvider.searchCount)
	}

	second, err := client.Search(context.Background(), SearchRequest{Query: "test", MaxResults: 5})
	if err != nil {
		t.Fatalf("second search: %v", err)
	}
	foundCooldown := false
	for _, attempt := range second.Attempts {
		if attempt.Provider == "quota" && attempt.Status == "cooling_down" {
			foundCooldown = true
		}
	}
	if !foundCooldown {
		t.Fatalf("second attempts = %+v, want quota cooling_down attempt", second.Attempts)
	}
	if quotaProvider.searchCount != 1 {
		t.Fatalf("quota provider search count after cooldown = %d, want still 1", quotaProvider.searchCount)
	}
}

func TestParseSearXNGResults(t *testing.T) {
	raw := []byte(`{
		"query": "test query",
		"number_of_results": 2,
		"results": [
			{
				"url": "https://example.com/one",
				"title": "Example One",
				"content": "First result content",
				"publishedDate": "2025-01-15T10:30:00",
				"engines": ["google", "bing"],
				"score": 8.5
			},
			{
				"url": "https://example.com/two",
				"title": "Example Two",
				"content": "Second result content",
				"publishedDate": null,
				"engines": ["duckduckgo"],
				"score": 3.2
			}
		]
	}`)

	results, err := parseSearXNGResults(raw, 10)
	if err != nil {
		t.Fatalf("parseSearXNGResults: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2", len(results))
	}
	if results[0].Title != "Example One" || results[0].URL != "https://example.com/one" {
		t.Fatalf("result[0] = %+v, want parsed title/url", results[0])
	}
	if results[0].Snippet != "First result content" {
		t.Fatalf("snippet = %q, want content", results[0].Snippet)
	}
	if results[0].Score != 8.5 {
		t.Fatalf("score = %v, want 8.5", results[0].Score)
	}
	if results[1].Title != "Example Two" {
		t.Fatalf("result[1] title = %q, want Example Two", results[1].Title)
	}
}

func TestParseSearXNGResults_MaxResults(t *testing.T) {
	raw := []byte(`{
		"results": [
			{"url": "https://a.com", "title": "A", "content": "a"},
			{"url": "https://b.com", "title": "B", "content": "b"},
			{"url": "https://c.com", "title": "C", "content": "c"}
		]
	}`)

	results, err := parseSearXNGResults(raw, 2)
	if err != nil {
		t.Fatalf("parseSearXNGResults: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2 (maxResults)", len(results))
	}
}

func TestParseSearXNGResults_EmptyURL(t *testing.T) {
	raw := []byte(`{
		"results": [
			{"url": "", "title": "No URL", "content": "skip"},
			{"url": "https://valid.com", "title": "Valid", "content": "keep"}
		]
	}`)

	results, err := parseSearXNGResults(raw, 10)
	if err != nil {
		t.Fatalf("parseSearXNGResults: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1 (empty URL skipped)", len(results))
	}
	if results[0].URL != "https://valid.com" {
		t.Fatalf("url = %q, want https://valid.com", results[0].URL)
	}
}

func TestSearXNGProvider_IsAvailable(t *testing.T) {
	p := &SearXNGProvider{}

	t.Setenv("SEARXNG_URL", "")
	if p.IsAvailable() {
		t.Fatal("expected not available when SEARXNG_URL unset")
	}

	t.Setenv("SEARXNG_URL", "http://localhost:8888")
	if !p.IsAvailable() {
		t.Fatal("expected available when SEARXNG_URL is set")
	}
}

func TestParseParallelResults(t *testing.T) {
	raw := []byte(`{
		"search_id": "search_test",
		"results": [
			{
				"url": "https://example.com/one",
				"title": "Example One",
				"publish_date": "2026-05-25",
				"excerpts": ["First excerpt.", "Second excerpt."]
			}
		],
		"session_id": "session_test"
	}`)

	results, err := parseParallelResults(raw)
	if err != nil {
		t.Fatalf("parseParallelResults: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}
	if results[0].Title != "Example One" || results[0].URL != "https://example.com/one" {
		t.Fatalf("result = %+v, want parsed title/url", results[0])
	}
	if !strings.Contains(results[0].Snippet, "First excerpt.") || !strings.Contains(results[0].Snippet, "Second excerpt.") {
		t.Fatalf("snippet = %q, want joined excerpts", results[0].Snippet)
	}
	if results[0].PublishedAt != "2026-05-25" {
		t.Fatalf("published_at = %q, want date", results[0].PublishedAt)
	}
}

func TestSearchClient_AllProvidersFail(t *testing.T) {
	fail1 := &mockSearchProvider{
		name:      "fail1",
		available: true,
		searchFunc: func(ctx context.Context, query string, maxResults int) ([]SearchResult, error) {
			return nil, errors.New("fail1 error")
		},
	}
	fail2 := &mockSearchProvider{
		name:      "fail2",
		available: true,
		searchFunc: func(ctx context.Context, query string, maxResults int) ([]SearchResult, error) {
			return nil, errors.New("fail2 error")
		},
	}

	client := testSearchClient([]SearchProvider{fail1, fail2}, 2)

	_, err := client.Search(context.Background(), SearchRequest{Query: "test", MaxResults: 5})
	if err == nil {
		t.Fatal("expected error when all providers fail")
	}
	if !strings.Contains(err.Error(), "search_outage") {
		t.Errorf("expected 'all search providers failed' error, got: %v", err)
	}
}

func TestSearchClient_MaxResultsClamping(t *testing.T) {
	mock := &mockSearchProvider{
		name:      "mock",
		available: true,
		searchFunc: func(ctx context.Context, query string, maxResults int) ([]SearchResult, error) {
			// Verify the maxResults is clamped
			if maxResults > 50 || maxResults < 1 {
				t.Errorf("maxResults should be clamped to [1,50], got %d", maxResults)
			}
			return []SearchResult{}, nil
		},
	}

	client := testSearchClient([]SearchProvider{mock}, 1)

	// Test zero (should default to 40)
	client.Search(context.Background(), SearchRequest{Query: "test", MaxResults: 0})

	// Test too large (should clamp to 50)
	client.Search(context.Background(), SearchRequest{Query: "test", MaxResults: 100})
}

func TestSearchClient_AvailableProviders(t *testing.T) {
	mock1 := &mockSearchProvider{name: "mock1", available: true}
	mock2 := &mockSearchProvider{name: "mock2", available: true}

	client := testSearchClient([]SearchProvider{mock1, mock2}, 1)

	names := client.AvailableProviders()
	if len(names) != 2 {
		t.Errorf("expected 2 providers, got %d", len(names))
	}
	if names[0] != "mock1" || names[1] != "mock2" {
		t.Errorf("expected [mock1, mock2], got %v", names)
	}
}

func TestSearXNGProvider_Search(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" {
			t.Errorf("path = %q, want /search", r.URL.Path)
		}
		q := r.URL.Query().Get("q")
		if q != "test query" {
			t.Errorf("q = %q, want 'test query'", q)
		}
		if r.URL.Query().Get("format") != "json" {
			t.Errorf("format = %q, want json", r.URL.Query().Get("format"))
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"query":"test query","results":[{"url":"https://result.com","title":"Result","content":"snippet","score":5.0}]}`)
	}))
	defer srv.Close()

	t.Setenv("SEARXNG_URL", srv.URL)
	p := &SearXNGProvider{}
	if !p.IsAvailable() {
		t.Fatal("expected available")
	}

	results, err := p.Search(context.Background(), "test query", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}
	if results[0].URL != "https://result.com" || results[0].Title != "Result" {
		t.Fatalf("result = %+v", results[0])
	}
	if results[0].Snippet != "snippet" {
		t.Fatalf("snippet = %q, want 'snippet'", results[0].Snippet)
	}
}

func TestSearXNGProvider_SearchError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, `{"error": "upstream unavailable"}`)
	}))
	defer srv.Close()

	t.Setenv("SEARXNG_URL", srv.URL)
	p := &SearXNGProvider{}

	_, err := p.Search(context.Background(), "test", 10)
	if err == nil {
		t.Fatal("expected error on 503")
	}
}

// --- Handler Tests ---

func TestHandleSearch_MethodNotAllowed(t *testing.T) {
	h := &Handler{searchClient: &SearchClient{}}
	req := httptest.NewRequest(http.MethodGet, "/provider/v1/search", nil)
	w := httptest.NewRecorder()

	h.HandleSearch(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestHandleSearch_MissingAuth(t *testing.T) {
	registry := NewIdentityRegistry(time.Hour)
	h := NewHandler(registry, nil)

	body := `{"query": "test"}`
	req := httptest.NewRequest(http.MethodPost, "/provider/v1/search", strings.NewReader(body))
	w := httptest.NewRecorder()

	h.HandleSearch(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestHandleSearch_InvalidBody(t *testing.T) {
	registry := NewIdentityRegistry(time.Hour)
	h := NewHandler(registry, nil)

	// Issue a valid credential
	cred, err := registry.IssueCredential("test-autoputer")
	if err != nil {
		t.Fatalf("failed to issue credential: %v", err)
	}

	body := `invalid json`
	req := httptest.NewRequest(http.MethodPost, "/provider/v1/search", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+cred.RawToken)
	w := httptest.NewRecorder()

	h.HandleSearch(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestHandleSearch_EmptyQuery(t *testing.T) {
	registry := NewIdentityRegistry(time.Hour)
	h := NewHandler(registry, nil)

	// Issue a valid credential
	cred, err := registry.IssueCredential("test-autoputer")
	if err != nil {
		t.Fatalf("failed to issue credential: %v", err)
	}

	body := `{"query": ""}`
	req := httptest.NewRequest(http.MethodPost, "/provider/v1/search", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+cred.RawToken)
	w := httptest.NewRecorder()

	h.HandleSearch(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestHandleSearch_NoProvidersConfigured(t *testing.T) {
	registry := NewIdentityRegistry(time.Hour)
	h := NewHandler(registry, nil)
	h.searchClient = testSearchClient(nil, 1)

	// Issue a valid credential
	cred, err := registry.IssueCredential("test-autoputer")
	if err != nil {
		t.Fatalf("failed to issue credential: %v", err)
	}

	body := `{"query": "test"}`
	req := httptest.NewRequest(http.MethodPost, "/provider/v1/search", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+cred.RawToken)
	w := httptest.NewRecorder()

	h.HandleSearch(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Code)
	}
}

func TestHandleSearch_Success(t *testing.T) {
	registry := NewIdentityRegistry(time.Hour)

	// Create a search client with a mock provider
	mock := &mockSearchProvider{
		name:      "test",
		available: true,
		searchFunc: func(ctx context.Context, query string, maxResults int) ([]SearchResult, error) {
			return []SearchResult{
				{Title: "Result 1", URL: "http://example.com/1", Snippet: "Snippet 1"},
				{Title: "Result 2", URL: "http://example.com/2", Snippet: "Snippet 2"},
			}, nil
		},
	}

	h := &Handler{
		registry:     registry,
		searchClient: testSearchClient([]SearchProvider{mock}, 1),
	}

	// Issue a valid credential
	cred, err := registry.IssueCredential("test-autoputer")
	if err != nil {
		t.Fatalf("failed to issue credential: %v", err)
	}

	body := `{"query": "test", "max_results": 5}`
	req := httptest.NewRequest(http.MethodPost, "/provider/v1/search", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+cred.RawToken)
	w := httptest.NewRecorder()

	h.HandleSearch(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp SearchResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if resp.Provider != "test" {
		t.Errorf("expected provider 'test', got %s", resp.Provider)
	}
	if resp.Query != "test" {
		t.Errorf("expected query 'test', got %s", resp.Query)
	}
	if len(resp.Results) != 2 {
		t.Errorf("expected 2 results, got %d", len(resp.Results))
	}
	if resp.Results[0].Title != "Result 1" {
		t.Errorf("expected first result title 'Result 1', got %s", resp.Results[0].Title)
	}
}

func TestHandleSearch_DeniesExternalPeerWithValidToken(t *testing.T) {
	registry := NewIdentityRegistry(time.Hour)

	mock := &mockSearchProvider{
		name:      "test",
		available: true,
		searchFunc: func(ctx context.Context, query string, maxResults int) ([]SearchResult, error) {
			return []SearchResult{{Title: "Result 1", URL: "http://example.com/1", Snippet: "Snippet 1"}}, nil
		},
	}

	h := &Handler{
		registry:     registry,
		searchClient: testSearchClient([]SearchProvider{mock}, 1),
	}

	cred, err := registry.IssueCredential("test-autoputer")
	if err != nil {
		t.Fatalf("failed to issue credential: %v", err)
	}

	body := `{"query": "test", "max_results": 5}`
	req := httptest.NewRequest(http.MethodPost, "/provider/v1/search", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+cred.RawToken)
	req.RemoteAddr = "8.8.8.8:12345"
	w := httptest.NewRecorder()

	h.HandleSearch(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

// --- Provider Integration Tests (requires env vars, skipped by default) ---

func TestTavilyProvider_Integration(t *testing.T) {
	if os.Getenv("RUN_INTEGRATION_TESTS") == "" {
		t.Skip("skipping live provider integration (set RUN_INTEGRATION_TESTS=1)")
	}
	apiKey := os.Getenv("TAVILY_API_KEY")
	if apiKey == "" {
		t.Skip("TAVILY_API_KEY not set, skipping integration test")
	}

	provider := &TavilyProvider{}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	results, err := provider.Search(ctx, "golang programming", 3)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}

	if len(results) == 0 {
		t.Error("expected at least one result")
	}

	for _, r := range results {
		if r.Title == "" {
			t.Error("expected non-empty title")
		}
		if r.URL == "" {
			t.Error("expected non-empty URL")
		}
	}
}

func TestBraveProvider_Integration(t *testing.T) {
	if os.Getenv("RUN_INTEGRATION_TESTS") == "" {
		t.Skip("skipping live provider integration (set RUN_INTEGRATION_TESTS=1)")
	}
	apiKey := os.Getenv("BRAVE_API_KEY")
	if apiKey == "" {
		t.Skip("BRAVE_API_KEY not set, skipping integration test")
	}

	provider := &BraveProvider{}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	results, err := provider.Search(ctx, "golang programming", 3)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}

	if len(results) == 0 {
		t.Error("expected at least one result")
	}

	for _, r := range results {
		if r.Title == "" {
			t.Error("expected non-empty title")
		}
		if r.URL == "" {
			t.Error("expected non-empty URL")
		}
	}
}

func TestExaProvider_Integration(t *testing.T) {
	if os.Getenv("RUN_INTEGRATION_TESTS") == "" {
		t.Skip("skipping live provider integration (set RUN_INTEGRATION_TESTS=1)")
	}
	apiKey := os.Getenv("EXA_API_KEY")
	if apiKey == "" {
		t.Skip("EXA_API_KEY not set, skipping integration test")
	}

	provider := &ExaProvider{}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	results, err := provider.Search(ctx, "golang programming", 3)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}

	if len(results) == 0 {
		t.Error("expected at least one result")
	}

	for _, r := range results {
		if r.Title == "" {
			t.Error("expected non-empty title")
		}
		if r.URL == "" {
			t.Error("expected non-empty URL")
		}
	}
}

func TestSerperProvider_Integration(t *testing.T) {
	if os.Getenv("RUN_INTEGRATION_TESTS") == "" {
		t.Skip("skipping live provider integration (set RUN_INTEGRATION_TESTS=1)")
	}
	apiKey := os.Getenv("SERPER_API_KEY")
	if apiKey == "" {
		t.Skip("SERPER_API_KEY not set, skipping integration test")
	}

	provider := &SerperProvider{}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	results, err := provider.Search(ctx, "golang programming", 3)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}

	if len(results) == 0 {
		t.Error("expected at least one result")
	}

	for _, r := range results {
		if r.Title == "" {
			t.Error("expected non-empty title")
		}
		if r.URL == "" {
			t.Error("expected non-empty URL")
		}
	}
}

func TestParseSerpAPIResults(t *testing.T) {
	results, err := parseSerpAPIResults([]byte(`{
		"organic_results": [
			{"title": "AI infra", "link": "https://example.com/ai", "snippet": "June 2026 launch", "date": "Jun 16, 2026"}
		]
	}`))
	if err != nil {
		t.Fatalf("parseSerpAPIResults: %v", err)
	}
	if len(results) != 1 || results[0].URL != "https://example.com/ai" {
		t.Fatalf("results = %#v, want one parsed organic hit", results)
	}
	if results[0].PublishedAt != "Jun 16, 2026" {
		t.Fatalf("published_at = %q", results[0].PublishedAt)
	}

	_, err = parseSerpAPIResults([]byte(`{"error":"Invalid API key."}`))
	if err == nil {
		t.Fatal("expected error for API error envelope")
	}
}

func TestSerpAPIProvider_Integration(t *testing.T) {
	if os.Getenv("RUN_INTEGRATION_TESTS") == "" {
		t.Skip("skipping live provider integration (set RUN_INTEGRATION_TESTS=1)")
	}
	apiKey := os.Getenv("SERPAPI_API_KEY")
	if apiKey == "" {
		t.Skip("SERPAPI_API_KEY not set, skipping integration test")
	}

	provider := &SerpAPIProvider{}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	results, err := provider.Search(ctx, "golang programming", 3)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}

	if len(results) == 0 {
		t.Error("expected at least one result")
	}
	for _, r := range results {
		if r.Title == "" {
			t.Error("expected non-empty title")
		}
		if r.URL == "" {
			t.Error("expected non-empty URL")
		}
	}
}

func TestParallelProvider_Integration(t *testing.T) {
	if os.Getenv("RUN_INTEGRATION_TESTS") == "" {
		t.Skip("skipping live provider integration (set RUN_INTEGRATION_TESTS=1)")
	}
	apiKey := os.Getenv("PARALLEL_API_KEY")
	if apiKey == "" {
		t.Skip("PARALLEL_API_KEY not set, skipping integration test")
	}

	provider := &ParallelProvider{}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	results, err := provider.Search(ctx, "golang programming", 3)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}

	if len(results) == 0 {
		t.Error("expected at least one result")
	}
	for _, r := range results {
		if r.Title == "" {
			t.Error("expected non-empty title")
		}
		if r.URL == "" {
			t.Error("expected non-empty URL")
		}
	}
}

func TestNewSearchClient_FromEnv(t *testing.T) {
	// Save current env vars
	tavilyKey := os.Getenv("TAVILY_API_KEY")
	braveKey := os.Getenv("BRAVE_API_KEY")
	parallelKey := os.Getenv("PARALLEL_API_KEY")
	exaKey := os.Getenv("EXA_API_KEY")
	serperKey := os.Getenv("SERPER_API_KEY")
	serpapiKey := os.Getenv("SERPAPI_API_KEY")
	chatGPTAuthPath := os.Getenv("CHATGPT_AUTH_PATH")

	// Clean up after test
	defer func() {
		os.Setenv("TAVILY_API_KEY", tavilyKey)
		os.Setenv("BRAVE_API_KEY", braveKey)
		os.Setenv("PARALLEL_API_KEY", parallelKey)
		os.Setenv("EXA_API_KEY", exaKey)
		os.Setenv("SERPER_API_KEY", serperKey)
		os.Setenv("SERPAPI_API_KEY", serpapiKey)
		os.Setenv("CHATGPT_AUTH_PATH", chatGPTAuthPath)
	}()

	// Test with no keys set
	os.Unsetenv("TAVILY_API_KEY")
	os.Unsetenv("BRAVE_API_KEY")
	os.Unsetenv("PARALLEL_API_KEY")
	os.Unsetenv("EXA_API_KEY")
	os.Unsetenv("SERPER_API_KEY")
	os.Unsetenv("SERPAPI_API_KEY")
	os.Unsetenv("CHATGPT_AUTH_PATH")

	client := NewSearchClient()
	providers := client.AvailableProviders()
	if len(providers) != 0 {
		t.Errorf("expected 0 providers with no env vars, got %d", len(providers))
	}

	// Test with one key set
	os.Setenv("TAVILY_API_KEY", "test-key")
	client = NewSearchClient()
	providers = client.AvailableProviders()
	if len(providers) != 1 || providers[0] != "tavily" {
		t.Errorf("expected [tavily], got %v", providers)
	}

	os.Unsetenv("TAVILY_API_KEY")
	authPath := t.TempDir() + "/auth.json"
	if err := os.WriteFile(authPath, []byte(`{"auth_mode":"chatgpt","tokens":{"access_token":"test-token"}}`), 0o600); err != nil {
		t.Fatalf("write auth fixture: %v", err)
	}
	os.Setenv("CHATGPT_AUTH_PATH", authPath)
	client = NewSearchClient()
	providers = client.AvailableProviders()
	if len(providers) != 1 || providers[0] != "chatgpt" {
		t.Errorf("expected [chatgpt], got %v", providers)
	}
}

// --- Response Format Tests ---

func TestSearchResponse_MarshalJSON(t *testing.T) {
	resp := SearchResponse{
		Provider: "test",
		Query:    "golang",
		Results: []SearchResult{
			{
				Title:       "Go Programming Language",
				URL:         "https://golang.org",
				Snippet:     "The Go programming language.",
				PublishedAt: "2024-01-01",
				Score:       0.95,
			},
		},
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	// Verify the JSON structure
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if decoded["provider"] != "test" {
		t.Errorf("expected provider 'test', got %v", decoded["provider"])
	}
	if decoded["query"] != "golang" {
		t.Errorf("expected query 'golang', got %v", decoded["query"])
	}

	results, ok := decoded["results"].([]any)
	if !ok || len(results) != 1 {
		t.Fatalf("expected 1 result, got %v", decoded["results"])
	}

	result := results[0].(map[string]any)
	if result["title"] != "Go Programming Language" {
		t.Errorf("expected title 'Go Programming Language', got %v", result["title"])
	}
}

func TestSearchRequest_UnmarshalJSON(t *testing.T) {
	jsonData := `{"query": "test query", "max_results": 15}`

	var req SearchRequest
	if err := json.Unmarshal([]byte(jsonData), &req); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if req.Query != "test query" {
		t.Errorf("expected query 'test query', got %s", req.Query)
	}
	if req.MaxResults != 15 {
		t.Errorf("expected max_results 15, got %d", req.MaxResults)
	}
}
