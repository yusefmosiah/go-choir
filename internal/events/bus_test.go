package events

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/yusefmosiah/go-choir/internal/types"
)

func TestEventBusPublishReceive(t *testing.T) {
	bus := NewEventBus()
	ch := bus.Subscribe()

	ev := RuntimeEvent{
		Record: types.EventRecord{
			EventID:   "evt-001",
			Seq:       1,
			Timestamp: time.Now().UTC(),
			RunID:    "task-001",
			Kind:      types.EventRunStarted,
			Payload:   json.RawMessage(`{}`),
		},
		Actor: ActorRuntime,
		Cause: CauseTaskLifecycle,
	}

	bus.Publish(ev)

	select {
	case received := <-ch:
		if received.Record.EventID != ev.Record.EventID {
			t.Errorf("event_id: got %q, want %q", received.Record.EventID, ev.Record.EventID)
		}
		if received.Actor != ev.Actor {
			t.Errorf("actor: got %q, want %q", received.Actor, ev.Actor)
		}
		if received.Cause != ev.Cause {
			t.Errorf("cause: got %q, want %q", received.Cause, ev.Cause)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}

	bus.Unsubscribe(ch)
}

func TestEventBusMultipleSubscribers(t *testing.T) {
	bus := NewEventBus()
	ch1 := bus.Subscribe()
	ch2 := bus.Subscribe()

	ev := RuntimeEvent{
		Record: types.EventRecord{
			EventID: "evt-002",
			Seq:     1,
			Kind:    types.EventRunSubmitted,
			Payload: json.RawMessage(`{}`),
		},
		Actor: ActorRuntime,
		Cause: CauseTaskLifecycle,
	}

	bus.Publish(ev)

	// Both subscribers should receive the event.
	for i, ch := range []chan RuntimeEvent{ch1, ch2} {
		select {
		case received := <-ch:
			if received.Record.EventID != ev.Record.EventID {
				t.Errorf("subscriber %d: event_id: got %q, want %q", i, received.Record.EventID, ev.Record.EventID)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d: timed out waiting for event", i)
		}
	}

	bus.Unsubscribe(ch1)
	bus.Unsubscribe(ch2)
}

func TestEventBusUnsubscribeStopsDelivery(t *testing.T) {
	bus := NewEventBus()
	ch := bus.Subscribe()
	bus.Unsubscribe(ch)

	ev := RuntimeEvent{
		Record: types.EventRecord{
			EventID: "evt-003",
			Kind:    types.EventRunSubmitted,
			Payload: json.RawMessage(`{}`),
		},
	}

	bus.Publish(ev)

	// Channel is closed after unsubscribe, so we should not receive.
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("received event on unsubscribed channel")
		}
	case <-time.After(100 * time.Millisecond):
		// Expected: no event received because channel was closed.
	}
}

func TestEventBusDropOnFullBuffer(t *testing.T) {
	bus := NewEventBus()
	// Small buffer to force drops.
	ch := bus.SubscribeWithBuffer(1)

	ev := RuntimeEvent{
		Record: types.EventRecord{
			EventID: "evt-drop",
			Kind:    types.EventRunProgress,
			Payload: json.RawMessage(`{}`),
		},
	}

	// Publish many events; the buffer will fill and drops will occur.
	for i := 0; i < 10; i++ {
		bus.Publish(ev)
	}

	pub, drops := bus.Stats()
	if pub != 10 {
		t.Errorf("published: got %d, want 10", pub)
	}
	if drops == 0 {
		t.Error("expected some drops with buffer size 1 and 10 publishes")
	}

	bus.Unsubscribe(ch)
}

func TestEventBusStats(t *testing.T) {
	bus := NewEventBus()

	ev := RuntimeEvent{
		Record: types.EventRecord{
			Kind:    types.EventRunSubmitted,
			Payload: json.RawMessage(`{}`),
		},
	}

	// No subscribers, so all events should be published with 0 drops
	// (no channels to drop to).
	bus.Publish(ev)
	bus.Publish(ev)

	pub, drops := bus.Stats()
	if pub != 2 {
		t.Errorf("published: got %d, want 2", pub)
	}
	if drops != 0 {
		t.Errorf("drops: got %d, want 0 (no subscribers)", drops)
	}
}

func TestEventBusSubscriberCount(t *testing.T) {
	bus := NewEventBus()

	if count := bus.SubscriberCount(); count != 0 {
		t.Errorf("initial count: got %d, want 0", count)
	}

	ch1 := bus.Subscribe()
	if count := bus.SubscriberCount(); count != 1 {
		t.Errorf("after 1 sub: got %d, want 1", count)
	}

	ch2 := bus.Subscribe()
	if count := bus.SubscriberCount(); count != 2 {
		t.Errorf("after 2 subs: got %d, want 2", count)
	}

	bus.Unsubscribe(ch1)
	if count := bus.SubscriberCount(); count != 1 {
		t.Errorf("after unsub 1: got %d, want 1", count)
	}

	bus.Unsubscribe(ch2)
	if count := bus.SubscriberCount(); count != 0 {
		t.Errorf("after unsub 2: got %d, want 0", count)
	}
}

func TestRequiresSupervisorAttention(t *testing.T) {
	tests := []struct {
		name    string
		event   RuntimeEvent
		wantAtt bool
	}{
		{
			name: "provider failure requires attention",
			event: RuntimeEvent{
				Record: types.EventRecord{Kind: types.EventRunFailed},
				Actor:  ActorProvider,
				Cause:  CauseProviderFailure,
			},
			wantAtt: true,
		},
		{
			name: "runtime degraded requires attention",
			event: RuntimeEvent{
				Record: types.EventRecord{Kind: types.EventRuntimeDegraded},
				Actor:  ActorRuntime,
				Cause:  CauseTaskLifecycle,
			},
			wantAtt: true,
		},
		{
			name: "task blocked requires attention",
			event: RuntimeEvent{
				Record: types.EventRecord{Kind: types.EventRunBlocked},
				Actor:  ActorProvider,
				Cause:  CauseProviderFailure,
			},
			wantAtt: true,
		},
		{
			name: "supervisor own action does not require attention",
			event: RuntimeEvent{
				Record: types.EventRecord{Kind: types.EventRunFailed},
				Actor:  ActorSupervisor,
				Cause:  CauseSupervisorRecovery,
			},
			wantAtt: false,
		},
		{
			name: "provider progress is noise for supervisor",
			event: RuntimeEvent{
				Record: types.EventRecord{Kind: types.EventRunProgress},
				Actor:  ActorProvider,
				Cause:  CauseProviderProgress,
			},
			wantAtt: false,
		},
		{
			name: "task submitted is not actionable for supervisor",
			event: RuntimeEvent{
				Record: types.EventRecord{Kind: types.EventRunSubmitted},
				Actor:  ActorRuntime,
				Cause:  CauseTaskLifecycle,
			},
			wantAtt: false,
		},
		{
			name: "host action requires attention",
			event: RuntimeEvent{
				Record: types.EventRecord{Kind: types.EventRuntimeHealth},
				Actor:  ActorHost,
				Cause:  CauseHostAction,
			},
			wantAtt: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.event.RequiresSupervisorAttention()
			if got != tt.wantAtt {
				t.Errorf("RequiresSupervisorAttention() = %v, want %v", got, tt.wantAtt)
			}
		})
	}
}

func TestIsTerminal(t *testing.T) {
	terminalKinds := []types.EventKind{
		types.EventRunCompleted,
		types.EventRunFailed,
		types.EventRunCancelled,
	}
	for _, k := range terminalKinds {
		if !IsTerminal(k) {
			t.Errorf("expected %q to be terminal", k)
		}
	}

	nonTerminalKinds := []types.EventKind{
		types.EventRunSubmitted,
		types.EventRunStarted,
		types.EventRunProgress,
		types.EventRunDelta,
		types.EventRunBlocked,
		types.EventRuntimeHealth,
		types.EventRuntimeDegraded,
	}
	for _, k := range nonTerminalKinds {
		if IsTerminal(k) {
			t.Errorf("expected %q to not be terminal", k)
		}
	}
}

func TestEventBusConcurrentPublish(t *testing.T) {
	bus := NewEventBus()
	ch := bus.SubscribeWithBuffer(256)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				bus.Publish(RuntimeEvent{
					Record: types.EventRecord{
						Kind:    types.EventRunProgress,
						Payload: json.RawMessage(`{}`),
					},
					Actor: ActorRuntime,
					Cause: CauseProviderProgress,
				})
			}
		}()
	}
	wg.Wait()

	pub, _ := bus.Stats()
	if pub != 1000 {
		t.Errorf("published: got %d, want 1000", pub)
	}

	// Drain some events from the channel to verify we got them.
	drained := 0
	for {
		select {
		case <-ch:
			drained++
		default:
			goto done
		}
	}
done:
	if drained == 0 {
		t.Error("expected to drain at least some events")
	}

	bus.Unsubscribe(ch)
}
