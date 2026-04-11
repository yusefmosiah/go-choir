// Package runtime provides e-text document API handlers for the go-choir
// sandbox runtime. These handlers expose the document CRUD, revision,
// history, snapshot, diff, and blame APIs through the authenticated
// same-origin proxy path.
//
// API endpoints:
//
//	POST   /api/etext/documents          — create a new document
//	GET    /api/etext/documents          — list documents for the authenticated user
//	GET    /api/etext/documents/{id}     — get a document by ID
//	PUT    /api/etext/documents/{id}     — update a document (e.g., title)
//	DELETE /api/etext/documents/{id}     — delete a document and its revisions
//	POST   /api/etext/documents/{id}/revisions — create a new revision (user edit or appagent edit)
//	GET    /api/etext/documents/{id}/revisions — list revisions for a document
//	GET    /api/etext/revisions/{id}    — get a specific revision (snapshot)
//	GET    /api/etext/documents/{id}/history — get revision history with attribution
//	GET    /api/etext/diff?from={id}&to={id} — diff two revisions
//	GET    /api/etext/revisions/{id}/blame — blame a revision
package runtime

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/yusefmosiah/go-choir/internal/types"
)

// ----- Request/Response types -----

// etextCreateDocRequest is the JSON payload for POST /api/etext/documents.
type etextCreateDocRequest struct {
	Title string `json:"title"`
}

// etextCreateDocResponse is the JSON response for POST /api/etext/documents.
type etextCreateDocResponse struct {
	DocID    string `json:"doc_id"`
	OwnerID  string `json:"owner_id"`
	Title    string `json:"title"`
	CreatedAt string `json:"created_at"`
}

// etextDocumentResponse is the JSON response for GET /api/etext/documents/{id}.
type etextDocumentResponse struct {
	DocID              string `json:"doc_id"`
	OwnerID            string `json:"owner_id"`
	Title              string `json:"title"`
	CurrentRevisionID  string `json:"current_revision_id,omitempty"`
	CreatedAt          string `json:"created_at"`
	UpdatedAt          string `json:"updated_at"`
}

// etextUpdateDocRequest is the JSON payload for PUT /api/etext/documents/{id}.
type etextUpdateDocRequest struct {
	Title string `json:"title"`
}

// etextListDocsResponse is the JSON response for GET /api/etext/documents.
type etextListDocsResponse struct {
	Documents []etextDocumentResponse `json:"documents"`
}

// etextCreateRevisionRequest is the JSON payload for
// POST /api/etext/documents/{id}/revisions.
type etextCreateRevisionRequest struct {
	Content          string           `json:"content"`
	AuthorKind       types.AuthorKind `json:"author_kind"`
	AuthorLabel      string           `json:"author_label"`
	Citations        json.RawMessage  `json:"citations,omitempty"`
	Metadata         json.RawMessage  `json:"metadata,omitempty"`
	ParentRevisionID string           `json:"parent_revision_id,omitempty"`
}

// etextRevisionResponse is the JSON response for revision-related endpoints.
type etextRevisionResponse struct {
	RevisionID       string              `json:"revision_id"`
	DocID            string              `json:"doc_id"`
	OwnerID          string              `json:"owner_id"`
	AuthorKind       types.AuthorKind    `json:"author_kind"`
	AuthorLabel      string              `json:"author_label"`
	Content          string              `json:"content"`
	Citations        json.RawMessage     `json:"citations,omitempty"`
	Metadata         json.RawMessage     `json:"metadata,omitempty"`
	ParentRevisionID string              `json:"parent_revision_id,omitempty"`
	CreatedAt        string              `json:"created_at"`
}

// etextListRevisionsResponse is the JSON response for
// GET /api/etext/documents/{id}/revisions.
type etextListRevisionsResponse struct {
	Revisions []etextRevisionResponse `json:"revisions"`
}

// etextHistoryResponse is the JSON response for
// GET /api/etext/documents/{id}/history.
type etextHistoryResponse struct {
	DocID   string                 `json:"doc_id"`
	Entries []types.HistoryEntry   `json:"entries"`
}

// etextDiffResponse is the JSON response for GET /api/etext/diff.
type etextDiffResponse struct {
	types.DiffResult
}

// etextBlameResponse is the JSON response for
// GET /api/etext/revisions/{id}/blame.
type etextBlameResponse struct {
	types.BlameResult
}

// ----- Helper functions -----

// extractDocID extracts the document ID from the URL path.
// Expected pattern: /api/etext/documents/{docID}/...
func extractDocID(path string) string {
	const prefix = "/api/etext/documents/"
	if !strings.HasPrefix(path, prefix) {
		return ""
	}
	rest := strings.TrimPrefix(path, prefix)
	// The docID is the first path segment.
	parts := strings.SplitN(rest, "/", 2)
	return parts[0]
}

// extractRevisionID extracts the revision ID from the URL path.
// Expected pattern: /api/etext/revisions/{revisionID}/...
func extractRevisionID(path string) string {
	const prefix = "/api/etext/revisions/"
	if !strings.HasPrefix(path, prefix) {
		return ""
	}
	rest := strings.TrimPrefix(path, prefix)
	parts := strings.SplitN(rest, "/", 2)
	return parts[0]
}

// ----- Handler methods -----

// HandleEtextCreateDocument handles POST /api/etext/documents.
// It creates a new document with a durable document identity (VAL-ETEXT-001).
func (h *APIHandler) HandleEtextCreateDocument(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIJSON(w, http.StatusMethodNotAllowed, apiError{Error: "method not allowed"})
		return
	}

	ownerID, err := authenticateUser(r)
	if err != nil {
		writeAPIJSON(w, http.StatusUnauthorized, apiError{Error: "authentication required"})
		return
	}

	var req etextCreateDocRequest
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
		log.Printf("etext api: create document: %v", err)
		writeAPIJSON(w, http.StatusInternalServerError, apiError{Error: "failed to create document"})
		return
	}

	writeAPIJSON(w, http.StatusCreated, etextCreateDocResponse{
		DocID:     doc.DocID,
		OwnerID:   doc.OwnerID,
		Title:     doc.Title,
		CreatedAt: doc.CreatedAt.Format("2006-01-02T15:04:05.000Z"),
	})
}

// HandleEtextListDocuments handles GET /api/etext/documents.
// It returns documents owned by the authenticated user.
func (h *APIHandler) HandleEtextListDocuments(w http.ResponseWriter, r *http.Request) {
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
		log.Printf("etext api: list documents: %v", err)
		writeAPIJSON(w, http.StatusInternalServerError, apiError{Error: "failed to list documents"})
		return
	}

	resp := etextListDocsResponse{Documents: make([]etextDocumentResponse, 0, len(docs))}
	for _, doc := range docs {
		resp.Documents = append(resp.Documents, etextDocumentResponse{
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

// HandleEtextDocument handles GET/PUT/DELETE /api/etext/documents/{id}.
func (h *APIHandler) HandleEtextDocument(w http.ResponseWriter, r *http.Request) {
	docID := extractDocID(r.URL.Path)
	if docID == "" {
		writeAPIJSON(w, http.StatusBadRequest, apiError{Error: "document ID is required"})
		return
	}

	switch r.Method {
	case http.MethodGet:
		h.handleEtextGetDocument(w, r, docID)
	case http.MethodPut:
		h.handleEtextUpdateDocument(w, r, docID)
	case http.MethodDelete:
		h.handleEtextDeleteDocument(w, r, docID)
	default:
		writeAPIJSON(w, http.StatusMethodNotAllowed, apiError{Error: "method not allowed"})
	}
}

func (h *APIHandler) handleEtextGetDocument(w http.ResponseWriter, r *http.Request, docID string) {
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

	writeAPIJSON(w, http.StatusOK, etextDocumentResponse{
		DocID:             doc.DocID,
		OwnerID:           doc.OwnerID,
		Title:             doc.Title,
		CurrentRevisionID: doc.CurrentRevisionID,
		CreatedAt:         doc.CreatedAt.Format("2006-01-02T15:04:05.000Z"),
		UpdatedAt:         doc.UpdatedAt.Format("2006-01-02T15:04:05.000Z"),
	})
}

func (h *APIHandler) handleEtextUpdateDocument(w http.ResponseWriter, r *http.Request, docID string) {
	ownerID, err := authenticateUser(r)
	if err != nil {
		writeAPIJSON(w, http.StatusUnauthorized, apiError{Error: "authentication required"})
		return
	}

	var req etextUpdateDocRequest
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
		log.Printf("etext api: update document: %v", err)
		writeAPIJSON(w, http.StatusInternalServerError, apiError{Error: "failed to update document"})
		return
	}

	writeAPIJSON(w, http.StatusOK, etextDocumentResponse{
		DocID:             doc.DocID,
		OwnerID:           doc.OwnerID,
		Title:             doc.Title,
		CurrentRevisionID: doc.CurrentRevisionID,
		CreatedAt:         doc.CreatedAt.Format("2006-01-02T15:04:05.000Z"),
		UpdatedAt:         doc.UpdatedAt.Format("2006-01-02T15:04:05.000Z"),
	})
}

func (h *APIHandler) handleEtextDeleteDocument(w http.ResponseWriter, r *http.Request, docID string) {
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

// HandleEtextRevisions handles POST and GET
// /api/etext/documents/{id}/revisions.
func (h *APIHandler) HandleEtextRevisions(w http.ResponseWriter, r *http.Request) {
	docID := extractDocID(r.URL.Path)
	if docID == "" {
		writeAPIJSON(w, http.StatusBadRequest, apiError{Error: "document ID is required"})
		return
	}

	switch r.Method {
	case http.MethodPost:
		h.handleEtextCreateRevision(w, r, docID)
	case http.MethodGet:
		h.handleEtextListRevisions(w, r, docID)
	default:
		writeAPIJSON(w, http.StatusMethodNotAllowed, apiError{Error: "method not allowed"})
	}
}

func (h *APIHandler) handleEtextCreateRevision(w http.ResponseWriter, r *http.Request, docID string) {
	ownerID, err := authenticateUser(r)
	if err != nil {
		writeAPIJSON(w, http.StatusUnauthorized, apiError{Error: "authentication required"})
		return
	}

	var req etextCreateRevisionRequest
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
		log.Printf("etext api: create revision: %v", err)
		writeAPIJSON(w, http.StatusInternalServerError, apiError{Error: "failed to create revision"})
		return
	}

	writeAPIJSON(w, http.StatusCreated, etextRevisionResponse{
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

func (h *APIHandler) handleEtextListRevisions(w http.ResponseWriter, r *http.Request, docID string) {
	ownerID, err := authenticateUser(r)
	if err != nil {
		writeAPIJSON(w, http.StatusUnauthorized, apiError{Error: "authentication required"})
		return
	}

	revs, err := h.rt.Store().ListRevisionsByDoc(r.Context(), docID, ownerID, 50)
	if err != nil {
		log.Printf("etext api: list revisions: %v", err)
		writeAPIJSON(w, http.StatusInternalServerError, apiError{Error: "failed to list revisions"})
		return
	}

	resp := etextListRevisionsResponse{Revisions: make([]etextRevisionResponse, 0, len(revs))}
	for _, rev := range revs {
		resp.Revisions = append(resp.Revisions, etextRevisionResponse{
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

// HandleEtextRevision handles GET /api/etext/revisions/{id}.
// Opening a historical revision does not mutate the document head
// (VAL-ETEXT-007: historical snapshots can be opened without mutating head).
func (h *APIHandler) HandleEtextRevision(w http.ResponseWriter, r *http.Request) {
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

	writeAPIJSON(w, http.StatusOK, etextRevisionResponse{
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

// HandleEtextHistory handles GET /api/etext/documents/{id}/history.
// It returns the revision history with explicit attribution metadata
// (VAL-ETEXT-006: version history lists revisions with explicit
// attribution metadata).
func (h *APIHandler) HandleEtextHistory(w http.ResponseWriter, r *http.Request) {
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
		log.Printf("etext api: get history: %v", err)
		writeAPIJSON(w, http.StatusInternalServerError, apiError{Error: "failed to get history"})
		return
	}

	writeAPIJSON(w, http.StatusOK, etextHistoryResponse{
		DocID:   docID,
		Entries: entries,
	})
}

// HandleEtextDiff handles GET /api/etext/diff?from={id}&to={id}.
// It compares selected from and to revisions and shows the changed
// sections (VAL-ETEXT-008: diff view compares selected revisions and
// changed sections).
func (h *APIHandler) HandleEtextDiff(w http.ResponseWriter, r *http.Request) {
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

	writeAPIJSON(w, http.StatusOK, etextDiffResponse{DiffResult: diff})
}

// HandleEtextBlame handles GET /api/etext/revisions/{id}/blame.
// It provides section-level attribution that distinguishes whether the
// last editor was the user or the agent (VAL-ETEXT-009: blame identifies
// the last editor per section).
func (h *APIHandler) HandleEtextBlame(w http.ResponseWriter, r *http.Request) {
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

	writeAPIJSON(w, http.StatusOK, etextBlameResponse{BlameResult: blame})
}
