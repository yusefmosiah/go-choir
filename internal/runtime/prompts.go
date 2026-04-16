package runtime

import (
	"encoding/json"
	"net/http"
	"strings"
)

type promptDescriptorResponse struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	Source  string `json:"source"`
}

type promptListResponse struct {
	Prompts []promptDescriptorResponse `json:"prompts"`
}

type promptUpdateRequest struct {
	Content string `json:"content"`
}

func (h *APIHandler) HandlePromptList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIJSON(w, http.StatusMethodNotAllowed, apiError{Error: "method not allowed"})
		return
	}
	ownerID, err := authenticateUser(r)
	if err != nil {
		writeAPIJSON(w, http.StatusUnauthorized, apiError{Error: "authentication required"})
		return
	}
	prompts, err := h.rt.PromptStore().List(ownerID)
	if err != nil {
		writeAPIJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
		return
	}
	resp := promptListResponse{Prompts: make([]promptDescriptorResponse, 0, len(prompts))}
	for _, prompt := range prompts {
		resp.Prompts = append(resp.Prompts, promptDescriptorResponse{
			Role:    prompt.Role,
			Content: prompt.Content,
			Source:  prompt.Source,
		})
	}
	writeAPIJSON(w, http.StatusOK, resp)
}

func (h *APIHandler) HandlePromptRole(w http.ResponseWriter, r *http.Request) {
	ownerID, err := authenticateUser(r)
	if err != nil {
		writeAPIJSON(w, http.StatusUnauthorized, apiError{Error: "authentication required"})
		return
	}
	role := strings.TrimPrefix(r.URL.Path, "/api/prompts/")
	role = strings.TrimSpace(role)
	if role == "" || strings.Contains(role, "/") {
		writeAPIJSON(w, http.StatusNotFound, apiError{Error: "prompt role not found"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		prompt, err := h.rt.PromptStore().Load(ownerID, role)
		if err != nil {
			writeAPIJSON(w, http.StatusBadRequest, apiError{Error: err.Error()})
			return
		}
		writeAPIJSON(w, http.StatusOK, promptDescriptorResponse{
			Role:    prompt.Role,
			Content: prompt.Content,
			Source:  prompt.Source,
		})
	case http.MethodPut:
		var req promptUpdateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeAPIJSON(w, http.StatusBadRequest, apiError{Error: "invalid request body"})
			return
		}
		prompt, err := h.rt.PromptStore().Save(ownerID, role, req.Content)
		if err != nil {
			writeAPIJSON(w, http.StatusBadRequest, apiError{Error: err.Error()})
			return
		}
		writeAPIJSON(w, http.StatusOK, promptDescriptorResponse{
			Role:    prompt.Role,
			Content: prompt.Content,
			Source:  prompt.Source,
		})
	case http.MethodDelete:
		prompt, err := h.rt.PromptStore().Reset(ownerID, role)
		if err != nil {
			writeAPIJSON(w, http.StatusBadRequest, apiError{Error: err.Error()})
			return
		}
		writeAPIJSON(w, http.StatusOK, promptDescriptorResponse{
			Role:    prompt.Role,
			Content: prompt.Content,
			Source:  prompt.Source,
		})
	default:
		writeAPIJSON(w, http.StatusMethodNotAllowed, apiError{Error: "method not allowed"})
	}
}
