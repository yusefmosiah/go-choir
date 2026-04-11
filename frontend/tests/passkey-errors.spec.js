/**
 * Playwright tests for cancelled and failed passkey ceremony states (VAL-FRONTEND-004).
 *
 * These tests verify that:
 * - Cancelled passkey ceremonies keep the user in a retryable guest auth state
 * - Failed passkey ceremonies show visible error/retry feedback
 * - The authenticated shell is never exposed after a cancelled or failed ceremony
 * - The user can retry after a cancelled or failed ceremony
 *
 * Uses the Playwright Chromium virtual-authenticator harness where needed,
 * and manually mocks navigator.credentials for cancel/failure scenarios.
 */
import { test, expect } from '@playwright/test';

const BASE_URL = 'http://localhost:4173';

function uniqueUsername() {
  return `err-test-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;
}

// ---------------------------------------------------------------
// Test: cancelled passkey ceremony shows error and stays in auth
// ---------------------------------------------------------------
test('cancelled passkey ceremony shows error and stays in guest auth UI', async ({
  page,
}) => {
  // Mock navigator.credentials.create to reject with NotAllowedError
  // (simulating user cancellation) before navigating.
  await page.goto(BASE_URL);

  // Install the mock before any WebAuthn calls.
  await page.evaluate(() => {
    Object.defineProperty(navigator, 'credentials', {
      value: {
        create: () => Promise.reject(new DOMException('User cancelled', 'NotAllowedError')),
        get: () => Promise.reject(new DOMException('User cancelled', 'NotAllowedError')),
      },
      configurable: true,
      writable: true,
    });
  });

  // Fill in the username and click the register button.
  const registerView = page.locator('[data-register-view]');
  const usernameInput = registerView.locator('input[type="text"]');
  const username = uniqueUsername();
  await usernameInput.fill(username);

  const submitBtn = registerView.locator('button[type="submit"]');
  await submitBtn.click();

  // The passkey error message should appear.
  const errorEl = page.locator('[data-passkey-error]');
  await expect(errorEl).toBeVisible();
  await expect(errorEl).toContainText('cancelled');

  // The guest auth entry should still be visible (not the shell).
  const authEntry = page.locator('[data-auth-entry]');
  await expect(authEntry).toBeVisible();

  // The shell should NOT be visible.
  const shell = page.locator('[data-shell]');
  await expect(shell).not.toBeVisible();
});

// ---------------------------------------------------------------
// Test: cancelled login passkey ceremony shows error and stays in auth
// ---------------------------------------------------------------
test('cancelled login passkey ceremony shows error and stays in guest auth UI', async ({
  page,
}) => {
  // For the login cancel test, we use the virtual-authenticator to
  // register first so /auth/login/begin can succeed, then mock
  // navigator.credentials.get to reject with NotAllowedError.
  // However, since this test file uses the base @playwright/test
  // without the authenticator fixture, we intercept the login/begin
  // response to simulate a successful begin (with fake challenge data),
  // then mock navigator.credentials.get to reject.
  await page.goto(BASE_URL);

  // Intercept /auth/login/begin to return plausible assertion options
  // so the ceremony reaches the navigator.credentials.get() step.
  await page.route('**/auth/login/begin', (route) => {
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        publicKey: {
          challenge: 'dGVzdC1jaGFsbGVuZ2U',
          rpId: 'localhost',
          allowCredentials: [],
          timeout: 60000,
          userVerification: 'preferred',
        },
      }),
    });
  });

  // Mock navigator.credentials.get to reject with NotAllowedError.
  await page.evaluate(() => {
    const original = navigator.credentials;
    Object.defineProperty(navigator, 'credentials', {
      value: {
        create: original ? original.create.bind(original) : () => Promise.reject(new DOMException('Not implemented', 'NotAllowedError')),
        get: () => Promise.reject(new DOMException('User cancelled', 'NotAllowedError')),
      },
      configurable: true,
      writable: true,
    });
  });

  // Switch to login view.
  const loginToggle = page.locator('[data-login-toggle]');
  await loginToggle.click();

  const loginView = page.locator('[data-login-view]');
  const usernameInput = loginView.locator('input[type="text"]');
  const username = uniqueUsername();
  await usernameInput.fill(username);

  const submitBtn = loginView.locator('button[type="submit"]');
  await submitBtn.click();

  // The passkey error message should appear, containing "cancelled"
  // because the NotAllowedError from navigator.credentials.get maps
  // to the cancelled message.
  const errorEl = page.locator('[data-passkey-error]');
  await expect(errorEl).toBeVisible();
  await expect(errorEl).toContainText('cancelled');

  // Still in guest auth UI.
  const authEntry = page.locator('[data-auth-entry]');
  await expect(authEntry).toBeVisible();

  // Shell not visible.
  const shell = page.locator('[data-shell]');
  await expect(shell).not.toBeVisible();
});

// ---------------------------------------------------------------
// Test: failed passkey ceremony shows error and stays in auth
// ---------------------------------------------------------------
test('failed passkey ceremony shows error and stays in guest auth UI', async ({
  page,
}) => {
  await page.goto(BASE_URL);

  // Mock navigator.credentials.create to reject with a generic error
  // (simulating a ceremony failure, not a user cancellation).
  await page.evaluate(() => {
    Object.defineProperty(navigator, 'credentials', {
      value: {
        create: () => Promise.reject(new Error('WebAuthn operation failed')),
        get: () => Promise.reject(new Error('WebAuthn operation failed')),
      },
      configurable: true,
      writable: true,
    });
  });

  const registerView = page.locator('[data-register-view]');
  const usernameInput = registerView.locator('input[type="text"]');
  const username = uniqueUsername();
  await usernameInput.fill(username);

  const submitBtn = registerView.locator('button[type="submit"]');
  await submitBtn.click();

  // The passkey error message should appear.
  const errorEl = page.locator('[data-passkey-error]');
  await expect(errorEl).toBeVisible();
  await expect(errorEl).toContainText('failed');

  // Still in guest auth UI.
  const authEntry = page.locator('[data-auth-entry]');
  await expect(authEntry).toBeVisible();

  // Shell not visible.
  const shell = page.locator('[data-shell]');
  await expect(shell).not.toBeVisible();
});

// ---------------------------------------------------------------
// Test: user can retry after a cancelled passkey ceremony
// ---------------------------------------------------------------
test('user can retry after a cancelled passkey ceremony', async ({
  page,
}) => {
  await page.goto(BASE_URL);

  // Mock to cancel on the first attempt.
  let callCount = 0;
  await page.evaluate(() => {
    Object.defineProperty(navigator, 'credentials', {
      get value() {
        return {
          create: () => {
            window.__mockCallCount = (window.__mockCallCount || 0) + 1;
            return Promise.reject(new DOMException('User cancelled', 'NotAllowedError'));
          },
          get: () => {
            window.__mockCallCount = (window.__mockCallCount || 0) + 1;
            return Promise.reject(new DOMException('User cancelled', 'NotAllowedError'));
          },
        };
      },
      configurable: true,
    });
  });

  const registerView = page.locator('[data-register-view]');
  const usernameInput = registerView.locator('input[type="text"]');
  const username = uniqueUsername();
  await usernameInput.fill(username);

  const submitBtn = registerView.locator('button[type="submit"]');
  await submitBtn.click();

  // Error should appear.
  const errorEl = page.locator('[data-passkey-error]');
  await expect(errorEl).toBeVisible();

  // The submit button should still be enabled (retryable).
  await expect(submitBtn).toBeEnabled();

  // The form input should still be present and editable.
  await expect(usernameInput).toBeVisible();
  await expect(usernameInput).toBeEditable();
});

// ---------------------------------------------------------------
// Test: switching views clears the passkey error
// ---------------------------------------------------------------
test('switching between register and login views clears the passkey error', async ({
  page,
}) => {
  await page.goto(BASE_URL);

  // Mock to cancel.
  await page.evaluate(() => {
    Object.defineProperty(navigator, 'credentials', {
      value: {
        create: () => Promise.reject(new DOMException('User cancelled', 'NotAllowedError')),
        get: () => Promise.reject(new DOMException('User cancelled', 'NotAllowedError')),
      },
      configurable: true,
      writable: true,
    });
  });

  const registerView = page.locator('[data-register-view]');
  const usernameInput = registerView.locator('input[type="text"]');
  await usernameInput.fill(uniqueUsername());

  const submitBtn = registerView.locator('button[type="submit"]');
  await submitBtn.click();

  // Error should appear.
  const errorEl = page.locator('[data-passkey-error]');
  await expect(errorEl).toBeVisible();

  // Switch to login view.
  const loginToggle = page.locator('[data-login-toggle]');
  await loginToggle.click();

  // Error should be cleared when switching views.
  await expect(errorEl).not.toBeVisible();

  // The login view should be shown without errors.
  const loginView = page.locator('[data-login-view]');
  await expect(loginView).toBeVisible();
});

// ---------------------------------------------------------------
// Test: begin-endpoint failure shows error in auth entry
// ---------------------------------------------------------------
test('auth begin endpoint failure shows error and stays in guest auth UI', async ({
  page,
}) => {
  await page.goto(BASE_URL);

  // Intercept /auth/register/begin to return an error.
  await page.route('**/auth/register/begin', (route) => {
    route.fulfill({
      status: 400,
      contentType: 'application/json',
      body: JSON.stringify({ error: 'Username already taken' }),
    });
  });

  const registerView = page.locator('[data-register-view]');
  const usernameInput = registerView.locator('input[type="text"]');
  const username = uniqueUsername();
  await usernameInput.fill(username);

  const submitBtn = registerView.locator('button[type="submit"]');
  await submitBtn.click();

  // An error should appear (from the begin endpoint failure).
  const errorEl = page.locator('[data-passkey-error]');
  await expect(errorEl).toBeVisible();

  // Still in guest auth UI.
  const authEntry = page.locator('[data-auth-entry]');
  await expect(authEntry).toBeVisible();

  // Shell not visible.
  const shell = page.locator('[data-shell]');
  await expect(shell).not.toBeVisible();
});
