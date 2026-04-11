// Playwright config for go-choir auth passkey validation.
// Chromium-only — WebAuthn virtual authenticator requires CDP (Chrome DevTools Protocol).
// Target: http://localhost:4173 (Vite dev server proxying to local auth service on :8081).
import { defineConfig, devices } from '@playwright/test';

export default defineConfig({
  testDir: './tests',
  fullyParallel: false, // serial — shared mutable auth DB, virtual-authenticator state
  retries: 0,
  timeout: 30_000,
  expect: { timeout: 10_000 },
  reporter: 'list',
  use: {
    baseURL: 'http://localhost:4173',
    // Chromium is required for CDP virtual authenticator (WebAuthn.enable).
    ...devices['Desktop Chrome'],
  },
  projects: [
    {
      name: 'chromium',
      use: {
        ...devices['Desktop Chrome'],
      },
    },
  ],
  // Do NOT start the webServer here — the harness assumes the local service
  // stack (auth + frontend dev) is already running per .factory/services.yaml.
  // Validators start services deterministically via the manifest before
  // invoking playwright.
});
