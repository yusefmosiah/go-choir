/**
 * Custom Playwright test fixtures for go-choir WebAuthn passkey tests.
 *
 * Extends the base test with a virtual authenticator fixture that is
 * automatically set up before each test and torn down afterwards.
 * This ensures clean virtual-authenticator state between tests.
 *
 * Usage:
 *   import { test, expect } from './helpers/fixtures';
 *   test('my test', async ({ page, authenticator }) => { ... });
 *   test('desktop test', async ({ desktopSession }) => { ... });
 */
import { test as base, expect } from '@playwright/test';
import {
  setupVirtualAuthenticator,
  removeVirtualAuthenticator,
} from './webauthn.js';
import { createAuthenticatedState } from './auth-state.js';

/**
 * @typedef {object} AuthenticatorFixture
 * @property {import('@playwright/test').CDPSession} client - CDP session for the page.
 * @property {string} authenticatorId - The virtual authenticator ID.
 */

/**
 * Extended test fixture with a scoped virtual authenticator.
 */
export const test = base.extend({
  /** Virtual authenticator set up for the test's browser context. */
  authenticator: [
    async ({ page }, use) => {
      const { client, authenticatorId } = await setupVirtualAuthenticator(page);
      await use({ client, authenticatorId });
      await removeVirtualAuthenticator(client, authenticatorId);
    },
    { scope: 'test' },
  ],
  authenticatedState: [
    async ({ browser }, use) => {
      const state = await createAuthenticatedState(browser);
      await use(state);
    },
    { scope: 'worker' },
  ],
  desktopSession: [
    async ({ browser, authenticatedState }, use) => {
      const context = await browser.newContext({
        storageState: authenticatedState.storageStatePath,
      });
      const page = await context.newPage();
      await page.goto(authenticatedState.baseURL);
      await page.reload();
      await page.locator('[data-desktop]').waitFor({ state: 'visible', timeout: 15000 });
      await use({
        context,
        page,
        email: authenticatedState.email,
        baseURL: authenticatedState.baseURL,
      });
      await context.close();
    },
    { scope: 'test' },
  ],
});

export { expect };
