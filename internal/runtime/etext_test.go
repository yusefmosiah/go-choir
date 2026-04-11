package runtime

import (
	"bytes"
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
