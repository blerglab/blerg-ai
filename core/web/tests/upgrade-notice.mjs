import assert from 'node:assert/strict';
import { createServer } from 'vite';
import { readFile } from 'node:fs/promises';
import { chromium } from 'playwright';
const server = await createServer({ server: { host: '127.0.0.1', port: 0 } });
let browser;
try {
  await server.listen();
  browser = await chromium.launch({ headless: true });
  for (const width of [390,3840]) {
    const page = await browser.newPage({viewport:{width,height:1000},permissions:['clipboard-read','clipboard-write']});
    await page.route('**/brand/tokens.css', async route => route.fulfill({status:200,contentType:'text/css',body:await readFile('../internal/landing/static/tokens.css','utf8')}));
    const install = 'desktop';
    let enabled = true;
    const github = [];
    page.on('request', r => { if (r.url().includes('api.github.com')) github.push(r.url()); });
    await page.route('**/api/**', async route => {
      const path = new URL(route.request().url()).pathname;
      let body = {};
      if (path === '/api/site') body = {release:'0.5.0',commit:'fixture',...(enabled ? {latest_version:'0.5.1',latest_url:'https://github.com/blerglab/blerg-ai/releases/tag/v0.5.1'} : {})};
      if (path === '/api/settings/update-notice') {
        if (route.request().method() === 'PUT') enabled = route.request().postDataJSON().enabled;
        body = {enabled,install_type:install};
      }
      if (path === '/api/settings/board-limits') {
        const values = {calls_per_minute:600,calls_in_flight:8,unrevoked_per_account:20,live_exchange_tokens:100};
        body = {...values,defaults:values,ranges:{calls_per_minute:[1,10000],calls_in_flight:[1,64],unrevoked_per_account:[1,1000],live_exchange_tokens:[10,10000]},runner_refresh_seconds:60};
      }
      if (path === '/api/settings/child-limits') body = {parallel:3,starts_per_hour:120,defaults:{parallel:3,starts_per_hour:120},ranges:{parallel:[1,50],starts_per_hour:[1,10000]}};
      if (path === '/api/credentials') body = {configured:[]};
      if (path === '/api/tokens') body = {tokens:[]};
      if (path === '/api/plugins') body = {plugins:[]};
      await route.fulfill({status:200,contentType:'application/json',body:JSON.stringify(body)});
    });
    const token = `x.${Buffer.from(JSON.stringify({caps:['account.manage']})).toString('base64url')}.y`;
    await page.goto(`${server.resolvedUrls.local[0]}settings#access_token=${token}`);
    const notice = page.getByRole('link',{name:'v0.5.1 available ↗'});
    await notice.waitFor();
    assert.equal(await notice.getAttribute('href'),'https://github.com/blerglab/blerg-ai/releases/tag/v0.5.1');
    assert.equal(await page.getByRole('link',{name:'How to upgrade',exact:true}).count(),0);
    const box = await notice.boundingBox();
    assert.ok(box && box.width > 0 && box.x >= 0 && box.x + box.width <= width);
    assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth <= innerWidth));
    await page.goto(`${server.resolvedUrls.local[0]}settings#access_token=${token}`);
    const checkbox = page.getByRole('checkbox',{name:'Check for newer releases'});
    await checkbox.waitFor();
    assert.equal(await checkbox.isChecked(),true);
    await checkbox.uncheck();
    await page.getByRole('button',{name:'Save release notices'}).click();
    await page.getByText('Release notice settings saved.').waitFor();
    assert.equal(enabled,false);
    await page.reload();
    await page.getByRole('checkbox',{name:'Check for newer releases'}).waitFor();
    assert.equal(await page.getByRole('link',{name:/available/}).count(),0);
    assert.deepEqual(github,[]);
    await page.close();
    console.log(`PASS core release notice ${width}px: release link, no upgrade link, disabled notice, no GitHub requests`);
  }
} finally { await browser?.close(); await server.close(); }
