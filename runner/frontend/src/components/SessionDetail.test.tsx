import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { MemoryRouter, Routes, Route, useNavigate } from 'react-router-dom'
import SessionDetail from './SessionDetail'
import { useSessionStore } from '../hooks/useSessionStore'
import { useToastStore } from '../hooks/useToastStore'
import { useMessageStore } from '../hooks/useMessageStore'
import * as wsMock from '../test/wsMock'
import { makeDaemon, makeSession } from '../test/fixtures'
import { arrowSeq, KEY_SEQ } from '../terminalKeys'
import type { SessionInfo, MessageInfo } from '../types'

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
        <Route path="/" element={<div>HOME</div>} />
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

  it('says Skills & plugins are not available for a terminal session', () => {
    seedSession({ status: 'running' })
    renderDetail()
    fireEvent.click(screen.getByTestId('capabilities-toggle-terminal'))
    expect(screen.getByRole('dialog', { name: 'Skills & plugins' })).toBeInTheDocument()
    expect(screen.getByTestId('caps-unavailable')).toHaveTextContent('Not available for terminal sessions')
    fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Escape' })
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
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
    expect(fetch).not.toHaveBeenCalled()

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

    // Switch to Chat tab
    fireEvent.click(screen.getByTestId('tab-chat'))

    // Only session A's messages are visible
    expect(screen.getByText('Message from A')).toBeInTheDocument()
    expect(screen.queryByText('Message from B')).not.toBeInTheDocument()
  })

  it('replying from the Chat tab POSTs to /api/messages/{id}/reply', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, json: async () => ({}) })
    vi.stubGlobal('fetch', fetchMock)

    seedSession({ id: 'sess-a' })
    useMessageStore.setState({
      messages: [
        makeMessage({ id: 'msg-42', session_id: 'sess-a', kind: 'ask', status: 'open', body: 'Question from A?' }),
      ],
    })
    renderDetail('sess-a')

    // Switch to Chat tab
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

  it('shows the posture banner for each runtime', () => {
    seedSession({ runtime: 'docker', skip_permissions: true })
    renderDetail()
    expect(screen.getByTestId('posture-banner')).toHaveTextContent('Docker sandbox — repo mounted at /workspace; your ~/.claude is shared · permission prompts bypassed')
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
