import { afterEach, describe, expect, it } from 'vitest'
import { render, waitFor } from '@testing-library/react'
import { page } from 'vitest/browser'
import { ArtifactViewer, ChatContext, Markdown, type ChatContextValue, type TransportFiles } from '@blerglab/chat'
import fixtures from '../test/mermaid/cases.json'
import '@blerglab/chat/blerg.css'
import '../index.css'

const samples = fixtures.filter(f => ['readme-6-gitGraph', 'readme-8-journey', 'eventmodeling-data'].includes(f.id))
const svgOf = (image: HTMLImageElement) => decodeURIComponent(image.src.split(',')[1])
async function images(container: HTMLElement, count: number) {
  await waitFor(() => {
    expect(container.querySelector('[data-testid="visual-failed"]'), container.textContent ?? '').toBeNull()
    expect(container.querySelectorAll('img.visual-diagram')).toHaveLength(count)
  }, { timeout: 30_000 })
  return [...container.querySelectorAll<HTMLImageElement>('img.visual-diagram')]
}

afterEach(async () => { await page.viewport(1280, 800) })

describe('corpus regressions through chat and file viewers', () => {
  it('renders git, journey and event modeling in Markdown fences', async () => {
    const { container } = render(<Markdown text={samples.map(f => `\`\`\`mermaid\n${f.source}\n\`\`\``).join('\n\n')} />)
    const shown = await images(container, samples.length)
    for (const image of shown) {
      expect(image.naturalWidth).toBeGreaterThan(0)
      expect(svgOf(image)).not.toMatch(/<foreignObject|class="error-text"/)
    }
  }, 40_000)

  for (const width of [390, 3840]) {
    it.each(samples)(`shows all of $id in a .mmd viewer at ${width}px`, async sample => {
      await page.viewport(width, width === 390 ? 844 : 2160)
      const files: TransportFiles = {
        list: async () => [], raw: async () => new Blob([sample.source], { type: 'text/plain' }),
        download: async () => {}, remove: async () => {}, upload: async () => { throw new Error('unused') },
      }
      const context: ChatContextValue = {
        sessionId: 'mermaid-corpus', files, view: () => {}, latestVersion: () => undefined,
        byName: () => undefined, reviewFor: async () => null, submitFeedback: async () => true,
      }
      const artifact = { id: sample.id, name: 'generic.mmd', size: sample.source.length, content_type: 'text/plain', view: 'text' }
      const { container } = render(<ChatContext.Provider value={context}>
        <ArtifactViewer sessionId="mermaid-corpus" files={files} artifact={artifact} onClose={() => {}} />
      </ChatContext.Provider>)
      // The viewer renders into a portal.
      const [image] = await images(document.body, 1)
      expect(container.querySelector('[data-testid="visual-failed"]')).toBeNull()
      await waitFor(() => {
        const box = image.getBoundingClientRect()
        const body = document.querySelector('.artifact-viewer .artifact-body')!.getBoundingClientRect()
        expect(box.width).toBeGreaterThan(0)
        expect(box.height).toBeGreaterThan(0)
        expect(box.left).toBeGreaterThanOrEqual(body.left - 1)
        expect(box.right).toBeLessThanOrEqual(body.right + 1)
        expect(box.top).toBeGreaterThanOrEqual(body.top - 1)
        expect(box.bottom).toBeLessThanOrEqual(body.bottom + 1)
        expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
      })
      expect(svgOf(image)).not.toContain('foreignObject')
    }, 40_000)
  }
})
