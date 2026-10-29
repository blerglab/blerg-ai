// Card #270: the explorer reads its workspace again by itself, quietly, when the agent's turn ends
// and when the page comes back; never while the person types in search or an explicit read is in
// flight, never while the tab is hidden.
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen } from '@testing-library/react'
import { useTranscriptStore, type AgentEvent } from '@blerglab/chat'
import WorkspaceTree from './WorkspaceTree'
import { inputClock } from '../../../../packages/chat/src/model/clock'
import { REFRESH_DEBOUNCE_MS } from '../lib/explorerAutoRefresh'

/** Long enough for anything still to come (a debounce, a deferral's recheck, a trailing refresh). */
const LONG = 10_000

const read = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch: read }))
vi.mock('../lib/chatTransport', () => ({ blergTransport: () => ({ files: null }) }))

const server = { added: [] as string[], changed: 1 }
const member = { id: 'app', name: 'app', path: 'app' }
function respond(url: string) {
  const query = new URL(url, 'https://example.com').searchParams
  const op = query.get('op'), path = query.get('path') ?? ''
  const entries = path === 'app'
    ? [{ name: 'index.ts', path: 'app/index.ts', directory: false }, ...server.added.map(name => ({ name, path: `app/${name}`, directory: false }))]
    : [{ name: 'app', path: 'app', directory: true, repository: true, member_id: 'app' }, { name: 'README.md', path: 'README.md', directory: false }]
  return new Response(JSON.stringify({ result: { ok: true, launch_generation: 'launch', generation: 1, entries, truncated: false, ...(op === 'status' ? { status: { branch: 'main', changed: server.changed, untracked: 0 } } : {}) }, members: [member], runtime: 'host' }))
}
const workspaceReads = () => read.mock.calls.filter(([url]) => String(url).includes('/workspace?')).length

let seq = 100
const ev = (kind: AgentEvent['kind'], payload: unknown): AgentEvent => ({ type: 'agent_event', session_id: 'session', client_event_id: `e${++seq}`, seq, ts: '2026-10-27T12:00:00Z', kind, payload })
const turnEnd = () => act(() => { useTranscriptStore.getState().ingest(ev('turn_done', {})) })
const advance = (ms: number) => act(async () => { await vi.advanceTimersByTimeAsync(ms) })

let visibility: DocumentVisibilityState = 'visible'
beforeEach(() => {
  visibility = 'visible'
  vi.spyOn(document, 'visibilityState', 'get').mockImplementation(() => visibility)
  Object.assign(server, { added: [], changed: 1 })
  read.mockReset().mockImplementation(async (url: string) => respond(url))
  // The session's transcript is loaded: what comes now is news.
  useTranscriptStore.setState({ sessions: {} })
  useTranscriptStore.getState().ingestMany('session', [ev('user_message', { text: 'go', source: 'chat' })])
  useTranscriptStore.getState().ingestReplayDone('session', { lastSeq: seq, hasMore: false })
})
afterEach(() => { vi.clearAllTimers(); vi.useRealTimers(); vi.restoreAllMocks() })

async function openApp() {
  render(<WorkspaceTree sessionId="session" active />)
  fireEvent.click(await screen.findByRole('treeitem', { name: 'app' }))
  await screen.findByText('index.ts')
  await screen.findByText(/main · 1 changed/)
  // Timers by hand from here (Testing Library's find* waits on real ones).
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
  read.mockClear()
}

it('reads the visible tree again quietly when the turn ends: new files and Git status appear without Loading', async () => {
  await openApp()
  server.added = ['NEW.ts']; server.changed = 2
  turnEnd()
  expect(workspaceReads()).toBe(0)
  await advance(REFRESH_DEBOUNCE_MS - 1)
  expect(workspaceReads()).toBe(0)
  expect(screen.queryByText('Loading…')).toBeNull()
  await advance(1)
  expect(screen.getByText('NEW.ts')).toBeInTheDocument()
  // The root, the open repository and its status: one quiet refresh, nothing more.
  expect(read.mock.calls.map(([url]) => decodeURIComponent(String(url)).replace(/.*workspace\?/, ''))).toEqual(['op=list&path=', 'op=list&path=app', 'op=status&path=app'])
  expect(screen.getByText(/main · 2 changed/)).toBeInTheDocument()
  expect(screen.queryByText('Loading…')).toBeNull()
  expect(screen.queryByText('Refreshing workspace…')).toBeNull()
  expect(screen.getByRole('treeitem', { name: 'app' })).toHaveAttribute('aria-expanded', 'true')
  await advance(LONG)
  expect(workspaceReads()).toBe(3)
})

it('reads again once when the tab becomes visible or the window regains focus', async () => {
  await openApp()
  act(() => { document.dispatchEvent(new Event('visibilitychange')); window.dispatchEvent(new Event('focus')) })
  await advance(LONG)
  expect(workspaceReads()).toBe(3)
})

it('waits while the person types in search, then reads', async () => {
  await openApp()
  let now = 1000
  vi.spyOn(inputClock, 'now').mockImplementation(() => now)
  const search = screen.getByRole('combobox', { name: 'Search workspace files' })
  search.focus()
  fireEvent.change(search, { target: { value: 'ind' } })
  turnEnd()
  await advance(LONG)
  expect(workspaceReads()).toBe(0)
  // The results list closed but the person typed a moment ago: still waiting.
  fireEvent.keyDown(search, { key: 'Escape' })
  await advance(3000)
  expect(workspaceReads()).toBe(0)
  now += 2000
  await advance(1000)
  expect(workspaceReads()).toBe(3)
})

it('with the tab hidden only marks the view stale, and reads when it is shown', async () => {
  await openApp()
  visibility = 'hidden'
  act(() => { document.dispatchEvent(new Event('visibilitychange')) })
  turnEnd()
  await advance(LONG)
  expect(workspaceReads()).toBe(0)
  visibility = 'visible'
  act(() => { document.dispatchEvent(new Event('visibilitychange')) })
  await advance(LONG)
  expect(workspaceReads()).toBe(3)
})

it('holds a refresh while an explicit read is in flight, and Refresh supersedes one asked before it', async () => {
  await openApp()
  let release: (() => void) | undefined
  let holding = true
  read.mockImplementation(async (url: string) => {
    if (holding && String(url).includes('op=list')) { holding = false; await new Promise<void>(r => { release = r }) }
    return respond(url)
  })
  // Asked before Refresh: Refresh reads everything, so it is not read again.
  turnEnd()
  await advance(0)
  fireEvent.click(screen.getByRole('button', { name: 'Refresh workspace' }))
  // Asked while Refresh's read is in flight: waits for it, then reads (the agent may have written after).
  turnEnd()
  await advance(LONG)
  expect(workspaceReads()).toBe(1)
  release!()
  await advance(0)
  expect(screen.getByText('Workspace refreshed.')).toBeInTheDocument()
  await advance(LONG)
  // Refresh closed the folders: the quiet read is the root's alone.
  expect(workspaceReads()).toBe(2)
})

it('an explicit Refresh does not bring back the quiet read it cancelled', async () => {
  await openApp()
  let held = true
  read.mockImplementation(async (url: string, init?: RequestInit) => {
    if (held && String(url).includes('op=list')) {
      held = false
      await new Promise<void>((_, reject) => init?.signal?.addEventListener('abort', () => reject(new DOMException('Cancelled', 'AbortError'))))
    }
    return respond(url)
  })
  turnEnd()
  await advance(REFRESH_DEBOUNCE_MS)
  expect(workspaceReads()).toBe(1) // the quiet read, in flight
  fireEvent.click(screen.getByRole('button', { name: 'Refresh workspace' }))
  await advance(0)
  expect(screen.getByText('Workspace refreshed.')).toBeInTheDocument()
  await advance(10_000)
  const roots = read.mock.calls.filter(([url]) => /op=list&path=$/.test(decodeURIComponent(String(url))))
  expect(roots).toHaveLength(2) // the cancelled quiet read and Refresh's: no third
})

it('a refresh that ends busy leaves the view stale; the next trigger tries once more, with no timed retries', async () => {
  await openApp()
  read.mockImplementation(async () => new Response(JSON.stringify({ error: 'The workspace explorer is busy.', code: 'busy' }), { status: 409 }))
  server.added = ['NEW.ts']
  turnEnd()
  await advance(60_000)
  // The request's own busy retries (eight attempts), then nothing more however long it waits.
  expect(workspaceReads()).toBe(8)
  await advance(10 * 60_000)
  expect(workspaceReads()).toBe(8)
  expect(screen.queryByText('NEW.ts')).toBeNull()
  read.mockImplementation(async (url: string) => respond(url))
  act(() => { window.dispatchEvent(new Event('focus')) })
  await advance(LONG)
  expect(screen.getByText('NEW.ts')).toBeInTheDocument()
  expect(screen.queryByText('The workspace explorer is busy.')).toBeNull()
  expect(workspaceReads()).toBe(8 + 3)
})

it('a tool call, however many, is not a trigger; only the turn end is', async () => {
  await openApp()
  act(() => {
    for (let i = 0; i < 20; i++) {
      useTranscriptStore.getState().ingest(ev('tool_call', { tool: 'Edit', call_id: `w${i}`, input: {} }))
      useTranscriptStore.getState().ingest(ev('tool_result', { call_id: `w${i}`, output: '', is_error: false, duration_ms: 1 }))
    }
  })
  await advance(LONG)
  expect(workspaceReads()).toBe(0)
  turnEnd()
  await advance(LONG)
  expect(workspaceReads()).toBe(3)
})

const busyReply = async () => new Response(JSON.stringify({ error: 'The workspace explorer is busy.', code: 'busy' }), { status: 409 })

it('a second turn end during a busy refresh is read after it', async () => {
  await openApp()
  read.mockImplementation(busyReply)
  turnEnd()
  await advance(500)
  turnEnd() // arrives while the first refresh waits out the busy server
  await advance(8500)
  // The first refresh's eight busy attempts are over, and the second turn end's refresh has begun.
  const first = workspaceReads()
  expect(first).toBeGreaterThan(8)
  server.added = ['SECOND.ts']
  read.mockImplementation(async (url: string) => respond(url))
  await advance(LONG)
  expect(screen.getByText('SECOND.ts')).toBeInTheDocument()
  // Its next attempt got through (the root), then the open repository and its status; then nothing.
  const done = workspaceReads()
  expect(done).toBe(first + 3)
  await advance(LONG * 6)
  expect(workspaceReads()).toBe(done)
})

it('showing the explorer after a busy refresh tries it once more and shows the new file', async () => {
  const observed: (() => void)[] = []
  vi.stubGlobal('ResizeObserver', class { constructor(callback: () => void) { observed.push(callback) } observe() {} unobserve() {} disconnect() {} })
  const view = render(<div data-testid="pane"><WorkspaceTree sessionId="session" active /></div>)
  fireEvent.click(await screen.findByRole('treeitem', { name: 'app' }))
  await screen.findByText(/main · 1 changed/)
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
  read.mockClear()
  read.mockImplementation(busyReply)
  turnEnd()
  await advance(LONG)
  expect(workspaceReads()).toBe(8)
  const pane = view.getByTestId('pane')
  const resized = () => act(() => { for (const callback of observed) callback() })
  pane.hidden = true; resized()
  server.added = ['SHOW.md']
  read.mockImplementation(async (url: string) => respond(url))
  // Another resize while hidden, or while shown, reads nothing.
  resized()
  await advance(LONG)
  expect(workspaceReads()).toBe(8)
  pane.hidden = false; resized()
  await advance(LONG)
  expect(screen.getByText('SHOW.md')).toBeInTheDocument()
  expect(workspaceReads()).toBe(8 + 3)
  resized()
  await advance(LONG)
  expect(workspaceReads()).toBe(8 + 3)
  vi.unstubAllGlobals()
})
