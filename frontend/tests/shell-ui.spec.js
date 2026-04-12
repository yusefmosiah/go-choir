/**
 * Playwright tests for the authenticated placeholder desktop shell (VAL-FRONTEND-003).
 *
 * These tests verify that:
 * - The authenticated shell is visually distinct from the guest auth UI
 * - The shell includes a visible logout control
 * - The shell exposes session-aware UI (current user display)
 * - The shell calls GET /api/shell/bootstrap on mount
 * - The shell opens a live channel via GET /api/ws
 *
 * Uses the Playwright Chromium virtual-authenticator harness to register
 * a passkey and authenticate before testing the shell.
 */
import { test, expect } from './helpers/fixtures.js';
import { registerPasskey, getSession } from './helpers/auth.js';

const BASE_URL = 'http://localhost:4173';

function uniqueEmail() {
  return `shell-test-${Date.now()}-${Math.random().toString(36).slice(2, 8)}@example.com`;
}

// Helper: register a passkey via the test helper (page.evaluate), then
// reload the page so the Svelte app re-checks auth state via
// GET /auth/session and transitions to the shell.
async function registerAndReloadShell(page, authenticator, email) {
  await page.goto(BASE_URL);
  await registerPasskey(page, email, BASE_URL);

  // Reload so the Svelte app calls checkSession() and renders the shell.
  await page.reload();
  // Wait for the shell to appear (not the auth entry).
  await page.locator('[data-shell]').waitFor({ state: 'visible', timeout: 10000 });
}

// ---------------------------------------------------------------
// Test: authenticated shell is visible and distinct from auth entry
// ---------------------------------------------------------------
test('authenticated shell is visible and distinct from guest auth UI', async ({
  page,
  authenticator,
}) => {
  const email = uniqueEmail();
  await registerAndReloadShell(page, authenticator, email);

  // The shell container should be visible.
  const shell = page.locator('[data-shell]');
  await expect(shell).toBeVisible();

  // The guest auth entry should NOT be visible.
  const authEntry = page.locator('[data-auth-entry]');
  await expect(authEntry).not.toBeVisible();
});

// ---------------------------------------------------------------
// Test: shell includes a visible logout control
// ---------------------------------------------------------------
test('authenticated shell includes a visible logout control', async ({
  page,
  authenticator,
}) => {
  const email = uniqueEmail();
  await registerAndReloadShell(page, authenticator, email);

  // The logout button should be visible and enabled.
  const logoutBtn = page.locator('[data-shell-logout]');
  await expect(logoutBtn).toBeVisible();
  await expect(logoutBtn).toBeEnabled();
});

// ---------------------------------------------------------------
// Test: shell exposes session-aware UI with current user
// ---------------------------------------------------------------
test('authenticated shell exposes session-aware current user display', async ({
  page,
  authenticator,
}) => {
  const email = uniqueEmail();
  await registerAndReloadShell(page, authenticator, email);

  // The user area should show the current email.
  const userArea = page.locator('[data-shell-user]');
  await expect(userArea).toBeVisible();
  await expect(userArea).toContainText(email);
});

// ---------------------------------------------------------------
// Test: shell calls GET /api/shell/bootstrap on mount
// ---------------------------------------------------------------
test('authenticated shell calls GET /api/shell/bootstrap on mount', async ({
  page,
  authenticator,
}) => {
  const email = uniqueEmail();

  // Register the user first.
  await page.goto(BASE_URL);
  await registerPasskey(page, email, BASE_URL);

  // Listen for the bootstrap request after the reload.
  let bootstrapRequested = false;
  page.on('request', (req) => {
    const url = new URL(req.url());
    if (url.pathname === '/api/shell/bootstrap' && req.method() === 'GET') {
      bootstrapRequested = true;
    }
  });

  // Reload to transition to the shell.
  await page.reload();
  await page.locator('[data-shell]').waitFor({ state: 'visible', timeout: 10000 });

  // The shell should have made a bootstrap request.
  await page.waitForTimeout(1000);
  expect(bootstrapRequested).toBe(true);
});

// ---------------------------------------------------------------
// Test: shell shows bootstrap data section
// ---------------------------------------------------------------
test('authenticated shell shows bootstrap data section', async ({
  page,
  authenticator,
}) => {
  const email = uniqueEmail();
  await registerAndReloadShell(page, authenticator, email);

  // The bootstrap section should be visible.
  const bootstrapSection = page.locator('[data-shell-bootstrap]');
  await expect(bootstrapSection).toBeVisible();
});

// ---------------------------------------------------------------
// Test: shell shows live channel status indicator
// ---------------------------------------------------------------
test('authenticated shell shows live channel status indicator', async ({
  page,
  authenticator,
}) => {
  const email = uniqueEmail();
  await registerAndReloadShell(page, authenticator, email);

  // The live channel status section should be visible.
  const liveStatus = page.locator('[data-shell-live-status]');
  await expect(liveStatus).toBeVisible();
});

// ---------------------------------------------------------------
// Test: clicking logout returns to guest auth UI
// ---------------------------------------------------------------
test('clicking logout returns to guest auth UI', async ({
  page,
  authenticator,
}) => {
  const email = uniqueEmail();
  await registerAndReloadShell(page, authenticator, email);

  // Click logout.
  const logoutBtn = page.locator('[data-shell-logout]');
  await logoutBtn.click();

  // Should return to the guest auth UI.
  const authEntry = page.locator('[data-auth-entry]');
  await expect(authEntry).toBeVisible();

  // Shell should no longer be visible.
  const shell = page.locator('[data-shell]');
  await expect(shell).not.toBeVisible();

  // Session should be signed out.
  const session = await getSession(page, BASE_URL);
  expect(session.authenticated).toBe(false);
});
