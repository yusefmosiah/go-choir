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
  
  // Capture all network requests
  const networkRequests = [];
  page.on('request', request => {
    const url = request.url();
    networkRequests.push({
      url: url,
      method: request.method(),
      host: new URL(url).hostname,
      resourceType: request.resourceType()
    });
  });
  
  // Navigate and submit a new task
  await page.goto('https://draft.choir-ip.com/');
  await page.waitForTimeout(3000);
  
  console.log('Submitting new task for network analysis...');
  const taskResponse = await page.evaluate(async () => {
    try {
      const res = await fetch('https://draft.choir-ip.com/api/agent/task', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ prompt: 'What is 2+2?' })
      });
      return { status: res.status, text: await res.text() };
    } catch (e) {
      return { error: e.message };
    }
  });
  console.log('Task submission:', JSON.stringify(taskResponse));
  
  // Wait for completion
  await page.waitForTimeout(5000);
  
  // Check status
  const taskId = JSON.parse(taskResponse.text).task_id;
  const statusResponse = await page.evaluate(async (url) => {
    try {
      const res = await fetch(url);
      return { status: res.status, text: await res.text() };
    } catch (e) {
      return { error: e.message };
    }
  }, `https://draft.choir-ip.com/api/agent/status?task_id=${taskId}`);
  console.log('Task status:', JSON.stringify(statusResponse));
  
  // Analyze network traffic
  console.log('\n\n=== NETWORK TRAFFIC ANALYSIS ===');
  
  const draftChoirIpRequests = networkRequests.filter(r => r.host === 'draft.choir-ip.com');
  const otherHosts = networkRequests.filter(r => r.host !== 'draft.choir-ip.com');
  
  console.log(`\nTotal requests: ${networkRequests.length}`);
  console.log(`Requests to draft.choir-ip.com: ${draftChoirIpRequests.length}`);
  console.log(`Requests to other hosts: ${otherHosts.length}`);
  
  if (otherHosts.length > 0) {
    console.log('\n⚠️ Requests to external hosts:');
    otherHosts.forEach(r => console.log(`  - ${r.method} ${r.url} (${r.resourceType})`));
  } else {
    console.log('\n✓ All traffic stayed on draft.choir-ip.com');
  }
  
  // Check for provider-specific domains
  const providerPatterns = ['anthropic', 'openai', 'bedrock', 'amazonaws', 'z.ai', 'api.ai', 'llm'];
  const providerRequests = networkRequests.filter(r => 
    providerPatterns.some(p => r.host.toLowerCase().includes(p))
  );
  
  if (providerRequests.length > 0) {
    console.log('\n⚠️ Potential provider API calls detected:');
    providerRequests.forEach(r => console.log(`  - ${r.url}`));
  } else {
    console.log('\n✓ No direct provider API calls from browser');
  }
  
  // Show all unique hosts
  const uniqueHosts = [...new Set(networkRequests.map(r => r.host))];
  console.log('\nAll hosts contacted:', uniqueHosts);
  
  await browser.close();
})();
