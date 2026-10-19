import {afterEach,beforeEach,describe,expect,it,vi} from 'vitest'
import {createAttachmentPreview,TEXT_PREVIEW_BYTES,MAX_PREVIEW_BYTES} from './attachmentPreview'
const pdf=vi.hoisted(()=>({getDocument:vi.fn()}))
vi.mock('../pdf/loader',()=>({loadPdfJs:async()=>({getDocument:pdf.getDocument})}))
const signal=()=>new AbortController().signal
const png=(width=1,height=1)=>{
 const bytes=new Uint8Array(33);bytes.set([137,80,78,71,13,10,26,10,0,0,0,13,73,72,68,82])
 const data=new DataView(bytes.buffer);data.setUint32(16,width);data.setUint32(20,height);bytes[24]=8;bytes[25]=6;return bytes
}
beforeEach(()=>{pdf.getDocument.mockReset()})
afterEach(()=>{vi.restoreAllMocks();vi.unstubAllGlobals()})
describe('attachment preview hostile inputs',()=>{
 it('rejects pixel bombs from PNG JPEG GIF WebP and BMP headers before browser decode',async()=>{
  const decode=vi.fn();vi.stubGlobal('createImageBitmap',decode)
  const jpeg=new Uint8Array([255,216,255,192,0,17,8,255,255,255,255,3,1,17,0,2,17,0,3,17,0,255,217])
  const gif=new Uint8Array([71,73,70,56,57,97,255,255,255,255,0,0,0])
  const webp=new Uint8Array(30);webp.set(new TextEncoder().encode('RIFF'),0);webp.set(new TextEncoder().encode('WEBPVP8X'),8);webp.fill(255,24,30)
  const bmp=new Uint8Array(54);bmp.set([66,77]);const data=new DataView(bmp.buffer);data.setUint32(14,40,true);data.setInt32(18,65535,true);data.setInt32(22,65535,true)
  for(const [name,type,bytes]of [['bomb.png','image/png',png(65535,65535)],['bomb.jpg','image/jpeg',jpeg],['bomb.gif','image/gif',gif],['bomb.webp','image/webp',webp],['bomb.bmp','image/bmp',bmp]] as const){
   expect(await createAttachmentPreview(new File([bytes],name,{type}),signal())).toBeUndefined()
  }
  expect(decode).not.toHaveBeenCalled();expect(pdf.getDocument).not.toHaveBeenCalled()
 })
 it('refuses SVG and MIME or extension spoofs before any renderer sees their content',async()=>{
  const decode=vi.fn();vi.stubGlobal('createImageBitmap',decode)
  for(const file of [new File(['<svg><image href="https://preview.invalid/leak"/></svg>'],'image.PNG',{type:'image/png'}),new File([png()],'image.svg',{type:'image/png'}),new File([png()],'image.jpg',{type:'image/png'}),new File([png()],'image.pdf',{type:'application/pdf'}),new File(['%PDF-1.7'],'image.png',{type:'image/png'})]){
   expect(await createAttachmentPreview(file,signal())).toBeUndefined()
  }
  expect(decode).not.toHaveBeenCalled();expect(pdf.getDocument).not.toHaveBeenCalled()
 })
 it('reads only a small plaintext slice and never asks for whole-file text',async()=>{
  const file=new File(['<script>do not execute</script>\n'+ 'x'.repeat(TEXT_PREVIEW_BYTES*3)],'notes.md',{type:'text/markdown'})
  const full=vi.spyOn(file,'text').mockRejectedValue(new Error('whole file read'))
  const slices=vi.spyOn(file,'slice')
  const preview=await createAttachmentPreview(file,signal())
  expect(preview?.kind).toBe('text');expect(preview&&'text'in preview&&preview.text).toContain('<script>')
  expect(full).not.toHaveBeenCalled()
  expect(slices.mock.calls.every(([start,end])=>(start??0)===0&&typeof end==='number'&&end<=TEXT_PREVIEW_BYTES)).toBe(true)
 })
 it('refuses oversized PDF input without reading it or loading the parser',async()=>{
  const file=new File([new Uint8Array(MAX_PREVIEW_BYTES+1)],'large.pdf',{type:'application/pdf'})
  const read=vi.spyOn(file,'arrayBuffer')
  expect(await createAttachmentPreview(file,signal())).toBeUndefined();expect(read).not.toHaveBeenCalled();expect(pdf.getDocument).not.toHaveBeenCalled()
 })
 it('configures PDF parsing to deny ancillary fetches fonts scripting and unlimited image decode',async()=>{
  const destroy=vi.fn(async()=>{})
  pdf.getDocument.mockImplementation(()=>({promise:Promise.reject(new Error('fixture stops parsing')),destroy}))
  expect(await createAttachmentPreview(new File(['%PDF-1.7\n'],'safe.pdf',{type:'application/pdf'}),signal())).toBeUndefined()
  expect(pdf.getDocument).toHaveBeenCalledOnce()
  const options=pdf.getDocument.mock.calls[0][0]
  expect(options.data).toBeInstanceOf(Uint8Array);expect(options.url).toBeUndefined()
  expect(options).toMatchObject({disableFontFace:true,useSystemFonts:false,useWorkerFetch:false,useWasm:false,disableAutoFetch:true,disableStream:true,disableRange:true})
  expect(options.maxImageSize).toBeGreaterThan(0);expect(options.maxImageSize).toBeLessThanOrEqual(24_000_000)
  expect(options.canvasMaxAreaInBytes).toBeGreaterThan(0)
  const factory=new options.BinaryDataFactory()
  await expect(factory.fetch({filename:'https://preview.invalid/font'})).rejects.toThrow()
  expect(destroy).toHaveBeenCalled()
 })
 it('times out hung raster callers while retaining decode slots and closes late bitmaps',async()=>{
  vi.useFakeTimers({toFake:['setTimeout','clearTimeout']})
  const pending:Array<(bitmap:ImageBitmap)=>void>=[]
  const decode=vi.fn(()=>new Promise<ImageBitmap>(resolve=>pending.push(resolve)))
  vi.stubGlobal('createImageBitmap',decode)
  const make=()=>{const file=new File([png()],'small.png',{type:'image/png'});vi.spyOn(file,'arrayBuffer').mockResolvedValue(png().buffer);return file}
  const flush=async()=>{for(let i=0;i<20;i++)await Promise.resolve()}
  const a=new AbortController(),b=new AbortController(),c=new AbortController()
  const first=createAttachmentPreview(make(),a.signal),second=createAttachmentPreview(make(),b.signal)
  const thirdFile=make(),thirdRead=vi.spyOn(thirdFile,'arrayBuffer')
  const third=createAttachmentPreview(thirdFile,c.signal)
  try{
   await flush();expect(decode).toHaveBeenCalledTimes(2);expect(thirdRead).not.toHaveBeenCalled()
   await vi.advanceTimersByTimeAsync(8001)
   expect(await first).toBeUndefined();expect(await second).toBeUndefined()
   expect(decode).toHaveBeenCalledTimes(2);expect(thirdRead).not.toHaveBeenCalled()
   c.abort();expect(await third).toBeUndefined()
   const closeA=vi.fn(),closeB=vi.fn()
   pending[0]({close:closeA} as unknown as ImageBitmap);pending[1]({close:closeB} as unknown as ImageBitmap)
   await flush();expect(closeA).toHaveBeenCalledOnce();expect(closeB).toHaveBeenCalledOnce()
  }finally{
   a.abort();b.abort();c.abort()
   for(const resolve of pending)resolve({close:()=>{}} as unknown as ImageBitmap)
   await flush();vi.useRealTimers()
  }
 })

})
