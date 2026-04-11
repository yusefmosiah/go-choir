# Playwright passkey automation research

## Context

- Repo: `/Users/wiz/go-choir`
- Goal: unblock Mission 2 auth validation assertions `VAL-AUTH-005`..`008` by automating real WebAuthn/passkey ceremonies instead of relying on manual browser steps.
- Current blocker: `.factory/validation/auth-foundation/user-testing/flows/auth-curl.json` confirms curl-only testing cannot complete passkey registration/login.

## Repo facts that matter

- `frontend/package.json` currently has no Playwright dependency and uses plain ESM JS.
- `frontend/src/App.svelte` is still a placeholder, so there is no existing auth UI to click through yet.
- `internal/auth/config.go` defaults the local RP to:
  - `AUTH_RP_ID=localhost`
  - `AUTH_RP_ORIGINS=http://localhost:4173`
  - `AUTH_COOKIE_SECURE=false`
- Deployed acceptance is same-origin on `https://draft.choir-ip.com` via Caddy (`nix/node-b.nix`).
- The auth service already exposes the full browser-facing contract:
  - `POST /auth/register/begin`
  - `POST /auth/register/finish`
  - `POST /auth/login/begin`
  - `POST /auth/login/finish`
  - `GET /auth/session`
  - `POST /auth/logout`

## Current Playwright/WebAuthn state (2026)

### 1. Playwright still does **not** have first-class WebAuthn APIs

As of 2026, the practical way in Playwright is still:

- Chromium only
- create a CDP session with `browserContext.newCDPSession(page)`
- call raw Chrome DevTools Protocol `WebAuthn.*` methods

Relevant sources:

- Playwright `CDPSession`: <https://playwright.dev/docs/api/class-cdpsession>
- Playwright `BrowserContext.newCDPSession()` note: Chromium-only
- Playwright issue still open: <https://github.com/microsoft/playwright/issues/7276>
- WebKit request explicitly redirected back to that issue: <https://github.com/microsoft/playwright/issues/26621>

### 2. CDP WebAuthn support is good enough for real end-to-end passkey ceremonies

The CDP `WebAuthn` domain currently supports:

- `WebAuthn.enable`
- `WebAuthn.addVirtualAuthenticator`
- `WebAuthn.getCredentials`
- `WebAuthn.setUserVerified`
- `WebAuthn.setAutomaticPresenceSimulation`
- `WebAuthn.setResponseOverrideBits`
- `WebAuthn.removeVirtualAuthenticator`
- events such as `WebAuthn.credentialAdded` and `WebAuthn.credentialAsserted`

Source:

- <https://chromedevtools.github.io/devtools-protocol/tot/WebAuthn/>

### 3. Modern browsers now make the browser-side JSON conversion much easier

This is the biggest 2026 simplifier.

Chromium now supports:

- `PublicKeyCredential.parseCreationOptionsFromJSON()`
- `PublicKeyCredential.parseRequestOptionsFromJSON()`
- `PublicKeyCredential.toJSON()` / `JSON.stringify(credential)`

These are baseline features from 2025 and remove most manual base64url/ArrayBuffer helper code.

Sources:

- <https://developer.mozilla.org/en-US/docs/Web/API/PublicKeyCredential/parseCreationOptionsFromJSON_static>
- <https://developer.mozilla.org/en-US/docs/Web/API/PublicKeyCredential/parseRequestOptionsFromJSON_static>
- <https://developer.mozilla.org/en-US/docs/Web/API/PublicKeyCredential/toJSON>

### 4. WebAuthn still requires a secure context / trustworthy origin

Useful for this repo:

- `https://draft.choir-ip.com` is valid for deployed acceptance
- `http://localhost:4173` is also acceptable locally because `localhost` is a trustworthy origin

Source:

- <https://developer.mozilla.org/en-US/docs/Web/Security/Defenses/Secure_Contexts>

## Recommended approach for this repo

## Recommendation

Add a **Chromium-only Playwright test suite under `frontend/`** that:

1. opens a page on the real app origin (`/`)
2. attaches a virtual authenticator via CDP
3. drives `navigator.credentials.create()` / `navigator.credentials.get()` from the page
4. drives `/auth/*` HTTP calls from Playwright request helpers
5. reuses the same spec against:
   - local dev origin for fast iteration
   - deployed `https://draft.choir-ip.com` for milestone validation

This is the best fit because it:

- uses the repo’s existing Node/pnpm frontend workspace
- matches the browser/origin/cookie model already used by the app
- proves the real auth begin/finish handlers instead of seeding fake credentials
- does **not** require waiting for the Svelte auth UI to be implemented first

## Why this harness shape fits `go-choir`

The frontend is still placeholder UI, but that is not a blocker.

For WebAuthn, the page only needs to be on the correct origin. The test can:

- `page.goto('/')`
- call `/auth/register/begin`
- run `navigator.credentials.create(...)` inside the page
- POST the resulting credential to `/auth/register/finish`

So the suite can validate the auth service now, even before a polished frontend passkey flow exists.

## Practical test structure

### Recommended layout

- `frontend/package.json` — add Playwright dependency/scripts
- `frontend/playwright.config.js` — ESM config, Chromium project, base URL
- `frontend/tests/auth-passkey.spec.js` — auth validation spec(s)
- `frontend/tests/helpers/passkey.js` — CDP + browser helper functions

Optional but likely useful:

- `frontend/vite.config.js` — local `/auth` (and later `/api`) proxy so local runs stay same-origin shaped
- `.github/workflows/ci.yml` — install browser and run Playwright auth validation
- `.gitignore` or `frontend/.gitignore` — ignore Playwright artifacts

## Best implementation pattern inside the tests

Use **two layers**:

### A. Browser page layer

Use the page only for WebAuthn operations:

- attach CDP virtual authenticator
- call `navigator.credentials.create()`
- call `navigator.credentials.get()`

### B. Playwright request layer

Use `browserContext.request` / `page.request` for the HTTP parts:

- `/auth/register/begin`
- `/auth/register/finish`
- `/auth/login/begin`
- `/auth/login/finish`
- `/auth/session`
- `/auth/logout`

Why this is especially good here:

- Playwright documents that `browserContext.request` shares cookie storage with the browser context
- auth cookies set by finish/login/logout immediately affect the page/browser context
- assertions against HTTP status and JSON bodies are much easier in Node-side test code than inside `page.evaluate`

Source:

- <https://playwright.dev/docs/api/class-apirequestcontext>

## Concrete flow I would use

### 1. Create one authenticator per test

Do this **after** the page exists, inside each test.

This matches community guidance from the long-running Playwright WebAuthn issue thread: the CDP session is page-bound, so setup should happen per test/page.

Suggested authenticator options:

```js
const client = await page.context().newCDPSession(page);
await client.send('WebAuthn.enable', { enableUI: false });
const { authenticatorId } = await client.send('WebAuthn.addVirtualAuthenticator', {
  options: {
    protocol: 'ctap2',
    ctap2Version: 'ctap2_1',
    transport: 'internal',
    hasResidentKey: true,
    hasUserVerification: true,
    isUserVerified: true,
    automaticPresenceSimulation: true,
  },
});
```

Notes:

- `transport: 'internal'` best matches a platform passkey-style authenticator.
- `automaticPresenceSimulation: true` is the simplest setting for this repo’s current straight-line register/login flows.
- `hasResidentKey: true` is future-friendly for passkey UX, though the current repo’s username-first login flow does not strictly require discoverable credentials.

### 2. Feed the server’s JSON options directly into the browser APIs

Do **not** reshape or partially re-serialize the server response.

Recommended pattern:

```js
const begin = await request.post('/auth/register/begin', {
  data: { username },
});
const beginJson = await begin.json();

const credentialJson = await page.evaluate(async ({ publicKeyJson }) => {
  const publicKey =
    PublicKeyCredential.parseCreationOptionsFromJSON(publicKeyJson);
  const credential = await navigator.credentials.create({ publicKey });
  return JSON.parse(JSON.stringify(credential));
}, { publicKeyJson: beginJson.publicKey });
```

Then:

```js
const finish = await request.post('/auth/register/finish', {
  data: credentialJson,
});
```

This avoids the exact class of bug seen in current community debugging: stripping fields like `authenticatorSelection` / `residentKey` can silently change credential behavior.

### 3. Do the same for login

```js
const begin = await request.post('/auth/login/begin', {
  data: { username },
});
const beginJson = await begin.json();

const assertionJson = await page.evaluate(async ({ publicKeyJson }) => {
  const publicKey =
    PublicKeyCredential.parseRequestOptionsFromJSON(publicKeyJson);
  const assertion = await navigator.credentials.get({ publicKey });
  return JSON.parse(JSON.stringify(assertion));
}, { publicKeyJson: beginJson.publicKey });

const finish = await request.post('/auth/login/finish', {
  data: assertionJson,
});
```

### 4. Use CDP only for extra proof/debugging, not as the primary success signal

Useful optional checks:

- `WebAuthn.getCredentials` after registration to confirm a credential exists
- `WebAuthn.getCredentials` after login to confirm `signCount` increased
- `client.once('WebAuthn.credentialAdded', ...)`
- `client.once('WebAuthn.credentialAsserted', ...)`

But the main pass/fail signal for this repo should still be the actual `/auth/*` responses and session behavior.

## How this maps to the blocked auth validations

### `VAL-AUTH-005`

Flow:

1. register a fresh user via browser ceremony
2. logout
3. call `/auth/login/begin` for that user
4. complete `navigator.credentials.get()`
5. POST `/auth/login/finish`

Assertions:

- `/auth/login/begin` returns assertion options
- `/auth/login/finish` succeeds

### `VAL-AUTH-006`

Best practical proof:

1. capture a successful register-finish or login-finish payload
2. replay the exact same payload
3. assert 4xx and no new valid session minted

Because the repo deletes challenge state immediately, this directly proves replay protection.

Optional extensions later:

- use `WebAuthn.setUserVerified(false)` to simulate failed verification
- use `WebAuthn.setResponseOverrideBits` to simulate bogus signature / UV / UP failures

### `VAL-AUTH-007`

After a successful finish:

- call `GET /auth/session`
- assert `authenticated: true`
- assert user identity is returned
- assert no secrets/tokens/passkey material are present

### `VAL-AUTH-008`

After a successful authenticated state:

1. `POST /auth/logout`
2. `GET /auth/session` should be signed out
3. repeat `POST /auth/logout`
4. still no 500 / still signed out

## Local vs deployed shape

## Deployed validation

This should be the **acceptance** target because `.factory/library/user-testing.md` says the primary surface is `https://draft.choir-ip.com`.

Recommended command shape:

```sh
PLAYWRIGHT_BASE_URL=https://draft.choir-ip.com pnpm exec playwright test
```

## Local iteration

Recommended local default:

- page base URL: `http://localhost:4173`
- auth service: local `cmd/auth`
- cookies insecure (`AUTH_COOKIE_SECURE=false`)
- RP origin/id defaults already line up with this

Two viable local modes:

### Preferred local mode: same-origin-shaped

Add Vite proxy rules for `/auth` (and later `/api`) so the same relative-URL tests work both locally and on staging.

Why preferred:

- keeps test code identical across local and deployed
- mirrors Caddy’s production routing
- avoids acceptance drift

### Lower-effort local shortcut

Skip the Vite proxy and point Playwright request calls directly at `http://localhost:8081`.

This can still work because:

- the page origin remains `http://localhost:4173` for WebAuthn
- Playwright request context shares browser cookies
- cookies are host-based, not port-based

But this is less production-shaped, so I would treat it as a shortcut, not the final validation shape.

## Suggested config direction

Playwright’s `webServer` support is a good fit here because it can launch multiple local processes.

Source:

- <https://playwright.dev/docs/test-webserver>

Sketch:

```js
export default defineConfig({
  testDir: './tests',
  use: {
    baseURL: process.env.PLAYWRIGHT_BASE_URL ?? 'http://localhost:4173',
  },
  projects: [
    {
      name: 'chromium',
      use: { browserName: 'chromium' },
    },
  ],
  webServer: process.env.PLAYWRIGHT_BASE_URL
    ? undefined
    : [
        {
          name: 'auth',
          command: 'go run ./cmd/auth',
          cwd: '..',
          url: 'http://127.0.0.1:8081/health',
          reuseExistingServer: !process.env.CI,
        },
        {
          name: 'frontend',
          command: 'pnpm dev --host localhost --port 4173',
          cwd: '.',
          url: 'http://localhost:4173',
          reuseExistingServer: !process.env.CI,
        },
      ],
});
```

## Dependencies / commands likely needed

## Node dependencies

Likely enough:

```sh
cd frontend
pnpm add -D @playwright/test
pnpm exec playwright install chromium
```

For CI on Ubuntu:

```sh
cd frontend
pnpm exec playwright install --with-deps chromium
```

## Likely package scripts

Examples:

```json
{
  "scripts": {
    "test:e2e:auth": "playwright test",
    "test:e2e:auth:headed": "playwright test --headed"
  }
}
```

## Likely local run sequence

If using Playwright `webServer`, the suite can own process startup.

If not:

```sh
./.factory/init.sh
go run ./cmd/auth
cd frontend && pnpm dev
cd frontend && pnpm exec playwright test
```

## Main caveats

1. **Chromium only.**  
   This is the major constraint. Playwright’s CDP route is Chromium-only, and Playwright still has no browser-agnostic WebAuthn API.

2. **Create the CDP session after the page exists, and do it per test.**  
   Reusing one setup across different Playwright-created pages is a common footgun.

3. **Use `localhost`, not `127.0.0.1`, as the browser page origin for local runs.**  
   The repo’s default RP ID is `localhost`. `127.0.0.1` is trustworthy, but it is not the same RP ID.

4. **Do not manually reshape begin options unless necessary.**  
   Pass the server JSON through `parseCreationOptionsFromJSON()` / `parseRequestOptionsFromJSON()` directly.

5. **The new JSON helper APIs assume a modern browser.**  
   They are supported in current Chromium, but if Playwright/browser is pinned too old, fallback helper code will be needed.

6. **The current repo does not yet have a real auth UI.**  
   That is okay for auth-service validation; the tests can call browser WebAuthn APIs directly from the origin page.

7. **If a test starts failing mysteriously, debug with headed Chrome.**  
   Community debugging around passkeys often uses `channel: 'chrome', headless: false` and `chrome://device-log` to inspect authenticator behavior.

## Bottom line

The best current path is **not** a third-party passkey helper package and **not** manual credential seeding.

The best path is:

- Playwright test suite in `frontend/`
- Chromium project only
- CDP virtual authenticator per test/page
- browser-side `navigator.credentials.*`
- request-side `/auth/*` assertions with shared cookie jar
- one baseURL-driven suite runnable against both localhost and `draft.choir-ip.com`

That will unblock automated proof for `VAL-AUTH-005`..`008` with the least mismatch against how this repo actually works.

## Paper trail

### Repo files reviewed

- `/Users/wiz/go-choir/README.md`
- `/Users/wiz/go-choir/frontend/package.json`
- `/Users/wiz/go-choir/frontend/src/App.svelte`
- `/Users/wiz/go-choir/frontend/vite.config.js`
- `/Users/wiz/go-choir/internal/auth/config.go`
- `/Users/wiz/go-choir/cmd/auth/main.go`
- `/Users/wiz/go-choir/nix/node-b.nix`
- `/Users/wiz/go-choir/.factory/library/user-testing.md`
- `/Users/wiz/go-choir/.factory/library/auth-storage.md`
- `/Users/wiz/go-choir/.factory/validation/auth-foundation/user-testing/flows/auth-curl.json`
- `/Users/wiz/go-choir/.github/workflows/ci.yml`

### Web sources reviewed

- Playwright CDP docs: <https://playwright.dev/docs/api/class-cdpsession>
- Playwright browser context docs: <https://playwright.dev/docs/api/class-browsercontext>
- Playwright API request context docs: <https://playwright.dev/docs/api/class-apirequestcontext>
- Playwright webServer docs: <https://playwright.dev/docs/test-webserver>
- Chrome DevTools Protocol WebAuthn domain: <https://chromedevtools.github.io/devtools-protocol/tot/WebAuthn/>
- Chrome DevTools WebAuthn panel docs: <https://developer.chrome.com/docs/devtools/webauthn>
- MDN WebAuthn overview: <https://developer.mozilla.org/en-US/docs/Web/API/Web_Authentication_API>
- MDN secure contexts: <https://developer.mozilla.org/en-US/docs/Web/Security/Defenses/Secure_Contexts>
- MDN `parseCreationOptionsFromJSON()`: <https://developer.mozilla.org/en-US/docs/Web/API/PublicKeyCredential/parseCreationOptionsFromJSON_static>
- MDN `parseRequestOptionsFromJSON()`: <https://developer.mozilla.org/en-US/docs/Web/API/PublicKeyCredential/parseRequestOptionsFromJSON_static>
- MDN `toJSON()`: <https://developer.mozilla.org/en-US/docs/Web/API/PublicKeyCredential/toJSON>
- Playwright WebAuthn feature issue: <https://github.com/microsoft/playwright/issues/7276>
- Playwright WebKit virtual authenticator request: <https://github.com/microsoft/playwright/issues/26621>
- SimpleWebAuthn discussion with working Playwright/CDP patterns and pitfalls: <https://github.com/MasterKale/SimpleWebAuthn/discussions/678>
- Corbado passkey Playwright guide (updated 2026): <https://www.corbado.com/blog/passkeys-e2e-playwright-testing-webauthn-virtual-authenticator>
