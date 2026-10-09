// Session start progress: folds a transcript's start_stage events into the
// latest start attempt, and decides whether the start panel is what the user
// should be looking at. Stages are data from the server/pod — nothing here
// knows which runtime produced them.
import type { AgentEvent, StartStage, StartStagePayload } from '../types'

/** How long a start may take before the panel says it is taking too long. */
export const STILL_STARTING_MS = 120_000

export interface AttemptStage extends StartStage {
  label: string
  /** ms timestamps of when the stage became active / finished (done or failed). */
  startedAt?: number
  endedAt?: number
}

export interface StartAttempt {
  runtime?: string
  stages: AttemptStage[]
  /** ms timestamp of the plan event that opened the attempt. */
  startedAt: number
  /** true once the "ready" stage is done. */
  ready: boolean
  readyAt?: number
  /** true when some stage is failed. */
  failed: boolean
}

/** What the start logic needs of the session: its status and, for a failed start, what it said. */
export interface StartSession {
  status: string
  error_reason?: string
  message?: string | null
}

function tsOf(ev: AgentEvent): number {
  const t = Date.parse(ev.ts)
  return Number.isFinite(t) ? t : Date.now()
}

function apply(attempt: StartAttempt, updates: StartStage[], at: number) {
  for (const u of updates) {
    const i = attempt.stages.findIndex(s => s.id === u.id)
    if (i < 0) {
      attempt.stages.push({ ...u, label: u.label || u.id, startedAt: u.state === 'active' ? at : undefined })
      continue
    }
    const cur = attempt.stages[i]
    const next: AttemptStage = { ...cur, state: u.state, detail: u.detail, hint: u.hint, label: u.label || cur.label }
    if (u.state === 'active' && cur.state !== 'active') next.startedAt = cur.startedAt ?? at
    if ((u.state === 'done' || u.state === 'failed' || u.state === 'warning') && cur.state !== u.state) {
      next.startedAt = cur.startedAt ?? at
      next.endedAt = at
    }
    attempt.stages[i] = next
  }
  // Everything before the furthest active/done stage is behind the session.
  let furthest = -1
  attempt.stages.forEach((s, i) => { if (s.state === 'active' || s.state === 'done') furthest = i })
  for (let i = 0; i < furthest; i++) {
    const s = attempt.stages[i]
    if (s.state !== 'done' && s.state !== 'warning') attempt.stages[i] = { ...s, state: 'done', hint: undefined, endedAt: s.endedAt ?? at }
  }
  const ready = attempt.stages.find(s => s.id === 'ready')
  if (ready?.state === 'done' && !attempt.ready) {
    attempt.ready = true
    attempt.readyAt = at
  }
  attempt.failed = attempt.stages.some(s => s.state === 'failed')
}

/** latestStartAttempt folds start_stage events (in seq order) into the most
 *  recent attempt, or null when the transcript has none. */
export function latestStartAttempt(events: AgentEvent[]): StartAttempt | null {
  let attempt: StartAttempt | null = null
  for (const ev of events) {
    if (ev.kind !== 'start_stage') continue
    const p = ev.payload as StartStagePayload
    if (!p || !Array.isArray(p.stages)) continue
    const at = tsOf(ev)
    if (p.plan) {
      attempt = {
        runtime: p.runtime,
        stages: p.stages.map(s => ({ ...s, label: s.label || s.id, startedAt: s.state === 'active' ? at : undefined })),
        startedAt: at,
        ready: false,
        failed: false,
      }
      apply(attempt, [], at)
      continue
    }
    if (!attempt) continue // updates with no plan in view (older page): nothing to hang them on
    apply(attempt, p.stages, at)
  }
  return attempt
}

const LIVE = new Set(['running', 'idle', 'waiting'])

/** startPanelVisible: the session has not become live yet in its current attempt. */
export function startPanelVisible(session: StartSession, attempt: StartAttempt | null): boolean {
  if (LIVE.has(session.status)) return false
  if (session.status === 'starting') return true
  // A resume in progress (or one that failed), or a start that never made it.
  return !!attempt && !attempt.ready
}

/** withSessionOutcome folds what the session row says into the attempt: a
 *  session that errored before any stage said why gets its error_reason on
 *  the stage it was on. */
export function withSessionOutcome(session: StartSession, attempt: StartAttempt | null): StartAttempt | null {
  if (!attempt || attempt.ready || attempt.failed) return attempt
  if (session.status !== 'error' && session.status !== 'stopped') return attempt
  const stages = attempt.stages.map(s => ({ ...s }))
  const i = stages.findIndex(s => s.state !== 'done' && s.state !== 'warning')
  if (i >= 0) {
    stages[i] = {
      ...stages[i],
      state: 'failed',
      detail: session.error_reason || session.message || (session.status === 'stopped' ? 'The session was stopped before it was ready' : 'The session failed to start'),
    }
  }
  return { ...attempt, stages, failed: true }
}

/** placeholderAttempt stands in while no plan has arrived yet (the request is
 *  in flight, or a server too old to send stages). */
export function placeholderAttempt(startedAt: number): StartAttempt {
  return {
    stages: [
      { id: 'queued', label: 'Queued', state: 'active', detail: 'Sending the start request', startedAt },
      { id: 'ready', label: 'Ready', state: 'pending' },
    ],
    startedAt,
    ready: false,
    failed: false,
  }
}
