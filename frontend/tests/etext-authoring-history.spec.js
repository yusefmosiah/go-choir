/**
 * Playwright tests for the e-text authoring and history UI
 * (VAL-ETEXT-001 through VAL-ETEXT-010).
 *
 * These tests verify that:
 * - Users can create and directly edit documents with canonical user
 *   revisions (VAL-ETEXT-001, VAL-ETEXT-002)
 * - Latest saved state survives reload and fresh login for the same user
 *   (VAL-ETEXT-005)
 * - Version history lists revisions with explicit attribution metadata
 *   (VAL-ETEXT-006)
 * - Historical snapshots can be opened without mutating head (VAL-ETEXT-007)
 * - Diff view compares selected revisions and changed sections (VAL-ETEXT-008)
 * - Blame identifies the last editor per section (VAL-ETEXT-009)
 * - Citations and metadata persist with document history (VAL-ETEXT-010)
 *
 * Uses the Playwright Chromium virtual-authenticator harness to register
 * a passkey and authenticate before testing the e-text UI.
 */
import { test, expect } from './helpers/fixtures.js';
import { registerPasskey, getSession, loginPasskey } from './helpers/auth.js';

const BASE_URL = 'http://localhost:4173';

function uniqueEmail() {
  return `etext-test-${Date.now()}-${Math.random().toString(36).slice(2, 8)}@example.com`;
}

// Helper: register a passkey and get to the authenticated desktop.
async function registerAndLoadDesktop(page, authenticator, email) {
  await page.goto(BASE_URL);
  await registerPasskey(page, email, BASE_URL);
  await page.reload();
  await page.locator('[data-desktop]').waitFor({ state: 'visible', timeout: 10000 });
}

// Helper: open E-Text from the launcher and wait for the editor.
async function openEText(page) {
  const launcherToggle = page.locator('[data-launcher-toggle]');
  await launcherToggle.click();
  const etextEntry = page.locator('[data-app-id="etext"]');
  await etextEntry.click();
  await page.locator('[data-etext-editor]').waitFor({ state: 'visible', timeout: 10000 });
}

// ---------------------------------------------------------------
// Test: user can create a document (VAL-ETEXT-001)
// ---------------------------------------------------------------
test('user can create a document', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);
  await openEText(page);

  // Click the "New Document" button.
  const newDocBtn = page.locator('[data-etext-newdoc]');
  await expect(newDocBtn).toBeVisible();
  await newDocBtn.click();

  // The new document form should appear.
  const titleInput = page.locator('[data-etext-newdoc-title]');
  await expect(titleInput).toBeVisible();

  // Enter a title and create the document.
  await titleInput.fill('My First Document');
  const submitBtn = page.locator('[data-etext-newdoc-submit]');
  await submitBtn.click();

  // The editor view should appear with the document title.
  const titleEl = page.locator('[data-etext-title]');
  await expect(titleEl).toContainText('My First Document');

  // The editing textarea should be visible.
  const editorArea = page.locator('[data-etext-editor-area]');
  await expect(editorArea).toBeVisible();
});

// ---------------------------------------------------------------
// Test: direct user edits create canonical user-authored revisions
// (VAL-ETEXT-002)
// ---------------------------------------------------------------
test('direct user edits create canonical user-authored revisions', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);
  await openEText(page);

  // Create a document.
  await page.locator('[data-etext-newdoc]').click();
  await page.locator('[data-etext-newdoc-title]').fill('Revision Test');
  await page.locator('[data-etext-newdoc-submit]').click();

  // Wait for the editor to appear.
  const editorArea = page.locator('[data-etext-editor-area]');
  await expect(editorArea).toBeVisible({ timeout: 5000 });

  // Type some content.
  await editorArea.fill('Hello, this is my first edit.');

  // Save the revision.
  const saveBtn = page.locator('[data-etext-save]');
  await saveBtn.click();

  // Wait for the save status.
  const saveStatus = page.locator('[data-etext-save-status]');
  await expect(saveStatus).toContainText('Saved', { timeout: 5000 });

  // Open the history view.
  const historyBtn = page.locator('[data-etext-history-btn]');
  await historyBtn.click();

  // The history should list at least one revision.
  const historyEntries = page.locator('[data-etext-history-entry]');
  await expect(historyEntries).toHaveCount(1, { timeout: 5000 });

  // The revision should be attributed to the user (author_kind=user).
  const authorEl = historyEntries.first().locator('[data-etext-history-author-kind]');
  await expect(authorEl).toHaveAttribute('data-etext-history-author-kind', 'user');

  // The author label should contain the email.
  const authorText = await authorEl.textContent();
  expect(authorText).toContain(email);
});

// ---------------------------------------------------------------
// Test: latest revision survives reload and fresh login for the
// same user (VAL-ETEXT-005)
// ---------------------------------------------------------------
test('latest revision survives reload', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);
  await openEText(page);

  // Create a document with content.
  await page.locator('[data-etext-newdoc]').click();
  await page.locator('[data-etext-newdoc-title]').fill('Persistence Test');
  await page.locator('[data-etext-newdoc-submit]').click();

  const editorArea = page.locator('[data-etext-editor-area]');
  await expect(editorArea).toBeVisible({ timeout: 5000 });
  await editorArea.fill('Content before reload');

  const saveBtn = page.locator('[data-etext-save]');
  await saveBtn.click();
  await expect(page.locator('[data-etext-save-status]')).toContainText('Saved', { timeout: 5000 });

  // Wait for desktop state to persist.
  await page.waitForTimeout(1500);

  // Reload the page.
  await page.reload();
  await page.locator('[data-desktop]').waitFor({ state: 'visible', timeout: 10000 });
  await page.waitForTimeout(1500);

  // The desktop should restore the window. Open E-Text from launcher
  // if window was not restored, or look for the editor in the restored window.
  const etextApp = page.locator('[data-etext-app]');
  if (await etextApp.isVisible()) {
    // Window was restored from desktop state.
  } else {
    // Open E-Text again from the launcher.
    await openEText(page);
  }

  // The document list should contain the document we created.
  const docItem = page.locator('[data-etext-docitem]');
  await expect(docItem).toHaveCount(1, { timeout: 5000 });
  await expect(docItem).toContainText('Persistence Test');

  // Open the document.
  await docItem.locator('.doc-item-btn').click();

  // The editor should show the saved content.
  const editorAfterReload = page.locator('[data-etext-editor-area]');
  await expect(editorAfterReload).toHaveValue('Content before reload', { timeout: 5000 });
});

// ---------------------------------------------------------------
// Test: version history lists revisions with explicit attribution
// metadata (VAL-ETEXT-006)
// ---------------------------------------------------------------
test('version history lists revisions with explicit attribution', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);
  await openEText(page);

  // Create a document with two user revisions.
  await page.locator('[data-etext-newdoc]').click();
  await page.locator('[data-etext-newdoc-title]').fill('History Test');
  await page.locator('[data-etext-newdoc-submit]').click();

  const editorArea = page.locator('[data-etext-editor-area]');
  await expect(editorArea).toBeVisible({ timeout: 5000 });

  // First edit.
  await editorArea.fill('First edit');
  await page.locator('[data-etext-save]').click();
  await expect(page.locator('[data-etext-save-status]')).toContainText('Saved', { timeout: 5000 });

  // Second edit.
  await editorArea.fill('Second edit');
  await page.locator('[data-etext-save]').click();
  await expect(page.locator('[data-etext-save-status]')).toContainText('Saved', { timeout: 5000 });

  // Open history.
  await page.locator('[data-etext-history-btn]').click();

  // History should list two revisions.
  const historyEntries = page.locator('[data-etext-history-entry]');
  await expect(historyEntries).toHaveCount(2, { timeout: 5000 });

  // Both should be attributed to the user.
  const authorElements = page.locator('[data-etext-history-author-kind="user"]');
  await expect(authorElements).toHaveCount(2);

  // Each should show the email.
  for (const el of await authorElements.all()) {
    const text = await el.textContent();
    expect(text).toContain(email);
  }
});

// ---------------------------------------------------------------
// Test: historical snapshots can be opened without mutating head
// (VAL-ETEXT-007)
// ---------------------------------------------------------------
test('historical snapshots can be opened without mutating head', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);
  await openEText(page);

  // Create a document with two revisions.
  await page.locator('[data-etext-newdoc]').click();
  await page.locator('[data-etext-newdoc-title]').fill('Snapshot Test');
  await page.locator('[data-etext-newdoc-submit]').click();

  const editorArea = page.locator('[data-etext-editor-area]');
  await expect(editorArea).toBeVisible({ timeout: 5000 });

  await editorArea.fill('Original content');
  await page.locator('[data-etext-save]').click();
  await expect(page.locator('[data-etext-save-status]')).toContainText('Saved', { timeout: 5000 });

  await editorArea.fill('Updated content');
  await page.locator('[data-etext-save]').click();
  await expect(page.locator('[data-etext-save-status]')).toContainText('Saved', { timeout: 5000 });

  // Open history and view the first revision.
  await page.locator('[data-etext-history-btn]').click();
  const historyEntries = page.locator('[data-etext-history-entry]');
  await expect(historyEntries).toHaveCount(2, { timeout: 5000 });

  // Click "View" on the older revision (last in the list).
  const viewButtons = page.locator('[data-etext-history-entry] .btn-small');
  // The older revision is the second entry in the list.
  await historyEntries.last().locator('.btn-small').first().click();

  // The snapshot view should show the older content.
  const snapshotContent = page.locator('[data-etext-snapshot-content]');
  await expect(snapshotContent).toBeVisible({ timeout: 5000 });
  await expect(snapshotContent).toContainText('Original content');

  // The snapshot notice should be visible.
  const snapshotNotice = page.locator('.snapshot-notice');
  await expect(snapshotNotice).toBeVisible();

  // Go back to the editor.
  await page.locator('.snapshot-header .btn-small').click();
  // Go back to editor from history.
  await page.locator('.history-header .btn-small').click();

  // The editor should still show the latest content (head not mutated).
  const editorAfterSnapshot = page.locator('[data-etext-editor-area]');
  await expect(editorAfterSnapshot).toHaveValue('Updated content', { timeout: 5000 });
});

// ---------------------------------------------------------------
// Test: diff view compares selected revisions and changed sections
// (VAL-ETEXT-008)
// ---------------------------------------------------------------
test('diff view compares selected revisions', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);
  await openEText(page);

  // Create a document with two revisions.
  await page.locator('[data-etext-newdoc]').click();
  await page.locator('[data-etext-newdoc-title]').fill('Diff Test');
  await page.locator('[data-etext-newdoc-submit]').click();

  const editorArea = page.locator('[data-etext-editor-area]');
  await expect(editorArea).toBeVisible({ timeout: 5000 });

  await editorArea.fill('Line 1\nLine 2\nLine 3');
  await page.locator('[data-etext-save]').click();
  await expect(page.locator('[data-etext-save-status]')).toContainText('Saved', { timeout: 5000 });

  await editorArea.fill('Line 1\nLine 2 modified\nLine 3\nLine 4 new');
  await page.locator('[data-etext-save]').click();
  await expect(page.locator('[data-etext-save-status]')).toContainText('Saved', { timeout: 5000 });

  // Open history.
  await page.locator('[data-etext-history-btn]').click();
  const historyEntries = page.locator('[data-etext-history-entry]');
  await expect(historyEntries).toHaveCount(2, { timeout: 5000 });

  // Click the "Diff ↓" button on the newer revision.
  // The newer revision is the first entry.
  const diffBtn = historyEntries.first().locator('.btn-small').filter({ hasText: 'Diff' });
  await diffBtn.first().click();

  // The diff view should appear.
  const diffSections = page.locator('[data-etext-diff-sections]');
  await expect(diffSections).toBeVisible({ timeout: 5000 });

  // The diff stats should show changes.
  const diffStats = page.locator('[data-etext-diff-stats]');
  await expect(diffStats).toBeVisible();
  const statsText = await diffStats.textContent();
  // There should be some added or removed lines.
  expect(statsText).toMatch(/\+(\d+)/);
});

// ---------------------------------------------------------------
// Test: blame identifies the last editor per section (VAL-ETEXT-009)
// ---------------------------------------------------------------
test('blame identifies the last editor per section', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);
  await openEText(page);

  // Create a document with content.
  await page.locator('[data-etext-newdoc]').click();
  await page.locator('[data-etext-newdoc-title]').fill('Blame Test');
  await page.locator('[data-etext-newdoc-submit]').click();

  const editorArea = page.locator('[data-etext-editor-area]');
  await expect(editorArea).toBeVisible({ timeout: 5000 });

  await editorArea.fill('Line 1\nLine 2\nLine 3');
  await page.locator('[data-etext-save]').click();
  await expect(page.locator('[data-etext-save-status]')).toContainText('Saved', { timeout: 5000 });

  // Open history and click Blame on the user revision.
  await page.locator('[data-etext-history-btn]').click();
  const historyEntries = page.locator('[data-etext-history-entry]');
  await expect(historyEntries).toHaveCount(1, { timeout: 5000 });

  // Click the "Blame" button.
  const blameBtn = historyEntries.first().locator('.btn-small').filter({ hasText: 'Blame' });
  await blameBtn.click();

  // The blame view should appear.
  const blameSections = page.locator('[data-etext-blame-sections]');
  await expect(blameSections).toBeVisible({ timeout: 5000 });

  // Each blame section should have user attribution.
  const blameAuthorKinds = page.locator('[data-etext-blame-author-kind]');
  const count = await blameAuthorKinds.count();
  expect(count).toBeGreaterThan(0);

  // All sections should be attributed to the user.
  for (const el of await blameAuthorKinds.all()) {
    const kind = await el.getAttribute('data-etext-blame-author-kind');
    expect(kind).toBe('user');
  }
});

// ---------------------------------------------------------------
// Test: citations and metadata persist with document history
// (VAL-ETEXT-010)
// ---------------------------------------------------------------
test('citations and metadata persist with document history', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);
  await openEText(page);

  // Create a document with citations and metadata.
  await page.locator('[data-etext-newdoc]').click();
  await page.locator('[data-etext-newdoc-title]').fill('Citations Test');
  await page.locator('[data-etext-newdoc-submit]').click();

  const editorArea = page.locator('[data-etext-editor-area]');
  await expect(editorArea).toBeVisible({ timeout: 5000 });

  await editorArea.fill('Document with citations');

  // Add citations.
  const citationsSection = page.locator('[data-etext-citations]');
  await citationsSection.locator('summary').click();
  const citationsTextarea = citationsSection.locator('textarea');
  await citationsTextarea.fill('[{"id":"1","type":"url","value":"https://example.com","label":"Example"}]');

  // Add metadata.
  const metadataSection = page.locator('[data-etext-metadata]');
  await metadataSection.locator('summary').click();
  const metadataTextarea = metadataSection.locator('textarea');
  await metadataTextarea.fill('{"source":"test","version":1}');

  // Save the revision.
  await page.locator('[data-etext-save]').click();
  await expect(page.locator('[data-etext-save-status]')).toContainText('Saved', { timeout: 5000 });

  // Make a second edit without citations/metadata changes.
  await editorArea.fill('Updated document with citations');
  await page.locator('[data-etext-save]').click();
  await expect(page.locator('[data-etext-save-status]')).toContainText('Saved', { timeout: 5000 });

  // Open history and view the first revision's snapshot.
  await page.locator('[data-etext-history-btn]').click();
  const historyEntries = page.locator('[data-etext-history-entry]');
  await expect(historyEntries).toHaveCount(2, { timeout: 5000 });

  // View the older (first) revision.
  await historyEntries.last().locator('.btn-small').first().click();

  // The snapshot should include citations and metadata.
  const snapshotCitations = page.locator('[data-etext-snapshot-citations]');
  await expect(snapshotCitations).toBeVisible({ timeout: 5000 });
  await expect(snapshotCitations).toContainText('https://example.com');

  const snapshotMetadata = page.locator('[data-etext-snapshot-metadata]');
  await expect(snapshotMetadata).toBeVisible({ timeout: 5000 });
  await expect(snapshotMetadata).toContainText('test');
});

// ---------------------------------------------------------------
// Test: latest revision survives a fresh login session (VAL-ETEXT-005)
// ---------------------------------------------------------------
test('latest revision survives a fresh login session', async ({ page, authenticator, context, browser }) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);
  await openEText(page);

  // Create a document with content.
  await page.locator('[data-etext-newdoc]').click();
  await page.locator('[data-etext-newdoc-title]').fill('Fresh Session Test');
  await page.locator('[data-etext-newdoc-submit]').click();

  const editorArea = page.locator('[data-etext-editor-area]');
  await expect(editorArea).toBeVisible({ timeout: 5000 });
  await editorArea.fill('Content saved before logout');
  await page.locator('[data-etext-save]').click();
  await expect(page.locator('[data-etext-save-status]')).toContainText('Saved', { timeout: 5000 });

  // Wait for persistence.
  await page.waitForTimeout(1500);

  // Logout.
  await page.locator('[data-desktop-logout]').click();
  await page.locator('[data-auth-entry]').waitFor({ state: 'visible', timeout: 5000 });

  // Log back in as the same user.
  await loginPasskey(page, email, BASE_URL);
  await page.reload();
  await page.locator('[data-desktop]').waitFor({ state: 'visible', timeout: 10000 });

  // Open E-Text.
  await openEText(page);

  // The document list should contain the document.
  const docItem = page.locator('[data-etext-docitem]');
  await expect(docItem).toHaveCount(1, { timeout: 5000 });
  await expect(docItem).toContainText('Fresh Session Test');

  // Open the document.
  await docItem.locator('.doc-item-btn').click();

  // The content should be the saved version.
  const editorAfterLogin = page.locator('[data-etext-editor-area]');
  await expect(editorAfterLogin).toHaveValue('Content saved before logout', { timeout: 5000 });
});
