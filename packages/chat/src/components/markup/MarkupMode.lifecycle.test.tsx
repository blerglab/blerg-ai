import {afterEach, beforeEach, expect, it, vi} from 'vitest'
import {act, fireEvent, render, screen} from '@testing-library/react'
import MarkupMode from './MarkupMode'

beforeEach(()=>{
 vi.spyOn(HTMLCanvasElement.prototype,'getContext').mockImplementation(()=>new Proxy({}, {get:()=>()=>{},set:()=>true}) as CanvasRenderingContext2D)
 vi.spyOn(HTMLCanvasElement.prototype,'toBlob').mockImplementation(cb=>cb(new Blob(['png'],{type:'image/png'})))
 vi.spyOn(HTMLCanvasElement.prototype,'getBoundingClientRect').mockReturnValue({left:0,top:0,width:400,height:300} as DOMRect)
})
afterEach(()=>{vi.restoreAllMocks();vi.useRealTimers()})

async function setupPending() {
 let finish!:(value:boolean)=>void
 const onSend=vi.fn(()=>new Promise<boolean>(resolve=>{finish=resolve}))
 const onClose=vi.fn()
 const view=render(<MarkupMode artifact={{id:'lifecycle',name:'picture.png',size:10,view:'image',content_type:'image/png'}} src="blob:picture" onSend={onSend} onClose={onClose}/>)
 const img=screen.getByRole('img')
 Object.defineProperties(img,{naturalWidth:{value:400},naturalHeight:{value:300}})
 fireEvent.load(img)
 fireEvent.click(screen.getByRole('button',{name:'Box'}))
 const canvas=screen.getByTestId('markup-canvas')
 const draw=()=>{
  fireEvent.pointerDown(canvas,{clientX:30,clientY:30,button:0,pointerId:1})
  fireEvent.pointerUp(canvas,{clientX:100,clientY:100,button:0,pointerId:1})
 }
 draw()
 const note=screen.getByRole('textbox',{name:'Comment for annotation 1'}) as HTMLTextAreaElement
 fireEvent.change(note,{target:{value:'first comment'}})
 fireEvent.click(screen.getByRole('button',{name:'Send'}))
 await act(async()=>{await Promise.resolve()})
 expect(onSend).toHaveBeenCalledTimes(1)
 return {onSend,onClose,note,draw,finish,unmount:view.unmount}
}

it('freezes comments, layer actions, canvas and shortcuts while the image snapshot is sending',async()=>{
 const {note,draw,finish}=await setupPending()
 expect(note).toBeDisabled()
 expect(screen.getByRole('button',{name:'Hide annotation 1'})).toBeDisabled()
 expect(screen.getByRole('button',{name:'Delete annotation 1'})).toBeDisabled()
 expect(screen.getByRole('button',{name:'Sending…'})).toBeDisabled()
 draw()
 expect(screen.queryByRole('button',{name:'Select Box annotation 2'})).toBeNull()
 fireEvent.keyDown(screen.getByTestId('markup-mode'),{key:'z'})
 expect(screen.getByRole('button',{name:'Select Box annotation 1'})).toBeInTheDocument()
 expect(screen.getByRole('button',{name:'Cancel'})).toBeEnabled()
 await act(async()=>{finish(false)})
})

it('keeps the successful snapshot frozen until the success delay closes the editor',async()=>{
 vi.useFakeTimers()
 const {note,draw,onClose,finish}=await setupPending()
 await act(async()=>{finish(true)})
 expect(note).toBeDisabled()
 expect(screen.getByRole('button',{name:'Send'})).toBeDisabled()
 draw()
 expect(screen.queryByRole('button',{name:'Select Box annotation 2'})).toBeNull()
 await act(async()=>{vi.advanceTimersByTime(800)})
 expect(onClose).toHaveBeenCalledTimes(1)
})

it('restores editing after failed upload without dropping the comment or layers',async()=>{
 const {note,draw,onClose,finish}=await setupPending()
 await act(async()=>{finish(false)})
 expect(note).toBeEnabled()
 expect(note).toHaveValue('first comment')
 expect(screen.getByRole('button',{name:'Send'})).toBeEnabled()
 expect(onClose).not.toHaveBeenCalled()
 fireEvent.change(note,{target:{value:'retry comment'}})
 draw()
 expect(screen.getByRole('button',{name:'Select Box annotation 2'})).toBeInTheDocument()
 fireEvent.click(screen.getByRole('button',{name:'Select Box annotation 1'}))
 expect(screen.getByRole('button',{name:'Select'})).toHaveAttribute('aria-pressed','true')
})

it('does not close a newer viewer when an abandoned upload resolves later',async()=>{
 vi.useFakeTimers()
 const {unmount,onClose,finish}=await setupPending()
 unmount()
 await act(async()=>{finish(true)})
 await act(async()=>{vi.advanceTimersByTime(800)})
 expect(onClose).not.toHaveBeenCalled()
})

it('cancels the delayed success close when the image editor is unmounted',async()=>{
 vi.useFakeTimers()
 const {unmount,onClose,finish}=await setupPending()
 await act(async()=>{finish(true)})
 unmount()
 await act(async()=>{vi.advanceTimersByTime(800)})
 expect(onClose).not.toHaveBeenCalled()
})
