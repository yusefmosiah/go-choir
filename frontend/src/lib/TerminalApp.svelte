<!--
  TerminalApp — ghostty-web terminal emulator rendered inside a floating window.

  Initializes ghostty-web WASM (once at module level), creates a Terminal instance
  with FitAddon, connects to /api/terminal/ws WebSocket, and renders in the
  parent container provided by FloatingWindow.

  Features:
    - Dark theme (#1a1b26 background, #a9b1d6 foreground)
    - Cursor blink enabled
    - 10000 line scrollback
    - Copy/paste via keyboard shortcuts (Cmd+C/V on macOS, Ctrl+Shift+C/V on Linux)
    - Responsive fit within window using FitAddon + ResizeObserver
    - Independent PTY sessions per window

  Props:
    windowId — unique window identifier for session management

  Data attributes for test targeting:
    data-terminal          — root container div
    data-terminal-canvas   — the canvas element (set by ghostty-web)
    data-terminal-error    — error message container
-->
<script>
  import { onMount, onDestroy } from 'svelte';
  import {
    createTerminalSession,
    updateTerminalSession,
    getTerminalSession,
    destroyTerminalSession,
    terminalSessions,
  } from './stores/terminal.js';

  export let windowId = '';

  // Reactive access to this session's error state
  $: session = $terminalSessions[windowId] || {};
  $: errorMessage = session.error;

  // ---- DOM refs ----
  let terminalContainer;

  // ---- ResizeObserver for responsive fit ----
  let resizeObserver = null;

  // ---- WASM init promise (module-level, initialized once) ----
  let wasmInitPromise = null;

  /**
   * Initialize ghostty-web WASM exactly once.
   * The init() function loads the ghostty-vt.wasm file.
   * We cache the promise so concurrent terminal windows don't re-init.
   */
  async function ensureWasmInit() {
    if (!wasmInitPromise) {
      const ghosttyWeb = await import('ghostty-web');
      wasmInitPromise = ghosttyWeb.init();
    }
    return wasmInitPromise;
  }

  /**
   * Detect macOS platform for keyboard shortcut handling.
   */
  function isMac() {
    return navigator.platform.toUpperCase().indexOf('MAC') >= 0 ||
           navigator.userAgent.toUpperCase().indexOf('MAC') >= 0;
  }

  /**
   * Initialize the terminal: create session, init WASM, create Terminal,
   * connect WebSocket, attach FitAddon + ResizeObserver.
   */
  async function initTerminal() {
    if (!terminalContainer) return;

    // Create session record
    createTerminalSession(windowId);

    try {
      // Initialize WASM (once globally)
      await ensureWasmInit();

      // Dynamic import for Terminal and FitAddon
      const ghosttyWeb = await import('ghostty-web');
      const { Terminal, FitAddon } = ghosttyWeb;

      // Create Terminal instance with dark theme
      const term = new Terminal({
        fontSize: 14,
        fontFamily: "'Menlo', 'Monaco', 'Courier New', monospace",
        cursorBlink: true,
        cursorStyle: 'block',
        scrollback: 10000,
        convertEol: false,
        theme: {
          background: '#1a1b26',
          foreground: '#a9b1d6',
          cursor: '#c0caf5',
          cursorAccent: '#1a1b26',
          selectionBackground: 'rgba(122, 158, 212, 0.3)',
          selectionForeground: '#c0caf5',
          black: '#15161e',
          red: '#f7768e',
          green: '#9ece6a',
          yellow: '#e0af68',
          blue: '#7aa2f7',
          magenta: '#bb9af7',
          cyan: '#7dcfff',
          white: '#a9b1d6',
          brightBlack: '#414868',
          brightRed: '#f7768e',
          brightGreen: '#9ece6a',
          brightYellow: '#e0af68',
          brightBlue: '#7aa2f7',
          brightMagenta: '#bb9af7',
          brightCyan: '#7dcfff',
          brightWhite: '#c0caf5',
        },
      });

      // Create FitAddon
      const fitAddon = new FitAddon();
      term.loadAddon(fitAddon);

      // Open terminal in container
      term.open(terminalContainer);

      // Add data-test attribute to the canvas element
      const canvas = terminalContainer.querySelector('canvas');
      if (canvas) {
        canvas.setAttribute('data-terminal-canvas', '');
      }

      // Fit to container
      fitAddon.fit();

      // Connect WebSocket to PTY backend
      const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
      const wsUrl = `${protocol}//${window.location.host}/api/terminal/ws`;
      const ws = new WebSocket(wsUrl);

      // Protocol uses text-based JSON messages (not binary).
      // Do NOT set ws.binaryType = 'arraybuffer'.

      ws.onopen = () => {
        // Send initial resize so backend PTY knows our size
        ws.send(JSON.stringify({
          type: 'resize',
          cols: term.cols,
          rows: term.rows,
        }));
      };

      // PTY output -> terminal write
      // Backend sends JSON: {type: "output", data: "..."} or {type: "error", data: "..."}
      ws.onmessage = (event) => {
        try {
          const msg = JSON.parse(event.data);
          if (msg.type === 'output' && typeof msg.data === 'string') {
            term.write(msg.data);
          } else if (msg.type === 'error') {
            updateTerminalSession(windowId, {
              error: msg.data || 'Unknown server error',
            });
          }
        } catch (_e) {
          // Fallback: if message is not JSON, write raw text
          term.write(event.data);
        }
      };

      ws.onerror = () => {
        updateTerminalSession(windowId, {
          error: 'WebSocket connection error',
        });
      };

      ws.onclose = (event) => {
        if (event.code !== 1000) {
          updateTerminalSession(windowId, {
            error: `WebSocket closed (code ${event.code})`,
          });
        }
      };

      // Terminal input -> WebSocket send (JSON protocol)
      term.onData((data) => {
        if (ws.readyState === WebSocket.OPEN) {
          ws.send(JSON.stringify({
            type: 'input',
            data: data,
          }));
        }
      });

      // On resize, inform the backend PTY
      term.onResize(({ cols, rows }) => {
        if (ws.readyState === WebSocket.OPEN) {
          ws.send(JSON.stringify({
            type: 'resize',
            cols,
            rows,
          }));
        }
      });

      // Enable automatic fitting on container resize
      fitAddon.observeResize();

      // Store references in session
      updateTerminalSession(windowId, {
        term,
        fitAddon,
        ws,
        initialized: true,
        error: null,
      });

    } catch (err) {
      console.error('[TerminalApp] Failed to initialize:', err);
      updateTerminalSession(windowId, {
        error: `Initialization failed: ${err.message}`,
      });
    }
  }

  /**
   * Clean up terminal session on component destroy.
   */
  function cleanup() {
    // Disconnect ResizeObserver
    if (resizeObserver) {
      resizeObserver.disconnect();
      resizeObserver = null;
    }
    // Destroy session (disposes terminal, closes WebSocket)
    destroyTerminalSession(windowId);
  }

  onMount(() => {
    initTerminal();
  });

  onDestroy(() => {
    cleanup();
  });
</script>

<div
  class="terminal-wrapper"
  bind:this={terminalContainer}
  data-terminal
>
  <!-- ghostty-web renders its canvas here via term.open() -->

  <!-- Error display overlay -->
  {#if errorMessage}
    <div class="terminal-error" data-terminal-error>
      <div class="terminal-error-content">
        <span class="terminal-error-icon">⚠</span>
        <span class="terminal-error-text">{errorMessage}</span>
      </div>
    </div>
  {/if}
</div>

<style>
  .terminal-wrapper {
    width: 100%;
    height: 100%;
    background: #1a1b26;
    overflow: hidden;
    position: relative;
  }

  /* Ensure ghostty-web canvas fills the container */
  .terminal-wrapper :global(canvas) {
    display: block;
  }

  /* Error state overlay */
  .terminal-error {
    position: absolute;
    top: 0;
    left: 0;
    right: 0;
    bottom: 0;
    display: flex;
    align-items: center;
    justify-content: center;
    background: rgba(26, 27, 38, 0.92);
    color: #f7768e;
    font-family: monospace;
    font-size: 0.85rem;
    padding: 1rem;
    text-align: center;
    z-index: 10;
  }

  .terminal-error-content {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 0.5rem;
    max-width: 320px;
  }

  .terminal-error-icon {
    font-size: 1.5rem;
  }

  .terminal-error-text {
    word-break: break-word;
  }
</style>
