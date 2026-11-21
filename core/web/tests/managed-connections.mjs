// Real Settings layout with a fake core API; run from core/web.
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { createServer } from 'vite';
import { chromium } from 'playwright';

const server = await createServer({ server: { host: '127.0.0.1', port: 0 } });
let browser;
let passed = 0;
try {
  await server.listen();
  browser = await chromium.launch({ headless: true });
  const origin = server.resolvedUrls.local[0];
  for (const width of [390, 3840]) {
    const page = await browser.newPage({ viewport: { width, height: 1000 } });
    await page.route('**/api/**', async route => {
      const path = new URL(route.request().url()).pathname;
      let body = {};
      if (path === '/api/settings/child-limits') body = { parallel: 3, starts_per_hour: 120, defaults: { parallel: 3, starts_per_hour: 120 }, ranges: { parallel: [1,50], starts_per_hour: [1,10000] }, custom: false };
      else if (path === '/api/settings/board-limits') {
        const limits = { calls_per_minute: 600, calls_in_flight: 8, unrevoked_per_account: 20, live_exchange_tokens: 100 };
        body = { ...limits, defaults: limits, ranges: { calls_per_minute: [1,10000], calls_in_flight: [1,64], unrevoked_per_account: [1,1000], live_exchange_tokens: [10,10000] }, runner_refresh_seconds: 60 };
      }
      else if (path === '/api/site') body = { name: 'Blerg', engines: [{ id: 'codex', name: 'Codex', credential_import: true }] };
      else if (path === '/api/credentials') body = [{ engine: 'codex', kind: 'codex_oauth', state: 'usable', updated_at: '' }];
      else if (path === '/api/credentials/managed/capabilities') body = [{ engine: 'codex', kind: 'codex_oauth', login: true, verification_origin: 'https://auth.example.test' }];
      else if (path === '/api/tokens') body = { tokens: [] };
      else if (path === '/api/plugins') body = { plugins: [] };
      await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) });
    });
    await page.route('**/brand/tokens.css', async route => route.fulfill({ status: 200, contentType: 'text/css', body: await readFile('../internal/landing/static/tokens.css', 'utf8') }));
    const token = `x.${Buffer.from(JSON.stringify({ caps: ['account.manage', 'card.read'] })).toString('base64url')}.y`;
    await page.goto(`${origin}settings?section=integrations#access_token=${token}`);
    const card = page.getByLabel('Codex managed connection', { exact: true });
    await card.getByRole('heading', { name: 'Codex', exact: true }).waitFor();
    assert.equal(await card.locator('.card-head .chip.up').innerText(), 'CONNECTED');
    assert.match(await card.innerText(), /desktop workspaces use the Codex login on your workstation/);
    assert.match(await card.innerText(), /pasted credential below/);
    assert.match(await card.innerText(), /First turn on device code sign-in for Codex in ChatGPT → Settings → Security\./);
    const button = card.getByRole('button', { name: 'Reconnect Codex' });
    await button.scrollIntoViewIfNeeded();
    const box = await card.boundingBox();
    assert.ok(box.x >= 0 && box.x + box.width <= width);
    assert.ok(await button.isVisible());
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
    passed++;
    console.log(`PASS managed Codex card at ${width} px`);
    await page.close();
  }
  console.log(`Tests: ${passed} passed, 0 failed`);
} finally { await browser?.close(); await server.close(); }
