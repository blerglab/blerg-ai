import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act, render, screen, fireEvent, waitFor, within } from '@testing-library/react'
import { MemoryRouter, Routes, Route } from 'react-router-dom'
import { useTranscriptStore, type AgentEvent } from '@blerglab/chat'
import SessionDetail from './SessionDetail'
import { useSessionStore } from '../hooks/useSessionStore'
import { useToastStore } from '../hooks/useToastStore'
import { useMessageStore } from '../hooks/useMessageStore'
import * as wsMock from '../test/wsMock'
import { makeDaemon, makeSession } from '../test/fixtures'
import { RAIL_PREF_KEY, RAIL_TAB_KEY } from '../lib/sessionRail'
import type { SessionInfo } from '../types'
import type { LiveReply } from '../lib/liveState'
import { OpenSessionContext } from '../lib/openSession'

vi.mock('../ws', () => import('../test/wsMock'))

// The session page's side panel: what needs you, what runs, the session's facts; a column beside
// the chat on a wide screen, a drawer on a narrow one.

const MIN = 60_000

// A page width: matchMedia answers (max-width: N px) queries for it.
function viewport(width: number) {
  window.matchMedia = vi.fn().mockImplementation((query: string) => {
    const m = /max-width:\s*(\d+)px/.exec(query)
    return {
      matches: m ? width <= Number(m[1]) : false, media: query, onchange: null,
      addEventListener: () => {}, removeEventListener: () => {}, addListener: () => {}, removeListener: () => {}, dispatchEvent: () => false,
    }
  }) as unknown as typeof window.matchMedia
}

function seedSession(over: Partial<SessionInfo> = {}) {
  const session = makeSession({ limit_resume_supported: true, kind: 'agent', status: 'running', title: 'Ship it', model: 'claude-opus-5-5', effort: 'high', runtime: 'docker', started_at: new Date(Date.now() - 135 * MIN).toISOString(), ...over })
  useSessionStore.setState({ daemons: [makeDaemon()], sessions: [session], statusChangedAt: { [session.id]: Date.now() }, serverVersion: 'test' })
  return session
}

let seq = 0
function ev(kind: string, payload: unknown, agoMs = 5 * MIN): AgentEvent {
  seq++
  return { session_id: 'sess-a', client_event_id: `r${seq}`, seq, ts: new Date(Date.now() - agoMs).toISOString(), kind, payload } as AgentEvent
}

const user = () => ev('user_message', { text: 'go', source: 'chat' }, 10 * MIN)
const ask = () => ev('ask', { body: 'Deploy to staging first?', message_id: 'm1' }, 3 * MIN)
const subagent = () => [
  ev('tool_call', { tool: 'Agent', call_id: 'A1', input: { description: 'Survey the repo', prompt: 'Look around', subagent_type: 'Explore' } }, 4 * MIN),
  ev('subagent_started', { child_id: 'A1', agent_type: 'Explore', model: '', summary: '' }, 4 * MIN),
  ev('subagent_update', { child_id: 'A1', agent_type: 'Explore', model: 'claude-haiku-5', summary: '', usage: { input_tokens: 12000, output_tokens: 400, cache_read_tokens: 0, cache_write_tokens: 0 } }, 2 * MIN),
  ev('tool_call', { tool: 'Bash', call_id: 'k1', input: { command: 'npm test' }, parent_call_id: 'A1' }, 1 * MIN),
]
const task = (id = 'b1', call = 'c1', description = 'Wait for the deploy') => [
  ev('tool_call', { tool: 'Bash', call_id: call, input: { command: 'make deploy', run_in_background: true } }, 2 * MIN),
  ev('tool_result', { call_id: call, output: 'started', is_error: false, duration_ms: 1 }, 2 * MIN),
  ev('background_task_started', { task_id: id, call_id: call, kind: 'bash', description, started_at: new Date(Date.now() - 65_000).toISOString() }, 65_000),
]

function seedEvents(...events: AgentEvent[]) {
  useTranscriptStore.setState({ sessions: {} })
  const s = useTranscriptStore.getState()
  for (const e of events) s.ingest(e)
  s.ingestReplayDone('sess-a', { lastSeq: seq, hasMore: false })
}

const iso = (offsetMs: number) => new Date(Date.now() + offsetMs).toISOString()

// GET /api/sessions/{id}/live's reply.
function liveReply(over: Partial<LiveReply> = {}): LiveReply {
  return {
    session_id: 'sess-a', through_seq: 100, generated_at: iso(0), tokens_hidden: false, asks: [], subagents: [], tasks: [],
    tool_changes: 0, counts: { needs_you: 0, running: 0 }, ...over,
  }
}

// GET /api/tool-changes answers `changes` pending changes on the session. GET /live answers `live`
// as JSON, or, without it, as a server that cannot (the page then reads the chat's loaded events, as
// it does on an older server). Anything else is empty.
let fetchMock: ReturnType<typeof vi.fn>
function stubFetch(changes = 0, live?: LiveReply) {
  fetchMock = vi.fn((url: string) => {
    if (String(url).includes('/live')) {
      return Promise.resolve(live
        ? { ok: true, status: 200, headers: new Headers({ 'Content-Type': 'application/json', ETag: '"1"' }), json: () => Promise.resolve(live) }
        : { ok: false, status: 404, headers: new Headers({ 'Content-Type': 'text/plain; charset=utf-8' }), json: () => Promise.reject(new SyntaxError('not JSON')) })
    }
    if (String(url).endsWith('/limit-resume')) return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({ enabled: false, state: 'disabled', reason: '', attempts: 0 }) })
    if (String(url).endsWith('/plan-nudges')) return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({ enabled: true }) })
    const body = String(url).includes('/api/tool-changes')
      ? { tool_changes: Array.from({ length: changes }, (_, i) => ({ id: `t${i}`, kind: 'changed', muted: false })) }
      : {}
    return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(body) })
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}
const fetchCalls = () => fetchMock.mock.calls.map(c => String(c[0]))

// Renders the page and lets its first read of the live state answer.
async function renderPage() {
  const r = render(
    <MemoryRouter initialEntries={['/sessions/sess-a']}>
      <Routes>
        <Route path="/sessions/:id" element={<SessionDetail />} />
        <Route path="/proposals" element={<div>PROPOSALS</div>} />
      </Routes>
    </MemoryRouter>,
  )
  await act(async () => { for (let k = 0; k < 10; k++) await Promise.resolve() })
  return r
}

const toggle = () => screen.getByTestId('rail-toggle')
// The panel toggle's counts, as shown: "?2 🤖3 ↳1" (needs you, work inside, child sessions); '' with none.
const badges = () => ['rail-badge-inside', 'rail-badge-children'].map(id => screen.queryByTestId(id)).filter(el => el !== null)
  .map(el => el.textContent).join(' ')
const rail = () => screen.queryByTestId('session-rail')
const desktop = window.matchMedia

describe('session side panel', () => {
  let scrolled: ReturnType<typeof vi.fn>
  beforeEach(() => {
    seq = 0
    wsMock.resetWsMock()
    wsMock.send.mockReturnValue(true)
    useSessionStore.setState({ daemons: [], sessions: [], statusChangedAt: {}, serverVersion: '', pendingSessionIds: {}, failedSessions: {} })
    useToastStore.setState({ toasts: [] })
    useMessageStore.setState({ messages: [] })
    useTranscriptStore.setState({ sessions: {} })
    localStorage.removeItem(RAIL_PREF_KEY)
    localStorage.removeItem(RAIL_TAB_KEY)
    stubFetch()
    viewport(1280)
    scrolled = vi.fn()
    Element.prototype.scrollIntoView = scrolled as unknown as Element['scrollIntoView']
  })
  afterEach(() => {
    vi.useRealTimers()
    vi.unstubAllGlobals()
    window.matchMedia = desktop
    delete (Element.prototype as { scrollIntoView?: unknown }).scrollIntoView
    document.body.style.overflow = ''
  })

  it('keeps successive pending proposals even with no proposal events in the loaded transcript', async () => {
    seedSession({ status: 'idle' })
    seedEvents()
    const previous = fetchMock.getMockImplementation() as (url: string) => Promise<unknown>
    let titles = ['First draft']
    fetchMock.mockImplementation((url: string) => {
      if (String(url).startsWith('/api/proposals?')) return Promise.resolve({ ok: true, status: 200, json: async () => ({ proposals: titles.map((title, n) => ({ id: `p${n}`, state: 'pending', kind: 'board_file', tool: 'file_ticket', connection_name: 'Board', session_id: 'sess-a', arguments: { title, type: 'bug', body: 'Full draft body' }, arguments_raw: '{}', expires_at: new Date(Date.now() + 7 * 86400_000).toISOString(), created_at: new Date().toISOString() })) }) })
      return previous(url)
    })
    await renderPage()
    const needs = await screen.findByTestId('rail-needs')
    expect(await within(needs).findByText('First draft')).toBeTruthy()
    titles = ['First draft', 'Second draft']
    fireEvent(window, new Event('focus'))
    expect(await within(needs).findByText('Second draft')).toBeTruthy()
    expect(within(needs).getByText('First draft')).toBeTruthy()
    expect(screen.getByTestId('rail-needs-count')).toHaveTextContent('2')
    // The toggle counts no needs-you (owner rule, 2026-10-26).
    expect(screen.getByTestId('rail-toggle').getAttribute('aria-label')).not.toMatch(/need/)
  })

  it('names the tool and connection a proposal runs before the title the agent wrote', async () => {
    seedSession({ status: 'idle' })
    seedEvents()
    const previous = fetchMock.getMockImplementation() as (url: string) => Promise<unknown>
    fetchMock.mockImplementation((url: string) => {
      if (String(url).startsWith('/api/proposals?')) return Promise.resolve({ ok: true, status: 200, json: async () => ({ proposals: [{ id: 'p-mismatch', state: 'pending', kind: 'mcp', tool: 'delete_branch', connection_name: 'Source host', session_id: 'sess-a', arguments: { title: 'Fix a typo', branch: 'main' }, arguments_raw: '{"title":"Fix a typo","branch":"main"}', expires_at: new Date(Date.now() + 7 * 86400_000).toISOString(), created_at: new Date().toISOString() }] }) })
      return previous(url)
    })
    await renderPage()
    const needs = await screen.findByTestId('rail-needs')
    const action = await within(needs).findByTestId('proposal-action-p-mismatch')
    expect(action.textContent).toBe('Runs delete_branch on Source host')
    const title = within(needs).getByTestId('proposal-agent-title-p-mismatch')
    expect(title.textContent).toBe('Title in the agent’s request: Fix a typo')
    expect(within(needs).queryByRole('heading', { name: 'Fix a typo' })).toBeNull()
    expect(action.compareDocumentPosition(title) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  it('uses an elapsed reasoning clock on the Running now session card', async () => {
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(new Date('2026-10-01T00:00:00Z'))
    seedSession(); seedEvents(user())
    stubFetch(0, liveReply({ children: [{ id: 'thinking-session', title: 'Reasoning fixture', engine: 'hermes', model: 'fixture', status: 'running', started_at: iso(-3 * MIN), ended_at: null, asking: false, tokens: null, activity: 'Thinking…', activity_since: iso(-83_000) }] }))
    await renderPage()
    expect(screen.getByTestId('rail-running')).toHaveTextContent('Thinking… 1:23')
  })

  it('shows live reasoning counts and elapsed fallback on the native child working line', async () => {
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(new Date('2026-10-01T00:00:00Z'))
    seedSession()
    seedEvents(user())
    stubFetch(0, liveReply({ subagents: [{ key: 'child', title: 'Reasoning fixture', type: '', model: '', tokens: null, doing: '', started_at: iso(-MIN), active_at: iso(0), background: false, seq: 1, reasoning: { since: Date.now() - 83_000, tokens: null } }] }))
    await renderPage()
    const row = within(screen.getByTestId('rail-running')).getByTestId('rail-subagent')
    expect(row).toHaveTextContent('Thinking… 1:23')
    act(() => useTranscriptStore.getState().ingest({ type: 'agent_event', session_id: 'sess-a', client_event_id: 'thinking-count', ts: iso(0), kind: 'thinking_progress', payload: { parent_call_id: 'child', tokens: 1234 }, transient: true }))
    expect(row).toHaveTextContent('Thinking… 1,234 tokens')
    act(() => useTranscriptStore.getState().ingest({ type: 'agent_event', session_id: 'sess-a', client_event_id: 'thinking-end', ts: iso(0), kind: 'thinking_done', payload: { parent_call_id: 'child' }, transient: true }))
    expect(row).not.toHaveTextContent('Thinking…')
  })

  it('keeps a subagent reasoning count to the owner rule: the clock for a viewer who may not see tokens', async () => {
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(new Date('2026-10-01T00:00:00Z'))
    seedSession()
    seedEvents(user())
    stubFetch(0, liveReply({ tokens_hidden: true, subagents: [{ key: 'child', title: 'Reasoning fixture', type: '', model: '', tokens: null, doing: '', started_at: iso(-MIN), active_at: iso(0), background: false, seq: 1, reasoning: { since: Date.now() - 83_000, tokens: null } }] }))
    await renderPage()
    const row = within(screen.getByTestId('rail-running')).getByTestId('rail-subagent')
    act(() => useTranscriptStore.getState().ingest({ type: 'agent_event', session_id: 'sess-a', client_event_id: 'thinking-count', ts: iso(-83_000), kind: 'thinking_progress', payload: { parent_call_id: 'child', tokens: 1234 }, transient: true }))
    expect(row).toHaveTextContent('Thinking… 1:23')
    expect(row).not.toHaveTextContent('1,234')
  })

  describe('progress bars', () => {
    const progress = { completed: 2, total: 5, label: 'Run tests', updated_at: iso(0) }
    it('shows separate session and child progress, with accessible bounds', async () => {
      seedSession()
      seedEvents(user())
      stubFetch(0, liveReply({ progress, subagents: [{ key: 'child', title: 'Security audit', type: 'reviewer', model: '', tokens: null, doing: '', started_at: iso(-MIN), active_at: iso(0), background: false, seq: 1, progress: { ...progress, completed: 1, total: 3, label: 'Check tokens' } }] }))
      await renderPage()
      const running = within(screen.getByTestId('rail-running'))
      const root = within(running.getByTestId('rail-session-progress'))
      expect(root.getByText('2/5')).toBeInTheDocument()
      expect(root.getByText('Run tests')).toBeInTheDocument()
      expect(root.getByRole('progressbar')).toHaveAttribute('aria-valuenow', '2')
      expect(root.getByRole('progressbar')).toHaveAttribute('aria-valuemax', '5')
      const child = within(running.getByTestId('rail-subagent'))
      expect(child.getByText('1/3')).toBeInTheDocument()
      expect(child.getByText('Check tokens')).toBeInTheDocument()
    })
    it('updates, completes and clears from authoritative replies after progress events', async () => {
      vi.useFakeTimers({ toFake: ['Date'] })
      seedSession()
      seedEvents(user())
      stubFetch(0, liveReply({ progress }))
      await renderPage()
      expect(within(screen.getByTestId('rail-session-progress')).getByText('2/5')).toBeInTheDocument()
      seq = 100
      for (const [completed, label] of [[3, 'Build'], [5, 'Done']] as const) {
        stubFetch(0, liveReply({ through_seq: seq + 1, progress: { ...progress, completed, label } }))
        vi.advanceTimersByTime(6000)
        act(() => useTranscriptStore.getState().ingest(ev('progress_updated', { completed, total: 5, label }, 0)))
        await waitFor(() => expect(within(screen.getByTestId('rail-session-progress')).getByText(`${completed}/5`)).toBeInTheDocument())
      }
      expect(within(screen.getByTestId('rail-session-progress')).getByRole('progressbar')).toHaveClass('complete')
      stubFetch(0, liveReply({ through_seq: seq + 1 }))
      vi.advanceTimersByTime(6000)
      act(() => useTranscriptStore.getState().ingest(ev('plan_updated', { items: [] }, 0)))
      await waitFor(() => expect(screen.queryByTestId('rail-session-progress')).not.toBeInTheDocument())
    })
    it('shows a finished green bar, and does not show bars without progress', async () => {
      seedSession()
      seedEvents(user())
      stubFetch(0, liveReply({ progress: { ...progress, completed: 5, label: 'Finished' } }))
      await renderPage()
      const bar = within(screen.getByTestId('rail-session-progress')).getByRole('progressbar')
      expect(bar).toHaveClass('complete')
      expect(bar).toHaveAttribute('aria-valuenow', '5')
    })
    it('keeps Running now free of invented progress on old-server replies', async () => {
      seedSession()
      seedEvents(user())
      stubFetch(0, liveReply())
      await renderPage()
      expect(within(screen.getByTestId('rail-running')).queryByRole('progressbar')).not.toBeInTheDocument()
      expect(screen.queryByTestId('rail-session-progress')).not.toBeInTheDocument()
    })
  })

  it.each(['claude', 'codex', 'future-engine'])('places %s automatic resume once, in the panel Session section', async engine => {
    seedSession({ engine, limit_resume_supported: true })
    seedEvents(user())
    await renderPage()
    expect(screen.queryByTestId('rail-settings')).toBeNull()
    expect(within(screen.getByTestId('rail-session')).getByRole('checkbox', { name: 'Resume when limit resets' })).toBeInTheDocument()
    expect(screen.getAllByRole('checkbox', { name: 'Resume when limit resets' })).toHaveLength(1)
    expect(fetchCalls().filter(url => url.endsWith('/limit-resume'))).toEqual(['/api/sessions/sess-a/limit-resume'])
  })

  it('does not offer automatic resume to engines without support', async () => {
    seedSession({ engine: 'hermes', limit_resume_supported: false })
    seedEvents(user())
    await renderPage()
    expect(within(screen.getByTestId('rail-session')).queryByRole('checkbox', { name: 'Resume when limit resets' })).not.toBeInTheDocument()
    expect(fetchCalls().filter(url => url.endsWith('/limit-resume'))).toEqual([])
  })

  it('offers the plan nudge setting in the panel Session section, on by default', async () => {
    seedSession({ engine: 'hermes', limit_resume_supported: false })
    seedEvents(user())
    await renderPage()
    const box = await within(screen.getByTestId('rail-session')).findByRole('checkbox', { name: 'Nudge when the plan drifts' })
    expect(box).toBeChecked()
    expect(fetchCalls().filter(url => url.endsWith('/plan-nudges'))).toEqual(['/api/sessions/sess-a/plan-nudges'])
  })

  describe('on a wide screen', () => {
    it('is open by default, a column of the page with no badge while nothing runs or needs you; the toggle closes it and remembers', async () => {
      seedSession({ status: 'idle' })
      seedEvents(user())
      await renderPage()
      expect(localStorage.getItem(RAIL_PREF_KEY)).toBeNull()
      expect(rail()).toBeVisible()
      expect(toggle()).toHaveAttribute('aria-expanded', 'true')
      expect(toggle()).toHaveAccessibleName('Session panel')
      expect(badges()).toBe('')
      // Not a popover or a dialog: a column of the page.
      expect(rail()!.tagName).toBe('ASIDE')
      expect(rail()).not.toHaveAttribute('role', 'dialog')
      expect(toggle()).toHaveAttribute('aria-controls', rail()!.id)
      expect(within(rail()!).getByTestId('rail-needs')).toHaveTextContent('Nothing needs you.')
      expect(within(rail()!).getByTestId('rail-running')).toHaveTextContent('Idle.')
      expect(within(rail()!).queryByTestId('rail-main')).toBeNull()

      fireEvent.click(toggle())
      expect(rail()).not.toBeVisible()
      expect(toggle()).toHaveAttribute('aria-expanded', 'false')
      expect(localStorage.getItem(RAIL_PREF_KEY)).toBe('0')

      fireEvent.click(toggle())
      expect(rail()).toBeVisible()
      expect(localStorage.getItem(RAIL_PREF_KEY)).toBe('1')
    })

    it('on a wide screen the shared header chevron collapses to a rail and the header toggle reopens it', async () => {
      seedSession({ status: 'idle' })
      seedEvents(user())
      localStorage.setItem(RAIL_PREF_KEY, '1')
      await renderPage()
      expect(rail()).toBeVisible()
      fireEvent.click(toggle())
      expect(screen.queryByRole('button', { name: 'Toggle session panel' })).toBeNull()
      expect(rail()).not.toBeVisible()
      expect(localStorage.getItem(RAIL_PREF_KEY)).toBe('0')
    })

    it('is open when something runs and no choice was made; a closed choice wins', async () => {
      seedSession({ status: 'idle' })
      seedEvents(user(), ...task())
      const { unmount } = await renderPage()
      expect(rail()).toBeVisible()
      expect(localStorage.getItem(RAIL_PREF_KEY)).toBeNull()
      unmount()
      localStorage.setItem(RAIL_PREF_KEY, '0')
      await renderPage()
      expect(rail()).not.toBeVisible()
      // The toggle still says what runs.
      expect(badges()).toBe('🤖1')
      expect(toggle()).toHaveAccessibleName('Session panel: 1 subagent or background task')
    })

    it('once closed by hand, opens by itself again only for something new that needs you, once', async () => {
      vi.useFakeTimers({ toFake: ['Date'] })
      seedSession({ status: 'idle' })
      seedEvents(user(), ...task())
      await renderPage()
      expect(rail()).toBeVisible()
      fireEvent.click(toggle())
      expect(rail()).not.toBeVisible()
      vi.advanceTimersByTime(1000)
      // More running is not something new that needs you.
      act(() => { for (const e of task('b2', 'c2', 'Watch the log')) useTranscriptStore.getState().ingest(e) })
      expect(rail()).not.toBeVisible()
      // A question asked after the close is.
      act(() => { useTranscriptStore.getState().ingest(ev('ask', { body: 'Merge now?', message_id: 'm9' }, 0)) })
      expect(rail()).toBeVisible()
      // Closed again, the same question does not bring it back.
      vi.advanceTimersByTime(1000)
      fireEvent.click(toggle())
      expect(rail()).not.toBeVisible()
      act(() => { useTranscriptStore.getState().ingest(ev('assistant_text', { text: 'Waiting for you.', done: true }, 0)) })
      expect(rail()).not.toBeVisible()
      expect(localStorage.getItem(RAIL_PREF_KEY)).toBe('0')
      vi.useRealTimers()
    })

    it('a question already there when the page opens with the panel closed is not new', async () => {
      seedSession({ status: 'waiting' })
      seedEvents(user(), ask())
      localStorage.setItem(RAIL_PREF_KEY, '0')
      await renderPage()
      expect(rail()).not.toBeVisible()
      // The toggle has no needs-you count (owner rule): the question is on Needs you.
      expect(badges()).toBe('')
      expect(within(rail()!).getByTestId('rail-needs-count')).toHaveTextContent('1')
    })

    it('lists what the server reads from the whole log on first paint, with no transcript loaded', async () => {
      stubFetch(0, liveReply({
        asks: [{ event_id: 'srv-ask', message_id: 'm7', question: 'Deploy to production?', asked_at: iso(-8 * MIN), seq: 4 }],
        subagents: [{ key: 'A0', title: 'Watch the deploy', type: 'general-purpose', model: 'claude-sonnet-5', tokens: 2500, doing: 'running Bash · kubectl get pods',
          started_at: iso(-90 * MIN), active_at: iso(-1 * MIN), background: true, seq: 2 },
        { key: 'S1', title: 'Fix the tests', type: 'general-purpose', model: '', tokens: null, doing: '',
          started_at: iso(-5 * MIN), active_at: iso(-1 * MIN), background: false, resumed: true, seq: 5 }],
        tasks: [{ task_id: 'b0', call_id: 'c0', kind: 'monitor', description: 'Tail the deploy log', started_at: iso(-80 * MIN), seq: 3 }],
        tool_changes: 1,
      }))
      seedSession({ status: 'idle', background_tasks: 2 })
      useTranscriptStore.setState({ sessions: {} })
      await renderPage()
      await waitFor(() => expect(badges()).toBe('🤖3'))
      const panel = rail()!
      expect(within(panel).getByTestId('rail-needs-count')).toHaveTextContent('2')
      expect(within(panel).getByTestId('rail-running-count')).toHaveTextContent('3')
      expect(within(panel).getByTestId('rail-ask')).toHaveTextContent('Deploy to production?')
      expect(within(panel).getByTestId('rail-tool-changes')).toHaveTextContent('1 tool change waits on your decision')
      const [sub, resumed] = within(panel).getAllByTestId('rail-subagent')
      // A resumed subagent's run says so.
      expect(within(resumed).getByTestId('rail-sub-resumed')).toHaveAccessibleName('Resumed')
      expect(within(sub).queryByTestId('rail-sub-resumed')).toBeNull()
      expect(sub).toHaveTextContent('Watch the deploy')
      expect(sub).toHaveClass('background')
      expect(within(sub).getByTestId('rail-sub-tokens')).toHaveTextContent(/^2\.5k$/)
      expect(within(sub).getByTestId('rail-sub-tokens').getAttribute('title')).toMatch(/^Tokens so far: 2\.5k tokens\nBreakdown unavailable/)
      expect(within(sub).getByTestId('rail-sub-doing')).toHaveTextContent('running Bash · kubectl get pods')
      expect(within(sub).getByTestId('rail-elapsed')).toHaveTextContent(/^1:(29:5\d|30:0\d)$/)
      const t = within(panel).getByTestId('rail-task')
      expect(t).toHaveTextContent('Tail the deploy log')
      expect(within(t).getByTestId('rail-elapsed')).toHaveTextContent(/^1:(19:5\d|20:0\d)$/)
      // Nothing "started earlier than what is loaded": the server read it all.
      expect(panel).not.toHaveTextContent('started earlier')
      // The page reads its tool changes from the live state, not on its own.
      expect(fetchCalls().some(u => u.includes('/api/tool-changes'))).toBe(false)
    })

    it('lists nothing from the loaded events while the server\'s first answer is on its way', async () => {
      let answer!: (r: unknown) => void
      stubFetch(0)
      fetchMock.mockImplementation((url: string) => (String(url).includes('/live')
        ? new Promise(r => { answer = r })
        : Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({}) })))
      seedSession({ status: 'running' })
      seedEvents(user(), ask(), ...subagent())
      await renderPage()
      // Nothing counts before the server answers (the main agent at work is not a count on the toggle).
      expect(badges()).toBe('')
      await act(async () => {
        answer({ ok: true, status: 200, headers: new Headers({ 'Content-Type': 'application/json' }), json: () => Promise.resolve(liveReply({ through_seq: seq, tool_changes: 3 })) })
        for (let k = 0; k < 10; k++) await Promise.resolve()
      })
      // The server's reading wins: it holds no ask or subagent, whatever the transcript drew.
      expect(badges()).toBe('')
      expect(within(rail()!).getByTestId('rail-needs-count')).toHaveTextContent('3')
    })

    it('moves at once with what comes after the server\'s reply, before it reads again', async () => {
      stubFetch(0, liveReply({ through_seq: 0 }))
      seedSession({ status: 'running' })
      seedEvents()
      await renderPage()
      await waitFor(() => expect(fetchCalls().some(u => u.includes('/live'))).toBe(true))
      act(() => { useTranscriptStore.getState().ingest(ev('subagent_started', { child_id: 'A9', agent_type: 'Explore', summary: 'Survey the repo' }, 0)) })
      await waitFor(() => expect(badges()).toBe('🤖1'))
      expect(within(rail()!).getByTestId('rail-subagent')).toHaveTextContent('Survey the repo')
      act(() => { useTranscriptStore.getState().ingest(ev('subagent_done', { child_id: 'A9', summary: 'ok' }, 0)) })
      // The main agent still works (Running now lists it), but the toggle counts only the work inside.
      expect(badges()).toBe('')
    })

    it('says what the badge counts to a screen reader as it changes', async () => {
      seedSession({ status: 'idle' })
      seedEvents(user())
      await renderPage()
      const live = screen.getByTestId('rail-announce')
      expect(live).toHaveAttribute('aria-live', 'polite')
      expect(live).toHaveTextContent(/^Session panel$/)
      // A question is not a toggle count (it shows on Needs you); work inside the session is.
      act(() => { useTranscriptStore.getState().ingest(ev('ask', { body: 'Merge now?', message_id: 'm9' }, 0)) })
      expect(live).toHaveTextContent(/^Session panel$/)
      act(() => { useTranscriptStore.getState().ingest(ev('subagent_started', { child_id: 'A7', agent_type: 'Explore', summary: 'Survey' }, 0)) })
      expect(live).toHaveTextContent('Session panel: 1 subagent or background task')
    })

    it('lists what needs you and what runs, with counts, and the badge adds them up', async () => {
      stubFetch(1)
      seedSession()
      seedEvents(user(), ask(), ...subagent(), ...task())
      await renderPage()
      const panel = rail()!
      await waitFor(() => expect(within(panel).getByTestId('rail-needs-count')).toHaveTextContent('2'))
      // The main agent at work counts in Running now (its subagent and task under it) and in the
      // header's badge, its label and announcement.
      expect(within(panel).getByTestId('rail-running-count')).toHaveTextContent('3')
      expect(within(panel).getByTestId('rail-main')).toHaveAttribute('data-state', 'working')
      expect(badges()).toBe('🤖2')
      expect(screen.getByTestId('rail-badge-inside')).not.toHaveClass('attention')
      expect(toggle()).toHaveAccessibleName('Session panel: 2 subagents and background tasks')
      expect(screen.getByTestId('rail-announce')).toHaveTextContent('Session panel: 2 subagents and background tasks')

      const askRow = within(panel).getByTestId('rail-ask')
      expect(askRow).toHaveTextContent('Deploy to staging first?')
      expect(within(askRow).getByTestId('rail-ask-wait')).toHaveTextContent('waiting 3 min')
      expect(within(askRow).getByLabelText('Your answer')).toBeInTheDocument()

      const changes = within(panel).getByTestId('rail-tool-changes')
      expect(changes).toHaveTextContent('1 tool change waits on your decision')
      expect(within(changes).getByRole('link', { name: 'Review on the Proposals page' })).toHaveAttribute('href', '/proposals?session=sess-a')

      const sub = within(panel).getByTestId('rail-subagent')
      expect(sub).toHaveTextContent('Survey the repo')
      // The type is in the title's tooltip; the model and tokens are glyphs with their words in tooltips.
      expect(within(sub).getByTestId('rail-show').getAttribute('title')).toContain('Explore')
      expect(within(sub).getByTestId('rail-sub-model')).toHaveAccessibleName('Model: haiku-5')
      // The chat's own reading (no /live here) has no owner/admin decision: no tokens, chip or hover.
      expect(within(sub).queryByTestId('rail-sub-tokens')).toBeNull()
      expect(sub.textContent).not.toMatch(/12k|12\.4k/)
      expect(within(sub).getByTestId('rail-sub-doing')).toHaveTextContent('running Bash · npm test')
      expect(within(sub).getByTestId('rail-elapsed')).toHaveTextContent('04:00')
      // A foreground subagent keeps the solid border.
      expect(sub).not.toHaveClass('background')
      expect(sub).toHaveAttribute('data-status', 'running')

      const t = within(panel).getByTestId('rail-task')
      expect(t).toHaveTextContent('Wait for the deploy')
      expect(within(t).getByTestId('rail-show').getAttribute('title')).toContain('started')
      expect(within(t).getByTestId('rail-elapsed')).toHaveTextContent('01:05')
      expect(within(t).getByRole('img', { name: 'Background command' })).toBeInTheDocument()

      // No row carries the chat's anchors: "Show in conversation" searches the whole page and must
      // find the chat's card, never the panel's row.
      expect(panel.querySelector('[data-event-id], [data-call-id], [data-call-ids]')).toBeNull()
    })

    it('an idle session with background tasks has no header chip: the panel says what runs', async () => {
      seedSession({ status: 'idle', background_tasks: 2 })
      seedEvents(user())
      localStorage.setItem(RAIL_PREF_KEY, '1')
      await renderPage()
      expect(screen.queryByTestId('detail-status')).toBeNull()
      expect(screen.queryByText(/working in background/)).toBeNull()
      expect(within(rail()!).queryByTestId('rail-status')).toBeNull()
    })

    it('a background shell command reads as a command: glyph, the command, the description, shell · elapsed · status, no model or effort', async () => {
      seedSession({ status: 'running' })
      seedEvents(user(), ...subagent(), ev('background_task_started', {
        task_id: 'b1', call_id: 'c1', kind: 'bash', description: 'Wait for the deploy', command: 'bash /srv/app/run.sh --wait',
        started_at: new Date(Date.now() - 65_000).toISOString(),
      }, 65_000))
      localStorage.setItem(RAIL_PREF_KEY, '1')
      await renderPage()
      const t = within(rail()!).getByTestId('rail-task')
      expect(within(t).getByRole('img', { name: 'Background command' })).toHaveTextContent('>_')
      expect(within(t).getByTestId('rail-command')).toHaveTextContent('$ bash /srv/app/run.sh --wait')
      const tip = within(t).getByTestId('rail-show').getAttribute('title')
      expect(tip).toContain('$ bash /srv/app/run.sh --wait')
      expect(within(t).getByTestId('rail-task-note')).toHaveTextContent('Wait for the deploy')
      expect(tip).toContain('shell · running')
      expect(within(t).getByTestId('rail-elapsed')).toHaveTextContent(/^01:0\d$/)
      expect(within(t).getByRole('img', { name: 'Running' })).toBeInTheDocument()
      expect(t).not.toHaveTextContent(/effort/)
      // The subagent keeps its own look: the agent glyph, model, effort.
      const sub = within(rail()!).getByTestId('rail-subagent')
      expect(within(sub).getByRole('img', { name: 'Subagent' })).toHaveTextContent('🤖')
      // The panel toggle's work-inside count carries the same robot face as this row (owner, #245).
      expect(screen.getByTestId('rail-badge-inside').querySelector('.icon-btn-count-glyph')!.textContent).toBe(within(sub).getByRole('img', { name: 'Subagent' }).textContent)
      // It reports no effort of its own: it runs at the session's, which is said, never "unknown".
      expect(within(sub).getByTestId('rail-show').getAttribute('title')).toContain("effort: high (session's)")
    })

    it('an older start with only a description shows that as the command line', async () => {
      seedSession({ status: 'running' })
      seedEvents(user(), ...task('b1', 'c1', 'Wait for the deploy'))
      localStorage.setItem(RAIL_PREF_KEY, '1')
      await renderPage()
      const t = within(rail()!).getByTestId('rail-task')
      expect(within(t).getByTestId('rail-command')).toHaveTextContent(/^Wait for the deploy$/)
      expect(within(t).queryByTestId('rail-task-note')).toBeNull()
    })

    it('an idle session whose live state has a task running lists it in the panel and counts it on the toggle, with no header chip', async () => {
      seedSession({ status: 'idle' })
      seedEvents(user())
      stubFetch(0, liveReply({
        tasks: [{ task_id: 'b1', call_id: 'c1', kind: 'bash', description: 'Wait for the deploy', started_at: iso(-MIN), seq: 90 }],
        counts: { needs_you: 0, running: 1 },
      }))
      localStorage.setItem(RAIL_PREF_KEY, '1')
      await renderPage()
      await waitFor(() => expect(within(rail()!).getByTestId('rail-task')).toHaveTextContent('Wait for the deploy'))
      expect(badges()).toBe('🤖1')
      expect(within(rail()!).getByTestId('rail-running-count')).toHaveTextContent('1')
      expect(screen.queryByTestId('detail-status')).toBeNull()
      expect(within(rail()!).queryByTestId('rail-status')).toBeNull()
    })

    it('the live state wins over the server\'s count: nothing running live reads idle, and the count is the live one', async () => {
      seedSession({ status: 'idle', background_tasks: 2 })
      seedEvents(user())
      stubFetch(0, liveReply())
      localStorage.setItem(RAIL_PREF_KEY, '1')
      await renderPage()
      await waitFor(() => expect(screen.queryByTestId('detail-status')).toBeNull())
      expect(screen.queryByTestId('detail-status')).toBeNull()
    })

    it('a running session does not duplicate its status in Session', async () => {
      seedSession({ status: 'running', background_tasks: 1 })
      seedEvents(user())
      localStorage.setItem(RAIL_PREF_KEY, '1')
      await renderPage()
      expect(within(rail()!).queryByTestId('rail-status')).toBeNull()
      expect(screen.queryByTestId('detail-status')).toBeNull()
    })

    it('keeps Insights only in the header and avoids duplicate panel links', async () => {
      seedSession({ status: 'idle' })
      seedEvents(user())
      localStorage.setItem(RAIL_PREF_KEY, '1')
      await renderPage()
      const facts = within(rail()!).getByTestId('rail-session')
      expect(within(facts).getByLabelText('Runs on')).toHaveTextContent('Local sandbox · workstation')
      expect(within(facts).getByTestId('rail-engine')).toHaveTextContent('Claude')
      expect(within(facts).getByLabelText('Model')).toHaveValue('claude-opus-5-5')
      expect(within(facts).getByLabelText('Effort')).toHaveValue('high')
      expect(within(facts).getByTestId('rail-lifetime')).toHaveTextContent('2 h 15 min')
      expect(within(facts).queryByTestId('rail-status')).toBeNull()
      // Idle with nothing in the background: the header names no state.
      expect(screen.queryByTestId('detail-status')).toBeNull()

      // No links to Insights, Files or Capabilities: the header's Insights button and the panel's own
      // tabs are the only way to them, so the section does not repeat them.
      for (const id of ['rail-open-insights', 'rail-open-files', 'rail-open-capabilities']) {
        expect(within(facts).queryByTestId(id)).toBeNull()
      }
      expect(within(facts).queryByRole('button', { name: 'Insights' })).toBeNull()
      expect(screen.getAllByRole('button', { name: 'Insights' })).toHaveLength(1)
      expect(within(facts).queryByRole('button', { name: /Files|Capabilities/ })).toBeNull()
    })

    it('"Show in conversation" brings the subagent card, the task\'s call and the ask card into view', async () => {
      seedSession()
      seedEvents(user(), ask(), ...subagent(), ...task())
      await renderPage()
      const panel = rail()!
      fireEvent.click(within(within(panel).getByTestId('rail-subagent')).getByTestId('rail-show'))
      expect((scrolled.mock.contexts.at(-1) as HTMLElement).dataset.testid).toBe('subagent-card')
      fireEvent.click(within(within(panel).getByTestId('rail-task')).getByTestId('rail-show'))
      expect((scrolled.mock.contexts.at(-1) as HTMLElement).dataset.callId).toBe('c1')
      fireEvent.click(within(within(panel).getByTestId('rail-ask')).getByTestId('rail-show'))
      expect((scrolled.mock.contexts.at(-1) as HTMLElement).dataset.testid).toBe('ask-card')
      expect(rail()).toBeVisible()
    })

    it('a Running now card is two lines, has no separate link, and a click anywhere on it brings it into view', async () => {
      seedSession()
      seedEvents(user(), ...subagent(), ...task())
      await renderPage()
      const panel = rail()!
      const sub = within(panel).getByTestId('rail-subagent')
      expect(sub.querySelectorAll(':scope > .rail-run-line')).toHaveLength(2)
      expect(within(panel).queryByText('Show in conversation')).toBeNull()
      // The title is the control, named for what it does, the rest in its description.
      expect(within(sub).getByTestId('rail-show')).toHaveAttribute('aria-description', expect.stringMatching(/^Runs inside Main\. Show in conversation\. /))
      scrolled.mockClear()
      fireEvent.click(within(sub).getByTestId('rail-elapsed'))
      expect((scrolled.mock.contexts.at(-1) as HTMLElement).dataset.testid).toBe('subagent-card')
      // A tap on the step line shows it whole in place instead (touch has no tooltip), and folds it again.
      scrolled.mockClear()
      const step = within(sub).getByTestId('rail-sub-doing')
      fireEvent.click(step)
      expect(step).toHaveAttribute('aria-expanded', 'true')
      expect(scrolled).not.toHaveBeenCalled()
      fireEvent.click(step)
      expect(step).toHaveAttribute('aria-expanded', 'false')
      scrolled.mockClear()
      fireEvent.click(within(panel).getByTestId('rail-task').querySelector('.rail-clock')!)
      expect((scrolled.mock.contexts.at(-1) as HTMLElement).dataset.callId).toBe('c1')
    })

    it('a control of its own on a Running now card does not jump', async () => {
      seedSession({ status: 'idle' })
      seedEvents(user(), ev('background_task_started', { task_id: 'b9', call_id: 'c9', kind: 'bash', description: 'Run it', command: 'make it', started_at: new Date(Date.now() - MIN).toISOString() }, MIN),
        ev('background_task_done', { task_id: 'b9', call_id: 'c9', kind: 'bash', status: 'failed', summary: 'exit 2' }, 0))
      await renderPage()
      const t = within(rail()!).getByTestId('rail-task')
      scrolled.mockClear()
      fireEvent.click(within(t).getByRole('button', { name: 'Dismiss' }))
      expect(scrolled).not.toHaveBeenCalled()
    })

    it('says so when the row is not in the part of the conversation the chat has drawn', async () => {
      seedSession({ status: 'idle' })
      // A task whose call is on an older page: only its start is held.
      seedEvents(user(), ev('background_task_started', { task_id: 'b9', call_id: 'gone', kind: 'monitor', description: 'Watch the log', started_at: new Date(Date.now() - MIN).toISOString() }, MIN))
      await renderPage()
      fireEvent.click(within(rail()!).getByTestId('rail-show'))
      expect(within(rail()!).getByRole('status')).toHaveTextContent('Not in the part of the conversation the chat has drawn. Scroll up in the chat to reach it.')
    })

    it('does not tell the owner they cannot answer a question while ownership is unknown', async () => {
      seedSession({ status: 'running' })
      seedEvents(user(), ev('ask', { body: 'Ship blue?', message_id: 'q1', owner_only: true, no_wait: true }, 2 * MIN))
      await renderPage()
      const row = within(rail()!).getByTestId('rail-ask')
      expect(row).toHaveTextContent('not blocking')
      expect(row).not.toHaveTextContent('Only the session owner')
      expect(within(row).getByLabelText('Your answer')).toBeInTheDocument()
    })

    it('answers an ask from the panel, as the ask card would', async () => {
      seedSession({ status: 'waiting' })
      seedEvents(user(), ask())
      await renderPage()
      const row = within(rail()!).getByTestId('rail-ask')
      fireEvent.change(within(row).getByLabelText('Your answer'), { target: { value: 'yes, staging' } })
      wsMock.send.mockClear()
      await act(async () => { fireEvent.click(within(row).getByRole('button', { name: 'Answer' })) })
      const sent = wsMock.send.mock.calls.map(c => c[0]).find(m => m.type === 'agent_user_message') as { text: string; answer_message_id?: string } | undefined
      expect(sent?.text).toBe('yes, staging')
      expect(sent?.answer_message_id).toBe('m1')
      // The answer's echo closes the ask: it leaves the panel.
      const clientId = (sent as unknown as { client_id: string }).client_id
      act(() => { useTranscriptStore.getState().ingest(ev('user_message', { text: 'yes, staging', source: 'ask_answer', message_id: 'm1', client_id: clientId }, 0)) })
      expect(within(rail()!).queryByTestId('rail-ask')).toBeNull()
      expect(within(rail()!).getByTestId('rail-needs')).toHaveTextContent('Nothing needs you.')
    })

    it('keeps a failed task until it is dismissed', async () => {
      seedSession({ status: 'idle' })
      seedEvents(user(), ...task())
      await renderPage()
      act(() => {
        useTranscriptStore.getState().ingest(ev('background_task_done', { task_id: 'b1', call_id: 'c1', kind: 'bash', status: 'failed', summary: 'Background command "make deploy" failed with exit code 2' }, 0))
      })
      const t = within(rail()!).getByTestId('rail-task')
      expect(t).toHaveClass('failed')
      expect(within(t).getByRole('img', { name: 'failed' })).toBeInTheDocument()
      expect(within(t).getByTestId('rail-task-summary')).toHaveTextContent('failed with exit code 2')
      fireEvent.click(within(t).getByTestId('rail-dismiss'))
      expect(within(rail()!).queryByTestId('rail-task')).toBeNull()
    })

    it('shows a background subagent once, as a subagent, not again as a task', async () => {
      seedSession({ status: 'idle' })
      seedEvents(
        user(),
        ev('tool_call', { tool: 'Agent', call_id: 'A2', input: { description: 'Watch the build', prompt: 'Watch', subagent_type: 'general-purpose', run_in_background: true } }, 3 * MIN),
        ev('subagent_started', { child_id: 'A2', agent_type: 'general-purpose', model: '', summary: '' }, 3 * MIN),
        ev('subagent_done', { child_id: 'A2', agent_type: 'general-purpose', model: '', summary: 'launched', background: true }, 3 * MIN),
        ev('tool_result', { call_id: 'A2', output: 'Async agent launched', is_error: false, duration_ms: 1 }, 3 * MIN),
        ev('background_task_started', { task_id: 'g1', call_id: 'A2', kind: 'agent', description: 'Watch the build', started_at: new Date(Date.now() - 3 * MIN).toISOString() }, 3 * MIN),
        ev('turn_done', { stop_reason: 'end_turn', model: 'm', usage: {} }, 2 * MIN),
      )
      await renderPage()
      expect(within(rail()!).getAllByTestId('rail-subagent')).toHaveLength(1)
      expect(within(within(rail()!).getByTestId('rail-subagent')).getByRole('img', { name: 'Running in the background' })).toBeInTheDocument()
      expect(within(within(rail()!).getByTestId('rail-subagent')).getByTestId('rail-show').getAttribute('title')).toContain('in the background')
      // Dashed, like its card in the transcript.
      expect(within(rail()!).getByTestId('rail-subagent')).toHaveClass('background')
      expect(within(rail()!).getByTestId('rail-subagent')).toHaveAttribute('data-status', 'background')
      expect(within(rail()!).queryByTestId('rail-task')).toBeNull()
      expect(within(rail()!).getByTestId('rail-running-count')).toHaveTextContent('1')
    })

    describe('read from the server', () => {
      const replyAsk = (asked: number) => ({ event_id: 'srv-m1', message_id: 'm1', question: 'Deploy to staging first?', asked_at: iso(asked), seq: 2 })
      const replyTask = { task_id: 'b0', call_id: 'c0', kind: 'bash', description: 'Wait for the deploy', started_at: iso(-60 * MIN), seq: 1 }

      it('once closed by hand, opens by itself again only for something new that needs you, once', async () => {
        vi.useFakeTimers({ toFake: ['Date'] })
        seedSession({ status: 'idle' })
        seedEvents(user())
        stubFetch(0, liveReply({ through_seq: seq, tasks: [replyTask] }))
        await renderPage()
        expect(rail()).toBeVisible()
        fireEvent.click(toggle())
        expect(rail()).not.toBeVisible()
        vi.advanceTimersByTime(1000)
        // More running (folded after the reply) is not something new that needs you.
        act(() => { useTranscriptStore.getState().ingest(ev('background_task_started', { task_id: 'b2', call_id: 'c2', kind: 'bash', description: 'Watch the log' }, 0)) })
        expect(rail()).not.toBeVisible()
        expect(badges()).toBe('🤖2')
        // A question asked after the close is.
        act(() => { useTranscriptStore.getState().ingest(ev('ask', { body: 'Merge now?', message_id: 'm9' }, 0)) })
        expect(rail()).toBeVisible()
        vi.advanceTimersByTime(1000)
        fireEvent.click(toggle())
        expect(rail()).not.toBeVisible()
        act(() => { useTranscriptStore.getState().ingest(ev('assistant_text', { text: 'Waiting for you.', done: true }, 0)) })
        expect(rail()).not.toBeVisible()
        vi.useRealTimers()
      })

      it('a question in the reply when the page opens with the panel closed is not new', async () => {
        seedSession({ status: 'waiting' })
        seedEvents(user())
        stubFetch(0, liveReply({ through_seq: seq, asks: [replyAsk(-3 * MIN)] }))
        localStorage.setItem(RAIL_PREF_KEY, '0')
        await renderPage()
        await waitFor(() => expect(within(rail()!).getByTestId('rail-needs-count')).toHaveTextContent('1'))
        expect(rail()).not.toBeVisible()
      })

      // Card #155 review I2: an engine command approval (owner-only, waited on) is allowed or denied,
      // never dismissed, so it offers no Dismiss; a question anyone may answer does.
      it('offers Dismiss on a question, never on an engine command approval', async () => {
        seedSession({ status: 'waiting' })
        seedEvents(user())
        stubFetch(0, liveReply({ through_seq: seq, can_release_prompt: true, asks: [
          { ...replyAsk(-3 * MIN), owner_only: true },
          { ...replyAsk(-2 * MIN), event_id: 'srv-m2', message_id: 'm2', question: 'Which name?' },
        ] }))
        await renderPage()
        await waitFor(() => expect(within(rail()!).getAllByTestId('rail-ask')).toHaveLength(2))
        expect(within(rail()!).queryByRole('button', { name: /^Dismiss question: Deploy to staging/ })).toBeNull()
        expect(within(rail()!).getByRole('button', { name: /^Dismiss question: Which name\?/ })).toBeInTheDocument()
      })

      it('offers no Dismiss on an owner-only question to someone who is not the owner', async () => {
        seedSession({ status: 'waiting' })
        seedEvents(user())
        stubFetch(0, liveReply({ through_seq: seq, asks: [{ ...replyAsk(-3 * MIN), owner_only: true, no_wait: true }] }))
        await renderPage()
        await within(rail()!).findByTestId('rail-ask')
        expect(within(rail()!).queryByRole('button', { name: /^Dismiss question/ })).toBeNull()
      })

      it('answers a question the server listed, and the answer\'s echo takes it off', async () => {
        seedSession({ status: 'waiting' })
        seedEvents(user())
        stubFetch(0, liveReply({ through_seq: seq, asks: [replyAsk(-3 * MIN)] }))
        await renderPage()
        const row = await within(rail()!).findByTestId('rail-ask')
        fireEvent.change(within(row).getByLabelText('Your answer'), { target: { value: 'yes, staging' } })
        wsMock.send.mockClear()
        await act(async () => { fireEvent.click(within(row).getByRole('button', { name: 'Answer' })) })
        const sent = wsMock.send.mock.calls.map(c => c[0]).find(m => m.type === 'agent_user_message') as { text: string; answer_message_id?: string; client_id: string } | undefined
        expect(sent?.answer_message_id).toBe('m1')
        act(() => { useTranscriptStore.getState().ingest(ev('user_message', { text: 'yes, staging', source: 'ask_answer', message_id: 'm1', client_id: sent!.client_id }, 0)) })
        expect(within(rail()!).queryByTestId('rail-ask')).toBeNull()
        // Nothing needs you; the main agent at work is in Running now, not a count on the toggle.
        expect(badges()).toBe('')
      })

      it('lists a question the agent did not wait on until the answer naming it; only the owner answers it', async () => {
        seedSession({ status: 'running' })
        seedEvents(user())
        const q = { ...replyAsk(-3 * MIN), no_wait: true }
        stubFetch(0, liveReply({ through_seq: seq, asks: [q], can_release_prompt: true }))
        await renderPage()
        const row = await within(rail()!).findByTestId('rail-ask')
        expect(row).toHaveTextContent('not blocking')
        // The person's chat message goes to the agent: it does not take the question off.
        act(() => { useTranscriptStore.getState().ingest(ev('user_message', { text: 'also fix the footer', source: 'chat' }, 0)) })
        act(() => { useTranscriptStore.getState().ingest(ev('user_message', { text: 'Answer to your question', source: 'question_answer' }, 0)) })
        expect(within(rail()!).getByTestId('rail-ask')).toBeInTheDocument()
        fireEvent.change(within(row).getByLabelText('Your answer'), { target: { value: 'blue' } })
        wsMock.send.mockClear()
        await act(async () => { fireEvent.click(within(row).getByRole('button', { name: 'Answer' })) })
        const sent = wsMock.send.mock.calls.map(c => c[0]).find(m => m.type === 'agent_user_message') as { answer_message_id?: string; client_id: string } | undefined
        expect(sent?.answer_message_id).toBe('m1')
        act(() => { useTranscriptStore.getState().ingest(ev('user_message', { text: 'blue', source: 'ask_answer', message_id: 'm1', client_id: sent!.client_id }, 0)) })
        expect(within(rail()!).queryByTestId('rail-ask')).toBeNull()
      })

      it('gives no answer box for a question the agent did not wait on to someone other than its owner', async () => {
        seedSession({ status: 'running' })
        seedEvents(user())
        stubFetch(0, liveReply({ through_seq: seq, asks: [{ ...replyAsk(-3 * MIN), no_wait: true }], can_release_prompt: false }))
        await renderPage()
        const row = await within(rail()!).findByTestId('rail-ask')
        expect(within(row).queryByLabelText('Your answer')).toBeNull()
        expect(row).toHaveTextContent('Only the session owner can answer this question.')
      })

      it('drops a question the server closed with no transcript event (it expired) at once', async () => {
        seedSession({ status: 'waiting' })
        seedEvents(user())
        stubFetch(0, liveReply({ through_seq: seq, asks: [replyAsk(-3 * MIN)] }))
        await renderPage()
        await waitFor(() => expect(within(rail()!).getByTestId('rail-needs-count')).toHaveTextContent('1'))
        act(() => {
          wsMock.emit({ type: 'message_answered', message: { id: 'm1', session_id: 'sess-a', kind: 'ask', body: 'Deploy to staging first?', status: 'answered', answer: '', created_at: iso(-3 * MIN) } } as never)
        })
        expect(badges()).toBe('')
        expect(within(rail()!).getByTestId('rail-needs')).toHaveTextContent('Nothing needs you.')
      })

      it('shows a background subagent once, as a subagent, not again as its task', async () => {
        seedSession({ status: 'idle' })
        seedEvents(user())
        stubFetch(0, liveReply({
          through_seq: seq,
          subagents: [{ key: 'A2', title: 'Watch the build', type: 'general-purpose', model: '', tokens: null, doing: '', started_at: iso(-3 * MIN), active_at: iso(-3 * MIN), background: true, seq: 1 }],
          tasks: [{ task_id: 'g1', call_id: 'A2', kind: 'agent', description: 'Watch the build', started_at: iso(-3 * MIN), seq: 1 }],
        }))
        await renderPage()
        await waitFor(() => expect(within(rail()!).getAllByTestId('rail-subagent')).toHaveLength(1))
        expect(within(rail()!).getByTestId('rail-subagent')).toHaveAttribute('data-status', 'background')
        expect(within(rail()!).queryByTestId('rail-task')).toBeNull()
        expect(within(rail()!).getByTestId('rail-running-count')).toHaveTextContent('1')
      })
    })

    it('has no panel for a terminal session', async () => {
      seedSession({ kind: 'tmux', status: 'running' })
      await renderPage()
      expect(screen.queryByTestId('rail-toggle')).toBeNull()
      expect(rail()).toBeNull()
    })
  })

  describe('on a narrow screen', () => {
    beforeEach(() => viewport(800))

    it('is a drawer, opened only from the toggle, that Escape closes with focus back on the toggle', async () => {
      seedSession({ status: 'idle' })
      seedEvents(user(), ...task())
      localStorage.setItem(RAIL_PREF_KEY, '1')
      await renderPage()
      // Something runs and the wide panel's choice is open: the drawer still waits for the toggle.
      expect(rail()).toBeNull()
      toggle().focus()
      fireEvent.click(toggle())
      const drawer = screen.getByRole('dialog', { name: 'Session panel' })
      expect(drawer).toHaveAttribute('aria-modal', 'true')
      expect(toggle()).toHaveAttribute('aria-expanded', 'true')
      expect(document.body.style.overflow).toBe('hidden')
      expect(drawer.contains(document.activeElement)).toBe(true)
      fireEvent.keyDown(drawer, { key: 'Escape' })
      expect(rail()).toBeNull()
      expect(document.body.style.overflow).toBe('')
      expect(toggle()).toHaveFocus()
      expect(localStorage.getItem(RAIL_PREF_KEY)).toBe('1')
    })

    it.each([false, true])('keeps Tab inside the drawer (options available: %s)', async options => {
      if (!options) {
        const previousFetch = fetchMock.getMockImplementation() as (url: string) => Promise<unknown>
        fetchMock.mockImplementation((url: string) => /\/(limit-resume|plan-nudges)$/.test(String(url))
          ? Promise.resolve({ ok: false, status: 404 }) : previousFetch(url))
      }
      seedSession({ status: 'idle' })
      seedEvents(user(), ...task())
      await renderPage()
      fireEvent.click(toggle())
      const drawer = screen.getByRole('dialog', { name: 'Session panel' })
      const close = within(drawer).getByRole('button', { name: 'Close' })
      // The last control on the tab on screen (Live), not one of a hidden tab.
      await within(drawer).findByRole('tabpanel', { name: /^Live/ })
      const live = within(drawer).getByRole('tabpanel', { name: /^Live/ })
      // Optional settings arrive asynchronously. Cover both the loaded options
      // and the older-server case where the model select is the final control.
      if (options) {
        await within(live).findByRole('checkbox', { name: 'Resume when limit resets' })
        await within(live).findByRole('checkbox', { name: 'Nudge when the plan drifts' })
      }
      const stops = Array.from(live.querySelectorAll<HTMLElement>('button:not([disabled]), select:not([disabled]), textarea:not([disabled]), input:not([disabled]), [href]'))
      const last = stops[stops.length - 1]
      expect(last.tagName).toBe(options ? 'INPUT' : 'SELECT')
      expect(close).toHaveFocus()
      fireEvent.keyDown(drawer, { key: 'Tab', shiftKey: true })
      expect(last).toHaveFocus()
      fireEvent.keyDown(drawer, { key: 'Tab' })
      expect(close).toHaveFocus()
    })

    it('closes on the backdrop, not on a click inside', async () => {
      seedSession({ status: 'idle' })
      seedEvents(user())
      await renderPage()
      fireEvent.click(toggle())
      const drawer = screen.getByRole('dialog', { name: 'Session panel' })
      fireEvent.pointerDown(drawer)
      fireEvent.click(drawer)
      expect(rail()).not.toBeNull()
      const backdrop = screen.getByTestId('session-rail-backdrop')
      fireEvent.pointerDown(backdrop)
      fireEvent.click(backdrop)
      expect(rail()).toBeNull()
    })

    it('"Show in conversation" scrolls the chat to the row and closes the drawer', async () => {
      seedSession({ status: 'idle' })
      seedEvents(user(), ...task())
      await renderPage()
      fireEvent.click(toggle())
      fireEvent.click(within(screen.getByRole('dialog', { name: 'Session panel' })).getByTestId('rail-show'))
      expect((scrolled.mock.contexts.at(-1) as HTMLElement).dataset.callId).toBe('c1')
      expect(rail()).toBeNull()
    })

    it('a row that is not loaded says so in the drawer, which stays open', async () => {
      seedSession({ status: 'idle' })
      seedEvents(user(), ev('background_task_started', { task_id: 'b9', call_id: 'gone', kind: 'monitor', description: 'Watch the log', started_at: new Date(Date.now() - MIN).toISOString() }, MIN))
      await renderPage()
      fireEvent.click(toggle())
      const drawer = screen.getByRole('dialog', { name: 'Session panel' })
      fireEvent.click(within(drawer).getByTestId('rail-show'))
      expect(rail()).toBe(drawer)
      expect(within(drawer).getByRole('status')).toHaveTextContent('Not in the part of the conversation the chat has drawn. Scroll up in the chat to reach it.')
    })

    it('keeps a typed answer when the drawer closes and opens again', async () => {
      seedSession({ status: 'waiting' })
      seedEvents(user(), ask())
      await renderPage()
      fireEvent.click(toggle())
      let box = within(screen.getByRole('dialog', { name: 'Session panel' })).getByLabelText('Your answer')
      fireEvent.change(box, { target: { value: 'yes, staging' } })
      fireEvent.keyDown(box, { key: 'Escape' })
      expect(rail()).toBeNull()
      fireEvent.click(toggle())
      box = within(screen.getByRole('dialog', { name: 'Session panel' })).getByLabelText('Your answer')
      expect(box).toHaveValue('yes, staging')
    })
  })
  it('renders root progress alongside child sessions and pending approvals from the whole-log live reply', async () => {
    seedSession({ status: 'idle' })
    seedEvents(user())
    stubFetch(0, liveReply({ progress: { completed: 2, total: 5, label: 'Verify integration' }, children: [{ id: 'child', title: 'Codex review', engine: 'codex', model: 'fixture', effort: 'high', status: 'running', started_at: iso(-MIN), ended_at: null, asking: true, tokens: 12000 }], delegation_requests: [{ request_id: 'request', engine: 'codex', title: 'Another review', expires_at: iso(MIN) }] }))
    await renderPage()
    expect((await screen.findAllByTestId('rail-child'))[0]).toHaveTextContent('Codex review')
    expect(screen.getByTestId('rail-session-progress')).toHaveTextContent('Verify integration')
    expect(within(screen.getByTestId('rail-session-progress')).getByRole('progressbar')).toHaveAttribute('aria-valuenow', '2')
    // A running child counts toward Running now and has its one card there, with everything (model,
    // effort, discard); Started sessions has a single line that jumps to it.
    expect(within(screen.getByTestId('rail-running')).getByTestId('rail-running-count')).toHaveTextContent('1')
    const card = within(screen.getByTestId('rail-running')).getByTestId('rail-child')
    expect(card).toHaveTextContent('Codex review')
    expect(within(card).getByTestId('rail-child-open').getAttribute('title')).toContain('effort: high')
    expect(within(card).getByRole('img', { name: 'Model: fixture' })).toBeInTheDocument()
    expect(within(card).getByRole('button', { name: /Discard/ })).toBeInTheDocument()
    expect(screen.getAllByTestId('rail-child')).toHaveLength(1)
    expect(screen.getByTestId('rail-running')).not.toHaveTextContent('Idle.')
    expect(within(screen.getByTestId('rail-started')).queryByTestId('rail-child')).toBeNull()
    expect(within(screen.getByTestId('rail-started')).getByTestId('rail-started-running')).toHaveTextContent('1 running ↑')
    expect(screen.getByTestId('rail-needs-count')).toHaveTextContent('1')
    expect(screen.getByTestId('rail-needs')).toHaveTextContent('Another review')
  })

  describe('Running now: the main agent', () => {
    const bash = (agoMs: number) => ev('tool_call', { tool: 'Bash', call_id: 'ff1', input: { command: 'git merge --ff-only feature', description: 'Fast-forward main to the feature branch' } }, agoMs)

    it('heads Running now while it works, with the working line\'s call, words and clocks, and counts', async () => {
      vi.useFakeTimers({ toFake: ['Date'] })
      vi.setSystemTime(new Date('2026-10-01T00:00:00Z'))
      seedSession({ status: 'running' })
      seedEvents(ev('user_message', { text: 'go', source: 'chat' }, 281_000), ev('status_changed', { status: 'running' }, 280_000), bash(249_000))
      await renderPage()
      const running = screen.getByTestId('rail-running')
      expect(running).not.toHaveTextContent('Nothing is running')
      expect(within(running).getByTestId('rail-running-count')).toHaveTextContent('1')
      const main = within(running).getByTestId('rail-main')
      expect(running.querySelector('.rail-list')!.firstElementChild).toBe(main)
      expect(main).toHaveAttribute('data-state', 'working')
      expect(within(main).getByTestId('rail-main-title')).toHaveTextContent(/^Main$/)
      expect(within(main).getByTestId('rail-main-engine')).toHaveAccessibleName('Engine: Claude')
      expect(within(main).getByRole('img', { name: 'Working' })).toBeInTheDocument()
      // The working line under the chat and the Main row say the same: the call, what it does, its clock.
      const line = screen.getByTestId('waiting-on').textContent!
      expect(line).toContain('waiting on Bash · Fast-forward main to the feature branch 4:09')
      expect(within(main).getByTestId('rail-main-doing')).toHaveTextContent('Bash: Fast-forward main to the feature branch')
      expect(within(main).getByTestId('rail-main-step-elapsed')).toHaveTextContent('4:09')
      expect(within(main).getByTestId('rail-elapsed')).toHaveTextContent(screen.getByTestId('working').querySelector('.agent-working-time')!.textContent!)
      const title = within(main).getByTestId('rail-main-show')
      expect(title.getAttribute('title')).toContain("The agent you're talking to. Indented rows run inside it; child sessions run on their own.")
      expect(title.getAttribute('aria-description')).toContain("The agent you're talking to. Indented rows run inside it; child sessions run on their own.")
      expect(title.getAttribute('title')).not.toContain('This session')
    })

    it('says "Working…" or what the agent is on when no call is named, as the working line does', async () => {
      seedSession({ status: 'running', engine: 'codex' })
      seedEvents(user())
      await renderPage()
      const main = within(screen.getByTestId('rail-running')).getByTestId('rail-main')
      expect(screen.getByTestId('working-words')).toHaveTextContent('Working…')
      expect(within(main).getByTestId('rail-main-doing')).toHaveTextContent('Working…')
      expect(within(main).getByTestId('rail-main-engine')).toHaveAccessibleName('Engine: Codex')
    })

    it('indents subagents and background commands under Main; a started session sits at Main\'s level', async () => {
      seedSession({ status: 'running' })
      seedEvents(user())
      stubFetch(0, liveReply({
        subagents: [{ key: 'S1', title: 'Survey the repo', type: 'Explore', model: '', tokens: null, doing: '', started_at: iso(-MIN), active_at: iso(0), background: false, seq: 2 }],
        tasks: [{ task_id: 'b1', call_id: 'c1', kind: 'bash', description: 'Wait for the deploy', started_at: iso(-MIN), seq: 3 }],
        children: [{ id: 'c-run', title: 'Parallel worker', engine: 'codex', model: 'fixture', status: 'running', started_at: iso(-MIN), ended_at: null, asking: false, tokens: null }] }))
      await renderPage()
      const running = screen.getByTestId('rail-running')
      await waitFor(() => expect(within(running).getByTestId('rail-child')).toBeInTheDocument())
      const rows = [...running.querySelector('.rail-list')!.children] as HTMLElement[]
      expect(rows.map(r => r.dataset.testid)).toEqual(['rail-main', 'rail-subagent', 'rail-task', 'rail-child'])
      for (const r of rows.slice(1, 3)) {
        expect(r).toHaveClass('rail-nested')
        expect(r.querySelector('.rail-run-nest')).toHaveTextContent('↳')
      }
      expect(rows[0]).not.toHaveClass('rail-nested')
      const child = rows[3]
      expect(child).not.toHaveClass('rail-nested')
      expect(child.querySelector('.rail-run-nest')).toBeNull()
      expect(within(child).getByRole('img', { name: 'Engine: Codex' })).toBeInTheDocument()
      expect(within(running).getByTestId('rail-running-count')).toHaveTextContent('4')
    })

    it('says "Idle." when the agent is idle and nothing runs; with a background command, Main is idle (Zz) and not counted', async () => {
      seedSession({ status: 'idle' })
      seedEvents(user())
      await renderPage()
      expect(screen.getByTestId('rail-running')).toHaveTextContent('Idle.')
      expect(screen.getByTestId('rail-running')).not.toHaveTextContent('Nothing is running')
      expect(screen.queryByTestId('rail-main')).toBeNull()
      act(() => { for (const e of task()) useTranscriptStore.getState().ingest(e) })
      const main = await within(screen.getByTestId('rail-running')).findByTestId('rail-main')
      expect(main).toHaveAttribute('data-state', 'idle')
      expect(within(main).getByRole('img', { name: 'Idle' })).toHaveTextContent('Zz')
      expect(within(main).queryByTestId('rail-main-doing')).toBeNull()
      expect(within(screen.getByTestId('rail-running')).getByTestId('rail-running-count')).toHaveTextContent('1')
      expect(within(screen.getByTestId('rail-running')).getByTestId('rail-task')).toHaveClass('rail-nested')
    })

    it.each(['stopped', 'ended', 'error', 'starting', 'disconnected'] as const)('a %s session keeps "Nothing is running." and has no Main card, even with a leftover progress value', async status => {
      seedSession({ status })
      seedEvents(user())
      stubFetch(0, liveReply({ progress: { completed: 2, total: 5, label: 'Leftover' } }))
      await renderPage()
      await waitFor(() => expect(fetchCalls().some(u => u.includes('/live'))).toBe(true))
      const running = screen.getByTestId('rail-running')
      expect(running).toHaveTextContent('Nothing is running.')
      expect(running).not.toHaveTextContent('Idle')
      expect(within(running).queryByTestId('rail-main')).toBeNull()
      expect(within(running).queryByTestId('rail-session-progress')).toBeNull()
      expect(within(running).getByTestId('rail-running-count')).toHaveTextContent('0')
    })

    it('an idle session with a progress value shows it on an idle Main card (Zz)', async () => {
      seedSession({ status: 'idle' })
      seedEvents(user())
      stubFetch(0, liveReply({ progress: { completed: 2, total: 5, label: 'Verify' } }))
      await renderPage()
      const main = await within(screen.getByTestId('rail-running')).findByTestId('rail-main')
      expect(main).toHaveAttribute('data-state', 'idle')
      expect(within(main).getByTestId('rail-session-progress')).toHaveTextContent('2/5')
      expect(within(screen.getByTestId('rail-running')).getByTestId('rail-running-count')).toHaveTextContent('0')
    })

    it('a turn starting is no count on the toggle and never opens the panel by itself', async () => {
      viewport(390)
      seedSession({ status: 'running' })
      seedEvents(user())
      await renderPage()
      // A phone, no saved choice: the drawer opens by itself only for something that needs you or runs beside the chat.
      expect(rail()).toBeNull()
      expect(badges()).toBe('')
      expect(toggle()).toHaveAccessibleName('Session panel')
      expect(screen.getByTestId('rail-announce')).toHaveTextContent(/^Session panel$/)
      expect(screen.queryByRole('dialog', { name: 'Session panel' })).toBeNull()
    })

    it('says "Waiting on you." while the agent waits on an answer and nothing runs', async () => {
      seedSession({ status: 'waiting' })
      seedEvents(user(), ask())
      await renderPage()
      expect(screen.getByTestId('rail-running')).toHaveTextContent('Waiting on you.')
      expect(within(screen.getByTestId('rail-running')).getByTestId('rail-running-count')).toHaveTextContent('0')
    })
  })

  it.each([
    ['only work inside', { subs: 2, kids: 0 }, '🤖2', 'Session panel: 2 subagents and background tasks'],
    ['only a child session', { subs: 0, kids: 1 }, '↳1', 'Session panel: 1 child session'],
    ['both', { subs: 1, kids: 3 }, '🤖1 ↳3', 'Session panel: 1 subagent or background task · 3 child sessions'],
  ] as const)('the toggle shows its two counts, each hidden at zero, matching Running now (%s)', async (_name, n, shown, label) => {
    seedSession({ status: 'idle' })
    seedEvents(user())
    const subagents = Array.from({ length: n.subs }, (_, i) => ({ key: `S${i}`, title: `Sub ${i}`, type: 'Explore', model: '', tokens: null, doing: '', started_at: iso(-MIN), active_at: iso(-1000), background: true, seq: 2 + i }))
    const children = Array.from({ length: n.kids }, (_, i) => ({ id: `c-${i}`, title: `Child ${i}`, engine: i % 2 ? 'codex' : 'claude', model: 'fixture', status: 'running', started_at: iso(-MIN), ended_at: null, asking: false, tokens: null }))
    stubFetch(0, liveReply({ subagents, children: [...children, { id: 'c-idle', title: 'Idle child', engine: 'codex', model: 'fixture', status: 'idle', started_at: iso(-MIN), ended_at: null, asking: false, tokens: null }] }))
    await renderPage()
    await waitFor(() => expect(badges()).toBe(shown))
    expect(toggle()).toHaveAccessibleName(label)
    expect(within(screen.getByTestId('rail-running')).getByTestId('rail-running-count')).toHaveTextContent(String(n.subs + n.kids))
  })

  it.each([
    ['the live path (tokens hidden by the server, usage folded from later events)', true],
    ['the transcript fallback (/live unavailable)', false],
  ] as const)('shows a non-owner no subagent token totals on %s', async (_name, live) => {
    seedSession({ status: 'running' })
    // The subagent's events carry real usage (12k input + 400 output).
    seedEvents(user(), ...subagent())
    stubFetch(0, live ? liveReply({ through_seq: 1, tokens_hidden: true }) : undefined)
    await renderPage()
    const sub = await within(rail()!).findByTestId('rail-subagent')
    if (live) await waitFor(() => expect(fetchCalls().some(u => u.includes('/live'))).toBe(true))
    expect(within(sub).queryByTestId('rail-sub-tokens')).toBeNull()
    expect(sub.textContent).not.toMatch(/12(\.4)?k/)
    for (const el of sub.querySelectorAll('[title], [aria-description]')) {
      expect(`${el.getAttribute('title') ?? ''} ${el.getAttribute('aria-description') ?? ''}`).not.toMatch(/token/i)
    }
  })

  it('never counts what needs you on the toggle: an own tool change and a child that is asking add no needs-you count', async () => {
    seedSession({ status: 'idle' })
    seedEvents(user())
    stubFetch(0, liveReply({ tool_changes: 1, children: [{ id: 'c-ask', title: 'Asking child', engine: 'codex', model: 'fixture', status: 'waiting', started_at: iso(-MIN), ended_at: null, asking: true, tokens: null }] }))
    await renderPage()
    await waitFor(() => expect(badges()).toBe('↳1'))
    expect(screen.getByTestId('rail-toggle').querySelector('.attention')).toBeNull()
    expect(toggle()).toHaveAccessibleName('Session panel: 1 child session')
    expect(screen.getByTestId('rail-announce')).not.toHaveTextContent(/need/)
  })

  it('an idle child does not badge or announce the panel toggle as running', async () => {
    seedSession({ status: 'idle' })
    seedEvents(user())
    stubFetch(0, liveReply({ children: [{ id: 'c-idle', title: 'Idle child', engine: 'codex', model: 'fixture', status: 'idle', started_at: iso(-MIN), ended_at: null, asking: false, tokens: null }] }))
    await renderPage()
    await waitFor(() => expect(screen.getByTestId('rail-started')).toHaveTextContent('Idle child'))
    expect(badges()).toBe('')
    expect(screen.getByTestId('rail-toggle').getAttribute('aria-label')).not.toMatch(/child session|running/)
    expect(screen.getByTestId('rail-announce')).not.toHaveTextContent(/child session|running/)
  })

  it('Running now lists working children only, and a row opens the child', async () => {
    seedSession({ status: 'idle' })
    seedEvents(user())
    const child = (id: string, title: string, status: string, ended: string | null) => ({ id, title, engine: 'codex', model: 'fixture', status, started_at: iso(-MIN), ended_at: ended, asking: false, tokens: null })
    stubFetch(0, liveReply({ children: [child('c-run', 'Running child', 'running', null), child('c-done', 'Finished child', 'ended', iso(-1000)), child('c-idle', 'Idle child', 'idle', null)] }))
    const opened = vi.fn()
    render(
      <OpenSessionContext.Provider value={opened}>
        <MemoryRouter initialEntries={['/sessions/sess-a']}>
          <Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes>
        </MemoryRouter>
      </OpenSessionContext.Provider>,
    )
    await act(async () => { for (let k = 0; k < 10; k++) await Promise.resolve() })
    const running = await screen.findByTestId('rail-running')
    await waitFor(() => expect(within(running).getByTestId('rail-running-count')).toHaveTextContent('1'))
    const rows = within(running).getAllByTestId('rail-child')
    expect(rows).toHaveLength(1)
    expect(rows[0]).toHaveTextContent('Running child')
    // Stopped ones (done, or idle with its turn over) are in Started sessions, once each, to reuse or
    // discard; the running one is a line there that brings Running now into view.
    const started = screen.getByTestId('rail-started')
    expect(within(started).getAllByTestId('rail-child').map(r => r.textContent)).toEqual([expect.stringContaining('Finished child'), expect.stringContaining('Idle child')])
    running.scrollIntoView = vi.fn()
    fireEvent.click(within(started).getByTestId('rail-started-running'))
    expect(running.scrollIntoView).toHaveBeenCalled()
    expect(document.activeElement).toBe(running)
    fireEvent.click(within(rows[0]).getByTestId('rail-child-open'))
    expect(opened).toHaveBeenCalledWith('c-run')
    // A click anywhere on the card (here its clock) opens it too.
    opened.mockClear()
    fireEvent.click(rows[0].querySelector('.rail-clock')!)
    expect(opened).toHaveBeenCalledWith('c-run')
  })

it('keeps archived sessions free of live management controls despite stale running flags', async () => {
  seedSession({archived_at:'2026-10-20T12:00:00Z',status:'running',limit_resume_supported:true})
  seedEvents(user())
  stubFetch(3, liveReply({can_release_prompt:true,delegation_requests:[{request_id:'old',engine:'codex',title:'Stale approval',expires_at:iso(MIN)}],delegation_grants:[{id:'grant',engine:'codex',runtime:'docker'}]}))
  await renderPage()
  expect(screen.queryByRole('group',{name:'Session permissions'})).toBeNull()
  expect(screen.queryByRole('checkbox',{name:'Resume when limit resets'})).toBeNull()
  expect(screen.queryByRole('button',{name:'End session'})).toBeNull()
  expect(screen.queryByRole('menuitem',{name:/Discard/,hidden:true})).toBeNull()
  expect(screen.queryByRole('button',{name:/Discard/})).toBeNull()
  expect(fetchCalls().filter(url=>url.endsWith('/limit-resume'))).toEqual([])
  expect(screen.getByTestId('rail-needs')).toHaveTextContent('Nothing needs you.')
  expect(screen.queryByText('Stale approval')).toBeNull()
  expect(screen.queryByRole('button',{name:/Child permissions/})).toBeNull()
})

})
