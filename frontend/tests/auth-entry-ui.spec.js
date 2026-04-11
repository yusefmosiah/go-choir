/**
 * Playwright tests for the signed-out auth entry UI (VAL-FRONTEND-001, VAL-FRONTEND-002).
 *
 * These tests verify that:
 * - Signed-out root renders guest auth UI instead of the placeholder-only landing page
 * - Users can reach distinct register and login views
 * - Each guest auth view has a clear primary action to begin the passkey flow
 * - Signed-out initial render does not spam failing protected bootstrap/live-channel calls
 *
 * No virtual authenticator needed — these test the signed-out guest UI only.
 */
import { test, expect } from '@playwright/test';

const BASE_URL = 'http://localhost:4173';

// ---------------------------------------------------------------
// Test: signed-out root shows auth UI, not the old placeholder
// ---------------------------------------------------------------
test('signed-out root shows guest auth UI instead of placeholder', async ({
  page,
}) => {
  // Navigate to root with no auth cookies.
  await page.goto(BASE_URL);

  // The old placeholder only had an <h1> with "go-choir" and a subtitle.
  // The new auth UI must contain the auth entry container.
  const authEntry = page.locator('[data-auth-entry]');
  await expect(authEntry).toBeVisible();

  // The auth entry should have register/login toggles, not just the old
  // static "go-choir" landing.
  const registerToggle = page.locator('[data-register-toggle]');
  const loginToggle = page.locator('[data-login-toggle]');
  await expect(registerToggle).toBeVisible();
  await expect(loginToggle).toBeVisible();
});

// ---------------------------------------------------------------
// Test: guest users can reach distinct register and login views
// ---------------------------------------------------------------
test('guest users can reach both register and login views', async ({ page }) => {
  await page.goto(BASE_URL);

  // There should be controls to switch between register and login views.
  const registerToggle = page.locator('[data-register-toggle]');
  const loginToggle = page.locator('[data-login-toggle]');

  // Both toggles should be present.
  await expect(registerToggle).toBeVisible();
  await expect(loginToggle).toBeVisible();

  // Register view should be visible by default.
  const registerView = page.locator('[data-register-view]');
  await expect(registerView).toBeVisible();

  // Switch to login view.
  await loginToggle.click();
  const loginView = page.locator('[data-login-view]');
  await expect(loginView).toBeVisible();

  // Register view should no longer be visible when login is active.
  await expect(registerView).not.toBeVisible();

  // Switch back to register view.
  await registerToggle.click();
  await expect(registerView).toBeVisible();
  await expect(loginView).not.toBeVisible();
});

// ---------------------------------------------------------------
// Test: each guest auth view has a clear primary action
// ---------------------------------------------------------------
test('register view has a clear primary action to begin passkey flow', async ({
  page,
}) => {
  await page.goto(BASE_URL);

  // Register view is visible by default.
  const registerView = page.locator('[data-register-view]');

  // There should be a primary action (button) to begin passkey registration.
  const registerAction = registerView.locator('button[type="submit"]');
  await expect(registerAction).toBeVisible();
  await expect(registerAction).toBeEnabled();
  await expect(registerAction).toContainText('Passkey');
});

test('login view has a clear primary action to begin passkey flow', async ({
  page,
}) => {
  await page.goto(BASE_URL);

  // Switch to login view.
  const loginToggle = page.locator('[data-login-toggle]');
  await loginToggle.click();

  const loginView = page.locator('[data-login-view]');

  // There should be a primary action (button) to begin passkey login.
  const loginAction = loginView.locator('button[type="submit"]');
  await expect(loginAction).toBeVisible();
  await expect(loginAction).toBeEnabled();
  await expect(loginAction).toContainText('Passkey');
});

// ---------------------------------------------------------------
// Test: signed-out initial render does not spam failing protected calls
// ---------------------------------------------------------------
test('signed-out render does not repeatedly fire failing protected requests', async ({
  page,
}) => {
  const failingProtectedRequests = [];

  // Listen for requests to protected routes.
  page.on('request', (req) => {
    const url = new URL(req.url());
    if (
      url.pathname === '/api/shell/bootstrap' ||
      url.pathname === '/api/ws'
    ) {
      failingProtectedRequests.push({
        url: req.url(),
        method: req.method(),
      });
    }
  });

  // Navigate to root with no auth cookies.
  await page.goto(BASE_URL);

  // Wait a moment for any deferred/eager requests.
  await page.waitForTimeout(1500);

  // No protected requests should have been made while signed out.
  expect(failingProtectedRequests).toHaveLength(0);
});
