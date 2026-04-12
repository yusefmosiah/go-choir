import { test, expect, chromium } from '@playwright/test';
import fs from 'fs';

const BASE_URL = process.env.BASE_URL || 'https://draft.choir-ip.com';

async function registerUser(email) {
  const browser = await chromium.launch();
  const context = await browser.newContext();
  const page = await context.newPage();

  const cdpSession = await context.newCDPSession(page);
  await cdpSession.send('WebAuthn.enable');
  await cdpSession.send('WebAuthn.addVirtualAuthenticator', {
    options: {
      protocol: 'ctap2',
      transport: 'internal',
      hasResidentKey: true,
      hasUserVerification: true,
      isUserVerified: true,
    },
  });

  await page.goto(BASE_URL);
  await page.waitForSelector('text=Register with Passkey');

  await page.locator('input[type="email"]').fill(email);
  await page.locator('button[type="submit"]').click();
  await page.waitForSelector('[data-shell]', { timeout: 15000 });

  const cookies = await context.cookies();
  fs.writeFileSync(`/tmp/cookies_${email}.json`, JSON.stringify(cookies, null, 2));
  console.log(`Cookies saved for ${email}`);

  await browser.close();
  return cookies;
}

test('register vmuser1', async () => {
  await registerUser('vmuser1@example.com');
});

test('register vmuser2', async () => {
  await registerUser('vmuser2@example.com');
});

test('register vmuser3', async () => {
  await registerUser('vmuser3@example.com');
});
