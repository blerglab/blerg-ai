import {beforeEach,describe,expect,it,vi} from 'vitest'
import {createAttachmentPreview} from './attachmentPreview'
const documentTask=vi.fn()
vi.mock('../pdf/loader',()=>({loadPdfJs:async()=>({getDocument:documentTask})}))
beforeEach(()=>{vi.restoreAllMocks();documentTask.mockReset()})
describe('local attachment previews',()=>{
  it('reads a bounded plain-text excerpt without rendering Markdown or HTML',async()=>{
    const result=await createAttachmentPreview(new File(['# Notes\n<img src="https://invalid.example/leak">\n'+ 'x'.repeat(10000)],'notes.md',{type:'text/markdown'}),new AbortController().signal)
    expect(result?.kind).toBe('text')
    expect(result && 'text' in result && result.text).toContain('<img')
    expect(result && 'text' in result && result.text.length).toBeLessThanOrEqual(280)
  })
  it('refuses SVG and raster MIME/signature mismatches before decoding',async()=>{
    const decode=vi.fn();vi.stubGlobal('createImageBitmap',decode)
    for(const file of [new File(['<svg/>'],'diagram.svg',{type:'image/svg+xml'}),new File(['<svg/>'],'image.png',{type:'image/png'})])expect(await createAttachmentPreview(file,new AbortController().signal)).toBeUndefined()
    expect(decode).not.toHaveBeenCalled()
  })
  it('does not start PDF parsing for an oversized file or an already cancelled preview',async()=>{
    const large=new File([new Uint8Array(8*1024*1024+1)],'large.pdf',{type:'application/pdf'})
    expect(await createAttachmentPreview(large,new AbortController().signal)).toBeUndefined()
    const ctl=new AbortController();ctl.abort()
    expect(await createAttachmentPreview(new File(['%PDF-1.4'],'small.pdf',{type:'application/pdf'}),ctl.signal)).toBeUndefined()
    expect(documentTask).not.toHaveBeenCalled()
  })
})
