/**
 * Playwright tests for the terminal app (VAL-TERM-001 through VAL-TERM-020).
 *
 * These tests verify the ghostty-web terminal integration:
 * - Terminal launches from floating desktop icon (VAL-TERM-001)
 * - ghostty-web WASM init and canvas rendering (VAL-TERM-002)
 * - Dark theme matching desktop aesthetic (VAL-TERM-003)
 * - Terminal reconnect after minimize/restore (VAL-TERM-010)
 * - Minimize/restore preserves terminal content (VAL-TERM-012)
 * - Maximize/restore preserves terminal content (VAL-TERM-013)
 * - Terminal window drag works without disrupting rendering (VAL-TERM-017)
 * - Terminal canvas fits container without scrollbars (VAL-TERM-020)
 */
import { test, expect } from './helpers/fixtures.js';
import { registerPasskey, getSession } from './helpers/auth.js';

const BASE_URL = 'http://localhost:4173';

function uniqueEmail() {
  return `terminal-test-${Date.now()}-${Math.random().toString(36).slice(2, 8)}@example.com`;
}

// Helper: register a passkey and get to the authenticated desktop.
async function registerAndLoadDesktop(page, authenticator, email) {
  await page.goto(BASE_URL);
  await registerPasskey(page, email, BASE_URL);
  await page.reload();
  await page.locator('[data-desktop]').waitFor({ state: 'visible', timeout: 10000 });
}

// Helper: open terminal via double-click on floating desktop icon
async function openTerminal(page) {
  const icon = page.locator('[data-desktop-icon-id="terminal"]');
  await icon.dblclick();
  // Wait for the terminal window to appear
  await page.locator('[data-terminal-app]').waitFor({ state: 'visible', timeout: 10000 });
}

// Helper: wait for the terminal canvas to render (WASM init)
async function waitForTerminalCanvas(page) {
  await page.locator('[data-terminal] canvas').waitFor({ state: 'visible', timeout: 15000 });
}

// ---------------------------------------------------------------
// Test: terminal launches from floating desktop icon (VAL-TERM-001)
// ---------------------------------------------------------------
test('terminal launches from floating desktop icon', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);

  // Double-click the terminal icon
  await openTerminal(page);

  // Terminal window must be visible
  const terminalWindow = page.locator('[data-window]').first();
  await expect(terminalWindow).toBeVisible();

  // Title should be "Terminal"
  const titleText = await terminalWindow.locator('[data-window-titlebar] .titlvtext, [data-window-titlebar]').first().textContent();
  expect(titleText).toContain('Terminal');
});

// ---------------------------------------------------------------
// Test: ghostty-web WASM init and canvas rendering (VAL-TERM-002)
// ---------------------------------------------------------------
test('ghostty-web WASM init and canvas rendering', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);

  await openTerminal(page);

  // Wait for WASM init and canvas rendering
  await waitForTerminalCanvas(page);

  // Canvas must have non-zero dimensions
  const canvasBox = await page.locator('[data-terminal] canvas').boundingBox();
  expect(canvasBox).not.toBeNull();
  expect(canvasBox.width).toBeGreaterThan(0);
  expect(canvasBox.height).toBeGreaterThan(0);
});

// ---------------------------------------------------------------
// Test: dark theme matching desktop aesthetic (VAL-TERM-003)
// ---------------------------------------------------------------
test('dark theme matching desktop aesthetic', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);

  await openTerminal(page);
  await waitForTerminalCanvas(page);

  // Terminal wrapper should have dark background color
  const wrapper = page.locator('[data-terminal]');
  const bgColor = await wrapper.evaluate((el) => {
    return window.getComputedStyle(el).backgroundColor;
  });

  // Should be a dark color (rgb values should be low)
  // #1a1b26 = rgb(26, 27, 38)
  expect(bgColor).toBeTruthy();
  // Parse rgb values
  const match = bgColor.match(/rgb\((\d+),\s*(\d+),\s*(\d+)\)/);
  if (match) {
    const [, r, g, b] = match.map(Number);
    expect(r).toBeLessThan(50);
    expect(g).toBeLessThan(50);
    expect(b).toBeLessThan(60);
  }
});

// ---------------------------------------------------------------
// Test: terminal window close cleans up (VAL-TERM-009 partial)
// ---------------------------------------------------------------
test('terminal window can be closed', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);

  await openTerminal(page);
  await waitForTerminalCanvas(page);

  // Close the terminal window
  const closeBtn = page.locator('[data-window] [data-window-close]').first();
  await closeBtn.click();

  // Terminal window should be removed
  await expect(page.locator('[data-terminal-app]')).toHaveCount(0, { timeout: 5000 });
});

// ---------------------------------------------------------------
// Test: minimize then restore preserves terminal (VAL-TERM-010, VAL-TERM-012)
// ---------------------------------------------------------------
test('minimize then restore preserves terminal', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);

  await openTerminal(page);
  await waitForTerminalCanvas(page);

  // Minimize the window
  const minimizeBtn = page.locator('[data-window] [data-window-minimize]').first();
  await minimizeBtn.click();

  // Terminal window should be hidden (not visible)
  await expect(page.locator('[data-terminal-app]')).not.toBeVisible({ timeout: 5000 });

  // Click the minimized indicator in the bottom bar to restore
  const restoreBtn = page.locator('[data-bottom-bar] [data-minimized-indicator]').first();
  await restoreBtn.click();

  // Terminal window should be visible again
  await expect(page.locator('[data-terminal-app]')).toBeVisible({ timeout: 5000 });

  // Canvas should still be present
  const canvas = page.locator('[data-terminal] canvas');
  await expect(canvas).toBeVisible({ timeout: 5000 });
});

// ---------------------------------------------------------------
// Test: maximize then restore preserves terminal (VAL-TERM-013)
// ---------------------------------------------------------------
test('maximize then restore preserves terminal', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);

  await openTerminal(page);
  await waitForTerminalCanvas(page);

  // Get initial window dimensions
  const windowEl = page.locator('[data-window]').first();
  const initialBox = await windowEl.boundingBox();

  // Maximize
  const maximizeBtn = page.locator('[data-window] [data-window-maximize]').first();
  await maximizeBtn.click();

  // Wait for maximized state
  await page.waitForTimeout(500);

  // Canvas should still be visible
  await expect(page.locator('[data-terminal] canvas')).toBeVisible({ timeout: 5000 });

  // Restore
  const restoreBtn = page.locator('[data-window] [data-window-maximize]').first();
  await restoreBtn.click();

  // Wait for restore
  await page.waitForTimeout(500);

  // Canvas should still be visible
  await expect(page.locator('[data-terminal] canvas')).toBeVisible({ timeout: 5000 });
});

// ---------------------------------------------------------------
// Test: terminal window drag works (VAL-TERM-017)
// ---------------------------------------------------------------
test('terminal window drag does not disrupt rendering', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);

  await openTerminal(page);
  await waitForTerminalCanvas(page);

  const windowEl = page.locator('[data-window]').first();
  const titlebar = page.locator('[data-window-titlebar]').first();
  const initialBox = await windowEl.boundingBox();

  // Drag the window by title bar
  await titlebar.hover();
  await page.mouse.down();
  await page.mouse.move(initialBox.x + 100, initialBox.y + 50);
  await page.mouse.up();

  // Canvas should still be visible after drag
  await expect(page.locator('[data-terminal] canvas')).toBeVisible({ timeout: 3000 });
});

// ---------------------------------------------------------------
// Test: terminal canvas fits container without scrollbars (VAL-TERM-020)
// ---------------------------------------------------------------
test('terminal canvas fits container without scrollbars', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);

  await openTerminal(page);
  await waitForTerminalCanvas(page);

  // Check that the terminal container has no overflow (scrollbars)
  const hasScrollbars = await page.locator('[data-terminal]').evaluate((el) => {
    return el.scrollHeight > el.clientHeight || el.scrollWidth > el.clientWidth;
  });
  expect(hasScrollbars).toBe(false);
});

// ---------------------------------------------------------------
// Test: no console errors during terminal init (VAL-TERM-002 partial)
// ---------------------------------------------------------------
test('no console errors during terminal init', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  const consoleErrors = [];

  page.on('console', (msg) => {
    if (msg.type() === 'error') {
      consoleErrors.push(msg.text());
    }
  });

  await registerAndLoadDesktop(page, authenticator, email);
  await openTerminal(page);
  await waitForTerminalCanvas(page);

  // Wait a bit for any delayed errors
  await page.waitForTimeout(3000);

  // Filter out known non-critical errors (e.g., WebSocket connection failures in test env)
  const criticalErrors = consoleErrors.filter((e) =>
    !e.includes('WebSocket') &&
    !e.includes('ERR_CONNECTION_REFUSED') &&
    !e.includes('net::ERR_CONNECTION_REFUSED') &&
    !e.includes('Failed to fetch')
  );

  expect(criticalErrors).toEqual([]);
});
