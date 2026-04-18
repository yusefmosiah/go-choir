<!--
  Shell — authenticated desktop shell with runtime prompt UI.

  Clearly distinct from the guest auth UI. Exposes:
    - visible logout control
    - session-aware current-user display
    - bootstrap data from GET /api/shell/bootstrap
    - live-channel status from GET /api/ws
    - runtime prompt/loop UI that submits through POST /api/agent/loop,
      renders status/event progress, and returns a real provider-backed
      answer (VAL-RUNTIME-007, VAL-CROSS-109)
    - in-shell protected refresh action that triggers renewal when
      access expires but refresh state is still valid

  Does not render or boot any protected traffic until the shell
  component is mounted in the authenticated state.

  Renewal and fallback behaviour (VAL-CROSS-004 / VAL-CROSS-008):
    - Protected bootstrap fetch uses fetchWithRenewal: on 401 the shell
      silently renews via GET /auth/session (refresh rotation) and retries.
    - If renewal fails, the shell dispatches an "authexpired" event so
      the root App transitions to the guest auth state.
    - The live channel reconnects after successful renewal. If renewal
      fails during reconnection, the shell also dispatches "authexpired".
    - The in-shell "Refresh" action re-fetches bootstrap data via
      fetchWithRenewal. When access is expired but refresh is valid,
      this triggers refresh rotation without a page reload. When both
      are invalid, the shell falls back cleanly to guest auth state.

  Renewal-safe loop submission (VAL-CROSS-111):
    - The TaskRunner component uses fetchWithRenewal for loop submission
      and guards against duplicate submission during renewal retry.

  Reattachment across reload/new-tab (VAL-CROSS-121):
    - The TaskRunner checks for an in-flight loop handle on mount
      and reattaches instead of resubmitting.

  Data attributes for test targeting:
    data-shell               — root container
    data-shell-header        — top bar with app name, user, logout
    data-shell-logout        — logout button
    data-shell-user          — current user display
    data-shell-bootstrap     — bootstrap data section
    data-shell-live-status   — live channel status indicator
    data-shell-refresh       — in-shell protected refresh action button
    data-shell-refresh-status — renewal status indicator next to refresh
-->
<script>
  import { createEventDispatcher } from 'svelte';
  import { fetchWithRenewal, AuthRequiredError, renewSession } from './auth.js';
  import TaskRunner from './TaskRunner.svelte';

  export let currentUser = null;

  const dispatch = createEventDispatcher();

  /** Bootstrap data from GET /api/shell/bootstrap. */
  let bootstrapData = null;

  /** Bootstrap fetch error, if any. */
  let bootstrapError = '';

  /** Live-channel state: 'disconnected' | 'connecting' | 'connected' | 'error'. */
  let liveStatus = 'disconnected';

  /** Live-channel error message. */
  let liveError = '';

  /** WebSocket reference. */
  let ws = null;

  // ----- In-shell refresh / renewal state -----
  /** Whether an in-shell refresh is in progress. */
  let refreshing = false;

  /** Status message from the last in-shell refresh attempt. */
  let refreshStatus = '';

  // ----- WS reconnection state -----
  /** Whether the WS was closed intentionally (logout). */
  let wsClosedByLogout = false;

  /** Current WS reconnection attempt number. */
  let wsReconnectAttempt = 0;

  /** Whether a WS reconnection is already in progress. */
  let wsReconnecting = false;

  /** Maximum number of WS reconnection attempts before giving up. */
  const MAX_WS_RECONNECT_ATTEMPTS = 5;

  /** Base delay in ms between WS reconnection attempts. */
  const WS_RECONNECT_BASE_DELAY = 1000;

  async function fetchBootstrap() {
    bootstrapError = '';
    try {
      const res = await fetchWithRenewal('/api/shell/bootstrap', {
        method: 'GET',
      });
      if (!res.ok) {
        bootstrapError = `Bootstrap failed (${res.status})`;
        return;
      }
      bootstrapData = await res.json();
    } catch (err) {
      if (err instanceof AuthRequiredError) {
        // Renewal failed — fall back to guest auth state.
        dispatch('authexpired');
        return;
      }
      bootstrapError = 'Bootstrap request failed';
    }
  }

  /**
   * In-shell protected action: re-fetches bootstrap data via
   * fetchWithRenewal. When the access JWT is expired but the refresh
   * cookie is still valid, this triggers refresh rotation through
   * GET /auth/session without a page reload. If renewal fails, the
   * shell dispatches "authexpired" so the app falls back to guest
   * auth state cleanly.
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
        // Renewal failed — fall back to guest auth state.
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
        wsReconnectAttempt = 0; // Reset on successful connection.
      };

      ws.onmessage = (_event) => {
        // Messages from the live channel — future feature will handle these.
      };

      ws.onerror = () => {
        liveStatus = 'error';
        liveError = 'Live channel error';
      };

      ws.onclose = () => {
        if (wsClosedByLogout) {
          // Logout triggered close — don't reconnect.
          liveStatus = 'disconnected';
          return;
        }

        if (liveStatus !== 'error') {
          liveStatus = 'disconnected';
        }

        // Attempt reconnection with renewal (VAL-CROSS-004).
        attemptWsReconnection();
      };
    } catch (_err) {
      liveStatus = 'error';
      liveError = 'Could not open live channel';
    }
  }

  /**
   * Attempts to reconnect the live channel after it closes.
   * Before reconnecting, checks/refreshes the session via GET /auth/session.
   * If renewal fails, dispatches "authexpired" so the app falls back to
   * guest auth state (VAL-CROSS-008).
   */
  async function attemptWsReconnection() {
    if (wsReconnecting) return; // Already reconnecting.
    if (wsClosedByLogout) return; // Logout triggered close.
    if (wsReconnectAttempt >= MAX_WS_RECONNECT_ATTEMPTS) {
      liveStatus = 'error';
      liveError = 'Could not reconnect after multiple attempts';
      return;
    }

    wsReconnecting = true;
    wsReconnectAttempt++;
    const delay = WS_RECONNECT_BASE_DELAY * wsReconnectAttempt;

    try {
      // Wait before attempting reconnection (exponential backoff).
      await new Promise(resolve => setTimeout(resolve, delay));

      // Check/renew session before reconnecting.
      const { renewed } = await renewSession();

      if (!renewed) {
        // Session renewal failed — fall back to guest auth state.
        dispatch('authexpired');
        return;
      }

      // Session is valid (possibly renewed) — attempt reconnection.
      connectLiveChannel();
    } finally {
      wsReconnecting = false;
    }
  }

  function handleLogout() {
    // Mark as intentional close so reconnection is not attempted.
    wsClosedByLogout = true;
    // Close the live channel before logging out.
    if (ws) {
      ws.close();
      ws = null;
    }
    dispatch('logout');
  }

  /**
   * Tears down the live channel when the Shell component is destroyed.
   *
   * Defense in depth: even if handleLogout was not called (e.g., the
   * App transitions away from the shell due to authexpiry), the
   * WebSocket must be closed and reconnection prevented so no stale
   * live channel survives into a new user's session (VAL-CROSS-007).
   */
  function teardownLiveChannel() {
    wsClosedByLogout = true; // Prevent any reconnection attempts.
    if (ws) {
      ws.close();
      ws = null;
    }
  }

  import { onMount } from 'svelte';
  import { onDestroy } from 'svelte';

  onMount(() => {
    // Boot the shell — fetch bootstrap data and open live channel.
    fetchBootstrap();
    connectLiveChannel();
  });

  onDestroy(() => {
    // Ensure the live channel is always torn down when the Shell is
    // destroyed, regardless of why (logout, authexpiry, navigation).
    teardownLiveChannel();
  });
</script>

<div class="shell" data-shell>
  <header class="shell-header" data-shell-header>
    <div class="header-left">
      <h1 class="app-name">go-choir</h1>
      <span class="app-badge">Shell</span>
    </div>
    <div class="header-right">
      <div class="user-area" data-shell-user>
        <span class="user-icon">👤</span>
        <span class="user-name">{currentUser?.email || 'unknown'}</span>
      </div>
      <button class="logout-btn" data-shell-logout on:click={handleLogout}>
        Sign Out
      </button>
    </div>
  </header>

  <main class="shell-main">
    <section class="panel bootstrap-panel" data-shell-bootstrap>
      <h2>Shell Bootstrap</h2>
      {#if bootstrapError}
        <p class="panel-error">{bootstrapError}</p>
      {:else if bootstrapData}
        <pre class="bootstrap-data">{JSON.stringify(bootstrapData, null, 2)}</pre>
      {:else}
        <p class="panel-loading">Loading bootstrap data…</p>
      {/if}
      <div class="refresh-row">
        <button class="refresh-btn" data-shell-refresh on:click={handleRefresh} disabled={refreshing}>
          {refreshing ? 'Refreshing…' : 'Refresh'}
        </button>
        {#if refreshStatus}
          <span class="refresh-status" data-shell-refresh-status>{refreshStatus}</span>
        {/if}
      </div>
    </section>

    <section class="panel live-panel" data-shell-live-status>
      <h2>Live Channel</h2>
      <div class="status-row">
        <span class="status-dot" class:connected={liveStatus === 'connected'} class:connecting={liveStatus === 'connecting'} class:error={liveStatus === 'error'}></span>
        <span class="status-text">
          {#if liveStatus === 'connected'}
            Connected
          {:else if liveStatus === 'connecting'}
            Connecting…
          {:else if liveStatus === 'error'}
            Error: {liveError}
          {:else}
            Disconnected
          {/if}
        </span>
      </div>
    </section>

    <section class="panel runtime-panel">
      <h2>Runtime Prompt</h2>
      <TaskRunner on:authexpired={() => dispatch('authexpired')} />
    </section>
  </main>
</div>

<style>
  .shell {
    display: flex;
    flex-direction: column;
    min-height: 100vh;
  }

  /* ---- Header ---- */
  .shell-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 0.75rem 1.5rem;
    background: #1a1a2e;
    border-bottom: 1px solid #2a2a4a;
    flex-shrink: 0;
  }

  .header-left {
    display: flex;
    align-items: center;
    gap: 0.75rem;
  }

  .app-name {
    font-size: 1.25rem;
    font-weight: 700;
    letter-spacing: 0.05em;
    color: #ffffff;
  }

  .app-badge {
    font-size: 0.7rem;
    font-weight: 600;
    letter-spacing: 0.08em;
    text-transform: uppercase;
    color: #3b82f6;
    background: rgba(59, 130, 246, 0.15);
    padding: 0.15rem 0.5rem;
    border-radius: 4px;
  }

  .header-right {
    display: flex;
    align-items: center;
    gap: 1rem;
  }

  .user-area {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    color: #c0c0c0;
  }

  .user-icon {
    font-size: 1.1rem;
  }

  .user-name {
    font-size: 0.9rem;
    font-weight: 500;
  }

  .logout-btn {
    padding: 0.4rem 1rem;
    font-size: 0.85rem;
    font-weight: 600;
    color: #f87171;
    background: rgba(248, 113, 113, 0.1);
    border: 1px solid rgba(248, 113, 113, 0.25);
    border-radius: 6px;
    cursor: pointer;
    transition: background 0.2s, border-color 0.2s;
  }

  .logout-btn:hover {
    background: rgba(248, 113, 113, 0.2);
    border-color: rgba(248, 113, 113, 0.4);
  }

  /* ---- Main content ---- */
  .shell-main {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(300px, 1fr));
    gap: 1rem;
    padding: 1.5rem;
    flex: 1;
  }

  .panel {
    background: #1a1a1a;
    border: 1px solid #2a2a2a;
    border-radius: 10px;
    padding: 1.25rem 1.5rem;
  }

  .panel h2 {
    font-size: 1rem;
    font-weight: 600;
    color: #a0a0a0;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    margin-bottom: 0.75rem;
  }

  .panel-error {
    color: #f87171;
    font-size: 0.9rem;
  }

  .panel-loading {
    color: #555;
    font-size: 0.9rem;
  }

  .bootstrap-data {
    font-size: 0.8rem;
    color: #a0a0a0;
    background: #111;
    border: 1px solid #222;
    border-radius: 6px;
    padding: 0.75rem;
    overflow-x: auto;
    white-space: pre-wrap;
    word-break: break-all;
  }

  /* ---- Live channel status ---- */
  .status-row {
    display: flex;
    align-items: center;
    gap: 0.6rem;
  }

  .status-dot {
    width: 10px;
    height: 10px;
    border-radius: 50%;
    background: #444;
    flex-shrink: 0;
  }

  .status-dot.connected {
    background: #4ade80;
    box-shadow: 0 0 6px rgba(74, 222, 128, 0.5);
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

  .status-text {
    font-size: 0.9rem;
    color: #c0c0c0;
  }

  /* ---- Refresh row ---- */
  .refresh-row {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    margin-top: 0.75rem;
  }

  .refresh-btn {
    padding: 0.35rem 0.9rem;
    font-size: 0.8rem;
    font-weight: 600;
    color: #60a5fa;
    background: rgba(96, 165, 250, 0.1);
    border: 1px solid rgba(96, 165, 250, 0.25);
    border-radius: 6px;
    cursor: pointer;
    transition: background 0.2s, border-color 0.2s;
  }

  .refresh-btn:hover:not(:disabled) {
    background: rgba(96, 165, 250, 0.2);
    border-color: rgba(96, 165, 250, 0.4);
  }

  .refresh-btn:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  .refresh-status {
    font-size: 0.8rem;
    color: #4ade80;
  }
</style>
