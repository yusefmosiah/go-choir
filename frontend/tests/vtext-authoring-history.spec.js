import { test, expect } from './helpers/fixtures.js';

async function openVText(page) {
  await page.locator('[data-desktop-icon-id="vtext"]').dblclick();
  await page.locator('[data-vtext-editor]').waitFor({ state: 'visible', timeout: 10000 });
}

test('vtext uses the document surface as the window and exposes version navigation', async ({ desktopSession }) => {
  const { page } = desktopSession;
  await openVText(page);

  const editor = page.locator('[data-vtext-editor-area]');
  const prev = page.locator('[data-vtext-prev]');
  const next = page.locator('[data-vtext-next]');

  await expect(editor).toBeVisible();
  await expect(prev).toBeDisabled();
  await expect(next).toBeDisabled();

  await editor.fill('Version zero content.\n\nExpand this into a better document.');
  await page.locator('[data-vtext-prompt]').click();

  await expect(page.locator('[data-vtext-save-status]')).toContainText(/First draft ready|Agent created next version/, { timeout: 10000 });
  await expect(prev).toBeEnabled();
  await expect(next).toBeDisabled();
});
