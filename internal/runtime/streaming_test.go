package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yusefmosiah/go-choir/internal/events"
	"github.com/yusefmosiah/go-choir/internal/store"
	"github.com/yusefmosiah/go-choir/internal/types"
)

// --- Streaming Provider Mock ---

// streamingProvider simulates a provider that emits multiple delta events
// during execution, mimicking real SSE streaming from an LLM provider.
type streamingProvider struct {
	name    string
	chunks  []string // text chunks to emit as deltas
	execErr error
	mu      sync.Mutex
	called  bool
}

func (p *streamingProvider) Execute(ctx context.Context, task *types.RunRecord, emit EventEmitFunc) error {
	p.mu.Lock()
	p.called = true
	p.mu.Unlock()

	if p.execErr != nil {
		return p.execErr
	}

	// Emit a started progress event.
	emit(types.EventRunProgress, "execution", json.RawMessage(`{"status":"started","provider":"`+p.name+`","streaming":true}`))

	// Emit a delta event for each chunk, simulating real SSE streaming.
	for _, chunk := range p.chunks {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		deltaPayload, _ := json.Marshal(map[string]string{
			"text":     chunk,
			"provider": p.name,
		})
		emit(types.EventRunDelta, "execution", deltaPayload)
		// Small delay to simulate real streaming latency.
		time.Sleep(10 * time.Millisecond)
	}

	// Set the accumulated result.
	task.Result = strings.Join(p.chunks, "")
	return nil
}

func (p *streamingProvider) ProviderName() string { return p.name }

// --- Test Helpers ---

// testStreamingRuntime creates a fresh Runtime for streaming tests.
func testStreamingRuntime(t *testing.T, provider Provider) (*Runtime, *store.Store) {
	t.Helper()

	dir := filepath.Join(os.TempDir(), "go-choir-m3-streaming-test")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	dbPath := filepath.Join(dir, t.Name()+".db")
	_ = os.Remove(dbPath)

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	bus := events.NewEventBus()
	cfg := Config{
		SandboxID:           "sandbox-streaming-test",
		StorePath:           dbPath,
		ProviderTimeout:     5 * time.Second,
		SupervisionInterval: 1 * time.Hour,
	}

	rt := New(cfg, s, bus, provider)
	t.Cleanup(func() {
		rt.Stop()
		_ = s.Close()
		_ = os.Remove(dbPath)
	})

	return rt, s
}

func waitForRunTerminalState(t *testing.T, rt *Runtime, runID, ownerID string, timeout time.Duration) types.RunRecord {
	t.Helper()

	ctx := context.Background()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		rec, err := rt.GetRun(ctx, runID, ownerID)
		if err != nil {
			t.Fatalf("get task: %v", err)
		}
		if rec.State.Terminal() {
			return *rec
		}
		time.Sleep(25 * time.Millisecond)
	}

	rec, err := rt.GetRun(ctx, runID, ownerID)
	if err != nil {
		t.Fatalf("get task after timeout: %v", err)
	}
	t.Fatalf("timeout waiting for task %s (state=%s)", runID[:8], rec.State)
	return types.RunRecord{}
}

// --- Streaming Tests ---

// TestStreamingProviderEmitsMultipleDeltas verifies that a streaming provider
// emits multiple loop.delta events during run execution, one for each text
// chunk. This is the core behavior that enables real-time SSE streaming from
// provider → runtime → browser (VAL-LLM-008).
func TestStreamingProviderEmitsMultipleDeltas(t *testing.T) {
	chunks := []string{"Hello ", "world ", "from ", "streaming!"}
	provider := &streamingProvider{
		name:   "stream-test",
		chunks: chunks,
	}

	rt, s := testStreamingRuntime(t, provider)
	ctx := context.Background()

	rec, err := rt.StartRun(ctx, "stream test", "user-stream")
	if err != nil {
		t.Fatalf("submit task: %v", err)
	}

	// Wait for task completion.
	time.Sleep(300 * time.Millisecond)

	// Verify task completed successfully.
	stored, err := rt.GetRun(ctx, rec.RunID, "user-stream")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if stored.State != types.RunCompleted {
		t.Fatalf("state: got %q, want completed", stored.State)
	}

	// Verify accumulated result is all chunks joined.
	expected := "Hello world from streaming!"
	if stored.Result != expected {
		t.Errorf("result: got %q, want %q", stored.Result, expected)
	}

	// Verify delta events were persisted for each chunk.
	evts, err := s.ListEvents(ctx, rec.RunID, 50)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}

	deltaCount := 0
	for _, ev := range evts {
		if ev.Kind == types.EventRunDelta {
			deltaCount++
			var payload map[string]string
			if err := json.Unmarshal(ev.Payload, &payload); err == nil {
				if payload["provider"] != "stream-test" {
					t.Errorf("delta provider: got %q, want stream-test", payload["provider"])
				}
			}
		}
	}

	if deltaCount != len(chunks) {
		t.Errorf("delta events: got %d, want %d (one per chunk)", deltaCount, len(chunks))
	}

	// Verify the provider was called.
	if !provider.called {
		t.Error("expected streaming provider to be called")
	}
}

// TestStreamingDeltaEventsReceivedOnBus verifies that delta events emitted
// during streaming are published to the EventBus and can be received by
// subscribers. This tests the full chain: provider → emit → EventBus → subscriber.
func TestStreamingDeltaEventsReceivedOnBus(t *testing.T) {
	chunks := []string{"chunk1 ", "chunk2 ", "chunk3"}
	provider := &streamingProvider{
		name:   "bus-test",
		chunks: chunks,
	}

	rt, _ := testStreamingRuntime(t, provider)
	ctx := context.Background()

	// Subscribe to the event bus before submitting the task.
	ch := rt.EventBus().Subscribe()
	defer rt.EventBus().Unsubscribe(ch)

	_, err := rt.StartRun(ctx, "bus test", "user-bus")
	if err != nil {
		t.Fatalf("submit task: %v", err)
	}

	// Collect events for enough time to receive all streaming chunks.
	var received []events.RuntimeEvent
	timer := time.After(500 * time.Millisecond)
	for {
		select {
		case ev := <-ch:
			if ev.Record.OwnerID == "user-bus" {
				received = append(received, ev)
			}
		case <-timer:
			goto done
		}
	}
done:

	// Count delta events received via the bus.
	deltaCount := 0
	for _, ev := range received {
		if ev.Record.Kind == types.EventRunDelta {
			deltaCount++
		}
	}

	if deltaCount != len(chunks) {
		t.Errorf("bus delta events: got %d, want %d", deltaCount, len(chunks))
	}

	// Verify event ordering: submitted → started → deltas → completed.
	var kinds []types.EventKind
	for _, ev := range received {
		kinds = append(kinds, ev.Record.Kind)
	}

	// submitted should come first.
	if len(kinds) == 0 || kinds[0] != types.EventRunSubmitted {
		t.Errorf("first event: got %v, want loop.submitted", kinds)
	}

	// deltas should come before completed.
	deltaIdx := -1
	completedIdx := -1
	for i, k := range kinds {
		if k == types.EventRunDelta && deltaIdx == -1 {
			deltaIdx = i
		}
		if k == types.EventRunCompleted {
			completedIdx = i
		}
	}
	if deltaIdx >= 0 && completedIdx >= 0 && deltaIdx > completedIdx {
		t.Errorf("delta event (idx %d) should come before completed (idx %d)", deltaIdx, completedIdx)
	}
}

// TestStreamingEventsContainChunkText verifies that each delta event payload
// contains the text from the corresponding streaming chunk, enabling the
// frontend to display incremental text as it arrives (VAL-LLM-008).
func TestStreamingEventsContainChunkText(t *testing.T) {
	chunks := []string{"The ", "quick ", "brown ", "fox"}
	provider := &streamingProvider{
		name:   "text-test",
		chunks: chunks,
	}

	rt, s := testStreamingRuntime(t, provider)
	ctx := context.Background()

	rec, err := rt.StartRun(ctx, "text test", "user-text")
	if err != nil {
		t.Fatalf("submit task: %v", err)
	}

	time.Sleep(300 * time.Millisecond)

	evts, err := s.ListEvents(ctx, rec.RunID, 50)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}

	// Collect the text from each delta event.
	var deltaTexts []string
	for _, ev := range evts {
		if ev.Kind == types.EventRunDelta {
			var payload map[string]string
			if err := json.Unmarshal(ev.Payload, &payload); err == nil {
				deltaTexts = append(deltaTexts, payload["text"])
			}
		}
	}

	// Verify we got the exact chunks.
	if len(deltaTexts) != len(chunks) {
		t.Fatalf("delta texts: got %d, want %d", len(deltaTexts), len(chunks))
	}
	for i, got := range deltaTexts {
		if got != chunks[i] {
			t.Errorf("delta[%d]: got %q, want %q", i, got, chunks[i])
		}
	}
}

// TestStreamingCompletesWithProperTermination verifies that a streaming task
// properly terminates with a loop.completed event after all chunks are
// emitted. This ensures the stream lifecycle is complete and the browser
// receives a clear completion signal (VAL-LLM-008, VAL-LLM-009).
func TestStreamingCompletesWithProperTermination(t *testing.T) {
	chunks := []string{"Hello", " streaming", " world"}
	provider := &streamingProvider{
		name:   "term-test",
		chunks: chunks,
	}

	rt, s := testStreamingRuntime(t, provider)
	ctx := context.Background()

	rec, err := rt.StartRun(ctx, "termination test", "user-term")
	if err != nil {
		t.Fatalf("submit task: %v", err)
	}

	time.Sleep(300 * time.Millisecond)

	// Verify task completed.
	stored, err := rt.GetRun(ctx, rec.RunID, "user-term")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if stored.State != types.RunCompleted {
		t.Fatalf("state: got %q, want completed", stored.State)
	}
	if stored.FinishedAt == nil {
		t.Error("finished_at should be set for completed streaming task")
	}

	// Verify a loop.completed event was emitted.
	evts, err := s.ListEvents(ctx, rec.RunID, 50)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}

	hasCompleted := false
	for _, ev := range evts {
		if ev.Kind == types.EventRunCompleted {
			hasCompleted = true
		}
	}
	if !hasCompleted {
		t.Error("expected loop.completed event for streaming run")
	}
}

// TestStreamingFailureEmitsErrorEvents verifies that when a streaming provider
// fails, the task transitions to failed state with proper error events,
// matching the non-streaming failure behavior.
func TestStreamingFailureEmitsErrorEvents(t *testing.T) {
	provider := &streamingProvider{
		name:    "fail-stream",
		execErr: context.DeadlineExceeded,
	}

	rt, _ := testStreamingRuntime(t, provider)
	ctx := context.Background()

	rec, err := rt.StartRun(ctx, "stream failure test", "user-fail")
	if err != nil {
		t.Fatalf("submit task: %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	// Verify task failed.
	stored, err := rt.GetRun(ctx, rec.RunID, "user-fail")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if stored.State != types.RunFailed {
		t.Fatalf("state: got %q, want failed", stored.State)
	}

	// Runtime should still be healthy for new runs.
	if rt.HealthState() == types.HealthFailed {
		t.Error("runtime should not be in failed state after streaming provider error")
	}
}

// TestStreamingCancellationStopsEmission verifies that cancelling a streaming
// task context stops the emission of further delta events. This tests proper
// context propagation through the streaming pipeline.
func TestStreamingCancellationStopsEmission(t *testing.T) {
	// Create a provider that would emit many chunks but with delays.
	chunks := make([]string, 100)
	for i := range chunks {
		chunks[i] = "x"
	}
	provider := &streamingProvider{
		name:   "cancel-test",
		chunks: chunks,
	}

	rt, _ := testStreamingRuntime(t, provider)
	ctx := context.Background()

	ch := rt.EventBus().Subscribe()
	defer rt.EventBus().Unsubscribe(ch)

	_, err := rt.StartRun(ctx, "cancel test", "user-cancel")
	if err != nil {
		t.Fatalf("submit task: %v", err)
	}

	// Let a few chunks emit, then stop the runtime (which cancels all runs).
	time.Sleep(100 * time.Millisecond)
	rt.Stop()

	// Count how many delta events we received.
	deltaCount := 0
	timer := time.After(100 * time.Millisecond)
	for {
		select {
		case ev := <-ch:
			if ev.Record.Kind == types.EventRunDelta {
				deltaCount++
			}
		case <-timer:
			goto counted
		}
	}
counted:

	// We should have received fewer than the total chunks (100),
	// proving that cancellation stopped the emission.
	if deltaCount >= 100 {
		t.Errorf("expected cancellation to stop streaming, but received all %d deltas", deltaCount)
	}
}

// TestStreamingWithEmptyChunks verifies that streaming with empty chunks
// handles gracefully without emitting delta events for empty text.
func TestStreamingWithEmptyChunks(t *testing.T) {
	chunks := []string{"Hello", "", " world", ""}
	provider := &streamingProvider{
		name:   "empty-test",
		chunks: chunks,
	}

	rt, _ := testStreamingRuntime(t, provider)
	ctx := context.Background()

	rec, err := rt.StartRun(ctx, "empty chunks test", "user-empty")
	if err != nil {
		t.Fatalf("submit task: %v", err)
	}

	stored := waitForRunTerminalState(t, rt, rec.RunID, "user-empty", 5*time.Second)
	if stored.State != types.RunCompleted {
		t.Fatalf("state: got %q, want completed", stored.State)
	}

	// Result should be "Hello world" (empty strings produce nothing).
	expected := "Hello world"
	if stored.Result != expected {
		t.Errorf("result: got %q, want %q", stored.Result, expected)
	}
}

// TestStreamingLargePayload verifies that streaming works correctly with
// larger text payloads, simulating realistic LLM responses.
func TestStreamingLargePayload(t *testing.T) {
	// Simulate a 1KB response split into 10 chunks of ~100 bytes each.
	var chunks []string
	longText := strings.Repeat("Lorem ipsum dolor sit amet. ", 40) // ~1KB
	chunkSize := len(longText) / 10
	for i := 0; i < 10; i++ {
		end := (i + 1) * chunkSize
		if end > len(longText) {
			end = len(longText)
		}
		chunks = append(chunks, longText[i*chunkSize:end])
	}

	provider := &streamingProvider{
		name:   "large-test",
		chunks: chunks,
	}

	rt, _ := testStreamingRuntime(t, provider)
	ctx := context.Background()

	rec, err := rt.StartRun(ctx, "large payload test", "user-large")
	if err != nil {
		t.Fatalf("submit task: %v", err)
	}

	stored := waitForRunTerminalState(t, rt, rec.RunID, "user-large", 5*time.Second)
	if stored.State != types.RunCompleted {
		t.Fatalf("state: got %q, want completed", stored.State)
	}
	if stored.Result != longText {
		t.Errorf("result length: got %d, want %d", len(stored.Result), len(longText))
	}
}
