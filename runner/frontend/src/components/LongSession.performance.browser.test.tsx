// #215: days of rows, including full checklist snapshots separated by other status rows.
import { beforeEach, afterEach, it, expect, onTestFinished, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import '../index.css'
import { seedRichRail } from '../test/railFixture'
import { reelReply } from '../test/reelFixture'
import { RAIL_TAB_KEY } from '../lib/sessionRail'
import { useTranscriptStore, type AgentEvent } from '@blerglab/chat'
import { Profiler } from 'react'
const profile = { counts: {} as Record<string, number>, durations: [] as number[] }
// React's development profiler records which fibers performed work in this commit. This is
// test instrumentation only; no production render counters or test flags enter the app.
function recordRender(_: string, __: string, duration: number, ___: number, started: number) {
 profile.durations.push(duration)
 const root = document.querySelector('[data-perf-root]')
 if (!root) return
 const key = Object.keys(root).find(k => k.startsWith('__reactContainer$'))
 if (!key) return
 interface Fiber { child: Fiber | null; sibling: Fiber | null; flags: number; subtreeFlags: number; actualStartTime: number; type?: { name?: string; type?: { name?: string } }; memoizedProps?: { session?: { id: string } }; stateNode?: { current: Fiber } }
 const container = (root as unknown as Record<string, Fiber>)[key]
 function visit(f: Fiber | null) {
  if (!f) return
  const name = f.type?.name ?? f.type?.type?.name
  if (name && (f.flags & 1) && f.actualStartTime >= started) {
   profile.counts[name] = (profile.counts[name] ?? 0) + 1
   if (['SessionCard', 'SessionTile', 'SessionStatusGlyph'].includes(name) && f.memoizedProps?.session?.id) {
    const instance = `${name}:${f.memoizedProps.session.id}`
    profile.counts[instance] = (profile.counts[instance] ?? 0) + 1
   }
  }
  // React bubbles PerformedWork through subtreeFlags. Skip bailed-out subtrees so the
  // instrumentation itself does not walk thousands of unchanged rows on every key.
  if (f.subtreeFlags & 1) visit(f.child)
  visit(f.sibling)
 }
 visit(container.stateNode?.current ?? container)
}
vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async orig => ({ ...(await orig<typeof import('../authClient')>()), getAccessToken: () => 'tok', redirectToRefresh: vi.fn() }))
vi.mock('../claims', async orig => ({ ...(await orig<typeof import('../claims')>()), tokenExpired: () => false }))
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))
import SessionDetail from './SessionDetail'
import SessionList from './SessionList'
import { useSessionStore } from '../hooks/useSessionStore'
import { makeSession } from '../test/fixtures'
import type { LiveReply } from '../lib/liveState'
import { emit, send } from '../test/wsMock'
const SHA = 'a'.repeat(40)
// .log, not .txt: unwrapped long lines; a diff of prose files wraps by default (#265).
const PATCH = Array.from({ length: 60 }, (_, i) => `diff --git a/file${i}.log b/file${i}.log\n--- a/file${i}.log\n+++ b/file${i}.log\n@@ -1 +1,${i === 0 ? 1000 : 10} @@\n-old\n${Array.from({ length: i === 0 ? 1000 : 10 }, (_, n) => '+' + (n === 0 ? 'x'.repeat(5000) : `line ${n}`)).join('\n')}\n`).join('')
let perfLive: LiveReply
const json = (body: unknown) => Promise.resolve(new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } }))
beforeEach(() => {
 profile.counts = {}; profile.durations = []
 send.mockClear()
 send.mockReturnValue(true)
 localStorage.clear()
 localStorage.setItem(RAIL_TAB_KEY, 'history')
 const { live } = seedRichRail()
 perfLive = live
 live.asks = []; live.counts.needs_you = 0
 // The Running now token chip and context meter (#191) render while the person types.
 live.tokens_hidden = false
 live.usage = { total: 1_412_000, input: 12_000, output: 45_000, cache_read: 1_300_000, cache_write: 55_000, subagents: 0, turns: 9, context: { tokens: 180_000, window: 200_000, compacted: true, compacted_from: 190_000 } }
 const state = useTranscriptStore.getState()
 let seq = 100
 const emit = (kind: string, payload: unknown) => state.ingest({ session_id: 'sess-a', client_event_id: `perf-${++seq}`, seq, ts: new Date(1792000000000 + seq * 60000).toISOString(), kind, payload } as AgentEvent)
 for (let i = 0; i < 500; i++) {
  emit('user_message', { text: `Request ${i}`, source: 'chat' })
  emit('checklist', { op: 'tick', items: Array.from({ length: 50 }, (_, j) => ({ id: `item-${j}`, text: `Checklist task ${j}`, done: j < i % 50 })), changed: { index: i % 50 + 1, text: `Checklist task ${i % 50}`, done: true } })
  emit('ticket', { action: 'pin', ticket: { source: 'github', key: `#${i}`, title: `Pinned task ${i}` } })
  emit('topic', { id: `tp-${i}`, title: `Work ${i}`, action: 'start' })
  emit('assistant_text', { text: `Completed step ${i}`, done: true })
  emit('user_message', { text: `Next step ${i}`, source: 'chat' })
 }
 emit('assistant_text', { text: '```text\n' + 'x'.repeat(5000) + '\n```\n\n| Column |\n| --- |\n| ' + 'y'.repeat(5000) + ' |', done: true })
 state.ingestReplayDone('sess-a', { lastSeq: seq, hasMore: false })
 const reply = reelReply()
 reply.topics.forEach(t => { t.items = []; t.counts = {} })
 reply.strokes = Array.from({ length: 2000 }, (_, i) => ({ id: `stroke-${i}`, kind: 'finding', source: 'mark', text: `Finding ${i}`, jump: null, seq: 3000 - i, at: new Date(Date.now() - i * 60000).toISOString(), topic_id: i < 1000 ? 'tp_open' : 'tp_done', ...(i === 0 ? { commits: [{ sha: SHA, url: null }] } : {}) }))
 let liveSignature = '', liveVersion = 0
 apiFetch.mockImplementation((url: string, options?: RequestInit) => {
  if (url.endsWith('/live')) {
   const signature = JSON.stringify(live)
   if (signature !== liveSignature) { liveSignature = signature; liveVersion++ }
   const etag = `"live-${liveVersion}"`
   return new Headers(options?.headers).get('If-None-Match') === etag
    ? Promise.resolve(new Response(null, { status: 304 }))
    : Promise.resolve(new Response(signature, { headers: { 'Content-Type': 'application/json', ETag: etag } }))
  }
  return json(url.endsWith('/topics') ? reply : url.startsWith('/api/tool-changes') ? { tool_changes: [] } : {})
 })
 vi.stubGlobal('fetch', vi.fn((url: string) => String(url).includes('/diff') ? Promise.resolve(new Response(PATCH)) : json(String(url).endsWith('/artifacts') ? { artifacts: [] } : [])))
})
afterEach(() => vi.unstubAllGlobals())
function mount(withSidebar = false) {
 const root = document.createElement('div')
 root.dataset.perfRoot = ''
 document.body.appendChild(root)
 render(<Profiler id="session" onRender={recordRender}><MemoryRouter initialEntries={['/sessions/sess-a']}><div style={{ height: '100vh', display: 'flex', minWidth: 0, overflow: 'hidden' }}>{withSidebar && <aside style={{ display: window.innerWidth < 600 ? 'none' : 'flex' }}><SessionList variant="sidebar" /></aside>}<div style={{ flex: 1, minWidth: 0, minHeight: 0 }}><Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes></div></div></MemoryRouter></Profiler>, { container: root })
}
async function history(width: number) {
 if (width === 390) await userEvent.click(screen.getByTestId('rail-toggle'))
 const panel = width === 390 ? await screen.findByRole('dialog', { name: 'Session panel' }) : screen.getByTestId('session-rail')
 await userEvent.click(within(panel).getByRole('tab', { name: /History/ }))
 await within(panel).findAllByTestId('stroke')
 return panel
}
async function typeAndMeasure(label: string) {
 // Drain tab activation and its promise-driven reads before attributing commits to typing.
 await act(async () => {})
 const box = document.querySelector<HTMLTextAreaElement>('.agent-composer textarea')!
 const names = ['ChatView', 'ChecklistRow', 'TicketCard', 'EventRow', 'HistoryTab', 'Transcript', 'SessionRail', 'SessionTabBar', 'CommitViewerBody']
 const counts = Object.fromEntries(names.map(name => [name, 0])), times: number[] = [], renders: number[] = []
 let composerRenders = 0
 for (let i = 0; i < 30; i++) {
  // Real keys arrive in separate browser tasks. Let the previous frame paint, then measure
  // every key including its synchronous layout, without charging unrelated clock ticks to it.
  await new Promise<void>(resolve => requestAnimationFrame(() => resolve()))
  await act(async () => {})
  const before = { ...profile.counts }, durationStart = profile.durations.length, start = performance.now()
  fireEvent.change(box, { target: { value: box.value + 'a' } })
  box.getBoundingClientRect()
  times.push(performance.now() - start)
  renders.push(...profile.durations.slice(durationStart))
  for (const name of names) counts[name] += (profile.counts[name] ?? 0) - (before[name] ?? 0)
  composerRenders += (profile.counts.Composer ?? 0) - (before.Composer ?? 0)
 }
 console.warn('PERF', label, JSON.stringify({ max: Math.max(...times), p95: [...times].sort((a,b) => a-b)[28], counts, maxRenderMs: Math.max(...renders) }))
 expect(composerRenders).toBe(30)
 return { counts, times }
}
it.each([390, 3840])('isolates typing with 2,000 strokes and 500 checklist snapshots at %ipx', async width => {
 await page.viewport(width, width === 390 ? 844 : 2160)
 mount()
 const panel = await history(width)
 await waitFor(() => expect(document.querySelectorAll('[data-testid=checklist-row]').length).toBeGreaterThan(0))
 for (const el of [document.documentElement, screen.getByTestId('session-columns'), screen.getByTestId('session-chat-column')]) expect(el.scrollWidth).toBeLessThanOrEqual(el.clientWidth)
 for (const selector of ['.md-codeblock pre', '.md-table-wrap']) {
  const el = document.querySelector<HTMLElement>(selector)!
  expect(getComputedStyle(el).overflowX).toBe('auto')
  expect(el.textContent).toContain(selector.includes('table') ? 'y'.repeat(5000) : 'x'.repeat(5000))
 }
 for (const name of ['ChecklistRow', 'TicketCard', 'EventRow', 'Transcript', 'HistoryTab', 'SessionRail', 'SessionTabBar']) expect(profile.counts[name]).toBeGreaterThan(0)
 const a = await typeAndMeasure(`history-${width}`)
 // Finish the tab transition (including Activity's deferred hidden work) before timing typing.
 await act(async () => { await userEvent.click(within(panel).getByRole('tab', { name: /Live/ })) })
 expect(await within(panel).findByTestId('rail-context')).toHaveTextContent('90% · 180k of 200k')
 const b = await typeAndMeasure(`after-history-${width}`)
 await userEvent.click(within(panel).getByRole('tab', { name: /History/ }))
 await userEvent.click(await within(panel).findByTestId('reel-commit'))
 await waitFor(() => expect(document.querySelectorAll('.diff-file').length).toBe(60))
 await userEvent.click(document.querySelector('.diff-file-toggle')!)
 await userEvent.click(screen.getByRole('button', { name: 'Show full file' }))
 await act(async () => { fireEvent.click(width === 390 ? screen.getByRole('button', { name: /← Chat/ }) : screen.getByRole('tab', { name: /Chat/ })) })
 expect(profile.counts.CommitViewerBody).toBeGreaterThan(0)
 const c = await typeAndMeasure(`after-viewer-${width}`)
 for (const result of [a,b,c]) {
  expect(Object.values(result.counts)).toEqual(Array(Object.keys(result.counts).length).fill(0))
  expect(Math.max(...result.times)).toBeLessThan(16)
 }
}, 60000)
it.each([390, 3840])('contains a 60-file commit at full height and restores the composer at %ipx', async width => {
 await page.viewport(width, width === 390 ? 844 : 2160)
 mount()
 const panel = await history(width)
 await userEvent.click(await within(panel).findByTestId('reel-commit'))
 const viewer = await screen.findByTestId('commit-viewer-overlay')
 await waitFor(() => expect(viewer.querySelectorAll('.diff-file').length).toBe(60))
 console.info('LAYOUT', width, JSON.stringify({ columns: screen.getByTestId('session-columns').getBoundingClientRect().toJSON(), composer: document.querySelector('.agent-composer')?.getBoundingClientRect().toJSON(), files: viewer.querySelectorAll('.diff-file-body').length }))
 expect(viewer.querySelectorAll('.diff-file-body').length).toBe(0)
 const composer = document.querySelector<HTMLElement>('.agent-composer')!
 expect(composer).not.toBeVisible()
 expect(viewer.getBoundingClientRect().bottom).toBeCloseTo(screen.getByTestId('session-chat-column').getBoundingClientRect().bottom, 0)
 for (const el of [document.documentElement, screen.getByTestId('session-columns'), screen.getByTestId('session-chat-column')]) expect(el.scrollWidth).toBeLessThanOrEqual(el.clientWidth)
 const shotDir = import.meta.env.VITE_LAUNCH_SHOTS_DIR
 if (width === 390) {
  const close = screen.queryByRole('dialog', { name: 'Session panel' })?.querySelector<HTMLButtonElement>('.rail-close')
  if (close) await userEvent.click(close)
 }
 if (shotDir) await page.screenshot({ path: `${shotDir}/commit-${width}.png` })
 await userEvent.click(viewer.querySelector('.diff-file-toggle')!)
 expect(viewer.querySelectorAll('.diff-row').length).toBeLessThanOrEqual(400)
 expect(viewer.querySelector<HTMLElement>('.diff-table-wrap')!.scrollWidth).toBeGreaterThan(viewer.querySelector<HTMLElement>('.diff-table-wrap')!.clientWidth)
 for (const el of [screen.getByTestId('session-columns'), screen.getByTestId('session-chat-column')]) expect(el.scrollWidth).toBeLessThanOrEqual(el.clientWidth)
 const top = viewer.getBoundingClientRect().top
 const scroller = viewer.querySelector<HTMLElement>('.diff-main')!
 scroller.scrollTop = 200
 expect(scroller.scrollTop).toBeGreaterThan(0)
 expect(viewer.getBoundingClientRect().top).toBe(top)
 expect(composer).not.toBeVisible()
 if (shotDir) await page.screenshot({ path: `${shotDir}/commit-expanded-${width}.png` })
 viewer.querySelector<HTMLElement>('[role="region"]')!.focus()
 await userEvent.keyboard('{Escape}')
 expect(composer).toBeVisible()
 if (width === 390) expect(composer.querySelector('textarea')).not.toHaveFocus()
 else expect(composer.querySelector('textarea')).toHaveFocus()
}, 60000)

// Activity preserves the DOM but reconnects effects. Exercise a real commit round trip,
// including a previous send (pin > 0) and output delivered while Chat has no effects.
const scrollCases = [390, 3840].flatMap(width => [false, true].flatMap(sent =>
 [false, true].flatMap(following => [false, true].map(update => ({ width, sent, following, update }))),
))
async function layoutFrames() {
 for (let i = 0; i < 3; i++) await new Promise<void>(resolve => requestAnimationFrame(() => resolve()))
}
function receiveLive(kind: AgentEvent['kind'], payload: unknown) {
 const state = useTranscriptStore.getState()
 const seq = state.sessions['sess-a'].lastSeq + 1
 act(() => state.ingest({ type: 'agent_event', session_id: 'sess-a', client_event_id: `live-${seq}`, seq, ts: new Date(1792000000000 + seq * 60000).toISOString(), kind, payload } as AgentEvent))
}
const bottomGap = (el: HTMLElement) => el.scrollHeight - el.clientHeight - el.scrollTop
it.each(scrollCases)('restores Chat at $width px (sent=$sent, following=$following, hidden update=$update)', async ({ width, sent, following, update }) => {
 await page.viewport(width, width === 390 ? 844 : 2160)
 mount()
 if (sent) {
  const box = document.querySelector<HTMLTextAreaElement>('.agent-composer textarea')!
  fireEvent.change(box, { target: { value: 'Continue the work' } })
  await act(async () => { fireEvent.keyDown(box, { key: 'Enter' }) })
  expect(box.value).toBe('')
  const message = send.mock.calls.map(([message]) => message).find(message => message.type === 'agent_user_message')
  expect(message?.type).toBe('agent_user_message')
  if (message?.type === 'agent_user_message') receiveLive('user_message', { text: message.text, source: 'chat', client_id: message.client_id })
 }
 const panel = await history(width)
 const timeline = document.querySelector<HTMLElement>('.agent-timeline')!
 await waitFor(() => expect(timeline.scrollHeight).toBeGreaterThan(timeline.clientHeight + 1000))
 await layoutFrames()
 expect(bottomGap(timeline)).toBeLessThanOrEqual(1)
 if (!following) {
  fireEvent.wheel(timeline, { deltaY: -400 })
  timeline.scrollTop -= 400
  fireEvent.scroll(timeline)
  await layoutFrames()
  expect(bottomGap(timeline)).toBeGreaterThan(300)
 }
 const before = timeline.scrollTop
 const chatPane = document.getElementById('session-view-chat')!
 await userEvent.click(await within(panel).findByTestId('reel-commit'))
 await waitFor(() => expect(document.querySelectorAll('.diff-file').length).toBe(60))
 expect(getComputedStyle(chatPane).display).toBe('none')
 const hiddenText = 'Output received while reading the commit'
 if (update) receiveLive('assistant_text', { text: hiddenText, done: true })
 fireEvent.click(width === 390 ? screen.getByRole('button', { name: /← Chat/ }) : screen.getByRole('tab', { name: /Chat/ }))
 await waitFor(() => expect(getComputedStyle(chatPane).display).not.toBe('none'))
 if (update) await within(timeline).findByText(hiddenText)
 await layoutFrames()
 expect(document.querySelector('.agent-timeline')).toBe(timeline)
 if (following) expect(bottomGap(timeline)).toBeLessThanOrEqual(1)
 else {
  expect(Math.abs(timeline.scrollTop - before)).toBeLessThan(2)
  if (update) expect(screen.getByTestId('new-output')).toBeTruthy()
 }
 // Reconnected observers and listeners must preserve the same choice for later output too.
 receiveLive('assistant_text', { text: 'Output after returning to Chat', done: true })
 await within(timeline).findByText('Output after returning to Chat')
 await layoutFrames()
 if (following) expect(bottomGap(timeline)).toBeLessThanOrEqual(1)
 else expect(Math.abs(timeline.scrollTop - before)).toBeLessThan(2)
}, 60000)

// #234: real sidebar/tiles beside a long parent transcript, six builders delivering independent
// transcript chunks and session broadcasts. Drive streams by event count, never a timer window.
const childCases = [390, 3840].flatMap(width => [false, true].flatMap(tiles => [false, true].map(collapsed => ({ width, tiles, collapsed }))))
const renderDelta = (before: Record<string, number>) => Object.fromEntries(Object.entries(profile.counts)
 .map(([name, count]) => [name, count - (before[name] ?? 0)] as const).filter(([, count]) => count))
const isolated = ['SessionDetail', 'AgentChatView', 'ChatView', 'Transcript', 'SessionTabBar', 'SessionRail', 'HostedViewers', 'CommitViewerBody', 'Composer']
it.each(childCases)('isolates typing and children streaming at $width px (tiles=$tiles, collapsed=$collapsed)', async ({ width, tiles, collapsed }) => {
 await page.viewport(width, width === 390 ? 844 : 2160)
 const children = Array.from({ length: 6 }, (_, i) => makeSession({ id: `builder-${i}`, title: `Builder ${i}`, parent_session_id: 'sess-a', kind: 'agent', status: 'running' }))
 perfLive.children = children.map(child => ({ id: child.id, title: child.title, engine: 'codex', model: 'model-next', status: child.status, started_at: child.started_at, asking: false, tokens: null }))
 useSessionStore.setState(s => ({ sessions: [...s.sessions, ...children, ...Array.from({ length: 12 }, (_, i) => makeSession({ id: `other-${i}`, title: `Other work ${i}`, kind: 'agent', status: 'running' }))] }))
 if (collapsed) localStorage.setItem('blerg-runner.sidebar.childrenClosed', JSON.stringify(['sess-a']))
 // A running session's 1 Hz clocks (ChatView's useTicker, the rail) render on their own. A tick that
 // lands just before a measured batch is flushed inside that batch's act() and would be counted as a
 // child-event render (ChatView + Composer: 1). Hold interval ticks from the first measured batch to
 // the end of the test, then run them once.
 const realInterval = window.setInterval.bind(window)
 let holdTicks = false
 const heldTicks = new Map<TimerHandler, () => void>()
 const intervals = vi.spyOn(window, 'setInterval').mockImplementation(((handler: TimerHandler, ms?: number, ...args: unknown[]) => realInterval(() => {
  if (typeof handler !== 'function') return
  const tick = () => (handler as (...a: unknown[]) => void)(...args)
  if (holdTicks) heldTicks.set(handler, tick)
  else tick()
 }, ms)) as typeof window.setInterval)
 onTestFinished(() => intervals.mockRestore())
 mount(true)
 // Retain a visited viewer too: child events must not wake file tabs while Chat is visible.
 const panel = await history(width)
 await userEvent.click(await within(panel).findByTestId('reel-commit'))
 await waitFor(() => expect(document.querySelectorAll('.diff-file').length).toBe(60))
 await act(async () => { fireEvent.click(width === 390 ? screen.getByRole('button', { name: /← Chat/ }) : screen.getByRole('tab', { name: /Chat/ })) })
 const close = document.querySelector('.rail-close')
 if (close) await act(async () => { fireEvent.click(close) })
 if (tiles) {
  await act(async () => { fireEvent.click(document.querySelector('[aria-label="Collapse session list"]')!) })
  expect(document.querySelectorAll('.session-tile')).toHaveLength(13)
 }
 await layoutFrames()
 for (const name of ['Transcript', 'Composer', 'SessionCard', 'CommitViewerBody', ...(tiles ? ['SessionTiles', 'SessionStatusGlyph'] : [])]) expect(profile.counts[name], name).toBeGreaterThan(0)
 const box = document.querySelector<HTMLTextAreaElement>('.agent-composer textarea')!
 const events: Record<string, number> = {}, keys: Record<string, number> = {}, times: number[] = [], eventTimes: number[] = [], keyTimes: number[] = [], renderTimes: number[] = []
 const add = (target: Record<string, number>, delta: Record<string, number>) => {
  for (const [name, count] of Object.entries(delta)) target[name] = (target[name] ?? 0) + count
 }
 holdTicks = true
 // Let a tick that was already scheduled render before the first measured batch.
 await new Promise<void>(resolve => setTimeout(resolve, 0))
 for (let i = 0; i < 30; i++) {
  await new Promise<void>(resolve => requestAnimationFrame(() => resolve()))
  await act(async () => {})
  const id = children[i % children.length].id
  // Measure the synchronous child event batch separately from key handling and layout.
  const start = performance.now(), durationStart = profile.durations.length
  let before = { ...profile.counts }
  act(() => {
   useTranscriptStore.getState().ingest({ type: 'agent_event', session_id: id, client_event_id: `child-${i}`, seq: i + 1, ts: new Date(1792000000000 + i).toISOString(), kind: 'assistant_text', payload: { text: `Building ${i}`, done: false } } as AgentEvent)
   emit({ type: 'session_background_changed', session_id: id, background_tasks: i + 1 })
   emit({ type: 'children_changed', session_id: 'sess-a' })
  })
  add(events, renderDelta(before))
  before = { ...profile.counts }
  const keyStart = performance.now()
  eventTimes.push(keyStart - start)
  fireEvent.change(box, { target: { value: box.value + 'a' } })
  box.getBoundingClientRect()
  const end = performance.now()
  keyTimes.push(end - keyStart)
  times.push(end - start)
  renderTimes.push(...profile.durations.slice(durationStart))
  add(keys, renderDelta(before))
 }
 console.warn('CHILD_PERF', width, tiles ? 'tiles' : 'cards', collapsed ? 'collapsed' : 'expanded', JSON.stringify({ maxEventMs: Math.max(...eventTimes), maxKeyMs: Math.max(...keyTimes), maxEventAndKeyMs: Math.max(...times), maxEventAndKeyRenderMs: Math.max(...renderTimes), events, keys }))
 expect.soft(keys).toEqual({ SessionComposer: 30, Composer: 30, ComposerChips: 30 })
 for (const name of [...isolated, 'SessionListBody', 'SessionTiles', 'SessionBranch']) expect.soft(events[name] ?? 0, name).toBe(0)
 expect.soft(events.SessionCard).toBe(collapsed ? 30 : 60) // visible affected child + parent, never a sibling
 expect.soft(events['SessionCard:sess-a']).toBe(30)
 for (const child of children) expect.soft(events[`SessionCard:${child.id}`] ?? 0).toBe(collapsed ? 0 : 5)
 if (tiles) {
  expect.soft(events.SessionTile).toBe(30)
  expect.soft(events['SessionTile:sess-a']).toBe(30)
  expect.soft(events.SessionStatusGlyph).toBe(30)
 }
 // Keep the original strict key + synchronous layout budget. Child delivery is a separate
 // browser task with its own bound; keep the combined span as a diagnostic.
 expect.soft(Math.max(...eventTimes), 'child-event batch').toBeLessThan(16)
 expect.soft(Math.max(...keyTimes)).toBeLessThan(16)
 // Advance the server-read clock explicitly: complete a telemetry-only /live refresh without
 // waiting for a wall-clock window. The retained parent checklist and viewer must stay asleep.
 const beforeRefresh = { ...profile.counts }
 perfLive.children![0].activity = 'Compiling the next target'
 const clock = vi.spyOn(Date, 'now').mockReturnValue(Date.now() + 6000)
 await act(async () => { emit({ type: 'children_changed', session_id: 'sess-a' }) })
 clock.mockRestore()
 const refreshCounts = renderDelta(beforeRefresh)
 console.warn('CHILD_REFRESH', width, tiles ? 'tiles' : 'cards', JSON.stringify(refreshCounts))
 for (const name of ['AgentChatView', 'ChatView', 'Transcript', 'Composer', 'HostedViewers', 'CommitViewerBody']) expect.soft(refreshCounts[name] ?? 0, name).toBe(0)
 // Reveal the Live tab after counting: the refreshed child must reach the Session panel too.
 if (width === 390) await userEvent.click(screen.getByTestId('rail-toggle'))
 const livePanel = width === 390 ? await screen.findByRole('dialog', { name: 'Session panel' }) : screen.getByTestId('session-rail')
 await userEvent.click(within(livePanel).getByRole('tab', { name: /Live/ }))
 const childRow = within(livePanel).getAllByTestId('rail-child').find(row => row.textContent?.includes('Builder 0'))!
 expect(within(childRow).getByTestId('child-activity')).toHaveTextContent('Compiling the next target')
 if (width === 390) await userEvent.click(within(livePanel).getByRole('button', { name: 'Close' }))

 // Non-layout updates still reach their card, and status changes still update the parent glyph.
 for (const message of [
  { type: 'session_meta_changed', session_id: 'builder-0', model: 'model-updated' },
  { type: 'session_title_changed', session_id: 'builder-0', title: 'Updated builder' },
  { type: 'session_state_changed', session_id: 'builder-0', status: 'waiting', unread: true },
 ] as const) {
  const before = { ...profile.counts }
  act(() => emit(message))
  const counts = renderDelta(before)
  console.warn('CHILD_EVENT', width, tiles ? 'tiles' : 'cards', message.type, JSON.stringify(counts))
  for (const name of [...isolated, 'SessionListBody', 'SessionTiles']) expect.soft(counts[name] ?? 0, name).toBe(0)
  for (let i = 0; i < 12; i++) for (const component of ['SessionCard', 'SessionTile', 'SessionStatusGlyph']) expect.soft(counts[`${component}:other-${i}`] ?? 0, `${component}:other-${i}`).toBe(0)
 }
 if (collapsed) expect(document.querySelector('[data-session-branch="builder-0"]')).toBeNull()
 else {
  expect(document.querySelector('[data-session-branch="builder-0"]')).toHaveTextContent('Updated builder')
  expect(document.querySelector('[data-session-branch="builder-0"] [title="model-updated"]')).toBeTruthy()
 }
 holdTicks = false
 await act(async () => { for (const tick of heldTicks.values()) tick() })
}, 60000)
