package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yusefmosiah/go-choir/internal/provider"
)

// --- PerSandboxRateLimiter unit tests ---

func TestRateLimiterAllowsUnderLimit(t *testing.T) {
	rl := NewPerSandboxRateLimiter(5, 1*time.Second)

	for i := 0; i < 5; i++ {
		if !rl.Allow("sandbox-1") {
			t.Fatalf("request %d should be allowed", i+1)
		}
	}
}

func TestRateLimiterBlocksOverLimit(t *testing.T) {
	rl := NewPerSandboxRateLimiter(3, 1*time.Second)

	for i := 0; i < 3; i++ {
		if !rl.Allow("sandbox-1") {
			t.Fatalf("request %d should be allowed", i+1)
		}
	}

	if rl.Allow("sandbox-1") {
		t.Fatal("request 4 should be blocked (over limit)")
	}
}

func TestRateLimiterIsolationBetweenSandboxes(t *testing.T) {
	rl := NewPerSandboxRateLimiter(2, 1*time.Second)

	// sandbox-1 uses its full quota.
	rl.Allow("sandbox-1")
	rl.Allow("sandbox-1")

	// sandbox-1 is now blocked.
	if rl.Allow("sandbox-1") {
		t.Fatal("sandbox-1 should be blocked after using its quota")
	}

	// sandbox-2 is independent and should still succeed.
	if !rl.Allow("sandbox-2") {
		t.Fatal("sandbox-2 should be allowed (independent quota)")
	}
	if !rl.Allow("sandbox-2") {
		t.Fatal("sandbox-2 second request should be allowed")
	}

	// sandbox-2 is now also blocked.
	if rl.Allow("sandbox-2") {
		t.Fatal("sandbox-2 should be blocked after using its quota")
	}

	// sandbox-1 is still blocked (its quota was not freed by sandbox-2).
	if rl.Allow("sandbox-1") {
		t.Fatal("sandbox-1 should still be blocked")
	}
}

func TestRateLimiterConcurrentAccess(t *testing.T) {
	rl := NewPerSandboxRateLimiter(100, 1*time.Second)

	var wg sync.WaitGroup
	allowed := atomic.Int32{}

	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if rl.Allow("sandbox-1") {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()

	count := allowed.Load()
	if count != 100 {
		t.Fatalf("expected exactly 100 allowed requests, got %d", count)
	}
}

func TestRateLimiterConcurrentMultiSandbox(t *testing.T) {
	rl := NewPerSandboxRateLimiter(50, 1*time.Second)

	var wg sync.WaitGroup
	sandbox1Allowed := atomic.Int32{}
	sandbox2Allowed := atomic.Int32{}

	for i := 0; i < 100; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			if rl.Allow("sandbox-1") {
				sandbox1Allowed.Add(1)
			}
		}()
		go func() {
			defer wg.Done()
			if rl.Allow("sandbox-2") {
				sandbox2Allowed.Add(1)
			}
		}()
	}
	wg.Wait()

	s1 := sandbox1Allowed.Load()
	s2 := sandbox2Allowed.Load()
	if s1 != 50 {
		t.Fatalf("sandbox-1: expected 50 allowed, got %d", s1)
	}
	if s2 != 50 {
		t.Fatalf("sandbox-2: expected 50 allowed, got %d", s2)
	}
}

func TestRateLimiterWindowReset(t *testing.T) {
	rl := NewPerSandboxRateLimiter(2, 100*time.Millisecond)

	rl.Allow("sandbox-1")
	rl.Allow("sandbox-1")

	if rl.Allow("sandbox-1") {
		t.Fatal("should be blocked")
	}

	// Wait for the window to pass.
	time.Sleep(150 * time.Millisecond)

	if !rl.Allow("sandbox-1") {
		t.Fatal("should be allowed after window reset")
	}
}

func TestRateLimiterRecordUpdatesUsage(t *testing.T) {
	rl := NewPerSandboxRateLimiter(5, 1*time.Second)

	// Record should update the bucket.
	if !rl.Record("sandbox-1") {
		t.Fatal("first record should succeed")
	}

	// After recording, Allow should count that usage.
	// We already used 1 via Record, so Allow should work 4 more times.
	for i := 0; i < 4; i++ {
		if !rl.Allow("sandbox-1") {
			t.Fatalf("allow %d should succeed (1 record + %d allows = 5 total)", i+1, i+1)
		}
	}

	// 6th attempt should fail.
	if rl.Allow("sandbox-1") {
		t.Fatal("should be blocked after 1 record + 4 allows + 1 more = 6 total")
	}
}

func TestRateLimiterRecordReturnsFalseOverLimit(t *testing.T) {
	rl := NewPerSandboxRateLimiter(2, 1*time.Second)

	if !rl.Record("sandbox-1") {
		t.Fatal("first record should succeed")
	}
	if !rl.Record("sandbox-1") {
		t.Fatal("second record should succeed")
	}
	if rl.Record("sandbox-1") {
		t.Fatal("third record should fail (over limit)")
	}
}

func TestRateLimiterConfigDefaults(t *testing.T) {
	cfg := RateLimiterConfig{
		MaxRequests: 0, // should default
		WindowSize:  0, // should default
	}
	resolved := cfg.Resolve()
	if resolved.MaxRequests != DefaultRateLimitMaxRequests {
		t.Errorf("MaxRequests = %d, want default %d", resolved.MaxRequests, DefaultRateLimitMaxRequests)
	}
	if resolved.WindowSize != DefaultRateLimitWindowSize {
		t.Errorf("WindowSize = %v, want default %v", resolved.WindowSize, DefaultRateLimitWindowSize)
	}
}

func TestRateLimiterConfigExplicit(t *testing.T) {
	cfg := RateLimiterConfig{
		MaxRequests: 42,
		WindowSize:  5 * time.Minute,
	}
	resolved := cfg.Resolve()
	if resolved.MaxRequests != 42 {
		t.Errorf("MaxRequests = %d, want 42", resolved.MaxRequests)
	}
	if resolved.WindowSize != 5*time.Minute {
		t.Errorf("WindowSize = %v, want 5m", resolved.WindowSize)
	}
}

// --- Integration tests: rate limiting through Handler.HandleInference ---

func TestHandleInference_RateLimited(t *testing.T) {
	h, reg, mp := setupHandlerWithRateLimit(t, 3, 1*time.Second)
	_ = mp

	result, _ := reg.IssueCredential("sandbox-1")

	payload := ProviderRequest{
		Messages: []provider.Message{{Role: "user", Content: []provider.Block{{Type: "text", Text: "Hi"}}}},
	}
	body, _ := json.Marshal(payload)

	// First 3 requests should succeed.
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodPost, "/provider/v1/inference", strings.NewReader(string(body)))
		req.Header.Set("Authorization", "Bearer "+result.RawToken)
		req.Header.Set("Content-Type", "application/json")

		w := httptest.NewRecorder()
		h.HandleInference(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want %d; body: %s", i+1, w.Code, http.StatusOK, w.Body.String())
		}
	}

	// 4th request should be rate limited.
	req := httptest.NewRequest(http.MethodPost, "/provider/v1/inference", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+result.RawToken)
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.HandleInference(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("rate-limited request: status = %d, want %d; body: %s", w.Code, http.StatusTooManyRequests, w.Body.String())
	}

	var errResp ErrorResponse
	if err := json.NewDecoder(w.Body).Decode(&errResp); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if !strings.Contains(errResp.Error, "rate limit") {
		t.Errorf("error = %q, want rate limit message", errResp.Error)
	}
}

func TestHandleInference_RateLimitIsolationBetweenSandboxes(t *testing.T) {
	// VAL-GATEWAY-005: One noisy sandbox can hit its limit while another
	// sandbox continues to receive provider-backed responses.
	h, reg, _ := setupHandlerWithRateLimit(t, 2, 1*time.Second)

	result1, _ := reg.IssueCredential("sandbox-1")
	result2, _ := reg.IssueCredential("sandbox-2")

	payload := ProviderRequest{
		Messages: []provider.Message{{Role: "user", Content: []provider.Block{{Type: "text", Text: "Hi"}}}},
	}
	body, _ := json.Marshal(payload)

	// sandbox-1 uses its full quota.
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/provider/v1/inference", strings.NewReader(string(body)))
		req.Header.Set("Authorization", "Bearer "+result1.RawToken)
		req.Header.Set("Content-Type", "application/json")

		w := httptest.NewRecorder()
		h.HandleInference(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("sandbox-1 request %d: status = %d, want %d", i+1, w.Code, http.StatusOK)
		}
	}

	// sandbox-1 is now rate-limited.
	req := httptest.NewRequest(http.MethodPost, "/provider/v1/inference", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+result1.RawToken)
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.HandleInference(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("sandbox-1 rate-limited: status = %d, want %d", w.Code, http.StatusTooManyRequests)
	}

	// sandbox-2 should still succeed.
	req = httptest.NewRequest(http.MethodPost, "/provider/v1/inference", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+result2.RawToken)
	req.Header.Set("Content-Type", "application/json")

	w = httptest.NewRecorder()
	h.HandleInference(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("sandbox-2 should still succeed: status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
}

func TestHandleInference_RateLimitDoesNotAffectAuth(t *testing.T) {
	// Rate limiting should only apply after successful authentication.
	// Unauthenticated requests should still get 401, not 429.
	h, _, _ := setupHandlerWithRateLimit(t, 1, 1*time.Second)

	req := httptest.NewRequest(http.MethodPost, "/provider/v1/inference", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.HandleInference(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated request: status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestHandleInference_RateLimitWindowReset(t *testing.T) {
	h, reg, _ := setupHandlerWithRateLimit(t, 1, 100*time.Millisecond)

	result, _ := reg.IssueCredential("sandbox-1")

	payload := ProviderRequest{
		Messages: []provider.Message{{Role: "user", Content: []provider.Block{{Type: "text", Text: "Hi"}}}},
	}
	body, _ := json.Marshal(payload)

	// First request succeeds.
	req := httptest.NewRequest(http.MethodPost, "/provider/v1/inference", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+result.RawToken)
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.HandleInference(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("first request: status = %d, want %d", w.Code, http.StatusOK)
	}

	// Second request is rate limited.
	req = httptest.NewRequest(http.MethodPost, "/provider/v1/inference", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+result.RawToken)
	req.Header.Set("Content-Type", "application/json")

	w = httptest.NewRecorder()
	h.HandleInference(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("rate-limited: status = %d, want %d", w.Code, http.StatusTooManyRequests)
	}

	// Wait for window to reset.
	time.Sleep(150 * time.Millisecond)

	// Should succeed again.
	req = httptest.NewRequest(http.MethodPost, "/provider/v1/inference", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+result.RawToken)
	req.Header.Set("Content-Type", "application/json")

	w = httptest.NewRecorder()
	h.HandleInference(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("after reset: status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestHandleInference_ParallelSandboxIsolation(t *testing.T) {
	// Parallel requests from two sandboxes: one hitting its limit
	// must not affect the other.
	h, reg, _ := setupHandlerWithRateLimit(t, 5, 1*time.Second)

	result1, _ := reg.IssueCredential("sandbox-1")
	result2, _ := reg.IssueCredential("sandbox-2")

	payload := ProviderRequest{
		Messages: []provider.Message{{Role: "user", Content: []provider.Block{{Type: "text", Text: "Hi"}}}},
	}
	body, _ := json.Marshal(payload)

	// Fire 8 requests from sandbox-1 (only 5 should succeed).
	var wg sync.WaitGroup
	sandbox1Statuses := make(chan int, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/provider/v1/inference", strings.NewReader(string(body)))
			req.Header.Set("Authorization", "Bearer "+result1.RawToken)
			req.Header.Set("Content-Type", "application/json")

			w := httptest.NewRecorder()
			h.HandleInference(w, req)
			sandbox1Statuses <- w.Code
		}()
	}

	wg.Wait()
	close(sandbox1Statuses)

	var s1ok, s1limited int
	for code := range sandbox1Statuses {
		switch code {
		case http.StatusOK:
			s1ok++
		case http.StatusTooManyRequests:
			s1limited++
		}
	}

	if s1ok != 5 {
		t.Errorf("sandbox-1: expected 5 ok, got %d", s1ok)
	}
	if s1limited != 3 {
		t.Errorf("sandbox-1: expected 3 rate-limited, got %d", s1limited)
	}

	// sandbox-2 should still have its full quota.
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodPost, "/provider/v1/inference", strings.NewReader(string(body)))
		req.Header.Set("Authorization", "Bearer "+result2.RawToken)
		req.Header.Set("Content-Type", "application/json")

		w := httptest.NewRecorder()
		h.HandleInference(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("sandbox-2 request %d: status = %d, want %d", i+1, w.Code, http.StatusOK)
		}
	}
}

func TestRateLimitHeadersIn429Response(t *testing.T) {
	h, reg, _ := setupHandlerWithRateLimit(t, 1, 1*time.Second)

	result, _ := reg.IssueCredential("sandbox-1")

	payload := ProviderRequest{
		Messages: []provider.Message{{Role: "user", Content: []provider.Block{{Type: "text", Text: "Hi"}}}},
	}
	body, _ := json.Marshal(payload)

	// Use up the quota.
	req := httptest.NewRequest(http.MethodPost, "/provider/v1/inference", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+result.RawToken)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleInference(w, req)

	// Trigger rate limit.
	req = httptest.NewRequest(http.MethodPost, "/provider/v1/inference", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+result.RawToken)
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	h.HandleInference(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusTooManyRequests)
	}

	// Check for Retry-After header.
	retryAfter := w.Header().Get("Retry-After")
	if retryAfter == "" {
		t.Error("expected Retry-After header in 429 response")
	}
}

// --- Health endpoint includes rate limiter status ---

func TestHandleHealth_WithRateLimiter(t *testing.T) {
	h, _, _ := setupHandlerWithRateLimit(t, 10, 1*time.Second)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	h.HandleHealth(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp gatewayHealthResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The rate limiter should be reported.
	if resp.RateLimitMaxRequests != 10 {
		t.Errorf("RateLimitMaxRequests = %d, want 10", resp.RateLimitMaxRequests)
	}
}

// --- Helpers ---

// setupHandlerWithRateLimit creates a Handler with a mock provider and
// a per-sandbox rate limiter configured with the given parameters.
func setupHandlerWithRateLimit(t *testing.T, maxReqs int, window time.Duration) (*Handler, *IdentityRegistry, *mockProvider) {
	t.Helper()
	reg := NewIdentityRegistry(1 * time.Hour)
	mp := &mockProvider{
		name: "bedrock",
		real: true,
		response: &provider.LLMResponse{
			ID:           "resp-123",
			Text:         "Hello from Bedrock!",
			Model:        "claude-sonnet",
			StopReason:   "end_turn",
			ProviderName: "bedrock",
			Usage:        provider.Usage{InputTokens: 10, OutputTokens: 20},
		},
	}
	rl := NewPerSandboxRateLimiter(maxReqs, window)
	return NewHandlerWithRateLimit(reg, mp, rl), reg, mp
}
