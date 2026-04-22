import { test, expect } from './helpers/fixtures.js';

async function openFilesApp(page) {
  await page.locator('[data-desktop-icon-id="files"]').dblclick();
  await page.locator('[data-file-list]').waitFor({ state: 'visible', timeout: 10000 });
}

async function openVText(page) {
  await page.locator('[data-desktop-icon-id="vtext"]').dblclick();
  await page.locator('[data-vtext-editor]').last().waitFor({ state: 'visible', timeout: 10000 });
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
  await page.locator('[data-vtext-app]').last().waitFor({ state: 'visible', timeout: 10000 });
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

async function listRevisions(page, docId) {
  return page.evaluate(async (docIdValue) => {
    const res = await fetch(`/api/vtext/documents/${encodeURIComponent(docIdValue)}/revisions`, {
      method: 'GET',
      credentials: 'include',
    });
    if (!res.ok) {
      const body = await res.text();
      throw new Error(`failed to list revisions: ${res.status} ${body}`);
    }
    return res.json();
  }, docId);
}

async function waitForRevisionTotal(page, docId, want, timeout = 12000) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    const revisions = await listRevisions(page, docId);
    if ((revisions.revisions || []).length >= want) {
      return revisions;
    }
    await page.waitForTimeout(200);
  }
  const revisions = await listRevisions(page, docId);
  throw new Error(`document ${docId} did not reach ${want} revisions, got ${(revisions.revisions || []).length}`);
}

async function submitTestResearchFindings(page, payload) {
  return page.evaluate(async (body) => {
    const res = await fetch('/api/test/vtext/research-findings', {
      method: 'POST',
      credentials: 'include',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    });
    if (!res.ok) {
      const err = await res.text();
      throw new Error(`failed to submit research findings: ${res.status} ${err}`);
    }
    return res.json();
  }, payload);
}

test('vtext auto-follows latest head when the editor is clean', async ({ desktopSession }) => {
  const { page } = desktopSession;
  const fileName = `auto-follow-${Date.now()}.txt`;
  const initialContent = 'Initial version from file open';
  const externalContent = 'External clean-head update';

  await seedTextFile(page, fileName, initialContent);
  const opened = await openFileInVText(page, fileName);

  const editor = page.locator('[data-vtext-app] [data-vtext-editor-area]').last();
  await expect(editor).toHaveValue(initialContent);

  await createExternalRevision(page, opened.doc_id, opened.current_revision_id, externalContent);

  await expect(editor).toHaveValue(externalContent, { timeout: 10000 });
  await expect(page.locator('[data-vtext-new-version]')).toHaveCount(0);
});

test('vtext keeps dirty edits and offers latest-head jump instead of auto-clobbering', async ({ desktopSession }) => {
  const { page } = desktopSession;
  const fileName = `dirty-protection-${Date.now()}.txt`;
  const initialContent = 'Seed content from file open';
  const dirtyContent = 'Local unsaved draft that must stay put';
  const externalContent = 'External update should not auto-overwrite local draft';

  await seedTextFile(page, fileName, initialContent);
  const opened = await openFileInVText(page, fileName);

  const editor = page.locator('[data-vtext-app] [data-vtext-editor-area]').last();
  await editor.fill(dirtyContent);

  await createExternalRevision(page, opened.doc_id, opened.current_revision_id, externalContent);

  const updateButton = page.locator('[data-vtext-new-version]');
  await expect(updateButton).toBeVisible({ timeout: 10000 });
  await expect(editor).toHaveValue(dirtyContent);

  await updateButton.click();
  await expect(editor).toHaveValue(externalContent, { timeout: 10000 });
  await expect(updateButton).toHaveCount(0);
});

test('reopening the same file path resolves to the same canonical vtext doc', async ({ desktopSession }) => {
  const { page } = desktopSession;
  const fileName = `canonical-alias-${Date.now()}.txt`;
  const initialContent = 'Alias seed content';

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

test('vtext file-backed window restores on reload with the latest canonical head', async ({ desktopSession }) => {
  const { page } = desktopSession;
  const fileName = `restart-recovery-${Date.now()}.txt`;
  const initialContent = 'Initial restart content';
  const externalContent = 'Recovered latest head after reload';

  await seedTextFile(page, fileName, initialContent);
  const opened = await openFileInVText(page, fileName);

  const editor = page.locator('[data-vtext-app] [data-vtext-editor-area]').last();
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

test('submit_research_findings batches rapid worker updates into one auto-advanced next version', async ({ desktopSession }) => {
  const { page } = desktopSession;
  const initialContent = 'Base draft that should get a findings-driven follow-up.';

  await openVText(page);
  const editor = page.locator('[data-vtext-app] [data-vtext-editor-area]').last();
  await editor.fill(initialContent);
  await expect(editor).toHaveValue(initialContent);

  const revisionRequest = page.waitForResponse((response) => {
    return response.request().method() === 'POST' &&
      /\/api\/vtext\/documents\/[^/]+\/agent-revision$/.test(new URL(response.url()).pathname);
  });
  await page.locator('[data-vtext-prompt]').last().click();
  const revisionResponse = await revisionRequest;
  expect(revisionResponse.status()).toBe(202);
  const revisionJSON = await revisionResponse.json();
  await expect(page.locator('[data-vtext-save-status]').last()).toContainText(/First draft ready|Agent created next version/, { timeout: 10000 });

  const baselineRevisions = await listRevisions(page, revisionJSON.doc_id);
  const baselineCount = baselineRevisions.revisions.length;

  await submitTestResearchFindings(page, {
    doc_id: revisionJSON.doc_id,
    finding_id: `finding-a-${Date.now()}`,
    findings: ['Finding A: a new sourced detail arrived.'],
    notes: ['Use a brief update.'],
  });
  await submitTestResearchFindings(page, {
    doc_id: revisionJSON.doc_id,
    finding_id: `finding-b-${Date.now()}`,
    findings: ['Finding B: another sourced detail arrived right after.'],
    notes: ['Still one follow-up revision.'],
  });

  const afterWake = await waitForRevisionTotal(page, revisionJSON.doc_id, baselineCount + 1, 12000);
  expect(afterWake.revisions.length).toBe(baselineCount + 1);
  await expect(page.locator('[data-vtext-new-version]')).toHaveCount(0);
  await expect(page.locator('[data-vtext-version]').last()).toHaveText(`v${afterWake.revisions.length - 1}`);

  await page.waitForTimeout(4000);
  const stableRevisions = await listRevisions(page, revisionJSON.doc_id);
  expect(stableRevisions.revisions.length).toBe(baselineCount + 1);
});
