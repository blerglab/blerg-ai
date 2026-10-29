import { PaletteContext } from '../commands/context'
import { CommandRegistry } from '../commands/registry'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act, render, screen, fireEvent, waitFor, within } from '@testing-library/react'
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

let commandRegistry: CommandRegistry
function runCommand(title: string) { act(() => { commandRegistry.getSnapshot().items.find(i => i.title === title)!.run() }) }
function renderDetail(id = 'sess-a') {
  commandRegistry = new CommandRegistry()
  return render(
    <MemoryRouter initialEntries={[`/sessions/${id}`]}>
      <Routes>
        <Route path="/sessions/:id" element={<PaletteContext.Provider value={{ registry: commandRegistry, open: () => {} }}><SessionDetail /></PaletteContext.Provider>} />
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
    seedSession({ kind: 'tmux', status: 'running' })
    renderDetail()
    runCommand('Skills & plugins')
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
    const insights = screen.getByRole('dialog', { name: 'Fix the bug · Insights' })
    expect(await within(insights).findByRole('alert')).toHaveTextContent('Could not load insights')
    fireEvent.keyDown(insights, { key: 'Escape' })
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('on a phone the compact header keeps Commands and Stop; session actions live in the palette', () => {
    const desktop = window.matchMedia
    window.matchMedia = vi.fn().mockImplementation((query: string) => ({
      matches: true, media: query, onchange: null,
      addEventListener: () => {}, removeEventListener: () => {}, addListener: () => {}, removeListener: () => {}, dispatchEvent: () => false,
    }))
    try {
      seedSession({ title: 'Fix the bug', status: 'running', kind: 'agent' })
      renderDetail()
      const group = screen.getByTestId('session-header-controls')
      expect(screen.queryByRole('button', { name: /^Notifications/ })).toBeNull()
      const controls = [...group.querySelectorAll<HTMLButtonElement>('button')]
      expect(controls.map(b => b.getAttribute('aria-label'))).toEqual(['Search commands and files', "Stop the agent's current turn"])
      expect(screen.queryByTestId('session-more')).toBeNull()
      runCommand('Star session')
      expect(wsMock.send).toHaveBeenCalledWith({ type: 'set_session_star', session_id: 'sess-a', starred: true })
      runCommand('Session insights')
      expect(screen.getByRole('dialog', { name: 'Fix the bug · Insights' })).toBeInTheDocument()
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
    await waitFor(() => expect(screen.getByTestId('rail-badge-inside')).toHaveTextContent(/^🤖1$/))
    expect(screen.getByTestId('rail-task')).toHaveTextContent('Wait for the deploy')
    // No "working in background" chip in the header: the panel says it.
    expect(screen.queryByTestId('detail-status')).toBeNull()
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
    // The toggle counts no needs-you (owner rule); the panel's Needs you does.
    await waitFor(() => expect(screen.getByTestId('rail-needs-count')).toHaveTextContent('1'))
    expect(screen.getByTestId('rail-toggle')).toHaveAccessibleName('Session panel')
    // The panel counts the same way (its Needs you section and its Live tab), and still lists the
    // overdue one, as expired.
    await waitFor(() => expect(screen.getByTestId('rail-needs-count')).toHaveTextContent('1'))
    expect(screen.getByTestId('rail-tab-count')).toHaveTextContent('1')
    expect(screen.getByTestId('proposal-action-waiting')).toBeInTheDocument()
    expect(screen.getByTestId('proposal-action-overdue')).toBeInTheDocument()
  })

  it('on a phone the side panel toggle is an icon with a name and its counts', async () => {
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
      await waitFor(() => expect(toggle.textContent).toBe('🤖1'))
      expect(within(toggle).getByTestId('rail-badge-inside').querySelector('.icon-btn-count-glyph')).toHaveTextContent('🤖')
      expect(toggle.querySelector('svg')).not.toBeNull()
      expect(toggle).toHaveAccessibleName('Session panel: 1 subagent or background task')
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
    seedSession({ kind: 'tmux', status: 'running' })
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
    seedSession({ kind: 'tmux', status: 'running' })
    renderDetail()
    wsMock.send.mockClear()
    fireEvent.click(screen.getByText('⏎'))
    expect(wsMock.send).toHaveBeenCalledWith({
      type: 'send_input',
      session_id: 'sess-a',
      data: btoa(KEY_SEQ.enter),
    })
  })

  // Card #142: End session asks in a confirmation anchored to the ✕, never by arming the ✕ itself.
  it('asks in an alertdialog focused on Cancel, and End session issues the DELETE', () => {
    seedSession({ status: 'running', kind: 'agent', runtime: 'daemon' })
    renderDetail()

    const end = screen.getByTestId('stop-session')
    fireEvent.click(end)
    // The ✕ never arms: same name, and a second press closes the question instead of ending.
    expect(end).toHaveAccessibleName('End session')
    expect(end.className).not.toContain('armed')
    expect(end).toHaveAttribute('aria-expanded', 'true')
    const dialog = screen.getByRole('alertdialog', { name: 'End this session?' })
    expect(dialog).toHaveAccessibleDescription('The agent stops. The conversation and files are kept.')
    expect(within(dialog).getByRole('button', { name: 'Cancel' })).toHaveFocus()
    expect(fetch).not.toHaveBeenCalledWith('/api/sessions/sess-a', expect.objectContaining({ method: 'DELETE' }))

    fireEvent.click(within(dialog).getByRole('button', { name: 'End session' }))
    // apiFetch (Task 18) adds an Authorization header on top of the call site's own init —
    // but only when an access token actually exists. There is none in this test, and
    // apiFetch no longer sends the literal string "Bearer null" in that case.
    expect(fetch).toHaveBeenCalledWith('/api/sessions/sess-a', {
      method: 'DELETE',
      headers: {},
    })
  })

  it('cancels End session on Esc, on Cancel and on a press outside, the focus back on the ✕', () => {
    seedSession({ status: 'running' })
    renderDetail()
    const end = screen.getByTestId('stop-session')
    fireEvent.click(end)
    fireEvent.keyDown(document.activeElement!, { key: 'Escape' })
    expect(screen.queryByRole('alertdialog')).toBeNull()
    expect(end).toHaveFocus()
    fireEvent.click(end)
    fireEvent.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: 'Cancel' }))
    expect(screen.queryByRole('alertdialog')).toBeNull()
    fireEvent.click(end)
    fireEvent.pointerDown(document.body)
    expect(screen.queryByRole('alertdialog')).toBeNull()
    expect(end).toHaveFocus()
    expect(fetch).not.toHaveBeenCalledWith('/api/sessions/sess-a', expect.objectContaining({ method: 'DELETE' }))
  })

  it('says what ending keeps for where the session runs', () => {
    seedSession({ status: 'running', kind: 'agent', runtime: 'cluster' })
    renderDetail()
    fireEvent.click(screen.getByTestId('stop-session'))
    expect(screen.getByRole('alertdialog')).toHaveAccessibleDescription(/pod is removed.*not pushed is lost/)
  })

  it('words a terminal session as a terminal, with no conversation', () => {
    seedSession({ kind: 'tmux', status: 'running', runtime: 'daemon' })
    renderDetail()
    fireEvent.click(screen.getByTestId('stop-session'))
    expect(screen.getByRole('alertdialog')).toHaveAccessibleDescription('The terminal session stops. Files are kept.')
  })

  it('cannot send End twice while one is on its way, and gives the ✕ back after a failure', async () => {
    let answer: (r: { ok: boolean; status: number }) => void = () => {}
    vi.stubGlobal('fetch', vi.fn((_url: string, init?: RequestInit) => init?.method === 'DELETE'
      ? new Promise(r => { answer = r })
      : Promise.resolve({ ok: true, status: 200, json: async () => ({}) })))
    seedSession({ status: 'running', kind: 'agent', runtime: 'daemon' })
    renderDetail()
    const end = screen.getByTestId('stop-session')
    fireEvent.click(end)
    fireEvent.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: 'End session' }))
    await waitFor(() => expect(end).toBeDisabled())
    expect(end).toHaveAttribute('aria-busy', 'true')
    fireEvent.click(end)
    expect(screen.queryByRole('alertdialog')).toBeNull()
    expect(vi.mocked(fetch).mock.calls.filter(c => (c[1] as RequestInit | undefined)?.method === 'DELETE')).toHaveLength(1)
    await act(async () => { answer({ ok: false, status: 503 }) })
    await waitFor(() => expect(end).toBeEnabled())
    const failure = screen.getByRole('alertdialog', { name: 'Ending the session failed' })
    expect(within(failure).getByRole('button', { name: 'Dismiss' })).toHaveFocus()
    fireEvent.click(within(failure).getByRole('button', { name: 'Dismiss' }))
    expect(end).toHaveFocus()
    expect(useToastStore.getState().toasts).toHaveLength(0)
  })

  it('makes the rest of the page inert while it asks, and gives it back after', () => {
    seedSession({ status: 'running', kind: 'agent', runtime: 'daemon' })
    const { container } = renderDetail()
    const root = container as HTMLElement
    fireEvent.click(screen.getByTestId('stop-session'))
    expect(root.inert).toBe(true)
    expect(screen.getByRole('alertdialog').closest('[inert]')).toBeNull()
    fireEvent.keyDown(document.activeElement!, { key: 'Escape' })
    expect(root.inert).toBe(false)
    expect(screen.getByTestId('stop-session')).toHaveFocus()
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

  // ── End session: navigate on success, stay + toast on failure ────────────────────

  it('navigates home after a successful DELETE (ok:true)', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve({ ok: true })))
    seedSession({ status: 'running' })
    renderDetail()

    fireEvent.click(screen.getByTestId('stop-session'))
    fireEvent.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: 'End session' }))

    await waitFor(() => expect(screen.getByText('HOME')).toBeInTheDocument())
    expect(useToastStore.getState().toasts).toHaveLength(0)
  })

  it('stays on session and anchors the failure to End when DELETE responds not-ok', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve({ ok: false, status: 503 })))
    const session = seedSession({ status: 'running' })
    renderDetail()

    fireEvent.click(screen.getByTestId('stop-session'))
    fireEvent.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: 'End session' }))

    await waitFor(() => expect(screen.getByRole('alertdialog')).toBeInTheDocument())
    expect(useToastStore.getState().toasts).toHaveLength(0)
    expect(screen.queryByText('HOME')).not.toBeInTheDocument()
    expect(screen.getByText(session.title || session.repo!)).toBeInTheDocument()
  })

  // ── Pause: frees a cluster session's pod, keeps the session ───────────────

  it('offers Pause on a live cluster session and posts to the pause endpoint', async () => {
    const fetchMock = vi.fn(() => Promise.resolve({ ok: true }))
    vi.stubGlobal('fetch', fetchMock)
    seedSession({ id: 'sess-a', status: 'idle', runtime: 'cluster' })
    renderDetail()

    runCommand('Pause session')

    await waitFor(() => expect(fetchMock).toHaveBeenCalled())
    expect(fetchMock).toHaveBeenCalledWith('/api/sessions/sess-a/pause', expect.objectContaining({ method: 'POST' }))
    // it stays on the session: pausing is not leaving
    expect(screen.queryByText('HOME')).not.toBeInTheDocument()
    expect(useToastStore.getState().toasts).toHaveLength(0)
  })

  it('anchors the failure when pause fails', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve({ ok: false, status: 502 })))
    seedSession({ status: 'running', runtime: 'cluster' })
    renderDetail()
    runCommand('Pause session')
    await waitFor(() => expect(screen.getByRole('alertdialog')).toBeInTheDocument())
    expect(useToastStore.getState().toasts).toHaveLength(0)
  })

  it('does not offer Pause on a desktop session, a starting one, or one already paused', () => {
    seedSession({ status: 'idle', runtime: 'daemon' })
    const { unmount } = renderDetail()
    expect(screen.queryByRole('menuitem', { name: /Pause/, hidden: true })).not.toBeInTheDocument()
    unmount()
    seedSession({ status: 'starting', runtime: 'cluster' })
    const second = renderDetail()
    expect(screen.queryByRole('menuitem', { name: /Pause/, hidden: true })).not.toBeInTheDocument()
    second.unmount()
    seedSession({ status: 'disconnected', runtime: 'cluster' })
    renderDetail()
    expect(screen.queryByRole('menuitem', { name: /Pause/, hidden: true })).not.toBeInTheDocument()
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

  // ── The header's controls: one style, grouped by intent, no bell (card #87) ──

  it('draws the header controls in one icon-button style, in order: star, insights, stop, end session', () => {
    seedSession({ status: 'running', kind: 'agent' })
    renderDetail()
    const group = screen.getByTestId('session-header-controls')
    const controls = [...group.querySelectorAll<HTMLButtonElement>('button:not([role="menuitem"])')]
    expect(controls.map(b => b.getAttribute('aria-label'))).toEqual(['Search commands and files', 'Star', 'Insights', "Stop the agent's current turn", 'End session'])
    for (const b of controls) {
      expect(b).toHaveClass('icon-btn')
      // Icons only: the word is in the tooltip and the label, no text or emoji drawn.
      expect(b.title).not.toBe('')
      expect(b.querySelector('svg')).not.toBeNull()
      expect(b.textContent?.replace(/\d/g, '')).toBe('')
    }
    // End session is the only red one.
    expect(controls.filter(b => b.classList.contains('danger'))).toEqual([screen.getByRole('button', { name: 'End session' })])
  })

  it('has no Notifications bell in the session header', () => {
    seedSession({ status: 'running', kind: 'agent' })
    renderDetail()
    expect(screen.queryAllByRole('button', { name: /Notifications/ })).toHaveLength(0)
    expect(screen.queryByText('🔔')).toBeNull()
  })

  it('reaches Discard from the palette and and confirms it while the session runs', () => {
    seedSession({ status: 'running', kind: 'agent', title: 'Busy work' })
    renderDetail()
    // Not a header button of its own any more.
    expect(screen.queryByRole('button', { name: 'Discard Busy work' })).toBeNull()
    expect(screen.queryByTestId('session-more')).toBeNull()
    runCommand('Discard session…')
    expect(screen.getByRole('alertdialog', { name: 'Discard session and all children?' })).toBeInTheDocument()
    expect(screen.getByTestId('discard-running')).toHaveTextContent('It is running now.')
    expect(fetch).not.toHaveBeenCalledWith(expect.stringContaining('/discard'), expect.anything())
  })

  it('keeps an idle session\'s Discard confirmation free of the running warning', () => {
    seedSession({ status: 'idle', kind: 'agent', title: 'Quiet work' })
    renderDetail()
    runCommand('Discard session…')
    expect(screen.getByRole('alertdialog', { name: 'Discard session and all children?' })).toBeInTheDocument()
    expect(screen.queryByTestId('discard-running')).toBeNull()
  })

  // ── Star toggle in header ──────────────────────────────────────────────────

  it('renders a filled star when session.starred is true', () => {
    seedSession({ id: 'sess-a', starred: true })
    renderDetail()
    const btn = screen.getByTestId('star-toggle')
    expect(btn).toHaveAttribute('data-starred', 'true')
    expect(btn).toHaveAttribute('aria-pressed', 'true')
    expect(btn).toHaveAccessibleName('Unstar')
  })

  it('renders an outline star when session.starred is false', () => {
    seedSession({ id: 'sess-a', starred: false })
    renderDetail()
    const btn = screen.getByTestId('star-toggle')
    expect(btn).toHaveAttribute('data-starred', 'false')
    expect(btn).toHaveAttribute('aria-pressed', 'false')
    expect(btn).toHaveAccessibleName('Star')
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
    seedSession({ kind: 'tmux', id: 'sess-a', title: 'Session A' })
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

  // Owner test 2026-10-28: a cluster agent session showed the Terminal/Messages layout on a phone.
  // Terminal sessions were removed in 0.5.0: a session whose kind is missing is an agent session.
  it('gives a session with no kind the agent layout, never the terminal one', () => {
    seedSession({ id: 'sess-nokind', status: 'running' })
    renderDetail('sess-nokind')
    expect(screen.queryByTestId('tab-terminal')).toBeNull()
    expect(screen.queryByTestId('tab-chat')).toBeNull()
  })

  it('a terminal session keeps its Messages tab', () => {
    seedSession({ kind: 'tmux', id: 'sess-term' })
    renderDetail('sess-term')
    expect(screen.getByTestId('tab-chat')).toHaveTextContent('Messages')
    expect(screen.getByTestId('tab-terminal')).toHaveTextContent('Terminal')
  })

  it('replying from the Messages tab POSTs to /api/messages/{id}/reply', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, json: async () => ({}) })
    vi.stubGlobal('fetch', fetchMock)

    seedSession({ kind: 'tmux', id: 'sess-a' })
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
    seedSession({ kind: 'agent', runtime: 'docker', engine, skip_permissions: true })
    renderDetail()
    expect(screen.getByTestId('rail-session')).toHaveTextContent('Runs in a sandbox on workstation, using your engine login')
    expect(screen.queryByTestId('posture-banner')).toBeNull()
  })
  it('does not call a cluster agent session unsandboxed', () => {
    seedSession({ kind: 'agent', runtime: 'cluster' })
    renderDetail()
    const banner = screen.getByTestId('rail-session')
    expect(banner).toHaveTextContent('Runs in a Kubernetes pod')
    expect(banner).not.toHaveTextContent('unsandboxed')
  })
  it('names agent-kind sessions as unsandboxed', () => {
    seedSession({ kind: 'agent', runtime: 'daemon' })
    renderDetail()
    expect(screen.getByTestId('rail-session')).toHaveTextContent('Runs unsandboxed on workstation as you, using your engine login')
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
