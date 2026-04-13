<!--
  FloatingWindow — simplified desktop window with bottom-right resize only.

  Rewrites Window.svelte with:
    - Title bar drag (no drag on buttons)
    - Single resize handle at bottom-right corner (no 8-handle system)
    - Minimum dimensions: width >= 200px, height >= 120px
    - Maximized fills desktop area excluding left rail and bottom bar
    - Maximize button icon changes to restore icon when maximized
    - Restore returns to pre-maximize geometry
    - Minimize hides window, shows indicator in bottom bar
    - Restore from minimized returns to pre-minimize geometry
    - Clicking window brings it to front (z-index management)
    - Active window has blue border (#3b82f6) and enhanced shadow
    - Cascade positioning: 30px offset per window, wraps after 8
    - Window close transfers focus to next highest z-index window

  Data attributes for test targeting:
    data-window          — root container
    data-window-id       — window identifier
    data-window-titlebar — title bar for drag and window controls
    data-window-close    — close button
    data-window-minimize — minimize button
    data-window-maximize — maximize/restore button
    data-window-content  — content area hosting the app
    data-resize-handle   — bottom-right resize handle (se only)
-->
<script>
  import { createEventDispatcher } from 'svelte';
  import { onMount, onDestroy } from 'svelte';

  export let windowId = '';
  export let appId = '';
  export let title = 'Window';
  export let x = 100;
  export let y = 50;
  export let width = 600;
  export let height = 400;
  export let mode = 'normal'; // 'normal' | 'minimized' | 'maximized'
  export let zIndex = 1;
  export let active = false;
  export let restoredGeometry = null;
  export let isMobile = false;

  // Suppress unused-export warnings — props used by parent for persistence
  $: _appId = appId;
  $: _restoredGeo = restoredGeometry;

  const dispatch = createEventDispatcher();

  // ---- Constants ----
  const MIN_WIDTH = 200;
  const MIN_HEIGHT = 120;

  // ---- Drag state ----
  let dragging = false;
  let dragOffsetX = 0;
  let dragOffsetY = 0;

  // ---- Resize state ----
  let resizing = false;
  let resizeStartX = 0;
  let resizeStartY = 0;
  let resizeStartWidth = 0;
  let resizeStartHeight = 0;

  // ---- Window control handlers ----

  function handleClose() {
    dispatch('close', { windowId });
  }

  function handleMinimize() {
    dispatch('minimize', { windowId });
  }

  function handleMaximizeRestore() {
    if (mode === 'maximized') {
      dispatch('restore', { windowId });
    } else {
      dispatch('maximize', { windowId });
    }
  }

  // ---- Focus handler ----

  function handleFocusWindow() {
    if (!active) {
      dispatch('focus', { windowId });
    }
  }

  // ---- Drag handlers (title bar only) ----

  function handleDragStart(event) {
    if (event.button !== 0) return;
    if (event.target.closest('button')) return;
    if (mode === 'maximized') return;
    if (isMobile) return;

    dragging = true;
    dragOffsetX = event.clientX - x;
    dragOffsetY = event.clientY - y;

    handleFocusWindow();
    event.preventDefault();
  }

  function handleDragMove(event) {
    if (!dragging) return;
    const newX = event.clientX - dragOffsetX;
    const newY = event.clientY - dragOffsetY;
    dispatch('move', { windowId, x: newX, y: newY });
  }

  function handleDragEnd() {
    dragging = false;
  }

  // ---- Resize handler (bottom-right handle only) ----

  function handleResizeStart(event) {
    if (mode !== 'normal') return;
    if (event.button !== 0) return;

    resizing = true;
    resizeStartX = event.clientX;
    resizeStartY = event.clientY;
    resizeStartWidth = width;
    resizeStartHeight = height;

    handleFocusWindow();
    event.preventDefault();
    event.stopPropagation();
  }

  function handleResizeMove(event) {
    if (!resizing) return;

    const dx = event.clientX - resizeStartX;
    const dy = event.clientY - resizeStartY;

    const newWidth = Math.max(MIN_WIDTH, resizeStartWidth + dx);
    const newHeight = Math.max(MIN_HEIGHT, resizeStartHeight + dy);

    dispatch('resize', { windowId, x, y, width: newWidth, height: newHeight });
  }

  function handleResizeEnd() {
    resizing = false;
  }

  // ---- Global mouse event wiring ----

  onMount(() => {
    window.addEventListener('mousemove', handleDragMove);
    window.addEventListener('mouseup', handleDragEnd);
    window.addEventListener('mousemove', handleResizeMove);
    window.addEventListener('mouseup', handleResizeEnd);
  });

  onDestroy(() => {
    window.removeEventListener('mousemove', handleDragMove);
    window.removeEventListener('mouseup', handleDragEnd);
    window.removeEventListener('mousemove', handleResizeMove);
    window.removeEventListener('mouseup', handleResizeEnd);
  });

  // ---- Computed styles ----

  $: windowStyle = mode === 'maximized'
    ? 'left:0; top:0; width:100%; height:calc(100%);'
    : mode === 'minimized'
    ? 'display:none;'
    : isMobile
    ? 'left:0; top:0; width:100%; height:calc(100%);'
    : `left:${x}px; top:${y}px; width:${width}px; height:${height}px;`;

  $: maxRestoreIcon = mode === 'maximized' ? '❐' : '☐';
  $: maxRestoreTitle = mode === 'maximized' ? 'Restore' : 'Maximize';

  // Only show resize handle in normal mode AND not on mobile
  $: showResizeHandle = mode === 'normal' && !isMobile;
</script>

<!-- svelte-ignore a11y-click-events-have-key-events -->
<!-- svelte-ignore a11y-no-static-element-interactions -->
<div
  class="window {active ? 'window-active' : ''} {isMobile ? 'window-mobile' : ''}"
  style="{windowStyle} z-index: {zIndex};"
  data-window
  data-window-id={windowId}
  on:mousedown={handleFocusWindow}
>
  <!-- Title bar -->
  <div
    class="titlebar"
    data-window-titlebar
    on:mousedown={handleDragStart}
  >
    <span class="title-text">{title}</span>
    <div class="window-controls">
      <button
        class="ctrl-btn minimize-btn"
        data-window-minimize
        on:click|stopPropagation={handleMinimize}
        title="Minimize"
        aria-label="Minimize"
      >—</button>
      <button
        class="ctrl-btn maximize-btn"
        data-window-maximize
        on:click|stopPropagation={handleMaximizeRestore}
        title={maxRestoreTitle}
        aria-label={maxRestoreTitle}
      >{maxRestoreIcon}</button>
      <button
        class="ctrl-btn close-btn"
        data-window-close
        on:click|stopPropagation={handleClose}
        title="Close"
        aria-label="Close"
      >✕</button>
    </div>
  </div>

  <!-- Content area -->
  <div class="window-content" data-window-content>
    <slot />
  </div>

  <!-- Resize handle: bottom-right corner only (normal mode, not mobile) -->
  {#if showResizeHandle}
    <div
      class="resize-handle resize-se"
      data-resize-handle
      on:mousedown|stopPropagation={handleResizeStart}
    ></div>
  {/if}
</div>

<style>
  .window {
    position: absolute;
    display: flex;
    flex-direction: column;
    background: #1e1e2e;
    border: 1px solid #333;
    border-radius: 8px;
    overflow: hidden;
    box-shadow: 0 4px 20px rgba(0, 0, 0, 0.4);
    transition: box-shadow 0.15s, border-color 0.15s;
    user-select: none;
  }

  .window-active {
    border-color: #3b82f6;
    box-shadow: 0 4px 24px rgba(59, 130, 246, 0.25), 0 0 0 1px rgba(59, 130, 246, 0.3);
  }

  /* ---- Title bar ---- */
  .titlebar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 0 0.5rem 0 0.75rem;
    height: 36px;
    min-height: 36px;
    background: #181825;
    border-bottom: 1px solid #2a2a3a;
    cursor: default;
    flex-shrink: 0;
  }

  .title-text {
    font-size: 0.8rem;
    font-weight: 600;
    color: #c0c0d0;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    flex: 1;
  }

  .window-controls {
    display: flex;
    align-items: center;
    gap: 2px;
    flex-shrink: 0;
  }

  .ctrl-btn {
    width: 28px;
    height: 28px;
    display: flex;
    align-items: center;
    justify-content: center;
    background: transparent;
    border: none;
    border-radius: 4px;
    font-size: 0.7rem;
    cursor: pointer;
    color: #888;
    transition: background 0.15s, color 0.15s;
  }

  .ctrl-btn:hover {
    background: rgba(255, 255, 255, 0.1);
    color: #ddd;
  }

  .close-btn:hover {
    background: rgba(239, 68, 68, 0.3);
    color: #f87171;
  }

  /* ---- Content area ---- */
  .window-content {
    flex: 1;
    overflow: auto;
    position: relative;
  }

  /* ---- Resize handle: bottom-right corner only ---- */
  .resize-handle {
    position: absolute;
    z-index: 10;
  }

  .resize-se {
    bottom: 0;
    right: 0;
    width: 16px;
    height: 16px;
    cursor: se-resize;
  }

  /* Subtle visual indicator for the resize handle */
  .resize-se::after {
    content: '';
    position: absolute;
    bottom: 3px;
    right: 3px;
    width: 8px;
    height: 8px;
    border-right: 2px solid rgba(255, 255, 255, 0.2);
    border-bottom: 2px solid rgba(255, 255, 255, 0.2);
  }

  /* Mobile: window is full-width, no drag, no resize, no border-radius */
  :global(.window-mobile) {
    border-radius: 0 !important;
    width: 100% !important;
    left: 0 !important;
    top: 0 !important;
  }

  :global(.window-mobile .titlebar) {
    cursor: default;
  }

  /* Tablet: max-width constraint so windows don't exceed viewport */
  @media (max-width: 1024px) and (min-width: 769px) {
    .window {
      max-width: calc(100vw - 56px - 20px);
    }
  }

  /* Mobile: full-width window styling via CSS media query (backup for JS class) */
  @media (max-width: 768px) {
    .window {
      max-width: 100vw;
      width: 100% !important;
      left: 0 !important;
      top: 0 !important;
      border-radius: 0;
      box-shadow: none;
      border: none;
      border-top: 1px solid #2a2a3a;
    }

    .window-active {
      border-color: transparent;
      box-shadow: none;
      border-top: 1px solid #3b82f6;
    }

    .window .titlebar {
      cursor: default;
    }
  }
</style>
