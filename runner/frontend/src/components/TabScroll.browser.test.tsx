// Every tab scrolls inside its group (#259). A group is a bounded flex box (SessionGroups.css); each
// viewer must take that box and scroll its own content, keeping its toolbar in view, with one
// scroller for its content (a diff: one for the file list and one for the hunks). Measured in real
// Chromium at 1024, 3840 and 390 px, in a small group of a side-by-side and of a stacked split.
import { createRef } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { cdp, page, userEvent } from 'vitest/browser'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { useTranscriptStore, type AgentEvent, type ArtifactInfo } from '@blerglab/chat'
import SessionDetail from './SessionDetail'
import CommandPaletteProvider from './CommandPalette'
import ShellWorkspace from '../test/ShellWorkspaceHarness'
import type { ShellWorkspaceHandle } from './ShellWorkspace'
import { FakeShell } from '../test/shellMock'
import { useSessionStore } from '../hooks/useSessionStore'
import { makeDaemon, makeSession } from '../test/fixtures'
import { sessionTabsKey, viewId, type SessionView } from '../hooks/useSessionTabs'
import type { TabLayout } from '../lib/sessionGroups'
import { RAIL_PREF_KEY } from '../lib/sessionRail'
import { invalidateChatTransport } from '../lib/chatTransport'
import '../index.css'
// The modal sizes of the diff and review viewers, appended again below to load them last.
import diffCss from '../../../../packages/chat/src/components/diff.css?raw'
import reviewCss from '../../../../packages/chat/src/components/review/review.css?raw'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async original => ({ ...await original<typeof import('../authClient')>(), getAccessToken: () => `x.${btoa(JSON.stringify({ sub: 'person-a', exp: 9999999999 }))}.x`, ensureFreshToken: async () => true }))
vi.mock('../shellTransport', async original => ({ ...await original<typeof import('../shellTransport')>(), restoreShells: async () => [] }))
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))
vi.mock('../lib/engineModels', async original => ({ ...await original<typeof import('../lib/engineModels')>(), useEngineModels: () => ({ models: [], loading: false, fallback: false, source: 'live', engineEfforts: [] }) }))

const shotDir = import.meta.env.VITE_FILE_TAB_SHOTS_DIR
const json = (body: unknown) => new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } })
const frame = () => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)))
const SHA = 'abcdef0123456789abcdef0123456789abcdef01'

// Content long enough to overflow any group; each ends with a marker the test scrolls to.
const lines = (n: number, line: (i: number) => string) => Array.from({ length: n }, (_, i) => line(i)).join('\n')
const SOURCE = lines(160, i => `export const value${i} = "${'source-code-'.repeat(4)}"`) + '\nexport const LAST_SOURCE_LINE = true'
const MARKDOWN = `# Release notes\n\n${lines(80, i => `Paragraph ${i}: drag a tab to an edge to split, or into the center to move it.\n`)}\nLAST_MARKDOWN_LINE\n`
const TEXT = lines(200, i => `log line ${i}`) + '\nLAST_TEXT_LINE'
function patch(files: number, hunk: number, { name = 'module', last = true } = {}) {
  return Array.from({ length: files }, (_, f) => {
    const path = `src/${name}${f}.ts`, marked = last && f === files - 1
    const body = lines(hunk, i => `+export const added${f}_${i} = ${i}`)
    return `diff --git a/${path} b/${path}\n--- a/${path}\n+++ b/${path}\n@@ -0,0 +1,${hunk + (marked ? 1 : 0)} @@\n${body}${marked ? '\n+LAST_DIFF_LINE' : ''}\n`
  }).join('')
}
// Enough files that the list column overflows too; the last one is named so the test can find it.
const PATCH = patch(60, 12)
// What `blerg-runner diff` publishes for a range: commits, each with its files. A commit chip opens
// it scrolled to that commit.
const SHAS = ['1111111111111111111111111111111111111111', '2222222222222222222222222222222222222222', '3333333333333333333333333333333333333333']
const RANGE = SHAS.map((sha, c) => `commit ${sha}\nAuthor: Example <dev@example.invalid>\nDate:   Tue Oct 27 10:00:00 2026 +0000\n\n    Change ${c}\n\n` + patch(20, 12, { name: `c${c}-module`, last: c === SHAS.length - 1 })).join('')
const COMMIT_DIFF = `commit ${SHA}\nAuthor: Example <dev@example.invalid>\nDate:   Tue Oct 27 10:00:00 2026 +0000\n\n    Split tab groups\n\n` + patch(4, 90)
const TALL_SVG = `<svg xmlns="http://www.w3.org/2000/svg" width="400" height="3000"><rect width="400" height="3000" fill="#4a7"/></svg>`

type Fixture = { info: ArtifactInfo; body: string }
const art = (id: string, name: string, view: string, body: string, content_type: string): Fixture => ({ info: { id, name, size: body.length, content_type, view: view as ArtifactInfo['view'] }, body })
const ARTIFACTS = {
  diff: art('a-diff', `changes.diff`, 'diff', PATCH, 'text/x-diff'),
  range: art('a-range', `commits-${SHAS[0].slice(0, 7)}.diff`, 'diff', RANGE, 'text/x-diff'),
  markdown: art('a-md', 'report.md', 'markdown', MARKDOWN, 'text/markdown'),
  text: art('a-text', 'build.log', 'text', TEXT, 'text/plain'),
  image: art('a-img', 'tall.svg', 'image', TALL_SVG, 'image/svg+xml'),
  html: art('a-html', 'page.html', 'html', `<!doctype html><title>Page</title>${'<p>A paragraph of the page.</p>'.repeat(400)}`, 'text/html'),
  pdf: art('a-pdf', 'paper.pdf', 'pdf', '%PDF-1.4\n%%EOF\n', 'application/pdf'),
}
const WORKSPACE: Record<string, { view: string; content: string; type: string }> = {
  'src/main.ts': { view: 'text', content: SOURCE, type: 'text/plain' },
  'docs/notes.md': { view: 'markdown', content: MARKDOWN, type: 'text/markdown' },
  'VERSION': { view: 'text', content: '0.5.0\n', type: 'text/plain' },
}

/** The last line: the element holding it, and the box of its own text (a <pre> holds every line). */
type Target = { el: Element; rect(): DOMRect }
/** `prepare` runs once the tab shows; `fits`: the content is scaled to the group (an image) rather than scrolled. */
type Kind = { name: string; view: SessionView | 'chat'; prepare?: (group: HTMLElement) => Promise<void>; ready: (group: HTMLElement) => Promise<Target>; fits?: boolean; toolbar?: string }
const artifactView = (a: Fixture, review = false): SessionView => ({ kind: 'artifact', artifact: a.info, ...(review ? { review: true } : {}) })
/** The text node holding `text`, found again on every read: highlighting can redraw the line. */
function textNode(group: HTMLElement, text: string): Text | null {
  const walker = document.createTreeWalker(group, NodeFilter.SHOW_TEXT)
  for (let node = walker.nextNode(); node; node = walker.nextNode()) if (node.textContent?.includes(text)) return node as Text
  return null
}
const marker = (text: string) => async (group: HTMLElement): Promise<Target> => {
  await waitFor(() => { if (!textNode(group, text)) throw new Error(`no ${text} yet`) }, { timeout: 8000 })
  const node = () => textNode(group, text)!
  return {
    get el() { return node().parentElement! },
    rect: () => { const n = node(), range = document.createRange(), at = n.data.indexOf(text); range.setStart(n, at); range.setEnd(n, at + text.length); return range.getBoundingClientRect() },
  }
}
const element = (el: Element): Target => ({ el, rect: () => el.getBoundingClientRect() })
/** A docked frame (HTML, PDF): its document scrolls inside it, so the frame must fit the group. */
const frameIn = (selector: string) => async (group: HTMLElement) => {
  await waitFor(() => expect(group.querySelector(selector)).toBeTruthy(), { timeout: 8000 })
  return element(group.querySelector(selector)!)
}
const KINDS: Kind[] = [
  { name: 'chat', view: 'chat', ready: marker('LAST_CHAT_MESSAGE'), toolbar: '.agent-composer' },
  { name: 'workspace code', view: { kind: 'workspace', path: 'src/main.ts' }, ready: marker('LAST_SOURCE_LINE'), toolbar: '.viewer-toolbar' },
  { name: 'workspace markdown', view: { kind: 'workspace', path: 'docs/notes.md' }, ready: marker('LAST_MARKDOWN_LINE'), toolbar: '.viewer-toolbar' },
  { name: 'artifact markdown', view: artifactView(ARTIFACTS.markdown), ready: marker('LAST_MARKDOWN_LINE'), toolbar: '.viewer-toolbar' },
  { name: 'artifact text', view: artifactView(ARTIFACTS.text), ready: marker('LAST_TEXT_LINE'), toolbar: '.viewer-toolbar' },
  { name: 'artifact image', view: artifactView(ARTIFACTS.image), ready: async group => { await waitFor(() => expect(group.querySelector('img.artifact-image')).toBeTruthy()); const img = group.querySelector<HTMLImageElement>('img.artifact-image')!; await waitFor(() => expect(img.complete && img.naturalHeight > 0).toBe(true)); return element(img) }, fits: true, toolbar: '.viewer-toolbar' },
  { name: 'artifact html', view: artifactView(ARTIFACTS.html), ready: frameIn('iframe.artifact-html'), fits: true, toolbar: '.viewer-toolbar' },
  { name: 'artifact pdf', view: artifactView(ARTIFACTS.pdf), ready: frameIn('.artifact-pdf iframe'), fits: true, toolbar: '.viewer-toolbar' },
  { name: 'artifact diff', view: artifactView(ARTIFACTS.diff), ready: marker('LAST_DIFF_LINE'), toolbar: '.viewer-toolbar' },
  { name: 'artifact diff at a commit', view: { ...artifactView(ARTIFACTS.range), commit: SHAS[1] } as SessionView, ready: marker('LAST_DIFF_LINE'), toolbar: '.viewer-toolbar' },
  { name: 'artifact diff review', view: artifactView(ARTIFACTS.diff, true), prepare: async group => { const files = await waitFor(() => { const tab = [...group.querySelectorAll<HTMLElement>('[role=tab]')].find(t => t.textContent === 'Files'); expect(tab).toBeTruthy(); return tab! }); await userEvent.click(files) }, ready: marker('LAST_DIFF_LINE'), toolbar: '.viewer-toolbar' },
  { name: 'commit', view: { kind: 'commit', sha: SHA, sessionId: 'sess-a' }, ready: marker('LAST_DIFF_LINE'), toolbar: '.viewer-toolbar' },
]

const key = sessionTabsKey('person-a', 'sess-a')
/** `axis` is the inner split's: horizontal stacks the tab above another group, vertical puts them side by side. */
function persist(tabView: SessionView | 'chat', axis: 'vertical' | 'horizontal') {
  const other: SessionView = { kind: 'workspace', path: 'VERSION' }, filler: SessionView = { kind: 'workspace', path: 'docs/notes.md' }
  const id = tabView === 'chat' ? 'chat' : viewId(tabView)
  const views = [...(tabView === 'chat' ? [filler] : [tabView]), other]
  const tabs = views.map(view => ({ id: viewId(view), view, preview: false }))
  const restId = tabView === 'chat' ? viewId(filler) : 'chat'
  // The tab under test is the first of a split nested in the second half of the root split, so its
  // group is small in both directions: about 300 px tall, and about half the width or less.
  const small = Math.min(0.85, Math.max(0.15, 300 / Math.max(1, innerHeight - 50)))
  const pair: TabLayout = { type: 'split', id: 'inner', axis, ratio: axis === 'horizontal' ? small : 0.5, first: { type: 'group', id: 'under-test', tabs: [id], active: id }, second: { type: 'group', id: 'other', tabs: [viewId(other)], active: viewId(other) } }
  const rest: TabLayout = { type: 'group', id: 'rest', tabs: [restId], active: restId }
  // A stacked pair sits beside the rest; a side-by-side pair sits under it.
  const layout: TabLayout = axis === 'horizontal'
    ? { type: 'split', id: 'root', axis: 'vertical', ratio: 0.5, first: rest, second: pair }
    : { type: 'split', id: 'root', axis: 'horizontal', ratio: 1 - small, first: rest, second: pair }
  localStorage.setItem(key, JSON.stringify({ tabs, active: id, layout, focused: 'under-test' }))
}
function persistPhone(tabView: SessionView | 'chat') {
  const id = tabView === 'chat' ? 'chat' : viewId(tabView)
  localStorage.setItem(key, JSON.stringify({ tabs: tabView === 'chat' ? [] : [{ id, view: tabView, preview: false }], active: id }))
}

function mount() {
  return render(<MemoryRouter initialEntries={['/sessions/sess-a']}><CommandPaletteProvider><div style={{ height: '100vh', display: 'flex' }}><div style={{ flex: 1, minWidth: 0 }}><Routes><Route path="/sessions/:id" element={<SessionDetail/>}/></Routes></div></div></CommandPaletteProvider></MemoryRouter>)
}

const scrollsY = (el: Element) => { const s = getComputedStyle(el); return /(auto|scroll)/.test(s.overflowY) && el.scrollHeight > el.clientHeight + 1 }
/** Every element from `node` up to `group` that scrolls vertically: the content needs exactly one. */
function scrollers(node: Element, group: Element) {
  const out: HTMLElement[] = []
  for (let el: Element | null = node; el && el !== group.parentElement; el = el.parentElement) if (scrollsY(el)) out.push(el as HTMLElement)
  return out
}
const within = (inner: DOMRect, outer: DOMRect) => inner.top >= outer.top - 1 && inner.bottom <= outer.bottom + 1 && inner.height > 0

/** The content scrolls in exactly one scroller bounded by the group, the toolbar stays visible, and
 *  scrolling that scroller to its end brings the last line into the group. */
async function expectScrollsWithinGroup(kind: Kind, group: HTMLElement, last: Target, shot: string) {
  const content = group.querySelector<HTMLElement>('.session-group-content') ?? group
  const box = content.getBoundingClientRect()
  if (kind.fits) {
    // Scaled to the group: all of it in view, nothing scrolls around it.
    expect(within(last.rect(), box), `${kind.name}: it fits in the group`).toBe(true)
    expect(scrollers(last.el, group), `${kind.name}: no scroller`).toHaveLength(0)
    expect(within(group.querySelector(kind.toolbar!)!.getBoundingClientRect(), box), `${kind.name}: its toolbar stays in view`).toBe(true)
    if (shotDir) await page.screenshot({ path: `${shotDir}/${shot}.png` })
    return
  }
  const found = scrollers(last.el, group)
  expect(found.map(el => el.className), `${kind.name}: one scroller for its content`).toHaveLength(1)
  const scroller = found[0]
  expect(scroller.scrollHeight, `${kind.name}: scrollHeight > clientHeight`).toBeGreaterThan(scroller.clientHeight)
  expect(within(scroller.getBoundingClientRect(), box), `${kind.name}: the scroller is inside the group`).toBe(true)
  // Nothing between the group and the scroller grew past the group (a viewport-sized viewer, #259).
  for (let el: HTMLElement | null = scroller; el && el !== content; el = el.parentElement) expect(el.getBoundingClientRect().bottom, `${kind.name}: ${el.className} ends inside the group`).toBeLessThanOrEqual(box.bottom + 1)
  // Scroll the content until its last line sits at the bottom of the scroller (past it, a viewer
  // may keep scroll-past-end space).
  scroller.scrollTop += last.rect().bottom - scroller.getBoundingClientRect().bottom
  await frame()
  expect(scroller.scrollTop, `${kind.name}: it scrolled`).toBeGreaterThan(0)
  const lastRect = last.rect(), view = scroller.getBoundingClientRect()
  expect(lastRect.bottom, `${kind.name}: the last line is reachable`).toBeLessThanOrEqual(Math.min(view.bottom, box.bottom) + 1)
  expect(lastRect.top, `${kind.name}: the last line is in view`).toBeGreaterThanOrEqual(Math.max(view.top, box.top) - 1)
  if (kind.toolbar) {
    const toolbar = group.querySelector(kind.toolbar)
    expect(toolbar, `${kind.name}: toolbar`).toBeTruthy()
    expect(within(toolbar!.getBoundingClientRect(), box), `${kind.name}: its toolbar stays in view`).toBe(true)
  }
  if (shotDir) await page.screenshot({ path: `${shotDir}/${shot}.png` })
}

/** A diff's file list scrolls on its own: its last file comes into view, the hunks do not move. */
async function expectFileListScrolls(group: HTMLElement) {
  const list = group.querySelector<HTMLElement>('.diff-files')
  if (!list) return // a phone shows the files in a picker
  const box = group.getBoundingClientRect(), main = group.querySelector<HTMLElement>('.diff-main')!
  expect(within(list.getBoundingClientRect(), box), 'the file list is inside the group').toBe(true)
  const items = [...list.querySelectorAll('li')]
  // A list that fits shows every file; a longer one scrolls.
  if (list.scrollHeight <= list.clientHeight + 1) { for (const item of items) expect(within(item.getBoundingClientRect(), box)).toBe(true); return }
  expect(scrollsY(list), 'the file list scrolls').toBe(true)
  // The list follows the file in view: settle the hunks at the top first, then scroll the list.
  main.scrollTop = 0
  await waitFor(() => expect(list.querySelector('button')).toHaveAttribute('aria-current', 'location'))
  await frame()
  list.scrollTop = list.scrollHeight; await frame()
  const lastFile = items.at(-1)!.getBoundingClientRect()
  expect(lastFile.bottom).toBeLessThanOrEqual(list.getBoundingClientRect().bottom + 1)
  expect(lastFile.bottom).toBeLessThanOrEqual(box.bottom + 1)
  expect(main.scrollTop, 'the hunks scroll separately').toBe(0)
}

beforeEach(() => {
  localStorage.clear(); localStorage.setItem(RAIL_PREF_KEY, '0')
  document.documentElement.setAttribute('data-theme', 'dark')
  useSessionStore.setState({ daemons: [makeDaemon()], sessions: [makeSession({ id: 'sess-a', kind: 'agent', status: 'idle', title: 'Scroll inside a group' })] })
  useTranscriptStore.setState({ sessions: {} })
  const store = useTranscriptStore.getState()
  for (let i = 1; i <= 40; i++) store.ingest({ session_id: 'sess-a', seq: i, client_event_id: `scroll-${i}`, ts: '2026-10-27T12:00:00Z', kind: 'assistant_text', payload: { text: i === 40 ? 'LAST_CHAT_MESSAGE' : `Message ${i}: each group keeps its own selected tab and scrolls its own content.`, done: true } } as AgentEvent)
  store.ingestReplayDone('sess-a', { lastSeq: 40, hasMore: false })
  apiFetch.mockReset().mockImplementation(async (url: string) => url.includes('/live') ? json({ can_release_prompt: true }) : url.endsWith('/shells') ? json([]) : url.endsWith('/shell-capability') ? json({ available: false }) : url.includes('/workspace?') ? json({ result: { ok: true, entries: [] }, members: [] }) : json({}))
  vi.stubGlobal('fetch', vi.fn(async (input: string) => {
    const url = new URL(String(input), location.origin)
    if (url.pathname.endsWith('/workspace-file')) {
      const path = url.searchParams.get('path')!, file = WORKSPACE[path]
      if (!file) return new Response('missing', { status: 404 })
      return json({ path, view: file.view, content_type: file.type, size: file.content.length, content_b64: btoa(file.content) })
    }
    if (url.pathname.endsWith(`/commits/${SHA}/diff`)) return new Response(COMMIT_DIFF, { headers: { 'Content-Type': 'text/x-diff', 'Content-Length': String(COMMIT_DIFF.length) } })
    const raw = url.pathname.match(/\/artifacts\/([^/]+)\/raw$/)
    if (raw) {
      const a = Object.values(ARTIFACTS).find(x => x.info.id === decodeURIComponent(raw[1]))!
      return new Response(new Blob([a.body], { type: a.info.content_type }), { headers: { 'Content-Type': a.info.content_type, 'Content-Length': String(a.body.length) } })
    }
    if (url.pathname.endsWith('/artifacts')) return json({ artifacts: Object.values(ARTIFACTS).map(a => a.info), limits: {}, can_delete: true })
    return json([])
  }))
  // A transport left from the previous test would hold the unstubbed fetch.
  invalidateChatTransport()
})
afterEach(async () => { invalidateChatTransport(); vi.unstubAllGlobals(); document.documentElement.removeAttribute('data-theme'); await page.viewport(1280, 800) })

const groupUnderTest = () => document.querySelector<HTMLElement>('[data-group-id="under-test"]') ?? document.querySelector<HTMLElement>('.session-group')!

describe.each([[1024, 768], [3840, 2160]] as const)('at %i × %i', (width, height) => {
  describe.each([['stacked', 'horizontal'], ['side-by-side', 'vertical']] as const)('in a %s split', (label, axis) => {
    it.each(KINDS)('$name scrolls within its small group', async kind => {
      await page.viewport(width, height)
      persist(kind.view, axis)
      mount()
      await waitFor(() => expect(document.querySelectorAll('.session-group').length).toBeGreaterThan(1))
      const group = groupUnderTest()
      if (kind.prepare) await kind.prepare(group)
      const last = await kind.ready(group)
      await frame()
      expect(group.getBoundingClientRect().height).toBeLessThan(height * 0.6)
      await expectScrollsWithinGroup(kind, group, last, `${kind.name.replace(/\s+/g, '-')}-${label}-${width}`)
      if (kind.name.includes('diff') || kind.name === 'commit') {
        await expectFileListScrolls(group)
        // Side by side keeps the same bounds (only offered on a wide screen).
        const sideBySide = [...group.querySelectorAll('button')].find(b => b.textContent === 'Side by side')
        if (sideBySide) {
          await userEvent.click(sideBySide)
          await waitFor(() => expect(group.querySelector('[data-testid="diff-table-split"]')).toBeTruthy())
          await expectScrollsWithinGroup(kind, group, await kind.ready(group), `${kind.name.replace(/\s+/g, '-')}-side-by-side-diff-${label}-${width}`)
        }
      }
    })
  })
})

// Production bundles do not promise the order of the package's stylesheets: a viewport-sized modal
// rule loaded after the docked one made a docked diff 92vh tall, cut off at the group's edge.
describe('whatever order the stylesheets load in', () => {
  it.each(KINDS.filter(kind => kind.name.includes('diff') || kind.name === 'commit'))('$name stays inside its group', async kind => {
    await page.viewport(3840, 2160)
    const late = document.createElement('style'); late.textContent = diffCss + reviewCss; document.head.append(late)
    try {
      persist(kind.view, 'horizontal')
      mount()
      await waitFor(() => expect(document.querySelectorAll('.session-group').length).toBeGreaterThan(1))
      const group = groupUnderTest()
      if (kind.prepare) await kind.prepare(group)
      const last = await kind.ready(group)
      await frame()
      await expectScrollsWithinGroup(kind, group, last, `${kind.name.replace(/\s+/g, '-')}-late-css`)
    } finally { late.remove() }
  })
})

// The smallest group a split allows (160 px, SessionGroups' floor) leaves the review's bars more than
// its body: the patch keeps its floor and the body scrolls the bars away, so whole lines, the last
// line and the last file stay reachable, and the tab strip and toolbar stay put.
describe('a diff review in the smallest group', () => {
  // A divider stops at 15%, so the 4K case is a short 4K window to reach the 160 px floor.
  it.each([[1024, 768], [3840, 1080]] as const)('hands scrolling to the body below the floor at %i px', async (width, height) => {
    await page.viewport(width, height)
    const kind = KINDS.find(k => k.name === 'artifact diff review')!
    const id = viewId(kind.view as SessionView), other: SessionView = { kind: 'workspace', path: 'VERSION' }
    localStorage.setItem(key, JSON.stringify({
      tabs: [{ id, view: kind.view, preview: false }, { id: viewId(other), view: other, preview: false }], active: id, focused: 'under-test',
      layout: { type: 'split', id: 'root', axis: 'vertical', ratio: 0.5, first: { type: 'group', id: 'rest', tabs: ['chat'], active: 'chat' }, second: { type: 'split', id: 'inner', axis: 'horizontal', ratio: 0.05, first: { type: 'group', id: 'under-test', tabs: [id], active: id }, second: { type: 'group', id: 'other', tabs: [viewId(other)], active: viewId(other) } } },
    }))
    mount()
    await waitFor(() => expect(document.querySelectorAll('.session-group').length).toBe(3))
    const group = groupUnderTest()
    await kind.prepare!(group)
    const last = await kind.ready(group)
    await frame()
    const box = group.getBoundingClientRect()
    expect(box.height).toBeCloseTo(160, 0)
    const body = group.querySelector<HTMLElement>('.artifact-body')!, main = group.querySelector<HTMLElement>('.diff-main')!
    const strip = group.querySelector('.session-tabbar')!.getBoundingClientRect(), toolbar = group.querySelector('.viewer-toolbar')!.getBoundingClientRect()
    // Below the floor the body owns the overflow; nothing between it and the patch clips it.
    expect(scrollsY(body), 'the body scrolls').toBe(true)
    body.scrollTop = body.scrollHeight; await frame()
    const view = body.getBoundingClientRect(), patch = main.getBoundingClientRect()
    expect(patch.top).toBeGreaterThanOrEqual(view.top - 1)
    expect(patch.bottom).toBeLessThanOrEqual(view.bottom + 1)
    expect(patch.height).toBeGreaterThan(3 * parseFloat(getComputedStyle(main).fontSize) * 1.5)
    // The last line, whole, inside the patch, the body and the group.
    main.scrollTop += last.rect().bottom - patch.bottom; await frame()
    const line = last.rect()
    for (const outer of [main.getBoundingClientRect(), view, box]) { expect(line.top).toBeGreaterThanOrEqual(outer.top - 1); expect(line.bottom).toBeLessThanOrEqual(outer.bottom + 1) }
    // The last file in the list, too.
    const list = group.querySelector<HTMLElement>('.diff-files')!
    list.scrollTop = list.scrollHeight; await frame()
    const file = [...list.querySelectorAll('li')].at(-1)!.getBoundingClientRect()
    expect(file.bottom).toBeLessThanOrEqual(Math.min(list.getBoundingClientRect().bottom, box.bottom) + 1)
    expect(file.top).toBeGreaterThanOrEqual(view.top - 1)
    // The tab strip and the viewer toolbar did not move.
    expect(group.querySelector('.session-tabbar')!.getBoundingClientRect().top).toBe(strip.top)
    expect(group.querySelector('.viewer-toolbar')!.getBoundingClientRect().top).toBe(toolbar.top)
    if (shotDir) await page.screenshot({ path: `${shotDir}/artifact-diff-review-160-${width}.png` })
  })
})

describe('on a phone', () => {
  it.each(KINDS)('$name scrolls within the screen', async kind => {
    await page.viewport(390, 844)
    persistPhone(kind.view)
    mount()
    const group = await waitFor(() => { const g = document.querySelector<HTMLElement>('.session-group'); expect(g).toBeTruthy(); return g! })
    if (kind.prepare) await kind.prepare(group)
    const last = await kind.ready(group)
    await frame()
    await expectScrollsWithinGroup(kind, group, last, `${kind.name.replace(/\s+/g, '-')}-phone`)
    expect(document.documentElement.scrollHeight).toBeLessThanOrEqual(innerHeight + 1)
  })
})

describe('a shell tab', () => {
  it.each([[1024, 768], [3840, 2160], [390, 844]] as const)('keeps its terminal inside a small group and scrolls its scrollback at %i px', async (width, height) => {
    await page.viewport(width, height)
    const shells: FakeShell[] = []
    const create = vi.fn(async () => { const p = new FakeShell(String(shells.length + 1)); shells.push(p); return p })
    render(<MemoryRouter initialEntries={['/sessions/session']}><CommandPaletteProvider><div style={{ height: '100dvh' }}>
      <ShellWorkspace sessionId="session" accountId="owner" ref={createRef<ShellWorkspaceHandle>()} restoreShells={async () => []} createShell={create}><div className="agent-composer"><textarea aria-label="Composer" /></div></ShellWorkspace>
    </div></CommandPaletteProvider></MemoryRouter>)
    await userEvent.click(screen.getByRole('button', { name: 'New shell' }))
    await waitFor(() => expect(document.querySelector('.xterm-helper-textarea')).toBeTruthy())
    if (width > 390) {
      // Drag the shell under the chat, then move the divider down until its group is small.
      const tab = screen.getByRole('tab', { name: 'Shell' }).parentElement!, transfer = new DataTransfer()
      act(() => { tab.dispatchEvent(new DragEvent('dragstart', { bubbles: true, cancelable: true, composed: true, dataTransfer: transfer })) })
      const target = document.querySelector<HTMLElement>('[data-group-id]')!, r = target.getBoundingClientRect()
      const at = { dataTransfer: transfer, clientX: r.left + r.width / 2, clientY: r.bottom - 4 }
      fireEvent(target, new DragEvent('dragover', { bubbles: true, cancelable: true, ...at }))
      fireEvent(target, new DragEvent('drop', { bubbles: true, cancelable: true, ...at }))
      await waitFor(() => expect(document.querySelectorAll('.session-group')).toHaveLength(2))
      const divider = screen.getByRole('separator', { name: /Resize tab groups/ })
      divider.focus()
      for (let i = 0; i < 12; i++) await userEvent.keyboard('{ArrowDown}')
    }
    shells[0].emit(Array.from({ length: 300 }, (_, i) => `line ${i}`).join('\r\n') + '\r\nLAST_SHELL_LINE\r\n')
    await waitFor(() => expect(document.querySelector('.xterm-rows')).toHaveTextContent('LAST_SHELL_LINE'))
    await frame()
    const group = document.querySelector<HTMLElement>('[data-shell-id="1"]')!.closest<HTMLElement>('.session-group')!, box = group.getBoundingClientRect()
    if (width > 390) expect(box.height).toBeLessThan(height * 0.5)
    // The terminal fits its rows to the group: nothing past its bottom edge, and its scrollback scrolls.
    await waitFor(() => expect(within(group.querySelector('.xterm-screen')!.getBoundingClientRect(), box)).toBe(true))
    const lastRow = [...group.querySelectorAll('.xterm-rows > div')].find(row => row.textContent?.includes('LAST_SHELL_LINE'))!
    expect(within(lastRow.getBoundingClientRect(), box)).toBe(true)
    // xterm draws its own scrollbar: the slider is shorter than its track while there is scrollback.
    const track = group.querySelector<HTMLElement>('.xterm .scrollbar.vertical')!, slider = track.querySelector<HTMLElement>('.slider')!
    expect(within(track.getBoundingClientRect(), box)).toBe(true)
    expect(slider.getBoundingClientRect().height).toBeLessThan(track.getBoundingClientRect().height)
    if (shotDir) await page.screenshot({ path: `${shotDir}/shell-${width}.png` })
    // A real wheel over the terminal scrolls its scrollback: the last line leaves the view.
    const r = group.querySelector('.xterm-screen')!.getBoundingClientRect(), frameBox = window.frameElement?.getBoundingClientRect(), scale = frameBox ? frameBox.width / innerWidth : 1
    const x = (frameBox?.left ?? 0) + (r.left + r.width / 2) * scale, y = (frameBox?.top ?? 0) + (r.top + r.height / 2) * scale
    for (let i = 0; i < 40 && group.querySelector('.xterm-rows')!.textContent?.includes('LAST_SHELL_LINE'); i++) { await cdp().send('Input.dispatchMouseEvent', { type: 'mouseWheel', x, y, deltaX: 0, deltaY: -3000 }); await frame() }
    expect(group.querySelector('.xterm-rows')!.textContent).not.toContain('LAST_SHELL_LINE')
  })
})
