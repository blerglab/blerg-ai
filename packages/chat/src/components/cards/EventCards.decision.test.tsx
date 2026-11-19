import { fireEvent, render, screen } from '@testing-library/react'
import { expect, it, vi } from 'vitest'
import { AskContext } from '../askContext'
import { renderEvent, NO_RESULTS } from './EventCards'

it('renders server decision actions in chat', () => {
 const answer = vi.fn(async () => true)
 render(<AskContext.Provider value={{ ownerOnly: 'yes', states: new Map([['decision-event', { open: true, answer: '' }]]), answer }}>
  {renderEvent({ type: 'agent_event', client_event_id: 'decision-event', session_id: 'session', seq: 1, ts: '2026-11-05T12:00:00Z', kind: 'ask', payload: { message_id: 'decision', body: 'Link Engineering (ENG)?', decision: true, owner_only: true, no_wait: true } }, NO_RESULTS)}
 </AskContext.Provider>)
 fireEvent.click(screen.getByRole('button', { name: /Link Engineering/ }))
 expect(screen.queryByRole('textbox')).toBeNull()
 fireEvent.click(screen.getByRole('button', { name: 'Decline' }))
 expect(answer).toHaveBeenCalledWith('no', 'decision')
})
