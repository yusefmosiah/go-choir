<!--
  BottomBar — fixed bottom bar for the ChoirOS desktop.

  Contains:
    - Left: Show Desktop button + minimized window indicators (click to restore)
    - Center: prompt bar input with "Ask anything..." placeholder
    - Right: connection status dot, user email + logout button

  Data attributes for test targeting:
    data-bottom-bar         — root bar container
    data-show-desktop-btn   — Show Desktop toggle button
    data-minimized-indicator — minimized window indicator
    data-prompt-input       — prompt text input
    data-bottom-user        — user info area
    data-bottom-logout      — logout button
    data-connection-status  — connection status indicator
-->
<script>
  import { createEventDispatcher } from 'svelte';
  import {
    minimizedWindows,
    restoreWindow,
    focusWindow,
    showDesktopMode,
    toggleShowDesktop,
  } from './stores/desktop.js';

  export let currentUser = null;
  export let liveStatus = 'disconnected';

  const dispatch = createEventDispatcher();

  let promptValue = '';

  function handleRestore(windowId) {
    restoreWindow(windowId);
  }

  function handleShowDesktop() {
    toggleShowDesktop();
  }

  function handlePromptKeydown(event) {
    if (event.key === 'Enter' && promptValue.trim()) {
      // Submit prompt (for now just clear — no Chat app yet)
      dispatch('promptsubmit', { text: promptValue.trim() });
      promptValue = '';
    } else if (event.key === 'Escape') {
      event.target.blur();
    }
  }

  function handleLogout() {
    dispatch('logout');
  }

  function getStatusColor() {
    if (liveStatus === 'connected') return '#4ade80';
    if (liveStatus === 'connecting') return '#fbbf24';
    if (liveStatus === 'error') return '#f87171';
    return '#444';
  }

  function getStatusText() {
    if (liveStatus === 'connected') return 'Connected';
    if (liveStatus === 'connecting') return 'Connecting';
    if (liveStatus === 'error') return 'Error';
    return 'Disconnected';
  }
</script>

<div class="bottom-bar" data-bottom-bar>
  <!-- Left section: show desktop + minimized windows -->
  <div class="bar-left">
    <!-- Show Desktop button -->
    <button
      class="show-desktop-btn"
      data-show-desktop-btn
      on:click={handleShowDesktop}
      aria-label="Show Desktop"
      title="Show Desktop"
    >
      <span class="show-desktop-icon">⊞</span>
    </button>

    <!-- Minimized window indicators -->
    <div class="minimized-indicators">
      {#each $minimizedWindows as win (win.windowId)}
        <button
          class="minimized-indicator"
          data-minimized-indicator
          on:click={() => handleRestore(win.windowId)}
          title={win.title}
          aria-label="Restore {win.title}"
        >
          <span class="indicator-icon">{win.icon || '📱'}</span>
          <span class="indicator-name">{win.title}</span>
        </button>
      {/each}
    </div>
  </div>

  <!-- Center section: prompt bar -->
  <div class="bar-center">
    <div class="prompt-bar">
      <input
        type="text"
        class="prompt-input"
        data-prompt-input
        bind:value={promptValue}
        on:keydown={handlePromptKeydown}
        placeholder="Ask anything..."
        aria-label="Prompt input"
      />
    </div>
  </div>

  <!-- Right section: user info, connection, logout -->
  <div class="bar-right">
    <!-- Connection status dot -->
    <div
      class="connection-status"
      data-connection-status
      data-desktop-live-status
      data-shell-live-status
      aria-live="polite"
      aria-label="Connection status: {getStatusText()}"
    >
      <span
        class="status-dot"
        style="background: {getStatusColor()}; {liveStatus === 'connecting' ? 'animation: pulse 1.5s infinite;' : ''}"
      ></span>
      <span class="status-text">{getStatusText()}</span>
    </div>

    <!-- User info -->
    <div class="user-info" data-bottom-user data-desktop-user data-shell-user>
      <span class="user-email">{currentUser?.email || 'unknown'}</span>
    </div>

    <!-- Logout button -->
    <button
      class="logout-btn"
      data-bottom-logout
      data-desktop-logout
      data-shell-logout
      on:click={handleLogout}
      aria-label="Sign out"
    >
      Sign Out
    </button>
  </div>
</div>

<style>
  .bottom-bar {
    position: fixed;
    bottom: 0;
    left: 0;
    right: 0;
    height: 56px;
    background: #11111b;
    border-top: 1px solid #2a2a3a;
    display: flex;
    align-items: center;
    padding: 0 12px;
    z-index: 100;
    gap: 12px;
  }

  .bar-left {
    display: flex;
    align-items: center;
    gap: 4px;
    flex-shrink: 0;
    min-width: 0;
  }

  .show-desktop-btn {
    width: 36px;
    height: 36px;
    display: flex;
    align-items: center;
    justify-content: center;
    background: transparent;
    border: 1px solid #333;
    border-radius: 6px;
    cursor: pointer;
    color: #c0c0d0;
    font-size: 1.1rem;
    flex-shrink: 0;
    transition: background 0.15s, border-color 0.15s;
  }

  .show-desktop-btn:hover {
    background: rgba(255, 255, 255, 0.06);
    border-color: #444;
  }

  .show-desktop-btn:focus-visible {
    outline: 2px solid #3b82f6;
    outline-offset: 2px;
  }

  .minimized-indicators {
    display: flex;
    align-items: center;
    gap: 4px;
    overflow-x: auto;
    flex-shrink: 0;
  }

  .minimized-indicator {
    display: flex;
    align-items: center;
    gap: 4px;
    padding: 4px 8px;
    background: rgba(255, 255, 255, 0.05);
    border: 1px solid #333;
    border-radius: 4px;
    cursor: pointer;
    color: #c0c0d0;
    transition: background 0.15s;
    white-space: nowrap;
    flex-shrink: 0;
  }

  .minimized-indicator:hover {
    background: rgba(59, 130, 246, 0.15);
    border-color: rgba(59, 130, 246, 0.3);
  }

  .minimized-indicator:focus-visible {
    outline: 2px solid #3b82f6;
    outline-offset: 2px;
  }

  .indicator-icon {
    font-size: 0.85rem;
  }

  .indicator-name {
    font-size: 0.7rem;
    max-width: 80px;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .bar-center {
    flex: 1;
    min-width: 0;
    display: flex;
    justify-content: center;
  }

  .prompt-bar {
    width: 100%;
    max-width: 600px;
  }

  .prompt-input {
    width: 100%;
    height: 36px;
    padding: 0 12px;
    background: rgba(255, 255, 255, 0.05);
    border: 1px solid #333;
    border-radius: 18px;
    color: #e0e0e0;
    font-size: 0.85rem;
    outline: none;
    transition: border-color 0.15s;
  }

  .prompt-input::placeholder {
    color: #666;
  }

  .prompt-input:focus {
    border-color: #3b82f6;
    background: rgba(255, 255, 255, 0.08);
  }

  .bar-right {
    display: flex;
    align-items: center;
    gap: 10px;
    flex-shrink: 0;
  }

  .connection-status {
    display: flex;
    align-items: center;
    gap: 6px;
  }

  .status-dot {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    flex-shrink: 0;
  }

  .status-text {
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

  :global(.status-dot-connected) {
    background: #4ade80;
    box-shadow: 0 0 4px rgba(74, 222, 128, 0.5);
  }

  .user-info {
    display: flex;
    align-items: center;
    gap: 4px;
    color: #999;
  }

  .user-email {
    font-size: 0.75rem;
    max-width: 150px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .logout-btn {
    padding: 4px 10px;
    font-size: 0.75rem;
    font-weight: 600;
    color: #f87171;
    background: rgba(248, 113, 113, 0.1);
    border: 1px solid rgba(248, 113, 113, 0.25);
    border-radius: 6px;
    cursor: pointer;
    transition: background 0.2s;
    white-space: nowrap;
  }

  .logout-btn:hover {
    background: rgba(248, 113, 113, 0.2);
  }

  .logout-btn:focus-visible {
    outline: 2px solid #f87171;
    outline-offset: 2px;
  }

  @keyframes pulse {
    0%, 100% { opacity: 1; }
    50% { opacity: 0.4; }
  }

  /* Responsive: Tablet */
  @media (max-width: 1024px) {
    .user-email {
      max-width: 100px;
    }
  }

  /* Responsive: Mobile */
  @media (max-width: 768px) {
    .bottom-bar {
      padding: 0 8px;
      gap: 8px;
    }

    .bar-center {
      flex: 1;
    }

    .prompt-bar {
      max-width: none;
    }

    .prompt-input {
      min-height: 44px;
    }

    .user-email {
      display: none;
    }
  }
</style>
