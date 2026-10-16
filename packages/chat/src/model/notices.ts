// Error events that end nothing. An `error` event is a turn's outcome as a rule (the working line
// stops, the typing text is cleared, the calls in flight are no longer live), but two kinds are
// only notices: the engine retrying the model API (code "api_retry"; the turn goes on), and a
// message the session dropped because its queue was full (it carries that message's client_id;
// the turn that was running goes on).
import type { AgentEvent } from '../types'

function field(ev: AgentEvent, name: string): string {
  const p = ev.payload
  if (!p || typeof p !== 'object') return ''
  const v = (p as Record<string, unknown>)[name]
  return typeof v === 'string' ? v : ''
}

/** An `error` event that reports the engine retrying the model API. */
export function isRetryNotice(ev: AgentEvent): boolean {
  return ev.kind === 'error' && field(ev, 'code') === 'api_retry'
}

/** An `error` event that reports a message the session dropped (its client id). */
export function droppedMessageId(ev: AgentEvent): string {
  return ev.kind === 'error' ? field(ev, 'client_id') : ''
}

/** An `error` event that is a turn's outcome: neither a retry notice nor a dropped message. */
export function isTurnError(ev: AgentEvent): boolean {
  return ev.kind === 'error' && !isRetryNotice(ev) && droppedMessageId(ev) === ''
}
