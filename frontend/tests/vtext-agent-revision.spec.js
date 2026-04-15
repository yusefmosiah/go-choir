import { test, expect } from './helpers/fixtures.js';
import { registerPasskey } from './helpers/auth.js';

const BASE_URL = 'http://localhost:4173';

function uniqueEmail() {
  return `vtext-agent-${Date.now()}-${Math.random().toString(36).slice(2, 8)}@example.com`;
}

async function registerAndLoadDesktop(page, email) {
  await page.goto(BASE_URL);
  await registerPasskey(page, email, BASE_URL);
  await page.reload();
  await page.locator('[data-desktop]').waitFor({ state: 'visible', timeout: 10000 });
}

async function openVText(page) {
  await page.locator('[data-desktop-icon-id="vtext"]').dblclick();
  await page.locator('[data-vtext-editor]').waitFor({ state: 'visible', timeout: 10000 });
}

test('prompt button submits a vtext agent revision request', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, email);
  await openVText(page);

  const editor = page.locator('[data-vtext-editor-area]');
  await editor.fill('Draft version with a note to expand the plan.');

  const revisionRequest = page.waitForResponse((response) => {
    return response.request().method() === 'POST' &&
      /\/api\/vtext\/documents\/[^/]+\/agent-revision$/.test(new URL(response.url()).pathname);
  });

  await page.locator('[data-vtext-prompt]').click();

  const response = await revisionRequest;
  expect(response.status()).toBe(202);
  await expect(page.locator('[data-vtext-save-status]')).toContainText(/Submitting|Prompting|Agent created next version/);
});
