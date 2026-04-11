<!--
  ETextEditor — e-text document editing component for the desktop shell.

  Supports:
    - Document creation with durable identity (VAL-ETEXT-001)
    - Direct user edits creating canonical user-authored revisions (VAL-ETEXT-002)
    - Document list and document opening
    - Revision history with explicit attribution metadata (VAL-ETEXT-006)
    - Historical snapshot viewing without mutating head (VAL-ETEXT-007)
    - Diff view comparing selected revisions (VAL-ETEXT-008)
    - Blame view identifying last editor per section (VAL-ETEXT-009)
    - Citations and metadata round-tripping (VAL-ETEXT-010)

  All API calls use cookie-backed same-origin auth via fetchWithRenewal.

  Data attributes for test targeting:
    data-etext-editor      — root editor container
    data-etext-doclist     — document list panel
    data-etext-docitem     — individual document in the list
    data-etext-newdoc      — new document button
    data-etext-newdoc-title — new document title input
    data-etext-newdoc-submit — new document submit button
    data-etext-editor-area — text editing textarea
    data-etext-save        — save button
    data-etext-title       — document title display
    data-etext-citations   — citations section
    data-etext-metadata    — metadata section
    data-etext-history-btn — button to open history view
    data-etext-back        — button to go back to document list
-->
<script>
  import { createEventDispatcher } from 'svelte';
  import { onMount } from 'svelte';
  import {
    createDocument, listDocuments, getDocument, updateDocument, deleteDocument,
    createRevision, getRevision, getHistory, getDiff, getBlame,
    submitAgentRevision,
  } from './etext.js';
  import { AuthRequiredError, fetchWithRenewal } from './auth.js';

  export let currentUser = null;

  const dispatch = createEventDispatcher();

  // ---- View state ----
  // 'list' | 'editor' | 'history' | 'snapshot' | 'diff' | 'blame'
  let view = 'list';
  let error = '';
  let loading = false;

  // ---- Document list state ----
  let documents = [];

  // ---- New document state ----
  let newDocTitle = '';
  let showNewDocForm = false;

  // ---- Current document/revision state ----
  let currentDoc = null;
  let currentRevision = null;
  let editContent = '';
  let editCitations = '';
  let editMetadata = '';
  let saveStatus = '';
  let saving = false;

  // ---- History state ----
  let historyEntries = [];

  // ---- Snapshot state (viewing historical revision without mutating head) ----
  let snapshotRevision = null;

  // ---- Diff state ----
  let diffResult = null;
  let diffFromId = '';
  let diffToId = '';

  // ---- Blame state ----
  let blameResult = null;

  // ---- Agent revision state (VAL-ETEXT-003, VAL-ETEXT-004) ----
  let agentPrompt = '';
  let agentTaskId = '';
  let agentStatus = ''; // 'pending', 'running', 'completed', 'failed', ''
  let agentSubmitting = false;

  // ---- Document list ----

  async function loadDocuments() {
    error = '';
    loading = true;
    try {
      const result = await listDocuments();
      documents = result.documents || [];
    } catch (err) {
      if (err instanceof AuthRequiredError) {
        dispatch('authexpired');
        return;
      }
      error = err.message || 'Failed to load documents';
    } finally {
      loading = false;
    }
  }

  async function handleCreateDocument() {
    if (!newDocTitle.trim()) return;
    error = '';
    loading = true;
    try {
      const doc = await createDocument(newDocTitle.trim());
      documents = [...documents, doc];
      newDocTitle = '';
      showNewDocForm = false;
      // Open the newly created document
      await openDocument(doc.doc_id);
    } catch (err) {
      if (err instanceof AuthRequiredError) {
        dispatch('authexpired');
        return;
      }
      error = err.message || 'Failed to create document';
    } finally {
      loading = false;
    }
  }

  async function openDocument(docId) {
    error = '';
    loading = true;
    try {
      const doc = await getDocument(docId);
      currentDoc = doc;

      if (doc.current_revision_id) {
        // Load the current head revision
        const rev = await getRevision(doc.current_revision_id);
        currentRevision = rev;
        editContent = rev.content || '';
        editCitations = rev.citations ? JSON.stringify(rev.citations, null, 2) : '[]';
        editMetadata = rev.metadata ? JSON.stringify(rev.metadata, null, 2) : '{}';
      } else {
        // New document with no revisions yet
        currentRevision = null;
        editContent = '';
        editCitations = '[]';
        editMetadata = '{}';
      }

      view = 'editor';
    } catch (err) {
      if (err instanceof AuthRequiredError) {
        dispatch('authexpired');
        return;
      }
      error = err.message || 'Failed to open document';
    } finally {
      loading = false;
    }
  }

  // ---- Save revision ----

  async function handleSave() {
    if (!currentDoc) return;
    error = '';
    saving = true;
    saveStatus = '';

    try {
      let citations;
      try {
        citations = JSON.parse(editCitations);
      } catch (_e) {
        citations = [];
      }

      let metadata;
      try {
        metadata = JSON.parse(editMetadata);
      } catch (_e) {
        metadata = {};
      }

      const rev = await createRevision(currentDoc.doc_id, {
        content: editContent,
        authorKind: 'user',
        authorLabel: currentUser?.username || 'unknown',
        citations,
        metadata,
      });

      currentRevision = rev;
      // Refresh the document to get the updated current_revision_id
      currentDoc = await getDocument(currentDoc.doc_id);
      saveStatus = 'Saved';
    } catch (err) {
      if (err instanceof AuthRequiredError) {
        dispatch('authexpired');
        return;
      }
      error = err.message || 'Failed to save revision';
      saveStatus = 'Save failed';
    } finally {
      saving = false;
    }
  }

  // ---- History view ----

  async function handleOpenHistory() {
    if (!currentDoc) return;
    error = '';
    loading = true;
    try {
      const result = await getHistory(currentDoc.doc_id);
      historyEntries = result.entries || [];
      view = 'history';
    } catch (err) {
      if (err instanceof AuthRequiredError) {
        dispatch('authexpired');
        return;
      }
      error = err.message || 'Failed to load history';
    } finally {
      loading = false;
    }
  }

  // ---- Snapshot view (historical revision without mutating head) ----

  async function handleOpenSnapshot(revisionId) {
    error = '';
    loading = true;
    try {
      const rev = await getRevision(revisionId);
      snapshotRevision = rev;
      view = 'snapshot';
    } catch (err) {
      if (err instanceof AuthRequiredError) {
        dispatch('authexpired');
        return;
      }
      error = err.message || 'Failed to load revision';
    } finally {
      loading = false;
    }
  }

  // ---- Diff view ----

  function handleSelectDiff(fromId, toId) {
    diffFromId = fromId;
    diffToId = toId;
    loadDiff();
  }

  async function loadDiff() {
    if (!diffFromId || !diffToId) return;
    error = '';
    loading = true;
    try {
      const result = await getDiff(diffFromId, diffToId);
      diffResult = result;
      view = 'diff';
    } catch (err) {
      if (err instanceof AuthRequiredError) {
        dispatch('authexpired');
        return;
      }
      error = err.message || 'Failed to compute diff';
    } finally {
      loading = false;
    }
  }

  // ---- Blame view ----

  async function handleOpenBlame(revisionId) {
    error = '';
    loading = true;
    try {
      const result = await getBlame(revisionId);
      blameResult = result;
      view = 'blame';
    } catch (err) {
      if (err instanceof AuthRequiredError) {
        dispatch('authexpired');
        return;
      }
      error = err.message || 'Failed to compute blame';
    } finally {
      loading = false;
    }
  }

  // ---- Agent revision (VAL-ETEXT-003, VAL-ETEXT-004) ----

  async function handleAgentRevision() {
    if (!currentDoc || !agentPrompt.trim()) return;
    error = '';
    agentSubmitting = true;
    agentStatus = 'pending';

    try {
      const resp = await submitAgentRevision(currentDoc.doc_id, agentPrompt.trim());
      agentTaskId = resp.task_id;
      agentStatus = resp.state || 'pending';
      agentPrompt = '';

      // Poll task status until completion.
      pollAgentRevision(resp.task_id);
    } catch (err) {
      if (err instanceof AuthRequiredError) {
        dispatch('authexpired');
        return;
      }
      error = err.message || 'Failed to submit agent revision';
      agentStatus = 'failed';
    } finally {
      agentSubmitting = false;
    }
  }

  async function pollAgentRevision(taskId) {
    // Poll the task status endpoint to track progress.
    const maxPolls = 120; // 120 * 500ms = 60 seconds max
    let pollCount = 0;

    const poll = async () => {
      try {
        const res = await fetchWithRenewal(`/api/agent/status?task_id=${encodeURIComponent(taskId)}`);
        if (!res.ok) {
          agentStatus = 'failed';
          return;
        }
        const data = await res.json();

        if (data.state === 'completed') {
          agentStatus = 'completed';
          // Reload the document to get the new appagent revision.
          await reloadDocument();
          return;
        } else if (data.state === 'failed' || data.state === 'cancelled') {
          agentStatus = 'failed';
          error = 'Agent revision failed: ' + (data.error || 'unknown error');
          return;
        } else if (data.state === 'running') {
          agentStatus = 'running';
        }

        pollCount++;
        if (pollCount < maxPolls) {
          setTimeout(poll, 500);
        } else {
          agentStatus = 'failed';
          error = 'Agent revision timed out';
        }
      } catch (err) {
        agentStatus = 'failed';
        error = err.message || 'Error polling agent revision status';
      }
    };

    setTimeout(poll, 200);
  }

  async function reloadDocument() {
    if (!currentDoc) return;
    try {
      const doc = await getDocument(currentDoc.doc_id);
      currentDoc = doc;

      if (doc.current_revision_id) {
        const rev = await getRevision(doc.current_revision_id);
        currentRevision = rev;
        editContent = rev.content || '';
        editCitations = rev.citations ? JSON.stringify(rev.citations, null, 2) : '[]';
        editMetadata = rev.metadata ? JSON.stringify(rev.metadata, null, 2) : '{}';
      }
    } catch (err) {
      if (err instanceof AuthRequiredError) {
        dispatch('authexpired');
      }
    }
  }

  // ---- Navigation helpers ----

  function goBackToList() {
    view = 'list';
    currentDoc = null;
    currentRevision = null;
    snapshotRevision = null;
    diffResult = null;
    blameResult = null;
    error = '';
    saveStatus = '';
    loadDocuments();
  }

  function goBackToEditor() {
    view = 'editor';
    snapshotRevision = null;
    diffResult = null;
    blameResult = null;
  }

  function goBackToHistory() {
    view = 'history';
    snapshotRevision = null;
    diffResult = null;
    blameResult = null;
  }

  // ---- Delete document ----

  async function handleDeleteDocument(docId) {
    error = '';
    try {
      await deleteDocument(docId);
      documents = documents.filter(d => d.doc_id !== docId);
      if (currentDoc && currentDoc.doc_id === docId) {
        goBackToList();
      }
    } catch (err) {
      if (err instanceof AuthRequiredError) {
        dispatch('authexpired');
        return;
      }
      error = err.message || 'Failed to delete document';
    }
  }

  // ---- Lifecycle ----

  onMount(() => {
    loadDocuments();
  });

  // ---- Citation/metadata validation ----

  $: citationsValid = (() => {
    try { JSON.parse(editCitations); return true; } catch (_e) { return false; }
  })();

  $: metadataValid = (() => {
    try { JSON.parse(editMetadata); return true; } catch (_e) { return false; }
  })();

  // ---- History entry helpers ----

  function formatTime(iso) {
    if (!iso) return '';
    try {
      const d = new Date(iso);
      return d.toLocaleString();
    } catch (_e) {
      return iso;
    }
  }

  function shortId(id) {
    if (!id) return '';
    return id.length > 8 ? id.slice(0, 8) + '…' : id;
  }
</script>

<div class="etext-editor" data-etext-editor>
  <!-- Error bar -->
  {#if error}
    <div class="error-bar">{error}</div>
  {/if}

  <!-- Loading indicator -->
  {#if loading}
    <div class="loading-bar">Loading…</div>
  {/if}

  <!-- ==================== DOCUMENT LIST VIEW ==================== -->
  {#if view === 'list'}
    <div class="doc-list" data-etext-doclist>
      <div class="doc-list-header">
        <h3>Documents</h3>
        <button class="btn btn-primary" data-etext-newdoc on:click={() => { showNewDocForm = true; }}>
          + New Document
        </button>
      </div>

      {#if showNewDocForm}
        <div class="new-doc-form">
          <input
            type="text"
            placeholder="Document title"
            bind:value={newDocTitle}
            data-etext-newdoc-title
            on:keydown={(e) => { if (e.key === 'Enter') handleCreateDocument(); }}
          />
          <button class="btn btn-primary" data-etext-newdoc-submit on:click={handleCreateDocument} disabled={!newDocTitle.trim()}>
            Create
          </button>
          <button class="btn" on:click={() => { showNewDocForm = false; newDocTitle = ''; }}>
            Cancel
          </button>
        </div>
      {/if}

      {#if documents.length === 0 && !loading}
        <div class="empty-state">
          <p>No documents yet. Create your first document to get started.</p>
        </div>
      {:else}
        <ul class="doc-items">
          {#each documents as doc (doc.doc_id)}
            <li class="doc-item" data-etext-docitem>
              <button class="doc-item-btn" on:click={() => openDocument(doc.doc_id)}>
                <span class="doc-icon">📄</span>
                <span class="doc-title">{doc.title}</span>
                <span class="doc-date">{formatTime(doc.updated_at)}</span>
              </button>
            </li>
          {/each}
        </ul>
      {/if}
    </div>

  <!-- ==================== EDITOR VIEW ==================== -->
  {:else if view === 'editor'}
    <div class="editor-view">
      <div class="editor-header">
        <button class="btn btn-small" data-etext-back on:click={goBackToList}>
          ← Back
        </button>
        <h3 class="doc-title" data-etext-title>{currentDoc?.title || 'Untitled'}</h3>
        <div class="editor-actions">
          <button
            class="btn btn-primary"
            data-etext-save
            on:click={handleSave}
            disabled={saving}
          >
            {saving ? 'Saving…' : 'Save'}
          </button>
          {#if saveStatus}
            <span class="save-status" data-etext-save-status>{saveStatus}</span>
          {/if}
          <button class="btn" data-etext-history-btn on:click={handleOpenHistory}>
            History
          </button>
        </div>
      </div>

      <div class="agent-revision-bar" data-etext-agent-revision>
        <div class="agent-revision-input">
          <input
            type="text"
            placeholder="Ask the agent to revise…"
            bind:value={agentPrompt}
            data-etext-agent-prompt
            disabled={agentSubmitting || agentStatus === 'running' || agentStatus === 'pending'}
            on:keydown={(e) => { if (e.key === 'Enter') handleAgentRevision(); }}
          />
          <button
            class="btn btn-primary btn-agent"
            data-etext-agent-submit
            on:click={handleAgentRevision}
            disabled={agentSubmitting || !agentPrompt.trim() || agentStatus === 'running' || agentStatus === 'pending'}
          >
            {agentSubmitting ? 'Submitting…' : '🤖 Revise'}
          </button>
        </div>
        {#if agentStatus === 'running' || agentStatus === 'pending'}
          <div class="agent-progress" data-etext-agent-progress>
            <span class="agent-progress-dot"></span>
            Agent is revising…
          </div>
        {/if}
        {#if agentStatus === 'completed'}
          <div class="agent-completed" data-etext-agent-completed>
            ✓ Revision complete
          </div>
        {/if}
        {#if agentStatus === 'failed'}
          <div class="agent-failed" data-etext-agent-failed>
            ✗ Revision failed
          </div>
        {/if}
      </div>

      <div class="editor-body">
        <textarea
          class="editor-textarea"
          data-etext-editor-area
          bind:value={editContent}
          placeholder="Start writing…"
        ></textarea>

        <div class="editor-sidebar">
          <details class="sidebar-section" data-etext-citations>
            <summary>Citations {!citationsValid ? '⚠️' : ''}</summary>
            <textarea
              class="json-textarea"
              bind:value={editCitations}
              class:invalid={!citationsValid}
              placeholder="Citations JSON array"
            ></textarea>
          </details>

          <details class="sidebar-section" data-etext-metadata>
            <summary>Metadata {!metadataValid ? '⚠️' : ''}</summary>
            <textarea
              class="json-textarea"
              bind:value={editMetadata}
              class:invalid={!metadataValid}
              placeholder="Metadata JSON object"
            ></textarea>
          </details>
        </div>
      </div>
    </div>

  <!-- ==================== HISTORY VIEW ==================== -->
  {:else if view === 'history'}
    <div class="history-view">
      <div class="history-header">
        <button class="btn btn-small" on:click={goBackToEditor}>
          ← Editor
        </button>
        <h3>History: {currentDoc?.title || 'Untitled'}</h3>
      </div>

      {#if historyEntries.length === 0 && !loading}
        <div class="empty-state">
          <p>No revisions yet. Save your first edit to create a revision.</p>
        </div>
      {:else}
        <ul class="history-entries" data-etext-history>
          {#each historyEntries as entry, i (entry.revision_id)}
            <li class="history-entry" data-etext-history-entry>
              <div class="entry-main">
                <span class="entry-rev" title={entry.revision_id}>{shortId(entry.revision_id)}</span>
                <span class="entry-author" data-etext-history-author-kind={entry.author_kind}>
                  {entry.author_kind === 'user' ? '👤' : '🤖'}
                  {entry.author_label}
                </span>
                <span class="entry-time">{formatTime(entry.created_at)}</span>
              </div>
              <div class="entry-actions">
                <button class="btn btn-small" on:click={() => handleOpenSnapshot(entry.revision_id)}>
                  View
                </button>
                {#if entry.author_kind === 'user'}
                  <button class="btn btn-small" on:click={() => handleOpenBlame(entry.revision_id)}>
                    Blame
                  </button>
                {/if}
                {#if i < historyEntries.length - 1}
                  <button class="btn btn-small" on:click={() => handleSelectDiff(historyEntries[i + 1].revision_id, entry.revision_id)}>
                    Diff ↓
                  </button>
                {/if}
                {#if i > 0}
                  <button class="btn btn-small" on:click={() => handleSelectDiff(entry.revision_id, historyEntries[i - 1].revision_id)}>
                    Diff ↑
                  </button>
                {/if}
              </div>
            </li>
          {/each}
        </ul>
      {/if}
    </div>

  <!-- ==================== SNAPSHOT VIEW ==================== -->
  {:else if view === 'snapshot'}
    <div class="snapshot-view">
      <div class="snapshot-header">
        <button class="btn btn-small" on:click={goBackToHistory}>
          ← History
        </button>
        <h3>Snapshot: {shortId(snapshotRevision?.revision_id)}</h3>
        <span class="snapshot-author">
          {snapshotRevision?.author_kind === 'user' ? '👤' : '🤖'}
          {snapshotRevision?.author_label}
        </span>
        <span class="snapshot-time">{formatTime(snapshotRevision?.created_at)}</span>
      </div>

      <div class="snapshot-content" data-etext-snapshot-content>
        <pre>{snapshotRevision?.content || ''}</pre>
      </div>

      {#if snapshotRevision?.citations && snapshotRevision.citations.length > 0}
        <details class="sidebar-section" data-etext-snapshot-citations>
          <summary>Citations</summary>
          <pre>{JSON.stringify(snapshotRevision.citations, null, 2)}</pre>
        </details>
      {/if}

      {#if snapshotRevision?.metadata && Object.keys(snapshotRevision.metadata).length > 0}
        <details class="sidebar-section" data-etext-snapshot-metadata>
          <summary>Metadata</summary>
          <pre>{JSON.stringify(snapshotRevision.metadata, null, 2)}</pre>
        </details>
      {/if}

      <div class="snapshot-notice">
        Viewing a historical revision — head is not affected (VAL-ETEXT-007)
      </div>
    </div>

  <!-- ==================== DIFF VIEW ==================== -->
  {:else if view === 'diff'}
    <div class="diff-view">
      <div class="diff-header">
        <button class="btn btn-small" on:click={goBackToHistory}>
          ← History
        </button>
        <h3>Diff</h3>
        <span class="diff-ids">{shortId(diffResult?.from_revision_id)} → {shortId(diffResult?.to_revision_id)}</span>
        <span class="diff-stats" data-etext-diff-stats>+{diffResult?.added_lines || 0} −{diffResult?.removed_lines || 0}</span>
      </div>

      <div class="diff-sections" data-etext-diff-sections>
        {#each diffResult?.sections || [] as section}
          <div class="diff-section diff-{section.type}">
            {#if section.type === 'unchanged'}
              <pre class="diff-unchanged">{section.to_content || section.from_content}</pre>
            {:else if section.type === 'added'}
              <pre class="diff-added">{section.to_content}</pre>
            {:else if section.type === 'removed'}
              <pre class="diff-removed">{section.from_content}</pre>
            {/if}
          </div>
        {/each}
      </div>
    </div>

  <!-- ==================== BLAME VIEW ==================== -->
  {:else if view === 'blame'}
    <div class="blame-view">
      <div class="blame-header">
        <button class="btn btn-small" on:click={goBackToHistory}>
          ← History
        </button>
        <h3>Blame</h3>
        <span class="blame-rev">{shortId(blameResult?.revision_id)}</span>
      </div>

      <div class="blame-sections" data-etext-blame-sections>
        {#each blameResult?.sections || [] as section}
          <div class="blame-section" data-etext-blame-section>
            <div class="blame-annotation">
              <span class="blame-author" data-etext-blame-author-kind={section.author_kind}>
                {section.author_kind === 'user' ? '👤' : '🤖'}
                {section.author_label}
              </span>
              <span class="blame-rev" title={section.revision_id}>{shortId(section.revision_id)}</span>
            </div>
            <pre class="blame-content">{section.content}</pre>
          </div>
        {/each}
      </div>
    </div>
  {/if}
</div>

<style>
  .etext-editor {
    display: flex;
    flex-direction: column;
    height: 100%;
    font-size: 0.85rem;
    color: #c0c0d0;
  }

  /* ---- Error / loading bars ---- */
  .error-bar {
    background: rgba(239, 68, 68, 0.15);
    border: 1px solid rgba(239, 68, 68, 0.3);
    color: #f87171;
    padding: 0.4rem 0.6rem;
    font-size: 0.8rem;
    border-radius: 4px;
    margin-bottom: 0.5rem;
  }

  .loading-bar {
    color: #888;
    font-size: 0.8rem;
    padding: 0.3rem 0;
  }

  /* ---- Buttons ---- */
  .btn {
    padding: 0.3rem 0.7rem;
    font-size: 0.75rem;
    font-weight: 600;
    color: #c0c0d0;
    background: rgba(255, 255, 255, 0.05);
    border: 1px solid #333;
    border-radius: 4px;
    cursor: pointer;
    transition: background 0.15s;
  }

  .btn:hover:not(:disabled) {
    background: rgba(255, 255, 255, 0.1);
  }

  .btn:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  .btn-primary {
    color: #60a5fa;
    background: rgba(96, 165, 250, 0.1);
    border-color: rgba(96, 165, 250, 0.25);
  }

  .btn-primary:hover:not(:disabled) {
    background: rgba(96, 165, 250, 0.2);
  }

  .btn-small {
    padding: 0.2rem 0.5rem;
    font-size: 0.7rem;
  }

  /* ---- Document list ---- */
  .doc-list {
    flex: 1;
    overflow: auto;
  }

  .doc-list-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 0.75rem;
  }

  .doc-list-header h3 {
    font-size: 1rem;
    font-weight: 600;
    color: #e0e0f0;
  }

  .new-doc-form {
    display: flex;
    gap: 0.4rem;
    margin-bottom: 0.75rem;
  }

  .new-doc-form input {
    flex: 1;
    padding: 0.35rem 0.5rem;
    background: #0a0a1a;
    border: 1px solid #333;
    border-radius: 4px;
    color: #e0e0e0;
    font-size: 0.8rem;
  }

  .doc-items {
    list-style: none;
    padding: 0;
  }

  .doc-item {
    margin-bottom: 0.25rem;
  }

  .doc-item-btn {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    width: 100%;
    padding: 0.5rem 0.6rem;
    background: rgba(255, 255, 255, 0.03);
    border: 1px solid transparent;
    border-radius: 4px;
    cursor: pointer;
    color: #c0c0d0;
    text-align: left;
    transition: background 0.15s, border-color 0.15s;
  }

  .doc-item-btn:hover {
    background: rgba(59, 130, 246, 0.1);
    border-color: rgba(59, 130, 246, 0.2);
  }

  .doc-icon {
    font-size: 1rem;
  }

  .doc-title {
    flex: 1;
    font-size: 0.85rem;
    font-weight: 500;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .doc-date {
    font-size: 0.7rem;
    color: #666;
  }

  .empty-state {
    color: #666;
    font-size: 0.8rem;
    padding: 1rem 0;
    text-align: center;
  }

  /* ---- Editor view ---- */
  .editor-view {
    display: flex;
    flex-direction: column;
    height: 100%;
  }

  .editor-header {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    margin-bottom: 0.5rem;
    flex-shrink: 0;
  }

  .doc-title {
    flex: 1;
    font-size: 0.95rem;
    font-weight: 600;
    color: #e0e0f0;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .editor-actions {
    display: flex;
    align-items: center;
    gap: 0.4rem;
  }

  .save-status {
    font-size: 0.75rem;
    color: #4ade80;
  }

  /* ---- Agent revision bar ---- */
  .agent-revision-bar {
    margin-bottom: 0.5rem;
    flex-shrink: 0;
  }

  .agent-revision-input {
    display: flex;
    gap: 0.4rem;
  }

  .agent-revision-input input {
    flex: 1;
    padding: 0.35rem 0.5rem;
    background: #0a0a1a;
    border: 1px solid #333;
    border-radius: 4px;
    color: #e0e0e0;
    font-size: 0.8rem;
  }

  .agent-revision-input input:focus {
    outline: none;
    border-color: rgba(168, 85, 247, 0.5);
  }

  .btn-agent {
    color: #a78bfa;
    background: rgba(167, 139, 250, 0.1);
    border-color: rgba(167, 139, 250, 0.25);
  }

  .btn-agent:hover:not(:disabled) {
    background: rgba(167, 139, 250, 0.2);
  }

  .agent-progress {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    margin-top: 0.3rem;
    font-size: 0.75rem;
    color: #a78bfa;
  }

  .agent-progress-dot {
    width: 6px;
    height: 6px;
    border-radius: 50%;
    background: #a78bfa;
    animation: pulse 1.5s ease-in-out infinite;
  }

  @keyframes pulse {
    0%, 100% { opacity: 0.3; }
    50% { opacity: 1; }
  }

  .agent-completed {
    margin-top: 0.3rem;
    font-size: 0.75rem;
    color: #4ade80;
  }

  .agent-failed {
    margin-top: 0.3rem;
    font-size: 0.75rem;
    color: #f87171;
  }

  .editor-body {
    display: flex;
    flex: 1;
    gap: 0.5rem;
    min-height: 0;
  }

  .editor-textarea {
    flex: 2;
    padding: 0.5rem;
    background: #0a0a1a;
    border: 1px solid #333;
    border-radius: 4px;
    color: #e0e0e0;
    font-family: 'SF Mono', 'Fira Code', 'Consolas', monospace;
    font-size: 0.85rem;
    line-height: 1.5;
    resize: none;
    min-height: 100px;
  }

  .editor-textarea:focus {
    outline: none;
    border-color: rgba(59, 130, 246, 0.5);
  }

  .editor-sidebar {
    flex: 1;
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
    min-width: 200px;
  }

  .sidebar-section {
    border: 1px solid #222;
    border-radius: 4px;
    background: #0d0d1a;
  }

  .sidebar-section summary {
    padding: 0.3rem 0.5rem;
    font-size: 0.75rem;
    font-weight: 600;
    color: #888;
    cursor: pointer;
  }

  .json-textarea {
    width: 100%;
    min-height: 60px;
    padding: 0.4rem;
    background: #080818;
    border: none;
    border-radius: 0 0 4px 4px;
    color: #aaa;
    font-family: 'SF Mono', 'Fira Code', 'Consolas', monospace;
    font-size: 0.7rem;
    resize: vertical;
  }

  .json-textarea.invalid {
    color: #f87171;
  }

  /* ---- History view ---- */
  .history-view {
    display: flex;
    flex-direction: column;
    height: 100%;
  }

  .history-header {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    margin-bottom: 0.5rem;
  }

  .history-header h3 {
    flex: 1;
    font-size: 0.9rem;
    font-weight: 600;
    color: #e0e0f0;
  }

  .history-entries {
    list-style: none;
    padding: 0;
    flex: 1;
    overflow: auto;
  }

  .history-entry {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 0.4rem 0.5rem;
    border-bottom: 1px solid #1a1a2a;
  }

  .entry-main {
    display: flex;
    align-items: center;
    gap: 0.5rem;
  }

  .entry-rev {
    font-family: 'SF Mono', monospace;
    font-size: 0.7rem;
    color: #666;
  }

  .entry-author {
    font-size: 0.8rem;
    font-weight: 500;
  }

  .entry-time {
    font-size: 0.7rem;
    color: #555;
  }

  .entry-actions {
    display: flex;
    gap: 0.25rem;
  }

  /* ---- Snapshot view ---- */
  .snapshot-view {
    display: flex;
    flex-direction: column;
    height: 100%;
  }

  .snapshot-header {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    margin-bottom: 0.5rem;
  }

  .snapshot-header h3 {
    font-size: 0.9rem;
    font-weight: 600;
    color: #e0e0f0;
  }

  .snapshot-author {
    font-size: 0.8rem;
    color: #aaa;
  }

  .snapshot-time {
    font-size: 0.7rem;
    color: #555;
  }

  .snapshot-content {
    flex: 1;
    overflow: auto;
    background: #0a0a1a;
    border: 1px solid #222;
    border-radius: 4px;
    padding: 0.5rem;
  }

  .snapshot-content pre {
    font-family: 'SF Mono', 'Fira Code', 'Consolas', monospace;
    font-size: 0.8rem;
    line-height: 1.5;
    color: #c0c0d0;
    white-space: pre-wrap;
    word-break: break-word;
  }

  .snapshot-notice {
    margin-top: 0.5rem;
    font-size: 0.7rem;
    color: #4ade80;
    background: rgba(74, 222, 128, 0.05);
    border: 1px solid rgba(74, 222, 128, 0.15);
    padding: 0.3rem 0.5rem;
    border-radius: 4px;
  }

  /* ---- Diff view ---- */
  .diff-view {
    display: flex;
    flex-direction: column;
    height: 100%;
  }

  .diff-header {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    margin-bottom: 0.5rem;
  }

  .diff-header h3 {
    font-size: 0.9rem;
    font-weight: 600;
    color: #e0e0f0;
  }

  .diff-ids {
    font-family: 'SF Mono', monospace;
    font-size: 0.7rem;
    color: #666;
  }

  .diff-stats {
    font-size: 0.75rem;
    color: #aaa;
  }

  .diff-sections {
    flex: 1;
    overflow: auto;
  }

  .diff-section pre {
    font-family: 'SF Mono', 'Fira Code', 'Consolas', monospace;
    font-size: 0.8rem;
    line-height: 1.4;
    margin: 0;
    padding: 0.25rem 0.4rem;
    white-space: pre-wrap;
    word-break: break-word;
  }

  .diff-unchanged {
    color: #888;
  }

  .diff-added {
    color: #4ade80;
    background: rgba(74, 222, 128, 0.08);
  }

  .diff-removed {
    color: #f87171;
    background: rgba(248, 113, 113, 0.08);
    text-decoration: line-through;
  }

  /* ---- Blame view ---- */
  .blame-view {
    display: flex;
    flex-direction: column;
    height: 100%;
  }

  .blame-header {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    margin-bottom: 0.5rem;
  }

  .blame-header h3 {
    font-size: 0.9rem;
    font-weight: 600;
    color: #e0e0f0;
  }

  .blame-rev {
    font-family: 'SF Mono', monospace;
    font-size: 0.7rem;
    color: #666;
  }

  .blame-sections {
    flex: 1;
    overflow: auto;
  }

  .blame-section {
    display: flex;
    border-bottom: 1px solid #1a1a2a;
  }

  .blame-annotation {
    width: 140px;
    flex-shrink: 0;
    padding: 0.25rem 0.4rem;
    font-size: 0.7rem;
    border-right: 1px solid #1a1a2a;
    display: flex;
    flex-direction: column;
    gap: 0.15rem;
  }

  .blame-author {
    font-weight: 500;
  }

  .blame-rev {
    font-family: 'SF Mono', monospace;
    font-size: 0.65rem;
    color: #555;
  }

  .blame-content {
    font-family: 'SF Mono', 'Fira Code', 'Consolas', monospace;
    font-size: 0.8rem;
    line-height: 1.4;
    padding: 0.25rem 0.4rem;
    color: #c0c0d0;
    white-space: pre-wrap;
    word-break: break-word;
    margin: 0;
    flex: 1;
  }
</style>
