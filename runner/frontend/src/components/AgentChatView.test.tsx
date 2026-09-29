import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, render, screen, fireEvent } from '@testing-library/react'
import AgentChatView from './AgentChatView'
import { useAgentTranscript } from '../hooks/useAgentTranscript'
import type { AgentEvent, SessionInfo } from '../types'

vi.mock('../ws', () => ({
  send: vi.fn(),
  onMessage: vi.fn(() => () => {}),
  onOpen: vi.fn(() => () => {}),
}))

import { send } from '../ws'

const session: SessionInfo = {
  id: 's1',
  daemon_id: 'd1',
  status: 'idle',
  project_path: '/r/proj',
  repo: 'proj',
  title: 'Test',
  model: 'claude-sonnet-5',
  started_at: '2026-07-26T00:00:00Z',
  kind: 'agent',
}

function ev(over: Partial<AgentEvent>): AgentEvent {
  return {
    type: 'agent_event',
    session_id: 's1',
    client_event_id: Math.random().toString(36),
    ts: '2026-07-26T00:00:00Z',
    kind: 'user_message',
    payload: {},
    ...over,
  }
}

describe('AgentChatView', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    useAgentTranscript.setState({ sessions: {} })
    // The model list fetch: offline unless a test says otherwise, so the
    // built-in list is what renders.
    vi.stubGlobal('fetch', vi.fn(() => Promise.reject(new Error('offline'))))
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    localStorage.removeItem('blerg.agent.compactTools')
  })

  it('renders a scripted transcript', () => {
    // The full-card view: compact tool calls are the default, and are covered
    // in AgentChatView.tools.test.tsx.
    localStorage.setItem('blerg.agent.compactTools', '0')
    const ingest = useAgentTranscript.getState().ingest
    ingest(ev({ seq: 1, kind: 'user_message', payload: { text: 'do the thing', source: 'chat' } }))
    ingest(ev({ seq: 2, kind: 'check_in', payload: { phase: 'task_start', summary: 'planning' } }))
    ingest(ev({ seq: 3, kind: 'tool_call', payload: { tool: 'bash', call_id: 'c1', input: { command: 'ls' } } }))
    ingest(ev({ seq: 4, kind: 'tool_result', payload: { call_id: 'c1', output: 'file.txt', is_error: false, duration_ms: 5 } }))
    ingest(ev({ seq: 5, kind: 'assistant_text', payload: { text: 'done!', done: true } }))
    ingest(ev({
      seq: 6, kind: 'turn_done',
      payload: { stop_reason: 'end_turn', model: 'claude-sonnet-5', usage: { input_tokens: 10, output_tokens: 5, cache_read_tokens: 0, cache_write_tokens: 0 } },
    }))

    render(<AgentChatView session={session} />)
    expect(screen.getByText('do the thing')).toBeTruthy()
    expect(screen.getByTestId('checkin').textContent).toContain('planning')
    expect(screen.getByTestId('tool-card').textContent).toContain('bash')
    expect(screen.getByText('done!')).toBeTruthy()
    expect(screen.getByTestId('turn-done').textContent).toContain('10 in · 5 out')
  })

  it('renders assistant and user text as Markdown, streaming included', () => {
    const ingest = useAgentTranscript.getState().ingest
    ingest(ev({ seq: 1, kind: 'user_message', payload: { text: 'please **fix** it', source: 'chat' } }))
    ingest(ev({ seq: 2, kind: 'assistant_text', payload: { text: 'Done: **bold** and `code`\n\n- a\n- b', done: true } }))
    ingest(ev({ kind: 'assistant_text', transient: true, payload: { text: 'Next:\n\n```sh\nmake te', done: false } }))
    const { container } = render(<AgentChatView session={{ ...session, status: 'running' }} />)
    const strongs = Array.from(container.querySelectorAll('strong')).map(n => n.textContent)
    expect(strongs).toEqual(['fix', 'bold'])
    expect(container.querySelectorAll('.agent-card.assistant li')).toHaveLength(2)
    // An unclosed fence mid-stream is a code block, not a crash or raw backticks.
    expect(screen.getByTestId('streaming').querySelector('pre')?.textContent).toContain('make te')
  })

  it('sends agent_user_message on Enter', () => {
    render(<AgentChatView session={session} />)
    const box = screen.getByPlaceholderText(/Message the agent/) as HTMLTextAreaElement
    fireEvent.change(box, { target: { value: 'hello agent' } })
    fireEvent.keyDown(box, { key: 'Enter' })
    expect(send).toHaveBeenCalledWith({ type: 'agent_user_message', session_id: 's1', text: 'hello agent' })
    expect(box.value).toBe('')
  })

  it('shows streaming text and stop button while running', () => {
    useAgentTranscript.getState().ingest(
      ev({ kind: 'assistant_text', transient: true, payload: { text: 'thinking…', done: false } }),
    )
    render(<AgentChatView session={{ ...session, status: 'running' }} />)
    expect(screen.getByTestId('streaming').textContent).toContain('thinking…')
    fireEvent.click(screen.getByText(/Stop/))
    expect(send).toHaveBeenCalledWith({ type: 'interrupt_session', session_id: 's1' })
  })

  it('model select sends set_session_model', () => {
    render(<AgentChatView session={session} />)
    fireEvent.change(screen.getByLabelText('Model'), { target: { value: 'claude-opus-5' } })
    expect(send).toHaveBeenCalledWith({ type: 'set_session_model', session_id: 's1', model: 'claude-opus-5' })
  })

  it('offers the fetched model list and the current model\'s efforts', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve({
      ok: true,
      json: () => Promise.resolve({ source: 'live', models: [
        { id: 'claude-sonnet-5', name: 'Sonnet 5', section: 'main', efforts: ['low', 'high'], default_effort: 'high' },
        { id: 'claude-next-1', name: 'Next 1', section: 'main', efforts: ['max'] },
      ] }),
    })))
    render(<AgentChatView session={{ ...session, effort: 'high' }} />)
    await screen.findByRole('option', { name: 'Next 1' })
    const effort = screen.getByLabelText('Effort') as HTMLSelectElement
    expect(Array.from(effort.options).map(o => o.value)).toEqual(['low', 'high'])
    fireEvent.change(effort, { target: { value: 'low' } })
    expect(send).toHaveBeenCalledWith({ type: 'set_session_model', session_id: 's1', effort: 'low' })
  })

  it('falls back to the built-in list and hides effort for Haiku', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.reject(new Error('offline'))))
    render(<AgentChatView session={{ ...session, model: 'claude-haiku-4-5-20251001' }} />)
    expect(screen.getByRole('option', { name: 'Opus 5.5' })).toBeInTheDocument()
    expect(screen.queryByLabelText('Effort')).not.toBeInTheDocument()
  })

  it("asks for the session engine's list, and shows no effort when that engine has none", async () => {
    const fetchMock = vi.fn(() => Promise.resolve({ ok: true, json: () => Promise.resolve({ source: 'none', models: [] }) }))
    vi.stubGlobal('fetch', fetchMock)
    render(<AgentChatView session={{ ...session, engine: 'codex', model: 'gpt-5-codex' }} />)
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalled())
    // Asked of the session's own daemon: Codex's list is what that daemon probed.
    expect((fetchMock.mock.calls[0] as unknown[])[0]).toBe('/api/models/codex?daemon_id=d1')
    expect((screen.getByLabelText('Model') as HTMLSelectElement).value).toBe('gpt-5-codex')
    expect(screen.queryByLabelText('Effort')).not.toBeInTheDocument()
  })

  it("offers no daemon-reported list for a session with no daemon (never another daemon's)", async () => {
    const fetchMock = vi.fn(() => Promise.resolve({ ok: true, json: () => Promise.resolve({
      source: 'daemon', daemon_id: 'd9', models: [
        { id: 'gpt-other', name: 'GPT Other', description: '', section: 'main', efforts: ['low', 'high'], default_effort: 'low' },
      ],
    }) }))
    vi.stubGlobal('fetch', fetchMock)
    render(<AgentChatView session={{ ...session, daemon_id: '', engine: 'codex', model: 'gpt-5-codex' }} />)
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalled())
    expect((fetchMock.mock.calls[0] as unknown[])[0]).toBe('/api/models/codex')
    await new Promise(r => setTimeout(r, 20))
    const options = Array.from((screen.getByLabelText('Model') as HTMLSelectElement).options).map(o => o.value)
    expect(options).toEqual(['gpt-5-codex'])
    expect(screen.queryByLabelText('Effort')).not.toBeInTheDocument()
  })

  it('keeps showing a model the list does not know, with every effort', () => {
    render(<AgentChatView session={{ ...session, model: 'sonnet' }} />)
    expect((screen.getByLabelText('Model') as HTMLSelectElement).value).toBe('sonnet')
    const effort = screen.getByLabelText('Effort') as HTMLSelectElement
    expect(Array.from(effort.options).map(o => o.value)).toEqual(['', 'low', 'medium', 'high', 'xhigh', 'max'])
  })
})

// ─── Start progress, ready card ─────────────────────────────────────────────

const plan = {
  plan: true,
  runtime: 'cluster',
  stages: [
    { id: 'queued', label: 'Queued', state: 'active' },
    { id: 'schedule', label: 'Scheduling pod', state: 'pending' },
    { id: 'image', label: 'Pulling image', state: 'pending' },
    { id: 'clone', label: 'Cloning repo', state: 'pending' },
    { id: 'ready', label: 'Ready', state: 'pending' },
  ],
}

describe('AgentChatView start and ready', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    useAgentTranscript.setState({ sessions: {} })
    vi.stubGlobal('fetch', vi.fn(() => Promise.reject(new Error('offline'))))
  })
  afterEach(() => vi.unstubAllGlobals())

  it('shows the start panel (not an empty chat) while starting, and keeps the composer shut', () => {
    const ingest = useAgentTranscript.getState().ingest
    ingest(ev({ seq: 1, kind: 'start_stage', payload: plan }))
    ingest(ev({ seq: 2, kind: 'start_stage', payload: { stages: [{ id: 'image', state: 'active', detail: 'Pulling the image' }] } }))
    render(<AgentChatView session={{ ...session, status: 'starting', runtime: 'cluster' }} />)
    const panel = screen.getByTestId('start-progress')
    expect(panel).toHaveTextContent('Starting session')
    expect(panel.querySelector('[data-stage="image"]')).toHaveAttribute('data-state', 'active')
    expect(panel.querySelector('[data-stage="schedule"]')).toHaveAttribute('data-state', 'done')
    expect(screen.queryByTestId('ready-card')).not.toBeInTheDocument()
    expect(screen.getByPlaceholderText(/starting/)).toBeDisabled()
  })

  it('rebuilds the panel from replayed history after a reload', () => {
    // A reload replays the persisted stage events through the same store.
    const ingest = useAgentTranscript.getState().ingest
    ingest(ev({ seq: 2, kind: 'start_stage', payload: { stages: [{ id: 'clone', state: 'failed', detail: 'Git authentication failed', hint: 'Check the credential' }] } }))
    ingest(ev({ seq: 1, kind: 'start_stage', payload: plan }))
    render(<AgentChatView session={{ ...session, status: 'error', error_reason: 'Git authentication failed' }} />)
    const panel = screen.getByTestId('start-progress')
    expect(panel).toHaveTextContent("Couldn't start the session")
    expect(panel.querySelector('[data-stage="clone"]')).toHaveAttribute('data-state', 'failed')
    expect(screen.getByTestId('start-hint')).toHaveTextContent('Check the credential')
  })

  it('shows the session-ready card once live: engine, model, effort, repo, runtime', () => {
    const ingest = useAgentTranscript.getState().ingest
    ingest(ev({ seq: 1, kind: 'start_stage', payload: plan }))
    ingest(ev({ seq: 2, kind: 'start_stage', ts: '2026-07-26T00:01:00Z', payload: { stages: [{ id: 'ready', state: 'done' }] } }))
    render(<AgentChatView session={{ ...session, status: 'idle', runtime: 'cluster', engine: 'claude', effort: 'high' }} />)
    expect(screen.queryByTestId('start-progress')).not.toBeInTheDocument()
    const card = screen.getByTestId('ready-card')
    expect(card).toHaveTextContent('Session ready')
    expect(card).toHaveTextContent('Claude')
    expect(card).toHaveTextContent('sonnet-5 · high')
    expect(card).toHaveTextContent('proj')
    expect(card).toHaveTextContent('Cluster pod')
    // Nothing sent yet: the composer invites the first message.
    expect(screen.getByPlaceholderText(/to get started/)).not.toBeDisabled()
  })

  it('shows the initial prompt as the first user message under the ready card', () => {
    useAgentTranscript.getState().ingest(ev({ seq: 1, kind: 'user_message', payload: { text: 'fix the build', source: 'chat' } }))
    render(<AgentChatView session={{ ...session, status: 'running' }} />)
    const timeline = screen.getByTestId('agent-chat').querySelector('.agent-timeline')!
    const cards = Array.from(timeline.children)
    expect(cards[0]).toHaveAttribute('data-testid', 'ready-card')
    expect(cards[1]).toHaveTextContent('fix the build')
    expect(screen.getByPlaceholderText('Message the agent… (/model, /effort, /<skill>)')).toBeInTheDocument()
  })
})

// ─── Working indicator ──────────────────────────────────────────────────────

describe('AgentChatView working indicator', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    useAgentTranscript.setState({ sessions: {} })
    vi.stubGlobal('fetch', vi.fn(() => Promise.reject(new Error('offline'))))
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval', 'setTimeout', 'clearTimeout', 'Date'] })
    vi.setSystemTime(new Date('2026-07-26T00:10:00Z'))
  })
  afterEach(() => {
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })

  function sendMessage(text: string) {
    const box = screen.getByRole('textbox') as HTMLTextAreaElement
    fireEvent.change(box, { target: { value: text } })
    fireEvent.keyDown(box, { key: 'Enter' })
  }

  it('starts optimistically on send, ticks, and clears on turn_done', () => {
    const { rerender } = render(<AgentChatView session={session} />)
    expect(screen.queryByTestId('working')).not.toBeInTheDocument()
    sendMessage('go')
    // No server round trip yet: the session is still idle.
    const w = screen.getByTestId('working')
    expect(w).toHaveAttribute('role', 'status')
    expect(w).toHaveAttribute('aria-live', 'polite')
    expect(w).toHaveTextContent('Working… 0:00')
    expect(screen.getByText(/Stop/)).toBeInTheDocument()
    act(() => { vi.advanceTimersByTime(2_000) })
    expect(screen.getByTestId('working')).toHaveTextContent('Working… 0:02')
    // The server picks the turn up; the clock keeps counting from the send.
    rerender(<AgentChatView session={{ ...session, status: 'running' }} />)
    act(() => { vi.advanceTimersByTime(40_000) })
    expect(screen.getByTestId('working')).toHaveTextContent('Working… 0:42')
    act(() => {
      useAgentTranscript.getState().ingest(ev({
        seq: 5, kind: 'turn_done',
        payload: { stop_reason: 'end_turn', model: 'm', usage: { input_tokens: 1, output_tokens: 1, cache_read_tokens: 0, cache_write_tokens: 0 } },
      }))
    })
    rerender(<AgentChatView session={{ ...session, status: 'idle' }} />)
    expect(screen.queryByTestId('working')).not.toBeInTheDocument()
  })

  it('clears on an error event', () => {
    render(<AgentChatView session={session} />)
    sendMessage('go')
    act(() => { useAgentTranscript.getState().ingest(ev({ seq: 3, kind: 'error', payload: { message: 'boom', retryable: false } })) })
    expect(screen.queryByTestId('working')).not.toBeInTheDocument()
  })

  it('clears on interrupt', () => {
    render(<AgentChatView session={session} />)
    sendMessage('go')
    fireEvent.click(screen.getByText(/Stop/))
    expect(send).toHaveBeenCalledWith({ type: 'interrupt_session', session_id: 's1' })
    expect(screen.queryByTestId('working')).not.toBeInTheDocument()
  })

  it('does not start for a settings command, but a bare /model is a prompt', () => {
    render(<AgentChatView session={session} />)
    sendMessage('/model claude-opus-5')
    expect(screen.queryByTestId('working')).not.toBeInTheDocument()
    sendMessage('/model')
    expect(screen.getByTestId('working')).toBeInTheDocument()
  })

  it('keeps the draft and says so when the socket is closed', () => {
    vi.mocked(send).mockReturnValueOnce(false)
    render(<AgentChatView session={session} />)
    sendMessage('go')
    expect(screen.queryByTestId('working')).not.toBeInTheDocument()
    expect(screen.getByTestId('send-notice')).toHaveTextContent('Not connected')
    expect((screen.getByRole('textbox') as HTMLTextAreaElement).value).toBe('go')
  })

  it('gives up on a message nothing answers', () => {
    render(<AgentChatView session={session} />)
    sendMessage('go')
    expect(screen.getByTestId('working')).toBeInTheDocument()
    act(() => { vi.advanceTimersByTime(21_000) })
    expect(screen.queryByTestId('working')).not.toBeInTheDocument()
    expect(screen.getByTestId('send-notice')).toHaveTextContent('No response')
  })

  it('keeps working once the session answers (status or events)', () => {
    const { rerender } = render(<AgentChatView session={session} />)
    sendMessage('go')
    rerender(<AgentChatView session={{ ...session, status: 'running' }} />)
    act(() => { vi.advanceTimersByTime(60_000) })
    expect(screen.getByTestId('working')).toHaveTextContent('Working… 1:00')
    expect(screen.queryByTestId('send-notice')).not.toBeInTheDocument()
  })

  it('measures the turn on the server clock', () => {
    // Browser 30 s fast; the turn started at server 00:09:18, server now is 00:10:00.
    vi.setSystemTime(new Date('2026-07-26T00:10:30Z'))
    const t = useAgentTranscript.getState()
    t.ingest(ev({ seq: 1, kind: 'status_changed', ts: '2026-07-26T00:09:18Z', payload: { status: 'running', reason: 'turn' } }))
    t.ingestReplayDone({ type: 'agent_events_replay_done', session_id: 's1', last_seq: 1, has_more: false, server_time: '2026-07-26T00:10:00Z' })
    render(<AgentChatView session={{ ...session, status: 'running' }} />)
    expect(screen.getByTestId('working')).toHaveTextContent('Working… 0:42')
  })

  it('recovers mid-turn after a reload from the status and the turn start', () => {
    useAgentTranscript.getState().ingest(ev({ seq: 1, kind: 'user_message', payload: { text: 'go', source: 'chat' } }))
    useAgentTranscript.getState().ingest(ev({ seq: 2, kind: 'status_changed', ts: '2026-07-26T00:09:18Z', payload: { status: 'running', reason: 'turn' } }))
    render(<AgentChatView session={{ ...session, status: 'running' }} />)
    expect(screen.getByTestId('working')).toHaveTextContent('Working… 0:42')
  })

  it('keeps a compact indicator in the toolbar while text streams', () => {
    useAgentTranscript.getState().ingest(ev({ kind: 'assistant_text', transient: true, payload: { text: 'part', done: false } }))
    render(<AgentChatView session={{ ...session, status: 'running' }} />)
    expect(screen.queryByTestId('working')).not.toBeInTheDocument()
    expect(screen.getByTestId('working-compact')).toHaveTextContent('Working…')
  })

  it('shows "Waiting for your answer" — not working — while the agent waits', () => {
    render(<AgentChatView session={{ ...session, status: 'waiting' }} />)
    expect(screen.getByTestId('waiting')).toHaveTextContent('Waiting for your answer')
    expect(screen.getByTestId('waiting')).toHaveAttribute('role', 'status')
    expect(screen.queryByTestId('working')).not.toBeInTheDocument()
  })

  it('uses static dots under prefers-reduced-motion', () => {
    const original = window.matchMedia
    window.matchMedia = vi.fn().mockImplementation((q: string) => ({
      matches: q.includes('prefers-reduced-motion'), media: q, onchange: null,
      addEventListener: () => {}, removeEventListener: () => {}, addListener: () => {}, removeListener: () => {}, dispatchEvent: () => false,
    })) as unknown as typeof window.matchMedia
    try {
      render(<AgentChatView session={{ ...session, status: 'running' }} />)
      const w = screen.getByTestId('working')
      expect(w).toHaveAttribute('data-reduced-motion', 'true')
      expect(w.className).toContain('static')
    } finally {
      window.matchMedia = original
    }
  })
})

describe('AgentChatView capabilities', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    useAgentTranscript.setState({ sessions: {} })
    vi.stubGlobal('fetch', vi.fn(() => Promise.reject(new Error('offline'))))
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('badges the Skills & plugins button with the latest report and opens the panel', () => {
    const ingest = useAgentTranscript.getState().ingest
    ingest(ev({ seq: 1, kind: 'capabilities', payload: { engine: 'claude', groups: [{ id: 'tools', label: 'Tools', items: [{ name: 'Bash' }] }] } }))
    ingest(ev({ seq: 2, kind: 'capabilities', payload: {
      engine: 'claude', engine_name: 'Claude Code', groups: [
        { id: 'skills', label: 'Skills', items: [{ name: 'release-notes' }] },
        { id: 'tools', label: 'Tools', items: [{ name: 'Bash' }, { name: 'Read' }] },
      ],
    } }))
    render(<AgentChatView session={session} />)
    // Not a timeline card.
    expect(screen.queryByText('release-notes')).not.toBeInTheDocument()
    const toggle = screen.getByTestId('capabilities-toggle')
    expect(screen.getByTestId('capabilities-badge')).toHaveTextContent('3')
    expect(toggle).toHaveAttribute('aria-label', 'Skills & plugins (3 loaded)')
    fireEvent.click(toggle)
    expect(screen.getByRole('dialog', { name: 'Skills & plugins' })).toBeInTheDocument()
    expect(screen.getByText('release-notes')).toBeInTheDocument()
    fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Escape' })
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('has no badge before anything is reported, and takes the latest from the server', async () => {
    vi.stubGlobal('fetch', vi.fn((url: string) => url.endsWith('/capabilities')
      ? Promise.resolve(new Response(JSON.stringify({
        capabilities: { engine: 'codex', groups: [{ id: 'skills', label: 'Skills', items: [{ name: 'a' }, { name: 'b' }] }] },
        seq: 900, ts: '2026-09-26T00:00:00Z',
      }), { status: 200 }))
      : Promise.reject(new Error('offline'))))
    const { rerender } = render(<AgentChatView session={session} />)
    expect(screen.queryByTestId('capabilities-badge')).not.toBeInTheDocument()
    expect(await screen.findByTestId('capabilities-badge')).toHaveTextContent('2')
    // An older report in the transcript does not replace the server's latest.
    act(() => {
      useAgentTranscript.getState().ingest(ev({ seq: 3, kind: 'capabilities', payload: { engine: 'codex', groups: [] } }))
    })
    rerender(<AgentChatView session={session} />)
    expect(screen.getByTestId('capabilities-badge')).toHaveTextContent('2')
  })
})
