// Real People and invite UI with fake transport, at phone and wide desktop widths.
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { createServer } from 'vite';
import { chromium } from 'playwright';
const server=await createServer({server:{host:'127.0.0.1',port:0}});
let browser;
try {
 await server.listen();browser=await chromium.launch({headless:true});
 const origin=server.resolvedUrls.local[0];
 for(const width of [390,3840]){
  const page=await browser.newPage({viewport:{width,height:1100}});const errors=[];
  page.on('pageerror',e=>errors.push(e.message));
  let disabled=false;
  await page.route('**/api/**',async route=>{
   const path=new URL(route.request().url()).pathname;
   let body={};
   if(path==='/api/people')body=route.request().method()==='POST'?{code:'a'.repeat(43)}:{people:[{id:'p',name:'A'.repeat(200),email:'b'.repeat(64)+'@example.com',username:'person',role:'member',disabled,pending:true}]};
   else if(path.endsWith('/disable')){disabled=true;body={status:'disabled'};}
   else if(path==='/api/site')body={version:'dev',runner_url:'https://runner.example.test',board_url:''};
   else if(path==='/api/credentials')body=[];
   else if(path==='/api/desktops')body={desktops:[],owners:[],actor:'me',can_manage_others:true};
   else if(path==='/api/desktop-enrollment-codes')body={codes:[]};
   await route.fulfill({status:200,contentType:'application/json',body:JSON.stringify(body)});
  });
  await page.route('**/brand/tokens.css',async route=>route.fulfill({status:200,contentType:'text/css',body:await readFile('../internal/landing/static/tokens.css','utf8')}));
  const token=`x.${Buffer.from(JSON.stringify({caps:['account.manage','card.read']})).toString('base64url')}.y`;
  await page.goto(`${origin}settings?section=people#access_token=${token}`);
  await page.getByRole('heading',{name:'People',exact:true}).waitFor();
  await page.getByLabel('Name',{exact:true}).fill('Someone');await page.getByLabel('Email',{exact:true}).fill('someone@example.com');
  await page.getByRole('button',{name:'Add a person'}).click();await page.getByLabel('Invite link',{exact:true}).waitFor();
  assert.equal(await page.getByLabel('Invite link',{exact:true}).inputValue(),`${new URL(origin).origin}/invite#code=${'a'.repeat(43)}`);
  await page.getByRole('button',{name:'Done',exact:true}).click();
  await page.getByRole('button',{name:'Disable',exact:true}).click();await page.getByRole('button',{name:'Disable person',exact:true}).click();await page.getByRole('button',{name:'Disconnect desktops',exact:true}).waitFor();
  assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),`People overflow at ${width}`);
  await page.goto(`${origin}invite#code=${'b'.repeat(43)}`);await page.getByRole('heading',{name:'Welcome to Blerg'}).waitFor();
  assert.equal(new URL(page.url()).hash,'');
  assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),`Invite overflow at ${width}`);
  assert.deepEqual(errors,[]);await page.close();console.log(`PASS People and invite at ${width}px`);
 }
}finally{await browser?.close();await server.close();}
