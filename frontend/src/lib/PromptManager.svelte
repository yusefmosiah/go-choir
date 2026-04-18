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

  function formatJSON(value) {
    if (!value || (typeof value === 'object' && Object.keys(value).length === 0)) {
      return '{}';
    }
    return JSON.stringify(value, null, 2);
  }

  function sortedPrompts(items) {
    return [...items].sort((a, b) => ROLE_ORDER.indexOf(a.role) - ROLE_ORDER.indexOf(b.role));
  }

  function missingDiagnostics(prompt) {
    if (!prompt) return ['effective system prompt', 'role policy', 'provider/model policy', 'tool definitions'];
    const missing = [];
    if (!prompt.effective_system_prompt) missing.push('effective system prompt');
    if (!prompt.role_policy?.profile) missing.push('role policy');
    if (!prompt.provider_policy?.active_provider) missing.push('provider/model policy');
    if (!Array.isArray(prompt.tools)) missing.push('tool definitions');
    return missing;
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
      const incomplete = prompts
        .map((prompt) => ({ role: prompt.role, missing: missingDiagnostics(prompt) }))
        .filter((item) => item.missing.length > 0);
      if (incomplete.length > 0) {
        error = `Sandbox returned incomplete prompt diagnostics for ${incomplete.map((item) => `${item.role} (${item.missing.join(', ')})`).join('; ')}. This comes from /api/prompts on the sandbox runtime, not vmctl.`;
      }
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
        <span class="source">{prompt.source === 'user' ? 'override' : 'seeded'}</span>
      </button>
    {/each}
  </div>

  <div class="editor-pane">
    <div class="editor-header">
      <div>
        <h2>{selectedRole}</h2>
        <div class="editor-subtitle">
          Source: {activePrompt?.source_label || 'Seeded default file'}
        </div>
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
      <div class="explainer">
        This textarea edits the role prompt text only. The read-only sections below show the effective system prompt, tool definitions, and provider/model policy actually in force.
      </div>

      <div class="section">
        <div class="section-title">Editable role prompt</div>
        <textarea
          bind:value={draft}
          class="editor"
          spellcheck="false"
          disabled={saving}
        ></textarea>
      </div>

      <details class="detail-section" open>
        <summary class="section-title">Effective system prompt</summary>
        <div class="section-subtitle">Role prompt + runtime-added policy + tool catalog.</div>
        <textarea
          class="readonly-editor"
          spellcheck="false"
          readonly
          value={activePrompt?.effective_system_prompt || '(runtime did not return an effective system prompt)'}
        ></textarea>
      </details>

      <div class="compact-grid">
        <details class="detail-card" open>
          <summary class="section-title">Role policy</summary>
          {#if activePrompt?.role_policy?.profile}
            <div class="policy-list">
              <div><strong>Profile</strong> {activePrompt.role_policy.profile}</div>
              <div><strong>Delegates to</strong> {activePrompt.role_policy.allowed_delegate_targets?.length ? activePrompt.role_policy.allowed_delegate_targets.join(', ') : 'none'}</div>
              <div><strong>Read-only files</strong> {activePrompt.role_policy.allow_read_only_files ? 'yes' : 'no'}</div>
              <div><strong>Writable files</strong> {activePrompt.role_policy.allow_writable_files ? 'yes' : 'no'}</div>
              <div><strong>Research tools</strong> {activePrompt.role_policy.allow_research_tools ? 'yes' : 'no'}</div>
              <div><strong>Evidence tools</strong> {activePrompt.role_policy.allow_evidence_tools ? 'yes' : 'no'}</div>
              <div><strong>Coding tools</strong> {activePrompt.role_policy.allow_coding_tools ? 'yes' : 'no'}</div>
              <div><strong>Co-agent tools</strong> {activePrompt.role_policy.allow_coagent_tools ? 'yes' : 'no'}</div>
            </div>
          {:else}
            <div class="empty-hint">Runtime did not return role policy.</div>
          {/if}
        </details>

        <details class="detail-card" open>
          <summary class="section-title">Provider and model policy</summary>
          {#if activePrompt?.provider_policy?.active_provider}
            <div class="policy-list">
              <div><strong>Active provider</strong> {activePrompt.provider_policy.active_provider}</div>
              <div><strong>Default model</strong> {activePrompt.provider_policy.default_model || 'provider default'}</div>
              <div><strong>Selection policy</strong> {activePrompt.provider_policy.model_selection || 'default'}</div>
              <div><strong>Per-run model override</strong> {activePrompt.provider_policy.supports_per_run_model_override ? 'supported' : 'not supported'}</div>
            </div>
            {#if activePrompt.provider_policy.notes?.length}
              <div class="policy-notes">
                {#each activePrompt.provider_policy.notes as note}
                  <div class="policy-note">{note}</div>
                {/each}
              </div>
            {/if}
          {:else}
            <div class="empty-hint">Runtime did not return provider/model policy.</div>
          {/if}
        </details>
      </div>

      <details class="detail-section">
        <summary class="section-title">Tool definitions <span class="tool-count">{(activePrompt?.tools || []).length} tools</span></summary>
        <div class="section-subtitle">Read-only. Edit tool behavior in code, not here.</div>
        {#if (activePrompt?.tools || []).length > 0}
          <div class="tool-list">
            {#each activePrompt.tools as tool}
              <div class="tool-card">
                <div class="tool-name">{tool.name}</div>
                <div class="tool-description">{tool.description || 'No description.'}</div>
                <pre class="tool-schema">{formatJSON(tool.parameters)}</pre>
              </div>
            {/each}
          </div>
        {:else}
          <div class="empty-hint">Runtime did not return tool definitions.</div>
        {/if}
      </details>
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
    overflow: auto;
    padding-right: 0.25rem;
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

  .explainer,
  .section-subtitle {
    color: #9aa4b2;
    font-size: 0.82rem;
    line-height: 1.45;
  }

  .section {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
    min-height: 0;
  }

  .section-title {
    color: #f3f4f6;
    font-size: 0.9rem;
    font-weight: 700;
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
    min-height: 260px;
    width: 100%;
    resize: none;
    padding: 1rem;
    border-radius: 14px;
    border: 1px solid rgba(255, 255, 255, 0.1);
    background: #111827;
    color: #f9fafb;
    font: 0.95rem/1.5 ui-monospace, SFMono-Regular, Menlo, monospace;
  }

  .detail-section {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
  }

  .detail-section > summary,
  .detail-card > summary {
    cursor: pointer;
    user-select: none;
  }

  .compact-grid {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 0.85rem;
  }

  .detail-card {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
    padding: 0.9rem;
    border-radius: 14px;
    border: 1px solid rgba(255, 255, 255, 0.08);
    background: rgba(255, 255, 255, 0.03);
    min-height: 0;
  }

  .readonly-editor,
  .tool-schema {
    width: 100%;
    border-radius: 12px;
    border: 1px solid rgba(255, 255, 255, 0.08);
    background: #0f172a;
    color: #e5e7eb;
    font: 0.8rem/1.5 ui-monospace, SFMono-Regular, Menlo, monospace;
  }

  .readonly-editor {
    min-height: 140px;
    max-height: 360px;
    padding: 0.85rem;
    resize: vertical;
  }

  .empty-hint {
    color: #6b7280;
    font-size: 0.82rem;
    font-style: italic;
  }

  .tool-count {
    color: #6b7280;
    font-size: 0.78rem;
    font-weight: 400;
    margin-left: 0.4rem;
  }

  .policy-list {
    display: grid;
    gap: 0.38rem;
    color: #d1d5db;
    font-size: 0.84rem;
    line-height: 1.45;
  }

  .policy-notes {
    display: flex;
    flex-direction: column;
    gap: 0.35rem;
  }

  .policy-note {
    color: #cbd5e1;
    font-size: 0.8rem;
    line-height: 1.45;
  }

  .tool-list {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(260px, 1fr));
    gap: 0.75rem;
  }

  .tool-card {
    display: flex;
    flex-direction: column;
    gap: 0.4rem;
    padding: 0.8rem;
    border-radius: 12px;
    border: 1px solid rgba(255, 255, 255, 0.06);
    background: rgba(15, 23, 42, 0.72);
  }

  .tool-name {
    font-size: 0.86rem;
    font-weight: 700;
    color: #f9fafb;
  }

  .tool-description {
    color: #cbd5e1;
    font-size: 0.8rem;
    line-height: 1.45;
  }

  .tool-schema {
    margin: 0;
    padding: 0.7rem;
    white-space: pre-wrap;
    word-break: break-word;
    overflow: auto;
    min-height: 96px;
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

    .compact-grid {
      grid-template-columns: 1fr;
    }
  }
</style>
