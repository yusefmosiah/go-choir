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
  
  // Screenshot 1: Auth page with cookies
  await page.goto('https://draft.choir-ip.com/');
  await page.waitForTimeout(3000);
  await page.screenshot({ 
    path: '/Users/wiz/.factory/missions/969491ec-3df3-47c7-b9bf-8e384615819d/evidence/gateway-vm/gateway-e2e-round5/06-auth-page-with-cookies.png', 
    fullPage: true 
  });
  console.log('Auth page screenshot saved');
  
  // Screenshot 2: Shell page
  await page.goto('https://draft.choir-ip.com/shell');
  await page.waitForTimeout(5000);
  await page.screenshot({ 
    path: '/Users/wiz/.factory/missions/969491ec-3df3-47c7-b9bf-8e384615819d/evidence/gateway-vm/gateway-e2e-round5/07-shell-page.png', 
    fullPage: true 
  });
  console.log('Shell page screenshot saved');
  
  // Screenshot 3: Submit task and show result
  await page.goto('https://draft.choir-ip.com/');
  await page.waitForTimeout(2000);
  
  // Submit task via console to show the flow
  const result = await page.evaluate(async () => {
    const res = await fetch('https://draft.choir-ip.com/api/agent/loop', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ prompt: 'Explain quantum computing in one sentence' })
    });
    const data = await res.json();
    
    // Wait for completion
    let status;
    for (let i = 0; i < 10; i++) {
      const s = await fetch(`https://draft.choir-ip.com/api/agent/status?task_id=${data.task_id}`);
      status = await s.json();
      if (status.state === 'completed') break;
      await new Promise(r => setTimeout(r, 1000));
    }
    
    return { task: data, status };
  });
  
  console.log('Final task result:', JSON.stringify(result, null, 2));
  
  // Save result to file
  const fs = await import('fs');
  fs.writeFileSync(
    '/Users/wiz/.factory/missions/969491ec-3df3-47c7-b9bf-8e384615819d/evidence/gateway-vm/gateway-e2e-round5/task-result.json',
    JSON.stringify(result, null, 2)
  );
  
  await page.screenshot({ 
    path: '/Users/wiz/.factory/missions/969491ec-3df3-47c7-b9bf-8e384615819d/evidence/gateway-vm/gateway-e2e-round5/08-final-state.png', 
    fullPage: true 
  });
  console.log('Final state screenshot saved');
  
  await browser.close();
})();
