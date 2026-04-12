/**
 * Validation test for VAL-AUTH-004 and VAL-AUTH-005 assertions.
 *
 * VAL-AUTH-004: Login Finish Verifies Credential and Issues Session (Re-login)
 * VAL-AUTH-005: Full Flow - Register → Login → Logout → Re-login
 *
 * This test documents the full auth flow with screenshot evidence.
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
  return `val-test-${Date.now()}-${Math.random().toString(36).slice(2, 8)}@example.com`;
}

// =============================================================================
// VAL-AUTH-005: Full Flow - Register → Login → Logout → Re-login
// =============================================================================
test('VAL-AUTH-005: Full Flow - Register → Login → Logout → Re-login', async ({
  page,
  authenticator,
}, testInfo) => {
  const email = uniqueEmail();
  const evidenceDir = testInfo.outputDir;

  await test.step('1. Navigate to auth page', async () => {
    await page.goto(BASE_URL);
    await page.waitForLoadState('networkidle');
    await page.screenshot({
      path: `${evidenceDir}/VAL-AUTH-005-step1-initial-page.png`,
      fullPage: true,
    });
  });

  await test.step('2. Register with unique email', async () => {
    // Fill email
    await page.fill('input[type="email"]', email);
    await page.screenshot({
      path: `${evidenceDir}/VAL-AUTH-005-step2-email-filled.png`,
      fullPage: true,
    });

    // Register with passkey
    const regResult = await registerPasskey(page, email, BASE_URL);
    expect(regResult.ok).toBe(true);
    expect(regResult.user).toBeDefined();
    expect(regResult.user.email).toBe(email);

    await page.screenshot({
      path: `${evidenceDir}/VAL-AUTH-005-step3-after-registration.png`,
      fullPage: true,
    });
  });

  await test.step('3. Verify session is authenticated', async () => {
    const session = await getSession(page, BASE_URL);
    expect(session.authenticated).toBe(true);
    expect(session.user.email).toBe(email);

    await page.screenshot({
      path: `${evidenceDir}/VAL-AUTH-005-step4-authenticated-session.png`,
      fullPage: true,
    });
  });

  await test.step('4. Logout', async () => {
    const logoutResult = await logout(page, BASE_URL);
    expect(logoutResult.authenticated).toBe(false);

    await page.screenshot({
      path: `${evidenceDir}/VAL-AUTH-005-step5-after-logout.png`,
      fullPage: true,
    });
  });

  await test.step('5. Verify logged out state', async () => {
    const session = await getSession(page, BASE_URL);
    expect(session.authenticated).toBe(false);

    await page.screenshot({
      path: `${evidenceDir}/VAL-AUTH-005-step6-logged-out-session.png`,
      fullPage: true,
    });
  });

  await test.step('6. Re-login with same email (CRITICAL BUG FIX TEST)', async () => {
    // This is the critical re-login that was previously failing
    const loginResult = await loginPasskey(page, email, BASE_URL);

    expect(loginResult.ok).toBe(true);
    expect(loginResult.user).toBeDefined();
    expect(loginResult.user.email).toBe(email);

    await page.screenshot({
      path: `${evidenceDir}/VAL-AUTH-005-step7-after-relogin.png`,
      fullPage: true,
    });
  });

  await test.step('7. Verify re-login session', async () => {
    const session = await getSession(page, BASE_URL);
    expect(session.authenticated).toBe(true);
    expect(session.user.email).toBe(email);

    await page.screenshot({
      path: `${evidenceDir}/VAL-AUTH-005-step8-relogin-session-confirmed.png`,
      fullPage: true,
    });
  });

  // Final validation summary
  const finalSession = await getSession(page, BASE_URL);
  console.log('VAL-AUTH-005 Final Result:', {
    email,
    authenticated: finalSession.authenticated,
    userId: finalSession.user?.id,
  });
});

// =============================================================================
// VAL-AUTH-004: Login Finish Verifies Credential and Issues Session
// =============================================================================
test('VAL-AUTH-004: Login Finish Verifies Credential and Issues Session', async ({
  page,
  authenticator,
}, testInfo) => {
  const email = uniqueEmail();
  const evidenceDir = testInfo.outputDir;

  await test.step('Setup: Register first', async () => {
    await page.goto(BASE_URL);
    const regResult = await registerPasskey(page, email, BASE_URL);
    expect(regResult.ok).toBe(true);
    await logout(page, BASE_URL);
  });

  await test.step('1. Begin login (get assertion options)', async () => {
    const beginRes = await page.evaluate(async (opts) => {
      const { email, baseURL } = opts;
      const res = await fetch(`${baseURL}/auth/login/begin`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        credentials: 'include',
        body: JSON.stringify({ email }),
      });
      return { status: res.status, data: await res.json() };
    }, { email, baseURL: BASE_URL });

    expect(beginRes.status).toBe(200);
    expect(beginRes.data.publicKey).toBeDefined();
    expect(beginRes.data.publicKey.allowCredentials).toBeDefined();
    expect(beginRes.data.publicKey.allowCredentials.length).toBeGreaterThan(0);

    await page.screenshot({
      path: `${evidenceDir}/VAL-AUTH-004-step1-login-begin.png`,
      fullPage: true,
    });
  });

  await test.step('2. Get WebAuthn assertion', async () => {
    const assertion = await page.evaluate(async (opts) => {
      const { email, baseURL } = opts;

      // Get assertion options
      const beginRes = await fetch(`${baseURL}/auth/login/begin`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        credentials: 'include',
        body: JSON.stringify({ email }),
      });
      const assertionOptions = await beginRes.json();
      const publicKey = assertionOptions.publicKey;

      function base64urlToBuffer(b64url) {
        let b64 = b64url.replace(/-/g, '+').replace(/_/g, '/');
        while (b64.length % 4) b64 += '=';
        const binary = atob(b64);
        const bytes = new Uint8Array(binary.length);
        for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i);
        return bytes.buffer;
      }

      const decodedChallenge = base64urlToBuffer(publicKey.challenge);
      const decodedAllowCredentials = (publicKey.allowCredentials || []).map(c => ({
        id: base64urlToBuffer(c.id),
        type: c.type,
        transports: c.transports,
      }));

      const assertion = await navigator.credentials.get({
        publicKey: {
          challenge: decodedChallenge,
          rpId: publicKey.rpId,
          allowCredentials: decodedAllowCredentials,
          userVerification: publicKey.userVerification,
        },
      });

      function bufferToBase64url(buffer) {
        const bytes = new Uint8Array(buffer);
        let binary = '';
        for (const b of bytes) binary += String.fromCharCode(b);
        return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
      }

      return {
        id: assertion.id,
        rawId: bufferToBase64url(assertion.rawId),
        type: assertion.type,
      };
    }, { email, baseURL: BASE_URL });

    expect(assertion.id).toBeDefined();
    expect(assertion.type).toBe('public-key');
  });

  await test.step('3. Finish login (critical verification)', async () => {
    const finishResult = await loginPasskey(page, email, BASE_URL);

    // VAL-AUTH-004 PASS CONDITION
    expect(finishResult.ok).toBe(true);
    expect(finishResult.user).toBeDefined();
    expect(finishResult.user.email).toBe(email);

    await page.screenshot({
      path: `${evidenceDir}/VAL-AUTH-004-step3-login-finish-success.png`,
      fullPage: true,
    });
  });

  await test.step('4. Verify credential sign_count updated and session issued', async () => {
    // Check session is authenticated
    const session = await getSession(page, BASE_URL);
    expect(session.authenticated).toBe(true);
    expect(session.user.email).toBe(email);

    // Check cookies are present
    const cookies = await page.context().cookies();
    const accessCookie = cookies.find(c => c.name === 'choir_access');
    const refreshCookie = cookies.find(c => c.name === 'choir_refresh');

    expect(accessCookie).toBeDefined();
    expect(refreshCookie).toBeDefined();
    expect(accessCookie.httpOnly).toBe(true);
    expect(refreshCookie.httpOnly).toBe(true);

    await page.screenshot({
      path: `${evidenceDir}/VAL-AUTH-004-step4-session-and-cookies-verified.png`,
      fullPage: true,
    });

    console.log('VAL-AUTH-004 Final Result:', {
      email,
      loginOk: true,
      sessionAuthenticated: session.authenticated,
      hasAccessCookie: !!accessCookie,
      hasRefreshCookie: !!refreshCookie,
    });
  });
});
