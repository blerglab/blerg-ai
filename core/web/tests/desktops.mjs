// Run from core/web: node tests/desktops.mjs. Real Settings UI, fake core API: Settings →
// Account → Your desktops (#193) at 390 and 3840 px, for a person and for an administrator.
import assert from 'node:assert/strict';
import { readFile, mkdir } from 'node:fs/promises';
import { createServer } from 'vite';
import { chromium } from 'playwright';

const shots = process.env.SETTINGS_SHOTS || '/tmp/settings-shots';
const code = 'bec_' + '0123456789'.repeat(4);
const person = { id: 'me' };
const desktops = admin => [
  { id: 'd1', name: 'Work laptop', host_name: 'laptop-4f2a', owner_id: person.id, ownership_revision: 2, state: 'enrolled', connected: true, last_seen_at: null, mine: true, can_enroll: false, can_rename: true, can_revoke: true },
  { id: 'd2', name: 'studio-mac', host_name: 'studio-mac', owner_id: person.id, ownership_revision: 1, state: 'offline', connected: false, last_seen_at: '2026-10-25T18:30:00Z', mine: true, can_enroll: false, can_rename: true, can_revoke: true },
  { id: 'd3', name: 'old-workstation', host_name: 'old-workstation', owner_id: '', ownership_revision: 4, state: 'needs_enrolling', connected: true, last_seen_at: null, mine: false, can_enroll: true, can_rename: false, can_revoke: false },
  ...(admin ? [{ id: 'd4', name: 'build-box', host_name: 'build-box', owner_id: 'other', ownership_revision: 3, state: 'offline', connected: false, last_seen_at: '2026-10-20T09:00:00Z', mine: false, can_enroll: false, can_rename: false, can_revoke: true }] : []),
];

const server = await createServer({ server: { host: '127.0.0.1', port: 0 } });
let browser;
try {
  await mkdir(shots, { recursive: true });
  await server.listen();
  browser = await chromium.launch({ headless: true });
  const origin = server.resolvedUrls.local[0];
  for (const admin of [false, true]) {
    for (const width of [390, 3840]) {
      const page = await browser.newPage({ viewport: { width, height: 1100 } });
      const errors = [];
      const calls = [];
      page.on('pageerror', error => errors.push(error.message));
      await page.route('**/api/**', async route => {
        const url = new URL(route.request().url());
        const method = route.request().method();
        calls.push(`${method} ${url.pathname}`);
        let body = {};
        if (url.pathname === '/api/desktops') body = { desktops: desktops(admin), owners: admin ? [{ id: 'me', name: 'me' }, { id: 'other', name: 'sam' }] : [], actor: 'me', can_manage_others: admin };
        else if (url.pathname === '/api/desktop-enrollment-codes' && method === 'POST') body = { id: 'c1', code, expires_at: new Date(Date.now() + 15 * 60_000).toISOString() };
        else if (url.pathname === '/api/desktop-enrollment-codes') body = { codes: [] };
        else if (url.pathname === '/api/site') body = { version: 'v', board_url: '', runner_url: 'https://runner.example.test' };
        else if (url.pathname === '/api/credentials') body = [];
        else if (url.pathname === '/api/tokens') body = { tokens: [] };
        else if (url.pathname === '/api/plugins') body = { plugins: [] };
        else if (url.pathname === '/api/me') body = { provider: 'local', provider_subject: 'me' };
        // The administrator-only sections around it, as tests/settings.mjs answers them.
        else if (url.pathname === '/api/settings/child-limits') body = { parallel: 3, starts_per_hour: 120, defaults: { parallel: 3, starts_per_hour: 120 }, ranges: { parallel: [1, 50], starts_per_hour: [1, 10000] }, custom: false };
        else if (url.pathname === '/api/settings/board-limits') {
          const board = { calls_per_minute: 600, calls_in_flight: 8, unrevoked_per_account: 20, live_exchange_tokens: 100 };
          body = { ...board, defaults: board, ranges: { calls_per_minute: [1, 10000], calls_in_flight: [1, 64], unrevoked_per_account: [1, 1000], live_exchange_tokens: [10, 10000] }, runner_refresh_seconds: 60 };
        } else if (url.pathname === '/api/settings/shell-policy') body = { version: 1, revision: 1, enabled: true };
        else if (url.pathname === '/api/settings/update-notice') body = { enabled: true };
        await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) });
      });
      await page.route('**/brand/tokens.css', async route => route.fulfill({ status: 200, contentType: 'text/css', body: await readFile('../internal/landing/static/tokens.css', 'utf8') }));
      const token = caps => `x.${Buffer.from(JSON.stringify({ caps })).toString('base64url')}.y`;
      await page.goto(`${origin}settings#access_token=${token(admin ? ['card.read', 'account.manage'] : ['card.read'])}`);
      const section = page.locator('#desktops');
      await page.getByTestId('desktop-d1').waitFor();
      assert.equal(await page.locator('h2#desktops-heading').textContent(), 'Your desktops');
      for (const [id, label] of [['d1', 'Enrolled'], ['d2', 'Offline'], ['d3', 'Needs enrolling']]) {
        assert.equal(await page.getByTestId(`desktop-${id}`).getByText(label, { exact: true }).count(), 1, `${id} state`);
      }
      assert.equal(await page.getByTestId('desktop-d4').count(), admin ? 1 : 0, 'everyone’s desktops are for administrators only');
      // Every button is a styled settings action, inside the viewport.
      for (const button of await section.getByRole('button').all()) {
        const css = await button.evaluate(el => { const s = getComputedStyle(el); return { radius: s.borderTopLeftRadius, border: s.borderTopStyle }; });
        assert.equal(css.radius, '8px'); assert.equal(css.border, 'solid');
        const box = await button.boundingBox();
        assert.ok(box && box.x >= 0 && box.x + box.width <= width, `button clipped at ${width}px`);
      }
      await page.getByRole('button', { name: 'Add a desktop' }).click();
      const command = page.getByTestId('enroll-command');
      await command.waitFor();
      assert.equal(await command.textContent(), `BLERG_RUNNER_SERVER_URL=wss://runner.example.test/ws/daemon install/desktop/daemon/install.sh install --enroll ${code}`);
      const box = await command.boundingBox();
      assert.ok(box && box.x + box.width <= width, `command clipped at ${width}px`);
      await page.getByTestId('desktop-d1').getByRole('button', { name: 'Remove' }).click();
      await page.getByRole('button', { name: 'Remove desktop' }).waitFor();
      assert.ok(!calls.some(c => c.startsWith('DELETE /api/desktops/')), 'removed without confirming');
      assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `horizontal overflow at ${width}px`);
      await section.scrollIntoViewIfNeeded();
      await section.screenshot({ path: `${shots}/core-your-desktops-${admin ? 'admin' : 'person'}-${width}.png` });
      assert.deepEqual(errors, []);
      await page.close();
      console.log(`PASS Your desktops: ${admin ? 'administrator' : 'person'} at ${width}px`);
    }
  }
} finally {
  await browser?.close();
  await server.close();
}
