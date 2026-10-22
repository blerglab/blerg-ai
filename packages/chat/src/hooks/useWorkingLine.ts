// useWorkingLine: what the working line under the chat says about the main agent (that it works,
// since when, the call it waits on, what it is on), shared with views elsewhere on the page — the
// host's side panel shows it as the "Main" row of Running now. ChatView reports the line's inputs;
// a view reads them through mainStep, which holds the call it names exactly as WorkingIndicator
// does (nextWaitingOn), so the two say the same thing.
import { useState } from 'react'
import { create } from 'zustand'
import { WAITING_ON_INITIAL, nextWaitingOn, type WaitingOn } from '../model/liveCalls'

/** The working line's inputs for one session, as the ChatView showing it last reported them. */
export interface WorkingLineInputs {
  /** Server time the turn began; null: unknown (the line shows 0 s). */
  since: number | null
  /** The oldest call in flight (or the subagent waited on), before the line's hold. */
  waitingOn: WaitingOn | null
  /** What the agent is on when known ("Thinking…", "Retrying the model…"). */
  activity: string | null
}

interface WorkingLineState {
  /** Per session: its inputs while the agent works; null (or absent) while it does not. */
  lines: Record<string, WorkingLineInputs | null>
  setLine: (sessionId: string, line: WorkingLineInputs | null) => void
}

function same(a: WorkingLineInputs | null | undefined, b: WorkingLineInputs | null): boolean {
  if (!a || !b) return (a ?? null) === b
  const wa = a.waitingOn
  const wb = b.waitingOn
  return a.since === b.since && a.activity === b.activity &&
    (wa === wb || (!!wa && !!wb && wa.tool === wb.tool && wa.startedAt === wb.startedAt && (wa.detail ?? '') === (wb.detail ?? '')))
}

/** Where ChatView reports its working line, so a view elsewhere on the page agrees with it. One
 *  reporter per session is assumed (the runner mounts one ChatView per session): with two, the last
 *  to report wins and either one going clears the line. */
export const useWorkingLineStore = create<WorkingLineState>(set => ({
  lines: {},
  setLine: (sessionId, line) => set(s => {
    if (same(s.lines[sessionId], line)) return s
    const lines = { ...s.lines }
    if (line) lines[sessionId] = line
    else delete lines[sessionId]
    return { lines }
  }),
}))

/** The main agent's step as a compact row shows it. */
export interface MainStep {
  /** Server time the turn began (the line's clock); null: unknown. */
  since: number | null
  /** "Bash: Fast-forward main…", "Thinking…", or "Working…". */
  text: string
  /** The tool the step names, when it names a call (or a subagent). */
  tool?: string
  /** Server time the named call started: the step's own clock. */
  stepSince?: number
}

/** mainStep: the row's words for what the working line says (the call it holds, else what the
 *  agent is on, else "Working…"). */
export function mainStep(line: WorkingLineInputs, shown: WaitingOn | null): MainStep {
  if (shown) return { since: line.since, text: shown.detail ? `${shown.tool}: ${shown.detail}` : shown.tool, tool: shown.tool, stepSince: shown.startedAt }
  return { since: line.since, text: line.activity || 'Working…' }
}

/** The main agent's current step at server time `now`, or null while it does not work (or no
 *  ChatView reports the session). Holds the named call as the working line does. */
export function useMainStep(sessionId: string, now: number): MainStep | null {
  const line = useWorkingLineStore(s => s.lines[sessionId] ?? null)
  const [held, setHeld] = useState(WAITING_ON_INITIAL)
  const next = nextWaitingOn(held, line ? line.waitingOn : null, now)
  if (next !== held) setHeld(next)
  return line ? mainStep(line, next.shown) : null
}
