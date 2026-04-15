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

	mu      sync.Mutex
	health  types.RuntimeHealthState
	running map[string]context.CancelFunc // task_id → cancel function

	super        *Super
	wg           sync.WaitGroup
	toolRegistry *ToolRegistry
	toolProfiles map[string]*ToolRegistry
	channelMgr   *ChannelManager
}

// New creates a new Runtime with the given config, store, event bus, and
// provider. The runtime is idle until Start is called.
// If a tool registry is provided, the runtime will use the tool-calling
// loop for task execution instead of the simple provider bridge path.
func New(cfg Config, s *store.Store, bus *events.EventBus, provider Provider, opts ...RuntimeOption) *Runtime {
	rt := &Runtime{
		cfg:        cfg,
		store:      s,
		bus:        bus,
		provider:   provider,
		health:     types.HealthReady,
		running:    make(map[string]context.CancelFunc),
		channelMgr: NewChannelManager(),
	}
	for _, opt := range opts {
		opt(rt)
	}
	rt.super = NewSuper(rt, cfg.SupervisionInterval)
	return rt
}

// RuntimeOption configures optional Runtime components.
type RuntimeOption func(*Runtime)

// WithToolRegistry sets the tool registry for the runtime. When a tool
// registry is provided, the runtime uses the tool-calling loop instead
// of the simple provider bridge path for task execution.
func WithToolRegistry(registry *ToolRegistry) RuntimeOption {
	return func(rt *Runtime) {
		rt.toolRegistry = registry
	}
}

// WithChannelManager sets a custom channel manager for the runtime.
// If not called, a default empty channel manager is created.
func WithChannelManager(mgr *ChannelManager) RuntimeOption {
	return func(rt *Runtime) {
		rt.channelMgr = mgr
	}
}

// Start begins the supervisor loop and recovers tasks that were interrupted
// by a previous sandbox process exit.
func (rt *Runtime) Start(ctx context.Context) {
	rt.recoverInterruptedTasks(ctx)
	rt.super.Start(ctx)
	log.Printf("runtime: started (sandbox=%s)", rt.cfg.SandboxID)
}

// Stop gracefully shuts down the runtime, cancelling all in-flight tasks
// and stopping the supervisor. It is safe to call Stop multiple times.
func (rt *Runtime) Stop() {
	rt.super.Stop()

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
	return rt.SubmitTaskWithMetadata(ctx, prompt, ownerID, nil)
}

// SubmitTaskWithMetadata creates a new task with the given metadata, persists
// it, emits a submitted event, and begins execution in a goroutine. Metadata
// is used to carry feature-specific context (e.g., vtext agent revision info).
func (rt *Runtime) SubmitTaskWithMetadata(ctx context.Context, prompt, ownerID string, metadata map[string]any) (*types.TaskRecord, error) {
	now := time.Now().UTC()
	if metadata == nil {
		metadata = make(map[string]any)
	}
	rec := &types.TaskRecord{
		TaskID:    uuid.New().String(),
		OwnerID:   ownerID,
		SandboxID: rt.cfg.SandboxID,
		State:     types.TaskPending,
		Prompt:    prompt,
		CreatedAt: now,
		UpdatedAt: now,
		Metadata:  metadata,
	}
	if _, ok := rec.Metadata[taskMetadataAgentID]; !ok {
		rec.Metadata[taskMetadataAgentID] = rec.TaskID
	}
	if _, ok := rec.Metadata[taskMetadataWorkID]; !ok {
		rec.Metadata[taskMetadataWorkID] = rec.TaskID
	}
	if _, ok := rec.Metadata[taskMetadataAgentRole]; !ok {
		rec.Metadata[taskMetadataAgentRole] = agentProfileForTask(rec)
	}

	if err := rt.store.CreateTask(ctx, *rec); err != nil {
		return nil, fmt.Errorf("persist task: %w", err)
	}

	promptLenPayload, _ := json.Marshal(map[string]int{"prompt_length": len(prompt)})
	rt.emitEvent(ctx, rec, types.EventTaskSubmitted, events.CauseTaskLifecycle, promptLenPayload)

	// Begin execution in a goroutine. Use a copy of the record to avoid
	// racing with the caller (the returned rec must retain TaskPending).
	taskRec := *rec

	taskCtx, cancel := context.WithCancel(context.Background())
	rt.mu.Lock()
	rt.running[rec.TaskID] = cancel
	rt.mu.Unlock()

	rt.wg.Add(1)
	go rt.executeTask(taskCtx, &taskRec)

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

// SpawnTask creates a child task linked to a parent task. It validates that
// the parent task exists, creates both a runtime TaskRecord and a WorkItem
// in the registry for parent-child tracking, and begins execution in a
// goroutine (VAL-CHOIR-001, VAL-CHOIR-004).
//
// The child task inherits the owner from the ownerID parameter (derived from
// auth context). Constraints are stored in the task metadata for use during
// execution.
func (rt *Runtime) SpawnTask(ctx context.Context, parentID, objective, ownerID string, constraints map[string]any) (*types.TaskRecord, error) {
	// Validate that the parent task exists.
	_, err := rt.store.GetTask(ctx, parentID)
	if err != nil {
		if err == store.ErrNotFound {
			return nil, fmt.Errorf("parent task not found: %s", parentID)
		}
		return nil, fmt.Errorf("lookup parent task: %w", err)
	}

	now := time.Now().UTC()

	// Build metadata from constraints and parent reference.
	metadata := map[string]any{
		"parent_id":  parentID,
		"spawned_by": ownerID,
	}
	for k, v := range constraints {
		metadata[k] = v
	}

	taskID := uuid.New().String()

	// Create the runtime task record.
	rec := &types.TaskRecord{
		TaskID:    taskID,
		OwnerID:   ownerID,
		SandboxID: rt.cfg.SandboxID,
		State:     types.TaskPending,
		Prompt:    objective,
		CreatedAt: now,
		UpdatedAt: now,
		Metadata:  metadata,
	}
	if _, ok := rec.Metadata[taskMetadataAgentID]; !ok {
		rec.Metadata[taskMetadataAgentID] = rec.TaskID
	}
	if _, ok := rec.Metadata[taskMetadataWorkID]; !ok {
		rec.Metadata[taskMetadataWorkID] = rec.TaskID
	}
	if _, ok := rec.Metadata[taskMetadataAgentRole]; !ok {
		rec.Metadata[taskMetadataAgentRole] = agentProfileForTask(rec)
	}

	if err := rt.store.CreateTask(ctx, *rec); err != nil {
		return nil, fmt.Errorf("persist spawned task: %w", err)
	}

	// Create the work item in the registry for parent-child tracking.
	workItem := types.WorkItem{
		ID:        taskID,
		ParentID:  parentID,
		OwnerID:   ownerID,
		Objective: objective,
		State:     types.TaskPending,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := rt.store.CreateWorkItem(ctx, workItem); err != nil {
		return nil, fmt.Errorf("persist work item: %w", err)
	}

	// Emit submitted event.
	objectiveLenPayload, _ := json.Marshal(map[string]any{
		"prompt_length": len(objective),
		"parent_id":     parentID,
	})
	rt.emitEvent(ctx, rec, types.EventTaskSubmitted, events.CauseTaskLifecycle, objectiveLenPayload)

	// Begin execution in a goroutine. Use a copy of the record to avoid
	// racing with the caller (the returned rec must retain TaskPending).
	taskRec := *rec

	taskCtx, cancel := context.WithCancel(context.Background())
	rt.mu.Lock()
	rt.running[rec.TaskID] = cancel
	rt.mu.Unlock()

	rt.wg.Add(1)
	go rt.executeTask(taskCtx, &taskRec)

	log.Printf("runtime: spawned child task %s for parent %s (owner=%s)", taskID, parentID, ownerID)

	// Ensure channels exist for both parent and child, enabling immediate
	// bidirectional communication (VAL-CHOIR-006). Children post results to
	// the parent's channel; parents can read/wait on it.
	if err := rt.channelMgr.ensureParentChildChannels(parentID, taskID); err != nil {
		log.Printf("runtime: ensure channels for spawned task %s: %v", taskID, err)
		// Non-fatal: channels will be created lazily on first access.
	}

	return rec, nil
}

// CancelTask cancels a running or pending task. It validates that the task
// exists and belongs to the given owner, then cancels the task's context
// and transitions it to cancelled state (VAL-CHOIR-010).
//
// Returns an error if:
//   - the task does not exist
//   - the task belongs to a different owner
//   - the task is already in a terminal state
func (rt *Runtime) CancelTask(ctx context.Context, taskID, ownerID string) error {
	rec, err := rt.store.GetTask(ctx, taskID)
	if err != nil {
		if err == store.ErrNotFound {
			return fmt.Errorf("task not found: %s", taskID)
		}
		return fmt.Errorf("lookup task: %w", err)
	}

	// Ownership check.
	if rec.OwnerID != ownerID {
		return store.ErrNotFound
	}

	// Only running or pending tasks can be cancelled.
	if rec.State.Terminal() {
		return fmt.Errorf("cannot cancel task in %s state", rec.State)
	}

	// Cancel the task's execution context.
	rt.mu.Lock()
	cancel, ok := rt.running[taskID]
	if ok {
		cancel()
		delete(rt.running, taskID)
	}
	rt.mu.Unlock()

	if !ok {
		// Task was not running in this process (e.g., pending or recovered).
		// Transition it directly to cancelled.
		now := time.Now().UTC()
		rec.State = types.TaskCancelled
		rec.UpdatedAt = now
		rec.FinishedAt = &now
		if err := rt.store.UpdateTask(ctx, rec); err != nil {
			return fmt.Errorf("update cancelled task: %w", err)
		}

		errPayload, _ := json.Marshal(map[string]string{"error": "task cancelled"})
		rt.emitEvent(ctx, &rec, types.EventTaskCancelled, events.CauseTaskLifecycle, errPayload)

		// Update work item state.
		rt.updateWorkItemState(ctx, taskID, types.TaskCancelled, "", "task cancelled")
	}

	return nil
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

// ToolRegistry returns the runtime's tool registry, or nil if none is configured.
func (rt *Runtime) ToolRegistry() *ToolRegistry {
	return rt.toolRegistry
}

// ChannelManager returns the runtime's channel manager.
func (rt *Runtime) ChannelManager() *ChannelManager {
	return rt.channelMgr
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
//
// When a tool registry is configured, the task executes through the real
// tool-calling loop (RunToolLoop), which handles tool_use stop reasons by
// invoking registered Go function-call tools and feeding results back to the
// provider. When no tool registry is configured, the task uses the simpler
// Provider.Execute path (stub or bridge provider).
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

	// Update work item state to running if this is a spawned child task.
	rt.updateWorkItemState(ctx, rec.TaskID, types.TaskRunning, "", "")

	rt.emitEvent(ctx, rec, types.EventTaskStarted, events.CauseTaskLifecycle,
		json.RawMessage(`{}`))

	emit := func(kind types.EventKind, phase string, payload json.RawMessage) {
		cause := events.CauseProviderProgress
		if kind == types.EventToolInvoked || kind == types.EventToolResult {
			cause = events.CauseToolExecution
		}
		// Also emit vtext-specific progress events for agent revision tasks.
		if taskType, _ := rec.Metadata["type"].(string); taskType == "vtext_agent_revision" {
			if docID, _ := rec.Metadata["doc_id"].(string); docID != "" {
				if kind == types.EventTaskProgress {
					progressPayload, _ := json.Marshal(map[string]string{
						"doc_id":  docID,
						"task_id": rec.TaskID,
						"phase":   phase,
					})
					rt.emitVTextAgentEvent(ctx, rec, types.EventVTextAgentRevisionProgress,
						events.CauseProviderProgress, progressPayload)
				}
			}
		}
		rt.emitEvent(ctx, rec, kind, cause, payload)
	}

	registry := rt.toolRegistryForTask(rec)

	// Use the tool-calling loop if a tool registry is configured and the
	// provider supports the ToolLoopProvider interface. Otherwise, fall back
	// to the simple Provider.Execute path.
	if registry != nil && registry.Size() > 0 {
		rt.executeWithToolLoop(ctx, rec, registry, emit)
	} else {
		rt.executeWithProvider(ctx, rec, emit)
	}
}

// executeWithToolLoop runs the task through the real tool-calling loop.
// This is the primary execution path when a tool registry is configured,
// enabling the LLM to invoke registered Go function-call tools.
func (rt *Runtime) executeWithToolLoop(ctx context.Context, rec *types.TaskRecord, registry *ToolRegistry, emit EventEmitFunc) {
	tlp := asToolLoopProvider(rt.provider)

	// Build the initial conversation from the task prompt.
	initialMessages := []json.RawMessage{}
	userMsg, _ := json.Marshal(map[string]any{
		"role": "user",
		"content": []any{
			map[string]string{"type": "text", "text": rec.Prompt},
		},
	})
	initialMessages = append(initialMessages, userMsg)

	systemPrompt := systemPromptForTask(rec)
	ctx = WithToolExecutionContext(ctx, rec)

	text, usage, err := RunToolLoop(ctx, tlp, registry, initialMessages, systemPrompt, 4096, emit)
	if err != nil {
		if ctx.Err() != nil {
			rt.handleExecutionError(ctx, rec, ctx.Err())
		} else {
			rt.handleExecutionError(ctx, rec, err)
		}
		return
	}

	// Transition to completed.
	now := time.Now().UTC()
	rec.State = types.TaskCompleted
	rec.Result = text
	rec.UpdatedAt = now
	rec.FinishedAt = &now

	// Store token usage in metadata.
	if rec.Metadata == nil {
		rec.Metadata = make(map[string]any)
	}
	rec.Metadata["input_tokens"] = usage.InputTokens
	rec.Metadata["output_tokens"] = usage.OutputTokens

	// For vtext agent revision tasks, create the canonical revision and emit the
	// vtext completion event before the task is surfaced as completed. This keeps
	// task completion aligned with document-version availability.
	rt.handleTaskCompletion(ctx, rec)

	// Use a background context for post-provider persistence so that a fast
	// shutdown or cancellation after the provider returns cannot drop the
	// completed-task transition or parent notification.
	persistCtx := context.Background()

	if err := rt.store.UpdateTask(persistCtx, *rec); err != nil {
		log.Printf("runtime: update task %s to completed: %v", rec.TaskID, err)
		return
	}
	resultLenPayload, _ := json.Marshal(map[string]any{
		"result_length": len(text),
		"input_tokens":  usage.InputTokens,
		"output_tokens": usage.OutputTokens,
	})
	rt.emitEvent(persistCtx, rec, types.EventTaskCompleted, events.CauseTaskLifecycle, resultLenPayload)

	// Update work item state for spawned child tasks (VAL-CHOIR-008).
	rt.updateWorkItemState(persistCtx, rec.TaskID, types.TaskCompleted, rec.Result, "")

	// Notify parent channel of child task completion (VAL-CHOIR-006, VAL-CHOIR-008).
	rt.notifyParent(persistCtx, rec)

}

// executeWithProvider runs the task through the simple Provider.Execute path.
// This is the legacy execution path used when no tool registry is configured
// (stub provider or bridge provider without tool-calling support).
func (rt *Runtime) executeWithProvider(ctx context.Context, rec *types.TaskRecord, emit EventEmitFunc) {
	// Execute through the provider. The provider may set rec.Result
	// directly (e.g., BridgeProvider sets it from the LLM response text).
	err := rt.provider.Execute(ctx, rec, emit)
	if err != nil {
		rt.handleExecutionError(ctx, rec, err)
		return
	}

	// Transition to completed.
	now := time.Now().UTC()
	rec.State = types.TaskCompleted
	result := rec.Result
	if result == "" {
		result = rt.providerResult()
	}
	rec.Result = result
	rec.UpdatedAt = now
	rec.FinishedAt = &now

	// For vtext agent revision tasks, create the canonical revision and emit the
	// vtext completion event before the task is surfaced as completed. This keeps
	// task completion aligned with document-version availability.
	rt.handleTaskCompletion(ctx, rec)

	// Use a background context for post-provider persistence so that a fast
	// shutdown or cancellation after the provider returns cannot drop the
	// completed-task transition or parent notification.
	persistCtx := context.Background()

	if err := rt.store.UpdateTask(persistCtx, *rec); err != nil {
		log.Printf("runtime: update task %s to completed: %v", rec.TaskID, err)
		return
	}
	resultLenPayload, _ := json.Marshal(map[string]int{"result_length": len(result)})
	rt.emitEvent(persistCtx, rec, types.EventTaskCompleted, events.CauseTaskLifecycle, resultLenPayload)

	// Update work item state for spawned child tasks (VAL-CHOIR-008).
	rt.updateWorkItemState(persistCtx, rec.TaskID, types.TaskCompleted, rec.Result, "")

	// Notify parent channel of child task completion (VAL-CHOIR-006, VAL-CHOIR-008).
	rt.notifyParent(persistCtx, rec)

}

// handleTaskCompletion processes feature-specific side effects after a task
// completes successfully. For vtext agent revision tasks, it creates the
// canonical appagent-authored revision and emits the completion event
// (VAL-ETEXT-003, VAL-CROSS-120).
//
// It uses the agent mutation table to ensure that only one canonical revision
// is created per mutation, even if the completion is processed multiple times
// (e.g., after crash recovery). This prevents duplicate canonical revisions
// (VAL-CROSS-122).
func (rt *Runtime) handleTaskCompletion(ctx context.Context, rec *types.TaskRecord) {
	taskType, _ := rec.Metadata["type"].(string)
	if taskType != "vtext_agent_revision" {
		return
	}

	// Use a background context for post-completion persistence so the canonical
	// revision and completion event survive a fast runtime shutdown that
	// cancels the task context immediately after the provider returns.
	persistCtx := context.Background()

	docID, _ := rec.Metadata["doc_id"].(string)
	if docID == "" {
		log.Printf("runtime: vtext agent revision task %s: missing doc_id in metadata", rec.TaskID)
		return
	}

	// Check the agent mutation record to ensure we don't create a duplicate
	// canonical revision (VAL-CROSS-122).
	mutation, err := rt.store.GetAgentMutationByTask(persistCtx, rec.TaskID)
	if err != nil {
		log.Printf("runtime: vtext agent revision task %s: get mutation: %v", rec.TaskID, err)
		return
	}
	if mutation == nil {
		log.Printf("runtime: vtext agent revision task %s: no mutation record found", rec.TaskID)
		return
	}
	if mutation.State == "completed" {
		// Already created the revision — idempotent skip.
		log.Printf("runtime: vtext agent revision task %s: mutation already completed, skipping", rec.TaskID)
		return
	}

	ownerID := rec.OwnerID
	content := rec.Result
	if content == "" {
		content = "(agent revision produced no content)"
	}

	// Get the current document state for the parent revision ID.
	doc, err := rt.store.GetDocument(persistCtx, docID, ownerID)
	if err != nil {
		log.Printf("runtime: vtext agent revision task %s: get document: %v", rec.TaskID, err)
		_ = rt.store.FailAgentMutation(persistCtx, rec.TaskID)
		return
	}

	parentID := doc.CurrentRevisionID
	// Also check if the metadata has a specific current_revision_id from
	// when the revision was requested. If the document has been edited
	// since then, we still use the document's current head as the parent
	// because the agent revision should be based on the latest state.
	if parentID == "" {
		if metaParentID, ok := rec.Metadata["current_revision_id"].(string); ok && metaParentID != "" {
			parentID = metaParentID
		}
	}

	now := time.Now().UTC()
	rev := types.Revision{
		RevisionID:       uuid.New().String(),
		DocID:            docID,
		OwnerID:          ownerID,
		AuthorKind:       types.AuthorAppAgent,
		AuthorLabel:      "appagent",
		Content:          content,
		Citations:        json.RawMessage("[]"),
		Metadata:         json.RawMessage(`{"source":"agent_revision","task_id":"` + rec.TaskID + `"}`),
		ParentRevisionID: parentID,
		CreatedAt:        now,
	}

	if err := rt.store.CreateRevision(persistCtx, rev); err != nil {
		log.Printf("runtime: vtext agent revision task %s: create revision: %v", rec.TaskID, err)
		_ = rt.store.FailAgentMutation(persistCtx, rec.TaskID)
		return
	}

	// Mark the mutation as completed. If this fails because it's already
	// completed, the revision was already created — no duplicate
	// (VAL-CROSS-122).
	if err := rt.store.CompleteAgentMutation(persistCtx, rec.TaskID, rev.RevisionID); err != nil {
		if err == store.ErrMutationAlreadyCompleted {
			log.Printf("runtime: vtext agent revision task %s: mutation already completed (race condition), revision %s is the canonical one", rec.TaskID, rev.RevisionID)
		} else {
			log.Printf("runtime: vtext agent revision task %s: complete mutation: %v", rec.TaskID, err)
		}
	}

	// Emit the vtext-specific agent revision completed event with doc_id
	// and revision_id so the frontend can correlate to the open document
	// (VAL-ETEXT-004).
	completedPayload, _ := json.Marshal(map[string]string{
		"doc_id":      docID,
		"revision_id": rev.RevisionID,
		"task_id":     rec.TaskID,
	})
	rt.emitVTextAgentEvent(persistCtx, rec, types.EventVTextAgentRevisionCompleted,
		events.CauseTaskLifecycle, completedPayload)

	log.Printf("runtime: vtext agent revision task %s: created canonical revision %s for doc %s", rec.TaskID, rev.RevisionID, docID)
}

// handleExecutionError transitions a task to failed/blocked and emits the
// appropriate event. The runtime remains available for later tasks
// (VAL-RUNTIME-008).
//
// Note: When the error is caused by context cancellation (runtime shutdown),
// the passed ctx will be cancelled. We use context.Background() for the
// critical store updates so that the task state is properly persisted even
// during shutdown (VAL-CHOIR-009, VAL-CHOIR-010).
func (rt *Runtime) handleExecutionError(ctx context.Context, rec *types.TaskRecord, err error) {
	now := time.Now().UTC()

	// Determine if the failure is recoverable (blocked) or permanent (failed).
	state := types.TaskFailed
	kind := types.EventTaskFailed
	cause := events.CauseProviderFailure

	if ctx.Err() != nil {
		// Context cancellation means the runtime is shutting down or the
		// task was cancelled, not a provider failure. Treat as cancelled.
		state = types.TaskCancelled
		kind = types.EventTaskCancelled
		cause = events.CauseTaskLifecycle
	}

	rec.State = state
	rec.Error = err.Error()
	rec.UpdatedAt = now
	rec.FinishedAt = &now

	// Use background context for persistence so that cancelled-task state
	// transitions are persisted even when the task context is cancelled.
	persistCtx := context.Background()
	if updateErr := rt.store.UpdateTask(persistCtx, *rec); updateErr != nil {
		log.Printf("runtime: update task %s to %s: %v", rec.TaskID, state, updateErr)
	}

	errPayload, _ := json.Marshal(map[string]string{"error": err.Error()})
	rt.emitEvent(persistCtx, rec, kind, cause, errPayload)

	// If this is an vtext agent revision task, mark the mutation as failed
	// and emit the vtext-specific failure event.
	if taskType, _ := rec.Metadata["type"].(string); taskType == "vtext_agent_revision" {
		_ = rt.store.FailAgentMutation(persistCtx, rec.TaskID)
		if docID, _ := rec.Metadata["doc_id"].(string); docID != "" {
			failPayload, _ := json.Marshal(map[string]string{
				"doc_id":  docID,
				"task_id": rec.TaskID,
				"error":   err.Error(),
			})
			rt.emitVTextAgentEvent(persistCtx, rec, types.EventVTextAgentRevisionFailed,
				events.CauseProviderFailure, failPayload)
		}
	}

	log.Printf("runtime: task %s → %s: %v", rec.TaskID, state, err)

	// Update work item state for spawned child tasks (VAL-CHOIR-008).
	rt.updateWorkItemState(persistCtx, rec.TaskID, state, "", err.Error())

	// Notify parent channel of child task failure (VAL-CHOIR-006, VAL-CHOIR-009).
	if state == types.TaskFailed || state == types.TaskCancelled {
		rt.notifyParent(persistCtx, rec)
	}
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

// updateWorkItemState updates the work item state in the registry if the task
// is a spawned child (has a work item record). It silently skips if no work
// item exists (e.g., for root tasks submitted via SubmitTask). This keeps the
// work registry in sync with the runtime task lifecycle
// (VAL-CHOIR-001, VAL-CHOIR-003, VAL-CHOIR-008).
func (rt *Runtime) updateWorkItemState(ctx context.Context, taskID string, state types.TaskState, result, errMsg string) {
	item, err := rt.store.GetWorkItem(ctx, taskID)
	if err != nil {
		// Not a spawned task — no work item. This is normal for root tasks.
		return
	}

	now := time.Now().UTC()
	item.State = state
	item.UpdatedAt = now

	if result != "" {
		item.Result = result
	}
	if errMsg != "" {
		item.Error = errMsg
	}

	if err := rt.store.UpdateWorkItem(ctx, item); err != nil {
		log.Printf("runtime: update work item %s to %s: %v", taskID, state, err)
	}
}

// notifyParent posts a result or error message to the parent's channel when
// a spawned child task reaches a terminal state. This enables the parent to
// collect results from all its children via channels
// (VAL-CHOIR-006, VAL-CHOIR-008).
//
// If the task has no parent_id in metadata, this is a no-op.
func (rt *Runtime) notifyParent(ctx context.Context, rec *types.TaskRecord) {
	parentID, _ := rec.Metadata["parent_id"].(string)
	if parentID == "" {
		return
	}

	switch rec.State {
	case types.TaskCompleted:
		result := rec.Result
		if result == "" {
			result = "(task completed with no result)"
		}
		if _, err := rt.PostChildResult(ctx, parentID, rec.TaskID, result); err != nil {
			log.Printf("runtime: notify parent %s of child %s completion: %v", parentID, rec.TaskID, err)
		}
	case types.TaskFailed:
		errMsg := rec.Error
		if errMsg == "" {
			errMsg = "(task failed with no error message)"
		}
		if _, err := rt.PostChildError(ctx, parentID, rec.TaskID, errMsg); err != nil {
			log.Printf("runtime: notify parent %s of child %s failure: %v", parentID, rec.TaskID, err)
		}
	}
}
