import assert from 'node:assert/strict';
import { mkdir, readFile } from 'node:fs/promises';
import { createServer } from 'vite';
import { chromium } from 'playwright';
const server = await createServer({ server: { host: '127.0.0.1', port: 0 } });
let browser;
try {
 await server.listen(); browser = await chromium.launch({ headless: true });
 await mkdir('.cache/blg514-shots', { recursive: true });
 for (const [name,width,pane] of [['phone',390,false],['pane',3840,true],['wide',3840,false],['colorblind',390,false]]) {
  const page = await browser.newPage({ viewport: { width, height: 1100 } });
  const errors=[];page.on('pageerror',e=>errors.push(e.message));
  let opted=false, mode='off', fail=false;const writes=[];
  await page.route('**/api/**', async route => {
   const req=route.request(), path=new URL(req.url()).pathname;let body={};
   if(path.startsWith('/api/settings/live-data')) {
    if(req.method()==='PUT') {
     if(fail) {await route.fulfill({status:503,body:'Unavailable'});return;}
     const write=req.postDataJSON();writes.push(write);
     if(path.endsWith('/policy')) mode=write.mode;else opted=write.unsafe_host_opt_in;
    }
    body={unsafe_host_opt_in:opted,polling_enabled:false,bindings:[{binding_id:'binding',revision:1,digest:'digest',connection_name:'Market',tool:'quotes',hash:'a'.repeat(64),tool_definition:{name:'quotes',description:'Market quotes',inputSchema:{type:'object',additionalProperties:false,properties:{symbol:{type:'string'}}}},mode,status:'pending'}]};
   } else if(path==='/api/site') body={name:'Blerg',engines:[]};
   else if(path==='/api/credentials') body={configured:[]};
   else if(path==='/api/tokens') body={tokens:[]};
   else if(path==='/api/plugins') body={plugins:[]};
   else if(path==='/api/mcp/connections') body={connections:[]};
   await route.fulfill({status:200,contentType:'application/json',body:JSON.stringify(body)});
  });
  await page.route('**/brand/tokens.css',async route=>route.fulfill({status:200,contentType:'text/css',body:await readFile('../internal/landing/static/tokens.css','utf8')}));
  const token=`x.${Buffer.from(JSON.stringify({caps:['card.read','account.manage']})).toString('base64url')}.y`;
  await page.goto(`${server.resolvedUrls.local[0]}settings?section=integrations#access_token=${token}`);
  const section=page.getByTestId('live-data-settings');await section.getByRole('checkbox').waitFor();
  if(pane) await page.addStyleTag({content:'body { width: 1024px; max-width: 1024px; }'});
  if(name==='colorblind') await page.evaluate(()=>document.documentElement.setAttribute('data-theme','colorblind-dark'));
  await section.scrollIntoViewIfNeeded();
  const checkbox=section.getByRole('checkbox',{name:'Allow polling on this machine'});
  assert.equal(await checkbox.isChecked(),false);
  await checkbox.click();await section.getByRole('status').waitFor();
  assert.equal(await checkbox.isChecked(),true);
  assert.deepEqual(writes[0],{unsafe_host_opt_in:true,acknowledgement:'Agents on this machine can reach the polling credentials.'});
  await section.getByLabel('Market · quotes').selectOption('allow');
  await page.waitForFunction(()=>!document.querySelector('[data-testid="live-data-settings"] select')?.disabled);
  assert.deepEqual(writes[1],{binding_id:'binding',revision:1,digest:'digest',mode:'allow'});
  await section.getByText('Tool definition',{exact:true}).click();
  assert.equal(await section.locator('pre').isVisible(),true);
  await page.screenshot({path:`.cache/blg514-shots/${name}.png`});
  assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'horizontal overflow');
  fail=true;await checkbox.click();await section.getByRole('alert').waitFor();
  assert.equal(await checkbox.isChecked(),true,'failed save must not claim opt-out');assert.deepEqual(errors,[]);
  await page.close();console.log(`PASS live data Settings: ${name}, consent, policy, failed save`);
 }
} finally {await browser?.close();await server.close();}
