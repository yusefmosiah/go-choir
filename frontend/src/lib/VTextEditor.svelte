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
  import {
    createDocument,
    createRevision,
    ensureDocumentManifest,
    getDocument,
    getRevision,
    listRevisions,
    openDocumentStream,
    submitAgentRevision,
  } from './vtext.js';

  export let currentUser = null;
  export let appContext = {};

  const dispatch = createEventDispatcher();

  let loading = true;
  let submitting = false;
  let agentPending = false;
  let error = '';
  let saveStatus = '';
  let currentDoc = null;
  let currentRevision = null;
  let revisions = [];
  let activeRevisionIndex = -1;
  let editorValue = '';
  let initializedKey = '';
  let latestHeadRevisionId = '';
  let pendingHeadRevisionId = '';
  let newVersionAvailable = false;
  let streamSource = null;
  let streamDocId = '';

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

  function buildRevisionMetadata() {
    return {
      source_path: appContext.sourcePath || '',
      seed_prompt: appContext.seedPrompt || '',
      conductor_loop_id: appContext.conductorLoopId || '',
    };
  }

  function hasAppAgentRevision() {
    return revisions.some((rev) => rev.author_kind === 'appagent') || currentRevision?.author_kind === 'appagent';
  }

  function synthStatusLabel() {
    return hasAppAgentRevision() ? 'Revising…' : 'Writing first draft…';
  }

  function clearNewVersionIndicator() {
    pendingHeadRevisionId = '';
    newVersionAvailable = false;
  }

  function closeDocumentStream() {
    if (streamSource) {
      streamSource.close();
      streamSource = null;
    }
    streamDocId = '';
  }

  function connectDocumentStream(docId) {
    if (!docId) return;
    if (streamSource && streamDocId === docId) return;
    closeDocumentStream();
    streamDocId = docId;
    streamSource = openDocumentStream(docId, {
      onEvent: (event) => {
        void handleDocumentStreamEvent(event);
      },
      onError: () => {
        // EventSource retries automatically. Each reconnect receives a fresh
        // snapshot from the server, which re-synchronizes the editor.
      },
    });
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
    const knownHeadId = latestHeadRevisionId || currentDoc?.current_revision_id || '';
    if (summary.revision_id === knownHeadId) {
      clearNewVersionIndicator();
    }
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

  async function ensureFileManifest() {
    if (!currentDoc?.doc_id || appContext.sourcePath) return;
    const manifest = await ensureDocumentManifest(currentDoc.doc_id);
    if (!manifest?.source_path) return;
    const bits = manifest.source_path.split('/');
    appContext = {
      ...appContext,
      sourcePath: manifest.source_path,
      fileName: appContext.fileName || bits[bits.length - 1] || '',
    };
    initializedKey = getContextKey(appContext);
  }

  async function reloadDocument(preferredRevisionId = '') {
    currentDoc = await getDocument(currentDoc.doc_id);
    latestHeadRevisionId = currentDoc.current_revision_id || latestHeadRevisionId;
    await refreshRevisions(currentDoc.doc_id, preferredRevisionId);
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

  async function applyHeadChange(revisionId) {
    if (!currentDoc || !revisionId) return;
    if (currentRevision?.revision_id === revisionId) {
      clearNewVersionIndicator();
      return;
    }

    const shouldAutoAdvance = !isViewingHistorical && !isDirty;
    if (!shouldAutoAdvance) {
      pendingHeadRevisionId = revisionId;
      newVersionAvailable = true;
      saveStatus = 'New version available';
      return;
    }

    const hadAgentVersionBefore = hasAppAgentRevision();
    await reloadDocument(revisionId);
    clearNewVersionIndicator();
    saveStatus = hadAgentVersionBefore ? 'Agent created next version' : 'First draft ready';
    if (appContext.sourcePath) {
      await writeThroughToFile(editorValue);
    }
  }

  async function handleDocumentStreamEvent(event) {
    if (!event || event.doc_id !== currentDoc?.doc_id) return;

    switch (event.kind) {
      case 'snapshot':
        latestHeadRevisionId = event.current_revision_id || latestHeadRevisionId;
        agentPending = !!event.pending;
        if (agentPending) {
          saveStatus = synthStatusLabel();
        }
        if (latestHeadRevisionId && currentRevision?.revision_id !== latestHeadRevisionId) {
          await applyHeadChange(latestHeadRevisionId);
        }
        return;
      case 'synth_started':
        agentPending = true;
        error = '';
        saveStatus = synthStatusLabel();
        return;
      case 'synth_completed':
        agentPending = false;
        return;
      case 'revision_created':
        latestHeadRevisionId = event.current_revision_id || event.revision_id || latestHeadRevisionId;
        return;
      case 'head_changed':
        latestHeadRevisionId = event.current_revision_id || event.revision_id || latestHeadRevisionId;
        agentPending = false;
        await applyHeadChange(latestHeadRevisionId);
        return;
      case 'synth_failed':
        agentPending = false;
        error = event.error || 'Agent revision failed';
        saveStatus = 'Revision failed';
        return;
      default:
        return;
    }
  }

  async function loadContext() {
    loading = true;
    submitting = false;
    agentPending = false;
    error = '';
    saveStatus = '';
    currentDoc = null;
    currentRevision = null;
    revisions = [];
    activeRevisionIndex = -1;
    editorValue = '';
    latestHeadRevisionId = '';
    clearNewVersionIndicator();
    closeDocumentStream();

    try {
      const initialValue = appContext.initialContent ?? appContext.seedPrompt ?? '';

      if (appContext.docId) {
        currentDoc = await getDocument(appContext.docId);
        latestHeadRevisionId = currentDoc.current_revision_id || '';
        await refreshRevisions(currentDoc.doc_id);
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
      }

      if (currentDoc?.doc_id) {
        await ensureFileManifest();
        connectDocumentStream(currentDoc.doc_id);
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
    if (!currentDoc || loading || submitting || agentPending) return;

    submitting = true;
    error = '';
    saveStatus = 'Saving user version…';

    try {
      await writeThroughToFile(editorValue);
      await saveUserVersion();
      saveStatus = 'Submitting revise event…';
      await submitAgentRevision(currentDoc.doc_id, {
        intent: 'revise',
      });
      agentPending = true;
      saveStatus = synthStatusLabel();
    } catch (err) {
      if (err instanceof AuthRequiredError) {
        dispatch('authexpired');
        return;
      }
      error = err.message || 'Failed to prompt VText';
      saveStatus = 'Prompt failed';
      agentPending = false;
    } finally {
      submitting = false;
    }
  }

  async function handlePrevVersion() {
    if (activeRevisionIndex <= 0 || submitting) return;
    error = '';
    saveStatus = '';
    await loadRevisionAt(activeRevisionIndex - 1);
    saveStatus = `Viewing v${activeRevisionIndex}`;
  }

  async function handleNextVersion() {
    if (activeRevisionIndex < 0 || activeRevisionIndex >= revisions.length - 1 || submitting) return;
    error = '';
    saveStatus = '';
    await loadRevisionAt(activeRevisionIndex + 1);
    if (activeRevisionIndex === revisions.length - 1) {
      saveStatus = 'Viewing latest version';
    } else {
      saveStatus = `Viewing v${activeRevisionIndex}`;
    }
  }

  async function handleShowLatestVersion() {
    if (!currentDoc || !pendingHeadRevisionId) return;
    error = '';
    await reloadDocument(pendingHeadRevisionId);
    clearNewVersionIndicator();
    saveStatus = 'Viewing latest version';
  }

  $: contextKey = getContextKey(appContext);
  $: if (contextKey && contextKey !== initializedKey) {
    initializedKey = contextKey;
    loadContext();
  }

  $: isViewingHistorical = revisions.length > 0 && activeRevisionIndex !== revisions.length - 1;
  $: isDirty = !!currentRevision && !isViewingHistorical && editorValue !== (currentRevision.content || '');
  $: versionLabel = activeRevisionIndex >= 0 ? `v${activeRevisionIndex}` : 'v0';
  $: promptLabel = submitting ? 'Submitting…' : agentPending ? 'Revising…' : 'Revise';
  $: navDisabled = loading || submitting;

  onMount(() => {
    if (!initializedKey) {
      initializedKey = contextKey;
      loadContext();
    }
  });

  onDestroy(() => {
    closeDocumentStream();
  });
</script>

<div class="vtext-editor" data-vtext-editor>
  <textarea
    class="editor"
    data-vtext-editor-area
    bind:value={editorValue}
    placeholder="Start typing the document..."
    disabled={loading}
    readonly={isViewingHistorical}
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
      disabled={navDisabled || activeRevisionIndex <= 0}
    >
      &lt;
    </button>
    <button
      class="nav-btn"
      data-vtext-next
      aria-label={activeRevisionIndex >= 0 && activeRevisionIndex < revisions.length - 1 ? `Newer version (v${activeRevisionIndex + 1})` : 'At latest version'}
      title={activeRevisionIndex >= 0 && activeRevisionIndex < revisions.length - 1 ? `Go to v${activeRevisionIndex + 1}` : 'At latest version'}
      on:click={handleNextVersion}
      disabled={navDisabled || activeRevisionIndex < 0 || activeRevisionIndex >= revisions.length - 1}
    >
      &gt;
    </button>
  </div>

  <button
    class="prompt-btn"
    data-vtext-prompt
    data-vtext-save
    on:click={handlePrompt}
    disabled={loading || submitting || agentPending || isViewingHistorical}
  >
    {promptLabel}
  </button>

  {#if newVersionAvailable}
    <button
      class="update-pill"
      data-vtext-new-version
      on:click={handleShowLatestVersion}
      disabled={loading}
    >
      New version available
    </button>
  {/if}

  {#if error}
    <div class="error-float" role="alert">{error}</div>
  {/if}

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
    top: 0.75rem;
    right: 0.75rem;
    display: flex;
    align-items: center;
    gap: 0.4rem;
    z-index: 2;
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
  .prompt-btn,
  .update-pill {
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

  .update-pill {
    position: absolute;
    right: 0.85rem;
    bottom: 4rem;
    z-index: 2;
    border-radius: 999px;
    padding: 0.55rem 0.9rem;
    font-size: 0.76rem;
    font-weight: 700;
    color: #bfdbfe;
  }

  .error-float {
    position: absolute;
    left: 0.85rem;
    bottom: 0.85rem;
    z-index: 2;
    max-width: min(32rem, calc(100% - 8rem));
    border-radius: 12px;
    border: 1px solid rgba(248, 113, 113, 0.32);
    background: rgba(127, 29, 29, 0.82);
    color: #fecaca;
    padding: 0.55rem 0.75rem;
    font-size: 0.75rem;
    line-height: 1.4;
    backdrop-filter: blur(10px);
  }

  .nav-btn:hover:enabled,
  .prompt-btn:hover:enabled,
  .update-pill:hover:enabled {
    transform: translateY(-1px);
    background: rgba(30, 41, 59, 0.92);
    border-color: rgba(96, 165, 250, 0.42);
  }

  .nav-btn:disabled,
  .prompt-btn:disabled,
  .update-pill:disabled {
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

    .prompt-btn {
      right: 0.7rem;
      bottom: 0.7rem;
      padding: 0.58rem 0.82rem;
    }

    .update-pill {
      right: 0.7rem;
      bottom: 3.75rem;
    }

    .error-float {
      left: 0.7rem;
      bottom: 6.6rem;
      max-width: calc(100% - 1.4rem);
    }
  }
</style>
