<!--
  Desktop — real desktop shell with window manager, launcher, and
  server-backed state persistence.

  Replaces the placeholder Shell with a real desktop that owns:
    - launcher
    - window lifecycle (open, focus, minimize, maximize, restore, close)
    - focus/z-order management
    - drag/resize geometry
    - server-backed desktop state persistence and restore

  Desktop state is persisted server-side through GET/PUT /api/desktop/state
  so that restore works across fresh browser contexts for the same user
  (VAL-DESKTOP-007).

  The desktop preserves the existing shell behaviors:
    - bootstrap data fetch
    - live channel connection
    - logout control
    - authexpired fallback
    - runtime prompt/task UI

  Data attributes for test targeting:
    data-desktop             — root desktop container
    data-desktop-bar         — top bar with app name, launcher, user, logout
    data-desktop-taskbar     — bottom taskbar with minimized windows
    data-desktop-logout      — logout button
    data-desktop-user        — current user display
    data-desktop-windows     — window container area
-->
<script>
  import { createEventDispatcher } from 'svelte';
  import { onMount } from 'svelte';
  import { onDestroy } from 'svelte';
  import { fetchWithRenewal, AuthRequiredError, renewSession } from './auth.js';
  import { fetchDesktopState, saveDesktopState } from './desktop.js';
  import TaskRunner from './TaskRunner.svelte';
  import Launcher from './Launcher.svelte';
  import Window from './Window.svelte';

  export let currentUser = null;

  const dispatch = createEventDispatcher();

  // ---- Window manager state ----
  let windows = [];
  let activeWindowId = '';
  let nextZIndex = 1;

  // ---- Bootstrap and live channel state (preserved from Shell) ----
  let bootstrapData = null;
  let bootstrapError = '';
  let liveStatus = 'disconnected';
  let liveError = '';
  let ws = null;

  // ---- In-shell refresh / renewal state (preserved from Shell) ----
  let refreshing = false;
  let refreshStatus = '';

  // ---- Desktop state persistence ----
  let stateLoaded = false;
  let saveTimer = null;

  const SAVE_DEBOUNCE_MS = 500;

  // ---- WS reconnection state ----
  let wsClosedByLogout = false;
  let wsReconnectAttempt = 0;
  let wsReconnecting = false;
  const MAX_WS_RECONNECT_ATTEMPTS = 5;
  const WS_RECONNECT_BASE_DELAY = 1000;

  // ---- Unique ID generation ----
  let windowCounter = 0;

  function generateWindowId() {
    windowCounter++;
    return `win-${Date.now()}-${windowCounter}`;
  }

  // ---- Window lifecycle ----

  function openApp(appId, appName, icon) {
    // Check if an app of this type is already open and not closed.
    const existing = windows.find(w => w.appId === appId && w.mode !== 'closed');
    if (existing) {
      // Focus the existing window instead of opening a new one.
      focusWindow(existing.windowId);
      // If it was minimized, restore it.
      if (existing.mode === 'minimized') {
        restoreWindow(existing.windowId);
      }
      return;
    }

    const windowId = generateWindowId();
    const offset = (windows.length % 8) * 30;
    const newWindow = {
      windowId,
      appId,
      title: appName || appId,
      icon: icon || '📱',
      x: 80 + offset,
      y: 60 + offset,
      width: 650,
      height: 450,
      mode: 'normal',
      zIndex: nextZIndex++,
      appContext: {},
    };

    windows = [...windows, newWindow];
    activeWindowId = windowId;
    scheduleSave();
  }

  function closeWindow(windowId) {
    windows = windows.filter(w => w.windowId !== windowId);
    if (activeWindowId === windowId) {
      // Focus the topmost remaining window.
      const remaining = windows.filter(w => w.mode !== 'closed');
      if (remaining.length > 0) {
        const topWindow = remaining.reduce((a, b) => a.zIndex > b.zIndex ? a : b);
        activeWindowId = topWindow.windowId;
      } else {
        activeWindowId = '';
      }
    }
    scheduleSave();
  }

  function focusWindow(windowId) {
    activeWindowId = windowId;
    // Bring the focused window to the top of the z-order.
    windows = windows.map(w => {
      if (w.windowId === windowId) {
        return { ...w, zIndex: nextZIndex++ };
      }
      return w;
    });
    scheduleSave();
  }

  function minimizeWindow(windowId) {
    windows = windows.map(w => {
      if (w.windowId === windowId) {
        return { ...w, mode: 'minimized' };
      }
      return w;
    });
    // Focus the next visible window.
    if (activeWindowId === windowId) {
      const visible = windows.filter(w => w.mode === 'normal' || w.mode === 'maximized');
      if (visible.length > 0) {
        const topWindow = visible.reduce((a, b) => a.zIndex > b.zIndex ? a : b);
        activeWindowId = topWindow.windowId;
      } else {
        activeWindowId = '';
      }
    }
    scheduleSave();
  }

  function maximizeWindow(windowId) {
    windows = windows.map(w => {
      if (w.windowId === windowId) {
        return {
          ...w,
          mode: 'maximized',
          restoredGeometry: { x: w.x, y: w.y, width: w.width, height: w.height },
        };
      }
      return w;
    });
    activeWindowId = windowId;
    scheduleSave();
  }

  function restoreWindow(windowId) {
    windows = windows.map(w => {
      if (w.windowId === windowId) {
        const restored = w.restoredGeometry || { x: 100, y: 100, width: 600, height: 400 };
        return {
          ...w,
          mode: 'normal',
          x: restored.x,
          y: restored.y,
          width: restored.width,
          height: restored.height,
          restoredGeometry: null,
        };
      }
      return w;
    });
    activeWindowId = windowId;
    scheduleSave();
  }

  function moveWindow(windowId, x, y) {
    windows = windows.map(w => {
      if (w.windowId === windowId) {
        return { ...w, x, y };
      }
      return w;
    });
    scheduleSave();
  }

  function resizeWindow(windowId, x, y, width, height) {
    windows = windows.map(w => {
      if (w.windowId === windowId) {
        return { ...w, x, y, width, height };
      }
      return w;
    });
    scheduleSave();
  }

  // ---- Desktop state persistence ----

  async function loadDesktopState() {
    try {
      const state = await fetchDesktopState();
      if (state && state.windows && state.windows.length > 0) {
        windows = state.windows.map(w => ({
          windowId: w.window_id,
          appId: w.app_id,
          title: w.title,
          icon: '📱',
          x: w.geometry?.x ?? 100,
          y: w.geometry?.y ?? 100,
          width: w.geometry?.width ?? 600,
          height: w.geometry?.height ?? 400,
          mode: w.mode ?? 'normal',
          zIndex: w.z_index ?? 1,
          restoredGeometry: w.restored_geometry ?? null,
          appContext: w.app_context ?? {},
        }));
        activeWindowId = state.active_window_id || '';
        // Recalculate nextZIndex.
        if (windows.length > 0) {
          nextZIndex = Math.max(...windows.map(w => w.zIndex)) + 1;
        }
      }
    } catch (err) {
      if (err instanceof AuthRequiredError) {
        dispatch('authexpired');
        return;
      }
      // Non-critical: desktop state load failure shouldn't block the shell.
    }
    stateLoaded = true;
  }

  function scheduleSave() {
    if (saveTimer) {
      clearTimeout(saveTimer);
    }
    saveTimer = setTimeout(() => {
      persistDesktopState();
    }, SAVE_DEBOUNCE_MS);
  }

  async function persistDesktopState() {
    try {
      const state = {
        windows: windows.map(w => ({
          window_id: w.windowId,
          app_id: w.appId,
          title: w.title,
          geometry: { x: w.x, y: w.y, width: w.width, height: w.height },
          restored_geometry: w.restoredGeometry,
          mode: w.mode,
          z_index: w.zIndex,
          app_context: w.appContext,
        })),
        active_window_id: activeWindowId,
      };
      await saveDesktopState(state);
    } catch (err) {
      if (err instanceof AuthRequiredError) {
        dispatch('authexpired');
        return;
      }
      // Non-critical: persistence failure shouldn't break the desktop.
    }
  }

  // ---- Window event handlers ----

  function handleWindowClose(event) {
    closeWindow(event.detail.windowId);
  }

  function handleWindowFocus(event) {
    focusWindow(event.detail.windowId);
  }

  function handleWindowMinimize(event) {
    minimizeWindow(event.detail.windowId);
  }

  function handleWindowMaximize(event) {
    maximizeWindow(event.detail.windowId);
  }

  function handleWindowRestore(event) {
    restoreWindow(event.detail.windowId);
  }

  function handleWindowMove(event) {
    moveWindow(event.detail.windowId, event.detail.x, event.detail.y);
  }

  function handleWindowResize(event) {
    resizeWindow(event.detail.windowId, event.detail.x, event.detail.y, event.detail.width, event.detail.height);
  }

  function handleLaunchApp(event) {
    openApp(event.detail.appId, event.detail.appName, event.detail.icon);
  }

  function handleTaskbarClick(windowId) {
    const win = windows.find(w => w.windowId === windowId);
    if (!win) return;
    if (win.mode === 'minimized') {
      restoreWindow(windowId);
    } else {
      focusWindow(windowId);
    }
  }

  // ---- Minimized windows for taskbar ----

  $: minimizedWindows = windows.filter(w => w.mode === 'minimized');

  // ---- Bootstrap and live channel (preserved from Shell) ----

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

  /**
   * In-shell protected action: re-fetches bootstrap data via
   * fetchWithRenewal. Preserved from the Shell component for
   * session renewal testing (VAL-CROSS-004).
   */
  async function handleRefresh() {
    refreshing = true;
    refreshStatus = '';
    bootstrapError = '';

    try {
      const res = await fetchWithRenewal('/api/shell/bootstrap', {
        method: 'GET',
      });
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
      bootstrapError = 'Bootstrap request failed';
      refreshStatus = 'Refresh failed';
    } finally {
      refreshing = false;
    }
  }

  function connectLiveChannel() {
    liveStatus = 'connecting';
    liveError = '';
    try {
      const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
      const wsUrl = `${protocol}//${window.location.host}/api/ws`;
      ws = new WebSocket(wsUrl);
      ws.onopen = () => {
        liveStatus = 'connected';
        wsReconnectAttempt = 0;
      };
      ws.onmessage = (_event) => { };
      ws.onerror = () => {
        liveStatus = 'error';
        liveError = 'Live channel error';
      };
      ws.onclose = () => {
        if (wsClosedByLogout) {
          liveStatus = 'disconnected';
          return;
        }
        if (liveStatus !== 'error') {
          liveStatus = 'disconnected';
        }
        attemptWsReconnection();
      };
    } catch (_err) {
      liveStatus = 'error';
      liveError = 'Could not open live channel';
    }
  }

  async function attemptWsReconnection() {
    if (wsReconnecting) return;
    if (wsClosedByLogout) return;
    if (wsReconnectAttempt >= MAX_WS_RECONNECT_ATTEMPTS) {
      liveStatus = 'error';
      liveError = 'Could not reconnect after multiple attempts';
      return;
    }
    wsReconnecting = true;
    wsReconnectAttempt++;
    const delay = WS_RECONNECT_BASE_DELAY * wsReconnectAttempt;
    try {
      await new Promise(resolve => setTimeout(resolve, delay));
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

  function handleLogout() {
    wsClosedByLogout = true;
    if (ws) {
      ws.close();
      ws = null;
    }
    dispatch('logout');
  }

  function teardownLiveChannel() {
    wsClosedByLogout = true;
    if (ws) {
      ws.close();
      ws = null;
    }
  }

  // ---- Lifecycle ----

  onMount(() => {
    fetchBootstrap();
    connectLiveChannel();
    loadDesktopState();
  });

  onDestroy(() => {
    teardownLiveChannel();
    if (saveTimer) {
      clearTimeout(saveTimer);
    }
  });
</script>

<div class="desktop" data-desktop data-shell>
  <!-- Top bar -->
  <header class="desktop-bar" data-desktop-bar>
    <div class="bar-left">
      <Launcher on:launchapp={handleLaunchApp} />
      <h1 class="app-name">go-choir</h1>
      <span class="app-badge">Desktop</span>
      <span class="live-indicator" data-desktop-live-status data-shell-live-status>
        <span class="status-dot" class:connected={liveStatus === 'connected'} class:connecting={liveStatus === 'connecting'} class:error={liveStatus === 'error'}></span>
        <span class="status-label">{liveStatus === 'connected' ? 'Connected' : liveStatus === 'connecting' ? 'Connecting' : liveStatus === 'error' ? 'Error' : 'Disconnected'}</span>
      </span>
    </div>
    <div class="bar-right">
      <div class="user-area" data-desktop-user data-shell-user>
        <span class="user-icon">👤</span>
        <span class="user-name">{currentUser?.username || 'unknown'}</span>
      </div>
      <button class="logout-btn" data-desktop-logout data-shell-logout on:click={handleLogout}>
        Sign Out
      </button>
    </div>
  </header>

  <!-- Window container -->
  <div class="desktop-area" data-desktop-windows>
    {#each windows as win (win.windowId)}
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
          active={win.windowId === activeWindowId}
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
            <div class="app-content etext-content">
              <div class="app-header">
                <span class="app-icon">📝</span>
                <span class="app-label">E-Text Editor</span>
                <span class="app-hint">Document editing will be available in a later feature</span>
              </div>
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

    <!-- Runtime prompt panel (always available as a desktop panel) -->
    <div class="runtime-panel">
      <!-- Bootstrap data section (preserved for test compatibility) -->
      <div class="bootstrap-row" data-shell-bootstrap>
        {#if bootstrapError}
          <span class="bootstrap-error">{bootstrapError}</span>
        {:else if bootstrapData}
          <details>
            <summary>Bootstrap</summary>
            <pre class="bootstrap-data">{JSON.stringify(bootstrapData, null, 2)}</pre>
          </details>
        {:else}
          <span class="bootstrap-loading">Loading…</span>
        {/if}
        <button class="refresh-btn" data-shell-refresh on:click={handleRefresh} disabled={refreshing}>
          {refreshing ? 'Refreshing…' : 'Refresh'}
        </button>
        {#if refreshStatus}
          <span class="refresh-status" data-shell-refresh-status>{refreshStatus}</span>
        {/if}
      </div>
      <TaskRunner on:authexpired={() => dispatch('authexpired')} />
    </div>
  </div>

  <!-- Taskbar with minimized windows -->
  {#if minimizedWindows.length > 0}
    <div class="taskbar" data-desktop-taskbar>
      {#each minimizedWindows as win (win.windowId)}
        <button
          class="taskbar-item"
          on:click={() => handleTaskbarClick(win.windowId)}
          title={win.title}
        >
          <span class="taskbar-icon">{win.icon || '📱'}</span>
          <span class="taskbar-name">{win.title}</span>
        </button>
      {/each}
    </div>
  {/if}
</div>

<style>
  .desktop {
    display: flex;
    flex-direction: column;
    min-height: 100vh;
    background: #0f0f0f;
  }

  /* ---- Top bar ---- */
  .desktop-bar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 0 1rem;
    height: 44px;
    background: #11111b;
    border-bottom: 1px solid #2a2a3a;
    flex-shrink: 0;
    z-index: 100;
  }

  .bar-left {
    display: flex;
    align-items: center;
    gap: 0.75rem;
  }

  .app-name {
    font-size: 1.1rem;
    font-weight: 700;
    letter-spacing: 0.04em;
    color: #ffffff;
  }

  .app-badge {
    font-size: 0.65rem;
    font-weight: 600;
    letter-spacing: 0.08em;
    text-transform: uppercase;
    color: #3b82f6;
    background: rgba(59, 130, 246, 0.15);
    padding: 0.15rem 0.5rem;
    border-radius: 4px;
  }

  .live-indicator {
    display: flex;
    align-items: center;
    gap: 0.4rem;
  }

  .status-dot {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background: #444;
  }

  .status-dot.connected {
    background: #4ade80;
    box-shadow: 0 0 4px rgba(74, 222, 128, 0.5);
  }

  .status-dot.connecting {
    background: #fbbf24;
    animation: pulse 1.5s infinite;
  }

  .status-dot.error {
    background: #f87171;
  }

  @keyframes pulse {
    0%, 100% { opacity: 1; }
    50% { opacity: 0.4; }
  }

  .status-label {
    font-size: 0.75rem;
    color: #888;
  }

  .bar-right {
    display: flex;
    align-items: center;
    gap: 1rem;
  }

  .user-area {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    color: #c0c0c0;
  }

  .user-icon {
    font-size: 1rem;
  }

  .user-name {
    font-size: 0.85rem;
    font-weight: 500;
  }

  .logout-btn {
    padding: 0.35rem 0.85rem;
    font-size: 0.8rem;
    font-weight: 600;
    color: #f87171;
    background: rgba(248, 113, 113, 0.1);
    border: 1px solid rgba(248, 113, 113, 0.25);
    border-radius: 6px;
    cursor: pointer;
    transition: background 0.2s;
  }

  .logout-btn:hover {
    background: rgba(248, 113, 113, 0.2);
  }

  /* ---- Desktop area (window container) ---- */
  .desktop-area {
    flex: 1;
    position: relative;
    overflow: hidden;
  }

  .runtime-panel {
    position: absolute;
    bottom: 0;
    left: 0;
    right: 0;
    background: #11111b;
    border-top: 1px solid #2a2a3a;
    padding: 0.75rem 1rem;
    z-index: 50;
  }

  /* ---- App content (inside windows) ---- */
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

  .app-icon {
    font-size: 1.2rem;
  }

  .app-label {
    font-size: 0.95rem;
    font-weight: 600;
    color: #c0c0d0;
  }

  .app-hint {
    font-size: 0.75rem;
    color: #666;
    margin-left: 0.5rem;
  }

  /* ---- Taskbar ---- */
  .taskbar {
    display: flex;
    align-items: center;
    gap: 0.25rem;
    padding: 0.4rem 0.75rem;
    background: #11111b;
    border-top: 1px solid #2a2a3a;
    height: 40px;
    flex-shrink: 0;
  }

  .taskbar-item {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    padding: 0.3rem 0.7rem;
    background: rgba(255, 255, 255, 0.05);
    border: 1px solid #333;
    border-radius: 4px;
    cursor: pointer;
    color: #c0c0c0;
    transition: background 0.15s;
  }

  .taskbar-item:hover {
    background: rgba(59, 130, 246, 0.15);
    border-color: rgba(59, 130, 246, 0.3);
  }

  .taskbar-icon {
    font-size: 0.9rem;
  }

  .taskbar-name {
    font-size: 0.75rem;
  }

  /* ---- Bootstrap row (preserved from Shell) ---- */
  .bootstrap-row {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    margin-bottom: 0.5rem;
    flex-wrap: wrap;
  }

  .bootstrap-error {
    color: #f87171;
    font-size: 0.8rem;
  }

  .bootstrap-loading {
    color: #555;
    font-size: 0.8rem;
  }

  .bootstrap-data {
    font-size: 0.7rem;
    color: #888;
    background: #0a0a0a;
    border: 1px solid #222;
    border-radius: 4px;
    padding: 0.4rem;
    max-height: 80px;
    overflow: auto;
    white-space: pre-wrap;
    word-break: break-all;
  }

  .refresh-btn {
    padding: 0.25rem 0.6rem;
    font-size: 0.75rem;
    font-weight: 600;
    color: #60a5fa;
    background: rgba(96, 165, 250, 0.1);
    border: 1px solid rgba(96, 165, 250, 0.25);
    border-radius: 4px;
    cursor: pointer;
  }

  .refresh-btn:hover:not(:disabled) {
    background: rgba(96, 165, 250, 0.2);
  }

  .refresh-btn:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  .refresh-status {
    font-size: 0.75rem;
    color: #4ade80;
  }
</style>
