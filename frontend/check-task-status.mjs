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
  
  const taskId = 'ee914e04-5add-423f-8a81-baaddf3d6b17';
  
  // Check task status multiple times
  console.log('Checking task status over time...\n');
  for (let i = 0; i < 6; i++) {
    const statusResponse = await page.evaluate(async (url) => {
      try {
        const res = await fetch(url);
        return { status: res.status, text: await res.text() };
      } catch (e) {
        return { error: e.message };
      }
    }, `https://draft.choir-ip.com/api/agent/status?task_id=${taskId}`);
    
    console.log(`Status check ${i + 1} (${(i + 1) * 5}s):`, JSON.stringify(statusResponse));
    
    if (i < 5) {
      await page.waitForTimeout(5000);
    }
  }
  
  // Try to check events
  console.log('\n\nChecking events...');
  const eventsResponse = await page.evaluate(async (url) => {
    try {
      const res = await fetch(url);
      return { status: res.status, text: await res.text() };
    } catch (e) {
      return { error: e.message };
    }
  }, `https://draft.choir-ip.com/api/events?task_id=${taskId}`);
  console.log('Events response:', JSON.stringify(eventsResponse));
  
  await browser.close();
})();
