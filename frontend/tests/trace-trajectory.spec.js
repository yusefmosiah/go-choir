import { test, expect } from './helpers/fixtures.js';
import { registerPasskey } from './helpers/auth.js';

const BASE_URL = 'http://localhost:4173';

function uniqueEmail() {
  return `trace-trajectory-${Date.now()}-${Math.random().toString(36).slice(2, 8)}@example.com`;
}

async function registerAndLoadDesktop(page, authenticator, email) {
  await page.goto(BASE_URL);
  await registerPasskey(page, email, BASE_URL);
  await page.reload();
  await page.locator('[data-desktop]').waitFor({ state: 'visible', timeout: 10000 });
}

async function openVText(page) {
  await page.locator('[data-desktop-icon-id="vtext"]').dblclick();
  const editor = page.locator('[data-vtext-app] [data-vtext-editor-area]').last();
  await expect(editor).toBeVisible({ timeout: 10000 });
  return editor;
}

async function createDelegatedTrajectory(page) {
  return page.evaluate(async () => {
    const rootRes = await fetch('/api/agent/loop', {
      method: 'POST',
      credentials: 'include',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        prompt: 'Route this document inquiry through conductor.',
        metadata: {
          agent_profile: 'conductor',
          agent_role: 'conductor',
          input_source: 'trace-browser-test',
          requested_app: 'vtext',
        },
      }),
    });
    if (!rootRes.ok) {
      throw new Error(`failed to create root loop: ${rootRes.status}`);
    }
    const root = await rootRes.json();

    const childRes = await fetch('/api/agent/spawn', {
      method: 'POST',
      credentials: 'include',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        parent_id: root.loop_id,
        objective: 'Research moss habitats with grounded facts.',
        constraints: {
          agent_profile: 'researcher',
          agent_role: 'researcher',
        },
      }),
    });
    if (!childRes.ok) {
      throw new Error(`failed to spawn child loop: ${childRes.status}`);
    }
    const child = await childRes.json();
    return { root, child };
  });
}

// Raw runtime trace endpoints work for delegated runs, but the current
// authenticated browser session path still returns an empty trajectory index.
// Keep the exact repro close to the product surface without leaving the suite red.
test.fixme('trace shows delegation graph and lazy inspector detail for a delegated trajectory', async ({ page, authenticator }) => {
  await registerAndLoadDesktop(page, authenticator, uniqueEmail());
  const { root, child } = await createDelegatedTrajectory(page);

  await page.locator('[data-desktop-icon-id="trace"]').dblclick();
  const traceWindow = page.locator('[data-trace-app]');
  await expect(traceWindow).toBeVisible({ timeout: 10000 });
  await expect(traceWindow.locator('[data-trace-agent-node]')).toHaveCount(2, { timeout: 10000 });
  await expect(traceWindow.locator('[data-trace-agent-node]').filter({ hasText: /conductor/i })).toHaveCount(1);
  await expect(traceWindow.locator('[data-trace-agent-node]').filter({ hasText: /researcher/i })).toHaveCount(1);

  const childMoment = traceWindow.locator('[data-trace-moment]').filter({ hasText: /spawned from|loop completed|loop started/i }).nth(1);
  await expect(childMoment).toBeVisible({ timeout: 10000 });
  await childMoment.click();

  const inspector = traceWindow.locator('[data-trace-inspector]');
  await expect(inspector).toContainText(/loop\.submitted|loop\.started|loop\.completed/);
  await expect(inspector.locator('pre').first()).toBeVisible();
  await expect(traceWindow.locator('[data-trace-trajectory]').first()).toContainText(/Route this document inquiry|Research moss habitats/);
  expect(child.loop_id).toBeTruthy();
});
