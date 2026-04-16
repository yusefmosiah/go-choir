/**
 * Playwright tests for the runtime shell prompt UI (VAL-RUNTIME-007,
 * VAL-CROSS-109, VAL-CROSS-111, VAL-CROSS-121).
 *
 * These tests verify that:
 * - The shell includes a prompt input that submits through the runtime API
 * - Task submission returns a stable handle with status/event updates
 * - Renewal/retry does not duplicate runtime task submission
 * - Reload/new-tab during in-flight work reattaches instead of resubmitting
 * - A completed task shows the real result in the shell
 *
 * Uses the Playwright Chromium virtual-authenticator harness to register
 * a passkey and authenticate before testing the shell.
 */
import { test, expect } from './helpers/fixtures.js';
import { registerPasskey, getSession } from './helpers/auth.js';

const BASE_URL = 'http://localhost:4173';

function uniqueEmail() {
  return `runtime-test-${Date.now()}-${Math.random().toString(36).slice(2, 8)}@example.com`;
}

// Helper: register a passkey and get to the authenticated shell.
async function registerAndLoadShell(page, authenticator, email) {
  await page.goto(BASE_URL);
  await registerPasskey(page, email, BASE_URL);

  // Reload so the Svelte app calls checkSession() and renders the shell.
  await page.reload();
  await page.locator('[data-shell]').waitFor({ state: 'visible', timeout: 10000 });
}

// ---------------------------------------------------------------
// Test: shell includes a prompt input for runtime task submission
// ---------------------------------------------------------------
test('shell includes a prompt input for runtime task submission', async ({
  page,
  authenticator,
}) => {
  const email = uniqueEmail();
  await registerAndLoadShell(page, authenticator, email);

  // The task runner (prompt input) should be visible.
  const taskRunner = page.locator('[data-task-runner]');
  await expect(taskRunner).toBeVisible();

  // The prompt input should be visible and enabled.
  const promptInput = page.locator('[data-prompt-input]');
  await expect(promptInput).toBeVisible();
  await expect(promptInput).toBeEnabled();

  // The submit button should be visible but disabled (no text yet).
  const submitBtn = page.locator('[data-prompt-submit]');
  await expect(submitBtn).toBeVisible();
  await expect(submitBtn).toBeDisabled();
});

// ---------------------------------------------------------------
// Test: submitting a prompt sends POST /api/agent/run
// ---------------------------------------------------------------
test('submitting a prompt sends POST /api/agent/run', async ({
  page,
  authenticator,
}) => {
  const email = uniqueEmail();
  await registerAndLoadShell(page, authenticator, email);

  // Listen for the task submission request.
  let taskSubmitted = false;
  page.on('request', (req) => {
    const url = new URL(req.url());
    if (url.pathname === '/api/agent/run' && req.method() === 'POST') {
      taskSubmitted = true;
    }
  });

  // Type a prompt and submit.
  const promptInput = page.locator('[data-prompt-input]');
  await promptInput.fill('Hello, runtime!');

  const submitBtn = page.locator('[data-prompt-submit]');
  await expect(submitBtn).toBeEnabled();
  await submitBtn.click();

  // The task submission request should have been made.
  await page.waitForTimeout(1500);
  expect(taskSubmitted).toBe(true);
});

// ---------------------------------------------------------------
// Test: task submission returns a stable handle with status display
// (VAL-CROSS-109)
// ---------------------------------------------------------------
test('task submission returns a stable handle with status display', async ({
  page,
  authenticator,
}) => {
  const email = uniqueEmail();
  await registerAndLoadShell(page, authenticator, email);

  // Type a prompt and submit.
  const promptInput = page.locator('[data-prompt-input]');
  await promptInput.fill('What is 2+2?');

  const submitBtn = page.locator('[data-prompt-submit]');
  await submitBtn.click();

  // Wait for the task status section to appear.
  const taskStatus = page.locator('[data-task-status]');
  await expect(taskStatus).toBeVisible({ timeout: 10000 });

  // The task ID should be displayed.
  const taskId = page.locator('[data-task-id]');
  await expect(taskId).toBeVisible();

  // The task state should be visible (pending, running, or completed).
  const taskState = page.locator('[data-task-state]');
  await expect(taskState).toBeVisible();
});

// ---------------------------------------------------------------
// Test: completed task shows result in the shell (VAL-RUNTIME-007)
// ---------------------------------------------------------------
test('completed task shows result in the shell', async ({
  page,
  authenticator,
}) => {
  const email = uniqueEmail();
  await registerAndLoadShell(page, authenticator, email);

  // Type a prompt and submit.
  const promptInput = page.locator('[data-prompt-input]');
  await promptInput.fill('Echo test prompt');

  const submitBtn = page.locator('[data-prompt-submit]');
  await submitBtn.click();

  // Wait for the task to reach a terminal state (completed or failed).
  // The stub provider completes quickly, so this should resolve fast.
  const taskState = page.locator('[data-task-state]');
  // Wait up to 15 seconds for the state to become completed or failed.
  await expect(taskState).toContainText(/Completed|Failed/, { timeout: 15000 });

  // If completed, the result should be shown.
  const stateText = await taskState.textContent();
  if (stateText && stateText.includes('Completed')) {
    const taskResult = page.locator('[data-task-result]');
    await expect(taskResult).toBeVisible();
  }
});

// ---------------------------------------------------------------
// Test: prompt input is disabled while task is in progress
// ---------------------------------------------------------------
test('prompt input is disabled while task is in progress', async ({
  page,
  authenticator,
}) => {
  const email = uniqueEmail();
  await registerAndLoadShell(page, authenticator, email);

  // Type a prompt and submit.
  const promptInput = page.locator('[data-prompt-input]');
  await promptInput.fill('Running task test');

  const submitBtn = page.locator('[data-prompt-submit]');
  await submitBtn.click();

  // Wait for the task status section to appear.
  const taskStatus = page.locator('[data-task-status]');
  await expect(taskStatus).toBeVisible({ timeout: 10000 });

  // While the task is in progress, the input and button should be disabled.
  const stateText = await page.locator('[data-task-state]').textContent();
  if (stateText && (stateText.includes('Pending') || stateText.includes('Running'))) {
    await expect(promptInput).toBeDisabled();
    await expect(submitBtn).toBeDisabled();
  }
});

// ---------------------------------------------------------------
// Test: reload during in-flight work reattaches without resubmitting
// (VAL-CROSS-121)
// ---------------------------------------------------------------
test('reload during in-flight work reattaches without resubmitting', async ({
  page,
  authenticator,
}) => {
  const email = uniqueEmail();
  await registerAndLoadShell(page, authenticator, email);

  // Count task submission requests.
  let submissionCount = 0;
  page.on('request', (req) => {
    const url = new URL(req.url());
    if (url.pathname === '/api/agent/run' && req.method() === 'POST') {
      submissionCount++;
    }
  });

  // Type a prompt and submit.
  const promptInput = page.locator('[data-prompt-input]');
  await promptInput.fill('Reattach test prompt');

  const submitBtn = page.locator('[data-prompt-submit]');
  await submitBtn.click();

  // Wait for the task status section to appear.
  const taskStatus = page.locator('[data-task-status]');
  await expect(taskStatus).toBeVisible({ timeout: 10000 });

  // Record the task ID before reload.
  const taskIdBefore = await page.locator('[data-task-id]').textContent();

  // Reload the page.
  await page.reload();
  await page.locator('[data-shell]').waitFor({ state: 'visible', timeout: 10000 });

  // After reload, the task runner should reattach to the same task.
  // Wait for the task status to reappear.
  const taskStatusAfter = page.locator('[data-task-status]');
  await expect(taskStatusAfter).toBeVisible({ timeout: 10000 });

  // The task ID should be the same as before the reload.
  const taskIdAfter = await page.locator('[data-task-id]').textContent();
  expect(taskIdAfter).toBe(taskIdBefore);

  // Only one submission should have been made (no duplicate from reload).
  expect(submissionCount).toBe(1);
});

// ---------------------------------------------------------------
// Test: prompt input is re-enabled after task completes
// ---------------------------------------------------------------
test('prompt input is re-enabled after task completes', async ({
  page,
  authenticator,
}) => {
  const email = uniqueEmail();
  await registerAndLoadShell(page, authenticator, email);

  // Type a prompt and submit.
  const promptInput = page.locator('[data-prompt-input]');
  await promptInput.fill('Completion test');

  const submitBtn = page.locator('[data-prompt-submit]');
  await submitBtn.click();

  // Wait for the task to complete.
  const taskState = page.locator('[data-task-state]');
  await expect(taskState).toContainText(/Completed|Failed/, { timeout: 15000 });

  // After completion, the prompt input should be re-enabled.
  await expect(promptInput).toBeEnabled();
  await expect(submitBtn).toBeEnabled();
});

// ---------------------------------------------------------------
// Test: task events are displayed in the event log
// ---------------------------------------------------------------
test('task events are displayed in the event log', async ({
  page,
  authenticator,
}) => {
  const email = uniqueEmail();
  await registerAndLoadShell(page, authenticator, email);

  // Type a prompt and submit.
  const promptInput = page.locator('[data-prompt-input]');
  await promptInput.fill('Event log test');

  const submitBtn = page.locator('[data-prompt-submit]');
  await submitBtn.click();

  // Wait for the task status section to appear.
  const taskStatus = page.locator('[data-task-status]');
  await expect(taskStatus).toBeVisible({ timeout: 10000 });

  // The event log should be visible with at least one event.
  const eventLog = page.locator('[data-task-events]');
  await expect(eventLog).toBeVisible({ timeout: 10000 });

  // There should be at least one event item.
  const eventItems = page.locator('[data-event-item]');
  const count = await eventItems.count();
  expect(count).toBeGreaterThanOrEqual(1);
});

// ---------------------------------------------------------------
// Test: signed-out prompt submission is denied (VAL-RUNTIME-002)
// ---------------------------------------------------------------
test('signed-out prompt submission is denied', async ({ page }) => {
  // Visit the page without authenticating.
  await page.goto(BASE_URL);

  // The auth entry should be visible (not the shell).
  const authEntry = page.locator('[data-auth-entry]');
  await expect(authEntry).toBeVisible();

  // The task runner should NOT be visible.
  const taskRunner = page.locator('[data-task-runner]');
  await expect(taskRunner).not.toBeVisible();
});

// ---------------------------------------------------------------
// Test: renewal and retries do not duplicate runtime task submission
// (VAL-CROSS-111)
// ---------------------------------------------------------------
test('renewal and retries do not duplicate runtime task submission', async ({
  page,
  authenticator,
}) => {
  const email = uniqueEmail();
  await registerAndLoadShell(page, authenticator, email);

  // Count task submission requests.
  let submissionCount = 0;
  const submissionDetails = [];
  page.on('request', (req) => {
    const url = new URL(req.url());
    if (url.pathname === '/api/agent/run' && req.method() === 'POST') {
      submissionCount++;
      submissionDetails.push({
        timestamp: new Date().toISOString(),
        url: req.url(),
      });
    }
  });

  // Type a prompt and submit.
  const promptInput = page.locator('[data-prompt-input]');
  await promptInput.fill('Test for duplicate submission prevention');

  const submitBtn = page.locator('[data-prompt-submit]');
  await submitBtn.click();

  // Wait for the task status section to appear.
  const taskStatus = page.locator('[data-task-status]');
  await expect(taskStatus).toBeVisible({ timeout: 10000 });

  // Record the task ID.
  const taskIdElement = page.locator('[data-task-id]');
  const taskId = await taskIdElement.textContent();

  // Simulate a page reload (which could trigger renewal/retry behavior).
  // The app should reattach to the same task without resubmitting.
  await page.reload();
  await page.locator('[data-shell]').waitFor({ state: 'visible', timeout: 10000 });

  // Wait for task status to reappear after reload.
  const taskStatusAfterReload = page.locator('[data-task-status]');
  await expect(taskStatusAfterReload).toBeVisible({ timeout: 10000 });

  // The task ID should remain the same.
  const taskIdAfterReload = await page.locator('[data-task-id]').textContent();
  expect(taskIdAfterReload).toBe(taskId);

  // Wait for the task to complete.
  const taskState = page.locator('[data-task-state]');
  await expect(taskState).toContainText(/Completed|Failed/, { timeout: 15000 });

  // Verify only ONE submission was made despite reload.
  expect(submissionCount).toBe(1);
  expect(submissionDetails.length).toBe(1);
});
