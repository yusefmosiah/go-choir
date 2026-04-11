package gateway

import (
	"fmt"
	"sync"
	"time"
)

const (
	// DefaultRateLimitMaxRequests is the default maximum inference requests
	// per sandbox per window. Configurable via GATEWAY_RATE_LIMIT_MAX_REQUESTS.
	DefaultRateLimitMaxRequests = 60

	// DefaultRateLimitWindowSize is the default sliding window duration for
	// per-sandbox rate limiting. Configurable via GATEWAY_RATE_LIMIT_WINDOW.
	DefaultRateLimitWindowSize = 1 * time.Minute
)

// RateLimiterConfig holds rate-limiting configuration resolved from
// environment variables.
type RateLimiterConfig struct {
	// MaxRequests is the maximum number of inference requests allowed per
	// sandbox per window. 0 means use the default.
	MaxRequests int

	// WindowSize is the sliding window duration. 0 means use the default.
	WindowSize time.Duration
}

// Resolve returns a config with zero values replaced by defaults.
func (c RateLimiterConfig) Resolve() RateLimiterConfig {
	r := c
	if r.MaxRequests <= 0 {
		r.MaxRequests = DefaultRateLimitMaxRequests
	}
	if r.WindowSize <= 0 {
		r.WindowSize = DefaultRateLimitWindowSize
	}
	return r
}

// tokenBucket is a simple sliding-window counter for a single sandbox.
// It tracks request counts within a time window and resets when the
// window expires. It is safe for concurrent use via the enclosing
// PerSandboxRateLimiter mutex.
type tokenBucket struct {
	windowStart time.Time
	count       int
}

// PerSandboxRateLimiter provides per-sandbox rate limiting using a
// fixed-window counter algorithm. Each sandbox identity gets its own
// independent quota. When one sandbox exhausts its quota, other
// sandboxes are unaffected (VAL-GATEWAY-005, VAL-CROSS-115).
type PerSandboxRateLimiter struct {
	mu       sync.Mutex
	buckets  map[string]*tokenBucket // sandbox_id → bucket
	maxReqs  int
	window   time.Duration
}

// NewPerSandboxRateLimiter creates a rate limiter that allows maxReqs
// requests per sandbox per window duration.
func NewPerSandboxRateLimiter(maxReqs int, window time.Duration) *PerSandboxRateLimiter {
	return &PerSandboxRateLimiter{
		buckets: make(map[string]*tokenBucket),
		maxReqs: maxReqs,
		window:  window,
	}
}

// Allow checks whether the sandbox is within its rate limit and
// atomically consumes one slot if so. Returns true if the request
// is allowed, false if the rate limit has been exceeded.
func (rl *PerSandboxRateLimiter) Allow(sandboxID string) bool {
	return rl.record(sandboxID, false)
}

// Record checks and records a request, returning whether it was allowed.
// This is the same as Allow but is named for clarity in the handler
// where we want to explicitly record the rate limit check.
func (rl *PerSandboxRateLimiter) Record(sandboxID string) bool {
	return rl.record(sandboxID, false)
}

// record is the internal implementation. If dryRun is true, it checks
// without consuming. Otherwise it atomically checks and consumes.
func (rl *PerSandboxRateLimiter) record(sandboxID string, dryRun bool) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	b, ok := rl.buckets[sandboxID]
	if !ok || now.Sub(b.windowStart) >= rl.window {
		// No bucket yet or window expired: start a new window.
		if dryRun {
			return true
		}
		rl.buckets[sandboxID] = &tokenBucket{
			windowStart: now,
			count:       1,
		}
		return true
	}

	if b.count >= rl.maxReqs {
		return false
	}

	if dryRun {
		return true
	}

	b.count++
	return true
}

// Status returns the current usage for a sandbox. Returns (used, limit, resetIn).
// Used is 0 if the sandbox has no bucket or the window has expired.
func (rl *PerSandboxRateLimiter) Status(sandboxID string) (used, limit int, resetIn time.Duration) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	limit = rl.maxReqs

	b, ok := rl.buckets[sandboxID]
	if !ok {
		return 0, limit, rl.window
	}

	now := time.Now()
	elapsed := now.Sub(b.windowStart)
	if elapsed >= rl.window {
		// Window expired; effectively 0 used.
		return 0, limit, rl.window
	}

	remaining := rl.window - elapsed
	return b.count, limit, remaining
}

// RemoveBucket removes the rate-limit bucket for a sandbox, typically
// when the sandbox is revoked or stopped. This prevents unbounded memory
// growth from stale sandbox entries.
func (rl *PerSandboxRateLimiter) RemoveBucket(sandboxID string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	delete(rl.buckets, sandboxID)
}

// String returns a human-readable description of the rate limiter config.
func (rl *PerSandboxRateLimiter) String() string {
	return fmt.Sprintf("PerSandboxRateLimiter(max=%d/window=%s)", rl.maxReqs, rl.window)
}
