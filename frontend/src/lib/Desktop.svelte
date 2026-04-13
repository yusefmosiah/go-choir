<!--
  Desktop — ChoirOS desktop shell with left rail, floating windows, and bottom bar.

  Responsive layout across three breakpoints:
    - Desktop (>1024px): full left rail (~180px) with labels, floating draggable windows
    - Tablet (768-1024px): icon-only left rail (~56px), floating windows with max-width
    - Mobile (<768px): left rail hidden, hamburger opens slide-out overlay,
      single focus window mode (one window at a time, full width, non-draggable)

  Data attributes for test targeting:
    data-desktop             — root desktop container
    data-desktop-windows     — window container area
    data-rail-backdrop       — mobile overlay backdrop
    data-shell               — backward compat with existing tests
-->
<script>
  import { createEventDispatcher } from 'svelte';
  import { onMount } from 'svelte';
  import { onDestroy } from 'svelte';
  import { fetchWithRenewal, AuthRequiredError, renewSession } from './auth.js';
  import { fetchDesktopState, saveDesktopState } from './desktop.js';
  import DesktopIcons from './DesktopIcons.svelte';
  import BottomBar from './BottomBar.svelte';
  import FloatingWindow from './FloatingWindow.svelte';
  import ETextEditor from './ETextEditor.svelte';
  import {
    windows,
    activeWindowId,
    liveStatus,
    openApp,
    closeWindow,
    focusWindow,
    minimizeWindow,
    maximizeWindow,
    restoreWindow,
    moveWindow,
    resizeWindow,
    setWindows,
    hideWindow,
    showWindow,
    visibleWindows,
  } from './stores/desktop.js';

  export let currentUser = null;

  const dispatch = createEventDispatcher();

  // ---- Bootstrap data (preserved for session renewal, not displayed) ----
  let bootstrapData = null;
  let bootstrapError = '';
  let refreshing = false;
  let refreshStatus = '';

  // ---- WebSocket state ----
  let ws = null;
  let wsClosedByLogout = false;
  let wsReconnectAttempt = 0;
  let wsReconnecting = false;
  const MAX_WS_RECONNECT_ATTEMPTS = 5;
  const WS_RECONNECT_BASE_DELAY = 1000;

  // ---- Desktop state persistence ----
  let stateLoaded = false;
  let saveTimer = null;
  const SAVE_DEBOUNCE_MS = 500;

  // ---- Responsive state ----
  let isMobile = false;
  let hamburgerOpen = false;

  function updateViewport() {
    isMobile = window.innerWidth < 768;
    // Close hamburger if resizing to desktop/tablet
    if (!isMobile) hamburgerOpen = false;
  }

  // ---- Desktop state persistence ----

  async function loadDesktopState() {
    try {
      const state = await fetchDesktopState();
      if (state && state.windows && state.windows.length > 0) {
        const restoredWindows = state.windows.map((w) => ({
          windowId: w.window_id,
          appId: w.app_id,
          title: w.title,
          icon: getIconForApp(w.app_id),
          x: w.geometry?.x ?? 100,
          y: w.geometry?.y ?? 100,
          width: w.geometry?.width ?? 600,
          height: w.geometry?.height ?? 400,
          mode: w.mode ?? 'normal',
          zIndex: w.z_index ?? 1,
          restoredGeometry: w.restored_geometry
            ? { x: w.restored_geometry.x, y: w.restored_geometry.y, width: w.restored_geometry.width, height: w.restored_geometry.height }
            : null,
          appContext: w.app_context ?? {},
        }));
        setWindows(restoredWindows, state.active_window_id || '');
      }
    } catch (err) {
      if (err instanceof AuthRequiredError) {
        dispatch('authexpired');
        return;
      }
    }
    stateLoaded = true;
  }

  function getIconForApp(appId) {
    const icons = {
      files: '📁',
      browser: '🌐',
      terminal: '💻',
      settings: '⚙️',
      etext: '📝',
    };
    return icons[appId] || '📱';
  }

  function scheduleSave() {
    if (saveTimer) clearTimeout(saveTimer);
    saveTimer = setTimeout(persistDesktopState, SAVE_DEBOUNCE_MS);
  }

  async function persistDesktopState() {
    try {
      let currentWindows;
      let currentActiveId;
      windows.subscribe((w) => { currentWindows = w; })();
      activeWindowId.subscribe((id) => { currentActiveId = id; })();

      const state = {
        windows: currentWindows
          .filter((w) => w.mode !== 'hidden')
          .map((w) => ({
            window_id: w.windowId,
            app_id: w.appId,
            title: w.title,
            geometry: { x: w.x, y: w.y, width: w.width, height: w.height },
            restored_geometry: w.restoredGeometry,
            mode: w.mode,
            z_index: w.zIndex,
            app_context: w.appContext,
          })),
        active_window_id: currentActiveId || '',
      };
      await saveDesktopState(state);
    } catch (err) {
      if (err instanceof AuthRequiredError) {
        dispatch('authexpired');
      }
    }
  }

  // ---- Auto-save on store changes ----

  let unsubscribeWindows;
  let unsubscribeActive;

  // ---- Bootstrap fetch (preserved for session renewal, not displayed) ----

  async function fetchBootstrap() {
    bootstrapError = '';
    try {
      const res = await fetchWithRenewal('/api/shell/bootstrap', { method: 'GET' });
      if (!res.ok) {
        bootstrapError = `Bootstrap failed (${res.status})`;
        return;
      }
      bootstrapData = await res.json();
    } catch (err) {
      if (err instanceof AuthRequiredError) {
        dispatch('authexpired');
        return;
      }
      bootstrapError = 'Bootstrap request failed';
    }
  }

  async function handleRefresh() {
    refreshing = true;
    refreshStatus = '';
    bootstrapError = '';
    try {
      const res = await fetchWithRenewal('/api/shell/bootstrap', { method: 'GET' });
      if (!res.ok) {
        bootstrapError = `Bootstrap failed (${res.status})`;
        refreshStatus = 'Refresh failed';
        return;
      }
      bootstrapData = await res.json();
      refreshStatus = 'Session renewed';
    } catch (err) {
      if (err instanceof AuthRequiredError) {
        dispatch('authexpired');
        return;
      }
      refreshStatus = 'Refresh failed';
    } finally {
      refreshing = false;
    }
  }

  // ---- Live channel (WebSocket) ----

  function connectLiveChannel() {
    liveStatus.set('connecting');
    try {
      const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
      const wsUrl = `${protocol}//${window.location.host}/api/ws`;
      ws = new WebSocket(wsUrl);
      ws.onopen = () => {
        liveStatus.set('connected');
        wsReconnectAttempt = 0;
      };
      ws.onmessage = () => {};
      ws.onerror = () => {
        liveStatus.set('error');
      };
      ws.onclose = () => {
        if (wsClosedByLogout) {
          liveStatus.set('disconnected');
          return;
        }
        liveStatus.update((s) => s === 'error' ? s : 'disconnected');
        attemptWsReconnection();
      };
    } catch (_err) {
      liveStatus.set('error');
    }
  }

  async function attemptWsReconnection() {
    if (wsReconnecting) return;
    if (wsClosedByLogout) return;
    if (wsReconnectAttempt >= MAX_WS_RECONNECT_ATTEMPTS) {
      liveStatus.set('error');
      return;
    }
    wsReconnecting = true;
    wsReconnectAttempt++;
    const delay = WS_RECONNECT_BASE_DELAY * wsReconnectAttempt;
    try {
      await new Promise((resolve) => setTimeout(resolve, delay));
      const { renewed } = await renewSession();
      if (!renewed) {
        dispatch('authexpired');
        return;
      }
      connectLiveChannel();
    } finally {
      wsReconnecting = false;
    }
  }

  // ---- Event handlers ----

  function handleLaunchApp(event) {
    if (isMobile) {
      // Mobile single-focus mode: hide all other visible windows first
      let currentWins;
      windows.subscribe((w) => { currentWins = w; })();
      const existing = currentWins.find(
        (w) => w.appId === event.detail.appId && w.mode !== 'closed' && w.mode !== 'hidden'
      );
      if (existing) {
        if (existing.mode === 'hidden') {
          // Show the hidden window, hide any other visible ones
          currentWins.forEach((w) => {
            if (w.windowId !== existing.windowId && w.mode !== 'closed' && w.mode !== 'hidden') {
              hideWindow(w.windowId);
            }
          });
          showWindow(existing.windowId);
        } else {
          // Already visible, just focus it
          focusWindow(existing.windowId);
        }
      } else {
        // Hide all visible windows, then open the new one
        currentWins.forEach((w) => {
          if (w.mode !== 'closed' && w.mode !== 'hidden') hideWindow(w.windowId);
        });
        openApp(event.detail.appId, event.detail.appName, event.detail.icon);
      }
    } else {
      openApp(event.detail.appId, event.detail.appName, event.detail.icon);
    }
    hamburgerOpen = false;
  }

  function handleWindowClose(event) {
    if (isMobile) {
      // On mobile, closing the last visible window returns to empty desktop
      // Also close any hidden windows to clean up state
      let currentWins;
      windows.subscribe((w) => { currentWins = w; })();
      currentWins.forEach((w) => {
        if (w.mode !== 'closed') closeWindow(w.windowId);
      });
    } else {
      closeWindow(event.detail.windowId);
    }
    scheduleSave();
  }

  function handleWindowFocus(event) {
    focusWindow(event.detail.windowId);
    scheduleSave();
  }

  function handleWindowMinimize(event) {
    if (isMobile) {
      // On mobile, minimizing closes the window (returns to empty desktop)
      closeWindow(event.detail.windowId);
    } else {
      minimizeWindow(event.detail.windowId);
    }
    scheduleSave();
  }

  function handleWindowMaximize(event) {
    maximizeWindow(event.detail.windowId);
    scheduleSave();
  }

  function handleWindowRestore(event) {
    restoreWindow(event.detail.windowId);
    scheduleSave();
  }

  function handleWindowMove(event) {
    if (isMobile) return; // No moving on mobile
    moveWindow(event.detail.windowId, event.detail.x, event.detail.y);
    scheduleSave();
  }

  function handleWindowResize(event) {
    if (isMobile) return; // No resizing on mobile
    resizeWindow(
      event.detail.windowId,
      event.detail.x,
      event.detail.y,
      event.detail.width,
      event.detail.height
    );
    scheduleSave();
  }

  function handleLogout() {
    wsClosedByLogout = true;
    if (ws) {
      ws.close();
      ws = null;
    }
    dispatch('logout');
  }

  function handlePromptSubmit(event) {
    // For now, just log it. Future: opens a Chat app or sends to runtime.
  }

  function handleToggleHamburger() {
    hamburgerOpen = !hamburgerOpen;
  }

  function handleBackdropClick() {
    hamburgerOpen = false;
  }

  // ---- Lifecycle ----

  onMount(() => {
    updateViewport();
    window.addEventListener('resize', updateViewport);

    fetchBootstrap();
    connectLiveChannel();
    loadDesktopState();

    // Subscribe to store changes for auto-save
    unsubscribeWindows = windows.subscribe(() => {
      if (stateLoaded) scheduleSave();
    });
    unsubscribeActive = activeWindowId.subscribe(() => {
      if (stateLoaded) scheduleSave();
    });
  });

  onDestroy(() => {
    window.removeEventListener('resize', updateViewport);
    wsClosedByLogout = true;
    if (ws) {
      ws.close();
      ws = null;
    }
    if (saveTimer) clearTimeout(saveTimer);
    if (unsubscribeWindows) unsubscribeWindows();
    if (unsubscribeActive) unsubscribeActive();
  });
</script>

<div class="desktop" data-desktop data-shell>
  <!-- Backdrop for mobile slide-out overlay -->
  {#if hamburgerOpen && isMobile}
    <!-- svelte-ignore a11y-click-events-have-key-events -->
    <!-- svelte-ignore a11y-no-static-element-interactions -->
    <div class="rail-backdrop" data-rail-backdrop role="presentation" on:click={handleBackdropClick}></div>
  {/if}

  <!-- Left rail -->
  <DesktopIcons {hamburgerOpen} on:launchapp={handleLaunchApp} />

  <!-- Window container area -->
  <!-- Hidden until desktop state loads to prevent flash of empty desktop (VAL-SHELL-022) -->
  <div class="desktop-area {stateLoaded ? 'state-loaded' : 'state-loading'}" data-desktop-windows>
    {#if stateLoaded}
      {#each $windows as win (win.windowId)}
        {#if win.mode !== 'closed' && win.mode !== 'hidden'}
          <FloatingWindow
            windowId={win.windowId}
            appId={win.appId}
            title={win.title}
            x={win.x}
            y={win.y}
            width={win.width}
            height={win.height}
            mode={win.mode}
            zIndex={win.zIndex}
            active={win.windowId === $activeWindowId}
            restoredGeometry={win.restoredGeometry}
            isMobile={isMobile}
            on:close={handleWindowClose}
            on:focus={handleWindowFocus}
            on:minimize={handleWindowMinimize}
            on:maximize={handleWindowMaximize}
            on:restore={handleWindowRestore}
            on:move={handleWindowMove}
            on:resize={handleWindowResize}
          >
            {#if win.appId === 'etext'}
              <div class="app-content etext-content" data-etext-app>
                <ETextEditor {currentUser} on:authexpired={() => dispatch('authexpired')} />
              </div>
            {:else}
              <div class="app-content">
                <div class="app-header">
                  <span class="app-label">{win.title}</span>
                </div>
              </div>
            {/if}
          </FloatingWindow>
        {/if}
      {/each}
    {/if}
  </div>

  <!-- Bottom bar -->
  <BottomBar
    {currentUser}
    liveStatus={$liveStatus}
    on:logout={handleLogout}
    on:promptsubmit={handlePromptSubmit}
    on:togglehamburger={handleToggleHamburger}
  />
</div>

<style>
  .desktop {
    display: flex;
    flex-direction: column;
    min-height: 100vh;
    background: #0f0f0f;
    overflow: hidden;
  }

  /* Backdrop for mobile slide-out overlay */
  .rail-backdrop {
    position: fixed;
    top: 0;
    left: 0;
    right: 0;
    bottom: 0;
    background: rgba(0, 0, 0, 0.5);
    z-index: 190; /* Below the rail overlay (200) so rail items remain clickable */
    cursor: pointer;
  }

  /* Desktop area (window container) — offset for left rail */
  .desktop-area {
    flex: 1;
    position: relative;
    overflow: hidden;
    margin-left: 180px; /* desktop: width of left rail */
    height: calc(100vh - 56px); /* subtract bottom bar height */
  }

  /* Prevent flash of empty desktop while state loads (VAL-SHELL-022) */
  .desktop-area.state-loading {
    visibility: hidden;
  }

  .desktop-area.state-loaded {
    visibility: visible;
  }

  /* App content inside windows */
  .app-content {
    padding: 1rem;
    height: 100%;
    display: flex;
    flex-direction: column;
  }

  .etext-content {
    background: #1a1a2a;
  }

  .app-header {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    flex-wrap: wrap;
  }

  .app-label {
    font-size: 0.95rem;
    font-weight: 600;
    color: #c0c0d0;
  }

  /* Responsive: Tablet (768-1024px) — narrower rail */
  @media (max-width: 1024px) {
    .desktop-area {
      margin-left: 56px;
    }
  }

  /* Responsive: Mobile (<768px) — no rail offset, full width */
  @media (max-width: 768px) {
    .desktop-area {
      margin-left: 0;
    }
  }
</style>
