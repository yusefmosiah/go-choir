package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/yusefmosiah/go-choir/internal/server"
	"github.com/yusefmosiah/go-choir/internal/types"
)

// apiError is a JSON error envelope for API responses.
type apiError struct {
	Error string `json:"error"`
}

// taskSubmitRequest is the JSON payload for POST /api/agent/task.
type taskSubmitRequest struct {
	Prompt   string         `json:"prompt"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// spawnRequest is the JSON payload for POST /api/agent/spawn.
// It creates a child task linked to a parent, with an objective and optional
// constraints (VAL-CHOIR-001).
type spawnRequest struct {
	ParentID    string         `json:"parent_id"`
	Objective   string         `json:"objective"`
	Constraints map[string]any `json:"constraints,omitempty"`
}

// spawnResponse is the JSON response for POST /api/agent/spawn.
// It returns the child task handle with the parent linkage (VAL-CHOIR-001,
// VAL-CHOIR-004).
type spawnResponse struct {
	TaskID    string          `json:"task_id"`
	ParentID  string          `json:"parent_id"`
	State     types.TaskState `json:"state"`
	OwnerID   string          `json:"owner_id"`
	CreatedAt string          `json:"created_at"`
}

// cancelRequest is the JSON payload for POST /api/agent/cancel.
// It cancels a running or pending task (VAL-CHOIR-010).
type cancelRequest struct {
	TaskID string `json:"task_id"`
}

// cancelResponse is the JSON response for POST /api/agent/cancel.
type cancelResponse struct {
	TaskID string          `json:"task_id"`
	State  types.TaskState `json:"state"`
}

// taskSubmitResponse is the JSON response for POST /api/agent/task.
// It returns the stable task handle and initial lifecycle state
// (VAL-RUNTIME-003).
type taskSubmitResponse struct {
	TaskID    string          `json:"task_id"`
	State     types.TaskState `json:"state"`
	OwnerID   string          `json:"owner_id"`
	CreatedAt string          `json:"created_at"`
}

// taskStatusResponse is the JSON response for GET /api/agent/status.
// It returns the full task record correlated to the submitted handle
// (VAL-RUNTIME-004).
type taskStatusResponse struct {
	TaskID     string          `json:"task_id"`
	OwnerID    string          `json:"owner_id"`
	SandboxID  string          `json:"sandbox_id"`
	State      types.TaskState `json:"state"`
	Prompt     string          `json:"prompt"`
	Result     string          `json:"result,omitempty"`
	Error      string          `json:"error,omitempty"`
	CreatedAt  string          `json:"created_at"`
	UpdatedAt  string          `json:"updated_at"`
	FinishedAt *string         `json:"finished_at,omitempty"`
	Metadata   map[string]any  `json:"metadata,omitempty"`
}

// runtimeHealthResponse is the JSON structure returned by GET /health.
// It reports runtime readiness for real task handling, and surfaces
// degraded state rather than hiding it behind a generic healthy response
// (VAL-RUNTIME-001). The active provider name is included so operators
// can distinguish real-provider paths from stub/canned paths.
type runtimeHealthResponse struct {
	Status          string                   `json:"status"`
	Service         string                   `json:"service"`
	SandboxID       string                   `json:"sandbox_id"`
	RuntimeHealth   types.RuntimeHealthState `json:"runtime_health"`
	RunningTasks    int                      `json:"running_tasks"`
	ResearcherCount int                      `json:"researcher_count"`
	ActiveProvider  string                   `json:"active_provider"`
}

// runtimeTopologyResponse is the JSON structure returned by GET /api/agent/topology.
// It surfaces the configured orchestration shape so operators and UI surfaces
// can see how many researchers the microVM expects and what the current runtime
// fan-out looks like.
type runtimeTopologyResponse struct {
	SandboxID           string `json:"sandbox_id"`
	ResearcherCount     int    `json:"researcher_count"`
	RunningTasks        int    `json:"running_tasks"`
	ChannelCount        int    `json:"channel_count"`
	SupervisionInterval string `json:"supervision_interval"`
	RuntimeHealth       string `json:"runtime_health"`
	ActiveProvider      string `json:"active_provider"`
}

// APIHandler provides HTTP handlers for the runtime API endpoints.
type APIHandler struct {
	rt *Runtime
}

// NewAPIHandler creates an APIHandler for the given runtime.
func NewAPIHandler(rt *Runtime) *APIHandler {
	return &APIHandler{rt: rt}
}

// authenticateUser extracts the authenticated user identity from the
// X-Authenticated-User header injected by the proxy. It returns an error if
// the header is missing, which provides defense-in-depth auth gating at the
// sandbox level (VAL-RUNTIME-002).
func authenticateUser(r *http.Request) (string, error) {
	user := r.Header.Get("X-Authenticated-User")
	if user == "" {
		return "", fmt.Errorf("missing authenticated user identity")
	}
	return user, nil
}

// writeJSON writes a JSON response with the given status code.
func writeAPIJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("runtime api: json encode error: %v", err)
	}
}

// HandleTaskSubmission handles POST /api/agent/task.
// It accepts work only through the authenticated same-origin proxy path and
// denies missing or invalid auth before runtime work starts
// (VAL-RUNTIME-002). Returns a stable task handle (VAL-RUNTIME-003).
func (h *APIHandler) HandleTaskSubmission(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIJSON(w, http.StatusMethodNotAllowed, apiError{Error: "method not allowed"})
		return
	}

	ownerID, err := authenticateUser(r)
	if err != nil {
		writeAPIJSON(w, http.StatusUnauthorized, apiError{Error: "authentication required"})
		return
	}

	var req taskSubmitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIJSON(w, http.StatusBadRequest, apiError{Error: "invalid request body"})
		return
	}

	if strings.TrimSpace(req.Prompt) == "" {
		writeAPIJSON(w, http.StatusBadRequest, apiError{Error: "prompt is required"})
		return
	}

	rec, err := h.rt.SubmitTaskWithMetadata(r.Context(), req.Prompt, ownerID, req.Metadata)
	if err != nil {
		log.Printf("runtime api: submit task: %v", err)
		writeAPIJSON(w, http.StatusInternalServerError, apiError{Error: "failed to submit task"})
		return
	}

	writeAPIJSON(w, http.StatusAccepted, taskSubmitResponse{
		TaskID:    rec.TaskID,
		State:     rec.State,
		OwnerID:   rec.OwnerID,
		CreatedAt: rec.CreatedAt.Format("2006-01-02T15:04:05.000Z"),
	})
}

// HandleSpawn handles POST /api/agent/spawn.
// It creates a child task linked to the given parent task, tracking it in
// the work registry with parent-child relationships (VAL-CHOIR-001,
// VAL-CHOIR-004). The child task inherits the owner from the authenticated
// user context and begins in pending state.
func (h *APIHandler) HandleSpawn(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIJSON(w, http.StatusMethodNotAllowed, apiError{Error: "method not allowed"})
		return
	}

	ownerID, err := authenticateUser(r)
	if err != nil {
		writeAPIJSON(w, http.StatusUnauthorized, apiError{Error: "authentication required"})
		return
	}

	var req spawnRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIJSON(w, http.StatusBadRequest, apiError{Error: "invalid request body"})
		return
	}

	if strings.TrimSpace(req.ParentID) == "" {
		writeAPIJSON(w, http.StatusBadRequest, apiError{Error: "parent_id is required"})
		return
	}

	if strings.TrimSpace(req.Objective) == "" {
		writeAPIJSON(w, http.StatusBadRequest, apiError{Error: "objective is required"})
		return
	}

	rec, err := h.rt.SpawnTask(r.Context(), req.ParentID, req.Objective, ownerID, req.Constraints)
	if err != nil {
		// Check if the parent was not found.
		if strings.Contains(err.Error(), "parent task not found") {
			writeAPIJSON(w, http.StatusNotFound, apiError{Error: "parent task not found"})
			return
		}
		log.Printf("runtime api: spawn task: %v", err)
		writeAPIJSON(w, http.StatusInternalServerError, apiError{Error: "failed to spawn task"})
		return
	}

	writeAPIJSON(w, http.StatusAccepted, spawnResponse{
		TaskID:    rec.TaskID,
		ParentID:  req.ParentID,
		State:     rec.State,
		OwnerID:   rec.OwnerID,
		CreatedAt: rec.CreatedAt.Format("2006-01-02T15:04:05.000Z"),
	})
}

// HandleCancel handles POST /api/agent/cancel.
// It cancels a running or pending task, transitioning it to cancelled state.
// The cancel endpoint is owner-scoped — a request for a task owned by a
// different user returns 404 to prevent IDOR probing (VAL-CHOIR-010).
func (h *APIHandler) HandleCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIJSON(w, http.StatusMethodNotAllowed, apiError{Error: "method not allowed"})
		return
	}

	ownerID, err := authenticateUser(r)
	if err != nil {
		writeAPIJSON(w, http.StatusUnauthorized, apiError{Error: "authentication required"})
		return
	}

	var req cancelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIJSON(w, http.StatusBadRequest, apiError{Error: "invalid request body"})
		return
	}

	if strings.TrimSpace(req.TaskID) == "" {
		writeAPIJSON(w, http.StatusBadRequest, apiError{Error: "task_id is required"})
		return
	}

	err = h.rt.CancelTask(r.Context(), req.TaskID, ownerID)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeAPIJSON(w, http.StatusNotFound, apiError{Error: "task not found"})
			return
		}
		if strings.Contains(err.Error(), "cannot cancel") {
			writeAPIJSON(w, http.StatusConflict, apiError{Error: err.Error()})
			return
		}
		log.Printf("runtime api: cancel task: %v", err)
		writeAPIJSON(w, http.StatusInternalServerError, apiError{Error: "failed to cancel task"})
		return
	}

	writeAPIJSON(w, http.StatusOK, cancelResponse{
		TaskID: req.TaskID,
		State:  types.TaskCancelled,
	})
}

// HandleTaskStatus handles GET /api/agent/status.
// It is exposed through the authenticated same-origin proxy path, accepts or
// returns a stable correlation to the task handle from submission, and exposes
// machine-readable lifecycle state including non-happy-path outcomes
// (VAL-RUNTIME-004, VAL-RUNTIME-006).
func (h *APIHandler) HandleTaskStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIJSON(w, http.StatusMethodNotAllowed, apiError{Error: "method not allowed"})
		return
	}

	ownerID, err := authenticateUser(r)
	if err != nil {
		writeAPIJSON(w, http.StatusUnauthorized, apiError{Error: "authentication required"})
		return
	}

	taskID := r.URL.Query().Get("task_id")
	if taskID == "" {
		writeAPIJSON(w, http.StatusBadRequest, apiError{Error: "task_id query parameter is required"})
		return
	}

	rec, err := h.rt.GetTask(r.Context(), taskID, ownerID)
	if err != nil {
		// ErrNotFound covers both "task doesn't exist" and "task belongs to
		// another user" so callers cannot probe for other users' tasks.
		writeAPIJSON(w, http.StatusNotFound, apiError{Error: "task not found"})
		return
	}

	var finishedAt *string
	if rec.FinishedAt != nil {
		s := rec.FinishedAt.Format("2006-01-02T15:04:05.000Z")
		finishedAt = &s
	}

	writeAPIJSON(w, http.StatusOK, taskStatusResponse{
		TaskID:     rec.TaskID,
		OwnerID:    rec.OwnerID,
		SandboxID:  rec.SandboxID,
		State:      rec.State,
		Prompt:     rec.Prompt,
		Result:     rec.Result,
		Error:      rec.Error,
		CreatedAt:  rec.CreatedAt.Format("2006-01-02T15:04:05.000Z"),
		UpdatedAt:  rec.UpdatedAt.Format("2006-01-02T15:04:05.000Z"),
		FinishedAt: finishedAt,
		Metadata:   rec.Metadata,
	})
}

// HandleTaskStatusByID handles GET /api/agent/{id}/status.
// It returns the full task record for the task identified by the URL path
// parameter {id}. The response includes state, result (if complete), error
// (if failed), and timestamps (VAL-CHOIR-002, VAL-CHOIR-005).
// Access is scoped to the authenticated owner — a request for a task owned
// by a different user returns 404 to prevent IDOR probing. State updates
// are visible immediately after change.
func (h *APIHandler) HandleTaskStatusByID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIJSON(w, http.StatusMethodNotAllowed, apiError{Error: "method not allowed"})
		return
	}

	ownerID, err := authenticateUser(r)
	if err != nil {
		writeAPIJSON(w, http.StatusUnauthorized, apiError{Error: "authentication required"})
		return
	}

	// Extract task ID from URL path: /api/agent/{id}/status
	// Expected prefix: /api/agent/  and suffix: /status
	path := r.URL.Path
	prefix := "/api/agent/"
	suffix := "/status"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		writeAPIJSON(w, http.StatusBadRequest, apiError{Error: "invalid status path"})
		return
	}
	taskID := strings.TrimPrefix(path, prefix)
	taskID = strings.TrimSuffix(taskID, suffix)
	if taskID == "" {
		writeAPIJSON(w, http.StatusBadRequest, apiError{Error: "task ID is required"})
		return
	}

	rec, err := h.rt.GetTask(r.Context(), taskID, ownerID)
	if err != nil {
		// ErrNotFound covers both "task doesn't exist" and "task belongs to
		// another user" so callers cannot probe for other users' tasks.
		writeAPIJSON(w, http.StatusNotFound, apiError{Error: "task not found"})
		return
	}

	var finishedAt *string
	if rec.FinishedAt != nil {
		s := rec.FinishedAt.Format("2006-01-02T15:04:05.000Z")
		finishedAt = &s
	}

	writeAPIJSON(w, http.StatusOK, taskStatusResponse{
		TaskID:     rec.TaskID,
		OwnerID:    rec.OwnerID,
		SandboxID:  rec.SandboxID,
		State:      rec.State,
		Prompt:     rec.Prompt,
		Result:     rec.Result,
		Error:      rec.Error,
		CreatedAt:  rec.CreatedAt.Format("2006-01-02T15:04:05.000Z"),
		UpdatedAt:  rec.UpdatedAt.Format("2006-01-02T15:04:05.000Z"),
		FinishedAt: finishedAt,
		Metadata:   rec.Metadata,
	})
}

// HandleTopology handles GET /api/agent/topology.
// It exposes the runtime's orchestration shape for operator/UI inspection:
// researcher count, supervision interval, current running task count, and
// the number of active coordination channels.
func (h *APIHandler) HandleTopology(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIJSON(w, http.StatusMethodNotAllowed, apiError{Error: "method not allowed"})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(runtimeTopologyResponse{
		SandboxID:           h.rt.cfg.SandboxID,
		ResearcherCount:     h.rt.cfg.ResearcherCount,
		RunningTasks:        h.rt.RunningCount(),
		ChannelCount:        len(h.rt.ChannelManager().ListChannels()),
		SupervisionInterval: h.rt.cfg.SupervisionInterval.String(),
		RuntimeHealth:       string(h.rt.HealthState()),
		ActiveProvider:      h.rt.provider.ProviderName(),
	})
}

// HandleEvents handles GET /api/events.
// It provides a long-lived incremental event stream through the authenticated
// same-origin proxy path, emitting ordered lifecycle updates correlated to
// accepted runtime work before final completion (VAL-RUNTIME-005).
// Status and events are auth-gated and caller-scoped (VAL-RUNTIME-006).
func (h *APIHandler) HandleEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIJSON(w, http.StatusMethodNotAllowed, apiError{Error: "method not allowed"})
		return
	}

	ownerID, err := authenticateUser(r)
	if err != nil {
		writeAPIJSON(w, http.StatusUnauthorized, apiError{Error: "authentication required"})
		return
	}

	// Set SSE headers.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable nginx buffering

	// Flush headers.
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	// Parse optional after_seq for catch-up.
	afterSeq := int64(0)
	if v := r.URL.Query().Get("after_seq"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			afterSeq = n
		}
	}

	// Send historical events for catch-up if requested.
	if afterSeq > 0 {
		h.sendHistoricalEvents(r.Context(), w, ownerID, afterSeq)
	}

	// Subscribe to live events.
	bus := h.rt.EventBus()
	ch := bus.SubscribeWithBuffer(128)
	defer bus.Unsubscribe(ch)

	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			// Filter by owner (caller scoping, VAL-RUNTIME-006).
			if ev.Record.OwnerID != ownerID && ev.Record.OwnerID != "" {
				continue
			}
			// Write SSE event.
			data, err := json.Marshal(ev.Record)
			if err != nil {
				log.Printf("runtime api: marshal event: %v", err)
				continue
			}
			_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}
}

// sendHistoricalEvents fetches and writes historical events from the store
// for the given owner with sequence > afterSeq. This supports SSE catch-up
// after reconnection.
func (h *APIHandler) sendHistoricalEvents(ctx context.Context, w http.ResponseWriter, ownerID string, afterSeq int64) {
	events, err := h.rt.Store().ListEventsByOwnerAfter(ctx, ownerID, afterSeq, 200)
	if err != nil {
		log.Printf("runtime api: fetch historical events: %v", err)
		return
	}
	for _, ev := range events {
		data, err := json.Marshal(ev)
		if err != nil {
			continue
		}
		_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
	}
	if len(events) > 0 {
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
}

// HandleHealth handles GET /health for the runtime service.
// It reports runtime readiness for real task handling, and surfaces degraded
// state rather than hiding it behind a generic healthy response
// (VAL-RUNTIME-001).
func (h *APIHandler) HandleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	health := h.rt.HealthState()

	// Map runtime health to HTTP status code.
	httpStatus := http.StatusOK
	switch health {
	case types.HealthFailed:
		httpStatus = http.StatusServiceUnavailable
	case types.HealthDegraded:
		httpStatus = http.StatusOK // degraded is still serving, just observable
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(httpStatus)
	_ = json.NewEncoder(w).Encode(runtimeHealthResponse{
		Status:          string(health),
		Service:         "sandbox",
		SandboxID:       h.rt.cfg.SandboxID,
		RuntimeHealth:   health,
		RunningTasks:    h.rt.RunningCount(),
		ResearcherCount: h.rt.cfg.ResearcherCount,
		ActiveProvider:  h.rt.provider.ProviderName(),
	})
}

// RegisterRoutes registers runtime API routes on the given server.
// The health handler overrides the default server health handler to
// report runtime readiness.
func RegisterRoutes(s *server.Server, h *APIHandler) {
	s.SetHealthHandler(h.HandleHealth)
	s.HandleFunc("/api/agent/task", h.HandleTaskSubmission)
	s.HandleFunc("/api/agent/topology", h.HandleTopology)
	s.HandleFunc("/api/agent/spawn", h.HandleSpawn)
	s.HandleFunc("/api/agent/cancel", h.HandleCancel)
	s.HandleFunc("/api/agent/status", h.HandleTaskStatus)
	s.HandleFunc("/api/agent/", h.HandleTaskStatusByID) // matches /api/agent/{id}/status
	s.HandleFunc("/api/events", h.HandleEvents)
	s.HandleFunc("/api/desktop/state", h.HandleDesktopState)

	// E-text document/revision/history/diff/blame APIs.
	// All e-text routes are dispatched from a single prefix handler
	// that inspects the URL path and method to route to the correct
	// handler. This avoids ambiguity with Go's ServeMux prefix matching.
	RegisterEtextRoutes(s, h)
	RegisterVTextRoutes(s, h)
}

// RegisterEtextRoutes registers e-text API routes on the given server.
// These routes expose document CRUD, revision, history, snapshot, diff,
// and blame APIs through the authenticated same-origin proxy path.
func RegisterEtextRoutes(s *server.Server, h *APIHandler) {
	// Exact match for document collection (create/list).
	s.HandleFunc("/api/etext/documents", h.HandleEtextDocumentsRoot)

	// Prefix match for all other e-text routes: individual documents,
	// revisions, history, diff, blame.
	s.HandleFunc("/api/etext/", h.HandleEtextRouter)
}

// RegisterVTextRoutes registers vtext API aliases on the given server.
// These routes rewrite /api/vtext/... requests to the historical /api/etext/...
// handlers so the product-facing naming can move ahead of the internal rename.
func RegisterVTextRoutes(s *server.Server, h *APIHandler) {
	// Exact match for document collection (create/list).
	s.HandleFunc("/api/vtext/documents", h.HandleVTextDocumentsRoot)

	// Prefix match for all other vtext routes.
	s.HandleFunc("/api/vtext/", h.HandleVTextRouter)
}

func rewriteEtextToVText(r *http.Request) *http.Request {
	req := r.Clone(r.Context())
	req.URL.Path = strings.Replace(req.URL.Path, "/api/vtext/", "/api/etext/", 1)
	return req
}

// HandleVTextRouter dispatches vtext API requests by rewriting them to the
// historical e-text route handlers.
func (h *APIHandler) HandleVTextRouter(w http.ResponseWriter, r *http.Request) {
	h.HandleEtextRouter(w, rewriteEtextToVText(r))
}

// HandleVTextDocumentsRoot routes POST to create and GET to list at
// /api/vtext/documents (exact match, no trailing slash).
func (h *APIHandler) HandleVTextDocumentsRoot(w http.ResponseWriter, r *http.Request) {
	h.HandleEtextDocumentsRoot(w, rewriteEtextToVText(r))
}

// HandleEtextRouter dispatches e-text API requests based on URL path and
// method. It handles all paths under /api/etext/ that are not matched by
// the exact /api/etext/documents route.
//
// Route mapping:
//
//	GET    /api/etext/documents/{id}           → get document
//	PUT    /api/etext/documents/{id}           → update document
//	DELETE /api/etext/documents/{id}           → delete document
//	POST   /api/etext/documents/{id}/revisions → create revision
//	GET    /api/etext/documents/{id}/revisions → list revisions
//	POST   /api/etext/documents/{id}/agent-revision → submit agent revision
//	GET    /api/etext/documents/{id}/history   → revision history
//	GET    /api/etext/revisions/{id}          → get revision (snapshot)
//	GET    /api/etext/revisions/{id}/blame     → blame revision
//	GET    /api/etext/diff                     → diff two revisions
func (h *APIHandler) HandleEtextRouter(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	// Diff endpoint: /api/etext/diff
	if path == "/api/etext/diff" {
		h.HandleEtextDiff(w, r)
		return
	}

	// Revision item: /api/etext/revisions/{id}
	if strings.HasPrefix(path, "/api/etext/revisions/") {
		// Check for blame suffix: /api/etext/revisions/{id}/blame
		if strings.HasSuffix(path, "/blame") {
			h.HandleEtextBlame(w, r)
			return
		}
		h.HandleEtextRevision(w, r)
		return
	}

	// Document sub-paths: /api/etext/documents/{id}/...
	if strings.HasPrefix(path, "/api/etext/documents/") {
		// Extract the part after /api/etext/documents/
		rest := strings.TrimPrefix(path, "/api/etext/documents/")

		// Check for sub-resource suffixes.
		if strings.HasSuffix(rest, "/revisions") {
			// /api/etext/documents/{id}/revisions
			h.HandleEtextRevisions(w, r)
			return
		}
		if strings.HasSuffix(rest, "/agent-revision") {
			// /api/etext/documents/{id}/agent-revision
			h.HandleEtextAgentRevision(w, r)
			return
		}
		if strings.HasSuffix(rest, "/history") {
			// /api/etext/documents/{id}/history
			h.HandleEtextHistory(w, r)
			return
		}

		// Otherwise, it's a document item: /api/etext/documents/{id}
		h.HandleEtextDocument(w, r)
		return
	}

	writeAPIJSON(w, http.StatusNotFound, apiError{Error: "e-text endpoint not found"})
}

// HandleEtextDocumentsRoot routes POST to create and GET to list at
// /api/etext/documents (exact match, no trailing slash).
func (h *APIHandler) HandleEtextDocumentsRoot(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		h.HandleEtextCreateDocument(w, r)
	case http.MethodGet:
		h.HandleEtextListDocuments(w, r)
	default:
		writeAPIJSON(w, http.StatusMethodNotAllowed, apiError{Error: "method not allowed"})
	}
}
