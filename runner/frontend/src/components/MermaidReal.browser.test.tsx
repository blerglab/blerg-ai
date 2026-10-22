// Board card #84: real Mermaid diagrams, as the owner publishes them, in real Chromium. Labels
// with the word "image", color styling, a forty-node service graph; a broken diagram says why;
// a published .mmd draws in the chat with a View link; a tall diagram is fully visible in the
// viewer at 390, 1920 and 3840 px.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { render, waitFor } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import { ArtifactCard, ArtifactViewer, ChatContext, VisualBlock, localStorageChatStorage, type ArtifactInfo, type ChatContextValue, type TransportFiles } from '@blerglab/chat'
import { BROKEN, CLASSDEF_AND_CLASS, IMAGE_WORD_LABEL, serviceGraph, tallDiagram } from '../../../../packages/chat/src/visual/mermaidSamples'
import '@blerglab/chat/blerg.css'
import '../index.css'

async function diagram(container: HTMLElement): Promise<HTMLImageElement> {
  await waitFor(() => {
    expect(container.querySelector('img.visual-diagram, [data-testid="visual-failed"]')).not.toBeNull()
  }, { timeout: 30_000 })
  const image = container.querySelector<HTMLImageElement>('img.visual-diagram')
  expect(image, container.textContent ?? '').not.toBeNull()
  await waitFor(() => expect(image!.naturalWidth).toBeGreaterThan(20))
  return image!
}
const svgOf = (image: HTMLImageElement) => decodeURIComponent(image.src.split(',')[1]!)
/** The diagram's words, as its SVG text runs (Mermaid sets each word in a tspan of its own). */
const textOf = (image: HTMLImageElement) => [...new DOMParser().parseFromString(svgOf(image), 'image/svg+xml').querySelectorAll('text')].map(t => t.textContent).join(' | ')
const frame = () => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)))

function files(text: string): TransportFiles {
  return {
    list: async () => [], raw: async () => new Blob([text], { type: 'text/plain' }), download: async () => {}, remove: async () => {},
    upload: async () => { throw new Error('unused') },
  }
}
function context(text: string, view = vi.fn()): ChatContextValue {
  return {
    sessionId: 'mermaid-real', files: files(text), view, latestVersion: () => undefined, byName: () => undefined,
    reviewFor: async () => null, submitFeedback: async () => true,
  }
}

afterEach(async () => {
  localStorageChatStorage.set('blerg.chat.viewer.maximized.file', null)
  await page.viewport(1280, 800)
})

describe('real Mermaid diagrams', () => {
  it('draws a label that contains the word "image"', async () => {
    const { container } = render(<VisualBlock kind="mermaid" source={IMAGE_WORD_LABEL} />)
    const image = await diagram(container)
    expect(textOf(image)).toContain('vm-image-controller')
  }, 40_000)

  it('draws classDef plus class with its colors', async () => {
    const { container } = render(<VisualBlock kind="mermaid" source={CLASSDEF_AND_CLASS} />)
    const svg = svgOf(await diagram(container)).toLowerCase()
    expect(svg).toContain('#e0f2fe')
    expect(svg).toContain('#fef3c7')
  }, 40_000)

  it('draws a forty-node service graph with several classDefs', async () => {
    const { container } = render(<VisualBlock kind="mermaid" source={serviceGraph()} />)
    const image = await diagram(container)
    const svg = svgOf(image)
    expect(textOf(image)).toContain('vm-image-controller')
    expect(textOf(image)).toContain('audit-log')
    expect(svg.toLowerCase()).toContain('#fee2e2')
    expect(svg.match(/class="node /g)?.length ?? 0).toBeGreaterThanOrEqual(40)
  }, 60_000)

  it('shows a broken diagram\'s source with Mermaid\'s reason', async () => {
    const { findByTestId } = render(<VisualBlock kind="mermaid" source={BROKEN} />)
    const failed = await findByTestId('visual-failed', {}, { timeout: 30_000 })
    expect(failed.textContent).toMatch(/Couldn’t draw this diagram: Mermaid: Parse error on line 3/)
    expect(failed.textContent).toContain('B --> --> C')
  }, 40_000)

  it('shows a refused diagram\'s source with the line and what is not allowed', async () => {
    const source = 'flowchart LR\n  A --> B\n  classDef x fill:url(#a)'
    const { findByTestId } = render(<VisualBlock kind="mermaid" source={source} />)
    const failed = await findByTestId('visual-failed', {}, { timeout: 30_000 })
    expect(failed.textContent).toContain('Couldn’t draw this diagram: line 3: url() not allowed')
    expect(failed.textContent).toContain('classDef x fill:url(#a)')
  }, 40_000)

  it('draws a published .mmd in the chat, with View', async () => {
    const source = serviceGraph()
    const view = vi.fn()
    const payload = { id: 'graph', name: 'services.mmd', size: source.length, content_type: 'text/plain', view: 'text' }
    const { container, getByRole } = render(
      <ChatContext.Provider value={context(source, view)}><ArtifactCard payload={payload} /></ChatContext.Provider>,
    )
    const image = await diagram(container.querySelector<HTMLElement>('[data-testid="artifact-preview-diagram"]')!)
    expect(textOf(image)).toContain('vm-image-controller')
    expect(container.querySelector('[data-testid="artifact-preview-excerpt"]')).toBeNull()
    await userEvent.click(getByRole('button', { name: 'View' }))
    expect(view).toHaveBeenCalledWith(payload)
  }, 60_000)

  it('shows a published .mmd that does not parse as its source and reason in the chat', async () => {
    const payload = { id: 'broken', name: 'broken.mmd', size: BROKEN.length, content_type: 'text/plain', view: 'text' }
    const { findByTestId } = render(<ChatContext.Provider value={context(BROKEN)}><ArtifactCard payload={payload} /></ChatContext.Provider>)
    const failed = await findByTestId('visual-failed', {}, { timeout: 30_000 })
    expect(failed.textContent).toMatch(/Parse error on line 3/)
  }, 40_000)

  it.each([[1920, 1080], [3840, 2160], [390, 844]] as const)('shows all of a tall diagram in the viewer on a %i × %i viewport', async (width, height) => {
    await page.viewport(width, height)
    const source = tallDiagram()
    const artifact: ArtifactInfo = { id: 'tall', name: 'tall.mmd', size: source.length, content_type: 'text/plain', view: 'text' }
    render(
      <ChatContext.Provider value={context(source)}>
        <ArtifactViewer sessionId="mermaid-real" files={files(source)} artifact={artifact} onClose={() => {}} />
      </ChatContext.Provider>,
    )
    const image = await diagram(document.body)
    await frame()
    const body = document.querySelector<HTMLElement>('.artifact-viewer .artifact-body')!
    // Scaled to fit: the whole diagram is inside the dialog's body without scrolling anything,
    // and it uses the room (it is not left in a small box).
    const box = image.getBoundingClientRect()
    const shown = body.getBoundingClientRect()
    expect(box.top).toBeGreaterThanOrEqual(shown.top - 1)
    expect(box.bottom).toBeLessThanOrEqual(shown.bottom + 1)
    expect(box.height).toBeGreaterThan(shown.height * 0.6)
    for (let el: HTMLElement | null = image.parentElement; el && el !== body.parentElement; el = el.parentElement) expect(el.scrollTop).toBe(0)
    expect(box.width).toBeLessThanOrEqual(shown.width)
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(innerWidth)
  }, 60_000)
})
