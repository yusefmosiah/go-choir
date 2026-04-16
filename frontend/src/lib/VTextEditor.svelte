<!--
  VTextEditor — focused version-native document surface for go-choir.

  The window should feel like the document itself:
    - the text area fills almost the entire window
    - floating controls handle prompt/apply and version navigation
    - prompt/apply creates a user revision, then invokes the vtext appagent
-->
<script>
  import { createEventDispatcher, onDestroy, onMount } from 'svelte';
  import { AuthRequiredError, fetchWithRenewal } from './auth.js';
  import { listAgentEvents, listAgentRuns } from './trace.js';
  import {
    createDocument,
    getDocument,
    getRevision,
    createRevision,
    listRevisions,
    submitAgentRevision,
    getAgentRevisionStatus,
  } from './vtext.js';

  export let currentUser = null;
  export let appContext = {};

  const dispatch = createEventDispatcher();

  let loading = true;
  let prompting = false;
  let error = '';
  let saveStatus = '';
  let currentDoc = null;
  let currentRevision = null;
  let revisions = [];
  let activeRevisionIndex = -1;
  let editorValue = '';
  let initializedKey = '';
  let watchedInitialTaskId = '';
  let activityTimer = null;
  let docRuns = [];
  let docEvents = [];

  function normalizeTitle(ctx) {
    if (ctx?.windowTitle) return ctx.windowTitle;
    if (ctx?.fileName) return ctx.fileName;
    if (ctx?.sourcePath) {
      const bits = ctx.sourcePath.split('/');
      return bits[bits.length - 1] || 'VText';
    }
    return 'VText';
  }

  function getAuthorLabel() {
    return currentUser?.email || 'unknown';
  }

  function getContextKey(ctx) {
    const key = {
      allowMultiple: !!ctx?.allowMultiple,
      docId: ctx?.docId || '',
      sourcePath: ctx?.sourcePath || '',
      fileName: ctx?.fileName || '',
      windowTitle: ctx?.windowTitle || '',
      initialContent: ctx?.initialContent || '',
      seedPrompt: ctx?.seedPrompt || '',
      createInitialVersion: !!ctx?.createInitialVersion,
      initialTaskId: ctx?.initialTaskId || '',
    };
    return JSON.stringify(key);
  }

  function buildFilePath(sourcePath) {
    if (!sourcePath) return '';
    return '/api/files/' + sourcePath.split('/').map(encodeURIComponent).join('/');
  }

  function sortRevisionsChronologically(items) {
    return [...items].sort((left, right) => {
      return new Date(left.created_at).getTime() - new Date(right.created_at).getTime();
    });
  }

  function parseDate(value) {
    const time = value ? new Date(value).getTime() : 0;
    return Number.isFinite(time) ? time : 0;
  }

  function sortTasksNewestFirst(items) {
    return [...items].sort((left, right) => parseDate(right.created_at) - parseDate(left.created_at));
  }

  function sortEventsAscending(items) {
    return [...items].sort((left, right) => {
      const tsDelta = parseDate(left.ts) - parseDate(right.ts);
      if (tsDelta !== 0) return tsDelta;
      return (left.seq || 0) - (right.seq || 0);
    });
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

  function runProfile(task) {
    return task?.metadata?.agent_profile || task?.metadata?.type || 'task';
  }

  function taskParentId(task) {
    return task?.parent_run_id || task?.metadata?.parent_id || '';
  }

  function isDocTask(task, docId) {
    if (!task || !docId) return false;
    if (task?.metadata?.doc_id === docId) return true;
    return task.run_id === appContext.conductorTaskId;
  }

  function collectFamilyIds(rootTasks, allRuns) {
    const ids = new Set((rootTasks || []).map((task) => task.run_id).filter(Boolean));
    const queue = [...ids];
    while (queue.length > 0) {
      const current = queue.shift();
      for (const task of allRuns || []) {
        if (taskParentId(task) === current && !ids.has(task.run_id)) {
          ids.add(task.run_id);
          queue.push(task.run_id);
        }
      }
    }
    return ids;
  }

  function activitySummary(eventRecord) {
    const payload = parsePayload(eventRecord?.payload);
    switch (eventRecord?.kind) {
      case 'run.submitted':
        return payload.parent_id ? `Spawned from ${String(payload.parent_id).slice(0, 8)}` : 'Run submitted';
      case 'run.started':
        return `${runProfile(docRuns.find((task) => task.run_id === eventRecord.run_id))} started`;
      case 'run.completed':
        return `${runProfile(docRuns.find((task) => task.run_id === eventRecord.run_id))} completed`;
      case 'run.failed':
      case 'run.blocked':
      case 'run.cancelled':
        return payload.error || eventRecord.kind;
      case 'channel.message':
        return `${payload.role || 'worker'} posted an update`;
      case 'vtext.agent_revision.started':
        return 'Writing next version';
      case 'vtext.agent_revision.progress':
        return payload.status || 'Revision in progress';
      case 'vtext.agent_revision.completed':
        return 'Next version ready';
      case 'tool.invoked':
        return `Used ${payload.tool || 'tool'}`;
      default:
        return eventRecord?.kind || '';
    }
  }

  function buildRevisionMetadata() {
    return {
      source_path: appContext.sourcePath || '',
      seed_prompt: appContext.seedPrompt || '',
      conductor_run_id: appContext.conductorTaskId || '',
    };
  }

  async function refreshRevisions(docId, preferredRevisionId = '') {
    const listed = await listRevisions(docId);
    const ordered = sortRevisionsChronologically(listed.revisions || []);
    revisions = ordered;

    if (ordered.length === 0) {
      activeRevisionIndex = -1;
      currentRevision = null;
      return;
    }

    let nextIndex = ordered.length - 1;
    if (preferredRevisionId) {
      const found = ordered.findIndex((rev) => rev.revision_id === preferredRevisionId);
      if (found >= 0) {
        nextIndex = found;
      }
    }

    await loadRevisionAt(nextIndex);
  }

  async function loadRevisionAt(index) {
    if (index < 0 || index >= revisions.length) return;
    const summary = revisions[index];
    const revision = await getRevision(summary.revision_id);
    currentRevision = revision;
    activeRevisionIndex = index;
    editorValue = revision.content || '';
  }

  async function writeThroughToFile(content) {
    if (!appContext.sourcePath) return;
    const filePath = buildFilePath(appContext.sourcePath);
    const fileRes = await fetchWithRenewal(filePath, {
      method: 'PUT',
      headers: { 'Content-Type': 'text/plain; charset=utf-8' },
      body: content,
    });
    if (!fileRes.ok) {
      const body = await fileRes.json().catch(() => ({}));
      throw new Error(body.error || `File save failed (${fileRes.status})`);
    }
  }

  async function reloadDocument(preferredRevisionId = '') {
    currentDoc = await getDocument(currentDoc.doc_id);
    await refreshRevisions(currentDoc.doc_id, preferredRevisionId);
    await refreshActivity();
  }

  async function saveUserVersion() {
    const revision = await createRevision(currentDoc.doc_id, {
      content: editorValue,
      authorKind: 'user',
      authorLabel: getAuthorLabel(),
      metadata: buildRevisionMetadata(),
      parentRevisionId: currentRevision?.revision_id || '',
    });

    await reloadDocument(revision.revision_id);
    return revision;
  }

  async function refreshActivity() {
    if (!currentDoc?.doc_id) {
      docRuns = [];
      docEvents = [];
      return;
    }

    const [taskResp, eventResp] = await Promise.all([
      listAgentRuns(200, { channelId: currentDoc.doc_id }),
      listAgentEvents({ channelId: currentDoc.doc_id, limit: 400 }),
    ]);

    const allRuns = sortTasksNewestFirst(taskResp.runs || []);
    const rootTasks = allRuns.filter((task) => isDocTask(task, currentDoc.doc_id));
    const familyIds = collectFamilyIds(rootTasks, allRuns);

    docRuns = allRuns.filter((task) => familyIds.has(task.run_id));
    docEvents = sortEventsAscending((eventResp.events || []).filter((eventRecord) => familyIds.has(eventRecord.run_id)));
  }

  async function watchAgentTask(taskId, options = {}) {
    if (!taskId) return;
    const writeThroughOnComplete = options.writeThroughOnComplete === true;
    const successStatus = options.successStatus || 'Agent created next version';

    prompting = true;
    error = '';
    saveStatus = successStatus === 'First draft ready' ? 'Waiting for first draft…' : 'Revising…';

    try {
      for (;;) {
        const status = await getAgentRevisionStatus(taskId);
        if (status.state === 'completed') {
          await reloadDocument();
          if (writeThroughOnComplete && appContext.sourcePath) {
            await writeThroughToFile(editorValue);
          }
          saveStatus = successStatus;
          return;
        }
        if (status.state === 'failed' || status.state === 'blocked' || status.state === 'cancelled') {
          throw new Error(status.error || `Agent revision ${status.state}`);
        }
        saveStatus = successStatus === 'First draft ready' ? 'Writing first draft…' : 'Revising…';
        await refreshActivity().catch(() => {});
        await new Promise((resolve) => setTimeout(resolve, 900));
      }
    } finally {
      prompting = false;
    }
  }

  async function loadContext() {
    loading = true;
    prompting = false;
    error = '';
    saveStatus = '';
    currentDoc = null;
    currentRevision = null;
    revisions = [];
    activeRevisionIndex = -1;
    editorValue = '';

    try {
      const initialValue = appContext.initialContent ?? appContext.seedPrompt ?? '';

      if (appContext.docId) {
        currentDoc = await getDocument(appContext.docId);
        await refreshRevisions(currentDoc.doc_id);
        await refreshActivity();
        if (revisions.length === 0) {
          editorValue = initialValue || '';
          saveStatus = initialValue ? 'Loaded document content' : 'Blank document ready';
        } else {
          saveStatus = 'Document loaded';
        }
      } else {
        currentDoc = await createDocument(normalizeTitle(appContext));
        editorValue = initialValue || '';

        if (appContext.createInitialVersion && initialValue) {
          const initialRevision = await createRevision(currentDoc.doc_id, {
            content: initialValue,
            authorKind: 'user',
            authorLabel: getAuthorLabel(),
            metadata: {
              ...buildRevisionMetadata(),
              created_from: 'conductor',
            },
          });
          await reloadDocument(initialRevision.revision_id);
          saveStatus = 'Created v0';
        } else {
          saveStatus = initialValue ? 'Loaded document content' : 'Blank document ready';
        }
        await refreshActivity();
      }

      if (appContext.initialTaskId && appContext.initialTaskId !== watchedInitialTaskId) {
        watchedInitialTaskId = appContext.initialTaskId;
        void watchAgentTask(appContext.initialTaskId, { successStatus: 'First draft ready' }).catch((err) => {
          if (err instanceof AuthRequiredError) {
            dispatch('authexpired');
            return;
          }
          error = err.message || 'Failed to watch initial VText task';
          saveStatus = 'First draft failed';
        });
      }
    } catch (err) {
      if (err instanceof AuthRequiredError) {
        dispatch('authexpired');
        return;
      }
      error = err.message || 'Failed to initialize VText';
    } finally {
      loading = false;
    }
  }

  async function handlePrompt() {
    if (!currentDoc || loading || prompting) return;

    prompting = true;
    error = '';
    saveStatus = 'Saving user version…';

    try {
      await writeThroughToFile(editorValue);
      await saveUserVersion();
      saveStatus = 'Submitting revise event…';

      const submitted = await submitAgentRevision(currentDoc.doc_id, {
        intent: 'revise',
      });
      await watchAgentTask(submitted.run_id, {
        writeThroughOnComplete: !!appContext.sourcePath,
        successStatus: 'Agent created next version',
      });
    } catch (err) {
      if (err instanceof AuthRequiredError) {
        dispatch('authexpired');
        return;
      }
      error = err.message || 'Failed to prompt VText';
      saveStatus = 'Prompt failed';
    } finally {
      prompting = false;
    }
  }

  async function handlePrevVersion() {
    if (activeRevisionIndex <= 0 || prompting) return;
    error = '';
    saveStatus = '';
    await loadRevisionAt(activeRevisionIndex - 1);
    saveStatus = `Viewing v${activeRevisionIndex}`;
  }

  async function handleNextVersion() {
    if (activeRevisionIndex < 0 || activeRevisionIndex >= revisions.length - 1 || prompting) return;
    error = '';
    saveStatus = '';
    await loadRevisionAt(activeRevisionIndex + 1);
    if (activeRevisionIndex === revisions.length - 1) {
      saveStatus = 'Viewing latest version';
    } else {
      saveStatus = `Viewing v${activeRevisionIndex}`;
    }
  }

  $: contextKey = getContextKey(appContext);
  $: if (contextKey && contextKey !== initializedKey) {
    initializedKey = contextKey;
    loadContext();
  }

  $: isViewingHistorical = revisions.length > 0 && activeRevisionIndex !== revisions.length - 1;
  $: versionLabel = activeRevisionIndex >= 0 ? `v${activeRevisionIndex}` : 'v0';
  $: activeDocTasks = docRuns.filter((task) => task.state === 'pending' || task.state === 'running');
  $: researcherCount = docRuns.filter((task) => runProfile(task) === 'researcher').length;
  $: superCount = docRuns.filter((task) => runProfile(task) === 'super').length;
  $: recentActivity = [...docEvents].slice(-3).reverse();
  $: activityStatus = isViewingHistorical
    ? `Viewing ${versionLabel}`
    : activeDocTasks.length > 0
    ? 'Delegating…'
    : 'Ready';

  onMount(() => {
    if (!initializedKey) {
      initializedKey = contextKey;
      loadContext();
    }
    activityTimer = setInterval(() => {
      if (!currentDoc?.doc_id) return;
      refreshActivity().catch((err) => {
        if (err instanceof AuthRequiredError) {
          dispatch('authexpired');
        }
      });
    }, 1500);
  });

  onDestroy(() => {
    if (activityTimer) {
      clearInterval(activityTimer);
      activityTimer = null;
    }
  });
</script>

<div class="vtext-editor" data-vtext-editor>
  <textarea
    class="editor"
    data-vtext-editor-area
    bind:value={editorValue}
    placeholder="Start typing the document..."
    disabled={loading}
    readonly={isViewingHistorical || prompting}
    spellcheck="true"
  ></textarea>

  <div class="nav-float">
    <span class="nav-version" data-vtext-version>{versionLabel}</span>
    <button
      class="nav-btn"
      data-vtext-prev
      aria-label={activeRevisionIndex > 0 ? `Older version (v${activeRevisionIndex - 1})` : 'At oldest version'}
      title={activeRevisionIndex > 0 ? `Go to v${activeRevisionIndex - 1}` : 'At oldest version'}
      on:click={handlePrevVersion}
      disabled={loading || prompting || activeRevisionIndex <= 0}
    >
      &lt;
    </button>
    <button
      class="nav-btn"
      data-vtext-next
      aria-label={activeRevisionIndex >= 0 && activeRevisionIndex < revisions.length - 1 ? `Newer version (v${activeRevisionIndex + 1})` : 'At latest version'}
      title={activeRevisionIndex >= 0 && activeRevisionIndex < revisions.length - 1 ? `Go to v${activeRevisionIndex + 1}` : 'At latest version'}
      on:click={handleNextVersion}
      disabled={loading || prompting || activeRevisionIndex < 0 || activeRevisionIndex >= revisions.length - 1}
    >
      &gt;
    </button>
  </div>

  <div class="activity-float" data-vtext-activity>
    <div class="activity-header">
      <span class="activity-status">{activityStatus}</span>
      {#if researcherCount > 0}
        <span class="activity-badge">researcher ×{researcherCount}</span>
      {/if}
      {#if superCount > 0}
        <span class="activity-badge">super ×{superCount}</span>
      {/if}
    </div>
    {#if error}
      <div class="activity-error">{error}</div>
    {:else if recentActivity.length > 0}
      <div class="activity-events">
        {#each recentActivity as eventRecord (eventRecord.event_id)}
          <div class="activity-line">{activitySummary(eventRecord)}</div>
        {/each}
      </div>
    {/if}
  </div>

  <button
    class="prompt-btn"
    data-vtext-prompt
    data-vtext-save
    on:click={handlePrompt}
    disabled={loading || prompting || isViewingHistorical}
  >
    {prompting ? 'Revising…' : 'Revise'}
  </button>

  <div class="sr-only" aria-live="polite" data-vtext-save-status>{saveStatus}</div>
  <div class="sr-only" aria-live="polite">{loading ? 'Loading VText…' : ''}</div>
</div>

<style>
  .vtext-editor {
    position: relative;
    height: 100%;
    min-height: 0;
    color: #eef2ff;
    background:
      radial-gradient(circle at top right, rgba(59, 130, 246, 0.08), transparent 30%),
      rgba(9, 10, 16, 0.98);
  }

  .editor {
    width: 100%;
    height: 100%;
    resize: none;
    border: none;
    background: transparent;
    color: #f8fafc;
    padding: 1rem 1rem 1.4rem;
    font: inherit;
    line-height: 1.65;
    outline: none;
  }

  .editor::placeholder {
    color: rgba(203, 213, 225, 0.45);
  }

  .editor:focus {
    box-shadow: inset 0 0 0 1px rgba(96, 165, 250, 0.22);
  }

  .editor[readonly] {
    color: rgba(226, 232, 240, 0.82);
  }

  .nav-float {
    position: absolute;
    display: flex;
    align-items: center;
    gap: 0.4rem;
    z-index: 2;
  }

  .nav-float {
    top: 0.75rem;
    right: 0.75rem;
  }

  .activity-float {
    position: absolute;
    top: 0.75rem;
    left: 0.75rem;
    z-index: 2;
    max-width: min(28rem, 62%);
    padding: 0.58rem 0.72rem;
    border-radius: 14px;
    border: 1px solid rgba(148, 163, 184, 0.16);
    background: rgba(15, 23, 42, 0.72);
    color: #dbe7ff;
    backdrop-filter: blur(10px);
  }

  .activity-header {
    display: flex;
    align-items: center;
    gap: 0.45rem;
    flex-wrap: wrap;
  }

  .activity-status {
    font-size: 0.76rem;
    font-weight: 700;
    color: #f8fafc;
  }

  .activity-badge {
    border-radius: 999px;
    border: 1px solid rgba(96, 165, 250, 0.2);
    background: rgba(30, 41, 59, 0.78);
    padding: 0.12rem 0.46rem;
    font-size: 0.68rem;
    color: #bfdbfe;
  }

  .activity-events {
    margin-top: 0.38rem;
    display: flex;
    flex-direction: column;
    gap: 0.18rem;
  }

  .activity-line,
  .activity-error {
    font-size: 0.72rem;
    line-height: 1.35;
    color: #cbd5e1;
  }

  .activity-error {
    margin-top: 0.38rem;
    color: #fecaca;
  }

  .nav-version {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    min-width: 2.3rem;
    height: 1.95rem;
    padding: 0 0.6rem;
    border-radius: 999px;
    border: 1px solid rgba(148, 163, 184, 0.16);
    background: rgba(15, 23, 42, 0.72);
    color: #e2e8f0;
    font-size: 0.76rem;
    font-weight: 650;
    backdrop-filter: blur(8px);
  }

  .nav-btn,
  .prompt-btn {
    border: 1px solid rgba(96, 165, 250, 0.28);
    background: rgba(15, 23, 42, 0.82);
    color: #e0ecff;
    cursor: pointer;
    backdrop-filter: blur(10px);
    transition: transform 120ms ease, background 120ms ease, border-color 120ms ease;
  }

  .nav-btn {
    width: 1.95rem;
    height: 1.95rem;
    border-radius: 999px;
    font-size: 0.92rem;
    font-weight: 700;
  }

  .prompt-btn {
    position: absolute;
    right: 0.85rem;
    bottom: 0.85rem;
    z-index: 2;
    border-radius: 999px;
    padding: 0.62rem 0.95rem;
    font-size: 0.82rem;
    font-weight: 700;
  }

  .nav-btn:hover:enabled,
  .prompt-btn:hover:enabled {
    transform: translateY(-1px);
    background: rgba(30, 41, 59, 0.92);
    border-color: rgba(96, 165, 250, 0.42);
  }

  .nav-btn:disabled,
  .prompt-btn:disabled {
    opacity: 0.46;
    cursor: not-allowed;
  }

  .sr-only {
    position: absolute;
    width: 1px;
    height: 1px;
    padding: 0;
    margin: -1px;
    overflow: hidden;
    clip: rect(0, 0, 0, 0);
    white-space: nowrap;
    border: 0;
  }

  @media (max-width: 768px) {
    .editor {
      padding: 0.85rem 0.85rem 1.25rem;
    }

    .activity-float {
      left: 0.7rem;
      top: 0.7rem;
      max-width: calc(100% - 6rem);
    }

    .prompt-btn {
      right: 0.7rem;
      bottom: 0.7rem;
      padding: 0.58rem 0.82rem;
    }
  }
</style>
