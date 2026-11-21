// Focused real-browser check of Core's shared picker and authenticated adapter.
import assert from 'node:assert/strict';
import { createServer } from 'vite';
import { chromium } from 'playwright';
const server = await createServer({ server: { host: '127.0.0.1', port: 0 } });
let browser;
let cases = 0;
try {
  await server.listen();
  browser = await chromium.launch({ headless: true });
  for (const width of [390, 3840]) {
    const page = await browser.newPage({ viewport: { width, height: 1000 } });
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    let theme = 'dark';
    await page.route('**/api/me/theme', async route => {
      assert.equal(route.request().headers().authorization, 'Bearer fixture');
      if (route.request().method() === 'PUT') theme = route.request().postDataJSON().theme;
      await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ theme }) });
    });
    await page.goto(`${server.resolvedUrls.local[0]}tests/themes.html#access_token=fixture`);
    const select = page.getByRole('combobox', { name: 'Theme' });
    await select.waitFor();
    for (const id of ['dark', 'light', 'colorblind-dark', 'colorblind-light', 'high-contrast',
      'catppuccin-mocha', 'catppuccin-latte', 'dracula', 'nord', 'tokyo-night']) {
      await select.selectOption(id);
      await page.waitForFunction(id => document.documentElement.dataset.theme === id, id);
      await page.waitForFunction(() => !document.querySelector('select').disabled);
      assert.equal(theme, id);
      const bounds = await select.boundingBox();
      assert.ok(bounds && bounds.x >= 0 && bounds.x + bounds.width <= width, `theme picker clipped at ${width}px`);
      assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `overflow at ${width}px`);
      cases++;
    }
    await select.selectOption('system');
    await page.waitForFunction(() => !document.documentElement.hasAttribute('data-theme'));
    await page.emulateMedia({ colorScheme: 'dark' });
    await page.waitForFunction(() => getComputedStyle(document.documentElement).getPropertyValue('--basalt').trim() === '#0d1117');
    await page.emulateMedia({ colorScheme: 'light' });
    await page.waitForFunction(() => getComputedStyle(document.documentElement).getPropertyValue('--basalt').trim() === '#eef1f6');
    assert.equal(theme, 'system');
    assert.equal(await page.evaluate(() => localStorage.getItem('blerg-theme')), 'system');
    cases++;
    await select.focus();
    assert.notEqual(await select.evaluate(el => getComputedStyle(el).outlineStyle), 'none', 'picker focus must remain visible');
    await select.press('ArrowDown');
    assert.equal(theme, 'system', 'closed-select arrows preview without saving');
    await select.press('Escape');
    assert.equal(await select.inputValue(), 'system');
    await select.press('End');
    await select.press('Enter');
    await page.waitForFunction(() => document.documentElement.dataset.theme === 'tokyo-night');
    assert.equal(theme, 'tokyo-night');
    cases++;
    assert.deepEqual(errors, []);
    await page.close();
  }
  console.log(`PASS: ${cases} Core theme/viewport cases (390 and 3840 px)`);
} finally {
  await browser?.close();
  await server.close();
}
