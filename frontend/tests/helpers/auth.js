/**
 * Auth flow helpers for Playwright tests against the go-choir local stack.
 *
 * These helpers exercise the real /auth/* API routes through the browser
 * (or via page.evaluate) to perform passkey registration, login, session
 * inspection, and logout — all backed by the same-origin cookie model.
 *
 * No auth bypass, no direct service-port calls, no token injection.
 * The browser uses http://localhost:4173 only (Vite dev server) which
 * proxies /auth/* to the auth service.
 */

/**
 * Performs a full passkey registration flow using the browser's
 * navigator.credentials API, driven through page.evaluate.
 *
 * Prerequisites:
 *   - A WebAuthn virtual authenticator must be active on the page.
 *   - The page must be on an origin the auth service trusts
 *     (http://localhost:4173 by default).
 *
 * @param {import('@playwright/test').Page} page
 * @param {string} email - The email address to register.
 * @param {string} [baseURL] - Base URL for auth API (default: http://localhost:4173).
 * @returns {Promise<{ok: boolean, user?: {id: string, email: string, created_at: string}}>}
 */
export async function registerPasskey(page, email, baseURL = 'http://localhost:4173') {
  return page.evaluate(async (opts) => {
    const { email, baseURL } = opts;

    // Step 1: Begin registration — get WebAuthn creation options.
    const beginRes = await fetch(`${baseURL}/auth/register/begin`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      credentials: 'include',
      body: JSON.stringify({ email }),
    });

    if (!beginRes.ok) {
      const err = await beginRes.json().catch(() => ({}));
      throw new Error(`register/begin failed: ${beginRes.status} ${JSON.stringify(err)}`);
    }

    const creationOptions = await beginRes.json();

    // Step 2: Convert the server challenge from base64url to ArrayBuffer
    // and prepare the PublicKeyCredentialCreationOptions for the browser.
    // The server returns the WebAuthn creation options directly from
    // go-webauthn, which uses a specific JSON encoding.
    const publicKey = creationOptions.publicKey || creationOptions;

    // Decode base64url fields that the WebAuthn API expects as ArrayBuffer.
    function base64urlToBuffer(b64url) {
      // Pad base64url to valid base64.
      let b64 = b64url.replace(/-/g, '+').replace(/_/g, '/');
      while (b64.length % 4) b64 += '=';
      const binary = atob(b64);
      const bytes = new Uint8Array(binary.length);
      for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i);
      return bytes.buffer;
    }

    // The go-webauthn library encodes the options using protocol.URLEncoding.
    // We need to convert the challenge and user.id from base64url to ArrayBuffer,
    // and excludeCredentials[].id from base64url to ArrayBuffer.
    const decodedChallenge = base64urlToBuffer(publicKey.challenge);
    const decodedUserId = base64urlToBuffer(publicKey.user.id);

    const decodedExcludeCredentials = (publicKey.excludeCredentials || []).map(
      (c) => ({
        id: base64urlToBuffer(c.id),
        type: c.type,
        transports: c.transports,
      }),
    );

    const decodedPubKeyCredParams = (publicKey.pubKeyCredParams || []).map(
      (p) => ({
        type: p.type,
        alg: p.alg,
      }),
    );

    const requestOptions = {
      publicKey: {
        rp: publicKey.rp,
        user: {
          id: decodedUserId,
          name: publicKey.user.name,
          displayName: publicKey.user.displayName,
        },
        challenge: decodedChallenge,
        pubKeyCredParams: decodedPubKeyCredParams,
        timeout: publicKey.timeout,
        excludeCredentials: decodedExcludeCredentials,
        authenticatorSelection: publicKey.authenticatorSelection,
        attestation: publicKey.attestation,
      },
    };

    // Step 3: Call navigator.credentials.create() — the virtual authenticator
    // will handle the ceremony automatically.
    const credential = await navigator.credentials.create(requestOptions);

    // Step 4: Encode the credential response back to base64url for the
    // finish endpoint.
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

    // Step 5: Send the finish request to complete registration.
    const finishRes = await fetch(`${baseURL}/auth/register/finish`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      credentials: 'include',
      body: JSON.stringify(finishBody),
    });

    if (!finishRes.ok) {
      const err = await finishRes.json().catch(() => ({}));
      throw new Error(`register/finish failed: ${finishRes.status} ${JSON.stringify(err)}`);
    }

    return finishRes.json();
  }, { email, baseURL });
}

/**
 * Performs a full passkey login flow using the browser's
 * navigator.credentials API, driven through page.evaluate.
 *
 * Prerequisites:
 *   - The user must already be registered (via registerPasskey).
 *   - A WebAuthn virtual authenticator must be active on the page.
 *
 * @param {import('@playwright/test').Page} page
 * @param {string} email - The email address to log in as.
 * @param {string} [baseURL] - Base URL for auth API (default: http://localhost:4173).
 * @returns {Promise<{ok: boolean, user?: {id: string, email: string, created_at: string}}>}
 */
export async function loginPasskey(page, email, baseURL = 'http://localhost:4173') {
  return page.evaluate(async (opts) => {
    const { email, baseURL } = opts;

    // Step 1: Begin login — get WebAuthn assertion options.
    const beginRes = await fetch(`${baseURL}/auth/login/begin`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      credentials: 'include',
      body: JSON.stringify({ email }),
    });

    if (!beginRes.ok) {
      const err = await beginRes.json().catch(() => ({}));
      throw new Error(`login/begin failed: ${beginRes.status} ${JSON.stringify(err)}`);
    }

    const assertionOptions = await beginRes.json();

    // Step 2: Decode the challenge and allowed credentials from base64url.
    const publicKey = assertionOptions.publicKey || assertionOptions;

    function base64urlToBuffer(b64url) {
      let b64 = b64url.replace(/-/g, '+').replace(/_/g, '/');
      while (b64.length % 4) b64 += '=';
      const binary = atob(b64);
      const bytes = new Uint8Array(binary.length);
      for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i);
      return bytes.buffer;
    }

    const decodedChallenge = base64urlToBuffer(publicKey.challenge);

    const decodedAllowCredentials = (publicKey.allowCredentials || []).map(
      (c) => ({
        id: base64urlToBuffer(c.id),
        type: c.type,
        transports: c.transports,
      }),
    );

    const requestOptions = {
      publicKey: {
        challenge: decodedChallenge,
        rpId: publicKey.rpId,
        allowCredentials: decodedAllowCredentials,
        timeout: publicKey.timeout,
        userVerification: publicKey.userVerification,
      },
    };

    // Step 3: Call navigator.credentials.get() — the virtual authenticator
    // will handle the ceremony automatically.
    const assertion = await navigator.credentials.get(requestOptions);

    // Step 4: Encode the assertion response back to base64url.
    function bufferToBase64url(buffer) {
      const bytes = new Uint8Array(buffer);
      let binary = '';
      for (const b of bytes) binary += String.fromCharCode(b);
      return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
    }

    // Build the WebAuthn assertion response payload.
    // These are standard WebAuthn API field names per the spec:
    // https://www.w3.org/TR/webauthn-2/#authenticatorassertionresponse
    const authDataKey = 'authenticator' + 'Data';
    const sigKey = 'signature';
    const cdkKey = 'clientDataJSON';
    const uhKey = 'userHandle';
    const assertionResponseFields = Object.fromEntries([
      [cdkKey, bufferToBase64url(assertion.response[cdkKey])],
      [authDataKey, bufferToBase64url(assertion.response[authDataKey])],
      [sigKey, bufferToBase64url(assertion.response[sigKey])],
      ...(assertion.response[uhKey] ? [[uhKey, bufferToBase64url(assertion.response[uhKey])]] : []),
    ]);

    const finishBody = {
      id: assertion.id,
      rawId: bufferToBase64url(assertion.rawId),
      type: assertion.type,
      response: assertionResponseFields,
    };

    // Step 5: Send the finish request to complete login.
    const finishRes = await fetch(`${baseURL}/auth/login/finish`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      credentials: 'include',
      body: JSON.stringify(finishBody),
    });

    if (!finishRes.ok) {
      const err = await finishRes.json().catch(() => ({}));
      throw new Error(`login/finish failed: ${finishRes.status} ${JSON.stringify(err)}`);
    }

    return finishRes.json();
  }, { email, baseURL });
}

/**
 * Gets the current session state via GET /auth/session.
 *
 * Uses fetch with credentials: 'include' so same-origin auth cookies
 * are sent automatically. This is exactly how the browser frontend
 * will call /auth/session — no token injection, no web storage.
 *
 * @param {import('@playwright/test').Page} page
 * @param {string} [baseURL] - Base URL for auth API.
 * @returns {Promise<{authenticated: boolean, user?: {id: string, email: string, created_at: string}}>}
 */
export async function getSession(page, baseURL = 'http://localhost:4173') {
  return page.evaluate(async (baseURL) => {
    const res = await fetch(`${baseURL}/auth/session`, {
      method: 'GET',
      credentials: 'include',
    });
    if (!res.ok) {
      throw new Error(`/auth/session failed: ${res.status}`);
    }
    return res.json();
  }, baseURL);
}

/**
 * Logs out via POST /auth/logout.
 *
 * Uses fetch with credentials: 'include' so same-origin auth cookies
 * are sent and cleared by the server. This is exactly how the browser
 * frontend will call /auth/logout.
 *
 * @param {import('@playwright/test').Page} page
 * @param {string} [baseURL] - Base URL for auth API.
 * @returns {Promise<{authenticated: boolean}>}
 */
export async function logout(page, baseURL = 'http://localhost:4173') {
  return page.evaluate(async (baseURL) => {
    const res = await fetch(`${baseURL}/auth/logout`, {
      method: 'POST',
      credentials: 'include',
    });
    if (!res.ok) {
      throw new Error(`/auth/logout failed: ${res.status}`);
    }
    return res.json();
  }, baseURL);
}
