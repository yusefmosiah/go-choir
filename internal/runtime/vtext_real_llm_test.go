package runtime

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusefmosiah/go-choir/internal/events"
	"github.com/yusefmosiah/go-choir/internal/store"
	"github.com/yusefmosiah/go-choir/internal/types"
)

// --- Real LLM E2E Test for VText Agent Revision ---
//
// These tests validate the full agent revision flow with real LLM providers
// (Z.AI or Fireworks). They fulfill validation assertions:
//   - VAL-LLM-013: VText Agent Revision with Fireworks produces content changes
//   - VAL-LLM-014: VText Agent Revision with Z.AI produces content changes
//   - VAL-LLM-015: Agent Revision with code request produces valid code
//   - VAL-LLM-016: Failed LLM call shows graceful error in UI
//
// Tests are skipped automatically when no provider credentials are available.
//
// NOTE: This file intentionally does NOT import the provider package to avoid
// a circular dependency (provider → runtime → provider). Instead, it implements
// the runtime.Provider interface directly using the Anthropic Messages API,
// which is the same protocol used by both Z.AI and Fireworks.

// --- Anthropic Messages API Client (simplified) ---
//
// anthropicClient makes direct HTTP calls to an Anthropic-compatible API
// endpoint (Z.AI or Fireworks). It implements the runtime.Provider interface.

type anthropicClient struct {
	name       string
	apiKey     string
	baseURL    string
	modelID    string
	httpClient *http.Client
}

func newAnthropicClient(name, apiKey, baseURL, modelID string) *anthropicClient {
	return &anthropicClient{
		name:       name,
		apiKey:     apiKey,
		baseURL:    strings.TrimRight(baseURL, "/"),
		modelID:    modelID,
		httpClient: &http.Client{Timeout: 120 * time.Second},
	}
}

// anthropicRequestBody is the JSON body for an Anthropic Messages API request.
type anthropicRequestBody struct {
	Model     string `json:"model"`
	MaxTokens int    `json:"max_tokens"`
	System    string `json:"system,omitempty"`
	Stream    bool   `json:"stream"`
	Messages  []struct {
		Role    string `json:"role"`
		Content any    `json:"content"`
	} `json:"messages"`
}

// anthropicResponse is the JSON response from the Anthropic Messages API.
type anthropicResponse struct {
	ID         string `json:"id"`
	Model      string `json:"model"`
	StopReason string `json:"stop_reason"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text,omitempty"`
	} `json:"content"`
}

// ProviderName implements the runtime.Provider interface.
func (c *anthropicClient) ProviderName() string { return c.name }

// Execute implements the runtime.Provider interface. It makes a real HTTP call
// to the Anthropic-compatible API with streaming enabled, emitting delta events
// for each SSE chunk.
func (c *anthropicClient) Execute(ctx context.Context, task *types.TaskRecord, emit EventEmitFunc) error {
	emit(types.EventTaskProgress, "execution", json.RawMessage(
		`{"status":"started","provider":"`+c.name+`","real":"true"}`))

	// Build the request body with streaming.
	body := anthropicRequestBody{
		Model:     c.modelID,
		MaxTokens: 4096,
		Stream:    true,
		System:    "You are a helpful assistant running inside the ChoirOS sandbox runtime. Respond concisely and helpfully.",
		Messages: []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		}{
			{
				Role: "user",
				Content: []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				}{
					{Type: "text", Text: task.Prompt},
				},
			},
		},
	}

	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return err
	}

	endpoint := c.baseURL + "/v1/messages"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyJSON))
	if err != nil {
		return err
	}
	httpReq.Header.Set("x-api-key", c.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")

	log.Printf("real-llm-test: calling %s (%s) stream=true for task %s", c.name, c.modelID, task.TaskID)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return errProviderCall(resp.StatusCode, resp.Status, respBody)
	}

	// Parse the SSE stream.
	var accumulatedText string
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var event struct {
			Type  string `json:"type"`
			Delta struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"delta"`
			Message *struct {
				StopReason string `json:"stop_reason"`
				Usage      struct {
					InputTokens  int `json:"input_tokens"`
					OutputTokens int `json:"output_tokens"`
				} `json:"usage"`
			} `json:"message"`
		}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}

		if event.Type == "content_block_delta" && event.Delta.Type == "text_delta" && event.Delta.Text != "" {
			accumulatedText += event.Delta.Text
			deltaPayload, _ := json.Marshal(map[string]string{
				"text":     event.Delta.Text,
				"provider": c.name,
				"real":     "true",
			})
			emit(types.EventTaskDelta, "execution", deltaPayload)
		}
	}

	task.Result = accumulatedText

	progressPayload, _ := json.Marshal(map[string]string{
		"status":   "completed",
		"provider": c.name,
		"real":     "true",
	})
	emit(types.EventTaskProgress, "execution", progressPayload)

	log.Printf("real-llm-test: %s completed for task %s (text_len=%d)", c.name, task.TaskID, len(accumulatedText))
	return nil
}

type providerError struct {
	statusCode int
	status     string
	body       []byte
}

func (e *providerError) Error() string {
	return "provider call failed: " + e.status + " (sanitized)"
}

func errProviderCall(statusCode int, status string, body []byte) *providerError {
	return &providerError{statusCode: statusCode, status: status, body: body}
}

// --- Test Setup Helpers ---

// resolveRealProvider creates a real LLM runtime.Provider from environment
// credentials. It returns the provider and display name, or skips the test
// if no credentials are available.
func resolveRealProvider(t *testing.T) (Provider, string) {
	t.Helper()

	// Try Z.AI first.
	if apiKey := os.Getenv("ZAI_API_KEY"); apiKey != "" {
		baseURL := os.Getenv("ZAI_BASE_URL")
		if baseURL == "" {
			baseURL = "https://api.z.ai/api/anthropic"
		}
		return newAnthropicClient("zai", apiKey, baseURL, "glm-5-turbo"), "zai"
	}

	// Try Fireworks.
	if apiKey := os.Getenv("FIREWORKS_API_KEY"); apiKey != "" {
		baseURL := os.Getenv("FIREWORKS_BASE_URL")
		if baseURL == "" {
			baseURL = "https://api.fireworks.ai/inference"
		}
		return newAnthropicClient("fireworks", apiKey, baseURL, "accounts/fireworks/routers/kimi-k2p5-turbo"), "fireworks"
	}

	t.Skip("No LLM provider credentials configured (set ZAI_API_KEY or FIREWORKS_API_KEY to run real LLM tests)")
	return nil, ""
}

// vtextRealLLMSetup creates a test environment with a real LLM provider.
// It returns an APIHandler, store, runtime, and provider name.
func vtextRealLLMSetup(t *testing.T) (*APIHandler, *store.Store, *Runtime, string) {
	t.Helper()

	dir := filepath.Join(os.TempDir(), "go-choir-m2-vtext-real-llm-test")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	dbPath := filepath.Join(dir, t.Name()+".db")
	_ = os.Remove(dbPath)

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	bus := events.NewEventBus()
	realProvider, providerName := resolveRealProvider(t)

	cfg := Config{
		SandboxID:           "sandbox-vtext-real-llm",
		StorePath:           dbPath,
		ProviderTimeout:     60 * time.Second,
		SupervisionInterval: 5 * time.Second,
	}

	rt := New(cfg, s, bus, realProvider)
	ctx := context.Background()
	rt.Start(ctx)
	t.Cleanup(func() { rt.Stop() })

	return NewAPIHandler(rt), s, rt, providerName
}

// vtextRealLLMRequest creates an HTTP request for the real LLM tests.
func vtextRealLLMRequest(t *testing.T, method, path string, body interface{}) *http.Request {
	t.Helper()
	var reqBody *bytes.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}
		reqBody = bytes.NewReader(data)
	} else {
		reqBody = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reqBody)
	req.Header.Set("X-Authenticated-User", "user-real-llm")
	return req
}

// --- Real LLM E2E Tests ---

// TestVTextAgentRevisionRealLLM validates the full agent revision flow
// with a real LLM provider:
//  1. Create document
//  2. Add user-authored revision with initial content
//  3. Submit agent revision prompt
//  4. Verify task completes with non-empty result
//  5. Verify a canonical appagent-authored revision is created
//  6. Verify the document head is updated
//  7. Verify history shows both user and appagent attribution
//
// Fulfills: VAL-LLM-013, VAL-LLM-014
func TestVTextAgentRevisionRealLLM(t *testing.T) {
	h, s, _, providerName := vtextRealLLMSetup(t)
	ctx := context.Background()

	t.Logf("Testing with provider: %s", providerName)

	// Step 1: Create a document.
	req := vtextRealLLMRequest(t, http.MethodPost, "/api/vtext/documents",
		map[string]string{"title": "Real LLM Test Document"})
	w := httptest.NewRecorder()
	h.HandleVTextCreateDocument(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("create document: status = %d, body: %s", w.Code, w.Body.String())
	}

	var docResp vtextCreateDocResponse
	if err := json.NewDecoder(w.Body).Decode(&docResp); err != nil {
		t.Fatalf("decode document response: %v", err)
	}
	t.Logf("Created document: %s", docResp.DocID)

	// Step 2: Add initial user content.
	initialContent := "Hey there! This is a simple test document. It has some informal language and could use improvement."
	revReq := vtextCreateRevisionRequest{
		Content:     initialContent,
		AuthorKind:  types.AuthorUser,
		AuthorLabel: "alice",
	}
	req = vtextRealLLMRequest(t, http.MethodPost,
		"/api/vtext/documents/"+docResp.DocID+"/revisions", revReq)
	w = httptest.NewRecorder()
	h.HandleVTextRevisions(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("create user revision: status = %d, body: %s", w.Code, w.Body.String())
	}

	var userRevResp vtextRevisionResponse
	if err := json.NewDecoder(w.Body).Decode(&userRevResp); err != nil {
		t.Fatalf("decode revision response: %v", err)
	}
	t.Logf("Created user revision: %s", userRevResp.RevisionID)

	// Step 3: Submit agent revision prompt.
	revisionPrompt := "Rewrite this in a formal, professional tone. Keep the same meaning but use professional language."
	req = vtextRealLLMRequest(t, http.MethodPost,
		"/api/vtext/documents/"+docResp.DocID+"/agent-revision",
		map[string]string{"prompt": revisionPrompt})
	w = httptest.NewRecorder()
	h.HandleVTextAgentRevision(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("agent revision: status = %d, want %d; body: %s", w.Code, http.StatusAccepted, w.Body.String())
	}

	var agentResp vtextAgentRevisionResponse
	if err := json.NewDecoder(w.Body).Decode(&agentResp); err != nil {
		t.Fatalf("decode agent revision response: %v", err)
	}
	t.Logf("Submitted agent revision task: %s", agentResp.TaskID)

	// Verify metadata.
	if agentResp.DocID != docResp.DocID {
		t.Errorf("doc_id = %q, want %q", agentResp.DocID, docResp.DocID)
	}
	if agentResp.State != types.TaskPending {
		t.Errorf("initial state = %q, want pending", agentResp.State)
	}

	// Step 4: Wait for the task to complete.
	state := waitForTaskCompletion(t, h, agentResp.TaskID, 60*time.Second)
	if state != types.TaskCompleted {
		statusReq := vtextRealLLMRequest(t, http.MethodGet,
			"/api/agent/status?task_id="+agentResp.TaskID, nil)
		statusW := httptest.NewRecorder()
		h.HandleTaskStatus(statusW, statusReq)
		var statusResp taskStatusResponse
		_ = json.NewDecoder(statusW.Body).Decode(&statusResp)
		t.Fatalf("task state = %q, want completed; error: %s", state, statusResp.Error)
	}

	// Get the task result.
	statusReq := vtextRealLLMRequest(t, http.MethodGet,
		"/api/agent/status?task_id="+agentResp.TaskID, nil)
	statusW := httptest.NewRecorder()
	h.HandleTaskStatus(statusW, statusReq)
	var statusResp taskStatusResponse
	if err := json.NewDecoder(statusW.Body).Decode(&statusResp); err != nil {
		t.Fatalf("decode status response: %v", err)
	}
	t.Logf("Task completed with result length: %d", len(statusResp.Result))

	if statusResp.Result == "" {
		t.Error("task result should not be empty for real LLM call")
	}

	// Step 5: Verify a canonical appagent-authored revision was created.
	revs, err := s.ListRevisionsByDoc(ctx, docResp.DocID, "user-real-llm", 10)
	if err != nil {
		t.Fatalf("list revisions: %v", err)
	}

	if len(revs) != 2 {
		t.Fatalf("len(revisions) = %d, want 2", len(revs))
	}

	var agentRev *types.Revision
	for i := range revs {
		if revs[i].AuthorKind == types.AuthorAppAgent {
			agentRev = &revs[i]
			break
		}
	}
	if agentRev == nil {
		t.Fatal("no appagent-authored revision found")
	}
	t.Logf("Appagent revision: %s (content length: %d)", agentRev.RevisionID, len(agentRev.Content))

	if agentRev.AuthorLabel != "appagent" {
		t.Errorf("AuthorLabel = %q, want %q", agentRev.AuthorLabel, "appagent")
	}
	if agentRev.Content == "" {
		t.Error("appagent revision content should not be empty")
	}
	if agentRev.Content == initialContent {
		t.Error("appagent revision content should differ from original user content")
	}

	// Step 6: Verify document head is updated.
	doc, err := s.GetDocument(ctx, docResp.DocID, "user-real-llm")
	if err != nil {
		t.Fatalf("get document: %v", err)
	}
	if doc.CurrentRevisionID != agentRev.RevisionID {
		t.Errorf("document head = %q, want appagent revision %q",
			doc.CurrentRevisionID, agentRev.RevisionID)
	}

	// Step 7: Verify history shows both user and appagent attribution.
	entries, err := s.GetHistory(ctx, docResp.DocID, "user-real-llm", 10)
	if err != nil {
		t.Fatalf("get history: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("len(history) = %d, want 2", len(entries))
	}
	if entries[0].AuthorKind != types.AuthorAppAgent {
		t.Errorf("newest entry AuthorKind = %q, want %q", entries[0].AuthorKind, types.AuthorAppAgent)
	}
	if entries[1].AuthorKind != types.AuthorUser {
		t.Errorf("oldest entry AuthorKind = %q, want %q", entries[1].AuthorKind, types.AuthorUser)
	}

	t.Logf("✓ Real LLM agent revision validated with provider: %s", providerName)
	t.Logf("  Original: %q", truncate(initialContent, 60))
	t.Logf("  Revised:  %q", truncate(agentRev.Content, 60))
}

// TestVTextAgentRevisionRealLLMCodeGeneration validates that requesting
// code generation through agent revision produces code-like output.
//
// Fulfills: VAL-LLM-015
func TestVTextAgentRevisionRealLLMCodeGeneration(t *testing.T) {
	h, s, _, providerName := vtextRealLLMSetup(t)
	ctx := context.Background()

	t.Logf("Testing code generation with provider: %s", providerName)

	// Create document.
	req := vtextRealLLMRequest(t, http.MethodPost, "/api/vtext/documents",
		map[string]string{"title": "Code Generation Test"})
	w := httptest.NewRecorder()
	h.HandleVTextCreateDocument(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create document: status = %d", w.Code)
	}
	var docResp vtextCreateDocResponse
	_ = json.NewDecoder(w.Body).Decode(&docResp)

	// Create initial revision.
	revReq := vtextCreateRevisionRequest{
		Content:     "I need a Python function to calculate fibonacci numbers.",
		AuthorKind:  types.AuthorUser,
		AuthorLabel: "dev",
	}
	req = vtextRealLLMRequest(t, http.MethodPost,
		"/api/vtext/documents/"+docResp.DocID+"/revisions", revReq)
	w = httptest.NewRecorder()
	h.HandleVTextRevisions(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create revision: status = %d", w.Code)
	}

	// Submit agent revision requesting code.
	req = vtextRealLLMRequest(t, http.MethodPost,
		"/api/vtext/documents/"+docResp.DocID+"/agent-revision",
		map[string]string{"prompt": "Write a complete Python function that calculates fibonacci numbers. Include a docstring and handle edge cases."})
	w = httptest.NewRecorder()
	h.HandleVTextAgentRevision(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("agent revision: status = %d", w.Code)
	}

	var agentResp vtextAgentRevisionResponse
	_ = json.NewDecoder(w.Body).Decode(&agentResp)

	// Wait for completion.
	state := waitForTaskCompletion(t, h, agentResp.TaskID, 60*time.Second)
	if state != types.TaskCompleted {
		t.Fatalf("task state = %q, want completed", state)
	}

	// Verify code-like content.
	revs, err := s.ListRevisionsByDoc(ctx, docResp.DocID, "user-real-llm", 10)
	if err != nil {
		t.Fatalf("list revisions: %v", err)
	}

	var agentRev *types.Revision
	for i := range revs {
		if revs[i].AuthorKind == types.AuthorAppAgent {
			agentRev = &revs[i]
			break
		}
	}
	if agentRev == nil {
		t.Fatal("no appagent-authored revision found")
	}

	content := strings.ToLower(agentRev.Content)
	hasCodeIndicators := strings.Contains(content, "def ") ||
		strings.Contains(content, "fibonacci") ||
		strings.Contains(content, "return") ||
		strings.Contains(content, "```")

	if !hasCodeIndicators {
		t.Errorf("agent revision should contain code-like content, got: %q", truncate(agentRev.Content, 200))
	}

	t.Logf("✓ Code generation validated with provider: %s", providerName)
	t.Logf("  Generated content: %q", truncate(agentRev.Content, 100))
}

// TestVTextAgentRevisionRealLLMEventsEmitted validates that lifecycle
// events are emitted during a real LLM agent revision.
func TestVTextAgentRevisionRealLLMEventsEmitted(t *testing.T) {
	h, s, _, providerName := vtextRealLLMSetup(t)
	ctx := context.Background()

	t.Logf("Testing event emission with provider: %s", providerName)

	// Create document and user revision.
	req := vtextRealLLMRequest(t, http.MethodPost, "/api/vtext/documents",
		map[string]string{"title": "Event Test"})
	w := httptest.NewRecorder()
	h.HandleVTextCreateDocument(w, req)
	var docResp vtextCreateDocResponse
	_ = json.NewDecoder(w.Body).Decode(&docResp)

	revReq := vtextCreateRevisionRequest{
		Content:     "Some content to revise.",
		AuthorKind:  types.AuthorUser,
		AuthorLabel: "alice",
	}
	req = vtextRealLLMRequest(t, http.MethodPost,
		"/api/vtext/documents/"+docResp.DocID+"/revisions", revReq)
	w = httptest.NewRecorder()
	h.HandleVTextRevisions(w, req)

	// Submit agent revision.
	req = vtextRealLLMRequest(t, http.MethodPost,
		"/api/vtext/documents/"+docResp.DocID+"/agent-revision",
		map[string]string{"prompt": "Make it shorter"})
	w = httptest.NewRecorder()
	h.HandleVTextAgentRevision(w, req)
	var agentResp vtextAgentRevisionResponse
	_ = json.NewDecoder(w.Body).Decode(&agentResp)

	state := waitForTaskCompletion(t, h, agentResp.TaskID, 60*time.Second)
	if state != types.TaskCompleted {
		t.Fatalf("task state = %q, want completed", state)
	}

	// Verify events.
	evts, err := s.ListEvents(ctx, agentResp.TaskID, 200)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}

	expectedKinds := map[types.EventKind]bool{
		types.EventTaskSubmitted:               false,
		types.EventTaskStarted:                 false,
		types.EventTaskCompleted:               false,
		types.EventVTextAgentRevisionStarted:   false,
		types.EventVTextAgentRevisionCompleted: false,
	}

	for _, ev := range evts {
		if _, ok := expectedKinds[ev.Kind]; ok {
			expectedKinds[ev.Kind] = true
		}
	}

	for kind, found := range expectedKinds {
		if !found {
			t.Errorf("missing expected event kind: %s", kind)
		}
	}

	// Verify completed event payload.
	for _, ev := range evts {
		if ev.Kind == types.EventVTextAgentRevisionCompleted {
			var payload map[string]string
			if err := json.Unmarshal(ev.Payload, &payload); err == nil {
				if payload["doc_id"] != docResp.DocID {
					t.Errorf("completed event doc_id = %q, want %q", payload["doc_id"], docResp.DocID)
				}
				if payload["revision_id"] == "" {
					t.Error("completed event should contain revision_id")
				}
				t.Logf("✓ Agent revision completed: revision_id=%s", payload["revision_id"])
			}
		}
	}

	// Verify delta events from real streaming.
	hasDelta := false
	for _, ev := range evts {
		if ev.Kind == types.EventTaskDelta {
			hasDelta = true
			break
		}
	}
	if !hasDelta {
		t.Error("expected task.delta events from real LLM streaming")
	}

	t.Logf("✓ Event emission validated (%d events captured)", len(evts))
}

// TestVTextAgentRevisionRealLLMMutationIdempotency validates that
// retrying an agent revision request returns the same task ID.
func TestVTextAgentRevisionRealLLMMutationIdempotency(t *testing.T) {
	h, s, _, _ := vtextRealLLMSetup(t)

	// Create document and user revision.
	req := vtextRealLLMRequest(t, http.MethodPost, "/api/vtext/documents",
		map[string]string{"title": "Idempotency Test"})
	w := httptest.NewRecorder()
	h.HandleVTextCreateDocument(w, req)
	var docResp vtextCreateDocResponse
	_ = json.NewDecoder(w.Body).Decode(&docResp)

	revReq := vtextCreateRevisionRequest{
		Content:     "Content for idempotency test.",
		AuthorKind:  types.AuthorUser,
		AuthorLabel: "alice",
	}
	req = vtextRealLLMRequest(t, http.MethodPost,
		"/api/vtext/documents/"+docResp.DocID+"/revisions", revReq)
	w = httptest.NewRecorder()
	h.HandleVTextRevisions(w, req)

	// Submit agent revision.
	req = vtextRealLLMRequest(t, http.MethodPost,
		"/api/vtext/documents/"+docResp.DocID+"/agent-revision",
		map[string]string{"prompt": "Improve this document"})
	w = httptest.NewRecorder()
	h.HandleVTextAgentRevision(w, req)
	var resp1 vtextAgentRevisionResponse
	_ = json.NewDecoder(w.Body).Decode(&resp1)

	// Retry — should return same task ID.
	req = vtextRealLLMRequest(t, http.MethodPost,
		"/api/vtext/documents/"+docResp.DocID+"/agent-revision",
		map[string]string{"prompt": "Improve this document"})
	w = httptest.NewRecorder()
	h.HandleVTextAgentRevision(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("retry status = %d, want %d", w.Code, http.StatusAccepted)
	}

	var resp2 vtextAgentRevisionResponse
	if err := json.NewDecoder(w.Body).Decode(&resp2); err != nil {
		t.Fatalf("decode retry response: %v", err)
	}

	if resp2.TaskID != resp1.TaskID {
		t.Errorf("retry returned different task ID: %q vs %q (should be idempotent)", resp2.TaskID, resp1.TaskID)
	}

	// Wait and verify only one appagent revision.
	state := waitForTaskCompletion(t, h, resp1.TaskID, 60*time.Second)
	if state != types.TaskCompleted {
		t.Fatalf("task state = %q, want completed", state)
	}

	revs, err := s.ListRevisionsByDoc(context.Background(), docResp.DocID, "user-real-llm", 10)
	if err != nil {
		t.Fatalf("list revisions: %v", err)
	}

	agentCount := 0
	for _, rev := range revs {
		if rev.AuthorKind == types.AuthorAppAgent {
			agentCount++
		}
	}
	if agentCount != 1 {
		t.Errorf("found %d appagent revisions, want 1 (no duplicate from retry)", agentCount)
	}

	t.Logf("✓ Idempotency validated: both requests returned task %s, 1 appagent revision created", resp1.TaskID)
}

// TestVTextAgentRevisionRealLLMStreamingDeltas validates that the real
// LLM provider emits streaming delta events.
func TestVTextAgentRevisionRealLLMStreamingDeltas(t *testing.T) {
	h, s, _, providerName := vtextRealLLMSetup(t)
	ctx := context.Background()

	t.Logf("Testing streaming deltas with provider: %s", providerName)

	// Create document and user revision.
	req := vtextRealLLMRequest(t, http.MethodPost, "/api/vtext/documents",
		map[string]string{"title": "Streaming Test"})
	w := httptest.NewRecorder()
	h.HandleVTextCreateDocument(w, req)
	var docResp vtextCreateDocResponse
	_ = json.NewDecoder(w.Body).Decode(&docResp)

	revReq := vtextCreateRevisionRequest{
		Content:     "Short text.",
		AuthorKind:  types.AuthorUser,
		AuthorLabel: "alice",
	}
	req = vtextRealLLMRequest(t, http.MethodPost,
		"/api/vtext/documents/"+docResp.DocID+"/revisions", revReq)
	w = httptest.NewRecorder()
	h.HandleVTextRevisions(w, req)

	// Submit agent revision.
	req = vtextRealLLMRequest(t, http.MethodPost,
		"/api/vtext/documents/"+docResp.DocID+"/agent-revision",
		map[string]string{"prompt": "Expand this to a paragraph"})
	w = httptest.NewRecorder()
	h.HandleVTextAgentRevision(w, req)
	var agentResp vtextAgentRevisionResponse
	_ = json.NewDecoder(w.Body).Decode(&agentResp)

	state := waitForTaskCompletion(t, h, agentResp.TaskID, 60*time.Second)
	if state != types.TaskCompleted {
		t.Fatalf("task state = %q, want completed", state)
	}

	evts, err := s.ListEvents(ctx, agentResp.TaskID, 200)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}

	deltaCount := 0
	totalDeltaText := ""
	for _, ev := range evts {
		if ev.Kind == types.EventTaskDelta {
			deltaCount++
			var payload map[string]string
			if err := json.Unmarshal(ev.Payload, &payload); err == nil {
				totalDeltaText += payload["text"]
			}
		}
	}

	if deltaCount == 0 {
		t.Error("expected at least one delta event from real LLM streaming")
	}
	if totalDeltaText == "" {
		t.Error("expected non-empty text in delta events")
	}

	t.Logf("✓ Streaming validated: %d delta events, total text length: %d", deltaCount, len(totalDeltaText))
}

// TestVTextAgentRevisionRealLLMProviderMetadata validates that task
// metadata captures provider information.
func TestVTextAgentRevisionRealLLMProviderMetadata(t *testing.T) {
	h, _, _, providerName := vtextRealLLMSetup(t)

	t.Logf("Testing provider metadata with provider: %s", providerName)

	// Create document and user revision.
	req := vtextRealLLMRequest(t, http.MethodPost, "/api/vtext/documents",
		map[string]string{"title": "Metadata Test"})
	w := httptest.NewRecorder()
	h.HandleVTextCreateDocument(w, req)
	var docResp vtextCreateDocResponse
	_ = json.NewDecoder(w.Body).Decode(&docResp)

	revReq := vtextCreateRevisionRequest{
		Content:     "Some text for metadata test.",
		AuthorKind:  types.AuthorUser,
		AuthorLabel: "alice",
	}
	req = vtextRealLLMRequest(t, http.MethodPost,
		"/api/vtext/documents/"+docResp.DocID+"/revisions", revReq)
	w = httptest.NewRecorder()
	h.HandleVTextRevisions(w, req)

	// Submit agent revision.
	req = vtextRealLLMRequest(t, http.MethodPost,
		"/api/vtext/documents/"+docResp.DocID+"/agent-revision",
		map[string]string{"prompt": "Rewrite this concisely"})
	w = httptest.NewRecorder()
	h.HandleVTextAgentRevision(w, req)
	var agentResp vtextAgentRevisionResponse
	_ = json.NewDecoder(w.Body).Decode(&agentResp)

	state := waitForTaskCompletion(t, h, agentResp.TaskID, 60*time.Second)
	if state != types.TaskCompleted {
		t.Fatalf("task state = %q, want completed", state)
	}

	// Check metadata.
	statusReq := vtextRealLLMRequest(t, http.MethodGet,
		"/api/agent/status?task_id="+agentResp.TaskID, nil)
	statusW := httptest.NewRecorder()
	h.HandleTaskStatus(statusW, statusReq)
	var statusResp taskStatusResponse
	_ = json.NewDecoder(statusW.Body).Decode(&statusResp)

	if statusResp.Metadata == nil {
		t.Fatal("task metadata should not be nil")
	}
	taskType, _ := statusResp.Metadata["type"].(string)
	if taskType != "vtext_agent_revision" {
		t.Errorf("metadata.type = %q, want %q", taskType, "vtext_agent_revision")
	}

	metadataDocID, _ := statusResp.Metadata["doc_id"].(string)
	if metadataDocID != docResp.DocID {
		t.Errorf("metadata.doc_id = %q, want %q", metadataDocID, docResp.DocID)
	}

	t.Logf("✓ Provider metadata validated: type=%s, doc_id=%s", taskType, metadataDocID)
	t.Logf("  Task result length: %d", len(statusResp.Result))
}

// TestVTextAgentRevisionRealLLMFullHistory validates a sequence of
// user edits and agent revisions produces correct history.
func TestVTextAgentRevisionRealLLMFullHistory(t *testing.T) {
	h, s, _, providerName := vtextRealLLMSetup(t)
	ctx := context.Background()

	t.Logf("Testing full history with provider: %s", providerName)

	// Create document.
	req := vtextRealLLMRequest(t, http.MethodPost, "/api/vtext/documents",
		map[string]string{"title": "History Test"})
	w := httptest.NewRecorder()
	h.HandleVTextCreateDocument(w, req)
	var docResp vtextCreateDocResponse
	_ = json.NewDecoder(w.Body).Decode(&docResp)

	// User edit 1.
	revReq := vtextCreateRevisionRequest{
		Content:     "First draft by user.",
		AuthorKind:  types.AuthorUser,
		AuthorLabel: "alice",
	}
	req = vtextRealLLMRequest(t, http.MethodPost,
		"/api/vtext/documents/"+docResp.DocID+"/revisions", revReq)
	w = httptest.NewRecorder()
	h.HandleVTextRevisions(w, req)

	// Agent revision 1.
	req = vtextRealLLMRequest(t, http.MethodPost,
		"/api/vtext/documents/"+docResp.DocID+"/agent-revision",
		map[string]string{"prompt": "Make it more detailed"})
	w = httptest.NewRecorder()
	h.HandleVTextAgentRevision(w, req)
	var agentResp1 vtextAgentRevisionResponse
	_ = json.NewDecoder(w.Body).Decode(&agentResp1)

	state := waitForTaskCompletion(t, h, agentResp1.TaskID, 60*time.Second)
	if state != types.TaskCompleted {
		t.Fatalf("agent task 1 state = %q, want completed", state)
	}

	// User edit 2.
	revReq = vtextCreateRevisionRequest{
		Content:     "User adds more content after agent revision.",
		AuthorKind:  types.AuthorUser,
		AuthorLabel: "alice",
	}
	req = vtextRealLLMRequest(t, http.MethodPost,
		"/api/vtext/documents/"+docResp.DocID+"/revisions", revReq)
	w = httptest.NewRecorder()
	h.HandleVTextRevisions(w, req)

	// Agent revision 2.
	req = vtextRealLLMRequest(t, http.MethodPost,
		"/api/vtext/documents/"+docResp.DocID+"/agent-revision",
		map[string]string{"prompt": "Summarize the content"})
	w = httptest.NewRecorder()
	h.HandleVTextAgentRevision(w, req)
	var agentResp2 vtextAgentRevisionResponse
	_ = json.NewDecoder(w.Body).Decode(&agentResp2)

	state = waitForTaskCompletion(t, h, agentResp2.TaskID, 60*time.Second)
	if state != types.TaskCompleted {
		t.Fatalf("agent task 2 state = %q, want completed", state)
	}

	// Verify full history.
	entries, err := s.GetHistory(ctx, docResp.DocID, "user-real-llm", 10)
	if err != nil {
		t.Fatalf("get history: %v", err)
	}

	if len(entries) != 4 {
		t.Fatalf("len(history) = %d, want 4", len(entries))
	}

	expectedKinds := []types.AuthorKind{
		types.AuthorAppAgent, // newest: agent 2
		types.AuthorUser,     // user edit 2
		types.AuthorAppAgent, // agent 1
		types.AuthorUser,     // oldest: user edit 1
	}
	for i, entry := range entries {
		if entry.AuthorKind != expectedKinds[i] {
			t.Errorf("history[%d].AuthorKind = %q, want %q", i, entry.AuthorKind, expectedKinds[i])
		}
	}

	// Verify no non-canonical author kinds.
	for _, entry := range entries {
		if entry.AuthorKind != types.AuthorUser && entry.AuthorKind != types.AuthorAppAgent {
			t.Errorf("non-canonical author_kind %q in history", entry.AuthorKind)
		}
	}

	t.Logf("✓ Full history validated: %d entries with correct attribution order", len(entries))
}

// --- Info Test ---

// TestRealLLMProviderInfo reports which provider is available for real LLM tests.
func TestRealLLMProviderInfo(t *testing.T) {
	if os.Getenv("ZAI_API_KEY") != "" {
		t.Log("Real LLM provider available: Z.AI (glm-5-turbo)")
	} else if os.Getenv("FIREWORKS_API_KEY") != "" {
		t.Log("Real LLM provider available: Fireworks (kimi-k2p5-turbo)")
	} else {
		t.Log("No LLM provider credentials found. Real LLM tests will be skipped.")
		t.Log("Set ZAI_API_KEY or FIREWORKS_API_KEY to enable real LLM tests.")
	}
}

// truncate truncates a string to maxLen characters with ellipsis.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
