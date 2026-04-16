// Package events provides the runtime event bus and vocabulary for the
// go-choir sandbox runtime.
//
// The event bus supports in-process pub/sub for runtime lifecycle events,
// task progress, and health state changes. Events are published by the runtime
// loop and consumed by the event streaming surface (/api/events) and
// supervisor components.
//
// Adapted from Cogent's event bus pattern but simplified for go-choir's
// constraints: no adapter-native session discovery, no transfer events,
// and no tool-hint translation. Those Cogent-specific concerns are replaced
// by go-choir's direct goroutine execution model.
package events

import (
	"sync"
	"sync/atomic"

	"github.com/yusefmosiah/go-choir/internal/types"
)

// EventActor identifies who or what caused the event.
type EventActor string

const (
	// ActorRuntime represents the runtime loop itself.
	ActorRuntime EventActor = "runtime"

	// ActorSupervisor represents the goroutine supervisor.
	ActorSupervisor EventActor = "supervisor"

	// ActorProvider represents an upstream provider interaction.
	ActorProvider EventActor = "provider"

	// ActorHost represents a host-side action (e.g., manual operator).
	ActorHost EventActor = "host"

	// ActorTool represents a tool invocation within the tool-calling loop.
	ActorTool EventActor = "tool"

	// ActorChannel represents a channel message for inter-agent coordination.
	ActorChannel EventActor = "channel"
)

// EventCause classifies why the event was emitted.
type EventCause string

const (
	// CauseTaskLifecycle represents a task state transition.
	CauseTaskLifecycle EventCause = "task_lifecycle"

	// CauseProviderProgress represents incremental progress from a provider.
	CauseProviderProgress EventCause = "provider_progress"

	// CauseProviderFailure represents a provider failure.
	CauseProviderFailure EventCause = "provider_failure"

	// CauseSupervisorRecovery represents a supervisor recovery action.
	CauseSupervisorRecovery EventCause = "supervisor_recovery"

	// CauseHostAction represents a manual host-side action.
	CauseHostAction EventCause = "host_action"

	// CauseToolExecution represents a tool invocation within the
	// tool-calling loop. Tool invocations emit observable events so
	// that later appagent and browser features can track tool-driven
	// task progress.
	CauseToolExecution EventCause = "tool_execution"

	// CauseChannelMessage represents an inter-agent channel message.
	CauseChannelMessage EventCause = "channel_message"
)

// RuntimeEvent wraps a types.EventRecord with additional context about the
// actor and cause. The bus publishes RuntimeEvent values, and consumers
// can inspect Actor and Cause for filtering decisions while the underlying
// EventRecord carries the stable vocabulary and persisted shape.
type RuntimeEvent struct {
	// Record is the stable event record that gets persisted and streamed.
	Record types.EventRecord

	// Actor identifies who caused the event.
	Actor EventActor

	// Cause classifies why the event was emitted.
	Cause EventCause
}

// RequiresSupervisorAttention returns true if this event should wake the
// supervisor for potential action (restart, recovery, escalation).
func (ev RuntimeEvent) RequiresSupervisorAttention() bool {
	// Supervisor's own actions should not re-wake itself.
	if ev.Actor == ActorSupervisor {
		return false
	}

	// Provider failures require supervisor attention for recovery.
	if ev.Cause == CauseProviderFailure {
		return true
	}

	// Terminal task events are actionable for the supervisor.
	if ev.Record.Kind == types.EventRunFailed || ev.Record.Kind == types.EventRunBlocked {
		return true
	}

	// Runtime degradation events are actionable.
	if ev.Record.Kind == types.EventRuntimeDegraded {
		return true
	}

	// Non-terminal task progress from the provider is noise for the supervisor.
	if ev.Cause == CauseProviderProgress && !IsTerminal(ev.Record.Kind) {
		return false
	}

	// Host actions may be actionable.
	if ev.Actor == ActorHost {
		return true
	}

	return false
}

// IsTerminal returns true if the event kind represents a terminal task state.
func IsTerminal(kind types.EventKind) bool {
	switch kind {
	case types.EventRunCompleted, types.EventRunFailed, types.EventRunCancelled:
		return true
	default:
		return false
	}
}

// EventBus provides in-process pub/sub for runtime events.
// Subscribers receive events through buffered channels. When a subscriber's
// channel is full, events are dropped and counted to prevent slow consumers
// from blocking the publisher.
type EventBus struct {
	mu        sync.Mutex
	subs      []chan RuntimeEvent
	drops     atomic.Int64
	published atomic.Int64
}

// NewEventBus creates a new EventBus.
func NewEventBus() *EventBus {
	return &EventBus{}
}

// Subscribe returns a new channel that receives runtime events.
// The channel has a default buffer size of 64.
func (b *EventBus) Subscribe() chan RuntimeEvent {
	return b.SubscribeWithBuffer(64)
}

// SubscribeWithBuffer returns a new channel with the given buffer size.
// A size <= 0 defaults to 64.
func (b *EventBus) SubscribeWithBuffer(size int) chan RuntimeEvent {
	if size <= 0 {
		size = 64
	}
	ch := make(chan RuntimeEvent, size)
	b.mu.Lock()
	b.subs = append(b.subs, ch)
	b.mu.Unlock()
	return ch
}

// Unsubscribe removes the given channel from the subscriber list and closes it.
func (b *EventBus) Unsubscribe(ch chan RuntimeEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i, sub := range b.subs {
		if sub == ch {
			b.subs = append(b.subs[:i], b.subs[i+1:]...)
			close(ch)
			return
		}
	}
}

// Publish sends the event to all subscribers. If a subscriber's channel is
// full, the event is dropped for that subscriber and the drop counter is
// incremented.
func (b *EventBus) Publish(ev RuntimeEvent) {
	b.published.Add(1)
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.subs {
		select {
		case ch <- ev:
		default:
			b.drops.Add(1)
		}
	}
}

// Stats returns the total number of published events and dropped events.
func (b *EventBus) Stats() (published int64, drops int64) {
	return b.published.Load(), b.drops.Load()
}

// SubscriberCount returns the current number of active subscribers.
func (b *EventBus) SubscriberCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
}
