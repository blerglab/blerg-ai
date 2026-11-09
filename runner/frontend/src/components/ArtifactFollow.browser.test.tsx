import { browserWidths } from '../test/browserWidths'
import { describe, expect, it } from 'vitest'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { page } from 'vitest/browser'
import { ArtifactViewer, WorkspaceFileViewer, HostedViewers, ViewerHostContext, type ViewerHostValue, type ArtifactInfo, type WorkspaceFile, type TransportFiles } from '@blerglab/chat'
import '@blerglab/chat/blerg.css'
import '../index.css'

describe('live artifact preview controls', () => {
  for (const width of browserWidths([390, 3840])) it(`watches a workspace preview at ${width}px without publishing`, async () => {
    await page.viewport(width, 900)
    let revision = 1
    const files: TransportFiles = {
      list: async () => [], raw: async () => { throw new Error('must not read an artifact') },
      workspaceFile: async () => {
        const text = `<p>workspace revision ${revision}</p>`
        return { path: 'viz/index.html', view: 'html', content_type: 'text/html', size: text.length, mtime: String(revision), blob: new Blob([text]) } as WorkspaceFile
      },
      download: async () => {}, remove: async () => {}, upload: async () => { throw new Error('must not publish') },
    }
    const view = render(<div className="chat-root" style={{ width: '100%', height: 800, display: 'flex', flexDirection: 'column' }}><WorkspaceFileViewer sessionId={`watch-${width}`} files={files} path="viz/index.html" embedded onClose={() => {}} /></div>)
    const frame = () => view.container.querySelector('iframe')!
    await waitFor(() => expect(frame()?.srcdoc).toContain('workspace revision 1'))
    revision = 2
    const button = screen.getByRole('button', { name: 'Watch file' })
    expect(button.getBoundingClientRect().right).toBeLessThanOrEqual(width)
    fireEvent.click(button)
    await waitFor(() => expect(frame()?.srcdoc).toContain('workspace revision 2'))
    revision = 3
    await waitFor(() => expect(frame()?.srcdoc).toContain('workspace revision 3'), { timeout: 4000 })
    expect(frame().getAttribute('sandbox')).toBe('allow-scripts')
    expect(frame().srcdoc).toContain("default-src 'none'")
    expect(view.container.scrollWidth).toBeLessThanOrEqual(width)
    fireEvent.click(screen.getByRole('button', { name: 'Stop watching' }))
  })
  for (const width of browserWidths([390, 3840])) it(`follows and pins without overflowing at ${width}px`, async () => {
    await page.viewport(width, 900)
    const first: ArtifactInfo = { id: `first-${width}`, name: 'viz.html', view: 'html', content_type: 'text/html', size: 20, version: 1 }
    const second = { ...first, id: `second-${width}`, version: 2 }
    const files: TransportFiles = {
      list: async () => [],
      raw: async (_session, id) => new Blob([`<p>${id}</p>`]),
      download: async () => {}, remove: async () => {},
      upload: async () => { throw new Error('unused') },
    }
    const props = { sessionId: `live-${width}`, files, artifact: first, latest: second, embedded: true, onClose: () => {} }
    const view = render(<div className="chat-root" style={{ width: '100%', height: 800, display: 'flex', flexDirection: 'column' }}><ArtifactViewer {...props} /></div>)
    const frame = () => view.container.querySelector('iframe')!
    await waitFor(() => expect(frame()?.srcdoc).toContain(first.id))
    await waitFor(() => expect(frame()?.srcdoc).toContain(second.id))
    expect(frame().getAttribute('sandbox')).toBe('allow-scripts')
    expect(frame().srcdoc).toContain("default-src 'none'")
    const pin = screen.getByRole('button', { name: 'Pin version' })
    const bounds = pin.getBoundingClientRect()
    expect(bounds.left).toBeGreaterThanOrEqual(0)
    expect(bounds.right).toBeLessThanOrEqual(width)
    expect(bounds.width).toBe(30)
    expect(view.container.scrollWidth).toBeLessThanOrEqual(width)
    fireEvent.click(pin)
    expect(screen.getByRole('button', { name: 'Follow latest' })).toHaveAttribute('aria-pressed', 'false')
  })
  it('keeps following through split-pane visibility and focus changes', async () => {
    const first: ArtifactInfo = { id: 'split-first', name: 'split.html', view: 'html', content_type: 'text/html', size: 20, version: 1 }
    const target = document.createElement('div')
    const other = document.createElement('div')
    document.body.append(target, other)
    const host: ViewerHostValue = {
      active: 'preview', views: [{ id: 'preview', view: { kind: 'artifact', artifact: first } }],
      visible: ['preview', 'chat'], target: null, targetFor: () => target, autoFocus: false,
      open: () => { throw new Error('following must keep the existing tab') }, close: () => {}, keep: () => {},
    }
    const files: TransportFiles = {
      list: async () => [], raw: async (_session, id) => new Blob([`<p>${id}</p>`]),
      download: async () => {}, remove: async () => {}, upload: async () => { throw new Error('unused') },
    }
    const draw = (visible: string[], version: number) =>
      <ViewerHostContext.Provider value={{ ...host, visible }}>
        <HostedViewers sessionId="split-live" files={files} artifacts={[first, { ...first, id: `split-v${version}`, version }]} />
      </ViewerHostContext.Provider>
    const view = render(draw(['preview', 'chat'], 2))
    try {
      await waitFor(() => expect(target.querySelector('iframe')?.srcdoc).toContain(first.id))
      await waitFor(() => expect(target.querySelector('iframe')?.srcdoc).toContain('split-v2'))
      // The same target moves into a focused pane, then becomes a hidden tab.
      other.append(target)
      view.rerender(draw(['chat'], 3))
      await waitFor(() => expect(target.querySelector('iframe')?.srcdoc).toContain('split-v3'))
      view.rerender(draw(['preview'], 3))
      expect(within(target).getByRole('button', { name: 'Pin version' })).toBeTruthy()
      expect(within(target).getByRole('button', { name: 'Pin version' })).toHaveAttribute('aria-pressed', 'true')
    } finally { view.unmount(); target.remove(); other.remove() }
  })

})
