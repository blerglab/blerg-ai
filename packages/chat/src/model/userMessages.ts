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
  | { as: 'worker-notice'; workerId: string; word: string }
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
const ADOPT_NOTE = 'Workspace recovered: the agent was asked to continue the task it was working on.'

/** Sources whose messages Blerg wrote on its own behalf (decisions relayed to the agent, a parent's
 *  note that its child's turn ended, and a nudge that the session's plan drifted). */
export function isBlergRelaySource(source: unknown): boolean {
  return source === 'review' || source === 'proposal' || source === 'child_turn' || source === 'plan_drift' || source === 'usage_limit' || source === 'process_limit' || source === 'blackboard_setup'
}

/** Who wrote a message that some automation, not the person, sent: a board's (a card's task, a
 *  board session's brief), a parent session's for its child, a cron's prompt. Drawn like the
 *  person's card under this author instead of "You" (a host may name it better through
 *  describeUserMessage, e.g. the parent's title); undefined for the person's own sources. */
export function automationAuthor(source: unknown): string | undefined {
  switch (source) {
    case 'page_result': return 'Page'
    case 'blerg-board': return 'Board'
    case 'session': return '↑ Main'
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
  return isBlergRelaySource(source) || (source === 'question_answer' || source === 'question_thread')
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

// A child_turn notice's fixed, server-built shape (runner worker_notices.go workerNotice):
// "Worker event: worker=<uuid> kind=<kind> id=<uuid>. ...". Matched before the generic UUID
// stripping above runs on it, so the worker and kind survive into a real row instead of a blank
// "worker= kind= id=" line (BLG-476).
const WORKER_NOTICE_RE = /^Worker event: worker=([0-9a-f-]{36}) kind=(\S+) id=[0-9a-f-]{36}\b/i

// The plain word a worker notice's row says for each server kind. Unlisted/unrecognized kinds
// (an older or newer daemon) fall back to "updated".
const WORKER_NOTICE_WORDS: Readonly<Record<string, string>> = {
  finished: 'finished',
  asked: 'asked something',
  approval: 'asked for approval',
  failed: 'failed',
  session_failed: 'failed',
  restart_paused: 'paused after a restart',
  discard_unanswered: 'ended with an unanswered question',
  question_person: 'asked the person',
  question_resolved: 'got its answer',
  channel_closed: 'closed its channel',
}

/** Parses a child_turn notice's fixed text into the worker's id and a plain-word kind; null for
 *  anything that is not that exact shape (the "unavailable" fallback line, an older event's text).
 *  Exported so a host can resolve the worker's title and a link from the same id this module
 *  draws the row from, rather than re-deriving it from the raw text itself. */
export function parseWorkerNotice(text: string): { workerId: string; word: string } | null {
  const m = WORKER_NOTICE_RE.exec(text)
  if (!m) return null
  return { workerId: m[1], word: WORKER_NOTICE_WORDS[m[2]] ?? 'updated' }
}

export function userMessageShape(payload: unknown): UserMessageShape {
  if (!payload || typeof payload !== 'object') return { as: 'person' }
  const p = payload as Partial<UserMessagePayload>
  const text = typeof p.text === 'string' ? p.text : ''
  if (p.source === 'system' && text.startsWith('[system] The process hosting this session restarted, and work that was running was cut off.')) return { as: 'hidden' }
  if (isHarnessText(text)) return { as: 'hidden' }
  // The answer to a question the agent did not wait on, as Blerg sent it on to the agent: the
  // question's card already shows the person's answer.
  if (p.source === 'question_answer' || p.source === 'question_thread') return { as: 'hidden' }
  const trimmed = text.trim()
  // The person's answer to one of the agent's asks: the ask's card shows it in full, with who and
  // when; the chat keeps one short row ("You answered: orange"), never a bubble. (A parent
  // session's answer to its child stays the parent's message, with its author.)
  if (p.source === 'ask_answer' && p.answered_by !== 'session') {
    return { as: 'notice', text: `answered: ${oneLine(trimmed)}`, label: 'You' }
  }
  // A worker's turn ending, asking something, or failing: named by the worker (the host resolves
  // its title and a link from the id), in plain words, never the raw worker=/kind=/id= text the
  // model reads (BLG-476). A text that is not that exact shape (an older event, the "unavailable"
  // fallback line) falls through to the generic Blerg notice below.
  if (p.source === 'child_turn') {
    const parsed = parseWorkerNotice(trimmed)
    if (parsed) return { as: 'worker-notice', ...parsed }
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
