package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/yusefmosiah/go-choir/internal/events"
	"github.com/yusefmosiah/go-choir/internal/store"
	"github.com/yusefmosiah/go-choir/internal/types"
)

// Runtime is the core runtime engine that manages task lifecycle, event
// emission, supervision, and health state. It persists all state through
// the store so that task handles and events survive sandbox process restarts
// (VAL-RUNTIME-010).
type Runtime struct {
	cfg      Config
	store    *store.Store
	bus      *events.EventBus
	provider Provider

	mu       sync.Mutex
	health   types.RuntimeHealthState
	running  map[string]context.CancelFunc // task_id → cancel function

	supervisor *Supervisor
	wg         sync.WaitGroup
}

// New creates a new Runtime with the given config, store, event bus, and
// provider. The runtime is idle until Start is called.
func New(cfg Config, s *store.Store, bus *events.EventBus, provider Provider) *Runtime {
	rt := &Runtime{
		cfg:      cfg,
		store:    s,
		bus:      bus,
		provider: provider,
		health:   types.HealthReady,
		running:  make(map[string]context.CancelFunc),
	}
	rt.supervisor = NewSupervisor(rt, cfg.SupervisionInterval)
	return rt
}

// Start begins the supervisor loop and recovers tasks that were interrupted
// by a previous sandbox process exit.
func (rt *Runtime) Start(ctx context.Context) {
	rt.recoverInterruptedTasks(ctx)
	rt.supervisor.Start(ctx)
	log.Printf("runtime: started (sandbox=%s)", rt.cfg.SandboxID)
}

// Stop gracefully shuts down the runtime, cancelling all in-flight tasks
// and stopping the supervisor. It is safe to call Stop multiple times.
func (rt *Runtime) Stop() {
	rt.supervisor.Stop()

	rt.mu.Lock()
	for taskID, cancel := range rt.running {
		cancel()
		delete(rt.running, taskID)
	}
	rt.mu.Unlock()

	rt.wg.Wait()
	log.Printf("runtime: stopped")
}

// SubmitTask creates a new task, persists it, emits a submitted event, and
// begins execution in a goroutine. It returns the task record with the stable
// task ID and initial pending state (VAL-RUNTIME-003).
func (rt *Runtime) SubmitTask(ctx context.Context, prompt, ownerID string) (*types.TaskRecord, error) {
	now := time.Now().UTC()
	rec := &types.TaskRecord{
		TaskID:    uuid.New().String(),
		OwnerID:   ownerID,
		SandboxID: rt.cfg.SandboxID,
		State:     types.TaskPending,
		Prompt:    prompt,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := rt.store.CreateTask(ctx, *rec); err != nil {
		return nil, fmt.Errorf("persist task: %w", err)
	}

	promptLenPayload, _ := json.Marshal(map[string]int{"prompt_length": len(prompt)})
	rt.emitEvent(ctx, rec, types.EventTaskSubmitted, events.CauseTaskLifecycle, promptLenPayload)

	// Begin execution in a goroutine.
	taskCtx, cancel := context.WithCancel(context.Background())
	rt.mu.Lock()
	rt.running[rec.TaskID] = cancel
	rt.mu.Unlock()

	rt.wg.Add(1)
	go rt.executeTask(taskCtx, rec)

	return rec, nil
}

// GetTask returns a task by ID, scoped to the given owner. If the task does
// not exist or does not belong to the owner, it returns ErrNotFound
// (VAL-RUNTIME-006: caller-scoped).
func (rt *Runtime) GetTask(ctx context.Context, taskID, ownerID string) (*types.TaskRecord, error) {
	rec, err := rt.store.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if rec.OwnerID != ownerID {
		return nil, store.ErrNotFound
	}
	return &rec, nil
}

// ListTasksByOwner returns recent tasks for the given owner, ordered by
// creation time descending.
func (rt *Runtime) ListTasksByOwner(ctx context.Context, ownerID string, limit int) ([]types.TaskRecord, error) {
	return rt.store.ListTasksByOwner(ctx, ownerID, limit)
}

// HealthState returns the current runtime health state.
func (rt *Runtime) HealthState() types.RuntimeHealthState {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.health
}

// SetHealth updates the runtime health state. If the state changes, it emits
// a health or degraded event to make the transition externally visible
// (VAL-RUNTIME-001, VAL-RUNTIME-009).
func (rt *Runtime) SetHealth(state types.RuntimeHealthState) {
	rt.mu.Lock()
	prev := rt.health
	rt.health = state
	rt.mu.Unlock()

	if prev == state {
		return
	}

	log.Printf("runtime: health %s → %s", prev, state)

	ctx := context.Background()
	kind := types.EventRuntimeHealth
	cause := events.CauseTaskLifecycle
	if state == types.HealthDegraded || state == types.HealthFailed {
		kind = types.EventRuntimeDegraded
		cause = events.CauseProviderFailure
	}

	payload, _ := json.Marshal(map[string]string{
		"previous": string(prev),
		"current":  string(state),
	})

	rt.bus.Publish(events.RuntimeEvent{
		Record: types.EventRecord{
			EventID:   uuid.New().String(),
			Timestamp: time.Now().UTC(),
			Kind:      kind,
			Payload:   payload,
		},
		Actor: events.ActorSupervisor,
		Cause: cause,
	})

	// Also persist the health event for post-restart recovery visibility.
	var rec types.TaskRecord // runtime-level events have no specific task
	_ = rt.persistEvent(ctx, &rec, kind, payload)
}

// EventBus returns the runtime event bus for SSE subscription.
func (rt *Runtime) EventBus() *events.EventBus {
	return rt.bus
}

// Store returns the runtime store for direct queries.
func (rt *Runtime) Store() *store.Store {
	return rt.store
}

// RunningCount returns the number of currently executing tasks.
func (rt *Runtime) RunningCount() int {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return len(rt.running)
}

// recoverInterruptedTasks finds tasks that were in non-terminal states when
// the runtime previously stopped and resolves them to explicit outcomes
// (VAL-RUNTIME-010).
func (rt *Runtime) recoverInterruptedTasks(ctx context.Context) {
	states := []types.TaskState{types.TaskPending, types.TaskRunning}
	for _, state := range states {
		tasks, err := rt.store.ListTasksByState(ctx, state, 100)
		if err != nil {
			log.Printf("runtime: recovery: query %s tasks: %v", state, err)
			continue
		}
		for i := range tasks {
			rec := &tasks[i]
			now := time.Now().UTC()
			rec.State = types.TaskFailed
			rec.Error = "runtime restarted, task interrupted"
			rec.UpdatedAt = now
			rec.FinishedAt = &now

			if err := rt.store.UpdateTask(ctx, *rec); err != nil {
				log.Printf("runtime: recovery: update task %s: %v", rec.TaskID, err)
				continue
			}
			rt.emitEvent(ctx, rec, types.EventTaskFailed, events.CauseSupervisorRecovery,
				json.RawMessage(`{"recovery":"interrupted_on_restart"}`))
			log.Printf("runtime: recovered task %s (was %s) -> failed", rec.TaskID, state)
		}
	}
}

// executeTask runs a task to completion using the configured provider.
// It transitions the task through pending → running → completed/failed/blocked,
// emitting events at each transition.
func (rt *Runtime) executeTask(ctx context.Context, rec *types.TaskRecord) {
	defer rt.wg.Done()
	defer rt.removeRunning(rec.TaskID)

	now := time.Now().UTC()

	// Transition to running.
	rec.State = types.TaskRunning
	rec.UpdatedAt = now
	if err := rt.store.UpdateTask(ctx, *rec); err != nil {
		log.Printf("runtime: update task %s to running: %v", rec.TaskID, err)
		rt.handleExecutionError(ctx, rec, fmt.Errorf("update task state: %w", err))
		return
	}
	rt.emitEvent(ctx, rec, types.EventTaskStarted, events.CauseTaskLifecycle,
		json.RawMessage(`{}`))

	// Execute through the provider.
	emit := func(kind types.EventKind, phase string, payload json.RawMessage) {
		rt.emitEvent(ctx, rec, kind, events.CauseProviderProgress, payload)
	}

	err := rt.provider.Execute(ctx, rec, emit)
	if err != nil {
		rt.handleExecutionError(ctx, rec, err)
		return
	}

	// Transition to completed.
	now = time.Now().UTC()
	rec.State = types.TaskCompleted
	result := rt.providerResult()
	rec.Result = result
	rec.UpdatedAt = now
	rec.FinishedAt = &now
	if err := rt.store.UpdateTask(ctx, *rec); err != nil {
		log.Printf("runtime: update task %s to completed: %v", rec.TaskID, err)
		return
	}
	resultLenPayload, _ := json.Marshal(map[string]int{"result_length": len(result)})
	rt.emitEvent(ctx, rec, types.EventTaskCompleted, events.CauseTaskLifecycle, resultLenPayload)
}

// handleExecutionError transitions a task to failed/blocked and emits the
// appropriate event. The runtime remains available for later tasks
// (VAL-RUNTIME-008).
func (rt *Runtime) handleExecutionError(ctx context.Context, rec *types.TaskRecord, err error) {
	now := time.Now().UTC()

	// Determine if the failure is recoverable (blocked) or permanent (failed).
	state := types.TaskFailed
	kind := types.EventTaskFailed
	cause := events.CauseProviderFailure

	if ctx.Err() != nil {
		// Context cancellation means the runtime is shutting down, not a
		// provider failure. Treat as cancelled.
		state = types.TaskCancelled
		kind = types.EventTaskCancelled
		cause = events.CauseTaskLifecycle
	}

	rec.State = state
	rec.Error = err.Error()
	rec.UpdatedAt = now
	rec.FinishedAt = &now
	if updateErr := rt.store.UpdateTask(ctx, *rec); updateErr != nil {
		log.Printf("runtime: update task %s to %s: %v", rec.TaskID, state, updateErr)
	}

	errPayload, _ := json.Marshal(map[string]string{"error": err.Error()})
	rt.emitEvent(ctx, rec, kind, cause, errPayload)

	log.Printf("runtime: task %s → %s: %v", rec.TaskID, state, err)
}

// providerResult returns the result text from the provider, if available.
func (rt *Runtime) providerResult() string {
	if sp, ok := rt.provider.(*StubProvider); ok {
		return sp.Result
	}
	return "Task completed."
}

// emitEvent creates and persists an event record, then publishes it on the
// event bus for live streaming.
func (rt *Runtime) emitEvent(ctx context.Context, rec *types.TaskRecord, kind types.EventKind, cause events.EventCause, payload json.RawMessage) {
	evRec := &types.EventRecord{
		EventID:   uuid.New().String(),
		TaskID:    rec.TaskID,
		OwnerID:   rec.OwnerID,
		Timestamp: time.Now().UTC(),
		Kind:      kind,
		Payload:   payload,
	}

	if err := rt.store.AppendEvent(ctx, evRec); err != nil {
		log.Printf("runtime: persist event %s: %v", evRec.EventID, err)
	}

	rt.bus.Publish(events.RuntimeEvent{
		Record: *evRec,
		Actor:  events.ActorRuntime,
		Cause:  cause,
	})
}

// persistEvent persists an event record without publishing it on the bus.
// Used for recovery events that may have occurred before subscribers connect.
func (rt *Runtime) persistEvent(ctx context.Context, rec *types.TaskRecord, kind types.EventKind, payload json.RawMessage) error {
	evRec := &types.EventRecord{
		EventID:   uuid.New().String(),
		TaskID:    rec.TaskID,
		OwnerID:   rec.OwnerID,
		Timestamp: time.Now().UTC(),
		Kind:      kind,
		Payload:   payload,
	}
	return rt.store.AppendEvent(ctx, evRec)
}

// removeRunning removes a task from the running map.
func (rt *Runtime) removeRunning(taskID string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	delete(rt.running, taskID)
}
