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
    let install = 'desktop', enabled = true, admin = true;
    const github = [];
    page.on('request', r => { if (r.url().includes('api.github.com')) github.push(r.url()); });
    await page.route('**/api/**', async route => {
      const path = new URL(route.request().url()).pathname;
      let body = {};
      if (path === '/api/site') body = {release:'0.5.0',commit:'fixture',...(enabled ? {latest_version:'0.5.1',latest_url:'https://github.com/blerglab/blerg-ai/releases/tag/v0.5.1'} : {})};
      if (path === '/api/settings/update-notice') {
        if (!admin) return route.fulfill({status:403,body:'forbidden'});
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
    for (install of ['desktop','kubernetes','unknown']) {
      await page.goto('about:blank');
      await page.goto(`${server.resolvedUrls.local[0]}upgrade#access_token=${token}`);
      await page.getByText(`Install type: ${install}.`,{exact:true}).waitFor();
      const notice = page.getByRole('link',{name:'v0.5.1 available ↗'});
      await notice.waitFor();
      assert.equal(await notice.getAttribute('href'),'https://github.com/blerglab/blerg-ai/releases/tag/v0.5.1');
      const help = page.getByRole('link',{name:'How to upgrade',exact:true});
      assert.equal(await help.count(),1);
      for (const link of [notice,help]) {
        const box = await link.boundingBox();
        assert.ok(box && box.width > 0 && box.x >= 0 && box.x + box.width <= width,`core link clipped at ${width}`);
      }
      const buttons = page.getByRole('button',{name:/^Copy /});
      assert.equal(await buttons.count(),install === 'unknown' ? 0 : 1);
      for (const button of await buttons.all()) {
        const command = (await button.getAttribute('aria-label')).slice(5);
        await button.click();
        assert.equal(await page.evaluate(()=>navigator.clipboard.readText()),command);
        const box = await button.boundingBox();
        assert.ok(box.x >= 0 && box.x + box.width <= width);
      }
      assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth <= innerWidth),`core overflow ${width}`);
    }
    await page.goto(`${server.resolvedUrls.local[0]}settings#access_token=${token}`);
    const checkbox = page.getByRole('checkbox',{name:'Check for newer releases'});
    await checkbox.waitFor();
    assert.equal(await checkbox.isChecked(),true);
    await checkbox.uncheck();
    await page.getByRole('button',{name:'Save release notices'}).click();
    await page.getByText('Release notice settings saved.').waitFor();
    assert.equal(enabled,false);
    await page.goto(`${server.resolvedUrls.local[0]}upgrade#access_token=${token}`);
    await page.getByText('Install type: unknown.',{exact:true}).waitFor();
    assert.equal(await page.getByRole('link',{name:/available/}).count(),0);
    for (const fragment of ['', `#access_token=x.${Buffer.from(JSON.stringify({caps:['card.read']})).toString('base64url')}.y`]) {
      admin = false;
      await page.goto('about:blank');
      await page.goto(`${server.resolvedUrls.local[0]}upgrade${fragment}`);
      await page.getByText(/Ask an install administrator/).waitFor();
      assert.equal(await page.getByText(/Install type:/).count(),0);
      assert.equal(await page.getByRole('button',{name:/^Copy /}).count(),0);
    }
    assert.deepEqual(github,[]);
    await page.close();
    console.log(`PASS core upgrade panel and footer ${width}px: install types, copy, disabled notice, no GitHub requests`);
  }
} finally { await browser?.close(); await server.close(); }
