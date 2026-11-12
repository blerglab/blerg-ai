// #215: days of rows, including full checklist snapshots separated by other status rows.
import { beforeEach, afterEach, it, expect, onTestFinished, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { commands, page, userEvent } from 'vitest/browser'
import { longWorkspace, painted } from '../test/longWorkspace'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import '../index.css'
import { medianSample } from '../test/performanceSamples'
import { seedRichRail } from '../test/railFixture'
import { reelReply } from '../test/reelFixture'
import { RAIL_TAB_KEY } from '../lib/sessionRail'
import { useTranscriptStore, type AgentEvent } from '@blerglab/chat'
import { memo, Profiler, useEffect, useState } from 'react'
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
import SessionRail from './SessionRail'
import { useSessionStore } from '../hooks/useSessionStore'
import { makeSession } from '../test/fixtures'
import { makeLive } from '../test/planTabFixture'
import type { LiveReply } from '../lib/liveState'
import type { ProgressSubagent } from '../lib/liveState'
import type { BackgroundTask } from '@blerglab/chat'
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
// Holds setInterval/setTimeout registrations by *when* they are made, not by their delay: while
// "live" is off (the default — setup, tab switches, the gaps between keystrokes), a new
// registration is neutralized, so SessionRail's elapsed-time ticker and HistoryTab's and
// useLiveStateServer's minute refreshers can never tick on real wall-clock time, however long the
// browser task around them runs under load. While "live" is on (bracketing the exact keystroke
// dispatch), a new registration passes straight to the real timer at any delay: a future
// regression where typing itself schedules a timer still fires for real and shows up as a counted
// render, instead of being silently absorbed. While off, sub-second registrations also pass
// through for real — testing-library's and userEvent's own internal scheduling (waitFor/findBy*
// polling, pointer-event yields) lives well under a second, and every known product clock here
// (the 1 Hz ticker, the 30 s/60 s minute refreshers) is a full second or slower — except a tab's
// own one-shot "now" refresh (HistoryTab's useNow), a real setTimeout(0): captureZeroDelay()
// drains that deterministically in setup instead of leaving it to race a later measurement. See
// the guard test below, which proves both the hold and the "a keystroke's own timer still fires"
// halves of this — holding on cause, not on an exact allowlisted delay.
function manageTimers() {
 let live = false, capturing: Array<() => void> | null = null
 const realSetInterval = window.setInterval.bind(window), realSetTimeout = window.setTimeout.bind(window)
 const passThrough = (ms?: number) => live || ms === undefined || ms < 1000
 const intervalSpy = vi.spyOn(window, 'setInterval').mockImplementation(((handler: TimerHandler, ms?: number, ...args: unknown[]) =>
  passThrough(ms) ? realSetInterval(handler, ms, ...args) : realSetInterval(() => {}, ms)) as typeof window.setInterval)
 const timeoutSpy = vi.spyOn(window, 'setTimeout').mockImplementation(((handler: TimerHandler, ms?: number, ...args: unknown[]) => {
  if (ms === 0 && capturing && typeof handler === 'function') { capturing.push(() => (handler as (...a: unknown[]) => void)(...args)); return realSetTimeout(() => {}, 0) }
  return passThrough(ms) ? realSetTimeout(handler, ms, ...args) : realSetTimeout(() => {}, ms)
 }) as typeof window.setTimeout)
 return {
  restore: () => { intervalSpy.mockRestore(); timeoutSpy.mockRestore() },
  setLive: (v: boolean) => { live = v },
  captureZeroDelay: async (run: () => Promise<void>) => {
   const captured: Array<() => void> = []
   capturing = captured
   await run()
   capturing = null
   act(() => captured.forEach(fire => fire()))
  },
 }
}
async function clickTab(panel: HTMLElement, name: RegExp, clocks?: ReturnType<typeof manageTimers>) {
 const click = async () => { await userEvent.click(within(panel).getByRole('tab', { name })) }
 if (clocks) await clocks.captureZeroDelay(click)
 else await click()
}
async function history(width: number, clocks?: ReturnType<typeof manageTimers>) {
 if (width === 390) await userEvent.click(screen.getByTestId('rail-toggle'))
 const panel = width === 390 ? await screen.findByRole('dialog', { name: 'Workspace panel' }) : screen.getByTestId('session-rail')
 await clickTab(panel, /History/, clocks)
 await within(panel).findAllByTestId('stroke')
 return panel
}
async function typeAndMeasure(label: string, clocks: ReturnType<typeof manageTimers>) {
 // Drain tab activation and its promise-driven reads before attributing commits to typing.
 await act(async () => {})
 const box = document.querySelector<HTMLTextAreaElement>('.agent-composer textarea')!
 // A plain, detached control: its own cost is the shared machine's current price for bare DOM
 // work, measured in the same breath as the real keystroke so both carry the same contention.
 const probe = document.createElement('textarea')
 document.body.appendChild(probe)
 const names = ['ChatView', 'ChecklistRow', 'TicketCard', 'EventRow', 'HistoryTab', 'Transcript', 'SessionRail', 'SessionTabBar', 'CommitViewerBody']
 const counts = Object.fromEntries(names.map(name => [name, 0])), times: number[] = [], baseline: number[] = [], renders: number[] = []
 let composerRenders = 0
 for (let i = 0; i < 30; i++) {
  // Real keys arrive in separate browser tasks. Let the previous frame paint, then measure
  // every key including its synchronous layout, without charging unrelated clock ticks to it.
  await new Promise<void>(resolve => requestAnimationFrame(() => resolve()))
  await act(async () => {})
  clocks.setLive(false)
  const before = { ...profile.counts }, durationStart = profile.durations.length, start = performance.now()
  clocks.setLive(true)
  fireEvent.change(box, { target: { value: box.value + 'a' } })
  box.getBoundingClientRect()
  times.push(performance.now() - start)
  const baseStart = performance.now()
  fireEvent.change(probe, { target: { value: probe.value + 'a' } })
  probe.getBoundingClientRect()
  baseline.push(performance.now() - baseStart)
  renders.push(...profile.durations.slice(durationStart))
  for (const name of names) counts[name] += (profile.counts[name] ?? 0) - (before[name] ?? 0)
  composerRenders += (profile.counts.Composer ?? 0) - (before.Composer ?? 0)
 }
 clocks.setLive(false)
 probe.remove()
 console.warn('PERF', label, JSON.stringify({ median: medianSample(times), max: Math.max(...times), p95: [...times].sort((a,b) => a-b)[28], baselineMedian: medianSample(baseline), counts, maxRenderMs: Math.max(...renders) }))
 expect(composerRenders).toBe(30)
 return { counts, times, baseline }
}
it.each([390, 3840])('isolates typing with 2,000 strokes and 500 checklist snapshots at %ipx', async width => {
 const clocks = manageTimers()
 onTestFinished(clocks.restore)
 await page.viewport(width, width === 390 ? 844 : 2160)
 mount()
 const panel = await history(width, clocks)
 await waitFor(() => expect(document.querySelectorAll('[data-testid=checklist-row]').length).toBeGreaterThan(0))
 for (const el of [document.documentElement, screen.getByTestId('session-columns'), screen.getByTestId('session-chat-column')]) expect(el.scrollWidth).toBeLessThanOrEqual(el.clientWidth)
 for (const selector of ['.md-codeblock pre', '.md-table-wrap']) {
  const el = document.querySelector<HTMLElement>(selector)!
  expect(getComputedStyle(el).overflowX).toBe('auto')
  expect(el.textContent).toContain(selector.includes('table') ? 'y'.repeat(5000) : 'x'.repeat(5000))
 }
 for (const name of ['ChecklistRow', 'TicketCard', 'EventRow', 'Transcript', 'HistoryTab', 'SessionRail', 'SessionTabBar']) expect(profile.counts[name]).toBeGreaterThan(0)
 const a = await typeAndMeasure(`history-${width}`, clocks)
 // Finish the tab transition (including Activity's deferred hidden work) before timing typing.
 await act(async () => { await userEvent.click(within(panel).getByRole('tab', { name: /Live/ })) })
 expect(await within(panel).findByTestId('rail-context')).toHaveTextContent('90% · 180k of 200k')
 const b = await typeAndMeasure(`after-history-${width}`, clocks)
 await clickTab(panel, /History/, clocks)
 await userEvent.click(await within(panel).findByTestId('reel-commit'))
 await waitFor(() => expect(document.querySelectorAll('.diff-file').length).toBe(60))
 await userEvent.click(document.querySelector('.diff-file-toggle')!)
 await userEvent.click(screen.getByRole('button', { name: 'Show full file' }))
 await act(async () => { fireEvent.click(width === 390 ? screen.getByRole('button', { name: /← Main/ }) : screen.getByRole('tab', { name: /Main/ })) })
 expect(profile.counts.CommitViewerBody).toBeGreaterThan(0)
 const c = await typeAndMeasure(`after-viewer-${width}`, clocks)
 for (const result of [a,b,c]) {
  expect(Object.values(result.counts)).toEqual(Array(Object.keys(result.counts).length).fill(0))
  expect(medianSample(result.times)).toBeLessThan(16)
 }
}, 60000)
// Guards manageTimers() itself: an environment clock registered before typing starts must never
// tick on its own, however long real time elapses, while a timer a keystroke schedules for
// itself — at any delay, including one of the exact "held" values above — must still fire for
// real and be caught. Without this, holding by *when* a timer was registered could quietly
// regress back into holding by *value*, the failure mode this design was chosen to avoid.
it('still lets a keystroke-scheduled timer fire while holding an unrelated clock', async () => {
 // Both are memoized and take no props, exactly like the real HistoryTab/SessionRail: typing
 // re-rendering their parent must not, by itself, call their render function again.
 const MemoEnvClock = memo(function EnvClock() {
  const [, setTick] = useState(0)
  useEffect(() => { const t = setInterval(() => setTick(n => n + 1), 1000); return () => clearInterval(t) }, [])
  return <span data-testid="env-clock" />
 })
 const MemoLeaky = memo(function Leaky() {
  const [, setFired] = useState(0)
  // The regression under test: a keystroke (the custom event, dispatched synchronously from the
  // composer's own onChange below) schedules a real, delayed timer that later re-renders a
  // component with no other link to typing at all.
  useEffect(() => {
   const onKeystroke = () => setTimeout(() => setFired(n => n + 1), 1000)
   window.addEventListener('guard-keystroke', onKeystroke)
   return () => window.removeEventListener('guard-keystroke', onKeystroke)
  }, [])
  return <span data-testid="leaky" />
 })
 function Harness() {
  const [text, setText] = useState('')
  return <><MemoEnvClock /><MemoLeaky /><textarea data-testid="guard-box" value={text} onChange={e => { setText(e.target.value); window.dispatchEvent(new CustomEvent('guard-keystroke')) }} /></>
 }
 const clocks = manageTimers()
 onTestFinished(clocks.restore)
 profile.counts = {}; profile.durations = []
 const root = document.createElement('div')
 root.dataset.perfRoot = ''
 document.body.appendChild(root)
 render(<Profiler id="guard" onRender={recordRender}><Harness /></Profiler>, { container: root })
 const box = screen.getByTestId('guard-box') as HTMLTextAreaElement
 // Mounting alone renders both once; snapshot after mount, before typing, so the assertions
 // below prove a render happened *because of* the keystroke and the wait, not just at mount.
 const envBefore = profile.counts.EnvClock ?? 0, leakyBefore = profile.counts.Leaky ?? 0
 for (let i = 0; i < 5; i++) {
  await new Promise<void>(resolve => requestAnimationFrame(() => resolve()))
  await act(async () => {})
  clocks.setLive(true)
  fireEvent.change(box, { target: { value: box.value + 'a' } })
  box.getBoundingClientRect()
 }
 clocks.setLive(false)
 // The Leaky timer that the last keystroke scheduled is real and due in ~1s; the EnvClock
 // interval registered at mount, under the held phase, must stay silent for that whole wait.
 // live=true here only lets this test's own wait use a real timer; it does not retroactively
 // un-hold EnvClock's interval, which was already neutralized at registration time.
 clocks.setLive(true)
 await act(async () => { await new Promise(resolve => setTimeout(resolve, 1300)) })
 clocks.setLive(false)
 expect(profile.counts.EnvClock ?? 0).toBe(envBefore)
 expect(profile.counts.Leaky ?? 0).toBeGreaterThan(leakyBefore)
})
// BLG-431: SessionRail's 1 Hz ticker used to live at its own top (useTicker(timed)), so every real
// tick re-rendered the whole ~1,400-line component - Needs-you, Tickets, Checklist, Workspace
// settings, none of which show a clock - on top of the rows that actually advance. Mounted directly
// (not through SessionDetail/useLiveStateServer, which has its own real 5 s re-read of /live,
// unrelated to this ticker and not yet isolated - see BLG-431 investigation doc): that would
// confound this test with a second, separate real clock this change does not touch. Real
// setInterval/setTimeout run throughout; nothing here holds a clock.
function mountRail(subagents: ProgressSubagent[], tasks: BackgroundTask[]) {
 const root = document.createElement('div')
 root.dataset.perfRoot = ''
 document.body.appendChild(root)
 // A session id distinct from 'sess-a' (every other test in this file's fixtures): this mount
 // reads session-keyed global stores (useWorkingLineStore, useBackgroundTasks) directly, and must
 // not inherit or race whatever the shared beforeEach/other tests are doing to 'sess-a'.
 const session = makeSession({ id: 'rail-tick-test', status: 'running' })
 render(<Profiler id="session" onRender={recordRender}><MemoryRouter><div style={{ display: 'flex', height: 900 }}>
  <SessionRail id="rail" session={session} toolChanges={0} mode="panel" open onClose={() => {}} onOpenArtifact={() => {}}
   live={makeLive({ subagents })} tasks={tasks} />
 </div></MemoryRouter></Profiler>, { container: root })
}
it('ticks only the clock-bearing rail rows over 3 real seconds, never the rail itself', async () => {
 await page.viewport(3840, 2160)
 const subagent: ProgressSubagent = { key: 'a1', title: 'Survey the repo', type: 'Explore', model: '', tokens: null, doing: 'Reading files', startedAt: Date.now() - 5000, background: false }
 const task: BackgroundTask = { taskId: 't1', callId: 'c1', kind: 'bash', description: 'Run tests', command: 'npm test', startedAt: Date.now() - 5000, done: null }
 mountRail([subagent], [task])
 await screen.findByTestId('rail-subagent')
 await screen.findByTestId('rail-task')
 const before = { ...profile.counts }
 const start = performance.now()
 await act(async () => { await new Promise<void>(resolve => setTimeout(resolve, 3000)) })
 const elapsedS = (performance.now() - start) / 1000
 const counts: Record<string, number> = {}
 for (const [name, count] of Object.entries(profile.counts)) counts[name] = count - (before[name] ?? 0)
 // Rows with no clock of their own: untouched by 3 real seconds of SubagentRow/TaskRow ticking.
 for (const name of ['SessionRail', 'RailBody', 'HistoryTab', 'ChecklistRow', 'TicketCard']) expect(counts[name] ?? 0, name).toBe(0)
 // Rows that do show elapsed time tick at least once over the real wait (loaded machines can
 // coalesce or delay a 1 Hz interval; the point proved here is "more than zero", not an exact count).
 expect(elapsedS).toBeGreaterThan(2)
 for (const name of ['SubagentRow', 'TaskRow']) expect(counts[name] ?? 0, name).toBeGreaterThanOrEqual(1)
}, 10_000)
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
  const close = screen.queryByRole('dialog', { name: 'Workspace panel' })?.querySelector<HTMLButtonElement>('.rail-close')
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
 fireEvent.click(width === 390 ? screen.getByRole('button', { name: /← Main/ }) : screen.getByRole('tab', { name: /Main/ }))
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
it.each([390, 3840])('budgets typing in a long live workspace at %ipx', async width => {
 await page.viewport(width, width === 390 ? 844 : 2160)
 const busy = longWorkspace()
 const original = apiFetch.getMockImplementation()!
 apiFetch.mockImplementation((url: string, options?: RequestInit) => {
  if (url.includes('/view?')) return json(busy.board)
  if (url.endsWith('/permissions')) return json(busy.permissions)
  if (url.startsWith('/api/insights/activity')) return json(busy.activity)
  if (url.startsWith('/api/insights')) return json(busy.insights)
  if (url.endsWith('/live')) return json(busy.live)
  return original(url, options)
 })
 // Exercise the board's real poll callback during input without waiting for two 15 s windows.
 const realTimeout = window.setTimeout.bind(window)
 let pollBoard: (() => void) | undefined
 const timers = vi.spyOn(window, 'setTimeout').mockImplementation(((handler: TimerHandler, ms?: number, ...args: unknown[]) => {
  const id = realTimeout(handler, ms, ...args)
  if (ms === 15_000 && typeof handler === 'function') pollBoard = () => { clearTimeout(id); (handler as () => void)() }
  return id
 }) as typeof window.setTimeout)
 onTestFinished(() => timers.mockRestore())
 mount(true)
 await screen.findByText('Please continue task 49998')
 if (width === 3840) await screen.findByText('Task 400', {}, { timeout: 20_000 })
 await painted()
 const box = document.querySelector<HTMLTextAreaElement>('.agent-composer textarea')!
 const before = { ...profile.counts }, times: number[] = [], keys: number[] = [], events: number[] = []
 const keyRenders: Record<string, number> = {}, eventRenders: Record<string, number> = {}
 const add = (target: Record<string, number>, delta: Record<string, number>) => { for (const [name, count] of Object.entries(delta)) target[name] = (target[name] ?? 0) + count }
 const traceDir = import.meta.env.VITE_LAUNCH_SHOTS_DIR
 if (traceDir) await commands.workspaceTrace('start')
 for (let i = 0; i < 30; i++) {
  await painted()
  const eventBefore = { ...profile.counts }
  const start = performance.now()
  performance.mark(`workspace-key-${i}`)
  act(() => {
   const worker = `worker-${i % 5}`
   useTranscriptStore.getState().ingest({ session_id: worker, client_event_id: `busy-${i}`, seq: i + 1, ts: '2026-10-20T12:00:00Z', kind: 'assistant_text', payload: { text: `Building ${i}`, done: false } } as AgentEvent)
   emit({ type: 'session_background_changed', session_id: worker, background_tasks: i + 1 })
  })
  const keyStart = performance.now()
  events.push(keyStart - start)
  add(eventRenders, renderDelta(eventBefore))
  const keyBefore = { ...profile.counts }
  fireEvent.change(box, { target: { value: box.value + 'a' } })
  box.getBoundingClientRect()
  keys.push(performance.now() - keyStart)
  add(keyRenders, renderDelta(keyBefore))
  await painted()
  times.push(performance.now() - start)
  performance.measure(`workspace-key-${i}`, `workspace-key-${i}`)
 }
 const counts = renderDelta(before)
 // Native browser keys too: the event timestamp includes main-thread queueing and excludes
 // Playwright's command round trip. The synchronous samples above prove render isolation.
 const native: number[] = []
 const boardReads = () => apiFetch.mock.calls.filter(([url]) => String(url).includes('/view?')).length
 const beforePolls = boardReads()
 const delivery = new MessageChannel()
 onTestFinished(() => { delivery.port1.close(); delivery.port2.close() })
 box.focus()
 for (let i = 0; i < 30; i++) {
  let inputAt = 0
  box.addEventListener('keydown', event => { inputAt = event.timeStamp; performance.mark(`workspace-native-key-${i}`, { startTime: inputAt }) }, { once: true })
  // Queue a real message task and send the native key from Playwright concurrently. Chromium
  // can timestamp the key while its renderer is busy delivering the worker update.
  const delivered = new Promise<void>(resolve => { delivery.port1.onmessage = () => {
   const id = `worker-${i % 5}`
   act(() => {
    useTranscriptStore.getState().ingest({ session_id: id, client_event_id: `native-${i}`, seq: 100 + i, ts: '2026-10-20T12:00:00Z', kind: 'assistant_text', payload: { text: `Building ${i}`, done: false } } as AgentEvent)
    emit({ type: 'session_background_changed', session_id: id, background_tasks: 31 + i })
   })
   if (width === 3840 && (i === 10 || i === 20)) pollBoard?.()
   resolve()
  } })
  delivery.port2.postMessage(i)
  await Promise.all([delivered, commands.workspaceKey()])
  await painted()
  expect(inputAt).toBeGreaterThan(0)
  native.push(performance.now() - inputAt)
  performance.measure(`workspace-native-key-${i}`, `workspace-native-key-${i}`)
 }
 delivery.port1.close(); delivery.port2.close()
 if (width === 3840) expect(boardReads()).toBeGreaterThan(beforePolls)
 console.warn('WORKSPACE_NATIVE', JSON.stringify({ width, paintMedianMs: medianSample(native), paintMaxMs: Math.max(...native) }))
 console.warn('WORKSPACE_TYPING', JSON.stringify({ width, eventMedianMs: medianSample(events), keyMedianMs: medianSample(keys), paintMedianMs: medianSample(times), paintMaxMs: Math.max(...times), keyRenders, eventRenders, counts }))
 if (traceDir) {
  await commands.writeFile(`${traceDir}/typing-${width}.trace.json`, await commands.workspaceTrace('stop'))
  await page.screenshot({ path: `${traceDir}/workspace-${width}.png` })
  await commands.writeFile(`${traceDir}/typing-${width}.json`, JSON.stringify({ width, events, keys, batchPaint: times, nativePaint: native, keyRenders, eventRenders, counts }))
 }
 expect(keyRenders).toEqual({ SessionComposer: 30, Composer: 30, ComposerChips: 30 })
 for (const name of ['SessionDetail', 'AgentChatView', 'Transcript', 'SessionListBody']) expect(eventRenders[name] ?? 0, name).toBe(0)
 // The complete dev/profiler tree also updates the sidebar and live worker rows. The isolated
 // three-strip broadcast test retains the 16 ms budget for the selector regression itself.
 // CPU tracing changes scheduler/JS costs; collect it separately from the budget run.
 if (!traceDir) {
  expect(medianSample(events)).toBeLessThan(100)
  expect(medianSample(keys)).toBeLessThan(16)
  expect(medianSample(native)).toBeLessThan(100)
 }
}, 120_000)
it.each(childCases)('isolates typing and children streaming at $width px (tiles=$tiles, collapsed=$collapsed)', async ({ width, tiles, collapsed }) => {
 await page.viewport(width, width === 390 ? 844 : 2160)
 const children = Array.from({ length: 6 }, (_, i) => makeSession({ id: `builder-${i}`, title: `Builder ${i}`, parent_session_id: 'sess-a', started_by: { kind: 'session', name: 'Main' }, kind: 'agent', status: 'running' }))
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
 await act(async () => { fireEvent.click(width === 390 ? screen.getByRole('button', { name: /← Main/ }) : screen.getByRole('tab', { name: /Main/ })) })
 const close = document.querySelector('.rail-close')
 if (close) await act(async () => { fireEvent.click(close) })
 if (tiles) {
  await act(async () => { fireEvent.click(document.querySelector('[aria-label="Collapse workspace list"]')!) })
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
 console.warn('CHILD_PERF', width, tiles ? 'tiles' : 'cards', collapsed ? 'collapsed' : 'expanded', JSON.stringify({ medianEventMs: medianSample(eventTimes), medianKeyMs: medianSample(keyTimes), maxEventMs: Math.max(...eventTimes), maxKeyMs: Math.max(...keyTimes), maxEventAndKeyMs: Math.max(...times), maxEventAndKeyRenderMs: Math.max(...renderTimes), events, keys }))
 expect.soft(keys).toEqual({ SessionComposer: 30, Composer: 30, ComposerChips: 30 })
 for (const name of [...isolated, 'SessionListBody', 'SessionTiles', 'SessionBranch']) expect.soft(events[name] ?? 0, name).toBe(0)
 expect.soft(events.SessionCard).toBe(30) // only the parent; children live in the Sessions dock
 expect.soft(events['SessionCard:sess-a']).toBe(30)
 for (const child of children) expect.soft(events[`SessionCard:${child.id}`] ?? 0).toBe(0)
 if (tiles) {
  expect.soft(events.SessionTile).toBe(30)
  expect.soft(events['SessionTile:sess-a']).toBe(30)
  expect.soft(events['SessionStatusGlyph:sess-a']).toBe(30)
 }
 // Keep the 16 ms key + synchronous layout budget on the median of 30 samples.
 // Maxima above diagnose scheduler interruptions on shared machines. Child delivery is a separate
 // browser task with its own bound; keep the combined span as a diagnostic.
 expect.soft(medianSample(eventTimes), 'median child-event batch').toBeLessThan(16)
 expect.soft(medianSample(keyTimes), 'median typing sample').toBeLessThan(16)
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
 const livePanel = width === 390 ? await screen.findByRole('dialog', { name: 'Workspace panel' }) : screen.getByTestId('session-rail')
 await userEvent.click(within(livePanel).getByRole('tab', { name: /Live/ }))
 const childRow = within(livePanel).getAllByTestId('rail-child').find(row => row.textContent?.includes('Builder 0'))!
 expect(within(childRow).getByTestId('child-activity')).toHaveTextContent('Compiling the next target')
 if (width === 390) await userEvent.click(within(livePanel).getByRole('button', { name: 'Close' }))

 // Non-layout updates still reach the store and panel, without restoring sidebar child cards.
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
 expect(document.querySelector('[data-session-branch="builder-0"]')).toBeNull()
 expect(useSessionStore.getState().sessions.find(session => session.id === 'builder-0')).toMatchObject({ title: 'Updated builder', model: 'model-updated', status: 'waiting', unread: true })
 holdTicks = false
 await act(async () => { for (const tick of heldTicks.values()) tick() })
}, 60000)

it('keeps the 400-card board asleep on worker telemetry broadcasts', async () => {
 await page.viewport(3840, 2160)
 const busy = longWorkspace()
 const original = apiFetch.getMockImplementation()!
 apiFetch.mockImplementation((url: string, options?: RequestInit) => {
  if (url.includes('/view?')) return json(busy.board)
  if (url.endsWith('/permissions')) return json(busy.permissions)
  if (url.startsWith('/api/insights/activity')) return json(busy.activity)
  if (url.startsWith('/api/insights')) return json(busy.insights)
  if (url.endsWith('/live')) return json(busy.live)
  return original(url, options)
 })
 mount(true)
 await screen.findByText('Task 400', {}, { timeout: 20_000 })
 await painted()
 await screen.findByText('Please continue task 49998')
 await act(async () => {})
 const counts: Record<string, number> = {}, times: number[] = []
 for (let i = 0; i < 30; i++) {
  const before = { ...profile.counts }
  const start = performance.now()
  act(() => emit({ type: 'session_background_changed', session_id: `worker-${i % 5}`, background_tasks: i + 1 }))
  times.push(performance.now() - start)
  // Attribute only the store delivery itself. Late reads, clocks and board
  // polls between frames are legitimate independent updates, measured elsewhere.
  for (const [name, count] of Object.entries(renderDelta(before))) counts[name] = (counts[name] ?? 0) + count
  await painted()
 }
 console.warn('BOARD_TELEMETRY', JSON.stringify({ medianMs: medianSample(times), counts }))
 for (const name of ['SessionBoardTabs', 'BoardTab', 'BoardCard']) expect(counts[name] ?? 0, name).toBe(0)
 // A field actually consumed by the board must still reach its subscriber.
 const kindBefore = { ...profile.counts }
 act(() => useSessionStore.setState(s => ({ sessions: s.sessions.map(session => session.id === 'worker-0' ? { ...session, worker_kind: 'review' } : session) })))
 const kindCounts = renderDelta(kindBefore)
 expect(kindCounts.BoardTab ?? 0).toBeGreaterThan(0)
 const shots = import.meta.env.VITE_LAUNCH_SHOTS_DIR
 if (shots) {
  await painted()
  await page.screenshot({ path: `${shots}/board-workspace-3840.png` })
  await page.viewport(390, 844)
  await painted()
  await page.screenshot({ path: `${shots}/board-workspace-390.png` })
 }
}, 60_000)
