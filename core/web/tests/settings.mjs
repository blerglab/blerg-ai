// Run from core/web: node tests/settings.mjs. Real Settings UI, fake core API.
import assert from 'node:assert/strict';
import { readFile, mkdir } from 'node:fs/promises';
import { createServer } from 'vite';
import { chromium } from 'playwright';

const server = await createServer({ server: { host: '127.0.0.1', port: 0 } });
let browser;
try {
  await mkdir(process.env.SETTINGS_SHOTS || '/tmp/settings-shots', { recursive: true });
  await server.listen();
  browser = await chromium.launch({ headless: true });
  const origin = server.resolvedUrls.local[0];
  for (const width of [390, 3840]) {
    const page = await browser.newPage({ viewport: { width, height: 1000 } });
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    const defaults = { parallel: 3, starts_per_hour: 120 };
    let limits = { ...defaults };
    const ranges = { parallel: [1, 50], starts_per_hour: [1, 10000] };
    const puts = [];
    await page.route('**/api/**', async route => {
      const path = new URL(route.request().url()).pathname;
      let body = {};
      if (path === '/api/settings/child-limits') {
        if (route.request().method() === 'PUT') {
          limits = route.request().postDataJSON(); puts.push(limits);
        }
        body = { ...limits, defaults, ranges, custom: puts.length > 0 };
      } else if (path === '/api/settings/board-limits') {
        const board = { calls_per_minute: 600, calls_in_flight: 8, unrevoked_per_account: 20, live_exchange_tokens: 100 };
        body = { ...board, defaults: board, ranges: { calls_per_minute: [1,10000], calls_in_flight: [1,64], unrevoked_per_account: [1,1000], live_exchange_tokens: [10,10000] }, runner_refresh_seconds: 60 };
      } else if (path === '/api/settings/shell-policy') body = { version: 1, revision: 1, enabled: true };
      else if (path === '/api/settings/update-notice') body = { enabled: true };
      else if (path === '/api/site') body = { name: 'Blerg', engines: [] };
      else if (path === '/api/credentials') body = { configured: [] };
      else if (path === '/api/tokens') body = { tokens: [] };
      else if (path === '/api/plugins') body = { plugins: [] };
      await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) });
    });
    await page.route('**/brand/tokens.css', async route => route.fulfill({ status: 200, contentType: 'text/css', body: await readFile('../internal/landing/static/tokens.css', 'utf8') }));
    const token = caps => `x.${Buffer.from(JSON.stringify({ caps })).toString('base64url')}.y`;
    await page.goto(`${origin}settings#access_token=${token(['account.manage'])}`);
    const section = page.getByTestId('child-limits');
    const parallel = page.getByLabel('Child sessions at once, per parent');
    const hourly = page.getByLabel('Child starts per rolling hour, per parent');
    await parallel.waitFor();
    await section.scrollIntoViewIfNeeded();
    assert.equal(await parallel.inputValue(), '3');
    assert.equal(await hourly.inputValue(), '120');
    assert.equal(await parallel.getAttribute('max'), '50');
    assert.equal(await hourly.getAttribute('max'), '10000');
    await parallel.fill('51');
    assert.equal(await parallel.evaluate(el => el.checkValidity()), false);
    await parallel.fill('8');
    await hourly.fill('240');
    await page.getByRole('button', { name: 'Save child limits', exact: true }).click();
    await section.getByRole('status').waitFor();
    assert.deepEqual(puts, [{ parallel: 8, starts_per_hour: 240 }]);
    for (const field of [parallel, hourly]) {
      const bounds = await field.boundingBox();
      assert.ok(bounds && bounds.x >= 0 && bounds.x + bounds.width <= width, `field clipped at ${width}px`);
    }
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `horizontal overflow at ${width}px`);
    const shell = page.getByRole('switch', { name: 'Allow owner shells' });
    const releases = page.getByRole('checkbox', { name: 'Check for newer releases' });
    await shell.waitFor(); await releases.waitFor();
    const headingStyles = await page.getByRole('heading', { level: 2 }).evaluateAll(nodes => nodes.map(el => {
      const css = getComputedStyle(el);
      return [css.fontFamily, css.fontSize, css.fontWeight, css.textTransform, css.letterSpacing];
    }));
    assert.ok(headingStyles.length >= 5);
    for (const style of headingStyles) assert.deepEqual(style, headingStyles[0], 'settings section headings must use the same shared style');
    for (const toggle of [shell, releases]) {
      assert.equal(await toggle.evaluate(el => getComputedStyle(el).appearance), 'none');
      assert.ok((await toggle.boundingBox()).width < 60, 'toggle must not inherit full-width input styling');
    }
    for (const heading of ['Owner shells', 'Release notices']) {
      const section = page.getByRole('heading', { name: heading }).locator('..');
      await section.scrollIntoViewIfNeeded();
      await section.screenshot({ path: `${process.env.SETTINGS_SHOTS || '/tmp/settings-shots'}/core-${heading.toLowerCase().replaceAll(' ', '-')}-${width}.png` });
    }
    await page.screenshot({ path: `${process.env.SETTINGS_SHOTS || '/tmp/settings-shots'}/core-settings-${width}.png`, fullPage: true });
    await page.goto('about:blank');
    await page.goto(`${origin}settings#access_token=${token(['card.read'])}`);
    await page.getByRole('heading', { name: 'Credentials', exact: true }).waitFor();
    assert.equal(await page.getByTestId('child-limits').count(), 0);
    assert.deepEqual(errors, []);
    await page.close();
    console.log(`PASS Settings child limits: ${width}px, save, range validation, admin-only visibility`);
  }
} finally {
  await browser?.close();
  await server.close();
}
