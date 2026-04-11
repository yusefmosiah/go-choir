import { chromium } from '@playwright/test';

(async () => {
  const browser = await chromium.launch({ headless: true });
  const context = await browser.newContext();
  const page = await context.newPage();
  
  // Navigate to the deployed origin
  await page.goto('https://draft.choir-ip.com/');
  await page.waitForLoadState('networkidle');
  
  // Take initial screenshot
  await page.screenshot({ path: '/Users/wiz/go-choir/.factory/validation/gateway-vm/user-testing/evidence/gateway-vm/gateway-e2e-new/05-playwright-initial.png', fullPage: true });
  
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
  
  // Try different selectors for the username input
  const username = `gateway-e2e-${Date.now()}-${Math.random().toString(36).substring(2, 8)}`;
  
  // Try finding the input by placeholder or type
  try {
    await page.fill('input[type="text"]', username, { timeout: 5000 });
    console.log('Filled input[type="text"]');
  } catch (e) {
    try {
      await page.fill('input[placeholder*="username" i]', username, { timeout: 5000 });
      console.log('Filled input by placeholder');
    } catch (e2) {
      try {
        await page.fill('input', username, { timeout: 5000 });
        console.log('Filled first input');
      } catch (e3) {
        // Get page content to debug
        const html = await page.content();
        console.log('Page HTML (first 2000 chars):', html.substring(0, 2000));
        throw new Error('Could not find username input');
      }
    }
  }
  
  // Click register button and wait for navigation or success
  await page.click('button:has-text("Register with Passkey")');
  
  // Wait for either redirect to shell or success state
  try {
    await page.waitForURL('**/shell**', { timeout: 10000 });
    console.log('Redirected to shell');
  } catch (e) {
    console.log('No shell redirect, checking for other success indicators');
  }
  
  await page.waitForTimeout(3000);
  
  // Get cookies to transfer to agent-browser
  const cookies = await context.cookies();
  const sessionCookie = cookies.find(c => c.name === 'session' || c.name.includes('session'));
  
  console.log('USERNAME:', username);
  console.log('COOKIES_JSON:', JSON.stringify(cookies));
  console.log('SESSION_COOKIE:', JSON.stringify(sessionCookie));
  console.log('CURRENT_URL:', page.url());
  console.log('PAGE_TITLE:', await page.title());
  
  // Save state for transfer
  await context.storageState({ path: './test-results/playwright-state.json' });
  
  // Take screenshot
  await page.screenshot({ path: '/Users/wiz/go-choir/.factory/validation/gateway-vm/user-testing/evidence/gateway-vm/gateway-e2e-new/05-playwright-registered.png', fullPage: true });
  
  await browser.close();
})();
