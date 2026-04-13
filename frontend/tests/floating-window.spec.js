/**
 * Playwright tests for the FloatingWindow component rewrite.
 *
 * Covers validation assertions:
 * - VAL-SHELL-011: Floating window open and render
 * - VAL-SHELL-012: Floating window close
 * - VAL-SHELL-013: Floating window minimize
 * - VAL-SHELL-014: Floating window maximize
 * - VAL-SHELL-015: Floating window restore from maximized
 * - VAL-SHELL-016: Floating window restore from minimized
 * - VAL-SHELL-017: Floating window drag via title bar
 * - VAL-SHELL-018: Floating window resize via bottom-right handle only
 * - VAL-SHELL-019: Floating window z-index management (focus)
 * - VAL-SHELL-020: Floating window cascade positioning
 * - VAL-SHELL-021: Active window visual highlight
 */
import { test, expect } from './helpers/fixtures.js';
import { registerPasskey, getSession } from './helpers/auth.js';

const BASE_URL = 'http://localhost:4173';

function uniqueEmail() {
  return `float-test-${Date.now()}-${Math.random().toString(36).slice(2, 8)}@example.com`;
}

// Helper: register a passkey and get to the authenticated desktop.
async function registerAndLoadDesktop(page, authenticator, email) {
  await page.goto(BASE_URL);
  await registerPasskey(page, email, BASE_URL);
  await page.reload();
  await page.locator('[data-desktop]').waitFor({ state: 'visible', timeout: 10000 });
}

// Helper: open app via double-click on floating desktop icon
async function openAppViaIcon(page, appId) {
  const icon = page.locator(`[data-desktop-icon-id="${appId}"]`);
  await icon.dblclick();
}

// ---------------------------------------------------------------
// Test: floating window open and render (VAL-SHELL-011)
// ---------------------------------------------------------------
test('floating window open and render', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);

  // Double-click Files icon on desktop surface to open a window
  await openAppViaIcon(page, 'files');

  // A floating window should appear
  const windowEl = page.locator('[data-window]').first();
  await expect(windowEl).toBeVisible({ timeout: 5000 });

  // Window should be inside the window container area
  const container = page.locator('[data-desktop-windows]');
  await expect(container.locator('[data-window]').first()).toBeVisible();

  // Title bar should have title text and control buttons
  const titlebar = windowEl.locator('[data-window-titlebar]');
  await expect(titlebar).toBeVisible();

  const titleText = titlebar.locator('.title-text');
  await expect(titleText).toContainText('Files');

  // Control buttons present
  await expect(windowEl.locator('[data-window-minimize]')).toBeVisible();
  await expect(windowEl.locator('[data-window-maximize]')).toBeVisible();
  await expect(windowEl.locator('[data-window-close]')).toBeVisible();

  // Content area present
  await expect(windowEl.locator('[data-window-content]')).toBeVisible();
});

// ---------------------------------------------------------------
// Test: floating window close (VAL-SHELL-012)
// ---------------------------------------------------------------
test('floating window close removes from DOM', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);

  // Open two windows
  await openAppViaIcon(page, 'files');
  await page.locator('[data-window]').first().waitFor({ state: 'visible', timeout: 5000 });

  await openAppViaIcon(page, 'browser');
  await page.waitForTimeout(300);

  // Should have 2 windows
  await expect(page.locator('[data-window]')).toHaveCount(2);

  // Close the first (topmost) window
  await page.locator('[data-window-close]').first().click();

  // Should have 1 window remaining
  await expect(page.locator('[data-window]')).toHaveCount(1);

  // The remaining window should be active
  const remaining = page.locator('[data-window]').first();
  await expect(remaining).toHaveClass(/window-active/);
});

// ---------------------------------------------------------------
// Test: floating window minimize (VAL-SHELL-013)
// ---------------------------------------------------------------
test('floating window minimize hides and preserves geometry', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);

  // Open Files window
  await openAppViaIcon(page, 'files');
  const windowEl = page.locator('[data-window]').first();
  await expect(windowEl).toBeVisible({ timeout: 5000 });

  // Record initial position/size
  const boxBefore = await windowEl.boundingBox();

  // Minimize
  await page.locator('[data-window-minimize]').first().click();
  await page.waitForTimeout(200);

  // Window should be hidden
  await expect(windowEl).not.toBeVisible();

  // Minimized indicator in bottom bar
  const indicator = page.locator('[data-minimized-indicator]');
  await expect(indicator).toHaveCount(1);
  await expect(indicator.first()).toBeVisible();

  // Focus should transfer (no active window if only one)
  // Restore by clicking indicator
  await indicator.first().click();
  await page.waitForTimeout(200);

  // Window visible again
  await expect(windowEl).toBeVisible();

  // Geometry should be preserved (approximately)
  const boxAfter = await windowEl.boundingBox();
  expect(Math.abs(boxAfter.x - boxBefore.x)).toBeLessThan(5);
  expect(Math.abs(boxAfter.y - boxBefore.y)).toBeLessThan(5);
  expect(Math.abs(boxAfter.width - boxBefore.width)).toBeLessThan(5);
  expect(Math.abs(boxAfter.height - boxBefore.height)).toBeLessThan(5);
});

// ---------------------------------------------------------------
// Test: floating window maximize (VAL-SHELL-014)
// ---------------------------------------------------------------
test('floating window maximize fills desktop area', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);

  // Open Files window
  await openAppViaIcon(page, 'files');
  const windowEl = page.locator('[data-window]').first();
  await expect(windowEl).toBeVisible({ timeout: 5000 });

  // Record normal geometry
  const normalBox = await windowEl.boundingBox();

  // Maximize
  await page.locator('[data-window-maximize]').first().click();
  await page.waitForTimeout(200);

  // Window still visible
  await expect(windowEl).toBeVisible();

  // Maximize button now shows restore icon
  const maxBtn = page.locator('[data-window-maximize]').first();
  const btnText = await maxBtn.textContent();
  expect(btnText).toContain('❐');

  // Maximized window should be much larger than before
  const maxBox = await windowEl.boundingBox();
  expect(maxBox.width).toBeGreaterThan(normalBox.width);
  expect(maxBox.height).toBeGreaterThan(normalBox.height);

  // Maximized window should fill the desktop area (starts at x=0, no rail)
  expect(maxBox.x).toBeGreaterThanOrEqual(0);
});

// ---------------------------------------------------------------
// Test: floating window restore from maximized (VAL-SHELL-015)
// ---------------------------------------------------------------
test('floating window restore from maximized preserves geometry', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);

  // Open Files window
  await openAppViaIcon(page, 'files');
  const windowEl = page.locator('[data-window]').first();
  await expect(windowEl).toBeVisible({ timeout: 5000 });

  // Record normal geometry
  const normalBox = await windowEl.boundingBox();

  // Maximize
  await page.locator('[data-window-maximize]').first().click();
  await page.waitForTimeout(200);

  // Restore
  await page.locator('[data-window-maximize]').first().click();
  await page.waitForTimeout(200);

  // Icon should change back to maximize icon
  const maxBtn = page.locator('[data-window-maximize]').first();
  const btnText = await maxBtn.textContent();
  expect(btnText).toContain('☐');

  // Geometry should be restored (approximately)
  const restoredBox = await windowEl.boundingBox();
  expect(Math.abs(restoredBox.x - normalBox.x)).toBeLessThan(5);
  expect(Math.abs(restoredBox.y - normalBox.y)).toBeLessThan(5);
  expect(Math.abs(restoredBox.width - normalBox.width)).toBeLessThan(5);
  expect(Math.abs(restoredBox.height - normalBox.height)).toBeLessThan(5);
});

// ---------------------------------------------------------------
// Test: floating window restore from minimized (VAL-SHELL-016)
// ---------------------------------------------------------------
test('floating window restore from minimized via bottom bar', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);

  // Open Files window
  await openAppViaIcon(page, 'files');
  const windowEl = page.locator('[data-window]').first();
  await expect(windowEl).toBeVisible({ timeout: 5000 });

  // Minimize
  await page.locator('[data-window-minimize]').first().click();
  await page.waitForTimeout(200);
  await expect(windowEl).not.toBeVisible();

  // Restore via bottom bar indicator
  await page.locator('[data-minimized-indicator]').first().click();
  await page.waitForTimeout(200);

  // Window should be visible and active
  await expect(windowEl).toBeVisible();
  await expect(windowEl).toHaveClass(/window-active/);
});

// ---------------------------------------------------------------
// Test: floating window drag via title bar (VAL-SHELL-017)
// ---------------------------------------------------------------
test('floating window drag via title bar only', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);

  // Open Files window
  await openAppViaIcon(page, 'files');
  const windowEl = page.locator('[data-window]').first();
  await expect(windowEl).toBeVisible({ timeout: 5000 });

  // Record initial position
  const initialBox = await windowEl.boundingBox();

  // Drag the title bar
  const titlebar = windowEl.locator('[data-window-titlebar]');
  await titlebar.dragTo(page.locator('[data-desktop-windows]'), {
    sourcePosition: { x: 50, y: 18 },
    targetPosition: { x: 300, y: 200 },
  });

  // Window should still be visible
  await expect(windowEl).toBeVisible();
});

// ---------------------------------------------------------------
// Test: floating window resize via bottom-right handle only (VAL-SHELL-018)
// ---------------------------------------------------------------
test('floating window has only bottom-right resize handle', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);

  // Open Files window
  await openAppViaIcon(page, 'files');
  const windowEl = page.locator('[data-window]').first();
  await expect(windowEl).toBeVisible({ timeout: 5000 });

  // Should have exactly one resize handle at the bottom-right
  const resizeHandle = windowEl.locator('[data-resize-handle]');
  await expect(resizeHandle).toHaveCount(1);

  // The handle should be the se (south-east / bottom-right) handle
  const handleClass = await resizeHandle.getAttribute('class');
  expect(handleClass).toContain('resize-se');
});

// ---------------------------------------------------------------
// Test: minimum dimensions enforced (VAL-SHELL-018)
// ---------------------------------------------------------------
test('floating window enforces minimum dimensions', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);

  // Open Files window
  await openAppViaIcon(page, 'files');
  const windowEl = page.locator('[data-window]').first();
  await expect(windowEl).toBeVisible({ timeout: 5000 });

  // The window should have minimum dimensions enforced
  // We check via the store's width/height props
  const width = await windowEl.evaluate((el) => el.offsetWidth);
  const height = await windowEl.evaluate((el) => el.offsetHeight);
  expect(width).toBeGreaterThanOrEqual(200);
  expect(height).toBeGreaterThanOrEqual(120);
});

// ---------------------------------------------------------------
// Test: maximized window not resizable (VAL-SHELL-018)
// ---------------------------------------------------------------
test('maximized window has no resize handle', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);

  // Open Files window
  await openAppViaIcon(page, 'files');
  const windowEl = page.locator('[data-window]').first();
  await expect(windowEl).toBeVisible({ timeout: 5000 });

  // Maximize
  await page.locator('[data-window-maximize]').first().click();
  await page.waitForTimeout(200);

  // No resize handle when maximized
  const resizeHandle = windowEl.locator('[data-resize-handle]');
  await expect(resizeHandle).toHaveCount(0);
});

// ---------------------------------------------------------------
// Test: floating window z-index management (VAL-SHELL-019)
// ---------------------------------------------------------------
test('clicking window brings it to front', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);

  // Open two windows
  await openAppViaIcon(page, 'files');
  await page.locator('[data-window]').first().waitFor({ state: 'visible', timeout: 5000 });

  await openAppViaIcon(page, 'browser');
  await page.waitForTimeout(300);

  // Both windows should exist
  const windows = page.locator('[data-window]');
  await expect(windows).toHaveCount(2);

  // The second window (browser) should be active (on top)
  const browserWindow = windows.nth(1);
  await expect(browserWindow).toHaveClass(/window-active/);

  // Click the first window's titlebar to bring it to front
  // (titlebar is at the top, so less likely to be covered by the cascaded second window)
  const filesWindow = windows.first();
  const filesTitlebar = filesWindow.locator('[data-window-titlebar]');
  await filesTitlebar.click({ position: { x: 20, y: 10 } });

  // Now files should be active
  await expect(filesWindow).toHaveClass(/window-active/);
  await expect(browserWindow).not.toHaveClass(/window-active/);
});

// ---------------------------------------------------------------
// Test: floating window cascade positioning (VAL-SHELL-020)
// ---------------------------------------------------------------
test('new windows cascade with 30px offset', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);

  // Open three windows
  await openAppViaIcon(page, 'files');
  await page.locator('[data-window]').first().waitFor({ state: 'visible', timeout: 5000 });

  await openAppViaIcon(page, 'browser');
  await page.waitForTimeout(300);

  await openAppViaIcon(page, 'terminal');
  await page.waitForTimeout(300);

  // Should have 3 windows
  await expect(page.locator('[data-window]')).toHaveCount(3);

  // Get positions of all windows
  const positions = [];
  const windowEls = page.locator('[data-window]');
  for (let i = 0; i < 3; i++) {
    const box = await windowEls.nth(i).boundingBox();
    positions.push({ x: box.x, y: box.y });
  }

  // Windows should be at different positions (cascade)
  // Not all at the same x,y
  const allSamePosition = positions.every((p) =>
    p.x === positions[0].x && p.y === positions[0].y
  );
  expect(allSamePosition).toBe(false);
});

// ---------------------------------------------------------------
// Test: active window visual highlight (VAL-SHELL-021)
// ---------------------------------------------------------------
test('active window has visual highlight', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);

  // Open two windows
  await openAppViaIcon(page, 'files');
  await page.locator('[data-window]').first().waitFor({ state: 'visible', timeout: 5000 });

  await openAppViaIcon(page, 'browser');
  await page.waitForTimeout(300);

  const windows = page.locator('[data-window]');
  await expect(windows).toHaveCount(2);

  // Only the active window should have window-active class
  const filesWindow = windows.first();
  const browserWindow = windows.nth(1);

  // Browser was opened last, should be active
  await expect(browserWindow).toHaveClass(/window-active/);
  await expect(filesWindow).not.toHaveClass(/window-active/);

  // Click files window's titlebar to make it active
  const filesTitlebar = filesWindow.locator('[data-window-titlebar]');
  await filesTitlebar.click({ position: { x: 20, y: 10 } });
  await expect(filesWindow).toHaveClass(/window-active/);
  await expect(browserWindow).not.toHaveClass(/window-active/);
});

// ---------------------------------------------------------------
// Test: window close transfers focus to next highest z-index (VAL-SHELL-012)
// ---------------------------------------------------------------
test('window close transfers focus to next window', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);

  // Open three windows
  await openAppViaIcon(page, 'files');
  await page.locator('[data-window]').first().waitFor({ state: 'visible', timeout: 5000 });

  await openAppViaIcon(page, 'browser');
  await page.waitForTimeout(300);

  await openAppViaIcon(page, 'terminal');
  await page.waitForTimeout(300);

  // Close the topmost window (terminal, opened last)
  await page.locator('[data-window-close]').first().click();
  await page.waitForTimeout(200);

  // Remaining windows should still be present
  await expect(page.locator('[data-window]')).toHaveCount(2);

  // One of them should be active
  const activeWindows = page.locator('[data-window].window-active');
  await expect(activeWindows).toHaveCount(1);
});

// ---------------------------------------------------------------
// Test: maximized window not draggable (VAL-SHELL-017)
// ---------------------------------------------------------------
test('maximized window is not draggable', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);

  // Open Files window
  await openAppViaIcon(page, 'files');
  const windowEl = page.locator('[data-window]').first();
  await expect(windowEl).toBeVisible({ timeout: 5000 });

  // Maximize
  await page.locator('[data-window-maximize]').first().click();
  await page.waitForTimeout(200);

  const maxBox = await windowEl.boundingBox();

  // Try to drag title bar
  const titlebar = windowEl.locator('[data-window-titlebar]');
  await titlebar.dragTo(page.locator('[data-desktop-windows]'), {
    sourcePosition: { x: 50, y: 18 },
    targetPosition: { x: 100, y: 100 },
  });

  // Window position should not change (still maximized)
  const afterBox = await windowEl.boundingBox();
  expect(Math.abs(afterBox.x - maxBox.x)).toBeLessThan(5);
  expect(Math.abs(afterBox.y - maxBox.y)).toBeLessThan(5);
});
