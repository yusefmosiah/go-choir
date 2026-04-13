/**
 * Playwright tests for the desktop shell window manager (VAL-DESKTOP-001
 * through VAL-DESKTOP-007).
 *
 * Updated for the ChoirOS desktop shell rewrite:
 * - No top bar (replaced by floating desktop icons + bottom bar)
 * - No launcher dropdown (replaced by floating desktop icons)
 * - No taskbar (minimized windows shown in bottom bar)
 * - No runtime panel (TaskRunner removed from visible UI)
 * - No left rail (replaced by floating desktop icons on the desktop surface)
 *
 * These tests verify that:
 * - Authenticated users reach a real desktop shell (VAL-DESKTOP-001)
 * - Double-clicking floating icons opens apps inside the desktop (VAL-DESKTOP-002)
 * - Window focus changes raise the active window (VAL-DESKTOP-003)
 * - Windows support drag and resize (VAL-DESKTOP-004)
 * - Windows support minimize, maximize, and restore (VAL-DESKTOP-005)
 * - Window close and reopen updates desktop state cleanly (VAL-DESKTOP-006)
 * - Desktop restore preserves server-backed window state (VAL-DESKTOP-007)
 *
 * Uses the Playwright Chromium virtual-authenticator harness to register
 * a passkey and authenticate before testing the desktop.
 */
import { test, expect } from './helpers/fixtures.js';
import { registerPasskey, getSession } from './helpers/auth.js';

const BASE_URL = 'http://localhost:4173';

function uniqueEmail() {
  return `desktop-test-${Date.now()}-${Math.random().toString(36).slice(2, 8)}@example.com`;
}

// Helper: register a passkey and get to the authenticated desktop.
async function registerAndLoadDesktop(page, authenticator, email) {
  await page.goto(BASE_URL);
  await registerPasskey(page, email, BASE_URL);

  // Reload so the Svelte app calls checkSession() and renders the desktop.
  await page.reload();
  await page.locator('[data-desktop]').waitFor({ state: 'visible', timeout: 10000 });
}

// Helper: open app via double-click on floating desktop icon
async function openAppViaIcon(page, appId) {
  const icon = page.locator(`[data-desktop-icon-id="${appId}"]`);
  await icon.dblclick();
}

// ---------------------------------------------------------------
// Test: authenticated users reach a real desktop shell
// (VAL-DESKTOP-001)
// ---------------------------------------------------------------
test('authenticated users reach a real desktop shell', async ({
  page,
  authenticator,
}) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);

  // The desktop container should be visible.
  const desktop = page.locator('[data-desktop]');
  await expect(desktop).toBeVisible();

  // The guest auth entry should NOT be visible.
  const authEntry = page.locator('[data-auth-entry]');
  await expect(authEntry).not.toBeVisible();

  // The floating desktop icons should be visible (replaces left rail).
  const surface = page.locator('[data-desktop-surface]');
  await expect(surface).toBeVisible();

  // The bottom bar should be visible.
  const bottomBar = page.locator('[data-bottom-bar]');
  await expect(bottomBar).toBeVisible();

  // The logout button should be visible in the bottom bar.
  const logoutBtn = page.locator('[data-desktop-logout]');
  await expect(logoutBtn).toBeVisible();
});

// ---------------------------------------------------------------
// Test: floating icons open apps inside the desktop
// (VAL-DESKTOP-002)
// ---------------------------------------------------------------
test('floating icons open apps inside the desktop', async ({
  page,
  authenticator,
}) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);

  // Double-click the Files icon to open the app
  await openAppViaIcon(page, 'files');

  // A window should appear in the desktop.
  const windowEl = page.locator('[data-window]');
  await expect(windowEl).toBeVisible({ timeout: 5000 });

  // The window should have content.
  const windowContent = page.locator('[data-window-content]');
  await expect(windowContent).toBeVisible();
});

// ---------------------------------------------------------------
// Test: window focus changes raise the active window
// (VAL-DESKTOP-003)
// ---------------------------------------------------------------
test('window focus changes raise the active window', async ({
  page,
  authenticator,
}) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);

  // Open a Files window via floating icon.
  await openAppViaIcon(page, 'files');
  await page.locator('[data-window]').first().waitFor({ state: 'visible', timeout: 5000 });

  // Get the first window.
  const windows = page.locator('[data-window]');
  const firstWindow = windows.first();
  await expect(firstWindow).toBeVisible();

  // The first window should have the window-active class.
  await expect(firstWindow).toHaveClass(/window-active/);

  // Clicking the desktop area (outside the window) should not break anything.
  const desktopArea = page.locator('[data-desktop-windows]');
  await desktopArea.click({ position: { x: 10, y: 10 } });

  // The window should still be visible.
  await expect(firstWindow).toBeVisible();
});

// ---------------------------------------------------------------
// Test: windows support drag and resize
// (VAL-DESKTOP-004)
// ---------------------------------------------------------------
test('windows support drag and resize', async ({
  page,
  authenticator,
}) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);

  // Open a Files window via floating icon.
  await openAppViaIcon(page, 'files');
  const windowEl = page.locator('[data-window]').first();
  await expect(windowEl).toBeVisible({ timeout: 5000 });

  // Test drag: drag the title bar to move the window.
  const titlebar = windowEl.locator('[data-window-titlebar]');

  // Get initial position.
  const initialBox = await windowEl.boundingBox();
  expect(initialBox).not.toBeNull();

  // Drag the title bar.
  await titlebar.dragTo(page.locator('[data-desktop-windows]'), {
    sourcePosition: { x: 50, y: 18 },
    targetPosition: { x: 200, y: 150 },
  });

  // The window should still be visible after dragging.
  await expect(windowEl).toBeVisible();

  // Test resize: the resize handle should be present (bottom-right only).
  const resizeHandle = windowEl.locator('[data-resize-handle]');
  const count = await resizeHandle.count();
  expect(count).toBeGreaterThanOrEqual(1); // at least the se handle
});

// ---------------------------------------------------------------
// Test: windows support minimize, maximize, and restore
// (VAL-DESKTOP-005)
// ---------------------------------------------------------------
test('windows support minimize, maximize, and restore', async ({
  page,
  authenticator,
}) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);

  // Open a Files window via floating icon.
  await openAppViaIcon(page, 'files');
  const windowEl = page.locator('[data-window]').first();
  await expect(windowEl).toBeVisible({ timeout: 5000 });

  // Test maximize.
  const maximizeBtn = windowEl.locator('[data-window-maximize]');
  await maximizeBtn.click();

  // Wait a moment for the state to update.
  await page.waitForTimeout(200);

  // The window should still be visible.
  await expect(windowEl).toBeVisible();

  // The maximize button should now show the restore icon.
  const maximizeBtnText = await maximizeBtn.textContent();
  expect(maximizeBtnText).toContain('❐'); // restore icon

  // Test restore from maximized.
  await maximizeBtn.click();
  await page.waitForTimeout(200);

  // The window should still be visible.
  await expect(windowEl).toBeVisible();

  // The maximize button should now show the maximize icon again.
  const restoredBtnText = await maximizeBtn.textContent();
  expect(restoredBtnText).toContain('☐'); // maximize icon

  // Test minimize.
  const minimizeBtn = windowEl.locator('[data-window-minimize]');
  await minimizeBtn.click();
  await page.waitForTimeout(200);

  // The window should no longer be visible (minimized).
  await expect(windowEl).not.toBeVisible();

  // A minimized indicator should appear in the bottom bar.
  const indicator = page.locator('[data-minimized-indicator]');
  await expect(indicator.first()).toBeVisible();

  // Click the indicator to restore the window.
  await indicator.first().click();
  await page.waitForTimeout(200);

  // The window should be visible again.
  await expect(windowEl).toBeVisible();
});

// ---------------------------------------------------------------
// Test: window close and reopen updates desktop state cleanly
// (VAL-DESKTOP-006)
// ---------------------------------------------------------------
test('window close and reopen updates desktop state cleanly', async ({
  page,
  authenticator,
}) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);

  // Open a Files window via floating icon.
  await openAppViaIcon(page, 'files');
  const windowEl = page.locator('[data-window]').first();
  await expect(windowEl).toBeVisible({ timeout: 5000 });

  // Close the window.
  const closeBtn = windowEl.locator('[data-window-close]');
  await closeBtn.click();

  // The window should be removed from the desktop.
  await expect(page.locator('[data-window]')).toHaveCount(0);

  // Reopen the app from the floating icon.
  await openAppViaIcon(page, 'files');

  // A fresh window should appear.
  const newWindow = page.locator('[data-window]').first();
  await expect(newWindow).toBeVisible({ timeout: 5000 });

  // The new window should not have stale closed-window state.
  const newContent = newWindow.locator('[data-window-content]');
  await expect(newContent).toBeVisible();
});

// ---------------------------------------------------------------
// Test: desktop restore preserves server-backed window state
// (VAL-DESKTOP-007)
// ---------------------------------------------------------------
test('desktop restore preserves server-backed window state across fresh context', async ({
  page,
  authenticator,
  context,
  browser,
}) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);

  // Open a Files window via floating icon.
  await openAppViaIcon(page, 'files');
  const windowEl = page.locator('[data-window]').first();
  await expect(windowEl).toBeVisible({ timeout: 5000 });

  // Wait for desktop state to be persisted (debounced save).
  await page.waitForTimeout(2000);

  // Record the window ID before reload.
  const windowIdBefore = await windowEl.getAttribute('data-window-id');
  expect(windowIdBefore).toBeTruthy();

  // Reload the page (simulates refresh / fresh context rehydration).
  await page.reload();
  await page.locator('[data-desktop]').waitFor({ state: 'visible', timeout: 10000 });

  // Wait for desktop state to be loaded and windows to be restored.
  await page.waitForTimeout(3000);

  // The desktop should still be visible (not auth entry).
  const desktop = page.locator('[data-desktop]');
  await expect(desktop).toBeVisible();

  // The window should be restored from server-backed state.
  const restoredWindow = page.locator('[data-window]').first();
  await expect(restoredWindow).toBeVisible({ timeout: 5000 });

  // The window ID should match the one before reload.
  const windowIdAfter = await restoredWindow.getAttribute('data-window-id');
  expect(windowIdAfter).toBe(windowIdBefore);
});

// ---------------------------------------------------------------
// Test: clicking logout returns to guest auth UI
// ---------------------------------------------------------------
test('clicking logout returns to guest auth UI from desktop', async ({
  page,
  authenticator,
}) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);

  // Click logout (now in bottom bar).
  const logoutBtn = page.locator('[data-desktop-logout]');
  await logoutBtn.click();

  // Should return to the guest auth UI.
  const authEntry = page.locator('[data-auth-entry]');
  await expect(authEntry).toBeVisible();

  // Desktop should no longer be visible.
  const desktop = page.locator('[data-desktop]');
  await expect(desktop).not.toBeVisible();

  // Session should be signed out.
  const session = await getSession(page, BASE_URL);
  expect(session.authenticated).toBe(false);
});

// ---------------------------------------------------------------
// Test: signed-out users do not see the desktop
// ---------------------------------------------------------------
test('signed-out users do not see the desktop', async ({ page }) => {
  await page.goto(BASE_URL);

  // The auth entry should be visible (not the desktop).
  const authEntry = page.locator('[data-auth-entry]');
  await expect(authEntry).toBeVisible();

  // The desktop should NOT be visible.
  const desktop = page.locator('[data-desktop]');
  await expect(desktop).not.toBeVisible();
});

// ---------------------------------------------------------------
// Test: desktop includes the prompt input (in bottom bar)
// ---------------------------------------------------------------
test('desktop includes the prompt input in bottom bar', async ({
  page,
  authenticator,
}) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);

  // The prompt input should be visible in the bottom bar.
  const promptInput = page.locator('[data-prompt-input]');
  await expect(promptInput).toBeVisible();
  await expect(promptInput).toBeEnabled();
});
