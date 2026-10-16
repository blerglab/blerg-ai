import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen } from '@testing-library/react'
import ChatView from './ChatView'
import { QUEUED_IDLE_MS, QUEUED_LINGER_MS } from '../hooks/useSession'
import { useTranscriptStore } from '../hooks/useTranscriptStore'
import { createFakeTransport } from '../test/fakeTransport'
import { SESSION, ev as mk, meta, resetChatState, seed } from '../test/chat'
import type { SessionMeta } from '../transport/types'
import type { AgentEvent } from '../types'

const base: SessionMeta = meta({ status: 'running', started_at: '2026-09-30T00:00:00Z' })

function ev(seq: number, kind: AgentEvent['kind'], payload: unknown): AgentEvent {
  return mk({ seq, kind, ts: '2026-09-30T00:00:00Z', payload })
}

function type(text: string) {
  const box = screen.getByRole('textbox') as HTMLTextAreaElement
  fireEvent.change(box, { target: { value: text } })
  return box
}

async function enter(box: HTMLElement) {
  await act(async () => { fireEvent.keyDown(box, { key: 'Enter' }) })
}

function mount(t = createFakeTransport(), m: SessionMeta = base) {
  const r = render(<ChatView session={SESSION} transport={t.transport} meta={m} />)
  return { t, ...r, rerender: (m2: SessionMeta) => r.rerender(<ChatView session={SESSION} transport={t.transport} meta={m2} />) }
}

describe('ChatView queued messages and Esc', () => {
  beforeEach(() => {
    resetChatState()
    seed(ev(1, 'user_message', { text: 'start', source: 'chat' }))
  })

  it('shows a message sent mid-turn as queued, clears the box, and swaps it for the real event', async () => {
    const scroll = vi.fn()
    Element.prototype.scrollIntoView = scroll
    const { t } = mount()
    scroll.mockClear()
    const box = type('and also check the tests')
    await enter(box)
    // The queued bubble is the newest thing in the transcript: the view follows it down.
    expect(scroll).toHaveBeenCalled()

    expect(t.sent).toEqual(['and also check the tests'])
    expect(box.value).toBe('')
    expect(screen.getByTestId('queued-message').textContent).toContain('and also check the tests')
    expect(screen.getByTestId('queued-message').textContent).toMatch(/Queued/)
    // the message is already with the agent: it says when it will be picked up
    expect(screen.getByTestId('queued-message').textContent).toMatch(/next step/)

    // The turn picks it up: the real event replaces the queued bubble.
    act(() => t.emit(ev(9, 'user_message', { text: 'and also check the tests', source: 'chat' })))
    expect(screen.queryByTestId('queued-message')).toBeNull()
    expect(screen.getAllByText('and also check the tests')).toHaveLength(1)
  })

  it('keeps two identical queued messages apart and resolves them one at a time', async () => {
    const { t } = mount()
    const box = type('ok')
    await enter(box)
    type('ok')
    await enter(box)
    expect(screen.getAllByTestId('queued-message')).toHaveLength(2)

    act(() => t.emit(ev(10, 'user_message', { text: 'ok', source: 'chat' })))
    expect(screen.getAllByTestId('queued-message')).toHaveLength(1)
    act(() => t.emit(ev(11, 'user_message', { text: 'ok', source: 'chat' })))
    expect(screen.queryByTestId('queued-message')).toBeNull()
  })

  it('shows a message sent to an idle or disconnected session at once, as sending, but not slash commands', async () => {
    const { t } = mount(createFakeTransport(), { ...base, status: 'disconnected' })
    const box = type('hello')
    await enter(box)
    expect(screen.getByTestId('queued-message').textContent).toContain('hello')
    expect(screen.getByTestId('queued-message').textContent).toMatch(/Sending/)
    expect(screen.getByTestId('queued-message').textContent).not.toMatch(/Queued/)
    // the real event replaces it
    act(() => t.emit(ev(9, 'user_message', { text: 'hello', source: 'chat' })))
    expect(screen.queryByTestId('queued-message')).toBeNull()

    const box2 = type('/model haiku')
    await enter(box2)
    expect(t.sent).toEqual(['hello', '/model haiku'])
    expect(screen.queryByTestId('queued-message')).toBeNull()
  })

  it('keeps a message that was never picked up on screen, marked as not delivered, when the session fails', async () => {
    const { rerender } = mount()
    const box = type('one more thing')
    await enter(box)
    expect(screen.getByTestId('queued-message')).toBeTruthy()
    rerender({ ...base, status: 'error' })
    expect(screen.getByTestId('queued-message').textContent).toMatch(/Not delivered/)
    expect(screen.getByTestId('queued-message').textContent).toContain('one more thing')
  })

  it('Esc interrupts a running turn, and does nothing when idle', () => {
    const t = createFakeTransport()
    const { unmount } = mount(t)
    fireEvent.keyDown(screen.getByRole('textbox'), { key: 'Escape' })
    expect(t.interrupts).toBe(1)
    unmount()

    const t2 = createFakeTransport()
    mount(t2, { ...base, status: 'idle' })
    fireEvent.keyDown(screen.getByRole('textbox'), { key: 'Escape' })
    expect(t2.interrupts).toBe(0)
  })

  it('tells you Enter sends and Esc interrupts while it works, and not when idle', () => {
    const { unmount } = mount()
    expect(screen.getByTestId('composer-hint').textContent).toMatch(/Enter sends/)
    expect(screen.getByTestId('composer-hint').textContent).toMatch(/Esc interrupts/)
    unmount()
    mount(createFakeTransport(), { ...base, status: 'idle' })
    expect(screen.queryByTestId('composer-hint')).toBeNull()
  })

  it('does not promise steering on an engine that holds the message until the turn ends', async () => {
    mount(createFakeTransport(), { ...base, engine: 'hermes' })
    expect(screen.getByTestId('composer-hint').textContent).toMatch(/Enter queues/)
    const box = type('later')
    await enter(box)
    expect(screen.getByTestId('queued-message').textContent).not.toMatch(/next step/)
  })

  it('offers mid-turn steering for Codex sessions', async () => {
    mount(createFakeTransport(), { ...base, engine: 'codex' })
    expect(screen.getByTestId('composer-hint').textContent).toMatch(/Enter sends/)
    const box = type('change direction')
    await enter(box)
    expect(screen.getByTestId('queued-message').textContent).toMatch(/next step/)
  })

  it('puts Attach beside Send, after the text box', () => {
    mount(createFakeTransport(), { ...base, status: 'idle' })
    const attach = screen.getByRole('button', { name: 'Attach files' })
    const send = screen.getByRole('button', { name: 'Send' })
    const box = screen.getByRole('textbox')
    expect(attach.parentElement).toBe(send.parentElement)
    expect(attach.parentElement?.classList.contains('agent-composer-actions')).toBe(true)
    // textarea first, then the Attach and Send buttons
    expect(box.compareDocumentPosition(attach) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(attach.compareDocumentPosition(send) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  it('puts the host’s own controls beside Send too', () => {
    render(<ChatView session={SESSION} transport={createFakeTransport().transport} meta={base} slots={{ composerActions: <button type="button">Rules</button> }} />)
    const rules = screen.getByRole('button', { name: 'Rules' })
    expect(rules.parentElement?.classList.contains('agent-composer-actions')).toBe(true)
  })

  it('says there is no pod when a cluster session is disconnected, and what a message will do', () => {
    mount(createFakeTransport(), { ...base, status: 'disconnected', runtime: 'cluster' })
    const banner = screen.getByTestId('offline-banner')
    expect(banner.textContent).toMatch(/No pod is running/)
    expect(banner.textContent).toMatch(/new pod will start/)
    expect(banner.textContent).toMatch(/uncommitted changes are not/)
  })

  it('shows a resume’s progress after the conversation, in place of the no-pod banner', () => {
    const gone: SessionMeta = { ...base, status: 'disconnected', runtime: 'cluster' }
    const plan = (detail?: string) => ({ plan: true, runtime: 'cluster', stages: [
      { id: 'queued', label: 'Queued', state: 'active', detail }, { id: 'ready', label: 'Ready', state: 'pending' },
    ] })
    const ready = { stages: [{ id: 'queued', state: 'done' }, { id: 'ready', state: 'done' }] }
    // The session's first start, long done, then its conversation.
    seed(ev(2, 'start_stage', plan()), ev(3, 'start_stage', ready), ev(4, 'assistant_text', { text: 'the first answer', done: true }))
    const { t, rerender } = mount(createFakeTransport(), gone)
    // The pod is gone and nothing has been sent: the banner, and no progress panel.
    expect(screen.getByTestId('offline-banner').textContent).toMatch(/No pod is running/)
    expect(screen.queryByTestId('start-progress')).toBeNull()

    // A message was sent: the server opens a new start attempt for the resume.
    act(() => t.emit(ev(5, 'start_stage', plan('Resuming: creating a new Job'))))
    const panel = screen.getByTestId('start-progress')
    expect(panel.textContent).toMatch(/Resuming session/)
    expect(panel.textContent).toMatch(/creating a new Job/)
    expect(screen.queryByTestId('offline-banner')).toBeNull()
    // It sits after the conversation, not above it.
    const answer = screen.getByText('the first answer')
    expect(answer.compareDocumentPosition(panel) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()

    // The new pod is ready: the panel goes, and so does the banner.
    act(() => t.emit(ev(6, 'start_stage', ready)))
    rerender({ ...gone, status: 'idle' })
    expect(screen.queryByTestId('start-progress')).toBeNull()
    expect(screen.queryByTestId('offline-banner')).toBeNull()
  })

  it('words the banner for a machine session differently, and shows none for a live session', () => {
    const { unmount } = mount(createFakeTransport(), { ...base, status: 'disconnected', runtime: 'daemon' })
    expect(screen.getByTestId('offline-banner').textContent).toMatch(/disconnected from its machine/)
    unmount()
    mount(createFakeTransport(), { ...base, status: 'idle' })
    expect(screen.queryByTestId('offline-banner')).toBeNull()
  })

  it('draws a sent-but-unrecorded message as a quiet bubble with a spinner and a status line', async () => {
    const { container } = mount(createFakeTransport(), { ...base, status: 'disconnected' })
    const box = type('first')
    await enter(box)
    const bubble = screen.getByTestId('queued-message')
    expect(bubble.classList.contains('sending')).toBe(true)
    expect(bubble.querySelector('.queue-spinner')).not.toBeNull()
    expect(bubble.querySelector('.card-author')?.textContent).toBe('You')
    expect(bubble.querySelector('.queue-status')?.textContent).toMatch(/Sending/)
    expect(container.querySelector('.agent-card.user.queued')).not.toBeNull()
  })

  it('numbers the queue when several messages wait behind a running turn', async () => {
    mount()
    const box = type('one')
    await enter(box)
    type('two')
    await enter(box)
    type('three')
    await enter(box)
    const statuses = screen.getAllByTestId('queued-message').map(b => b.querySelector('.queue-status')?.textContent)
    expect(statuses).toEqual(['Queued · 1 of 3', 'Queued · 2 of 3', 'Queued · 3 of 3'])
  })

  it('marks a message the session never picked up with a warning and no spinner', async () => {
    const { rerender } = mount()
    const box = type('lost')
    await enter(box)
    rerender({ ...base, status: 'error' })
    const bubble = screen.getByTestId('queued-message')
    expect(bubble.classList.contains('failed')).toBe(true)
    expect(bubble.querySelector('.queue-spinner')).toBeNull()
    expect(bubble.querySelector('.queue-status')?.textContent).toMatch(/Not delivered/)
  })

  it('keeps every toolbar button nameable and iconised so it can collapse to icons on a narrow screen', () => {
    const { container } = mount()
    for (const name of [/Files/, 'Stop']) {
      const btn = screen.getByRole('button', { name })
      expect(btn.querySelector('.tb-icon')).not.toBeNull()
      expect(btn.querySelector('.tb-label')).not.toBeNull()
    }
    const compact = container.querySelector('.compact-toggle')
    expect(compact?.querySelector('.tb-icon')).not.toBeNull()
    expect(screen.getByRole('checkbox', { name: 'Compact tool calls' })).toBeTruthy()
  })

  it('leads the toolbar with the host’s header slot', () => {
    render(<ChatView session={SESSION} transport={createFakeTransport().transport} meta={base} slots={{ header: <select aria-label="Model"><option>m</option></select> }} />)
    const toolbar = screen.getByTestId('agent-chat').querySelector('.agent-toolbar')!
    expect(toolbar.firstElementChild).toBe(screen.getByLabelText('Model'))
  })

  it('says a stopped session has ended, says why, and locks the composer', () => {
    mount(createFakeTransport(), { ...base, status: 'stopped', end_reason: 'stopped_by_user', ended_by: { kind: 'human', self: true } })
    const banner = screen.getByTestId('ended-banner')
    expect(banner.textContent).toMatch(/This session has ended/)
    expect(banner.textContent).toMatch(/Ended by you/)
    expect(banner.textContent).toMatch(/start a new session/)
    expect((screen.getByRole('textbox') as HTMLTextAreaElement).disabled).toBe(true)
    expect((screen.getByRole('textbox') as HTMLTextAreaElement).placeholder).toMatch(/has ended/)
    expect(screen.getByRole('button', { name: 'Send' })).toBeDisabled()
  })

  it('still says it ended when no reason was recorded, and shows nothing for a live session', () => {
    const { unmount } = mount(createFakeTransport(), { ...base, status: 'ended' })
    expect(screen.getByTestId('ended-banner').textContent).toMatch(/This session has ended/)
    unmount()
    mount(createFakeTransport(), { ...base, status: 'idle' })
    expect(screen.queryByTestId('ended-banner')).toBeNull()
    expect((screen.getByRole('textbox') as HTMLTextAreaElement).disabled).toBe(false)
  })

  it('keeps a separate draft per session, and nothing queued carries over', async () => {
    const t = createFakeTransport()
    const { rerender } = render(<ChatView session={SESSION} transport={t.transport} meta={base} />)
    const box = type('for s1')
    await enter(box)
    expect(screen.getByTestId('queued-message')).toBeTruthy()
    type('unsent s1 draft')
    rerender(<ChatView session="s2" transport={t.transport} meta={base} />)
    expect((screen.getByRole('textbox') as HTMLTextAreaElement).value).toBe('')
    expect(screen.queryByTestId('queued-message')).toBeNull()
    rerender(<ChatView session={SESSION} transport={t.transport} meta={base} />)
    expect((screen.getByRole('textbox') as HTMLTextAreaElement).value).toBe('unsent s1 draft')
    expect(useTranscriptStore.getState().sessions.s2).toBeUndefined()
  })
})

describe('harness text in the user role', () => {
  beforeEach(() => {
    resetChatState()
  })

  it('is never drawn as the person, whatever source an older pod recorded', () => {
    seed(
      ev(1, 'user_message', { text: 'start', source: 'chat' }),
      ev(2, 'user_message', { text: '<task-notification>\n<task-id>x</task-id>\n<summary>Background command finished</summary>\n</task-notification>', source: 'chat' }),
      ev(3, 'user_message', { text: '  <system-reminder>\nThe file changed.\n</system-reminder>', source: 'chat' }),
      ev(4, 'user_message', { text: '[SYSTEM NOTIFICATION - NOT USER INPUT]\nsomething', source: 'chat' }),
      ev(5, 'user_message', { text: 'a real follow-up', source: 'chat' }),
    )
    mount()
    const authored = screen.getAllByText('You').length
    expect(authored).toBe(2)
    expect(screen.queryByText(/Background command finished/)).toBeNull()
    expect(screen.queryByText(/SYSTEM NOTIFICATION/)).toBeNull()
    expect(screen.getByText('a real follow-up')).toBeInTheDocument()
  })
})

describe('queued messages matched by their client id', () => {
  beforeEach(() => {
    resetChatState()
    seed(ev(1, 'user_message', { text: 'start', source: 'chat' }))
  })

  it('sends each message with a client id of its own', async () => {
    const { t } = mount()
    const box = type('one')
    await enter(box)
    type('two')
    await enter(box)
    const ids = t.sentWith.map(s => s.opts?.clientId)
    expect(ids[0]).toBeTruthy()
    expect(ids[1]).toBeTruthy()
    expect(ids[0]).not.toBe(ids[1])
  })

  it('clears the bubble whose id the event echoes, whatever text was recorded', async () => {
    const { t } = mount()
    const box = type('/review the plan')
    await enter(box)
    type('and then the tests')
    await enter(box)
    const [first, second] = t.sentWith.map(s => s.opts!.clientId!)
    // The second is recorded first, under another text: only its own bubble goes.
    act(() => t.emit(ev(10, 'user_message', { text: 'something else entirely', source: 'chat', client_id: second })))
    const left = screen.getAllByTestId('queued-message')
    expect(left).toHaveLength(1)
    expect(left[0].textContent).toContain('/review the plan')
    act(() => t.emit(ev(11, 'user_message', { text: 'the skill body', source: 'chat', client_id: first })))
    expect(screen.queryByTestId('queued-message')).toBeNull()
  })

  it('a dropped message clears its bubble and shows an error row', async () => {
    const { t } = mount()
    const box = type('one more thing')
    await enter(box)
    const id = t.sentWith[0].opts!.clientId!
    act(() => t.emit(ev(10, 'error', { message: 'message queue full — dropped', retryable: true, client_id: id })))
    expect(screen.queryByTestId('queued-message')).toBeNull()
    expect(screen.getByTestId('message-dropped').textContent).toMatch(/not delivered.*queue full/)
    // The turn that was running goes on.
    expect(screen.getByTestId('composer-hint')).toBeInTheDocument()
  })

  it('a message never recorded under its text no longer holds up the ones after it (an older daemon)', async () => {
    const { t } = mount()
    const box = type('/review the plan')
    await enter(box)
    type('next')
    await enter(box)
    act(() => t.emit(ev(10, 'user_message', { text: 'the whole SKILL.md body', source: 'chat' })))
    act(() => t.emit(ev(11, 'user_message', { text: 'next', source: 'chat' })))
    const left = screen.getAllByTestId('queued-message')
    expect(left).toHaveLength(1)
    expect(left[0].textContent).toContain('/review the plan')
  })
})

describe('a queued message nothing records', () => {
  beforeEach(() => {
    resetChatState()
    seed(ev(1, 'user_message', { text: 'start', source: 'chat' }))
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
  })
  afterEach(() => { vi.useRealTimers() })

  const turnDone = (seq: number) => ev(seq, 'turn_done', {
    stop_reason: 'end_turn', model: 'm', usage: { input_tokens: 1, output_tokens: 1, cache_read_tokens: 0, cache_write_tokens: 0 },
  })

  it('gives up once the session has sat idle after a later turn end, says so, then goes', async () => {
    const { t, rerender } = mount()
    await enter(type('lost on the way'))
    act(() => t.emit(turnDone(5)))
    rerender({ ...base, status: 'idle' })
    act(() => { vi.advanceTimersByTime(QUEUED_IDLE_MS - 1) })
    expect(screen.getByTestId('queued-message').textContent).not.toMatch(/may not have been delivered/i)
    act(() => { vi.advanceTimersByTime(1) })
    expect(screen.getByTestId('queued-message').textContent).toMatch(/may not have been delivered/i)
    act(() => { vi.advanceTimersByTime(QUEUED_LINGER_MS) })
    expect(screen.queryByTestId('queued-message')).toBeNull()
  })

  it('does not give up while the session is still running, or once its event arrives', async () => {
    const { t, rerender } = mount()
    await enter(type('picked up later'))
    act(() => t.emit(turnDone(5)))
    act(() => { vi.advanceTimersByTime(QUEUED_IDLE_MS * 2) })
    expect(screen.getByTestId('queued-message').textContent).not.toMatch(/may not have been delivered/i)
    rerender({ ...base, status: 'idle' })
    act(() => t.emit(ev(6, 'user_message', { text: 'picked up later', source: 'chat', client_id: t.sentWith[0].opts!.clientId })))
    act(() => { vi.advanceTimersByTime(QUEUED_IDLE_MS * 2) })
    expect(screen.queryByTestId('queued-message')).toBeNull()
  })

  it('gives up when the send went unanswered', async () => {
    mount(createFakeTransport(), { ...base, status: 'idle' })
    await enter(type('anyone there?'))
    act(() => { vi.advanceTimersByTime(20_000) })
    expect(screen.getByTestId('send-notice')).toBeInTheDocument()
    expect(screen.getByTestId('queued-message').textContent).toMatch(/may not have been delivered/i)
    act(() => { vi.advanceTimersByTime(QUEUED_LINGER_MS) })
    expect(screen.queryByTestId('queued-message')).toBeNull()
  })

  it('a message once recorded does not come back when the transcript is loaded again', async () => {
    const { t } = mount()
    await enter(type('recorded'))
    act(() => t.emit(ev(7, 'user_message', { text: 'recorded', source: 'chat', client_id: t.sentWith[0].opts!.clientId })))
    expect(screen.queryByTestId('queued-message')).toBeNull()
    act(() => useTranscriptStore.getState().resetEvents(SESSION))
    expect(screen.queryByTestId('queued-message')).toBeNull()
  })
})
