import { StrictMode } from 'react'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import VisualBlock from '../components/VisualBlock'
import { ViewerHostContext, type ViewerHostValue } from '../components/ViewerHost'
import { ViewerActionsContext } from '../components/ViewerToolbar'
import { renderVisual, type RenderedVisual } from './render'

vi.mock('./render', async original => ({...await original<typeof import('./render')>(), renderVisual: vi.fn()}))
const renderer = vi.mocked(renderVisual)
beforeEach(() => {
  renderer.mockReset()
  vi.spyOn(HTMLElement.prototype, 'clientWidth', 'get').mockReturnValue(640)
  vi.spyOn(HTMLElement.prototype, 'clientHeight', 'get').mockReturnValue(360)
})
const drawn = (dispose: () => void, extra: Partial<RenderedVisual> = {}): RenderedVisual =>
  ({svg: '<svg xmlns="http://www.w3.org/2000/svg"/>', width: 100, height: 50, dispose, ...extra})

it.each([true,false])('reserves the native chart footer only for a filled surface (%s)',async filled=>{
 const bounds=vi.spyOn(HTMLElement.prototype,'getBoundingClientRect').mockImplementation(function(this:HTMLElement){return {x:0,y:0,top:0,left:0,right:100,bottom:22,width:100,height:this.matches('.visual-blerg-table > summary')?22:0,toJSON:()=>({})}})
 const resize=vi.fn()
 renderer.mockImplementation(async(_kind,_source,host)=>{
  host.innerHTML='<svg/><details class="visual-blerg-table"><summary>Data table</summary></details>'
  return drawn(()=>{}, {resize})
 })
 const view=render(<VisualBlock kind="vega-lite" source='{"mark":"line","data":{"values":[{"x":1,"y":2}]},"encoding":{"x":{"field":"x","type":"quantitative"},"y":{"field":"y","type":"quantitative"}}}' hostedTab={filled} stretch/>)
 try {
 await waitFor(()=>expect(view.container.querySelector('[data-scene-state="exact"]')).toBeTruthy())
 if(filled)expect(resize).toHaveBeenCalledWith({width:640,height:338},expect.any(Number))
 else expect(resize).not.toHaveBeenCalled()
 expect(view.container.querySelector('.visual-surface')).toHaveAttribute('data-measured-height','360')
 } finally {view.unmount();bounds.mockRestore()}
})
it('keeps the fitted width when only the native footer limits height',async()=>{
 vi.spyOn(HTMLElement.prototype,'clientHeight','get').mockReturnValue(48)
 const bounds=vi.spyOn(HTMLElement.prototype,'getBoundingClientRect').mockImplementation(function(this:HTMLElement){return {x:0,y:0,top:0,left:0,right:100,bottom:22,width:100,height:this.matches('.visual-blerg-table > summary')?22:0,toJSON:()=>({})}})
 const resize=vi.fn()
 renderer.mockImplementation(async(_kind,_source,host)=>{
  host.innerHTML='<svg/><details class="visual-blerg-table"><summary>Data table</summary></details>'
  return drawn(()=>{}, {width:640,height:64,resize})
 })
 const view=render(<VisualBlock kind="vega-lite" source='{"height":32,"mark":"bar","data":{"values":[{"x":"A","y":2}]},"encoding":{"x":{"field":"x","type":"nominal"},"y":{"field":"y","type":"quantitative"}}}' hostedTab/>)
 try {
  await waitFor(()=>expect(view.container.querySelector('[data-scene-state="exact"]')).toBeTruthy())
  expect(resize).toHaveBeenCalledWith({width:640,height:26},expect.any(Number))
  expect(view.container.querySelector('.visual-surface')).toHaveAttribute('data-measured-height','48')
 }finally{view.unmount();bounds.mockRestore()}
})
function fakeHost(open: ViewerHostValue['open']): ViewerHostValue {
  return { open, close: () => {}, keep: () => {}, views: [], active: 'chat', target: null }
}

describe('visual block controls and lifetime', () => {
  it('shows source, copies original, opens a zoomable viewer', async () => {
    const copy = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(navigator, 'clipboard', {configurable: true, value: {writeText: copy}})
    renderer.mockImplementation(async (_kind, _source, host) => {
      host.textContent = 'Rendered diagram'
      return drawn(() => host.replaceChildren())
    })
    render(<VisualBlock kind="mermaid" source="flowchart LR; A-->B" />)
    expect(await screen.findByText('Rendered diagram')).toBeInTheDocument()
    await act(async () => {fireEvent.click(screen.getByRole('button', {name: 'Copy source'}))})
    expect(copy).toHaveBeenCalledWith('flowchart LR; A-->B')
    fireEvent.click(screen.getByRole('button', {name: 'Show source'}))
    expect(screen.getByText('flowchart LR; A-->B')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', {name: 'Full screen'}))
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', {name: 'Zoom in'}))
    expect(screen.getByRole('button', {name: 'Reset zoom'})).toHaveTextContent('125%')
    fireEvent.keyDown(screen.getByRole('dialog'), {key: 'Escape'})
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })
  it('falls back to source after runtime or parsing failure', async () => {
    renderer.mockRejectedValue(new Error('Mermaid: Parse error on line 1: Expecting \'GRAPH\', got \'NODE_STRING\''))
    render(<VisualBlock kind="mermaid" source="invalid source" />)
    // Explain the failure; reveal source only on request.
    expect(await screen.findByText('Couldn’t draw this diagram: Mermaid: Parse error on line 1: Expecting \'GRAPH\', got \'NODE_STRING\'')).toBeInTheDocument()
    expect(screen.queryByText('invalid source')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Show source' }))
    expect(screen.getByText('invalid source')).toBeInTheDocument()
  })
  it('isolates late StrictMode work from the current rendering and disposes both', async () => {
    const pending: {host: HTMLElement; resolve: (visual: RenderedVisual) => void}[] = []
    renderer.mockImplementation((_kind, _source, host) => new Promise(resolve => pending.push({host, resolve})))
    const firstCleanup = vi.fn()
    const currentCleanup = vi.fn()
    const {unmount} = render(<StrictMode><VisualBlock kind="mermaid" source="flowchart LR; A-->B" /></StrictMode>)
    expect(pending).toHaveLength(2)
    await act(async () => {
      pending[1].host.textContent = 'current visual'
      pending[1].resolve(drawn(currentCleanup))
    })
    await act(async () => {
      pending[0].host.textContent = 'late stale visual'
      pending[0].resolve(drawn(firstCleanup))
    })
    expect(await screen.findByText('current visual')).toBeInTheDocument()
    expect(screen.queryByText('late stale visual')).not.toBeInTheDocument()
    await waitFor(() => expect(firstCleanup).toHaveBeenCalledOnce())
    unmount()
    expect(currentCleanup).toHaveBeenCalledOnce()
  })
  it('cancels pending chart work when its source changes or it unmounts', async () => {
    const signals: AbortSignal[] = []
    renderer.mockImplementation((_kind, _source, _host, _theme, signal) => {
      signals.push(signal!)
      return new Promise(() => {})
    })
    const {rerender, unmount} = render(<VisualBlock kind="vega-lite" source='{"mark":"bar"}' />)
    await waitFor(() => expect(signals).toHaveLength(1))
    expect(signals[0].aborted).toBe(false)
    rerender(<VisualBlock kind="vega-lite" source='{"mark":"point"}' />)
    await waitFor(() => expect(signals).toHaveLength(2))
    expect(signals[0].aborted).toBe(true)
    expect(signals[1].aborted).toBe(false)
    unmount()
    expect(signals[1].aborted).toBe(true)
  })
  it('keeps source and the plain heavy-chart message after a worker refusal', async () => {
    renderer.mockRejectedValueOnce(new Error('This chart is too heavy to draw'))
    const source = '{"mark":"bar"}'
    render(<VisualBlock kind="vega-lite" source={source} />)
    expect(await screen.findByRole('status')).toHaveTextContent(/^This chart is too heavy to draw$/)
    expect(screen.queryByText(source)).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Show source' }))
    expect(screen.getByText(source)).toBeInTheDocument()
  })
  it('reports the current RenderedVisual to onRendered, and null while redrawing or on failure', async () => {
    renderer.mockImplementation(async (_kind, _source, host) => {
      host.textContent = 'Rendered diagram'
      return drawn(() => host.replaceChildren())
    })
    const onRendered = vi.fn()
    const {rerender} = render(<VisualBlock kind="mermaid" source="flowchart LR; A-->B" onRendered={onRendered} />)
    await waitFor(() => expect(onRendered).toHaveBeenLastCalledWith(expect.objectContaining({width: 100, height: 50})))
    onRendered.mockClear()
    renderer.mockImplementation(async () => { throw new Error('line 1: refused') })
    rerender(<VisualBlock kind="mermaid" source="flowchart LR; A-->C" onRendered={onRendered} />)
    // The redraw reports null before the renderer even settles, then stays null on failure.
    expect(onRendered).toHaveBeenCalledWith(null)
    await screen.findByRole('status')
    expect(onRendered).not.toHaveBeenCalledWith(expect.objectContaining({width: 100}))
  })
  it('offers no Tab button without a surrounding tab host', async () => {
    renderer.mockImplementation(async (_kind, _source, host) => { host.textContent = 'x'; return drawn(() => {}) })
    render(<VisualBlock kind="mermaid" source="flowchart LR; A-->B" />)
    await screen.findByText('x')
    expect(screen.queryByRole('button', { name: 'Open in a tab' })).toBeNull()
  })
  it('pops out a snapshot through the surrounding tab host, by a stable content id', async () => {
    renderer.mockImplementation(async (_kind, _source, host) => { host.textContent = 'x'; return drawn(() => {}) })
    const open = vi.fn()
    render(
      <ViewerHostContext.Provider value={fakeHost(open)}>
        <VisualBlock kind="mermaid" source="flowchart LR; A-->B" />
      </ViewerHostContext.Provider>,
    )
    await screen.findByText('x')
    fireEvent.click(screen.getByRole('button', { name: 'Open in a tab' }))
    expect(open).toHaveBeenCalledWith({ kind: 'visual', id: expect.any(String), visualKind: 'mermaid', source: 'flowchart LR; A-->B', label: 'Diagram' })
    const [[{ id: firstId }]] = open.mock.calls
    open.mockClear()
    fireEvent.click(screen.getByRole('button', { name: 'Open in a tab' }))
    const [[{ id: secondId }]] = open.mock.calls
    expect(secondId).toBe(firstId) // same source -> the same tab, not a stacked duplicate
  })
  it('offers no Tab button when already a tab\'s own body, even with a real host present', async () => {
    renderer.mockImplementation(async (_kind, _source, host) => { host.textContent = 'x'; return drawn(() => {}) })
    const open = vi.fn()
    render(
      <ViewerHostContext.Provider value={fakeHost(open)}>
        <VisualBlock kind="mermaid" source="flowchart LR; A-->B" hostedTab />
      </ViewerHostContext.Provider>,
    )
    await screen.findByText('x')
    expect(screen.queryByRole('button', { name: 'Open in a tab' })).toBeNull()
  })
  it('offers no Tab button on a hosted tab\'s own failed draw either', async () => {
    renderer.mockRejectedValue(new Error('line 12: url() not allowed'))
    const open = vi.fn()
    render(
      <ViewerHostContext.Provider value={fakeHost(open)}>
        <VisualBlock kind="mermaid" source="flowchart LR; A-->B" hostedTab />
      </ViewerHostContext.Provider>,
    )
    await screen.findByRole('status')
    expect(screen.queryByRole('button', { name: 'Open in a tab' })).toBeNull()
  })
  it('still offers Tab on a refused or failed diagram, opening the source as a snapshot', async () => {
    renderer.mockRejectedValue(new Error('line 12: url() not allowed'))
    const open = vi.fn()
    render(
      <ViewerHostContext.Provider value={fakeHost(open)}>
        <VisualBlock kind="mermaid" source="flowchart LR; A-->B" />
      </ViewerHostContext.Provider>,
    )
    await screen.findByRole('status')
    fireEvent.click(screen.getByRole('button', { name: 'Open in a tab' }))
    expect(open).toHaveBeenCalledWith({ kind: 'visual', id: expect.any(String), visualKind: 'mermaid', source: 'flowchart LR; A-->B', label: 'Diagram' })
  })
  it('mounts a renderer\'s own toolbar into the shared .visual-actions row in chat', async () => {
    const toolbarButton = document.createElement('button')
    toolbarButton.setAttribute('aria-label', 'Zoom in')
    renderer.mockImplementation(async (_kind, _source, host) => {
      host.textContent = 'Rendered diagram'
      return drawn(() => host.replaceChildren(), { toolbar: toolbarButton })
    })
    render(<VisualBlock kind="mermaid" source="gantt\nsection S\nT : 2024-01-01, 1d" />)
    await screen.findByText('Rendered diagram')
    const zoomIn = await screen.findByRole('button', { name: 'Zoom in' })
    expect(zoomIn.closest('.visual-actions')).not.toBeNull()
  })
  it('mounts a renderer\'s own toolbar into a surrounding tab\'s ViewerActionsContext host instead, when one is provided', async () => {
    const toolbarButton = document.createElement('button')
    toolbarButton.setAttribute('aria-label', 'Zoom in')
    renderer.mockImplementation(async (_kind, _source, host) => {
      host.textContent = 'Rendered diagram'
      return drawn(() => host.replaceChildren(), { toolbar: toolbarButton })
    })
    const toolbarHost = document.createElement('span')
    document.body.append(toolbarHost)
    try {
      render(
        <ViewerActionsContext.Provider value={toolbarHost}>
          <VisualBlock kind="mermaid" source="gantt\nsection S\nT : 2024-01-01, 1d" hostedTab />
        </ViewerActionsContext.Provider>,
      )
      await screen.findByText('Rendered diagram')
      await waitFor(() => expect(toolbarHost.querySelector('button[aria-label="Zoom in"]')).not.toBeNull())
      expect(screen.getByRole('button', { name: 'Zoom in' }).closest('.visual-actions')).toBeNull()
    } finally {
      toolbarHost.remove()
    }
  })
  it('says why a refused diagram or a chart that cannot be drawn shows its source', async () => {
    renderer.mockRejectedValueOnce(new Error('line 12: url() not allowed'))
    render(<VisualBlock kind="mermaid" source="flowchart LR; A-->B" />)
    expect(await screen.findByRole('status')).toHaveTextContent('Couldn’t draw this diagram: line 12: url() not allowed')
    renderer.mockRejectedValueOnce('odd')
    render(<VisualBlock kind="vega-lite" source="{}" />)
    expect(await screen.findByText('Couldn’t draw this chart: odd')).toBeInTheDocument()
  })
})

it('retains the complete chart across source arrival and disposes only after replacement commits', async () => {
  const pending: {host: HTMLElement; resolve: (visual: RenderedVisual) => void}[] = []
  renderer.mockImplementation((_kind, _source, host) => new Promise(resolve => pending.push({host, resolve})))
  const currentCleanup = vi.fn(() => { expect(document.body.textContent).toContain('replacement chart') })
  const {rerender, unmount} = render(<VisualBlock kind="vega-lite" source='{"mark":"bar","data":{"values":[]}}' />)
  await waitFor(() => expect(pending).toHaveLength(1))
  await act(async () => { pending[0].host.textContent = 'complete chart'; pending[0].resolve(drawn(currentCleanup)) })
  await screen.findByText('complete chart')
  rerender(<VisualBlock kind="vega-lite" source='{"mark":"point","data":{"values":[]}}' />)
  await waitFor(() => expect(pending).toHaveLength(2))
  expect(screen.getByText('complete chart')).toBeInTheDocument()
  expect(currentCleanup).not.toHaveBeenCalled()
  await act(async () => { pending[1].host.textContent = 'replacement chart'; pending[1].resolve(drawn(() => {})) })
  await screen.findByText('replacement chart')
  expect(currentCleanup).toHaveBeenCalledOnce()
  unmount()
})

it('keeps a fitted transfer snapshot when destination preparation fails', async () => {
  const source = '{"mark":"bar","data":{"values":[{"key":"snapshot-failure","n":1}]}}'
  renderer.mockImplementation(async (_kind, _source, host) => {
    host.innerHTML = '<svg xmlns="http://www.w3.org/2000/svg" width="100" height="50"><rect width="100" height="50"/></svg>'
    return drawn(() => host.replaceChildren())
  })
  const first = render(<VisualBlock kind="vega-lite" source={source} />)
  await waitFor(() => expect(first.container.querySelector('.visual-surface[data-scene-state="exact"]')).toBeTruthy())
  first.unmount()
  renderer.mockRejectedValue(new Error('Worker unavailable'))
  const destination = render(<VisualBlock kind="vega-lite" source={source} />)
  await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('Showing the last complete drawing'))
  expect(destination.container.querySelector('.visual-retained-snapshot svg rect')).toBeTruthy()
})

it('does not redraw Mermaid on an unrelated font load', async () => {
 const old=document.fonts
 const fonts=Object.assign(new EventTarget(),{ready:Promise.resolve()})
 Object.defineProperty(document,'fonts',{configurable:true,value:fonts})
 try {
  renderer.mockImplementation(async (_kind,_source,host)=>{host.textContent='font drawing';return drawn(()=>{})})
  const view=render(<VisualBlock kind="mermaid" source="flowchart LR; A-->B" />)
  await screen.findByText('font drawing')
  await act(async()=>{fonts.dispatchEvent(Object.assign(new Event('loadingdone'),{fontfaces:[{family:'Unrelated family'}]}))})
  expect(renderer).toHaveBeenCalledTimes(1);view.unmount()
 } finally {Object.defineProperty(document,'fonts',{configurable:true,value:old})}
})

it('invalidates a chart once, only for its own font family after an exact draw', async () => {
 const old=document.fonts, fonts=Object.assign(new EventTarget(),{ready:Promise.resolve()})
 Object.defineProperty(document,'fonts',{configurable:true,value:fonts})
 try {
  renderer.mockImplementation(async (_kind,_source,host)=>{host.innerHTML='<svg><rect width="30" height="30"/></svg>';return drawn(()=>{})})
  const view=render(<VisualBlock kind="vega-lite" source='{"mark":"bar"}' />)
  await waitFor(()=>expect(view.container.querySelector('[data-scene-state="exact"]')).toBeTruthy())
  const family=renderer.mock.calls[0][3]!.fontFamily.split(',')[0].trim().replace(/^['"]|['"]$/g,'')
  const load=async(family:string)=>act(async()=>{fonts.dispatchEvent(Object.assign(new Event('loadingdone'),{fontfaces:[{family}]}))})
  await load('Unrelated family');expect(renderer).toHaveBeenCalledTimes(1)
  await load(family);await waitFor(()=>expect(renderer).toHaveBeenCalledTimes(2))
  await waitFor(()=>expect(view.container.querySelector('[data-scene-state="exact"]')).toBeTruthy())
  await load(family);expect(renderer).toHaveBeenCalledTimes(2);view.unmount()
 } finally {Object.defineProperty(document,'fonts',{configurable:true,value:old})}
})

it('defers a matching hidden font invalidation until shown without redrawing ordinary round trips', async () => {
 const old=document.fonts, fonts=Object.assign(new EventTarget(),{ready:Promise.resolve()})
 Object.defineProperty(document,'fonts',{configurable:true,value:fonts})
 let width=640, notify: (()=>void) | undefined
 vi.spyOn(HTMLElement.prototype,'clientWidth','get').mockImplementation(()=>width)
 vi.stubGlobal('ResizeObserver', class {constructor(callback: ResizeObserverCallback){notify=()=>callback([{target:document.querySelector('.visual-surface')!}] as ResizeObserverEntry[],this as unknown as ResizeObserver)}observe(){}unobserve(){}disconnect(){}})
 try {
  renderer.mockImplementation(async (_kind,_source,host)=>{host.innerHTML='<svg><rect width="30" height="30"/></svg>';return drawn(()=>{})})
  const view=render(<VisualBlock kind="vega-lite" source='{"mark":"bar"}' />)
  await waitFor(()=>expect(view.container.querySelector('[data-scene-state="exact"]')).toBeTruthy())
  const family=renderer.mock.calls[0][3]!.fontFamily.split(',')[0].trim().replace(/^['"]|['"]$/g,'')
  await act(async()=>{width=0;notify!();fonts.dispatchEvent(Object.assign(new Event('loadingdone'),{fontfaces:[{family}]}))})
  expect(renderer).toHaveBeenCalledTimes(1)
  await act(async()=>{width=640;notify!()})
  await waitFor(()=>expect(renderer).toHaveBeenCalledTimes(2))
  await waitFor(()=>expect(view.container.querySelector('[data-scene-state="exact"]')).toBeTruthy())
  await act(async()=>{width=0;notify!();width=640;notify!()})
  expect(renderer).toHaveBeenCalledTimes(2);view.unmount()
 } finally {Object.defineProperty(document,'fonts',{configurable:true,value:old});vi.unstubAllGlobals()}
})

it('routes data-only source revisions through the mounted native handle', async()=>{
 const applyData=vi.fn(()=>true),dispose=vi.fn()
 renderer.mockImplementation(async (_kind,_source,host)=>{host.innerHTML='<svg><rect width="30" height="30"/></svg>';return drawn(dispose,{applyData})})
 const source=JSON.stringify({mark:'bar',data:{values:[{x:'A',n:1}]},encoding:{x:{field:'x',type:'nominal'},y:{field:'n',type:'quantitative'}}})
 const view=render(<VisualBlock kind="vega-lite" source={source}/>)
 await waitFor(()=>expect(view.container.querySelector('[data-scene-state="exact"]')).toBeTruthy())
 const root=view.container.querySelector('.visual-surface svg')!
 view.rerender(<VisualBlock kind="vega-lite" source={source.replace('"n":1','"n":9')}/>)
 await waitFor(()=>expect(applyData).toHaveBeenCalledOnce())
 expect(renderer).toHaveBeenCalledOnce();expect(view.container.querySelector('.visual-surface svg')).toBe(root)
 expect(dispose).not.toHaveBeenCalled();view.unmount();expect(dispose).toHaveBeenCalledOnce()
})

it('keeps a refused rows revision responsive to resize, theme and font changes without retrying it', async () => {
 let notify: (()=>void)|undefined, width=390
 vi.stubGlobal('ResizeObserver',class {constructor(cb:(entries: {target:Element}[])=>void){notify=()=>cb([...document.querySelectorAll('.visual-surface')].map(target=>({target})))}observe(){}unobserve(){}disconnect(){}})
 vi.spyOn(HTMLElement.prototype,'clientWidth','get').mockImplementation(()=>width)
 const applyData=vi.fn(()=>false),resize=vi.fn(),setTheme=vi.fn(),warn=vi.spyOn(console,'warn').mockImplementation(()=>{})
 renderer.mockImplementation(async (_kind,_source,host)=>{host.innerHTML='<svg><rect/></svg>';return drawn(()=>{}, {applyData,resize,setTheme})})
 const oldFonts=document.fonts,fonts=Object.assign(new EventTarget(),{ready:Promise.resolve()})
 Object.defineProperty(document,'fonts',{configurable:true,value:fonts})
 const spec={mark:'bar',data:{values:[{x:'A',n:1}]},encoding:{x:{field:'x',type:'nominal'},y:{field:'n',type:'quantitative'}}}
 const view=render(<VisualBlock kind="vega-lite" source={JSON.stringify(spec)} />)
 await waitFor(()=>expect(view.container.querySelector('[data-scene-state="exact"]')).toBeTruthy())
 view.rerender(<VisualBlock kind="vega-lite" source={JSON.stringify({...spec,data:{values:[{x:'A',n:2}]}})} />)
 await waitFor(()=>expect(applyData).toHaveBeenCalledOnce())
 await act(async()=>{width=500;notify!()})
 await waitFor(()=>expect(resize).toHaveBeenCalled())
 view.container.style.setProperty('--chat-accent','rgb(10, 90, 160)')
 await waitFor(()=>expect(setTheme).toHaveBeenCalled())
 const resizes=resize.mock.calls.length
 const family=renderer.mock.calls[0][3]!.fontFamily.split(',')[0].trim().replace(/^["']|["']$/g,'')
 await act(async()=>fonts.dispatchEvent(Object.assign(new Event('loadingdone'),{fontfaces:[{family}]})))
 expect(resize.mock.calls.length).toBeGreaterThan(resizes)
 expect(applyData).toHaveBeenCalledOnce();expect(renderer).toHaveBeenCalledOnce()
 expect(warn.mock.calls.length).toBeLessThanOrEqual(1)
 expect(view.container).toHaveTextContent('Showing the last complete drawing')
 view.unmount();Object.defineProperty(document,'fonts',{configurable:true,value:oldFonts});vi.unstubAllGlobals()
})

it('coalesces native chart resizes outside ResizeObserver delivery', async () => {
 let notify: (() => void) | undefined, width = 640
 vi.stubGlobal('ResizeObserver', class {
  constructor(callback: ResizeObserverCallback) { notify = () => callback([...document.querySelectorAll('.visual-surface')].map(target => ({target})) as ResizeObserverEntry[], this as unknown as ResizeObserver) }
  observe() {} unobserve() {} disconnect() {}
 })
 vi.spyOn(HTMLElement.prototype, 'clientWidth', 'get').mockImplementation(() => width)
 const resize = vi.fn()
 renderer.mockImplementation(async (_kind, _source, host) => { host.innerHTML = '<svg/>'; return drawn(() => {}, {resize}) })
 const view = render(<VisualBlock kind="vega-lite" source='{"mark":"bar","data":{"values":[{"x":"A","n":1}]},"encoding":{"x":{"field":"x","type":"nominal"},"y":{"field":"n","type":"quantitative"}}}' />)
 try {
  await waitFor(() => expect(view.container.querySelector('[data-scene-state="exact"]')).toBeTruthy())
  const frames = new Map<number, FrameRequestCallback>(); let next = 0
  vi.stubGlobal('requestAnimationFrame', (callback: FrameRequestCallback) => { frames.set(++next, callback); return next })
  vi.stubGlobal('cancelAnimationFrame', (id: number) => frames.delete(id))
  act(() => { width = 300; notify!(); width = 320; notify!() })
  expect(resize).not.toHaveBeenCalled()
  expect(frames.size).toBe(1)
  await act(async () => { for (const callback of frames.values()) callback(performance.now()); frames.clear() })
  expect(resize).toHaveBeenCalledOnce()
  expect(view.container.querySelector('.visual-surface')).toHaveAttribute('data-measured-width', '320')
 } finally { view.unmount(); vi.unstubAllGlobals() }
})
