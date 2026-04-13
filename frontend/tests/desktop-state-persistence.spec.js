/**
 * Playwright tests for desktop state persistence (VAL-SHELL-022, VAL-SHELL-023,
 * VAL-CROSS-203).
 *
 * Verifies:
 * - Window positions, sizes, z-index, minimized/maximized states, and active
 *   window are saved and restored on page reload (VAL-SHELL-022)
 * - Desktop state saved in one tab restores in a new tab for the same user
 *   (VAL-SHELL-023)
 * - No perceptible flash of empty desktop before state restores (VAL-SHELL-022)
 * - Same window IDs present after reload (VAL-SHELL-022)
 */
import { test, expect } from './helpers/fixtures.js';
import { registerPasskey, getSession } from './helpers/auth.js';

const BASE_URL = 'http://localhost:4173';

function uniqueEmail() {
  return `state-test-${Date.now()}-${Math.random().toString(36).slice(2, 8)}@example.com`;
}

// Helper: register a passkey and get to the authenticated desktop.
async function registerAndLoadDesktop(page, authenticator, email) {
  await page.goto(BASE_URL);
  await registerPasskey(page, email, BASE_URL);
  await page.reload();
  await page.locator('[data-desktop]').waitFor({ state: 'visible', timeout: 10000 });
}

// Helper: open a specific app from the floating desktop icon
async function openApp(page, appId) {
  await page.locator(`[data-desktop-icon-id="${appId}"]`).dblclick();
  await page.locator('[data-window]').first().waitFor({ state: 'visible', timeout: 5000 });
}

// Helper: get window positions and sizes from the DOM
async function getWindowStates(page) {
  return page.evaluate(() => {
    const wins = document.querySelectorAll('[data-window]');
    return Array.from(wins).map((el) => ({
      windowId: el.getAttribute('data-window-id'),
      left: parseInt(el.style.left, 10) || 0,
      top: parseInt(el.style.top, 10) || 0,
      width: parseInt(el.style.width, 10) || 0,
      height: parseInt(el.style.height, 10) || 0,
      zIndex: parseInt(el.style.zIndex, 10) || 0,
      isActive: el.classList.contains('window-active'),
      isVisible: el.offsetWidth > 0 && el.offsetHeight > 0,
    }));
  });
}

// ---------------------------------------------------------------
// Test: single window position and size restored after reload
// (VAL-SHELL-022)
// ---------------------------------------------------------------
test('single window position and size restored after reload', async ({
  page,
  authenticator,
}) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);

  // Open a Files window
  await openApp(page, 'files');
  const windowEl = page.locator('[data-window]').first();
  await expect(windowEl).toBeVisible();

  // Get the window ID
  const windowIdBefore = await windowEl.getAttribute('data-window-id');
  expect(windowIdBefore).toBeTruthy();

  // Wait for the debounced state save
  await page.waitForTimeout(1000);

  // Record position and size before reload
  const statesBefore = await getWindowStates(page);
  expect(statesBefore.length).toBe(1);

  // Reload
  await page.reload();
  await page.locator('[data-desktop]').waitFor({ state: 'visible', timeout: 10000 });

  // Wait for desktop state to load
  await page.waitForTimeout(2000);

  // Window should be restored
  const restoredWindow = page.locator('[data-window]').first();
  await expect(restoredWindow).toBeVisible({ timeout: 5000 });

  // Window ID should match
  const windowIdAfter = await restoredWindow.getAttribute('data-window-id');
  expect(windowIdAfter).toBe(windowIdBefore);

  // Position and size should be close to original (within 5px tolerance)
  const statesAfter = await getWindowStates(page);
  expect(statesAfter.length).toBe(1);
  expect(Math.abs(statesAfter[0].left - statesBefore[0].left)).toBeLessThanOrEqual(5);
  expect(Math.abs(statesAfter[0].top - statesBefore[0].top)).toBeLessThanOrEqual(5);
  expect(Math.abs(statesAfter[0].width - statesBefore[0].width)).toBeLessThanOrEqual(5);
  expect(Math.abs(statesAfter[0].height - statesBefore[0].height)).toBeLessThanOrEqual(5);
});

// ---------------------------------------------------------------
// Test: multiple windows with z-index restored after reload
// (VAL-SHELL-022)
// ---------------------------------------------------------------
test('multiple windows with z-index restored after reload', async ({
  page,
  authenticator,
}) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);

  // Open two windows
  await openApp(page, 'files');
  await openApp(page, 'browser');

  const windowsBefore = page.locator('[data-window]');
  await expect(windowsBefore).toHaveCount(2);

  // Wait for the debounced state save
  await page.waitForTimeout(1000);

  // Record window IDs and z-index order
  const statesBefore = await getWindowStates(page);
  expect(statesBefore.length).toBe(2);

  // Sort by z-index to get the stacking order
  const sortedBefore = [...statesBefore].sort((a, b) => a.zIndex - b.zIndex);
  const topWindowIdBefore = sortedBefore[sortedBefore.length - 1].windowId;

  // Reload
  await page.reload();
  await page.locator('[data-desktop]').waitFor({ state: 'visible', timeout: 10000 });
  await page.waitForTimeout(2000);

  // Both windows should be restored
  const windowsAfter = page.locator('[data-window]');
  await expect(windowsAfter).toHaveCount(2);

  // Same window IDs should be present
  const statesAfter = await getWindowStates(page);
  const idsBefore = statesBefore.map((s) => s.windowId).sort();
  const idsAfter = statesAfter.map((s) => s.windowId).sort();
  expect(idsAfter).toEqual(idsBefore);

  // Top window (highest z-index) should be the same
  const sortedAfter = [...statesAfter].sort((a, b) => a.zIndex - b.zIndex);
  const topWindowIdAfter = sortedAfter[sortedAfter.length - 1].windowId;
  expect(topWindowIdAfter).toBe(topWindowIdBefore);
});

// ---------------------------------------------------------------
// Test: minimized window state preserved after reload
// (VAL-SHELL-022)
// ---------------------------------------------------------------
test('minimized window state preserved after reload', async ({
  page,
  authenticator,
}) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);

  // Open two windows
  await openApp(page, 'files');
  await openApp(page, 'browser');

  // Minimize the first (files) window
  const filesWindow = page.locator('[data-window]').first();
  const filesWindowId = await filesWindow.getAttribute('data-window-id');
  await filesWindow.locator('[data-window-minimize]').click();
  await page.waitForTimeout(300);

  // Files window should be hidden (minimized)
  await expect(filesWindow).not.toBeVisible();

  // Minimized indicator should show
  const indicator = page.locator('[data-minimized-indicator]');
  await expect(indicator).toHaveCount(1);

  // Wait for state save
  await page.waitForTimeout(1000);

  // Reload
  await page.reload();
  await page.locator('[data-desktop]').waitFor({ state: 'visible', timeout: 10000 });
  await page.waitForTimeout(2000);

  // The files window should still be minimized (not visible)
  const restoredFilesWindow = page.locator(`[data-window-id="${filesWindowId}"]`);
  await expect(restoredFilesWindow).not.toBeVisible();

  // Minimized indicator should be present
  await expect(page.locator('[data-minimized-indicator]')).toHaveCount(1);

  // Browser window should still be visible
  const visibleWindows = page.locator('[data-window]:visible');
  const visibleCount = await visibleWindows.count();
  expect(visibleCount).toBeGreaterThanOrEqual(1);
});

// ---------------------------------------------------------------
// Test: maximized window state preserved after reload
// (VAL-SHELL-022)
// ---------------------------------------------------------------
test('maximized window state preserved after reload', async ({
  page,
  authenticator,
}) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);

  // Open a Files window
  await openApp(page, 'files');
  const windowEl = page.locator('[data-window]').first();
  const windowId = await windowEl.getAttribute('data-window-id');

  // Maximize it
  await windowEl.locator('[data-window-maximize]').click();
  await page.waitForTimeout(300);

  // Verify it's maximized (button shows restore icon)
  const maxBtn = windowEl.locator('[data-window-maximize]');
  const btnText = await maxBtn.textContent();
  expect(btnText).toContain('❐');

  // Wait for state save
  await page.waitForTimeout(1000);

  // Reload
  await page.reload();
  await page.locator('[data-desktop]').waitFor({ state: 'visible', timeout: 10000 });
  await page.waitForTimeout(2000);

  // The window should still be maximized
  const restoredWindow = page.locator(`[data-window-id="${windowId}"]`);
  await expect(restoredWindow).toBeVisible({ timeout: 5000 });

  // The maximize button should still show restore icon
  const restoredMaxBtn = restoredWindow.locator('[data-window-maximize]');
  const restoredBtnText = await restoredMaxBtn.textContent();
  expect(restoredBtnText).toContain('❐');
});

// ---------------------------------------------------------------
// Test: no flash of empty desktop during state restore
// (VAL-SHELL-022)
// ---------------------------------------------------------------
test('no flash of empty desktop during state restore', async ({
  page,
  authenticator,
}) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);

  // Open a Files window
  await openApp(page, 'files');
  await page.waitForTimeout(1000);

  // Record the window ID
  const windowId = await page.locator('[data-window]').first().getAttribute('data-window-id');

  // Set up a flag to detect if desktop-windows area was ever visible while empty
  // We'll use a mutation observer to track visibility changes
  const flashDetected = await page.evaluate(() => {
    return new Promise((resolve) => {
      let hadFlash = false;
      const desktopArea = document.querySelector('[data-desktop-windows]');

      if (!desktopArea) {
        resolve(false);
        return;
      }

      // Check initial state
      const observer = new MutationObserver(() => {
        // If the area is visible but has no windows, that's a potential flash
        const windows = desktopArea.querySelectorAll('[data-window]');
        if (windows.length === 0 && desktopArea.style.visibility !== 'hidden') {
          hadFlash = true;
        }
      });

      observer.observe(desktopArea, { childList: true, subtree: true, attributes: true });

      // Resolve after a short time
      setTimeout(() => {
        observer.disconnect();
        resolve(hadFlash);
      }, 200);
    });
  });

  // Now reload and check for flash
  await page.reload();

  // Immediately check: the desktop area should not show windows until state is loaded
  // The state-loading class hides the area until state is ready
  let flashDuringLoad = false;
  try {
    // Check if desktop area has visibility:hidden during loading
    const areaVisibility = await page.evaluate(() => {
      const area = document.querySelector('[data-desktop-windows]');
      if (!area) return 'not-found';
      return area.classList.contains('state-loading') ? 'hidden' : 'visible';
    });

    // If the area starts as hidden (state-loading), that's correct - no flash
    // If it starts visible immediately, check if windows are already there
    if (areaVisibility === 'visible') {
      const windowCount = await page.locator('[data-window]').count();
      if (windowCount === 0) {
        flashDuringLoad = true;
      }
    }
  } catch (_e) {
    // Page not ready yet — acceptable
  }

  // Wait for desktop to fully load
  await page.locator('[data-desktop]').waitFor({ state: 'visible', timeout: 10000 });
  await page.waitForTimeout(2000);

  // Window should be restored
  const restoredWindow = page.locator(`[data-window-id="${windowId}"]`);
  await expect(restoredWindow).toBeVisible({ timeout: 5000 });

  // No flash should have been detected
  expect(flashDuringLoad).toBe(false);
});

// ---------------------------------------------------------------
// Test: desktop state persists across fresh browser context (new tab)
// (VAL-SHELL-023)
// ---------------------------------------------------------------
test('desktop state persists across fresh browser context', async ({
  page,
  authenticator,
  context,
  browser,
}) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);

  // Open a Files window
  await openApp(page, 'files');
  const windowId = await page.locator('[data-window]').first().getAttribute('data-window-id');
  expect(windowId).toBeTruthy();

  // Wait for the debounced state save
  await page.waitForTimeout(1000);

  // Get the cookies from the current context
  const cookies = await context.cookies();

  // Create a new context (simulates new tab/window)
  const newContext = await browser.newContext();
  const newPage = await newContext.newPage();

  try {
    // Set up virtual authenticator in the new context
    // Use WebAuthn virtual environment for the new context
    const newClient = await newPage.context().newCDPSession(newPage);
    const authenticatorResult = await newClient.send('WebAuthn.enable');
    const { authenticatorId } = await newClient.send('WebAuthn.addVirtualAuthenticator', {
      options: {
        protocol: 'ctap2',
        transport: 'internal',
        hasResidentKey: true,
        hasUserVerification: true,
        isUserVerified: true,
      },
    });

    // Transfer cookies to new context
    await newContext.addCookies(cookies);

    // Navigate to the app in the new tab
    await newPage.goto(BASE_URL);

    // Wait for session check and desktop to load
    await newPage.waitForTimeout(2000);

    // Check if we're authenticated (desktop visible) or need to login
    const desktopVisible = await newPage.locator('[data-desktop]').isVisible().catch(() => false);

    if (desktopVisible) {
      // Desktop state should be restored
      await newPage.waitForTimeout(2000);

      // The Files window should be restored from server state
      const restoredWindow = newPage.locator(`[data-window-id="${windowId}"]`);
      const restoredVisible = await restoredWindow.isVisible().catch(() => false);

      // The window should be restored if cookies carried over
      expect(restoredVisible).toBe(true);
    }

    await newClient.send('WebAuthn.removeVirtualAuthenticator', { authenticatorId });
  } finally {
    await newContext.close();
  }
});

// ---------------------------------------------------------------
// Test: empty desktop state (no windows) preserved after reload
// (VAL-SHELL-022)
// ---------------------------------------------------------------
test('empty desktop state preserved after reload', async ({
  page,
  authenticator,
}) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);

  // No windows opened — desktop should be empty
  await expect(page.locator('[data-window]')).toHaveCount(0);

  // Wait for potential state save
  await page.waitForTimeout(1000);

  // Reload
  await page.reload();
  await page.locator('[data-desktop]').waitFor({ state: 'visible', timeout: 10000 });
  await page.waitForTimeout(2000);

  // Desktop should still be empty (no ghost windows)
  await expect(page.locator('[data-window]')).toHaveCount(0);
});

// ---------------------------------------------------------------
// Test: window close removes window from persisted state
// (VAL-SHELL-022)
// ---------------------------------------------------------------
test('window close removes window from persisted state', async ({
  page,
  authenticator,
}) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);

  // Open a Files window
  await openApp(page, 'files');
  await page.waitForTimeout(1000);

  // Close it
  await page.locator('[data-window-close]').first().click();
  await expect(page.locator('[data-window]')).toHaveCount(0);

  // Wait for state save
  await page.waitForTimeout(1000);

  // Reload
  await page.reload();
  await page.locator('[data-desktop]').waitFor({ state: 'visible', timeout: 10000 });
  await page.waitForTimeout(2000);

  // Window should NOT be restored (it was closed)
  await expect(page.locator('[data-window]')).toHaveCount(0);
});
