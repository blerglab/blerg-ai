import { describe, expect, it } from 'vitest'
import { askStates } from './asks'
import type { AgentEvent } from '../types'

let n = 0
function ev(kind: AgentEvent['kind'], payload: unknown): AgentEvent {
  n++
  return { type: 'agent_event', session_id: 's1', client_event_id: `e${n}`, seq: n, ts: '', kind, payload }
}
const NONE = new Map<string, string>()

describe('askStates', () => {
  it('an ask is open until the answer naming it arrives', () => {
    const ask = ev('ask', { body: 'Ship it?', message_id: 'm1' })
    const open = askStates([ask], NONE, false)
    expect(open.open).toBe(ask)
    expect(open.byEvent.get(ask.client_event_id)).toEqual({ open: true, answer: '' })
    const done = askStates([ask, ev('user_message', { text: 'yes', source: 'ask_answer', message_id: 'm1' })], NONE, false)
    expect(done.open).toBeNull()
    expect(done.byEvent.get(ask.client_event_id)).toMatchObject({ open: false, answer: 'yes', by: 'you' })
  })

  it('an answer to another ask, or a plain message, leaves an ask with an id open', () => {
    const ask = ev('ask', { body: 'Which one?', message_id: 'm2' })
    const r = askStates([
      ask,
      ev('user_message', { text: 'unrelated', source: 'chat' }),
      ev('user_message', { text: 'other', source: 'ask_answer', message_id: 'mX' }),
    ], NONE, false)
    expect(r.open).toBe(ask)
  })

  it('the server closing it, the turn ending, or the session ending closes it', () => {
    const ask = ev('ask', { body: '?', message_id: 'm3' })
    expect(askStates([ask], new Map([['m3', '']]), false).byEvent.get(ask.client_event_id)).toEqual({ open: false, answer: '' })
    expect(askStates([ask], new Map([['m3', 'from the Messages page']]), false).byEvent.get(ask.client_event_id)?.answer).toBe('from the Messages page')
    expect(askStates([ask], NONE, true).open).toBeNull()
    // The turn ending does not: the server still holds it open.
    expect(askStates([ask, ev('turn_done', { stop_reason: 'end_turn' })], NONE, false).open).toBe(ask)
    const old = ev('ask', { body: 'no id' })
    expect(askStates([old, ev('turn_done', { stop_reason: 'end_turn' })], NONE, false).open).toBeNull()
  })

  it('an ask recorded without an id is answered by the next answer or message of the person', () => {
    const ask = ev('ask', { body: 'old?' })
    expect(askStates([ask, ev('user_message', { text: 'sure', source: 'chat' })], NONE, false).byEvent.get(ask.client_event_id)).toEqual({ open: false, answer: 'sure' })
    expect(askStates([ask, ev('user_message', { text: 'x', source: 'system' })], NONE, false).open).toBe(ask)
    const ask2 = ev('ask', { body: 'old 2?' })
    expect(askStates([ask2, ev('user_message', { text: 'ok', source: 'ask_answer' })], NONE, false).open).toBeNull()
  })

  it("a board's or a cron's message is not the person answering; a parent session's is", () => {
    const ask = ev('ask', { body: 'old?' })
    expect(askStates([ask, ev('user_message', { text: 'Card #43 moved to Review', source: 'blerg-board' })], NONE, false).open).toBe(ask)
    expect(askStates([ask, ev('user_message', { text: 'Summarize my inbox', source: 'cron' })], NONE, false).open).toBe(ask)
    expect(askStates([ask, ev('user_message', { text: 'use the second', source: 'session' })], NONE, false).byEvent.get(ask.client_event_id)).toEqual({ open: false, answer: 'use the second' })
  })

  it('a question the agent did not wait on stays open, but the composer never answers it', () => {
    const waited = ev('ask', { body: 'blocking?', message_id: 'w1' })
    const q = ev('ask', { body: 'Ship blue?', message_id: 'q1', no_wait: true })
    const both = askStates([waited, q], NONE, false)
    expect(both.open).toBe(waited)
    expect(both.anyOpen).toBe(true)
    const only = askStates([q, ev('user_message', { text: 'also fix the footer', source: 'chat' }), ev('turn_done', { stop_reason: 'end_turn' })], NONE, false)
    expect(only.open).toBeNull()
    expect(only.anyOpen).toBe(true)
    expect(only.byEvent.get(q.client_event_id)).toEqual({ open: true, answer: '' })
    // A bare ask_answer goes to an ask the composer answers, never to the question.
    expect(askStates([q, ev('user_message', { text: 'x', source: 'ask_answer' })], NONE, false).byEvent.get(q.client_event_id)?.open).toBe(true)
    // The answer Blerg sends on to the agent closes nothing; the answer naming it does.
    const old = ev('ask', { body: 'no id' })
    expect(askStates([old, ev('user_message', { text: 'Answer to your question q1', source: 'question_answer' })], NONE, false).open).toBe(old)
    const done = askStates([q, ev('user_message', { text: 'yes', source: 'ask_answer', message_id: 'q1' })], NONE, false)
    expect(done.anyOpen).toBe(false)
    expect(done.byEvent.get(q.client_event_id)).toMatchObject({ open: false, answer: 'yes', by: 'you' })
  })

  it('the newest open ask is the one the composer answers', () => {
    const a = ev('ask', { body: 'first', message_id: 'a' })
    const b = ev('ask', { body: 'second', message_id: 'b' })
    expect(askStates([a, b], NONE, false).open).toBe(b)
    expect(askStates([a, b, ev('user_message', { text: 'x', source: 'ask_answer', message_id: 'b' })], NONE, false).open).toBe(a)
  })
})

describe('askStates and the call that asked', () => {
  it('a blerg-runner ask the server holds stays open when its command returns (cut off, or backgrounded)', () => {
    const call = ev('tool_call', { tool: 'Bash', call_id: 'b1', input: { command: 'blerg-runner ask "Ship it?"' } })
    const ask = ev('ask', { body: 'Ship it?', message_id: 'm1' })
    const back = ev('tool_result', { call_id: 'b1', output: 'Command timed out', is_error: true, duration_ms: 1 })
    expect(askStates([call, ask, back], NONE, false).open).toBe(ask)
    expect(askStates([call, ask, back], new Map([['m1', '']]), false).open).toBeNull()
  })

  it('an ask with no id closes when its command returns', () => {
    const call = ev('tool_call', { tool: 'Bash', call_id: 'b9', input: { command: 'blerg-runner ask "Old?"' } })
    const ask = ev('ask', { body: 'Old?' })
    expect(askStates([call, ask, ev('tool_result', { call_id: 'b9', output: '', is_error: false, duration_ms: 1 })], NONE, false).open).toBeNull()
  })

  it("the native loop's ask closes when its tool returns, id or not", () => {
    const call = ev('tool_call', { tool: 'ask', call_id: 'a9', input: { question: 'Go?' } })
    const ask = ev('ask', { body: 'Go?', message_id: 'm9' })
    expect(askStates([call, ask, ev('tool_result', { call_id: 'a9', output: '[ask closed without an answer]', is_error: false, duration_ms: 1 })], NONE, false).open).toBeNull()
  })

  it("closes when the native loop's ask tool returns, and keeps the answer recorded before it", () => {
    const call = ev('tool_call', { tool: 'ask', call_id: 'a1', input: { question: 'Which?' } })
    const ask = ev('ask', { body: 'Which?', message_id: 'm2' })
    const r = askStates([call, ask, ev('user_message', { text: 'the first', source: 'ask_answer', message_id: 'm2' }), ev('tool_result', { call_id: 'a1', output: 'the first', is_error: false, duration_ms: 1 })], NONE, false)
    expect(r.byEvent.get(ask.client_event_id)).toMatchObject({ open: false, answer: 'the first' })
  })

  it('another call returning leaves it open', () => {
    const call = ev('tool_call', { tool: 'Bash', call_id: 'b2', input: { command: 'blerg-runner ask "Now?"' } })
    const other = ev('tool_call', { tool: 'Bash', call_id: 'b3', input: { command: 'ls' } })
    const ask = ev('ask', { body: 'Now?', message_id: 'm3' })
    expect(askStates([call, other, ask, ev('tool_result', { call_id: 'b3', output: '', is_error: false, duration_ms: 1 })], NONE, false).open).toBe(ask)
  })
})
