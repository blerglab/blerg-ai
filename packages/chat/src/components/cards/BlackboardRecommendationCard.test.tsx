import { fireEvent, render, screen } from '@testing-library/react'
import { expect, it, vi } from 'vitest'
import BlackboardRecommendationCard from './BlackboardRecommendationCard'
import { ChatContext, type ChatContextValue } from '../context'
import type { AgentEvent } from '../../types'

it('shows the three actions, by keyboard, and records Not now', async () => {
  const decide = vi.fn(async () => ({ id: 'r1', reason: '3 workers running at once', state: 'not_now' as const }))
  const ctx = {
    sessionId: 'main',
    blackboardRecommendation: async () => ({ id: 'r1', reason: '3 workers running at once', state: 'pending' as const }),
    decideBlackboardRecommendation: decide,
    openBlackboard: vi.fn(),
  } as unknown as ChatContextValue
  render(<ChatContext.Provider value={ctx}><BlackboardRecommendationCard ev={{ payload: { id: 'r1', reason: '3 workers running at once', state: 'pending' } } as AgentEvent} /></ChatContext.Provider>)
  // One primary action (BLG-395's card shell/buttons, one emphasized): only "Set it up with
  // Main" carries it, the other two stay the plain quiet button.
  expect(await screen.findByRole('button', { name: 'Set it up with Main' })).toHaveClass('primary')
  expect(screen.getByRole('button', { name: 'View blackboard' })).not.toHaveClass('primary')
  expect(screen.getByRole('button', { name: 'Not now' })).not.toHaveClass('primary')
  for (const button of screen.getAllByRole('button')) expect(button).toHaveAttribute('type', 'button')
  const notNow = await screen.findByRole('button', { name: 'Not now' })
  notNow.focus()
  fireEvent.keyDown(notNow, { key: 'Enter' })
  fireEvent.click(notNow)
  await screen.findByText('Put off for later.')
  expect(screen.getByRole('button', { name: 'Open blackboard' })).toHaveAttribute('type', 'button')
  fireEvent.click(screen.getByRole('button', { name: 'Open blackboard' }))
  expect(ctx.openBlackboard).toHaveBeenCalled()
  expect(screen.getByText('Put off for later.').parentElement).toHaveClass('delegation-decided')
  expect(decide).toHaveBeenCalledWith('main', 'r1', 'not_now')
  expect(screen.queryByRole('button', { name: 'Set it up with Main' })).toBeNull()
})

it('viewing the blackboard only navigates', async () => {
  const decide = vi.fn(async () => ({ id: 'r1', reason: '8 cards in progress or linked', state: 'setup_myself' as const }))
  const openBlackboard = vi.fn()
  const ctx = {
    sessionId: 'main',
    blackboardRecommendation: async () => ({ id: 'r1', reason: '8 cards in progress or linked', state: 'pending' as const }),
    decideBlackboardRecommendation: decide,
    openBlackboard,
  } as unknown as ChatContextValue
  render(<ChatContext.Provider value={ctx}><BlackboardRecommendationCard ev={{ payload: { id: 'r1', reason: '8 cards in progress or linked', state: 'pending' } } as AgentEvent} /></ChatContext.Provider>)
  fireEvent.click(await screen.findByRole('button', { name: 'View blackboard' }))
  expect(decide).not.toHaveBeenCalled()
  expect(openBlackboard).toHaveBeenCalled()
})

it('keeps a deferred recommendation reachable after replay', () => {
  const ctx = { sessionId: 'main', openBlackboard: vi.fn() } as unknown as ChatContextValue
  render(<ChatContext.Provider value={ctx}><BlackboardRecommendationCard ev={{ payload: { id: 'r1', reason: 'x', state: 'not_now' } } as AgentEvent} /></ChatContext.Provider>)
  expect(screen.getByText('Put off for later.')).toBeVisible()
  expect(screen.getByRole('button', { name: 'Open blackboard' })).toHaveAttribute('type', 'button')
  fireEvent.click(screen.getByRole('button', { name: 'Open blackboard' }))
  expect(ctx.openBlackboard).toHaveBeenCalled()
})

it.each([
  ['setup_with_main', 'Sent to Main — it’ll follow up with you in the chat.'],
  ['setup_myself', 'Opened the blackboard.'],
])('replays %s as a quiet decision', (state, line) => {
  render(<BlackboardRecommendationCard ev={{ payload: { id: 'r1', reason: 'A shared plan', state } } as AgentEvent} />)
  expect(screen.getByText(line).parentElement).toHaveClass('delegation-decided')
  expect(screen.queryByText('A shared plan')).toBeNull()
})

it('logs a safe cause and keeps the action available when saving fails', async () => {
  const log = vi.spyOn(console, 'warn').mockImplementation(() => {})
  const ctx = {
    sessionId: 'main',
    blackboardRecommendation: async () => ({ id: 'r1', reason: 'A shared plan', state: 'pending' }),
    decideBlackboardRecommendation: async () => { throw new Error('Could not save the decision.') },
  } as unknown as ChatContextValue
  render(<ChatContext.Provider value={ctx}><BlackboardRecommendationCard ev={{ payload: { id: 'r1', reason: 'A shared plan', state: 'pending' } } as AgentEvent} /></ChatContext.Provider>)
  fireEvent.click(await screen.findByRole('button', { name: 'Not now' }))
  expect(await screen.findByRole('alert')).toHaveTextContent('Could not save the decision.')
  expect(log).toHaveBeenCalledWith('recommendation_decision_failed', { kind: 'blackboard', cause: 'transport_rejected' })
  expect(screen.getByRole('button', { name: 'Not now' })).toBeEnabled()
  log.mockRestore()
})

it('shows a deferred outcome without a dead link when the host cannot open Blackboard', () => {
  render(<BlackboardRecommendationCard ev={{ payload: { id: 'r1', reason: 'A shared plan', state: 'not_now' } } as AgentEvent} />)
  expect(screen.getByText('Put off for later.')).toBeVisible()
  expect(screen.queryByRole('button', { name: 'Open blackboard' })).toBeNull()
  expect(screen.queryByRole('link')).toBeNull()
})

it('explains who can decide a read-only pending recommendation', () => {
  render(<BlackboardRecommendationCard ev={{ payload: { id: 'r1', reason: 'A shared plan', state: 'pending' } } as AgentEvent} />)
  expect(screen.getByText('Waiting for the workspace owner.')).toHaveClass('delegation-readonly')
  expect(screen.queryByRole('button')).toBeNull()
})
