package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/yusefmosiah/go-choir/internal/provider"
	"github.com/yusefmosiah/go-choir/internal/server"
)

// HealthResponse is the JSON structure returned by GET /health.
type gatewayHealthResponse struct {
	Status         string `json:"status"`
	Service        string `json:"service"`
	Provider       string `json:"provider"`
	ActiveIdentities int   `json:"active_identities"`
}

// ProviderRequest is the JSON payload for POST /provider/v1/inference.
// Sandbox callers send this to request an LLM inference call through
// the gateway, which injects host-side credentials before forwarding
// to the upstream provider (VAL-GATEWAY-004).
type ProviderRequest struct {
	// Provider is the requested provider ("bedrock" or "zai").
	// If empty, the gateway uses the default resolved provider.
	Provider string `json:"provider,omitempty"`

	// Model is an optional model override.
	Model string `json:"model,omitempty"`

	// Messages is the conversation history in Anthropic Messages format.
	Messages []provider.Message `json:"messages"`

	// System is the optional system prompt.
	System string `json:"system,omitempty"`

	// Tools is the list of tool definitions.
	Tools []provider.ToolDef `json:"tools,omitempty"`

	// MaxTokens is the maximum output tokens.
	MaxTokens int `json:"max_tokens,omitempty"`

	// Stream controls streaming. The gateway forces this to false for
	// now because it proxies non-streaming calls.
	Stream bool `json:"stream,omitempty"`
}

// ProviderResponse is the JSON response for successful inference calls.
type ProviderResponse struct {
	// ID is the provider-assigned response ID.
	ID string `json:"id"`

	// Text is the concatenated text content.
	Text string `json:"text"`

	// Model is the model that produced the response.
	Model string `json:"model"`

	// StopReason is why generation stopped.
	StopReason string `json:"stop_reason"`

	// Usage contains token counts.
	Usage provider.Usage `json:"usage"`

	// ToolCalls contains any tool invocation requests.
	ToolCalls []provider.ContentToolCall `json:"tool_calls,omitempty"`

	// ProviderName identifies which provider handled this request.
	ProviderName string `json:"provider_name"`
}

// ErrorResponse is the JSON error envelope returned by the gateway.
// Errors are sanitized and never contain credentials, raw auth headers,
// or upstream response bodies (VAL-GATEWAY-007).
type ErrorResponse struct {
	Error string `json:"error"`
}

// Handler provides HTTP handlers for the gateway service.
type Handler struct {
	registry *IdentityRegistry
	provider provider.Provider // the resolved real provider (bedrock or zai)
}

// NewHandler creates a gateway Handler with the given identity registry
// and provider. The provider may be nil if no real provider is configured;
// in that case, inference requests will fail with a clear error.
func NewHandler(registry *IdentityRegistry, p provider.Provider) *Handler {
	return &Handler{
		registry: registry,
		provider: p,
	}
}

// HandleHealth handles GET /health for the gateway service.
// It reports the active provider and identity count.
func (h *Handler) HandleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeGatewayJSON(w, http.StatusMethodNotAllowed, ErrorResponse{Error: "method not allowed"})
		return
	}

	providerName := "none"
	if h.provider != nil {
		providerName = h.provider.Name()
	}

	activeCount := 0
	for _, id := range h.registry.identities {
		if id.Active {
			activeCount++
		}
	}

	writeGatewayJSON(w, http.StatusOK, gatewayHealthResponse{
		Status:         "ok",
		Service:        "gateway",
		Provider:       providerName,
		ActiveIdentities: activeCount,
	})
}

// HandleInference handles POST /provider/v1/inference.
// It authenticates the sandbox caller via Bearer token, injects host-side
// provider credentials, calls the upstream provider, and returns the
// response with sanitized errors (VAL-GATEWAY-001, VAL-GATEWAY-003,
// VAL-GATEWAY-004, VAL-GATEWAY-007).
func (h *Handler) HandleInference(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeGatewayJSON(w, http.StatusMethodNotAllowed, ErrorResponse{Error: "method not allowed"})
		return
	}

	// Step 1: Authenticate the sandbox caller.
	sandboxID, err := h.authenticateSandbox(r)
	if err != nil {
		log.Printf("gateway: authentication denied: %v", err)
		writeGatewayJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "authentication required"})
		return
	}

	// Step 2: Check that a real provider is configured.
	if h.provider == nil {
		log.Printf("gateway: no provider configured for request from sandbox %s", sandboxID)
		writeGatewayJSON(w, http.StatusServiceUnavailable, ErrorResponse{
			Error: "no provider configured",
		})
		return
	}

	// Step 3: Decode the request.
	var req ProviderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeGatewayJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid request body"})
		return
	}

	// Step 4: Reject unsupported providers (VAL-GATEWAY-006).
	if req.Provider != "" && req.Provider != h.provider.Name() {
		log.Printf("gateway: unsupported provider %q requested (have %s)", req.Provider, h.provider.Name())
		writeGatewayJSON(w, http.StatusBadRequest, ErrorResponse{
			Error: fmt.Sprintf("unsupported provider: %s", req.Provider),
		})
		return
	}

	// Step 5: Build the LLM request and call the provider.
	llmReq := provider.LLMRequest{
		Model:     req.Model,
		System:    req.System,
		Messages:  req.Messages,
		Tools:     req.Tools,
		MaxTokens: req.MaxTokens,
		Stream:    false, // gateway forces non-streaming
	}

	log.Printf("gateway: inference request from sandbox %s (provider=%s messages=%d)",
		sandboxID, h.provider.Name(), len(req.Messages))

	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()

	resp, err := h.provider.Call(ctx, llmReq)
	if err != nil {
		// Sanitize the error: never include raw upstream bodies,
		// credentials, or auth headers in the response
		// (VAL-GATEWAY-007).
		sanitized := sanitizeError(err)
		log.Printf("gateway: provider call failed for sandbox %s: %v (sanitized: %s)",
			sandboxID, err, sanitized)
		writeGatewayJSON(w, http.StatusBadGateway, ErrorResponse{Error: sanitized})
		return
	}

	// Step 6: Return the successful response.
	log.Printf("gateway: inference succeeded for sandbox %s (provider=%s tokens=%d+%d text_len=%d)",
		sandboxID, resp.ProviderName, resp.Usage.InputTokens, resp.Usage.OutputTokens, len(resp.Text))

	writeGatewayJSON(w, http.StatusOK, ProviderResponse{
		ID:           resp.ID,
		Text:         resp.Text,
		Model:        resp.Model,
		StopReason:   resp.StopReason,
		Usage:        resp.Usage,
		ToolCalls:    resp.ToolCalls,
		ProviderName: resp.ProviderName,
	})
}

// HandleIssueCredential handles POST /provider/v1/credentials/issue.
// This is an internal operator endpoint for issuing sandbox credentials.
// It is only accessible from localhost (VAL-GATEWAY-004).
func (h *Handler) HandleIssueCredential(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeGatewayJSON(w, http.StatusMethodNotAllowed, ErrorResponse{Error: "method not allowed"})
		return
	}

	// Only allow localhost access for credential issuance.
	if !isLocalhost(r) {
		writeGatewayJSON(w, http.StatusForbidden, ErrorResponse{Error: "access denied"})
		return
	}

	var req struct {
		SandboxID string `json:"sandbox_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeGatewayJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid request body"})
		return
	}
	if req.SandboxID == "" {
		writeGatewayJSON(w, http.StatusBadRequest, ErrorResponse{Error: "sandbox_id is required"})
		return
	}

	result, err := h.registry.IssueCredential(req.SandboxID)
	if err != nil {
		writeGatewayJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to issue credential"})
		return
	}

	log.Printf("gateway: issued credential for sandbox %s (expires=%s)", req.SandboxID, result.ExpiresAt)

	writeGatewayJSON(w, http.StatusCreated, result)
}

// HandleRevokeCredential handles POST /provider/v1/credentials/revoke.
// This is an internal operator endpoint for revoking sandbox credentials
// after lifecycle changes (VAL-GATEWAY-008).
func (h *Handler) HandleRevokeCredential(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeGatewayJSON(w, http.StatusMethodNotAllowed, ErrorResponse{Error: "method not allowed"})
		return
	}

	if !isLocalhost(r) {
		writeGatewayJSON(w, http.StatusForbidden, ErrorResponse{Error: "access denied"})
		return
	}

	var req struct {
		SandboxID string `json:"sandbox_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeGatewayJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid request body"})
		return
	}
	if req.SandboxID == "" {
		writeGatewayJSON(w, http.StatusBadRequest, ErrorResponse{Error: "sandbox_id is required"})
		return
	}

	h.registry.RevokeCredential(req.SandboxID)
	log.Printf("gateway: revoked credential for sandbox %s", req.SandboxID)

	writeGatewayJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

// HandleRotateCredential handles POST /provider/v1/credentials/rotate.
// This is an internal operator endpoint for rotating sandbox credentials.
func (h *Handler) HandleRotateCredential(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeGatewayJSON(w, http.StatusMethodNotAllowed, ErrorResponse{Error: "method not allowed"})
		return
	}

	if !isLocalhost(r) {
		writeGatewayJSON(w, http.StatusForbidden, ErrorResponse{Error: "access denied"})
		return
	}

	var req struct {
		SandboxID string `json:"sandbox_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeGatewayJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid request body"})
		return
	}
	if req.SandboxID == "" {
		writeGatewayJSON(w, http.StatusBadRequest, ErrorResponse{Error: "sandbox_id is required"})
		return
	}

	result, err := h.registry.RotateCredential(req.SandboxID)
	if err != nil {
		writeGatewayJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to rotate credential"})
		return
	}

	log.Printf("gateway: rotated credential for sandbox %s (expires=%s)", req.SandboxID, result.ExpiresAt)

	writeGatewayJSON(w, http.StatusOK, result)
}

// authenticateSandbox validates the Bearer token from the Authorization
// header against the identity registry. Returns the sandbox ID if valid
// (VAL-GATEWAY-003).
func (h *Handler) authenticateSandbox(r *http.Request) (string, error) {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return "", fmt.Errorf("missing authorization header")
	}

	if !strings.HasPrefix(auth, "Bearer ") {
		return "", fmt.Errorf("invalid authorization scheme")
	}

	token := strings.TrimPrefix(auth, "Bearer ")
	if token == "" {
		return "", fmt.Errorf("empty bearer token")
	}

	return h.registry.ValidateCredential(token)
}

// sanitizeError converts a provider error into a safe, bounded error
// message that does not leak credentials, raw auth headers, or stack
// traces (VAL-GATEWAY-007).
func sanitizeError(err error) string {
	msg := err.Error()

	// The provider package already returns sanitized errors
	// (e.g., "bedrock: status 503 (sanitized)"), but we add an
	// additional layer of protection here.

	// Strip any accidental credential-like patterns.
	// This is defense-in-depth; the provider layer should already
	// sanitize.
	for _, pattern := range []string{"Bearer ", "x-api-key ", "Authorization:"} {
		if idx := strings.Index(msg, pattern); idx >= 0 {
			// Truncate at the first sign of credential leakage.
			msg = msg[:idx] + "(redacted)"
		}
	}

	// Bound the error message length.
	if len(msg) > 500 {
		msg = msg[:497] + "..."
	}

	return msg
}

// isLocalhost checks whether the request originated from localhost.
func isLocalhost(r *http.Request) bool {
	host := r.Host
	if strings.Contains(host, ":") {
		host = strings.Split(host, ":")[0]
	}
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// writeGatewayJSON writes a JSON response.
func writeGatewayJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("gateway: json encode error: %v", err)
	}
}

// drainBody reads and discards the response body to allow connection reuse.
func drainBody(resp *http.Response) {
	if resp != nil && resp.Body != nil {
		_, _ = io.Copy(io.Discard, resp.Body)
	}
}

// RegisterRoutes registers all gateway routes on the given server.
func RegisterRoutes(s *server.Server, h *Handler) {
	s.SetHealthHandler(h.HandleHealth)
	s.HandleFunc("/provider/v1/inference", h.HandleInference)
	s.HandleFunc("/provider/v1/credentials/issue", h.HandleIssueCredential)
	s.HandleFunc("/provider/v1/credentials/revoke", h.HandleRevokeCredential)
	s.HandleFunc("/provider/v1/credentials/rotate", h.HandleRotateCredential)
}
