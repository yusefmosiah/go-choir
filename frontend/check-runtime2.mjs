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
  await page.goto('https://draft.choir-ip.com/');
  
  // Check all relevant APIs with full URLs
  const endpoints = [
    'https://draft.choir-ip.com/api/health',
    'https://draft.choir-ip.com/api/agent/health',
    'https://draft.choir-ip.com/api/sandbox/health',
    'https://draft.choir-ip.com/api/shell/bootstrap',
    'https://draft.choir-ip.com/api/vm/status',
  ];
  
  for (const endpoint of endpoints) {
    console.log(`\nChecking ${endpoint}...`);
    const response = await page.evaluate(async (url) => {
      try {
        const res = await fetch(url);
        return { status: res.status, text: await res.text().catch(() => 'BINARY') };
      } catch (e) {
        return { error: e.message };
      }
    }, endpoint);
    console.log('Response:', JSON.stringify(response));
  }
  
  // Try to submit a task
  console.log('\n\nTrying to submit a task...');
  const taskResponse = await page.evaluate(async () => {
    try {
      const res = await fetch('https://draft.choir-ip.com/api/agent/loop', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ prompt: 'Say hello world' })
      });
      return { status: res.status, text: await res.text() };
    } catch (e) {
      return { error: e.message };
    }
  });
  console.log('Task submission response:', JSON.stringify(taskResponse));
  
  await browser.close();
})();
