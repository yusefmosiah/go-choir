/**
 * Playwright tests for passkey registration, login, session, and logout
 * flows using the Chromium virtual-authenticator harness.
 *
 * These tests exercise the real /auth/* API routes through the browser,
 * using same-origin cookie-backed auth only. No auth bypass, no direct
 * service-port calls, no token injection.
 *
 * Prerequisites:
 *   - Auth service running on localhost:8081 (AUTH_RP_ID="localhost",
 *     AUTH_RP_ORIGINS="http://localhost:4173", AUTH_COOKIE_SECURE="false")
 *   - Vite dev server running on localhost:4173, proxying /auth/* to :8081
 */
import { test, expect } from './helpers/fixtures.js';
import {
  registerPasskey,
  loginPasskey,
  getSession,
  logout,
} from './helpers/auth.js';
import { getVirtualAuthenticatorCredentials } from './helpers/webauthn.js';

const BASE_URL = 'http://localhost:4173';

// Generate a unique email per test to avoid DB collisions.
function uniqueEmail() {
  return `pw-test-${Date.now()}-${Math.random().toString(36).slice(2, 8)}@example.com`;
}

// ---------------------------------------------------------------
// Test: passkey registration creates a credential and session
// ---------------------------------------------------------------
test('passkey registration creates a credential and authenticated session', async ({
  page,
  authenticator,
}) => {
  const email = uniqueEmail();

  // Navigate to the frontend so the virtual authenticator is bound to the
  // correct origin before calling navigator.credentials.
  await page.goto(BASE_URL);

  // Register a new passkey.
  const result = await registerPasskey(page, email, BASE_URL);

  // Verify the registration succeeded and returned user info.
  expect(result.ok).toBe(true);
  expect(result.user).toBeDefined();
  expect(result.user.email).toBe(email);
  expect(result.user.id).toBeTruthy();

  // Verify the virtual authenticator stored a credential.
  const { credentials } = await getVirtualAuthenticatorCredentials(
    authenticator.client,
    authenticator.authenticatorId,
  );
  expect(credentials.length).toBeGreaterThanOrEqual(1);

  // Verify /auth/session reports an authenticated user.
  const session = await getSession(page, BASE_URL);
  expect(session.authenticated).toBe(true);
  expect(session.user).toBeDefined();
  expect(session.user.email).toBe(email);
});

// ---------------------------------------------------------------
// Test: passkey login returns assertion options for registered user
// ---------------------------------------------------------------
test('passkey login returns assertion options and creates session', async ({
  page,
  authenticator,
}) => {
  const email = uniqueEmail();

  await page.goto(BASE_URL);

  // Register first so we have a passkey to log in with.
  const regResult = await registerPasskey(page, email, BASE_URL);
  expect(regResult.ok).toBe(true);

  // Log out to get a clean signed-out state.
  await logout(page, BASE_URL);

  // Verify session is signed out.
  let session = await getSession(page, BASE_URL);
  expect(session.authenticated).toBe(false);

  // Log in with the passkey.
  const loginResult = await loginPasskey(page, email, BASE_URL);

  expect(loginResult.ok).toBe(true);
  expect(loginResult.user).toBeDefined();
  expect(loginResult.user.email).toBe(email);

  // Verify /auth/session reports an authenticated user.
  session = await getSession(page, BASE_URL);
  expect(session.authenticated).toBe(true);
  expect(session.user.email).toBe(email);
});

// ---------------------------------------------------------------
// Test: authenticated /auth/session returns user identity without secrets
// ---------------------------------------------------------------
test('authenticated /auth/session returns user identity without secrets', async ({
  page,
  authenticator,
}) => {
  const email = uniqueEmail();

  await page.goto(BASE_URL);
  await registerPasskey(page, email, BASE_URL);

  const session = await getSession(page, BASE_URL);

  // Must be authenticated.
  expect(session.authenticated).toBe(true);
  expect(session.user).toBeDefined();
  expect(session.user.email).toBe(email);
  expect(session.user.id).toBeTruthy();
  expect(session.user.created_at).toBeTruthy();

  // Must NOT leak secrets or credential material.
  const sessionStr = JSON.stringify(session);
  expect(sessionStr).not.toContain('token');
  expect(sessionStr).not.toContain('refresh');
  expect(sessionStr).not.toContain('credential');
  expect(sessionStr).not.toContain('challenge');
});

// ---------------------------------------------------------------
// Test: logout invalidates session and is safe to repeat
// ---------------------------------------------------------------
test('logout invalidates session and is safe to repeat', async ({
  page,
  authenticator,
}) => {
  const email = uniqueEmail();

  await page.goto(BASE_URL);
  await registerPasskey(page, email, BASE_URL);

  // Verify we are authenticated.
  let session = await getSession(page, BASE_URL);
  expect(session.authenticated).toBe(true);

  // Log out.
  const logoutResult = await logout(page, BASE_URL);
  expect(logoutResult.authenticated).toBe(false);

  // Verify session is now signed out.
  session = await getSession(page, BASE_URL);
  expect(session.authenticated).toBe(false);

  // Repeat logout — must not error (non-5xx signed-out result).
  const repeatResult = await logout(page, BASE_URL);
  expect(repeatResult.authenticated).toBe(false);
});

// ---------------------------------------------------------------
// Test: replayed finish payload does not create a session
// ---------------------------------------------------------------
test('replayed registration finish payload does not create a session', async ({
  page,
  authenticator,
}) => {
  const email = uniqueEmail();

  await page.goto(BASE_URL);

  // Use page.evaluate to do a manual registration and capture the finish
  // body, then replay it.
  const replayResult = await page.evaluate(async (opts) => {
    const { email, baseURL } = opts;

    // Begin registration.
    const beginRes = await fetch(`${baseURL}/auth/register/begin`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      credentials: 'include',
      body: JSON.stringify({ email }),
    });
    if (!beginRes.ok) throw new Error('begin failed');
    const creationOptions = await beginRes.json();

    const publicKey = creationOptions.publicKey || creationOptions;

    function base64urlToBuffer(b64url) {
      let b64 = b64url.replace(/-/g, '+').replace(/_/g, '/');
      while (b64.length % 4) b64 += '=';
      const binary = atob(b64);
      const bytes = new Uint8Array(binary.length);
      for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i);
      return bytes.buffer;
    }

    const decodedChallenge = base64urlToBuffer(publicKey.challenge);
    const decodedUserId = base64urlToBuffer(publicKey.user.id);
    const decodedExcludeCredentials = (publicKey.excludeCredentials || []).map(
      (c) => ({ id: base64urlToBuffer(c.id), type: c.type, transports: c.transports }),
    );

    const credential = await navigator.credentials.create({
      publicKey: {
        rp: publicKey.rp,
        user: { id: decodedUserId, name: publicKey.user.name, displayName: publicKey.user.displayName },
        challenge: decodedChallenge,
        pubKeyCredParams: publicKey.pubKeyCredParams || [],
        excludeCredentials: decodedExcludeCredentials,
        authenticatorSelection: publicKey.authenticatorSelection,
        attestation: publicKey.attestation,
      },
    });

    function bufferToBase64url(buffer) {
      const bytes = new Uint8Array(buffer);
      let binary = '';
      for (const b of bytes) binary += String.fromCharCode(b);
      return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
    }

    const finishBody = {
      id: credential.id,
      rawId: bufferToBase64url(credential.rawId),
      type: credential.type,
      response: {
        clientDataJSON: bufferToBase64url(credential.response.clientDataJSON),
        attestationObject: bufferToBase64url(credential.response.attestationObject),
      },
    };

    // First finish — should succeed.
    const firstRes = await fetch(`${baseURL}/auth/register/finish`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      credentials: 'include',
      body: JSON.stringify(finishBody),
    });
    const firstResult = await firstRes.json();

    // Replay the same finish body — should fail.
    const replayRes = await fetch(`${baseURL}/auth/register/finish`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      credentials: 'include',
      body: JSON.stringify(finishBody),
    });
    const replayStatus = replayRes.status;

    // Check session after replay.
    const sessionRes = await fetch(`${baseURL}/auth/session`, {
      credentials: 'include',
    });
    const session = await sessionRes.json();

    return { firstOk: firstResult.ok, replayStatus, session };
  }, { email, baseURL: BASE_URL });

  // First registration succeeded.
  expect(replayResult.firstOk).toBe(true);

  // Replay must fail (4xx — challenge already used).
  expect(replayResult.replayStatus).toBeGreaterThanOrEqual(400);
  expect(replayResult.replayStatus).toBeLessThan(500);

  // Session is still authenticated from the original registration,
  // not from the replay. The replay did NOT create a new session.
  expect(replayResult.session.authenticated).toBe(true);
});

// ---------------------------------------------------------------
// Test: GET /auth/session returns signed-out for missing cookies
// ---------------------------------------------------------------
test('GET /auth/session returns signed-out for missing auth cookies', async ({
  page,
  authenticator,
}) => {
  await page.goto(BASE_URL);

  // No registration or login — should be signed out.
  const session = await getSession(page, BASE_URL);
  expect(session.authenticated).toBe(false);
  expect(session.user).toBeUndefined();
});
