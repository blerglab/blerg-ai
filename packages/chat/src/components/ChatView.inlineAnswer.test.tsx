// Card #156: the person answers a question inline (its Needs you card or its card in the chat). The
// question's card says "Answered: orange", by whom and when; the chat keeps one short row ("You
// answered: orange"), never the person's bubble, never Blerg's relay text with the question's id;
// the relayed decisions are short rows too; a dismissed question says so.
import { beforeEach, describe, expect, it } from 'vitest'
import { act, render, screen } from '@testing-library/react'
import ChatView from './ChatView'
import { createFakeTransport } from '../test/fakeTransport'
import { SESSION, ev as mk, meta, resetChatState, seed } from '../test/chat'
import type { SessionMeta } from '../transport/types'
import type { AgentEvent } from '../types'

const running: SessionMeta = meta({ status: 'running', started_at: '2026-10-14T00:00:00Z' })
const ID = '7b5dcd88-7c6a-4a6d-b093-d6417264f6d2'
const QUESTION = "Test question (reply inline in this card): what's your favorite color?"
const RELAY = `Answer to your question ${ID} ("${QUESTION}"): orange`

function ev(seq: number, kind: AgentEvent['kind'], payload: unknown, ts = '2026-10-14T00:00:00Z'): AgentEvent {
  return mk({ seq, kind, ts, payload })
}

describe('an inline answer to a question (#156)', () => {
  beforeEach(() => resetChatState())

  it('updates the card in place and draws no bubble and no uuid, whether or not the event names its source', () => {
    for (const accepted of [
      { client_id: `question-${ID}`, text: RELAY, source: 'question_answer' },
      // A server before #156 recorded no source: the client id still says it is Blerg's relay.
      { client_id: `question-${ID}`, text: RELAY },
    ]) {
      resetChatState()
      seed(
        ev(1, 'ask', { body: QUESTION, message_id: ID, no_wait: true, owner_only: true }),
        ev(2, 'user_message', { text: 'orange', source: 'ask_answer', message_id: ID }, '2026-10-14T00:01:00Z'),
        // Blerg's relay of the answer to the agent, queued while the agent works.
        ev(3, 'input_accepted', accepted),
      )
      const t = createFakeTransport()
      const { unmount } = render(<ChatView session={SESSION} transport={t.transport} meta={running} />)
      expect(screen.queryByTestId('queued-message')).toBeNull()
      expect(document.body.textContent).not.toContain(ID)
      expect(document.body.textContent).not.toContain('Answer to your question')
      expect(screen.getByTestId('ask-closed').textContent).toMatch(/^Answered: orange · by you · /)
      const row = screen.getByTestId('system-note')
      expect(row.textContent).toMatch(/^You\s*answered: orange/)
      expect(document.querySelector('.agent-card.user')).toBeNull()
      unmount()
    }
  })

  it('draws a relayed decision as one short Blerg row without the request id', () => {
    seed(ev(1, 'user_message', {
      text: `Review of plan.md v2 (request ${ID}): APPROVED\nConfirm it before you act: blerg-runner review status ${ID}`,
      source: 'review',
    }))
    render(<ChatView session={SESSION} transport={createFakeTransport().transport} meta={running} />)
    const row = screen.getByTestId('system-note')
    expect(row.textContent).toMatch(/^Blerg\s*Review of plan\.md v2: APPROVED/)
    expect(row.textContent).not.toContain(ID)
    expect(row.textContent).not.toContain('Confirm it before you act')
    expect(document.querySelector('.agent-card.user')).toBeNull()
  })

  it('says a question the person dismissed was dismissed', () => {
    seed(ev(1, 'ask', { body: QUESTION, message_id: ID, no_wait: true, owner_only: true }))
    const t = createFakeTransport()
    render(<ChatView session={SESSION} transport={t.transport} meta={running} />)
    act(() => t.closeAsk(ID, '', true, 'you'))
    expect(screen.getByTestId('ask-closed').textContent).toBe('Dismissed by you (maybe answered in the chat)')
  })

  // Card #155 review: someone else's dismissal is not "by you"; with no one known, it names no one.
  it('says another person dismissed it when it was not you', () => {
    for (const [by, words] of [['other', 'Dismissed by another person (maybe answered in the chat)'], [undefined, 'Dismissed (maybe answered in the chat)']] as const) {
      resetChatState()
      seed(ev(1, 'ask', { body: QUESTION, message_id: ID }))
      const t = createFakeTransport()
      const { unmount } = render(<ChatView session={SESSION} transport={t.transport} meta={running} />)
      act(() => t.closeAsk(ID, '', true, by))
      expect(screen.getByTestId('ask-closed').textContent).toBe(words)
      unmount()
    }
  })

  // Card #155 review I4: a person's own message is drawn as theirs while it is queued, whatever its
  // id: only an event with no source (an older server) is read by its id.
  it("keeps a person's queued message under a relay-like id as their bubble", () => {
    seed(
      ev(1, 'user_message', { text: 'hi', source: 'chat' }),
      ev(2, 'input_accepted', { client_id: `question-${ID}`, text: 'my own words', source: 'chat' }),
    )
    const t = createFakeTransport()
    render(<ChatView session={SESSION} transport={t.transport} meta={running} />)
    expect(screen.getByTestId('queued-message').textContent).toContain('my own words')
  })
})
