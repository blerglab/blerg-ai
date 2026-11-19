// Real settings and shared styles, synthetic API, no provider IO.
import assert from 'node:assert/strict';
import { mkdir, readFile } from 'node:fs/promises';
import { createServer } from 'vite';
import { chromium } from 'playwright';
const server = await createServer({ server: { host: '127.0.0.1', port: 0 } });
const id = '00000000-0000-4000-8000-000000000145';
const shots = process.env.SETTINGS_SHOTS || '/tmp/blg145-session-sign-in-shots';
let browser;
try {
  await mkdir(shots, { recursive: true });
  await server.listen();
  browser = await chromium.launch({ headless: true });
  for (const width of [390, 3840]) {
    const page = await browser.newPage({ viewport: { width, height: 1000 } });
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    await page.route('**/api/**', async route => {
      const path = new URL(route.request().url()).pathname;
      let body = {};
      if (path === '/api/mcp/connections') body = { connections: [], max: 20 };
      else if (path.endsWith('/preview')) body = { draft_id: id, runner_session_id: id, name: 'session-server', source: 'session_suggestion_saved_by_human', resource_host: 'resource.example.com', issuer_host: 'issuer.example.com', provider_host: 'provider.example.com', scopes: ['read'] };
      else if (path === '/api/credentials') body = [];
      else if (path === '/api/tokens') body = { tokens: [] };
      else if (path === '/api/plugins') body = { plugins: [] };
      else if (path === '/api/engines') body = { engines: [], plugin_engines: [] };
      else if (path === '/api/desktops') body = { desktops: [] };
      else if (path === '/api/me') body = { provider: 'local', provider_subject: 'example-user' };
      await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) });
    });
    await page.route('**/brand/tokens.css', async route => route.fulfill({ status: 200, contentType: 'text/css', body: await readFile('../internal/landing/static/tokens.css', 'utf8') }));
    const token = `x.${Buffer.from(JSON.stringify({ caps: ['card.read', 'account.manage'] })).toString('base64url')}.y`;
    await page.goto(`${server.resolvedUrls.local[0]}settings?section=integrations&mcp_runner_session=${id}&mcp_definition=${id}&scopes=write#access_token=${token}`);
    const group = page.getByRole('group', { name: 'Session MCP sign-in' });
    assert.equal(await page.getByLabel('Scopes for this session (optional)').inputValue(), '');
    await page.getByLabel('Scopes for this session (optional)').fill('read');
    await group.getByRole('button', { name: 'Review session sign-in' }).click();
    await group.getByRole('button', { name: 'Continue to provider.example.com' }).waitFor();
    assert.equal(await group.getByText('Suggested by this session', { exact: true }).count(), 1);
    await group.scrollIntoViewIfNeeded();
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `horizontal overflow at ${width}`);
    for (const button of await group.getByRole('button').all()) {
      const box = await button.boundingBox();
      assert.ok(box && box.x >= 0 && box.x + box.width <= width, `clipped button at ${width}`);
    }
    await page.screenshot({ path: `${shots}/preview-${width}.png`, fullPage: true });
    const cdp = await page.context().newCDPSession(page);
    await cdp.send('Emulation.setEmulatedVisionDeficiency', { type: 'deuteranopia' });
    await group.screenshot({ path: `${shots}/preview-deuteranopia-${width}.png` });
    assert.deepEqual(errors, []);
    await page.close();
  }
  console.log('PASS: settings preview at 390/3840px, no overflow, provider host, provenance, blank scopes; deuteranopia screenshots.');
} finally {
  await browser?.close();
  await server.close();
}
