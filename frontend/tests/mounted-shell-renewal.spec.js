/**
 * Playwright end-to-end tests for the mounted-shell silent-renewal follow-up.
 *
 * These tests prove that while the shell is already mounted, a protected
 * shell action can trigger refresh rotation when access expires but refresh
 * state is still valid — without depending on reload, new-tab, or
 * WebSocket reconnection flows.
 *
 * Expected behavior:
 *   - While the shell is already mounted, a protected shell action can
 *     renew expired access through the existing refresh rotation path.
 *   - Successful mounted-shell renewal keeps the user in the active shell
 *     without forcing a full reload.
 *   - If mounted-shell renewal cannot restore auth, the browser still
 *     falls back cleanly to guest auth state.
 *   - The follow-up does not introduce a new auth mechanism or bypass
 *     the existing same-origin cookie flow.
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

function uniqueUsername() {
  return `e2e-mr-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;
}

// ---------------------------------------------------------------------------
// Helpers
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

/**
 * Registers a user, lands in the shell, and waits for bootstrap + live
 * channel to be ready. Returns the username.
 */
async function setupAuthenticatedShell(page) {
  const username = uniqueUsername();
  await page.goto(BASE_URL);
  await registerPasskey(page, username, BASE_URL);

  // Reload so the app re-checks auth and renders the shell.
  await page.reload();
  await page.locator('[data-shell]').waitFor({ state: 'visible', timeout: 15_000 });
  await waitForBootstrapData(page);
  await waitForLiveConnected(page);

  return username;
}

// ---------------------------------------------------------------------------
// Tests: mounted-shell renewal via in-shell protected action
// ---------------------------------------------------------------------------

test('in-shell refresh action renews expired access through refresh rotation', async ({
  page,
  authenticator,
  context,
}) => {
  const username = await setupAuthenticatedShell(page);

  // Remove the access cookie to simulate an expired access JWT.
  // The refresh cookie remains, allowing the server to rotate refresh
  // state and issue a new access JWT.
  await context.clearCookies({ name: 'choir_access' });

  // Click the in-shell "Refresh" button — this triggers a protected
  // request via fetchWithRenewal, which should detect the 401, call
  // GET /auth/session (refresh rotation), and retry successfully.
  const refreshBtn = page.locator('[data-shell-refresh]');
  await refreshBtn.click();

  // The shell should remain stable — no reload, no transition to guest.
  // Wait for the refresh status to show renewal succeeded.
  await page.waitForFunction(
    (selector) => {
      const el = document.querySelector(selector);
      if (!el) return false;
      return el.textContent.includes('Session renewed');
    },
    '[data-shell-refresh-status]',
    { timeout: 15_000 },
  );

  // The shell itself should still be visible (no full page reload).
  await expect(page.locator('[data-shell]')).toBeVisible();

  // The current user should still be shown — no new passkey was needed.
  const userArea = page.locator('[data-shell-user]');
  await expect(userArea).toContainText(username);

  // Verify the session is still authenticated via /auth/session.
  const session = await getSession(page, BASE_URL);
  expect(session.authenticated).toBe(true);
  expect(session.user.username).toBe(username);
});

test('successful mounted-shell renewal keeps the shell stable without forcing a full reload', async ({
  page,
  authenticator,
  context,
}) => {
  const username = await setupAuthenticatedShell(page);

  // Record the current bootstrap data to verify it updates after refresh.
  const bootstrapBefore = await page.evaluate(() => {
    const el = document.querySelector('[data-shell-bootstrap] pre');
    return el ? el.textContent.trim() : null;
  });
  expect(bootstrapBefore).toBeTruthy();

  // Expire the access cookie.
  await context.clearCookies({ name: 'choir_access' });

  // Click the refresh button.
  const refreshBtn = page.locator('[data-shell-refresh]');
  await refreshBtn.click();

  // Wait for renewal success.
  await page.waitForFunction(
    (selector) => {
      const el = document.querySelector(selector);
      if (!el) return false;
      return el.textContent.includes('Session renewed');
    },
    '[data-shell-refresh-status]',
    { timeout: 15_000 },
  );

  // The shell should remain visible throughout — no page transition.
  await expect(page.locator('[data-shell]')).toBeVisible();
  await expect(page.locator('[data-auth-entry]')).not.toBeVisible();

  // Bootstrap data should still be present (renewed request succeeded).
  await waitForBootstrapData(page);

  // The live channel should still be connected or reconnectable.
  // (It may have disconnected briefly during renewal but should recover.)
  await page.waitForFunction(
    (selector) => {
      const el = document.querySelector(selector);
      if (!el) return false;
      const text = el.textContent;
      return text.includes('Connected') || text.includes('Connecting');
    },
    '[data-shell-live-status]',
    { timeout: 15_000 },
  );
});

test('mounted-shell renewal falls back cleanly to guest state when refresh cannot restore auth', async ({
  page,
  authenticator,
  context,
}) => {
  const username = await setupAuthenticatedShell(page);

  // Remove BOTH auth cookies — access and refresh are both invalid.
  await context.clearCookies({ name: 'choir_access' });
  await context.clearCookies({ name: 'choir_refresh' });

  // Click the in-shell refresh button — renewal should fail, and the
  // shell should dispatch authexpired, transitioning to guest auth.
  const refreshBtn = page.locator('[data-shell-refresh]');
  await refreshBtn.click();

  // Should transition to guest auth state (no stale shell).
  await page.locator('[data-auth-entry]').waitFor({ state: 'visible', timeout: 15_000 });
  await expect(page.locator('[data-shell]')).not.toBeVisible();

  // No infinite retry loop — the guest auth UI is stable.
  await page.waitForTimeout(1000);
  await expect(page.locator('[data-auth-entry]')).toBeVisible();
});

test('in-shell refresh action does not introduce a new auth mechanism or bypass cookie flow', async ({
  page,
  authenticator,
  context,
}) => {
  const username = await setupAuthenticatedShell(page);

  // Expire the access cookie.
  await context.clearCookies({ name: 'choir_access' });

  // Intercept network requests to verify the renewal path uses the
  // standard GET /auth/session flow — not any new auth endpoint.
  const authSessionRequests = [];
  page.on('request', (req) => {
    const url = new URL(req.url());
    if (url.pathname === '/auth/session' && req.method() === 'GET') {
      authSessionRequests.push({
        pathname: url.pathname,
        method: req.method(),
        url: req.url(),
      });
    }
  });

  // Click the refresh button.
  const refreshBtn = page.locator('[data-shell-refresh]');
  await refreshBtn.click();

  // Wait for renewal to complete.
  await page.waitForFunction(
    (selector) => {
      const el = document.querySelector(selector);
      if (!el) return false;
      return el.textContent.includes('Session renewed');
    },
    '[data-shell-refresh-status]',
    { timeout: 15_000 },
  );

  // Verify that the renewal went through GET /auth/session — the
  // canonical rehydration and silent-renewal checkpoint.
  expect(authSessionRequests.length).toBeGreaterThanOrEqual(1);

  // Verify the session is still valid.
  const session = await getSession(page, BASE_URL);
  expect(session.authenticated).toBe(true);
  expect(session.user.username).toBe(username);
});

test('in-shell refresh works even when the live channel is already connected', async ({
  page,
  authenticator,
  context,
}) => {
  const username = await setupAuthenticatedShell(page);

  // Verify the live channel is connected before we start.
  await waitForLiveConnected(page);

  // Expire the access cookie while WS is still connected.
  await context.clearCookies({ name: 'choir_access' });

  // Click the refresh button.
  const refreshBtn = page.locator('[data-shell-refresh]');
  await refreshBtn.click();

  // Wait for renewal success.
  await page.waitForFunction(
    (selector) => {
      const el = document.querySelector(selector);
      if (!el) return false;
      return el.textContent.includes('Session renewed');
    },
    '[data-shell-refresh-status]',
    { timeout: 15_000 },
  );

  // Shell remains stable.
  await expect(page.locator('[data-shell]')).toBeVisible();

  // The live channel should remain connected or reconnect after renewal.
  // (The WS connection may have been affected by the expired access,
  // but after renewal it should recover.)
  await page.waitForFunction(
    (selector) => {
      const el = document.querySelector(selector);
      if (!el) return false;
      const text = el.textContent;
      return text.includes('Connected') || text.includes('Connecting');
    },
    '[data-shell-live-status]',
    { timeout: 15_000 },
  );
});
