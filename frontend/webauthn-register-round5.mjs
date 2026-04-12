import { chromium } from '@playwright/test';

(async () => {
  const browser = await chromium.launch({ 
    headless: true,
    ignoreHTTPSErrors: true
  });
  const context = await browser.newContext();
  const page = await context.newPage();
  
  // Navigate to the deployed origin
  await page.goto('https://draft.choir-ip.com/');
  await page.waitForLoadState('networkidle');
  
  // Take initial screenshot
  await page.screenshot({ 
    path: '/Users/wiz/.factory/missions/969491ec-3df3-47c7-b9bf-8e384615819d/evidence/gateway-vm/gateway-e2e-round5/02-playwright-initial.png', 
    fullPage: true 
  });
  console.log('Initial screenshot saved');
  
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
  
  // Generate unique username
  const username = `gateway-e2e-${Date.now()}`;
  
  // Fill username input
  try {
    await page.fill('input[type="text"]', username, { timeout: 5000 });
    console.log('Filled username:', username);
  } catch (e) {
    console.log('Failed to fill username:', e.message);
    const html = await page.content();
    console.log('Page HTML (first 1000 chars):', html.substring(0, 1000));
    throw e;
  }
  
  // Click register button
  await page.click('button:has-text("Register with Passkey")');
  console.log('Clicked Register with Passkey');
  
  // Wait for authentication to complete (longer wait for VM boot + runtime)
  try {
    await page.waitForURL('**/shell**', { timeout: 60000 });
    console.log('Redirected to shell');
  } catch (e) {
    console.log('No shell redirect within 60s, checking current state...');
  }
  
  // Wait additional time for any loading
  await page.waitForTimeout(5000);
  
  // Get cookies and state
  const cookies = await context.cookies();
  const sessionCookie = cookies.find(c => c.name === 'session');
  
  console.log('USERNAME:', username);
  console.log('COOKIES_JSON:', JSON.stringify(cookies));
  console.log('SESSION_COOKIE:', JSON.stringify(sessionCookie));
  console.log('CURRENT_URL:', page.url());
  console.log('PAGE_TITLE:', await page.title());
  
  // Save storage state
  await context.storageState({ 
    path: '/Users/wiz/.factory/missions/969491ec-3df3-47c7-b9bf-8e384615819d/evidence/gateway-vm/gateway-e2e-round5/storage-state.json' 
  });
  
  // Take final screenshot
  await page.screenshot({ 
    path: '/Users/wiz/.factory/missions/969491ec-3df3-47c7-b9bf-8e384615819d/evidence/gateway-vm/gateway-e2e-round5/03-post-registration.png', 
    fullPage: true 
  });
  console.log('Final screenshot saved');
  
  // Check for console errors
  const logs = [];
  page.on('console', msg => {
    logs.push(`${msg.type()}: ${msg.text()}`);
  });
  page.on('pageerror', error => {
    logs.push(`PAGE_ERROR: ${error.message}`);
  });
  
  // Wait a bit to collect any console messages
  await page.waitForTimeout(2000);
  console.log('CONSOLE_LOGS:', JSON.stringify(logs.slice(-20))); // Last 20 logs
  
  await browser.close();
})();
