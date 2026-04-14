<!--
  VTextEditor — minimal versioned document surface for go-choir.

  This is the first-cut VText UI:
    - one large editable text area
    - no sidebar-first layout
    - footer prompt/version action only
    - accepts appContext for blank prompts or file-backed documents
    - speaks the vtext API directly
-->
<script>
  import { createEventDispatcher, onMount } from 'svelte';
  import { AuthRequiredError, fetchWithRenewal } from './auth.js';
  import { createDocument, getDocument, getRevision, createRevision } from './vtext.js';

  export let currentUser = null;
  export let appContext = {};

  const dispatch = createEventDispatcher();

  let loading = true;
  let saving = false;
  let error = '';
  let saveStatus = '';
  let currentDoc = null;
  let currentRevision = null;
  let editorValue = '';
  let initializedKey = '';

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

  async function loadContext() {
    loading = true;
    error = '';
    saveStatus = '';
    currentDoc = null;
    currentRevision = null;
    editorValue = '';

    try {
      const initialValue = appContext.initialContent ?? appContext.seedPrompt ?? '';

      if (appContext.docId) {
        currentDoc = await getDocument(appContext.docId);
        if (currentDoc.current_revision_id) {
          currentRevision = await getRevision(currentDoc.current_revision_id);
          editorValue = currentRevision.content || '';
        } else {
          editorValue = initialValue || '';
        }
        saveStatus = 'Document loaded';
      } else {
        const title = normalizeTitle(appContext);
        currentDoc = await createDocument(title);
        editorValue = initialValue || '';
        if (appContext.createInitialVersion && initialValue) {
          currentRevision = await createRevision(currentDoc.doc_id, {
            content: initialValue,
            authorKind: 'user',
            authorLabel: getAuthorLabel(),
            metadata: {
              source_path: appContext.sourcePath || '',
              seed_prompt: appContext.seedPrompt || '',
              created_from: 'conductor',
              conductor_task_id: appContext.conductorTaskId || '',
            },
          });
          currentDoc = await getDocument(currentDoc.doc_id);
          saveStatus = 'Created v0';
        } else {
          currentRevision = null;
          saveStatus = initialValue
            ? 'Loaded document content'
            : 'Blank document ready';
        }
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

  async function handleVersion() {
    if (!currentDoc) return;
    saving = true;
    error = '';
    saveStatus = '';

    try {
      if (appContext.sourcePath) {
        const filePath = buildFilePath(appContext.sourcePath);
        const fileRes = await fetchWithRenewal(filePath, {
          method: 'PUT',
          headers: { 'Content-Type': 'text/plain; charset=utf-8' },
          body: editorValue,
        });
        if (!fileRes.ok) {
          const body = await fileRes.json().catch(() => ({}));
          throw new Error(body.error || `File save failed (${fileRes.status})`);
        }
      }

      const metadata = {
        source_path: appContext.sourcePath || '',
        seed_prompt: appContext.seedPrompt || '',
      };

      const revision = await createRevision(currentDoc.doc_id, {
        content: editorValue,
        authorKind: 'user',
        authorLabel: getAuthorLabel(),
        metadata,
      });

      currentRevision = revision;
      currentDoc = await getDocument(currentDoc.doc_id);
      saveStatus = 'Version saved';
    } catch (err) {
      if (err instanceof AuthRequiredError) {
        dispatch('authexpired');
        return;
      }
      error = err.message || 'Failed to save version';
      saveStatus = 'Save failed';
    } finally {
      saving = false;
    }
  }

  $: contextKey = getContextKey(appContext);
  $: if (contextKey && contextKey !== initializedKey) {
    initializedKey = contextKey;
    loadContext();
  }

  onMount(() => {
    if (!initializedKey) {
      initializedKey = contextKey;
      loadContext();
    }
  });
</script>

<div class="vtext-editor" data-vtext-editor data-etext-editor>
  {#if error}
    <div class="notice notice-error">{error}</div>
  {/if}

  {#if loading}
    <div class="notice notice-loading">Loading VText…</div>
  {/if}

  <div class="header">
    <div class="title-row">
      <h3 class="title" data-vtext-title data-etext-title>{normalizeTitle(appContext)}</h3>
      {#if appContext.sourcePath}
        <span class="source-path">{appContext.sourcePath}</span>
      {/if}
    </div>
    <div class="version-row">
      <span class="version-label">Current version</span>
      <span class="version-id">{currentRevision?.revision_id ? currentRevision.revision_id.slice(0, 8) : 'v0'}</span>
      {#if saveStatus}
        <span class="save-status" data-vtext-save-status data-etext-save-status>{saveStatus}</span>
      {/if}
    </div>
  </div>

  <textarea
    class="editor"
    data-vtext-editor-area data-etext-editor-area
    bind:value={editorValue}
    placeholder="Start typing the document..."
    disabled={loading}
  ></textarea>

  <div class="footer">
    <div class="footer-copy">
      <span class="footer-label">Prompt/apply creates the next version. Multiple edits before that count as one version.</span>
    </div>
    <button
      class="version-btn"
      data-vtext-save data-etext-save
      on:click={handleVersion}
      disabled={loading || saving}
    >
      {saving ? 'Saving…' : 'Prompt / Version'}
    </button>
  </div>
</div>

<style>
  .vtext-editor {
    display: flex;
    flex-direction: column;
    height: 100%;
    min-height: 0;
    gap: 0.75rem;
    color: #e7e7ef;
  }

  .notice {
    padding: 0.55rem 0.75rem;
    border-radius: 10px;
    font-size: 0.84rem;
  }

  .notice-error {
    background: rgba(239, 68, 68, 0.12);
    border: 1px solid rgba(239, 68, 68, 0.25);
    color: #fca5a5;
  }

  .notice-loading {
    color: #9ca3af;
  }

  .header {
    display: flex;
    flex-direction: column;
    gap: 0.35rem;
    min-height: 0;
  }

  .title-row,
  .version-row {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    flex-wrap: wrap;
  }

  .title {
    margin: 0;
    font-size: 1.05rem;
    font-weight: 650;
    color: #fff;
  }

  .source-path,
  .version-label,
  .version-id,
  .save-status,
  .footer-label {
    font-size: 0.78rem;
    color: #a7a7bb;
  }

  .version-id {
    color: #d9d9ee;
    background: rgba(255, 255, 255, 0.06);
    padding: 0.2rem 0.45rem;
    border-radius: 999px;
  }

  .editor {
    flex: 1;
    min-height: 0;
    width: 100%;
    resize: none;
    border-radius: 16px;
    border: 1px solid rgba(255, 255, 255, 0.08);
    background: rgba(11, 12, 19, 0.98);
    color: #f3f4f6;
    padding: 1rem 1.1rem;
    font: inherit;
    line-height: 1.6;
    outline: none;
  }

  .editor:focus {
    border-color: rgba(96, 165, 250, 0.35);
    box-shadow: 0 0 0 3px rgba(59, 130, 246, 0.14);
  }

  .footer {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 0.75rem;
    flex-wrap: wrap;
  }

  .footer-copy {
    min-width: 0;
  }

  .version-btn {
    border: 1px solid rgba(96, 165, 250, 0.28);
    background: rgba(96, 165, 250, 0.16);
    color: #d9e9ff;
    border-radius: 999px;
    padding: 0.6rem 0.95rem;
    font-weight: 650;
    cursor: pointer;
  }

  .version-btn:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }
</style>
