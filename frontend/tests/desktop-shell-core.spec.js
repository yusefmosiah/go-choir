/**
 * Playwright tests for the desktop shell core components (VAL-SHELL-001
 * through VAL-SHELL-032).
 *
 * These tests verify the desktop shell rewrite:
 * - No top bar rendered (VAL-SHELL-001)
 * - Floating desktop icons render with emoji and labels (VAL-SHELL-002)
 * - Double-click icon opens single-instance window (VAL-SHELL-003)
 * - Active window indicator on desktop icon (VAL-SHELL-004)
 * - Bottom bar always visible (VAL-SHELL-006)
 * - Bottom bar prompt input (VAL-SHELL-007)
 * - Minimized window indicators in bottom bar (VAL-SHELL-008)
 * - User info and logout in bottom bar (VAL-SHELL-009)
 * - Live connection status dot (VAL-SHELL-010)
 * - No bootstrap accordion or runtime panel (VAL-SHELL-024)
 * - No left rail, no hamburger button, no backdrop (VAL-SHELL-026)
 */
import { test, expect } from './helpers/fixtures.js';
import { registerPasskey, getSession } from './helpers/auth.js';

const BASE_URL = 'http://localhost:4173';

function uniqueEmail() {
  return `shell-test-${Date.now()}-${Math.random().toString(36).slice(2, 8)}@example.com`;
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
// Test: no top bar present after rewrite (VAL-SHELL-001)
// ---------------------------------------------------------------
test('no top bar present after rewrite', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);

  // data-desktop-bar must be absent from DOM
  const topBar = page.locator('[data-desktop-bar]');
  await expect(topBar).toHaveCount(0);
});

// ---------------------------------------------------------------
// Test: no left rail, no hamburger, no backdrop (VAL-SHELL-026)
// ---------------------------------------------------------------
test('no left rail, no hamburger button, no backdrop', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);

  // data-desktop-rail must be absent
  await expect(page.locator('[data-desktop-rail]')).toHaveCount(0);

  // data-hamburger-btn must be absent
  await expect(page.locator('[data-hamburger-btn]')).toHaveCount(0);

  // data-rail-backdrop must be absent
  await expect(page.locator('[data-rail-backdrop]')).toHaveCount(0);
});

// ---------------------------------------------------------------
// Test: floating desktop icons render with emoji and labels (VAL-SHELL-002)
// ---------------------------------------------------------------
test('floating desktop icons render with emoji and labels', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);

  const surface = page.locator('[data-desktop-surface]');
  await expect(surface).toBeVisible();

  // Should have exactly 4 desktop icons (Files, Browser, Terminal, Settings)
  const icons = surface.locator('[data-desktop-icon]');
  await expect(icons).toHaveCount(4);

  // Verify each app icon is present
  await expect(surface.locator('[data-desktop-icon-id="files"]')).toBeVisible();
  await expect(surface.locator('[data-desktop-icon-id="browser"]')).toBeVisible();
  await expect(surface.locator('[data-desktop-icon-id="terminal"]')).toBeVisible();
  await expect(surface.locator('[data-desktop-icon-id="settings"]')).toBeVisible();

  // Each icon should have an emoji and a label
  const filesEmoji = surface.locator('[data-desktop-icon-id="files"] [data-desktop-icon-emoji]');
  await expect(filesEmoji).toContainText('📁');

  const filesLabel = surface.locator('[data-desktop-icon-id="files"] [data-desktop-icon-label]');
  await expect(filesLabel).toContainText('Files');
});

// ---------------------------------------------------------------
// Test: double-click icon opens single-instance window (VAL-SHELL-003)
// ---------------------------------------------------------------
test('double-click icon opens single-instance window', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);

  // Double-click the Files icon
  await openAppViaIcon(page, 'files');

  // A window should appear
  const windowEl = page.locator('[data-window]');
  await expect(windowEl).toHaveCount(1);
  await expect(windowEl.first()).toBeVisible({ timeout: 5000 });

  // Double-click the same icon again — should NOT open a second window
  await openAppViaIcon(page, 'files');
  await expect(page.locator('[data-window]')).toHaveCount(1);

  // The window title should match
  const titleText = page.locator('[data-window-titlebar] .title-text');
  await expect(titleText.first()).toContainText('Files');
});

// ---------------------------------------------------------------
// Test: floating icon active indicator (VAL-SHELL-004)
// ---------------------------------------------------------------
test('floating icon active indicator highlights open app', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);

  // Open Files app
  await openAppViaIcon(page, 'files');
  await page.locator('[data-window]').first().waitFor({ state: 'visible', timeout: 5000 });

  // Files icon should have icon-active class
  const filesIcon = page.locator('[data-desktop-icon-id="files"].icon-active');
  await expect(filesIcon).toBeVisible();

  // Open another app — Browser
  await openAppViaIcon(page, 'browser');
  await page.waitForTimeout(300);

  // Browser should now be active
  const browserIcon = page.locator('[data-desktop-icon-id="browser"].icon-active');
  await expect(browserIcon).toBeVisible();
});

// ---------------------------------------------------------------
// Test: bottom bar always visible (VAL-SHELL-006)
// ---------------------------------------------------------------
test('bottom bar always visible', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);

  const bottomBar = page.locator('[data-bottom-bar]');
  await expect(bottomBar).toBeVisible();

  // Bottom bar should have a fixed height approximately 56px
  const height = await bottomBar.evaluate((el) => el.offsetHeight);
  expect(height).toBeGreaterThanOrEqual(52);
  expect(height).toBeLessThanOrEqual(60);

  // Open a window and check bottom bar is still visible
  await openAppViaIcon(page, 'files');
  await page.locator('[data-window]').first().waitFor({ state: 'visible', timeout: 5000 });
  await expect(bottomBar).toBeVisible();
});

// ---------------------------------------------------------------
// Test: bottom bar prompt input (VAL-SHELL-007)
// ---------------------------------------------------------------
test('bottom bar prompt input with placeholder', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);

  const promptInput = page.locator('[data-prompt-input]');
  await expect(promptInput).toBeVisible();
  await expect(promptInput).toBeEnabled();

  // Check placeholder text
  const placeholder = await promptInput.getAttribute('placeholder');
  expect(placeholder).toBe('Ask anything...');

  // Type text and submit with Enter
  await promptInput.fill('Hello world');
  await promptInput.press('Enter');

  // Input should be cleared after submit
  await expect(promptInput).toHaveValue('');
});

// ---------------------------------------------------------------
// Test: minimized window indicators in bottom bar (VAL-SHELL-008)
// ---------------------------------------------------------------
test('minimized window indicators in bottom bar', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);

  // Open Files window
  await openAppViaIcon(page, 'files');
  const windowEl = page.locator('[data-window]').first();
  await expect(windowEl).toBeVisible({ timeout: 5000 });

  // Minimize it
  await windowEl.locator('[data-window-minimize]').click();
  await page.waitForTimeout(200);

  // Window should be hidden
  await expect(windowEl).not.toBeVisible();

  // A minimized indicator should appear in bottom bar
  const indicator = page.locator('[data-minimized-indicator]');
  await expect(indicator).toHaveCount(1);
  await expect(indicator.first()).toBeVisible();

  // Click the indicator to restore
  await indicator.first().click();
  await page.waitForTimeout(200);

  // Window should be visible again
  await expect(windowEl).toBeVisible();
  // Indicator should be gone
  await expect(page.locator('[data-minimized-indicator]')).toHaveCount(0);
});

// ---------------------------------------------------------------
// Test: user info and logout in bottom bar (VAL-SHELL-009)
// ---------------------------------------------------------------
test('user info and logout in bottom bar', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);

  // User info should show email
  const userInfo = page.locator('[data-bottom-user]');
  await expect(userInfo).toBeVisible();

  // Logout button should be present
  const logoutBtn = page.locator('[data-bottom-logout]');
  await expect(logoutBtn).toBeVisible();

  // Click logout
  await logoutBtn.click();

  // Should return to guest auth UI
  const authEntry = page.locator('[data-auth-entry]');
  await expect(authEntry).toBeVisible();

  // Desktop should not be visible
  await expect(page.locator('[data-desktop]')).not.toBeVisible();
});

// ---------------------------------------------------------------
// Test: live connection status dot (VAL-SHELL-010)
// ---------------------------------------------------------------
test('live connection status dot in bottom bar', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);

  const statusEl = page.locator('[data-connection-status]');
  await expect(statusEl).toBeVisible();

  // Should have a status dot inside
  const dot = statusEl.locator('.status-dot');
  await expect(dot).toBeVisible();

  // Check it has aria-live for accessibility
  const ariaLive = await statusEl.getAttribute('aria-live');
  expect(ariaLive).toBe('polite');
});

// ---------------------------------------------------------------
// Test: no bootstrap accordion or runtime panel (VAL-SHELL-024)
// ---------------------------------------------------------------
test('no bootstrap accordion or runtime panel', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);

  // No bootstrap element should be present
  await expect(page.locator('[data-shell-bootstrap]')).toHaveCount(0);

  // No task runner should be visible
  await expect(page.locator('[data-task-runner]')).toHaveCount(0);

  // No launcher toggle should be present
  await expect(page.locator('[data-launcher-toggle]')).toHaveCount(0);
});

// ---------------------------------------------------------------
// Test: floating window close removes from DOM (VAL-SHELL-012)
// ---------------------------------------------------------------
test('floating window close removes from DOM', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);

  // Open Files window
  await openAppViaIcon(page, 'files');
  await page.locator('[data-window]').first().waitFor({ state: 'visible', timeout: 5000 });

  // Close it
  await page.locator('[data-window-close]').first().click();

  // Window should be removed
  await expect(page.locator('[data-window]')).toHaveCount(0);
});

// ---------------------------------------------------------------
// Test: floating window minimize and restore (VAL-SHELL-013)
// ---------------------------------------------------------------
test('floating window minimize hides and shows indicator', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);

  // Open Files window
  await openAppViaIcon(page, 'files');
  await page.locator('[data-window]').first().waitFor({ state: 'visible', timeout: 5000 });

  // Minimize
  await page.locator('[data-window-minimize]').first().click();
  await page.waitForTimeout(200);

  // Window hidden (still in DOM but display:none), indicator shown
  await expect(page.locator('[data-window]').first()).not.toBeVisible();
  await expect(page.locator('[data-minimized-indicator]')).toHaveCount(1);
});

// ---------------------------------------------------------------
// Test: floating window maximize and restore (VAL-SHELL-014)
// ---------------------------------------------------------------
test('floating window maximize fills desktop area', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);

  // Open Files window
  await openAppViaIcon(page, 'files');
  const windowEl = page.locator('[data-window]').first();
  await expect(windowEl).toBeVisible({ timeout: 5000 });

  // Maximize
  await page.locator('[data-window-maximize]').first().click();
  await page.waitForTimeout(200);

  // Window should still be visible
  await expect(windowEl).toBeVisible();

  // Maximize button should now show restore icon
  const maxBtn = page.locator('[data-window-maximize]').first();
  const btnText = await maxBtn.textContent();
  expect(btnText).toContain('❐');

  // Click again to restore
  await maxBtn.click();
  await page.waitForTimeout(200);

  // Restore icon should change back
  const restoredText = await maxBtn.textContent();
  expect(restoredText).toContain('☐');
});

// ---------------------------------------------------------------
// Test: aria labels on desktop icons and window controls (VAL-SHELL-031)
// ---------------------------------------------------------------
test('aria labels on desktop icons and window controls', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);

  // Check desktop icons have aria-labels
  const filesIcon = page.locator('[data-desktop-icon-id="files"]');
  const filesAria = await filesIcon.getAttribute('aria-label');
  expect(filesAria).toBe('Files');

  // Open a window and check its controls have aria-labels
  await filesIcon.dblclick();
  await page.locator('[data-window]').first().waitFor({ state: 'visible', timeout: 5000 });

  const closeBtn = page.locator('[data-window-close]').first();
  const closeAria = await closeBtn.getAttribute('aria-label');
  expect(closeAria).toBe('Close');

  const minBtn = page.locator('[data-window-minimize]').first();
  const minAria = await minBtn.getAttribute('aria-label');
  expect(minAria).toBe('Minimize');

  const maxBtn = page.locator('[data-window-maximize]').first();
  const maxAria = await maxBtn.getAttribute('aria-label');
  expect(maxAria).toBe('Maximize');
});
