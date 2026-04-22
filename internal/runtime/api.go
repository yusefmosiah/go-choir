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

// runSubmitRequest is the JSON payload for POST /api/agent/loop.
type runSubmitRequest struct {
	Prompt   string         `json:"prompt"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// spawnRequest is the JSON payload for POST /api/agent/spawn.
// It creates a child run linked to a parent, with an objective and optional
// constraints (VAL-CHOIR-001).
type spawnRequest struct {
	ParentID    string         `json:"parent_id"`
	Objective   string         `json:"objective"`
	Constraints map[string]any `json:"constraints,omitempty"`
}

// spawnResponse is the JSON response for POST /api/agent/spawn.
// It returns the child run handle with the parent linkage.
type spawnResponse struct {
	AgentID   string         `json:"agent_id"`
	RunID     string         `json:"loop_id"`
	ChannelID string         `json:"channel_id,omitempty"`
	ParentID  string         `json:"parent_id"`
	State     types.RunState `json:"state"`
	OwnerID   string         `json:"owner_id"`
	CreatedAt string         `json:"created_at"`
}

// cancelRequest is the JSON payload for POST /api/agent/cancel.
// It cancels a running or pending run (VAL-CHOIR-010).
type cancelRequest struct {
	RunID string `json:"loop_id"`
}

// cancelResponse is the JSON response for POST /api/agent/cancel.
type cancelResponse struct {
	RunID string         `json:"loop_id"`
	State types.RunState `json:"state"`
}

// runSubmitResponse is the JSON response for POST /api/agent/loop.
// It returns the stable run handle and initial lifecycle state
// (VAL-RUNTIME-003).
type runSubmitResponse struct {
	AgentID   string         `json:"agent_id"`
	RunID     string         `json:"loop_id"`
	ChannelID string         `json:"channel_id,omitempty"`
	State     types.RunState `json:"state"`
	OwnerID   string         `json:"owner_id"`
	CreatedAt string         `json:"created_at"`
}

// runStatusResponse is the JSON response for GET /api/agent/status.
// It returns the full run record correlated to the submitted handle
// (VAL-RUNTIME-004).
type runStatusResponse struct {
	AgentID      string         `json:"agent_id"`
	RunID        string         `json:"loop_id"`
	ChannelID    string         `json:"channel_id,omitempty"`
	ParentRunID  string         `json:"parent_loop_id,omitempty"`
	AgentProfile string         `json:"agent_profile,omitempty"`
	AgentRole    string         `json:"agent_role,omitempty"`
	OwnerID      string         `json:"owner_id"`
	SandboxID    string         `json:"sandbox_id"`
	State        types.RunState `json:"state"`
	Prompt       string         `json:"prompt"`
	Result       string         `json:"result,omitempty"`
	Error        string         `json:"error,omitempty"`
	CreatedAt    string         `json:"created_at"`
	UpdatedAt    string         `json:"updated_at"`
	FinishedAt   *string        `json:"finished_at,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
}

// runListResponse is the JSON response for GET /api/agent/loops.
// It returns recent runs owned by the authenticated user so debugging
// surfaces can group live events into runs and child delegations.
type runListResponse struct {
	Runs []runStatusResponse `json:"runs"`
}

// eventListResponse is the JSON response for GET /api/agent/events.
// It returns historical runtime events either for a specific run or for
// the authenticated owner across recent runs.
type eventListResponse struct {
	Events []types.EventRecord `json:"events"`
}

// channelMessageListResponse is the JSON response for GET /api/agent/channel-messages.
// It returns durable channel message bodies for a specific coordination channel.
type channelMessageListResponse struct {
	Messages []types.ChannelMessage `json:"messages"`
}

// runtimeHealthResponse is the JSON structure returned by GET /health.
// It reports runtime readiness for real run handling, and surfaces
// degraded state rather than hiding it behind a generic healthy response
// (VAL-RUNTIME-001). The active provider name is included so operators
// can distinguish real-provider paths from stub/canned paths.
type runtimeHealthResponse struct {
	Status          string                   `json:"status"`
	Service         string                   `json:"service"`
	SandboxID       string                   `json:"sandbox_id"`
	RuntimeHealth   types.RuntimeHealthState `json:"runtime_health"`
	RunningRuns     int                      `json:"running_runs"`
	ResearcherCount int                      `json:"researcher_count"`
	ActiveProvider  string                   `json:"active_provider"`
}

// runtimeTopologyResponse is the JSON structure returned by GET /api/agent/topology.
// It surfaces the configured orchestration shape so operators and UI surfaces
// can see how many researchers the microVM expects and what the current runtime
// fan-out looks like.
type runtimeTopologyResponse struct {
	SandboxID       string `json:"sandbox_id"`
	ResearcherCount int    `json:"researcher_count"`
	RunningRuns     int    `json:"running_runs"`
	ChannelCount    int    `json:"channel_count"`
	RuntimeHealth   string `json:"runtime_health"`
	ActiveProvider  string `json:"active_provider"`
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

// HandleRunSubmission handles POST /api/agent/loop.
// It accepts work only through the authenticated same-origin proxy path and
// denies missing or invalid auth before runtime work starts
// (VAL-RUNTIME-002). Returns a stable run handle (VAL-RUNTIME-003).
func (h *APIHandler) HandleRunSubmission(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIJSON(w, http.StatusMethodNotAllowed, apiError{Error: "method not allowed"})
		return
	}

	ownerID, err := authenticateUser(r)
	if err != nil {
		writeAPIJSON(w, http.StatusUnauthorized, apiError{Error: "authentication required"})
		return
	}

	var req runSubmitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIJSON(w, http.StatusBadRequest, apiError{Error: "invalid request body"})
		return
	}

	if strings.TrimSpace(req.Prompt) == "" {
		writeAPIJSON(w, http.StatusBadRequest, apiError{Error: "prompt is required"})
		return
	}
	if req.Metadata == nil {
		req.Metadata = make(map[string]any)
	}
	req.Metadata[runMetadataDesktopID] = requestDesktopID(r)

	rec, err := h.rt.StartRunWithMetadata(r.Context(), req.Prompt, ownerID, req.Metadata)
	if err != nil {
		log.Printf("runtime api: submit run: %v", err)
		writeAPIJSON(w, http.StatusInternalServerError, apiError{Error: "failed to submit run"})
		return
	}

	writeAPIJSON(w, http.StatusAccepted, runSubmitResponse{
		AgentID:   rec.AgentID,
		RunID:     rec.RunID,
		ChannelID: rec.ChannelID,
		State:     rec.State,
		OwnerID:   rec.OwnerID,
		CreatedAt: rec.CreatedAt.Format("2006-01-02T15:04:05.000Z"),
	})
}

// HandleSpawn handles POST /api/agent/spawn.
// It creates a child run linked to the given parent. The child run inherits
// the owner from the authenticated user context and begins in pending state.
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

	rec, err := h.rt.StartChildRun(r.Context(), req.ParentID, req.Objective, ownerID, req.Constraints)
	if err != nil {
		// Check if the parent run was not found.
		if strings.Contains(err.Error(), "parent run not found") {
			writeAPIJSON(w, http.StatusNotFound, apiError{Error: "parent run not found"})
			return
		}
		log.Printf("runtime api: start child run: %v", err)
		writeAPIJSON(w, http.StatusInternalServerError, apiError{Error: "failed to start child run"})
		return
	}

	writeAPIJSON(w, http.StatusAccepted, spawnResponse{
		AgentID:   rec.AgentID,
		RunID:     rec.RunID,
		ChannelID: rec.ChannelID,
		ParentID:  req.ParentID,
		State:     rec.State,
		OwnerID:   rec.OwnerID,
		CreatedAt: rec.CreatedAt.Format("2006-01-02T15:04:05.000Z"),
	})
}

// HandleCancel handles POST /api/agent/cancel.
// It cancels a running or pending run, transitioning it to cancelled state.
// The cancel endpoint is owner-scoped — a request for a run owned by a
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

	if strings.TrimSpace(req.RunID) == "" {
		writeAPIJSON(w, http.StatusBadRequest, apiError{Error: "loop_id is required"})
		return
	}

	err = h.rt.CancelRun(r.Context(), req.RunID, ownerID)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeAPIJSON(w, http.StatusNotFound, apiError{Error: "run not found"})
			return
		}
		if strings.Contains(err.Error(), "cannot cancel") {
			writeAPIJSON(w, http.StatusConflict, apiError{Error: err.Error()})
			return
		}
		log.Printf("runtime api: cancel run: %v", err)
		writeAPIJSON(w, http.StatusInternalServerError, apiError{Error: "failed to cancel run"})
		return
	}

	writeAPIJSON(w, http.StatusOK, cancelResponse{
		RunID: req.RunID,
		State: types.RunCancelled,
	})
}

// HandleRunStatus handles GET /api/agent/status.
// It is exposed through the authenticated same-origin proxy path, accepts or
// returns a stable correlation to the run handle from submission, and exposes
// machine-readable lifecycle state including non-happy-path outcomes
// (VAL-RUNTIME-004, VAL-RUNTIME-006).
func (h *APIHandler) HandleRunStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIJSON(w, http.StatusMethodNotAllowed, apiError{Error: "method not allowed"})
		return
	}

	ownerID, err := authenticateUser(r)
	if err != nil {
		writeAPIJSON(w, http.StatusUnauthorized, apiError{Error: "authentication required"})
		return
	}

	runID := r.URL.Query().Get("loop_id")
	if runID == "" {
		writeAPIJSON(w, http.StatusBadRequest, apiError{Error: "loop_id query parameter is required"})
		return
	}

	rec, err := h.rt.GetRun(r.Context(), runID, ownerID)
	if err != nil {
		// ErrNotFound covers both "run doesn't exist" and "run belongs to
		// another user" so callers cannot probe for other users' runs.
		writeAPIJSON(w, http.StatusNotFound, apiError{Error: "run not found"})
		return
	}

	var finishedAt *string
	if rec.FinishedAt != nil {
		s := rec.FinishedAt.Format("2006-01-02T15:04:05.000Z")
		finishedAt = &s
	}

	writeAPIJSON(w, http.StatusOK, runStatusResponse{
		AgentID:      rec.AgentID,
		RunID:        rec.RunID,
		ChannelID:    rec.ChannelID,
		ParentRunID:  rec.ParentRunID,
		AgentProfile: rec.AgentProfile,
		AgentRole:    rec.AgentRole,
		OwnerID:      rec.OwnerID,
		SandboxID:    rec.SandboxID,
		State:        rec.State,
		Prompt:       rec.Prompt,
		Result:       rec.Result,
		Error:        rec.Error,
		CreatedAt:    rec.CreatedAt.Format("2006-01-02T15:04:05.000Z"),
		UpdatedAt:    rec.UpdatedAt.Format("2006-01-02T15:04:05.000Z"),
		FinishedAt:   finishedAt,
		Metadata:     rec.Metadata,
	})
}

// HandleRunList handles GET /api/agent/loops.
// It returns recent owner-scoped runs in reverse chronological order so
// debugging and orchestration surfaces can inspect current work and run
// families without polling individual IDs one by one.
func (h *APIHandler) HandleRunList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIJSON(w, http.StatusMethodNotAllowed, apiError{Error: "method not allowed"})
		return
	}

	ownerID, err := authenticateUser(r)
	if err != nil {
		writeAPIJSON(w, http.StatusUnauthorized, apiError{Error: "authentication required"})
		return
	}

	limit := 100
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}

	channelID := strings.TrimSpace(r.URL.Query().Get("channel_id"))
	var runs []types.RunRecord
	if channelID != "" {
		runs, err = h.rt.Store().ListRunsByChannel(r.Context(), ownerID, channelID, limit)
	} else {
		runs, err = h.rt.ListRunsByOwner(r.Context(), ownerID, limit)
	}
	if err != nil {
		log.Printf("runtime api: list runs: %v", err)
		writeAPIJSON(w, http.StatusInternalServerError, apiError{Error: "failed to list runs"})
		return
	}

	resp := runListResponse{Runs: make([]runStatusResponse, 0, len(runs))}
	for _, rec := range runs {
		var finishedAt *string
		if rec.FinishedAt != nil {
			s := rec.FinishedAt.Format("2006-01-02T15:04:05.000Z")
			finishedAt = &s
		}
		resp.Runs = append(resp.Runs, runStatusResponse{
			AgentID:      rec.AgentID,
			RunID:        rec.RunID,
			ChannelID:    rec.ChannelID,
			ParentRunID:  rec.ParentRunID,
			AgentProfile: rec.AgentProfile,
			AgentRole:    rec.AgentRole,
			OwnerID:      rec.OwnerID,
			SandboxID:    rec.SandboxID,
			State:        rec.State,
			Prompt:       rec.Prompt,
			Result:       rec.Result,
			Error:        rec.Error,
			CreatedAt:    rec.CreatedAt.Format("2006-01-02T15:04:05.000Z"),
			UpdatedAt:    rec.UpdatedAt.Format("2006-01-02T15:04:05.000Z"),
			FinishedAt:   finishedAt,
			Metadata:     rec.Metadata,
		})
	}

	writeAPIJSON(w, http.StatusOK, resp)
}

// HandleEventList handles GET /api/agent/events.
// When loop_id is present, it returns historical events for that specific
// loop after verifying owner access. Otherwise it returns recent owner-scoped
// events across loops. This complements the live /api/events SSE feed.
func (h *APIHandler) HandleEventList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIJSON(w, http.StatusMethodNotAllowed, apiError{Error: "method not allowed"})
		return
	}

	ownerID, err := authenticateUser(r)
	if err != nil {
		writeAPIJSON(w, http.StatusUnauthorized, apiError{Error: "authentication required"})
		return
	}

	limit := 200
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}

	runID := strings.TrimSpace(r.URL.Query().Get("loop_id"))
	if runID != "" {
		if _, err := h.rt.GetRun(r.Context(), runID, ownerID); err != nil {
			writeAPIJSON(w, http.StatusNotFound, apiError{Error: "run not found"})
			return
		}
		events, err := h.rt.Store().ListEvents(r.Context(), runID, limit)
		if err != nil {
			log.Printf("runtime api: list run events: %v", err)
			writeAPIJSON(w, http.StatusInternalServerError, apiError{Error: "failed to list run events"})
			return
		}
		writeAPIJSON(w, http.StatusOK, eventListResponse{Events: events})
		return
	}
	channelID := strings.TrimSpace(r.URL.Query().Get("channel_id"))
	if channelID != "" {
		events, err := h.rt.Store().ListEventsByChannel(r.Context(), ownerID, channelID, limit)
		if err != nil {
			log.Printf("runtime api: list channel events: %v", err)
			writeAPIJSON(w, http.StatusInternalServerError, apiError{Error: "failed to list channel events"})
			return
		}
		writeAPIJSON(w, http.StatusOK, eventListResponse{Events: events})
		return
	}

	events, err := h.rt.Store().ListEventsByOwner(r.Context(), ownerID, limit)
	if err != nil {
		log.Printf("runtime api: list owner events: %v", err)
		writeAPIJSON(w, http.StatusInternalServerError, apiError{Error: "failed to list events"})
		return
	}
	writeAPIJSON(w, http.StatusOK, eventListResponse{Events: events})
}

// HandleChannelMessageList handles GET /api/agent/channel-messages.
// It returns persisted message bodies for a specific owner-scoped coordination channel.
func (h *APIHandler) HandleChannelMessageList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIJSON(w, http.StatusMethodNotAllowed, apiError{Error: "method not allowed"})
		return
	}

	ownerID, err := authenticateUser(r)
	if err != nil {
		writeAPIJSON(w, http.StatusUnauthorized, apiError{Error: "authentication required"})
		return
	}

	channelID := strings.TrimSpace(r.URL.Query().Get("channel_id"))
	if channelID == "" {
		writeAPIJSON(w, http.StatusBadRequest, apiError{Error: "channel_id is required"})
		return
	}

	limit := 200
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}

	afterSeq := int64(0)
	if raw := strings.TrimSpace(r.URL.Query().Get("after_seq")); raw != "" {
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil && n >= 0 {
			afterSeq = n
		}
	}

	messages, err := h.rt.Store().ListChannelMessages(r.Context(), ownerID, channelID, afterSeq, limit)
	if err != nil {
		log.Printf("runtime api: list channel messages: %v", err)
		writeAPIJSON(w, http.StatusInternalServerError, apiError{Error: "failed to list channel messages"})
		return
	}

	writeAPIJSON(w, http.StatusOK, channelMessageListResponse{Messages: messages})
}

// HandleRunStatusByID handles GET /api/agent/{id}/status.
// It returns the full run record for the run identified by the URL path
// parameter {id}. The response includes state, result (if complete), error
// (if failed), and timestamps (VAL-CHOIR-002, VAL-CHOIR-005).
// Access is scoped to the authenticated owner — a request for a run owned
// by a different user returns 404 to prevent IDOR probing. State updates
// are visible immediately after change.
func (h *APIHandler) HandleRunStatusByID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIJSON(w, http.StatusMethodNotAllowed, apiError{Error: "method not allowed"})
		return
	}

	ownerID, err := authenticateUser(r)
	if err != nil {
		writeAPIJSON(w, http.StatusUnauthorized, apiError{Error: "authentication required"})
		return
	}

	// Extract run ID from URL path: /api/agent/{id}/status
	// Expected prefix: /api/agent/  and suffix: /status
	path := r.URL.Path
	prefix := "/api/agent/"
	suffix := "/status"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		writeAPIJSON(w, http.StatusBadRequest, apiError{Error: "invalid status path"})
		return
	}
	runID := strings.TrimPrefix(path, prefix)
	runID = strings.TrimSuffix(runID, suffix)
	if runID == "" {
		writeAPIJSON(w, http.StatusBadRequest, apiError{Error: "run ID is required"})
		return
	}

	rec, err := h.rt.GetRun(r.Context(), runID, ownerID)
	if err != nil {
		// ErrNotFound covers both "run doesn't exist" and "run belongs to
		// another user" so callers cannot probe for other users' runs.
		writeAPIJSON(w, http.StatusNotFound, apiError{Error: "run not found"})
		return
	}

	var finishedAt *string
	if rec.FinishedAt != nil {
		s := rec.FinishedAt.Format("2006-01-02T15:04:05.000Z")
		finishedAt = &s
	}

	writeAPIJSON(w, http.StatusOK, runStatusResponse{
		AgentID:      rec.AgentID,
		RunID:        rec.RunID,
		ChannelID:    rec.ChannelID,
		ParentRunID:  rec.ParentRunID,
		AgentProfile: rec.AgentProfile,
		AgentRole:    rec.AgentRole,
		OwnerID:      rec.OwnerID,
		SandboxID:    rec.SandboxID,
		State:        rec.State,
		Prompt:       rec.Prompt,
		Result:       rec.Result,
		Error:        rec.Error,
		CreatedAt:    rec.CreatedAt.Format("2006-01-02T15:04:05.000Z"),
		UpdatedAt:    rec.UpdatedAt.Format("2006-01-02T15:04:05.000Z"),
		FinishedAt:   finishedAt,
		Metadata:     rec.Metadata,
	})
}

// HandleTopology handles GET /api/agent/topology.
// It exposes the runtime's orchestration shape for operator/UI inspection:
// researcher count, current running run count, and the number of active
// coordination channels.
func (h *APIHandler) HandleTopology(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIJSON(w, http.StatusMethodNotAllowed, apiError{Error: "method not allowed"})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(runtimeTopologyResponse{
		SandboxID:       h.rt.cfg.SandboxID,
		ResearcherCount: h.rt.cfg.ResearcherCount,
		RunningRuns:     h.rt.RunningCount(),
		ChannelCount:    len(h.rt.ChannelManager().ListChannels()),
		RuntimeHealth:   string(h.rt.HealthState()),
		ActiveProvider:  h.rt.provider.ProviderName(),
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
// It reports runtime readiness for real run handling, and surfaces degraded
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
		RunningRuns:     h.rt.RunningCount(),
		ResearcherCount: h.rt.cfg.ResearcherCount,
		ActiveProvider:  h.rt.provider.ProviderName(),
	})
}

// RegisterRoutes registers runtime API routes on the given server.
// The health handler overrides the default server health handler to
// report runtime readiness.
func RegisterRoutes(s *server.Server, h *APIHandler) {
	s.SetHealthHandler(h.HandleHealth)
	s.HandleFunc("/api/agent/loop", h.HandleRunSubmission)
	s.HandleFunc("/api/agent/loops", h.HandleRunList)
	s.HandleFunc("/api/agent/events", h.HandleEventList)
	s.HandleFunc("/api/agent/channel-messages", h.HandleChannelMessageList)
	s.HandleFunc("/api/agent/topology", h.HandleTopology)
	s.HandleFunc("/api/agent/spawn", h.HandleSpawn)
	s.HandleFunc("/api/agent/cancel", h.HandleCancel)
	s.HandleFunc("/api/agent/status", h.HandleRunStatus)
	s.HandleFunc("/api/agent/", h.HandleRunStatusByID) // matches /api/agent/{id}/status
	s.HandleFunc("/api/events", h.HandleEvents)
	s.HandleFunc("/api/desktop/state", h.HandleDesktopState)
	s.HandleFunc("/api/prompts", h.HandlePromptList)
	s.HandleFunc("/api/prompts/", h.HandlePromptRole)

	// VText document/revision/history/diff/blame APIs.
	// All routes are dispatched from a single prefix handler that inspects
	// the URL path and method to route to the correct handler. This avoids
	// ambiguity with Go's ServeMux prefix matching.
	RegisterVTextRoutes(s, h)
}

// RegisterVTextRoutes registers the vtext API routes on the given server.
// These routes expose document CRUD, revision, history, snapshot, diff,
// and blame APIs through the authenticated same-origin proxy path.
func RegisterVTextRoutes(s *server.Server, h *APIHandler) {
	// Exact match for document collection (create/list).
	s.HandleFunc("/api/vtext/documents", h.HandleVTextDocumentsRoot)

	// Prefix match for all other vtext routes.
	s.HandleFunc("/api/vtext/", h.HandleVTextRouter)
}

// HandleVTextRouter dispatches vtext API requests based on URL path and
// method. It handles all paths under /api/vtext/ that are not matched by
// the exact /api/vtext/documents route.
//
// Route mapping:
//
//	POST   /api/vtext/files/open               → resolve/create aliased file document
//	GET    /api/vtext/documents/{id}           → get document
//	PUT    /api/vtext/documents/{id}           → update document
//	DELETE /api/vtext/documents/{id}           → delete document
//	POST   /api/vtext/documents/{id}/revisions → create revision
//	GET    /api/vtext/documents/{id}/revisions → list revisions
//	GET    /api/vtext/documents/{id}/stream    → document-scoped stream
//	POST   /api/vtext/documents/{id}/agent-revision → submit agent revision
//	GET    /api/vtext/documents/{id}/history   → revision history
//	GET    /api/vtext/revisions/{id}          → get revision (snapshot)
//	GET    /api/vtext/revisions/{id}/blame     → blame revision
//	GET    /api/vtext/diff                     → diff two revisions
func (h *APIHandler) HandleVTextRouter(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	// Diff endpoint: /api/vtext/diff
	if path == "/api/vtext/diff" {
		h.HandleVTextDiff(w, r)
		return
	}
	if path == "/api/vtext/files/open" {
		h.HandleVTextOpenFile(w, r)
		return
	}

	// Revision item: /api/vtext/revisions/{id}
	if strings.HasPrefix(path, "/api/vtext/revisions/") {
		// Check for blame suffix: /api/vtext/revisions/{id}/blame
		if strings.HasSuffix(path, "/blame") {
			h.HandleVTextBlame(w, r)
			return
		}
		h.HandleVTextRevision(w, r)
		return
	}

	// Document sub-paths: /api/vtext/documents/{id}/...
	if strings.HasPrefix(path, "/api/vtext/documents/") {
		// Extract the part after /api/vtext/documents/
		rest := strings.TrimPrefix(path, "/api/vtext/documents/")

		// Check for sub-resource suffixes.
		if strings.HasSuffix(rest, "/revisions") {
			// /api/vtext/documents/{id}/revisions
			h.HandleVTextRevisions(w, r)
			return
		}
		if strings.HasSuffix(rest, "/stream") {
			// /api/vtext/documents/{id}/stream
			h.HandleVTextDocumentStream(w, r)
			return
		}
		if strings.HasSuffix(rest, "/agent-revision") {
			// /api/vtext/documents/{id}/agent-revision
			h.HandleVTextAgentRevision(w, r)
			return
		}
		if strings.HasSuffix(rest, "/history") {
			// /api/vtext/documents/{id}/history
			h.HandleVTextHistory(w, r)
			return
		}

		// Otherwise, it's a document item: /api/vtext/documents/{id}
		h.HandleVTextDocument(w, r)
		return
	}

	writeAPIJSON(w, http.StatusNotFound, apiError{Error: "vtext endpoint not found"})
}

// HandleVTextDocumentsRoot routes POST to create and GET to list at
// /api/vtext/documents (exact match, no trailing slash).
func (h *APIHandler) HandleVTextDocumentsRoot(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		h.HandleVTextCreateDocument(w, r)
	case http.MethodGet:
		h.HandleVTextListDocuments(w, r)
	default:
		writeAPIJSON(w, http.StatusMethodNotAllowed, apiError{Error: "method not allowed"})
	}
}
