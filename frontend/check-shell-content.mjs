import { chromium } from '@playwright/test';

(async () => {
  const browser = await chromium.launch({ 
    headless: true,
    ignoreHTTPSErrors: true
  });
  
  const context = await browser.newContext({
    storageState: '/Users/wiz/.factory/missions/969491ec-3df3-47c7-b9bf-8e384615819d/evidence/gateway-vm/gateway-e2e-round5/storage-state.json'
  });
  
  const page = await context.newPage();
  
  // Check shell content
  await page.goto('https://draft.choir-ip.com/shell');
  await page.waitForTimeout(5000);
  
  const shellHtml = await page.content();
  console.log('Shell page HTML:', shellHtml);
  console.log('Shell URL:', page.url());
  
  // Check network/console for errors
  const logs = [];
  page.on('console', msg => logs.push(`CONSOLE: ${msg.type()}: ${msg.text()}`));
  page.on('pageerror', error => logs.push(`PAGE_ERROR: ${error.message}`));
  page.on('response', response => {
    if (response.status() >= 400) {
      logs.push(`HTTP_ERROR: ${response.status()} ${response.url()}`);
    }
  });
  
  // Wait to collect any errors
  await page.waitForTimeout(3000);
  
  console.log('\nLogs:', JSON.stringify(logs, null, 2));
  
  // Check /api/shell/bootstrap
  console.log('\nChecking bootstrap API...');
  const bootstrapResponse = await page.evaluate(async () => {
    try {
      const res = await fetch('/api/shell/bootstrap');
      return { status: res.status, text: await res.text() };
    } catch (e) {
      return { error: e.message };
    }
  });
  console.log('Bootstrap response:', JSON.stringify(bootstrapResponse));
  
  // Check health
  console.log('\nChecking health API...');
  const healthResponse = await page.evaluate(async () => {
    try {
      const res = await fetch('/api/health');
      return { status: res.status, text: await res.text() };
    } catch (e) {
      return { error: e.message };
    }
  });
  console.log('Health response:', JSON.stringify(healthResponse));
  
  await browser.close();
})();
