<!--
  DesktopIcons — left rail with app icons for the ChoirOS desktop.

  Displays a vertical stack of 4 app icons (Files, Browser, Terminal, Settings)
  with emoji + label, active indicator, and scrollable overflow.

  Data attributes for test targeting:
    data-desktop-rail    — root rail container
    data-rail-item       — individual rail icon entry
    data-rail-icon       — emoji icon span
    data-rail-label      — text label span
-->
<script>
  import { createEventDispatcher } from 'svelte';
  import { APP_REGISTRY, windows, activeWindowId } from './stores/desktop.js';

  export let hamburgerOpen = false;
  // Reactive reference to suppress unused-export warning
  $: _hamburgerOpen = hamburgerOpen;

  const dispatch = createEventDispatcher();

  /** The 4 main apps shown in the left rail */
  const railApps = APP_REGISTRY.filter((app) =>
    ['files', 'browser', 'terminal', 'settings'].includes(app.id)
  );

  /** Check if an app has an open (non-closed) window */
  function isAppOpen($windows, appId) {
    return $windows.some((w) => w.appId === appId && w.mode !== 'closed');
  }

  /** Check if an app's window is the active one */
  function isAppActive($windows, $activeWindowId, appId) {
    const activeWin = $windows.find((w) => w.windowId === $activeWindowId);
    return activeWin && activeWin.appId === appId;
  }

  /** Handle rail icon click */
  function handleIconClick(app) {
    dispatch('launchapp', { appId: app.id, appName: app.name, icon: app.icon });
  }
</script>

<div class="desktop-rail" data-desktop-rail>
  <div class="rail-items">
    {#each railApps as app (app.id)}
      <button
        class="rail-item"
        class:active={isAppActive($windows, $activeWindowId, app.id)}
        data-rail-item
        data-app-id={app.id}
        on:click={() => handleIconClick(app)}
        title={app.description}
        aria-label={app.name}
      >
        <span class="rail-icon" data-rail-icon>{app.icon}</span>
        <span class="rail-label" data-rail-label>{app.name}</span>
        {#if isAppOpen($windows, app.id)}
          <span class="active-dot"></span>
        {/if}
      </button>
    {/each}
  </div>
</div>

<style>
  .desktop-rail {
    position: fixed;
    left: 0;
    top: 0;
    bottom: 56px; /* height of bottom bar */
    width: 80px;
    background: #11111b;
    border-right: 1px solid #2a2a3a;
    display: flex;
    flex-direction: column;
    z-index: 50;
    overflow-y: auto;
    overflow-x: hidden;
    scrollbar-width: thin;
    scrollbar-color: #333 transparent;
  }

  .desktop-rail::-webkit-scrollbar {
    width: 4px;
  }

  .desktop-rail::-webkit-scrollbar-thumb {
    background: #333;
    border-radius: 2px;
  }

  .rail-items {
    display: flex;
    flex-direction: column;
    align-items: center;
    padding: 12px 0;
    gap: 4px;
    width: 100%;
  }

  .rail-item {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 2px;
    width: 64px;
    height: 64px;
    padding: 6px 4px;
    background: transparent;
    border: 2px solid transparent;
    border-radius: 8px;
    cursor: pointer;
    color: #c0c0d0;
    transition: background 0.15s, border-color 0.15s;
    position: relative;
    flex-shrink: 0;
  }

  .rail-item:hover {
    background: rgba(255, 255, 255, 0.05);
  }

  .rail-item:focus-visible {
    outline: 2px solid #3b82f6;
    outline-offset: 2px;
  }

  .rail-item.active {
    background: rgba(59, 130, 246, 0.1);
    border-color: rgba(59, 130, 246, 0.3);
  }

  .rail-icon {
    font-size: 1.5rem;
    line-height: 1;
  }

  .rail-label {
    font-size: 0.65rem;
    font-weight: 500;
    color: #999;
    text-align: center;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    max-width: 60px;
  }

  .rail-item.active .rail-label {
    color: #e0e0e0;
  }

  .active-dot {
    position: absolute;
    left: 2px;
    top: 50%;
    transform: translateY(-50%);
    width: 3px;
    height: 16px;
    background: #3b82f6;
    border-radius: 2px;
  }

  /* Responsive: Tablet — icon-only mode */
  @media (max-width: 1024px) {
    .desktop-rail {
      width: 56px;
    }

    .rail-item {
      width: 44px;
      height: 52px;
    }

    .rail-label {
      display: none;
    }

    .rail-icon {
      font-size: 1.4rem;
    }
  }

  /* Responsive: Mobile — hidden */
  @media (max-width: 768px) {
    .desktop-rail {
      display: none;
    }

    :global(.desktop-rail.mobile-open) {
      display: flex;
      position: fixed;
      top: 0;
      left: 0;
      bottom: 0;
      right: auto;
      width: 200px;
      z-index: 200;
      background: #11111b;
    }

    :global(.desktop-rail.mobile-open) .rail-label {
      display: inline;
    }

    :global(.desktop-rail.mobile-open) .rail-item {
      width: auto;
      height: auto;
      flex-direction: row;
      gap: 12px;
      padding: 12px 16px;
      width: 100%;
    }
  }
</style>
