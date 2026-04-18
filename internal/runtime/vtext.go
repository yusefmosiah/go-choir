// Package runtime provides vtext document API handlers for the go-choir
// sandbox runtime. These handlers expose the document CRUD, revision,
// history, snapshot, diff, blame, and agent revision APIs through the
// authenticated same-origin proxy path.
//
// API endpoints:
//
//	POST   /api/vtext/documents          — create a new document
//	GET    /api/vtext/documents          — list documents for the authenticated user
//	GET    /api/vtext/documents/{id}     — get a document by ID
//	PUT    /api/vtext/documents/{id}     — update a document (e.g., title)
//	DELETE /api/vtext/documents/{id}     — delete a document and its revisions
//	POST   /api/vtext/documents/{id}/revisions — create a new revision (user edit or appagent edit)
//	GET    /api/vtext/documents/{id}/revisions — list revisions for a document
//	GET    /api/vtext/revisions/{id}    — get a specific revision (snapshot)
//	GET    /api/vtext/documents/{id}/history — get revision history with attribution
//	GET    /api/vtext/diff?from={id}&to={id} — diff two revisions
//	GET    /api/vtext/revisions/{id}/blame — blame a revision
package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/yusefmosiah/go-choir/internal/events"
	"github.com/yusefmosiah/go-choir/internal/store"
	"github.com/yusefmosiah/go-choir/internal/types"
)

// ----- Request/Response types -----

// vtextCreateDocRequest is the JSON payload for POST /api/vtext/documents.
type vtextCreateDocRequest struct {
	Title string `json:"title"`
}

// vtextCreateDocResponse is the JSON response for POST /api/vtext/documents.
type vtextCreateDocResponse struct {
	DocID     string `json:"doc_id"`
	OwnerID   string `json:"owner_id"`
	Title     string `json:"title"`
	CreatedAt string `json:"created_at"`
}

// vtextDocumentResponse is the JSON response for GET /api/vtext/documents/{id}.
type vtextDocumentResponse struct {
	DocID             string `json:"doc_id"`
	OwnerID           string `json:"owner_id"`
	Title             string `json:"title"`
	CurrentRevisionID string `json:"current_revision_id,omitempty"`
	CreatedAt         string `json:"created_at"`
	UpdatedAt         string `json:"updated_at"`
}

// vtextUpdateDocRequest is the JSON payload for PUT /api/vtext/documents/{id}.
type vtextUpdateDocRequest struct {
	Title string `json:"title"`
}

// vtextListDocsResponse is the JSON response for GET /api/vtext/documents.
type vtextListDocsResponse struct {
	Documents []vtextDocumentResponse `json:"documents"`
}

// vtextCreateRevisionRequest is the JSON payload for
// POST /api/vtext/documents/{id}/revisions.
type vtextCreateRevisionRequest struct {
	Content          string           `json:"content"`
	AuthorKind       types.AuthorKind `json:"author_kind"`
	AuthorLabel      string           `json:"author_label"`
	Citations        json.RawMessage  `json:"citations,omitempty"`
	Metadata         json.RawMessage  `json:"metadata,omitempty"`
	ParentRevisionID string           `json:"parent_revision_id,omitempty"`
}

// vtextRevisionResponse is the JSON response for revision-related endpoints.
type vtextRevisionResponse struct {
	RevisionID       string           `json:"revision_id"`
	DocID            string           `json:"doc_id"`
	OwnerID          string           `json:"owner_id"`
	AuthorKind       types.AuthorKind `json:"author_kind"`
	AuthorLabel      string           `json:"author_label"`
	Content          string           `json:"content"`
	Citations        json.RawMessage  `json:"citations,omitempty"`
	Metadata         json.RawMessage  `json:"metadata,omitempty"`
	ParentRevisionID string           `json:"parent_revision_id,omitempty"`
	CreatedAt        string           `json:"created_at"`
}

// vtextListRevisionsResponse is the JSON response for
// GET /api/vtext/documents/{id}/revisions.
type vtextListRevisionsResponse struct {
	Revisions []vtextRevisionResponse `json:"revisions"`
}

// vtextHistoryResponse is the JSON response for
// GET /api/vtext/documents/{id}/history.
type vtextHistoryResponse struct {
	DocID   string               `json:"doc_id"`
	Entries []types.HistoryEntry `json:"entries"`
}

// vtextDiffResponse is the JSON response for GET /api/vtext/diff.
type vtextDiffResponse struct {
	types.DiffResult
}

// vtextBlameResponse is the JSON response for
// GET /api/vtext/revisions/{id}/blame.
type vtextBlameResponse struct {
	types.BlameResult
}

// ----- Helper functions -----

// extractDocID extracts the document ID from the URL path.
// Expected pattern: /api/vtext/documents/{docID}/...
func extractDocID(path string) string {
	const prefix = "/api/vtext/documents/"
	if !strings.HasPrefix(path, prefix) {
		return ""
	}
	rest := strings.TrimPrefix(path, prefix)
	// The docID is the first path segment.
	parts := strings.SplitN(rest, "/", 2)
	return parts[0]
}

// extractRevisionID extracts the revision ID from the URL path.
// Expected pattern: /api/vtext/revisions/{revisionID}/...
func extractRevisionID(path string) string {
	const prefix = "/api/vtext/revisions/"
	if !strings.HasPrefix(path, prefix) {
		return ""
	}
	rest := strings.TrimPrefix(path, prefix)
	parts := strings.SplitN(rest, "/", 2)
	return parts[0]
}

// ----- Handler methods -----

// HandleVTextCreateDocument handles POST /api/vtext/documents.
// It creates a new document with a durable document identity (VAL-ETEXT-001).
func (h *APIHandler) HandleVTextCreateDocument(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIJSON(w, http.StatusMethodNotAllowed, apiError{Error: "method not allowed"})
		return
	}

	ownerID, err := authenticateUser(r)
	if err != nil {
		writeAPIJSON(w, http.StatusUnauthorized, apiError{Error: "authentication required"})
		return
	}

	var req vtextCreateDocRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIJSON(w, http.StatusBadRequest, apiError{Error: "invalid request body"})
		return
	}

	if strings.TrimSpace(req.Title) == "" {
		writeAPIJSON(w, http.StatusBadRequest, apiError{Error: "title is required"})
		return
	}

	now := time.Now().UTC()
	doc := types.Document{
		DocID:     uuid.New().String(),
		OwnerID:   ownerID,
		Title:     req.Title,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := h.rt.Store().CreateDocument(r.Context(), doc); err != nil {
		log.Printf("vtext api: create document: %v", err)
		writeAPIJSON(w, http.StatusInternalServerError, apiError{Error: "failed to create document"})
		return
	}

	writeAPIJSON(w, http.StatusCreated, vtextCreateDocResponse{
		DocID:     doc.DocID,
		OwnerID:   doc.OwnerID,
		Title:     doc.Title,
		CreatedAt: doc.CreatedAt.Format("2006-01-02T15:04:05.000Z"),
	})
}

// HandleVTextListDocuments handles GET /api/vtext/documents.
// It returns documents owned by the authenticated user.
func (h *APIHandler) HandleVTextListDocuments(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIJSON(w, http.StatusMethodNotAllowed, apiError{Error: "method not allowed"})
		return
	}

	ownerID, err := authenticateUser(r)
	if err != nil {
		writeAPIJSON(w, http.StatusUnauthorized, apiError{Error: "authentication required"})
		return
	}

	docs, err := h.rt.Store().ListDocumentsByOwner(r.Context(), ownerID, 50)
	if err != nil {
		log.Printf("vtext api: list documents: %v", err)
		writeAPIJSON(w, http.StatusInternalServerError, apiError{Error: "failed to list documents"})
		return
	}

	resp := vtextListDocsResponse{Documents: make([]vtextDocumentResponse, 0, len(docs))}
	for _, doc := range docs {
		resp.Documents = append(resp.Documents, vtextDocumentResponse{
			DocID:             doc.DocID,
			OwnerID:           doc.OwnerID,
			Title:             doc.Title,
			CurrentRevisionID: doc.CurrentRevisionID,
			CreatedAt:         doc.CreatedAt.Format("2006-01-02T15:04:05.000Z"),
			UpdatedAt:         doc.UpdatedAt.Format("2006-01-02T15:04:05.000Z"),
		})
	}

	writeAPIJSON(w, http.StatusOK, resp)
}

// HandleVTextDocument handles GET/PUT/DELETE /api/vtext/documents/{id}.
func (h *APIHandler) HandleVTextDocument(w http.ResponseWriter, r *http.Request) {
	docID := extractDocID(r.URL.Path)
	if docID == "" {
		writeAPIJSON(w, http.StatusBadRequest, apiError{Error: "document ID is required"})
		return
	}

	switch r.Method {
	case http.MethodGet:
		h.handleVTextGetDocument(w, r, docID)
	case http.MethodPut:
		h.handleVTextUpdateDocument(w, r, docID)
	case http.MethodDelete:
		h.handleVTextDeleteDocument(w, r, docID)
	default:
		writeAPIJSON(w, http.StatusMethodNotAllowed, apiError{Error: "method not allowed"})
	}
}

func (h *APIHandler) handleVTextGetDocument(w http.ResponseWriter, r *http.Request, docID string) {
	ownerID, err := authenticateUser(r)
	if err != nil {
		writeAPIJSON(w, http.StatusUnauthorized, apiError{Error: "authentication required"})
		return
	}

	doc, err := h.rt.Store().GetDocument(r.Context(), docID, ownerID)
	if err != nil {
		writeAPIJSON(w, http.StatusNotFound, apiError{Error: "document not found"})
		return
	}

	writeAPIJSON(w, http.StatusOK, vtextDocumentResponse{
		DocID:             doc.DocID,
		OwnerID:           doc.OwnerID,
		Title:             doc.Title,
		CurrentRevisionID: doc.CurrentRevisionID,
		CreatedAt:         doc.CreatedAt.Format("2006-01-02T15:04:05.000Z"),
		UpdatedAt:         doc.UpdatedAt.Format("2006-01-02T15:04:05.000Z"),
	})
}

func (h *APIHandler) handleVTextUpdateDocument(w http.ResponseWriter, r *http.Request, docID string) {
	ownerID, err := authenticateUser(r)
	if err != nil {
		writeAPIJSON(w, http.StatusUnauthorized, apiError{Error: "authentication required"})
		return
	}

	var req vtextUpdateDocRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIJSON(w, http.StatusBadRequest, apiError{Error: "invalid request body"})
		return
	}

	doc, err := h.rt.Store().GetDocument(r.Context(), docID, ownerID)
	if err != nil {
		writeAPIJSON(w, http.StatusNotFound, apiError{Error: "document not found"})
		return
	}

	doc.Title = req.Title
	doc.UpdatedAt = time.Now().UTC()

	if err := h.rt.Store().UpdateDocument(r.Context(), doc); err != nil {
		log.Printf("vtext api: update document: %v", err)
		writeAPIJSON(w, http.StatusInternalServerError, apiError{Error: "failed to update document"})
		return
	}

	writeAPIJSON(w, http.StatusOK, vtextDocumentResponse{
		DocID:             doc.DocID,
		OwnerID:           doc.OwnerID,
		Title:             doc.Title,
		CurrentRevisionID: doc.CurrentRevisionID,
		CreatedAt:         doc.CreatedAt.Format("2006-01-02T15:04:05.000Z"),
		UpdatedAt:         doc.UpdatedAt.Format("2006-01-02T15:04:05.000Z"),
	})
}

func (h *APIHandler) handleVTextDeleteDocument(w http.ResponseWriter, r *http.Request, docID string) {
	ownerID, err := authenticateUser(r)
	if err != nil {
		writeAPIJSON(w, http.StatusUnauthorized, apiError{Error: "authentication required"})
		return
	}

	if err := h.rt.Store().DeleteDocument(r.Context(), docID, ownerID); err != nil {
		writeAPIJSON(w, http.StatusNotFound, apiError{Error: "document not found"})
		return
	}

	writeAPIJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// HandleVTextRevisions handles POST and GET
// /api/vtext/documents/{id}/revisions.
func (h *APIHandler) HandleVTextRevisions(w http.ResponseWriter, r *http.Request) {
	docID := extractDocID(r.URL.Path)
	if docID == "" {
		writeAPIJSON(w, http.StatusBadRequest, apiError{Error: "document ID is required"})
		return
	}

	switch r.Method {
	case http.MethodPost:
		h.handleVTextCreateRevision(w, r, docID)
	case http.MethodGet:
		h.handleVTextListRevisions(w, r, docID)
	default:
		writeAPIJSON(w, http.StatusMethodNotAllowed, apiError{Error: "method not allowed"})
	}
}

func (h *APIHandler) handleVTextCreateRevision(w http.ResponseWriter, r *http.Request, docID string) {
	ownerID, err := authenticateUser(r)
	if err != nil {
		writeAPIJSON(w, http.StatusUnauthorized, apiError{Error: "authentication required"})
		return
	}

	var req vtextCreateRevisionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIJSON(w, http.StatusBadRequest, apiError{Error: "invalid request body"})
		return
	}

	// Validate author kind — only user and appagent are canonical editors.
	if !req.AuthorKind.Valid() {
		writeAPIJSON(w, http.StatusBadRequest, apiError{Error: "invalid author_kind; must be 'user' or 'appagent'"})
		return
	}

	// Verify the document exists and belongs to this owner.
	doc, err := h.rt.Store().GetDocument(r.Context(), docID, ownerID)
	if err != nil {
		writeAPIJSON(w, http.StatusNotFound, apiError{Error: "document not found"})
		return
	}

	now := time.Now().UTC()

	// If parent_revision_id is not specified, use the document's current head.
	parentID := req.ParentRevisionID
	if parentID == "" {
		parentID = doc.CurrentRevisionID
	}

	citations := req.Citations
	if citations == nil {
		citations = json.RawMessage("[]")
	}
	metadata := req.Metadata
	if metadata == nil {
		metadata = json.RawMessage("{}")
	}

	rev := types.Revision{
		RevisionID:       uuid.New().String(),
		DocID:            docID,
		OwnerID:          ownerID,
		AuthorKind:       req.AuthorKind,
		AuthorLabel:      req.AuthorLabel,
		Content:          req.Content,
		Citations:        citations,
		Metadata:         metadata,
		ParentRevisionID: parentID,
		CreatedAt:        now,
	}

	if err := h.rt.Store().CreateRevision(r.Context(), rev); err != nil {
		log.Printf("vtext api: create revision: %v", err)
		writeAPIJSON(w, http.StatusInternalServerError, apiError{Error: "failed to create revision"})
		return
	}

	writeAPIJSON(w, http.StatusCreated, vtextRevisionResponse{
		RevisionID:       rev.RevisionID,
		DocID:            rev.DocID,
		OwnerID:          rev.OwnerID,
		AuthorKind:       rev.AuthorKind,
		AuthorLabel:      rev.AuthorLabel,
		Content:          rev.Content,
		Citations:        rev.Citations,
		Metadata:         rev.Metadata,
		ParentRevisionID: rev.ParentRevisionID,
		CreatedAt:        rev.CreatedAt.Format("2006-01-02T15:04:05.000Z"),
	})
}

func (h *APIHandler) handleVTextListRevisions(w http.ResponseWriter, r *http.Request, docID string) {
	ownerID, err := authenticateUser(r)
	if err != nil {
		writeAPIJSON(w, http.StatusUnauthorized, apiError{Error: "authentication required"})
		return
	}

	revs, err := h.rt.Store().ListRevisionsByDoc(r.Context(), docID, ownerID, 50)
	if err != nil {
		log.Printf("vtext api: list revisions: %v", err)
		writeAPIJSON(w, http.StatusInternalServerError, apiError{Error: "failed to list revisions"})
		return
	}

	resp := vtextListRevisionsResponse{Revisions: make([]vtextRevisionResponse, 0, len(revs))}
	for _, rev := range revs {
		resp.Revisions = append(resp.Revisions, vtextRevisionResponse{
			RevisionID:       rev.RevisionID,
			DocID:            rev.DocID,
			OwnerID:          rev.OwnerID,
			AuthorKind:       rev.AuthorKind,
			AuthorLabel:      rev.AuthorLabel,
			Content:          rev.Content,
			Citations:        rev.Citations,
			Metadata:         rev.Metadata,
			ParentRevisionID: rev.ParentRevisionID,
			CreatedAt:        rev.CreatedAt.Format("2006-01-02T15:04:05.000Z"),
		})
	}

	writeAPIJSON(w, http.StatusOK, resp)
}

// HandleVTextRevision handles GET /api/vtext/revisions/{id}.
// Opening a historical revision does not mutate the document head
// (VAL-ETEXT-007: historical snapshots can be opened without mutating head).
func (h *APIHandler) HandleVTextRevision(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIJSON(w, http.StatusMethodNotAllowed, apiError{Error: "method not allowed"})
		return
	}

	revisionID := extractRevisionID(r.URL.Path)
	if revisionID == "" {
		writeAPIJSON(w, http.StatusBadRequest, apiError{Error: "revision ID is required"})
		return
	}

	ownerID, err := authenticateUser(r)
	if err != nil {
		writeAPIJSON(w, http.StatusUnauthorized, apiError{Error: "authentication required"})
		return
	}

	rev, err := h.rt.Store().GetRevision(r.Context(), revisionID, ownerID)
	if err != nil {
		writeAPIJSON(w, http.StatusNotFound, apiError{Error: "revision not found"})
		return
	}

	writeAPIJSON(w, http.StatusOK, vtextRevisionResponse{
		RevisionID:       rev.RevisionID,
		DocID:            rev.DocID,
		OwnerID:          rev.OwnerID,
		AuthorKind:       rev.AuthorKind,
		AuthorLabel:      rev.AuthorLabel,
		Content:          rev.Content,
		Citations:        rev.Citations,
		Metadata:         rev.Metadata,
		ParentRevisionID: rev.ParentRevisionID,
		CreatedAt:        rev.CreatedAt.Format("2006-01-02T15:04:05.000Z"),
	})
}

// HandleVTextHistory handles GET /api/vtext/documents/{id}/history.
// It returns the revision history with explicit attribution metadata
// (VAL-ETEXT-006: version history lists revisions with explicit
// attribution metadata).
func (h *APIHandler) HandleVTextHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIJSON(w, http.StatusMethodNotAllowed, apiError{Error: "method not allowed"})
		return
	}

	docID := extractDocID(r.URL.Path)
	if docID == "" {
		writeAPIJSON(w, http.StatusBadRequest, apiError{Error: "document ID is required"})
		return
	}

	ownerID, err := authenticateUser(r)
	if err != nil {
		writeAPIJSON(w, http.StatusUnauthorized, apiError{Error: "authentication required"})
		return
	}

	entries, err := h.rt.Store().GetHistory(r.Context(), docID, ownerID, 50)
	if err != nil {
		log.Printf("vtext api: get history: %v", err)
		writeAPIJSON(w, http.StatusInternalServerError, apiError{Error: "failed to get history"})
		return
	}

	writeAPIJSON(w, http.StatusOK, vtextHistoryResponse{
		DocID:   docID,
		Entries: entries,
	})
}

// HandleVTextDiff handles GET /api/vtext/diff?from={id}&to={id}.
// It compares selected from and to revisions and shows the changed
// sections (VAL-ETEXT-008: diff view compares selected revisions and
// changed sections).
func (h *APIHandler) HandleVTextDiff(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIJSON(w, http.StatusMethodNotAllowed, apiError{Error: "method not allowed"})
		return
	}

	ownerID, err := authenticateUser(r)
	if err != nil {
		writeAPIJSON(w, http.StatusUnauthorized, apiError{Error: "authentication required"})
		return
	}

	fromRevID := r.URL.Query().Get("from")
	toRevID := r.URL.Query().Get("to")
	if fromRevID == "" || toRevID == "" {
		writeAPIJSON(w, http.StatusBadRequest, apiError{Error: "from and to revision IDs are required"})
		return
	}

	diff, err := h.rt.Store().GetDiff(r.Context(), fromRevID, toRevID, ownerID)
	if err != nil {
		writeAPIJSON(w, http.StatusNotFound, apiError{Error: fmt.Sprintf("failed to compute diff: %v", err)})
		return
	}

	writeAPIJSON(w, http.StatusOK, vtextDiffResponse{DiffResult: diff})
}

// HandleVTextBlame handles GET /api/vtext/revisions/{id}/blame.
// It provides section-level attribution that distinguishes whether the
// last editor was the user or the agent (VAL-ETEXT-009: blame identifies
// the last editor per section).
func (h *APIHandler) HandleVTextBlame(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIJSON(w, http.StatusMethodNotAllowed, apiError{Error: "method not allowed"})
		return
	}

	revisionID := extractRevisionID(r.URL.Path)
	if revisionID == "" {
		writeAPIJSON(w, http.StatusBadRequest, apiError{Error: "revision ID is required"})
		return
	}

	ownerID, err := authenticateUser(r)
	if err != nil {
		writeAPIJSON(w, http.StatusUnauthorized, apiError{Error: "authentication required"})
		return
	}

	blame, err := h.rt.Store().GetBlame(r.Context(), revisionID, ownerID)
	if err != nil {
		writeAPIJSON(w, http.StatusNotFound, apiError{Error: "revision not found"})
		return
	}

	writeAPIJSON(w, http.StatusOK, vtextBlameResponse{BlameResult: blame})
}

// ----- Agent revision -----

// vtextAgentRevisionRequest is the JSON payload for
// POST /api/vtext/documents/{id}/agent-revision.
// Submitting a natural-language revision request from within an open document
// creates a new canonical revision attributable to the appagent
// (VAL-ETEXT-003).
type vtextAgentRevisionRequest struct {
	Intent string `json:"intent,omitempty"`
	Prompt string `json:"prompt,omitempty"`
}

// vtextAgentRevisionResponse is the JSON response for agent revision
// submission. It returns the stable task handle so the client can track
// progress through the event stream (VAL-ETEXT-004).
type vtextAgentRevisionResponse struct {
	RunID     string         `json:"loop_id"`
	DocID     string         `json:"doc_id"`
	State     types.RunState `json:"state"`
	CreatedAt string         `json:"created_at"`
}

// HandleVTextAgentRevision handles POST
// /api/vtext/documents/{id}/agent-revision.
//
// It creates a runtime task that, when completed, will create a canonical
// appagent-authored revision. The task ID is returned so the client can
// track progress and completion through the existing event stream
// (VAL-ETEXT-003, VAL-ETEXT-004).
//
// If a pending agent mutation already exists for this document (e.g., from
// a previous request that is still in-flight), the existing task ID is
// returned instead of creating a new mutation, preventing duplicate
// canonical revisions when renewal/retry occurs mid-mutation
// (VAL-CROSS-122).
func (h *APIHandler) HandleVTextAgentRevision(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIJSON(w, http.StatusMethodNotAllowed, apiError{Error: "method not allowed"})
		return
	}

	docID := extractDocID(r.URL.Path)
	if docID == "" {
		writeAPIJSON(w, http.StatusBadRequest, apiError{Error: "document ID is required"})
		return
	}

	ownerID, err := authenticateUser(r)
	if err != nil {
		writeAPIJSON(w, http.StatusUnauthorized, apiError{Error: "authentication required"})
		return
	}

	var req vtextAgentRevisionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIJSON(w, http.StatusBadRequest, apiError{Error: "invalid request body"})
		return
	}

	// Verify the document exists and belongs to this owner.
	doc, err := h.rt.Store().GetDocument(r.Context(), docID, ownerID)
	if err != nil {
		writeAPIJSON(w, http.StatusNotFound, apiError{Error: "document not found"})
		return
	}

	// Check for an existing pending agent mutation on this document.
	// If one exists, return the existing run ID instead of creating a new
	// mutation. This prevents duplicate canonical revisions when
	// renewal/retry occurs mid-mutation (VAL-CROSS-122).
	existing, err := h.rt.Store().GetPendingAgentMutationByDoc(r.Context(), docID, ownerID)
	if err != nil {
		log.Printf("vtext api: check pending mutation: %v", err)
	} else if existing != nil {
		// Return the existing run — idempotent response.
		writeAPIJSON(w, http.StatusAccepted, vtextAgentRevisionResponse{
			RunID:     existing.RunID,
			DocID:     docID,
			State:     types.RunPending,
			CreatedAt: existing.CreatedAt.Format("2006-01-02T15:04:05.000Z"),
		})
		return
	}

	rec, err := h.rt.submitVTextAgentRevisionRun(r.Context(), doc, ownerID, req, "")
	if err != nil {
		log.Printf("vtext api: submit agent revision run: %v", err)
		writeAPIJSON(w, http.StatusInternalServerError, apiError{Error: "failed to submit agent revision"})
		return
	}

	writeAPIJSON(w, http.StatusAccepted, vtextAgentRevisionResponse{
		RunID:     rec.RunID,
		DocID:     docID,
		State:     rec.State,
		CreatedAt: rec.CreatedAt.Format("2006-01-02T15:04:05.000Z"),
	})
}

func (rt *Runtime) submitVTextAgentRevisionRun(ctx context.Context, doc types.Document, ownerID string, req vtextAgentRevisionRequest, parentRunID string) (*types.RunRecord, error) {
	// Build the backend-owned vtext revision request from current document state.
	var currentRevision types.Revision
	var currentRevisionLoaded bool
	if doc.CurrentRevisionID != "" {
		rev, err := rt.Store().GetRevision(ctx, doc.CurrentRevisionID, ownerID)
		if err == nil {
			currentRevision = rev
			currentRevisionLoaded = true
		}
	}
	metadata := decodeRevisionMetadata(currentRevision.Metadata)
	var previousRevision *types.Revision
	if currentRevisionLoaded && currentRevision.ParentRevisionID != "" {
		prev, err := rt.Store().GetRevision(ctx, currentRevision.ParentRevisionID, ownerID)
		if err == nil {
			previousRevision = &prev
		}
	}

	diffSummary := ""
	if currentRevisionLoaded && previousRevision != nil {
		if diff, err := rt.Store().GetDiff(ctx, previousRevision.RevisionID, currentRevision.RevisionID, ownerID); err == nil {
			diffSummary = summarizeDiffResult(diff)
		}
	}

	hasGroundedHistory, historyErr := rt.channelHasGroundedHistory(ctx, ownerID, doc.DocID, time.Time{})
	if historyErr != nil {
		log.Printf("vtext api: check grounded history: %v", historyErr)
		hasGroundedHistory = false
	}

	recentWorkerMessages, workerErr := rt.recentWorkerMessages(ctx, ownerID, doc.DocID, 12)
	if workerErr != nil {
		log.Printf("vtext api: recent worker messages: %v", workerErr)
	}
	userRevisionDiffs, userDiffErr := rt.userRevisionDiffSummaries(ctx, ownerID, doc.DocID, 200)
	if userDiffErr != nil {
		log.Printf("vtext api: user revision diffs: %v", userDiffErr)
	}

	agentPrompt := buildAgentRevisionRequest(currentRevision, previousRevision, metadata, req, diffSummary, hasGroundedHistory, recentWorkerMessages, userRevisionDiffs)

	// Create the runtime run with vtext agent revision metadata.
	// Carry forward durable context keys from the current head revision
	// so they survive into appagent revision metadata.
	runMetadata := map[string]any{
		"type":                "vtext_agent_revision",
		"agent_profile":       AgentProfileVText,
		"agent_role":          AgentProfileVText,
		"agent_id":            "vtext:" + doc.DocID,
		"channel_id":          doc.DocID,
		"doc_id":              doc.DocID,
		"current_revision_id": doc.CurrentRevisionID,
		"request_intent":      strings.TrimSpace(req.Intent),
		"original_prompt":     strings.TrimSpace(req.Prompt),
	}
	for _, key := range durableMetadataKeys {
		if val := metadataString(metadata, key); val != "" {
			runMetadata[key] = val
		}
	}

	var (
		rec *types.RunRecord
		err error
	)
	if strings.TrimSpace(parentRunID) != "" {
		rec, err = rt.StartChildRun(ctx, parentRunID, agentPrompt, ownerID, runMetadata)
	} else {
		rec, err = rt.StartRunWithMetadata(ctx, agentPrompt, ownerID, runMetadata)
	}
	if err != nil {
		return nil, err
	}

	// Record the agent mutation for idempotency tracking (VAL-CROSS-122).
	if err := rt.Store().CreateAgentMutation(ctx, store.AgentMutation{
		DocID:     doc.DocID,
		RunID:     rec.RunID,
		OwnerID:   ownerID,
		State:     "pending",
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		log.Printf("vtext api: create agent mutation: %v", err)
	}

	// Emit the vtext-specific agent revision started event.
	startedPayload, _ := json.Marshal(map[string]string{
		"doc_id": doc.DocID,
		"loop_id": rec.RunID,
	})
	rt.emitVTextAgentEvent(ctx, rec, types.EventVTextAgentRevisionStarted,
		events.CauseTaskLifecycle, startedPayload)

	return rec, nil
}

// buildAgentRevisionRequest constructs the backend-owned vtext revision
// request sent as the user turn for the vtext appagent.
func buildAgentRevisionRequest(current types.Revision, previous *types.Revision, metadata map[string]any, req vtextAgentRevisionRequest, diffSummary string, hasGroundedHistory bool, recentWorkerMessages []ChannelMessage, userRevisionDiffs []string) string {
	var b strings.Builder
	b.WriteString("A revise event was triggered for the current vtext document.")

	intent := strings.TrimSpace(req.Intent)
	if intent == "" {
		intent = "revise"
	}
	b.WriteString("\nIntent: ")
	b.WriteString(intent)
	b.WriteString(".")

	if seedPrompt := metadataString(metadata, "seed_prompt"); seedPrompt != "" {
		b.WriteString("\n\nOriginal user request:\n")
		b.WriteString(seedPrompt)
	}
	if legacyPrompt := strings.TrimSpace(req.Prompt); legacyPrompt != "" {
		b.WriteString("\n\nAdditional user instruction:\n")
		b.WriteString(legacyPrompt)
	}
	if sourcePath := metadataString(metadata, "source_path"); sourcePath != "" {
		b.WriteString("\n\nSource path: ")
		b.WriteString(sourcePath)
		b.WriteString(". Preserve the file-backed structure while producing the next version.")
	}
	if conductorLoopID := metadataString(metadata, "conductor_loop_id"); conductorLoopID != "" {
		b.WriteString("\nConductor loop: ")
		b.WriteString(conductorLoopID)
		b.WriteString(".")
	}
	if current.RevisionID != "" {
		b.WriteString("\n\nCurrent head revision: ")
		b.WriteString(current.RevisionID)
		b.WriteString(" (")
		b.WriteString(string(current.AuthorKind))
		b.WriteString(" by ")
		b.WriteString(current.AuthorLabel)
		b.WriteString(").")
	}
	if previous != nil {
		b.WriteString("\nPrevious revision: ")
		b.WriteString(previous.RevisionID)
		b.WriteString(".")
	}
	if diffSummary != "" {
		b.WriteString("\n\nLatest revision diff/context:\n")
		b.WriteString(diffSummary)
	}
	if len(recentWorkerMessages) > 0 {
		b.WriteString("\n\nRecent worker messages on the shared channel:\n")
		for _, message := range recentWorkerMessages {
			b.WriteString("- [")
			if !message.Timestamp.IsZero() {
				b.WriteString(message.Timestamp.UTC().Format(time.RFC3339))
			} else {
				b.WriteString("unknown-time")
			}
			b.WriteString("] ")
			if role := strings.TrimSpace(message.Role); role != "" {
				b.WriteString(role)
			} else {
				b.WriteString("worker")
			}
			if from := strings.TrimSpace(message.From); from != "" {
				b.WriteString(" ")
				b.WriteString(from)
			}
			b.WriteString(": ")
			b.WriteString(truncatePromptSnippet(message.Content, 800))
			b.WriteString("\n")
		}
	}
	if len(userRevisionDiffs) > 0 {
		b.WriteString("\nUser-authored revision diffs (oldest to newest):\n")
		for _, summary := range userRevisionDiffs {
			b.WriteString("- ")
			b.WriteString(summary)
			b.WriteString("\n")
		}
	}

	b.WriteString("\n\nCurrent canonical document content:\n---\n")
	if current.Content != "" {
		b.WriteString(current.Content)
	} else {
		b.WriteString("(empty document)")
	}
	b.WriteString("\n---\n")
	if current.AuthorKind == types.AuthorUser {
		b.WriteString("\nTreat this latest user-authored revision as the canonical input for the next version.")
	}
	if hasGroundedHistory {
		b.WriteString("\nThis document already has grounded workflow history on the shared channel.")
		b.WriteString("\nReuse the informed context already present in the current document and prior worker messages.")
		b.WriteString("\nOpen new researcher or super work when this follow-up needs facts, evidence, or validation beyond what the workflow has already grounded.")
	} else {
		b.WriteString("\nThis document does not yet have grounded workflow history.")
		b.WriteString("\nIf you can already produce a useful next version from the current material and your priors, do it promptly.")
		b.WriteString("\nStart researcher or super work early when the request needs outside facts, validation, or deeper investigation so later versions can integrate grounded findings.")
	}
	b.WriteString("\nTreat this run as one step in an ongoing document loop.")
	b.WriteString("\nWorker messages can wake later vtext runs and trigger the next revision.")
	b.WriteString("\nBuild from the current canonical document, recent worker messages, recent change context, and user-authored diffs.")
	b.WriteString("\nIntermediate appagent revisions are compactable context, not the source of truth.")
	b.WriteString("\nDo not claim to be researching unless you actually open worker runs and incorporate their messages.")
	b.WriteString("\nProduce the next canonical document version.")
	return b.String()
}

func (rt *Runtime) recentWorkerMessages(ctx context.Context, ownerID, channelID string, limit int) ([]ChannelMessage, error) {
	if limit <= 0 {
		limit = 12
	}
	messages, err := rt.Store().ListChannelMessages(ctx, ownerID, channelID, 0, 200)
	if err != nil {
		return nil, err
	}
	runs, err := rt.Store().ListRunsByChannel(ctx, ownerID, channelID, 200)
	if err != nil {
		return nil, err
	}
	runProfiles := make(map[string]string, len(runs))
	for _, run := range runs {
		runProfiles[run.RunID] = agentProfileForRun(&run)
	}
	filtered := make([]ChannelMessage, 0, len(messages))
	for _, message := range messages {
		switch runProfiles[strings.TrimSpace(message.FromRunID)] {
		case AgentProfileResearcher, AgentProfileSuper, AgentProfileCoSuper:
			filtered = append(filtered, message)
		}
	}
	if len(filtered) > limit {
		filtered = filtered[len(filtered)-limit:]
	}
	return filtered, nil
}

func (rt *Runtime) userRevisionDiffSummaries(ctx context.Context, ownerID, docID string, limit int) ([]string, error) {
	revs, err := rt.Store().ListRevisionsByDoc(ctx, docID, ownerID, limit)
	if err != nil {
		return nil, err
	}
	summaries := make([]string, 0, len(revs))
	for i := len(revs) - 1; i >= 0; i-- {
		rev := revs[i]
		if rev.AuthorKind != types.AuthorUser {
			continue
		}
		label := rev.CreatedAt.UTC().Format(time.RFC3339)
		if rev.ParentRevisionID == "" {
			summaries = append(summaries, fmt.Sprintf("%s %s: initial user-authored draft", rev.RevisionID, label))
			continue
		}
		diff, err := rt.Store().GetDiff(ctx, rev.ParentRevisionID, rev.RevisionID, ownerID)
		if err != nil {
			continue
		}
		summaries = append(summaries, fmt.Sprintf("%s %s: %s", rev.RevisionID, label, summarizeDiffResult(diff)))
	}
	return summaries, nil
}

func truncatePromptSnippet(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || len(value) <= limit {
		return value
	}
	if limit <= 3 {
		return value[:limit]
	}
	return value[:limit-3] + "..."
}

func decodeRevisionMetadata(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

func metadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}

func summarizeDiffResult(diff types.DiffResult) string {
	if len(diff.Sections) == 0 {
		return "No line-level changes were detected."
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Added lines: %d. Removed lines: %d.", diff.AddedLines, diff.RemovedLines))
	changesShown := 0
	for _, section := range diff.Sections {
		if section.Type == "unchanged" {
			continue
		}
		if changesShown >= 4 {
			b.WriteString("\n- Additional changed sections omitted for brevity.")
			break
		}
		var snippet string
		switch section.Type {
		case "added":
			snippet = strings.TrimSpace(section.ToContent)
		case "removed":
			snippet = strings.TrimSpace(section.FromContent)
		default:
			snippet = strings.TrimSpace(section.ToContent)
			if snippet == "" {
				snippet = strings.TrimSpace(section.FromContent)
			}
		}
		if snippet == "" {
			snippet = "(empty change block)"
		}
		if len(snippet) > 240 {
			snippet = snippet[:240] + "..."
		}
		b.WriteString("\n- ")
		b.WriteString(section.Type)
		b.WriteString(": ")
		b.WriteString(snippet)
		changesShown++
	}
	return b.String()
}

// emitVTextAgentEvent is a helper that emits an vtext-specific agent revision
// event, carrying the doc_id in the payload so the frontend can correlate
// progress to the open document (VAL-ETEXT-004).
func (rt *Runtime) emitVTextAgentEvent(ctx context.Context, rec *types.RunRecord, kind types.EventKind, cause events.EventCause, payload json.RawMessage) {
	rt.bus.Publish(events.RuntimeEvent{
		Record: types.EventRecord{
			EventID:   uuid.New().String(),
			RunID:     rec.RunID,
			OwnerID:   rec.OwnerID,
			Timestamp: time.Now().UTC(),
			Kind:      kind,
			Payload:   payload,
		},
		Actor: events.ActorRuntime,
		Cause: cause,
	})

	// Also persist for catch-up.
	if err := rt.store.AppendEvent(ctx, &types.EventRecord{
		EventID:   uuid.New().String(),
		RunID:     rec.RunID,
		OwnerID:   rec.OwnerID,
		Timestamp: time.Now().UTC(),
		Kind:      kind,
		Payload:   payload,
	}); err != nil {
		log.Printf("runtime: persist vtext agent event: %v", err)
	}
}
