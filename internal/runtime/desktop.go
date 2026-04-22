// Package runtime provides desktop state API handlers for the go-choir
// sandbox runtime. Desktop state is persisted server-side so that desktop
// restore works across fresh browser contexts for the same user
// (VAL-DESKTOP-007).
package runtime

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/yusefmosiah/go-choir/internal/types"
)

// desktopStateGetResponse is the JSON response for GET /api/desktop/state.
type desktopStateGetResponse struct {
	OwnerID        string               `json:"owner_id"`
	DesktopID      string               `json:"desktop_id"`
	Windows        []types.WindowState  `json:"windows"`
	ActiveWindowID string               `json:"active_window_id,omitempty"`
	UpdatedAt      string               `json:"updated_at"`
}

// desktopStateSaveRequest is the JSON payload for PUT /api/desktop/state.
type desktopStateSaveRequest struct {
	Windows        []types.WindowState `json:"windows"`
	ActiveWindowID string              `json:"active_window_id,omitempty"`
}

// desktopStateSaveResponse is the JSON response for PUT /api/desktop/state.
type desktopStateSaveResponse struct {
	OK        bool   `json:"ok"`
	DesktopID string `json:"desktop_id"`
	UpdatedAt string `json:"updated_at"`
}

func requestDesktopID(r *http.Request) string {
	if r == nil {
		return types.PrimaryDesktopID
	}
	if desktopID := strings.TrimSpace(r.URL.Query().Get("desktop_id")); desktopID != "" {
		return desktopID
	}
	if desktopID := strings.TrimSpace(r.Header.Get("X-Choir-Desktop")); desktopID != "" {
		return desktopID
	}
	return types.PrimaryDesktopID
}

// HandleDesktopStateGet handles GET /api/desktop/state.
// It returns the persisted desktop state for the authenticated user,
// including open windows, active window, geometry, and app context
// (VAL-DESKTOP-007). If no state exists, it returns an empty default state.
func (h *APIHandler) HandleDesktopStateGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIJSON(w, http.StatusMethodNotAllowed, apiError{Error: "method not allowed"})
		return
	}

	ownerID, err := authenticateUser(r)
	if err != nil {
		writeAPIJSON(w, http.StatusUnauthorized, apiError{Error: "authentication required"})
		return
	}
	desktopID := requestDesktopID(r)

	state, err := h.rt.Store().GetDesktopStateForDesktop(r.Context(), ownerID, desktopID)
	if err != nil {
		log.Printf("runtime api: get desktop state: %v", err)
		writeAPIJSON(w, http.StatusInternalServerError, apiError{Error: "failed to get desktop state"})
		return
	}

	writeAPIJSON(w, http.StatusOK, desktopStateGetResponse{
		OwnerID:        state.OwnerID,
		DesktopID:      state.DesktopID,
		Windows:        state.Windows,
		ActiveWindowID: state.ActiveWindowID,
		UpdatedAt:      state.UpdatedAt.Format("2006-01-02T15:04:05.000Z"),
	})
}

// HandleDesktopStateSave handles PUT /api/desktop/state.
// It persists the desktop state for the authenticated user, including
// window identities, geometry, mode, active window, and app context
// (VAL-DESKTOP-007). The state is stored server-side and survives
// fresh browser contexts.
func (h *APIHandler) HandleDesktopStateSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		writeAPIJSON(w, http.StatusMethodNotAllowed, apiError{Error: "method not allowed"})
		return
	}

	ownerID, err := authenticateUser(r)
	if err != nil {
		writeAPIJSON(w, http.StatusUnauthorized, apiError{Error: "authentication required"})
		return
	}
	desktopID := requestDesktopID(r)

	var req desktopStateSaveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIJSON(w, http.StatusBadRequest, apiError{Error: "invalid request body"})
		return
	}

	now := time.Now().UTC()

	state := types.DesktopState{
		OwnerID:        ownerID,
		DesktopID:      desktopID,
		Windows:        req.Windows,
		ActiveWindowID: req.ActiveWindowID,
		UpdatedAt:      now,
	}

	if err := h.rt.Store().SaveDesktopState(r.Context(), state); err != nil {
		log.Printf("runtime api: save desktop state: %v", err)
		writeAPIJSON(w, http.StatusInternalServerError, apiError{Error: "failed to save desktop state"})
		return
	}

	writeAPIJSON(w, http.StatusOK, desktopStateSaveResponse{
		OK:        true,
		DesktopID: desktopID,
		UpdatedAt: now.Format("2006-01-02T15:04:05.000Z"),
	})
}

// HandleDesktopState routes GET and PUT /api/desktop/state to the
// appropriate handler.
func (h *APIHandler) HandleDesktopState(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.HandleDesktopStateGet(w, r)
	case http.MethodPut:
		h.HandleDesktopStateSave(w, r)
	default:
		writeAPIJSON(w, http.StatusMethodNotAllowed, apiError{Error: "method not allowed"})
	}
}
