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

func etextAPISetup(t *testing.T) (*APIHandler, *store.Store) {
	t.Helper()
	dir := filepath.Join(os.TempDir(), "go-choir-m3-etext-test")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	dbPath := filepath.Join(dir, t.Name()+".db")
	_ = os.Remove(dbPath)

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open etext api test store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	cfg := Config{
		SandboxID:           "sandbox-etext-test",
		StorePath:           dbPath,
		ProviderTimeout:     2 * time.Second,
		SupervisionInterval: 5 * time.Second,
	}

	bus := events.NewEventBus()
	provider := NewStubProvider(2 * time.Second)
	rt := New(cfg, s, bus, provider)

	return NewAPIHandler(rt), s
}

func etextRequest(t *testing.T, method, path string, body interface{}) *http.Request {
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

func TestEtextAPICreateDocument(t *testing.T) {
	h, _ := etextAPISetup(t)

	req := etextRequest(t, http.MethodPost, "/api/etext/documents",
		map[string]string{"title": "My Document"})
	w := httptest.NewRecorder()
	h.HandleEtextCreateDocument(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusCreated, w.Body.String())
	}

	var resp etextCreateDocResponse
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

func TestEtextAPICreateDocumentAuth(t *testing.T) {
	h, _ := etextAPISetup(t)

	// No auth header.
	req := httptest.NewRequest(http.MethodPost, "/api/etext/documents",
		bytes.NewReader([]byte(`{"title":"test"}`)))
	w := httptest.NewRecorder()
	h.HandleEtextCreateDocument(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

// ----- Document list -----

func TestEtextAPIListDocuments(t *testing.T) {
	h, _ := etextAPISetup(t)

	// Create 2 documents.
	for _, title := range []string{"Doc A", "Doc B"} {
		req := etextRequest(t, http.MethodPost, "/api/etext/documents",
			map[string]string{"title": title})
		w := httptest.NewRecorder()
		h.HandleEtextCreateDocument(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("create document: status = %d", w.Code)
		}
	}

	// List documents.
	req := etextRequest(t, http.MethodGet, "/api/etext/documents", nil)
	w := httptest.NewRecorder()
	h.HandleEtextListDocuments(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp etextListDocsResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Documents) != 2 {
		t.Errorf("len(documents) = %d, want 2", len(resp.Documents))
	}
}

// ----- Document get -----

func TestEtextAPIGetDocument(t *testing.T) {
	h, _ := etextAPISetup(t)

	// Create a document.
	req := etextRequest(t, http.MethodPost, "/api/etext/documents",
		map[string]string{"title": "Test Doc"})
	w := httptest.NewRecorder()
	h.HandleEtextCreateDocument(w, req)
	var createResp etextCreateDocResponse
	_ = json.NewDecoder(w.Body).Decode(&createResp)

	// Get the document.
	req = etextRequest(t, http.MethodGet, "/api/etext/documents/"+createResp.DocID, nil)
	w = httptest.NewRecorder()
	h.HandleEtextDocument(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var resp etextDocumentResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.DocID != createResp.DocID {
		t.Errorf("DocID = %q, want %q", resp.DocID, createResp.DocID)
	}
}

// ----- Revision creation (user edit) -----

func TestEtextAPICreateRevisionUserEdit(t *testing.T) {
	h, _ := etextAPISetup(t)

	// Create a document.
	req := etextRequest(t, http.MethodPost, "/api/etext/documents",
		map[string]string{"title": "Test Doc"})
	w := httptest.NewRecorder()
	h.HandleEtextCreateDocument(w, req)
	var docResp etextCreateDocResponse
	_ = json.NewDecoder(w.Body).Decode(&docResp)

	// Create a user-authored revision.
	revReq := etextCreateRevisionRequest{
		Content:     "Hello, world!",
		AuthorKind:  types.AuthorUser,
		AuthorLabel: "alice",
	}
	req = etextRequest(t, http.MethodPost, "/api/etext/documents/"+docResp.DocID+"/revisions", revReq)
	w = httptest.NewRecorder()
	h.HandleEtextRevisions(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusCreated, w.Body.String())
	}

	var revResp etextRevisionResponse
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

func TestEtextAPICreateRevisionAppAgent(t *testing.T) {
	h, _ := etextAPISetup(t)

	// Create a document.
	req := etextRequest(t, http.MethodPost, "/api/etext/documents",
		map[string]string{"title": "Test Doc"})
	w := httptest.NewRecorder()
	h.HandleEtextCreateDocument(w, req)
	var docResp etextCreateDocResponse
	_ = json.NewDecoder(w.Body).Decode(&docResp)

	// Create a user revision first.
	revReq := etextCreateRevisionRequest{
		Content:     "First draft",
		AuthorKind:  types.AuthorUser,
		AuthorLabel: "alice",
	}
	req = etextRequest(t, http.MethodPost, "/api/etext/documents/"+docResp.DocID+"/revisions", revReq)
	w = httptest.NewRecorder()
	h.HandleEtextRevisions(w, req)

	// Create an appagent revision.
	revReq = etextCreateRevisionRequest{
		Content:     "AI-improved draft",
		AuthorKind:  types.AuthorAppAgent,
		AuthorLabel: "appagent",
	}
	req = etextRequest(t, http.MethodPost, "/api/etext/documents/"+docResp.DocID+"/revisions", revReq)
	w = httptest.NewRecorder()
	h.HandleEtextRevisions(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusCreated, w.Body.String())
	}

	var revResp etextRevisionResponse
	if err := json.NewDecoder(w.Body).Decode(&revResp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if revResp.AuthorKind != types.AuthorAppAgent {
		t.Errorf("AuthorKind = %q, want %q", revResp.AuthorKind, types.AuthorAppAgent)
	}
}

// ----- Invalid author kind rejected -----

func TestEtextAPIRejectInvalidAuthorKind(t *testing.T) {
	h, _ := etextAPISetup(t)

	// Create a document.
	req := etextRequest(t, http.MethodPost, "/api/etext/documents",
		map[string]string{"title": "Test Doc"})
	w := httptest.NewRecorder()
	h.HandleEtextCreateDocument(w, req)
	var docResp etextCreateDocResponse
	_ = json.NewDecoder(w.Body).Decode(&docResp)

	// Try to create a revision with "worker" author kind.
	revReq := etextCreateRevisionRequest{
		Content:     "Worker content",
		AuthorKind:  "worker",
		AuthorLabel: "worker-1",
	}
	req = etextRequest(t, http.MethodPost, "/api/etext/documents/"+docResp.DocID+"/revisions", revReq)
	w = httptest.NewRecorder()
	h.HandleEtextRevisions(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

// ----- History -----

func TestEtextAPIGetHistory(t *testing.T) {
	h, _ := etextAPISetup(t)

	// Create a document.
	req := etextRequest(t, http.MethodPost, "/api/etext/documents",
		map[string]string{"title": "Test Doc"})
	w := httptest.NewRecorder()
	h.HandleEtextCreateDocument(w, req)
	var docResp etextCreateDocResponse
	_ = json.NewDecoder(w.Body).Decode(&docResp)

	// Create revisions.
	revReq := etextCreateRevisionRequest{
		Content:     "First draft",
		AuthorKind:  types.AuthorUser,
		AuthorLabel: "alice",
	}
	req = etextRequest(t, http.MethodPost, "/api/etext/documents/"+docResp.DocID+"/revisions", revReq)
	w = httptest.NewRecorder()
	h.HandleEtextRevisions(w, req)

	revReq = etextCreateRevisionRequest{
		Content:     "AI-improved",
		AuthorKind:  types.AuthorAppAgent,
		AuthorLabel: "appagent",
	}
	req = etextRequest(t, http.MethodPost, "/api/etext/documents/"+docResp.DocID+"/revisions", revReq)
	w = httptest.NewRecorder()
	h.HandleEtextRevisions(w, req)

	// Get history.
	req = etextRequest(t, http.MethodGet, "/api/etext/documents/"+docResp.DocID+"/history", nil)
	w = httptest.NewRecorder()
	h.HandleEtextHistory(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var resp etextHistoryResponse
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

func TestEtextAPIGetDiff(t *testing.T) {
	h, _ := etextAPISetup(t)

	// Create a document and revisions.
	req := etextRequest(t, http.MethodPost, "/api/etext/documents",
		map[string]string{"title": "Test Doc"})
	w := httptest.NewRecorder()
	h.HandleEtextCreateDocument(w, req)
	var docResp etextCreateDocResponse
	_ = json.NewDecoder(w.Body).Decode(&docResp)

	revReq := etextCreateRevisionRequest{
		Content:     "First draft",
		AuthorKind:  types.AuthorUser,
		AuthorLabel: "alice",
	}
	req = etextRequest(t, http.MethodPost, "/api/etext/documents/"+docResp.DocID+"/revisions", revReq)
	w = httptest.NewRecorder()
	h.HandleEtextRevisions(w, req)
	var rev1Resp etextRevisionResponse
	_ = json.NewDecoder(w.Body).Decode(&rev1Resp)

	revReq = etextCreateRevisionRequest{
		Content:     "AI-improved draft",
		AuthorKind:  types.AuthorAppAgent,
		AuthorLabel: "appagent",
	}
	req = etextRequest(t, http.MethodPost, "/api/etext/documents/"+docResp.DocID+"/revisions", revReq)
	w = httptest.NewRecorder()
	h.HandleEtextRevisions(w, req)
	var rev2Resp etextRevisionResponse
	_ = json.NewDecoder(w.Body).Decode(&rev2Resp)

	// Get diff.
	req = etextRequest(t, http.MethodGet,
		"/api/etext/diff?from="+rev1Resp.RevisionID+"&to="+rev2Resp.RevisionID, nil)
	w = httptest.NewRecorder()
	h.HandleEtextDiff(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var resp etextDiffResponse
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

func TestEtextAPIGetBlame(t *testing.T) {
	h, _ := etextAPISetup(t)

	// Create a document and revisions.
	req := etextRequest(t, http.MethodPost, "/api/etext/documents",
		map[string]string{"title": "Test Doc"})
	w := httptest.NewRecorder()
	h.HandleEtextCreateDocument(w, req)
	var docResp etextCreateDocResponse
	_ = json.NewDecoder(w.Body).Decode(&docResp)

	revReq := etextCreateRevisionRequest{
		Content:     "First draft",
		AuthorKind:  types.AuthorUser,
		AuthorLabel: "alice",
	}
	req = etextRequest(t, http.MethodPost, "/api/etext/documents/"+docResp.DocID+"/revisions", revReq)
	w = httptest.NewRecorder()
	h.HandleEtextRevisions(w, req)

	revReq = etextCreateRevisionRequest{
		Content:     "AI-improved draft",
		AuthorKind:  types.AuthorAppAgent,
		AuthorLabel: "appagent",
	}
	req = etextRequest(t, http.MethodPost, "/api/etext/documents/"+docResp.DocID+"/revisions", revReq)
	w = httptest.NewRecorder()
	h.HandleEtextRevisions(w, req)
	var rev2Resp etextRevisionResponse
	_ = json.NewDecoder(w.Body).Decode(&rev2Resp)

	// Get blame.
	req = etextRequest(t, http.MethodGet,
		"/api/etext/revisions/"+rev2Resp.RevisionID+"/blame", nil)
	w = httptest.NewRecorder()
	h.HandleEtextBlame(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var resp etextBlameResponse
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

func TestEtextAPISnapshotDoesNotMutateHead(t *testing.T) {
	h, s := etextAPISetup(t)

	// Create a document.
	req := etextRequest(t, http.MethodPost, "/api/etext/documents",
		map[string]string{"title": "Test Doc"})
	w := httptest.NewRecorder()
	h.HandleEtextCreateDocument(w, req)
	var docResp etextCreateDocResponse
	_ = json.NewDecoder(w.Body).Decode(&docResp)

	// Create two revisions.
	revReq := etextCreateRevisionRequest{
		Content:     "First draft",
		AuthorKind:  types.AuthorUser,
		AuthorLabel: "alice",
	}
	req = etextRequest(t, http.MethodPost, "/api/etext/documents/"+docResp.DocID+"/revisions", revReq)
	w = httptest.NewRecorder()
	h.HandleEtextRevisions(w, req)
	var rev1Resp etextRevisionResponse
	_ = json.NewDecoder(w.Body).Decode(&rev1Resp)

	revReq = etextCreateRevisionRequest{
		Content:     "Second draft",
		AuthorKind:  types.AuthorUser,
		AuthorLabel: "alice",
	}
	req = etextRequest(t, http.MethodPost, "/api/etext/documents/"+docResp.DocID+"/revisions", revReq)
	w = httptest.NewRecorder()
	h.HandleEtextRevisions(w, req)

	// View the first (historical) revision.
	req = etextRequest(t, http.MethodGet,
		"/api/etext/revisions/"+rev1Resp.RevisionID, nil)
	w = httptest.NewRecorder()
	h.HandleEtextRevision(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var snapshotResp etextRevisionResponse
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

// ----- Auth gating on e-text endpoints -----

func TestEtextAPIAuthGating(t *testing.T) {
	h, _ := etextAPISetup(t)

	endpoints := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/etext/documents"},
		{http.MethodPost, "/api/etext/documents"},
		{http.MethodGet, "/api/etext/diff"},
	}

	for _, ep := range endpoints {
		req := httptest.NewRequest(ep.method, ep.path, bytes.NewReader(nil))
		w := httptest.NewRecorder()

		switch {
		case strings.HasPrefix(ep.path, "/api/etext/documents"):
			h.HandleEtextDocumentsRoot(w, req)
		case strings.HasPrefix(ep.path, "/api/etext/diff"):
			h.HandleEtextDiff(w, req)
		}

		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: status = %d, want %d", ep.method, ep.path, w.Code, http.StatusUnauthorized)
		}
	}
}

// ----- Citations and metadata -----

func TestEtextAPICitationsMetadataRoundTrip(t *testing.T) {
	h, _ := etextAPISetup(t)

	// Create a document.
	req := etextRequest(t, http.MethodPost, "/api/etext/documents",
		map[string]string{"title": "Test Doc"})
	w := httptest.NewRecorder()
	h.HandleEtextCreateDocument(w, req)
	var docResp etextCreateDocResponse
	_ = json.NewDecoder(w.Body).Decode(&docResp)

	// Create a revision with citations and metadata.
	citations := []types.Citation{
		{ID: "c1", Type: "url", Value: "https://example.com", Label: "Example"},
	}
	citJSON, _ := json.Marshal(citations)
	metaJSON, _ := json.Marshal(map[string]any{"tags": []string{"draft"}})

	revReq := etextCreateRevisionRequest{
		Content:     "Document with citations",
		AuthorKind:  types.AuthorUser,
		AuthorLabel: "alice",
		Citations:   citJSON,
		Metadata:    metaJSON,
	}
	req = etextRequest(t, http.MethodPost, "/api/etext/documents/"+docResp.DocID+"/revisions", revReq)
	w = httptest.NewRecorder()
	h.HandleEtextRevisions(w, req)

	var revResp etextRevisionResponse
	if err := json.NewDecoder(w.Body).Decode(&revResp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	// Get the revision back and check citations/metadata.
	req = etextRequest(t, http.MethodGet,
		"/api/etext/revisions/"+revResp.RevisionID, nil)
	w = httptest.NewRecorder()
	h.HandleEtextRevision(w, req)

	var getResp etextRevisionResponse
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

// etextAPISetupWithRuntime creates a test setup with a started runtime
// so that tasks actually execute and complete.
func etextAPISetupWithRuntime(t *testing.T) (*APIHandler, *store.Store, *Runtime) {
	t.Helper()
	dir := filepath.Join(os.TempDir(), "go-choir-m3-etext-test")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	dbPath := filepath.Join(dir, t.Name()+".db")
	_ = os.Remove(dbPath)

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open etext api test store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	cfg := Config{
		SandboxID:           "sandbox-etext-test",
		StorePath:           dbPath,
		ProviderTimeout:     5 * time.Second,
		SupervisionInterval: 5 * time.Second,
	}

	bus := events.NewEventBus()
	// Use a short-delay stub provider so tasks complete quickly in tests.
	provider := NewStubProvider(50 * time.Millisecond)
	rt := New(cfg, s, bus, provider)

	// Start the runtime so tasks execute.
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
	req := etextRequest(t, http.MethodPost, "/api/etext/documents",
		map[string]string{"title": "Test Doc"})
	w := httptest.NewRecorder()
	h.HandleEtextCreateDocument(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create document: status = %d, body: %s", w.Code, w.Body.String())
	}
	var docResp etextCreateDocResponse
	_ = json.NewDecoder(w.Body).Decode(&docResp)

	// Create a user-authored revision.
	revReq := etextCreateRevisionRequest{
		Content:     "Hello, world!",
		AuthorKind:  types.AuthorUser,
		AuthorLabel: "alice",
	}
	req = etextRequest(t, http.MethodPost, "/api/etext/documents/"+docResp.DocID+"/revisions", revReq)
	w = httptest.NewRecorder()
	h.HandleEtextRevisions(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create user revision: status = %d, body: %s", w.Code, w.Body.String())
	}
	var revResp etextRevisionResponse
	_ = json.NewDecoder(w.Body).Decode(&revResp)

	return docResp.DocID, revResp.RevisionID
}

// waitForTaskCompletion polls the task status until it reaches a terminal
// state or the timeout expires.
func waitForTaskCompletion(t *testing.T, h *APIHandler, taskID string, timeout time.Duration) types.TaskState {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		req := etextRequest(t, http.MethodGet, "/api/agent/status?task_id="+taskID, nil)
		w := httptest.NewRecorder()
		h.HandleTaskStatus(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("get task status: status = %d", w.Code)
		}
		var resp taskStatusResponse
		_ = json.NewDecoder(w.Body).Decode(&resp)
		if resp.State.Terminal() {
			return resp.State
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("task %s did not complete within %v", taskID, timeout)
	return ""
}

// TestEtextAgentRevisionCreatesCanonicalRevision verifies that submitting
// an agent revision prompt creates a canonical appagent-authored revision
// (VAL-ETEXT-003).
func TestEtextAgentRevisionCreatesCanonicalRevision(t *testing.T) {
	h, s, _ := etextAPISetupWithRuntime(t)

	docID, _ := createDocWithUserRevision(t, h)

	// Submit an agent revision request.
	req := etextRequest(t, http.MethodPost, "/api/etext/documents/"+docID+"/agent-revision",
		map[string]string{"prompt": "Make it more formal"})
	w := httptest.NewRecorder()
	h.HandleEtextAgentRevision(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("agent revision: status = %d, want %d; body: %s", w.Code, http.StatusAccepted, w.Body.String())
	}

	var resp etextAgentRevisionResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.TaskID == "" {
		t.Error("TaskID is empty")
	}
	if resp.DocID != docID {
		t.Errorf("DocID = %q, want %q", resp.DocID, docID)
	}

	// Wait for the task to complete and the revision to be created.
	state := waitForTaskCompletion(t, h, resp.TaskID, 5*time.Second)
	if state != types.TaskCompleted {
		t.Fatalf("task state = %q, want %q", state, types.TaskCompleted)
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

// TestEtextAgentRevisionAuthRequired verifies that agent revision
// requires authentication (VAL-ETEXT-003: auth-gated).
func TestEtextAgentRevisionAuthRequired(t *testing.T) {
	h, _, _ := etextAPISetupWithRuntime(t)

	docID, _ := createDocWithUserRevision(t, h)

	// No auth header.
	req := httptest.NewRequest(http.MethodPost, "/api/etext/documents/"+docID+"/agent-revision",
		bytes.NewReader([]byte(`{"prompt":"test"}`)))
	w := httptest.NewRecorder()
	h.HandleEtextAgentRevision(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

// TestEtextAgentRevisionPreservesUserAndAppAgentAttribution verifies
// that an end-to-end flow preserves both user and appagent attribution
// in history (VAL-CROSS-119).
func TestEtextAgentRevisionPreservesUserAndAppAgentAttribution(t *testing.T) {
	h, s, _ := etextAPISetupWithRuntime(t)

	docID, _ := createDocWithUserRevision(t, h)

	// Submit an agent revision.
	req := etextRequest(t, http.MethodPost, "/api/etext/documents/"+docID+"/agent-revision",
		map[string]string{"prompt": "Improve the text"})
	w := httptest.NewRecorder()
	h.HandleEtextAgentRevision(w, req)
	var resp etextAgentRevisionResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)

	// Wait for completion.
	state := waitForTaskCompletion(t, h, resp.TaskID, 5*time.Second)
	if state != types.TaskCompleted {
		t.Fatalf("task state = %q, want %q", state, types.TaskCompleted)
	}

	// Make another user edit after the agent revision.
	revReq := etextCreateRevisionRequest{
		Content:     "User final edit",
		AuthorKind:  types.AuthorUser,
		AuthorLabel: "alice",
	}
	req = etextRequest(t, http.MethodPost, "/api/etext/documents/"+docID+"/revisions", revReq)
	w = httptest.NewRecorder()
	h.HandleEtextRevisions(w, req)
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

// TestEtextAgentRevisionNoWorkerAuthorship verifies that when subordinate
// workers might contribute to an appagent-driven change, the resulting
// canonical history attributes the change to the appagent, not to any
// worker identity (VAL-CROSS-120).
func TestEtextAgentRevisionNoWorkerAuthorship(t *testing.T) {
	h, s, _ := etextAPISetupWithRuntime(t)

	docID, _ := createDocWithUserRevision(t, h)

	// Submit an agent revision.
	req := etextRequest(t, http.MethodPost, "/api/etext/documents/"+docID+"/agent-revision",
		map[string]string{"prompt": "Make it better"})
	w := httptest.NewRecorder()
	h.HandleEtextAgentRevision(w, req)
	var resp etextAgentRevisionResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)

	// Wait for completion.
	state := waitForTaskCompletion(t, h, resp.TaskID, 5*time.Second)
	if state != types.TaskCompleted {
		t.Fatalf("task state = %q, want %q", state, types.TaskCompleted)
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

// TestEtextAgentRevisionNoDuplicateOnRenewalRetry verifies that renewal
// and retry does not duplicate a canonical document mutation (VAL-CROSS-122).
func TestEtextAgentRevisionNoDuplicateOnRenewalRetry(t *testing.T) {
	h, s, _ := etextAPISetupWithRuntime(t)

	docID, _ := createDocWithUserRevision(t, h)

	// Submit an agent revision.
	req := etextRequest(t, http.MethodPost, "/api/etext/documents/"+docID+"/agent-revision",
		map[string]string{"prompt": "Make it concise"})
	w := httptest.NewRecorder()
	h.HandleEtextAgentRevision(w, req)
	var resp1 etextAgentRevisionResponse
	_ = json.NewDecoder(w.Body).Decode(&resp1)

	// Simulate a renewal/retry by submitting the same request again
	// before the task completes. The idempotency check should return
	// the same task ID.
	req = etextRequest(t, http.MethodPost, "/api/etext/documents/"+docID+"/agent-revision",
		map[string]string{"prompt": "Make it concise"})
	w = httptest.NewRecorder()
	h.HandleEtextAgentRevision(w, req)

	var resp2 etextAgentRevisionResponse
	if err := json.NewDecoder(w.Body).Decode(&resp2); err != nil {
		t.Fatalf("decode retry response: %v", err)
	}

	// The retry should return the same task ID (idempotent).
	if resp2.TaskID != resp1.TaskID {
		t.Errorf("retry returned different task ID: %q vs %q — should be idempotent (VAL-CROSS-122)", resp2.TaskID, resp1.TaskID)
	}

	// Wait for the task to complete.
	state := waitForTaskCompletion(t, h, resp1.TaskID, 5*time.Second)
	if state != types.TaskCompleted {
		t.Fatalf("task state = %q, want %q", state, types.TaskCompleted)
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

// TestEtextAgentRevisionMutationCompletedOnlyOnce verifies that even if
// the task completion hook is called multiple times (e.g., after crash
// recovery), only one canonical revision is created (VAL-CROSS-122).
func TestEtextAgentRevisionMutationCompletedOnlyOnce(t *testing.T) {
	_, s, rt := etextAPISetupWithRuntime(t)

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
		TaskID:    "task-mutation-test",
		OwnerID:   "user-1",
		State:     "pending",
		CreatedAt: time.Now().UTC(),
	}
	if err := s.CreateAgentMutation(ctx, mutation); err != nil {
		t.Fatalf("create agent mutation: %v", err)
	}

	// Create a completed task record with etext agent revision metadata.
	taskRec := &types.TaskRecord{
		TaskID:    "task-mutation-test",
		OwnerID:   "user-1",
		SandboxID: "sandbox-etext-test",
		State:     types.TaskCompleted,
		Prompt:    "Revise the document",
		Result:    "Revised content",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
		Metadata: map[string]any{
			"type":              "etext_agent_revision",
			"doc_id":            "doc-mutation-test",
			"current_revision_id": "rev-user-1",
		},
	}

	// Call handleTaskCompletion twice to simulate duplicate processing.
	rt.handleTaskCompletion(ctx, taskRec)
	rt.handleTaskCompletion(ctx, taskRec)

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

// TestEtextAgentRevisionProgressEvents verifies that progress events
// are emitted during agent revision execution with the doc_id so
// the frontend can correlate to the open document (VAL-ETEXT-004).
func TestEtextAgentRevisionProgressEvents(t *testing.T) {
	h, s, _ := etextAPISetupWithRuntime(t)

	docID, _ := createDocWithUserRevision(t, h)

	// Subscribe to events before submitting the task.
	bus := s // We'll use the store to query events after completion.

	// Submit an agent revision.
	req := etextRequest(t, http.MethodPost, "/api/etext/documents/"+docID+"/agent-revision",
		map[string]string{"prompt": "Add more detail"})
	w := httptest.NewRecorder()
	h.HandleEtextAgentRevision(w, req)
	var resp etextAgentRevisionResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)

	// Wait for completion.
	state := waitForTaskCompletion(t, h, resp.TaskID, 5*time.Second)
	if state != types.TaskCompleted {
		t.Fatalf("task state = %q, want %q", state, types.TaskCompleted)
	}

	// Check that etext agent revision events were persisted.
	events, err := bus.ListEvents(context.Background(), resp.TaskID, 200)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}

	// We should find etext.agent_revision.started and
	// etext.agent_revision.completed events.
	var foundStarted, foundCompleted bool
	for _, ev := range events {
		switch ev.Kind {
		case types.EventEtextAgentRevisionStarted:
			foundStarted = true
			// Verify the payload contains doc_id.
			var payload map[string]string
			if err := json.Unmarshal(ev.Payload, &payload); err == nil {
				if payload["doc_id"] != docID {
					t.Errorf("started event doc_id = %q, want %q", payload["doc_id"], docID)
				}
			}
		case types.EventEtextAgentRevisionCompleted:
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
		t.Error("missing etext.agent_revision.started event (VAL-ETEXT-004)")
	}
	if !foundCompleted {
		t.Error("missing etext.agent_revision.completed event (VAL-ETEXT-004)")
	}
}

// TestEtextAgentRevisionEmptyPromptRejected verifies that an empty prompt
// is rejected with a bad request.
func TestEtextAgentRevisionEmptyPromptRejected(t *testing.T) {
	h, _, _ := etextAPISetupWithRuntime(t)

	docID, _ := createDocWithUserRevision(t, h)

	req := etextRequest(t, http.MethodPost, "/api/etext/documents/"+docID+"/agent-revision",
		map[string]string{"prompt": ""})
	w := httptest.NewRecorder()
	h.HandleEtextAgentRevision(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

// TestEtextAgentRevisionDocumentNotFound verifies that requesting an
// agent revision for a non-existent document returns 404.
func TestEtextAgentRevisionDocumentNotFound(t *testing.T) {
	h, _, _ := etextAPISetupWithRuntime(t)

	req := etextRequest(t, http.MethodPost, "/api/etext/documents/nonexistent/agent-revision",
		map[string]string{"prompt": "test"})
	w := httptest.NewRecorder()
	h.HandleEtextAgentRevision(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

// TestEtextAgentRevisionWrongOwner verifies that requesting an agent
// revision for a document owned by another user returns 404.
func TestEtextAgentRevisionWrongOwner(t *testing.T) {
	h, _, _ := etextAPISetupWithRuntime(t)

	docID, _ := createDocWithUserRevision(t, h)

	// Use a different user.
	req := httptest.NewRequest(http.MethodPost, "/api/etext/documents/"+docID+"/agent-revision",
		bytes.NewReader([]byte(`{"prompt":"test"}`)))
	req.Header.Set("X-Authenticated-User", "user-2")
	w := httptest.NewRecorder()
	h.HandleEtextAgentRevision(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d (wrong owner)", w.Code, http.StatusNotFound)
	}
}
