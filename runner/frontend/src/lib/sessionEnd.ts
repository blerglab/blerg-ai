// sessionEnd.ts — the one line a finished session's page says about why it
// ended (server: migration 018, db/session_end.go).
//
// The server records a reason from a fixed vocabulary plus, when an actor
// ended the session, that actor's kind and whether it is (or acts for) the
// viewer — the server works that out and never sends the account. This module
// turns that into text. Adding a reason is one entry in END_REASON; nothing
// else branches on the codes.

import type { SessionInfo, EndedBy } from '../types'

// A session in one of these statuses is over. The server also writes
// "ended" (a runner-contract stop, auto-stop, a finished Job), which the
// SessionStatus union predates.
const TERMINAL = new Set(['stopped', 'ended', 'error'])

export function isTerminalStatus(status: string): boolean {
  return TERMINAL.has(status)
}

interface EndReasonEntry {
  // The line, given who ended it (null when no actor did) and whether that
  // actor is — or acts for — the person looking at the page.
  text: (by: EndedBy | null, isViewer: boolean) => string
  // A failure the error banner already explains in its own words
  // (error_reason): the page shows that instead of repeating it.
  failure?: boolean
}

function actorText(by: EndedBy | null, isViewer: boolean): string {
  switch (by?.kind) {
    case 'human':
      return isViewer ? 'Ended by you' : 'Ended by another signed-in user'
    case 'agent':
      return isViewer ? 'Ended by one of your agent tokens' : 'Ended by an agent token'
    case 'service':
      return 'Ended by a service'
    case 'runner_key':
      return 'Ended by an automated caller (runner key)'
    default:
      return 'Stopped on request'
  }
}

export const END_REASON: Record<string, EndReasonEntry> = {
  stopped_by_user: { text: actorText },
  stopped_by_agent: { text: actorText },
  auto_stopped: { text: () => 'Stopped automatically when its task finished' },
  process_exited: { text: () => 'The process exited on its own' },
  process_failed: { text: () => 'The process exited with an error', failure: true },
  start_failed: { text: () => 'The session failed to start', failure: true },
  job_finished: { text: () => 'The cluster job finished' },
  job_failed: { text: () => 'The cluster job failed', failure: true },
  job_disappeared: { text: () => 'The cluster job was removed outside Blerg', failure: true },
  not_resumed: { text: () => 'Expired: nobody resumed it within a day', failure: true },
  daemon_lost: { text: () => 'Its daemon disconnected and never came back', failure: true },
  daemon_unreported: { text: () => 'Its daemon no longer had this session' },
}

export interface SessionEndLine {
  text: string
  // True when the error banner (error_reason) already says this, so the
  // page should show the banner alone.
  coveredByError: boolean
}

// describeSessionEnd returns the line for a finished session, or null when
// there is nothing to say: the session is still going, or it ended before the
// runner recorded reasons (the page then shows exactly what it always did).
export function describeSessionEnd(
  session: Pick<SessionInfo, 'status' | 'end_reason' | 'ended_by' | 'error_reason'>,
): SessionEndLine | null {
  if (!isTerminalStatus(session.status) || !session.end_reason) return null
  const by = session.ended_by ?? null
  const isViewer = by?.self === true
  // Own keys only: a code like "constructor" must not find Object's.
  if (!Object.hasOwn(END_REASON, session.end_reason)) return { text: 'Ended', coveredByError: false }
  const entry = END_REASON[session.end_reason]
  const coveredByError = !!entry.failure && session.status === 'error' && !!session.error_reason
  return { text: entry.text(by, isViewer), coveredByError }
}
