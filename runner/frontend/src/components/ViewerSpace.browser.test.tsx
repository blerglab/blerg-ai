// The artifact viewer uses the screen it is given (0.4.4): review and diff take about 96% × 92% of
// the viewport with no pixel cap, the review's panes split the width (the source with room for its
// lines, the page at GitHub's article width, the comments at a fixed width), maximize goes edge to
// edge, and a markdown document reads in GitHub's box (1012px of text, a 1px border, 32px of padding;
// 0.4.5) centered in the wider viewer. Measured in real Chromium on a 4K display, a 1080p one and a phone.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { render, waitFor } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import { ArtifactViewer, ChatContext, TicketModal, localStorageChatStorage, type ArtifactInfo, type ChatContextValue, type TransportFiles } from '@blerglab/chat'
import '@blerglab/chat/blerg.css'
import '../index.css'

const LONG = 'A source line that runs well past any reading measure, the way a table row or a long link does: ' + 'x'.repeat(100)
// Source lines of about 105 characters (one long paragraph on the page), then one of about 200.
const MARKDOWN = `# Viewer space\n\n${'A paragraph long enough to wrap at any reading measure, so the page shows how wide it lets its prose run.\n'.repeat(6)}\n${LONG}\n`
const PATCH = 'diff --git a/src/main.ts b/src/main.ts\n--- a/src/main.ts\n+++ b/src/main.ts\n@@ -1,2 +1,2 @@\n keep\n-old\n+new\n'

const md: ArtifactInfo = { id: 'doc', name: 'report.md', size: MARKDOWN.length, content_type: 'text/markdown', view: 'markdown' }
const diff: ArtifactInfo = { id: 'patch', name: 'change.patch', size: PATCH.length, content_type: 'text/x-diff', view: 'diff' }

const files = (body: string): TransportFiles => ({
  list: async () => [], raw: async () => new Blob([body]), download: async () => {}, remove: async () => {},
  upload: async () => { throw new Error('unused') },
})
const context: ChatContextValue = {
  sessionId: 'viewer-space', files: null, view: vi.fn(), latestVersion: () => undefined, byName: () => undefined,
  reviewFor: async () => null, submitFeedback: async () => true,
}

function open(artifact: ArtifactInfo, body: string, review = false) {
  return render(
    <ChatContext.Provider value={context}>
      <ArtifactViewer sessionId="viewer-space" files={files(body)} artifact={artifact} review={review} onClose={() => {}} />
    </ChatContext.Provider>,
  )
}

const rect = (selector: string) => {
  const el = document.querySelector<HTMLElement>(selector)
  if (!el) throw new Error(`missing ${selector}`)
  return el.getBoundingClientRect()
}
const frame = () => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)))

const SIZES = [[3840, 2160], [1920, 1080], [390, 844]] as const

afterEach(async () => {
  for (const kind of ['file', 'review', 'markup', 'diff', 'ticket']) localStorageChatStorage.set(`blerg.chat.viewer.maximized.${kind}`, null)
  localStorage.clear()
  await page.viewport(1280, 800)
})

describe('the viewer uses the screen', () => {
  it.each(SIZES)('review takes about 96 × 92 percent of a %i × %i viewport and splits its panes', async (width, height) => {
    await page.viewport(width, height)
    const { findByRole, getByRole } = open(md, MARKDOWN, true)
    await findByRole('textbox', { name: 'Document' })
    await userEvent.click(getByRole('button', { name: 'Source' }))
    await findByRole('textbox', { name: 'Source' })
    await frame()
    const dialog = rect('.artifact-viewer')
    if (width < 900) {
      // A phone keeps its full-screen sheet and the stacked panes.
      expect(dialog.width).toBeCloseTo(width, 0)
      expect(rect('.review-source').width).toBeLessThanOrEqual(width)
      expect(rect('.review-source').height).toBeCloseTo(height * 0.35, 0)
      expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(innerWidth)
      return
    }
    expect(dialog.width).toBeGreaterThan(width * 0.95)
    expect(dialog.width).toBeLessThanOrEqual(width * 0.97)
    expect(dialog.height).toBeGreaterThan(height * 0.91)
    expect(dialog.height).toBeLessThanOrEqual(height * 0.93)

    const source = rect('.review-source')
    const column = rect('.review-page-col')
    const margin = rect('.document-margin')
    // The comments keep a fixed comfortable width, right beside the page.
    expect(margin.width).toBeCloseTo(300, 0)
    const pageRect = rect('.review-page')
    expect(margin.left - pageRect.right).toBeGreaterThanOrEqual(0)
    expect(margin.left - pageRect.right).toBeLessThan(48)
    // The page reads at GitHub's article width.
    expect(pageRect.width).toBeLessThanOrEqual(1078 + 1) // GitHub's box
    expect(pageRect.width).toBeGreaterThan(600)
    // The source takes the left, about half; the page and its comments are centered as one group in
    // the rest.
    const panes = rect('.review-panes')
    expect(source.left - panes.left).toBeLessThan(2)
    expect(source.width).toBeGreaterThan(panes.width * 0.4)
    // (The rest starts after the source's 12px gutter.)
    if (width >= 3840) expect(Math.abs((column.left - source.right - 12) - (panes.right - margin.right))).toBeLessThan(4)
    // The source fills the pane's height and scrolls its long lines on its own.
    const textarea = document.querySelector<HTMLTextAreaElement>('.review-textarea')!
    expect(source.height).toBeGreaterThan(rect('.review-panes').height - 4)
    if (width >= 3840) expect(textarea.scrollWidth).toBeLessThanOrEqual(textarea.clientWidth) // the whole line fits
    else expect(textarea.scrollWidth).toBeGreaterThan(textarea.clientWidth) // its own horizontal scroll
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(innerWidth)
  })

  it.each(SIZES)('a diff takes about 96 percent of a %i × %i viewport', async (width, height) => {
    await page.viewport(width, height)
    open(diff, PATCH)
    await waitFor(() => expect(document.querySelector('.diff-main')).not.toBeNull())
    await frame()
    const dialog = rect('.artifact-viewer')
    if (width < 900) {
      expect(dialog.width).toBeCloseTo(width, 0)
    } else {
      expect(dialog.width).toBeGreaterThan(width * 0.95)
      expect(dialog.width).toBeLessThanOrEqual(width * 0.97)
      expect(dialog.height).toBeGreaterThan(height * 0.91)
      expect(dialog.height).toBeLessThanOrEqual(height * 0.93)
    }
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(innerWidth)
  })

  it.each(SIZES)('maximize goes edge to edge on a %i × %i viewport and restores', async (width, height) => {
    await page.viewport(width, height)
    const { findByRole, getByRole } = open(md, MARKDOWN, true)
    await findByRole('textbox', { name: 'Document' })
    const before = rect('.artifact-viewer')
    await userEvent.click(getByRole('button', { name: 'Maximize' }))
    await frame()
    const max = rect('.artifact-viewer')
    expect(max.left).toBeCloseTo(0, 0)
    expect(max.top).toBeCloseTo(0, 0)
    expect(max.width).toBeCloseTo(width, 0)
    expect(max.height).toBeCloseTo(height, 0)
    // Restore, by the keyboard this time.
    await userEvent.keyboard('{Control>}{Alt>}f{/Alt}{/Control}')
    await frame()
    const back = rect('.artifact-viewer')
    expect(back.width).toBeCloseTo(before.width, 0)
    expect(back.height).toBeCloseTo(before.height, 0)
  })

  it.each(SIZES)('the plain viewer keeps its size and a markdown file reads in GitHub’s box on a %i × %i viewport', async (width, height) => {
    await page.viewport(width, height)
    open(md, MARKDOWN)
    await waitFor(() => expect(document.querySelector('.artifact-markdown')).not.toBeNull())
    await frame()
    const dialog = rect('.artifact-viewer')
    if (width < 900) {
      expect(dialog.width).toBeCloseTo(width, 0)
    } else {
      // The viewer keeps its usual size; the document is GitHub's box, centered in it.
      expect(dialog.width).toBeCloseTo(Math.min(1400, width * 0.94), 0)
      const body = rect('.artifact-body')
      const prose = rect('.artifact-markdown')
      expect(prose.width).toBeCloseTo(1078, 0) // GitHub's box, however wide the dialog
      expect(Math.abs((prose.left - body.left) - (body.right - prose.right))).toBeLessThan(2)
    }
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(innerWidth)
  })

  it.each([[390, 844], [1440, 900], [3840, 2160]] as const)('the ticket dialog uses a %i × %i viewport, and maximize takes all of it', async (width, height) => {
    await page.viewport(width, height)
    const { getByRole } = render(<TicketModal ticket={{ source: 'jira', key: 'ENG-1', title: 'A ticket', status: 'To Do', type: 'Bug', description: MARKDOWN }} onClose={() => {}} />)
    await frame()
    const dialog = rect('.ticket-modal')
    if (width < 900) expect(dialog.width).toBeCloseTo(width, 0)
    else {
      expect(dialog.width).toBeCloseTo(Math.min(1400, 0.94 * width), 0)
      expect(dialog.height).toBeLessThanOrEqual(height * 0.9 + 1)
    }
    // The text keeps a reading width, centered in the dialog.
    const main = rect('.ticket-modal .ticket-main')
    expect(main.width).toBeLessThanOrEqual(1100 + 1)
    expect(Math.abs((main.left - dialog.left) - (dialog.right - main.right))).toBeLessThanOrEqual(40)
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(innerWidth)
    await userEvent.click(getByRole('button', { name: 'Maximize' }))
    await frame()
    const max = rect('.ticket-modal')
    expect(max.left).toBeCloseTo(0, 0)
    expect(max.top).toBeCloseTo(0, 0)
    expect(max.width).toBeCloseTo(width, 0)
    expect(max.height).toBeCloseTo(height, 0)
    expect(localStorageChatStorage.get('blerg.chat.viewer.maximized.ticket')).toBe('1')
    await userEvent.click(getByRole('button', { name: 'Restore size' }))
    await frame()
    expect(rect('.ticket-modal').width).toBeCloseTo(dialog.width, 0)
    expect(localStorageChatStorage.get('blerg.chat.viewer.maximized.ticket')).toBeNull()
  })

  it('without the source, centers the page and its comments together on a 3840 × 2160 viewport', async () => {
    await page.viewport(3840, 2160)
    const { findByRole } = open(md, MARKDOWN, true)
    await findByRole('textbox', { name: 'Document' })
    await frame()
    const panes = rect('.review-panes')
    const pageRect = rect('.review-page')
    const margin = rect('.document-margin')
    expect(margin.width).toBeCloseTo(300, 0)
    expect(margin.left - pageRect.right).toBeGreaterThanOrEqual(0)
    expect(margin.left - pageRect.right).toBeLessThan(48)
    expect(pageRect.width).toBeLessThanOrEqual(1078 + 1) // GitHub's box
    expect(Math.abs((pageRect.left - panes.left) - (panes.right - margin.right))).toBeLessThan(4)
  })
})
