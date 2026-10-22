// Board card #84: a published .mmd shows as the diagram in the chat, not as its text.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import ArtifactCard from './ArtifactCard'
import { ChatContext, type ChatContextValue } from './context'
import { clearPreviewCache } from '../model/artifactPreview'
import { createFakeFiles, type FakeFiles } from '../test/fakeTransport'
import { renderVisual } from '../visual/render'

vi.mock('../visual/render', () => ({ renderVisual: vi.fn() }))
const renderer = vi.mocked(renderVisual)

let files: FakeFiles
let chat: ChatContextValue
const SOURCE = 'flowchart LR\n  ctl[vm-image-controller] --> reg[(image registry)]\n'

beforeEach(() => {
  clearPreviewCache()
  renderer.mockReset().mockImplementation(async (_kind, source, host) => {
    host.textContent = `drawn: ${source}`
    return () => host.replaceChildren()
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
    expect(renderer).toHaveBeenCalledWith('mermaid', SOURCE.trimEnd(), expect.any(HTMLElement), expect.anything())
    expect(screen.queryByTestId('artifact-preview-excerpt')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'View' }))
    expect(chat.view).toHaveBeenCalledWith(payload)
  })

  it('shows the source and the reason when the diagram cannot be drawn', async () => {
    renderer.mockRejectedValue(new Error('line 2: HTML not allowed'))
    files.serve(SOURCE)
    render(<ChatContext.Provider value={chat}><ArtifactCard payload={{ id: 'm2', name: 'bad.mermaid', size: SOURCE.length, content_type: 'text/plain', view: 'text' }} /></ChatContext.Provider>)
    expect(await screen.findByRole('status')).toHaveTextContent('Couldn’t draw this diagram: line 2: HTML not allowed')
    expect(screen.getByTestId('visual-failed')).toHaveTextContent('vm-image-controller')
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
      await new Promise(resolve => setTimeout(resolve, 50))
      expect(raw).not.toHaveBeenCalled()
      expect(renderer).not.toHaveBeenCalled()
      act(() => fire!())
      await waitFor(() => expect(renderer).toHaveBeenCalledTimes(1))
    } finally { vi.unstubAllGlobals() }
  })
})
