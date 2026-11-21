// VisualViewer (BLG-451): the tab an inline chart/diagram pops out into -- the same VisualBlock
// rendering as the chat block itself, plus Download SVG/PNG/source in its own toolbar.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, within } from '@testing-library/react'
import VisualViewer from './VisualViewer'
import { HostedViewers, ViewerHostContext, type ViewerHostValue } from './ViewerHost'
import { createFakeFiles } from '../test/fakeTransport'
import { renderVisual } from '../visual/render'

vi.mock('../visual/render', () => ({ renderVisual: vi.fn() }))
const renderer = vi.mocked(renderVisual)
afterEach(() => { renderer.mockReset() })

function host(over: Partial<ViewerHostValue> = {}): ViewerHostValue {
  return { open: vi.fn(), close: vi.fn(), keep: vi.fn(), views: [], active: 'chat', target: null, ...over }
}
function drawn(dispose: () => void) {
  return { svg: '<svg xmlns="http://www.w3.org/2000/svg" width="80" height="40"/>', width: 80, height: 40, dispose }
}

describe('VisualViewer', () => {
  it('draws the snapshot through VisualBlock and enables downloads once drawn', async () => {
    renderer.mockImplementation(async (_kind, _source, hostEl) => { hostEl.textContent = 'drawn'; return drawn(() => {}) })
    render(<VisualViewer visualKind="mermaid" source="flowchart LR; A-->B" label="Diagram" onClose={() => {}} embedded />)
    await screen.findByText('drawn')
    expect((screen.getByRole('button', { name: 'Download SVG' }) as HTMLButtonElement).disabled).toBe(false)
    expect((screen.getByRole('button', { name: 'Download PNG' }) as HTMLButtonElement).disabled).toBe(false)
    expect(screen.getByRole('button', { name: 'Download source' })).toBeInTheDocument()
  })

  // A tab popping out its own snapshot would reopen itself (M1): under a real host -- not just
  // the absent one every other test here renders without -- this tab's own VisualBlock must still
  // offer no Tab button.
  it('offers no Tab button for its own VisualBlock, even under a real tab host', async () => {
    renderer.mockImplementation(async (_kind, _source, hostEl) => { hostEl.textContent = 'drawn'; return drawn(() => {}) })
    render(
      <ViewerHostContext.Provider value={host()}>
        <VisualViewer visualKind="mermaid" source="flowchart LR; A-->B" label="Diagram" onClose={() => {}} embedded />
      </ViewerHostContext.Provider>,
    )
    await screen.findByText('drawn')
    expect(screen.queryByRole('button', { name: 'Tab' })).toBeNull()
  })

  it('disables Download SVG/PNG, but still offers Download source, before anything has drawn', () => {
    renderer.mockImplementation(() => new Promise(() => {}))
    render(<VisualViewer visualKind="vega-lite" source="{}" label="Chart" onClose={() => {}} embedded />)
    expect((screen.getByRole('button', { name: 'Download SVG' }) as HTMLButtonElement).disabled).toBe(true)
    expect((screen.getByRole('button', { name: 'Download PNG' }) as HTMLButtonElement).disabled).toBe(true)
    expect(screen.getByRole('button', { name: 'Download source' })).toBeInTheDocument()
  })

  it('names the tab from the given label, with a Close action when not embedded', () => {
    renderer.mockImplementation(() => new Promise(() => {}))
    const onClose = vi.fn()
    render(<VisualViewer visualKind="mermaid" source="flowchart LR; A-->B" label="Rollout flow" onClose={onClose} />)
    expect(screen.getByRole('heading', { name: 'Rollout flow' })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Close' }))
    expect(onClose).toHaveBeenCalledOnce()
  })

  it('says the over-cap snapshot will not survive a reload, matching the message tab\'s own wording shape', () => {
    renderer.mockImplementation(() => new Promise(() => {}))
    render(<VisualViewer visualKind="mermaid" source="flowchart LR; A-->B" label="Diagram" onClose={() => {}} embedded kept={false} />)
    expect(screen.getByTestId('visual-not-kept')).toHaveTextContent('This diagram was too large to keep — it won’t be here after a reload.')
  })

  it('is what HostedViewers renders for a visual tab, with no file read involved', async () => {
    renderer.mockImplementation(async (_kind, _source, hostEl) => { hostEl.textContent = 'drawn diagram'; return drawn(() => {}) })
    const files = createFakeFiles()
    const target = document.createElement('div')
    document.body.appendChild(target)
    const value = host({ target, active: 'visual:v1', views: [{ id: 'visual:v1', view: { kind: 'visual', id: 'v1', visualKind: 'mermaid', source: 'flowchart LR; A-->B', label: 'Snapshot' } }] })
    render(<ViewerHostContext.Provider value={value}><HostedViewers sessionId="s1" files={files} /></ViewerHostContext.Provider>)
    try {
      await screen.findByText('drawn diagram')
      // A tab's body: a region inside the tab, not a modal dialog over the workspace.
      expect(within(screen.getByTestId('visual-viewer')).getAllByRole('region').length).toBeGreaterThan(0)
      expect(screen.queryByRole('dialog')).toBeNull()
      expect(files.rawCalls).toEqual([])
    } finally { target.remove() }
  })
})
