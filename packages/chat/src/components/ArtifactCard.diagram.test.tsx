// Board card #84: a published .mmd shows as the diagram in the chat, not as its text.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import ArtifactCard from './ArtifactCard'
import ArtifactViewer from './ArtifactViewer'
import { ChatContext, type ChatContextValue } from './context'
import { clearPreviewCache } from '../model/artifactPreview'
import { createFakeFiles, type FakeFiles } from '../test/fakeTransport'
import { renderVisual } from '../visual/render'

vi.mock('../visual/render', async original => ({...await original<typeof import('../visual/render')>(), renderVisual: vi.fn()}))
const renderer = vi.mocked(renderVisual)

afterEach(() => vi.restoreAllMocks())

let files: FakeFiles
let chat: ChatContextValue
const SOURCE = 'flowchart LR\n  ctl[vm-image-controller] --> reg[(image registry)]\n'

beforeEach(() => {
  clearPreviewCache()
  renderer.mockReset().mockImplementation(async (_kind, source, host) => {
    host.textContent = `drawn: ${source}`
    return {svg: '<svg xmlns="http://www.w3.org/2000/svg"/>', width: 100, height: 50, dispose: () => host.replaceChildren()}
  })
  files = createFakeFiles()
  chat = {
    sessionId: 's1', files, view: vi.fn(), latestVersion: () => undefined, byName: () => undefined,
    submitFeedback: vi.fn(async () => true), reviewFor: vi.fn(async () => null),
  }
})

describe('ArtifactCard: a Mermaid file', () => {
  it('draws a published .mmd as the diagram, with View', async () => {
    files.serve(SOURCE)
    const payload = { id: 'm1', name: 'services.mmd', size: SOURCE.length, content_type: 'text/plain', view: 'text' }
    render(<ChatContext.Provider value={chat}><ArtifactCard payload={payload} /></ChatContext.Provider>)
    await waitFor(() => expect(screen.getByTestId('artifact-preview-diagram')).toHaveTextContent('drawn: flowchart LR'))
    expect(renderer).toHaveBeenCalledWith('mermaid', SOURCE.trimEnd(), expect.any(HTMLElement), expect.anything(), expect.any(AbortSignal), expect.objectContaining({ fill: true }), undefined)
    expect(screen.queryByTestId('artifact-preview-excerpt')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'View' }))
    expect(chat.view).toHaveBeenCalledWith(payload)
  })

  it('keeps refused source behind the source toggle', async () => {
    renderer.mockRejectedValue(new Error('line 2: HTML not allowed'))
    files.serve(SOURCE)
    render(<ChatContext.Provider value={chat}><ArtifactCard payload={{ id: 'm2', name: 'bad.mermaid', size: SOURCE.length, content_type: 'text/plain', view: 'text' }} /></ChatContext.Provider>)
    expect(await screen.findByRole('status')).toHaveTextContent('Couldn’t draw this diagram: line 2: HTML not allowed')
    expect(screen.getByTestId('visual-failed')).not.toHaveTextContent('vm-image-controller')
    fireEvent.click(screen.getByRole('button', { name: 'Show source' }))
    expect(screen.getByTestId('visual-failed')).toHaveTextContent('vm-image-controller')
  })

  it('draws a published Vega-Lite chart and refreshes a live revision', async () => {
    const applyData=vi.fn(()=>true)
    renderer.mockImplementation(async (_kind,source,host)=>{
      host.textContent='drawn: '+source
      return {svg:'<svg/>',width:100,height:50,applyData,dispose:()=>host.replaceChildren()}
    })
    vi.spyOn(HTMLElement.prototype, 'clientWidth', 'get').mockReturnValue(600)
    vi.spyOn(HTMLElement.prototype, 'clientHeight', 'get').mockReturnValue(260)
    const source = '{"data":{"values":[{"category":"A","n":2}]},"mark":"bar","encoding":{"x":{"field":"category","type":"nominal"},"y":{"field":"n","type":"quantitative"}}}'
    files.serve(source)
    const payload = { id: 'v1', name: 'counts.vl.json', size: source.length, content_type: 'application/json', view: 'json', live: true, updated_at: '2026-11-04T00:00:00Z' }
    const view = render(<ChatContext.Provider value={chat}><ArtifactCard payload={payload} /></ChatContext.Provider>)
    await waitFor(() => expect(renderer).toHaveBeenCalledWith('vega-lite', source, expect.any(HTMLElement), expect.anything(), expect.any(AbortSignal), expect.anything(), undefined))
    expect(screen.queryByTestId('artifact-preview-excerpt')).toBeNull()
    // A renderer invocation starts preparation; live applyData requires its committed scene.
    await waitFor(() => expect(view.container.querySelector('.visual-surface')).toHaveAttribute('data-scene-state', 'exact'))
    files.serve(source.replace('"n":2', '"n":9'))
    view.rerender(<ChatContext.Provider value={chat}><ArtifactCard payload={{ ...payload, updated_at: '2026-11-04T00:05:00Z' }} /></ChatContext.Provider>)
    await waitFor(() => expect(applyData).toHaveBeenCalledWith(expect.objectContaining({data:{values:[{category:'A',n:9}]}}),expect.any(Number)))
    expect(renderer).toHaveBeenCalledTimes(1)
  })

  it('keeps the text excerpt for other text files', async () => {
    files.serve('plain notes\n')
    render(<ChatContext.Provider value={chat}><ArtifactCard payload={{ id: 't1', name: 'notes.txt', size: 12, content_type: 'text/plain', view: 'text' }} /></ChatContext.Provider>)
    expect(await screen.findByTestId('artifact-preview-excerpt')).toBeInTheDocument()
    expect(renderer).not.toHaveBeenCalled()
  })

  it('draws only once the card comes near the viewport', async () => {
    let fire: (() => void) | undefined
    vi.stubGlobal('IntersectionObserver', class {
      constructor(cb: IntersectionObserverCallback) { fire = () => cb([{ isIntersecting: true } as IntersectionObserverEntry], this as unknown as IntersectionObserver) }
      observe() {}
      disconnect() {}
    })
    try {
      files.serve(SOURCE)
      const raw = vi.spyOn(files, 'raw')
      render(<ChatContext.Provider value={chat}><ArtifactCard payload={{ id: 'm3', name: 'far.mmd', size: SOURCE.length, content_type: 'text/plain', view: 'text' }} /></ChatContext.Provider>)
      await act(async () => {})
      expect(raw).not.toHaveBeenCalled()
      expect(renderer).not.toHaveBeenCalled()
      act(() => fire!())
      await waitFor(() => expect(renderer).toHaveBeenCalledTimes(1))
    } finally { vi.unstubAllGlobals() }
  })
})

it('refreshes an inline dashboard preview on live publication', async () => {
  const document = {format: 'blerg-dashboard', version: 1, title: 'Local telemetry', cards: [{id: 'workers', kind: 'stat', title: 'Workers', span: 1, data: {kind: 'inline', rows: [{n: 2}]}, view: {value: 'n', selection: 'single'}}]}
  files.serve(JSON.stringify(document))
  const payload = {id: 'dash1', name: 'telemetry.dash.json', size: 200, content_type: 'application/json', view: 'json', live: true, updated_at: '2026-11-04T00:00:00Z'}
  const mounted = render(<ChatContext.Provider value={chat}><ArtifactCard payload={payload} /></ChatContext.Provider>)
  await waitFor(() => expect(mounted.container.querySelector('.dashboard-value')).toHaveTextContent('2'))
  document.cards[0].data.rows[0].n = 9
  files.serve(JSON.stringify(document))
  mounted.rerender(<ChatContext.Provider value={chat}><ArtifactCard payload={{...payload, updated_at: '2026-11-04T00:01:00Z'}} /></ChatContext.Provider>)
  await waitFor(() => expect(mounted.container.querySelector('.dashboard-value')).toHaveTextContent('9'))
  expect(mounted.container.querySelector('[data-testid="artifact-preview-excerpt"]')).toBeNull()
})

it('opens a prepared chart without rereading its source or exposing a loading shell', async () => {
  vi.spyOn(HTMLElement.prototype, 'clientWidth', 'get').mockReturnValue(600)
  vi.spyOn(HTMLElement.prototype, 'clientHeight', 'get').mockReturnValue(260)
  const source = '{"data":{"values":[{"category":"A","n":2}]},"mark":"bar","encoding":{"x":{"field":"category","type":"nominal"},"y":{"field":"n","type":"quantitative"}}}'
  files.serve(source)
  const artifact = {id: 'transfer-chart', name: 'counts.vl.json', size: source.length, content_type: 'application/json', view: 'json', updated_at: '2026-11-04T00:00:00Z'}
  render(<ChatContext.Provider value={chat}><ArtifactCard payload={artifact} /></ChatContext.Provider>)
  await waitFor(() => expect(screen.getByTestId('artifact-preview-diagram')).toHaveTextContent('drawn:'))
  const reads = files.rawCalls.length
  const viewer = render(<ArtifactViewer sessionId="s1" files={files} artifact={artifact} onClose={() => {}} embedded />)
  expect(viewer.queryByTestId('artifact-loading')).toBeNull()
  await waitFor(() => expect(viewer.container).toHaveTextContent('drawn:'))
  expect(files.rawCalls.length).toBe(reads)
})


it('follows file revisions when a native model falls back at runtime',async()=>{
 vi.spyOn(HTMLElement.prototype,'clientWidth','get').mockReturnValue(600)
 vi.spyOn(HTMLElement.prototype,'clientHeight','get').mockReturnValue(260)
 const source=JSON.stringify({mark:'bar',data:{values:[{x:'A',n:2}]},encoding:{x:{field:'x',type:'nominal'},y:{field:'n',type:'quantitative'}}})
 files.serve(source)
 const payload={id:'runtime-static',name:'chart.vl.json',size:source.length,content_type:'application/json',view:'json',live:true,updated_at:'2026-11-04T00:00:00Z'}
 const view=render(<ChatContext.Provider value={chat}><ArtifactCard payload={payload} /></ChatContext.Provider>)
 await waitFor(()=>expect(view.container.querySelector('[title^="unsupported-live"]')).toBeTruthy())
 const reads=files.rawCalls.length
 view.rerender(<ChatContext.Provider value={chat}><ArtifactCard payload={{...payload,updated_at:'2026-11-04T00:05:00Z'}} /></ChatContext.Provider>)
 await waitFor(()=>expect(files.rawCalls).toHaveLength(reads+1))
 await waitFor(()=>expect(view.container.querySelector('.artifact-status time')).toHaveAttribute('datetime','2026-11-04T00:05:00.000Z'))
})

for(const target of ['preview','tab'])it(`recovers live support on a new committed native scene in the ${target}`,async()=>{
 vi.spyOn(HTMLElement.prototype,'clientWidth','get').mockReturnValue(600)
 vi.spyOn(HTMLElement.prototype,'clientHeight','get').mockReturnValue(260)
 const source=JSON.stringify({mark:'bar',data:{values:[{x:'A',n:2}]},encoding:{x:{field:'x',type:'nominal'},y:{field:'n',type:'quantitative'}}})
 files.serve(source)
 const payload={id:'runtime-recover-'+target,name:'recover.vl.json',size:source.length,content_type:'application/json',view:'json',live:true,updated_at:'2026-11-04T00:00:00Z'}
 const draw=()=>target==='preview'?<ArtifactCard payload={payload}/>:<ArtifactViewer sessionId="s1" files={files} artifact={payload} embedded onClose={()=>{}}/>
 const view=render(<ChatContext.Provider value={chat}>{draw()}</ChatContext.Provider>)
 await waitFor(()=>expect(view.container.querySelector('[title^="unsupported-live"]')).toBeTruthy())
 renderer.mockImplementation(async (_kind,_source,host)=>{host.innerHTML='<svg/>';return {svg:'<svg/>',width:100,height:50,applyData:()=>true,dispose:()=>host.replaceChildren()}})
 view.container.style.setProperty('--chat-accent','rgb(10, 90, 160)')
 await waitFor(()=>expect(view.container.querySelector('[title^="unsupported-live"]')).toBeNull())
 expect(view.container.querySelector('.visual-surface')).toHaveAttribute('data-live-support','true')
 view.unmount()
})
