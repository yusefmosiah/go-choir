package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

type fakeChatGPTSearchTokenSource struct {
	configured  bool
	token       string
	refreshErr  error
	refreshes   int
	refreshedTo string
}

func (s *fakeChatGPTSearchTokenSource) Header(context.Context) (string, error) {
	if s.token == "" {
		return "", errors.New("missing token")
	}
	return "Bearer " + s.token, nil
}

func (s *fakeChatGPTSearchTokenSource) Refresh(context.Context) error {
	s.refreshes++
	if s.refreshErr != nil {
		return s.refreshErr
	}
	s.token = s.refreshedTo
	return nil
}

func (s *fakeChatGPTSearchTokenSource) Configured() bool { return s.configured }

func TestChatGPTSearchProviderSearch(t *testing.T) {
	const answer = "😀 Go 1.26.5 is listed as the current stable release. [Official downloads](https://go.dev/dl/)."
	annotationStart := strings.Index(answer, "[Official")
	annotationStart = len([]rune(answer[:annotationStart]))
	annotationEnd := len([]rune(answer))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("Accept"); got != "text/event-stream" {
			t.Errorf("Accept = %q", got)
		}
		var request chatGPTSearchRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if request.Model != defaultChatGPTSearchModel {
			t.Errorf("model = %q", request.Model)
		}
		if len(request.Tools) != 1 || request.Tools[0].Type != "web_search" || request.Tools[0].SearchContextSize != "low" {
			t.Errorf("tools = %#v", request.Tools)
		}
		if request.ToolChoice != "required" || !request.Stream || request.Store {
			t.Errorf("request controls = choice:%q stream:%v store:%v", request.ToolChoice, request.Stream, request.Store)
		}
		if len(request.Include) != 1 || request.Include[0] != "web_search_call.action.sources" {
			t.Errorf("include = %#v", request.Include)
		}
		if len(request.Input) != 1 || !strings.Contains(request.Input[0].Content[0].Text, "current stable Go release") {
			t.Errorf("input = %#v", request.Input)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		writeChatGPTSearchEvent(t, w, map[string]any{
			"type": "response.output_item.done",
			"item": map[string]any{
				"type": "web_search_call",
				"action": map[string]any{"sources": []map[string]any{
					{"type": "url", "url": "https://go.dev/dl/?utm_source=openai"},
					{"type": "url", "url": "https://go.dev/doc/devel/release"},
				}},
			},
		})
		writeChatGPTSearchEvent(t, w, map[string]any{
			"type": "response.output_item.done",
			"item": map[string]any{
				"type": "message",
				"content": []map[string]any{{
					"type": "output_text",
					"text": answer,
					"annotations": []map[string]any{{
						"type":        "url_citation",
						"start_index": annotationStart,
						"end_index":   annotationEnd,
						"url":         "https://go.dev/dl/?utm_source=openai",
						"title":       "All releases - The Go Programming Language",
					}},
				}},
			},
		})
		writeChatGPTSearchEvent(t, w, map[string]any{"type": "response.completed"})
	}))
	defer server.Close()

	provider := &ChatGPTSearchProvider{
		httpClient:        server.Client(),
		baseURL:           server.URL,
		model:             defaultChatGPTSearchModel,
		reasoningEffort:   defaultChatGPTSearchReasoning,
		searchContextSize: defaultChatGPTSearchContextSize,
		tokenSource:       &fakeChatGPTSearchTokenSource{configured: true, token: "test-token"},
	}
	results, err := provider.Search(context.Background(), "current stable Go release", 2)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %#v, want two", results)
	}
	if results[0].Title != "All releases - The Go Programming Language" {
		t.Errorf("cited title = %q", results[0].Title)
	}
	if results[0].URL != "https://go.dev/dl" {
		t.Errorf("cited URL = %q", results[0].URL)
	}
	if !strings.Contains(results[0].Snippet, "Go 1.26.5") {
		t.Errorf("cited snippet = %q", results[0].Snippet)
	}
	if results[0].Provider != "chatgpt" || results[1].Provider != "chatgpt" {
		t.Errorf("providers = %q, %q", results[0].Provider, results[1].Provider)
	}
	if results[1].URL != "https://go.dev/doc/devel/release" {
		t.Errorf("fallback source URL = %q", results[1].URL)
	}
}

func TestChatGPTSearchProviderRetriesUnauthorized(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			if got := r.Header.Get("Authorization"); got != "Bearer stale-token" {
				t.Errorf("first Authorization = %q", got)
			}
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer fresh-token" {
			t.Errorf("retry Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		writeChatGPTSearchEvent(t, w, map[string]any{
			"type": "response.output_item.done",
			"item": map[string]any{
				"type": "web_search_call",
				"action": map[string]any{"sources": []map[string]any{{
					"type": "url", "url": "https://example.com/source",
				}}},
			},
		})
		writeChatGPTSearchEvent(t, w, map[string]any{"type": "response.completed"})
	}))
	defer server.Close()

	tokens := &fakeChatGPTSearchTokenSource{
		configured:  true,
		token:       "stale-token",
		refreshedTo: "fresh-token",
	}
	provider := &ChatGPTSearchProvider{
		httpClient:        server.Client(),
		baseURL:           server.URL,
		model:             defaultChatGPTSearchModel,
		reasoningEffort:   defaultChatGPTSearchReasoning,
		searchContextSize: defaultChatGPTSearchContextSize,
		tokenSource:       tokens,
	}
	results, err := provider.Search(context.Background(), "example", 1)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if requests != 2 || tokens.refreshes != 1 {
		t.Fatalf("requests = %d refreshes = %d", requests, tokens.refreshes)
	}
	if len(results) != 1 || results[0].URL != "https://example.com/source" {
		t.Fatalf("results = %#v", results)
	}
}

func TestChatGPTSearchProviderRejectsIncompleteStream(t *testing.T) {
	_, err := parseChatGPTSearchStream(strings.NewReader("data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"web_search_call\",\"action\":{\"sources\":[{\"url\":\"https://example.com\"}]}}}\n\n"), "example", 1)
	if err == nil || !strings.Contains(err.Error(), "before response.completed") {
		t.Fatalf("error = %v", err)
	}
}

func TestNormalizeChatGPTSearchContextSize(t *testing.T) {
	for input, want := range map[string]string{
		"":       "low",
		"LOW":    "low",
		"medium": "medium",
		" HIGH ": "high",
		"huge":   "low",
	} {
		if got := normalizeChatGPTSearchContextSize(input); got != want {
			t.Errorf("normalizeChatGPTSearchContextSize(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestNormalizeChatGPTSearchURL(t *testing.T) {
	if got := normalizeChatGPTSearchURL("https://example.com/path#fragment"); got != "https://example.com/path" {
		t.Fatalf("normalized URL = %q", got)
	}
	for _, raw := range []string{"javascript://example.com/alert", "file://example.com/path", "not-a-url"} {
		if got := normalizeChatGPTSearchURL(raw); got != "" {
			t.Errorf("normalizeChatGPTSearchURL(%q) = %q, want empty", raw, got)
		}
	}
}

func TestChatGPTSearchProviderIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live ChatGPT search in short mode")
	}
	if strings.TrimSpace(os.Getenv("CHATGPT_AUTH_PATH")) == "" {
		t.Skip("CHATGPT_AUTH_PATH not set")
	}
	provider := NewChatGPTSearchProviderFromEnv()
	client := testSearchClient([]SearchProvider{provider}, 1)
	client.planeConfig.RequestTimeout = 60 * time.Second
	response, err := client.Search(context.Background(), SearchRequest{
		Query:      "official OpenAI Responses API web search documentation",
		MaxResults: 3,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(response.Results) == 0 || len(response.Results) > 3 {
		t.Fatalf("result count = %d, want 1..3", len(response.Results))
	}
	if len(response.Attempts) != 1 || response.Attempts[0].Provider != "chatgpt" {
		t.Fatalf("attempts = %#v", response.Attempts)
	}
	for _, result := range response.Results {
		if result.Title == "" || result.URL == "" || result.Provider != "chatgpt" {
			t.Errorf("invalid normalized result: %#v", result)
		}
	}
}

func writeChatGPTSearchEvent(t *testing.T, w http.ResponseWriter, event any) {
	t.Helper()
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	if _, err := w.Write(append(append([]byte("data: "), data...), '\n', '\n')); err != nil {
		t.Fatalf("write event: %v", err)
	}
}
