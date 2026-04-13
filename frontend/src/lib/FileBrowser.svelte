<!--
  FileBrowser — file/directory browser app for the ChoirOS desktop.

  Features:
    - File/directory listing with folder/file icons
    - Breadcrumb navigation with clickable segments
    - Click directory to navigate into it
    - Click file to trigger download
    - New Folder button with inline input (no alert/prompt)
    - Delete with inline confirmation (no confirm())
    - Empty state message
    - Error display for permission/other issues
    - Back/forward navigation history
    - Responsive: works in mobile focus mode with >=44px touch targets

  Data attributes for test targeting:
    data-file-list        — file listing container
    data-file-item        — individual file/directory row
    data-file-icon        — folder/file icon span
    data-file-name        — file/directory name span
    data-file-size        — file size span
    data-breadcrumb       — breadcrumb navigation container
    data-breadcrumb-segment — clickable breadcrumb path segment
    data-new-folder-btn   — new folder button
    data-new-folder-input — inline folder name input
    data-new-folder-confirm — confirm new folder button
    data-delete-btn       — delete button on a file item
    data-delete-confirm   — confirm delete button
    data-delete-cancel    — cancel delete button
    data-empty-state      — empty directory message
    data-error-message    — error message display
    data-nav-back         — back navigation button
    data-nav-forward      — forward navigation button
-->
<script>
  import { onMount } from 'svelte';
  import { fetchWithRenewal, AuthRequiredError } from './auth.js';
  import { createEventDispatcher } from 'svelte';

  const dispatch = createEventDispatcher();

  // Auto-focus action for inputs
  function autofocus(node) {
    node.focus();
  }

  // ---- State ----
  let entries = [];
  let currentPath = []; // array of path segments, e.g. ['documents', 'project']
  let loading = false;
  let error = '';

  // Navigation history for back/forward
  let history = [[]]; // start with root path
  let historyIndex = 0;

  // New folder inline input
  let showNewFolderInput = false;
  let newFolderName = '';
  let newFolderError = '';

  // Delete confirmation
  let deleteTarget = null; // { name, type }
  let deleteError = '';

  // ---- API calls ----

  async function fetchDirectory(pathSegments) {
    loading = true;
    error = '';
    entries = [];
    try {
      const path = pathSegments.length > 0
        ? '/api/files/' + pathSegments.map(encodeURIComponent).join('/')
        : '/api/files';
      const res = await fetchWithRenewal(path);
      if (!res.ok) {
        if (res.status === 401) {
          // Session expired and renewal failed — trigger auth fallback.
          dispatch('authexpired');
          return;
        }
        const body = await res.json().catch(() => ({}));
        if (res.status === 403) {
          error = 'Access denied: you do not have permission to view this directory.';
        } else if (res.status === 404) {
          error = 'Directory not found.';
        } else {
          error = body.error || `Failed to load directory (${res.status})`;
        }
        return;
      }
      const data = await res.json();
      // Sort: directories first, then files, both alphabetically
      entries = (data || []).sort((a, b) => {
        if (a.type === 'directory' && b.type !== 'directory') return -1;
        if (a.type !== 'directory' && b.type === 'directory') return 1;
        return a.name.localeCompare(b.name);
      });
    } catch (err) {
      if (err instanceof AuthRequiredError) {
        dispatch('authexpired');
        return;
      }
      error = 'Failed to load directory. Please try again.';
    } finally {
      loading = false;
    }
  }

  async function createFolder() {
    newFolderError = '';
    const name = newFolderName.trim();
    if (!name) {
      newFolderError = 'Folder name cannot be empty.';
      return;
    }
    // Check for invalid characters
    if (name.includes('/') || name.includes('\\')) {
      newFolderError = 'Folder name cannot contain / or \\';
      return;
    }

    const path = currentPath.length > 0
      ? '/api/files/' + [...currentPath, name].map(encodeURIComponent).join('/')
      : '/api/files/' + encodeURIComponent(name);

    try {
      const res = await fetchWithRenewal(path, { method: 'POST' });
      if (!res.ok) {
        if (res.status === 401) {
          dispatch('authexpired');
          return;
        }
        const body = await res.json().catch(() => ({}));
        if (res.status === 409) {
          newFolderError = 'A folder with this name already exists.';
        } else {
          newFolderError = body.error || 'Failed to create folder.';
        }
        return;
      }
      // Success — refresh listing
      showNewFolderInput = false;
      newFolderName = '';
      await fetchDirectory(currentPath);
    } catch (err) {
      if (err instanceof AuthRequiredError) {
        dispatch('authexpired');
        return;
      }
      newFolderError = 'Failed to create folder.';
    }
  }

  async function deleteItem() {
    if (!deleteTarget) return;
    deleteError = '';

    const path = currentPath.length > 0
      ? '/api/files/' + [...currentPath, deleteTarget.name].map(encodeURIComponent).join('/')
      : '/api/files/' + encodeURIComponent(deleteTarget.name);

    try {
      const res = await fetchWithRenewal(path, { method: 'DELETE' });
      if (!res.ok) {
        if (res.status === 401) {
          dispatch('authexpired');
          return;
        }
        const body = await res.json().catch(() => ({}));
        deleteError = body.error || 'Failed to delete.';
        return;
      }
      // Success — refresh listing
      deleteTarget = null;
      await fetchDirectory(currentPath);
    } catch (err) {
      if (err instanceof AuthRequiredError) {
        dispatch('authexpired');
        return;
      }
      deleteError = 'Failed to delete.';
    }
  }

  // ---- Navigation ----

  function navigateTo(pathSegments) {
    if (pathSegments === currentPath) return;
    currentPath = pathSegments;
    showNewFolderInput = false;
    newFolderName = '';
    newFolderError = '';
    deleteTarget = null;
    deleteError = '';

    // Update history
    // Trim forward history when navigating
    history = history.slice(0, historyIndex + 1);
    history.push([...pathSegments]);
    historyIndex = history.length - 1;

    fetchDirectory(pathSegments);
  }

  function navigateIntoDirectory(dirName) {
    navigateTo([...currentPath, dirName]);
  }

  function navigateToBreadcrumb(index) {
    navigateTo(currentPath.slice(0, index));
  }

  function goBack() {
    if (historyIndex > 0) {
      historyIndex--;
      currentPath = [...history[historyIndex]];
      showNewFolderInput = false;
      newFolderName = '';
      newFolderError = '';
      deleteTarget = null;
      deleteError = '';
      fetchDirectory(currentPath);
    }
  }

  function goForward() {
    if (historyIndex < history.length - 1) {
      historyIndex++;
      currentPath = [...history[historyIndex]];
      showNewFolderInput = false;
      newFolderName = '';
      newFolderError = '';
      deleteTarget = null;
      deleteError = '';
      fetchDirectory(currentPath);
    }
  }

  function handleFileClick(entry) {
    if (entry.type === 'directory') {
      navigateIntoDirectory(entry.name);
    } else {
      // Trigger download
      const path = currentPath.length > 0
        ? '/api/files/' + [...currentPath, entry.name].map(encodeURIComponent).join('/')
        : '/api/files/' + encodeURIComponent(entry.name);

      // Create a temporary link to trigger download
      const a = document.createElement('a');
      a.href = path;
      a.download = entry.name;
      a.style.display = 'none';
      document.body.appendChild(a);
      a.click();
      document.body.removeChild(a);
    }
  }

  function handleNewFolderClick() {
    showNewFolderInput = true;
    newFolderName = '';
    newFolderError = '';
  }

  function cancelNewFolder() {
    showNewFolderInput = false;
    newFolderName = '';
    newFolderError = '';
  }

  function handleNewFolderKeydown(event) {
    if (event.key === 'Enter') {
      createFolder();
    } else if (event.key === 'Escape') {
      cancelNewFolder();
    }
  }

  function startDelete(entry) {
    deleteTarget = { name: entry.name, type: entry.type };
    deleteError = '';
  }

  function cancelDelete() {
    deleteTarget = null;
    deleteError = '';
  }

  function formatFileSize(bytes) {
    if (bytes === 0) return '0 B';
    const units = ['B', 'KB', 'MB', 'GB'];
    const i = Math.floor(Math.log(bytes) / Math.log(1024));
    const val = (bytes / Math.pow(1024, i)).toFixed(i === 0 ? 0 : 1);
    return `${val} ${units[i]}`;
  }

  // Can go back/forward?
  $: canGoBack = historyIndex > 0;
  $: canGoForward = historyIndex < history.length - 1;

  // ---- Lifecycle ----

  onMount(() => {
    fetchDirectory([]);
  });
</script>

<div class="file-browser" data-file-list>
  <!-- Toolbar: breadcrumb + actions -->
  <div class="toolbar">
    <div class="nav-buttons">
      <button
        class="nav-btn"
        data-nav-back
        on:click={goBack}
        disabled={!canGoBack}
        title="Back"
        aria-label="Go back"
      >
        ←
      </button>
      <button
        class="nav-btn"
        data-nav-forward
        on:click={goForward}
        disabled={!canGoForward}
        title="Forward"
        aria-label="Go forward"
      >
        →
      </button>
    </div>

    <!-- Breadcrumb -->
    <div class="breadcrumb" data-breadcrumb>
      <button
        class="breadcrumb-segment"
        data-breadcrumb-segment
        on:click={() => navigateToBreadcrumb(0)}
        aria-label="Root directory"
      >
        Root
      </button>
      {#each currentPath as segment, i}
        <span class="breadcrumb-sep">/</span>
        <button
          class="breadcrumb-segment"
          data-breadcrumb-segment
          on:click={() => navigateToBreadcrumb(i + 1)}
          aria-label="Navigate to {segment}"
        >
          {segment}
        </button>
      {/each}
    </div>

    <button
      class="action-btn new-folder-btn"
      data-new-folder-btn
      on:click={handleNewFolderClick}
      title="New Folder"
      aria-label="Create new folder"
    >
      + Folder
    </button>
  </div>

  <!-- New folder inline input -->
  {#if showNewFolderInput}
    <div class="inline-input-row">
      <span class="inline-icon">📁</span>
      <input
        type="text"
        class="folder-name-input"
        data-new-folder-input
        use:autofocus
        bind:value={newFolderName}
        on:keydown={handleNewFolderKeydown}
        placeholder="Folder name"
        aria-label="New folder name"
      />
      <button
        class="inline-confirm-btn"
        data-new-folder-confirm
        on:click={createFolder}
        title="Create folder"
        aria-label="Confirm create folder"
      >
        ✓
      </button>
      <button
        class="inline-cancel-btn"
        data-new-folder-cancel
        on:click={cancelNewFolder}
        title="Cancel"
        aria-label="Cancel create folder"
      >
        ✕
      </button>
      {#if newFolderError}
        <span class="inline-error">{newFolderError}</span>
      {/if}
    </div>
  {/if}

  <!-- Error display -->
  {#if error}
    <div class="error-message" data-error-message role="alert">
      <span class="error-icon">⚠️</span>
      {error}
    </div>
  {/if}

  <!-- Delete error display -->
  {#if deleteError}
    <div class="error-message" data-error-message role="alert">
      <span class="error-icon">⚠️</span>
      {deleteError}
    </div>
  {/if}

  <!-- Loading state -->
  {#if loading}
    <div class="loading-state">
      <span class="loading-spinner"></span>
      Loading...
    </div>
  {:else if !error}
    <!-- Empty state -->
    {#if entries.length === 0}
      <div class="empty-state" data-empty-state>
        <span class="empty-icon">📂</span>
        This folder is empty
      </div>
    {:else}
      <!-- File listing -->
      <div class="file-listing">
        {#each entries as entry (entry.name)}
          {#if deleteTarget && deleteTarget.name === entry.name}
            <!-- Delete confirmation row -->
            <div class="file-item delete-confirm-row" data-file-item data-entry-type={entry.type}>
              <span class="file-icon" data-file-icon>{entry.type === 'directory' ? '📁' : '📄'}</span>
              <span class="file-name" data-file-name>{entry.name}</span>
              <span class="delete-prompt">Delete?</span>
              <button
                class="delete-confirm-btn"
                data-delete-confirm
                on:click={deleteItem}
                aria-label="Confirm delete {entry.name}"
              >
                Yes
              </button>
              <button
                class="delete-cancel-btn"
                data-delete-cancel
                on:click={cancelDelete}
                aria-label="Cancel delete"
              >
                No
              </button>
            </div>
          {:else}
            <!-- Normal file/directory row -->
            <!-- svelte-ignore a11y-click-events-have-key-events -->
            <!-- svelte-ignore a11y-no-static-element-interactions -->
            <div
              class="file-item"
              data-file-item
              data-entry-type={entry.type}
              on:click={() => handleFileClick(entry)}
            >
              <span class="file-icon" data-file-icon>{entry.type === 'directory' ? '📁' : '📄'}</span>
              <span class="file-name" data-file-name>{entry.name}</span>
              {#if entry.type === 'file'}
                <span class="file-size" data-file-size>{formatFileSize(entry.size)}</span>
              {/if}
              <button
                class="delete-btn"
                data-delete-btn
                on:click|stopPropagation={() => startDelete(entry)}
                title="Delete {entry.name}"
                aria-label="Delete {entry.name}"
              >
                🗑
              </button>
            </div>
          {/if}
        {/each}
      </div>
    {/if}
  {/if}
</div>

<style>
  .file-browser {
    display: flex;
    flex-direction: column;
    height: 100%;
    overflow: hidden;
    background: #1a1a2a;
    color: #c0c0d0;
    font-size: 0.85rem;
  }

  /* ---- Toolbar ---- */
  .toolbar {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 8px 12px;
    background: #181825;
    border-bottom: 1px solid #2a2a3a;
    flex-shrink: 0;
    flex-wrap: wrap;
  }

  .nav-buttons {
    display: flex;
    gap: 2px;
    flex-shrink: 0;
  }

  .nav-btn {
    width: 32px;
    height: 32px;
    display: flex;
    align-items: center;
    justify-content: center;
    background: transparent;
    border: 1px solid #333;
    border-radius: 4px;
    color: #c0c0d0;
    cursor: pointer;
    font-size: 1rem;
    transition: background 0.15s;
  }

  .nav-btn:hover:not(:disabled) {
    background: rgba(255, 255, 255, 0.08);
  }

  .nav-btn:disabled {
    opacity: 0.3;
    cursor: not-allowed;
  }

  /* ---- Breadcrumb ---- */
  .breadcrumb {
    display: flex;
    align-items: center;
    gap: 2px;
    flex: 1;
    min-width: 0;
    overflow-x: auto;
    scrollbar-width: none;
  }

  .breadcrumb::-webkit-scrollbar {
    display: none;
  }

  .breadcrumb-segment {
    background: transparent;
    border: none;
    color: #8888aa;
    cursor: pointer;
    font-size: 0.8rem;
    padding: 2px 6px;
    border-radius: 3px;
    white-space: nowrap;
    transition: color 0.15s, background 0.15s;
  }

  .breadcrumb-segment:hover {
    color: #e0e0f0;
    background: rgba(255, 255, 255, 0.06);
  }

  .breadcrumb-sep {
    color: #555;
    font-size: 0.75rem;
    flex-shrink: 0;
  }

  /* ---- Action buttons ---- */
  .action-btn {
    padding: 6px 12px;
    background: rgba(59, 130, 246, 0.15);
    border: 1px solid rgba(59, 130, 246, 0.3);
    border-radius: 4px;
    color: #7eb8ff;
    cursor: pointer;
    font-size: 0.8rem;
    white-space: nowrap;
    transition: background 0.15s;
  }

  .action-btn:hover {
    background: rgba(59, 130, 246, 0.25);
  }

  /* ---- Inline input (new folder) ---- */
  .inline-input-row {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 8px 12px;
    background: rgba(59, 130, 246, 0.05);
    border-bottom: 1px solid rgba(59, 130, 246, 0.15);
    flex-shrink: 0;
  }

  .inline-icon {
    font-size: 1.1rem;
  }

  .folder-name-input {
    flex: 1;
    padding: 6px 10px;
    background: #11111b;
    border: 1px solid #333;
    border-radius: 4px;
    color: #e0e0e0;
    font-size: 0.85rem;
    min-width: 0;
  }

  .folder-name-input:focus {
    outline: none;
    border-color: #3b82f6;
  }

  .inline-confirm-btn,
  .inline-cancel-btn {
    width: 28px;
    height: 28px;
    display: flex;
    align-items: center;
    justify-content: center;
    border: none;
    border-radius: 4px;
    cursor: pointer;
    font-size: 0.85rem;
    transition: background 0.15s;
  }

  .inline-confirm-btn {
    background: rgba(34, 197, 94, 0.2);
    color: #4ade80;
  }

  .inline-confirm-btn:hover {
    background: rgba(34, 197, 94, 0.35);
  }

  .inline-cancel-btn {
    background: rgba(239, 68, 68, 0.15);
    color: #f87171;
  }

  .inline-cancel-btn:hover {
    background: rgba(239, 68, 68, 0.3);
  }

  .inline-error {
    color: #f87171;
    font-size: 0.8rem;
    white-space: nowrap;
  }

  /* ---- Error message ---- */
  .error-message {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 12px 16px;
    background: rgba(239, 68, 68, 0.1);
    border-bottom: 1px solid rgba(239, 68, 68, 0.2);
    color: #fca5a5;
    font-size: 0.85rem;
    flex-shrink: 0;
  }

  .error-icon {
    font-size: 1rem;
  }

  /* ---- Loading state ---- */
  .loading-state {
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 8px;
    padding: 32px;
    color: #888;
    font-size: 0.9rem;
  }

  .loading-spinner {
    display: inline-block;
    width: 16px;
    height: 16px;
    border: 2px solid #333;
    border-top-color: #3b82f6;
    border-radius: 50%;
    animation: spin 0.6s linear infinite;
  }

  @keyframes spin {
    to { transform: rotate(360deg); }
  }

  /* ---- Empty state ---- */
  .empty-state {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 8px;
    padding: 40px 16px;
    color: #888;
    font-size: 0.9rem;
  }

  .empty-icon {
    font-size: 2rem;
    opacity: 0.5;
  }

  /* ---- File listing ---- */
  .file-listing {
    flex: 1;
    overflow-y: auto;
    padding: 4px 0;
    scrollbar-width: thin;
    scrollbar-color: #333 transparent;
  }

  .file-listing::-webkit-scrollbar {
    width: 6px;
  }

  .file-listing::-webkit-scrollbar-thumb {
    background: #333;
    border-radius: 3px;
  }

  .file-item {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 8px 16px;
    min-height: 44px; /* Touch target size for mobile (VAL-FILES-018) */
    cursor: pointer;
    transition: background 0.1s;
    position: relative;
  }

  .file-item:hover {
    background: rgba(255, 255, 255, 0.04);
  }

  .file-icon {
    font-size: 1.2rem;
    flex-shrink: 0;
    width: 24px;
    text-align: center;
  }

  .file-name {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    color: #c0c0d0;
    font-size: 0.85rem;
  }

  .file-size {
    color: #666;
    font-size: 0.75rem;
    flex-shrink: 0;
    margin-right: 4px;
  }

  /* Delete button - hidden until hover, always visible on mobile */
  .delete-btn {
    width: 28px;
    height: 28px;
    display: flex;
    align-items: center;
    justify-content: center;
    background: transparent;
    border: none;
    border-radius: 4px;
    cursor: pointer;
    font-size: 0.9rem;
    opacity: 0;
    transition: opacity 0.15s, background 0.15s;
    flex-shrink: 0;
  }

  .file-item:hover .delete-btn {
    opacity: 0.6;
  }

  .delete-btn:hover {
    opacity: 1 !important;
    background: rgba(239, 68, 68, 0.2);
  }

  /* ---- Delete confirmation row ---- */
  .delete-confirm-row {
    background: rgba(239, 68, 68, 0.06);
    cursor: default;
  }

  .delete-prompt {
    color: #fca5a5;
    font-size: 0.8rem;
    white-space: nowrap;
  }

  .delete-confirm-btn,
  .delete-cancel-btn {
    padding: 4px 12px;
    border: none;
    border-radius: 4px;
    cursor: pointer;
    font-size: 0.8rem;
    transition: background 0.15s;
  }

  .delete-confirm-btn {
    background: rgba(239, 68, 68, 0.25);
    color: #f87171;
  }

  .delete-confirm-btn:hover {
    background: rgba(239, 68, 68, 0.4);
  }

  .delete-cancel-btn {
    background: rgba(255, 255, 255, 0.08);
    color: #999;
  }

  .delete-cancel-btn:hover {
    background: rgba(255, 255, 255, 0.15);
  }

  /* ---- Mobile responsive ---- */
  @media (max-width: 768px) {
    .toolbar {
      padding: 6px 8px;
      gap: 6px;
    }

    .breadcrumb-segment {
      font-size: 0.75rem;
      padding: 2px 4px;
    }

    .action-btn {
      padding: 6px 8px;
      font-size: 0.75rem;
    }

    .file-item {
      padding: 10px 12px;
    }

    /* Always show delete button on mobile (no hover) */
    .delete-btn {
      opacity: 0.5;
    }

    .file-name {
      font-size: 0.9rem;
    }
  }
</style>
