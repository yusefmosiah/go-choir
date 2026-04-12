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
	Status               string   `json:"status"`
	Service              string   `json:"service"`
	Provider             string   `json:"provider"`
	ActiveIdentities     int      `json:"active_identities"`
	RateLimitMaxRequests int      `json:"rate_limit_max_requests,omitempty"`
	RateLimitWindowMs    int64    `json:"rate_limit_window_ms,omitempty"`
	SearchProviders      []string `json:"search_providers,omitempty"`
}

// ProviderRequest is the JSON payload for POST /provider/v1/inference.
// Sandbox callers send this to request an LLM inference call through
// the gateway, which injects host-side credentials before forwarding
// to the upstream provider (VAL-GATEWAY-004).
type ProviderRequest struct {
	// Provider is the requested provider ("bedrock", "zai", or "fireworks").
	// If empty, the gateway uses model-based routing or the default provider.
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
	registry     *IdentityRegistry
	provider     provider.Provider       // single provider mode (backward compat)
	providers    *provider.MultiProvider // multi-provider mode (may be nil)
	rateLimiter  *PerSandboxRateLimiter  // per-sandbox rate limiter (may be nil)
	searchClient *SearchClient           // web search client with rotation (may be nil)
}

// NewHandler creates a gateway Handler with the given identity registry
// and a single provider. The provider may be nil if no real provider is
// configured; in that case, inference requests will fail with a clear error.
// No rate limiting is applied when using this constructor.
func NewHandler(registry *IdentityRegistry, p provider.Provider) *Handler {
	return &Handler{
		registry:     registry,
		provider:     p,
		providers:    nil,
		rateLimiter:  nil,
		searchClient: NewSearchClient(),
	}
}

// NewHandlerWithRateLimit creates a gateway Handler with per-sandbox rate
// limiting and a single provider. Each sandbox identity gets an independent
// request quota so one noisy sandbox cannot starve another
// (VAL-GATEWAY-005, VAL-CROSS-115).
func NewHandlerWithRateLimit(registry *IdentityRegistry, p provider.Provider, rl *PerSandboxRateLimiter) *Handler {
	return &Handler{
		registry:     registry,
		provider:     p,
		providers:    nil,
		rateLimiter:  rl,
		searchClient: NewSearchClient(),
	}
}

// NewMultiHandler creates a gateway Handler with multi-provider routing.
// The gateway selects the appropriate provider based on the request's
// provider field or model parameter (VAL-LLM-001, VAL-LLM-005).
func NewMultiHandler(registry *IdentityRegistry, mp *provider.MultiProvider) *Handler {
	return &Handler{
		registry:     registry,
		provider:     nil,
		providers:    mp,
		rateLimiter:  nil,
		searchClient: NewSearchClient(),
	}
}

// NewMultiHandlerWithRateLimit creates a gateway Handler with multi-provider
// routing and per-sandbox rate limiting.
func NewMultiHandlerWithRateLimit(registry *IdentityRegistry, mp *provider.MultiProvider, rl *PerSandboxRateLimiter) *Handler {
	return &Handler{
		registry:     registry,
		provider:     nil,
		providers:    mp,
		rateLimiter:  rl,
		searchClient: NewSearchClient(),
	}
}

// HandleHealth handles GET /health for the gateway service.
// It reports the active provider(s), identity count, and rate limiter config.
func (h *Handler) HandleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeGatewayJSON(w, http.StatusMethodNotAllowed, ErrorResponse{Error: "method not allowed"})
		return
	}

	providerName := "none"
	if h.providers != nil {
		// Multi-provider mode: report all registered providers.
		names := h.providers.Names()
		if len(names) > 0 {
			providerName = strings.Join(names, ",")
		}
	} else if h.provider != nil {
		providerName = h.provider.Name()
	}

	activeCount := 0
	for _, id := range h.registry.identities {
		if id.Active {
			activeCount++
		}
	}

	resp := gatewayHealthResponse{
		Status:           "ok",
		Service:          "gateway",
		Provider:         providerName,
		ActiveIdentities: activeCount,
	}

	if h.rateLimiter != nil {
		used, limit, resetIn := h.rateLimiter.Status("__health_check__")
		_ = used
		_ = resetIn
		resp.RateLimitMaxRequests = limit
		resp.RateLimitWindowMs = h.rateLimiter.window.Milliseconds()
	}

	if h.searchClient != nil {
		resp.SearchProviders = h.searchClient.AvailableProviders()
	}

	writeGatewayJSON(w, http.StatusOK, resp)
}

// HandleInference handles POST /provider/v1/inference.
// It authenticates the sandbox caller via Bearer token, resolves the
// appropriate provider (multi-provider routing or single-provider
// fallback), injects host-side provider credentials, calls the upstream
// provider, and returns the response with sanitized errors
// (VAL-GATEWAY-001, VAL-GATEWAY-003, VAL-GATEWAY-004, VAL-GATEWAY-007).
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

	// Step 2: Enforce per-sandbox rate limiting (VAL-GATEWAY-005).
	if h.rateLimiter != nil {
		if !h.rateLimiter.Record(sandboxID) {
			_, _, resetIn := h.rateLimiter.Status(sandboxID)
			retrySeconds := int(resetIn.Seconds())
			if retrySeconds < 1 {
				retrySeconds = 1
			}
			log.Printf("gateway: rate limit exceeded for sandbox %s", sandboxID)
			w.Header().Set("Retry-After", fmt.Sprintf("%d", retrySeconds))
			writeGatewayJSON(w, http.StatusTooManyRequests, ErrorResponse{
				Error: fmt.Sprintf("rate limit exceeded for sandbox %s (limit %d requests per %s)",
					sandboxID, h.rateLimiter.maxReqs, h.rateLimiter.window),
			})
			return
		}
	}

	// Step 3: Decode the request.
	var req ProviderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeGatewayJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid request body"})
		return
	}

	// Step 4: Resolve the provider (multi-provider or single-provider).
	p, err := h.resolveProvider(req)
	if err != nil {
		log.Printf("gateway: provider resolution failed for sandbox %s: %v", sandboxID, err)
		writeGatewayJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}
	if p == nil {
		log.Printf("gateway: no provider configured for request from sandbox %s", sandboxID)
		writeGatewayJSON(w, http.StatusServiceUnavailable, ErrorResponse{
			Error: "no provider configured",
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
		Stream:    req.Stream,
	}

	log.Printf("gateway: inference request from sandbox %s (provider=%s model=%s messages=%d stream=%v)",
		sandboxID, p.Name(), req.Model, len(req.Messages), req.Stream)

	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()

	// If the client requests streaming, use SSE streaming.
	if req.Stream {
		h.handleStreamingInference(w, r, p, llmReq, sandboxID)
		return
	}

	resp, err := p.Call(ctx, llmReq)
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

// handleStreamingInference handles streaming inference requests by setting up
// an SSE response and forwarding chunks from the provider to the client in
// real-time (VAL-LLM-003, VAL-LLM-004).
func (h *Handler) handleStreamingInference(w http.ResponseWriter, r *http.Request, p provider.Provider, llmReq provider.LLMRequest, sandboxID string) {
	// Set up SSE response headers.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // Disable nginx buffering

	flusher, canFlush := w.(http.Flusher)

	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()

	resp, err := p.Stream(ctx, llmReq, func(chunk provider.StreamChunk) {
		// Marshal the chunk as SSE data.
		data, err := json.Marshal(chunk)
		if err != nil {
			log.Printf("gateway: marshal stream chunk: %v", err)
			return
		}

		// Write SSE event.
		fmt.Fprintf(w, "data: %s\n\n", data)
		if canFlush {
			flusher.Flush()
		}
	})

	if err != nil {
		// If we haven't written any SSE data yet, we can return an HTTP error.
		// Otherwise, send an error event.
		sanitized := sanitizeError(err)
		log.Printf("gateway: streaming provider call failed for sandbox %s: %v (sanitized: %s)",
			sandboxID, err, sanitized)

		errData, _ := json.Marshal(map[string]string{"error": sanitized})
		fmt.Fprintf(w, "data: %s\n\n", errData)
		if canFlush {
			flusher.Flush()
		}
		return
	}

	// Send final [DONE] marker.
	fmt.Fprintf(w, "data: [DONE]\n\n")
	if canFlush {
		flusher.Flush()
	}

	log.Printf("gateway: streaming inference succeeded for sandbox %s (provider=%s tokens=%d+%d text_len=%d)",
		sandboxID, resp.ProviderName, resp.Usage.InputTokens, resp.Usage.OutputTokens, len(resp.Text))
}

// resolveProvider selects the appropriate provider for the given request.
// In multi-provider mode, it routes based on the explicit provider field
// or the model parameter. In single-provider mode, it validates that the
// requested provider matches the configured one.
func (h *Handler) resolveProvider(req ProviderRequest) (provider.Provider, error) {
	// Multi-provider mode: route based on provider name or model.
	if h.providers != nil {
		return h.resolveFromMultiProvider(req)
	}

	// Single-provider mode: validate the requested provider matches.
	if req.Provider != "" && h.provider != nil && req.Provider != h.provider.Name() {
		return nil, fmt.Errorf("unsupported provider: %s", req.Provider)
	}

	return h.provider, nil
}

// resolveFromMultiProvider selects a provider from the multi-provider
// registry. It tries explicit provider name first, then model-based
// routing, then falls back to the first registered provider.
func (h *Handler) resolveFromMultiProvider(req ProviderRequest) (provider.Provider, error) {
	// Step 1: Explicit provider name routing.
	if req.Provider != "" {
		p := h.providers.Get(req.Provider)
		if p == nil {
			return nil, fmt.Errorf("unsupported provider: %s", req.Provider)
		}
		return p, nil
	}

	// Step 2: Model-based routing using SupportedModels.
	if req.Model != "" {
		for _, mi := range provider.SupportedModels() {
			if mi.ID == req.Model {
				p := h.providers.Get(mi.Provider)
				if p != nil {
					return p, nil
				}
			}
		}

		// Fallback: heuristic model routing for known patterns.
		if strings.Contains(req.Model, "fireworks") {
			if p := h.providers.Get("fireworks"); p != nil {
				return p, nil
			}
		}
		if strings.HasPrefix(req.Model, "glm-") {
			if p := h.providers.Get("zai"); p != nil {
				return p, nil
			}
		}
		if strings.Contains(req.Model, "anthropic") || strings.Contains(req.Model, "claude") {
			if p := h.providers.Get("bedrock"); p != nil {
				return p, nil
			}
		}
	}

	// Step 3: Default to the first registered provider.
	names := h.providers.Names()
	if len(names) > 0 {
		return h.providers.Get(names[0]), nil
	}

	return nil, nil
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
// nolint:unused
func drainBody(resp *http.Response) {
	if resp != nil && resp.Body != nil {
		_, _ = io.Copy(io.Discard, resp.Body)
	}
}

// HandleSearch handles POST /provider/v1/search for web search requests.
// It authenticates the sandbox caller, validates the request, and routes
// to available search providers with round-robin rotation and fallback.
func (h *Handler) HandleSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeGatewayJSON(w, http.StatusMethodNotAllowed, ErrorResponse{Error: "method not allowed"})
		return
	}

	// Step 1: Authenticate the sandbox caller.
	sandboxID, err := h.authenticateSandbox(r)
	if err != nil {
		log.Printf("gateway: search authentication denied: %v", err)
		writeGatewayJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "authentication required"})
		return
	}

	// Step 2: Enforce per-sandbox rate limiting.
	if h.rateLimiter != nil {
		if !h.rateLimiter.Record(sandboxID) {
			_, _, resetIn := h.rateLimiter.Status(sandboxID)
			retrySeconds := int(resetIn.Seconds())
			if retrySeconds < 1 {
				retrySeconds = 1
			}
			log.Printf("gateway: search rate limit exceeded for sandbox %s", sandboxID)
			w.Header().Set("Retry-After", fmt.Sprintf("%d", retrySeconds))
			writeGatewayJSON(w, http.StatusTooManyRequests, ErrorResponse{
				Error: fmt.Sprintf("rate limit exceeded for sandbox %s", sandboxID),
			})
			return
		}
	}

	// Step 3: Decode the request.
	var req SearchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeGatewayJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid request body"})
		return
	}

	if req.Query == "" {
		writeGatewayJSON(w, http.StatusBadRequest, ErrorResponse{Error: "query is required"})
		return
	}

	// Step 4: Check if search is configured.
	if h.searchClient == nil || len(h.searchClient.AvailableProviders()) == 0 {
		writeGatewayJSON(w, http.StatusServiceUnavailable, ErrorResponse{
			Error: "search not configured: no search providers available",
		})
		return
	}

	log.Printf("gateway: search request from sandbox %s (query=%q max_results=%d)",
		sandboxID, req.Query, req.MaxResults)

	// Step 5: Execute the search with timeout.
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	resp, err := h.searchClient.Search(ctx, req)
	if err != nil {
		sanitized := sanitizeError(err)
		log.Printf("gateway: search failed for sandbox %s: %v", sandboxID, sanitized)
		writeGatewayJSON(w, http.StatusBadGateway, ErrorResponse{Error: sanitized})
		return
	}

	log.Printf("gateway: search succeeded for sandbox %s (provider=%s results=%d)",
		sandboxID, resp.Provider, len(resp.Results))

	writeGatewayJSON(w, http.StatusOK, resp)
}

// RegisterRoutes registers all gateway routes on the given server.
func RegisterRoutes(s *server.Server, h *Handler) {
	s.SetHealthHandler(h.HandleHealth)
	s.HandleFunc("/provider/v1/inference", h.HandleInference)
	s.HandleFunc("/provider/v1/search", h.HandleSearch)
	s.HandleFunc("/provider/v1/credentials/issue", h.HandleIssueCredential)
	s.HandleFunc("/provider/v1/credentials/revoke", h.HandleRevokeCredential)
	s.HandleFunc("/provider/v1/credentials/rotate", h.HandleRotateCredential)
}
