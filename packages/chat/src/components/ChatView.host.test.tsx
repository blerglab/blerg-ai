// What a host can change about the chat beyond its slots: who a user message is from, whether it
// is drawn as a collapsed brief, and whether the person may write at all.
import { beforeEach, describe, expect, it } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import ChatView from './ChatView'
import type { DescribeUserMessage } from './cards/EventCards'
import { createFakeTransport } from '../test/fakeTransport'
import { SESSION, ev, meta, resetChatState, seed } from '../test/chat'

const live = meta({ status: 'idle' })

// A host like the board: the prompt a session was started with is a brief, its own automation
// speaks as itself, and only what a signed-in person typed is "You".
const describe_: DescribeUserMessage = (_ev, p) => {
  if (p.source === 'chat') return { brief: true }
  if (p.source === 'human') return null
  return { author: 'the board' }
}

describe('ChatView: describeUserMessage', () => {
  beforeEach(() => {
    resetChatState()
    seed(
      ev({ seq: 1, payload: { text: 'You are a worker. Do the card.\n\n**Rules:** many.', source: 'chat' } }),
      ev({ seq: 2, payload: { text: 'review found two problems', source: 'blerg-board' } }),
      ev({ seq: 3, payload: { text: 'please also fix the title', source: 'human' } }),
    )
  })

  it('draws a brief as one collapsed line that opens, and never as the person', () => {
    render(<ChatView session={SESSION} transport={createFakeTransport().transport} meta={live} describeUserMessage={describe_} />)
    const brief = screen.getByTestId('message-brief')
    expect(brief.textContent).toMatch(/session brief · \d+ chars/)
    expect(screen.queryByText(/You are a worker/)).toBeNull()
    fireEvent.click(brief.querySelector('button')!)
    expect(screen.getByText(/You are a worker/)).toBeInTheDocument()
    expect(brief.querySelector('strong')?.textContent).toBe('Rules:') // rendered as markdown
    expect(brief.querySelector('.card-author')).toBeNull()
  })

  it('names another author in place of You, and leaves the person’s own message alone', () => {
    const { container } = render(<ChatView session={SESSION} transport={createFakeTransport().transport} meta={live} describeUserMessage={describe_} />)
    const cards = [...container.querySelectorAll('.agent-card.user')]
    expect(cards).toHaveLength(2)
    expect(cards[0].querySelector('.card-author')?.textContent).toBe('the board')
    expect(cards[0].classList.contains('other-author')).toBe(true)
    expect(cards[1].querySelector('.card-author')?.textContent).toBe('You')
    expect(cards[1].classList.contains('other-author')).toBe(false)
  })

  it('uses the host’s own label for a brief', () => {
    render(<ChatView session={SESSION} transport={createFakeTransport().transport} meta={live}
      describeUserMessage={(_e, p) => (p.source === 'chat' ? { brief: 'kickoff' } : null)} />)
    expect(screen.getByTestId('message-brief').textContent).toMatch(/^.*kickoff · /)
  })

  it('without the hook every user message is the person’s, as before', () => {
    const { container } = render(<ChatView session={SESSION} transport={createFakeTransport().transport} meta={live} />)
    expect(screen.queryByTestId('message-brief')).toBeNull()
    expect([...container.querySelectorAll('.agent-card.user .card-author')].map(n => n.textContent)).toEqual(['You', 'You', 'You'])
  })
})

describe('ChatView: readOnly and placeholder', () => {
  beforeEach(() => {
    resetChatState()
    seed(ev({ seq: 1, payload: { text: 'hello', source: 'human' } }))
  })

  it('readOnly shows the transcript and files but no composer, attach control or hint', () => {
    const t = createFakeTransport()
    render(<ChatView session={SESSION} transport={t.transport} meta={meta({ status: 'running' })} readOnly />)
    expect(screen.getByText('hello')).toBeInTheDocument()
    expect(screen.queryByRole('textbox')).toBeNull()
    expect(screen.queryByRole('button', { name: 'Send' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Attach files' })).toBeNull()
    expect(screen.queryByTestId('attach-input')).toBeNull()
    expect(screen.queryByTestId('composer-hint')).toBeNull()
    expect(screen.getByTestId('files-toggle')).toBeInTheDocument()
  })

  it('uses the host’s placeholder for a session that can take a message', () => {
    render(<ChatView session={SESSION} transport={createFakeTransport().transport} meta={live} placeholder="steer the session…" />)
    expect(screen.getByPlaceholderText('steer the session…')).toBeInTheDocument()
  })

  it('keeps its own wording for a session that cannot take one', () => {
    render(<ChatView session={SESSION} transport={createFakeTransport().transport} meta={meta({ status: 'stopped' })} placeholder="steer the session…" />)
    expect(screen.getByPlaceholderText(/has ended/)).toBeInTheDocument()
  })
})
