package runtime

import (
	"context"
	"log"
	"time"

	"github.com/yusefmosiah/go-choir/internal/events"
	"github.com/yusefmosiah/go-choir/internal/types"
)

// Super monitors runtime health and surfaces degraded/recovery signals
// through observable surfaces (VAL-RUNTIME-009). It runs a periodic check
// loop that detects problems, sets health state, and publishes recovery events.
type Super struct {
	rt       *Runtime
	interval time.Duration
	done     chan struct{}
}

// NewSuper creates a super for the given runtime.
func NewSuper(rt *Runtime, interval time.Duration) *Super {
	return &Super{
		rt:       rt,
		interval: interval,
		done:     make(chan struct{}),
	}
}

// NewSupervisor is kept as a compatibility alias while the codebase
// transitions to the shorter "super" naming.
func NewSupervisor(rt *Runtime, interval time.Duration) *Super {
	return NewSuper(rt, interval)
}

// Start begins the supervision loop in a background goroutine.
func (sup *Super) Start(ctx context.Context) {
	go sup.loop(ctx)
	log.Printf("super: started (interval=%s)", sup.interval)
}

// Stop signals the super to stop and waits for it to finish.
// It is safe to call Stop multiple times.
func (sup *Super) Stop() {
	select {
	case <-sup.done:
		// Already stopped.
		return
	default:
		close(sup.done)
	}
	log.Printf("super: stopped")
}

// loop runs the periodic supervision check until context cancellation or
// the done channel is closed.
func (sup *Super) loop(ctx context.Context) {
	ticker := newTicker(sup.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-sup.done:
			return
		case <-ticker.C():
			sup.checkAndRecover(ctx)
		}
	}
}

// checkAndRecover performs one supervision cycle. It assesses the runtime
// health based on recent task outcomes and sets the health state accordingly.
func (sup *Super) checkAndRecover(ctx context.Context) {
	currentHealth := sup.rt.HealthState()

	// Count recent task failures to assess runtime health.
	tasks, err := sup.rt.Store().ListTasksByState(ctx, types.TaskFailed, 10)
	if err != nil {
		log.Printf("super: query failed tasks: %v", err)
		sup.rt.SetHealth(types.HealthDegraded)
		return
	}

	blocked, err := sup.rt.Store().ListTasksByState(ctx, types.TaskBlocked, 10)
	if err != nil {
		log.Printf("super: query blocked tasks: %v", err)
		sup.rt.SetHealth(types.HealthDegraded)
		return
	}

	totalProblems := len(tasks) + len(blocked)

	// Determine new health state based on problem count.
	var newHealth types.RuntimeHealthState
	switch {
	case totalProblems >= 5:
		newHealth = types.HealthFailed
	case totalProblems >= 2:
		newHealth = types.HealthDegraded
	default:
		newHealth = types.HealthReady
	}

	if newHealth != currentHealth {
		prev := currentHealth
		sup.rt.SetHealth(newHealth)

		// Emit a super recovery event when transitioning out of
		// degraded/failed state (VAL-RUNTIME-009: recovery is externally
		// visible).
		if newHealth == types.HealthReady && (prev == types.HealthDegraded || prev == types.HealthFailed) {
			sup.rt.EventBus().Publish(events.RuntimeEvent{
				Record: eventRecord(string(events.CauseSupervisorRecovery)),
				Actor:  events.ActorSupervisor,
				Cause:  events.CauseSupervisorRecovery,
			})
			log.Printf("super: recovery complete (%s → %s)", prev, newHealth)
		}
	}
}

func eventRecord(phase string) types.EventRecord {
	return types.EventRecord{
		Kind:  types.EventRuntimeHealth,
		Phase: phase,
	}
}

// ticker interface allows testing with a mock ticker.
type ticker interface {
	C() <-chan time.Time
	Stop()
}

// realTicker wraps time.Ticker to implement the ticker interface.
type realTicker struct {
	t *time.Ticker
}

func (r *realTicker) C() <-chan time.Time { return r.t.C }
func (r *realTicker) Stop()               { r.t.Stop() }

func newTicker(d time.Duration) ticker {
	return &realTicker{t: time.NewTicker(d)}
}
