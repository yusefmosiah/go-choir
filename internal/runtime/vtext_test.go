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

func vtextAPISetupWithProvider(t *testing.T, provider Provider, installTools bool) (*APIHandler, *store.Store, *Runtime) {
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
		VTextWakeDebounce:   50 * time.Millisecond,
	}

	bus := events.NewEventBus()
	rt := New(cfg, s, bus, provider)
	if installTools {
		cwd, err := os.Getwd()
		if err != nil {
			t.Fatalf("get working directory: %v", err)
		}
		if err := rt.InstallDefaultAgentTools(cwd); err != nil {
			t.Fatalf("install default agent tools: %v", err)
		}
	}

	// Start the runtime so runs execute.
	ctx := context.Background()
	rt.Start(ctx)
	t.Cleanup(func() { rt.Stop() })

	return NewAPIHandler(rt), s, rt
}

// vtextAPISetupWithRuntime creates a test setup with a started runtime
// so that runs actually execute and complete.
func vtextAPISetupWithRuntime(t *testing.T) (*APIHandler, *store.Store, *Runtime) {
	t.Helper()
	return vtextAPISetupWithProvider(t, NewStubProvider(50*time.Millisecond), false)
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
		req := vtextRequest(t, http.MethodGet, "/api/agent/status?loop_id="+taskID, nil)
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

func waitForRevisionCount(t *testing.T, s *store.Store, docID, ownerID string, want int, timeout time.Duration) []types.Revision {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		revs, err := s.ListRevisionsByDoc(context.Background(), docID, ownerID, 20)
		if err == nil && len(revs) >= want {
			return revs
		}
		time.Sleep(20 * time.Millisecond)
	}
	revs, _ := s.ListRevisionsByDoc(context.Background(), docID, ownerID, 20)
	t.Fatalf("document %s did not reach %d revisions within %v; got %d", docID, want, timeout, len(revs))
	return nil
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

func TestVTextSystemPromptSharesChoirCoreContext(t *testing.T) {
	rt, _ := testRuntime(t)

	rec := &types.RunRecord{
		RunID:        "run-vtext-shared-prompt",
		AgentID:      "vtext:doc-1",
		ChannelID:    "doc-1",
		OwnerID:      "user-alice",
		AgentProfile: AgentProfileVText,
		AgentRole:    AgentProfileVText,
		Prompt:       "What's the latest with AI?",
	}

	prompt, err := rt.systemPromptForRun(rec)
	if err != nil {
		t.Fatalf("systemPromptForRun: %v", err)
	}
	if !strings.Contains(prompt, "You are one agent inside Choir, a multiagent writing, research, and execution system.") {
		t.Fatalf("system prompt missing shared Choir context: %q", prompt)
	}
	if !strings.Contains(prompt, "VText is a durable document owner, not a one-shot answerer.") {
		t.Fatalf("system prompt missing vtext wake semantics: %q", prompt)
	}
	if !strings.Contains(prompt, "Current coordination channel: doc-1.") {
		t.Fatalf("system prompt missing coordination channel: %q", prompt)
	}
}

func TestVTextAgentRevisionAllowsPriorsFirstDraft(t *testing.T) {
	provider := newMockToolLoopProvider(&ToolLoopResponse{
		StopReason: "end_turn",
		Text:       "Here is a polished answer from priors alone.",
		Model:      "test-model",
	})
	provider.Provider = NewStubProvider(1 * time.Millisecond)

	h, s, _ := vtextAPISetupWithProvider(t, provider, true)
	docID, _ := createDocWithUserRevision(t, h)

	req := vtextRequest(t, http.MethodPost, "/api/vtext/documents/"+docID+"/agent-revision",
		map[string]string{"prompt": "What's the latest with AI?"})
	w := httptest.NewRecorder()
	h.HandleVTextAgentRevision(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("agent revision: status = %d, want %d; body: %s", w.Code, http.StatusAccepted, w.Body.String())
	}

	var resp vtextAgentRevisionResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	state := waitForTaskCompletion(t, h, resp.RunID, 5*time.Second)
	if state != types.RunCompleted {
		t.Fatalf("run state = %q, want %q", state, types.RunCompleted)
	}

	revs, err := s.ListRevisionsByDoc(context.Background(), docID, "user-1", 10)
	if err != nil {
		t.Fatalf("list revisions: %v", err)
	}
	if len(revs) != 2 {
		t.Fatalf("revision count = %d, want 2", len(revs))
	}
	foundAppAgent := false
	for _, rev := range revs {
		if rev.AuthorKind == types.AuthorAppAgent && strings.Contains(rev.Content, "priors alone") {
			foundAppAgent = true
			break
		}
	}
	if !foundAppAgent {
		t.Fatalf("expected priors draft appagent revision, got %+v", revs)
	}
}

func TestVTextWorkerMessageAutoWakeCreatesFollowUpRevision(t *testing.T) {
	provider := NewStubProvider(1 * time.Millisecond)
	provider.Result = "Integrated grounded findings into the next revision."

	h, s, rt := vtextAPISetupWithProvider(t, provider, false)
	docID, _ := createDocWithUserRevision(t, h)

	userRevReq := vtextCreateRevisionRequest{
		Content:     "Original draft.\n\nAdd a short section about recent model releases.",
		AuthorKind:  types.AuthorUser,
		AuthorLabel: "user",
	}
	userRevReqBody := vtextRequest(t, http.MethodPost, "/api/vtext/documents/"+docID+"/revisions", userRevReq)
	userRevW := httptest.NewRecorder()
	h.HandleVTextRevisions(userRevW, userRevReqBody)
	if userRevW.Code != http.StatusCreated {
		t.Fatalf("second user revision: status = %d, want %d; body: %s", userRevW.Code, http.StatusCreated, userRevW.Body.String())
	}

	researchRun, err := rt.StartRunWithMetadata(context.Background(), "Ground the recent release claims", "user-1", map[string]any{
		runMetadataAgentProfile: AgentProfileResearcher,
		runMetadataAgentRole:    AgentProfileResearcher,
		runMetadataChannelID:    docID,
	})
	if err != nil {
		t.Fatalf("start research run: %v", err)
	}
	if _, err := rt.ChannelCast(WithToolExecutionContext(context.Background(), researchRun), docID, "vtext:"+docID, "", "researcher-1", "researcher", "Evidence: the latest public model releases shipped this week with stronger reasoning and tool use."); err != nil {
		t.Fatalf("post worker message: %v", err)
	}

	revs := waitForRevisionCount(t, s, docID, "user-1", 3, 5*time.Second)
	foundAppAgent := false
	for _, rev := range revs {
		if rev.AuthorKind == types.AuthorAppAgent && strings.Contains(rev.Content, "Integrated grounded findings") {
			foundAppAgent = true
			break
		}
	}
	if !foundAppAgent {
		t.Fatalf("expected wake-driven appagent revision, got %+v", revs)
	}

	runs, err := rt.Store().ListRunsByChannel(context.Background(), "user-1", docID, 20)
	if err != nil {
		t.Fatalf("list channel runs: %v", err)
	}
	var wakeRun *types.RunRecord
	for i := range runs {
		if agentProfileForRun(&runs[i]) == AgentProfileVText && runs[i].ParentRunID == researchRun.RunID {
			wakeRun = &runs[i]
			break
		}
	}
	if wakeRun == nil {
		t.Fatalf("expected wake-driven vtext run on channel %s, got %+v", docID, runs)
	}
	if !strings.Contains(wakeRun.Prompt, "Recent addressed worker messages") {
		t.Fatalf("wake run prompt missing worker message context: %q", wakeRun.Prompt)
	}
	if !strings.Contains(wakeRun.Prompt, "Evidence: the latest public model releases") {
		t.Fatalf("wake run prompt missing worker message content: %q", wakeRun.Prompt)
	}
	if !strings.Contains(wakeRun.Prompt, "User-authored revision diffs (oldest to newest)") {
		t.Fatalf("wake run prompt missing user diff compaction context: %q", wakeRun.Prompt)
	}
}

func TestVTextWorkerMessageAutoWakeBatchesRapidMessages(t *testing.T) {
	provider := NewStubProvider(1 * time.Millisecond)
	provider.Result = "Integrated multiple grounded findings into one revision."

	h, s, rt := vtextAPISetupWithProvider(t, provider, false)
	docID, _ := createDocWithUserRevision(t, h)

	userRevReq := vtextCreateRevisionRequest{
		Content:     "Original draft.\n\nNeed the newest facts.",
		AuthorKind:  types.AuthorUser,
		AuthorLabel: "user",
	}
	userRevReqBody := vtextRequest(t, http.MethodPost, "/api/vtext/documents/"+docID+"/revisions", userRevReq)
	userRevW := httptest.NewRecorder()
	h.HandleVTextRevisions(userRevW, userRevReqBody)
	if userRevW.Code != http.StatusCreated {
		t.Fatalf("second user revision: status = %d, want %d; body: %s", userRevW.Code, http.StatusCreated, userRevW.Body.String())
	}

	researchRun, err := rt.StartRunWithMetadata(context.Background(), "Research the latest facts", "user-1", map[string]any{
		runMetadataAgentProfile: AgentProfileResearcher,
		runMetadataAgentRole:    AgentProfileResearcher,
		runMetadataChannelID:    docID,
	})
	if err != nil {
		t.Fatalf("start research run: %v", err)
	}
	postWorkerMessage := func(content string) {
		t.Helper()
		if _, err := rt.ChannelCast(WithToolExecutionContext(context.Background(), researchRun), docID, "vtext:"+docID, "", "researcher-1", "researcher", content); err != nil {
			t.Fatalf("post worker message %q: %v", content, err)
		}
	}
	postWorkerMessage("Evidence A: the first grounded fact arrived.")
	postWorkerMessage("Evidence B: the second grounded fact arrived.")

	revs := waitForRevisionCount(t, s, docID, "user-1", 3, 5*time.Second)
	appAgentRevisions := 0
	for _, rev := range revs {
		if rev.AuthorKind == types.AuthorAppAgent && strings.Contains(rev.Content, "Integrated multiple grounded findings") {
			appAgentRevisions++
		}
	}
	if appAgentRevisions != 1 {
		t.Fatalf("expected exactly one wake-driven appagent revision, got %d revisions: %+v", appAgentRevisions, revs)
	}

	runs, err := rt.Store().ListRunsByChannel(context.Background(), "user-1", docID, 20)
	if err != nil {
		t.Fatalf("list channel runs: %v", err)
	}
	var wakeRuns []types.RunRecord
	for i := range runs {
		if agentProfileForRun(&runs[i]) == AgentProfileVText && runs[i].ParentRunID == researchRun.RunID {
			wakeRuns = append(wakeRuns, runs[i])
		}
	}
	if len(wakeRuns) != 1 {
		t.Fatalf("expected one debounced vtext wake run, got %+v", wakeRuns)
	}
	if !strings.Contains(wakeRuns[0].Prompt, "Evidence A: the first grounded fact arrived.") {
		t.Fatalf("wake run prompt missing first worker message: %q", wakeRuns[0].Prompt)
	}
	if !strings.Contains(wakeRuns[0].Prompt, "Evidence B: the second grounded fact arrived.") {
		t.Fatalf("wake run prompt missing second worker message: %q", wakeRuns[0].Prompt)
	}
}

func TestSubmitResearchFindingsWakeUsesSameDebouncedPath(t *testing.T) {
	provider := NewStubProvider(1 * time.Millisecond)
	provider.Result = "Integrated persisted findings into the next revision."

	h, s, rt := vtextAPISetupWithProvider(t, provider, true)
	docID, _ := createDocWithUserRevision(t, h)

	userRevReq := vtextCreateRevisionRequest{
		Content:     "Original draft.\n\nNeed a sourced update.",
		AuthorKind:  types.AuthorUser,
		AuthorLabel: "user",
	}
	userRevReqBody := vtextRequest(t, http.MethodPost, "/api/vtext/documents/"+docID+"/revisions", userRevReq)
	userRevW := httptest.NewRecorder()
	h.HandleVTextRevisions(userRevW, userRevReqBody)
	if userRevW.Code != http.StatusCreated {
		t.Fatalf("second user revision: status = %d, want %d; body: %s", userRevW.Code, http.StatusCreated, userRevW.Body.String())
	}

	vtextRun, err := rt.StartRunWithMetadata(context.Background(), "Own the document", "user-1", map[string]any{
		runMetadataAgentProfile: AgentProfileVText,
		runMetadataAgentRole:    AgentProfileVText,
		runMetadataChannelID:    docID,
		runMetadataAgentID:      "vtext:" + docID,
		"doc_id":                docID,
	})
	if err != nil {
		t.Fatalf("start vtext run: %v", err)
	}
	researcherRun, err := rt.StartChildRun(context.Background(), vtextRun.RunID, "Research the update", "user-1", map[string]any{
		runMetadataAgentProfile: AgentProfileResearcher,
		runMetadataAgentRole:    AgentProfileResearcher,
		runMetadataChannelID:    docID,
	})
	if err != nil {
		t.Fatalf("start researcher run: %v", err)
	}

	researcherRegistry := rt.ToolRegistryForProfile(AgentProfileResearcher)
	raw, err := researcherRegistry.Execute(WithToolExecutionContext(context.Background(), researcherRun), "submit_research_findings", json.RawMessage(`{
		"finding_id":"finding-stream-001",
		"findings":["A new release landed this week."],
		"evidence":[
			{
				"kind":"web_page",
				"source_uri":"https://example.com/release",
				"title":"Release notes",
				"content":"The release notes describe the new capabilities."
			}
		],
		"notes":["Prefer a brief update in the next draft."]
	}`))
	if err != nil {
		t.Fatalf("submit_research_findings: %v", err)
	}
	var findingResp struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(raw), &findingResp); err != nil {
		t.Fatalf("decode submit_research_findings: %v", err)
	}
	if findingResp.Status != "submitted" {
		t.Fatalf("submit_research_findings status = %q, want submitted", findingResp.Status)
	}

	revs := waitForRevisionCount(t, s, docID, "user-1", 3, 5*time.Second)
	foundAppAgent := false
	for _, rev := range revs {
		if rev.AuthorKind == types.AuthorAppAgent && strings.Contains(rev.Content, "Integrated persisted findings") {
			foundAppAgent = true
			break
		}
	}
	if !foundAppAgent {
		t.Fatalf("expected findings-driven appagent revision, got %+v", revs)
	}

	runs, err := rt.Store().ListRunsByChannel(context.Background(), "user-1", docID, 20)
	if err != nil {
		t.Fatalf("list channel runs: %v", err)
	}
	var wakeRun *types.RunRecord
	for i := range runs {
		if agentProfileForRun(&runs[i]) == AgentProfileVText && runs[i].ParentRunID == researcherRun.RunID {
			wakeRun = &runs[i]
			break
		}
	}
	if wakeRun == nil {
		t.Fatalf("expected findings-driven vtext wake run on channel %s, got %+v", docID, runs)
	}
	if !strings.Contains(wakeRun.Prompt, "Release notes") {
		t.Fatalf("wake run prompt missing persisted findings evidence context: %q", wakeRun.Prompt)
	}
}

func TestHandleTestVTextResearchFindingsUsesResearcherToolPath(t *testing.T) {
	provider := NewStubProvider(1 * time.Millisecond)
	provider.Result = "Browser test findings revision."

	h, s, rt := vtextAPISetupWithProvider(t, provider, true)
	rt.cfg.EnableTestAPIs = true

	docID, _ := createDocWithUserRevision(t, h)

	revReq := vtextRequest(t, http.MethodPost, "/api/vtext/documents/"+docID+"/agent-revision",
		map[string]string{"prompt": "Write the first draft"})
	revW := httptest.NewRecorder()
	h.HandleVTextAgentRevision(revW, revReq)
	if revW.Code != http.StatusAccepted {
		t.Fatalf("agent revision: status = %d, want %d; body: %s", revW.Code, http.StatusAccepted, revW.Body.String())
	}
	var revResp vtextAgentRevisionResponse
	if err := json.NewDecoder(revW.Body).Decode(&revResp); err != nil {
		t.Fatalf("decode agent revision response: %v", err)
	}
	if state := waitForTaskCompletion(t, h, revResp.RunID, 5*time.Second); state != types.RunCompleted {
		t.Fatalf("agent revision state = %q, want %q", state, types.RunCompleted)
	}

	req := vtextRequest(t, http.MethodPost, "/api/test/vtext/research-findings", map[string]any{
		"doc_id":     docID,
		"finding_id": "browser-hook-001",
		"findings":   []string{"A sourced update arrived."},
		"notes":      []string{"Fold this into the next revision."},
	})
	w := httptest.NewRecorder()
	h.HandleTestVTextResearchFindings(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("test findings status = %d, want %d; body: %s", w.Code, http.StatusAccepted, w.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode findings response: %v", err)
	}
	if got, _ := resp["status"].(string); got != "submitted" {
		t.Fatalf("status = %q, want submitted", got)
	}
	if got, _ := resp["loop_id"].(string); strings.TrimSpace(got) == "" {
		t.Fatal("loop_id should not be empty")
	}

	revs := waitForRevisionCount(t, s, docID, "user-1", 3, 5*time.Second)
	found := false
	for _, rev := range revs {
		if rev.AuthorKind == types.AuthorAppAgent && strings.Contains(rev.Content, "Browser test findings revision.") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected findings-driven revision, got %+v", revs)
	}
}

func TestVTextOpenFileResolvesCanonicalAlias(t *testing.T) {
	h, s, _ := vtextAPISetupWithRuntime(t)

	openReq := func(initialContent string) *httptest.ResponseRecorder {
		req := vtextRequest(t, http.MethodPost, "/api/vtext/files/open", map[string]string{
			"source_path":     "notes/ai-news.md",
			"title":           "ai-news.md",
			"initial_content": initialContent,
		})
		w := httptest.NewRecorder()
		h.HandleVTextRouter(w, req)
		return w
	}

	first := openReq("Initial file content")
	if first.Code != http.StatusCreated {
		t.Fatalf("first open file: status = %d, want %d; body: %s", first.Code, http.StatusCreated, first.Body.String())
	}
	var firstResp vtextOpenFileResponse
	if err := json.NewDecoder(first.Body).Decode(&firstResp); err != nil {
		t.Fatalf("decode first open file response: %v", err)
	}
	if !firstResp.Created {
		t.Fatalf("first open created = false, want true")
	}

	second := openReq("Changed file bytes that should not fork a new doc")
	if second.Code != http.StatusOK {
		t.Fatalf("second open file: status = %d, want %d; body: %s", second.Code, http.StatusOK, second.Body.String())
	}
	var secondResp vtextOpenFileResponse
	if err := json.NewDecoder(second.Body).Decode(&secondResp); err != nil {
		t.Fatalf("decode second open file response: %v", err)
	}
	if secondResp.Created {
		t.Fatalf("second open created = true, want false")
	}
	if secondResp.DocID != firstResp.DocID {
		t.Fatalf("second open doc_id = %q, want %q", secondResp.DocID, firstResp.DocID)
	}

	revs, err := s.ListRevisionsByDoc(context.Background(), firstResp.DocID, "user-1", 10)
	if err != nil {
		t.Fatalf("ListRevisionsByDoc: %v", err)
	}
	if len(revs) != 1 {
		t.Fatalf("len(revisions) = %d, want 1", len(revs))
	}
	if revs[0].Content != "Initial file content" {
		t.Fatalf("initial aliased revision content = %q, want initial file content", revs[0].Content)
	}
}

func TestVTextEnsureManifestCreatesAliasAndFile(t *testing.T) {
	h, s, _ := vtextAPISetupWithRuntime(t)
	filesRoot := t.TempDir()
	t.Setenv("SANDBOX_FILES_ROOT", filesRoot)

	docID, _ := createDocWithUserRevision(t, h)

	req := vtextRequest(t, http.MethodPost, "/api/vtext/documents/"+docID+"/manifest", nil)
	w := httptest.NewRecorder()
	h.HandleVTextRouter(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("ensure manifest: status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var resp vtextEnsureManifestResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode ensure manifest response: %v", err)
	}
	if resp.DocID != docID {
		t.Fatalf("response doc_id = %q, want %q", resp.DocID, docID)
	}
	if resp.SourcePath == "" {
		t.Fatal("response source_path should not be empty")
	}
	if filepath.Ext(resp.SourcePath) != ".vtext" {
		t.Fatalf("response source_path extension = %q, want .vtext", filepath.Ext(resp.SourcePath))
	}

	aliasedDocID, err := s.GetDocumentAlias(context.Background(), "user-1", resp.SourcePath)
	if err != nil {
		t.Fatalf("GetDocumentAlias: %v", err)
	}
	if aliasedDocID != docID {
		t.Fatalf("aliased doc_id = %q, want %q", aliasedDocID, docID)
	}

	bytes, err := os.ReadFile(filepath.Join(filesRoot, filepath.FromSlash(resp.SourcePath)))
	if err != nil {
		t.Fatalf("read manifest file: %v", err)
	}
	var shortcut vtextShortcutFile
	if err := json.Unmarshal(bytes, &shortcut); err != nil {
		t.Fatalf("unmarshal shortcut file: %v\nraw=%s", err, string(bytes))
	}
	if shortcut.Kind != "vtext" {
		t.Fatalf("shortcut kind = %q, want %q", shortcut.Kind, "vtext")
	}
	if shortcut.DocID != docID {
		t.Fatalf("shortcut doc_id = %q, want %q", shortcut.DocID, docID)
	}
	if shortcut.SourcePath != resp.SourcePath {
		t.Fatalf("shortcut source_path = %q, want %q", shortcut.SourcePath, resp.SourcePath)
	}
}

func TestVTextEnsureManifestReusesExistingAlias(t *testing.T) {
	h, s, _ := vtextAPISetupWithRuntime(t)
	filesRoot := t.TempDir()
	t.Setenv("SANDBOX_FILES_ROOT", filesRoot)

	docID, _ := createDocWithUserRevision(t, h)

	firstReq := vtextRequest(t, http.MethodPost, "/api/vtext/documents/"+docID+"/manifest", nil)
	firstW := httptest.NewRecorder()
	h.HandleVTextRouter(firstW, firstReq)
	if firstW.Code != http.StatusOK {
		t.Fatalf("first ensure manifest: status = %d, want %d; body: %s", firstW.Code, http.StatusOK, firstW.Body.String())
	}
	var firstResp vtextEnsureManifestResponse
	if err := json.NewDecoder(firstW.Body).Decode(&firstResp); err != nil {
		t.Fatalf("decode first ensure manifest response: %v", err)
	}

	secondReq := vtextRequest(t, http.MethodPost, "/api/vtext/documents/"+docID+"/manifest", nil)
	secondW := httptest.NewRecorder()
	h.HandleVTextRouter(secondW, secondReq)
	if secondW.Code != http.StatusOK {
		t.Fatalf("second ensure manifest: status = %d, want %d; body: %s", secondW.Code, http.StatusOK, secondW.Body.String())
	}
	var secondResp vtextEnsureManifestResponse
	if err := json.NewDecoder(secondW.Body).Decode(&secondResp); err != nil {
		t.Fatalf("decode second ensure manifest response: %v", err)
	}
	if secondResp.SourcePath != firstResp.SourcePath {
		t.Fatalf("second source_path = %q, want %q", secondResp.SourcePath, firstResp.SourcePath)
	}

	sourcePath, err := s.GetDocumentAliasSourcePath(context.Background(), "user-1", docID)
	if err != nil {
		t.Fatalf("GetDocumentAliasSourcePath: %v", err)
	}
	if sourcePath != firstResp.SourcePath {
		t.Fatalf("stored source_path = %q, want %q", sourcePath, firstResp.SourcePath)
	}
}

func TestVTextCreateRevisionRejectsStaleHead(t *testing.T) {
	h, _, _ := vtextAPISetupWithRuntime(t)
	docID, baseRevisionID := createDocWithUserRevision(t, h)

	headReq := vtextRequest(t, http.MethodPost, "/api/vtext/documents/"+docID+"/revisions", vtextCreateRevisionRequest{
		Content:          "Latest head",
		AuthorKind:       types.AuthorUser,
		AuthorLabel:      "alice",
		ParentRevisionID: baseRevisionID,
	})
	headW := httptest.NewRecorder()
	h.HandleVTextRevisions(headW, headReq)
	if headW.Code != http.StatusCreated {
		t.Fatalf("create head revision: status = %d, want %d; body: %s", headW.Code, http.StatusCreated, headW.Body.String())
	}

	staleReq := vtextRequest(t, http.MethodPost, "/api/vtext/documents/"+docID+"/revisions", vtextCreateRevisionRequest{
		Content:          "Stale write",
		AuthorKind:       types.AuthorUser,
		AuthorLabel:      "alice",
		ParentRevisionID: baseRevisionID,
	})
	staleW := httptest.NewRecorder()
	h.HandleVTextRevisions(staleW, staleReq)
	if staleW.Code != http.StatusConflict {
		t.Fatalf("stale create revision: status = %d, want %d; body: %s", staleW.Code, http.StatusConflict, staleW.Body.String())
	}
}

func TestVTextDocumentStreamSendsSnapshot(t *testing.T) {
	h, s := vtextAPISetup(t)
	docID, _ := createDocWithUserRevision(t, h)

	req := vtextRequest(t, http.MethodGet, "/api/vtext/documents/"+docID+"/stream", nil)
	ctx, cancel := context.WithCancel(context.Background())
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		h.HandleVTextDocumentStream(w, req)
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	if got := w.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("content-type = %q, want text/event-stream", got)
	}

	doc, err := s.GetDocument(context.Background(), docID, "user-1")
	if err != nil {
		t.Fatalf("get document: %v", err)
	}

	var foundSnapshot bool
	for _, ev := range parseVTextStreamEvents(t, w.Body.String()) {
		if ev.Kind != "snapshot" {
			continue
		}
		foundSnapshot = true
		if ev.DocID != docID {
			t.Fatalf("snapshot doc_id = %q, want %q", ev.DocID, docID)
		}
		if ev.CurrentRevisionID != doc.CurrentRevisionID {
			t.Fatalf("snapshot current_revision_id = %q, want %q", ev.CurrentRevisionID, doc.CurrentRevisionID)
		}
		if ev.Pending {
			t.Fatal("snapshot should not report a pending mutation")
		}
	}
	if !foundSnapshot {
		t.Fatal("expected snapshot event in document stream")
	}
}

func TestVTextDocumentStreamEmitsHeadChangeAfterAgentRevision(t *testing.T) {
	h, s, _ := vtextAPISetupWithRuntime(t)
	docID, _ := createDocWithUserRevision(t, h)

	req := vtextRequest(t, http.MethodGet, "/api/vtext/documents/"+docID+"/stream", nil)
	ctx, cancel := context.WithCancel(context.Background())
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		h.HandleVTextDocumentStream(w, req)
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)

	revReq := vtextRequest(t, http.MethodPost, "/api/vtext/documents/"+docID+"/agent-revision",
		map[string]string{"prompt": "Make it more formal"})
	revW := httptest.NewRecorder()
	h.HandleVTextAgentRevision(revW, revReq)
	if revW.Code != http.StatusAccepted {
		t.Fatalf("agent revision: status = %d, want %d; body: %s", revW.Code, http.StatusAccepted, revW.Body.String())
	}

	var resp vtextAgentRevisionResponse
	if err := json.NewDecoder(revW.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	state := waitForTaskCompletion(t, h, resp.RunID, 5*time.Second)
	if state != types.RunCompleted {
		t.Fatalf("task state = %q, want %q", state, types.RunCompleted)
	}

	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done

	doc, err := s.GetDocument(context.Background(), docID, "user-1")
	if err != nil {
		t.Fatalf("get document: %v", err)
	}

	var foundStarted, foundCompleted, foundRevisionCreated, foundHeadChanged bool
	for _, ev := range parseVTextStreamEvents(t, w.Body.String()) {
		switch ev.Kind {
		case "synth_started":
			foundStarted = true
		case "synth_completed":
			foundCompleted = true
		case "revision_created":
			foundRevisionCreated = true
			if ev.RevisionID == "" {
				t.Fatal("revision_created event missing revision_id")
			}
		case "head_changed":
			foundHeadChanged = true
			if ev.CurrentRevisionID != doc.CurrentRevisionID {
				t.Fatalf("head_changed current_revision_id = %q, want %q", ev.CurrentRevisionID, doc.CurrentRevisionID)
			}
		}
	}
	if !foundStarted {
		t.Fatal("expected synth_started event")
	}
	if !foundCompleted {
		t.Fatal("expected synth_completed event")
	}
	if !foundRevisionCreated {
		t.Fatal("expected revision_created event")
	}
	if !foundHeadChanged {
		t.Fatal("expected head_changed event")
	}
}

func TestVTextDocumentStreamEmitsHeadChangeAfterUserRevision(t *testing.T) {
	h, s, _ := vtextAPISetupWithRuntime(t)
	docID, baseRevisionID := createDocWithUserRevision(t, h)

	req := vtextRequest(t, http.MethodGet, "/api/vtext/documents/"+docID+"/stream", nil)
	ctx, cancel := context.WithCancel(context.Background())
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		h.HandleVTextDocumentStream(w, req)
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)

	createReq := vtextRequest(t, http.MethodPost, "/api/vtext/documents/"+docID+"/revisions", vtextCreateRevisionRequest{
		Content:          "User-authored next head",
		AuthorKind:       types.AuthorUser,
		AuthorLabel:      "alice",
		ParentRevisionID: baseRevisionID,
	})
	createW := httptest.NewRecorder()
	h.HandleVTextRevisions(createW, createReq)
	if createW.Code != http.StatusCreated {
		t.Fatalf("create revision: status = %d, want %d; body: %s", createW.Code, http.StatusCreated, createW.Body.String())
	}

	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done

	doc, err := s.GetDocument(context.Background(), docID, "user-1")
	if err != nil {
		t.Fatalf("get document: %v", err)
	}

	var foundRevisionCreated, foundHeadChanged bool
	for _, ev := range parseVTextStreamEvents(t, w.Body.String()) {
		switch ev.Kind {
		case "revision_created":
			foundRevisionCreated = true
			if ev.RevisionID == "" {
				t.Fatal("revision_created event missing revision_id")
			}
		case "head_changed":
			foundHeadChanged = true
			if ev.CurrentRevisionID != doc.CurrentRevisionID {
				t.Fatalf("head_changed current_revision_id = %q, want %q", ev.CurrentRevisionID, doc.CurrentRevisionID)
			}
		}
	}
	if !foundRevisionCreated {
		t.Fatal("expected revision_created event")
	}
	if !foundHeadChanged {
		t.Fatal("expected head_changed event")
	}
}

func parseVTextStreamEvents(t *testing.T, body string) []vtextDocumentStreamEvent {
	t.Helper()
	lines := strings.Split(body, "\n")
	events := make([]vtextDocumentStreamEvent, 0)
	for _, line := range lines {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev vtextDocumentStreamEvent
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
			t.Fatalf("decode vtext stream event: %v", err)
		}
		events = append(events, ev)
	}
	return events
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
		RunID:     "task-mutation-test",
		OwnerID:   "user-1",
		State:     "pending",
		CreatedAt: time.Now().UTC(),
	}
	if err := s.CreateAgentMutation(ctx, mutation); err != nil {
		t.Fatalf("create agent mutation: %v", err)
	}

	// Create a completed task record with vtext agent revision metadata.
	taskRec := &types.RunRecord{
		RunID:     "task-mutation-test",
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
