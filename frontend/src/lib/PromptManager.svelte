<script>
  import { onMount } from 'svelte';
  import { AuthRequiredError } from './auth.js';
  import { listPrompts, updatePrompt, resetPrompt } from './prompts.js';
  import { createEventDispatcher } from 'svelte';

  const dispatch = createEventDispatcher();

  const ROLE_ORDER = ['conductor', 'vtext', 'researcher', 'super', 'co-super'];

  let loading = true;
  let saving = false;
  let error = '';
  let status = '';
  let prompts = [];
  let selectedRole = 'conductor';
  let draft = '';

  function sortedPrompts(items) {
    return [...items].sort((a, b) => ROLE_ORDER.indexOf(a.role) - ROLE_ORDER.indexOf(b.role));
  }

  function selectedPrompt() {
    return prompts.find((item) => item.role === selectedRole) || null;
  }

  async function loadPrompts() {
    loading = true;
    error = '';
    try {
      const data = await listPrompts();
      prompts = sortedPrompts(data.prompts || []);
      if (!selectedPrompt() && prompts.length > 0) {
        selectedRole = prompts[0].role;
      }
      draft = selectedPrompt()?.content || '';
    } catch (err) {
      if (err instanceof AuthRequiredError) {
        dispatch('authexpired');
        return;
      }
      error = err.message || 'Failed to load prompts';
    } finally {
      loading = false;
    }
  }

  function handleSelect(role) {
    selectedRole = role;
    draft = selectedPrompt()?.content || '';
    status = '';
    error = '';
  }

  async function handleSave() {
    if (!selectedRole || saving) return;
    saving = true;
    error = '';
    status = '';
    try {
      const updated = await updatePrompt(selectedRole, draft);
      prompts = prompts.map((item) => (item.role === updated.role ? updated : item));
      draft = updated.content || '';
      status = `Saved ${updated.role}`;
    } catch (err) {
      if (err instanceof AuthRequiredError) {
        dispatch('authexpired');
        return;
      }
      error = err.message || 'Failed to save prompt';
    } finally {
      saving = false;
    }
  }

  async function handleReset() {
    if (!selectedRole || saving) return;
    saving = true;
    error = '';
    status = '';
    try {
      const reset = await resetPrompt(selectedRole);
      prompts = prompts.map((item) => (item.role === reset.role ? reset : item));
      draft = reset.content || '';
      status = `Reset ${reset.role} to default`;
    } catch (err) {
      if (err instanceof AuthRequiredError) {
        dispatch('authexpired');
        return;
      }
      error = err.message || 'Failed to reset prompt';
    } finally {
      saving = false;
    }
  }

  $: activePrompt = selectedPrompt();

  onMount(loadPrompts);
</script>

<div class="prompt-manager" data-prompt-manager>
  <div class="sidebar">
    <div class="sidebar-title">Prompts</div>
    {#each prompts as prompt}
      <button
        class:selected={prompt.role === selectedRole}
        class="role-button"
        on:click={() => handleSelect(prompt.role)}
      >
        <span>{prompt.role}</span>
        <span class="source">{prompt.source}</span>
      </button>
    {/each}
  </div>

  <div class="editor-pane">
    <div class="editor-header">
      <div>
        <h2>{selectedRole}</h2>
        <div class="editor-subtitle">Effective source: {activePrompt?.source || 'default'}</div>
      </div>
      <div class="actions">
        <button class="secondary" on:click={handleReset} disabled={saving || loading}>Reset</button>
        <button class="primary" on:click={handleSave} disabled={saving || loading || !draft.trim()}>
          {saving ? 'Saving…' : 'Save'}
        </button>
      </div>
    </div>

    {#if loading}
      <div class="state">Loading prompts…</div>
    {:else}
      <textarea
        bind:value={draft}
        class="editor"
        spellcheck="false"
        disabled={saving}
      ></textarea>
    {/if}

    {#if error}
      <div class="error">{error}</div>
    {/if}
    {#if status}
      <div class="status">{status}</div>
    {/if}
  </div>
</div>

<style>
  .prompt-manager {
    display: grid;
    grid-template-columns: 220px minmax(0, 1fr);
    gap: 1rem;
    height: 100%;
    min-height: 0;
  }

  .sidebar {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
    padding: 0.75rem;
    background: rgba(255, 255, 255, 0.03);
    border: 1px solid rgba(255, 255, 255, 0.08);
    border-radius: 12px;
  }

  .sidebar-title {
    font-size: 0.78rem;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    color: #9aa4b2;
    margin-bottom: 0.25rem;
  }

  .role-button {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 0.75rem;
    width: 100%;
    padding: 0.7rem 0.8rem;
    border-radius: 10px;
    border: 1px solid rgba(255, 255, 255, 0.08);
    background: rgba(255, 255, 255, 0.02);
    color: #f3f4f6;
    cursor: pointer;
  }

  .role-button.selected {
    background: rgba(59, 130, 246, 0.16);
    border-color: rgba(59, 130, 246, 0.45);
  }

  .source {
    font-size: 0.72rem;
    color: #9aa4b2;
    text-transform: uppercase;
    letter-spacing: 0.06em;
  }

  .editor-pane {
    display: flex;
    flex-direction: column;
    min-height: 0;
    gap: 0.75rem;
  }

  .editor-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 1rem;
  }

  .editor-header h2 {
    margin: 0;
    font-size: 1.05rem;
    color: #f9fafb;
  }

  .editor-subtitle {
    color: #9aa4b2;
    font-size: 0.82rem;
  }

  .actions {
    display: flex;
    gap: 0.5rem;
  }

  .primary,
  .secondary {
    border-radius: 10px;
    border: 1px solid rgba(255, 255, 255, 0.12);
    padding: 0.55rem 0.9rem;
    cursor: pointer;
  }

  .primary {
    background: #2563eb;
    color: white;
    border-color: rgba(37, 99, 235, 0.7);
  }

  .secondary {
    background: rgba(255, 255, 255, 0.04);
    color: #e5e7eb;
  }

  .editor {
    flex: 1;
    min-height: 320px;
    width: 100%;
    resize: none;
    padding: 1rem;
    border-radius: 14px;
    border: 1px solid rgba(255, 255, 255, 0.1);
    background: #111827;
    color: #f9fafb;
    font: 0.95rem/1.5 ui-monospace, SFMono-Regular, Menlo, monospace;
  }

  .state,
  .status,
  .error {
    font-size: 0.84rem;
  }

  .status {
    color: #86efac;
  }

  .error {
    color: #fca5a5;
  }

  @media (max-width: 900px) {
    .prompt-manager {
      grid-template-columns: 1fr;
    }
  }
</style>
