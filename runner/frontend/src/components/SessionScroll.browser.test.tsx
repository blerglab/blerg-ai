// #281 (owner addendum): switching sessions keeps scroll positions. A→B→A puts back the chat
// (following its end, or the exact reading place, which new messages while away do not move), an
// open file tab's scroll, and the session panel's tab (the Files list) scroll, before the first paint.
import { beforeEach, afterEach, expect, it, vi } from 'vitest'
import { useEffect } from 'react'
import { act, fireEvent, render, screen, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes, useNavigate } from 'react-router-dom'
import { page, userEvent } from 'vitest/browser'
import { useTranscriptStore, type AgentEvent } from '@blerglab/chat'
import SessionDetail from './SessionDetail'
import CommandPaletteProvider from './CommandPalette'
import { useSessionStore } from '../hooks/useSessionStore'
import { explorerPrefKey } from '../hooks/useExplorerLayout'
import { makeDaemon, makeSession } from '../test/fixtures'
import { RAIL_PREF_KEY, RAIL_TAB_KEY } from '../lib/sessionRail'
import { invalidateChatTransport } from '../lib/chatTransport'
import { resetSessionRetentionForTest } from '../lib/sessionRetention'
import { emit } from '../test/wsMock'
import '../index.css'
import '@blerglab/chat/blerg.css'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async original => ({ ...await original<typeof import('../authClient')>(), getAccessToken: () => `x.${btoa(JSON.stringify({ sub: 'person-a', exp: 9999999999 }))}.x`, ensureFreshToken: async () => true }))
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))

const shotDir = (import.meta.env as Record<string, string | undefined>).VITE_SCROLL_SHOT_DIR
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
const LONG = 400
const ev = (session: string, seq: number): AgentEvent => ({ session_id: session, client_event_id: `${session}-${seq}`, seq, ts: '2026-10-27T12:00:00Z', kind: 'user_message', payload: { text: `${session} message ${seq}\n\nwith a second paragraph so each row has some height`, source: 'chat' } } as AgentEvent)
const longText = Array.from({ length: 600 }, (_, i) => `line ${i + 1} of the notes`).join('\n')
const artifacts = (id: string) => Array.from({ length: 80 }, (_, i) => ({ id: `art-${id}-${i}`, name: `report-${id}-${String(i).padStart(2, '0')}.md`, size: 12, view: 'markdown', content_type: 'text/markdown', version: 1, latest_version: 1 }))

beforeEach(() => {
  resetSessionRetentionForTest()
  useTranscriptStore.setState({ sessions: {} })
  const transcript = useTranscriptStore.getState()
  for (const [session, n] of [['sess-a', LONG], ['sess-b', 3]] as const) {
    transcript.ingestMany(session, Array.from({ length: n }, (_, i) => ev(session, i + 1)))
    transcript.ingestReplayDone(session, { lastSeq: n, hasMore: false })
  }
  useSessionStore.setState({ daemons: [makeDaemon()], sessions: ['a', 'b'].map(x => makeSession({ id: `sess-${x}`, kind: 'agent', status: 'idle', title: `Session ${x.toUpperCase()}` })) })
  localStorage.setItem(RAIL_PREF_KEY, '1')
  localStorage.setItem(RAIL_TAB_KEY, 'files')
  localStorage.setItem(explorerPrefKey('person-a'), JSON.stringify({ open: true, width: 320 }))
  apiFetch.mockReset().mockImplementation(async (url: string) => {
    const id = /\/api\/sessions\/([^/]+)\//.exec(url)?.[1] ?? ''
    if (url.includes('/workspace?')) return json({ result: { ok: true, launch_generation: 'launch', generation: 1, entries: [{ name: `notes-${id}.md`, path: 'notes.md', directory: false }], truncated: false }, members: [], runtime: 'host' })
    if (url.endsWith('/permissions')) return json({ can_grant: true, reason: '', grants: [], mcp_tools: [], boards: [] })
    return json({})
  })
  vi.stubGlobal('fetch', vi.fn(async (input: string) => {
    const url = String(input)
    const id = /\/api\/sessions\/([^/]+)\//.exec(url)?.[1] ?? ''
    if (url.endsWith('/artifacts')) return json({ artifacts: artifacts(id) })
    if (url.includes('/workspace-file')) return json({ path: 'notes.md', view: 'text', content_type: 'text/plain', mtime: '2026-10-27T12:00:00Z', content_b64: btoa(longText) })
    return json([])
  }))
})
afterEach(() => { invalidateChatTransport(); vi.unstubAllGlobals(); resetSessionRetentionForTest() })

const nav: { go: (path: string) => void } = { go: () => {} }
function Nav() { const navigate = useNavigate(); useEffect(() => { nav.go = navigate }, [navigate]); return null }
const go = (path: string) => nav.go(path)
function mount() {
  return render(<MemoryRouter initialEntries={['/sessions/sess-a']}><CommandPaletteProvider><Nav />
    <div style={{ display: 'flex', height: '100vh' }}><div style={{ flex: 1, minWidth: 0 }}><Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes></div></div>
  </CommandPaletteProvider></MemoryRouter>)
}
const frame = () => act(async () => { await new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r))) })
const timeline = () => document.querySelector<HTMLElement>('.agent-timeline')!
const atEnd = (el: HTMLElement) => el.scrollHeight - el.scrollTop - el.clientHeight < 4
/** The first row whose bottom is below the timeline's top: what the reader sees first. */
function firstSeen(): string {
  const root = timeline(), top = root.getBoundingClientRect().top
  const row = [...root.querySelectorAll<HTMLElement>('.agent-card.user')].find(el => el.getBoundingClientRect().bottom > top + 1)
  return row?.textContent?.match(/sess-a message \d+/)?.[0] ?? ''
}
async function awayAndBack(whileAway?: () => void) {
  await act(async () => go('/sessions/sess-b'))
  await screen.findByText(/sess-b message 3/)
  whileAway?.()
  await act(async () => go('/sessions/sess-a'))
}
/** New messages for A that arrive as its subscription resumes. */
async function arrive(from: number, to: number) {
  await act(async () => {
    emit({ type: 'agent_events_replay_done', session_id: 'sess-a', last_seq: LONG, has_more: false } as never)
    for (let seq = from; seq <= to; seq++) emit({ ...ev('sess-a', seq), type: 'agent_event' } as never)
  })
  await frame()
}

for (const [width, height] of [[390, 844], [3840, 2160]] as const) {
  it(`a reader's place in the chat comes back exactly, and new messages while away do not move it, at ${width}px`, async () => {
    await page.viewport(width, height)
    mount()
    await screen.findByText(/sess-a message 400/)
    await frame()
    const root = timeline()
    root.scrollTop = Math.round(root.scrollHeight / 2)
    root.dispatchEvent(new Event('scroll'))
    await frame()
    const place = root.scrollTop, seen = firstSeen()
    expect(atEnd(root)).toBe(false)
    await awayAndBack()
    await frame()
    // The same place, the same first row, once the page around it has laid out.
    expect(timeline().scrollTop).toBe(place)
    expect(firstSeen()).toBe(seen)
    await arrive(LONG + 1, LONG + 5)
    expect(firstSeen()).toBe(seen)
    expect(Math.abs(timeline().scrollTop - place)).toBeLessThanOrEqual(1)
  })

  it(`a chat that followed its end comes back at the end and keeps following, at ${width}px`, async () => {
    await page.viewport(width, height)
    mount()
    await screen.findByText(/sess-a message 400/)
    await frame()
    expect(atEnd(timeline())).toBe(true)
    await awayAndBack()
    expect(atEnd(timeline())).toBe(true)
    await arrive(LONG + 1, LONG + 3)
    await screen.findByText(/sess-a message 403/)
    await frame()
    expect(atEnd(timeline())).toBe(true)
  })

  it(`an open file tab's scroll comes back, at ${width}px`, async () => {
    await page.viewport(width, height)
    mount()
    await userEvent.click(await screen.findByRole('treeitem', { name: /^notes-sess-a\.md/ }))
    await screen.findByText(/line 1 of the notes/)
    await frame()
    const scroller = () => [...document.querySelectorAll<HTMLElement>('.hosted-view *')].find(el => el.scrollHeight > el.clientHeight + 50 && getComputedStyle(el).overflowY !== 'visible' && getComputedStyle(el).overflowY !== 'hidden')!
    scroller().scrollTop = 1200
    scroller().dispatchEvent(new Event('scroll'))
    await frame()
    const place = scroller().scrollTop
    expect(place).toBeGreaterThan(100)
    await awayAndBack()
    await screen.findByText(/line 1 of the notes/)
    expect(scroller().scrollTop).toBe(place)
  })
}

for (const [width, height] of [[390, 844], [3840, 2160]] as const) {
  it(`the session panel tab (the Files list) comes back where it was scrolled, at ${width}px`, async () => {
    await page.viewport(width, height)
    localStorage.setItem(explorerPrefKey('person-a'), JSON.stringify({ open: false, width: 320 }))
    mount()
    // On a phone the panel is a drawer: opened from its toggle on each visit.
    const show = async () => { if (width < 1024 && !document.querySelector('[data-testid="session-rail"]')) await userEvent.click(await screen.findByTestId('rail-toggle')) }
    await show()
    await screen.findByText('report-sess-a-79.md', {}, { timeout: 10_000 })
    const body = () => document.querySelector<HTMLElement>('[data-testid="session-rail"] .rail-body')!
    body().style.maxHeight = '400px'
    body().scrollTop = 600
    body().dispatchEvent(new Event('scroll'))
    await frame()
    const place = body().scrollTop
    expect(place).toBeGreaterThan(100)
    await awayAndBack()
    await show()
    await screen.findByText('report-sess-a-79.md', {}, { timeout: 10_000 })
    body().style.maxHeight = '400px'
    await frame()
    expect(body().scrollTop).toBe(place)
  })
}

it('each panel tab keeps its own scroll place, also when shown from the command palette (re-review 2)', async () => {
  await page.viewport(3840, 2160)
  mount()
  await screen.findByText('report-sess-a-79.md', {}, { timeout: 10_000 })
  const body = () => document.querySelector<HTMLElement>('[data-testid="session-rail"] .rail-body')!
  const scrollTo = async (top: number) => { body().scrollTop = top; body().dispatchEvent(new Event('scroll')); await frame() }
  const tab = (name: string) => within(screen.getByTestId('session-rail')).getByRole('tab', { name: new RegExp(`^${name}`) })
  body().style.maxHeight = '120px'
  await scrollTo(600)
  const files = body().scrollTop
  expect(files).toBeGreaterThan(100)
  fireEvent.click(tab('Live'))
  await frame()
  await scrollTo(40)
  const live = body().scrollTop
  expect(live).toBeGreaterThan(0)
  expect(live).not.toBe(files)
  fireEvent.click(tab('Files'))
  await frame()
  expect(body().scrollTop).toBe(files)
  fireEvent.click(tab('Live'))
  await frame()
  expect(body().scrollTop).toBe(live)
  // The palette's command focuses the tab it shows: the kept place stays.
  async function command(title: string) {
    screen.getByTestId('rail-toggle').focus()
    await userEvent.keyboard('{Control>}k{/Control}')
    const palette = screen.getByRole('dialog', { name: 'Command palette' })
    await userEvent.fill(within(palette).getByRole('combobox'), `>${title}`)
    await userEvent.keyboard('{Enter}')
    await frame()
  }
  await command('Show Files panel')
  expect(body().scrollTop).toBe(files)
  await command('Show Live panel')
  expect(body().scrollTop).toBe(live)
})

it('a reading place that came back is kept on the next leaving too, across a layout change (re-review 3)', async () => {
  await page.viewport(390, 844)
  mount()
  await screen.findByText(/sess-a message 400/)
  await frame()
  const root = timeline()
  root.scrollTop = Math.round(root.scrollHeight / 2)
  root.dispatchEvent(new Event('scroll'))
  await frame()
  const seen = firstSeen()
  await awayAndBack()
  await frame()
  expect(firstSeen()).toBe(seen)
  // Read without scrolling, leave again, and come back at another width.
  await act(async () => { await new Promise(r => setTimeout(r, 300)) })
  await act(async () => go('/sessions/sess-b'))
  await screen.findByText(/sess-b message 3/)
  await page.viewport(3840, 2160)
  await act(async () => go('/sessions/sess-a'))
  await frame(); await frame()
  expect(firstSeen()).toBe(seen)
})

for (const [width, height] of [[390, 844], [1440, 900]] as const) {
  it(`a panel tab focused after its kept place is put back stays fully in view (palette and ArrowRight) at ${width}px (re-review 2)`, async () => {
    await page.viewport(width, height)
    localStorage.setItem(explorerPrefKey('person-a'), JSON.stringify({ open: false, width: 320 }))
    mount()
    if (width < 1024) await userEvent.click(await screen.findByTestId('rail-toggle'))
    await screen.findByText('report-sess-a-79.md', {}, { timeout: 10_000 })
    const rail = () => screen.getByTestId('session-rail')
    const body = () => rail().querySelector<HTMLElement>('.rail-body')!
    const tab = (name: string) => within(rail()).getByRole('tab', { name: new RegExp(`^${name}`) })
    const inView = (el: Element) => {
      const r = el.getBoundingClientRect(), b = body().getBoundingClientRect()
      return r.top >= b.top - 0.5 && r.bottom <= b.bottom + 0.5 && r.left >= b.left - 0.5 && r.right <= b.right + 0.5 && r.height > 0
    }
    body().style.maxHeight = '300px'
    body().scrollTop = 600
    body().dispatchEvent(new Event('scroll'))
    await frame()
    const files = body().scrollTop
    expect(files).toBeGreaterThan(100)
    // ArrowRight from Files to Capabilities and back with ArrowLeft: the focused tab is in view.
    fireEvent.keyDown(tab('Files'), { key: 'ArrowRight' })
    await frame()
    expect(document.activeElement).toBe(tab('Capabilities'))
    expect(inView(document.activeElement!)).toBe(true)
    fireEvent.keyDown(tab('Capabilities'), { key: 'ArrowLeft' })
    await frame()
    expect(document.activeElement).toBe(tab('Files'))
    expect(body().scrollTop).toBe(files)
    expect(inView(document.activeElement!)).toBe(true)
    // The palette command too.
    {
      // On a phone the drawer is closed first; the command opens it on Files (a new body, put back).
      if (width < 1024) { await userEvent.keyboard('{Escape}'); await frame(); screen.getByTestId('rail-toggle').focus() }
      else tab('Files').focus()
      await userEvent.keyboard('{Control>}k{/Control}')
      const palette = screen.getByRole('dialog', { name: 'Command palette' })
      await userEvent.fill(within(palette).getByRole('combobox'), '>Show Files panel')
      await userEvent.keyboard('{Enter}')
      await frame()
      expect(document.activeElement).toBe(tab('Files'))
      expect(body().scrollTop).toBe(files)
      expect(inView(document.activeElement!)).toBe(true)
    }
    // One scroll container: the panel scrolls, the page does not.
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
  })
}

for (const [width, height] of [[390, 844], [1024, 768], [3840, 2160]] as const) {
  it(`the panel has one scroller, its body, with the tab strip pinned above the content, at ${width}px`, async () => {
    await page.viewport(width, height)
    localStorage.setItem(explorerPrefKey('person-a'), JSON.stringify({ open: false, width: 320 }))
    mount()
    if (width < 1024) await userEvent.click(await screen.findByTestId('rail-toggle'))
    await screen.findByText('report-sess-a-79.md', {}, { timeout: 10_000 })
    const rail = screen.getByTestId('session-rail')
    const body = rail.querySelector<HTMLElement>('.rail-body')!
    const strip = rail.querySelector<HTMLElement>('.rail-tabs')!
    body.scrollTop = 600
    body.dispatchEvent(new Event('scroll'))
    await frame()
    expect(body.scrollTop).toBeGreaterThan(100)
    if (shotDir) await page.screenshot({ path: `${shotDir}/panel-${width}x${height}.png` })
    // The strip stays at the body's top edge, fully shown, while the content below it scrolls.
    expect(Math.abs(strip.getBoundingClientRect().top - body.getBoundingClientRect().top)).toBeLessThanOrEqual(1)
    expect(strip.scrollHeight).toBeLessThanOrEqual(strip.clientHeight + 1)
    // Nothing else scrolls: the panel itself fits, and the body fills it down to its bottom.
    expect(rail.scrollHeight).toBeLessThanOrEqual(rail.clientHeight + 1)
    expect(body.getBoundingClientRect().bottom).toBeLessThanOrEqual(rail.getBoundingClientRect().bottom + 1)
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
  })
}
