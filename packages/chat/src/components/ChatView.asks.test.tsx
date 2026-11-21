import { beforeEach, describe, expect, it } from 'vitest'
import { act, fireEvent, render, screen, within } from '@testing-library/react'
import ChatView from './ChatView'
import { createFakeTransport } from '../test/fakeTransport'
import { SESSION, ev as mk, meta, resetChatState, seed } from '../test/chat'
import type { SessionMeta } from '../transport/types'
import type { AgentEvent } from '../types'

const running: SessionMeta = meta({ status: 'running', started_at: '2026-10-14T00:00:00Z' })

function ev(seq: number, kind: AgentEvent['kind'], payload: unknown): AgentEvent {
  return mk({ seq, kind, ts: '2026-10-14T00:00:00Z', payload })
}

function mount(t = createFakeTransport({ answersAsks: true }), m: SessionMeta = running) {
  render(<ChatView session={SESSION} transport={t.transport} meta={m} />)
  if (screen.queryByTestId('ask-card')) openQuestion()
  return t
}

function openQuestion() {
 fireEvent.click(within(screen.getByTestId('ask-card')).getByRole('button'))
 return screen.getByRole('dialog')
}
const ASK = { body: 'Deploy to **staging** first?', message_id: 'm1' }

describe('the agent asks a question', () => {
  beforeEach(() => {
    resetChatState()
    seed(ev(1, 'user_message', { text: 'go', source: 'chat' }), ev(2, 'ask', ASK))
  })

  it('shows the question with a reply box that answers that ask', async () => {
    const t = mount()
    const card = openQuestion()
    expect(card.textContent).toContain('Deploy to staging first?')
    expect(within(card.querySelector('.ticket-modal-title') as HTMLElement).getByText('staging').tagName).toBe('STRONG')
    expect(within(card).getByRole('heading', { name: 'Deploy to staging first?' })).toBeInTheDocument()
    const box = within(card).getByLabelText('Your answer')
    fireEvent.change(box, { target: { value: 'yes, staging' } })
    await act(async () => { fireEvent.click(within(card).getByRole('button', { name: 'Answer' })) })
    expect(t.sentWith).toHaveLength(1)
    expect(t.sentWith[0].text).toBe('yes, staging')
    expect(t.sentWith[0].opts?.answerTo).toBe('m1')
    expect((box as HTMLTextAreaElement).value).toBe('')
    // The answer is queued like any message, and cleared by its echo.
    expect(screen.getByTestId('queued-message').textContent).toContain('yes, staging')
    const clientId = t.sentWith[0].opts!.clientId!
    act(() => t.emit(ev(3, 'user_message', { text: 'yes, staging', source: 'ask_answer', message_id: 'm1', client_id: clientId })))
    expect(screen.queryByTestId('queued-message')).toBeNull()
    expect(screen.getByTestId('ask-closed').textContent).toMatch(/^Answered: yes, staging · by you · /)
    expect(within(screen.getByTestId('ask-card')).queryByLabelText('Your answer')).toBeNull()
  })

  it('marks owner-only approvals and hides answers from other viewers', () => {
    resetChatState()
    seed(ev(1, 'user_message', { text: 'go', source: 'chat' }), ev(2, 'ask', { ...ASK, owner_only: true }))
    mount()
    const card = openQuestion()
    expect(card.textContent).toContain('owner-only approval')
    expect(card.textContent).toContain('Only the workspace owner can answer')
    expect(within(card).queryByLabelText('Your answer')).toBeNull()
    expect(screen.queryByTestId('ask-composer-hint')).toBeNull()
  })

  it('says who can answer an owner-only approval only once the host knows', () => {
    resetChatState()
    seed(ev(1, 'user_message', { text: 'go', source: 'chat' }), ev(2, 'ask', { ...ASK, owner_only: true }))
    const t = createFakeTransport({ answersAsks: true })
    const { rerender } = render(<ChatView session={SESSION} transport={t.transport} meta={running} canAnswerApprovals={null} />)
    openQuestion()
    // Still loading: the owner must not be told they cannot answer.
    expect(screen.queryByTestId('ask-owner-only')).toBeNull()
    expect(screen.queryByLabelText('Your answer')).toBeNull()
    rerender(<ChatView session={SESSION} transport={t.transport} meta={running} canAnswerApprovals={false} />)
    expect(screen.getByTestId('ask-owner-only').textContent).toBe('Only the workspace owner can answer this approval.')
    rerender(<ChatView session={SESSION} transport={t.transport} meta={running} canAnswerApprovals />)
    openQuestion()
    expect(screen.queryByTestId('ask-owner-only')).toBeNull()
    expect(screen.getByLabelText('Your answer')).toBeInTheDocument()
  })

  it('a question the agent did not wait on is answered on its card, never from the composer', async () => {
    resetChatState()
    seed(ev(1, 'user_message', { text: 'go', source: 'chat' }), ev(2, 'ask', { body: 'Ship the blue one?', message_id: 'q1', owner_only: true, no_wait: true }))
    const t = createFakeTransport({ answersAsks: true })
    render(<ChatView session={SESSION} transport={t.transport} meta={running} canAnswerApprovals />)
    openQuestion()
    const card = openQuestion()
    expect(card.querySelector('.ticket-modal-title')).toHaveTextContent('Ship the blue one?')
    expect(card.textContent).not.toContain('owner-only approval')
    expect(card.textContent).toContain('the agent keeps working')
    // The composer stays the composer: its message goes to the agent as itself.
    expect(screen.queryByPlaceholderText('Answer the agent’s question…')).toBeNull()
    fireEvent.change(within(card).getByLabelText('Your answer'), { target: { value: 'yes' } })
    await act(async () => { fireEvent.click(within(card).getByRole('button', { name: 'Answer' })) })
    expect(t.sentWith[0].opts?.answerTo).toBe('q1')
    // The answer Blerg sends on to the agent is not drawn again.
    act(() => t.emit(ev(3, 'user_message', { text: 'yes', source: 'ask_answer', message_id: 'q1' })))
    act(() => t.emit(ev(4, 'user_message', { text: 'Answer to your question q1 ("Ship the blue one?"): yes', source: 'question_answer' })))
    expect(screen.queryByText(/Answer to your question q1/)).toBeNull()
    expect(screen.getByTestId('ask-closed').textContent).toMatch(/^Answered: yes · by you · /)
  })

  it('points a host that never answers approvals to Runner', () => {
    resetChatState()
    seed(ev(1, 'user_message', { text: 'go', source: 'chat' }), ev(2, 'ask', { ...ASK, owner_only: true }))
    mount()
    expect(screen.getByTestId('ask-owner-only').textContent).toContain('from the workspace in Runner')
  })

  it('lets the signed-in owner answer an owner-only approval', () => {
    resetChatState()
    seed(ev(1, 'user_message', { text: 'go', source: 'chat' }), ev(2, 'ask', { ...ASK, owner_only: true }))
    const t = createFakeTransport({ answersAsks: true })
    render(<ChatView session={SESSION} transport={t.transport} meta={running} canAnswerApprovals />)
    openQuestion()
    expect(screen.getByLabelText('Your answer')).toBeInTheDocument()
    expect(screen.getByRole('dialog').textContent).toContain('owner-only approval')
  })

  it('Ctrl+Enter in the question composer answers too', async () => {
    const t = mount()
    const box = screen.getByLabelText('Your answer')
    fireEvent.change(box, { target: { value: 'no' } })
    await act(async () => { fireEvent.keyDown(box, { key: 'Enter', ctrlKey: true }) })
    expect(t.sentWith.map(s => s.opts?.answerTo)).toEqual(['m1'])
  })

  it('keeps composer messages separate from questions', () => {
    mount()
    expect(screen.queryByTestId('ask-composer-hint')).toBeNull()
    expect(screen.getByTestId('composer-hint')).toBeInTheDocument()
    expect(screen.queryByPlaceholderText('Answer the agent’s question…')).toBeNull()
  })

  it('message_answered closes the card (an answer elsewhere, an expiry)', () => {
    const t = mount()
    act(() => t.closeAsk('m1', ''))
    expect(screen.getByTestId('ask-closed').textContent).toBe('closed')
    expect(screen.queryByTestId('ask-composer-hint')).toBeNull()
    expect(screen.getByTestId('composer-hint')).toBeInTheDocument()
  })

  it('stays open when its blerg-runner ask command returns or the turn ends, until message_answered', () => {
    resetChatState()
    seed(
      ev(1, 'user_message', { text: 'go', source: 'chat' }),
      ev(2, 'tool_call', { tool: 'Bash', call_id: 'b1', input: { command: 'blerg-runner ask "Deploy?"' } }),
      ev(3, 'ask', ASK),
    )
    const t = mount()
    act(() => t.emit(ev(4, 'tool_result', { call_id: 'b1', output: 'Command timed out after 2m', is_error: true, duration_ms: 120000 })))
    act(() => t.emit(ev(5, 'turn_done', { stop_reason: 'end_turn', model: 'm', usage: { input_tokens: 0, output_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0 } })))
    expect(screen.queryByTestId('ask-closed')).toBeNull()
    expect(screen.queryByTestId('ask-composer-hint')).toBeNull()
    act(() => t.closeAsk('m1', ''))
    expect(screen.getByTestId('ask-closed').textContent).toBe('closed')
    expect(screen.queryByTestId('ask-composer-hint')).toBeNull()
  })

  it('a transport that cannot address an answer (the board\'s proxy) gets no reply box: the composer answers it', () => {
    mount(createFakeTransport())
    const card = openQuestion()
    expect(within(card).queryByLabelText('Your answer')).toBeNull()
    expect(card.textContent).toMatch(/Open this question in Runner to answer/)
    expect(screen.queryByTestId('ask-composer-hint')).toBeNull()
  })

  it('a read-only chat shows the question without a reply box', () => {
    const t = createFakeTransport({ answersAsks: true })
    render(<ChatView session={SESSION} transport={t.transport} meta={running} readOnly />)
    expect(within(screen.getByTestId('ask-card')).queryByLabelText('Your answer')).toBeNull()
  })
})

describe('updates and notes', () => {
  beforeEach(() => resetChatState())

  it('are system cards with their text', () => {
    seed(ev(1, 'update', { body: 'Tests pass.', message_id: 'u1' }), ev(2, 'note', { body: 'FYI: the flag is off.' }))
    mount()
    expect(screen.getByTestId('message-update').textContent).toMatch(/update.*Tests pass\./)
    expect(screen.getByTestId('message-note').textContent).toMatch(/note.*the flag is off/)
    expect(screen.queryByTestId('ask-composer-hint')).toBeNull()
  })
})

it('shows a withdrawn question as one quiet notice without an update badge', () => {
 resetChatState()
 seed(ev(1, 'note', { body: 'Question withdrawn', message_id: 'internal-question-id', question_withdrawn: true }))
 mount()
 const notice = screen.getByTestId('question-withdrawn')
 expect(notice).toHaveTextContent('Question withdrawn')
 expect(notice).toHaveClass('system-note')
 expect(notice.querySelector('.message-kind')).toBeNull()
 expect(notice).not.toHaveTextContent('internal-question-id')
})
