import { render, screen } from '@testing-library/react'
import { expect, it } from 'vitest'
import { ReadyCard, reportedModel } from './ChatView'
import type { AgentEvent } from '../types'

const ev = (kind: string, payload: Record<string, unknown>, seq: number): AgentEvent =>
  ({ type: 'agent_event', session_id: 's', client_event_id: `c${seq}`, seq, ts: '', kind, payload }) as AgentEvent

// The board sees no configured model for a session on the engine's default (the runner hides the
// startup report from it): the card names the model the turns report, marked as the default.
it('names the reported model when the session runs on the engine default', () => {
  render(<ReadyCard meta={{ status: 'running', started_at: '', engine: 'claude' }} readyAt={0} reportedModel={{ model: 'claude-sonnet-5', chosen: false }} />)
  expect(screen.getByText('sonnet-5')).toBeTruthy()
  expect(screen.getByText('engine default')).toBeTruthy()
})

it('says engine default alone until a turn reports a model, and prefers a configured model', () => {
  const { unmount } = render(<ReadyCard meta={{ status: 'running', started_at: '', engine: 'claude' }} readyAt={0} />)
  expect(screen.getByText('engine default')).toBeTruthy()
  unmount()
  render(<ReadyCard meta={{ status: 'running', started_at: '', engine: 'claude', model: 'claude-haiku-5-5' }} readyAt={0} reportedModel={{ model: 'claude-sonnet-5', chosen: false }} />)
  expect(screen.queryByText('engine default')).toBeNull()
  expect(screen.queryByText('sonnet-5')).toBeNull()
})

it('takes the latest top-level turn or model change, never a subagent', () => {
  expect(reportedModel([
    ev('turn_done', { model: 'claude-sonnet-5', stop_reason: 'end_turn' }, 1),
    ev('model_changed', { model: 'claude-haiku-5-5', effort: '', source: 'user' }, 2),
    ev('turn_done', { model: 'claude-opus-5-5', parent_call_id: 'x', stop_reason: 'end_turn' }, 3),
  ])).toEqual({ model: 'claude-haiku-5-5', chosen: true })
  expect(reportedModel([ev('turn_done', { model: 'claude-sonnet-5', stop_reason: 'end_turn' }, 1)])).toEqual({ model: 'claude-sonnet-5', chosen: false })
  expect(reportedModel([])).toEqual({ model: '', chosen: false })
})

it('does not call a model chosen in the session the engine default', () => {
  render(<ReadyCard meta={{ status: 'running', started_at: '', engine: 'claude' }} readyAt={0} reportedModel={{ model: 'claude-haiku-5-5', chosen: true }} />)
  expect(screen.getByText('haiku-5-5')).toBeTruthy()
  expect(screen.queryByText('engine default')).toBeNull()
})
