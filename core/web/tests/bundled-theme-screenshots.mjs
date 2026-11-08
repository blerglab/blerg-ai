// BLG-474: real-app screenshots of three of the six bundled open-source schemes on the real
// Settings → Account page (same harness as managed-connections.mjs), at 390 and 3840 px. Only
// takes screenshots: pass BLERG_THEME_SHOTS_DIR to write PNGs there; otherwise this is a no-op
// smoke check that the page still renders in each theme.
import { readFile } from 'node:fs/promises';
import { createServer } from 'vite';
import { chromium } from 'playwright';

const shotDir = process.env.BLERG_THEME_SHOTS_DIR;
const THEMES = ['catppuccin-mocha', 'catppuccin-latte', 'nord', 'tokyo-night'];

const server = await createServer({ server: { host: '127.0.0.1', port: 0 } });
let browser;
try {
  await server.listen();
  browser = await chromium.launch({ headless: true });
  const origin = server.resolvedUrls.local[0];
  for (const theme of THEMES) {
    for (const width of [390, 3840]) {
      const page = await browser.newPage({ viewport: { width, height: width === 390 ? 844 : 2160 } });
      await page.route('**/api/**', async route => {
        const path = new URL(route.request().url()).pathname;
        let body = {};
        if (path === '/api/me') body = { kind: 'human', is_admin: true, account_id: 'theme-preview', username: 'theme-preview', provider: 'local', provider_subject: 'theme-preview', caps: ['account.manage'] };
        else if (path === '/api/me/theme') body = { theme };
        else if (path === '/api/site') body = { name: 'Blerg', engines: [{ id: 'codex', name: 'Codex', credential_import: true }] };
        await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) });
      });
      await page.route('**/brand/tokens.css', async route => route.fulfill({ status: 200, contentType: 'text/css', body: await readFile('../internal/landing/static/tokens.css', 'utf8') }));
      await page.addInitScript(id => { localStorage.setItem('blerg-theme', id); }, theme);
      const token = `x.${Buffer.from(JSON.stringify({ caps: ['account.manage', 'card.read'] })).toString('base64url')}.y`;
      await page.goto(`${origin}settings?section=account#access_token=${token}`);
      await page.waitForFunction(id => document.documentElement.dataset.theme === id, theme);
      await page.getByRole('heading', { name: 'Account' }).waitFor();
      if (shotDir) await page.screenshot({ path: `${shotDir}/core-${theme}-${width}.png` });
      await page.close();
      console.log(`PASS core Settings account page in ${theme} at ${width}px`);
    }
  }
} finally {
  await browser?.close();
  await server.close();
}
