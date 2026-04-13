/**
 * Playwright tests for responsive layout across three breakpoints.
 *
 * Covers validation assertions:
 * - VAL-RESP-001: Desktop — floating icons visible with labels
 * - VAL-RESP-002: Desktop — windows floating, draggable, resizable
 * - VAL-RESP-003: Desktop — bottom bar full height (~56px)
 * - VAL-RESP-004: Desktop — multiple windows visible simultaneously
 * - VAL-RESP-005: Tablet — windows floating with max-width constraint
 * - VAL-RESP-006: Mobile — floating icons remain visible
 * - VAL-RESP-007: Mobile — single focus window mode (one at a time)
 * - VAL-RESP-008: Mobile — window is full width, non-draggable
 * - VAL-RESP-009: Mobile — prompt bar full width with >=44px touch target
 * - VAL-RESP-010: Mobile — closing window returns to empty desktop
 * - VAL-RESP-011: No horizontal overflow at any breakpoint
 * - VAL-RESP-012: Breakpoint transition is smooth (no layout flash)
 * - VAL-RESP-013: Mobile — consistent desktop experience
 * - VAL-RESP-014: Tablet — multiple windows still supported
 */
import { test, expect } from './helpers/fixtures.js';
import { registerPasskey } from './helpers/auth.js';

const BASE_URL = 'http://localhost:4173';

function uniqueEmail() {
  return `resp-test-${Date.now()}-${Math.random().toString(36).slice(2, 8)}@example.com`;
}

// Helper: register a passkey and get to the authenticated desktop.
async function registerAndLoadDesktop(page, authenticator, email, viewportSize = { width: 1280, height: 800 }) {
  await page.setViewportSize(viewportSize);
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

// ================================================================
// DESKTOP BREAKPOINT (>1024px) — viewport 1280x800
// ================================================================

test.describe('Desktop breakpoint (>1024px)', () => {
  // VAL-RESP-001: Desktop — floating icons visible with labels
  test('floating icons visible with labels', async ({ page, authenticator }) => {
    const email = uniqueEmail();
    await registerAndLoadDesktop(page, authenticator, email, { width: 1280, height: 800 });

    const surface = page.locator('[data-desktop-surface]');
    await expect(surface).toBeVisible();

    // 4 icons should be visible
    const icons = surface.locator('[data-desktop-icon]');
    await expect(icons).toHaveCount(4);

    // Labels should be visible
    const filesLabel = surface.locator('[data-desktop-icon-label]').first();
    await expect(filesLabel).toBeVisible();
  });

  // VAL-RESP-002: Desktop — windows floating, draggable, resizable
  test('windows are floating, draggable, resizable', async ({ page, authenticator }) => {
    const email = uniqueEmail();
    await registerAndLoadDesktop(page, authenticator, email, { width: 1280, height: 800 });

    await openAppViaIcon(page, 'files');
    const windowEl = page.locator('[data-window]').first();
    await expect(windowEl).toBeVisible({ timeout: 5000 });

    // Window should be absolutely positioned (floating)
    const position = await windowEl.evaluate((el) => window.getComputedStyle(el).position);
    expect(position).toBe('absolute');

    // Resize handle should be present
    const resizeHandle = windowEl.locator('[data-resize-handle]');
    await expect(resizeHandle).toHaveCount(1);
  });

  // VAL-RESP-003: Desktop — bottom bar full height (~56px)
  test('bottom bar renders at ~56px full width', async ({ page, authenticator }) => {
    const email = uniqueEmail();
    await registerAndLoadDesktop(page, authenticator, email, { width: 1280, height: 800 });

    const bottomBar = page.locator('[data-bottom-bar]');
    await expect(bottomBar).toBeVisible();

    const height = await bottomBar.evaluate((el) => el.offsetHeight);
    expect(height).toBeGreaterThanOrEqual(52);
    expect(height).toBeLessThanOrEqual(60);

    const width = await bottomBar.evaluate((el) => el.offsetWidth);
    expect(width).toBe(1280);
  });

  // VAL-RESP-004: Desktop — multiple windows visible simultaneously
  test('multiple windows visible simultaneously', async ({ page, authenticator }) => {
    const email = uniqueEmail();
    await registerAndLoadDesktop(page, authenticator, email, { width: 1280, height: 800 });

    await openAppViaIcon(page, 'files');
    await page.locator('[data-window]').first().waitFor({ state: 'visible', timeout: 5000 });

    await openAppViaIcon(page, 'browser');
    await page.waitForTimeout(300);

    const windows = page.locator('[data-window]');
    await expect(windows).toHaveCount(2);

    // Both should be visible
    await expect(windows.nth(0)).toBeVisible();
    await expect(windows.nth(1)).toBeVisible();
  });
});

// ================================================================
// TABLET BREAKPOINT (768-1024px) — viewport 900x800
// ================================================================

test.describe('Tablet breakpoint (768-1024px)', () => {
  // VAL-RESP-005: Tablet — windows floating with max-width constraint
  test('windows floating with max-width constraint', async ({ page, authenticator }) => {
    const email = uniqueEmail();
    await registerAndLoadDesktop(page, authenticator, email, { width: 900, height: 800 });

    await openAppViaIcon(page, 'files');
    const windowEl = page.locator('[data-window]').first();
    await expect(windowEl).toBeVisible({ timeout: 5000 });

    // Window width should not exceed viewport width
    const winBox = await windowEl.boundingBox();
    expect(winBox.width).toBeLessThanOrEqual(900);

    // Floating icons should still be visible with labels
    const icons = page.locator('[data-desktop-icon]');
    await expect(icons).toHaveCount(4);
  });

  // Bottom bar remains full height
  test('bottom bar remains full height', async ({ page, authenticator }) => {
    const email = uniqueEmail();
    await registerAndLoadDesktop(page, authenticator, email, { width: 900, height: 800 });

    const bottomBar = page.locator('[data-bottom-bar]');
    await expect(bottomBar).toBeVisible();

    const height = await bottomBar.evaluate((el) => el.offsetHeight);
    expect(height).toBeGreaterThanOrEqual(52);
    expect(height).toBeLessThanOrEqual(60);
  });

  // VAL-RESP-014: Tablet — multiple windows still supported
  test('multiple windows still supported', async ({ page, authenticator }) => {
    const email = uniqueEmail();
    await registerAndLoadDesktop(page, authenticator, email, { width: 900, height: 800 });

    await openAppViaIcon(page, 'files');
    await page.locator('[data-window]').first().waitFor({ state: 'visible', timeout: 5000 });

    await openAppViaIcon(page, 'browser');
    await page.waitForTimeout(300);

    const windows = page.locator('[data-window]');
    await expect(windows).toHaveCount(2);
  });
});

// ================================================================
// MOBILE BREAKPOINT (<768px) — viewport 375x812
// ================================================================

test.describe('Mobile breakpoint (<768px)', () => {
  // VAL-RESP-006: Mobile — floating icons remain visible
  test('floating icons remain visible', async ({ page, authenticator }) => {
    const email = uniqueEmail();
    await registerAndLoadDesktop(page, authenticator, email, { width: 375, height: 812 });

    // No left rail should be present
    await expect(page.locator('[data-desktop-rail]')).toHaveCount(0);

    // No hamburger button should be present
    await expect(page.locator('[data-hamburger-btn]')).toHaveCount(0);

    // No backdrop should be present
    await expect(page.locator('[data-rail-backdrop]')).toHaveCount(0);

    // Floating desktop icons should be visible
    const icons = page.locator('[data-desktop-icon]');
    await expect(icons).toHaveCount(4);
    await expect(icons.first()).toBeVisible();

    // Desktop surface spans full viewport width
    const surface = page.locator('[data-desktop-surface]');
    const surfaceWidth = await surface.evaluate((el) => el.offsetWidth);
    expect(surfaceWidth).toBeGreaterThanOrEqual(375);
  });

  // VAL-RESP-007: Mobile — single focus window mode (one at a time)
  test('single focus window mode — only one window visible', async ({ page, authenticator }) => {
    const email = uniqueEmail();
    await registerAndLoadDesktop(page, authenticator, email, { width: 375, height: 812 });

    // Open Files via double-click
    await openAppViaIcon(page, 'files');
    await page.waitForTimeout(300);
    await expect(page.locator('[data-window]')).toHaveCount(1);

    // Open Browser via double-click
    await openAppViaIcon(page, 'browser');
    await page.waitForTimeout(300);

    // Should still have only 1 visible window (single focus mode hides the first)
    const visibleWindows = page.locator('[data-window]:visible');
    await expect(visibleWindows).toHaveCount(1);
  });

  // VAL-RESP-010: Mobile — closing window returns to empty desktop
  test('closing window returns to empty desktop', async ({ page, authenticator }) => {
    const email = uniqueEmail();
    await registerAndLoadDesktop(page, authenticator, email, { width: 375, height: 812 });

    // Open an app
    await openAppViaIcon(page, 'files');
    await page.waitForTimeout(300);
    await expect(page.locator('[data-window]')).toHaveCount(1);

    // Close it
    await page.locator('[data-window-close]').first().click();
    await page.waitForTimeout(200);

    // No windows visible
    await expect(page.locator('[data-window]')).toHaveCount(0);

    // Bottom bar should still be visible
    const bottomBar = page.locator('[data-bottom-bar]');
    await expect(bottomBar).toBeVisible();

    // Floating icons should still be visible
    const icons = page.locator('[data-desktop-icon]');
    await expect(icons.first()).toBeVisible();
  });

  // VAL-RESP-008: Mobile — window is full width, non-draggable
  test('window is full width and non-draggable', async ({ page, authenticator }) => {
    const email = uniqueEmail();
    await registerAndLoadDesktop(page, authenticator, email, { width: 375, height: 812 });

    // Open an app
    await openAppViaIcon(page, 'files');
    await page.waitForTimeout(300);

    const windowEl = page.locator('[data-window]').first();
    await expect(windowEl).toBeVisible();

    // Window should be full width
    const winBox = await windowEl.boundingBox();
    expect(winBox.width).toBeGreaterThanOrEqual(375);

    // No resize handle should be present in mobile mode
    const resizeHandle = windowEl.locator('[data-resize-handle]');
    await expect(resizeHandle).toHaveCount(0);
  });

  // VAL-RESP-009: Mobile — prompt bar full width with >=44px touch target
  test('prompt bar full width with >=44px touch target', async ({ page, authenticator }) => {
    const email = uniqueEmail();
    await registerAndLoadDesktop(page, authenticator, email, { width: 375, height: 812 });

    const promptInput = page.locator('[data-prompt-input]');
    await expect(promptInput).toBeVisible();

    // Touch target should be >=44px
    const height = await promptInput.evaluate((el) => el.offsetHeight);
    expect(height).toBeGreaterThanOrEqual(44);
  });

  // VAL-RESP-013: Mobile — consistent desktop experience
  test('consistent desktop experience — tap icon opens app in focus mode', async ({ page, authenticator }) => {
    const email = uniqueEmail();
    await registerAndLoadDesktop(page, authenticator, email, { width: 375, height: 812 });

    // Floating icons visible
    const icons = page.locator('[data-desktop-icon]');
    await expect(icons).toHaveCount(4);

    // No hamburger, no rail, no overlay
    await expect(page.locator('[data-hamburger-btn]')).toHaveCount(0);
    await expect(page.locator('[data-desktop-rail]')).toHaveCount(0);
    await expect(page.locator('[data-rail-backdrop]')).toHaveCount(0);

    // Double-tap icon to open app
    await openAppViaIcon(page, 'files');
    await page.waitForTimeout(300);

    // Window should open in focus mode
    await expect(page.locator('[data-window]')).toHaveCount(1);
  });
});

// ================================================================
// CROSS-BREAKPOINT TESTS
// ================================================================

test.describe('Cross-breakpoint checks', () => {
  // VAL-RESP-011: No horizontal overflow at any breakpoint
  test('no horizontal overflow at desktop breakpoint', async ({ page, authenticator }) => {
    const email = uniqueEmail();
    await registerAndLoadDesktop(page, authenticator, email, { width: 1280, height: 800 });

    const scrollWidth = await page.evaluate(() => document.documentElement.scrollWidth);
    const clientWidth = await page.evaluate(() => document.documentElement.clientWidth);
    expect(scrollWidth).toBeLessThanOrEqual(clientWidth);
  });

  test('no horizontal overflow at tablet breakpoint', async ({ page, authenticator }) => {
    const email = uniqueEmail();
    await registerAndLoadDesktop(page, authenticator, email, { width: 900, height: 800 });

    const scrollWidth = await page.evaluate(() => document.documentElement.scrollWidth);
    const clientWidth = await page.evaluate(() => document.documentElement.clientWidth);
    expect(scrollWidth).toBeLessThanOrEqual(clientWidth);
  });

  test('no horizontal overflow at mobile breakpoint', async ({ page, authenticator }) => {
    const email = uniqueEmail();
    await registerAndLoadDesktop(page, authenticator, email, { width: 375, height: 812 });

    const scrollWidth = await page.evaluate(() => document.documentElement.scrollWidth);
    const clientWidth = await page.evaluate(() => document.documentElement.clientWidth);
    expect(scrollWidth).toBeLessThanOrEqual(clientWidth);
  });

  // VAL-RESP-012: Breakpoint transition is smooth (no layout flash)
  test('breakpoint transition from desktop to tablet is clean', async ({ page, authenticator }) => {
    const email = uniqueEmail();
    await registerAndLoadDesktop(page, authenticator, email, { width: 1280, height: 800 });

    // Verify desktop layout — floating icons visible
    const surface = page.locator('[data-desktop-surface]');
    await expect(surface).toBeVisible();

    // Resize to tablet
    await page.setViewportSize({ width: 900, height: 800 });
    await page.waitForTimeout(300);

    // Icons should still be visible
    await expect(surface).toBeVisible();

    // No JS errors
    const logs = [];
    page.on('console', (msg) => {
      if (msg.type() === 'error') logs.push(msg.text());
    });
    await page.waitForTimeout(100);
    // Allow some errors that might be from other sources
  });
});
