<script>
  import { createEventDispatcher, onDestroy, onMount } from 'svelte';
  import { AuthRequiredError } from './auth.js';
  import { getAgentTopology, listAgentEvents, listAgentRuns, listChannelMessages, openEventStream } from './trace.js';

  const dispatch = createEventDispatcher();

  let loading = true;
  let detailLoading = false;
  let error = '';
  let streamStatus = 'connecting';
  let topology = null;
  let ownerRuns = [];
  let workflowRuns = [];
  let workflowEvents = [];
  let workflowMessages = [];
  let selectedChannelId = '';
  let lastSeq = 0;
  let refreshTimer = null;
  let detailRefreshTimer = null;
  let stream = null;

  function parseDate(value) {
    const time = value ? new Date(value).getTime() : 0;
    return Number.isFinite(time) ? time : 0;
  }

  function sortRunsNewestFirst(items) {
    return [...items].sort((left, right) => parseDate(right.created_at) - parseDate(left.created_at));
  }

  function sortRunsOldestFirst(items) {
    return [...items].sort((left, right) => parseDate(left.created_at) - parseDate(right.created_at));
  }

  function sortEventsAscending(items) {
    return [...items].sort((left, right) => {
      const tsDelta = parseDate(left.ts) - parseDate(right.ts);
      if (tsDelta !== 0) return tsDelta;
      return (left.seq || 0) - (right.seq || 0);
    });
  }

  function sortMessagesAscending(items) {
    return [...items].sort((left, right) => {
      const seqDelta = (left.seq || 0) - (right.seq || 0);
      if (seqDelta !== 0) return seqDelta;
      return parseDate(left.timestamp) - parseDate(right.timestamp);
    });
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

  function workflowTrajectoryId(run) {
    return (
      run?.metadata?.trajectory_id ||
      run?.trajectory_id ||
      run?.channel_id ||
      run?.metadata?.channel_id ||
      run?.run_id ||
      ''
    );
  }

  function workflowDetailChannelId(run) {
    return run?.channel_id || run?.metadata?.channel_id || run?.run_id || '';
  }

  // Back-compat alias: anything still calling workflowChannelId gets the
  // trajectory grouping key so the sidebar renders one tile per trajectory.
  function workflowChannelId(run) {
    return workflowTrajectoryId(run);
  }

  function excerpt(text, max = 72) {
    const normalized = (text || '').replace(/\s+/g, ' ').trim();
    if (!normalized) return 'Untitled run';
    if (normalized.length <= max) return normalized;
    return `${normalized.slice(0, max - 1)}…`;
  }

  function taskStateTone(state) {
    if (state === 'completed') return 'tone-success';
    if (state === 'running') return 'tone-running';
    if (state === 'failed' || state === 'blocked' || state === 'cancelled') return 'tone-error';
    return 'tone-neutral';
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

  function workflowTitle(workflow) {
    const latestRun = workflow?.latestRun;
    const prompt = excerpt(latestRun?.prompt || latestRun?.metadata?.original_prompt || '', 54);
    if (prompt !== 'Untitled run') return prompt;
    if (latestRun?.metadata?.doc_id) return `vtext ${String(latestRun.metadata.doc_id).slice(0, 8)}`;
    return workflow?.channelId || 'workflow';
  }

  function workflowSubtitle(workflow) {
    const latestRun = workflow?.latestRun;
    if (!latestRun) return workflow?.channelId || '';
    return `${runProfile(latestRun)} · ${workflow.channelId}`;
  }

  function buildWorkflowIndex(runItems) {
    const grouped = new Map();
    for (const run of sortRunsNewestFirst(runItems)) {
      const trajectoryId = workflowTrajectoryId(run);
      if (!trajectoryId) continue;
      if (!grouped.has(trajectoryId)) {
        grouped.set(trajectoryId, {
          channelId: trajectoryId,
          trajectoryId,
          detailChannelId: workflowDetailChannelId(run),
          latestRun: run,
          latestAt: parseDate(run.updated_at || run.created_at),
          runs: [],
        });
      } else {
        // Prefer the latest run's channel for detail fetches so users see the
        // most recent worker/vtext activity when a trajectory spans multiple
        // channels.
        const existing = grouped.get(trajectoryId);
        const runAt = parseDate(run.updated_at || run.created_at);
        if (runAt > existing.latestAt) {
          existing.latestAt = runAt;
          existing.latestRun = run;
          existing.detailChannelId = workflowDetailChannelId(run);
        }
      }
      grouped.get(trajectoryId).runs.push(run);
    }
    return [...grouped.values()].sort((left, right) => right.latestAt - left.latestAt);
  }

  function eventSummary(eventRecord) {
    const payload = parsePayload(eventRecord.payload);
    switch (eventRecord.kind) {
      case 'run.submitted':
        return payload.parent_id ? `spawned from ${payload.parent_id.slice(0, 8)}` : 'run submitted';
      case 'run.started':
        return 'run started';
      case 'run.progress':
        if (payload.tool_calls !== undefined) {
          return `tool loop iteration ${payload.iteration || '?'} with ${payload.tool_calls} tool calls`;
        }
        if (payload.status) {
          return payload.provider ? `${payload.status} via ${payload.provider}` : payload.status;
        }
        return 'progress update';
      case 'tool.invoked':
        return `invoked ${payload.tool || 'tool'}`;
      case 'tool.result':
        return `${payload.tool || 'tool'} returned${payload.is_error ? ' error' : ''}`;
      case 'channel.message':
        return `${payload.role || 'agent'} posted to ${payload.channel_id || 'channel'}`;
      case 'run.completed':
        return 'run completed';
      case 'run.failed':
      case 'run.blocked':
      case 'run.cancelled':
        return payload.error || eventRecord.kind;
      case 'vtext.agent_revision.started':
        return 'vtext revision started';
      case 'vtext.agent_revision.progress':
        return payload.phase ? `vtext ${payload.phase}` : 'vtext revision progress';
      case 'vtext.agent_revision.completed':
        return `vtext created revision ${payload.revision_id ? payload.revision_id.slice(0, 8) : ''}`;
      case 'vtext.agent_revision.failed':
        return payload.error || 'vtext revision failed';
      default:
        return eventRecord.kind;
    }
  }

  function eventIsVisible(eventRecord) {
    return eventRecord?.kind !== 'run.delta';
  }

  function formatTimestamp(value) {
    if (!value) return '';
    return new Date(value).toLocaleTimeString();
  }

  async function refreshWorkflowIndex() {
    const runResp = await listAgentRuns(200);
    ownerRuns = sortRunsNewestFirst(runResp.runs || []);
    const workflows = buildWorkflowIndex(ownerRuns);
    if (!selectedChannelId && workflows.length > 0) {
      selectedChannelId = workflows[0].channelId;
      await refreshSelectedWorkflow(selectedChannelId);
      return;
    }
    if (selectedChannelId && !workflows.some((workflow) => workflow.channelId === selectedChannelId)) {
      selectedChannelId = workflows[0]?.channelId || '';
      await refreshSelectedWorkflow(selectedChannelId);
    }
  }

  async function refreshSelectedWorkflow(trajectoryId = selectedChannelId) {
    if (!trajectoryId) {
      workflowRuns = [];
      workflowEvents = [];
      workflowMessages = [];
      return;
    }
    detailLoading = true;
    try {
      // A trajectory may span multiple channels (conductor.channel = run_id,
      // vtext.channel = doc_id, researcher channel = inherited). Filter the
      // locally-known owner runs by trajectory_id, then fan-out event and
      // message queries over each distinct channel in the trajectory. This
      // stitches prompt-bar → conductor → vtext → workers into one workflow
      // without a backend trajectory-aware endpoint.
      const trajectoryRuns = (ownerRuns || []).filter(
        (run) => workflowTrajectoryId(run) === trajectoryId,
      );
      const channels = new Set();
      for (const run of trajectoryRuns) {
        const ch = workflowDetailChannelId(run);
        if (ch) channels.add(ch);
      }
      if (channels.size === 0) channels.add(trajectoryId);

      const eventResponses = await Promise.all(
        [...channels].map((channelId) => listAgentEvents({ limit: 400, channelId })),
      );
      const messageResponses = await Promise.all(
        [...channels].map((channelId) => listChannelMessages({ channelId, limit: 200 })),
      );

      const allEvents = eventResponses.flatMap((resp) => resp.events || []);
      const allMessages = messageResponses.flatMap((resp) => resp.messages || []);

      workflowRuns = sortRunsOldestFirst(trajectoryRuns);
      workflowEvents = sortEventsAscending(allEvents);
      workflowMessages = sortMessagesAscending(allMessages);
    } finally {
      detailLoading = false;
    }
  }

  function scheduleWorkflowRefresh() {
    if (refreshTimer) return;
    refreshTimer = setTimeout(async () => {
      refreshTimer = null;
      try {
        await refreshWorkflowIndex();
      } catch (err) {
        if (err instanceof AuthRequiredError) {
          dispatch('authexpired');
        }
      }
    }, 250);
  }

  function scheduleSelectedWorkflowRefresh() {
    if (!selectedChannelId || detailRefreshTimer) return;
    detailRefreshTimer = setTimeout(async () => {
      detailRefreshTimer = null;
      try {
        await refreshSelectedWorkflow(selectedChannelId);
      } catch (err) {
        if (err instanceof AuthRequiredError) {
          dispatch('authexpired');
          return;
        }
        error = err.message || 'Failed to refresh Trace';
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
        scheduleWorkflowRefresh();
        if (
          selectedChannelId &&
          (eventRecord.channel_id === selectedChannelId || workflowRuns.some((run) => run.run_id === eventRecord.run_id))
        ) {
          scheduleSelectedWorkflowRefresh();
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
      const [topologyResp, runResp] = await Promise.all([
        getAgentTopology(),
        listAgentRuns(200),
      ]);

      topology = topologyResp;
      ownerRuns = sortRunsNewestFirst(runResp.runs || []);
      const workflows = buildWorkflowIndex(ownerRuns);
      if (!selectedChannelId && workflows.length > 0) {
        selectedChannelId = workflows[0].channelId;
      }
      await refreshSelectedWorkflow(selectedChannelId || workflows[0]?.channelId || '');
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

  async function selectWorkflow(channelId) {
    if (!channelId || channelId === selectedChannelId) return;
    selectedChannelId = channelId;
    error = '';
    try {
      await refreshSelectedWorkflow(channelId);
    } catch (err) {
      if (err instanceof AuthRequiredError) {
        dispatch('authexpired');
        return;
      }
      error = err.message || 'Failed to load workflow trace';
    }
  }

  $: workflows = buildWorkflowIndex(ownerRuns);
  $: selectedWorkflow = workflows.find((workflow) => workflow.channelId === selectedChannelId) || null;
  $: latestRun = selectedWorkflow?.latestRun || workflowRuns[workflowRuns.length - 1] || null;
  $: visibleWorkflowEvents = workflowEvents.filter(eventIsVisible);
  $: hiddenDeltaCount = workflowEvents.length - visibleWorkflowEvents.length;
  $: delegationRuns = workflowRuns.filter((run) => runParentId(run));
  $: researcherRuns = workflowRuns.filter((run) => runProfile(run) === 'researcher');
  $: superRuns = workflowRuns.filter((run) => ['super', 'co-super'].includes(runProfile(run)));
  $: toolInvocations = workflowEvents.filter((eventRecord) => eventRecord.kind === 'tool.invoked').length;
  $: childMessages = workflowMessages.filter((message) => message.from_run_id && message.from_run_id !== latestRun?.run_id);

  onMount(() => {
    loadTraceState();
  });

  onDestroy(() => {
    if (refreshTimer) clearTimeout(refreshTimer);
    if (detailRefreshTimer) clearTimeout(detailRefreshTimer);
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
        <div><strong>Configured researcher slots</strong> {topology.researcher_count}</div>
        <div><strong>Running runs</strong> {topology.running_runs}</div>
        <div><strong>Active channels</strong> {topology.channel_count}</div>
        <div><strong>Health</strong> {topology.runtime_health}</div>
      </div>
    {/if}

    <div class="workflow-list" data-trace-workflow-list>
      {#if loading}
        <div class="empty-state">Loading workflows…</div>
      {:else if workflows.length === 0}
        <div class="empty-state">No workflows yet. Open `vtext` or conductor to start tracing.</div>
      {:else}
        {#each workflows as workflow (workflow.channelId)}
          <button
            class:selected={workflow.channelId === selectedChannelId}
            class={`workflow-item ${taskStateTone(workflow.latestRun?.state)}`}
            data-trace-workflow
            data-trace-channel-id={workflow.channelId}
            on:click={() => selectWorkflow(workflow.channelId)}
          >
            <div class="workflow-item-top">
              <span class="task-profile">{runProfile(workflow.latestRun)}</span>
              <span class={`task-state ${taskStateTone(workflow.latestRun?.state)}`}>{workflow.latestRun?.state}</span>
            </div>
            <div class="workflow-title">{workflowTitle(workflow)}</div>
            <div class="workflow-meta">
              <span>{workflowSubtitle(workflow)}</span>
              <span>{workflow.runs.length} runs</span>
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

    {#if selectedWorkflow}
      <section class="summary-panel" data-trace-summary>
        <div class="summary-header">
          <div>
            <h3>{workflowTitle(selectedWorkflow)}</h3>
            <p>{runProfile(latestRun)} · {runRole(latestRun)} · channel {selectedWorkflow.channelId}</p>
          </div>
          <span class={`task-state ${taskStateTone(latestRun?.state)}`}>{latestRun?.state}</span>
        </div>

        <div class="summary-metrics">
          <div class="metric"><span>{workflowRuns.length}</span><div class="metric-label">runs</div></div>
          <div class="metric"><span>{delegationRuns.length}</span><div class="metric-label">delegations</div></div>
          <div class="metric"><span>{researcherRuns.length}</span><div class="metric-label">researchers</div></div>
          <div class="metric"><span>{superRuns.length}</span><div class="metric-label">supers</div></div>
          <div class="metric"><span>{toolInvocations}</span><div class="metric-label">tool calls</div></div>
          <div class="metric"><span>{workflowMessages.length}</span><div class="metric-label">messages</div></div>
        </div>
      </section>

      <section class="graph-panel" data-trace-family>
        <h3>Workflow graph</h3>
        <div class="section-note">One shared channel, many runs. Parent edges show delegation. Message bodies live below.</div>
        {#if detailLoading && workflowRuns.length === 0}
          <div class="empty-state">Loading workflow runs…</div>
        {:else if workflowRuns.length === 0}
          <div class="empty-state">No runs recorded for this channel yet.</div>
        {:else}
          <div class="family-grid">
            {#each workflowRuns as run (run.run_id)}
              <div class={`family-card ${taskStateTone(run.state)}`}>
                <div class="family-card-top">
                  <strong>{runProfile(run)}</strong>
                  <span>{run.state}</span>
                </div>
                <div class="family-card-body">{excerpt(run.prompt, 92)}</div>
                <div class="family-card-meta">
                  <span>{run.run_id}</span>
                  {#if runParentId(run)}
                    <span>parent {runParentId(run).slice(0, 8)}</span>
                  {/if}
                  <span>{formatTimestamp(run.created_at)}</span>
                </div>
              </div>
            {/each}
          </div>
        {/if}
      </section>

      <section class="messages-panel" data-trace-messages>
        <h3>Channel messages</h3>
        <div class="section-note">This is the actual shared conversation, not an event count.</div>
        {#if detailLoading && workflowMessages.length === 0}
          <div class="empty-state">Loading channel messages…</div>
        {:else if workflowMessages.length === 0}
          <div class="empty-state">No channel messages captured yet for this workflow.</div>
        {:else}
          <div class="message-list">
            {#each workflowMessages as message (`${message.channel_id}-${message.seq}`)}
              <div class={`message-card ${message.from_run_id && latestRun && message.from_run_id !== latestRun.run_id ? 'message-child' : ''}`}>
                <div class="message-card-top">
                  <strong>{message.from || message.role || 'agent'}</strong>
                  <span>{message.role || 'message'}</span>
                  <span>seq {message.seq}</span>
                  <span>{formatTimestamp(message.timestamp)}</span>
                </div>
                <div class="message-card-meta">
                  {#if message.from_agent_id}
                    <span>agent {message.from_agent_id}</span>
                  {/if}
                  {#if message.from_run_id}
                    <span>run {message.from_run_id}</span>
                  {/if}
                </div>
                <pre class="message-content">{message.content}</pre>
              </div>
            {/each}
          </div>
        {/if}
      </section>

      <section class="timeline-panel">
        <h3>Event timeline</h3>
        {#if hiddenDeltaCount > 0}
          <div class="timeline-note">Hiding {hiddenDeltaCount} raw `run.delta` events so the causal steps stay readable.</div>
        {/if}
        {#if visibleWorkflowEvents.length === 0}
          <div class="empty-state">No events captured yet for this workflow.</div>
        {:else}
          <div class="event-list" data-trace-event-list>
            {#each visibleWorkflowEvents as eventRecord (eventRecord.event_id)}
              <div class="event-row" data-trace-event>
                <div class="event-time">{formatTimestamp(eventRecord.ts)}</div>
                <div class="event-kind">{eventRecord.kind}</div>
                <div class="event-summary">
                  <div>{eventSummary(eventRecord)}</div>
                  {#if eventRecord.run_id}
                    <div class="event-meta">run {eventRecord.run_id}</div>
                  {/if}
                </div>
              </div>
            {/each}
          </div>
        {/if}
      </section>
    {:else if !loading}
      <div class="empty-state">Select a workflow to inspect its runs, message passing, and event history.</div>
    {/if}
  </div>
</div>

<style>
  .trace-app {
    height: 100%;
    display: grid;
    grid-template-columns: 320px minmax(0, 1fr);
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
  .workflow-item-top,
  .message-card-top {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 0.6rem;
  }

  .trace-sidebar-header h2,
  .summary-panel h3,
  .graph-panel h3,
  .timeline-panel h3,
  .messages-panel h3 {
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
  .messages-panel,
  .workflow-item,
  .family-card,
  .message-card {
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

  .workflow-list {
    display: flex;
    flex-direction: column;
    gap: 0.55rem;
    overflow: auto;
    min-height: 0;
  }

  .workflow-item {
    padding: 0.75rem;
    text-align: left;
    color: inherit;
    cursor: pointer;
  }

  .workflow-item.selected {
    box-shadow: inset 0 0 0 1px rgba(96, 165, 250, 0.45);
  }

  .workflow-title {
    margin-top: 0.5rem;
    font-size: 0.82rem;
    line-height: 1.45;
  }

  .workflow-meta,
  .family-card-meta,
  .summary-header p,
  .message-card-meta,
  .event-meta {
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
    overflow: auto;
  }

  .summary-panel,
  .graph-panel,
  .timeline-panel,
  .messages-panel {
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

  .section-note,
  .timeline-note,
  .empty-state,
  .error-banner {
    padding: 0.9rem;
    border-radius: 12px;
    font-size: 0.84rem;
  }

  .section-note,
  .timeline-note {
    margin-top: 0.8rem;
    color: #bfdbfe;
    background: rgba(15, 23, 42, 0.42);
    border: 1px solid rgba(96, 165, 250, 0.18);
    font-size: 0.8rem;
  }

  .family-grid {
    margin-top: 0.8rem;
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
    gap: 0.75rem;
  }

  .family-card {
    padding: 0.75rem;
    min-height: 120px;
    display: flex;
    flex-direction: column;
    gap: 0.55rem;
  }

  .family-card-body {
    font-size: 0.82rem;
    line-height: 1.45;
  }

  .message-list,
  .event-list {
    margin-top: 0.8rem;
    display: flex;
    flex-direction: column;
    gap: 0.55rem;
  }

  .message-card {
    padding: 0.75rem;
    background: rgba(2, 6, 23, 0.42);
  }

  .message-child {
    box-shadow: inset 0 0 0 1px rgba(96, 165, 250, 0.22);
  }

  .message-content {
    margin-top: 0.65rem;
    white-space: pre-wrap;
    word-break: break-word;
    font-family: inherit;
    font-size: 0.82rem;
    line-height: 1.45;
    color: #e2e8f0;
    background: rgba(15, 23, 42, 0.42);
    border-radius: 12px;
    padding: 0.75rem;
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
    display: grid;
    gap: 0.25rem;
  }

  .empty-state {
    color: #94a3b8;
    background: rgba(15, 23, 42, 0.34);
    border: 1px dashed rgba(71, 85, 105, 0.28);
  }

  .error-banner {
    color: #fecaca;
    background: rgba(127, 29, 29, 0.78);
    border: 1px solid rgba(248, 113, 113, 0.28);
  }

  @media (max-width: 960px) {
    .trace-app {
      grid-template-columns: 1fr;
    }

    .trace-sidebar {
      border-right: none;
      border-bottom: 1px solid rgba(71, 85, 105, 0.26);
      max-height: 42%;
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
