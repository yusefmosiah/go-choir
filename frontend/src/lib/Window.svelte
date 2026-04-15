<!--
  Window — desktop window component with full lifecycle support.

  Supports:
    - Title bar with close, minimize, maximize/restore buttons
    - Drag via title bar to move window position
    - Resize via edge/corner handles
    - Window modes: normal, minimized (taskbar), maximized (full desktop)
    - Focus/z-order: clicking anywhere in the window brings it to front
    - State preservation across mode transitions

  Data attributes for test targeting:
    data-window          — root container
    data-window-id        — window identifier
    data-window-titlebar  — title bar for drag and window controls
    data-window-close     — close button
    data-window-minimize  — minimize button
    data-window-maximize  — maximize/restore button
    data-window-content   — content area hosting the app
-->
<script>
  import { createEventDispatcher } from 'svelte';

  export let windowId = '';
  export let appId = ''; // used externally for data-window-id attribute mapping
  export let title = 'Window';
  export let x = 100;
  export let y = 50;
  export let width = 600;
  export let height = 400;
  export let mode = 'normal'; // 'normal' | 'minimized' | 'maximized'
  export let zIndex = 1;
  export let active = false;
  export let restoredGeometry = null; // geometry before maximize, used by parent

  // Reactive reference to suppress unused-export warnings — these props
  // are used by the parent component for data-attribute mapping and state
  // persistence, not directly in the template.
  $: _appId = appId;
  $: _restoredGeo = restoredGeometry;

  const dispatch = createEventDispatcher();

  // ---- Drag state ----
  let dragging = false;
  let dragOffsetX = 0;
  let dragOffsetY = 0;

  // ---- Resize state ----
  let resizing = false;
  let resizeDir = '';
  let resizeStartX = 0;
  let resizeStartY = 0;
  let resizeStartWidth = 0;
  let resizeStartHeight = 0;
  let resizeStartLeft = 0;
  let resizeStartTop = 0;

  const MIN_WIDTH = 200;
  const MIN_HEIGHT = 120;

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
    // Only drag on left mouse button, not on buttons
    if (event.button !== 0) return;
    if (event.target.closest('button')) return;
    if (mode === 'maximized') return; // can't drag maximized window

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

  // ---- Resize handlers (edge/corner handles) ----

  function handleResizeStart(event, dir) {
    if (mode === 'maximized') return;
    if (event.button !== 0) return;

    resizing = true;
    resizeDir = dir;
    resizeStartX = event.clientX;
    resizeStartY = event.clientY;
    resizeStartWidth = width;
    resizeStartHeight = height;
    resizeStartLeft = x;
    resizeStartTop = y;

    handleFocusWindow();
    event.preventDefault();
    event.stopPropagation();
  }

  function handleResizeMove(event) {
    if (!resizing) return;

    const dx = event.clientX - resizeStartX;
    const dy = event.clientY - resizeStartY;

    let newWidth = resizeStartWidth;
    let newHeight = resizeStartHeight;
    let newX = resizeStartLeft;
    let newY = resizeStartTop;

    if (resizeDir.includes('e')) {
      newWidth = Math.max(MIN_WIDTH, resizeStartWidth + dx);
    }
    if (resizeDir.includes('w')) {
      newWidth = Math.max(MIN_WIDTH, resizeStartWidth - dx);
      if (newWidth > MIN_WIDTH) {
        newX = resizeStartLeft + dx;
      }
    }
    if (resizeDir.includes('s')) {
      newHeight = Math.max(MIN_HEIGHT, resizeStartHeight + dy);
    }
    if (resizeDir.includes('n')) {
      newHeight = Math.max(MIN_HEIGHT, resizeStartHeight - dy);
      if (newHeight > MIN_HEIGHT) {
        newY = resizeStartTop + dy;
      }
    }

    dispatch('resize', { windowId, x: newX, y: newY, width: newWidth, height: newHeight });
  }

  function handleResizeEnd() {
    resizing = false;
  }

  // ---- Global mouse event wiring ----

  import { onMount } from 'svelte';
  import { onDestroy } from 'svelte';

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
    ? 'left:0; top:0; width:100%; height:calc(100% - 40px);'
    : mode === 'minimized'
    ? 'display:none;'
    : `left:${x}px; top:${y}px; width:${width}px; height:${height}px;`;

  $: maxRestoreIcon = mode === 'maximized' ? '❐' : '☐';
  $: maxRestoreTitle = mode === 'maximized' ? 'Restore' : 'Maximize';
</script>

<!-- svelte-ignore a11y-click-events-have-key-events -->
<!-- svelte-ignore a11y-no-static-element-interactions -->
<div
  class="window {active ? 'window-active' : ''}"
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
    <span class="titlvtext">{title}</span>
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

  <!-- Resize handles (only in normal mode) -->
  {#if mode === 'normal'}
    <div class="resize-handle resize-n" on:mousedown|stopPropagation={(e) => handleResizeStart(e, 'n')}></div>
    <div class="resize-handle resize-s" on:mousedown|stopPropagation={(e) => handleResizeStart(e, 's')}></div>
    <div class="resize-handle resize-e" on:mousedown|stopPropagation={(e) => handleResizeStart(e, 'e')}></div>
    <div class="resize-handle resize-w" on:mousedown|stopPropagation={(e) => handleResizeStart(e, 'w')}></div>
    <div class="resize-handle resize-ne" on:mousedown|stopPropagation={(e) => handleResizeStart(e, 'ne')}></div>
    <div class="resize-handle resize-nw" on:mousedown|stopPropagation={(e) => handleResizeStart(e, 'nw')}></div>
    <div class="resize-handle resize-se" on:mousedown|stopPropagation={(e) => handleResizeStart(e, 'se')}></div>
    <div class="resize-handle resize-sw" on:mousedown|stopPropagation={(e) => handleResizeStart(e, 'sw')}></div>
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
    transition: box-shadow 0.15s;
    user-select: none;
  }

  .window-active {
    border-color: #3b82f6;
    box-shadow: 0 4px 24px rgba(59, 130, 246, 0.25);
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

  .titlvtext {
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

  /* ---- Resize handles ---- */
  .resize-handle {
    position: absolute;
    z-index: 10;
  }

  .resize-n {
    top: -3px; left: 8px; right: 8px; height: 6px;
    cursor: n-resize;
  }
  .resize-s {
    bottom: -3px; left: 8px; right: 8px; height: 6px;
    cursor: s-resize;
  }
  .resize-e {
    top: 8px; right: -3px; bottom: 8px; width: 6px;
    cursor: e-resize;
  }
  .resize-w {
    top: 8px; left: -3px; bottom: 8px; width: 6px;
    cursor: w-resize;
  }
  .resize-ne {
    top: -3px; right: -3px; width: 12px; height: 12px;
    cursor: ne-resize;
  }
  .resize-nw {
    top: -3px; left: -3px; width: 12px; height: 12px;
    cursor: nw-resize;
  }
  .resize-se {
    bottom: -3px; right: -3px; width: 12px; height: 12px;
    cursor: se-resize;
  }
  .resize-sw {
    bottom: -3px; left: -3px; width: 12px; height: 12px;
    cursor: sw-resize;
  }
</style>
