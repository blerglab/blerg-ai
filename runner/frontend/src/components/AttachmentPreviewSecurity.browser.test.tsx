import { expectOnlyFileIcons } from '../test/assertFileIcons'
import {afterEach,describe,expect,it,vi} from 'vitest'
import {fireEvent,render,waitFor} from '@testing-library/react'
import {cdp} from 'vitest/browser'
import type {CDPSession} from '@vitest/browser-playwright'
import {ComposerChips,useAttachments,resetKeptAttachments} from '@blerglab/chat'
import '@blerglab/chat/blerg.css'
type Files=NonNullable<Parameters<typeof useAttachments>[1]>
const files=():Files=>({list:vi.fn(async()=>[]),raw:vi.fn(async()=>new Blob()),download:vi.fn(async()=>{}),remove:vi.fn(async()=>{}),upload:vi.fn(async(sid,file)=>({id:`${sid}/${file.name}`,name:file.name,size:file.size}))})
function Harness({sid,transport,chosen=[]}:{sid:string;transport:Files;chosen?:File[]}){
 const attached=useAttachments(sid,transport)
 return <><button onClick={()=>attached.addFiles(chosen)}>Attach fixture</button><button onClick={attached.clear}>Send fixture</button><output data-testid="attached-ids">{attached.done.map(item=>item.id).join('|')}</output><ComposerChips items={attached.items} onRemove={attached.remove}/></>
}
async function raster(){
 const canvas=document.createElement('canvas');canvas.width=80;canvas.height=40
 canvas.getContext('2d')!.fillRect(0,0,80,40)
 const blob=await new Promise<Blob>(resolve=>canvas.toBlob(value=>resolve(value!),'image/png'))
 canvas.width=0;canvas.height=0;return new File([blob],'safe.png',{type:'image/png'})
}
function hostilePdf(){
 const js=[...new TextEncoder().encode('app.launchURL("https://attachment-security.invalid/action")')].map(byte=>byte.toString(16).padStart(2,'0')).join('')
 const stream='0.2 0.6 0.3 rg 10 10 200 100 re f\n'
 const objects=['<< /Type /Catalog /Pages 2 0 R /OpenAction 6 0 R >>','<< /Type /Pages /Kids [3 0 R] /Count 1 >>','<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 200] /Contents 4 0 R /Annots [5 0 R] /AA << /O 6 0 R >> >>',`<< /Length ${stream.length} >>\nstream\n${stream}endstream`,'<< /Type /Annot /Subtype /Link /Rect [0 0 300 200] /A << /S /URI /URI (https://attachment-security.invalid/link) >> >>',`<< /S /JavaScript /JS <${js}> >>`]
 let body='%PDF-1.7\n';const offsets=[0]
 objects.forEach((object,index)=>{offsets.push(body.length);body+=`${index+1} 0 obj\n${object}\nendobj\n`})
 const start=body.length;body+=`xref\n0 ${objects.length+1}\n0000000000 65535 f \n`+offsets.slice(1).map(offset=>`${String(offset).padStart(10,'0')} 00000 n \n`).join('')+`trailer\n<< /Size ${objects.length+1} /Root 1 0 R >>\nstartxref\n${start}\n%%EOF\n`
 return new File([body],'actions.pdf',{type:'application/pdf'})
}
afterEach(()=>{resetKeptAttachments();vi.restoreAllMocks()})
describe('attachment preview adversarial Chromium cases',()=>{
 it('renders only local raster/PDF thumbnails and inert text without outbound resources or actions',async()=>{
  const session=cdp() as CDPSession;const requests:string[]=[]
  const received=(event:{request:{url:string}})=>{if(event.request.url.includes('attachment-security.invalid'))requests.push(event.request.url)}
  await session.send('Network.enable')
  // Vitest's CDP on() returns synchronously while backend subscription uses an RPC.
  // A subsequent acknowledged CDP command fences listener installation before the probe.
  session.on('Network.requestWillBeSent',received)
  await session.send('Network.setBlockedURLs',{urls:['*://attachment-security.invalid/*']})
  try{
   await fetch('https://attachment-security.invalid/probe').catch(()=>{});await waitFor(()=>expect(requests.some(url=>url.endsWith('/probe'))).toBe(true),{timeout:5000});requests.length=0
   const chosen=[await raster(),hostilePdf(),new File(['<svg onload="location.href=\'https://attachment-security.invalid/svg\'"><image href="https://attachment-security.invalid/image"/></svg>'],'spoof.png',{type:'image/png'}),new File(['![track](https://attachment-security.invalid/md) <img src="https://attachment-security.invalid/html">'],'notes.md',{type:'text/markdown'})]
   const view=render(<Harness sid="safe-session" transport={files()} chosen={chosen}/>)
   fireEvent.click(view.getByRole('button',{name:'Attach fixture'}))
   const image=await view.findByRole('img',{name:'Preview of safe.png'}) as HTMLImageElement
   const pdf=await view.findByRole('img',{name:'First page of actions.pdf'},{timeout:10000}) as HTMLImageElement
   await waitFor(()=>expect(image.complete&&pdf.complete).toBe(true))
   for(const thumbnail of [image,pdf]){expect(thumbnail.src).toMatch(/^data:image\/png;base64,/);expect(thumbnail.naturalWidth).toBeLessThanOrEqual(160);expect(thumbnail.naturalHeight).toBeLessThanOrEqual(120)}
   const proof=document.createElement('canvas');proof.width=pdf.naturalWidth;proof.height=pdf.naturalHeight
   const context=proof.getContext('2d')!;context.drawImage(pdf,0,0);const pixels=context.getImageData(0,0,proof.width,proof.height).data
   let green=0;for(let index=0;index<pixels.length;index+=4)if(pixels[index+1]>100&&pixels[index+1]>pixels[index]*1.5&&pixels[index+1]>pixels[index+2]*1.5)green++
   expect(green).toBeGreaterThan(100);proof.width=0;proof.height=0
   expect(view.container.querySelectorAll('img')).toHaveLength(2)
   expect(view.container.querySelector('.attach-chip-excerpt')).toHaveTextContent('<img src=')
   expect(view.container.querySelector('iframe,script,a,style')).toBeNull()
   expectOnlyFileIcons(view.container)
   await new Promise(resolve=>setTimeout(resolve,100));expect(requests).toEqual([])
   fireEvent.click(view.getByRole('button',{name:'Remove safe.png'}));expect(view.queryByRole('img',{name:'Preview of safe.png'})).toBeNull()
   fireEvent.click(view.getByRole('button',{name:'Send fixture'}));expect(view.queryAllByTestId('attach-chip')).toHaveLength(0)
  }finally{await session.off('Network.requestWillBeSent',received);await session.send('Network.setBlockedURLs',{urls:[]});await session.send('Network.disable')}
 },15000)
 it('isolates same-instance session switches and ignores a late aborted preview result',async()=>{
  const slow=new File(['SECRET_A_LOCAL_PREVIEW'],'a-only.md',{type:'text/markdown'})
  let resolve!:(bytes:ArrayBuffer)=>void;const pending=new Promise<ArrayBuffer>(done=>{resolve=done})
  const slice=slow.slice.bind(slow)
  const reading=vi.spyOn(slow,'slice').mockImplementation((...args)=>{const chunk=slice(...args);Object.defineProperty(chunk,'arrayBuffer',{value:()=>pending});return chunk})
  const transport=files(),chosenB=[new File(['B_ONLY_PREVIEW'],'b-only.md',{type:'text/markdown'})]
  const view=render(<Harness sid="session-a" transport={transport} chosen={[slow]}/>)
  fireEvent.click(view.getByRole('button',{name:'Attach fixture'}));await waitFor(()=>expect(reading).toHaveBeenCalled())
  await waitFor(()=>expect(view.getByTestId('attached-ids')).toHaveTextContent('session-a/a-only.md'))
  view.rerender(<Harness sid="session-b" transport={transport} chosen={chosenB}/>)
  expect(view.queryByText('a-only.md')).toBeNull();expect(view.queryByText('SECRET_A_LOCAL_PREVIEW')).toBeNull()
  fireEvent.click(view.getByRole('button',{name:'Attach fixture'}));await view.findByText('B_ONLY_PREVIEW')
  resolve(new TextEncoder().encode('SECRET_A_LOCAL_PREVIEW').buffer);await new Promise(done=>setTimeout(done,20))
  expect(view.queryByText('SECRET_A_LOCAL_PREVIEW')).toBeNull();expect(view.getByTestId('attached-ids')).toHaveTextContent('session-b/b-only.md')
  view.rerender(<Harness sid="session-a" transport={transport}/>)
  expect(view.getByText('a-only.md')).toBeInTheDocument();expect(view.getByTestId('attached-ids')).toHaveTextContent('session-a/a-only.md')
  expect(view.queryByText('B_ONLY_PREVIEW')).toBeNull();expect(view.queryByText('SECRET_A_LOCAL_PREVIEW')).toBeNull()
 })
 it('evicts old preview data without losing completed unsent attachment metadata',async()=>{
  const transport=files();const initial=new File(['OLDEST_PREVIEW'],'oldest.md',{type:'text/markdown'})
  const view=render(<Harness sid="oldest-session" transport={transport} chosen={[initial]}/>)
  fireEvent.click(view.getByRole('button',{name:'Attach fixture'}));await view.findByText('OLDEST_PREVIEW')
  for(let index=0;index<18;index++){
   const sid=`later-${index}`,text=`LATER_PREVIEW_${index}`,chosen=[new File([text],`later-${index}.md`,{type:'text/markdown'})]
   view.rerender(<Harness sid={sid} transport={transport} chosen={chosen}/>)
   fireEvent.click(view.getByRole('button',{name:'Attach fixture'}));await view.findByText(text)
   await waitFor(()=>expect(view.getByTestId('attached-ids')).toHaveTextContent(`${sid}/later-${index}.md`))
  }
  view.rerender(<Harness sid="oldest-session" transport={transport}/>)
  expect(view.getByText('oldest.md')).toBeInTheDocument();expect(view.getByTestId('attached-ids')).toHaveTextContent('oldest-session/oldest.md')
  expect(view.queryByText('OLDEST_PREVIEW')).toBeNull()
  fireEvent.click(view.getByRole('button',{name:'Send fixture'}));view.rerender(<Harness sid="elsewhere" transport={transport}/>);view.rerender(<Harness sid="oldest-session" transport={transport}/>)
  expect(view.queryByText('oldest.md')).toBeNull()
 })
})
