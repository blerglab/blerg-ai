// #250: time from switching back to a session to its first explorer row, with the explorer and
// panel cache (drawn from memory) and without it (the cache dropped: every part waits for its
// read, as before #250). The fake server answers each read after SERVER_MS, as a daemon does.
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import { useEffect } from 'react'
import { MemoryRouter, Route, Routes, useNavigate } from 'react-router-dom'
import { commands, page } from 'vitest/browser'
import { useTranscriptStore, type AgentEvent } from '@blerglab/chat'
import SessionDetail from './SessionDetail'
import { forgetMarkdown, markdownCacheForTest } from '../../../../packages/chat/src/components/Markdown'
import { useSessionStore } from '../hooks/useSessionStore'
import { explorerPrefKey } from '../hooks/useExplorerLayout'
import { makeDaemon, makeSession } from '../test/fixtures'
import { RAIL_PREF_KEY, RAIL_TAB_KEY } from '../lib/sessionRail'
import { invalidateChatTransport } from '../lib/chatTransport'
import { dropView } from '../lib/sessionViewCache'
import '../index.css'
import { longWorkspace, painted } from '../test/longWorkspace'
import SessionList from './SessionList'
import SessionTabBar from './SessionTabBar'
import { useSessionTabs } from '../hooks/useSessionTabs'
import { useExplorerLayout } from '../hooks/useExplorerLayout'

function MeasuredStrip() {
  const tabs = useSessionTabs('person-a', 'sess-a', false)
  const explorer = useExplorerLayout(false)
  return <SessionTabBar tabs={tabs} explorer={explorer} shellOpen={false} />
}

it('budgets session broadcasts with retained workers and three tab strips', async () => {
  longWorkspace('person-a')
  render(<MemoryRouter><MeasuredStrip /><MeasuredStrip /><MeasuredStrip /></MemoryRouter>)
  await painted()
  const times: number[] = []
  for (let i = 0; i < 30; i++) {
    const start = performance.now()
    act(() => useSessionStore.setState(s => ({ sessions: s.sessions.map(session => session.id === 'worker-0' ? { ...session, background_tasks: i + 1 } : session) })))
    times.push(performance.now() - start)
    await painted()
  }
  const median = [...times].sort((a, b) => a - b)[15]
  console.warn('WORKSPACE_BROADCAST', JSON.stringify({ medianMs: median, maxMs: Math.max(...times) }))
  expect(median).toBeLessThan(16)
}, 60_000)

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async original => ({ ...await original<typeof import('../authClient')>(), getAccessToken: () => `x.${btoa(JSON.stringify({ sub: 'person-a', exp: 9999999999 }))}.x`, ensureFreshToken: async () => true }))
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))
const timingsFile = (import.meta.env as Record<string, string | undefined>).VITE_SWITCH_TIMINGS

/** A daemon under load: the explorer's reads wait their turn (one per person at a time). */
const SERVER_MS = 250
const json = (body: unknown) => new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } })
const entries = Array.from({ length: 120 }, (_, i) => ({ name: `file-${i}.ts`, path: `file-${i}.ts`, directory: false }))
beforeEach(() => {
  localStorage.setItem('blerg-runner.session-dock.windows', JSON.stringify({ active: 'files', panels: [], ratio: .5 }))
  useTranscriptStore.setState({ sessions: {} })
  useSessionStore.setState({ daemons: [makeDaemon()], sessions: ['a', 'b'].map(x => makeSession({ id: `sess-${x}`, kind: 'agent', status: 'idle', title: x })) })
  localStorage.setItem(RAIL_PREF_KEY, '1')
  localStorage.setItem(RAIL_TAB_KEY, 'live')
  localStorage.setItem(explorerPrefKey('person-a'), JSON.stringify({ open: true, width: 320 }))
  apiFetch.mockReset().mockImplementation(async (url: string) => {
    await new Promise(r => setTimeout(r, SERVER_MS))
    if (url.includes('/workspace?')) return json({ result: { ok: true, launch_generation: 'launch', generation: 1, entries, truncated: false }, members: [], runtime: 'host' })
    if (url.endsWith('/permissions')) return json({ can_grant: true, reason: '', grants: [], mcp_tools: [], boards: [] })
    return json({})
  })
  vi.stubGlobal('fetch', vi.fn(async () => json([])))
})
afterEach(() => { invalidateChatTransport(); vi.unstubAllGlobals() })

const nav: { go: (path: string) => void } = { go: () => {} }
function Nav() { const navigate = useNavigate(); useEffect(() => { nav.go = navigate }, [navigate]); return null }
const go = (path: string) => nav.go(path)
const row = () => document.querySelector('.explorer-dock [role="tree"][aria-label="Workspace files"] [role="treeitem"]')
const permissions = () => screen.queryByText('None yet: this workspace asks you each time.')

it('budgets switching into a long live workspace with three panels', async () => {
  await page.viewport(3840, 2160)
  const busy = longWorkspace('person-a')
  const original = apiFetch.getMockImplementation()!
  apiFetch.mockImplementation((url: string) => {
    if (url.includes('/view?')) return Promise.resolve(json(busy.board))
    if (url.endsWith('/permissions')) return Promise.resolve(json(busy.permissions))
    if (url.startsWith('/api/insights/activity')) return Promise.resolve(json(busy.activity))
    if (url.startsWith('/api/insights')) return Promise.resolve(json(busy.insights))
    if (url.endsWith('/live')) return Promise.resolve(json(busy.live))
    return original(url)
  })
  render(<MemoryRouter initialEntries={['/sessions/sess-a']}><Nav /><div style={{ height: '100vh', display: 'flex' }}><aside><SessionList variant="sidebar" /></aside><div style={{ flex: 1, minWidth: 0 }}><Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes></div></div></MemoryRouter>)
  await screen.findByText('Task 400', {}, { timeout: 20_000 })
  await screen.findByText('Please continue task 49998')
  const times: number[] = []
  const formatted: number[] = []
  const dates = vi.spyOn(Intl.DateTimeFormat.prototype, 'formatToParts')
  const traceDir = import.meta.env.VITE_LAUNCH_SHOTS_DIR
  if (traceDir) await commands.workspaceTrace('start')
  for (let i = 0; i < 5; i++) {
    await act(async () => go('/sessions/sess-b'))
    await painted()
    performance.mark(`workspace-switch-${i}`)
    const start = performance.now()
    const beforeDates = dates.mock.calls.length
    act(() => go('/sessions/sess-a'))
    await screen.findByText('Please continue task 49998')
    await painted()
    times.push(performance.now() - start)
    formatted.push(dates.mock.calls.length - beforeDates)
    performance.measure(`workspace-switch-${i}`, `workspace-switch-${i}`)
  }
  const median = [...times].sort((a, b) => a - b)[2]
  dates.mockRestore()
  console.warn('WORKSPACE_SWITCH', JSON.stringify({ medianMs: median, maxMs: Math.max(...times), samples: times, formatted }))
  if (traceDir) {
    await commands.writeFile(`${traceDir}/switch.trace.json`, await commands.workspaceTrace('stop'))
    await commands.writeFile(`${traceDir}/switch.json`, JSON.stringify({ times, median, formatted }))
  } else {
    // A full first paint mounts 300 message rows and 400 cards in React's development build;
    // the small explorer-only cache check below has its separate SERVER_MS budget.
    expect(median).toBeLessThan(2000)
  }
  expect([...formatted].sort((a, b) => a - b)[2]).toBeLessThan(1000)
}, 120_000)

async function switchBack(cached: boolean): Promise<{ row: number; panel: number }> {
  await act(async () => go('/sessions/sess-b'))
  await waitFor(() => expect(row()).not.toBeNull())
  expect(row()).toHaveTextContent('file-')
  await act(async () => { await new Promise(r => setTimeout(r, 4 * SERVER_MS)) })
  if (!cached) dropView('sess-a')
  const start = performance.now()
  let firstRow = 0, panel = 0
  act(() => go('/sessions/sess-a'))
  await waitFor(() => {
    if (!firstRow && row()) firstRow = performance.now() - start
    if (!panel && permissions()) panel = performance.now() - start
    expect(firstRow && panel).toBeTruthy()
  }, { interval: 2, timeout: 5000 })
  await act(async () => { await new Promise(r => setTimeout(r, 4 * SERVER_MS)) })
  return { row: firstRow, panel }
}

it('switching back draws the first explorer row and the panel from memory, not after the reads', async () => {
  await page.viewport(1440, 900)
  render(<MemoryRouter initialEntries={['/sessions/sess-a']}><Nav /><div style={{ height: '100vh' }}><Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes></div></MemoryRouter>)
  await waitFor(() => expect(row()).not.toBeNull())
  await within(screen.getByTestId('session-rail')).findByText('None yet: this workspace asks you each time.')
  const before: { row: number; panel: number }[] = [], after: { row: number; panel: number }[] = []
  for (let i = 0; i < 3; i++) {
    before.push(await switchBack(false))
    after.push(await switchBack(true))
  }
  const median = (xs: number[]) => [...xs].sort((a, b) => a - b)[Math.floor(xs.length / 2)]
  const result = {
    serverMs: SERVER_MS,
    before: { firstRowMs: median(before.map(x => x.row)), panelMs: median(before.map(x => x.panel)) },
    after: { firstRowMs: median(after.map(x => x.row)), panelMs: median(after.map(x => x.panel)) },
  }
  if (timingsFile) await commands.writeFile(timingsFile, JSON.stringify(result, null, 2))
  // Without the cache the row waits for the listing; with it, it is there before any read answers.
  expect(result.before.firstRowMs).toBeGreaterThanOrEqual(SERVER_MS)
  expect(result.before.panelMs).toBeGreaterThanOrEqual(SERVER_MS)
  expect(result.after.firstRowMs).toBeLessThan(SERVER_MS)
  expect(result.after.panelMs).toBeLessThan(SERVER_MS)
}, 60_000)

// #306: exercise the shipped route, not just a Markdown component remount. The cold control
// removes only parsed Markdown; network/view caches and rendered message content stay the same.
it('reuses finished Markdown across session switches and measures main-thread work', async () => {
  await page.viewport(1440, 900)
  localStorage.setItem(explorerPrefKey('person-a'), JSON.stringify({ open: false, width: 320 }))
  const events = Array.from({ length: 600 }, (_, i) => ({
    session_id: 'sess-a', client_event_id: `markdown-${i}`, seq: i + 1,
    ts: '2026-09-01T12:00:00Z', kind: 'assistant_text',
    payload: { text: `## Completed task ${i}\n\nChecked **formatting**, [links](https://example.com) and \`code\`.\n\n- First result\n- Second result\n\n| Check | Result |\n| --- | --- |\n| Build ${i} | Passed |`, done: true },
  }) as AgentEvent)
  const transcript = useTranscriptStore.getState()
  transcript.ingestMany('sess-a', events)
  transcript.ingestReplayDone('sess-a', { lastSeq: 600, hasMore: false })
  const firstLoadStart = performance.now()
  render(<MemoryRouter initialEntries={['/sessions/sess-a']}><Nav /><div style={{ height: '100vh' }}><Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes></div></MemoryRouter>)
  await screen.findByText('Completed task 599')
  await painted()
  const firstLoadMs = performance.now() - firstLoadStart
  const content = () => document.querySelector('.agent-timeline')!.textContent
  const original = content()
  const samples: { cold: boolean; taskMs: number; scriptMs: number; layoutMs: number; styleMs: number; reparsed: number }[] = []
  const traceDir = import.meta.env.VITE_LAUNCH_SHOTS_DIR
  for (let i = 0; i < 10; i++) {
    await act(async () => go('/sessions/sess-b'))
    await painted()
    const cold = i % 2 === 1
    if (cold) forgetMarkdown()
    const before = markdownCacheForTest.misses()
    if (traceDir && i === 0) await commands.workspaceTrace('start')
    await commands.workspaceTaskTime('start')
    act(() => go('/sessions/sess-a'))
    await screen.findByText('Completed task 599')
    await painted()
    samples.push({ cold, ...await commands.workspaceTaskTime('stop'), reparsed: markdownCacheForTest.misses() - before })
    if (traceDir && i === 0) await commands.writeFile(`${traceDir}/markdown-switch.trace.json`, await commands.workspaceTrace('stop'))
    expect(content()).toBe(original)
  }
  const median = (values: number[]) => [...values].sort((a, b) => a - b)[Math.floor(values.length / 2)]
  const result = {
    firstLoadMs, profiled: !!traceDir,
    warmMedianMs: median(samples.filter(s => !s.cold).map(s => s.taskMs)),
    coldMedianMs: median(samples.filter(s => s.cold).map(s => s.taskMs)), samples,
  }
  console.warn('MARKDOWN_SWITCH', JSON.stringify(result))
  if (timingsFile) await commands.writeFile(timingsFile, JSON.stringify(result, null, 2))
  // Profiling changes execution cost; budget only the unprofiled measurements.
  if (!traceDir) expect(result.warmMedianMs).toBeLessThan(1600)
  expect(samples.filter(s => !s.cold).map(s => s.reparsed)).toEqual([0, 0, 0, 0, 0])
  expect(samples.filter(s => s.cold).every(s => s.reparsed >= 300)).toBe(true)
}, 120_000)

// Measure actual scheduled polling, without advancing/shortening its clock. The
// completion signal is the third board response (initial read plus two polls).
it('measures polling demand with a long workspace and three visible panels', async () => {
  await page.viewport(3840, 2160)
  const busy = longWorkspace('person-a')
  const requests: { route: string; at: number }[] = []
  const original = apiFetch.getMockImplementation()!
  apiFetch.mockImplementation((url: string) => {
    const at = performance.now()
    if (url.includes('/view?')) { requests.push({ route: 'board', at }); return Promise.resolve(json(busy.board)) }
    if (url.endsWith('/permissions')) { requests.push({ route: 'permissions', at }); return Promise.resolve(json(busy.permissions)) }
    if (url.startsWith('/api/insights/activity')) { requests.push({ route: 'activity', at }); return Promise.resolve(json(busy.activity)) }
    if (url.startsWith('/api/insights')) { requests.push({ route: 'insights', at }); return Promise.resolve(json(busy.insights)) }
    if (url.endsWith('/live')) { requests.push({ route: 'live', at }); return Promise.resolve(json(busy.live)) }
    return original(url)
  })
  const start = performance.now()
  render(<MemoryRouter initialEntries={['/sessions/sess-a']}><div style={{ height: '100vh' }}><Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes></div></MemoryRouter>)
  await screen.findByText('Task 400', {}, { timeout: 20_000 })
  await waitFor(() => expect(requests.filter(r => r.route === 'board').length).toBeGreaterThanOrEqual(3), { timeout: 40_000 })
  const counts = Object.fromEntries(['board', 'permissions', 'activity', 'insights', 'live'].map(route => [route, requests.filter(r => r.route === route).length]))
  const boardTimes = requests.filter(r => r.route === 'board').map(r => r.at)
  console.warn('WORKSPACE_POLLING', JSON.stringify({ elapsedMs: performance.now() - start, counts, boardIntervalsMs: boardTimes.slice(1).map((at, i) => at - boardTimes[i]) }))
  // One visible board schedules one poll chain, rather than one per card/worker.
  expect(counts.board).toBe(3)
}, 60_000)
