import { test, expect } from './helpers/fixtures.js';
import { registerPasskey } from './helpers/auth.js';

const BASE_URL = 'http://localhost:4173';

function uniqueEmail() {
  return `vtext-stream-${Date.now()}-${Math.random().toString(36).slice(2, 8)}@example.com`;
}

async function registerAndLoadDesktop(page, email) {
  await page.goto(BASE_URL);
  await registerPasskey(page, email, BASE_URL);
  await page.reload();
  await page.locator('[data-desktop]').waitFor({ state: 'visible', timeout: 10000 });
}

async function openFilesApp(page) {
  await page.locator('[data-desktop-icon-id="files"]').dblclick();
  await page.locator('[data-file-list]').waitFor({ state: 'visible', timeout: 10000 });
}

async function seedTextFile(page, fileName, content) {
  await page.evaluate(async ({ fileName, content }) => {
    const res = await fetch(`/api/files/${encodeURIComponent(fileName)}`, {
      method: 'PUT',
      credentials: 'include',
      headers: { 'Content-Type': 'text/plain; charset=utf-8' },
      body: content,
    });
    if (!res.ok) {
      throw new Error(`failed to seed text file ${fileName}: ${res.status}`);
    }
  }, { fileName, content });
}

async function openFileInVText(page, fileName) {
  await openFilesApp(page);
  const openResponse = page.waitForResponse((response) => {
    const url = new URL(response.url());
    return response.request().method() === 'POST' && url.pathname === '/api/vtext/files/open';
  });
  const fileItem = page.locator('[data-file-item]').filter({ hasText: fileName }).first();
  await expect(fileItem).toBeVisible({ timeout: 5000 });
  await fileItem.click();
  await page.locator('[data-vtext-app]').waitFor({ state: 'visible', timeout: 10000 });
  return (await openResponse).json();
}

async function createExternalRevision(page, docId, parentRevisionId, content) {
  return page.evaluate(async ({ docId, parentRevisionId, content }) => {
    const res = await fetch(`/api/vtext/documents/${encodeURIComponent(docId)}/revisions`, {
      method: 'POST',
      credentials: 'include',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        content,
        author_kind: 'user',
        author_label: 'browser-test',
        parent_revision_id: parentRevisionId,
      }),
    });
    if (!res.ok) {
      const body = await res.text();
      throw new Error(`failed to create external revision: ${res.status} ${body}`);
    }
    return res.json();
  }, { docId, parentRevisionId, content });
}

test('vtext auto-follows latest head when the editor is clean', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  const fileName = 'auto-follow.txt';
  const initialContent = 'Initial version from file open';
  const externalContent = 'External clean-head update';

  await registerAndLoadDesktop(page, email);
  await seedTextFile(page, fileName, initialContent);
  const opened = await openFileInVText(page, fileName);

  const editor = page.locator('[data-vtext-app] [data-vtext-editor-area]');
  await expect(editor).toHaveValue(initialContent);

  await createExternalRevision(page, opened.doc_id, opened.current_revision_id, externalContent);

  await expect(editor).toHaveValue(externalContent, { timeout: 10000 });
  await expect(page.locator('[data-vtext-new-version]')).toHaveCount(0);
});

test('vtext keeps dirty edits and offers latest-head jump instead of auto-clobbering', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  const fileName = 'dirty-protection.txt';
  const initialContent = 'Seed content from file open';
  const dirtyContent = 'Local unsaved draft that must stay put';
  const externalContent = 'External update should not auto-overwrite local draft';

  await registerAndLoadDesktop(page, email);
  await seedTextFile(page, fileName, initialContent);
  const opened = await openFileInVText(page, fileName);

  const editor = page.locator('[data-vtext-app] [data-vtext-editor-area]');
  await editor.fill(dirtyContent);

  await createExternalRevision(page, opened.doc_id, opened.current_revision_id, externalContent);

  const updateButton = page.locator('[data-vtext-new-version]');
  await expect(updateButton).toBeVisible({ timeout: 10000 });
  await expect(editor).toHaveValue(dirtyContent);

  await updateButton.click();
  await expect(editor).toHaveValue(externalContent, { timeout: 10000 });
  await expect(updateButton).toHaveCount(0);
});

test('reopening the same file path resolves to the same canonical vtext doc', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  const fileName = 'canonical-alias.txt';
  const initialContent = 'Alias seed content';

  await registerAndLoadDesktop(page, email);
  await seedTextFile(page, fileName, initialContent);

  const firstOpen = await openFileInVText(page, fileName);
  expect(firstOpen.created).toBe(true);

  const secondOpen = await openFileInVText(page, fileName);
  expect(secondOpen.created).toBe(false);
  expect(secondOpen.doc_id).toBe(firstOpen.doc_id);

  const revisions = await page.evaluate(async (docId) => {
    const res = await fetch(`/api/vtext/documents/${encodeURIComponent(docId)}/revisions`, {
      method: 'GET',
      credentials: 'include',
    });
    if (!res.ok) {
      throw new Error(`failed to list revisions: ${res.status}`);
    }
    return res.json();
  }, firstOpen.doc_id);
  expect(revisions.revisions).toHaveLength(1);
  expect(revisions.revisions[0].content).toBe(initialContent);
});

test('vtext file-backed window restores on reload with the latest canonical head', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  const fileName = 'restart-recovery.txt';
  const initialContent = 'Initial restart content';
  const externalContent = 'Recovered latest head after reload';

  await registerAndLoadDesktop(page, email);
  await seedTextFile(page, fileName, initialContent);
  const opened = await openFileInVText(page, fileName);

  const editor = page.locator('[data-vtext-app] [data-vtext-editor-area]');
  await expect(editor).toHaveValue(initialContent);

  await createExternalRevision(page, opened.doc_id, opened.current_revision_id, externalContent);
  await expect(editor).toHaveValue(externalContent, { timeout: 10000 });

  await page.waitForTimeout(1000);
  await page.reload();
  await page.locator('[data-desktop]').waitFor({ state: 'visible', timeout: 10000 });
  await page.waitForTimeout(1500);

  const restoredEditor = page.locator('[data-vtext-app] [data-vtext-editor-area]').last();
  await expect(restoredEditor).toHaveValue(externalContent, { timeout: 10000 });
});
