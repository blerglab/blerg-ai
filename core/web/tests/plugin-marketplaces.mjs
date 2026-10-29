// Run from core/web: node tests/plugin-marketplaces.mjs. Real Settings UI, fake core API.
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { createServer } from 'vite';
import { chromium } from 'playwright';

const server = await createServer({ configLoader: "runner", server: { host: '127.0.0.1', port: 0 } });
let browser;
try {
  await server.listen();
  browser = await chromium.launch({ headless: true });
  const origin = server.resolvedUrls.local[0];
  for (const width of [390, 3840]) {
    const page = await browser.newPage({ viewport: { width, height: 1000 } });
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    let policy = { version: 1, revision: 1, marketplaces: ["anthropics/claude-plugins-official", "codex:openai-curated", "codex:openai-curated-remote"].map(source => ({ source, enabled: true })), any_marketplace: false };
    const puts = [];
    await page.route('**/api/**', async route => {
      const path = new URL(route.request().url()).pathname;
      let body = {};
      if (path === '/api/settings/plugin-marketplaces') {
        if (route.request().method() === 'PUT') { policy = { ...route.request().postDataJSON(), revision: policy.revision + 1 }; puts.push(policy); }
        body = policy;
      } else if (path.startsWith('/api/plugins/')) body = { plugins: [{ marketplace: "codex:disabled", plugin: "retained-plugin" }], allowed_marketplaces: policy.marketplaces.filter(m => m.enabled).map(m => m.source), any_marketplace: false, max: 20 };
      else if (path === '/api/engines') body = { engines: [{ id: "codex", name: "Codex", plugins: true }], plugin_engines: ["codex"] };
      else if (path === '/api/settings/board-limits') {
        const b = { calls_per_minute: 600, calls_in_flight: 8, unrevoked_per_account: 20, live_exchange_tokens: 100 };
        body = { ...b, defaults: b, ranges: { calls_per_minute: [1,10000], calls_in_flight: [1,64], unrevoked_per_account: [1,1000], live_exchange_tokens: [10,10000] }, runner_refresh_seconds: 60 };
      } else if (path === '/api/settings/child-limits') body = { parallel: 3, starts_per_hour: 120, defaults: { parallel: 3, starts_per_hour: 120 }, ranges: { parallel: [1,50], starts_per_hour: [1,10000] } };
      else if (path === '/api/site') body = { name: 'Blerg', engines: [] };
      else if (path === '/api/credentials') body = { configured: [] };
      else if (path === '/api/tokens') body = { tokens: [] };
      else if (path === '/api/settings/shell-policy') body = { version: 1, revision: 1, enabled: true };
      await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) });
    });
    await page.route('**/brand/tokens.css', async route => route.fulfill({ status: 200, contentType: 'text/css', body: await readFile('../internal/landing/static/tokens.css', 'utf8') }));
    const token = caps => `x.${Buffer.from(JSON.stringify({ caps })).toString('base64url')}.y`;
    await page.goto(`${origin}settings#access_token=${token(['account.manage'])}`);
    const section = page.getByTestId('plugin-marketplaces');
    await section.waitFor();
    await section.scrollIntoViewIfNeeded();
    const official = section.getByRole('switch', { name: 'Allow codex:openai-curated-remote' });
    await official.waitFor();
    assert.equal(await official.getAttribute('aria-checked'), 'true');
    assert.equal(await section.getByRole('button', { name: /Remove codex:/ }).count(), 0);
    await official.click();
    await section.getByRole('status').waitFor();
    assert.equal(policy.marketplaces.find(m => m.source === 'codex:openai-curated-remote').enabled, false);
    const input = section.getByLabel(/Add marketplace/);
    await input.fill('../escape');
    assert.equal(await section.getByRole('button', { name: 'Add marketplace', exact: true }).isDisabled(), true);
    await input.fill('example-org/team-plugins');
    await section.getByRole('button', { name: 'Add marketplace', exact: true }).click();
    const remove = section.getByRole('button', { name: 'Remove example-org/team-plugins' });
    await remove.waitFor();
    await remove.click();
    await remove.waitFor({ state: 'detached' });
    assert.equal(puts.length, 3);
    assert.equal(policy.marketplaces.length, 3);
    await page.getByText(/Marketplace no longer allowed/).waitFor();
    const bounds = await input.boundingBox();
    assert.ok(bounds && bounds.x >= 0 && bounds.x + bounds.width <= width, `field clipped at ${width}px`);
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `horizontal overflow at ${width}px`);
    await section.screenshot({ path: `../../.cache/marketplace-settings-${width}.png` });
    await page.goto('about:blank');
    await page.goto(`${origin}settings#access_token=${token(['card.read'])}`);
    await page.getByRole('heading', { name: 'Credentials', exact: true }).waitFor();
    assert.equal(await page.getByTestId('plugin-marketplaces').count(), 0);
    assert.deepEqual(errors, []);
    await page.close();
    console.log(`PASS Settings plugin marketplaces: ${width}px, toggle, add/remove, validation, retained-plugin warning, admin-only visibility`);
  }
} finally {
  await browser?.close();
  await server.close();
}
