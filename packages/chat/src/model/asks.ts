// Which of the agent's questions (kind "ask") are still open. An ask is answered by the
// user_message that answers it (source "ask_answer", with the ask's message_id when the daemon
// records one). Otherwise:
//   - an ask with a message_id is open for as long as the server holds it open: it closes only
//     when the server says it was answered or closed (message_answered, initial_state: an answer
//     from the Messages page, an expiry) or the session is over. Never when the `blerg-runner ask`
//     command returns or the turn ends: Claude Code cuts a foreground command off after two
//     minutes, and a background one returns at once, while the ask stays open on the server and
//     takes the next chat message as its answer. The one exception is the native loop's ask tool,
//     which holds its ask for exactly as long as the call runs;
//   - an ask recorded before asks carried a message_id closes when the call that asked it
//     returns, when its turn ends, or with the person's next message.
// Pure.
import type { AgentEvent, MessagingPayload, ToolCallPayload, ToolResultPayload, UserMessagePayload } from '../types'
import { automationAuthor, isBlergRelaySource } from './userMessages'

export interface AskState {
  open: boolean
  /** The answer, when it is known (empty: closed without one). */
  answer: string
}

export interface Asks {
  /** By the ask event's client_event_id. */
  byEvent: Map<string, AskState>
  /** The newest ask still open that the composer's next message answers, or null. A question the
   *  agent does not wait on (no_wait) is never it: only an answer addressed to it answers it. */
  open: AgentEvent | null
  /** Whether any ask is open, a no_wait question included. */
  anyOpen: boolean
}

/** A board's or a cron's message is not the person answering (the server never takes one as an
 *  answer either: answersAsk). A parent session's message is: it answers its child's ask. */
function closesNoAsk(source: unknown): boolean {
  return source !== 'session' && automationAuthor(source) !== undefined
}

export const NO_ASKS: Asks = { byEvent: new Map(), open: null, anyOpen: false }

function str(v: unknown): string {
  return typeof v === 'string' ? v : ''
}

// Whether a tool call is one that asks the person: the native loop's ask tool, or a shell command
// running `blerg-runner ask`.
function asksPerson(p: Partial<ToolCallPayload>): boolean {
  if (p.tool === 'ask') return true
  const input = p.input && typeof p.input === 'object' ? (p.input as Record<string, unknown>) : null
  return typeof input?.command === 'string' && /\bblerg-runner\s+ask\b/.test(input.command)
}

// The last states read from each events array, with what they were read under: the chat and the
// side panel read the same events with the same closed asks.
const read = new WeakMap<AgentEvent[], { closed: ReadonlyMap<string, string>; ended: boolean; asks: Asks }>()

/** closed: asks the server reported answered or closed, by message id, with their answer. */
export function askStates(events: AgentEvent[], closed: ReadonlyMap<string, string>, ended: boolean): Asks {
  const last = read.get(events)
  if (last && last.closed === closed && last.ended === ended) return last.asks
  const asks = readAsks(events, closed, ended)
  read.set(events, { closed, ended, asks })
  return asks
}

function readAsks(events: AgentEvent[], closed: ReadonlyMap<string, string>, ended: boolean): Asks {
  if (!events.some(ev => ev.kind === 'ask')) return NO_ASKS
  const asks: Array<{ ev: AgentEvent; id: string; call: string; native: boolean; noWait: boolean; state: AskState }> = []
  // The main agent's calls that ask the person and have not returned yet, oldest first, and
  // which of them are the native loop's ask tool.
  const asking: string[] = []
  const nativeCalls = new Set<string>()
  // Whether a call returning, or the turn ending, closes the ask: never for one the server holds.
  const endsWithCall = (a: { id: string; native: boolean }) => a.id === '' || a.native
  const close = (a: { state: AskState }, answer: string) => {
    a.state = { open: false, answer }
  }
  const newestOpen = (pred: (a: { id: string; noWait: boolean }) => boolean) => {
    for (let i = asks.length - 1; i >= 0; i--) if (asks[i].state.open && pred(asks[i])) return asks[i]
    return null
  }
  for (const ev of events) {
    if (ev.kind === 'ask') {
      const call = asking[asking.length - 1] ?? ''
      const p = (ev.payload ?? {}) as Partial<MessagingPayload>
      const id = str(p.message_id)
      asks.push({
        ev,
        id,
        call,
        native: call !== '' && nativeCalls.has(call),
        // Only a question the server holds can be one the agent does not wait on.
        noWait: p.no_wait === true && id !== '',
        state: { open: true, answer: '' },
      })
    } else if (ev.kind === 'tool_call') {
      const p = (ev.payload ?? {}) as Partial<ToolCallPayload>
      if (!p.parent_call_id && p.call_id && asksPerson(p)) {
        asking.push(p.call_id)
        if (p.tool === 'ask') nativeCalls.add(p.call_id)
      }
    } else if (ev.kind === 'tool_result') {
      const id = str((ev.payload as Partial<ToolResultPayload> | null)?.call_id)
      const i = id ? asking.indexOf(id) : -1
      if (i >= 0) {
        asking.splice(i, 1)
        for (const a of asks) if (a.state.open && a.call === id && endsWithCall(a)) close(a, '')
      }
    } else if (ev.kind === 'user_message') {
      const p = (ev.payload ?? {}) as Partial<UserMessagePayload>
      if (p.source === 'ask_answer') {
        const id = str(p.message_id)
        const a = id ? asks.find(x => x.id === id) : newestOpen(x => !x.noWait)
        if (a) close(a, str(p.text))
      } else if (p.source !== 'system' && p.source !== 'question_answer' && !isBlergRelaySource(p.source) && !closesNoAsk(p.source)) {
        const a = newestOpen(x => x.id === '')
        if (a) close(a, str(p.text))
      }
    } else if (ev.kind === 'turn_done') {
      for (const a of asks) if (a.state.open && endsWithCall(a)) close(a, '')
    }
  }
  const byEvent = new Map<string, AskState>()
  let open: AgentEvent | null = null
  let anyOpen = false
  for (const a of asks) {
    if (a.state.open && (ended || (a.id && closed.has(a.id)))) close(a, a.id ? closed.get(a.id) ?? '' : '')
    byEvent.set(a.ev.client_event_id, a.state)
    if (a.state.open) {
      anyOpen = true
      if (!a.noWait) open = a.ev
    }
  }
  return { byEvent, open, anyOpen }
}
