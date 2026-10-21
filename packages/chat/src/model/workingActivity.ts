// What the working line says the agent is on when it names no call: "Thinking…" while the model
// thinks, "Retrying the model (attempt 2)…" while the engine retries a failed API call. Pure.
//
// Thinking is known only live: the daemon sends a transient thinking_started / thinking_done as a
// thinking block opens and closes (no text; the text comes in assistant_thinking). The store keeps
// one flag per session from them (nextThinking). A retry is a persisted notice (an error with code
// api_retry, model/notices), so it is read from the events: the newest one of the running turn with
// nothing from the model after it.
import type { AgentEvent } from '../types'
import { isRetryNotice, isTurnError } from './notices'

function payloadOf(ev: AgentEvent): Record<string, unknown> {
  const p = ev.payload
  return p && typeof p === 'object' ? (p as Record<string, unknown>) : {}
}

function parentOf(ev: AgentEvent): string {
  const v = payloadOf(ev).parent_call_id
  return typeof v === 'string' ? v : ''
}

/**
 * nextThinking: whether the main agent is thinking after ev, given whether it was. A thinking block
 * opens on thinking_started and closes on thinking_done; a dropped done is made good by anything
 * that cannot come while it is open: the block's own assistant_thinking, the model's text or a tool
 * call, a retry notice, the turn's end, a status other than running, or a message from the person.
 * A subagent's events (parent_call_id) change nothing: the line is the main agent's.
 */
export function nextThinking(open: boolean, ev: AgentEvent): boolean {
  if (parentOf(ev) !== '') return open
  switch (ev.kind) {
    case 'thinking_progress': {
      const n = payloadOf(ev).tokens
      return typeof n === 'number' && Number.isSafeInteger(n) && n >= 0 ? true : open
    }
    case 'thinking_started':
      return true
    case 'thinking_done':
    case 'assistant_thinking':
    case 'assistant_text':
    case 'tool_call':
    case 'turn_done':
      return false
    case 'error':
      return isRetryNotice(ev) || isTurnError(ev) ? false : open
    case 'status_changed':
      return payloadOf(ev).status === 'running' ? open : false
    case 'user_message':
      return payloadOf(ev).source === 'system' ? open : false
  }
  return open
}

/**
 * nextAnswering: whether the model has said something live (a typing delta, a thinking block
 * opening) since the newest retry notice, given whether it had. A retry notice resets it; the main
 * agent's next live word sets it, so "Retrying the model…" goes as soon as the answer starts, not
 * only when its whole first block is stored. A subagent's live words change nothing.
 */
export function nextAnswering(answering: boolean, ev: AgentEvent): boolean {
  if (isRetryNotice(ev)) return false
  if (!ev.transient || parentOf(ev) !== '') return answering
  return ev.kind === 'assistant_text' || ev.kind === 'thinking_started' ? true : answering
}

/** The engine is retrying the model's API: which attempt (null when the notice does not say). */
export interface ModelRetry {
  attempt: number | null
}

function attemptOf(ev: AgentEvent): number | null {
  const p = payloadOf(ev)
  if (typeof p.attempt === 'number' && Number.isFinite(p.attempt) && p.attempt > 0) return p.attempt
  // A daemon older than the attempt field says it in the message: "retrying the API (attempt 2 of 10)…".
  const m = typeof p.message === 'string' ? /\battempt (\d+)/.exec(p.message) : null
  return m ? Number(m[1]) : null
}

/**
 * modelRetry: the retry under way in the running turn: its newest retry notice, when nothing from
 * the model (text, thinking, a tool call or its result) has come since. Null when the session is
 * not running, or the turn ended or a new one began after the notice.
 */
export function modelRetry(events: AgentEvent[], running: boolean): ModelRetry | null {
  if (!running) return null
  for (let i = events.length - 1; i >= 0; i--) {
    const ev = events[i]
    if (isRetryNotice(ev)) return { attempt: attemptOf(ev) }
    switch (ev.kind) {
      case 'assistant_text':
      case 'assistant_thinking':
      case 'tool_call':
      case 'tool_result':
      case 'turn_done':
        return null
      case 'error':
        if (isTurnError(ev)) return null
        break
      case 'status_changed':
        if (payloadOf(ev).status !== 'running') return null
        break
      case 'user_message':
        if (payloadOf(ev).source !== 'system') return null
        break
    }
  }
  return null
}

/** activityLabel: the working line's words for what the agent is on; null: the plain "Working…".
 *  Thinking wins: a think that opened after a retry notice means the retry got through. */
export function activityLabel(thinking: boolean, retry: ModelRetry | null): string | null {
  if (thinking) return 'Thinking…'
  if (retry) return retry.attempt ? `Retrying the model (attempt ${retry.attempt})…` : 'Retrying the model…'
  return null
}
