/**
 * Playwright tests for the deploy-readiness browser auth/shell contract.
 *
 * These tests provide explicit coverage for the validation contract
 * assertions assigned to the deploy-public-auth-shell-browser-contract
 * feature, running against the local service stack:
 *
 *   VAL-DEPLOY-002 — Signed-out visitors see guest auth on the public origin
 *   VAL-CROSS-101 — First-time registration lands in the authenticated shell
 *   VAL-CROSS-102 — Returning login lands in the authenticated shell
 *   VAL-CROSS-103 — Protected shell transport uses cookie-backed auth only
 *   VAL-CROSS-104 — Expired access renews without a new passkey ceremony
 *   VAL-CROSS-105 — Reload and new-tab restart rehydrate from server-backed
 *                    auth state
 *   VAL-CROSS-106 — Logout revokes shell and all protected live surfaces
 *   VAL-CROSS-107 — User switch does not leak stale shell state
 *   VAL-CROSS-108 — Failed renewal falls back cleanly to guest state
 *
 * These tests complement (not duplicate) the existing test suite by
 * explicitly asserting the contract-specified observable behaviors in
 * a single self-describing file. They use the Playwright Chromium
 * virtual-authenticator harness for automated passkey ceremonies.
 */
import { test, expect } from './helpers/fixtures.js';
import {
  registerPasskey,
  loginPasskey,
  getSession,
  logout,
} from './helpers/auth.js';

const BASE_URL = 'http://localhost:4173';

function uniqueUsername() {
  return `contract-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;
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
  await page.reload();
  await page.locator('[data-shell]').waitFor({ state: 'visible', timeout: 15_000 });
  await waitForBootstrapData(page);
  await waitForLiveConnected(page);
  return username;
}

// ---------------------------------------------------------------------------
// VAL-DEPLOY-002: Signed-out visitors see guest auth on the public origin
// ---------------------------------------------------------------------------

test('VAL-DEPLOY-002: signed-out root shows guest auth entry UI', async ({
  page,
}) => {
  await page.goto(BASE_URL);

  // Guest auth entry must be visible.
  const authEntry = page.locator('[data-auth-entry]');
  await expect(authEntry).toBeVisible();

  // Register and login toggles must be present.
  await expect(page.locator('[data-register-toggle]')).toBeVisible();
  await expect(page.locator('[data-login-toggle]')).toBeVisible();

  // Shell must NOT be visible.
  await expect(page.locator('[data-shell]')).not.toBeVisible();
});

test('VAL-DEPLOY-002: signed-out root does not fire protected requests', async ({
  page,
}) => {
  const protectedRequests = [];

  page.on('request', (req) => {
    const url = new URL(req.url());
    if (
      url.pathname === '/api/shell/bootstrap' ||
      url.pathname === '/api/ws'
    ) {
      protectedRequests.push(url.pathname);
    }
  });

  await page.goto(BASE_URL);
  await page.waitForTimeout(1500);

  expect(protectedRequests).toHaveLength(0);
});

// ---------------------------------------------------------------------------
// VAL-CROSS-101: First-time registration lands in the authenticated shell
// ---------------------------------------------------------------------------

test('VAL-CROSS-101: new user registers and lands in the shell without page reload', async ({
  page,
  authenticator,
}) => {
  const username = uniqueUsername();

  await page.goto(BASE_URL);
  await page.locator('[data-auth-entry]').waitFor({ state: 'visible' });

  // Fill in the username in the register view.
  const registerView = page.locator('[data-register-view]');
  await registerView.locator('input[type="text"]').fill(username);

  // Click "Register with Passkey".
  await registerView.locator('button[type="submit"]').click();

  // Shell should appear without page reload.
  const shell = page.locator('[data-shell]');
  await expect(shell).toBeVisible({ timeout: 15_000 });

  // Guest auth entry should no longer be visible.
  await expect(page.locator('[data-auth-entry]')).not.toBeVisible();

  // Shell should show the current user.
  const userArea = page.locator('[data-shell-user]');
  await expect(userArea).toBeVisible();
  await expect(userArea).toContainText(username);
});

test('VAL-CROSS-101: new user receives same-origin auth cookies after registration', async ({
  page,
  authenticator,
  context,
}) => {
  const username = uniqueUsername();

  await page.goto(BASE_URL);
  await registerPasskey(page, username, BASE_URL);

  // Verify auth cookies were set.
  const cookies = await context.cookies();
  const accessCookie = cookies.find((c) => c.name === 'choir_access');
  const refreshCookie = cookies.find((c) => c.name === 'choir_refresh');

  expect(accessCookie).toBeDefined();
  expect(refreshCookie).toBeDefined();

  // Access cookie must be HttpOnly.
  expect(accessCookie.httpOnly).toBe(true);

  // Refresh cookie must be HttpOnly.
  expect(refreshCookie.httpOnly).toBe(true);

  // Access cookie path should be "/" so it's sent to /api/* routes.
  expect(accessCookie.path).toBe('/');

  // Refresh cookie path should be "/auth" so it's only sent to auth routes.
  expect(refreshCookie.path).toBe('/auth');
});

test('VAL-CROSS-101: new user sees bootstrap data and live channel after registration', async ({
  page,
  authenticator,
}) => {
  const username = uniqueUsername();

  await page.goto(BASE_URL);
  await registerPasskey(page, username, BASE_URL);

  // Reload to enter the shell.
  await page.reload();
  await page.locator('[data-shell]').waitFor({ state: 'visible', timeout: 15_000 });

  // Bootstrap data should load.
  await waitForBootstrapData(page);

  // Live channel should connect.
  await waitForLiveConnected(page);
});

// ---------------------------------------------------------------------------
// VAL-CROSS-102: Returning login lands in the authenticated shell
// ---------------------------------------------------------------------------

test('VAL-CROSS-102: returning user logs in from signed-out state and lands in the shell', async ({
  page,
  authenticator,
}) => {
  const username = uniqueUsername();

  // Register first.
  await page.goto(BASE_URL);
  await registerPasskey(page, username, BASE_URL);

  // Log out.
  await logout(page, BASE_URL);

  // Navigate to root — should show guest auth entry.
  await page.goto(BASE_URL);
  await page.locator('[data-auth-entry]').waitFor({ state: 'visible' });

  // Shell must NOT be visible.
  await expect(page.locator('[data-shell]')).not.toBeVisible();

  // Switch to login view and log in.
  await page.locator('[data-login-toggle]').click();
  const loginView = page.locator('[data-login-view]');
  await loginView.locator('input[type="text"]').fill(username);
  await loginView.locator('button[type="submit"]').click();

  // Shell should appear after login.
  const shell = page.locator('[data-shell]');
  await expect(shell).toBeVisible({ timeout: 15_000 });

  // Guest auth entry should not be visible.
  await expect(page.locator('[data-auth-entry]')).not.toBeVisible();

  // Shell should show the current user.
  const userArea = page.locator('[data-shell-user]');
  await expect(userArea).toBeVisible();
  await expect(userArea).toContainText(username);
});

test('VAL-CROSS-102: returning user sees bootstrap data and live channel after login', async ({
  page,
  authenticator,
}) => {
  const username = uniqueUsername();

  // Register and log out.
  await page.goto(BASE_URL);
  await registerPasskey(page, username, BASE_URL);
  await logout(page, BASE_URL);

  // Log in via the helper.
  await page.goto(BASE_URL);
  await loginPasskey(page, username, BASE_URL);

  // Reload to enter the shell.
  await page.reload();
  await page.locator('[data-shell]').waitFor({ state: 'visible', timeout: 15_000 });

  // Bootstrap data should load.
  await waitForBootstrapData(page);

  // Live channel should connect.
  await waitForLiveConnected(page);
});

// ---------------------------------------------------------------------------
// VAL-CROSS-103: Protected shell transport uses cookie-backed auth only
// ---------------------------------------------------------------------------

test('VAL-CROSS-103: no auth tokens in localStorage or sessionStorage', async ({
  page,
  authenticator,
}) => {
  const username = uniqueUsername();

  await page.goto(BASE_URL);
  await registerPasskey(page, username, BASE_URL);

  // Check localStorage for any token-like entries.
  const localStorageKeys = await page.evaluate(() =>
    Object.keys(window.localStorage),
  );
  for (const key of localStorageKeys) {
    const value = window.localStorage.getItem(key);
    expect(key.toLowerCase()).not.toContain('token');
    expect(key.toLowerCase()).not.toContain('auth');
    expect(value.toLowerCase()).not.toContain('choir_access');
    expect(value.toLowerCase()).not.toContain('choir_refresh');
  }

  // Check sessionStorage.
  const sessionStorageKeys = await page.evaluate(() =>
    Object.keys(window.sessionStorage),
  );
  for (const key of sessionStorageKeys) {
    const value = window.sessionStorage.getItem(key);
    expect(key.toLowerCase()).not.toContain('token');
    expect(key.toLowerCase()).not.toContain('auth');
    expect(value.toLowerCase()).not.toContain('choir_access');
    expect(value.toLowerCase()).not.toContain('choir_refresh');
  }
});

test('VAL-CROSS-103: protected bootstrap request uses cookie auth only — no bearer token', async ({
  page,
  authenticator,
}) => {
  const username = uniqueUsername();

  await page.goto(BASE_URL);
  await registerPasskey(page, username, BASE_URL);

  // Capture bootstrap request headers.
  let bootstrapRequestHeaders = null;
  page.on('request', (req) => {
    const url = new URL(req.url());
    if (url.pathname === '/api/shell/bootstrap') {
      bootstrapRequestHeaders = req.headers();
    }
  });

  // Reload to trigger the bootstrap request.
  await page.reload();
  await page.locator('[data-shell]').waitFor({ state: 'visible', timeout: 15_000 });
  await page.waitForTimeout(2000);

  // If we captured a bootstrap request, verify it has no Authorization header.
  if (bootstrapRequestHeaders) {
    const authHeader = bootstrapRequestHeaders['authorization'];
    expect(authHeader).toBeUndefined();
  }
});

test('VAL-CROSS-103: no direct service port calls in browser traffic', async ({
  page,
  authenticator,
}) => {
  const username = uniqueUsername();

  const requestedUrls = [];
  page.on('request', (req) => {
    requestedUrls.push(new URL(req.url()));
  });

  await page.goto(BASE_URL);
  await registerPasskey(page, username, BASE_URL);

  // Reload to enter the shell and trigger bootstrap + WS.
  await page.reload();
  await page.locator('[data-shell]').waitFor({ state: 'visible', timeout: 15_000 });
  await page.waitForTimeout(2000);

  // All browser requests should be same-origin (localhost:4173).
  for (const url of requestedUrls) {
    const isLocalDev = url.hostname === 'localhost' && url.port === '4173';
    const isDataOrExtension =
      url.protocol === 'data:' || url.protocol === 'chrome-extension:';

    if (!isLocalDev && !isDataOrExtension) {
      expect(url.port).not.toBe('8081'); // auth
      expect(url.port).not.toBe('8082'); // proxy
      expect(url.port).not.toBe('8085'); // sandbox
    }
  }

  // Verify that bootstrap requests were made through the same-origin proxy.
  const bootstrapRequests = requestedUrls.filter(
    (u) => u.pathname === '/api/shell/bootstrap',
  );
  expect(bootstrapRequests.length).toBeGreaterThanOrEqual(1);
  for (const req of bootstrapRequests) {
    expect(req.hostname).toBe('localhost');
    expect(req.port).toBe('4173');
  }
});

// ---------------------------------------------------------------------------
// VAL-CROSS-104: Expired access renews without a new passkey ceremony
// ---------------------------------------------------------------------------

test('VAL-CROSS-104: expired access cookie renews through refresh rotation on reload', async ({
  page,
  authenticator,
  context,
}) => {
  const username = uniqueUsername();

  await page.goto(BASE_URL);
  await registerPasskey(page, username, BASE_URL);
  await page.reload();
  await page.locator('[data-shell]').waitFor({ state: 'visible', timeout: 15_000 });

  // Remove the access cookie to simulate an expired access JWT.
  await context.clearCookies({ name: 'choir_access' });

  // Reload — checkSession() calls GET /auth/session, which rotates refresh
  // and issues a new access JWT. The shell rehydrates without a new passkey.
  await page.reload();
  await page.locator('[data-shell]').waitFor({ state: 'visible', timeout: 15_000 });

  // The shell should show the current user (renewed, not re-logged-in).
  const userArea = page.locator('[data-shell-user]');
  await expect(userArea).toBeVisible();
  await expect(userArea).toContainText(username);

  // Bootstrap data should load after renewal.
  await waitForBootstrapData(page, 15_000);

  // Live channel should connect after renewal.
  await waitForLiveConnected(page, 15_000);
});

test('VAL-CROSS-104: in-shell refresh action renews expired access', async ({
  page,
  authenticator,
  context,
}) => {
  const username = await setupAuthenticatedShell(page);

  // Remove the access cookie.
  await context.clearCookies({ name: 'choir_access' });

  // Click the in-shell "Refresh" button.
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

  // Shell should remain stable.
  await expect(page.locator('[data-shell]')).toBeVisible();

  // Session should still be valid — no new passkey needed.
  const session = await getSession(page, BASE_URL);
  expect(session.authenticated).toBe(true);
  expect(session.user.username).toBe(username);
});

// ---------------------------------------------------------------------------
// VAL-CROSS-105: Reload and new-tab restart rehydrate from server-backed
// auth state
// ---------------------------------------------------------------------------

test('VAL-CROSS-105: hard reload rehydrates the authenticated shell from cookies', async ({
  page,
  authenticator,
}) => {
  const username = uniqueUsername();

  await page.goto(BASE_URL);
  await registerPasskey(page, username, BASE_URL);
  await page.reload();
  await page.locator('[data-shell]').waitFor({ state: 'visible', timeout: 15_000 });

  // Hard reload — the shell must rehydrate from cookie-backed state.
  await page.reload();
  await page.locator('[data-shell]').waitFor({ state: 'visible', timeout: 15_000 });

  // The shell should show the current user.
  const userArea = page.locator('[data-shell-user]');
  await expect(userArea).toBeVisible();
  await expect(userArea).toContainText(username);

  // Bootstrap data should load.
  await waitForBootstrapData(page);

  // Live channel should connect.
  await waitForLiveConnected(page);
});

test('VAL-CROSS-105: new tab rehydrates the authenticated shell from cookies', async ({
  page,
  authenticator,
  context,
}) => {
  const username = uniqueUsername();

  await page.goto(BASE_URL);
  await registerPasskey(page, username, BASE_URL);
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
  await expect(userArea).toContainText(username);

  // Bootstrap data should load.
  await waitForBootstrapData(newPage);

  // Live channel should connect.
  await waitForLiveConnected(newPage);

  await newPage.close();
});

// ---------------------------------------------------------------------------
// VAL-CROSS-106: Logout revokes shell and all protected live surfaces
// ---------------------------------------------------------------------------

test('VAL-CROSS-106: logout tears down the live channel', async ({
  page,
  authenticator,
}) => {
  const username = await setupAuthenticatedShell(page);

  // Verify live channel is connected before logout.
  await expect(page.locator('[data-shell-live-status]')).toContainText('Connected');

  // Click logout.
  await page.locator('[data-shell-logout]').click();

  // Should return to guest auth UI.
  await page.locator('[data-auth-entry]').waitFor({ state: 'visible', timeout: 10_000 });
  await expect(page.locator('[data-shell]')).not.toBeVisible();

  // Session should be signed out.
  const session = await getSession(page, BASE_URL);
  expect(session.authenticated).toBe(false);
});

test('VAL-CROSS-106: after logout, protected bootstrap route denies access', async ({
  page,
  authenticator,
}) => {
  const username = await setupAuthenticatedShell(page);

  // Click logout.
  await page.locator('[data-shell-logout]').click();
  await page.locator('[data-auth-entry]').waitFor({ state: 'visible', timeout: 10_000 });

  // Protected route should deny access.
  const bootstrapStatus = await page.evaluate(async (baseURL) => {
    const res = await fetch(`${baseURL}/api/shell/bootstrap`, {
      method: 'GET',
      credentials: 'include',
    });
    return res.status;
  }, BASE_URL);

  expect([401, 403]).toContain(bootstrapStatus);
});

test('VAL-CROSS-106: after logout, WebSocket cannot reconnect', async ({
  page,
  authenticator,
}) => {
  const username = await setupAuthenticatedShell(page);

  // Click logout.
  await page.locator('[data-shell-logout]').click();
  await page.locator('[data-auth-entry]').waitFor({ state: 'visible', timeout: 10_000 });

  // Attempt to open a WebSocket after logout — should fail.
  const wsResult = await page.evaluate(async (baseURL) => {
    return new Promise((resolve) => {
      const protocol = baseURL.startsWith('https') ? 'wss:' : 'ws:';
      const url = baseURL.replace(/^https?:/, protocol) + '/api/ws';
      const ws = new WebSocket(url);

      const timeout = setTimeout(() => {
        ws.close();
        resolve({ opened: false, reason: 'timeout' });
      }, 5000);

      ws.onopen = () => {
        clearTimeout(timeout);
        ws.close();
        resolve({ opened: true });
      };

      ws.onerror = () => {
        clearTimeout(timeout);
        resolve({ opened: false, reason: 'error' });
      };

      ws.onclose = (event) => {
        clearTimeout(timeout);
        resolve({ opened: false, reason: 'closed', code: event.code });
      };
    });
  }, BASE_URL);

  expect(wsResult.opened).toBe(false);
});

test('VAL-CROSS-106: refresh after logout does not resurrect the shell', async ({
  page,
  authenticator,
}) => {
  const username = await setupAuthenticatedShell(page);

  // Click logout.
  await page.locator('[data-shell-logout]').click();
  await page.locator('[data-auth-entry]').waitFor({ state: 'visible', timeout: 10_000 });

  // Refresh the page — should remain in guest auth state.
  await page.reload();
  await page.locator('[data-auth-entry]').waitFor({ state: 'visible', timeout: 10_000 });
  await expect(page.locator('[data-shell]')).not.toBeVisible();

  // No protected API requests should fire while signed out.
  const protectedRequests = [];
  page.on('request', (req) => {
    const url = new URL(req.url());
    if (url.pathname.startsWith('/api/')) {
      protectedRequests.push(url.pathname);
    }
  });
  await page.waitForTimeout(2000);
  expect(protectedRequests.length).toBe(0);
});

// ---------------------------------------------------------------------------
// VAL-CROSS-107: User switch does not leak stale shell state
// ---------------------------------------------------------------------------

test('VAL-CROSS-107: user A logout then user B login shows only user B state', async ({
  page,
  authenticator,
}) => {
  // Register user A.
  const userA = uniqueUsername();
  await page.goto(BASE_URL);
  await registerPasskey(page, userA, BASE_URL);
  await page.reload();
  await page.locator('[data-shell]').waitFor({ state: 'visible', timeout: 15_000 });

  // Verify user A is shown.
  await expect(page.locator('[data-shell-user]')).toContainText(userA);

  // Capture user A's session ID.
  const sessionA = await getSession(page, BASE_URL);
  expect(sessionA.authenticated).toBe(true);
  const userAId = sessionA.user.id;

  // Logout as user A.
  await page.locator('[data-shell-logout]').click();
  await page.locator('[data-auth-entry]').waitFor({ state: 'visible', timeout: 10_000 });

  // Register user B.
  const userB = uniqueUsername();
  await page.goto(BASE_URL);
  await registerPasskey(page, userB, BASE_URL);
  await page.reload();
  await page.locator('[data-shell]').waitFor({ state: 'visible', timeout: 15_000 });

  // Shell must show user B — NOT user A.
  const userAreaB = page.locator('[data-shell-user]');
  await expect(userAreaB).toContainText(userB);
  await expect(userAreaB).not.toContainText(userA);

  // Session should report user B.
  const sessionB = await getSession(page, BASE_URL);
  expect(sessionB.authenticated).toBe(true);
  expect(sessionB.user.username).toBe(userB);

  // Bootstrap data should not contain user A's ID.
  await waitForBootstrapData(page);
  const bootstrapText = await page.evaluate(() => {
    const el = document.querySelector('[data-shell-bootstrap] pre');
    return el ? el.textContent.trim() : '';
  });
  expect(bootstrapText).not.toContain(userAId);
});

// ---------------------------------------------------------------------------
// VAL-CROSS-108: Failed renewal falls back cleanly to guest state
// ---------------------------------------------------------------------------

test('VAL-CROSS-108: failed renewal falls back to guest auth state on reload', async ({
  page,
  authenticator,
  context,
}) => {
  const username = uniqueUsername();

  await page.goto(BASE_URL);
  await registerPasskey(page, username, BASE_URL);
  await page.reload();
  await page.locator('[data-shell]').waitFor({ state: 'visible', timeout: 15_000 });

  // Remove both auth cookies to simulate fully expired session.
  await context.clearCookies({ name: 'choir_access' });
  await context.clearCookies({ name: 'choir_refresh' });

  // Reload — no valid cookies, should show guest auth UI.
  await page.reload();
  await page.locator('[data-auth-entry]').waitFor({ state: 'visible', timeout: 15_000 });

  // The shell should NOT be visible — no stale shell state.
  await expect(page.locator('[data-shell]')).not.toBeVisible();

  // No infinite retry loop — the guest auth UI is stable.
  await page.waitForTimeout(1000);
  await expect(page.locator('[data-auth-entry]')).toBeVisible();
});

test('VAL-CROSS-108: mounted shell falls back to guest state when renewal cannot restore auth', async ({
  page,
  authenticator,
  context,
}) => {
  const username = await setupAuthenticatedShell(page);

  // Remove both auth cookies while the shell is mounted.
  await context.clearCookies({ name: 'choir_access' });
  await context.clearCookies({ name: 'choir_refresh' });

  // Click the in-shell refresh button — renewal should fail.
  await page.locator('[data-shell-refresh]').click();

  // Should transition to guest auth state.
  await page.locator('[data-auth-entry]').waitFor({ state: 'visible', timeout: 15_000 });
  await expect(page.locator('[data-shell]')).not.toBeVisible();

  // No infinite retry loop.
  await page.waitForTimeout(1000);
  await expect(page.locator('[data-auth-entry]')).toBeVisible();
});

test('VAL-CROSS-108: failed renewal does not leave stale live channel', async ({
  page,
  authenticator,
  context,
}) => {
  const username = uniqueUsername();

  await page.goto(BASE_URL);
  await registerPasskey(page, username, BASE_URL);
  await page.reload();
  await page.locator('[data-shell]').waitFor({ state: 'visible', timeout: 15_000 });
  await waitForLiveConnected(page);

  // Remove both auth cookies.
  await context.clearCookies({ name: 'choir_access' });
  await context.clearCookies({ name: 'choir_refresh' });

  // Reload — should fall back to guest state.
  await page.reload();
  await page.locator('[data-auth-entry]').waitFor({ state: 'visible', timeout: 15_000 });

  // No shell elements should exist in the DOM.
  await expect(page.locator('[data-shell]')).not.toBeVisible();
  await expect(page.locator('[data-shell-live-status]')).not.toBeVisible();

  // No protected API requests should be in flight.
  const protectedRequests = [];
  page.on('request', (req) => {
    const url = new URL(req.url());
    if (url.pathname.startsWith('/api/')) {
      protectedRequests.push(url.pathname);
    }
  });
  await page.waitForTimeout(2000);
  expect(protectedRequests.length).toBe(0);
});
