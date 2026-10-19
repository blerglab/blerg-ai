import {useState} from 'react'
import {afterEach,describe,expect,it} from 'vitest'
import {fireEvent,render,waitFor,within} from '@testing-library/react'
import {page,userEvent} from 'vitest/browser'
import {ComposerChips,resetKeptAttachments,useAttachments,type TransportFiles} from '@blerglab/chat'
import '@blerglab/chat/blerg.css'
const PDF=`%PDF-1.4
1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj
2 0 obj<</Type/Pages/Kids[3 0 R 5 0 R]/Count 2>>endobj
3 0 obj<</Type/Page/Parent 2 0 R/MediaBox[0 0 400 200]/Contents 4 0 R/Resources<</Font<</F1 7 0 R>>>>>>endobj
4 0 obj<</Length 100>>stream
0.2 0.4 0.8 rg 20 20 120 30 re f
0 0 0 rg BT /F1 24 Tf 20 120 Td (FIRST PAGE) Tj ET
endstream endobj
5 0 obj<</Type/Page/Parent 2 0 R/MediaBox[0 0 400 200]/Contents 6 0 R/Resources<<>>>>endobj
6 0 obj<</Length 30>>stream
1 0 0 rg 0 0 400 200 re f
endstream endobj
7 0 obj<</Type/Font/Subtype/Type1/BaseFont/Helvetica>>endobj
trailer<</Root 1 0 R/Size 8>>
%%EOF`
async function imageFile(){const c=document.createElement('canvas');c.width=400;c.height=300;const g=c.getContext('2d')!;g.fillStyle='#ec7545';g.fillRect(0,0,400,300);g.fillStyle='#f4f7fb';g.fillRect(40,50,320,200);g.fillStyle='#213249';g.font='24px sans-serif';g.fillText('Local image',75,150);return new File([await new Promise<Blob>(resolve=>c.toBlob(blob=>resolve(blob!),'image/png'))],'screenshot.png',{type:'image/png'})}
function Harness({files}:{files:TransportFiles}){
 const[sid,setSid]=useState('A');const attached=useAttachments(sid,files)
 return <div style={{maxWidth:800,width:'100%'}}><button onClick={()=>setSid(sid==='A'?'B':'A')}>Switch session</button><span>Session {sid}</span><input aria-label="Choose attachments" type="file" multiple onChange={event=>event.target.files&&attached.addFiles(event.target.files)}/><ComposerChips items={attached.items} onRemove={attached.remove}/><button onClick={attached.clear}>Clear attachments</button></div>
}
function transport(pending=false){const completions:Array<()=>void>=[];const files:TransportFiles={list:async()=>[],raw:async()=>new Blob(),download:async()=>{},remove:async()=>{},upload:async(_sid,file,progress)=>{progress?.(Math.floor(file.size/2),file.size);if(pending)await new Promise<void>(resolve=>completions.push(resolve));return{id:file.name,name:file.name,size:file.size}}};return{files,completions}}
afterEach(async()=>{resetKeptAttachments();await page.viewport(1280,800)})
describe('composer attachment previews in Chromium',()=>{
 it('previews local image, PDF and inert Markdown before upload finishes, then retains them across session switches',async()=>{
  const{files,completions}=transport(true);const{getByRole,container}=render(<Harness files={files}/>)
  const list=[await imageFile(),new File([PDF],'brief.pdf',{type:'application/pdf'}),new File(['# Plan\n\n<img src="https://invalid.example/leak">\nKeep this plain.'],'plan.md',{type:'text/markdown'}),new File(['binary'],'archive.zip',{type:'application/zip'})]
  fireEvent.change(getByRole('button',{name:'Clear attachments'}).parentElement!.querySelector('input')!,{target:{files:list}})
  await waitFor(()=>expect(getByRole('img',{name:'Preview of screenshot.png'})).toBeVisible())
  await waitFor(()=>expect(getByRole('img',{name:'First page of brief.pdf'})).toBeVisible(),{timeout:12000})
  const pdfImage=getByRole('img',{name:'First page of brief.pdf'}) as HTMLImageElement
  await pdfImage.decode()
  const proof=document.createElement('canvas');proof.width=pdfImage.naturalWidth;proof.height=pdfImage.naturalHeight
  const context=proof.getContext('2d')!;context.drawImage(pdfImage,0,0)
  const pixels=context.getImageData(0,0,proof.width,proof.height).data
  let glyphs=0,blue=0,secondPageRed=0
  for(let p=0;p<pixels.length;p+=4){if(pixels[p]<80&&pixels[p+1]<80&&pixels[p+2]<80)glyphs++;if(pixels[p]<100&&pixels[p+1]>50&&pixels[p+1]<150&&pixels[p+2]>150)blue++;if(pixels[p]>180&&pixels[p+1]<80&&pixels[p+2]<80)secondPageRed++}
  expect(glyphs).toBeGreaterThan(10)
  expect(blue).toBeGreaterThan(100)
  expect(secondPageRed).toBe(0)
  expect(container.querySelector('.attach-chip-excerpt')?.textContent).toContain('<img src=')
  expect(container.querySelectorAll('img')).toHaveLength(2)
  expect(container.querySelectorAll('script,iframe,svg')).toHaveLength(0)
  expect(container.querySelectorAll('[role="progressbar"]')).toHaveLength(4)
  completions.forEach(done=>done())
  await waitFor(()=>expect(container.querySelectorAll('[role="progressbar"]')).toHaveLength(0))
  await userEvent.click(getByRole('button',{name:'Switch session'}));expect(container.querySelectorAll('[data-testid="attach-chip"]')).toHaveLength(0)
  await userEvent.click(getByRole('button',{name:'Switch session'}));expect(container.querySelectorAll('[data-testid="attach-chip"]')).toHaveLength(4)
  expect(getByRole('img',{name:'Preview of screenshot.png'})).toBeVisible()
  await userEvent.click(getByRole('button',{name:'Remove brief.pdf'}));expect(container.querySelectorAll('img')).toHaveLength(1)
  await userEvent.click(getByRole('button',{name:'Clear attachments'}));expect(container.querySelectorAll('[data-testid="attach-chip"]')).toHaveLength(0)
 },20000)
 it('keeps ten phone previews in a small scrollable strip with every remove control reachable',async()=>{
  await page.viewport(390,844);const{files}=transport();const{getByRole,container}=render(<Harness files={files}/>)
  const image=await imageFile();const list=Array.from({length:10},(_,n)=>n%2?new File(['# Attachment '+n],'notes-'+n+'.md',{type:'text/markdown'}):new File([image],'shot-'+n+'.png',{type:'image/png'}))
  fireEvent.change(container.querySelector('input')!,{target:{files:list}})
  await waitFor(()=>expect(container.querySelectorAll('.has-preview')).toHaveLength(10))
  const strip=getByRole('list',{name:'Attached files'})
  expect(strip.getBoundingClientRect().height).toBeLessThanOrEqual(180)
  expect(strip.getBoundingClientRect().right).toBeLessThanOrEqual(innerWidth)
  expect(strip.scrollWidth).toBeGreaterThan(strip.clientWidth*3)
  const first=within(strip).getByRole('button',{name:'Remove shot-0.png'})
  const last=within(strip).getByRole('button',{name:'Remove notes-9.md'})
  await userEvent.click(last);expect(strip.scrollLeft).toBeGreaterThan(0)
  await userEvent.click(first);expect(container.querySelectorAll('[data-testid="attach-chip"]')).toHaveLength(8)
 })
})
