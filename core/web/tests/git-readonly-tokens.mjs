// Real Settings layout with a fake core API; run from core/web.
// node tests/git-readonly-tokens.mjs
import assert from 'node:assert/strict';
import { readFile, writeFile, mkdir } from 'node:fs/promises';
import { createServer } from 'vite';
import { chromium } from 'playwright';

const server = await createServer({ server: { host: '127.0.0.1', port: 0 } });
let browser;
let passed = 0;
try {
  await server.listen();
  browser = await chromium.launch({ headless: true });
  const origin = server.resolvedUrls.local[0];
  await mkdir('/tmp/blg460-screens', { recursive: true });
  const token = `x.${Buffer.from(JSON.stringify({ caps: ['account.manage', 'card.read'] })).toString('base64url')}.y`;
  const tokensCss = await readFile('../internal/landing/static/tokens.css', 'utf8');

  async function openSettings(width, stored) {
    const page = await browser.newPage({ viewport: { width, height: 1200 } });
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    await page.route('**/api/**', async route => {
      const req = route.request();
      const path = new URL(req.url()).pathname;
      let body = {};
      if (path === '/api/credentials' && req.method() === 'GET') body = stored;
      else if (path === '/api/credentials' && req.method() === 'POST') {
        const posted = req.postDataJSON();
        // Mirrors the real core refusal (BLG-460 security re-review finding 2): a classic
        // GitHub token (the "ghp_" prefix alone) is refused rather than stored, so the
        // screenshot of this state matches what the real backend actually does.
        if (posted.engine.startsWith('git-readonly:github:') && posted.credential.startsWith('ghp_')) {
          await route.fulfill({
            status: 400,
            contentType: 'text/plain',
            body: "Classic GitHub tokens can't be limited to read. Use a fine-grained token with Contents: read-only.",
          });
          return;
        }
        stored = stored.filter(c => c.engine !== posted.engine).concat([{ engine: posted.engine, updated_at: new Date().toISOString() }]);
        body = {};
      } else if (path.startsWith('/api/credentials/') && req.method() === 'DELETE') {
        const engine = decodeURIComponent(path.slice('/api/credentials/'.length));
        stored = stored.filter(c => c.engine !== engine);
        body = {};
      } else if (path === '/api/site') body = { name: 'Blerg', engines: [{ id: 'claude', name: 'Claude', credential_import: true }] };
      else if (path === '/api/tokens') body = { tokens: [] };
      else if (path === '/api/plugins') body = { plugins: [] };
      await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) });
    });
    await page.route('**/brand/tokens.css', async route => route.fulfill({ status: 200, contentType: 'text/css', body: tokensCss }));
    await page.goto(`${origin}settings?section=integrations#access_token=${token}`);
    const heading = page.getByRole('heading', { name: 'Read-only tokens for workers' });
    await heading.waitFor();
    const section = heading.locator('xpath=ancestor::section[1]');
    return { page, section, errors, getStored: () => stored };
  }

  async function shoot(page, section, width, name) {
    await section.scrollIntoViewIfNeeded();
    const box = await section.boundingBox();
    assert.ok(box.x >= 0 && box.x + box.width <= width, `section overflowed at ${width}px: ${JSON.stringify(box)}`);
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), true, `horizontal overflow at ${width}px`);
    const shot = `/tmp/blg460-screens/git-readonly-tokens-${name}-${width}.png`;
    await page.screenshot({ path: shot, fullPage: true });
    const meta = await page.evaluate(() => ({ w: window.innerWidth, scrollW: document.documentElement.scrollWidth }));
    await writeFile(`${shot}.meta.json`, JSON.stringify({ width, ...meta }));
    console.log(`PASS git-readonly tokens (${name}) at ${width} px -> ${shot}`);
  }

  for (const width of [390, 3840]) {
    // Empty state: no owners added yet.
    {
      const { page, section, errors } = await openSettings(width, []);
      await section.getByText('No read-only tokens yet.').waitFor();
      assert.deepEqual(errors, [], `console errors (empty, ${width}px): ${errors.join(', ')}`);
      await shoot(page, section, width, 'empty');
      await page.close();
      passed++;
    }

    // Two owners added: the ordinary populated state.
    {
      const { page, section, errors } = await openSettings(width, [
        { engine: 'git-readonly:github:acme-corp', updated_at: '2026-01-01T00:00:00Z', last_used_at: '2026-02-03T12:00:00Z' },
        { engine: 'git-readonly:gitlab:personal-account', updated_at: '2026-01-05T00:00:00Z' },
      ]);
      await section.getByText('acme-corp').waitFor();
      assert.match(await section.innerText(), /acme-corp/);
      assert.match(await section.innerText(), /GitHub/);
      assert.match(await section.innerText(), /Feb 3, 2026/);
      assert.match(await section.innerText(), /personal-account/);
      assert.match(await section.innerText(), /GitLab/);
      assert.match(await section.innerText(), /Never used/);
      assert.deepEqual(errors, [], `console errors (two-owners, ${width}px): ${errors.join(', ')}`);
      await shoot(page, section, width, 'two-owners');

      // Add a token for a third owner, then remove it — the real add/remove round trip
      // against the real component, not a stub. Exercised after the screenshot so the
      // published image shows a clean, settled state. A fine-grained-shaped value, since a
      // "ghp_" classic value is now refused (see the POST mock above).
      await section.getByLabel('Owner').fill('new-org');
      await section.getByLabel('Read-only token').fill('github_pat_fixture_not_real');
      await section.getByRole('button', { name: 'Add token' }).click();
      await section.getByText('new-org').waitFor();
      assert.doesNotMatch(await section.innerText(), /github_pat_fixture_not_real/);
      const newOrgRow = section.getByText('new-org').locator('xpath=ancestor::tr[1]');
      await newOrgRow.getByRole('button', { name: 'Remove' }).click();
      await page.waitForFunction(() => !document.body.innerText.includes('new-org'));
      assert.deepEqual(errors, [], `console errors after add/remove (${width}px): ${errors.join(', ')}`);

      await page.close();
      passed++;
    }

    // A refused classic GitHub token: the person sees why, and the row is never added.
    {
      const { page, section, errors } = await openSettings(width, [
        { engine: 'git-readonly:github:acme-corp', updated_at: '2026-01-01T00:00:00Z', last_used_at: '2026-02-03T12:00:00Z' },
        { engine: 'git-readonly:gitlab:personal-account', updated_at: '2026-01-05T00:00:00Z' },
      ]);
      await section.getByText('acme-corp').waitFor();
      await section.getByLabel('Owner').fill('blocked-org');
      await section.getByLabel('Read-only token').fill('ghp_classictoken1234567890');
      await section.getByRole('button', { name: 'Add token' }).click();
      const alert = section.getByRole('alert');
      await alert.waitFor();
      assert.match(await alert.innerText(), /Classic GitHub tokens can't be limited to read/);
      // The per-save error must say it once — not repeat the section's own description hint.
      assert.doesNotMatch(await alert.innerText(), /fine-grained token's permissions/);
      assert.equal(await section.locator('table tbody tr').count(), 2, 'a refused token must never add a table row');
      assert.deepEqual(errors, [], `console errors (classic-refused, ${width}px): ${errors.join(', ')}`);
      await shoot(page, section, width, 'classic-refused');
      await page.close();
      passed++;
    }
  }
  console.log(`Tests: ${passed} passed, 0 failed`);
} finally { await browser?.close(); await server.close(); }
