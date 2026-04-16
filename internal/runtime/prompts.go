package runtime

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/yusefmosiah/go-choir/internal/types"
)

type promptDescriptorResponse struct {
	Role                  string                   `json:"role"`
	Content               string                   `json:"content"`
	Source                string                   `json:"source"`
	SourceLabel           string                   `json:"source_label,omitempty"`
	EffectiveSystemPrompt string                   `json:"effective_system_prompt,omitempty"`
	Tools                 []toolDescriptorResponse `json:"tools,omitempty"`
	RolePolicy            rolePolicyResponse       `json:"role_policy"`
	ProviderPolicy        ProviderPolicy           `json:"provider_policy"`
}

type promptListResponse struct {
	Prompts []promptDescriptorResponse `json:"prompts"`
}

type promptUpdateRequest struct {
	Content string `json:"content"`
}

type toolDescriptorResponse struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type rolePolicyResponse struct {
	Profile                string   `json:"profile"`
	AllowedDelegateTargets []string `json:"allowed_delegate_targets,omitempty"`
	AllowReadOnlyFiles     bool     `json:"allow_read_only_files"`
	AllowWritableFiles     bool     `json:"allow_writable_files"`
	AllowResearchTools     bool     `json:"allow_research_tools"`
	AllowEvidenceTools     bool     `json:"allow_evidence_tools"`
	AllowCodingTools       bool     `json:"allow_coding_tools"`
	AllowCoAgentTools      bool     `json:"allow_coagent_tools"`
}

func promptSourceLabel(source string) string {
	switch strings.TrimSpace(source) {
	case "user":
		return "Per-user override"
	case "default":
		return "Seeded default file"
	default:
		return strings.TrimSpace(source)
	}
}

func rolePolicyFromSpec(spec AgentRoleSpec) rolePolicyResponse {
	return rolePolicyResponse{
		Profile:                spec.Profile,
		AllowedDelegateTargets: append([]string(nil), spec.AllowedDelegateTargets...),
		AllowReadOnlyFiles:     spec.AllowReadOnlyFiles,
		AllowWritableFiles:     spec.AllowWritableFiles,
		AllowResearchTools:     spec.AllowResearchTools,
		AllowEvidenceTools:     spec.AllowEvidenceTools,
		AllowCodingTools:       spec.AllowCodingTools,
		AllowCoAgentTools:      spec.AllowCoAgentTools,
	}
}

func settingsPreviewRun(ownerID, role string) *types.RunRecord {
	rec := &types.RunRecord{
		RunID:        "settings-preview-run",
		AgentID:      "settings-preview-agent",
		ChannelID:    "<channel_id>",
		OwnerID:      ownerID,
		SandboxID:    "settings-preview",
		AgentProfile: role,
		AgentRole:    role,
		Metadata: map[string]any{
			runMetadataAgentProfile: role,
			runMetadataAgentRole:    role,
			runMetadataAgentID:      "settings-preview-agent",
			runMetadataChannelID:    "<channel_id>",
		},
	}
	switch role {
	case AgentProfileConductor:
		rec.Metadata["requested_app"] = "<requested_app>"
		rec.Metadata["seed_prompt"] = "<seed_prompt>"
	case AgentProfileVText:
		rec.AgentID = "vtext:<doc_id>"
		rec.ChannelID = "<doc_id>"
		rec.Metadata[runMetadataAgentID] = "vtext:<doc_id>"
		rec.Metadata[runMetadataChannelID] = "<doc_id>"
		rec.Metadata["doc_id"] = "<doc_id>"
	}
	return rec
}

func toolResponsesForRegistry(registry *ToolRegistry) []toolDescriptorResponse {
	if registry == nil {
		return nil
	}
	tools := registry.Tools()
	out := make([]toolDescriptorResponse, 0, len(tools))
	for _, tool := range tools {
		out = append(out, toolDescriptorResponse{
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  cloneSchemaMap(tool.Parameters),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out
}

func (h *APIHandler) promptResponse(ownerID string, prompt PromptDescriptor) (promptDescriptorResponse, error) {
	rec := settingsPreviewRun(ownerID, prompt.Role)
	systemPrompt, err := h.rt.systemPromptForRun(rec)
	if err != nil {
		return promptDescriptorResponse{}, err
	}
	registry := h.rt.ToolRegistryForProfile(prompt.Role)
	effective := buildSystemPromptWithTools(systemPrompt, registry)
	rolePolicy := rolePolicyFromSpec(roleSpec(prompt.Role))
	providerPolicy := providerPolicyForRuntime(h.rt.provider)
	if !rolePolicy.AllowCoAgentTools {
		providerPolicy.SupportsPerRunModelOverride = false
		providerPolicy.Notes = append(providerPolicy.Notes,
			"This role cannot spawn child agents, so it cannot request model overrides through spawn_agent.")
	}
	return promptDescriptorResponse{
		Role:                  prompt.Role,
		Content:               prompt.Content,
		Source:                prompt.Source,
		SourceLabel:           promptSourceLabel(prompt.Source),
		EffectiveSystemPrompt: effective,
		Tools:                 toolResponsesForRegistry(registry),
		RolePolicy:            rolePolicy,
		ProviderPolicy:        providerPolicy,
	}, nil
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
		item, err := h.promptResponse(ownerID, prompt)
		if err != nil {
			writeAPIJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
			return
		}
		resp.Prompts = append(resp.Prompts, item)
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
		resp, err := h.promptResponse(ownerID, prompt)
		if err != nil {
			writeAPIJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
			return
		}
		writeAPIJSON(w, http.StatusOK, resp)
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
		resp, err := h.promptResponse(ownerID, prompt)
		if err != nil {
			writeAPIJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
			return
		}
		writeAPIJSON(w, http.StatusOK, resp)
	case http.MethodDelete:
		prompt, err := h.rt.PromptStore().Reset(ownerID, role)
		if err != nil {
			writeAPIJSON(w, http.StatusBadRequest, apiError{Error: err.Error()})
			return
		}
		resp, err := h.promptResponse(ownerID, prompt)
		if err != nil {
			writeAPIJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
			return
		}
		writeAPIJSON(w, http.StatusOK, resp)
	default:
		writeAPIJSON(w, http.StatusMethodNotAllowed, apiError{Error: "method not allowed"})
	}
}
