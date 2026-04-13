<!--
  Desktop — ChoirOS desktop shell with left rail, floating windows, and bottom bar.

  Replaces the previous top-bar + launcher paradigm. Layout:
    - Left rail (DesktopIcons): vertical stack of app icons
    - Center: floating window container
    - Bottom bar (BottomBar): prompt input, minimized indicators, user info

  Preserves from previous Desktop:
    - Bootstrap data fetch (for session renewal compatibility)
    - Live channel WebSocket connection
    - Logout control
    - authexpired fallback
    - Desktop state persistence (GET/PUT /api/desktop/state)

  Removes:
    - Top bar (data-desktop-bar)
    - Launcher dropdown
    - Bootstrap accordion display
    - Runtime panel / TaskRunner display

  Data attributes for test targeting:
    data-desktop             — root desktop container
    data-desktop-windows     — window container area
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
  import Window from './Window.svelte';
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

  // ---- Mobile hamburger state ----
  let hamburgerOpen = false;

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
          restoredGeometry: w.restored_geometry ?? null,
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
        windows: currentWindows.map((w) => ({
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
    openApp(event.detail.appId, event.detail.appName, event.detail.icon);
    hamburgerOpen = false;
  }

  function handleWindowClose(event) {
    closeWindow(event.detail.windowId);
    scheduleSave();
  }

  function handleWindowFocus(event) {
    focusWindow(event.detail.windowId);
    scheduleSave();
  }

  function handleWindowMinimize(event) {
    minimizeWindow(event.detail.windowId);
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
    moveWindow(event.detail.windowId, event.detail.x, event.detail.y);
    scheduleSave();
  }

  function handleWindowResize(event) {
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

  // ---- Lifecycle ----

  onMount(() => {
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
  <!-- Left rail -->
  <DesktopIcons on:launchapp={handleLaunchApp} />

  <!-- Window container area (shifted right by rail width) -->
  <div class="desktop-area" data-desktop-windows>
    {#each $windows as win (win.windowId)}
      {#if win.mode !== 'closed'}
        <Window
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
        </Window>
      {/if}
    {/each}
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

  /* Desktop area (window container) — offset for left rail */
  .desktop-area {
    flex: 1;
    position: relative;
    overflow: hidden;
    margin-left: 80px; /* width of left rail */
    height: calc(100vh - 56px); /* subtract bottom bar height */
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

  /* Responsive: Tablet — narrower rail */
  @media (max-width: 1024px) {
    .desktop-area {
      margin-left: 56px;
    }
  }

  /* Responsive: Mobile — no rail, full width */
  @media (max-width: 768px) {
    .desktop-area {
      margin-left: 0;
    }
  }
</style>
