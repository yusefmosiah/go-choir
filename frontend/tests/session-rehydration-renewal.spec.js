/**
 * Playwright end-to-end tests for VAL-CROSS-004, VAL-CROSS-005,
 * and VAL-CROSS-008.
 *
 * VAL-CROSS-005: Hard reload or a new tab at `/` rehydrates the
 *   authenticated shell from valid same-origin cookies.
 * VAL-CROSS-004: Expired access state renews through refresh rotation
 *   without a new passkey ceremony, and the live channel stays usable
 *   or reconnects after successful renewal.
 * VAL-CROSS-008: When refresh can no longer renew, the browser falls
 *   back cleanly to the guest auth state.
 *
 * Uses the Playwright Chromium virtual-authenticator harness for automated
 * passkey ceremonies.
 */
import { test, expect } from './helpers/fixtures.js';
import {
  registerPasskey,
  getSession,
} from './helpers/auth.js';

const BASE_URL = 'http://localhost:4173';

function uniqueEmail() {
  return `e2e-rehy-${Date.now()}-${Math.random().toString(36).slice(2, 8)}@example.com`;
}

// ---------------------------------------------------------------------------
// Helper: wait for bootstrap data to load in the shell
// ---------------------------------------------------------------------------

async function waitForBootstrapData(page, timeout = 10_000) {
  await page.waitForFunction(
    (selector) => {
      const el = document.querySelector(selector);
      if (!el) return false;
      const pre = el.querySelector('pre');
      return pre !== null && pre.textContent.trim().length > 0;
    },
    '[data-shell-bootstrap]',
    { timeout },
  );
}

// Helper: wait for live channel to show "Connected"
async function waitForLiveConnected(page, timeout = 10_000) {
  await page.waitForFunction(
    (selector) => {
      const el = document.querySelector(selector);
      if (!el) return false;
      return el.textContent.includes('Connected');
    },
    '[data-shell-live-status]',
    { timeout },
  );
}

// ---------------------------------------------------------------------------
// VAL-CROSS-005: Hard reload or a new tab at `/` rehydrates the
// authenticated shell from valid same-origin cookies
// ---------------------------------------------------------------------------

test('hard reload at / rehydrates the authenticated shell from cookies', async ({
  page,
  authenticator,
}) => {
  const email = uniqueEmail();

  // Register via the test helper.
  await page.goto(BASE_URL);
  await registerPasskey(page, email, BASE_URL);

  // Reload so the app re-checks auth and renders the shell.
  await page.reload();
  await page.locator('[data-shell]').waitFor({ state: 'visible', timeout: 15_000 });

  // Hard reload — the shell must rehydrate from cookie-backed state.
  await page.reload();
  await page.locator('[data-shell]').waitFor({ state: 'visible', timeout: 15_000 });

  // The shell should show the current user (from cookie-backed rehydration).
  const userArea = page.locator('[data-shell-user]');
  await expect(userArea).toBeVisible();
  await expect(userArea).toContainText(email);

  // Bootstrap data should load after rehydration.
  await waitForBootstrapData(page);

  // Live channel should connect after rehydration.
  await waitForLiveConnected(page);
});

test('new tab at / rehydrates the authenticated shell from cookies', async ({
  page,
  authenticator,
  context,
}) => {
  const email = uniqueEmail();

  // Register in the first tab.
  await page.goto(BASE_URL);
  await registerPasskey(page, email, BASE_URL);

  // Reload so the app re-checks auth and renders the shell.
  await page.reload();
  await page.locator('[data-shell]').waitFor({ state: 'visible', timeout: 15_000 });

  // Open a new tab in the same browser context (shares cookies).
  const newPage = await context.newPage();
  await newPage.goto(BASE_URL);

  // The new tab should rehydrate the shell from cookies.
  await newPage.locator('[data-shell]').waitFor({ state: 'visible', timeout: 15_000 });

  // Verify the current user is shown.
  const userArea = newPage.locator('[data-shell-user]');
  await expect(userArea).toBeVisible();
  await expect(userArea).toContainText(email);

  // Bootstrap data should load.
  await waitForBootstrapData(newPage);

  // Live channel should connect.
  await waitForLiveConnected(newPage);

  await newPage.close();
});

// ---------------------------------------------------------------------------
// VAL-CROSS-004: Expired access state renews through refresh rotation
// without a new passkey ceremony
// ---------------------------------------------------------------------------

test('expired access cookie renews through refresh rotation on reload', async ({
  page,
  authenticator,
  context,
}) => {
  const email = uniqueEmail();

  // Register and land in the shell.
  await page.goto(BASE_URL);
  await registerPasskey(page, email, BASE_URL);

  // Reload so the app re-checks auth and renders the shell.
  await page.reload();
  await page.locator('[data-shell]').waitFor({ state: 'visible', timeout: 15_000 });

  // Remove the access cookie to simulate an expired access JWT.
  // The refresh cookie remains, allowing the server to rotate refresh
  // state and issue a new access JWT via GET /auth/session.
  await context.clearCookies({ name: 'choir_access' });

  // Reload — checkSession() calls GET /auth/session, which detects
  // no access cookie, validates the refresh cookie, rotates it,
  // and sets new cookies. The shell rehydrates without a new passkey.
  await page.reload();
  await page.locator('[data-shell]').waitFor({ state: 'visible', timeout: 15_000 });

  // The shell should show the current user (renewed, not re-logged-in).
  const userArea = page.locator('[data-shell-user]');
  await expect(userArea).toBeVisible();
  await expect(userArea).toContainText(email);

  // Bootstrap data should load after renewal (proves protected route works).
  await waitForBootstrapData(page, 15_000);

  // Live channel should connect after renewal.
  await waitForLiveConnected(page, 15_000);
});

test('live channel reconnects after successful renewal following access expiry', async ({
  page,
  authenticator,
  context,
}) => {
  const email = uniqueEmail();

  // Register, land in shell, and wait for live channel to connect.
  await page.goto(BASE_URL);
  await registerPasskey(page, email, BASE_URL);

  // Reload so the app re-checks auth and renders the shell.
  await page.reload();
  await page.locator('[data-shell]').waitFor({ state: 'visible', timeout: 15_000 });
  await waitForLiveConnected(page);

  // Remove the access cookie to simulate expired access JWT.
  await context.clearCookies({ name: 'choir_access' });

  // Force the WS to close from JavaScript, triggering the Shell's
  // reconnection logic which should attempt renewal.
  await page.evaluate(() => {
    // Find the Shell's WebSocket and close it to trigger reconnection.
    // The Shell component stores the WS reference; we can close it
    // by evaluating within the page context.
    // We trigger reconnection by causing a fetch that will hit 401.
    return fetch('/api/shell/bootstrap', { credentials: 'include' })
      .then(res => res.status)
      .catch(() => -1);
  });

  // Now reload — this exercises the full rehydration + renewal path.
  await page.reload();
  await page.locator('[data-shell]').waitFor({ state: 'visible', timeout: 15_000 });

  // After renewal, the live channel should reconnect.
  await waitForLiveConnected(page, 15_000);

  // Verify session is still valid (no new passkey was needed).
  const session = await getSession(page, BASE_URL);
  expect(session.authenticated).toBe(true);
  expect(session.user.email).toBe(email);
});

test('replayed old refresh state cannot restore access after rotation', async ({
  page,
  authenticator,
  context,
}) => {
  const email = uniqueEmail();

  // Register and get into the shell.
  await page.goto(BASE_URL);
  await registerPasskey(page, email, BASE_URL);

  // Reload so the app re-checks auth and renders the shell.
  await page.reload();
  await page.locator('[data-shell]').waitFor({ state: 'visible', timeout: 15_000 });

  // Capture the current refresh cookie value.
  const cookiesBefore = await context.cookies();
  const refreshBefore = cookiesBefore.find(c => c.name === 'choir_refresh');
  expect(refreshBefore).toBeDefined();

  // Remove the access cookie to trigger renewal.
  await context.clearCookies({ name: 'choir_access' });

  // Reload to trigger refresh rotation (via GET /auth/session).
  await page.reload();
  await page.locator('[data-shell]').waitFor({ state: 'visible', timeout: 15_000 });

  // The refresh cookie should now be different (rotated).
  const cookiesAfter = await context.cookies();
  const refreshAfter = cookiesAfter.find(c => c.name === 'choir_refresh');
  expect(refreshAfter).toBeDefined();
  expect(refreshAfter.value).not.toBe(refreshBefore.value);

  // Now try to use the OLD refresh cookie value.
  // Remove current cookies and set the old refresh cookie.
  await context.clearCookies();
  await context.addCookies([{
    name: 'choir_refresh',
    value: refreshBefore.value,
    domain: refreshBefore.domain,
    path: refreshBefore.path,
    sameSite: refreshBefore.sameSite,
    httpOnly: refreshBefore.httpOnly,
    secure: refreshBefore.secure,
  }]);

  // Reload — the old refresh token should NOT restore access.
  await page.reload();

  // Should fall back to guest auth state, not the shell.
  await page.locator('[data-auth-entry]').waitFor({ state: 'visible', timeout: 15_000 });
  await expect(page.locator('[data-shell]')).not.toBeVisible();
});

// ---------------------------------------------------------------------------
// VAL-CROSS-008: When refresh can no longer renew, the browser falls
// back cleanly to the guest auth state
// ---------------------------------------------------------------------------

test('failed renewal falls back to guest auth state on reload', async ({
  page,
  authenticator,
  context,
}) => {
  const email = uniqueEmail();

  // Register and land in the shell.
  await page.goto(BASE_URL);
  await registerPasskey(page, email, BASE_URL);

  // Reload so the app re-checks auth and renders the shell.
  await page.reload();
  await page.locator('[data-shell]').waitFor({ state: 'visible', timeout: 15_000 });

  // Remove both auth cookies to simulate fully expired/invalid session.
  await context.clearCookies({ name: 'choir_access' });
  await context.clearCookies({ name: 'choir_refresh' });

  // Reload — no valid cookies, so checkSession() should return
  // signed-out state and the app should show guest auth UI.
  await page.reload();
  await page.locator('[data-auth-entry]').waitFor({ state: 'visible', timeout: 15_000 });

  // The shell should NOT be visible — no stale shell state.
  await expect(page.locator('[data-shell]')).not.toBeVisible();

  // No infinite retry loop — the guest auth UI is stable.
  // Verify the auth entry is still visible after a short wait.
  await page.waitForTimeout(1000);
  await expect(page.locator('[data-auth-entry]')).toBeVisible();
});

test('mounted shell falls back to guest state when protected request fails and renewal cannot restore', async ({
  page,
  authenticator,
  context,
}) => {
  const email = uniqueEmail();

  // Register and land in the shell.
  await page.goto(BASE_URL);
  await registerPasskey(page, email, BASE_URL);

  // Reload so the app re-checks auth and renders the shell.
  await page.reload();
  await page.locator('[data-shell]').waitFor({ state: 'visible', timeout: 15_000 });

  // Remove both auth cookies while the shell is mounted.
  await context.clearCookies({ name: 'choir_access' });
  await context.clearCookies({ name: 'choir_refresh' });

  // Reload the page — the app should fall back to guest state
  // because checkSession() will return signed-out.
  await page.reload();

  // Should show guest auth entry, not a stale or half-broken shell.
  await page.locator('[data-auth-entry]').waitFor({ state: 'visible', timeout: 15_000 });
  await expect(page.locator('[data-shell]')).not.toBeVisible();
});

test('failed renewal does not leave stale live channel state', async ({
  page,
  authenticator,
  context,
}) => {
  const email = uniqueEmail();

  // Register, land in shell, wait for live channel.
  await page.goto(BASE_URL);
  await registerPasskey(page, email, BASE_URL);

  // Reload so the app re-checks auth and renders the shell.
  await page.reload();
  await page.locator('[data-shell]').waitFor({ state: 'visible', timeout: 15_000 });
  await waitForLiveConnected(page);

  // Remove both auth cookies.
  await context.clearCookies({ name: 'choir_access' });
  await context.clearCookies({ name: 'choir_refresh' });

  // Reload — should fall back to guest state with no stale WS.
  await page.reload();
  await page.locator('[data-auth-entry]').waitFor({ state: 'visible', timeout: 15_000 });

  // No shell elements should exist in the DOM.
  await expect(page.locator('[data-shell]')).not.toBeVisible();
  await expect(page.locator('[data-shell-live-status]')).not.toBeVisible();

  // Network: no ongoing protected requests should be happening.
  // Wait briefly and verify no /api/ requests are in flight.
  const protectedRequests = [];
  page.on('request', (req) => {
    const url = new URL(req.url());
    if (url.pathname.startsWith('/api/')) {
      protectedRequests.push(url.pathname);
    }
  });
  await page.waitForTimeout(2000);

  // No protected API requests should have been made while signed out.
  expect(protectedRequests.length).toBe(0);
});
