package runtime

import (
	"bytes"
	"context"
	"encoding/json"
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

func vtextAPISetup(t *testing.T) (*APIHandler, *store.Store) {
	t.Helper()
	dir := filepath.Join(os.TempDir(), "go-choir-m3-vtext-test")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	dbPath := filepath.Join(dir, t.Name()+".db")
	promptRoot := filepath.Join(dir, t.Name()+"-prompts")
	_ = os.Remove(dbPath)
	_ = os.RemoveAll(promptRoot)

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open vtext api test store: %v", err)
	}
	t.Cleanup(func() {
		_ = s.Close()
		_ = os.RemoveAll(promptRoot)
	})

	cfg := Config{
		SandboxID:           "sandbox-vtext-test",
		StorePath:           dbPath,
		PromptRoot:          promptRoot,
		ProviderTimeout:     2 * time.Second,
		SupervisionInterval: 5 * time.Second,
	}

	bus := events.NewEventBus()
	provider := NewStubProvider(2 * time.Second)
	rt := New(cfg, s, bus, provider)

	return NewAPIHandler(rt), s
}

func vtextRequest(t *testing.T, method, path string, body interface{}) *http.Request {
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
	req.Header.Set("X-Authenticated-User", "user-1")
	return req
}

// ----- Document creation -----

func TestVTextAPICreateDocument(t *testing.T) {
	h, _ := vtextAPISetup(t)

	req := vtextRequest(t, http.MethodPost, "/api/vtext/documents",
		map[string]string{"title": "My Document"})
	w := httptest.NewRecorder()
	h.HandleVTextCreateDocument(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusCreated, w.Body.String())
	}

	var resp vtextCreateDocResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.DocID == "" {
		t.Error("DocID is empty")
	}
	if resp.Title != "My Document" {
		t.Errorf("Title = %q, want %q", resp.Title, "My Document")
	}
	if resp.OwnerID != "user-1" {
		t.Errorf("OwnerID = %q, want %q", resp.OwnerID, "user-1")
	}
}

func TestVTextAPICreateDocumentAuth(t *testing.T) {
	h, _ := vtextAPISetup(t)

	// No auth header.
	req := httptest.NewRequest(http.MethodPost, "/api/vtext/documents",
		bytes.NewReader([]byte(`{"title":"test"}`)))
	w := httptest.NewRecorder()
	h.HandleVTextCreateDocument(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

// ----- Document list -----

func TestVTextAPIListDocuments(t *testing.T) {
	h, _ := vtextAPISetup(t)

	// Create 2 documents.
	for _, title := range []string{"Doc A", "Doc B"} {
		req := vtextRequest(t, http.MethodPost, "/api/vtext/documents",
			map[string]string{"title": title})
		w := httptest.NewRecorder()
		h.HandleVTextCreateDocument(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("create document: status = %d", w.Code)
		}
	}

	// List documents.
	req := vtextRequest(t, http.MethodGet, "/api/vtext/documents", nil)
	w := httptest.NewRecorder()
	h.HandleVTextListDocuments(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp vtextListDocsResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Documents) != 2 {
		t.Errorf("len(documents) = %d, want 2", len(resp.Documents))
	}
}

// ----- Document get -----

func TestVTextAPIGetDocument(t *testing.T) {
	h, _ := vtextAPISetup(t)

	// Create a document.
	req := vtextRequest(t, http.MethodPost, "/api/vtext/documents",
		map[string]string{"title": "Test Doc"})
	w := httptest.NewRecorder()
	h.HandleVTextCreateDocument(w, req)
	var createResp vtextCreateDocResponse
	_ = json.NewDecoder(w.Body).Decode(&createResp)

	// Get the document.
	req = vtextRequest(t, http.MethodGet, "/api/vtext/documents/"+createResp.DocID, nil)
	w = httptest.NewRecorder()
	h.HandleVTextDocument(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var resp vtextDocumentResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.DocID != createResp.DocID {
		t.Errorf("DocID = %q, want %q", resp.DocID, createResp.DocID)
	}
}

// ----- Revision creation (user edit) -----

func TestVTextAPICreateRevisionUserEdit(t *testing.T) {
	h, _ := vtextAPISetup(t)

	// Create a document.
	req := vtextRequest(t, http.MethodPost, "/api/vtext/documents",
		map[string]string{"title": "Test Doc"})
	w := httptest.NewRecorder()
	h.HandleVTextCreateDocument(w, req)
	var docResp vtextCreateDocResponse
	_ = json.NewDecoder(w.Body).Decode(&docResp)

	// Create a user-authored revision.
	revReq := vtextCreateRevisionRequest{
		Content:     "Hello, world!",
		AuthorKind:  types.AuthorUser,
		AuthorLabel: "alice",
	}
	req = vtextRequest(t, http.MethodPost, "/api/vtext/documents/"+docResp.DocID+"/revisions", revReq)
	w = httptest.NewRecorder()
	h.HandleVTextRevisions(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusCreated, w.Body.String())
	}

	var revResp vtextRevisionResponse
	if err := json.NewDecoder(w.Body).Decode(&revResp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if revResp.RevisionID == "" {
		t.Error("RevisionID is empty")
	}
	if revResp.AuthorKind != types.AuthorUser {
		t.Errorf("AuthorKind = %q, want %q", revResp.AuthorKind, types.AuthorUser)
	}
	if revResp.AuthorLabel != "alice" {
		t.Errorf("AuthorLabel = %q, want %q", revResp.AuthorLabel, "alice")
	}
}

// ----- Revision creation (appagent edit) -----

func TestVTextAPICreateRevisionAppAgent(t *testing.T) {
	h, _ := vtextAPISetup(t)

	// Create a document.
	req := vtextRequest(t, http.MethodPost, "/api/vtext/documents",
		map[string]string{"title": "Test Doc"})
	w := httptest.NewRecorder()
	h.HandleVTextCreateDocument(w, req)
	var docResp vtextCreateDocResponse
	_ = json.NewDecoder(w.Body).Decode(&docResp)

	// Create a user revision first.
	revReq := vtextCreateRevisionRequest{
		Content:     "First draft",
		AuthorKind:  types.AuthorUser,
		AuthorLabel: "alice",
	}
	req = vtextRequest(t, http.MethodPost, "/api/vtext/documents/"+docResp.DocID+"/revisions", revReq)
	w = httptest.NewRecorder()
	h.HandleVTextRevisions(w, req)

	// Create an appagent revision.
	revReq = vtextCreateRevisionRequest{
		Content:     "AI-improved draft",
		AuthorKind:  types.AuthorAppAgent,
		AuthorLabel: "appagent",
	}
	req = vtextRequest(t, http.MethodPost, "/api/vtext/documents/"+docResp.DocID+"/revisions", revReq)
	w = httptest.NewRecorder()
	h.HandleVTextRevisions(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusCreated, w.Body.String())
	}

	var revResp vtextRevisionResponse
	if err := json.NewDecoder(w.Body).Decode(&revResp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if revResp.AuthorKind != types.AuthorAppAgent {
		t.Errorf("AuthorKind = %q, want %q", revResp.AuthorKind, types.AuthorAppAgent)
	}
}

// ----- Invalid author kind rejected -----

func TestVTextAPIRejectInvalidAuthorKind(t *testing.T) {
	h, _ := vtextAPISetup(t)

	// Create a document.
	req := vtextRequest(t, http.MethodPost, "/api/vtext/documents",
		map[string]string{"title": "Test Doc"})
	w := httptest.NewRecorder()
	h.HandleVTextCreateDocument(w, req)
	var docResp vtextCreateDocResponse
	_ = json.NewDecoder(w.Body).Decode(&docResp)

	// Try to create a revision with "worker" author kind.
	revReq := vtextCreateRevisionRequest{
		Content:     "Worker content",
		AuthorKind:  "worker",
		AuthorLabel: "worker-1",
	}
	req = vtextRequest(t, http.MethodPost, "/api/vtext/documents/"+docResp.DocID+"/revisions", revReq)
	w = httptest.NewRecorder()
	h.HandleVTextRevisions(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

// ----- History -----

func TestVTextAPIGetHistory(t *testing.T) {
	h, _ := vtextAPISetup(t)

	// Create a document.
	req := vtextRequest(t, http.MethodPost, "/api/vtext/documents",
		map[string]string{"title": "Test Doc"})
	w := httptest.NewRecorder()
	h.HandleVTextCreateDocument(w, req)
	var docResp vtextCreateDocResponse
	_ = json.NewDecoder(w.Body).Decode(&docResp)

	// Create revisions.
	revReq := vtextCreateRevisionRequest{
		Content:     "First draft",
		AuthorKind:  types.AuthorUser,
		AuthorLabel: "alice",
	}
	req = vtextRequest(t, http.MethodPost, "/api/vtext/documents/"+docResp.DocID+"/revisions", revReq)
	w = httptest.NewRecorder()
	h.HandleVTextRevisions(w, req)

	revReq = vtextCreateRevisionRequest{
		Content:     "AI-improved",
		AuthorKind:  types.AuthorAppAgent,
		AuthorLabel: "appagent",
	}
	req = vtextRequest(t, http.MethodPost, "/api/vtext/documents/"+docResp.DocID+"/revisions", revReq)
	w = httptest.NewRecorder()
	h.HandleVTextRevisions(w, req)

	// Get history.
	req = vtextRequest(t, http.MethodGet, "/api/vtext/documents/"+docResp.DocID+"/history", nil)
	w = httptest.NewRecorder()
	h.HandleVTextHistory(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var resp vtextHistoryResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Entries) != 2 {
		t.Errorf("len(entries) = %d, want 2", len(resp.Entries))
	}
	// Newest first.
	if resp.Entries[0].AuthorKind != types.AuthorAppAgent {
		t.Errorf("first entry AuthorKind = %q, want %q", resp.Entries[0].AuthorKind, types.AuthorAppAgent)
	}
}

// ----- Diff -----

func TestVTextAPIGetDiff(t *testing.T) {
	h, _ := vtextAPISetup(t)

	// Create a document and revisions.
	req := vtextRequest(t, http.MethodPost, "/api/vtext/documents",
		map[string]string{"title": "Test Doc"})
	w := httptest.NewRecorder()
	h.HandleVTextCreateDocument(w, req)
	var docResp vtextCreateDocResponse
	_ = json.NewDecoder(w.Body).Decode(&docResp)

	revReq := vtextCreateRevisionRequest{
		Content:     "First draft",
		AuthorKind:  types.AuthorUser,
		AuthorLabel: "alice",
	}
	req = vtextRequest(t, http.MethodPost, "/api/vtext/documents/"+docResp.DocID+"/revisions", revReq)
	w = httptest.NewRecorder()
	h.HandleVTextRevisions(w, req)
	var rev1Resp vtextRevisionResponse
	_ = json.NewDecoder(w.Body).Decode(&rev1Resp)

	revReq = vtextCreateRevisionRequest{
		Content:     "AI-improved draft",
		AuthorKind:  types.AuthorAppAgent,
		AuthorLabel: "appagent",
	}
	req = vtextRequest(t, http.MethodPost, "/api/vtext/documents/"+docResp.DocID+"/revisions", revReq)
	w = httptest.NewRecorder()
	h.HandleVTextRevisions(w, req)
	var rev2Resp vtextRevisionResponse
	_ = json.NewDecoder(w.Body).Decode(&rev2Resp)

	// Get diff.
	req = vtextRequest(t, http.MethodGet,
		"/api/vtext/diff?from="+rev1Resp.RevisionID+"&to="+rev2Resp.RevisionID, nil)
	w = httptest.NewRecorder()
	h.HandleVTextDiff(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var resp vtextDiffResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.FromRevisionID != rev1Resp.RevisionID {
		t.Errorf("FromRevisionID = %q, want %q", resp.FromRevisionID, rev1Resp.RevisionID)
	}
	if resp.ToRevisionID != rev2Resp.RevisionID {
		t.Errorf("ToRevisionID = %q, want %q", resp.ToRevisionID, rev2Resp.RevisionID)
	}
}

// ----- Blame -----

func TestVTextAPIGetBlame(t *testing.T) {
	h, _ := vtextAPISetup(t)

	// Create a document and revisions.
	req := vtextRequest(t, http.MethodPost, "/api/vtext/documents",
		map[string]string{"title": "Test Doc"})
	w := httptest.NewRecorder()
	h.HandleVTextCreateDocument(w, req)
	var docResp vtextCreateDocResponse
	_ = json.NewDecoder(w.Body).Decode(&docResp)

	revReq := vtextCreateRevisionRequest{
		Content:     "First draft",
		AuthorKind:  types.AuthorUser,
		AuthorLabel: "alice",
	}
	req = vtextRequest(t, http.MethodPost, "/api/vtext/documents/"+docResp.DocID+"/revisions", revReq)
	w = httptest.NewRecorder()
	h.HandleVTextRevisions(w, req)

	revReq = vtextCreateRevisionRequest{
		Content:     "AI-improved draft",
		AuthorKind:  types.AuthorAppAgent,
		AuthorLabel: "appagent",
	}
	req = vtextRequest(t, http.MethodPost, "/api/vtext/documents/"+docResp.DocID+"/revisions", revReq)
	w = httptest.NewRecorder()
	h.HandleVTextRevisions(w, req)
	var rev2Resp vtextRevisionResponse
	_ = json.NewDecoder(w.Body).Decode(&rev2Resp)

	// Get blame.
	req = vtextRequest(t, http.MethodGet,
		"/api/vtext/revisions/"+rev2Resp.RevisionID+"/blame", nil)
	w = httptest.NewRecorder()
	h.HandleVTextBlame(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var resp vtextBlameResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.RevisionID != rev2Resp.RevisionID {
		t.Errorf("RevisionID = %q, want %q", resp.RevisionID, rev2Resp.RevisionID)
	}
	if len(resp.Sections) == 0 {
		t.Error("no blame sections")
	}
}

// ----- Snapshot (view historical revision) -----

func TestVTextAPISnapshotDoesNotMutateHead(t *testing.T) {
	h, s := vtextAPISetup(t)

	// Create a document.
	req := vtextRequest(t, http.MethodPost, "/api/vtext/documents",
		map[string]string{"title": "Test Doc"})
	w := httptest.NewRecorder()
	h.HandleVTextCreateDocument(w, req)
	var docResp vtextCreateDocResponse
	_ = json.NewDecoder(w.Body).Decode(&docResp)

	// Create two revisions.
	revReq := vtextCreateRevisionRequest{
		Content:     "First draft",
		AuthorKind:  types.AuthorUser,
		AuthorLabel: "alice",
	}
	req = vtextRequest(t, http.MethodPost, "/api/vtext/documents/"+docResp.DocID+"/revisions", revReq)
	w = httptest.NewRecorder()
	h.HandleVTextRevisions(w, req)
	var rev1Resp vtextRevisionResponse
	_ = json.NewDecoder(w.Body).Decode(&rev1Resp)

	revReq = vtextCreateRevisionRequest{
		Content:     "Second draft",
		AuthorKind:  types.AuthorUser,
		AuthorLabel: "alice",
	}
	req = vtextRequest(t, http.MethodPost, "/api/vtext/documents/"+docResp.DocID+"/revisions", revReq)
	w = httptest.NewRecorder()
	h.HandleVTextRevisions(w, req)

	// View the first (historical) revision.
	req = vtextRequest(t, http.MethodGet,
		"/api/vtext/revisions/"+rev1Resp.RevisionID, nil)
	w = httptest.NewRecorder()
	h.HandleVTextRevision(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var snapshotResp vtextRevisionResponse
	_ = json.NewDecoder(w.Body).Decode(&snapshotResp)
	if snapshotResp.Content != "First draft" {
		t.Errorf("snapshot content = %q, want %q", snapshotResp.Content, "First draft")
	}

	// Verify the document head is still the second revision.
	doc, err := s.GetDocument(req.Context(), docResp.DocID, "user-1")
	if err != nil {
		t.Fatalf("GetDocument: %v", err)
	}
	if doc.CurrentRevisionID == rev1Resp.RevisionID {
		t.Error("viewing historical snapshot should not change document head")
	}
}

// ----- Auth gating on vtext endpoints -----

func TestVTextAPIAuthGating(t *testing.T) {
	h, _ := vtextAPISetup(t)

	endpoints := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/vtext/documents"},
		{http.MethodPost, "/api/vtext/documents"},
		{http.MethodGet, "/api/vtext/diff"},
	}

	for _, ep := range endpoints {
		req := httptest.NewRequest(ep.method, ep.path, bytes.NewReader(nil))
		w := httptest.NewRecorder()

		switch {
		case strings.HasPrefix(ep.path, "/api/vtext/documents"):
			h.HandleVTextDocumentsRoot(w, req)
		case strings.HasPrefix(ep.path, "/api/vtext/diff"):
			h.HandleVTextDiff(w, req)
		}

		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: status = %d, want %d", ep.method, ep.path, w.Code, http.StatusUnauthorized)
		}
	}
}

// ----- Citations and metadata -----

func TestVTextAPICitationsMetadataRoundTrip(t *testing.T) {
	h, _ := vtextAPISetup(t)

	// Create a document.
	req := vtextRequest(t, http.MethodPost, "/api/vtext/documents",
		map[string]string{"title": "Test Doc"})
	w := httptest.NewRecorder()
	h.HandleVTextCreateDocument(w, req)
	var docResp vtextCreateDocResponse
	_ = json.NewDecoder(w.Body).Decode(&docResp)

	// Create a revision with citations and metadata.
	citations := []types.Citation{
		{ID: "c1", Type: "url", Value: "https://example.com", Label: "Example"},
	}
	citJSON, _ := json.Marshal(citations)
	metaJSON, _ := json.Marshal(map[string]any{"tags": []string{"draft"}})

	revReq := vtextCreateRevisionRequest{
		Content:     "Document with citations",
		AuthorKind:  types.AuthorUser,
		AuthorLabel: "alice",
		Citations:   citJSON,
		Metadata:    metaJSON,
	}
	req = vtextRequest(t, http.MethodPost, "/api/vtext/documents/"+docResp.DocID+"/revisions", revReq)
	w = httptest.NewRecorder()
	h.HandleVTextRevisions(w, req)

	var revResp vtextRevisionResponse
	if err := json.NewDecoder(w.Body).Decode(&revResp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	// Get the revision back and check citations/metadata.
	req = vtextRequest(t, http.MethodGet,
		"/api/vtext/revisions/"+revResp.RevisionID, nil)
	w = httptest.NewRecorder()
	h.HandleVTextRevision(w, req)

	var getResp vtextRevisionResponse
	if err := json.NewDecoder(w.Body).Decode(&getResp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	var gotCitations []types.Citation
	if err := json.Unmarshal(getResp.Citations, &gotCitations); err != nil {
		t.Fatalf("unmarshal citations: %v", err)
	}
	if len(gotCitations) != 1 || gotCitations[0].Value != "https://example.com" {
		t.Errorf("citations round-trip failed: %v", gotCitations)
	}

	var gotMeta map[string]any
	if err := json.Unmarshal(getResp.Metadata, &gotMeta); err != nil {
		t.Fatalf("unmarshal metadata: %v", err)
	}
	tags, _ := gotMeta["tags"].([]interface{})
	if len(tags) != 1 {
		t.Errorf("metadata tags round-trip failed: %v", tags)
	}
}

// ----- Agent revision tests -----

// vtextAPISetupWithRuntime creates a test setup with a started runtime
// so that runs actually execute and complete.
func vtextAPISetupWithRuntime(t *testing.T) (*APIHandler, *store.Store, *Runtime) {
	t.Helper()
	dir := filepath.Join(os.TempDir(), "go-choir-m3-vtext-test")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	dbPath := filepath.Join(dir, t.Name()+".db")
	promptRoot := filepath.Join(dir, t.Name()+"-prompts")
	_ = os.Remove(dbPath)
	_ = os.RemoveAll(promptRoot)

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open vtext api test store: %v", err)
	}
	t.Cleanup(func() {
		_ = s.Close()
		_ = os.RemoveAll(promptRoot)
	})

	cfg := Config{
		SandboxID:           "sandbox-vtext-test",
		StorePath:           dbPath,
		PromptRoot:          promptRoot,
		ProviderTimeout:     5 * time.Second,
		SupervisionInterval: 5 * time.Second,
	}

	bus := events.NewEventBus()
	// Use a short-delay stub provider so runs complete quickly in tests.
	provider := NewStubProvider(50 * time.Millisecond)
	rt := New(cfg, s, bus, provider)

	// Start the runtime so runs execute.
	ctx := context.Background()
	rt.Start(ctx)
	t.Cleanup(func() { rt.Stop() })

	return NewAPIHandler(rt), s, rt
}

// createDocWithUserRevision is a test helper that creates a document and
// a user-authored revision, returning the doc ID and revision ID.
func createDocWithUserRevision(t *testing.T, h *APIHandler) (string, string) {
	t.Helper()

	// Create a document.
	req := vtextRequest(t, http.MethodPost, "/api/vtext/documents",
		map[string]string{"title": "Test Doc"})
	w := httptest.NewRecorder()
	h.HandleVTextCreateDocument(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create document: status = %d, body: %s", w.Code, w.Body.String())
	}
	var docResp vtextCreateDocResponse
	_ = json.NewDecoder(w.Body).Decode(&docResp)

	// Create a user-authored revision.
	revReq := vtextCreateRevisionRequest{
		Content:     "Hello, world!",
		AuthorKind:  types.AuthorUser,
		AuthorLabel: "alice",
	}
	req = vtextRequest(t, http.MethodPost, "/api/vtext/documents/"+docResp.DocID+"/revisions", revReq)
	w = httptest.NewRecorder()
	h.HandleVTextRevisions(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create user revision: status = %d, body: %s", w.Code, w.Body.String())
	}
	var revResp vtextRevisionResponse
	_ = json.NewDecoder(w.Body).Decode(&revResp)

	return docResp.DocID, revResp.RevisionID
}

// waitForTaskCompletion polls the task status until it reaches a terminal
// state or the timeout expires.
func waitForTaskCompletion(t *testing.T, h *APIHandler, taskID string, timeout time.Duration) types.RunState {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		req := vtextRequest(t, http.MethodGet, "/api/agent/status?run_id="+taskID, nil)
		w := httptest.NewRecorder()
		h.HandleRunStatus(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("get task status: status = %d", w.Code)
		}
		var resp runStatusResponse
		_ = json.NewDecoder(w.Body).Decode(&resp)
		if resp.State.Terminal() {
			return resp.State
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("task %s did not complete within %v", taskID, timeout)
	return ""
}

// TestVTextAgentRevisionCreatesCanonicalRevision verifies that submitting
// an agent revision prompt creates a canonical appagent-authored revision
// (VAL-ETEXT-003).
func TestVTextAgentRevisionCreatesCanonicalRevision(t *testing.T) {
	h, s, _ := vtextAPISetupWithRuntime(t)

	docID, _ := createDocWithUserRevision(t, h)

	// Submit an agent revision request.
	req := vtextRequest(t, http.MethodPost, "/api/vtext/documents/"+docID+"/agent-revision",
		map[string]string{"prompt": "Make it more formal"})
	w := httptest.NewRecorder()
	h.HandleVTextAgentRevision(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("agent revision: status = %d, want %d; body: %s", w.Code, http.StatusAccepted, w.Body.String())
	}

	var resp vtextAgentRevisionResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.RunID == "" {
		t.Error("RunID is empty")
	}
	if resp.DocID != docID {
		t.Errorf("DocID = %q, want %q", resp.DocID, docID)
	}

	// Wait for the task to complete and the revision to be created.
	state := waitForTaskCompletion(t, h, resp.RunID, 5*time.Second)
	if state != types.RunCompleted {
		t.Fatalf("task state = %q, want %q", state, types.RunCompleted)
	}

	// Verify that a canonical appagent-authored revision was created.
	revs, err := s.ListRevisionsByDoc(context.Background(), docID, "user-1", 10)
	if err != nil {
		t.Fatalf("list revisions: %v", err)
	}

	// Should have 2 revisions: user + appagent.
	if len(revs) != 2 {
		t.Fatalf("len(revisions) = %d, want 2", len(revs))
	}

	// Find the appagent revision.
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
	if agentRev.AuthorLabel != "appagent" {
		t.Errorf("AuthorLabel = %q, want %q", agentRev.AuthorLabel, "appagent")
	}
	if agentRev.Content == "" {
		t.Error("appagent revision content is empty")
	}

	// Document head should be the appagent revision.
	doc, err := s.GetDocument(context.Background(), docID, "user-1")
	if err != nil {
		t.Fatalf("get document: %v", err)
	}
	if doc.CurrentRevisionID != agentRev.RevisionID {
		t.Errorf("document head = %q, want appagent revision %q", doc.CurrentRevisionID, agentRev.RevisionID)
	}
}

// TestVTextAgentRevisionAuthRequired verifies that agent revision
// requires authentication (VAL-ETEXT-003: auth-gated).
func TestVTextAgentRevisionAuthRequired(t *testing.T) {
	h, _, _ := vtextAPISetupWithRuntime(t)

	docID, _ := createDocWithUserRevision(t, h)

	// No auth header.
	req := httptest.NewRequest(http.MethodPost, "/api/vtext/documents/"+docID+"/agent-revision",
		bytes.NewReader([]byte(`{"prompt":"test"}`)))
	w := httptest.NewRecorder()
	h.HandleVTextAgentRevision(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

// TestVTextAgentRevisionPreservesUserAndAppAgentAttribution verifies
// that an end-to-end flow preserves both user and appagent attribution
// in history (VAL-CROSS-119).
func TestVTextAgentRevisionPreservesUserAndAppAgentAttribution(t *testing.T) {
	h, s, _ := vtextAPISetupWithRuntime(t)

	docID, _ := createDocWithUserRevision(t, h)

	// Submit an agent revision.
	req := vtextRequest(t, http.MethodPost, "/api/vtext/documents/"+docID+"/agent-revision",
		map[string]string{"prompt": "Improve the text"})
	w := httptest.NewRecorder()
	h.HandleVTextAgentRevision(w, req)
	var resp vtextAgentRevisionResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)

	// Wait for completion.
	state := waitForTaskCompletion(t, h, resp.RunID, 5*time.Second)
	if state != types.RunCompleted {
		t.Fatalf("task state = %q, want %q", state, types.RunCompleted)
	}

	// Make another user edit after the agent revision.
	revReq := vtextCreateRevisionRequest{
		Content:     "User final edit",
		AuthorKind:  types.AuthorUser,
		AuthorLabel: "alice",
	}
	req = vtextRequest(t, http.MethodPost, "/api/vtext/documents/"+docID+"/revisions", revReq)
	w = httptest.NewRecorder()
	h.HandleVTextRevisions(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("user edit after agent: status = %d, body: %s", w.Code, w.Body.String())
	}

	// Get the history and verify both user and appagent attribution.
	entries, err := s.GetHistory(context.Background(), docID, "user-1", 10)
	if err != nil {
		t.Fatalf("get history: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("len(history) = %d, want 3", len(entries))
	}

	// History is newest-first.
	// Entry 0: latest user edit
	// Entry 1: appagent revision
	// Entry 2: initial user edit
	if entries[0].AuthorKind != types.AuthorUser {
		t.Errorf("entry 0 AuthorKind = %q, want %q", entries[0].AuthorKind, types.AuthorUser)
	}
	if entries[1].AuthorKind != types.AuthorAppAgent {
		t.Errorf("entry 1 AuthorKind = %q, want %q", entries[1].AuthorKind, types.AuthorAppAgent)
	}
	if entries[2].AuthorKind != types.AuthorUser {
		t.Errorf("entry 2 AuthorKind = %q, want %q", entries[2].AuthorKind, types.AuthorUser)
	}

	// Verify that the appagent revision has the correct label.
	if entries[1].AuthorLabel != "appagent" {
		t.Errorf("entry 1 AuthorLabel = %q, want %q", entries[1].AuthorLabel, "appagent")
	}
}

// TestVTextAgentRevisionNoWorkerAuthorship verifies that when subordinate
// workers might contribute to an appagent-driven change, the resulting
// canonical history attributes the change to the appagent, not to any
// worker identity (VAL-CROSS-120).
func TestVTextAgentRevisionNoWorkerAuthorship(t *testing.T) {
	h, s, _ := vtextAPISetupWithRuntime(t)

	docID, _ := createDocWithUserRevision(t, h)

	// Submit an agent revision.
	req := vtextRequest(t, http.MethodPost, "/api/vtext/documents/"+docID+"/agent-revision",
		map[string]string{"prompt": "Make it better"})
	w := httptest.NewRecorder()
	h.HandleVTextAgentRevision(w, req)
	var resp vtextAgentRevisionResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)

	// Wait for completion.
	state := waitForTaskCompletion(t, h, resp.RunID, 5*time.Second)
	if state != types.RunCompleted {
		t.Fatalf("task state = %q, want %q", state, types.RunCompleted)
	}

	// Verify that no "worker" author kind exists in the history.
	entries, err := s.GetHistory(context.Background(), docID, "user-1", 10)
	if err != nil {
		t.Fatalf("get history: %v", err)
	}
	for _, entry := range entries {
		if entry.AuthorKind != types.AuthorUser && entry.AuthorKind != types.AuthorAppAgent {
			t.Errorf("found non-canonical author_kind %q in history — workers must not be canonical authors (VAL-CROSS-120)", entry.AuthorKind)
		}
	}
}

// TestVTextAgentRevisionNoDuplicateOnRenewalRetry verifies that renewal
// and retry does not duplicate a canonical document mutation (VAL-CROSS-122).
func TestVTextAgentRevisionNoDuplicateOnRenewalRetry(t *testing.T) {
	h, s, _ := vtextAPISetupWithRuntime(t)

	docID, _ := createDocWithUserRevision(t, h)

	// Submit an agent revision.
	req := vtextRequest(t, http.MethodPost, "/api/vtext/documents/"+docID+"/agent-revision",
		map[string]string{"prompt": "Make it concise"})
	w := httptest.NewRecorder()
	h.HandleVTextAgentRevision(w, req)
	var resp1 vtextAgentRevisionResponse
	_ = json.NewDecoder(w.Body).Decode(&resp1)

	// Simulate a renewal/retry by submitting the same request again
	// before the task completes. The idempotency check should return
	// the same task ID.
	req = vtextRequest(t, http.MethodPost, "/api/vtext/documents/"+docID+"/agent-revision",
		map[string]string{"prompt": "Make it concise"})
	w = httptest.NewRecorder()
	h.HandleVTextAgentRevision(w, req)

	var resp2 vtextAgentRevisionResponse
	if err := json.NewDecoder(w.Body).Decode(&resp2); err != nil {
		t.Fatalf("decode retry response: %v", err)
	}

	// The retry should return the same task ID (idempotent).
	if resp2.RunID != resp1.RunID {
		t.Errorf("retry returned different task ID: %q vs %q — should be idempotent (VAL-CROSS-122)", resp2.RunID, resp1.RunID)
	}

	// Wait for the task to complete.
	state := waitForTaskCompletion(t, h, resp1.RunID, 5*time.Second)
	if state != types.RunCompleted {
		t.Fatalf("task state = %q, want %q", state, types.RunCompleted)
	}

	// Verify only one appagent revision was created (no duplicate).
	revs, err := s.ListRevisionsByDoc(context.Background(), docID, "user-1", 10)
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
		t.Errorf("found %d appagent revisions, want 1 — duplicate mutation detected (VAL-CROSS-122)", agentCount)
	}
}

// TestVTextAgentRevisionMutationCompletedOnlyOnce verifies that even if
// the task completion hook is called multiple times (e.g., after crash
// recovery), only one canonical revision is created (VAL-CROSS-122).
func TestVTextAgentRevisionMutationCompletedOnlyOnce(t *testing.T) {
	_, s, rt := vtextAPISetupWithRuntime(t)

	ctx := context.Background()

	// Create a document manually.
	doc := types.Document{
		DocID:     "doc-mutation-test",
		OwnerID:   "user-1",
		Title:     "Mutation Test",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := s.CreateDocument(ctx, doc); err != nil {
		t.Fatalf("create document: %v", err)
	}

	// Create a user revision.
	rev := types.Revision{
		RevisionID:  "rev-user-1",
		DocID:       "doc-mutation-test",
		OwnerID:     "user-1",
		AuthorKind:  types.AuthorUser,
		AuthorLabel: "alice",
		Content:     "Original content",
		CreatedAt:   time.Now().UTC(),
	}
	if err := s.CreateRevision(ctx, rev); err != nil {
		t.Fatalf("create revision: %v", err)
	}

	// Create an agent mutation record.
	mutation := store.AgentMutation{
		DocID:     "doc-mutation-test",
		RunID:    "task-mutation-test",
		OwnerID:   "user-1",
		State:     "pending",
		CreatedAt: time.Now().UTC(),
	}
	if err := s.CreateAgentMutation(ctx, mutation); err != nil {
		t.Fatalf("create agent mutation: %v", err)
	}

	// Create a completed task record with vtext agent revision metadata.
	taskRec := &types.RunRecord{
		RunID:    "task-mutation-test",
		OwnerID:   "user-1",
		SandboxID: "sandbox-vtext-test",
		State:     types.RunCompleted,
		Prompt:    "Revise the document",
		Result:    "Revised content",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
		Metadata: map[string]any{
			"type":                "vtext_agent_revision",
			"doc_id":              "doc-mutation-test",
			"current_revision_id": "rev-user-1",
		},
	}

	// Call handleRunCompletion twice to simulate duplicate processing.
	rt.handleRunCompletion(ctx, taskRec)
	rt.handleRunCompletion(ctx, taskRec)

	// Verify only one appagent revision was created.
	revs, err := s.ListRevisionsByDoc(ctx, "doc-mutation-test", "user-1", 10)
	if err != nil {
		t.Fatalf("list revisions: %v", err)
	}

	agentCount := 0
	for _, r := range revs {
		if r.AuthorKind == types.AuthorAppAgent {
			agentCount++
		}
	}
	if agentCount != 1 {
		t.Errorf("found %d appagent revisions, want 1 — duplicate canonical revision detected (VAL-CROSS-122)", agentCount)
	}
}

// TestVTextAgentRevisionProgressEvents verifies that progress events
// are emitted during agent revision execution with the doc_id so
// the frontend can correlate to the open document (VAL-ETEXT-004).
func TestVTextAgentRevisionProgressEvents(t *testing.T) {
	h, s, _ := vtextAPISetupWithRuntime(t)

	docID, _ := createDocWithUserRevision(t, h)

	// Subscribe to events before submitting the task.
	bus := s // We'll use the store to query events after completion.

	// Submit an agent revision.
	req := vtextRequest(t, http.MethodPost, "/api/vtext/documents/"+docID+"/agent-revision",
		map[string]string{"prompt": "Add more detail"})
	w := httptest.NewRecorder()
	h.HandleVTextAgentRevision(w, req)
	var resp vtextAgentRevisionResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)

	// Wait for completion.
	state := waitForTaskCompletion(t, h, resp.RunID, 5*time.Second)
	if state != types.RunCompleted {
		t.Fatalf("task state = %q, want %q", state, types.RunCompleted)
	}

	// Check that vtext agent revision events were persisted.
	events, err := bus.ListEvents(context.Background(), resp.RunID, 200)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}

	// We should find vtext.agent_revision.started and
	// vtext.agent_revision.completed events.
	var foundStarted, foundCompleted bool
	for _, ev := range events {
		switch ev.Kind {
		case types.EventVTextAgentRevisionStarted:
			foundStarted = true
			// Verify the payload contains doc_id.
			var payload map[string]string
			if err := json.Unmarshal(ev.Payload, &payload); err == nil {
				if payload["doc_id"] != docID {
					t.Errorf("started event doc_id = %q, want %q", payload["doc_id"], docID)
				}
			}
		case types.EventVTextAgentRevisionCompleted:
			foundCompleted = true
			var payload map[string]string
			if err := json.Unmarshal(ev.Payload, &payload); err == nil {
				if payload["doc_id"] != docID {
					t.Errorf("completed event doc_id = %q, want %q", payload["doc_id"], docID)
				}
				if payload["revision_id"] == "" {
					t.Error("completed event missing revision_id")
				}
			}
		}
	}
	if !foundStarted {
		t.Error("missing vtext.agent_revision.started event (VAL-ETEXT-004)")
	}
	if !foundCompleted {
		t.Error("missing vtext.agent_revision.completed event (VAL-ETEXT-004)")
	}
}

// TestVTextAgentRevisionAcceptsReviseEventWithoutPrompt verifies that the
// frontend can submit a plain revise event and let the backend compile the
// effective vtext request from document state.
func TestVTextAgentRevisionAcceptsReviseEventWithoutPrompt(t *testing.T) {
	h, _, rt := vtextAPISetupWithRuntime(t)

	docID, _ := createDocWithUserRevision(t, h)

	req := vtextRequest(t, http.MethodPost, "/api/vtext/documents/"+docID+"/agent-revision",
		map[string]string{"intent": "revise"})
	w := httptest.NewRecorder()
	h.HandleVTextAgentRevision(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusAccepted, w.Body.String())
	}

	var resp vtextAgentRevisionResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	task, err := rt.GetRun(context.Background(), resp.RunID, "user-1")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if !strings.Contains(task.Prompt, "A revise event was triggered") {
		t.Fatalf("compiled prompt missing revise event context: %q", task.Prompt)
	}
	if !strings.Contains(task.Prompt, "Hello, world!") {
		t.Fatalf("compiled prompt missing current document content: %q", task.Prompt)
	}
}

// TestVTextAgentRevisionDocumentNotFound verifies that requesting an
// agent revision for a non-existent document returns 404.
func TestVTextAgentRevisionDocumentNotFound(t *testing.T) {
	h, _, _ := vtextAPISetupWithRuntime(t)

	req := vtextRequest(t, http.MethodPost, "/api/vtext/documents/nonexistent/agent-revision",
		map[string]string{"prompt": "test"})
	w := httptest.NewRecorder()
	h.HandleVTextAgentRevision(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

// TestVTextAgentRevisionWrongOwner verifies that requesting an agent
// revision for a document owned by another user returns 404.
func TestVTextAgentRevisionWrongOwner(t *testing.T) {
	h, _, _ := vtextAPISetupWithRuntime(t)

	docID, _ := createDocWithUserRevision(t, h)

	// Use a different user.
	req := httptest.NewRequest(http.MethodPost, "/api/vtext/documents/"+docID+"/agent-revision",
		bytes.NewReader([]byte(`{"prompt":"test"}`)))
	req.Header.Set("X-Authenticated-User", "user-2")
	w := httptest.NewRecorder()
	h.HandleVTextAgentRevision(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d (wrong owner)", w.Code, http.StatusNotFound)
	}
}
