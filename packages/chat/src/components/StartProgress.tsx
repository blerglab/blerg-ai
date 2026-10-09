// StartProgress: what a session is doing between "Start" and ready. Renders
// whatever stages the server and runner pod reported (model/startStages) — the
// stage list is data, so a runtime with other steps needs no change here.
import { useEffect, useRef, useState } from 'react'
import { useTicker } from '../hooks/useTicker'
import { formatElapsed } from '../model/timeLabel'
import { STILL_STARTING_MS, type AttemptStage, type StartAttempt } from '../model/startStages'

function StageIcon({ state }: { state: AttemptStage['state'] }) {
  switch (state) {
    case 'done':
      return <span className="sp-icon sp-done" aria-hidden="true">✓</span>
    case 'failed':
      return <span className="sp-icon sp-failed" aria-hidden="true">✕</span>
    case 'warning':
      return <span className="sp-icon sp-warning" aria-hidden="true">!</span>
    case 'active':
      return <span className="sp-icon sp-spinner" aria-hidden="true" />
    default:
      return <span className="sp-icon sp-pending" aria-hidden="true" />
  }
}

const STATE_WORDS: Record<AttemptStage['state'], string> = {
  done: 'done', failed: 'failed', active: 'in progress', pending: 'waiting', warning: 'done with a warning',
}

interface Props {
  attempt: StartAttempt
  /** The session's id and status; absent while there is no session row yet. */
  session?: { id: string; status: string }
  /** Ends the session (the transport's stop). Absent: no Stop button. */
  onStop?: () => Promise<void>
  /** client clock − server clock (ms): stage times are server time. */
  clockOffset?: number
  /** The session already has a conversation and is being brought back (a new pod for a
   *  session whose pod went away): worded as a resume, not a first start. */
  resuming?: boolean
}

const TERMINAL = new Set(['error', 'stopped'])

export default function StartProgress({ attempt, session, onStop, clockOffset = 0, resuming = false }: Props) {
  const [showDetails, setShowDetails] = useState(false)
  const [stopArmed, setStopArmed] = useState(false)
  const [stopError, setStopError] = useState<string | null>(null)
  const armTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  // A failed stage is only the end when the session itself has ended: k8s
  // keeps retrying an unschedulable pod or a failing image pull, and either
  // can still come good.
  const gaveUp = attempt.failed && !!session && TERMINAL.has(session.status)
  const retrying = attempt.failed && !gaveUp
  const settled = gaveUp || attempt.ready
  const now = useTicker(!settled) - clockOffset

  useEffect(() => () => { if (armTimer.current) clearTimeout(armTimer.current) }, [])

  const endedAt = settled
    ? Math.max(attempt.startedAt, ...attempt.stages.map(s => s.endedAt ?? 0))
    : now
  const elapsed = endedAt - attempt.startedAt
  const slow = !settled && elapsed >= STILL_STARTING_MS
  const current = attempt.stages.find(s => s.state === 'failed') ?? attempt.stages.find(s => s.state === 'active')
  const failedStage = attempt.stages.find(s => s.state === 'failed')
  const live = !session || ['starting', 'disconnected'].includes(session.status)

  async function stop() {
    if (!session || !onStop) return
    if (!stopArmed) {
      setStopArmed(true)
      armTimer.current = setTimeout(() => setStopArmed(false), 3000)
      return
    }
    if (armTimer.current) clearTimeout(armTimer.current)
    setStopArmed(false)
    try {
      await onStop()
      setStopError(null)
    } catch (e) {
      setStopError(e instanceof Error && e.message ? e.message : 'Network error')
    }
  }

  const title = resuming
    ? (gaveUp
      ? "Couldn't resume the session"
      : retrying
        ? 'Resuming session — a step is failing, still retrying'
        : attempt.ready
        ? 'Session ready'
        : 'Resuming session — your message is delivered when it is ready')
    : gaveUp
    ? "Couldn't start the session"
    : retrying
      ? 'Starting session — a step is failing, still retrying'
      : attempt.ready
      ? 'Session ready'
      : 'Starting session'

  return (
    <section
      className={`start-progress${gaveUp ? ' failed' : retrying ? ' retrying' : ''}${resuming ? ' resuming' : ''}`}
      aria-label="Session start progress"
      data-testid="start-progress"
    >
      <header className="sp-header">
        <span className="sp-title">{title}</span>
        <span className="sp-elapsed" data-testid="start-elapsed" aria-hidden="true">{formatElapsed(elapsed)}</span>
      </header>
      {/* One polite announcement per stage change — not per clock tick. */}
      <div className="sp-sr-only" aria-live="polite">
        {current ? `${current.label}: ${current.state === 'failed' && retrying ? 'failing, still retrying' : STATE_WORDS[current.state]}` : title}
      </div>

      <ol className="sp-stages">
        {attempt.stages.map(s => {
          const showText = showDetails || s.state === 'active' || s.state === 'failed' || s.state === 'warning'
          const took = s.startedAt && s.endedAt ? formatElapsed(s.endedAt - s.startedAt) : null
          return (
            <li
              key={s.id}
              className={`sp-stage sp-${s.state}`}
              data-stage={s.id}
              data-state={s.state}
              aria-current={s.state === 'active' ? 'step' : undefined}
            >
              <StageIcon state={s.state} />
              <div className="sp-body">
                <div className="sp-label">
                  {s.label}
                  <span className="sp-sr-only"> ({STATE_WORDS[s.state]})</span>
                  {s.state === 'failed' && retrying && <span className="sp-retrying">failing — still retrying</span>}
                  {showDetails && took && <span className="sp-took">{took}</span>}
                </div>
                {showText && s.detail && <div className="sp-detail">{s.detail}</div>}
                {s.state === 'failed' && s.hint && <div className="sp-hint" data-testid="start-hint">{s.hint}</div>}
                {showDetails && s.state !== 'failed' && s.hint && <div className="sp-hint">{s.hint}</div>}
              </div>
            </li>
          )
        })}
      </ol>

      {slow && (
        <div className="sp-slow" role="alert" data-testid="start-slow">
          {/* Announced once when it appears: the ticking clock is hidden
              from assistive tech, which hears a fixed "over 2 minutes". */}
          Still starting after <span aria-hidden="true">{formatElapsed(elapsed)}</span>
          <span className="sp-sr-only">over 2 minutes</span>. {current ? `It is on “${current.label}”.` : ''}
          {' '}Starts can take a few minutes (a cold node pulling the image, a large clone); if nothing moves, view the details or stop the session.
        </div>
      )}

      <div className="sp-actions">
        <button type="button" className="sp-btn" onClick={() => setShowDetails(d => !d)} aria-expanded={showDetails}>
          {showDetails ? 'Hide details' : 'View details'}
        </button>
        {onStop && session && (live || failedStage) && (
          <button type="button" className={`sp-btn sp-stop${stopArmed ? ' armed' : ''}`} onClick={() => void stop()}>
            {stopArmed ? 'Confirm stop?' : live ? 'Stop session' : 'Delete session'}
          </button>
        )}
        {stopError && <span className="sp-stop-error">Stop failed: {stopError}</span>}
      </div>
      {showDetails && (
        <div className="sp-meta">
          {attempt.runtime && <span>runtime: {attempt.runtime}</span>}
          {session && <span>session: {session.id}</span>}
          <span>started {new Date(attempt.startedAt).toLocaleTimeString()}</span>
        </div>
      )}
    </section>
  )
}
