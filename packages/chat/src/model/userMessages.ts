// How a user_message is drawn. Most are the person's (or a host's automation's, which the host
// says through describeUserMessage), but the user role also carries text nobody typed:
//   - harness text Claude Code writes itself (a task notification, a system reminder): hidden;
//   - the native loop's "[system reminder] …" nudges to the model: hidden;
//   - the daemon's own notes, "[system] Session resumed after a restart…": a system note (an
//     older daemon recorded the restart note as the person's, so it is recognised by its text);
//   - the daemon's "Continue the task you were working on." after it recovered a session: a system
//     note saying so;
//   - anything else recorded as source "system" — a slash command's output (/cost, /context): a
//     system card, kept as written.
import type { AgentEvent, UserMessagePayload } from '../types'
import { isHarnessText } from './harnessText'

export type UserMessageShape =
  | { as: 'person' }
  | { as: 'hidden' }
  | { as: 'notice'; text: string; label?: string; detail?: string; restart?: 'paused' | 'continued' }
  | { as: 'output'; text: string }

const NOTE_PREFIX = '[system] '
// The restart note an older daemon queued as the person's message.
const RESTART_NOTE = '[system] Session resumed after a restart'
// What the daemon sends an agent session it recovered (daemon/claudeadopt.go adoptContinuePrompt),
// and what a paused restart row's Resume button sends for the person.
const ADOPT_PROMPT = 'Continue the task you were working on.'
export const RESUME_PROMPT = ADOPT_PROMPT
/** A paused restart row's words for the person, and the one-line reason its details open with. */
export const PAUSED_ROW_TEXT = 'Paused after a restart'
export const PAUSED_ROW_REASON = 'A command was cut off: it may not have finished.'
const ADOPT_NOTE = 'Session recovered: the agent was asked to continue the task it was working on.'

/** Sources whose messages Blerg wrote on its own behalf (decisions relayed to the agent, a parent's
 *  note that its child's turn ended, and a nudge that the session's plan drifted). */
export function isBlergRelaySource(source: unknown): boolean {
  return source === 'review' || source === 'proposal' || source === 'child_turn' || source === 'plan_drift' || source === 'usage_limit'
}

/** Who wrote a message that some automation, not the person, sent: a board's (a card's task, a
 *  board session's brief), a parent session's for its child, a cron's prompt. Drawn like the
 *  person's card under this author instead of "You" (a host may name it better through
 *  describeUserMessage, e.g. the parent's title); undefined for the person's own sources. */
export function automationAuthor(source: unknown): string | undefined {
  switch (source) {
    case 'blerg-board': return 'Board'
    case 'session': return '↑ Parent'
    case 'cron': return 'Cron'
  }
  return undefined
}

/** Whether a user message is the parent session's: its message to this child (source "session"),
 *  or its answer to this child's question (an ask_answer the runner marked answered_by "session").
 *  Both are set by the runner from the parent's own token, never by a client. */
export function isFromParentSession(payload: unknown): boolean {
  if (!payload || typeof payload !== 'object') return false
  const p = payload as Partial<UserMessagePayload>
  return p.source === 'session' || (p.source === 'ask_answer' && p.answered_by === 'session')
}

/** Sources of the messages Blerg sends the agent on the person's behalf, that the chat never draws
 *  as the person's bubble: decisions relayed (review, proposal), a child's turn, a question's answer. */
export function isRelayInputSource(source: unknown): boolean {
  return isBlergRelaySource(source) || source === 'question_answer'
}

const UUID = /\s*\(request [0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\)|\s*[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/gi

function oneLine(text: string): string {
  return text.split(/\s+/u).filter(Boolean).join(' ')
}

/** A relayed decision as one short line: its first line, without request ids. */
export function compactRelay(text: string): string {
  const first = text.split('\n').find(line => line.trim() !== '') ?? ''
  return oneLine(first.replace(UUID, ''))
}

export function userMessageShape(payload: unknown): UserMessageShape {
  if (!payload || typeof payload !== 'object') return { as: 'person' }
  const p = payload as Partial<UserMessagePayload>
  const text = typeof p.text === 'string' ? p.text : ''
  if (p.source === 'system' && text.startsWith('[system] The process hosting this session restarted, and work that was running was cut off.')) return { as: 'hidden' }
  if (isHarnessText(text)) return { as: 'hidden' }
  // The answer to a question the agent did not wait on, as Blerg sent it on to the agent: the
  // question's card already shows the person's answer.
  if (p.source === 'question_answer') return { as: 'hidden' }
  const trimmed = text.trim()
  // The person's answer to one of the agent's asks: the ask's card shows it in full, with who and
  // when; the chat keeps one short row ("You answered: orange"), never a bubble. (A parent
  // session's answer to its child stays the parent's message, with its author.)
  if (p.source === 'ask_answer' && p.answered_by !== 'session') {
    return { as: 'notice', text: `answered: ${oneLine(trimmed)}`, label: 'You' }
  }
  // Blerg itself telling the agent how something it asked for was decided (a review request, a
  // proposal): never the person's words, so never drawn as theirs; one short line (the agent has
  // the whole message), without the request ids.
  if (isBlergRelaySource(p.source)) return { as: 'notice', text: compactRelay(trimmed), label: 'Blerg' }
  if (p.source !== 'system') {
    return trimmed.startsWith(RESTART_NOTE) ? { as: 'notice', text: trimmed.slice(NOTE_PREFIX.length) } : { as: 'person' }
  }
  if (trimmed === '' || trimmed.startsWith('[system reminder]')) return { as: 'hidden' }
  if (trimmed.startsWith('[system] ↻ ')) {
    const [summary, ...detail] = trimmed.slice('[system] ↻ '.length).split('\n')
    // The daemon's paused row ("Host restarted · paused — check the interrupted command") speaks to
    // the agent; the person reads one plain line, the reason and the cut-off list in its details.
    if (/ · paused(?: —|$)/u.test(summary)) {
      return { as: 'notice', text: PAUSED_ROW_TEXT, label: '↻', detail: [PAUSED_ROW_REASON, ...detail].join('\n'), restart: 'paused' }
    }
    return { as: 'notice', text: summary, label: '↻', detail: detail.join('\n') || undefined, restart: / · continued$/u.test(summary) ? 'continued' : undefined }
  }
  if (trimmed.startsWith(NOTE_PREFIX)) return { as: 'notice', text: trimmed.slice(NOTE_PREFIX.length) }
  if (trimmed === ADOPT_PROMPT) return { as: 'notice', text: ADOPT_NOTE }
  return { as: 'output', text }
}

/** Where a paused restart row stands: 'paused' while the session still waits for it, 'resumed' once
 *  someone sent the session a message after it (its Resume button, or a typed message), and
 *  'superseded' when a later restart row took its place before anyone did. */
export type RestartRowState = 'paused' | 'resumed' | 'superseded'

/** The state of every paused restart row in the transcript, by client_event_id. */
export function restartRowStates(events: readonly AgentEvent[]): Map<string, RestartRowState> {
  const states = new Map<string, RestartRowState>()
  let open: string | null = null
  for (const ev of events) {
    if (ev.kind !== 'user_message') continue
    const shape = userMessageShape(ev.payload)
    const p = ev.payload as Partial<UserMessagePayload> | undefined
    const restartRow = shape.as === 'notice' && shape.label === '↻'
    const fromSomeone = shape.as === 'person' || p?.source === 'ask_answer'
    if (open !== null && (restartRow || fromSomeone)) {
      states.set(open, restartRow ? 'superseded' : 'resumed')
      open = null
    }
    if (shape.as === 'notice' && shape.restart === 'paused') {
      states.set(ev.client_event_id, 'paused')
      open = ev.client_event_id
    }
  }
  return states
}

/** Whether the transcript draws anything for a user_message. */
export function isShownUserMessage(payload: unknown): boolean {
  return userMessageShape(payload).as !== 'hidden'
}

/** The client_event_id of the first user message drawn as the person's (or a host's), or null
 *  when there is none among these events. */
export function firstPersonMessage(events: AgentEvent[]): string | null {
  for (const ev of events) {
    if (ev.kind === 'user_message' && userMessageShape(ev.payload).as === 'person') return ev.client_event_id
  }
  return null
}
