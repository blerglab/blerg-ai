// A person's long pasted message shows compacted (first lines, Show all, Open); short ones and a
// session brief are left alone.
import { beforeEach, describe, expect, it } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import ChatView from './ChatView'
import { createFakeTransport } from '../test/fakeTransport'
import { SESSION, ev, meta, resetChatState, seed } from '../test/chat'

const lines = (n: number) => Array.from({ length: n }, (_, i) => `row ${i + 1}`).join('\n')
const live = meta({ status: 'idle' })
const show = (describe?: Parameters<typeof ChatView>[0]['describeUserMessage']) =>
  render(<ChatView session={SESSION} transport={createFakeTransport().transport} meta={live} describeUserMessage={describe} />)

describe('ChatView: pasted snippets', () => {
  beforeEach(() => resetChatState())

  it('compacts a message over 25 lines and expands it in place', () => {
    seed(ev({ seq: 1, payload: { text: lines(40), source: 'human' } }))
    const { container } = show()
    const card = container.querySelector('.agent-card.user')!
    expect(card.textContent).toContain('row 15')
    expect(card.textContent).not.toContain('row 16')
    fireEvent.click(screen.getByRole('button', { name: 'Show all 40 lines' }))
    expect(card.textContent).toContain('row 40')
    fireEvent.click(screen.getByRole('button', { name: 'Show less' }))
    fireEvent.click(screen.getByRole('button', { name: 'Open' }))
    expect(screen.getByRole('dialog').textContent).toContain('row 40')
  })

  it('leaves a short message alone', () => {
    seed(ev({ seq: 1, payload: { text: lines(25), source: 'human' } }))
    show()
    expect(screen.queryByTestId('message-capped')).toBeNull()
    expect(screen.queryByRole('button', { name: /Show all/ })).toBeNull()
  })

  it('does not compact a message that is already the collapsed brief', () => {
    seed(ev({ seq: 1, payload: { text: lines(40), source: 'chat' } }))
    show((_e, p) => (p.source === 'chat' ? { brief: true } : null))
    expect(screen.getByTestId('message-brief')).toBeInTheDocument()
    expect(screen.queryByTestId('message-capped')).toBeNull()
  })

  it('caps the long fence inside a message once, not the message too', () => {
    seed(ev({ seq: 1, payload: { text: 'here:\n```\n' + lines(40) + '\n```', source: 'human' } }))
    show()
    expect(screen.queryByTestId('message-capped')).toBeNull()
    expect(screen.getAllByRole('button', { name: 'Show all 40 lines' })).toHaveLength(1)
  })
})
