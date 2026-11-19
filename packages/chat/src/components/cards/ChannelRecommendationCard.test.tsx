import { fireEvent, render, screen } from '@testing-library/react'
import { expect, it, vi } from 'vitest'
import ChannelRecommendationCard from './ChannelRecommendationCard'
import { ChatContext, type ChatContextValue } from '../context'
import type { AgentEvent } from '../../types'

it('lets the person keep the topic here and removes the actions', async () => {
  const decide = vi.fn(async () => ({ id: 'r1', topic: 'TDD skill', state: 'kept' as const }))
  const ctx = { sessionId: 'main', channelRecommendation: async () => ({ id: 'r1', topic: 'TDD skill', state: 'pending' as const }), decideChannelRecommendation: decide } as unknown as ChatContextValue
  render(<ChatContext.Provider value={ctx}><ChannelRecommendationCard ev={{ payload: { id: 'r1', topic: 'TDD skill', state: 'pending' } } as AgentEvent} /></ChatContext.Provider>)
  fireEvent.click(await screen.findByRole('button', { name: 'Keep in this thread' }))
  expect((await screen.findByText('Kept in this thread')).parentElement).toHaveClass('delegation-decided')
  expect(screen.getByTestId('channel-recommendation')).toHaveClass('agent-card', 'delegation-card')
  expect(decide).toHaveBeenCalledWith('main', 'r1', 'keep')
  expect(screen.queryByRole('button', { name: 'Approve' })).toBeNull()
})

it.each(['kept', 'approved'])('replays %s as a quiet decision', state => {
  render(<ChannelRecommendationCard ev={{ payload: { id: 'r1', topic: 'Release notes', state, ...(state === 'approved' ? { channel_id: 'channel/1' } : {}) } } as AgentEvent} />)
  expect(screen.getByText(state === 'kept' ? 'Kept in this thread' : 'Side channel approved').parentElement).toHaveClass('delegation-decided')
  if (state === 'approved') expect(screen.getByRole('link', { name: 'Open channel' })).toHaveAttribute('href', '/sessions/channel%2F1')
  expect(screen.queryByText('Release notes')).toBeNull()
})

it('explains who can decide a read-only pending recommendation', () => {
  render(<ChannelRecommendationCard ev={{ payload: { id: 'r1', topic: 'Release notes', state: 'pending' } } as AgentEvent} />)
  expect(screen.getByText('Waiting for the workspace owner.')).toHaveClass('delegation-readonly')
  expect(screen.queryByRole('button')).toBeNull()
})

it('approves the channel and reveals its link without submitting the surrounding form', async () => {
  const decide = vi.fn(async () => ({ id: 'r1', topic: 'Release notes', state: 'approved' as const, channel_id: 'channel-1' }))
  const submit = vi.fn((event: React.FormEvent) => event.preventDefault())
  const ctx = { sessionId: 'main', channelRecommendation: async () => ({ id: 'r1', topic: 'Release notes', state: 'pending' }), decideChannelRecommendation: decide } as unknown as ChatContextValue
  render(<form onSubmit={submit}><ChatContext.Provider value={ctx}><ChannelRecommendationCard ev={{ payload: { id: 'r1', topic: 'Release notes', state: 'pending' } } as AgentEvent} /></ChatContext.Provider></form>)
  fireEvent.click(await screen.findByRole('button', { name: 'Approve' }))
  expect(await screen.findByRole('link', { name: 'Open channel' })).toHaveAttribute('href', '/sessions/channel-1')
  expect(decide).toHaveBeenCalledWith('main', 'r1', 'approve')
  expect(submit).not.toHaveBeenCalled()
  expect(screen.queryByRole('button', { name: 'Approve' })).toBeNull()
})
