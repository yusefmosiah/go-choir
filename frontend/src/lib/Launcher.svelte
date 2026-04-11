<!--
  Launcher — app launcher for the desktop shell.

  Provides entries for installed apps. When an app is launched, it
  dispatches a "launchapp" event so the Desktop can create a new
  window for that application.

  The launcher includes E-Text as the first real app (VAL-DESKTOP-002)
  and lists placeholder entries for future apps.

  Data attributes for test targeting:
    data-launcher          — root container
    data-launcher-toggle   — button to open/close the launcher
    data-launcher-menu     — dropdown menu with app entries
    data-launcher-app      — individual app entry
    data-launcher-app-etext — E-Text app entry specifically
-->
<script>
  import { createEventDispatcher } from 'svelte';

  const dispatch = createEventDispatcher();

  /** Whether the launcher menu is open. */
  let menuOpen = false;

  /** Registered apps available in the launcher. */
  const apps = [
    { id: 'etext', name: 'E-Text', icon: '📝', description: 'Document editor' },
    { id: 'terminal', name: 'Terminal', icon: '💻', description: 'Coming soon' },
    { id: 'files', name: 'Files', icon: '📁', description: 'Coming soon' },
    { id: 'mindgraph', name: 'Mind Graph', icon: '🕸️', description: 'Coming soon' },
  ];

  function toggleMenu() {
    menuOpen = !menuOpen;
  }

  function launchApp(app) {
    dispatch('launchapp', { appId: app.id, appName: app.name, icon: app.icon });
    menuOpen = false;
  }

  function handleKeydown(event) {
    if (event.key === 'Escape') {
      menuOpen = false;
    }
  }
</script>

<!-- svelte-ignore a11y-click-events-have-key-events -->
<!-- svelte-ignore a11y-no-static-element-interactions -->
<div class="launcher" data-launcher on:keydown={handleKeydown}>
  <button
    class="launcher-toggle"
    data-launcher-toggle
    on:click={toggleMenu}
    title="Launch applications"
  >
    <span class="launcher-icon">⬡</span>
  </button>

  {#if menuOpen}
    <!-- svelte-ignore a11y-click-events-have-key-events -->
    <!-- svelte-ignore a11y-no-static-element-interactions -->
    <div class="launcher-backdrop" on:click={() => { menuOpen = false; }}></div>
    <ul class="launcher-menu" data-launcher-menu>
      {#each apps as app}
        <li>
          <button
            class="launcher-app"
            data-launcher-app
            data-app-id={app.id}
            on:click={() => launchApp(app)}
            disabled={app.id !== 'etext'}
            title={app.description}
          >
            <span class="app-icon">{app.icon}</span>
            <span class="app-name">{app.name}</span>
            {#if app.id !== 'etext'}
              <span class="app-badge">Soon</span>
            {/if}
          </button>
        </li>
      {/each}
    </ul>
  {/if}
</div>

<style>
  .launcher {
    position: relative;
  }

  .launcher-toggle {
    width: 36px;
    height: 36px;
    display: flex;
    align-items: center;
    justify-content: center;
    background: rgba(59, 130, 246, 0.15);
    border: 1px solid rgba(59, 130, 246, 0.3);
    border-radius: 8px;
    cursor: pointer;
    transition: background 0.2s;
  }

  .launcher-toggle:hover {
    background: rgba(59, 130, 246, 0.3);
  }

  .launcher-icon {
    font-size: 1.1rem;
    color: #3b82f6;
  }

  .launcher-backdrop {
    position: fixed;
    top: 0;
    left: 0;
    right: 0;
    bottom: 0;
    z-index: 999;
  }

  .launcher-menu {
    position: absolute;
    top: 42px;
    left: 0;
    background: #1e1e2e;
    border: 1px solid #333;
    border-radius: 10px;
    padding: 0.5rem;
    list-style: none;
    min-width: 220px;
    z-index: 1000;
    box-shadow: 0 8px 32px rgba(0, 0, 0, 0.5);
  }

  .launcher-app {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    width: 100%;
    padding: 0.6rem 0.75rem;
    background: transparent;
    border: none;
    border-radius: 6px;
    cursor: pointer;
    color: #e0e0e0;
    transition: background 0.15s;
    text-align: left;
  }

  .launcher-app:hover:not(:disabled) {
    background: rgba(59, 130, 246, 0.15);
  }

  .launcher-app:disabled {
    opacity: 0.4;
    cursor: not-allowed;
  }

  .app-icon {
    font-size: 1.2rem;
    width: 24px;
    text-align: center;
    flex-shrink: 0;
  }

  .app-name {
    font-size: 0.9rem;
    font-weight: 500;
    flex: 1;
  }

  .app-badge {
    font-size: 0.65rem;
    font-weight: 600;
    letter-spacing: 0.05em;
    text-transform: uppercase;
    color: #888;
    background: rgba(136, 136, 136, 0.1);
    padding: 0.1rem 0.4rem;
    border-radius: 3px;
  }
</style>
