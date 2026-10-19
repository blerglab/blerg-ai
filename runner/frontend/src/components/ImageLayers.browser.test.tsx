import {afterEach, describe, expect, it, vi} from 'vitest'
import {fireEvent, render, waitFor} from '@testing-library/react'
import {page, userEvent} from 'vitest/browser'
import {MarkupMode, type Pin} from '@blerglab/chat'
import '@blerglab/chat/blerg.css'

function imageSource() {
  const canvas = document.createElement('canvas')
  canvas.width = 400; canvas.height = 300
  const ctx = canvas.getContext('2d')!
  ctx.fillStyle = '#888888'; ctx.fillRect(0, 0, 400, 300)
  return canvas.toDataURL('image/png')
}
async function setup(delivery?: () => Promise<boolean>) {
  const src = imageSource()
  const sent: Array<{file: File; pins: Pin[]}> = []
  const view = render(<div style={{width: 'min(900px, 100%)'}}><MarkupMode artifact={{id:'layers',name:'design.png',size:100,view:'image',content_type:'image/png'}} src={src} onSend={async(file,pins)=>{sent.push({file,pins});return delivery ? delivery() : false}} onClose={vi.fn()} /></div>)
  const img = view.getByRole('img') as HTMLImageElement
  await waitFor(()=>expect(img.naturalWidth).toBe(400))
  const canvas = view.getByTestId('markup-canvas')
  await waitFor(()=>expect(canvas.getBoundingClientRect().width).toBeGreaterThan(50))
  const point = (x:number,y:number) => {
    const rect=canvas.getBoundingClientRect()
    return {clientX:rect.left+rect.width*x,clientY:rect.top+rect.height*y,pointerId:1,button:0,isPrimary:true}
  }
  const draw=(x:number,y:number,tx:number,ty:number)=>{
    fireEvent.pointerDown(canvas,point(x,y));fireEvent.pointerMove(canvas,point(tx,ty));fireEvent.pointerUp(canvas,point(tx,ty))
  }
  return {...view,src,sent,img,canvas,point,draw}
}
afterEach(async()=>{await page.viewport(1280,800)})

describe('image annotation layers in Chromium',()=>{
  it('selects and edits drawings, excludes hidden layers from the real PNG and preserves the source',async()=>{
    const {getByRole,draw,sent,img,src}=await setup()
    await userEvent.click(getByRole('button',{name:'Box'}));draw(.1,.1,.4,.4)
    await userEvent.fill(getByRole('textbox',{name:'Comment for annotation 1'}),'Move this group')
    await userEvent.click(getByRole('button',{name:'Danger color'}))
    await userEvent.click(getByRole('button',{name:'Stroke 6'}))
    await userEvent.click(getByRole('button',{name:'Hide annotation 1'}))
    expect((getByRole('button',{name:'Send'}) as HTMLButtonElement).disabled).toBe(true)
    await userEvent.click(getByRole('button',{name:'Arrow'}));draw(.6,.6,.8,.8)
    await userEvent.fill(getByRole('textbox',{name:'Comment for annotation 2'}),'Use this destination')
    await userEvent.click(getByRole('button',{name:'Send'}))
    await waitFor(()=>expect(sent.length).toBe(1))
    expect(sent[0]!.pins).toEqual([{n:2,note:'Use this destination',x:.8,y:.8}])
    expect(img.src).toBe(src)
    const bitmap=await createImageBitmap(sent[0]!.file)
    const result=document.createElement('canvas');result.width=400;result.height=300
    const ctx=result.getContext('2d')!;ctx.drawImage(bitmap,0,0);bitmap.close()
    const pixels=ctx.getImageData(20,20,160,120).data
    for(let i=0;i<pixels.length;i+=4) expect([...pixels.slice(i,i+3)]).toEqual([136,136,136])
    expect([...ctx.getImageData(320,240,1,1).data.slice(0,3)]).not.toEqual([136,136,136])
    await userEvent.click(getByRole('button',{name:'Show annotation 1'}))
    await userEvent.click(getByRole('button',{name:'Select Box annotation 1'}))
    expect(getByRole('button',{name:'Danger color'}).getAttribute('aria-pressed')).toBe('true')
    expect(getByRole('button',{name:'Stroke 6'}).getAttribute('aria-pressed')).toBe('true')
    await userEvent.click(getByRole('button',{name:'Delete annotation 1'}))
    expect(document.querySelector('[aria-label="Comment for annotation 1"]')).toBeNull()
  })

  it('uses native disabled fields and ignores canvas edits while an export is being delivered',async()=>{
    let finish!: (result: boolean) => void
    const delivery=new Promise<boolean>(resolve=>{finish=resolve})
    const {container,getByRole,draw}=await setup(()=>delivery)
    await userEvent.click(getByRole('button',{name:'Box'}));draw(.1,.1,.4,.4)
    await userEvent.fill(getByRole('textbox',{name:'Comment for annotation 1'}),'Preserve this comment')
    await userEvent.click(getByRole('button',{name:'Send'}))
    await waitFor(()=>expect(getByRole('button',{name:'Sending…'})).toBeDisabled())
    expect(getByRole('textbox',{name:'Comment for annotation 1'}).matches(':disabled')).toBe(true)
    expect(getByRole('button',{name:'Hide annotation 1'}).matches(':disabled')).toBe(true)
    expect(getComputedStyle(container.querySelector('.markup-editing')!).display).toBe('contents')
    draw(.6,.6,.8,.8)
    expect(container.querySelectorAll('.markup-layer')).toHaveLength(1)
    finish(false)
    await waitFor(()=>expect(getByRole('button',{name:'Send'})).toBeEnabled())
    expect(getByRole('textbox',{name:'Comment for annotation 1'}).matches(':disabled')).toBe(false)
    expect((getByRole('textbox',{name:'Comment for annotation 1'}) as HTMLTextAreaElement).value).toBe('Preserve this comment')
  })

  it('keeps layer comments and controls usable on a phone and selects a drawing by tapping its edge',async()=>{
    await page.viewport(390,844)
    const {container,getByRole,draw,canvas,point}=await setup()
    await userEvent.click(getByRole('button',{name:'Box'}));draw(.15,.15,.65,.6)
    await userEvent.click(getByRole('button',{name:'Arrow'}));draw(.7,.7,.9,.9)
    await userEvent.click(getByRole('button',{name:'Select'}))
    fireEvent.pointerDown(canvas,point(.15,.4))
    expect(getByRole('button',{name:'Select Box annotation 1'}).getAttribute('aria-pressed')).toBe('true')
    await userEvent.fill(getByRole('textbox',{name:'Comment for annotation 1'}),'Phone comment')
    for(const el of container.querySelectorAll<HTMLElement>('button,textarea')){
      const bounds=el.getBoundingClientRect()
      expect(bounds.left).toBeGreaterThanOrEqual(0)
      expect(bounds.right).toBeLessThanOrEqual(innerWidth)
    }
    const stage=container.querySelector('.markup-stage')!.getBoundingClientRect()
    const sidebar=getByRole('complementary',{name:'Annotations'}).getBoundingClientRect()
    expect(sidebar.top).toBeGreaterThanOrEqual(stage.bottom)
  })
})
