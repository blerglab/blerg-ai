// WorkingIndicator: "the agent is on it" — bouncing dots plus the time the
// current turn has been running. Static dots under prefers-reduced-motion.
import { usePrefersReducedMotion } from '../hooks/useTicker'
import { formatElapsed } from '../model/timeLabel'
import { runningLabel } from '../model/liveCalls'

interface Props {
  since: number
  now: number
  /** compact: one line for the toolbar while text is streaming. */
  compact?: boolean
  /** The oldest tool call in flight (server-time start), if any. */
  waitingOn?: { tool: string; startedAt: number } | null
}

export default function WorkingIndicator({ since, now, compact, waitingOn }: Props) {
  const reduced = usePrefersReducedMotion()
  const elapsed = formatElapsed(now - since)
  return (
    <div
      className={`agent-working${compact ? ' compact' : ''}${reduced ? ' static' : ''}`}
      role="status"
      aria-live="polite"
      data-testid={compact ? 'working-compact' : 'working'}
      data-reduced-motion={reduced ? 'true' : 'false'}
    >
      <span className="agent-dots" aria-hidden="true"><i /><i /><i /></span>
      <span className="agent-working-text">
        Working… {/* The clock is visible but not re-announced every second. */}
        <span className="agent-working-time" aria-hidden="true">{elapsed}</span>
        {waitingOn && (
          <span className="agent-working-time" aria-hidden="true" data-testid="waiting-on">
            {' · '}waiting on {waitingOn.tool} {runningLabel(now - waitingOn.startedAt)}
          </span>
        )}
      </span>
    </div>
  )
}
