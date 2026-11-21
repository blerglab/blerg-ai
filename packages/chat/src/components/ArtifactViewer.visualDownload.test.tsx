// A published chart/diagram's tab gets Download SVG/PNG, built from its current drawing
// (RenderedVisual), alongside the existing Download (source) action -- BLG-451.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import ArtifactViewer from './ArtifactViewer'
import { ViewerHostContext, type ViewerHostValue } from './ViewerHost'
import { createFakeFiles, type FakeFiles } from '../test/fakeTransport'
import type { ArtifactInfo } from '../transport/types'
import { renderVisual } from '../visual/render'

vi.mock('../visual/render', () => ({ renderVisual: vi.fn() }))
const renderer = vi.mocked(renderVisual)
let files: FakeFiles
const artifact: ArtifactInfo = { id: 'd1', name: 'services.mmd', size: 40, content_type: 'text/plain', view: 'text' }

beforeEach(() => {
  files = createFakeFiles()
  renderer.mockReset()
})
afterEach(() => vi.restoreAllMocks())

function show() {
  return render(<ArtifactViewer sessionId="s1" files={files} artifact={artifact} onClose={vi.fn()} />)
}

describe('ArtifactViewer: chart/diagram downloads', () => {
  it('enables Download SVG/PNG only once the diagram has drawn, with Download labeled as source', async () => {
    renderer.mockResolvedValue({
      svg: '<svg xmlns="http://www.w3.org/2000/svg" width="80" height="40"><rect width="80" height="40"/></svg>',
      width: 80, height: 40, dispose: vi.fn(),
    })
    files.serve('flowchart LR; A-->B')
    show()
    expect((await screen.findByRole('button', { name: 'Download SVG' }) as HTMLButtonElement).disabled).toBe(true)
    expect(screen.getByRole('button', { name: 'Print' })).toBeDisabled()
    await waitFor(() => expect((screen.getByRole('button', { name: 'Download SVG' }) as HTMLButtonElement).disabled).toBe(false))
    expect((screen.getByRole('button', { name: 'Download PNG' }) as HTMLButtonElement).disabled).toBe(false)
    expect(screen.getByRole('button', { name: 'Print' })).toBeEnabled()
    expect(screen.getByRole('button', { name: 'Download source' })).toBeInTheDocument()
  })

  it('downloads the drawn SVG unmodified through the shared saveBlob path', async () => {
    renderer.mockResolvedValue({
      svg: '<svg xmlns="http://www.w3.org/2000/svg" width="80" height="40"><rect width="80" height="40"/></svg>',
      width: 80, height: 40, dispose: vi.fn(),
    })
    files.serve('flowchart LR; A-->B')
    show()
    await waitFor(() => expect((screen.getByRole('button', { name: 'Download SVG' }) as HTMLButtonElement).disabled).toBe(false))
    let saved: Blob | null = null
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
    vi.spyOn(URL, 'createObjectURL').mockImplementation((b: Blob | MediaSource) => { saved = b as Blob; return 'blob:svg' })
    vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => {})
    fireEvent.click(screen.getByRole('button', { name: 'Download SVG' }))
    expect(click).toHaveBeenCalledOnce()
    expect(saved!.type).toBe('image/svg+xml')
    expect(await saved!.text()).toContain('<rect')
  })

  it('never offers SVG/PNG for a diagram the renderer refused, but still offers the source', async () => {
    renderer.mockRejectedValue(new Error('line 2: HTML not allowed'))
    files.serve('flowchart LR; A-->B')
    show()
    await screen.findByText(/Couldn.t draw this diagram/)
    expect((screen.getByRole('button', { name: 'Download SVG' }) as HTMLButtonElement).disabled).toBe(true)
    expect((screen.getByRole('button', { name: 'Download PNG' }) as HTMLButtonElement).disabled).toBe(true)
    expect(screen.getByRole('button', { name: 'Download source' })).toBeInTheDocument()
  })

  it('reports a PNG export failure without touching the SVG download or the drawing', async () => {
    renderer.mockResolvedValue({
      svg: '<svg xmlns="http://www.w3.org/2000/svg" width="80" height="40"><rect width="80" height="40"/></svg>',
      width: 80, height: 40, dispose: vi.fn(),
    })
    files.serve('flowchart LR; A-->B')
    show()
    await waitFor(() => expect((screen.getByRole('button', { name: 'Download PNG' }) as HTMLButtonElement).disabled).toBe(false))
    // jsdom has no real canvas 2D context: exportPng throws, which the viewer must report inline,
    // not let escape past its own render boundary.
    fireEvent.click(screen.getByRole('button', { name: 'Download PNG' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Could not export the PNG.')
    expect((screen.getByRole('button', { name: 'Download SVG' }) as HTMLButtonElement).disabled).toBe(false)
  })

  it('is absent for a non-visual file, which keeps the plain Download label', async () => {
    files.serve('plain notes')
    render(<ArtifactViewer sessionId="s1" files={files} artifact={{ id: 't1', name: 'notes.txt', size: 11, content_type: 'text/plain', view: 'text' }} onClose={vi.fn()} />)
    await screen.findByRole('button', { name: 'Download' })
    expect(screen.queryByRole('button', { name: 'Download SVG' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Download PNG' })).toBeNull()
  })

  // A published file's tab popping itself out would freeze a copy that stops following versions
  // (M1): under a real host -- the actual shape a file tab is shown in (HostedViewers) -- this
  // tab's own VisualBlock must still offer no Tab button.
  it('offers no Tab button for its own VisualBlock, even under a real tab host', async () => {
    renderer.mockResolvedValue({
      svg: '<svg xmlns="http://www.w3.org/2000/svg" width="80" height="40"><rect width="80" height="40"/></svg>',
      width: 80, height: 40, dispose: vi.fn(),
    })
    const fakeHost: ViewerHostValue = { open: vi.fn(), close: vi.fn(), keep: vi.fn(), views: [], active: 'chat', target: null }
    files.serve('flowchart LR; A-->B')
    render(<ViewerHostContext.Provider value={fakeHost}><ArtifactViewer sessionId="s1" files={files} artifact={artifact} onClose={vi.fn()} embedded /></ViewerHostContext.Provider>)
    await waitFor(() => expect((screen.getByRole('button', { name: 'Download SVG' }) as HTMLButtonElement).disabled).toBe(false))
    expect(screen.queryByRole('button', { name: 'Tab' })).toBeNull()
  })
})

it('explicitly blocks MCP dashboard payload exports while source stays reference-only', async () => {
  const source=JSON.stringify({format:'blerg-dashboard',version:1,title:'Simulated private quotes',cards:[{id:'quote',kind:'stat',title:'Quote',span:1,data:{kind:'dataset',name:'blerg.mcp',parameters:{binding:'00000000-0000-4000-8000-000000000514'}},view:{value:'value',selection:'latest'}}]})
  files.serve(source)
  const dashboard={...artifact,id:'mcp-dash',name:'quotes.dash.json',content_type:'application/json',view:'json' as const,size:source.length}
  const view=render(<ArtifactViewer sessionId="s1" files={files} artifact={dashboard} onClose={vi.fn()} />)
  await screen.findByRole('button',{name:'Download source'})
  expect(await screen.findByText('Dashboard data exports are disabled. Download source contains binding references only for MCP cards.')).toBeInTheDocument()
  expect(screen.queryByRole('button',{name:'Download SVG'})).toBeNull()
  expect(screen.queryByRole('button',{name:'Download PNG'})).toBeNull()
  const saved: Blob[] = []
  vi.spyOn(URL, 'createObjectURL').mockImplementation(blob => { saved.push(blob as Blob); return 'blob:test-export' })
  vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
  fireEvent.click(screen.getByRole('button',{name:'Download source'}))
  await waitFor(() => expect(saved).toHaveLength(1))
  expect(await saved[0].text()).toBe(source)
  expect(files.downloads).toEqual([])
  expect(screen.getByRole('button', { name: 'Print' })).toBeDisabled()
  expect(source).not.toContain('185.25')
  expect(view.container.querySelectorAll('canvas,img')).toHaveLength(0)
})
