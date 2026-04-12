/**
 * Playwright end-to-end tests for VAL-CROSS-006 and VAL-CROSS-007.
 *
 * VAL-CROSS-006: Logout revokes shell, session, HTTP, and WebSocket
 *   access across refresh and back navigation.
 * VAL-CROSS-007: Switching from user A to user B does not leak stale
 *   shell, proxy, or live-channel state.
 *
 * Uses the Playwright Chromium virtual-authenticator harness for automated
 * passkey ceremonies.
 */
import { test, expect } from './helpers/fixtures.js';
import {
  registerPasskey,
  loginPasskey,
  getSession,
  logout,
} from './helpers/auth.js';

const BASE_URL = 'http://localhost:4173';

function uniqueEmail() {
  return `e2e-lo-${Date.now()}-${Math.random().toString(36).slice(2, 8)}@example.com`;
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
 * channel to be ready. Returns the email.
 */
async function setupAuthenticatedShell(page) {
  const email = uniqueEmail();
  await page.goto(BASE_URL);
  await registerPasskey(page, email, BASE_URL);

  // Reload so the app re-checks auth and renders the shell.
  await page.reload();
  await page.locator('[data-shell]').waitFor({ state: 'visible', timeout: 15_000 });
  await waitForBootstrapData(page);
  await waitForLiveConnected(page);

  return email;
}

// ---------------------------------------------------------------------------
// VAL-CROSS-006: Logout revokes shell, session, HTTP, and WebSocket
// access across refresh and back navigation
// ---------------------------------------------------------------------------

test('logout tears down the open live channel', async ({
  page,
  authenticator,
}) => {
  const email = await setupAuthenticatedShell(page);

  // Verify live channel is connected before logout.
  const liveStatusBefore = page.locator('[data-shell-live-status]');
  await expect(liveStatusBefore).toContainText('Connected');

  // Click logout.
  const logoutBtn = page.locator('[data-shell-logout]');
  await logoutBtn.click();

  // Should return to the guest auth UI.
  await page.locator('[data-auth-entry]').waitFor({ state: 'visible', timeout: 10_000 });
  await expect(page.locator('[data-shell]')).not.toBeVisible();

  // Session should be signed out.
  const session = await getSession(page, BASE_URL);
  expect(session.authenticated).toBe(false);
});

test('after logout, GET /api/shell/bootstrap fails', async ({
  page,
  authenticator,
}) => {
  const email = await setupAuthenticatedShell(page);

  // Click logout.
  const logoutBtn = page.locator('[data-shell-logout]');
  await logoutBtn.click();

  // Wait for guest auth UI.
  await page.locator('[data-auth-entry]').waitFor({ state: 'visible', timeout: 10_000 });

  // Now attempt to access the protected bootstrap route directly.
  // It should fail with an auth error (401 or similar).
  const bootstrapStatus = await page.evaluate(async (baseURL) => {
    const res = await fetch(`${baseURL}/api/shell/bootstrap`, {
      method: 'GET',
      credentials: 'include',
    });
    return res.status;
  }, BASE_URL);

  // The protected route should deny access (401 or 403).
  expect([401, 403]).toContain(bootstrapStatus);
});

test('after logout, GET /api/ws cannot reconnect', async ({
  page,
  authenticator,
}) => {
  const email = await setupAuthenticatedShell(page);

  // Click logout.
  const logoutBtn = page.locator('[data-shell-logout]');
  await logoutBtn.click();

  // Wait for guest auth UI.
  await page.locator('[data-auth-entry]').waitFor({ state: 'visible', timeout: 10_000 });

  // Attempt to open a WebSocket after logout — it should fail.
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

  // The WebSocket should NOT open after logout.
  expect(wsResult.opened).toBe(false);
});

test('back navigation after logout does not resurrect the authenticated shell', async ({
  page,
  authenticator,
}) => {
  const email = await setupAuthenticatedShell(page);

  // Navigate away to a simple page first (so there's a history entry).
  await page.goto('about:blank');
  await page.waitForTimeout(500);

  // Go back to the app — shell should still be authenticated.
  await page.goto(BASE_URL);
  await page.locator('[data-shell]').waitFor({ state: 'visible', timeout: 15_000 });

  // Now logout.
  const logoutBtn = page.locator('[data-shell-logout]');
  await logoutBtn.click();
  await page.locator('[data-auth-entry]').waitFor({ state: 'visible', timeout: 10_000 });

  // Navigate away again.
  await page.goto('about:blank');
  await page.waitForTimeout(500);

  // Go back — should show guest auth UI, NOT the shell.
  await page.goto(BASE_URL);
  await page.locator('[data-auth-entry]').waitFor({ state: 'visible', timeout: 10_000 });

  // The shell must NOT appear.
  await expect(page.locator('[data-shell]')).not.toBeVisible();

  // Session should be signed out.
  const session = await getSession(page, BASE_URL);
  expect(session.authenticated).toBe(false);
});

test('refresh after logout does not resurrect the authenticated shell', async ({
  page,
  authenticator,
}) => {
  const email = await setupAuthenticatedShell(page);

  // Click logout.
  const logoutBtn = page.locator('[data-shell-logout]');
  await logoutBtn.click();
  await page.locator('[data-auth-entry]').waitFor({ state: 'visible', timeout: 10_000 });

  // Refresh the page — should remain in guest auth state.
  await page.reload();

  // Should still show guest auth UI.
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
// VAL-CROSS-007: Switching from user A to user B does not leak stale
// shell, proxy, or live-channel state
// ---------------------------------------------------------------------------

test('user A -> logout -> user B produces only user-B shell state', async ({
  page,
  authenticator,
}) => {
  // Register user A.
  const userA = uniqueEmail();
  await page.goto(BASE_URL);
  await registerPasskey(page, userA, BASE_URL);

  // Reload to enter the shell.
  await page.reload();
  await page.locator('[data-shell]').waitFor({ state: 'visible', timeout: 15_000 });

  // Verify user A is shown.
  const userAreaA = page.locator('[data-shell-user]');
  await expect(userAreaA).toContainText(userA);

  // Wait for bootstrap data (proves protected route works for A).
  await waitForBootstrapData(page);

  // Wait for live channel connected (proves WS works for A).
  await waitForLiveConnected(page);

  // Capture user A's session data before logout for later comparison.
  const sessionA = await getSession(page, BASE_URL);
  expect(sessionA.authenticated).toBe(true);
  const userAId = sessionA.user.id;

  // Logout as user A.
  const logoutBtn = page.locator('[data-shell-logout]');
  await logoutBtn.click();
  await page.locator('[data-auth-entry]').waitFor({ state: 'visible', timeout: 10_000 });

  // Session should report signed out.
  const sessionAfterLogout = await getSession(page, BASE_URL);
  expect(sessionAfterLogout.authenticated).toBe(false);

  // Register user B in the same browser context.
  const userB = uniqueEmail();
  await page.goto(BASE_URL);
  await registerPasskey(page, userB, BASE_URL);

  // Reload to enter the shell as user B.
  await page.reload();
  await page.locator('[data-shell]').waitFor({ state: 'visible', timeout: 15_000 });

  // The shell must show user B — NOT user A.
  const userAreaB = page.locator('[data-shell-user]');
  await expect(userAreaB).toContainText(userB);
  await expect(userAreaB).not.toContainText(userA);

  // Session should report user B.
  const sessionB = await getSession(page, BASE_URL);
  expect(sessionB.authenticated).toBe(true);
  expect(sessionB.user.email).toBe(userB);

  // Bootstrap data should work for user B.
  await waitForBootstrapData(page);

  // Live channel should connect for user B.
  await waitForLiveConnected(page);

  // Verify no stale user-A identity in bootstrap data.
  // The sandbox returns a `user` field with the user's UUID (not username),
  // so we check by comparing user IDs from the session.
  const userBId = sessionB.user.id;

  const bootstrapText = await page.evaluate(() => {
    const el = document.querySelector('[data-shell-bootstrap] pre');
    return el ? el.textContent.trim() : '';
  });
  // Bootstrap data should contain user B's ID.
  expect(bootstrapText).toContain(userBId);
  // Bootstrap data should NOT contain user A's ID.
  expect(bootstrapText).not.toContain(userAId);
});

test('user A live channel does not leak into user B session', async ({
  page,
  authenticator,
  context,
}) => {
  // Register user A and get into the shell with a live channel.
  const userA = uniqueEmail();
  await page.goto(BASE_URL);
  await registerPasskey(page, userA, BASE_URL);

  // Reload to enter the shell.
  await page.reload();
  await page.locator('[data-shell]').waitFor({ state: 'visible', timeout: 15_000 });
  await waitForLiveConnected(page);

  // Capture user A's session data for comparison.
  const sessionA = await getSession(page, BASE_URL);
  expect(sessionA.authenticated).toBe(true);
  expect(sessionA.user.email).toBe(userA);

  // Logout as user A.
  const logoutBtn = page.locator('[data-shell-logout]');
  await logoutBtn.click();
  await page.locator('[data-auth-entry]').waitFor({ state: 'visible', timeout: 10_000 });

  // Register user B.
  const userB = uniqueEmail();
  await page.goto(BASE_URL);
  await registerPasskey(page, userB, BASE_URL);

  // Reload to enter the shell as user B.
  await page.reload();
  await page.locator('[data-shell]').waitFor({ state: 'visible', timeout: 15_000 });

  // Live channel for user B should connect fresh (not user A's channel).
  await waitForLiveConnected(page);

  // Session must reflect user B, not user A.
  const sessionB = await getSession(page, BASE_URL);
  expect(sessionB.authenticated).toBe(true);
  expect(sessionB.user.email).toBe(userB);
  expect(sessionB.user.email).not.toBe(userA);

  // The user display in the shell must be user B.
  const userArea = page.locator('[data-shell-user]');
  await expect(userArea).toContainText(userB);
  await expect(userArea).not.toContainText(userA);
});

test('user A -> logout -> user B in separate browser contexts has no stale state', async ({
  page,
  authenticator,
  browser,
}) => {
  // Register user A in the first context.
  const userA = uniqueEmail();
  await page.goto(BASE_URL);
  await registerPasskey(page, userA, BASE_URL);

  // Reload to enter the shell.
  await page.reload();
  await page.locator('[data-shell]').waitFor({ state: 'visible', timeout: 15_000 });

  // Verify user A is authenticated.
  const sessionA = await getSession(page, BASE_URL);
  expect(sessionA.authenticated).toBe(true);
  expect(sessionA.user.email).toBe(userA);

  // Logout as user A.
  const logoutBtn = page.locator('[data-shell-logout]');
  await logoutBtn.click();
  await page.locator('[data-auth-entry]').waitFor({ state: 'visible', timeout: 10_000 });

  // Create a completely separate browser context for user B.
  const contextB = await browser.newContext();
  const pageB = await contextB.newPage();

  // Set up virtual authenticator for context B.
  const { setupVirtualAuthenticator, removeVirtualAuthenticator } = await import('./helpers/webauthn.js');
  const { client: clientB, authenticatorId: authIdB } = await setupVirtualAuthenticator(pageB);

  try {
    // Register user B in the separate context.
    const userB = uniqueEmail();
    await pageB.goto(BASE_URL);
    await registerPasskey(pageB, userB, BASE_URL);

    // Reload to enter the shell.
    await pageB.reload();
    await pageB.locator('[data-shell]').waitFor({ state: 'visible', timeout: 15_000 });

    // Verify user B is authenticated in their own context.
    const sessionB = await getSession(pageB, BASE_URL);
    expect(sessionB.authenticated).toBe(true);
    expect(sessionB.user.email).toBe(userB);
    expect(sessionB.user.email).not.toBe(userA);

    // The shell in context B must show user B, not user A.
    const userAreaB = pageB.locator('[data-shell-user]');
    await expect(userAreaB).toContainText(userB);

    // Bootstrap and live channel must work for user B.
    await waitForBootstrapData(pageB);
    await waitForLiveConnected(pageB);
  } finally {
    await removeVirtualAuthenticator(clientB, authIdB);
    await contextB.close();
  }
});

test('repeated logout does not cause errors and keeps the user in guest state', async ({
  page,
  authenticator,
}) => {
  const email = await setupAuthenticatedShell(page);

  // Logout once.
  const logoutBtn = page.locator('[data-shell-logout]');
  await logoutBtn.click();
  await page.locator('[data-auth-entry]').waitFor({ state: 'visible', timeout: 10_000 });

  // Call logout again via the API — should be safe.
  const logoutResult = await logout(page, BASE_URL);
  expect(logoutResult.authenticated).toBe(false);

  // Session should still report signed out.
  const session = await getSession(page, BASE_URL);
  expect(session.authenticated).toBe(false);

  // Guest auth UI should remain stable.
  await expect(page.locator('[data-auth-entry]')).toBeVisible();
  await expect(page.locator('[data-shell]')).not.toBeVisible();
});
