import { chromium } from '@playwright/test';
import { execSync } from 'child_process';

(async () => {
  const browser = await chromium.launch({ headless: true });
  const context = await browser.newContext();
  const page = await context.newPage();
  
  // Navigate and set cookies
  await page.goto('https://draft.choir-ip.com/');
  
  // Set the authentication cookies
  await context.addCookies([
    {
      name: 'choir_refresh',
      value: 'd6d33153-5a0b-4dcf-828a-0db7c138bb75',
      domain: 'draft.choir-ip.com',
      path: '/auth',
      expires: 1778541526,
      httpOnly: true,
      secure: true,
      sameSite: 'Lax'
    },
    {
      name: 'choir_access',
      value: 'eyJhbGciOiJFZERTQSIsInR5cCI6IkpXVCJ9.eyJleHAiOjE3NzU5NDk4MjYsImlhdCI6MTc3NTk0OTUyNiwic2NvcGUiOiJhY2Nlc3MiLCJzdWIiOiI2OWYyNmVmMC1mNmIyLTRmMWMtOTdkYS0wYjliYWFkZDIzYzcifQ.oH5ty2AA-4MN3GfKVaAqNnGwB7yETp3yVG-ZQ2gA7L18go62OXwmvE8U9FQym1nwxJZyrf1cGtLD00JWb2N7Cg',
      domain: 'draft.choir-ip.com',
      path: '/',
      expires: 1775949826,
      httpOnly: true,
      secure: true,
      sameSite: 'Lax'
    }
  ]);
  
  // Reload with cookies
  await page.reload();
  await page.waitForTimeout(3000);
  
  // Check if we're authenticated and have shell access
  console.log('URL after reload:', page.url());
  console.log('Title:', await page.title());
  
  // Save state for transfer
  await context.storageState({ path: '/tmp/auth-state.json' });
  
  // Take screenshot
  await page.screenshot({ path: '/Users/wiz/go-choir/.factory/validation/gateway-vm/user-testing/evidence/gateway-vm/gateway-e2e-new/06-shell-initial.png', fullPage: true });
  
  // Check if shell loaded
  const hasPrompt = await page.locator('[placeholder="Ask anything"]').isVisible().catch(() => false);
  console.log('HAS_PROMPT_INPUT:', hasPrompt);
  
  // Try to find task runner elements
  const pageContent = await page.content();
  console.log('PAGE_HAS_SHELL:', pageContent.includes('shell') || pageContent.includes('prompt') || pageContent.includes('Ask'));
  
  await browser.close();
  
  // Print the state file for reference
  const fs = await import('fs');
  const state = JSON.parse(fs.readFileSync('/tmp/auth-state.json', 'utf8'));
  console.log('COOKIES_EXPORTED:', JSON.stringify(state.cookies, null, 2));
})();
