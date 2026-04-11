/**
 * WebAuthn virtual-authenticator helpers for Playwright Chromium.
 *
 * Uses Chrome DevTools Protocol (CDP) to enable WebAuthn and register a
 * virtual authenticator that automatically simulates user presence and
 * verification. This allows automated passkey registration and login
 * flows without any product-side auth bypass.
 *
 * Important: These helpers only work with Chromium-based browsers because
 * they depend on the CDP WebAuthn domain, which is not available in
 * Firefox or WebKit.
 */

/**
 * Sets up a WebAuthn virtual authenticator on the given Playwright page.
 *
 * Must be called BEFORE navigating to the page or triggering any
 * navigator.credentials call. The virtual authenticator will automatically
 * handle WebAuthn ceremonies (user presence + verification) so
 * navigator.credentials.create() and .get() resolve without manual
 * interaction.
 *
 * @param {import('@playwright/test').Page} page
 * @returns {Promise<{client: import('@playwright/test').CDPSession, authenticatorId: string}>}
 */
export async function setupVirtualAuthenticator(page) {
  const client = await page.context().newCDPSession(page);
  await client.send('WebAuthn.enable');

  const { authenticatorId } = await client.send(
    'WebAuthn.addVirtualAuthenticator',
    {
      options: {
        protocol: 'ctap2',
        transport: 'internal',
        hasResidentKey: true,
        hasUserVerification: true,
        isUserVerified: true,
        automaticPresenceSimulation: true,
      },
    },
  );

  return { client, authenticatorId };
}

/**
 * Removes a virtual authenticator and disables WebAuthn on the CDP session.
 *
 * Call this in test teardown to prevent virtual-authenticator state from
 * leaking between tests.
 *
 * @param {import('@playwright/test').CDPSession} client
 * @param {string} authenticatorId
 */
export async function removeVirtualAuthenticator(client, authenticatorId) {
  await client.send('WebAuthn.removeVirtualAuthenticator', { authenticatorId });
  await client.send('WebAuthn.disable');
}

/**
 * Gets the credentials stored in the virtual authenticator.
 *
 * Useful for verifying that a registration ceremony created a credential,
 * or that the expected number of credentials exist.
 *
 * @param {import('@playwright/test').CDPSession} client
 * @param {string} authenticatorId
 * @returns {Promise<{credentials: Array<{id: string, rpId: string, userHandle: string}>}>}
 */
export async function getVirtualAuthenticatorCredentials(client, authenticatorId) {
  return client.send('WebAuthn.getCredentials', { authenticatorId });
}
