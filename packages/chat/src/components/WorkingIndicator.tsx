// WorkingIndicator: "the agent is on it" — bouncing dots plus the time the
// current turn has been running. Static dots under prefers-reduced-motion.
import { useState } from 'react'
import { usePrefersReducedMotion } from '../hooks/useTicker'
import { formatElapsed } from '../model/timeLabel'
import { WAITING_ON_INITIAL, nextWaitingOn, runningLabel, type WaitingOn } from '../model/liveCalls'
import { backgroundLabel } from '../model/backgroundTasks'

interface Props {
  since: number
  now: number
  /** compact: one line for the toolbar while text is streaming. */
  compact?: boolean
  /** The oldest tool call in flight (server-time start), if any. It is named only once it has run
   *  2 s, and the suffix changes at most every 3 s (model/liveCalls nextWaitingOn); what it does
   *  (its detail: a Bash call's description) follows its name. */
  waitingOn?: WaitingOn | null
  /** What the agent is on when that is known ("Thinking…", "Retrying the model (attempt 2)…";
   *  model/workingActivity): said instead of "Working…" and of the background words. */
  activity?: string | null
  /** Background tasks running: unless the line names a call, it names them instead of a bare
   *  "Working…". */
  background?: { count: number; description: string } | null
}

export default function WorkingIndicator({ since, now, compact, waitingOn, background, activity }: Props) {
  const reduced = usePrefersReducedMotion()
  const elapsed = formatElapsed(now - since)
  // The suffix as shown: held steady, so a run of short calls does not make it flash in and out.
  const [held, setHeld] = useState(WAITING_ON_INITIAL)
  const next = nextWaitingOn(held, waitingOn ?? null, now)
  if (next !== held) setHeld(next)
  const shownOn = next.shown
  return (
    <div
      className={`agent-working${compact ? ' compact' : ''}${reduced ? ' static' : ''}`}
      role="status"
      aria-live="polite"
      data-testid={compact ? 'working-compact' : 'working'}
      data-reduced-motion={reduced ? 'true' : 'false'}
      data-background={background && !shownOn && !activity ? 'true' : undefined}
      data-activity={activity ? 'true' : undefined}
    >
      <span className="agent-dots" aria-hidden="true"><i /><i /><i /></span>
      <span className="agent-working-text">
        <span data-testid="working-words">
          {activity || (background && !shownOn ? backgroundLabel(background.count, background.description) : 'Working…')}
        </span>{' '}
        {/* The clock is visible but not re-announced every second. */}
        <span className="agent-working-time" aria-hidden="true">{elapsed}</span>
        {shownOn && (
          <span
            key={`${shownOn.tool}@${shownOn.startedAt}`}
            className="agent-working-time agent-waiting-on"
            aria-hidden="true"
            data-testid="waiting-on"
          >
            {' · '}waiting on {shownOn.tool}{shownOn.detail ? ` · ${shownOn.detail}` : ''} {runningLabel(now - shownOn.startedAt)}
          </span>
        )}
      </span>
    </div>
  )
}
