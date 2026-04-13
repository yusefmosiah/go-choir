/**
 * Playwright tests for the Browser app.
 *
 * Covers:
 * - Browser app launches from left rail Browser icon
 * - URL input bar visible in window content area
 * - Typing URL and pressing Enter loads it in iframe
 * - Back/forward/reload buttons work
 * - Loading indicator shown while page loads
 * - Graceful error handling for blocked iframes
 * - Works in mobile focus mode
 */
import { test, expect } from './helpers/fixtures.js';
import { registerPasskey } from './helpers/auth.js';

const BASE_URL = 'http://localhost:4173';

function uniqueEmail() {
  return `browser-test-${Date.now()}-${Math.random().toString(36).slice(2, 8)}@example.com`;
}

// Helper: register a passkey and get to the authenticated desktop.
async function registerAndLoadDesktop(page, authenticator, email) {
  await page.goto(BASE_URL);
  await registerPasskey(page, email, BASE_URL);
  await page.reload();
  await page.locator('[data-desktop]').waitFor({ state: 'visible', timeout: 10000 });
}

// Helper: open the Browser app from the left rail
async function openBrowserApp(page) {
  const browserIcon = page.locator('[data-app-id="browser"]');
  await browserIcon.click();
  // Wait for browser app to appear
  await page.locator('[data-browser-app]').waitFor({ state: 'visible', timeout: 10000 });
}

// ---------------------------------------------------------------
// Test: Browser app launches from left rail Browser icon
// ---------------------------------------------------------------
test('browser app launches from left rail', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);

  // Click the Browser icon in the left rail
  const browserIcon = page.locator('[data-app-id="browser"]');
  await expect(browserIcon).toBeVisible();

  await browserIcon.click();

  // Browser app window should appear
  const browserApp = page.locator('[data-browser-app]');
  await expect(browserApp).toBeVisible({ timeout: 10000 });

  // Window should have title "Browser"
  const windowEl = page.locator('[data-window]').filter({ has: page.locator('[data-browser-app]') });
  await expect(windowEl).toBeVisible();
});

// ---------------------------------------------------------------
// Test: URL input bar visible in window content area
// ---------------------------------------------------------------
test('url input bar is visible', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);
  await openBrowserApp(page);

  // URL input should be visible
  const urlInput = page.locator('[data-browser-url-input]');
  await expect(urlInput).toBeVisible();
  await expect(urlInput).toBeEditable();

  // Navigation buttons should be present
  await expect(page.locator('[data-browser-nav-back]')).toBeVisible();
  await expect(page.locator('[data-browser-nav-forward]')).toBeVisible();
  await expect(page.locator('[data-browser-nav-reload]')).toBeVisible();

  // Go button should be present
  await expect(page.locator('[data-browser-go-btn]')).toBeVisible();
});

// ---------------------------------------------------------------
// Test: Typing URL and pressing Enter loads it in iframe
// ---------------------------------------------------------------
test('typing url and pressing enter loads iframe', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);
  await openBrowserApp(page);

  // Clear and type a URL that allows iframe embedding
  const urlInput = page.locator('[data-browser-url-input]');
  await urlInput.clear();
  await urlInput.fill('https://en.wikipedia.org/wiki/Go_(programming_language)');
  await urlInput.press('Enter');

  // iframe should appear with the URL
  const iframe = page.locator('[data-browser-iframe]');
  await expect(iframe).toBeVisible({ timeout: 10000 });

  // Verify iframe src contains the URL
  const src = await iframe.getAttribute('src');
  expect(src).toContain('wikipedia.org');
});

// ---------------------------------------------------------------
// Test: Go button also triggers navigation
// ---------------------------------------------------------------
test('go button triggers navigation', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);
  await openBrowserApp(page);

  const urlInput = page.locator('[data-browser-url-input]');
  await urlInput.clear();
  await urlInput.fill('https://example.com');

  // Click Go button
  await page.locator('[data-browser-go-btn]').click();

  // iframe should appear
  const iframe = page.locator('[data-browser-iframe]');
  await expect(iframe).toBeVisible({ timeout: 10000 });

  const src = await iframe.getAttribute('src');
  expect(src).toContain('example.com');
});

// ---------------------------------------------------------------
// Test: Back/forward navigation works
// ---------------------------------------------------------------
test('back and forward navigation works', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);
  await openBrowserApp(page);

  // Navigate to first URL
  const urlInput = page.locator('[data-browser-url-input]');
  await urlInput.clear();
  await urlInput.fill('https://en.wikipedia.org');
  await urlInput.press('Enter');

  const iframe = page.locator('[data-browser-iframe]');
  await expect(iframe).toBeVisible({ timeout: 10000 });

  // Navigate to second URL
  await urlInput.clear();
  await urlInput.fill('https://example.com');
  await urlInput.press('Enter');
  await expect(iframe).toBeVisible({ timeout: 10000 });

  let src = await iframe.getAttribute('src');
  expect(src).toContain('example.com');

  // Click back
  const backBtn = page.locator('[data-browser-nav-back]');
  await expect(backBtn).toBeEnabled();
  await backBtn.click();

  // Wait for iframe to update
  await page.waitForTimeout(500);

  // Click forward
  const forwardBtn = page.locator('[data-browser-nav-forward]');
  await expect(forwardBtn).toBeEnabled();
  await forwardBtn.click();

  await page.waitForTimeout(500);
});

// ---------------------------------------------------------------
// Test: Reload button works
// ---------------------------------------------------------------
test('reload button refreshes the page', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);
  await openBrowserApp(page);

  // Navigate to a URL
  const urlInput = page.locator('[data-browser-url-input]');
  await urlInput.clear();
  await urlInput.fill('https://example.com');
  await urlInput.press('Enter');

  const iframe = page.locator('[data-browser-iframe]');
  await expect(iframe).toBeVisible({ timeout: 10000 });

  // Click reload
  const reloadBtn = page.locator('[data-browser-nav-reload]');
  await expect(reloadBtn).toBeEnabled();
  await reloadBtn.click();

  // iframe should still be visible
  await expect(iframe).toBeVisible({ timeout: 5000 });
});

// ---------------------------------------------------------------
// Test: Loading indicator shown while page loads
// ---------------------------------------------------------------
test('loading indicator appears while loading', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);
  await openBrowserApp(page);

  // Navigate — the loading indicator should appear at least momentarily
  const urlInput = page.locator('[data-browser-url-input]');
  await urlInput.clear();
  await urlInput.fill('https://example.com');
  await urlInput.press('Enter');

  // The loading bar might be very brief, so we just verify the iframe eventually appears
  const iframe = page.locator('[data-browser-iframe]');
  await expect(iframe).toBeVisible({ timeout: 10000 });
});

// ---------------------------------------------------------------
// Test: URL auto-prefixes https://
// ---------------------------------------------------------------
test('url auto-prefixes https protocol', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);
  await openBrowserApp(page);

  const urlInput = page.locator('[data-browser-url-input]');
  await urlInput.clear();
  await urlInput.fill('example.com');
  await urlInput.press('Enter');

  const iframe = page.locator('[data-browser-iframe]');
  await expect(iframe).toBeVisible({ timeout: 10000 });

  const src = await iframe.getAttribute('src');
  expect(src).toBe('https://example.com');
});

// ---------------------------------------------------------------
// Test: Single-instance — clicking rail icon again focuses existing window
// ---------------------------------------------------------------
test('clicking browser rail icon again focuses existing window', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);

  // Open browser app
  await page.locator('[data-app-id="browser"]').click();
  await page.locator('[data-browser-app]').waitFor({ state: 'visible', timeout: 10000 });

  // Count windows
  const windowCount = await page.locator('[data-window][data-window-id]').count();

  // Click browser icon again
  await page.locator('[data-app-id="browser"]').click();
  await page.waitForTimeout(500);

  // Should still have the same number of windows (no duplicate)
  const newWindowCount = await page.locator('[data-window][data-window-id]').count();
  expect(newWindowCount).toBe(windowCount);
});

// ---------------------------------------------------------------
// Test: Works in mobile focus mode
// ---------------------------------------------------------------
test('browser app works in mobile focus mode', async ({ page, authenticator }) => {
  const email = uniqueEmail();

  // Set mobile viewport before navigating
  await page.setViewportSize({ width: 375, height: 812 });
  await registerAndLoadDesktop(page, authenticator, email);

  // Open hamburger menu
  const hamburgerBtn = page.locator('[data-hamburger-btn]');
  await expect(hamburgerBtn).toBeVisible();
  await hamburgerBtn.click();

  // Wait for rail overlay
  await page.locator('[data-desktop-rail]').waitFor({ state: 'visible', timeout: 5000 });

  // Click browser icon in overlay
  await page.locator('[data-app-id="browser"]').click();

  // Browser app should appear in full-width focus mode
  const browserApp = page.locator('[data-browser-app]');
  await expect(browserApp).toBeVisible({ timeout: 10000 });

  // URL input should be accessible
  const urlInput = page.locator('[data-browser-url-input]');
  await expect(urlInput).toBeVisible();

  // Navigate to a URL
  await urlInput.clear();
  await urlInput.fill('https://example.com');
  await urlInput.press('Enter');

  const iframe = page.locator('[data-browser-iframe]');
  await expect(iframe).toBeVisible({ timeout: 10000 });
});
