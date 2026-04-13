/**
 * Desktop stores for window management in the go-choir desktop shell.
 *
 * Provides Svelte writable stores for:
 *   - windows: array of open window objects
 *   - activeWindowId: currently focused window ID
 *   - nextZIndex: next z-index counter for window stacking
 *   - liveStatus: WebSocket connection status
 *   - iconPositions: positions of floating desktop icons
 *   - showDesktopMode: whether all windows are minimized (show desktop)
 *   - selectedIconId: currently selected desktop icon (single-click)
 *
 * Each window object has:
 *   { windowId, appId, title, icon, x, y, width, height, mode, zIndex, restoredGeometry, appContext }
 *
 * App registry defines the 4 hardcoded apps with their icons.
 */

import { writable, derived } from 'svelte/store';

// ---- App registry ----

export const APP_REGISTRY = [
  { id: 'files', name: 'Files', icon: '📁', description: 'File Browser' },
  { id: 'browser', name: 'Browser', icon: '🌐', description: 'Web Browser' },
  { id: 'terminal', name: 'Terminal', icon: '💻', description: 'Terminal' },
  { id: 'settings', name: 'Settings', icon: '⚙️', description: 'Settings' },
  { id: 'etext', name: 'E-Text', icon: '📝', description: 'Document Editor' },
];

/** The 4 main apps shown as floating desktop icons */
export const DESKTOP_ICON_APPS = APP_REGISTRY.filter((app) =>
  ['files', 'browser', 'terminal', 'settings'].includes(app.id)
);

// ---- Window counter ----

let windowCounter = 0;

function generateWindowId() {
  windowCounter++;
  return `win-${Date.now()}-${windowCounter}`;
}

// ---- Default icon grid positions ----

/** Default grid positions for floating desktop icons (column layout, left side) */
export function getDefaultIconPositions() {
  const positions = {};
  const startX = 32;
  const startY = 32;
  const colWidth = 100;
  const rowHeight = 90;
  DESKTOP_ICON_APPS.forEach((app, i) => {
    positions[app.id] = { x: startX, y: startY + i * rowHeight };
  });
  return positions;
}

// ---- Stores ----

/** @type {import('svelte/store').Writable<Array>} */
export const windows = writable([]);

/** @type {import('svelte/store').Writable<string>} */
export const activeWindowId = writable('');

/** @type {import('svelte/store').Writable<number>} */
export const nextZIndex = writable(1);

/** @type {import('svelte/store').Writable<string>} */
export const liveStatus = writable('disconnected');

/** @type {import('svelte/store').Writable<Object>} */
export const iconPositions = writable(getDefaultIconPositions());

/** @type {import('svelte/store').Writable<boolean>} */
export const showDesktopMode = writable(false);

/** @type {import('svelte/store').Writable<string>} */
export const selectedIconId = writable('');

// ---- Derived stores ----

/** Minimized windows (shown in bottom bar) */
export const minimizedWindows = derived(windows, ($windows) =>
  $windows.filter((w) => w.mode === 'minimized')
);

/** Visible (non-closed, non-minimized, non-hidden) windows */
export const visibleWindows = derived(windows, ($windows) =>
  $windows.filter((w) => w.mode !== 'closed' && w.mode !== 'minimized' && w.mode !== 'hidden')
);

// ---- Store actions ----

/**
 * Open an app window (single-instance per appId).
 * If the app is already open, focuses/restores it instead.
 */
export function openApp(appId, appName, icon) {
  windows.update(($windows) => {
    const existing = $windows.find((w) => w.appId === appId && w.mode !== 'closed');
    if (existing) {
      // Focus existing window
      activeWindowId.set(existing.windowId);
      let updated = $windows.map((w) =>
        w.windowId === existing.windowId
          ? { ...w, zIndex: getNextZIndex(), mode: w.mode === 'minimized' ? 'normal' : w.mode }
          : w
      );
      // If it was minimized, restore its geometry
      if (existing.mode === 'minimized' && existing.restoredGeometry) {
        const geo = existing.restoredGeometry;
        updated = updated.map((w) =>
          w.windowId === existing.windowId
            ? { ...w, x: geo.x, y: geo.y, width: geo.width, height: geo.height, restoredGeometry: null }
            : w
        );
      }
      return updated;
    }

    // New window with cascade positioning
    const windowId = generateWindowId();
    const openCount = $windows.filter((w) => w.mode !== 'closed').length;
    const offset = (openCount % 8) * 30;
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
      zIndex: getNextZIndex(),
      restoredGeometry: null,
      appContext: {},
    };

    activeWindowId.set(windowId);
    return [...$windows, newWindow];
  });
}

/** Close a window by ID */
export function closeWindow(windowId) {
  windows.update(($windows) => {
    const remaining = $windows.filter((w) => w.windowId !== windowId);
    // Update active window
    activeWindowId.update(($activeId) => {
      if ($activeId === windowId) {
        const visible = remaining.filter((w) => w.mode !== 'closed');
        if (visible.length > 0) {
          return visible.reduce((a, b) => (a.zIndex > b.zIndex ? a : b)).windowId;
        }
        return '';
      }
      return $activeId;
    });
    return remaining;
  });
}

/** Focus a window (bring to top z-index) */
export function focusWindow(windowId) {
  activeWindowId.set(windowId);
  windows.update(($windows) =>
    $windows.map((w) =>
      w.windowId === windowId ? { ...w, zIndex: getNextZIndex() } : w
    )
  );
}

/** Minimize a window */
export function minimizeWindow(windowId) {
  windows.update(($windows) => {
    const updated = $windows.map((w) =>
      w.windowId === windowId ? { ...w, mode: 'minimized' } : w
    );
    // Transfer focus to next visible window
    activeWindowId.update(($activeId) => {
      if ($activeId === windowId) {
        const visible = updated.filter((w) => w.mode === 'normal' || w.mode === 'maximized');
        if (visible.length > 0) {
          return visible.reduce((a, b) => (a.zIndex > b.zIndex ? a : b)).windowId;
        }
        return '';
      }
      return $activeId;
    });
    return updated;
  });
}

/** Maximize a window */
export function maximizeWindow(windowId) {
  windows.update(($windows) =>
    $windows.map((w) => {
      if (w.windowId === windowId) {
        return {
          ...w,
          mode: 'maximized',
          restoredGeometry: { x: w.x, y: w.y, width: w.width, height: w.height },
        };
      }
      return w;
    })
  );
  activeWindowId.set(windowId);
}

/** Restore a window from minimized or maximized */
export function restoreWindow(windowId) {
  windows.update(($windows) =>
    $windows.map((w) => {
      if (w.windowId === windowId) {
        if (w.mode === 'minimized' && w.restoredGeometry) {
          const geo = w.restoredGeometry;
          return {
            ...w,
            mode: 'normal',
            x: geo.x,
            y: geo.y,
            width: geo.width,
            height: geo.height,
            restoredGeometry: null,
          };
        }
        if (w.mode === 'maximized' && w.restoredGeometry) {
          const geo = w.restoredGeometry;
          return {
            ...w,
            mode: 'normal',
            x: geo.x,
            y: geo.y,
            width: geo.width,
            height: geo.height,
            restoredGeometry: null,
          };
        }
        return { ...w, mode: 'normal', restoredGeometry: null };
      }
      return w;
    })
  );
  activeWindowId.set(windowId);
}

/** Move a window */
export function moveWindow(windowId, x, y) {
  windows.update(($windows) =>
    $windows.map((w) => (w.windowId === windowId ? { ...w, x, y } : w))
  );
}

/** Resize a window */
export function resizeWindow(windowId, x, y, width, height) {
  windows.update(($windows) =>
    $windows.map((w) => (w.windowId === windowId ? { ...w, x, y, width, height } : w))
  );
}

/** Hide a window (used for mobile single-focus mode — keeps window in state but hides it visually) */
export function hideWindow(windowId) {
  windows.update(($windows) => {
    const updated = $windows.map((w) =>
      w.windowId === windowId
        ? { ...w, mode: 'hidden', _prevMode: w.mode === 'hidden' ? (w._prevMode || 'normal') : w.mode }
        : w
    );
    // Transfer active to next visible window
    const visible = updated.filter((w) => w.mode !== 'closed' && w.mode !== 'hidden');
    activeWindowId.update(($activeId) => {
      if ($activeId === windowId) {
        if (visible.length > 0) {
          return visible.reduce((a, b) => (a.zIndex > b.zIndex ? a : b)).windowId;
        }
        return '';
      }
      return $activeId;
    });
    return updated;
  });
}

/** Show a previously hidden window (mobile single-focus mode) */
export function showWindow(windowId) {
  windows.update(($windows) =>
    $windows.map((w) =>
      w.windowId === windowId
        ? { ...w, mode: w._prevMode || 'normal', _prevMode: null }
        : w
    )
  );
  activeWindowId.set(windowId);
}

/** Set windows state (used for loading from server) */
export function setWindows(newWindows, newActiveId) {
  windows.set(newWindows);
  activeWindowId.set(newActiveId || '');
  if (newWindows.length > 0) {
    const maxZ = Math.max(...newWindows.map((w) => w.zIndex || 1));
    nextZIndex.set(maxZ + 1);
  }
}

// ---- Icon position actions ----

/** Move a desktop icon to a new position */
export function moveIcon(appId, x, y) {
  iconPositions.update((positions) => ({
    ...positions,
    [appId]: { x, y },
  }));
}

/** Set icon positions (used for loading from server) */
export function setIconPositions(positions) {
  if (positions && Object.keys(positions).length > 0) {
    iconPositions.set(positions);
  } else {
    iconPositions.set(getDefaultIconPositions());
  }
}

// ---- Show Desktop actions ----

/** Toggle show desktop mode (minimize/restore all windows) */
export function toggleShowDesktop() {
  let currentShowDesktop;
  showDesktopMode.subscribe((v) => { currentShowDesktop = v; })();

  if (currentShowDesktop) {
    // Restore all windows that were minimized by show desktop
    windows.update(($windows) =>
      $windows.map((w) => {
        if (w._showDesktopMinimized) {
          const { _showDesktopMinimized, _showDesktopPrevMode, ...rest } = w;
          return { ...rest, mode: _showDesktopPrevMode || 'normal' };
        }
        return w;
      })
    );
    showDesktopMode.set(false);
  } else {
    // Minimize all visible windows and remember their previous mode
    windows.update(($windows) =>
      $windows.map((w) => {
        if (w.mode !== 'closed' && w.mode !== 'hidden' && w.mode !== 'minimized') {
          return { ...w, _showDesktopMinimized: true, _showDesktopPrevMode: w.mode, mode: 'minimized' };
        }
        return w;
      })
    );
    showDesktopMode.set(true);
  }
}

// ---- Internal helpers ----

function getNextZIndex() {
  let next;
  nextZIndex.update((n) => {
    next = n;
    return n + 1;
  });
  return next;
}
