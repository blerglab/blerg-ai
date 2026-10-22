import { afterEach, describe, expect, it } from 'vitest'
import { render, waitFor } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import { ArtifactViewer, ChatContext, Markdown, ReviewMode, type ChatContextValue, type ReviewFile, type TransportFiles } from '@blerglab/chat'
import '../index.css'
import '@blerglab/chat/blerg.css'

// Rendered Markdown follows GitHub's README styling (github-markdown-css): the values below are
// GitHub's, measured in a real browser inside the app's own stylesheet (index.css styles bare
// h1/h2/code globally, which must not leak into a document).
const DOC = [
  'Intro paragraph with `inline code` and a [link](https://example.com).',
  '',
  '# Heading one',
  '',
  'A paragraph under the first heading.',
  '',
  '## Heading two',
  '',
  '- first item',
  '- second item',
  '- [x] a done task',
  '',
  '### Heading three',
  '',
  '```js',
  'const answer = 42',
  '```',
  '',
  '| Name | Value |',
  '| ---- | ----- |',
  '| a    | 1     |',
  '| b    | 2     |',
  '| c    | 3     |',
  '',
  '> A quoted passage.',
  '',
  '---',
  '',
  'The closing paragraph.',
].join('\n')

const files: TransportFiles = {
  list: async () => [],
  raw: async () => new Blob([DOC], { type: 'text/markdown' }),
  download: async () => {},
  remove: async () => {},
  upload: async () => { throw new Error('not in this test') },
}

const shotsDir = (import.meta as unknown as { env: Record<string, string | undefined> }).env.VITE_MD_SHOTS_DIR

async function openViewer() {
  localStorage.clear()
  const view = render(
    <ArtifactViewer
      sessionId="md-github"
      files={files}
      artifact={{ id: 'md1', name: 'README.md', size: DOC.length, content_type: 'text/markdown', view: 'markdown' }}
      onClose={() => {}}
    />,
  )
  await waitFor(() => expect(document.querySelector('.artifact-markdown h3')).not.toBeNull())
  const doc = document.querySelector<HTMLElement>('.artifact-markdown')!
  return { view, doc }
}

const px = (v: string) => parseFloat(v)
const css = (el: Element) => getComputedStyle(el)

afterEach(async () => {
  localStorage.clear()
  delete document.documentElement.dataset.theme
  await page.viewport(1280, 900)
})

describe('Markdown follows GitHub’s styling (real browser)', () => {
  it('gives the viewer GitHub’s type, rhythm and blocks', async () => {
    await page.viewport(1280, 900)
    const { doc } = await openViewer()
    const q = (sel: string) => doc.querySelector(sel)!

    const base = css(doc)
    expect(base.fontSize).toBe('16px')
    expect(px(base.lineHeight)).toBeCloseTo(24, 1)
    expect(base.fontFamily).toMatch(/^-apple-system, BlinkMacSystemFont, "Segoe UI", "Noto Sans", Helvetica, Arial, sans-serif/)

    const p = css(q('h1 + p'))
    expect(p.marginTop).toBe('0px')
    expect(px(p.marginBottom)).toBeCloseTo(16, 1)

    const sizes: Record<string, number> = { h1: 32, h2: 24, h3: 20 }
    for (const [tag, size] of Object.entries(sizes)) {
      const h = css(q(tag))
      expect(px(h.fontSize), tag).toBeCloseTo(size, 1)
      expect(px(h.marginTop), `${tag} margin-top`).toBeCloseTo(24, 1)
      expect(px(h.marginBottom), `${tag} margin-bottom`).toBeCloseTo(16, 1)
      expect(h.fontWeight, tag).toBe('600')
      expect(px(h.lineHeight), tag).toBeCloseTo(size * 1.25, 1)
      expect(h.letterSpacing, tag).toBe('normal') // the app's display heading style does not leak in
      expect(h.fontFamily, tag).toBe(base.fontFamily)
    }
    for (const tag of ['h1', 'h2']) {
      const h = css(q(tag))
      expect(px(h.paddingBottom), tag).toBeCloseTo(px(h.fontSize) * 0.3, 1)
      expect(h.borderBottomWidth, tag).toBe('1px')
      expect(h.borderBottomStyle, tag).toBe('solid')
    }
    expect(css(q('h3')).borderBottomStyle).toBe('none')

    expect(px(css(q('li + li')).marginTop)).toBeCloseTo(4, 1)
    expect(px(css(q('ul')).paddingLeft)).toBeCloseTo(32, 1)
    expect(px(css(q('ul')).marginBottom)).toBeCloseTo(16, 1)
    expect(css(q('li.task-list-item')).listStyleType).toBe('none')

    const code = css(q('p > code'))
    expect(px(code.fontSize)).toBeCloseTo(13.6, 1)
    expect(px(code.paddingTop)).toBeCloseTo(13.6 * 0.2, 1)
    expect(px(code.paddingLeft)).toBeCloseTo(13.6 * 0.4, 1)
    expect(code.borderTopLeftRadius).toBe('6px')
    expect(code.display).toBe('inline') // not the app's inline-flex box, which loosens the line
    expect(code.backgroundColor).not.toBe('rgba(0, 0, 0, 0)')

    const pre = css(q('pre'))
    expect(px(pre.paddingTop)).toBeCloseTo(16, 1)
    expect(px(pre.paddingLeft)).toBeCloseTo(16, 1)
    expect(px(pre.fontSize)).toBeCloseTo(13.6, 1)
    expect(px(pre.lineHeight)).toBeCloseTo(13.6 * 1.45, 1)
    expect(pre.borderTopLeftRadius).toBe('6px')
    expect(px(css(q('.md-codeblock')).marginBottom)).toBeCloseTo(16, 1)

    const table = css(q('table'))
    expect(table.borderCollapse).toBe('collapse')
    const td = css(q('td'))
    expect(px(td.paddingTop)).toBeCloseTo(6, 1)
    expect(px(td.paddingLeft)).toBeCloseTo(13, 1)
    expect(td.borderTopWidth).toBe('1px')
    const rows = doc.querySelectorAll('tbody tr')
    expect(css(rows[1]).backgroundColor).not.toBe(css(rows[0]).backgroundColor)
    expect(px(css(q('.md-table-wrap')).marginBottom)).toBeCloseTo(16, 1)

    const quote = css(q('blockquote'))
    expect(px(quote.paddingLeft)).toBeCloseTo(16, 1)
    expect(px(quote.paddingRight)).toBeCloseTo(16, 1)
    expect(px(quote.borderLeftWidth)).toBeCloseTo(4, 1)
    expect(quote.color).not.toBe(base.color)
    expect(px(quote.marginBottom)).toBeCloseTo(16, 1)

    const hr = css(q('hr'))
    expect(px(hr.height)).toBeCloseTo(4, 1)
    expect(px(hr.marginTop)).toBeCloseTo(24, 1)

  })

  // GitHub's box: a centered column with a 1px border, a 6px radius and 32px of padding around a
  // 1012px text column, however wide the dialog. A phone shows the page itself: no border, full width.
  const BOX = 1012 + 2 * 32 + 2
  function box(doc: HTMLElement) {
    const cs = css(doc)
    const rect = doc.getBoundingClientRect()
    const text = rect.width - px(cs.paddingLeft) - px(cs.paddingRight) - px(cs.borderLeftWidth) - px(cs.borderRightWidth)
    return { cs, rect, text }
  }

  it.each([1280, 3840])('reads in GitHub’s box at %ipx: 1012px of text, bordered, padded, centered', async width => {
    await page.viewport(width, 1000)
    const { doc } = await openViewer()
    if (shotsDir) await page.screenshot({ path: `${shotsDir}/viewer-${width}.png` })
    const { cs, rect, text } = box(doc)
    expect(rect.width).toBeCloseTo(BOX, 0)
    expect(text).toBeCloseTo(1012, 0)
    expect(cs.borderTopWidth).toBe('1px')
    expect(cs.borderTopStyle).toBe('solid')
    expect(cs.borderTopLeftRadius).toBe('6px')
    expect(px(cs.paddingLeft)).toBe(32)
    expect(px(cs.paddingTop)).toBe(32)
    const body = document.querySelector<HTMLElement>('.artifact-body')!
    const inner = body.getBoundingClientRect()
    const bodyCs = css(body)
    const left = rect.left - inner.left - px(bodyCs.paddingLeft)
    const right = inner.right - px(bodyCs.paddingRight) - rect.right - (body.scrollHeight > body.clientHeight ? body.offsetWidth - body.clientWidth : 0)
    expect(Math.abs(left - right)).toBeLessThan(2) // centered in the dialog
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
  })

  it.each([390, 680])('in a narrow viewer is the page itself: no border, the full width, the viewer’s padding (%ipx)', async width => {
    await page.viewport(width, 844)
    const { doc } = await openViewer()
    if (shotsDir) await page.screenshot({ path: `${shotsDir}/viewer-${width}.png` })
    const { cs, rect } = box(doc)
    expect(cs.borderTopStyle).toBe('none')
    expect(px(cs.paddingLeft)).toBe(0)
    const body = document.querySelector('.artifact-body')!
    const bodyCs = css(body)
    expect(rect.width).toBeCloseTo(body.clientWidth - px(bodyCs.paddingLeft) - px(bodyCs.paddingRight), 0)
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
  })

  it('keeps the box at GitHub’s width when the viewer is maximized (3840px)', async () => {
    await page.viewport(3840, 1200)
    const { view, doc } = await openViewer()
    await userEvent.click(view.getByRole('button', { name: 'Maximize' }))
    await waitFor(() => expect(document.querySelector('.artifact-viewer.is-maximized')).not.toBeNull())
    if (shotsDir) await page.screenshot({ path: `${shotsDir}/viewer-3840-maximized.png` })
    expect(document.querySelector('.artifact-viewer')!.getBoundingClientRect().width).toBeCloseTo(3840, 0)
    expect(box(doc).rect.width).toBeCloseTo(BOX, 0)
    expect(box(doc).text).toBeCloseTo(1012, 0)
    // The choice is remembered (in memory too): restore it so later tests see the normal viewer.
    await userEvent.click(view.getByRole('button', { name: 'Restore size' }))
  })

  it.each(['dark', 'light'])('maps GitHub’s colors onto the %s theme', async theme => {
    document.documentElement.dataset.theme = theme
    await page.viewport(1280, 900)
    const { doc } = await openViewer()
    expect(document.querySelector('.artifact-viewer.is-maximized')).toBeNull()
    if (shotsDir) await page.screenshot({ path: `${shotsDir}/viewer-1280-${theme}.png` })
    const dialog = css(document.querySelector('.artifact-viewer')!)
    const q = (sel: string) => doc.querySelector(sel)!
    expect(css(q('pre')).backgroundColor).not.toBe(dialog.backgroundColor)
    const rows = doc.querySelectorAll('tbody tr')
    expect(css(rows[1]).backgroundColor).not.toBe(css(rows[0]).backgroundColor)
    expect(css(q('h1')).borderBottomColor).not.toBe('rgba(0, 0, 0, 0)')
    expect(css(q('blockquote')).color).not.toBe(css(doc).color)
    expect(css(doc).color).not.toBe(dialog.backgroundColor)
  })

  it('keeps a chat message compact: GitHub’s rhythm at .75em gaps, tamer headings, at the chat’s size', async () => {
    await page.viewport(1280, 900)
    const view = render(<div style={{ fontSize: '14px', width: 600, padding: 12, background: 'var(--chat-surface)', color: 'var(--chat-fg)' }}><Markdown text={DOC} /></div>)
    const md = view.container.querySelector('.md')!
    if (shotsDir) await page.screenshot({ path: `${shotsDir}/chat-message-14px.png` })
    const q = (sel: string) => md.querySelector(sel)!
    expect(px(css(md).fontSize)).toBeCloseTo(14, 1)
    // Block gap: .75em of the message's text.
    expect(px(css(q('h1 + p')).marginBottom)).toBeCloseTo(10.5, 1)
    expect(px(css(q('ul')).marginBottom)).toBeCloseTo(10.5, 1)
    expect(px(css(q('.md-codeblock')).marginBottom)).toBeCloseTo(10.5, 1)
    // Headings: 1.3 / 1.15 / 1.05em, no rules, 1.5 gaps above and one below (in body text, not their own em).
    expect(px(css(q('h1')).fontSize)).toBeCloseTo(18.2, 1)
    expect(css(q('h1')).borderBottomStyle).toBe('none')
    expect(px(css(q('h1')).marginTop)).toBeCloseTo(15.75, 1)
    expect(px(css(q('h1')).marginBottom)).toBeCloseTo(10.5, 1)
    expect(px(css(q('h2')).fontSize)).toBeCloseTo(16.1, 1)
    expect(css(q('h2')).borderBottomStyle).toBe('none')
    expect(px(css(q('h2')).marginTop)).toBeCloseTo(15.75, 1)
    expect(px(css(q('h3')).fontSize)).toBeCloseTo(14.7, 1)
    expect(px(css(q('h3')).marginBottom)).toBeCloseTo(10.5, 1)
    expect(px(css(q('pre')).paddingTop)).toBeCloseTo(14, 1)
    expect(px(css(q('td')).paddingLeft)).toBeCloseTo(14 * 13 / 16, 1)
    expect(css(q('p > code')).display).toBe('inline')
  })

  it('still resolves review anchors on GitHub-styled text (code, lists, quotes, tables)', async () => {
    await page.viewport(1280, 900)
    // The rich review editor takes a rule written `***` (a leading `---` line reads as front matter).
    const text = DOC.replace('\n---\n', '\n***\n')
    const artifact = { id: 'md1', name: 'README.md', view: 'markdown', content_type: 'text/markdown', size: text.length, version: 1 }
    const quotes = ['inline code', 'second item', 'A quoted passage.', 'closing paragraph']
    const review: ReviewFile = {
      version: 1, file: { name: artifact.name, artifactId: artifact.id },
      requests: quotes.map((quote, i) => ({ id: `r${i}`, text: `About ${quote}`, status: 'open' as const, createdAt: '2026-01-01T00:00:00Z', anchor: { kind: 'text' as const, quote, occurrence: 1 } })),
    }
    const value: ChatContextValue = { sessionId: 'md-github', files: null, view: () => {}, byName: () => undefined, latestVersion: () => undefined, reviewFor: async () => review, submitFeedback: async () => true }
    const view = render(<ChatContext.Provider value={value}><ReviewMode artifact={artifact} text={text} onClose={() => {}} /></ChatContext.Provider>)
    const editor = await view.findByRole('textbox', { name: 'Document' })
    expect(editor.classList.contains('md-doc')).toBe(true)
    await waitFor(() => expect(view.getAllByRole('button', { name: 'Show passage' })).toHaveLength(quotes.length))
    for (const button of view.getAllByRole('button', { name: 'Show passage' })) expect(button).toBeEnabled()
  })

  it.each([1280, 3840])('review mode reads in the box too, with its comments beside it and on their passage (%ipx)', async width => {
    await page.viewport(width, 1000)
    const text = DOC.replace('\n---\n', '\n***\n')
    const artifact = { id: 'md2', name: 'README.md', view: 'markdown', content_type: 'text/markdown', size: text.length, version: 1 }
    const review: ReviewFile = {
      version: 1, file: { name: artifact.name, artifactId: artifact.id },
      requests: [{ id: 'q1', text: 'About the second item', status: 'open', createdAt: '2026-01-01T00:00:00Z', anchor: { kind: 'text', quote: 'second item', occurrence: 1 } }],
    }
    const value: ChatContextValue = { sessionId: 'md-box', files: null, view: () => {}, byName: () => undefined, latestVersion: () => undefined, reviewFor: async () => review, submitFeedback: async () => true }
    const view = render(<ChatContext.Provider value={value}><div className="artifact-dialog artifact-viewer is-review is-maximized" style={{ width, height: 1000 }}><ReviewMode artifact={artifact} text={text} onClose={() => {}} /></div></ChatContext.Provider>)
    const editor = await view.findByRole('textbox', { name: 'Document' })
    await waitFor(() => expect(view.getByRole('button', { name: 'Show passage' })).toBeEnabled())
    if (shotsDir) await page.screenshot({ path: `${shotsDir}/review-${width}.png` })
    const cs = css(editor)
    expect(cs.borderTopWidth).toBe('1px')
    expect(px(cs.paddingLeft)).toBe(32)
    const boxRect = editor.getBoundingClientRect()
    expect(boxRect.width).toBeLessThanOrEqual(BOX + 0.5)
    const card = await waitFor(() => { const c = view.container.querySelector('.document-margin-card'); if (!c) throw new Error('no card'); return c })
    const cardRect = card.getBoundingClientRect()
    expect(cardRect.left).toBeGreaterThanOrEqual(boxRect.right - 0.5) // beside the box, not over it
    expect(cardRect.left - boxRect.right).toBeLessThan(48)
    const li = Array.from(editor.querySelectorAll('li')).find(el => el.textContent?.includes('second item'))!
    expect(Math.abs(cardRect.top - li.getBoundingClientRect().top)).toBeLessThan(40) // on its passage
  })

  // The narrow rule reads the column, not the screen: review mode's page beside its source at 1280px
  // is narrow, so the box gives up its border and padding there.
  it('review mode beside its source drops the box’s border in the narrow page column (1280px)', async () => {
    await page.viewport(1280, 1000)
    const text = DOC.replace('\n---\n', '\n***\n')
    const artifact = { id: 'md3', name: 'README.md', view: 'markdown', content_type: 'text/markdown', size: text.length, version: 1 }
    const value: ChatContextValue = { sessionId: 'md-box-src', files: null, view: () => {}, byName: () => undefined, latestVersion: () => undefined, reviewFor: async () => null, submitFeedback: async () => true }
    const view = render(<ChatContext.Provider value={value}><div className="artifact-dialog artifact-viewer is-review is-maximized" style={{ width: 1280, height: 1000 }}><ReviewMode artifact={artifact} text={text} onClose={() => {}} /></div></ChatContext.Provider>)
    await view.findByRole('textbox', { name: 'Document' })
    await userEvent.click(view.getByRole('button', { name: 'Source' }))
    await view.findByRole('textbox', { name: 'Source' })
    const pageBox = await waitFor(() => { const b = view.container.querySelector<HTMLElement>('.review-page .md-box'); if (!b) throw new Error('no page'); return b })
    if (shotsDir) await page.screenshot({ path: `${shotsDir}/review-source-1280.png` })
    const col = view.container.querySelector('.review-page-col')!.getBoundingClientRect()
    expect(col.width).toBeLessThanOrEqual(700)
    expect(css(pageBox).borderTopStyle).toBe('none')
    expect(px(css(pageBox).paddingLeft)).toBe(0)
  })
})
