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
 */
import { test as base, expect } from '@playwright/test';
import {
  setupVirtualAuthenticator,
  removeVirtualAuthenticator,
} from './webauthn.js';

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
});

export { expect };
