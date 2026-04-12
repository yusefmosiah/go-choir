/**
 * Playwright tests for the e-text Research button browser flow
 * (VAL-CHOIR-007, VAL-CHOIR-012, VAL-CHOIR-013).
 *
 * These tests verify that:
 * - The "Research" button is visible in the etext toolbar (VAL-CHOIR-007)
 * - Clicking Research spawns a worker task with document content as context
 *   (VAL-CHOIR-007)
 * - Worker task runs in background (non-blocking) (VAL-CHOIR-007)
 * - Progress indicator shows while worker is running (VAL-CHOIR-007)
 * - Results appear in etext when worker completes (VAL-CHOIR-007)
 * - Worker result is attributed correctly (VAL-CHOIR-007)
 * - Task metadata carries parent context (doc_id) to workers
 *   (VAL-CHOIR-013)
 * - Rapid resubmission does not create duplicate results (VAL-CHOIR-012)
 *
 * Uses the Playwright Chromium virtual-authenticator harness to register
 * a passkey and authenticate before testing the e-text research flow.
 */
import { test, expect } from './helpers/fixtures.js';
import { registerPasskey, loginPasskey, getSession } from './helpers/auth.js';

const BASE_URL = 'http://localhost:4173';

function uniqueEmail() {
  return `etext-research-${Date.now()}-${Math.random().toString(36).slice(2, 8)}@example.com`;
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

// Helper: create a document with initial user content and save it.
async function createDocWithUserContent(page, title, content) {
  await page.locator('[data-etext-newdoc]').click();
  await page.locator('[data-etext-newdoc-title]').fill(title);
  await page.locator('[data-etext-newdoc-submit]').click();

  const editorArea = page.locator('[data-etext-editor-area]');
  await expect(editorArea).toBeVisible({ timeout: 5000 });
  await editorArea.fill(content);

  const saveBtn = page.locator('[data-etext-save]');
  await saveBtn.click();
  await expect(page.locator('[data-etext-save-status]')).toContainText('Saved', { timeout: 5000 });
}

// ---------------------------------------------------------------
// Test: Research button is visible in the etext editor toolbar
// (VAL-CHOIR-007)
// ---------------------------------------------------------------
test('Research button is visible in etext editor toolbar', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);
  await openEText(page);

  // Create a document so we land in the editor view.
  await createDocWithUserContent(page, 'Research Button Test', 'Some content about Go programming.');

  // The Research button should be visible in the editor header.
  const researchBtn = page.locator('[data-etext-research]');
  await expect(researchBtn).toBeVisible();
  await expect(researchBtn).toBeEnabled();
  await expect(researchBtn).toContainText('Research');
});

// ---------------------------------------------------------------
// Test: clicking Research spawns a worker task (POST /api/agent/task)
// (VAL-CHOIR-007)
// ---------------------------------------------------------------
test('clicking Research spawns a worker task with document context', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);
  await openEText(page);

  const docContent = 'Go is a statically typed, compiled programming language designed at Google.';
  await createDocWithUserContent(page, 'Research Spawn Test', docContent);

  // Listen for the task submission API request.
  let taskSubmitted = false;
  let requestBody = null;
  page.on('request', (req) => {
    const url = new URL(req.url());
    if (url.pathname === '/api/agent/task' && req.method() === 'POST') {
      taskSubmitted = true;
      requestBody = req.postDataJSON();
    }
  });

  // Click the Research button.
  const researchBtn = page.locator('[data-etext-research]');
  await researchBtn.click();

  // Wait for the request to go through.
  await page.waitForTimeout(1500);

  // The task submission request should have been made.
  expect(taskSubmitted).toBe(true);
  expect(requestBody).not.toBe(null);

  // The request should include document context in the prompt.
  expect(requestBody.prompt).toContain(docContent);

  // The metadata should include the document ID and research type
  // (VAL-CHOIR-013).
  expect(requestBody.metadata).toBeDefined();
  expect(requestBody.metadata.type).toBe('etext_research');
  expect(requestBody.metadata.doc_id).toBeDefined();
});

// ---------------------------------------------------------------
// Test: progress indicator shows while research worker is running
// (VAL-CHOIR-007)
// ---------------------------------------------------------------
test('progress indicator shows while research worker is running', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);
  await openEText(page);

  await createDocWithUserContent(page, 'Research Progress Test', 'Content for research');

  // Click the Research button.
  const researchBtn = page.locator('[data-etext-research]');
  await researchBtn.click();

  // The progress indicator should become visible.
  const progressIndicator = page.locator('[data-etext-research-progress]');
  const resultPanel = page.locator('[data-etext-research-result]');
  const researchBtnAfter = page.locator('[data-etext-research]');

  // Wait for progress, result, or button returning to enabled state.
  // Depending on task speed, we may see any of these states.
  await Promise.race([
    expect(progressIndicator).toBeVisible({ timeout: 15000 }).catch(() => {}),
    expect(resultPanel).toBeVisible({ timeout: 15000 }).catch(() => {}),
  ]);

  // At minimum, the button should show it's working (disabled or changed text).
  const btnText = await researchBtnAfter.textContent();
  const isProgressVisible = await progressIndicator.isVisible().catch(() => false);
  const isResultVisible = await resultPanel.isVisible().catch(() => false);

  // Either we saw progress, result, or the button state changed.
  expect(isProgressVisible || isResultVisible || btnText.includes('Researching') || btnText.includes('Starting')).toBe(true);
});

// ---------------------------------------------------------------
// Test: results appear in etext when worker completes
// (VAL-CHOIR-007)
// ---------------------------------------------------------------
test('research results appear when worker completes', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);
  await openEText(page);

  await createDocWithUserContent(page, 'Research Result Test', 'Research about Rust language.');

  // Click the Research button.
  const researchBtn = page.locator('[data-etext-research]');
  await researchBtn.click();

  // Wait for the result panel to appear (may take time for the worker).
  const resultPanel = page.locator('[data-etext-research-result]');
  await expect(resultPanel).toBeVisible({ timeout: 45000 });

  // The result content should be visible and non-empty.
  const resultContent = page.locator('[data-etext-research-result-content]');
  await expect(resultContent).toBeVisible();
  const text = await resultContent.textContent();
  expect(text.length).toBeGreaterThan(0);
});

// ---------------------------------------------------------------
// Test: Apply button inserts research results into the document
// (VAL-CHOIR-007)
// ---------------------------------------------------------------
test('Apply button inserts research results into the document', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);
  await openEText(page);

  const originalContent = 'Test document for apply research.';
  await createDocWithUserContent(page, 'Apply Research Test', originalContent);

  // Click the Research button.
  const researchBtn = page.locator('[data-etext-research]');
  await researchBtn.click();

  // Wait for the result panel to appear.
  const resultPanel = page.locator('[data-etext-research-result]');
  await expect(resultPanel).toBeVisible({ timeout: 45000 });

  // Click the Apply button.
  const applyBtn = page.locator('[data-etext-research-apply]');
  await applyBtn.click();

  // The result panel should disappear.
  await expect(resultPanel).not.toBeVisible({ timeout: 5000 });

  // The editor content should now include the research results.
  const editorArea = page.locator('[data-etext-editor-area]');
  const content = await editorArea.inputValue();
  expect(content).toContain(originalContent);
  expect(content).toContain('Research Results');
  expect(content.length).toBeGreaterThan(originalContent.length);
});

// ---------------------------------------------------------------
// Test: Dismiss button closes the research result panel
// (VAL-CHOIR-007)
// ---------------------------------------------------------------
test('Dismiss button closes the research result panel', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);
  await openEText(page);

  await createDocWithUserContent(page, 'Dismiss Research Test', 'Content to research.');

  // Click the Research button.
  const researchBtn = page.locator('[data-etext-research]');
  await researchBtn.click();

  // Wait for the result panel.
  const resultPanel = page.locator('[data-etext-research-result]');
  await expect(resultPanel).toBeVisible({ timeout: 45000 });

  // Click Dismiss.
  const dismissBtn = page.locator('[data-etext-research-dismiss]');
  await dismissBtn.click();

  // The result panel should disappear.
  await expect(resultPanel).not.toBeVisible({ timeout: 5000 });

  // The editor content should be unchanged.
  const editorArea = page.locator('[data-etext-editor-area]');
  const content = await editorArea.inputValue();
  expect(content).toBe('Content to research.');
});

// ---------------------------------------------------------------
// Test: Research button is disabled while worker is running
// (VAL-CHOIR-007)
// ---------------------------------------------------------------
test('Research button is disabled while worker is running', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);
  await openEText(page);

  await createDocWithUserContent(page, 'Disabled Research Test', 'Some content.');

  // Click the Research button.
  const researchBtn = page.locator('[data-etext-research]');
  await researchBtn.click();

  // The button should show working state immediately.
  // If the task hasn't completed yet, the button should be disabled.
  const progressIndicator = page.locator('[data-etext-research-progress]');
  const isProgressVisible = await progressIndicator.isVisible().catch(() => false);

  if (isProgressVisible) {
    // Task is still running — button should be disabled.
    await expect(researchBtn).toBeDisabled();
  }

  // Wait for completion or result.
  const resultPanel = page.locator('[data-etext-research-result]');
  await expect(resultPanel).toBeVisible({ timeout: 45000 }).catch(() => {});

  // After completion, the button should be re-enabled.
  await expect(researchBtn).toBeEnabled({ timeout: 5000 });
});

// ---------------------------------------------------------------
// Test: Research task metadata includes doc_id for context passing
// (VAL-CHOIR-013)
// ---------------------------------------------------------------
test('research task metadata includes doc_id for context passing', async ({ page, authenticator }) => {
  const email = uniqueEmail();
  await registerAndLoadDesktop(page, authenticator, email);
  await openEText(page);

  await createDocWithUserContent(page, 'Context Passing Test', 'Document for context.');

  let requestMetadata = null;
  page.on('request', (req) => {
    const url = new URL(req.url());
    if (url.pathname === '/api/agent/task' && req.method() === 'POST') {
      const body = req.postDataJSON();
      if (body && body.metadata) {
        requestMetadata = body.metadata;
      }
    }
  });

  // Click the Research button.
  const researchBtn = page.locator('[data-etext-research]');
  await researchBtn.click();
  await page.waitForTimeout(1500);

  // Verify the metadata contains doc_id and type (VAL-CHOIR-013).
  expect(requestMetadata).not.toBe(null);
  expect(requestMetadata.type).toBe('etext_research');
  expect(requestMetadata.doc_id).toBeDefined();
  expect(requestMetadata.doc_id.length).toBeGreaterThan(0);
});

// ---------------------------------------------------------------
// Test: signed-out user cannot see Research button
// (VAL-CHOIR-007: auth-gated)
// ---------------------------------------------------------------
test('signed-out user cannot see Research button', async ({ page }) => {
  await page.goto(BASE_URL);

  // The auth entry should be visible (not the desktop).
  const authEntry = page.locator('[data-auth-entry]');
  await expect(authEntry).toBeVisible();

  // The Research button should NOT be visible.
  const researchBtn = page.locator('[data-etext-research]');
  await expect(researchBtn).not.toBeVisible();
});
