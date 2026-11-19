// Full Core app entry point, deterministic HTTP boundary, normal home/sign-in routes.
// BLG-473: AUDIT_SHOTS=<dir> also screenshots every theme (not just dark), for a human to look at.
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { createServer } from 'vite';
import { chromium } from 'playwright';
const THEMES = process.env.AUDIT_SHOTS
  ? ['dark', 'light', 'colorblind-dark', 'colorblind-light', 'high-contrast',
    'catppuccin-mocha', 'catppuccin-latte', 'dracula', 'nord', 'tokyo-night']
  : ['dark'];
const server = await createServer({ server: { host: '127.0.0.1', port: 0 } });
let browser;
let cases = 0;
try {
  await server.listen();
  browser = await chromium.launch({ headless: true });
  for (const width of [390, 3840]) {
    for (const theme of THEMES) {
    const page = await browser.newPage({ viewport: { width, height: 1000 }, colorScheme: 'dark' });
    await page.addInitScript(theme => localStorage.setItem('blerg-theme', theme), theme);
    await page.route('**/brand/**', async route => {
      const name = new URL(route.request().url()).pathname.split('/').at(-1);
      const body = await readFile(new URL(`../../../brand/${name}`, import.meta.url));
      await route.fulfill({ body, contentType: name.endsWith('.css') ? 'text/css' : 'image/svg+xml' });
    });
    await page.route('**/api/**', route => {
      const path = new URL(route.request().url()).pathname;
      const data = path === '/api/me' ? { provider_subject: 'alex', role: 'admin' }
        : path === '/api/site' ? { board_url: 'https://board.example.com', runner_url: 'https://runner.example.com' }
        : path === '/api/me/theme' ? { theme } : { release: '0.5.0', commit: 'fixture' };
      return route.fulfill({ json: data });
    });
    await page.route('**/healthz', route => route.fulfill({ body: 'ok' }));
    await page.route('**/auth/provider', route => route.fulfill({ json: { id: 'local' } }));
    await page.goto(`${server.resolvedUrls.local[0]}app#access_token=fixture`);
    await page.getByRole('heading', { name: 'Board', exact: true }).waitFor();
    await page.getByText('Checking', { exact: true }).waitFor({ state: 'hidden' });
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
    if (process.env.AUDIT_SHOTS) await page.screenshot({ path: `${process.env.AUDIT_SHOTS}/core-home-${width}-${theme}.png` });
    await page.goto(`${server.resolvedUrls.local[0]}login`);
    await page.getByLabel('Username').waitFor();
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
    if (process.env.AUDIT_SHOTS) await page.screenshot({ path: `${process.env.AUDIT_SHOTS}/core-sign-in-${width}-${theme}.png` });
    await page.close();
    cases++;
    }
  }
  console.log(`PASS: Core home and sign-in at 390 and 3840 px (${cases * 2} views)`);
} finally { await browser?.close(); await server.close(); }
