import { test, expect } from './helpers/fixtures.js';

async function openVText(page) {
  await page.locator('[data-desktop-icon-id="vtext"]').dblclick();
  await page.locator('[data-vtext-editor]').waitFor({ state: 'visible', timeout: 10000 });
}

test('prompt button submits a vtext agent revision request', async ({ desktopSession }) => {
  const { page } = desktopSession;
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
  await expect(page.locator('[data-vtext-save-status]')).toContainText(/Writing first draft|First draft ready|Agent created next version/);
});
