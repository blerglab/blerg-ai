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
  | { as: 'notice'; text: string; label?: string }
  | { as: 'output'; text: string }

const NOTE_PREFIX = '[system] '
// The restart note an older daemon queued as the person's message.
const RESTART_NOTE = '[system] Session resumed after a restart'
// What the daemon sends an agent session it recovered (daemon/claudeadopt.go adoptContinuePrompt).
const ADOPT_PROMPT = 'Continue the task you were working on.'
const ADOPT_NOTE = 'Session recovered: the agent was asked to continue the task it was working on.'

/** Sources whose messages Blerg wrote on its own behalf (decisions relayed to the agent, and a
 *  parent's note that its child's turn ended). */
export function isBlergRelaySource(source: unknown): boolean {
  return source === 'review' || source === 'proposal' || source === 'child_turn'
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

export function userMessageShape(payload: unknown): UserMessageShape {
  if (!payload || typeof payload !== 'object') return { as: 'person' }
  const p = payload as Partial<UserMessagePayload>
  const text = typeof p.text === 'string' ? p.text : ''
  if (isHarnessText(text)) return { as: 'hidden' }
  // The answer to a question the agent did not wait on, as Blerg sent it on to the agent: the
  // question's card already shows the person's answer.
  if (p.source === 'question_answer') return { as: 'hidden' }
  const trimmed = text.trim()
  // Blerg itself telling the agent how something it asked for was decided (a review request, a
  // proposal): never the person's words, so never drawn as theirs.
  if (isBlergRelaySource(p.source)) return { as: 'notice', text: trimmed, label: 'Blerg' }
  if (p.source !== 'system') {
    return trimmed.startsWith(RESTART_NOTE) ? { as: 'notice', text: trimmed.slice(NOTE_PREFIX.length) } : { as: 'person' }
  }
  if (trimmed === '' || trimmed.startsWith('[system reminder]')) return { as: 'hidden' }
  if (trimmed.startsWith(NOTE_PREFIX)) return { as: 'notice', text: trimmed.slice(NOTE_PREFIX.length) }
  if (trimmed === ADOPT_PROMPT) return { as: 'notice', text: ADOPT_NOTE }
  return { as: 'output', text }
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
