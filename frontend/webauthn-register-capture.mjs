import { chromium } from '@playwright/test';
import fs from 'fs';

(async () => {
  const browser = await chromium.launch({ headless: true });
  const context = await browser.newContext();
  const page = await context.newPage();

  // Navigate to the deployed origin
  await page.goto('https://draft.choir-ip.com/');
  await page.waitForLoadState('networkidle');

  // Add virtual authenticator via CDP
  const cdpSession = await context.newCDPSession(page);
  await cdpSession.send('WebAuthn.enable');
  const authenticator = await cdpSession.send('WebAuthn.addVirtualAuthenticator', {
    options: {
      protocol: 'ctap2',
      transport: 'internal',
      hasResidentKey: true,
      hasUserVerification: true,
      isUserVerified: true,
    }
  });

  console.log('Virtual authenticator created:', authenticator.authenticatorId);

  const username = `concurrent-test-${Date.now()}-${Math.random().toString(36).substring(2, 8)}`;

  try {
    await page.fill('input[type="text"]', username, { timeout: 5000 });
    console.log('Filled username:', username);
  } catch (e) {
    console.error('Could not find username input:', e);
    await browser.close();
    process.exit(1);
  }

  await page.click('button:has-text("Register with Passkey")');
  await page.waitForTimeout(5000);

  // Get cookies
  const cookies = await context.cookies();

  // Write cookies and username to file
  const captureData = {
    username,
    cookies,
    url: page.url(),
    title: await page.title()
  };

  fs.writeFileSync('/Users/wiz/go-choir/test-results/captured-auth.json', JSON.stringify(captureData, null, 2));
  console.log('Captured auth data written to test-results/captured-auth.json');

  // Also save storage state
  await context.storageState({ path: '/Users/wiz/go-choir/test-results/playwright-state.json' });

  await browser.close();
})();
