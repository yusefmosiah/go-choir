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

// taskSubmitResponse is the JSON response for POST /api/agent/task.
// It returns the stable task handle and initial lifecycle state
// (VAL-RUNTIME-003).
type taskSubmitResponse struct {
	TaskID    string         `json:"task_id"`
	State     types.TaskState `json:"state"`
	OwnerID   string         `json:"owner_id"`
	CreatedAt string         `json:"created_at"`
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
	Status         string                  `json:"status"`
	Service        string                  `json:"service"`
	SandboxID      string                  `json:"sandbox_id"`
	RuntimeHealth  types.RuntimeHealthState `json:"runtime_health"`
	RunningTasks   int                     `json:"running_tasks"`
	ActiveProvider string                  `json:"active_provider"`
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

	rec, err := h.rt.SubmitTask(r.Context(), req.Prompt, ownerID)
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
			fmt.Fprintf(w, "data: %s\n\n", data)
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
		fmt.Fprintf(w, "data: %s\n\n", data)
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
	if health == types.HealthFailed {
		httpStatus = http.StatusServiceUnavailable
	} else if health == types.HealthDegraded {
		httpStatus = http.StatusOK // degraded is still serving, just observable
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(httpStatus)
	_ = json.NewEncoder(w).Encode(runtimeHealthResponse{
		Status:         string(health),
		Service:        "sandbox",
		SandboxID:      h.rt.cfg.SandboxID,
		RuntimeHealth:  health,
		RunningTasks:   h.rt.RunningCount(),
		ActiveProvider: h.rt.provider.ProviderName(),
	})
}

// RegisterRoutes registers runtime API routes on the given server.
// The health handler overrides the default server health handler to
// report runtime readiness.
func RegisterRoutes(s *server.Server, h *APIHandler) {
	s.SetHealthHandler(h.HandleHealth)
	s.HandleFunc("/api/agent/task", h.HandleTaskSubmission)
	s.HandleFunc("/api/agent/status", h.HandleTaskStatus)
	s.HandleFunc("/api/events", h.HandleEvents)
}
