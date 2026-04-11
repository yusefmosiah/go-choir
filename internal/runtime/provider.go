package runtime

import (
	"context"
	"encoding/json"
	"time"

	"github.com/yusefmosiah/go-choir/internal/types"
)

// EventEmitFunc is a callback for emitting incremental events during task
// execution. Providers call this to report progress, deltas, or other
// lifecycle updates before the task completes.
type EventEmitFunc func(kind types.EventKind, phase string, payload json.RawMessage)

// Provider is the interface for executing a runtime task. The stub provider
// simulates execution; the real Bedrock/Z.AI bridge will implement this
// interface in a later feature.
type Provider interface {
	// Execute runs the task to completion, emitting incremental events via
	// the callback. Returns nil on success or an error describing the failure.
	// The runtime transitions the task to failed/blocked on error and remains
	// available for later tasks (VAL-RUNTIME-008).
	Execute(ctx context.Context, task *types.TaskRecord, emit EventEmitFunc) error
}

// StubProvider simulates task execution with a configurable delay and optional
// failure. It emits progress events during the simulated work and returns a
// canned result or error.
type StubProvider struct {
	// Delay is the simulated execution duration.
	Delay time.Duration

	// FailErr, if set, causes Execute to return this error instead of
	// succeeding. This simulates provider failures for VAL-RUNTIME-008.
	FailErr error

	// Result is the text returned as the task result on success.
	Result string
}

// NewStubProvider creates a StubProvider with the given delay and default
// result text.
func NewStubProvider(delay time.Duration) *StubProvider {
	return &StubProvider{
		Delay:  delay,
		Result: "Task completed successfully (stub provider).",
	}
}

// Execute simulates task execution by sleeping for the configured delay,
// emitting progress events, and returning the configured result or error.
func (p *StubProvider) Execute(ctx context.Context, task *types.TaskRecord, emit EventEmitFunc) error {
	// Emit a progress event at the start.
	emit(types.EventTaskProgress, "execution", json.RawMessage(`{"status":"started","provider":"stub"}`))

	// Simulate work in increments.
	deadline := time.After(p.Delay)
	tick := time.NewTicker(p.Delay / 4)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			goto done
		case <-tick.C:
			emit(types.EventTaskProgress, "execution", json.RawMessage(`{"status":"working","provider":"stub"}`))
		}
	}

done:
	if p.FailErr != nil {
		return p.FailErr
	}

	emit(types.EventTaskDelta, "execution",
		json.RawMessage(`{"text":"`+p.Result+`","provider":"stub"}`))

	return nil
}
