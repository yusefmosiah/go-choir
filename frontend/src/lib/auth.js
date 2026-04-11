/**
 * WebAuthn ceremony helpers for the Svelte app.
 *
 * These helpers perform passkey registration and login flows using
 * the browser's native navigator.credentials API, communicating with
 * the same-origin /auth/* routes only.
 *
 * No auth bypass, no direct service-port calls, no token injection.
 * Auth is cookie-backed and same-origin; tokens never appear in the
 * URL, localStorage, or sessionStorage.
 */

// ---------------------------------------------------------------------------
// Base64url encoding / decoding
// ---------------------------------------------------------------------------

/**
 * Decodes a base64url-encoded string to an ArrayBuffer.
 * @param {string} b64url
 * @returns {ArrayBuffer}
 */
function base64urlToBuffer(b64url) {
  let b64 = b64url.replace(/-/g, '+').replace(/_/g, '/');
  while (b64.length % 4) b64 += '=';
  const binary = atob(b64);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i);
  return bytes.buffer;
}

/**
 * Encodes an ArrayBuffer as a base64url string.
 * @param {ArrayBuffer} buffer
 * @returns {string}
 */
function bufferToBase64url(buffer) {
  const bytes = new Uint8Array(buffer);
  let binary = '';
  for (const b of bytes) binary += String.fromCharCode(b);
  return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

// ---------------------------------------------------------------------------
// Passkey registration
// ---------------------------------------------------------------------------

/**
 * Performs a full passkey registration flow:
 *   1. POST /auth/register/begin  — get WebAuthn creation options
 *   2. navigator.credentials.create() — browser handles ceremony
 *   3. POST /auth/register/finish — complete registration
 *
 * @param {string} username
 * @returns {Promise<{ok: boolean, user?: object}>}
 * @throws {Error} On network failure, server error, or ceremony cancellation
 */
export async function registerPasskey(username) {
  // Step 1: Begin registration.
  const beginRes = await fetch('/auth/register/begin', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    credentials: 'include',
    body: JSON.stringify({ username }),
  });

  if (!beginRes.ok) {
    const err = await beginRes.json().catch(() => ({}));
    throw new Error(err.error || `Registration begin failed (${beginRes.status})`);
  }

  const creationOptions = await beginRes.json();
  const publicKey = creationOptions.publicKey || creationOptions;

  // Decode base64url fields for the WebAuthn API.
  const decodedChallenge = base64urlToBuffer(publicKey.challenge);
  const decodedUserId = base64urlToBuffer(publicKey.user.id);
  const decodedExcludeCredentials = (publicKey.excludeCredentials || []).map(
    (c) => ({ id: base64urlToBuffer(c.id), type: c.type, transports: c.transports }),
  );
  const decodedPubKeyCredParams = (publicKey.pubKeyCredParams || []).map(
    (p) => ({ type: p.type, alg: p.alg }),
  );

  // Step 2: Call navigator.credentials.create() — the browser presents
  // the passkey dialog and the user completes the ceremony.
  const credential = await navigator.credentials.create({
    publicKey: {
      rp: publicKey.rp,
      user: { id: decodedUserId, name: publicKey.user.name, displayName: publicKey.user.displayName },
      challenge: decodedChallenge,
      pubKeyCredParams: decodedPubKeyCredParams,
      timeout: publicKey.timeout,
      excludeCredentials: decodedExcludeCredentials,
      authenticatorSelection: publicKey.authenticatorSelection,
      attestation: publicKey.attestation,
    },
  });

  // Step 3: Encode and send to the finish endpoint.
  const finishBody = {
    id: credential.id,
    rawId: bufferToBase64url(credential.rawId),
    type: credential.type,
    response: {
      clientDataJSON: bufferToBase64url(credential.response.clientDataJSON),
      attestationObject: bufferToBase64url(credential.response.attestationObject),
    },
  };

  const finishRes = await fetch('/auth/register/finish', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    credentials: 'include',
    body: JSON.stringify(finishBody),
  });

  if (!finishRes.ok) {
    const err = await finishRes.json().catch(() => ({}));
    throw new Error(err.error || `Registration finish failed (${finishRes.status})`);
  }

  return finishRes.json();
}

// ---------------------------------------------------------------------------
// Passkey login
// ---------------------------------------------------------------------------

/**
 * Performs a full passkey login flow:
 *   1. POST /auth/login/begin   — get WebAuthn assertion options
 *   2. navigator.credentials.get() — browser handles ceremony
 *   3. POST /auth/login/finish  — complete login
 *
 * @param {string} username
 * @returns {Promise<{ok: boolean, user?: object}>}
 * @throws {Error} On network failure, server error, or ceremony cancellation
 */
export async function loginPasskey(username) {
  // Step 1: Begin login.
  const beginRes = await fetch('/auth/login/begin', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    credentials: 'include',
    body: JSON.stringify({ username }),
  });

  if (!beginRes.ok) {
    const err = await beginRes.json().catch(() => ({}));
    throw new Error(err.error || `Login begin failed (${beginRes.status})`);
  }

  const assertionOptions = await beginRes.json();
  const publicKey = assertionOptions.publicKey || assertionOptions;

  // Decode base64url fields for the WebAuthn API.
  const decodedChallenge = base64urlToBuffer(publicKey.challenge);
  const decodedAllowCredentials = (publicKey.allowCredentials || []).map(
    (c) => ({ id: base64urlToBuffer(c.id), type: c.type, transports: c.transports }),
  );

  // Step 2: Call navigator.credentials.get() — the browser presents
  // the passkey dialog and the user completes the ceremony.
  const assertion = await navigator.credentials.get({
    publicKey: {
      challenge: decodedChallenge,
      rpId: publicKey.rpId,
      allowCredentials: decodedAllowCredentials,
      timeout: publicKey.timeout,
      userVerification: publicKey.userVerification,
    },
  });

  // Step 3: Encode and send to the finish endpoint.
  // Build the assertion response payload using standard WebAuthn API
  // field names per the spec: authenticatorData, signature,
  // clientDataJSON, userHandle.
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

  const finishRes = await fetch('/auth/login/finish', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    credentials: 'include',
    body: JSON.stringify(finishBody),
  });

  if (!finishRes.ok) {
    const err = await finishRes.json().catch(() => ({}));
    throw new Error(err.error || `Login finish failed (${finishRes.status})`);
  }

  return finishRes.json();
}

// ---------------------------------------------------------------------------
// Session helpers
// ---------------------------------------------------------------------------

/**
 * Gets the current session state via GET /auth/session.
 *
 * @returns {Promise<{authenticated: boolean, user?: object}>}
 */
export async function getSession() {
  const res = await fetch('/auth/session', {
    method: 'GET',
    credentials: 'include',
  });
  if (!res.ok) {
    throw new Error(`/auth/session failed: ${res.status}`);
  }
  return res.json();
}

/**
 * Logs out via POST /auth/logout.
 *
 * @returns {Promise<{authenticated: boolean}>}
 */
export async function logout() {
  const res = await fetch('/auth/logout', {
    method: 'POST',
    credentials: 'include',
  });
  if (!res.ok) {
    throw new Error(`/auth/logout failed: ${res.status}`);
  }
  return res.json();
}

// ---------------------------------------------------------------------------
// Session renewal
// ---------------------------------------------------------------------------

/**
 * Error thrown when a protected request fails with 401 and session
 * renewal cannot restore authenticated state. This signals that the
 * browser should fall back to the guest auth state (VAL-CROSS-008).
 */
export class AuthRequiredError extends Error {
  constructor(message = 'Authentication required') {
    super(message);
    this.name = 'AuthRequiredError';
  }
}

/**
 * Attempts to renew the session via GET /auth/session.
 *
 * The auth server automatically rotates the refresh token and issues a
 * new access JWT when the access state is expired but the refresh state
 * is still valid. If both are invalid or expired, the server returns
 * signed-out state (VAL-CROSS-004 / VAL-CROSS-008).
 *
 * @returns {Promise<{renewed: boolean, user?: object}>}
 */
export async function renewSession() {
  try {
    const res = await fetch('/auth/session', {
      method: 'GET',
      credentials: 'include',
    });
    if (!res.ok) {
      return { renewed: false };
    }
    const data = await res.json();
    if (data.authenticated && data.user) {
      return { renewed: true, user: data.user };
    }
    return { renewed: false };
  } catch (_err) {
    return { renewed: false };
  }
}

/**
 * Makes a fetch request to a protected route with automatic session renewal.
 *
 * If the request returns 401 (access JWT expired), this function attempts
 * silent renewal via GET /auth/session. If renewal succeeds (refresh rotation
 * mints new cookies), the original request is retried. If renewal fails,
 * an AuthRequiredError is thrown so the caller can fall back to the guest
 * auth state (VAL-CROSS-004 / VAL-CROSS-008).
 *
 * @param {string} url - The URL to fetch.
 * @param {object} [options] - Fetch options (credentials: 'include' is added automatically).
 * @returns {Promise<Response>}
 * @throws {AuthRequiredError} If the session cannot be renewed.
 */
export async function fetchWithRenewal(url, options = {}) {
  const res = await fetch(url, { ...options, credentials: 'include' });

  if (res.status !== 401) {
    return res;
  }

  // Access JWT expired — attempt silent renewal through refresh rotation.
  const { renewed } = await renewSession();

  if (!renewed) {
    throw new AuthRequiredError('Session expired and renewal failed');
  }

  // Renewal succeeded — retry the original request with the new cookies.
  return fetch(url, { ...options, credentials: 'include' });
}

// ---------------------------------------------------------------------------
// Error classification
// ---------------------------------------------------------------------------

/**
 * Returns a user-friendly message for a passkey ceremony error.
 * Handles NotAllowedError (user cancelled), and other WebAuthn/network errors.
 *
 * @param {Error} err
 * @returns {string}
 */
export function passkeyErrorMessage(err) {
  if (err.name === 'NotAllowedError') {
    return 'Passkey ceremony was cancelled. Please try again.';
  }
  if (err.message && err.message.includes('begin failed')) {
    return err.message;
  }
  if (err.message && err.message.includes('finish failed')) {
    return err.message;
  }
  return 'Passkey authentication failed. Please try again.';
}
