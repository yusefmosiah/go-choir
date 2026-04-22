import fs from 'fs';
import path from 'path';
import { fileURLToPath } from 'url';
import { registerPasskey } from './auth.js';
import { setupVirtualAuthenticator, removeVirtualAuthenticator } from './webauthn.js';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);

const DEFAULT_BASE_URL = process.env.BASE_URL || 'http://localhost:4173';
const TEST_RESULTS_DIR = path.resolve(__dirname, '../../test-results');
const AUTH_STATE_PATH = path.join(TEST_RESULTS_DIR, 'playwright-auth-state.json');
const AUTH_META_PATH = path.join(TEST_RESULTS_DIR, 'playwright-auth-meta.json');
const AUTH_LOCK_PATH = path.join(TEST_RESULTS_DIR, 'playwright-auth-state.lock');

function uniqueEmail() {
  return `playwright-state-${Date.now()}-${Math.random().toString(36).slice(2, 8)}@example.com`;
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function readStoredState() {
  const meta = JSON.parse(fs.readFileSync(AUTH_META_PATH, 'utf8'));
  return {
    email: meta.email,
    baseURL: meta.baseURL,
    storageStatePath: AUTH_STATE_PATH,
    metaPath: AUTH_META_PATH,
  };
}

async function waitForStoredState(timeoutMs = 30000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (fs.existsSync(AUTH_STATE_PATH) && fs.existsSync(AUTH_META_PATH)) {
      return readStoredState();
    }
    await sleep(250);
  }
  throw new Error('timed out waiting for shared Playwright auth state');
}

export async function createAuthenticatedState(browser, baseURL = DEFAULT_BASE_URL) {
  fs.mkdirSync(TEST_RESULTS_DIR, { recursive: true });
  if (fs.existsSync(AUTH_STATE_PATH) && fs.existsSync(AUTH_META_PATH)) {
    return readStoredState();
  }

  let lockFd = null;
  try {
    lockFd = fs.openSync(AUTH_LOCK_PATH, 'wx');
  } catch (err) {
    if (err && err.code === 'EEXIST') {
      return waitForStoredState();
    }
    throw err;
  }

  if (fs.existsSync(AUTH_STATE_PATH) && fs.existsSync(AUTH_META_PATH)) {
    fs.closeSync(lockFd);
    fs.rmSync(AUTH_LOCK_PATH, { force: true });
    return readStoredState();
  }

  const context = await browser.newContext();
  const page = await context.newPage();
  const { client, authenticatorId } = await setupVirtualAuthenticator(page);
  const email = uniqueEmail();

  try {
    await page.goto(baseURL);
    await registerPasskey(page, email, baseURL);
    await page.reload();
    await page.locator('[data-desktop]').waitFor({ state: 'visible', timeout: 15000 });

    await context.storageState({ path: AUTH_STATE_PATH });
    fs.writeFileSync(
      AUTH_META_PATH,
      JSON.stringify({ email, baseURL }, null, 2),
      'utf8',
    );

    return {
      email,
      baseURL,
      storageStatePath: AUTH_STATE_PATH,
      metaPath: AUTH_META_PATH,
    };
  } finally {
    await removeVirtualAuthenticator(client, authenticatorId);
    await context.close();
    if (lockFd !== null) {
      fs.closeSync(lockFd);
      fs.rmSync(AUTH_LOCK_PATH, { force: true });
    }
  }
}
