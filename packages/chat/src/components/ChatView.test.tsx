import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen } from '@testing-library/react'
import ChatView from './ChatView'
import { useTranscriptStore } from '../hooks/useTranscriptStore'
import { createCardRegistry, defineCard, type CardProps } from '../cards/registry'
import { createFakeTransport } from '../test/fakeTransport'
import { COMPACT, SESSION, ev, ingest, meta, resetChatState, seed } from '../test/chat'
import { useChat, type ChatContextValue } from './context'
import type { ArtifactInfo } from '../transport/types'

describe('ChatView', () => {
  beforeEach(() => {
    resetChatState()
  })

  afterEach(() => {
    localStorage.removeItem(COMPACT)
  })

  it('renders a scripted transcript', () => {
    // The full-card view: compact tool calls are the default, and are covered in ChatView.tools.test.tsx.
    localStorage.setItem(COMPACT, '0')
    seed(
      ev({ seq: 1, kind: 'user_message', payload: { text: 'do the thing', source: 'chat' } }),
      ev({ seq: 2, kind: 'check_in', payload: { phase: 'task_start', summary: 'planning' } }),
      ev({ seq: 3, kind: 'tool_call', payload: { tool: 'bash', call_id: 'c1', input: { command: 'ls' } } }),
      ev({ seq: 4, kind: 'tool_result', payload: { call_id: 'c1', output: 'file.txt', is_error: false, duration_ms: 5 } }),
      ev({ seq: 5, kind: 'assistant_text', payload: { text: 'done!', done: true } }),
      ev({
        seq: 6, kind: 'turn_done',
        payload: { stop_reason: 'end_turn', model: 'claude-sonnet-5', usage: { input_tokens: 10, output_tokens: 5, cache_read_tokens: 0, cache_write_tokens: 0 } },
      }),
    )
    const t = createFakeTransport()
    render(<ChatView session={SESSION} transport={t.transport} meta={meta()} />)
    expect(screen.getByText('do the thing')).toBeTruthy()
    expect(screen.getByTestId('checkin').textContent).toContain('planning')
    expect(screen.getByTestId('tool-card').textContent).toContain('bash')
    expect(screen.getByText('done!')).toBeTruthy()
    expect(screen.getByTestId('turn-done').textContent).toContain('10 in · 5 out')
  })

  it('folds a turn footer into the assistant bubble it closes, and leaves it standalone after a tool card', () => {
    localStorage.setItem(COMPACT, '0')
    const usage = { input_tokens: 3, output_tokens: 4, cache_read_tokens: 0, cache_write_tokens: 0 }
    seed(
      ev({ seq: 1, kind: 'user_message', payload: { text: 'go', source: 'chat' } }),
      ev({ seq: 2, kind: 'assistant_text', payload: { text: 'first reply', done: true } }),
      ev({ seq: 3, kind: 'turn_done', payload: { stop_reason: 'end_turn', model: 'm1', usage } }),
      ev({ seq: 4, kind: 'user_message', payload: { text: 'again', source: 'chat' } }),
      ev({ seq: 5, kind: 'assistant_text', payload: { text: 'second reply', done: true } }),
      ev({ seq: 6, kind: 'tool_call', payload: { tool: 'bash', call_id: 'c9', input: { command: 'ls' } } }),
      ev({ seq: 7, kind: 'tool_result', payload: { call_id: 'c9', output: 'x', is_error: false, duration_ms: 1 } }),
      ev({ seq: 8, kind: 'turn_done', payload: { stop_reason: 'end_turn', model: 'm2', usage } }),
    )
    const t = createFakeTransport()
    render(<ChatView session={SESSION} transport={t.transport} meta={meta()} />)
    const [folded, standalone] = screen.getAllByTestId('turn-done')
    expect(folded.closest('.agent-card')?.classList.contains('assistant')).toBe(true)
    expect(folded.textContent).toContain('m1')
    expect(standalone.classList.contains('marker')).toBe(true)
    expect(standalone.textContent).toContain('m2')
  })

  it('renders assistant and user text as Markdown, streaming included', () => {
    seed(
      ev({ seq: 1, kind: 'user_message', payload: { text: 'please **fix** it', source: 'chat' } }),
      ev({ seq: 2, kind: 'assistant_text', payload: { text: 'Done: **bold** and `code`\n\n- a\n- b', done: true } }),
    )
    const t = createFakeTransport()
    const { container } = render(<ChatView session={SESSION} transport={t.transport} meta={meta({ status: 'running' })} />)
    act(() => t.emit(ev({ kind: 'assistant_text', transient: true, payload: { text: 'Next:\n\n```sh\nmake te', done: false } })))
    const strongs = Array.from(container.querySelectorAll('strong')).map(n => n.textContent)
    expect(strongs).toEqual(['fix', 'bold'])
    expect(container.querySelectorAll('.agent-card.assistant li')).toHaveLength(2)
    // An unclosed fence mid-stream is a code block, not a crash or raw backticks.
    expect(screen.getByTestId('streaming').querySelector('pre')?.textContent).toContain('make te')
  })

  it('sends the text through the transport on Enter and clears the box', async () => {
    const t = createFakeTransport()
    render(<ChatView session={SESSION} transport={t.transport} meta={meta()} />)
    const box = screen.getByPlaceholderText(/Message the agent/) as HTMLTextAreaElement
    fireEvent.change(box, { target: { value: 'hello agent' } })
    await act(async () => { fireEvent.keyDown(box, { key: 'Enter' }) })
    expect(t.sent).toEqual(['hello agent'])
    expect(box.value).toBe('')
  })

  it('shows streaming text and a Stop button while running; Stop interrupts', () => {
    const t = createFakeTransport()
    render(<ChatView session={SESSION} transport={t.transport} meta={meta({ status: 'running' })} />)
    act(() => t.emit(ev({ kind: 'assistant_text', transient: true, payload: { text: 'thinking…', done: false } })))
    expect(screen.getByTestId('streaming').textContent).toContain('thinking…')
    fireEvent.click(screen.getByText(/Stop/))
    expect(t.interrupts).toBe(1)
  })

  it('shows no Stop when the transport cannot interrupt', () => {
    const t = createFakeTransport({ interrupt: false })
    render(<ChatView session={SESSION} transport={t.transport} meta={meta({ status: 'running' })} />)
    expect(screen.queryByText(/Stop/)).toBeNull()
    fireEvent.keyDown(screen.getByRole('textbox'), { key: 'Escape' })
    expect(t.interrupts).toBe(0)
  })

  it('reads the session row from the transport when the host has none, and follows status messages', async () => {
    const t = createFakeTransport({ meta: { status: 'idle', model: 'claude-opus-5' } })
    render(<ChatView session={SESSION} transport={t.transport} />)
    // Nothing known yet: the composer waits.
    expect(screen.getByRole('textbox')).toBeDisabled()
    await act(async () => {})
    expect(screen.getByRole('textbox')).not.toBeDisabled()
    expect(screen.getByTestId('ready-card')).toHaveTextContent('opus-5')
    act(() => t.setStatus('waiting'))
    expect(screen.getByTestId('waiting')).toBeInTheDocument()
    act(() => t.setStatus('stopped'))
    expect(screen.getByTestId('ended-banner')).toBeInTheDocument()
    expect(screen.getByRole('textbox')).toBeDisabled()
  })

  it('the host’s own row wins over the transport’s status messages', async () => {
    const t = createFakeTransport()
    render(<ChatView session={SESSION} transport={t.transport} meta={meta({ status: 'idle' })} />)
    act(() => t.setStatus('stopped'))
    await act(async () => {})
    expect(screen.queryByTestId('ended-banner')).toBeNull()
    expect(screen.getByRole('textbox')).not.toBeDisabled()
  })

  it('R3: a card fence that never closes while streaming stays a code block', () => {
    function X({ data }: CardProps) { return <div className="x-card">{String(data.a)}</div> }
    const cards = createCardRegistry([defineCard('x', X)])
    const t = createFakeTransport()
    const { container } = render(<ChatView session={SESSION} transport={t.transport} meta={meta({ status: 'running' })} cards={cards} />)
    act(() => t.emit(ev({ kind: 'assistant_text', transient: true, payload: { text: '```card:x\n', done: false } })))
    act(() => t.emit(ev({ kind: 'assistant_text', transient: true, payload: { text: '{"a":1', done: false } })))
    const streaming = screen.getByTestId('streaming')
    expect(streaming.textContent).toContain('{"a":1')
    expect(container.querySelector('.md-card')).toBeNull()
    expect(container.querySelector('.x-card')).toBeNull()
    expect(streaming.querySelector('pre')).not.toBeNull()
    // Once the fence closes in the recorded message, the card is drawn.
    act(() => t.emit(ev({ seq: 1, kind: 'assistant_text', payload: { text: '```card:x\n{"a":1}\n```', done: true } })))
    expect(screen.queryByTestId('streaming')).toBeNull()
    expect(container.querySelector('.md-card .x-card')?.textContent).toBe('1')
  })
})

// ─── Feedback (review, mark-up) through the context ─────────────────────────

describe('ChatView feedback', () => {
  beforeEach(() => {
    resetChatState()
  })

  // A slot that hands the test the context the review and mark-up modes use.
  let ctx: ChatContextValue | null = null
  function Probe() {
    ctx = useChat()
    return null
  }

  it('submitFeedback uploads the files, sends the message with the attachment note, and reads the list again', async () => {
    const t = createFakeTransport()
    render(<ChatView session={SESSION} transport={t.transport} meta={meta()} slots={{ header: <Probe /> }} />)
    await act(async () => {})
    const before = t.files!.listCalls
    let ok = false
    await act(async () => {
      ok = await ctx!.submitFeedback('Review of report.md (v3): 1 request.', [
        new File(['{"version":1}'], 'report.md.review.json', { type: 'application/json' }),
        new File(['# edited'], 'report.md', { type: 'text/markdown' }),
      ])
    })
    expect(ok).toBe(true)
    expect(t.files!.uploads.map(u => u.file.name)).toEqual(['report.md.review.json', 'report.md'])
    expect(t.sent).toEqual([
      'Review of report.md (v3): 1 request.\n\nAttached files (fetch them with: blerg-runner fetch --all): report.md.review.json (13 B), report.md (8 B)',
    ])
    expect(t.files!.listCalls).toBeGreaterThan(before)
    expect(screen.getByTestId('queued-message')).toHaveTextContent('Review of report.md')
  })

  it('submitFeedback says nothing was sent when the transport refuses', async () => {
    const t = createFakeTransport()
    t.refuseSend(true)
    render(<ChatView session={SESSION} transport={t.transport} meta={meta()} slots={{ header: <Probe /> }} />)
    await act(async () => {})
    let ok = true
    await act(async () => { ok = await ctx!.submitFeedback('hello', []) })
    expect(ok).toBe(false)
    expect(t.sent).toEqual([])
  })

  it('reviewFor merges the newest user and agent review files', async () => {
    const info = (over: Partial<ArtifactInfo>): ArtifactInfo =>
      ({ id: 'x', name: 'report.md.review.json', size: 10, content_type: 'application/json', view: 'json', ...over })
    const items: ArtifactInfo[] = [
      info({ id: 'u2', origin: 'user', version: 2, latest_version: 2 }),
      info({ id: 'g1', origin: 'agent', version: 1, latest_version: 1 }),
      info({ id: 'u1', origin: 'user', version: 1, latest_version: 2 }),
      info({ id: 'r1', name: 'report.md', origin: 'agent', version: 3, latest_version: 3, view: 'markdown' }),
    ]
    const req = (id: string, text: string, status = 'open', reply?: string) =>
      ({ id, anchor: { kind: 'text', quote: 'q' }, text, status, reply, createdAt: '2026-10-06T10:00:00Z' })
    const bodies: Record<string, string> = {
      u1: JSON.stringify({ version: 1, file: { name: 'report.md', artifactId: 'r1' }, requests: [req('old', 'stale')] }),
      u2: JSON.stringify({ version: 1, file: { name: 'report.md', artifactId: 'r1', artifactVersion: 3 }, requests: [req('one', 'first'), req('two', 'second')] }),
      g1: JSON.stringify({ version: 1, file: { name: 'report.md', artifactId: 'r1' }, requests: [req('one', 'first', 'done', 'fixed it')] }),
    }
    const t = createFakeTransport({ files: { items } })
    t.files!.raw = async (_s, id) => { t.files!.rawCalls.push(id); return new Response(bodies[id]).blob() }
    render(<ChatView session={SESSION} transport={t.transport} meta={meta()} slots={{ header: <Probe /> }} />)
    await act(async () => {})
    let review: Awaited<ReturnType<ChatContextValue['reviewFor']>> = null
    await act(async () => { review = await ctx!.reviewFor('report.md') })
    expect(t.files!.rawCalls.sort()).toEqual(['g1', 'u2'])
    expect(review).toEqual({
      version: 1,
      file: { name: 'report.md', artifactId: 'r1', artifactVersion: 3 },
      requests: [
        { id: 'one', anchor: { kind: 'text', quote: 'q' }, text: 'first', status: 'done', reply: 'fixed it', createdAt: '2026-10-06T10:00:00Z' },
        { id: 'two', anchor: { kind: 'text', quote: 'q' }, text: 'second', status: 'open', createdAt: '2026-10-06T10:00:00Z' },
      ],
    })
    // No review of that name: null, and nothing read.
    t.files!.rawCalls.length = 0
    let none: unknown = {}
    await act(async () => { none = await ctx!.reviewFor('other.md') })
    expect(none).toBeNull()
    expect(t.files!.rawCalls).toEqual([])
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

describe('ChatView start and ready', () => {
  beforeEach(() => {
    resetChatState()
  })

  it('shows the start panel (not an empty chat) while starting, and keeps the composer shut', () => {
    seed(
      ev({ seq: 1, kind: 'start_stage', payload: plan }),
      ev({ seq: 2, kind: 'start_stage', payload: { stages: [{ id: 'image', state: 'active', detail: 'Pulling the image' }] } }),
    )
    const t = createFakeTransport()
    render(<ChatView session={SESSION} transport={t.transport} meta={meta({ status: 'starting', runtime: 'cluster' })} />)
    const panel = screen.getByTestId('start-progress')
    expect(panel).toHaveTextContent('Starting session')
    expect(panel.querySelector('[data-stage="image"]')).toHaveAttribute('data-state', 'active')
    expect(panel.querySelector('[data-stage="schedule"]')).toHaveAttribute('data-state', 'done')
    expect(screen.queryByTestId('ready-card')).not.toBeInTheDocument()
    expect(screen.getByPlaceholderText(/starting/)).toBeDisabled()
  })

  it('rebuilds the panel from replayed history after a reload', () => {
    // A reload replays the persisted stage events through the same store, in any order.
    seed(
      ev({ seq: 2, kind: 'start_stage', payload: { stages: [{ id: 'clone', state: 'failed', detail: 'Git authentication failed', hint: 'Check the credential' }] } }),
      ev({ seq: 1, kind: 'start_stage', payload: plan }),
    )
    const t = createFakeTransport()
    render(<ChatView session={SESSION} transport={t.transport} meta={meta({ status: 'error', error_reason: 'Git authentication failed' })} />)
    const panel = screen.getByTestId('start-progress')
    expect(panel).toHaveTextContent("Couldn't start the session")
    expect(panel.querySelector('[data-stage="clone"]')).toHaveAttribute('data-state', 'failed')
    expect(screen.getByTestId('start-hint')).toHaveTextContent('Check the credential')
  })

  it('Stop in the start panel ends the session through the transport', async () => {
    seed(ev({ seq: 1, kind: 'start_stage', payload: plan }))
    const t = createFakeTransport()
    render(<ChatView session={SESSION} transport={t.transport} meta={meta({ status: 'starting' })} />)
    fireEvent.click(screen.getByRole('button', { name: 'Stop session' }))
    await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Confirm stop?' })) })
    expect(t.stops).toBe(1)
  })

  it('shows the session-ready card once live: engine, model, effort, runtime', () => {
    seed(
      ev({ seq: 1, kind: 'start_stage', payload: plan }),
      ev({ seq: 2, kind: 'start_stage', ts: '2026-07-26T00:01:00Z', payload: { stages: [{ id: 'ready', state: 'done' }] } }),
    )
    const t = createFakeTransport()
    render(<ChatView session={SESSION} transport={t.transport} meta={meta({ status: 'idle', runtime: 'cluster', engine: 'claude', effort: 'high' })} />)
    expect(screen.queryByTestId('start-progress')).not.toBeInTheDocument()
    const card = screen.getByTestId('ready-card')
    expect(card).toHaveTextContent('Session ready')
    expect(card).toHaveTextContent(/claude/i)
    expect(card).toHaveTextContent('sonnet-5 · high')
    expect(card).toHaveTextContent(/cluster/i)
    expect(card.querySelector('time')?.getAttribute('datetime')).toBe('2026-07-26T00:01:00.000Z')
    // Nothing sent yet: the composer invites the first message.
    expect(screen.getByPlaceholderText(/to get started/)).not.toBeDisabled()
  })

  it('lets the host head the transcript with a ready card of its own', () => {
    const t = createFakeTransport()
    render(<ChatView session={SESSION} transport={t.transport} meta={meta({ status: 'idle' })} slots={{ readyCard: m => <div data-testid="host-ready">{m.model} is up</div> }} />)
    expect(screen.queryByTestId('ready-card')).toBeNull()
    expect(screen.getByTestId('host-ready')).toHaveTextContent('claude-sonnet-5 is up')
  })

  it('shows the initial prompt as the first user message under the ready card', () => {
    seed(ev({ seq: 1, kind: 'user_message', payload: { text: 'fix the build', source: 'chat' } }))
    const t = createFakeTransport()
    render(<ChatView session={SESSION} transport={t.transport} meta={meta({ status: 'running' })} />)
    const timeline = screen.getByTestId('agent-chat').querySelector('.agent-timeline')!
    const cards = Array.from(timeline.children)
    expect(cards[0]).toHaveAttribute('data-testid', 'ready-card')
    expect(cards[1]).toHaveTextContent('fix the build')
    expect(screen.getByPlaceholderText('Message the agent… (/model, /effort, /<skill>)')).toBeInTheDocument()
  })
})

// ─── Working indicator ──────────────────────────────────────────────────────

describe('ChatView working indicator', () => {
  beforeEach(() => {
    resetChatState()
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval', 'setTimeout', 'clearTimeout', 'Date'] })
    vi.setSystemTime(new Date('2026-07-26T00:10:00Z'))
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  async function sendMessage(text: string) {
    const box = screen.getByRole('textbox') as HTMLTextAreaElement
    fireEvent.change(box, { target: { value: text } })
    await act(async () => { fireEvent.keyDown(box, { key: 'Enter' }) })
  }

  const turnDone = (seq: number) => ev({
    seq, kind: 'turn_done',
    payload: { stop_reason: 'end_turn', model: 'm', usage: { input_tokens: 1, output_tokens: 1, cache_read_tokens: 0, cache_write_tokens: 0 } },
  })

  it('starts optimistically on send, ticks, and clears on turn_done', async () => {
    const t = createFakeTransport()
    const { rerender } = render(<ChatView session={SESSION} transport={t.transport} meta={meta()} />)
    expect(screen.queryByTestId('working')).not.toBeInTheDocument()
    await sendMessage('go')
    // No server round trip yet: the session is still idle.
    const w = screen.getByTestId('working')
    expect(w).toHaveAttribute('role', 'status')
    expect(w).toHaveAttribute('aria-live', 'polite')
    expect(w).toHaveTextContent('Working… 0:00')
    expect(screen.getByText(/Stop/)).toBeInTheDocument()
    act(() => { vi.advanceTimersByTime(2_000) })
    expect(screen.getByTestId('working')).toHaveTextContent('Working… 0:02')
    // The server picks the turn up; the clock keeps counting from the send.
    rerender(<ChatView session={SESSION} transport={t.transport} meta={meta({ status: 'running' })} />)
    act(() => { vi.advanceTimersByTime(40_000) })
    expect(screen.getByTestId('working')).toHaveTextContent('Working… 0:42')
    act(() => t.emit(turnDone(5)))
    rerender(<ChatView session={SESSION} transport={t.transport} meta={meta({ status: 'idle' })} />)
    expect(screen.queryByTestId('working')).not.toBeInTheDocument()
  })

  it('clears on an error event', async () => {
    const t = createFakeTransport()
    render(<ChatView session={SESSION} transport={t.transport} meta={meta()} />)
    await sendMessage('go')
    act(() => t.emit(ev({ seq: 3, kind: 'error', payload: { message: 'boom', retryable: false } })))
    expect(screen.queryByTestId('working')).not.toBeInTheDocument()
  })

  it('clears on interrupt', async () => {
    const t = createFakeTransport()
    render(<ChatView session={SESSION} transport={t.transport} meta={meta()} />)
    await sendMessage('go')
    await act(async () => { fireEvent.click(screen.getByText(/Stop/)) })
    expect(t.interrupts).toBe(1)
    expect(screen.queryByTestId('working')).not.toBeInTheDocument()
  })

  it('does not start for a settings command, but a bare /model is a prompt', async () => {
    const t = createFakeTransport()
    render(<ChatView session={SESSION} transport={t.transport} meta={meta()} />)
    await sendMessage('/model claude-opus-5')
    expect(t.sent).toEqual(['/model claude-opus-5'])
    expect(screen.queryByTestId('working')).not.toBeInTheDocument()
    await sendMessage('/model')
    expect(screen.getByTestId('working')).toBeInTheDocument()
  })

  it('takes the host’s own list of no-turn commands', async () => {
    const t = createFakeTransport()
    render(<ChatView session={SESSION} transport={t.transport} meta={meta()} noTurnCommands={/^\/persona\s+\S/} />)
    await sendMessage('/persona reviewer')
    expect(screen.queryByTestId('working')).not.toBeInTheDocument()
    await sendMessage('/model haiku')
    expect(screen.getByTestId('working')).toBeInTheDocument()
  })

  it('keeps the draft and says so when the link is down', async () => {
    const t = createFakeTransport()
    render(<ChatView session={SESSION} transport={t.transport} meta={meta()} />)
    act(() => t.setConnected(false))
    await sendMessage('go')
    expect(screen.queryByTestId('working')).not.toBeInTheDocument()
    expect(screen.getByTestId('send-notice')).toHaveTextContent('Not connected')
    expect((screen.getByRole('textbox') as HTMLTextAreaElement).value).toBe('go')
    expect(t.sent).toEqual([])
  })

  it('keeps the draft when the transport says nothing was queued', async () => {
    const t = createFakeTransport()
    t.refuseSend(true)
    render(<ChatView session={SESSION} transport={t.transport} meta={meta()} />)
    await sendMessage('go')
    expect(screen.getByTestId('send-notice')).toHaveTextContent('not sent')
    expect((screen.getByRole('textbox') as HTMLTextAreaElement).value).toBe('go')
    expect(screen.queryByTestId('working')).not.toBeInTheDocument()
  })

  it('R2: a link that drops mid-turn keeps the draft, and the send goes through once it is back', async () => {
    const t = createFakeTransport()
    render(<ChatView session={SESSION} transport={t.transport} meta={meta({ status: 'running' })} />)
    act(() => t.setConnected(false))
    await sendMessage('still here')
    expect(screen.getByTestId('send-notice')).toHaveTextContent('Not connected')
    const box = screen.getByRole('textbox') as HTMLTextAreaElement
    expect(box.value).toBe('still here')
    expect(box).not.toBeDisabled()
    expect(t.sent).toEqual([])
    expect(screen.queryByTestId('queued-message')).toBeNull()

    act(() => t.setConnected(true))
    await act(async () => { fireEvent.keyDown(box, { key: 'Enter' }) })
    expect(t.sent).toEqual(['still here'])
    expect(box.value).toBe('')
    expect(screen.queryByTestId('send-notice')).toBeNull()
    expect(screen.getByTestId('queued-message')).toHaveTextContent('still here')
  })

  it('gives up on a message nothing answers', async () => {
    const t = createFakeTransport()
    render(<ChatView session={SESSION} transport={t.transport} meta={meta()} />)
    await sendMessage('go')
    expect(screen.getByTestId('working')).toBeInTheDocument()
    act(() => { vi.advanceTimersByTime(21_000) })
    expect(screen.queryByTestId('working')).not.toBeInTheDocument()
    expect(screen.getByTestId('send-notice')).toHaveTextContent('No response')
  })

  it('keeps working once the session answers (status or events)', async () => {
    const t = createFakeTransport()
    const { rerender } = render(<ChatView session={SESSION} transport={t.transport} meta={meta()} />)
    await sendMessage('go')
    rerender(<ChatView session={SESSION} transport={t.transport} meta={meta({ status: 'running' })} />)
    act(() => { vi.advanceTimersByTime(60_000) })
    expect(screen.getByTestId('working')).toHaveTextContent('Working… 1:00')
    expect(screen.queryByTestId('send-notice')).not.toBeInTheDocument()
  })

  it('measures the turn on the server clock', () => {
    // Browser 30 s fast; the turn started at server 00:09:18, server now is 00:10:00.
    vi.setSystemTime(new Date('2026-07-26T00:10:30Z'))
    const s = useTranscriptStore.getState()
    s.ingest(ev({ seq: 1, kind: 'status_changed', ts: '2026-07-26T00:09:18Z', payload: { status: 'running', reason: 'turn' } }))
    s.ingestReplayDone(SESSION, { lastSeq: 1, hasMore: false, serverTime: '2026-07-26T00:10:00Z' })
    const t = createFakeTransport()
    render(<ChatView session={SESSION} transport={t.transport} meta={meta({ status: 'running' })} />)
    expect(screen.getByTestId('working')).toHaveTextContent('Working… 0:42')
  })

  it('recovers mid-turn after a reload from the status and the turn start', () => {
    seed(
      ev({ seq: 1, kind: 'user_message', payload: { text: 'go', source: 'chat' } }),
      ev({ seq: 2, kind: 'status_changed', ts: '2026-07-26T00:09:18Z', payload: { status: 'running', reason: 'turn' } }),
    )
    const t = createFakeTransport()
    render(<ChatView session={SESSION} transport={t.transport} meta={meta({ status: 'running' })} />)
    expect(screen.getByTestId('working')).toHaveTextContent('Working… 0:42')
  })

  it('keeps a compact indicator in the toolbar while text streams', () => {
    ingest(ev({ kind: 'assistant_text', transient: true, payload: { text: 'part', done: false } }))
    const t = createFakeTransport()
    render(<ChatView session={SESSION} transport={t.transport} meta={meta({ status: 'running' })} />)
    expect(screen.queryByTestId('working')).not.toBeInTheDocument()
    expect(screen.getByTestId('working-compact')).toHaveTextContent('Working…')
  })

  it('shows "Waiting for your answer" — not working — while the agent waits', () => {
    const t = createFakeTransport()
    render(<ChatView session={SESSION} transport={t.transport} meta={meta({ status: 'waiting' })} />)
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
      const t = createFakeTransport()
      render(<ChatView session={SESSION} transport={t.transport} meta={meta({ status: 'running' })} />)
      const w = screen.getByTestId('working')
      expect(w).toHaveAttribute('data-reduced-motion', 'true')
      expect(w.className).toContain('static')
    } finally {
      window.matchMedia = original
    }
  })
})
