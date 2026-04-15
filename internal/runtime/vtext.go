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

	writeAPIJSON(w, http.StatusOK, vtextDocumentResponse{
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

func (h *APIHandler) handleEtextListRevisions(w http.ResponseWriter, r *http.Request, docID string) {
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
	Prompt string `json:"prompt"`
}

// vtextAgentRevisionResponse is the JSON response for agent revision
// submission. It returns the stable task handle so the client can track
// progress through the event stream (VAL-ETEXT-004).
type vtextAgentRevisionResponse struct {
	TaskID    string          `json:"task_id"`
	DocID     string          `json:"doc_id"`
	State     types.TaskState `json:"state"`
	CreatedAt string          `json:"created_at"`
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

	if strings.TrimSpace(req.Prompt) == "" {
		writeAPIJSON(w, http.StatusBadRequest, apiError{Error: "prompt is required"})
		return
	}

	// Verify the document exists and belongs to this owner.
	doc, err := h.rt.Store().GetDocument(r.Context(), docID, ownerID)
	if err != nil {
		writeAPIJSON(w, http.StatusNotFound, apiError{Error: "document not found"})
		return
	}

	// Check for an existing pending agent mutation on this document.
	// If one exists, return the existing task ID instead of creating a new
	// mutation. This prevents duplicate canonical revisions when
	// renewal/retry occurs mid-mutation (VAL-CROSS-122).
	existing, err := h.rt.Store().GetPendingAgentMutationByDoc(r.Context(), docID, ownerID)
	if err != nil {
		log.Printf("vtext api: check pending mutation: %v", err)
	} else if existing != nil {
		// Return the existing task — idempotent response.
		writeAPIJSON(w, http.StatusAccepted, vtextAgentRevisionResponse{
			TaskID:    existing.TaskID,
			DocID:     docID,
			State:     types.TaskPending,
			CreatedAt: existing.CreatedAt.Format("2006-01-02T15:04:05.000Z"),
		})
		return
	}

	// Build the prompt for the provider, including current document content.
	var currentContent string
	if doc.CurrentRevisionID != "" {
		rev, err := h.rt.Store().GetRevision(r.Context(), doc.CurrentRevisionID, ownerID)
		if err == nil {
			currentContent = rev.Content
		}
	}

	agentPrompt := buildAgentRevisionPrompt(currentContent, req.Prompt)

	// Create the runtime task with vtext agent revision metadata.
	metadata := map[string]any{
		"type":                "vtext_agent_revision",
		"agent_profile":       AgentProfileVText,
		"agent_role":          AgentProfileVText,
		"doc_id":              docID,
		"current_revision_id": doc.CurrentRevisionID,
		"original_prompt":     req.Prompt,
	}

	rec, err := h.rt.SubmitTaskWithMetadata(r.Context(), agentPrompt, ownerID, metadata)
	if err != nil {
		log.Printf("vtext api: submit agent revision task: %v", err)
		writeAPIJSON(w, http.StatusInternalServerError, apiError{Error: "failed to submit agent revision"})
		return
	}

	// Record the agent mutation for idempotency tracking (VAL-CROSS-122).
	if err := h.rt.Store().CreateAgentMutation(r.Context(), store.AgentMutation{
		DocID:     docID,
		TaskID:    rec.TaskID,
		OwnerID:   ownerID,
		State:     "pending",
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		log.Printf("vtext api: create agent mutation: %v", err)
	}

	// Emit the vtext-specific agent revision started event.
	startedPayload, _ := json.Marshal(map[string]string{
		"doc_id":  docID,
		"task_id": rec.TaskID,
	})
	h.rt.emitVTextAgentEvent(r.Context(), rec, types.EventVTextAgentRevisionStarted,
		events.CauseTaskLifecycle, startedPayload)

	writeAPIJSON(w, http.StatusAccepted, vtextAgentRevisionResponse{
		TaskID:    rec.TaskID,
		DocID:     docID,
		State:     rec.State,
		CreatedAt: rec.CreatedAt.Format("2006-01-02T15:04:05.000Z"),
	})
}

// buildAgentRevisionPrompt constructs the prompt for the provider that
// includes the current document content and the user's revision request.
func buildAgentRevisionPrompt(currentContent, userPrompt string) string {
	var b strings.Builder
	b.WriteString("You are the ChoirOS vtext agent, responsible for creating the next canonical document version.\n\n")
	b.WriteString("Rules:\n")
	b.WriteString("- The current document is canonical input state.\n")
	b.WriteString("- You may spawn researcher or super agents and coordinate over shared channels when the request needs external, current, or specialist information.\n")
	b.WriteString("- Workers may read the document and send findings, but they must not directly author canonical text.\n")
	b.WriteString("- Your final answer must be the complete next document version only.\n\n")
	b.WriteString("The user has requested the following change:\n\n")
	b.WriteString(userPrompt)
	b.WriteString("\n\nCurrent document content:\n---\n")
	if currentContent != "" {
		b.WriteString(currentContent)
	} else {
		b.WriteString("(empty document)")
	}
	b.WriteString("\n---\n\n")
	b.WriteString("If the request is local-only editing, you may revise directly. If it requires research or current facts, delegate first and then synthesize the findings into the next full document version. Output only the revised document content, with no commentary or explanation.")
	return b.String()
}

// emitVTextAgentEvent is a helper that emits an vtext-specific agent revision
// event, carrying the doc_id in the payload so the frontend can correlate
// progress to the open document (VAL-ETEXT-004).
func (rt *Runtime) emitVTextAgentEvent(ctx context.Context, rec *types.TaskRecord, kind types.EventKind, cause events.EventCause, payload json.RawMessage) {
	rt.bus.Publish(events.RuntimeEvent{
		Record: types.EventRecord{
			EventID:   uuid.New().String(),
			TaskID:    rec.TaskID,
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
		TaskID:    rec.TaskID,
		OwnerID:   rec.OwnerID,
		Timestamp: time.Now().UTC(),
		Kind:      kind,
		Payload:   payload,
	}); err != nil {
		log.Printf("runtime: persist vtext agent event: %v", err)
	}
}
