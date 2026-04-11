package vmctl

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"

	"github.com/yusefmosiah/go-choir/internal/server"
)

// errorResponse is a JSON error envelope.
type vmctlErrorResponse struct {
	Error string `json:"error"`
}

// vmctlHealthResponse is the JSON structure for GET /health.
type vmctlHealthResponse struct {
	Status       string `json:"status"`
	Service      string `json:"service"`
	ActiveVMs    int    `json:"active_vms"`
	TotalOwnerships int  `json:"total_ownerships"`
}

// resolveRequest is the JSON payload for POST /internal/vmctl/resolve.
type resolveRequest struct {
	UserID string `json:"user_id"`
}

// resolveResponse is the JSON response for POST /internal/vmctl/resolve.
type resolveResponse struct {
	VMID       string `json:"vm_id"`
	UserID     string `json:"user_id"`
	SandboxURL string `json:"sandbox_url"`
	State      string `json:"state"`
}

// ownershipResponse is the JSON response for ownership queries.
type ownershipResponse struct {
	VMID         string `json:"vm_id"`
	UserID       string `json:"user_id"`
	SandboxURL   string `json:"sandbox_url"`
	State        string `json:"state"`
	CreatedAt    string `json:"created_at"`
	LastActiveAt string `json:"last_active_at"`
	Epoch        int64  `json:"epoch"`
	StoppedBy    string `json:"stopped_by,omitempty"`
}

// Handler provides HTTP handlers for the vmctl service.
type Handler struct {
	registry *OwnershipRegistry
}

// NewHandler creates a vmctl Handler with the given ownership registry.
func NewHandler(registry *OwnershipRegistry) *Handler {
	return &Handler{registry: registry}
}

// writeJSON writes a JSON response.
func writeVMCTLJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("vmctl: json encode error: %v", err)
	}
}

// HandleHealth handles GET /health for the vmctl service.
func (h *Handler) HandleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeVMCTLJSON(w, http.StatusMethodNotAllowed, vmctlErrorResponse{Error: "method not allowed"})
		return
	}

	writeVMCTLJSON(w, http.StatusOK, vmctlHealthResponse{
		Status:          "ok",
		Service:         "vmctl",
		ActiveVMs:       h.registry.ActiveCount(),
		TotalOwnerships: len(h.registry.ListOwnerships()),
	})
}

// HandleResolve handles POST /internal/vmctl/resolve.
// Given a user ID, it resolves or assigns a VM for that user.
// This is the primary endpoint the proxy calls to route authenticated
// requests through VM ownership (VAL-VM-001).
//
// This endpoint is internal-only and must not be exposed publicly
// (VAL-VM-012). The proxy is the only intended caller.
func (h *Handler) HandleResolve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeVMCTLJSON(w, http.StatusMethodNotAllowed, vmctlErrorResponse{Error: "method not allowed"})
		return
	}

	// Enforce internal-only access (VAL-VM-012).
	if !isInternalCaller(r) {
		writeVMCTLJSON(w, http.StatusForbidden, vmctlErrorResponse{
			Error: "vmctl control endpoints are not publicly accessible",
		})
		return
	}

	var req resolveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeVMCTLJSON(w, http.StatusBadRequest, vmctlErrorResponse{Error: "invalid request body"})
		return
	}

	if req.UserID == "" {
		writeVMCTLJSON(w, http.StatusBadRequest, vmctlErrorResponse{Error: "user_id is required"})
		return
	}

	own, err := h.registry.ResolveOrAssign(req.UserID)
	if err != nil {
		log.Printf("vmctl: resolve failed for user %s: %v", req.UserID, err)
		writeVMCTLJSON(w, http.StatusInternalServerError, vmctlErrorResponse{Error: "failed to resolve VM"})
		return
	}

	writeVMCTLJSON(w, http.StatusOK, resolveResponse{
		VMID:       own.VMID,
		UserID:     own.UserID,
		SandboxURL: own.SandboxURL,
		State:      string(own.State),
	})
}

// HandleLookup handles GET /internal/vmctl/lookup?user_id=...
// Returns the current ownership for a user without creating a new VM.
func (h *Handler) HandleLookup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeVMCTLJSON(w, http.StatusMethodNotAllowed, vmctlErrorResponse{Error: "method not allowed"})
		return
	}

	if !isInternalCaller(r) {
		writeVMCTLJSON(w, http.StatusForbidden, vmctlErrorResponse{
			Error: "vmctl control endpoints are not publicly accessible",
		})
		return
	}

	userID := r.URL.Query().Get("user_id")
	if userID == "" {
		writeVMCTLJSON(w, http.StatusBadRequest, vmctlErrorResponse{Error: "user_id query parameter is required"})
		return
	}

	own := h.registry.GetOwnership(userID)
	if own == nil {
		writeVMCTLJSON(w, http.StatusNotFound, vmctlErrorResponse{Error: "no VM found for user"})
		return
	}

	writeVMCTLJSON(w, http.StatusOK, ownershipResponse{
		VMID:         own.VMID,
		UserID:       own.UserID,
		SandboxURL:   own.SandboxURL,
		State:        string(own.State),
		CreatedAt:    own.CreatedAt.Format("2006-01-02T15:04:05.000Z"),
		LastActiveAt: own.LastActiveAt.Format("2006-01-02T15:04:05.000Z"),
		Epoch:        own.Epoch,
		StoppedBy:    own.StoppedBy,
	})
}

// HandleStop handles POST /internal/vmctl/stop.
// Stops the VM for a given user.
func (h *Handler) HandleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeVMCTLJSON(w, http.StatusMethodNotAllowed, vmctlErrorResponse{Error: "method not allowed"})
		return
	}

	if !isInternalCaller(r) {
		writeVMCTLJSON(w, http.StatusForbidden, vmctlErrorResponse{
			Error: "vmctl control endpoints are not publicly accessible",
		})
		return
	}

	var req resolveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeVMCTLJSON(w, http.StatusBadRequest, vmctlErrorResponse{Error: "invalid request body"})
		return
	}

	if req.UserID == "" {
		writeVMCTLJSON(w, http.StatusBadRequest, vmctlErrorResponse{Error: "user_id is required"})
		return
	}

	if err := h.registry.StopVM(req.UserID); err != nil {
		writeVMCTLJSON(w, http.StatusNotFound, vmctlErrorResponse{Error: err.Error()})
		return
	}

	writeVMCTLJSON(w, http.StatusOK, map[string]string{"status": "stopped"})
}

// HandleRemove handles POST /internal/vmctl/remove.
// Removes the ownership for a user (used during logout).
func (h *Handler) HandleRemove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeVMCTLJSON(w, http.StatusMethodNotAllowed, vmctlErrorResponse{Error: "method not allowed"})
		return
	}

	if !isInternalCaller(r) {
		writeVMCTLJSON(w, http.StatusForbidden, vmctlErrorResponse{
			Error: "vmctl control endpoints are not publicly accessible",
		})
		return
	}

	var req resolveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeVMCTLJSON(w, http.StatusBadRequest, vmctlErrorResponse{Error: "invalid request body"})
		return
	}

	if req.UserID == "" {
		writeVMCTLJSON(w, http.StatusBadRequest, vmctlErrorResponse{Error: "user_id is required"})
		return
	}

	_ = h.registry.RemoveOwnership(req.UserID)
	writeVMCTLJSON(w, http.StatusOK, map[string]string{"status": "removed"})
}

// HandleHibernate handles POST /internal/vmctl/hibernate.
// Hibernates the VM for a given user, preserving persistent state
// for later resume (VAL-VM-008, VAL-CROSS-116).
func (h *Handler) HandleHibernate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeVMCTLJSON(w, http.StatusMethodNotAllowed, vmctlErrorResponse{Error: "method not allowed"})
		return
	}

	if !isInternalCaller(r) {
		writeVMCTLJSON(w, http.StatusForbidden, vmctlErrorResponse{
			Error: "vmctl control endpoints are not publicly accessible",
		})
		return
	}

	var req resolveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeVMCTLJSON(w, http.StatusBadRequest, vmctlErrorResponse{Error: "invalid request body"})
		return
	}

	if req.UserID == "" {
		writeVMCTLJSON(w, http.StatusBadRequest, vmctlErrorResponse{Error: "user_id is required"})
		return
	}

	if err := h.registry.HibernateVM(req.UserID); err != nil {
		writeVMCTLJSON(w, http.StatusNotFound, vmctlErrorResponse{Error: err.Error()})
		return
	}

	own := h.registry.GetOwnership(req.UserID)
	writeVMCTLJSON(w, http.StatusOK, map[string]interface{}{
		"status": "hibernated",
		"vm_id":  own.VMID,
		"epoch":  own.Epoch,
	})
}

// HandleResume handles POST /internal/vmctl/resume.
// Resumes a stopped or hibernated VM for a user, restoring the
// same user's persisted state (VAL-CROSS-116).
// The epoch does NOT increment on resume (VAL-CROSS-117).
func (h *Handler) HandleResume(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeVMCTLJSON(w, http.StatusMethodNotAllowed, vmctlErrorResponse{Error: "method not allowed"})
		return
	}

	if !isInternalCaller(r) {
		writeVMCTLJSON(w, http.StatusForbidden, vmctlErrorResponse{
			Error: "vmctl control endpoints are not publicly accessible",
		})
		return
	}

	var req resolveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeVMCTLJSON(w, http.StatusBadRequest, vmctlErrorResponse{Error: "invalid request body"})
		return
	}

	if req.UserID == "" {
		writeVMCTLJSON(w, http.StatusBadRequest, vmctlErrorResponse{Error: "user_id is required"})
		return
	}

	own, err := h.registry.ResumeVM(req.UserID)
	if err != nil {
		writeVMCTLJSON(w, http.StatusNotFound, vmctlErrorResponse{Error: err.Error()})
		return
	}

	writeVMCTLJSON(w, http.StatusOK, resolveResponse{
		VMID:       own.VMID,
		UserID:     own.UserID,
		SandboxURL: own.SandboxURL,
		State:      string(own.State),
	})
}

// HandleRecover handles POST /internal/vmctl/recover.
// Recovers an unhealthy or failed VM by creating a fresh boot with
// a new epoch (VAL-VM-009, VAL-CROSS-117).
func (h *Handler) HandleRecover(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeVMCTLJSON(w, http.StatusMethodNotAllowed, vmctlErrorResponse{Error: "method not allowed"})
		return
	}

	if !isInternalCaller(r) {
		writeVMCTLJSON(w, http.StatusForbidden, vmctlErrorResponse{
			Error: "vmctl control endpoints are not publicly accessible",
		})
		return
	}

	var req resolveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeVMCTLJSON(w, http.StatusBadRequest, vmctlErrorResponse{Error: "invalid request body"})
		return
	}

	if req.UserID == "" {
		writeVMCTLJSON(w, http.StatusBadRequest, vmctlErrorResponse{Error: "user_id is required"})
		return
	}

	own, err := h.registry.RecoverVM(req.UserID)
	if err != nil {
		writeVMCTLJSON(w, http.StatusNotFound, vmctlErrorResponse{Error: err.Error()})
		return
	}

	writeVMCTLJSON(w, http.StatusOK, resolveResponse{
		VMID:       own.VMID,
		UserID:     own.UserID,
		SandboxURL: own.SandboxURL,
		State:      string(own.State),
	})
}

// HandleLogout handles POST /internal/vmctl/logout.
// Transitions only the current user's VM to stopped state on logout
// (VAL-VM-008). Other users' VMs are not affected.
func (h *Handler) HandleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeVMCTLJSON(w, http.StatusMethodNotAllowed, vmctlErrorResponse{Error: "method not allowed"})
		return
	}

	if !isInternalCaller(r) {
		writeVMCTLJSON(w, http.StatusForbidden, vmctlErrorResponse{
			Error: "vmctl control endpoints are not publicly accessible",
		})
		return
	}

	var req resolveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeVMCTLJSON(w, http.StatusBadRequest, vmctlErrorResponse{Error: "invalid request body"})
		return
	}

	if req.UserID == "" {
		writeVMCTLJSON(w, http.StatusBadRequest, vmctlErrorResponse{Error: "user_id is required"})
		return
	}

	_ = h.registry.LogoutVM(req.UserID)
	writeVMCTLJSON(w, http.StatusOK, map[string]string{"status": "stopped", "reason": "logout"})
}

// HandleIdleCheck handles POST /internal/vmctl/idle-check.
// Triggers an idle VM sweep, stopping or hibernating VMs that have
// exceeded the idle timeout (VAL-VM-008).
func (h *Handler) HandleIdleCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeVMCTLJSON(w, http.StatusMethodNotAllowed, vmctlErrorResponse{Error: "method not allowed"})
		return
	}

	if !isInternalCaller(r) {
		writeVMCTLJSON(w, http.StatusForbidden, vmctlErrorResponse{
			Error: "vmctl control endpoints are not publicly accessible",
		})
		return
	}

	stopped := h.registry.StopIdleVMs()
	writeVMCTLJSON(w, http.StatusOK, map[string]interface{}{
		"status":             "ok",
		"vms_stopped":        stopped,
	})
}

// HandleList handles GET /internal/vmctl/list.
// Lists all current ownerships (operator visibility).
func (h *Handler) HandleList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeVMCTLJSON(w, http.StatusMethodNotAllowed, vmctlErrorResponse{Error: "method not allowed"})
		return
	}

	if !isInternalCaller(r) {
		writeVMCTLJSON(w, http.StatusForbidden, vmctlErrorResponse{
			Error: "vmctl control endpoints are not publicly accessible",
		})
		return
	}

	ownerships := h.registry.ListOwnerships()
	result := make([]ownershipResponse, 0, len(ownerships))
	for _, own := range ownerships {
		result = append(result, ownershipResponse{
			VMID:         own.VMID,
			UserID:       own.UserID,
			SandboxURL:   own.SandboxURL,
			State:        string(own.State),
			CreatedAt:    own.CreatedAt.Format("2006-01-02T15:04:05.000Z"),
			LastActiveAt: own.LastActiveAt.Format("2006-01-02T15:04:05.000Z"),
			Epoch:        own.Epoch,
			StoppedBy:    own.StoppedBy,
		})
	}

	writeVMCTLJSON(w, http.StatusOK, map[string]interface{}{
		"ownerships": result,
		"count":      len(result),
	})
}

// isInternalCaller checks whether the request originated from an internal
// caller (localhost or internal service). vmctl control endpoints must only
// be reachable from internal host/service paths (VAL-VM-012).
func isInternalCaller(r *http.Request) bool {
	internal := map[string]bool{
		"localhost": true,
		"127.0.0.1": true,
		"::1":       true,
	}

	// Check if the request has the internal service header.
	// This allows service-to-service calls where the request
	// comes through a loopback connection.
	if r.Header.Get("X-Internal-Caller") == "true" {
		return true
	}

	// Extract host from Host header, handling both host:port and [ipv6]:port.
	if host, _, err := net.SplitHostPort(r.Host); err == nil {
		if internal[host] {
			return true
		}
	} else {
		// No port in Host, check directly.
		if internal[r.Host] {
			return true
		}
	}

	// Check RemoteAddr for loopback connections.
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		if internal[host] {
			return true
		}
	} else {
		if internal[r.RemoteAddr] {
			return true
		}
	}

	return false
}

// RegisterRoutes registers all vmctl routes on the given server.
// All control endpoints use the /internal/vmctl/ prefix to make it
// clear these are not public-facing routes (VAL-VM-012).
func RegisterRoutes(s *server.Server, h *Handler) {
	s.SetHealthHandler(h.HandleHealth)
	s.HandleFunc("/internal/vmctl/resolve", h.HandleResolve)
	s.HandleFunc("/internal/vmctl/lookup", h.HandleLookup)
	s.HandleFunc("/internal/vmctl/stop", h.HandleStop)
	s.HandleFunc("/internal/vmctl/remove", h.HandleRemove)
	s.HandleFunc("/internal/vmctl/list", h.HandleList)
	s.HandleFunc("/internal/vmctl/hibernate", h.HandleHibernate)
	s.HandleFunc("/internal/vmctl/resume", h.HandleResume)
	s.HandleFunc("/internal/vmctl/recover", h.HandleRecover)
	s.HandleFunc("/internal/vmctl/logout", h.HandleLogout)
	s.HandleFunc("/internal/vmctl/idle-check", h.HandleIdleCheck)
}

// ResolveEndpoint returns the full resolve endpoint URL for the vmctl
// service at the given base URL.
func ResolveEndpoint(baseURL string) string {
	return baseURL + "/internal/vmctl/resolve"
}

// LookupEndpoint returns the full lookup endpoint URL for the vmctl
// service at the given base URL.
func LookupEndpoint(baseURL string) string {
	return baseURL + "/internal/vmctl/lookup"
}

// StopEndpoint returns the full stop endpoint URL for the vmctl
// service at the given base URL.
func StopEndpoint(baseURL string) string {
	return fmt.Sprintf("%s/internal/vmctl/stop", baseURL)
}

// RemoveEndpoint returns the full remove endpoint URL for the vmctl
// service at the given base URL.
func RemoveEndpoint(baseURL string) string {
	return fmt.Sprintf("%s/internal/vmctl/remove", baseURL)
}

// HibernateEndpoint returns the full hibernate endpoint URL for the vmctl
// service at the given base URL.
func HibernateEndpoint(baseURL string) string {
	return fmt.Sprintf("%s/internal/vmctl/hibernate", baseURL)
}

// ResumeEndpoint returns the full resume endpoint URL for the vmctl
// service at the given base URL.
func ResumeEndpoint(baseURL string) string {
	return fmt.Sprintf("%s/internal/vmctl/resume", baseURL)
}

// RecoverEndpoint returns the full recover endpoint URL for the vmctl
// service at the given base URL.
func RecoverEndpoint(baseURL string) string {
	return fmt.Sprintf("%s/internal/vmctl/recover", baseURL)
}

// LogoutEndpoint returns the full logout endpoint URL for the vmctl
// service at the given base URL.
func LogoutEndpoint(baseURL string) string {
	return fmt.Sprintf("%s/internal/vmctl/logout", baseURL)
}

// IdleCheckEndpoint returns the full idle-check endpoint URL for the vmctl
// service at the given base URL.
func IdleCheckEndpoint(baseURL string) string {
	return fmt.Sprintf("%s/internal/vmctl/idle-check", baseURL)
}
