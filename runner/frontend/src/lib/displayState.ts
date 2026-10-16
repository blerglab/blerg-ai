// What a session's state reads as on screen. The engine's status (running, idle, waiting, ...) is
// about the main turn: a session whose turn is over but whose agent still has background work
// running (a background shell, a monitor, a background subagent) is "idle" to the engine, and would
// read as resting when it is not. On screen it is "background": not idle, and not waiting for anyone.
// The status itself is untouched (events, notifications and the server keep using it). Pure.
import type { SessionInfo, SessionStatus } from '../types'

export type DisplayState = SessionStatus | 'background'

/** displayState: 'background' for an idle session that has background work running; otherwise the
 *  session's status. On the open session's page the live state read from the server's whole log
 *  (`liveRunning`: its running subagents and tasks not done) decides alone when there is one; the
 *  server's per-session count (session.background_tasks) decides when there is none (the sidebar's
 *  cards, a page whose live read has not answered or cannot). */
export function displayState(session: Pick<SessionInfo, 'status' | 'background_tasks'>, liveRunning: number | null = null): DisplayState {
  if (session.status === 'idle' && backgroundCount(session, liveRunning) > 0) return 'background'
  return session.status
}

/** How many background tasks to name for the session: the live state's count when there is one,
 *  else the server's. */
export function backgroundCount(session: Pick<SessionInfo, 'background_tasks'>, liveRunning: number | null = null): number {
  return liveRunning ?? session.background_tasks ?? 0
}

/** The state in words, for a pill, a tooltip or a screen reader. */
export const DISPLAY_WORD: Record<DisplayState, string> = {
  starting: 'starting',
  running: 'running',
  idle: 'idle',
  waiting: 'waiting',
  background: 'working in background',
  stopped: 'stopped',
  error: 'error',
  // a cluster pod died; the session resumes when you send it a message
  disconnected: 'paused',
  ended: 'ended',
}

/** The words with the task count: "working in background, 2 tasks". */
export function displayLabel(state: DisplayState, tasks = 0): string {
  const word = DISPLAY_WORD[state] ?? state
  if (state !== 'background' || tasks <= 0) return word
  return `${word}, ${tasks === 1 ? '1 task' : `${tasks} tasks`}`
}

/** The background state's colour: quieter than running's, livelier than idle's. */
export const BACKGROUND_COLOR = 'color-mix(in srgb, var(--lichen) 55%, var(--fog-dim))'

/** pausedByRestart: a daemon restart cut off what the session was doing and did not continue it by
 *  itself (it was waiting for the person, or had been continued once already): it waits for a
 *  message. Only while it waits — a session that runs again or ended is not paused. */
export function pausedByRestart(session: Pick<SessionInfo, 'status' | 'paused_reason'>): boolean {
  return session.paused_reason === 'restart' && (session.status === 'idle' || session.status === 'waiting')
}

/** What "Resume all" sends a session paused by a restart: the message the daemon sends one it
 *  continues by itself (agentadopt.go, adoptContinuePrompt). The daemon puts the restart note and
 *  the list of what was cut off before it, on the engine's line. */
export const RESUME_PROMPT = 'Continue the task you were working on.'

/** The tooltip and accessible name of a paused session's glyph. */
export const PAUSED_WORDS = 'paused by a restart'

/** The waiting banner's words: "2 sessions waiting for input". */
export function waitingBannerText(waiting: number): string {
  return `${waiting} session${waiting !== 1 ? 's' : ''} waiting for input`
}
