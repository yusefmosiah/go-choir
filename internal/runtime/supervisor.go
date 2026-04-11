package runtime

import (
	"context"
	"log"
	"time"

	"github.com/yusefmosiah/go-choir/internal/events"
	"github.com/yusefmosiah/go-choir/internal/types"
)

// Supervisor monitors runtime health and surfaces degraded/recovery signals
// through observable surfaces (VAL-RUNTIME-009). It runs a periodic check
// loop that detects problems, sets health state, and publishes recovery events.
type Supervisor struct {
	rt       *Runtime
	interval time.Duration
	done     chan struct{}
}

// NewSupervisor creates a supervisor for the given runtime.
func NewSupervisor(rt *Runtime, interval time.Duration) *Supervisor {
	return &Supervisor{
		rt:       rt,
		interval: interval,
		done:     make(chan struct{}),
	}
}

// Start begins the supervision loop in a background goroutine.
func (sup *Supervisor) Start(ctx context.Context) {
	go sup.loop(ctx)
	log.Printf("supervisor: started (interval=%s)", sup.interval)
}

// Stop signals the supervisor to stop and waits for it to finish.
// It is safe to call Stop multiple times.
func (sup *Supervisor) Stop() {
	select {
	case <-sup.done:
		// Already stopped.
		return
	default:
		close(sup.done)
	}
	log.Printf("supervisor: stopped")
}

// loop runs the periodic supervision check until context cancellation or
// the done channel is closed.
func (sup *Supervisor) loop(ctx context.Context) {
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
func (sup *Supervisor) checkAndRecover(ctx context.Context) {
	currentHealth := sup.rt.HealthState()

	// Count recent task failures to assess runtime health.
	tasks, err := sup.rt.Store().ListTasksByState(ctx, types.TaskFailed, 10)
	if err != nil {
		log.Printf("supervisor: query failed tasks: %v", err)
		sup.rt.SetHealth(types.HealthDegraded)
		return
	}

	blocked, err := sup.rt.Store().ListTasksByState(ctx, types.TaskBlocked, 10)
	if err != nil {
		log.Printf("supervisor: query blocked tasks: %v", err)
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

		// Emit a supervisor recovery event when transitioning out of
		// degraded/failed state (VAL-RUNTIME-009: recovery is externally
		// visible).
		if newHealth == types.HealthReady && (prev == types.HealthDegraded || prev == types.HealthFailed) {
			sup.rt.EventBus().Publish(events.RuntimeEvent{
				Record: eventRecord(string(events.CauseSupervisorRecovery)),
				Actor:  events.ActorSupervisor,
				Cause:  events.CauseSupervisorRecovery,
			})
			log.Printf("supervisor: recovery complete (%s → %s)", prev, newHealth)
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

func (r *realTicker) C() <-chan time.Time    { return r.t.C }
func (r *realTicker) Stop()                   { r.t.Stop() }

func newTicker(d time.Duration) ticker {
	return &realTicker{t: time.NewTicker(d)}
}
