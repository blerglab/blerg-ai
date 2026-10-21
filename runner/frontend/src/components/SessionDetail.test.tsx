import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { MemoryRouter, Routes, Route, useNavigate } from 'react-router-dom'
import SessionDetail from './SessionDetail'
import SettingsDialog from './SettingsDialog'
import { useSessionStore } from '../hooks/useSessionStore'
import { useToastStore } from '../hooks/useToastStore'
import { useMessageStore } from '../hooks/useMessageStore'
import * as wsMock from '../test/wsMock'
import { makeDaemon, makeSession } from '../test/fixtures'
import { arrowSeq, KEY_SEQ } from '../terminalKeys'
import type { SessionInfo, MessageInfo } from '../types'
import { useTranscriptStore, type AgentEvent } from '@blerglab/chat'

vi.mock('../ws', () => import('../test/wsMock'))

function makeMessage(over: Partial<MessageInfo> = {}): MessageInfo {
  return {
    id: 'msg-1',
    session_id: 'sess-a',
    kind: 'update',
    body: 'Task finished',
    status: 'answered',
    created_at: '2026-06-23T10:00:00Z',
    ...over,
  }
}

function seedSession(over: Partial<SessionInfo> = {}) {
  const session = makeSession(over)
  useSessionStore.setState({
    daemons: [makeDaemon()],
    sessions: [session],
    statusChangedAt: { [session.id]: Date.now() },
    serverVersion: 'test',
  })
  return session
}

function renderDetail(id = 'sess-a') {
  return render(
    <MemoryRouter initialEntries={[`/sessions/${id}`]}>
      <Routes>
        <Route path="/sessions/:id" element={<SessionDetail />} />
        <Route path="/sessions" element={<div>HOME</div>} />
      </Routes>
    </MemoryRouter>,
  )
}

describe('SessionDetail', () => {
  beforeEach(() => {
    wsMock.resetWsMock()
    useSessionStore.setState({
      daemons: [],
      sessions: [],
      statusChangedAt: {},
      serverVersion: '',
      pendingSessionIds: {},
      failedSessions: {},
    })
    useToastStore.setState({ toasts: [] })
    useMessageStore.setState({ messages: [] })
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve({ ok: true, json: () => Promise.resolve({}) })))
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('renders the session title but not a status chip (status is visible in the terminal text)', () => {
    seedSession({ title: 'Fix the bug', status: 'running' })
    renderDetail()
    expect(screen.getByText('Fix the bug')).toBeInTheDocument()
    expect(screen.queryByText('running')).not.toBeInTheDocument()
  })

  it('a child\'s header says who started it and from where, never "started by from …"', () => {
    const child = makeSession({ id: 'sess-a', status: 'idle', parent_session_id: 'parent-1', started_by: { kind: 'session', name: 'Parent work' } })
    useSessionStore.setState({
      daemons: [makeDaemon()],
      sessions: [makeSession({ id: 'parent-1', title: 'Parent work', engine: 'codex' }), child],
      statusChangedAt: {},
      serverVersion: 'test',
    })
    renderDetail()
    expect(screen.getByTestId('detail-started-by')).toHaveTextContent('started by Codex agent from Parent work ·')
  })

  it('says Skills & plugins are not available for a terminal session', () => {
    seedSession({ status: 'running' })
    renderDetail()
    fireEvent.click(screen.getByTestId('capabilities-toggle-terminal'))
    expect(screen.getByRole('dialog', { name: 'Skills & plugins' })).toBeInTheDocument()
    expect(screen.getByTestId('caps-unavailable')).toHaveTextContent('Not available for terminal sessions')
    fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Escape' })
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('opens this session\'s Insights from the header, and Escape closes it', async () => {
    seedSession({ title: 'Fix the bug', status: 'running' })
    const fetchMock = vi.fn(() => Promise.resolve({ ok: false, status: 503, json: () => Promise.resolve({}) }))
    vi.stubGlobal('fetch', fetchMock)
    renderDetail()
    fireEvent.click(screen.getByTestId('session-insights-toggle'))
    expect(screen.getByRole('dialog', { name: 'Fix the bug · Insights' })).toBeInTheDocument()
    await waitFor(() => expect(fetchMock.mock.calls.some((c) => String((c as unknown[])[0]).includes('/api/sessions/sess-a/insights'))).toBe(true))
    expect(await screen.findByRole('alert')).toHaveTextContent('Could not load insights')
    fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Escape' })
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('on a phone the header\'s Insights button is an icon with a name, so it never squeezes the title out', () => {
    const desktop = window.matchMedia
    window.matchMedia = vi.fn().mockImplementation((query: string) => ({
      matches: true, media: query, onchange: null,
      addEventListener: () => {}, removeEventListener: () => {}, addListener: () => {}, removeListener: () => {}, dispatchEvent: () => false,
    }))
    try {
      seedSession({ title: 'Fix the bug', status: 'running' })
      renderDetail()
      const toggle = screen.getByTestId('session-insights-toggle')
      expect(toggle).toHaveAccessibleName('Insights')
      expect(toggle.textContent).toBe('')
      expect(toggle.querySelector('svg')).not.toBeNull()
      expect(toggle).toHaveAttribute('title', "This session's time, work, tokens and cost")
    } finally {
      window.matchMedia = desktop
    }
  })

  // A background command the agent left running, as the session's transcript holds it.
  function seedBackgroundTask(sessionId = 'sess-a') {
    const at = new Date(Date.now() - 65_000).toISOString()
    const ev = (seq: number, kind: string, payload: unknown): AgentEvent =>
      ({ session_id: sessionId, client_event_id: `bg${seq}`, seq, ts: at, kind, payload } as AgentEvent)
    useTranscriptStore.setState({ sessions: {} })
    const store = useTranscriptStore.getState()
    store.ingest(ev(1, 'tool_call', { tool: 'Bash', call_id: 'c1', input: { command: 'make deploy', run_in_background: true } }))
    store.ingest(ev(2, 'background_task_started', { task_id: 'b1', call_id: 'c1', kind: 'bash', description: 'Wait for the deploy', started_at: at }))
  }

  it('lists what runs in the background in the side panel, not in a chip, a popover or the conversation', async () => {
    seedSession({ kind: 'agent', title: 'Ship it', status: 'idle' })
    seedBackgroundTask()
    renderDetail()
    expect(screen.queryByTestId('background-head-chip')).not.toBeInTheDocument()
    expect(screen.queryByTestId('background-strip')).not.toBeInTheDocument()
    // This stub's server cannot answer the live state: the page reads the chat's loaded events.
    await waitFor(() => expect(screen.getByTestId('rail-badge')).toHaveTextContent('1'))
    expect(screen.getByTestId('rail-task')).toHaveTextContent('Wait for the deploy')
  })

  it('counts a pending proposal in Needs you only until its deadline: past it, it is listed as expired, not counted', async () => {
    seedSession({ kind: 'agent', title: 'Ship it', status: 'idle' })
    useTranscriptStore.setState({ sessions: {} })
    const proposal = (id: string, expiresAt: number) => ({ id, state: 'pending', kind: 'board_file', connection_id: '', connection_name: 'Board', tool: 'file_ticket',
      arguments: { title: `Card ${id}`, type: 'bug', body: 'Body' }, arguments_raw: '{}', agent_summary: '', session_id: 'sess-a', cron_id: null,
      created_at: new Date().toISOString(), expires_at: new Date(expiresAt).toISOString(), decided_at: null, decided_by: null, result: null })
    const proposals = [proposal('waiting', Date.now() + 86400_000), proposal('overdue', Date.now() - 60_000)]
    vi.stubGlobal('fetch', vi.fn((url: string) => String(url).startsWith('/api/proposals?')
      ? Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({ proposals }) })
      : Promise.resolve({ ok: true, json: () => Promise.resolve({}) })))
    renderDetail()
    await waitFor(() => expect(screen.getByTestId('rail-badge')).toHaveTextContent('1'))
    expect(screen.getByTestId('rail-toggle')).toHaveAccessibleName(expect.stringContaining('1 needs you'))
    // The panel counts the same way (its Needs you section and its Live tab), and still lists the
    // overdue one, as expired.
    await waitFor(() => expect(screen.getByTestId('rail-needs-count')).toHaveTextContent('1'))
    expect(screen.getByTestId('rail-tab-count')).toHaveTextContent('1')
    expect(screen.getByTestId('proposal-action-waiting')).toBeInTheDocument()
    expect(screen.getByTestId('proposal-action-overdue')).toBeInTheDocument()
  })

  it('on a phone the side panel toggle is an icon with a name and its count', async () => {
    const desktop = window.matchMedia
    window.matchMedia = vi.fn().mockImplementation((query: string) => ({
      matches: true, media: query, onchange: null,
      addEventListener: () => {}, removeEventListener: () => {}, addListener: () => {}, removeListener: () => {}, dispatchEvent: () => false,
    }))
    try {
      seedSession({ kind: 'agent', status: 'running' })
      seedBackgroundTask()
      renderDetail()
      const toggle = screen.getByTestId('rail-toggle')
      await waitFor(() => expect(toggle.textContent).toBe('1'))
      expect(toggle.querySelector('svg')).not.toBeNull()
      expect(toggle).toHaveAccessibleName('Session panel: 1 running')
    } finally {
      window.matchMedia = desktop
    }
  })

  it('shows "Session not found" for an unknown id', () => {
    seedSession({ id: 'sess-a' })
    renderDetail('does-not-exist')
    expect(screen.getByText('Session not found')).toBeInTheDocument()
  })

  it('sends an arrow key as send_input when an arrow button is clicked', () => {
    seedSession({ status: 'running' })
    renderDetail()
    wsMock.send.mockClear()
    fireEvent.click(screen.getByText('→'))
    expect(wsMock.send).toHaveBeenCalledWith({
      type: 'send_input',
      session_id: 'sess-a',
      data: btoa(arrowSeq('right', false)),
    })
  })

  it('sends the enter sequence as send_input when the ⏎ button is clicked', () => {
    seedSession({ status: 'running' })
    renderDetail()
    wsMock.send.mockClear()
    fireEvent.click(screen.getByText('⏎'))
    expect(wsMock.send).toHaveBeenCalledWith({
      type: 'send_input',
      session_id: 'sess-a',
      data: btoa(KEY_SEQ.enter),
    })
  })

  it('requires a second click to confirm a kill, then issues a DELETE', () => {
    seedSession({ status: 'running' })
    renderDetail()

    fireEvent.click(screen.getByText('✕ Kill'))
    expect(screen.getByText('✕ Confirm kill?')).toBeInTheDocument()
    expect(fetch).not.toHaveBeenCalledWith('/api/sessions/sess-a', expect.objectContaining({ method: 'DELETE' }))

    fireEvent.click(screen.getByText('✕ Confirm kill?'))
    // apiFetch (Task 18) adds an Authorization header on top of the call site's own init —
    // but only when an access token actually exists. There is none in this test, and
    // apiFetch no longer sends the literal string "Bearer null" in that case.
    expect(fetch).toHaveBeenCalledWith('/api/sessions/sess-a', {
      method: 'DELETE',
      headers: {},
    })
  })

  // ── error_reason banner: gated on status === 'error', not just presence ────

  it('renders no error-reason banner for a running session with a stale reason', () => {
    seedSession({ status: 'running', error_reason: 'stale' })
    renderDetail()
    expect(screen.queryByTestId('error-reason')).not.toBeInTheDocument()
  })

  it('renders the error-reason banner for an errored session', () => {
    seedSession({ status: 'error', error_reason: 'claude not found on PATH' })
    renderDetail()
    expect(screen.getByTestId('error-reason')).toHaveTextContent('claude not found on PATH')
  })

  // ── Why it ended (end_reason) ──────────────────────────────────────────────

  it('says who ended a stopped session', () => {
    seedSession({ status: 'stopped', end_reason: 'stopped_by_agent', ended_by: { kind: 'agent' } })
    renderDetail()
    expect(screen.getByTestId('end-reason')).toHaveTextContent('Ended by an agent token')
  })

  it('says a process exited on its own', () => {
    seedSession({ status: 'stopped', end_reason: 'process_exited', ended_by: null })
    renderDetail()
    expect(screen.getByTestId('end-reason')).toHaveTextContent('The process exited on its own')
  })

  it('renders a legacy ended session (no reason) exactly as before', () => {
    seedSession({ status: 'stopped', end_reason: undefined, ended_by: undefined })
    const { container } = renderDetail()
    expect(screen.queryByTestId('end-reason')).not.toBeInTheDocument()
    expect(container.textContent).not.toContain('undefined')
  })

  it('leaves a failure to the error banner instead of repeating it', () => {
    seedSession({ status: 'error', end_reason: 'job_failed', error_reason: 'cluster job failed: DeadlineExceeded' })
    renderDetail()
    expect(screen.queryByTestId('end-reason')).not.toBeInTheDocument()
    expect(screen.getByTestId('error-reason')).toHaveTextContent('cluster job failed: DeadlineExceeded')
  })

  it('shows no end line for a running session', () => {
    seedSession({ status: 'running', end_reason: 'stopped_by_user', ended_by: { kind: 'human' } })
    renderDetail()
    expect(screen.queryByTestId('end-reason')).not.toBeInTheDocument()
  })

  // ── State precedence: pending / failed / not-found ─────────────────────────

  it('shows "Starting…" loading view when session is pending', () => {
    useSessionStore.setState({ pendingSessionIds: { 'pending-sess': true } })
    renderDetail('pending-sess')
    expect(screen.getByText(/Starting/)).toBeInTheDocument()
    expect(screen.queryByText('Session not found')).not.toBeInTheDocument()
    // The start panel, not a bare word: a stage in progress and a clock.
    expect(screen.getByTestId('start-progress').querySelector('[data-state="active"]')).not.toBeNull()
    expect(screen.getByTestId('start-elapsed')).toHaveTextContent('0:00')
  })

  it('shows error view with the failure message', () => {
    useSessionStore.setState({ failedSessions: { 'fail-sess': 'Session crashed on startup' } })
    renderDetail('fail-sess')
    expect(screen.getByText('Session crashed on startup')).toBeInTheDocument()
  })

  it('shows generic fallback when failure message is empty', () => {
    useSessionStore.setState({ failedSessions: { 'fail-sess': '' } })
    renderDetail('fail-sess')
    expect(screen.getByText('Session failed to start.')).toBeInTheDocument()
  })

  it('failed takes priority over pending when id is in both', () => {
    useSessionStore.setState({
      pendingSessionIds: { 'sess-x': true },
      failedSessions: { 'sess-x': 'Boom!' },
    })
    renderDetail('sess-x')
    expect(screen.getByText('Boom!')).toBeInTheDocument()
    expect(screen.queryByText(/Starting/)).not.toBeInTheDocument()
  })

  it('clicking "Back to home" from error view clears the session and navigates home', () => {
    useSessionStore.setState({ failedSessions: { 'fail-sess': 'Crash!' } })
    renderDetail('fail-sess')
    fireEvent.click(screen.getByText('Back to home'))
    expect(screen.getByText('HOME')).toBeInTheDocument()
    expect(useSessionStore.getState().failedSessions).not.toHaveProperty('fail-sess')
  })

  it('transitions from loading to terminal when session_started arrives for the pending id', async () => {
    const session = makeSession({ id: 'pending-sess', title: 'New session' })
    useSessionStore.setState({
      pendingSessionIds: { 'pending-sess': true },
      daemons: [makeDaemon()],
    })
    renderDetail('pending-sess')
    expect(screen.getByText(/Starting/)).toBeInTheDocument()

    wsMock.emit({ type: 'session_started', session })

    await waitFor(() => expect(screen.queryByText(/Starting/)).not.toBeInTheDocument())
    expect(screen.getByText('New session')).toBeInTheDocument()
  })

  // ── Kill: navigate on success, stay + toast on failure ────────────────────

  it('navigates home after a successful DELETE (ok:true)', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve({ ok: true })))
    seedSession({ status: 'running' })
    renderDetail()

    fireEvent.click(screen.getByText('✕ Kill'))
    fireEvent.click(screen.getByText('✕ Confirm kill?'))

    await waitFor(() => expect(screen.getByText('HOME')).toBeInTheDocument())
    expect(useToastStore.getState().toasts).toHaveLength(0)
  })

  it('stays on session and shows a toast when DELETE responds not-ok', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve({ ok: false, status: 503 })))
    const session = seedSession({ status: 'running' })
    renderDetail()

    fireEvent.click(screen.getByText('✕ Kill'))
    fireEvent.click(screen.getByText('✕ Confirm kill?'))

    await waitFor(() => expect(useToastStore.getState().toasts).toHaveLength(1))
    expect(screen.queryByText('HOME')).not.toBeInTheDocument()
    expect(screen.getByText(session.title || session.repo!)).toBeInTheDocument()
  })

  // ── Pause: frees a cluster session's pod, keeps the session ───────────────

  it('offers Pause on a live cluster session and posts to the pause endpoint', async () => {
    const fetchMock = vi.fn(() => Promise.resolve({ ok: true }))
    vi.stubGlobal('fetch', fetchMock)
    seedSession({ id: 'sess-a', status: 'idle', runtime: 'cluster' })
    renderDetail()

    fireEvent.click(screen.getByText('⏸ Pause'))

    await waitFor(() => expect(fetchMock).toHaveBeenCalled())
    expect(fetchMock).toHaveBeenCalledWith('/api/sessions/sess-a/pause', expect.objectContaining({ method: 'POST' }))
    // it stays on the session: pausing is not leaving
    expect(screen.queryByText('HOME')).not.toBeInTheDocument()
    expect(useToastStore.getState().toasts).toHaveLength(0)
  })

  it('toasts when the pause fails', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve({ ok: false, status: 502 })))
    seedSession({ status: 'running', runtime: 'cluster' })
    renderDetail()
    fireEvent.click(screen.getByText('⏸ Pause'))
    await waitFor(() => expect(useToastStore.getState().toasts).toHaveLength(1))
  })

  it('does not offer Pause on a desktop session, a starting one, or one already paused', () => {
    seedSession({ status: 'idle', runtime: 'daemon' })
    const { unmount } = renderDetail()
    expect(screen.queryByText('⏸ Pause')).not.toBeInTheDocument()
    unmount()
    seedSession({ status: 'starting', runtime: 'cluster' })
    const second = renderDetail()
    expect(screen.queryByText('⏸ Pause')).not.toBeInTheDocument()
    second.unmount()
    seedSession({ status: 'disconnected', runtime: 'cluster' })
    renderDetail()
    expect(screen.queryByText('⏸ Pause')).not.toBeInTheDocument()
  })

  it('sends mark_session_read on mount when a sessionId is present', () => {
    seedSession({ id: 'sess-a' })
    wsMock.send.mockClear()
    renderDetail('sess-a')
    expect(wsMock.send).toHaveBeenCalledWith({
      type: 'mark_session_read',
      session_id: 'sess-a',
    })
  })

  // ── Star toggle in header ──────────────────────────────────────────────────

  it('renders a filled star when session.starred is true', () => {
    seedSession({ id: 'sess-a', starred: true })
    renderDetail()
    const btn = screen.getByTestId('star-toggle')
    expect(btn).toHaveAttribute('data-starred', 'true')
    expect(btn).toHaveTextContent('★')
  })

  it('renders an outline star when session.starred is false', () => {
    seedSession({ id: 'sess-a', starred: false })
    renderDetail()
    const btn = screen.getByTestId('star-toggle')
    expect(btn).toHaveAttribute('data-starred', 'false')
    expect(btn).toHaveTextContent('☆')
  })

  it('sends set_session_star with starred:true when clicking an unstarred session star', () => {
    seedSession({ id: 'sess-a', starred: false })
    renderDetail()
    wsMock.send.mockClear()
    fireEvent.click(screen.getByTestId('star-toggle'))
    expect(wsMock.send).toHaveBeenCalledWith({
      type: 'set_session_star',
      session_id: 'sess-a',
      starred: true,
    })
  })

  it('sends set_session_star with starred:false when clicking a starred session star', () => {
    seedSession({ id: 'sess-a', starred: true })
    renderDetail()
    wsMock.send.mockClear()
    fireEvent.click(screen.getByTestId('star-toggle'))
    expect(wsMock.send).toHaveBeenCalledWith({
      type: 'set_session_star',
      session_id: 'sess-a',
      starred: false,
    })
  })

  // ── Per-session chat thread ────────────────────────────────────────────────

  it('Chat tab shows only this session\'s messages, not other sessions\'', () => {
    seedSession({ id: 'sess-a', title: 'Session A' })
    useMessageStore.setState({
      messages: [
        makeMessage({ id: 'msg-a', session_id: 'sess-a', body: 'Message from A', status: 'answered' }),
        makeMessage({ id: 'msg-b', session_id: 'sess-b', body: 'Message from B', status: 'answered' }),
      ],
    })
    renderDetail('sess-a')

    // Terminal tab is active by default — messages not yet visible
    expect(screen.queryByText('Message from A')).not.toBeInTheDocument()

    // Switch to the Messages tab
    fireEvent.click(screen.getByTestId('tab-chat'))

    // Only session A's messages are visible
    expect(screen.getByText('Message from A')).toBeInTheDocument()
    expect(screen.queryByText('Message from B')).not.toBeInTheDocument()
  })

  it('an agent session has one view and no tabs: its messages are in its conversation', () => {
    seedSession({ id: 'sess-agent', kind: 'agent' })
    renderDetail('sess-agent')
    expect(screen.queryByTestId('tab-chat')).toBeNull()
    expect(screen.queryByTestId('tab-terminal')).toBeNull()
  })

  it('a terminal session keeps its Messages tab', () => {
    seedSession({ id: 'sess-term' })
    renderDetail('sess-term')
    expect(screen.getByTestId('tab-chat')).toHaveTextContent('Messages')
    expect(screen.getByTestId('tab-terminal')).toHaveTextContent('Terminal')
  })

  it('replying from the Messages tab POSTs to /api/messages/{id}/reply', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, json: async () => ({}) })
    vi.stubGlobal('fetch', fetchMock)

    seedSession({ id: 'sess-a' })
    useMessageStore.setState({
      messages: [
        makeMessage({ id: 'msg-42', session_id: 'sess-a', kind: 'ask', status: 'open', body: 'Question from A?' }),
      ],
    })
    renderDetail('sess-a')

    // Switch to the Messages tab
    fireEvent.click(screen.getByTestId('tab-chat'))

    const input = screen.getByRole('textbox')
    fireEvent.change(input, { target: { value: 'My answer' } })
    fireEvent.click(screen.getByRole('button', { name: /send/i }))

    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        '/api/messages/msg-42/reply',
        expect.objectContaining({
          method: 'POST',
          body: JSON.stringify({ answer: 'My answer' }),
        }),
      )
    })
  })

  it.each(['claude', 'codex', 'hermes'])('describes the Docker posture for %s using its own engine login', engine => {
    seedSession({ runtime: 'docker', engine, skip_permissions: true })
    renderDetail()
    expect(screen.getByTestId('posture-banner')).toHaveTextContent('Docker sandbox — repo mounted at /workspace; using your engine login · permission prompts bypassed')
  })
  it('does not call a cluster agent session unsandboxed', () => {
    seedSession({ kind: 'agent', runtime: 'cluster' })
    renderDetail()
    const banner = screen.getByTestId('posture-banner')
    expect(banner).toHaveTextContent('Cluster session — runs in a Kubernetes pod')
    expect(banner).not.toHaveTextContent('unsandboxed')
  })
  it('names agent-kind sessions as unsandboxed', () => {
    seedSession({ kind: 'agent', runtime: 'daemon' })
    renderDetail()
    expect(screen.getByTestId('posture-banner')).toHaveTextContent('Agent session — runs unsandboxed on workstation as you, no permission prompts, using your engine login')
  })

  it('labels a session started unattended', () => {
    seedSession({ kind: 'agent', interaction: 'unattended' })
    renderDetail()
    const label = screen.getByTestId('detail-unattended')
    expect(label).toHaveTextContent('Unattended')
    expect(label).toHaveAttribute('title', 'Started unattended: the agent does not wait for you in the chat')
  })
  it.each([
    ['an interactive session', { interaction: 'interactive' as const }],
    ['a session from before the field', {}],
  ])('shows no Unattended label for %s', (_name, over) => {
    seedSession({ kind: 'agent', ...over })
    renderDetail()
    expect(screen.queryByTestId('detail-unattended')).not.toBeInTheDocument()
  })

  it('Settings has a Show thinking checkbox that is on by default, toggles and persists', () => {
    localStorage.removeItem('blerg-runner.chat.thinking')
    seedSession({ kind: 'agent', status: 'idle' })
    renderDetail()
    render(<SettingsDialog onClose={() => {}} />)
    const box = screen.getByRole('checkbox', { name: 'Show thinking' })
    expect(box).toBeChecked()
    fireEvent.click(box)
    expect(box).not.toBeChecked()
    expect(localStorage.getItem('blerg-runner.chat.thinking')).toBe('0')
    fireEvent.click(box)
    expect(box).toBeChecked()
    expect(localStorage.getItem('blerg-runner.chat.thinking')).toBe('1')
  })

  it('draws thinking only while Show thinking is on', () => {
    localStorage.setItem('blerg-runner.chat.thinking', '0')
    seedSession({ kind: 'agent', status: 'idle' })
    useTranscriptStore.getState().ingest({
      session_id: 'sess-a', client_event_id: 'th1', seq: 1, ts: '2026-06-23T10:00:00Z', kind: 'assistant_thinking', payload: { text: 'pondering' },
    } as AgentEvent)
    renderDetail()
    render(<SettingsDialog onClose={() => {}} />)
    expect(screen.queryByTestId('thinking')).not.toBeInTheDocument()
    expect(screen.getByRole('checkbox', { name: 'Show thinking' })).not.toBeChecked()
    fireEvent.click(screen.getByRole('checkbox', { name: 'Show thinking' }))
    expect(screen.getByTestId('thinking')).toHaveTextContent('pondering')
  })

  it('does not carry the chat draft from one agent session to the next', () => {
    const a = makeSession({ id: 'sess-a', kind: 'agent', status: 'idle' })
    const b = makeSession({ id: 'sess-b', kind: 'agent', status: 'idle' })
    useSessionStore.setState({ daemons: [makeDaemon()], sessions: [a, b], statusChangedAt: {}, serverVersion: 'test' })
    function GoToB() {
      const navigate = useNavigate()
      return <button onClick={() => navigate('/sessions/sess-b')}>go-b</button>
    }
    render(
      <MemoryRouter initialEntries={['/sessions/sess-a']}>
        <GoToB />
        <Routes>
          <Route path="/sessions/:id" element={<SessionDetail />} />
        </Routes>
      </MemoryRouter>,
    )
    const box = () => screen.getByPlaceholderText(/Message the agent/) as HTMLTextAreaElement
    fireEvent.change(box(), { target: { value: 'meant for A' } })
    fireEvent.click(screen.getByText('go-b'))
    expect(box().value).toBe('')
  })
})
