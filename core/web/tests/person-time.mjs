// Real Account Settings rendering with a fake API; screenshots contain only synthetic data.
import assert from 'node:assert/strict';
import { readFile, mkdir } from 'node:fs/promises';
import { createServer } from 'vite';
import { chromium } from 'playwright';
const shots=process.env.SETTINGS_SHOTS || '/tmp/blg-515-shots';
await mkdir(shots,{recursive:true});
const server=await createServer({server:{host:'127.0.0.1',port:0}});
let browser;
try{
 await server.listen();browser=await chromium.launch({headless:true});
 for(const width of [390,1920]){
  const page=await browser.newPage({viewport:{width,height:1000},timezoneId:'America/New_York'});
  let clock={timezone:''};const writes=[];
  await page.route('**/api/**',async route=>{
   const path=new URL(route.request().url()).pathname;let body={};
   if(path==='/api/me/time'){
    if(route.request().method()==='PUT'){const value=route.request().postDataJSON();writes.push(value);clock={timezone:value.timezone,...(value.quiet_hours?{quiet_hours:value.quiet_hours}:{})};}
    body=clock;
   }else if(path==='/api/me')body={provider:'github',provider_subject:'example-user'};
   else if(path==='/api/site')body={name:'Blerg',engines:[]};
   else if(['/api/credentials','/api/mcp-connections'].includes(path))body=[];
   else if(path==='/api/tokens')body={tokens:[]};
   else if(path==='/api/plugins')body={plugins:[]};
   else if(path==='/api/engines')body={engines:[],plugin_engines:[]};
   else if(path==='/api/desktops')body={desktops:[]};
   await route.fulfill({status:200,contentType:'application/json',body:JSON.stringify(body)});
  });
  await page.route('**/brand/tokens.css',async route=>route.fulfill({contentType:'text/css',body:await readFile('../internal/landing/static/tokens.css','utf8')}));
  const token=`x.${Buffer.from(JSON.stringify({caps:['card.read']})).toString('base64url')}.y`;
  await page.goto(`${server.resolvedUrls.local[0]}settings?section=account#access_token=${token}`);
  const field=page.getByLabel('Time zone',{exact:true});
  await field.waitFor();
  assert.equal(await field.getAttribute('list'),'person-timezones');
  assert.ok(await field.evaluate(el=>el.list.options.length>100));
  assert.equal(await field.evaluate(el=>Array.from(el.list.options).some(o=>o.value==='EST')),false);
  await page.waitForFunction(()=>document.querySelector('#person-timezone')?.value==='America/New_York');
  assert.deepEqual(writes,[{timezone:'America/New_York',initialize:true}]);
  await field.fill('Europe/Paris');await page.getByRole('button',{name:'Save time settings',exact:true}).click();
  await page.getByRole('status').filter({hasText:'Time settings saved.'}).waitFor();
  await field.fill('America/New_York');await page.getByRole('button',{name:'Save time settings',exact:true}).click();
  await page.getByRole('status').filter({hasText:'Time settings saved.'}).waitFor();
  await page.getByRole('checkbox',{name:'Quiet hours',exact:true}).check();
  await page.getByLabel('From',{exact:true}).fill('22:00');await page.getByLabel('Until',{exact:true}).fill('07:00');
  await page.getByRole('button',{name:'Save time settings',exact:true}).click();
  await page.getByRole('status').filter({hasText:'Time settings saved.'}).waitFor();
  assert.deepEqual(clock.quiet_hours,{enabled:true,start:'22:00',end:'07:00'});
  const box=await field.boundingBox();assert.ok(box.x>=0 && box.x+box.width<=width);
  assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'horizontal overflow');
  await page.screenshot({path:`${shots}/settings-quiet-hours-${width}.png`,fullPage:true});
  if(width===1920){
   await field.focus();await field.press('ArrowDown');
   await page.screenshot({path:`${shots}/settings-zone-picker-1920.png`,fullPage:true});
   await field.press('Escape');
  }
  await page.getByRole('combobox',{name:'Theme',exact:true}).selectOption('colorblind-dark');
  await page.waitForFunction(()=>document.documentElement.dataset.theme==='colorblind-dark');
  await page.screenshot({path:`${shots}/settings-quiet-hours-colorblind-${width}.png`,fullPage:true});
  await page.close();
 }
 console.log('Account time zone: initialization, save and layout passed at 390px and 1920px.');
}finally{await browser?.close();await server.close();}
