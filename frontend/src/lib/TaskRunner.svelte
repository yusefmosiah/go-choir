<!--
  TaskRunner — shell prompt and runtime loop progress UI.

  Submits prompts through POST /api/agent/loop, renders loop/status/event
  progress, returns the real provider-backed answer, and supports
  reattachment across reload/new-tab without resubmitting (VAL-CROSS-121).

  Renewal-safe submission (VAL-CROSS-111):
    - submitLoop() uses fetchWithRenewal so expired access tokens are
      silently renewed before retry.
    - The client-side active-loop guard prevents duplicate submission
      even if renewal causes a second fetch attempt.

  Reattachment (VAL-CROSS-121):
    - On mount, reattachToActiveLoop() checks sessionStorage for an
      in-flight loop handle and resumes progress instead of resubmitting.
    - The loop handle is cleared from sessionStorage when the loop
      reaches a terminal state.

  Data attributes for test targeting:
    data-task-runner           — root container
    data-prompt-input          — prompt text input
    data-prompt-submit         — submit button
    data-task-status           — status indicator section
    data-task-id               — task ID display
    data-task-state            — task state badge
    data-task-result           — result text container
    data-task-error            — error message container
    data-task-events           — event log container
    data-event-item            — individual event entry
-->
<script>
  import { createEventDispatcher } from 'svelte';
  import {
    submitLoop,
    fetchLoopStatus,
    connectEventStream,
    reattachToActiveLoop,
    clearActiveLoop,
    isTerminalState,
  } from './runtime.js';
  import { AuthRequiredError } from './auth.js';

  const dispatch = createEventDispatcher();

  /** Current prompt text. */
  let promptText = '';

  /** Whether a submission is in progress. */
  let submitting = false;

  /** The current loop ID (stable handle). */
  let currentLoopId = '';

  /** The current loop state. */
  let currentLoopState = '';

  /** The loop result text (populated on completion). */
  let taskResult = '';

  /** Loop error message (populated on failure). */
  let taskError = '';

  /** Collected events for the current loop. */
  let taskEvents = [];

  /** Whether we are reattaching to an in-flight loop. */
  let reattaching = false;

  /** Event stream handle. */
  let eventStreamHandle = null;

  /** Status polling interval handle. */
  let statusPollInterval = null;

  /** Submission error message. */
  let submissionError = '';

  // ---- Lifecycle ----

  import { onMount } from 'svelte';
  import { onDestroy } from 'svelte';

  onMount(async () => {
    // Attempt reattachment to any in-flight loop (VAL-CROSS-121).
    try {
      reattaching = true;
      const status = await reattachToActiveLoop();
      if (status && !isTerminalState(status.state)) {
        // Found an in-flight loop — resume tracking it.
        currentLoopId = status.loop_id;
        currentLoopState = status.state;
        taskResult = status.result || '';
        taskError = status.error || '';
        promptText = status.prompt || '';
        startEventStream();
        startStatusPolling();
      } else if (status && isTerminalState(status.state)) {
        // Loop already finished — show the result.
        currentLoopId = status.loop_id;
        currentLoopState = status.state;
        taskResult = status.result || '';
        taskError = status.error || '';
        promptText = status.prompt || '';
      }
    } catch (_err) {
      // Reattachment failed — start fresh.
    } finally {
      reattaching = false;
    }
  });

  onDestroy(() => {
    cleanup();
  });

  function cleanup() {
    if (eventStreamHandle) {
      eventStreamHandle.close();
      eventStreamHandle = null;
    }
    if (statusPollInterval) {
      clearInterval(statusPollInterval);
      statusPollInterval = null;
    }
  }

  // ---- Loop submission ----

  async function handleSubmit() {
    const trimmed = promptText.trim();
    if (!trimmed || submitting) return;

    submissionError = '';
    taskError = '';
    taskResult = '';
    taskEvents = [];
    submitting = true;

    try {
      const loopInfo = await submitLoop(trimmed);

      currentLoopId = loopInfo.loop_id;
      currentLoopState = loopInfo.state;

      // Start tracking progress.
      startEventStream();
      startStatusPolling();
    } catch (err) {
      if (err instanceof AuthRequiredError) {
        dispatch('authexpired');
        return;
      }
      submissionError = err.message || 'Loop submission failed';
    } finally {
      submitting = false;
    }
  }

  function handleKeydown(event) {
    if (event.key === 'Enter' && !event.shiftKey) {
      event.preventDefault();
      handleSubmit();
    }
  }

  // ---- Event stream ----

  function startEventStream() {
    // Clean up any existing stream.
    if (eventStreamHandle) {
      eventStreamHandle.close();
    }

    let lastSeq = 0;
    // If we have events already, use the last seq for catch-up.
    if (taskEvents.length > 0) {
      lastSeq = taskEvents[taskEvents.length - 1].seq || 0;
    }

    eventStreamHandle = connectEventStream(
      (event) => {
        // Only track events for the current loop.
        const eventLoopId = event.loop_id || '';
        if (currentLoopId && eventLoopId && eventLoopId !== currentLoopId) {
          return;
        }

        // Avoid duplicate events (SSE catch-up may replay).
        if (event.seq && taskEvents.some((e) => e.seq === event.seq)) {
          return;
        }

        taskEvents = [...taskEvents, event];

        // Update state from events if the event carries state info.
        if (event.kind === 'loop.started') {
          currentLoopState = 'running';
        } else if (event.kind === 'loop.completed') {
          currentLoopState = 'completed';
          handleTaskComplete(event);
        } else if (event.kind === 'loop.failed') {
          currentLoopState = 'failed';
          handleTaskFailed(event);
        } else if (event.kind === 'loop.cancelled') {
          currentLoopState = 'cancelled';
          handleTaskTerminal();
        }
      },
      { afterSeq: lastSeq > 0 ? lastSeq : undefined },
    );
  }

  // ---- Status polling ----

  function startStatusPolling() {
    if (statusPollInterval) {
      clearInterval(statusPollInterval);
    }

    statusPollInterval = setInterval(async () => {
      if (!currentLoopId) return;

      try {
        const status = await fetchLoopStatus(currentLoopId);
        currentLoopState = status.state;

        if (status.result) {
          taskResult = status.result;
        }
        if (status.error) {
          taskError = status.error;
        }

        if (isTerminalState(status.state)) {
          // Loop reached terminal state — stop polling and clean up.
          clearInterval(statusPollInterval);
          statusPollInterval = null;

          if (eventStreamHandle) {
            eventStreamHandle.close();
            eventStreamHandle = null;
          }

          // Clear the stored loop handle (VAL-CROSS-121).
          clearActiveLoop();
        }
      } catch (_err) {
        // Status poll failed — may be transient. Keep polling.
      }
    }, 2000);
  }

  // ---- Terminal state handlers ----

  function handleTaskComplete(event) {
    // Extract result from the event payload if available.
    try {
      const payload = typeof event.payload === 'string' ? JSON.parse(event.payload) : event.payload;
      if (payload && payload.result_length !== undefined) {
        // Result length is known, but actual text comes from status poll.
      }
    } catch (_err) {
      // Ignore parse errors.
    }

    // Do a final status fetch to get the full result text.
    if (currentLoopId) {
      fetchLoopStatus(currentLoopId)
        .then((status) => {
          if (status.result) taskResult = status.result;
          if (status.error) taskError = status.error;
          currentLoopState = status.state;
        })
        .catch(() => {});
    }

    handleTaskTerminal();
  }

  function handleTaskFailed(event) {
    try {
      const payload = typeof event.payload === 'string' ? JSON.parse(event.payload) : event.payload;
      if (payload && payload.error) {
        taskError = payload.error;
      }
    } catch (_err) {
      // Ignore.
    }
    handleTaskTerminal();
  }

  function handleTaskTerminal() {
    // Clear the stored task handle so a new submission is allowed.
    clearActiveLoop();

    if (eventStreamHandle) {
      eventStreamHandle.close();
      eventStreamHandle = null;
    }
    if (statusPollInterval) {
      clearInterval(statusPollInterval);
      statusPollInterval = null;
    }
  }

  // ---- Helpers ----

  function stateLabel(state) {
    switch (state) {
      case 'pending': return 'Pending';
      case 'running': return 'Running';
      case 'completed': return 'Completed';
      case 'failed': return 'Failed';
      case 'cancelled': return 'Cancelled';
      case 'blocked': return 'Blocked';
      default: return state || 'Idle';
    }
  }

  function stateClass(state) {
    switch (state) {
      case 'pending': return 'state-pending';
      case 'running': return 'state-running';
      case 'completed': return 'state-completed';
      case 'failed': return 'state-failed';
      case 'cancelled': return 'state-cancelled';
      case 'blocked': return 'state-blocked';
      default: return '';
    }
  }

  function eventKindLabel(kind) {
    switch (kind) {
      case 'loop.submitted': return 'Loop Submitted';
      case 'loop.started': return 'Loop Started';
      case 'loop.progress': return 'Loop Progress';
      case 'loop.delta': return 'Loop Delta';
      case 'loop.completed': return 'Loop Completed';
      case 'loop.failed': return 'Loop Failed';
      case 'loop.blocked': return 'Loop Blocked';
      case 'loop.cancelled': return 'Loop Cancelled';
      case 'tool.invoked': return 'Tool Call';
      case 'tool.result': return 'Tool Result';
      case 'runtime.health': return 'Health';
      case 'runtime.degraded': return 'Degraded';
      default: return kind;
    }
  }

  /** Whether the task is in progress (pending or running). */
  $: taskInProgress = currentLoopState === 'pending' || currentLoopState === 'running';
</script>

<div class="task-runner" data-task-runner>
  <!-- Prompt input -->
  <div class="prompt-area">
    <div class="prompt-row">
      <input
        type="text"
        class="prompt-input"
        data-prompt-input
        bind:value={promptText}
        on:keydown={handleKeydown}
        placeholder="Ask the runtime..."
        disabled={submitting || taskInProgress}
      />
      <button
        class="submit-btn"
        data-prompt-submit
        on:click={handleSubmit}
        disabled={submitting || !promptText.trim() || taskInProgress}
      >
        {#if submitting}
          Submitting…
        {:else if taskInProgress}
          Running…
        {:else}
          Send
        {/if}
      </button>
    </div>
    {#if submissionError}
      <p class="submission-error">{submissionError}</p>
    {/if}
  </div>

  <!-- Task status and progress -->
  {#if currentLoopId}
    <div class="task-progress" data-task-status>
      <div class="task-header">
        <span class="task-id" data-task-id>Loop: {currentLoopId.slice(0, 8)}…</span>
        <span class="task-state {stateClass(currentLoopState)}" data-task-state>
          {stateLabel(currentLoopState)}
        </span>
      </div>

      {#if taskInProgress}
        <div class="progress-indicator">
          <div class="progress-spinner"></div>
          <span class="progress-text">
            {#if reattaching}
              Reattaching to in-flight loop…
            {:else}
              Processing your request…
            {/if}
          </span>
        </div>
      {/if}

      <!-- Result -->
      {#if taskResult}
        <div class="task-result" data-task-result>
          <h3>Result</h3>
          <pre class="result-text">{taskResult}</pre>
        </div>
      {/if}

      <!-- Error -->
      {#if taskError}
        <div class="task-error" data-task-error>
          <h3>Error</h3>
          <p>{taskError}</p>
        </div>
      {/if}

      <!-- Event log (collapsible) -->
      {#if taskEvents.length > 0}
        <details class="event-log" data-task-events open={taskInProgress}>
          <summary>Events ({taskEvents.length})</summary>
          <ul class="event-list">
            {#each taskEvents as event}
              <li class="event-item" data-event-item>
                <span class="event-kind {stateClass(event.kind === 'loop.completed' ? 'completed' : event.kind === 'loop.failed' ? 'failed' : '')}">
                  {eventKindLabel(event.kind)}
                </span>
                <span class="event-seq">#{event.seq || '?'}</span>
              </li>
            {/each}
          </ul>
        </details>
      {/if}
    </div>
  {/if}
</div>

<style>
  .task-runner {
    display: flex;
    flex-direction: column;
    gap: 1rem;
  }

  /* ---- Prompt area ---- */
  .prompt-area {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
  }

  .prompt-row {
    display: flex;
    gap: 0.5rem;
  }

  .prompt-input {
    flex: 1;
    padding: 0.6rem 1rem;
    font-size: 0.95rem;
    color: #e0e0e0;
    background: #111;
    border: 1px solid #333;
    border-radius: 8px;
    outline: none;
    transition: border-color 0.2s;
  }

  .prompt-input:focus {
    border-color: #3b82f6;
  }

  .prompt-input::placeholder {
    color: #555;
  }

  .prompt-input:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }

  .submit-btn {
    padding: 0.6rem 1.2rem;
    font-size: 0.9rem;
    font-weight: 600;
    color: #fff;
    background: #3b82f6;
    border: none;
    border-radius: 8px;
    cursor: pointer;
    transition: background 0.2s, opacity 0.2s;
  }

  .submit-btn:hover:not(:disabled) {
    background: #2563eb;
  }

  .submit-btn:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  .submission-error {
    color: #f87171;
    font-size: 0.85rem;
  }

  /* ---- Task progress ---- */
  .task-progress {
    background: #111;
    border: 1px solid #222;
    border-radius: 10px;
    padding: 1rem;
  }

  .task-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 0.75rem;
    margin-bottom: 0.5rem;
  }

  .task-id {
    font-size: 0.8rem;
    color: #888;
    font-family: monospace;
  }

  .task-state {
    font-size: 0.75rem;
    font-weight: 600;
    letter-spacing: 0.05em;
    text-transform: uppercase;
    padding: 0.2rem 0.6rem;
    border-radius: 4px;
    color: #aaa;
    background: rgba(170, 170, 170, 0.1);
  }

  .task-state.state-pending {
    color: #fbbf24;
    background: rgba(251, 191, 36, 0.15);
  }

  .task-state.state-running {
    color: #60a5fa;
    background: rgba(96, 165, 250, 0.15);
  }

  .task-state.state-completed {
    color: #4ade80;
    background: rgba(74, 222, 128, 0.15);
  }

  .task-state.state-failed,
  .task-state.state-cancelled {
    color: #f87171;
    background: rgba(248, 113, 113, 0.15);
  }

  .task-state.state-blocked {
    color: #fb923c;
    background: rgba(251, 146, 60, 0.15);
  }

  /* ---- Progress indicator ---- */
  .progress-indicator {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    padding: 0.5rem 0;
  }

  .progress-spinner {
    width: 18px;
    height: 18px;
    border: 2px solid #333;
    border-top-color: #3b82f6;
    border-radius: 50%;
    animation: spin 1s linear infinite;
  }

  @keyframes spin {
    to { transform: rotate(360deg); }
  }

  .progress-text {
    font-size: 0.85rem;
    color: #888;
  }

  /* ---- Result ---- */
  .task-result h3,
  .task-error h3 {
    font-size: 0.8rem;
    font-weight: 600;
    color: #888;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    margin-bottom: 0.5rem;
  }

  .result-text {
    font-size: 0.85rem;
    color: #d0d0d0;
    background: #0a0a0a;
    border: 1px solid #1a1a1a;
    border-radius: 6px;
    padding: 0.75rem;
    white-space: pre-wrap;
    word-break: break-word;
    max-height: 300px;
    overflow-y: auto;
  }

  /* ---- Error ---- */
  .task-error p {
    color: #f87171;
    font-size: 0.85rem;
  }

  /* ---- Event log ---- */
  .event-log {
    margin-top: 0.75rem;
  }

  .event-log summary {
    font-size: 0.8rem;
    color: #888;
    cursor: pointer;
    user-select: none;
  }

  .event-list {
    list-style: none;
    padding: 0;
    margin-top: 0.5rem;
  }

  .event-item {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    padding: 0.25rem 0;
    font-size: 0.75rem;
  }

  .event-kind {
    font-weight: 600;
    color: #aaa;
  }

  .event-kind.completed {
    color: #4ade80;
  }

  .event-kind.failed {
    color: #f87171;
  }

  .event-seq {
    color: #555;
    font-family: monospace;
  }
</style>
