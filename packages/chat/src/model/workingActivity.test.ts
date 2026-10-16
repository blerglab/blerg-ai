import { describe, expect, it } from 'vitest'
import { activityLabel, modelRetry, nextAnswering, nextThinking } from './workingActivity'
import type { AgentEvent } from '../types'

let n = 0
const ev = (kind: string, payload: unknown = {}): AgentEvent =>
  ({ type: 'agent_event', session_id: 's', client_event_id: `c${++n}`, seq: n, ts: '', kind, payload }) as AgentEvent
const retry = (attempt?: number, message = 'retrying the API…') =>
  ev('error', { message, retryable: true, code: 'api_retry', ...(attempt ? { attempt } : {}) })

describe('nextThinking', () => {
  it('opens on thinking_started and closes on what cannot come while a think is open', () => {
    expect(nextThinking(false, ev('thinking_started'))).toBe(true)
    for (const close of [
      ev('thinking_done'), ev('assistant_thinking', { text: 't' }), ev('assistant_text', { text: 'a', done: false }),
      ev('tool_call', { tool: 'Read' }), ev('turn_done'), retry(2), ev('error', { message: 'boom', retryable: true }),
      ev('status_changed', { status: 'idle' }), ev('user_message', { text: 'hi', source: 'chat' }),
    ]) expect(nextThinking(true, close), close.kind).toBe(false)
  })
  it('stays as it was for anything else, and for a subagent', () => {
    for (const keep of [
      ev('status_changed', { status: 'running' }), ev('user_message', { text: 'x', source: 'system' }),
      ev('subagent_update'), ev('thinking_started', { parent_call_id: 'a' }), ev('tool_call', { tool: 'Read', parent_call_id: 'a' }),
    ]) {
      expect(nextThinking(true, keep), keep.kind).toBe(true)
    }
    expect(nextThinking(false, ev('thinking_started', { parent_call_id: 'a' }))).toBe(false)
  })
})

describe('modelRetry', () => {
  it('is the newest retry notice with nothing from the model after it', () => {
    const turn = [ev('user_message', { text: 'go', source: 'chat' }), ev('status_changed', { status: 'running' })]
    expect(modelRetry([...turn, retry(1), retry(2)], true)).toEqual({ attempt: 2 })
    expect(modelRetry([...turn, retry(2), ev('status_changed', { status: 'running' })], true)).toEqual({ attempt: 2 })
    // A daemon older than the attempt field says it in the message.
    expect(modelRetry([...turn, retry(undefined, 'retrying the API (attempt 3 of 10) after overloaded_error (529)…')], true)).toEqual({ attempt: 3 })
    expect(modelRetry([...turn, retry()], true)).toEqual({ attempt: null })
  })
  it('ends with the next word from the model, the turn, or a session not running', () => {
    for (const after of [
      ev('assistant_text', { text: 'ok', done: true }), ev('assistant_thinking', { text: 't' }), ev('tool_call', { tool: 'Read' }),
      ev('tool_result', { call_id: 'a' }), ev('turn_done'), ev('error', { message: 'API Error: 529', retryable: true, code: 'api_error' }),
      ev('status_changed', { status: 'idle' }), ev('user_message', { text: 'next', source: 'chat' }),
    ]) expect(modelRetry([retry(2), after], true), after.kind).toBeNull()
    expect(modelRetry([retry(2)], false)).toBeNull()
    expect(modelRetry([], true)).toBeNull()
  })
})

describe('activityLabel', () => {
  it('says thinking over a retry, the attempt when known, and nothing otherwise', () => {
    expect(activityLabel(true, { attempt: 2 })).toBe('Thinking…')
    expect(activityLabel(false, { attempt: 2 })).toBe('Retrying the model (attempt 2)…')
    expect(activityLabel(false, { attempt: null })).toBe('Retrying the model…')
    expect(activityLabel(false, null)).toBeNull()
  })
})

describe('nextAnswering', () => {
  const live = (kind: string, payload: unknown = {}) => ({ ...ev(kind, payload), transient: true }) as AgentEvent
  it("is reset by a retry notice and set by the main agent's next live word", () => {
    expect(nextAnswering(true, retry(2))).toBe(false)
    expect(nextAnswering(false, live('assistant_text', { text: 'Do', done: false }))).toBe(true)
    expect(nextAnswering(false, live('thinking_started'))).toBe(true)
  })
  it('is not moved by a subagent, a stored event or a think ending', () => {
    expect(nextAnswering(false, live('thinking_started', { parent_call_id: 'a' }))).toBe(false)
    expect(nextAnswering(false, live('thinking_done'))).toBe(false)
    expect(nextAnswering(false, ev('status_changed', { status: 'running' }))).toBe(false)
    expect(nextAnswering(true, ev('tool_call', { tool: 'Read' }))).toBe(true)
  })
})
