<!--
  BrowserApp — simple web browser app for the ChoirOS desktop.

  Features:
    - URL input bar at the top of the content area
    - Basic navigation: back, forward, reload
    - Loading indicator while page loads
    - iframe loads URLs directly (no server-side proxy)
    - Graceful error message when sites block iframe embedding
    - Works in floating window and mobile focus mode

  Data attributes for test targeting:
    data-browser-app        — root browser container
    data-browser-url-bar    — URL bar area
    data-browser-url-input  — URL text input
    data-browser-go-btn     — Go/submit button
    data-browser-nav-back   — back navigation button
    data-browser-nav-forward — forward navigation button
    data-browser-nav-reload — reload button
    data-browser-loading    — loading indicator
    data-browser-iframe     — iframe element
    data-browser-error      — error message display
-->
<script>
  import { onMount } from 'svelte';

  // Auto-focus action for inputs
  function autofocus(node) {
    node.focus();
  }

  // ---- State ----
  let urlInput = 'https://en.wikipedia.org';
  let currentUrl = '';
  let loading = false;
  let error = '';
  let iframeEl = null;

  // Navigation history
  let history = [];
  let historyIndex = -1;

  // Can go back/forward?
  $: canGoBack = historyIndex > 0;
  $: canGoForward = historyIndex < history.length - 1;

  // ---- Navigation ----

  function normalizeUrl(raw) {
    let url = raw.trim();
    if (!url) return '';
    // Add https:// if no protocol specified
    if (!url.match(/^[a-zA-Z]+:\/\//)) {
      // Check if it looks like a domain (contains a dot)
      if (url.includes('.')) {
        url = 'https://' + url;
      } else {
        // Treat as a search query — use Wikipedia search
        url = 'https://en.wikipedia.org/wiki/Special:Search?search=' + encodeURIComponent(url);
      }
    }
    return url;
  }

  function navigateToUrl(url, addToHistory = true) {
    const normalized = normalizeUrl(url);
    if (!normalized) return;

    error = '';
    loading = true;
    urlInput = normalized;
    currentUrl = normalized;

    if (addToHistory) {
      // Trim forward history
      history = history.slice(0, historyIndex + 1);
      history.push(normalized);
      historyIndex = history.length - 1;
    }
  }

  function handleGo() {
    navigateToUrl(urlInput);
  }

  function handleUrlKeydown(event) {
    if (event.key === 'Enter') {
      handleGo();
    }
  }

  function goBack() {
    if (canGoBack) {
      historyIndex--;
      const url = history[historyIndex];
      urlInput = url;
      currentUrl = url;
      loading = true;
      error = '';
    }
  }

  function goForward() {
    if (canGoForward) {
      historyIndex++;
      const url = history[historyIndex];
      urlInput = url;
      currentUrl = url;
      loading = true;
      error = '';
    }
  }

  function reload() {
    if (currentUrl) {
      loading = true;
      error = '';
      // Force reload by briefly clearing the src
      const url = currentUrl;
      currentUrl = '';
      requestAnimationFrame(() => {
        currentUrl = url;
      });
    }
  }

  function handleIframeLoad() {
    loading = false;
    // Try to detect if the iframe loaded correctly
    // We can't read iframe content due to cross-origin policy,
    // but the load event fires regardless
  }

  function handleIframeError() {
    loading = false;
    error = 'Failed to load this page. The website may be unavailable or block iframe embedding.';
  }

  // Monitor for iframe load timeout (sites that block may not fire error event)
  let loadTimeout = null;

  $: if (loading && currentUrl) {
    if (loadTimeout) clearTimeout(loadTimeout);
    loadTimeout = setTimeout(() => {
      // If still loading after 15 seconds, show a message
      if (loading) {
        error = 'Page is taking too long to load. The website may block iframe embedding.';
        loading = false;
      }
    }, 15000);
  }

  // ---- Lifecycle ----

  onMount(() => {
    // Navigate to default URL
    navigateToUrl(urlInput);
  });
</script>

<div class="browser-app" data-browser-app>
  <!-- URL bar -->
  <div class="url-bar" data-browser-url-bar>
    <button
      class="nav-btn"
      data-browser-nav-back
      on:click={goBack}
      disabled={!canGoBack}
      title="Back"
      aria-label="Go back"
    >
      ←
    </button>
    <button
      class="nav-btn"
      data-browser-nav-forward
      on:click={goForward}
      disabled={!canGoForward}
      title="Forward"
      aria-label="Go forward"
    >
      →
    </button>
    <button
      class="nav-btn"
      data-browser-nav-reload
      on:click={reload}
      disabled={!currentUrl}
      title="Reload"
      aria-label="Reload page"
    >
      ↻
    </button>
    <input
      type="text"
      class="url-input"
      data-browser-url-input
      bind:value={urlInput}
      on:keydown={handleUrlKeydown}
      placeholder="Enter URL..."
      aria-label="URL input"
    />
    <button
      class="go-btn"
      data-browser-go-btn
      on:click={handleGo}
      title="Go"
      aria-label="Navigate to URL"
    >
      Go
    </button>
  </div>

  <!-- Loading indicator -->
  {#if loading}
    <div class="loading-bar" data-browser-loading>
      <div class="loading-progress"></div>
    </div>
  {/if}

  <!-- Error message -->
  {#if error}
    <div class="error-message" data-browser-error role="alert">
      <span class="error-icon">⚠️</span>
      <span class="error-text">{error}</span>
      <button
        class="error-dismiss"
        on:click={() => { error = ''; }}
        title="Dismiss"
        aria-label="Dismiss error"
      >
        ✕
      </button>
    </div>
  {/if}

  <!-- iframe -->
  {#if currentUrl}
    <div class="iframe-container">
      <!-- svelte-ignore a11y-missing-attribute -->
      <iframe
        class="browser-iframe"
        data-browser-iframe
        bind:this={iframeEl}
        src={currentUrl}
        on:load={handleIframeLoad}
        on:error={handleIframeError}
        title="Browser content"
        sandbox="allow-scripts allow-same-origin allow-forms allow-popups allow-popups-to-escape-sandbox"
        allow="accelerometer; camera; encrypted-media; geolocation; gyroscope; microphone"
      ></iframe>
    </div>
  {:else}
    <div class="empty-state">
      <span class="empty-icon">🌐</span>
      <span>Enter a URL to start browsing</span>
    </div>
  {/if}
</div>

<style>
  .browser-app {
    display: flex;
    flex-direction: column;
    height: 100%;
    overflow: hidden;
    background: #1a1a2a;
    color: #c0c0d0;
  }

  /* ---- URL bar ---- */
  .url-bar {
    display: flex;
    align-items: center;
    gap: 4px;
    padding: 6px 8px;
    background: #181825;
    border-bottom: 1px solid #2a2a3a;
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
    flex-shrink: 0;
  }

  .nav-btn:hover:not(:disabled) {
    background: rgba(255, 255, 255, 0.08);
  }

  .nav-btn:disabled {
    opacity: 0.3;
    cursor: not-allowed;
  }

  .url-input {
    flex: 1;
    padding: 6px 10px;
    background: #11111b;
    border: 1px solid #333;
    border-radius: 4px;
    color: #e0e0e0;
    font-size: 0.85rem;
    min-width: 0;
  }

  .url-input:focus {
    outline: none;
    border-color: #3b82f6;
  }

  .go-btn {
    padding: 6px 14px;
    background: rgba(59, 130, 246, 0.15);
    border: 1px solid rgba(59, 130, 246, 0.3);
    border-radius: 4px;
    color: #7eb8ff;
    cursor: pointer;
    font-size: 0.8rem;
    white-space: nowrap;
    transition: background 0.15s;
    flex-shrink: 0;
  }

  .go-btn:hover {
    background: rgba(59, 130, 246, 0.25);
  }

  /* ---- Loading bar ---- */
  .loading-bar {
    height: 2px;
    background: #1a1a2a;
    flex-shrink: 0;
    overflow: hidden;
  }

  .loading-progress {
    height: 100%;
    width: 30%;
    background: #3b82f6;
    animation: loading-slide 1.5s ease-in-out infinite;
  }

  @keyframes loading-slide {
    0% { transform: translateX(-100%); }
    50% { transform: translateX(200%); }
    100% { transform: translateX(-100%); }
  }

  /* ---- Error message ---- */
  .error-message {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 8px 12px;
    background: rgba(239, 68, 68, 0.1);
    border-bottom: 1px solid rgba(239, 68, 68, 0.2);
    color: #fca5a5;
    font-size: 0.8rem;
    flex-shrink: 0;
  }

  .error-icon {
    font-size: 1rem;
    flex-shrink: 0;
  }

  .error-text {
    flex: 1;
    min-width: 0;
  }

  .error-dismiss {
    width: 24px;
    height: 24px;
    display: flex;
    align-items: center;
    justify-content: center;
    background: transparent;
    border: none;
    border-radius: 4px;
    color: #f87171;
    cursor: pointer;
    font-size: 0.8rem;
    flex-shrink: 0;
  }

  .error-dismiss:hover {
    background: rgba(239, 68, 68, 0.2);
  }

  /* ---- iframe container ---- */
  .iframe-container {
    flex: 1;
    overflow: hidden;
    position: relative;
  }

  .browser-iframe {
    width: 100%;
    height: 100%;
    border: none;
    background: #fff;
    display: block;
  }

  /* ---- Empty state ---- */
  .empty-state {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 12px;
    flex: 1;
    color: #666;
    font-size: 0.9rem;
  }

  .empty-icon {
    font-size: 3rem;
    opacity: 0.4;
  }

  /* ---- Mobile responsive ---- */
  @media (max-width: 768px) {
    .url-bar {
      padding: 6px 6px;
      gap: 3px;
    }

    .nav-btn {
      width: 36px;
      height: 36px;
      min-width: 36px;
    }

    .url-input {
      font-size: 16px; /* Prevent iOS zoom */
      padding: 8px 8px;
    }

    .go-btn {
      padding: 8px 10px;
      min-height: 36px;
    }
  }
</style>
