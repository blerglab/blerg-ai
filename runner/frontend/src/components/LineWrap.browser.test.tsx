// Line wrap in the code viewer and the diff (#265): a toolbar toggle, Alt+Z and a palette command;
// on by default for prose, off for code; kept per account and kind; wrapped lines keep one number
// at the top of their row; no horizontal scroll while wrapping. Measured in real Chromium.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { resetLineWrap, useTranscriptStore, type ArtifactInfo } from '@blerglab/chat'
import SessionDetail from './SessionDetail'
import CommandPaletteProvider from './CommandPalette'
import { useSessionStore } from '../hooks/useSessionStore'
import { makeDaemon, makeSession } from '../test/fixtures'
import { sessionTabsKey, viewId, type SessionView } from '../hooks/useSessionTabs'
import type { TabLayout } from '../lib/sessionGroups'
import { RAIL_PREF_KEY } from '../lib/sessionRail'
import { invalidateChatTransport } from '../lib/chatTransport'
import '../index.css'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async original => ({ ...await original<typeof import('../authClient')>(), getAccessToken: () => `x.${btoa(JSON.stringify({ sub: 'person-a', exp: 9999999999 }))}.x`, ensureFreshToken: async () => true }))
vi.mock('../shellTransport', async original => ({ ...await original<typeof import('../shellTransport')>(), restoreShells: async () => [] }))
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))
vi.mock('../lib/engineModels', async original => ({ ...await original<typeof import('../lib/engineModels')>(), useEngineModels: () => ({ models: [], loading: false, fallback: false, source: 'live', engineEfforts: [] }) }))

const shotDir = import.meta.env.VITE_FILE_TAB_SHOTS_DIR
const json = (body: unknown) => new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } })
const frame = () => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)))
const LONG = 'a long line that runs well past any pane width, '.repeat(40)
const PROSE = `Short opening line.\n${LONG}END_OF_LONG\nThe line after the long one.\n`
const CODE = `export const short = 1\nexport const long = "${LONG}"\nexport const after = 2\n`
const PATCH = `diff --git a/src/main.ts b/src/main.ts\n--- a/src/main.ts\n+++ b/src/main.ts\n@@ -1,3 +1,3 @@\n export const short = 1\n-export const long = "old ${LONG}"\n+export const long = "new ${LONG}"\n export const after = 2\n`
// Over 20,000 lines, under the 2 MiB text view: every line wraps on a phone.
const BIG = Array.from({ length: 20001 }, (_, i) => `line ${i + 1}: ${'long word '.repeat(5)}`).join('\n') + '\n'
const WORKSPACE: Record<string, string> = { 'notes.txt': PROSE, 'src/main.ts': CODE, 'big.txt': BIG }
const diff: ArtifactInfo = { id: 'a-diff', name: 'change.diff', size: PATCH.length, content_type: 'text/x-diff', view: 'diff' }
const key = sessionTabsKey('person-a', 'sess-a')
const views: Record<string, SessionView> = { prose: { kind: 'workspace', path: 'notes.txt' }, code: { kind: 'workspace', path: 'src/main.ts' }, diff: { kind: 'artifact', artifact: diff } }

function open(view: SessionView, layout?: TabLayout) {
  const others = Object.values(views).filter(v => viewId(v) !== viewId(view))
  localStorage.setItem(key, JSON.stringify({ tabs: [view, ...others].map(v => ({ id: viewId(v), view: v, preview: false })), active: viewId(view), ...(layout ? { layout, focused: 'under-test' } : {}) }))
  return render(<MemoryRouter initialEntries={['/sessions/sess-a']}><CommandPaletteProvider><div style={{ height: '100vh', display: 'flex' }}><div style={{ flex: 1, minWidth: 0 }}><Routes><Route path="/sessions/:id" element={<SessionDetail/>}/></Routes></div></div></CommandPaletteProvider></MemoryRouter>)
}
const scroller = async () => { await waitFor(() => { expect(document.querySelector('.hosted-view:not([hidden]) [data-wrap]')).toBeTruthy(); expect(wrapButton()).toBeTruthy() }, { timeout: 8000 }); return document.querySelector<HTMLElement>('.hosted-view:not([hidden]) [data-wrap]')! }
const wrapButton = () => document.querySelector<HTMLElement>('.hosted-view:not([hidden]) [data-wrap-toggle]')!
async function command(title: string) {
  await userEvent.keyboard('{Control>}k{/Control}')
  await userEvent.fill(screen.getByRole('combobox', { name: /^Search commands/ }), `>${title}`)
  await userEvent.keyboard('{Enter}')
}

/** Wrapped: every line is a row numbered once at its top; the long one takes several rows, and
 *  nothing scrolls sideways. */
function expectWrappedRows(box: HTMLElement) {
  const rows = [...box.querySelectorAll<HTMLElement>('.code-line')]
  expect(rows.map(r => getComputedStyle(r, '::before').content)).toEqual(rows.map((_, i) => `"${i + 1}"`))
  for (const row of rows) expectOneRowNumber(row, row.dataset.n!)
  const lineHeight = parseFloat(getComputedStyle(rows[0]).lineHeight)
  expect(rows[0].getBoundingClientRect().height).toBeCloseTo(lineHeight, 0)
  expect(rows[1].getBoundingClientRect().height).toBeGreaterThan(lineHeight * 2)
  // The next number sits right under the wrapped row, level with its own line.
  expect(rows[2].getBoundingClientRect().top).toBeCloseTo(rows[1].getBoundingClientRect().bottom, 0)
  expect(box.scrollWidth).toBeLessThanOrEqual(box.clientWidth)
}

beforeEach(() => {
  resetLineWrap()
  localStorage.clear(); localStorage.setItem(RAIL_PREF_KEY, '0')
  document.documentElement.setAttribute('data-theme', 'dark')
  useSessionStore.setState({ daemons: [makeDaemon()], sessions: [makeSession({ id: 'sess-a', kind: 'agent', status: 'idle', title: 'Wrap long lines' })] })
  useTranscriptStore.setState({ sessions: {} })
  useTranscriptStore.getState().ingestReplayDone('sess-a', { lastSeq: 0, hasMore: false })
  apiFetch.mockReset().mockImplementation(async (url: string) => url.includes('/live') ? json({ can_release_prompt: true }) : url.endsWith('/shells') ? json([]) : url.endsWith('/shell-capability') ? json({ available: false }) : url.includes('/workspace?') ? json({ result: { ok: true, entries: [] }, members: [] }) : json({}))
  vi.stubGlobal('fetch', vi.fn(async (input: string) => {
    const url = new URL(String(input), location.origin)
    if (url.pathname.endsWith('/workspace-file')) {
      const path = url.searchParams.get('path')!
      return json({ path, view: 'text', content_type: 'text/plain', size: WORKSPACE[path].length, content_b64: btoa(WORKSPACE[path]) })
    }
    if (url.pathname.endsWith('/raw')) return new Response(PATCH, { headers: { 'Content-Type': 'text/x-diff', 'Content-Length': String(PATCH.length) } })
    if (url.pathname.endsWith('/artifacts')) return json({ artifacts: [diff], limits: {}, can_delete: true })
    return json([])
  }))
  invalidateChatTransport()
})
afterEach(async () => { invalidateChatTransport(); vi.unstubAllGlobals(); document.documentElement.removeAttribute('data-theme'); await page.viewport(1280, 800) })

describe('the code viewer', () => {
  it('wraps prose by default with one number per line and no sideways scroll', async () => {
    await page.viewport(1024, 768)
    open(views.prose)
    const box = await scroller()
    expect(box).toHaveAttribute('data-wrap', 'on')
    expect(wrapButton()).toHaveAttribute('aria-pressed', 'true')
    await frame()
    expectWrappedRows(box)
    if (shotDir) await page.screenshot({ path: `${shotDir}/wrap-prose-1024.png` })
  })

  it('leaves code unwrapped by default; the button, Alt+Z and the palette toggle it, and the choice is kept', async () => {
    await page.viewport(1024, 768)
    const view = open(views.code)
    let box = await scroller()
    expect(box).toHaveAttribute('data-wrap', 'off')
    expect(box.scrollWidth).toBeGreaterThan(box.clientWidth)
    await userEvent.click(wrapButton())
    await waitFor(() => expect(box).toHaveAttribute('data-wrap', 'on'))
    expect(wrapButton()).toHaveAttribute('aria-pressed', 'true')
    await frame()
    expectWrappedRows(box)
    // Alt+Z from inside the viewer.
    document.querySelector<HTMLElement>('.hosted-view:not([hidden]) [role="region"]')!.focus()
    await userEvent.keyboard('{Alt>}z{/Alt}')
    await waitFor(() => expect(box).toHaveAttribute('data-wrap', 'off'))
    // The palette command.
    await command('Toggle line wrap')
    await waitFor(() => expect(box).toHaveAttribute('data-wrap', 'on'))
    // Kept across a reload, for code only: prose keeps its own default.
    view.unmount(); invalidateChatTransport()
    open(views.code)
    box = await scroller()
    expect(box).toHaveAttribute('data-wrap', 'on')
    expect(Object.keys(localStorage).filter(k => k.startsWith('blerg.chat.wrap.code.'))).toHaveLength(1)
    expect(Object.keys(localStorage).some(k => k.startsWith('blerg.chat.wrap.prose'))).toBe(false)
  })

  it('leaves Alt+Z alone when focus is outside the viewer; the palette command still works', async () => {
    await page.viewport(1024, 768)
    open(views.code)
    const box = await scroller()
    expect(box).toHaveAttribute('data-wrap', 'off')
    // A header action, the tab strip: Alt+Z is not the viewer's there.
    for (const outside of [document.querySelector<HTMLElement>('.session-tab [role="tab"]')!, [...document.querySelectorAll<HTMLElement>('button')].find(b => !b.closest('.hosted-view, .session-tabbar'))!]) {
      outside.focus()
      expect(document.activeElement).toBe(outside)
      await userEvent.keyboard('{Alt>}z{/Alt}')
      await frame()
      expect(box, `${outside.className}: unchanged`).toHaveAttribute('data-wrap', 'off')
    }
    await command('Toggle line wrap')
    await waitFor(() => expect(box).toHaveAttribute('data-wrap', 'on'))
  })

  it('wraps inside a small split group with one scroller (#259)', async () => {
    await page.viewport(1024, 768)
    const layout: TabLayout = { type: 'split', id: 'root', axis: 'vertical', ratio: 0.5, first: { type: 'group', id: 'rest', tabs: ['chat', viewId(views.code), viewId(views.diff)], active: 'chat' }, second: { type: 'group', id: 'under-test', tabs: [viewId(views.prose)], active: viewId(views.prose) } }
    open(views.prose, layout)
    const box = await scroller()
    await frame()
    expectWrappedRows(box)
    const group = box.closest('.session-group')!.getBoundingClientRect()
    expect(box.getBoundingClientRect().right).toBeLessThanOrEqual(group.right + 1)
  })
})

/** A Range over `text` inside `root`'s text (its first occurrence). */
function textRange(root: Element, text: string, through?: string) {
  const all = root.textContent!, from = all.indexOf(text), to = through ? all.indexOf(through) + through.length : from + text.length
  const point = (offset: number, end: boolean): [Node, number] => {
    const walk = document.createTreeWalker(root, NodeFilter.SHOW_TEXT)
    for (let node = walk.nextNode(); node; node = walk.nextNode()) {
      const length = node.textContent!.length
      if (end ? offset <= length : offset < length) return [node, offset]
      offset -= length
    }
    throw new Error('past the end')
  }
  const range = document.createRange()
  range.setStart(...point(from, false)); range.setEnd(...point(to, true))
  return { range, expected: all.slice(from, to) }
}
/** A line number draws on one row: it does not wrap and fits its gutter cell. */
function expectOneRowNumber(el: Element, digits: string) {
  const before = getComputedStyle(el, '::before')
  expect(before.content).toBe(`"${digits}"`)
  expect(before.whiteSpace).toBe('pre')
  const probe = document.createElement('span')
  probe.style.cssText = `position:absolute;visibility:hidden;white-space:pre;font:${before.font};letter-spacing:${before.letterSpacing}`
  probe.textContent = digits
  document.body.append(probe)
  const width = probe.getBoundingClientRect().width
  probe.remove()
  expect(width, `${digits} fits its cell`).toBeLessThanOrEqual(parseFloat(before.width) + 0.5)
}

// A file over 5,000 lines does not wrap (integrator ruling): the toggle is off and disabled with
// its reason, Alt+Z and the palette leave it, a saved choice does not apply, and the unwrapped view
// keeps selection, copy, find and links working.
describe('a file over 5,000 lines', () => {
  const settle = () => new Promise(resolve => setTimeout(resolve, 300))
  async function openBig(line?: number) {
    await page.viewport(390, 844)
    open({ kind: 'workspace', path: 'big.txt', ...(line ? { line } : {}) })
    const box = await scroller()
    return box
  }
  const inView = (box: HTMLElement, r: DOMRect) => { const v = box.getBoundingClientRect(); return r.top >= v.top - 1 && r.bottom <= v.bottom + 1 && r.height > 0 }

  it('does not wrap: the toggle is disabled with its reason, Alt+Z and the palette leave it', async () => {
    // Prose wraps by default and the person turned prose wrapping on: neither applies here.
    await page.viewport(390, 844)
    const view = open(views.prose)
    await scroller()
    await userEvent.click(wrapButton()); await userEvent.click(wrapButton())
    expect(wrapButton()).toHaveAttribute('aria-pressed', 'true')
    view.unmount(); invalidateChatTransport()
    const box = await openBig()
    expect(box).toHaveAttribute('data-wrap', 'off')
    expect(box.querySelector('.code-viewer-gutter')).toBeTruthy()
    const button = wrapButton()
    expect(button).toBeDisabled()
    expect(button).toHaveAttribute('aria-pressed', 'false')
    expect(button).toHaveAttribute('title', 'Wrap is off for files over 5,000 lines')
    document.querySelector<HTMLElement>('.hosted-view:not([hidden]) [role="region"]')!.focus()
    await userEvent.keyboard('{Alt>}z{/Alt}')
    await frame()
    expect(box).toHaveAttribute('data-wrap', 'off')
    await command('Toggle line wrap')
    await frame()
    expect(box).toHaveAttribute('data-wrap', 'off')
    // The prose choice itself is untouched: a short prose file still wraps.
    expect(Object.entries(localStorage).filter(([key]) => key.startsWith('blerg.chat.wrap.prose')).map(([, value]) => value)).toEqual(['1'])
  })

  it('opens a deep link at its line', async () => {
    const box = await openBig(10000)
    await waitFor(() => expect(box.querySelector('[data-testid="workspace-line"]')).toBeTruthy())
    await frame()
    expect(inView(box, box.querySelector('[data-testid="workspace-line"]')!.getBoundingClientRect()), 'the band is in view').toBe(true)
    expect(inView(box, textRange(box.querySelector('code')!, 'line 10000:').range.getBoundingClientRect()), 'the line is in view').toBe(true)
    if (shotDir) await page.screenshot({ path: `${shotDir}/large-file-deep-link-390.png` })
  })

  it('keeps a selection from line 100 to line 9000 as it scrolls, and copies all of it', async () => {
    const box = await openBig()
    const code = box.querySelector('code')!
    await waitFor(() => expect(code.textContent).toBe(BIG))
    const { range, expected } = textRange(code, 'line 100:', 'line 9000:')
    const selection = getSelection()!
    selection.removeAllRanges(); selection.addRange(range)
    expect(selection.toString()).toBe(expected)
    box.scrollTop += range.getBoundingClientRect().bottom - box.getBoundingClientRect().bottom + 40
    await settle(); await frame()
    expect(selection.toString()).toBe(expected)
    box.focus()
    await userEvent.keyboard('{Control>}c{/Control}')
    // Read it back by pasting into a field (the page may not read the clipboard directly).
    const field = document.createElement('textarea')
    document.body.append(field); field.focus()
    await userEvent.keyboard('{Control>}v{/Control}')
    const copied = field.value
    field.remove()
    expect(copied.length).toBe(expected.length)
    expect(copied).toBe(expected)
  })

  it('keeps a find on a far line as it comes into view', async () => {
    const box = await openBig()
    getSelection()!.removeAllRanges()
    expect((window as unknown as { find(text: string): boolean }).find('line 15000:')).toBe(true)
    const selection = getSelection()!
    expect(selection.toString()).toBe('line 15000:')
    box.scrollTop += selection.getRangeAt(0).getBoundingClientRect().top - box.getBoundingClientRect().top - 100
    await settle(); await frame()
    expect(selection.toString()).toBe('line 15000:')
    expect(inView(box, selection.getRangeAt(0).getBoundingClientRect())).toBe(true)
  })
})

describe('the diff', () => {
  it.each([[390, 844], [1024, 768], [3840, 2160]] as const)('wraps unified and side by side at %i px: one number per row, marks and backgrounds span it', async (width, height) => {
    await page.viewport(width, height)
    open(views.diff)
    await waitFor(() => expect(document.querySelector('.hosted-view:not([hidden]) .diff-view')).toBeTruthy(), { timeout: 8000 })
    const view = document.querySelector<HTMLElement>('.hosted-view:not([hidden]) .diff-view')!
    // A code diff starts unwrapped.
    expect(view).toHaveClass('is-unwrapped')
    expect(wrapButton()).toHaveAttribute('aria-pressed', 'false')
    await userEvent.click(wrapButton())
    await waitFor(() => expect(view).toHaveClass('is-wrapped'))
    const layouts = width >= 900 ? ['unified', 'split'] : ['unified']
    for (const layout of layouts) {
      if (layout === 'split') await userEvent.click(screen.getByRole('button', { name: 'Side by side' }))
      await waitFor(() => expect(view.querySelector(`[data-testid="diff-table-${layout}"]`)).toBeTruthy())
      await frame()
      const wrap = view.querySelector<HTMLElement>('.diff-table-wrap')!
      expect(wrap.scrollWidth, `${layout}: no sideways scroll`).toBeLessThanOrEqual(wrap.clientWidth)
      const added = [...view.querySelectorAll<HTMLElement>(layout === 'split' ? 'td.diff-add' : 'tr.diff-add')].find(el => el.textContent?.includes('new a long line'))!
      const row = added.closest('tr')!, cells = [...row.cells]
      const lineHeight = parseFloat(getComputedStyle(added).lineHeight)
      expect(row.getBoundingClientRect().height, `${layout}: the long line wraps`).toBeGreaterThan(lineHeight * 2)
      // Its number is shown once, at the top of the row.
      const num = cells.find(cell => cell.classList.contains('diff-num') && cell.textContent?.trim() === '2' && (layout === 'unified' || cell.nextElementSibling?.classList.contains('diff-add')))!
      expect(num.getBoundingClientRect().height).toBeCloseTo(row.getBoundingClientRect().height, 0)
      expect(getComputedStyle(num).verticalAlign).toBe('top')
      // The + mark starts the first row; the background covers every wrapped row.
      const cell = layout === 'split' ? added : cells.find(c => c.classList.contains('diff-code'))!
      expect(cell.querySelector('.diff-mark')!.getBoundingClientRect().top).toBeCloseTo(cell.getBoundingClientRect().top, 0)
      expect(cell.getBoundingClientRect().height).toBeCloseTo(row.getBoundingClientRect().height, 0)
      expect(getComputedStyle(layout === 'split' ? added : row).backgroundColor).not.toBe('rgba(0, 0, 0, 0)')
      if (shotDir) await page.screenshot({ path: `${shotDir}/wrap-diff-${layout}-${width}.png` })
    }
    // Unwrapped, side by side scrolls sideways again.
    await userEvent.click(wrapButton())
    await waitFor(() => expect(view).toHaveClass('is-unwrapped'))
    await frame()
    expect(view.querySelector<HTMLElement>('.diff-table-wrap')!.scrollWidth).toBeGreaterThan(view.querySelector<HTMLElement>('.diff-table-wrap')!.clientWidth)
  })
})
