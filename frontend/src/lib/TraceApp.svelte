<script>
  import { createEventDispatcher, onDestroy, onMount } from 'svelte';
  import { AuthRequiredError } from './auth.js';
  import { getAgentTopology, listAgentEvents, listAgentRuns, openEventStream } from './trace.js';

  const dispatch = createEventDispatcher();

  let loading = true;
  let error = '';
  let streamStatus = 'connecting';
  let topology = null;
  let runs = [];
  let ownerEvents = [];
  let selectedRunId = '';
  let lastSeq = 0;
  let refreshTimer = null;
  let stream = null;

  function parseDate(value) {
    const time = value ? new Date(value).getTime() : 0;
    return Number.isFinite(time) ? time : 0;
  }

  function sortRunsNewestFirst(items) {
    return [...items].sort((left, right) => parseDate(right.created_at) - parseDate(left.created_at));
  }

  function sortEventsAscending(items) {
    return [...items].sort((left, right) => {
      const tsDelta = parseDate(left.ts) - parseDate(right.ts);
      if (tsDelta !== 0) return tsDelta;
      return (left.seq || 0) - (right.seq || 0);
    });
  }

  function upsertEvent(eventRecord) {
    if (!eventRecord?.event_id) return;
    const existing = ownerEvents.findIndex((item) => item.event_id === eventRecord.event_id);
    if (existing >= 0) {
      ownerEvents[existing] = eventRecord;
      ownerEvents = sortEventsAscending(ownerEvents);
      return;
    }
    ownerEvents = sortEventsAscending([...ownerEvents, eventRecord]).slice(-500);
  }

  function applyRunEvent(runId, kind) {
    if (!runId) return;
    const index = runs.findIndex((run) => run.run_id === runId);
    if (index < 0) return;

    const current = runs[index];
    let nextState = current.state;
    if (kind === 'run.submitted') nextState = 'pending';
    if (kind === 'run.started') nextState = 'running';
    if (kind === 'run.completed') nextState = 'completed';
    if (kind === 'run.failed') nextState = 'failed';
    if (kind === 'run.blocked') nextState = 'blocked';
    if (kind === 'run.cancelled') nextState = 'cancelled';

    runs[index] = {
      ...current,
      state: nextState,
      updated_at: new Date().toISOString(),
    };
    runs = sortRunsNewestFirst(runs);
  }

  function scheduleRunRefresh() {
    if (refreshTimer) return;
    refreshTimer = setTimeout(async () => {
      refreshTimer = null;
      try {
        const response = await listAgentRuns(120);
        runs = sortRunsNewestFirst(response.runs || []);
      } catch (err) {
        if (err instanceof AuthRequiredError) {
          dispatch('authexpired');
          return;
        }
      }
    }, 250);
  }

  function connectStream() {
    if (stream) {
      stream.close();
      stream = null;
    }

    streamStatus = 'connecting';
    stream = openEventStream({
      afterSeq: lastSeq,
      onEvent: (eventRecord) => {
        streamStatus = 'connected';
        lastSeq = Math.max(lastSeq, eventRecord.seq || 0);
        upsertEvent(eventRecord);
        applyRunEvent(eventRecord.run_id, eventRecord.kind);
        if (eventRecord.run_id) {
          scheduleRunRefresh();
        }
      },
      onError: () => {
        streamStatus = 'reconnecting';
      },
    });
  }

  async function loadTraceState() {
    loading = true;
    error = '';
    try {
      const [runResp, eventResp, topologyResp] = await Promise.all([
        listAgentRuns(120),
        listAgentEvents({ limit: 300 }),
        getAgentTopology(),
      ]);

      runs = sortRunsNewestFirst(runResp.runs || []);
      ownerEvents = sortEventsAscending(eventResp.events || []);
      topology = topologyResp;
      lastSeq = ownerEvents.reduce((max, event) => Math.max(max, event.seq || 0), 0);

      if (!selectedRunId && runs.length > 0) {
        selectedRunId = runs[0].run_id;
      }
      connectStream();
    } catch (err) {
      if (err instanceof AuthRequiredError) {
        dispatch('authexpired');
        return;
      }
      error = err.message || 'Failed to load Trace';
    } finally {
      loading = false;
    }
  }

  function selectRun(runId) {
    selectedRunId = runId;
  }

  function runProfile(run) {
    return run?.agent_profile || run?.metadata?.agent_profile || run?.metadata?.type || 'run';
  }

  function runRole(run) {
    return run?.agent_role || run?.metadata?.agent_role || runProfile(run);
  }

  function runParentId(run) {
    return run?.parent_run_id || run?.metadata?.parent_id || '';
  }

  function taskStateTone(state) {
    if (state === 'completed') return 'tone-success';
    if (state === 'running') return 'tone-running';
    if (state === 'failed' || state === 'blocked' || state === 'cancelled') return 'tone-error';
    return 'tone-neutral';
  }

  function excerpt(text, max = 72) {
    const normalized = (text || '').replace(/\s+/g, ' ').trim();
    if (!normalized) return 'Untitled run';
    if (normalized.length <= max) return normalized;
    return `${normalized.slice(0, max - 1)}…`;
  }

  function parsePayload(payload) {
    if (!payload) return {};
    if (typeof payload === 'object') return payload;
    try {
      return JSON.parse(payload);
    } catch {
      return {};
    }
  }

  function eventSummary(eventRecord) {
    const payload = parsePayload(eventRecord.payload);
    switch (eventRecord.kind) {
      case 'run.submitted':
        return payload.parent_id ? `Spawned from ${payload.parent_id.slice(0, 8)}` : 'Run submitted';
      case 'run.started':
        return 'Run started';
      case 'run.progress':
        if (payload.tool_calls !== undefined) {
          return `Tool loop iteration ${payload.iteration || '?'} with ${payload.tool_calls} tool calls`;
        }
        if (payload.status) {
          return payload.provider ? `${payload.status} via ${payload.provider}` : payload.status;
        }
        return 'Progress update';
      case 'tool.invoked':
        return `Invoked ${payload.tool || 'tool'}`;
      case 'tool.result':
        return `${payload.tool || 'tool'} returned${payload.is_error ? ' error' : ''}`;
      case 'channel.message':
        return `${payload.role || 'agent'} ${payload.from ? `(${payload.from.slice(0, 8)}) ` : ''}posted ${payload.content_len || 0} chars`;
      case 'run.completed':
        return 'Run completed';
      case 'run.failed':
        return payload.error || 'Run failed';
      case 'run.blocked':
        return payload.error || 'Run blocked';
      case 'run.cancelled':
        return payload.error || 'Run cancelled';
      case 'vtext.agent_revision.started':
        return 'VText revision started';
      case 'vtext.agent_revision.progress':
        return payload.status || 'VText revision progress';
      case 'vtext.agent_revision.completed':
        return `VText created revision ${payload.revision_id ? payload.revision_id.slice(0, 8) : ''}`;
      case 'vtext.agent_revision.failed':
        return payload.error || 'VText revision failed';
      default:
        return eventRecord.kind;
    }
  }

  function eventIsVisible(eventRecord) {
    return eventRecord?.kind !== 'run.delta';
  }

  function collectFamilyIds(rootId) {
    const ids = new Set();
    if (!rootId) return ids;
    const queue = [rootId];
    while (queue.length > 0) {
      const current = queue.shift();
      if (!current || ids.has(current)) continue;
      ids.add(current);
      for (const run of runs) {
        if (runParentId(run) === current) {
          queue.push(run.run_id);
        }
      }
    }
    return ids;
  }

  $: selectedTask = runs.find((run) => run.run_id === selectedRunId) || null;
  $: selectedFamilyIds = collectFamilyIds(selectedRunId);
  $: familyTasks = sortRunsNewestFirst(runs.filter((run) => selectedFamilyIds.has(run.run_id))).reverse();
  $: familyEvents = sortEventsAscending(ownerEvents.filter((eventRecord) => selectedFamilyIds.has(eventRecord.run_id)));
  $: visibleFamilyEvents = familyEvents.filter(eventIsVisible);
  $: childTasks = familyTasks.filter((task) => task.run_id !== selectedRunId);
  $: familyToolCount = familyEvents.filter((eventRecord) => eventRecord.kind === 'tool.invoked').length;
  $: familyChannelCount = familyEvents.filter((eventRecord) => eventRecord.kind === 'channel.message').length;
  $: familyResearcherCount = childTasks.filter((task) => runProfile(task) === 'researcher').length;
  $: familySuperCount = childTasks.filter((task) => runProfile(task) === 'super').length;
  $: hiddenDeltaCount = familyEvents.length - visibleFamilyEvents.length;

  onMount(() => {
    loadTraceState();
  });

  onDestroy(() => {
    if (refreshTimer) clearTimeout(refreshTimer);
    if (stream) stream.close();
  });
</script>

<div class="trace-app" data-trace-app>
  <div class="trace-sidebar">
    <div class="trace-sidebar-header">
      <h2>Trace</h2>
      <span class={`stream-pill ${streamStatus}`}>{streamStatus}</span>
    </div>

    {#if topology}
      <div class="topology-card" data-trace-topology>
        <div><strong>Provider</strong> {topology.active_provider}</div>
        <div><strong>Researchers</strong> {topology.researcher_count}</div>
        <div><strong>Channels</strong> {topology.channel_count}</div>
        <div><strong>Health</strong> {topology.runtime_health}</div>
      </div>
    {/if}

    <div class="task-list" data-trace-task-list>
      {#if loading}
        <div class="empty-state">Loading recent runs…</div>
      {:else if runs.length === 0}
        <div class="empty-state">No runs yet. Run conductor or `vtext` to start tracing.</div>
      {:else}
        {#each runs as task (task.run_id)}
          <button
            class:selected={task.run_id === selectedRunId}
            class={`task-item ${taskStateTone(task.state)}`}
            data-trace-task
            data-trace-task-id={task.run_id}
            on:click={() => selectRun(task.run_id)}
          >
            <div class="task-item-top">
              <span class="task-profile">{runProfile(task)}</span>
              <span class={`task-state ${taskStateTone(task.state)}`}>{task.state}</span>
            </div>
            <div class="task-prompt">{excerpt(task.prompt, 58)}</div>
            <div class="task-meta">
              <span>{runRole(task)}</span>
              {#if runParentId(task)}
                <span>child</span>
              {/if}
            </div>
          </button>
        {/each}
      {/if}
    </div>
  </div>

  <div class="trace-main">
    {#if error}
      <div class="error-banner">{error}</div>
    {/if}

    {#if selectedTask}
      <section class="summary-panel" data-trace-summary>
        <div class="summary-header">
          <div>
            <h3>{excerpt(selectedTask.prompt, 90)}</h3>
            <p>{runProfile(selectedTask)} · {runRole(selectedTask)} · {selectedTask.run_id}</p>
          </div>
          <span class={`task-state ${taskStateTone(selectedTask.state)}`}>{selectedTask.state}</span>
        </div>

        <div class="summary-metrics">
          <div class="metric"><span>{familyTasks.length}</span><div class="metric-label">runs</div></div>
          <div class="metric"><span>{childTasks.length}</span><div class="metric-label">delegations</div></div>
          <div class="metric"><span>{familyResearcherCount}</span><div class="metric-label">researchers</div></div>
          <div class="metric"><span>{familySuperCount}</span><div class="metric-label">supers</div></div>
          <div class="metric"><span>{familyToolCount}</span><div class="metric-label">tool calls</div></div>
          <div class="metric"><span>{familyChannelCount}</span><div class="metric-label">messages</div></div>
        </div>
      </section>

      <section class="graph-panel" data-trace-family>
        <h3>Run Family</h3>
        <div class="family-grid">
          {#each familyTasks as task (task.run_id)}
            <div class={`family-card ${taskStateTone(task.state)}`}>
              <div class="family-card-top">
                <strong>{runProfile(task)}</strong>
                <span>{task.state}</span>
              </div>
              <div class="family-card-body">{excerpt(task.prompt, 68)}</div>
              <div class="family-card-meta">
                <span>{task.run_id}</span>
                {#if runParentId(task)}
                  <span>parent {runParentId(task).slice(0, 8)}</span>
                {/if}
                {#if task.channel_id}
                  <span>channel {task.channel_id}</span>
                {/if}
              </div>
            </div>
          {/each}
        </div>
      </section>

      <section class="timeline-panel">
        <h3>Event Timeline</h3>
        {#if hiddenDeltaCount > 0}
          <div class="timeline-note">Hiding {hiddenDeltaCount} raw `run.delta` events so the causal steps stay readable.</div>
        {/if}
        {#if visibleFamilyEvents.length === 0}
          <div class="empty-state">No family events captured yet for this run.</div>
        {:else}
          <div class="event-list" data-trace-event-list>
            {#each visibleFamilyEvents as eventRecord (eventRecord.event_id)}
              <div class="event-row" data-trace-event>
                <div class="event-time">{new Date(eventRecord.ts).toLocaleTimeString()}</div>
                <div class="event-kind">{eventRecord.kind}</div>
                <div class="event-summary">{eventSummary(eventRecord)}</div>
              </div>
            {/each}
          </div>
        {/if}
      </section>
    {:else if !loading}
      <div class="empty-state">Select a run to inspect its run family, tool calls, and channel traffic.</div>
    {/if}
  </div>
</div>

<style>
  .trace-app {
    height: 100%;
    display: grid;
    grid-template-columns: 280px minmax(0, 1fr);
    min-height: 0;
    background:
      radial-gradient(circle at top left, rgba(59, 130, 246, 0.08), transparent 30%),
      rgba(9, 10, 16, 0.98);
    color: #e2e8f0;
  }

  .trace-sidebar {
    border-right: 1px solid rgba(71, 85, 105, 0.26);
    padding: 0.9rem;
    display: flex;
    flex-direction: column;
    gap: 0.8rem;
    min-height: 0;
  }

  .trace-sidebar-header,
  .summary-header,
  .family-card-top,
  .task-item-top {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 0.6rem;
  }

  .trace-sidebar-header h2,
  .summary-panel h3,
  .graph-panel h3,
  .timeline-panel h3 {
    margin: 0;
    font-size: 0.98rem;
  }

  .stream-pill,
  .task-state,
  .task-profile {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    padding: 0.18rem 0.48rem;
    border-radius: 999px;
    font-size: 0.72rem;
    border: 1px solid rgba(148, 163, 184, 0.16);
    background: rgba(15, 23, 42, 0.72);
  }

  .stream-pill.connected,
  .tone-success {
    color: #86efac;
    border-color: rgba(74, 222, 128, 0.3);
  }

  .stream-pill.connecting,
  .stream-pill.reconnecting,
  .tone-running {
    color: #fde68a;
    border-color: rgba(250, 204, 21, 0.28);
  }

  .tone-error {
    color: #fca5a5;
    border-color: rgba(248, 113, 113, 0.28);
  }

  .tone-neutral {
    color: #cbd5e1;
  }

  .topology-card,
  .summary-panel,
  .graph-panel,
  .timeline-panel,
  .task-item,
  .family-card {
    border: 1px solid rgba(71, 85, 105, 0.24);
    background: rgba(15, 23, 42, 0.56);
    border-radius: 14px;
  }

  .topology-card {
    padding: 0.75rem;
    font-size: 0.82rem;
    display: grid;
    gap: 0.35rem;
  }

  .task-list {
    display: flex;
    flex-direction: column;
    gap: 0.55rem;
    overflow: auto;
    min-height: 0;
  }

  .task-item {
    padding: 0.75rem;
    text-align: left;
    color: inherit;
    cursor: pointer;
  }

  .task-item.selected {
    box-shadow: inset 0 0 0 1px rgba(96, 165, 250, 0.45);
  }

  .task-prompt {
    margin-top: 0.5rem;
    font-size: 0.82rem;
    line-height: 1.45;
  }

  .task-meta,
  .family-card-meta,
  .summary-header p {
    margin: 0;
    color: #94a3b8;
    font-size: 0.74rem;
    display: flex;
    gap: 0.45rem;
    flex-wrap: wrap;
  }

  .trace-main {
    padding: 1rem;
    display: flex;
    flex-direction: column;
    gap: 0.9rem;
    min-height: 0;
  }

  .summary-panel,
  .graph-panel,
  .timeline-panel {
    padding: 0.9rem;
  }

  .summary-metrics {
    margin-top: 0.9rem;
    display: grid;
    grid-template-columns: repeat(6, minmax(0, 1fr));
    gap: 0.65rem;
  }

  .metric {
    padding: 0.65rem 0.7rem;
    border-radius: 12px;
    background: rgba(2, 6, 23, 0.45);
    border: 1px solid rgba(71, 85, 105, 0.18);
  }

  .metric span {
    display: block;
    font-size: 1rem;
    font-weight: 700;
  }

  .metric-label {
    display: block;
    margin-top: 0.2rem;
    color: #94a3b8;
    font-size: 0.72rem;
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }

  .family-grid {
    margin-top: 0.8rem;
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
    gap: 0.75rem;
  }

  .family-card {
    padding: 0.75rem;
    min-height: 110px;
    display: flex;
    flex-direction: column;
    gap: 0.55rem;
  }

  .family-card-body {
    font-size: 0.82rem;
    line-height: 1.45;
  }

  .event-list {
    margin-top: 0.8rem;
    display: flex;
    flex-direction: column;
    gap: 0.45rem;
    max-height: 100%;
    overflow: auto;
  }

  .event-row {
    display: grid;
    grid-template-columns: 92px 220px minmax(0, 1fr);
    gap: 0.7rem;
    align-items: start;
    padding: 0.65rem 0.75rem;
    border-radius: 12px;
    background: rgba(2, 6, 23, 0.42);
    border: 1px solid rgba(71, 85, 105, 0.16);
    font-size: 0.8rem;
  }

  .event-time,
  .event-kind {
    color: #93c5fd;
  }

  .event-summary {
    color: #e2e8f0;
    line-height: 1.45;
  }

  .empty-state,
  .timeline-note,
  .error-banner {
    padding: 0.9rem;
    border-radius: 12px;
    font-size: 0.84rem;
  }

  .empty-state {
    color: #94a3b8;
    background: rgba(15, 23, 42, 0.34);
    border: 1px dashed rgba(71, 85, 105, 0.28);
  }

  .timeline-note {
    margin-top: 0.8rem;
    color: #bfdbfe;
    background: rgba(15, 23, 42, 0.42);
    border: 1px solid rgba(96, 165, 250, 0.18);
    font-size: 0.8rem;
  }

  .error-banner {
    color: #fecaca;
    background: rgba(127, 29, 29, 0.78);
    border: 1px solid rgba(248, 113, 113, 0.28);
  }

  @media (max-width: 900px) {
    .trace-app {
      grid-template-columns: 1fr;
    }

    .trace-sidebar {
      border-right: none;
      border-bottom: 1px solid rgba(71, 85, 105, 0.26);
      max-height: 38%;
    }

    .summary-metrics {
      grid-template-columns: repeat(3, minmax(0, 1fr));
    }

    .event-row {
      grid-template-columns: 76px minmax(0, 1fr);
    }

    .event-summary {
      grid-column: 1 / -1;
    }
  }
</style>
