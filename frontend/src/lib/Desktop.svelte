<!--
  Desktop — ChoirOS desktop shell with floating desktop icons, floating windows, and bottom bar.

  Layout:
    - Floating desktop icons freely draggable on the desktop surface
    - Floating windows draggable/resizable on top of icons
    - Bottom bar fixed at viewport bottom

  Responsive layout across three breakpoints:
    - Desktop (>1024px): full floating icons with labels, floating draggable windows
    - Tablet (768-1024px): floating icons, windows with max-width constraint
    - Mobile (<768px): floating icons, single focus window mode (one at a time, full width)

  Data attributes for test targeting:
    data-desktop             — root desktop container
    data-desktop-windows     — window container area
    data-desktop-surface     — desktop surface with floating icons
    data-shell               — backward compat with existing tests
-->
<script>
  import { createEventDispatcher } from 'svelte';
  import { onMount } from 'svelte';
  import { onDestroy } from 'svelte';
  import { fetchWithRenewal, AuthRequiredError, renewSession } from './auth.js';
  import { fetchDesktopState, saveDesktopState } from './desktop.js';
  import FloatingDesktopIcons from './FloatingDesktopIcons.svelte';
  import BottomBar from './BottomBar.svelte';
  import FloatingWindow from './FloatingWindow.svelte';
  import ETextEditor from './ETextEditor.svelte';
  import FileBrowser from './FileBrowser.svelte';
  import BrowserApp from './BrowserApp.svelte';
  import {
    windows,
    activeWindowId,
    liveStatus,
    iconPositions,
    showDesktopMode,
    selectedIconId,
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
    setIconPositions,
    getDefaultIconPositions,
    toggleShowDesktop,
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

  function updateViewport() {
    isMobile = window.innerWidth < 768;
  }

  // ---- Desktop state persistence ----

  async function loadDesktopState() {
    try {
      const state = await fetchDesktopState();
      if (state) {
        // Restore icon positions
        if (state.icon_positions && Object.keys(state.icon_positions).length > 0) {
          setIconPositions(state.icon_positions);
        }
        // Restore windows
        if (state.windows && state.windows.length > 0) {
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
      let currentIconPositions;
      windows.subscribe((w) => { currentWindows = w; })();
      activeWindowId.subscribe((id) => { currentActiveId = id; })();
      iconPositions.subscribe((p) => { currentIconPositions = p; })();

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
        icon_positions: currentIconPositions,
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
  let unsubscribeIconPositions;

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

  function handleIconPositionsChanged() {
    scheduleSave();
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
    unsubscribeIconPositions = iconPositions.subscribe(() => {
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
    if (unsubscribeIconPositions) unsubscribeIconPositions();
  });
</script>

<div class="desktop" data-desktop data-shell>
  <!-- Desktop surface (floating icons + windows, full viewport width) -->
  <div class="desktop-area {stateLoaded ? 'state-loaded' : 'state-loading'}" data-desktop-windows>
    <!-- Floating desktop icons (z-index below windows) -->
    <FloatingDesktopIcons on:launchapp={handleLaunchApp} on:iconpositionschanged={handleIconPositionsChanged} />

    <!-- Floating windows (rendered on top of icons) -->
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
            {#if win.appId === 'files'}
              <div class="app-content files-content" data-files-app>
                <FileBrowser on:authexpired={() => dispatch('authexpired')} />
              </div>
            {:else if win.appId === 'browser'}
              <div class="app-content browser-content" data-browser-app-container>
                <BrowserApp />
              </div>
            {:else if win.appId === 'etext'}
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

  /* Desktop area (window container) — full viewport width, no left rail */
  .desktop-area {
    flex: 1;
    position: relative;
    overflow: hidden;
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
</style>
