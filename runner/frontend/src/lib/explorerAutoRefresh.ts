// explorerAutoRefresh: when the file explorer reads its workspace again by itself (card #270).
// Event-driven, never a poll of the workspace, and only two triggers:
//   - the agent's turn ends (turn_done, or an error that ends the turn);
//   - the window regains focus or the tab becomes visible again (the person's own edits).
// A burst of triggers inside REFRESH_DEBOUNCE_MS is one refresh. What a refresh is (the explorer's
// quiet revalidation of #250) and when it must wait (the person is typing in search, an explicit
// read is in flight, the explorer or the tab is hidden) are the explorer's: RefreshScheduler only
// decides when to ask.

import { isTurnError, useTranscriptStore, type AgentEvent } from '@blerglab/chat'

/** Triggers this close together (a turn end and a focus, focus and visibilitychange) are one refresh. */
export const REFRESH_DEBOUNCE_MS = 300
/** How often a refresh that has to wait (typing, an explicit read in flight) checks again, locally. */
export const DEFER_RECHECK_MS = 1000

/** true: refresh now. 'hidden': wait, stale, for wake() (the tab or the explorer is not on screen).
 *  'later': check again in DEFER_RECHECK_MS (typing in search, an explicit read in flight). */
export type Readiness = true | 'hidden' | 'later'
/** How a refresh ended. 'failed': it could not read (still busy after the request's own retries,
 *  or another failure): stale until the next trigger. 'cancelled': an expand or Load more cancelled
 *  it: asked again. */
export type RunOutcome = 'done' | 'failed' | 'cancelled'

/** One refresh at a time, debounced, with one more afterwards for a trigger that came while it ran.
 *  `pending`: a trigger waits to be read. `failed`: the last read could not finish (still busy after
 *  the request's own retries); the view stays stale until the next trigger, or until the explorer
 *  comes on screen again (wake), which retries it once. Never a timed retry. */
export class RefreshScheduler {
  private pending = false
  private failed = false
  private running = false
  private stopped = false
  /** Moves on at every satisfied(): a refresh started before it ends without asking again. */
  private generation = 0
  private debounce: ReturnType<typeof setTimeout> | null = null
  private recheck: ReturnType<typeof setTimeout> | null = null
  private readonly run: () => Promise<RunOutcome>
  private readonly ready: () => Readiness

  constructor(run: () => Promise<RunOutcome>, ready: () => Readiness) {
    this.run = run
    this.ready = ready
  }

  /** Something changed: refresh soon. */
  request(): void {
    if (this.stopped) return
    this.pending = true
    this.schedule()
  }

  /** The explorer came on screen again (call it on the transition, not on every resize): a refresh
   *  it held while hidden runs, and a read that failed is tried once more. */
  wake(): void {
    if (this.stopped) return
    if (this.failed) { this.failed = false; this.pending = true }
    this.pump()
  }

  /** Refresh read everything: nothing pending is needed, and a quiet read it cancelled is not asked
   *  for again. A trigger after this call is still read. */
  satisfied(): void {
    this.pending = false
    this.failed = false
    this.generation++
    if (this.recheck) { clearTimeout(this.recheck); this.recheck = null }
  }

  /** Whether the view is known to be out of date: a trigger waits, or the last read failed. */
  get stale(): boolean { return this.pending || this.failed }

  stop(): void {
    this.stopped = true
    if (this.debounce) clearTimeout(this.debounce)
    if (this.recheck) clearTimeout(this.recheck)
    this.debounce = this.recheck = null
  }

  private schedule(): void {
    if (!this.debounce) this.debounce = setTimeout(() => { this.debounce = null; this.pump() }, REFRESH_DEBOUNCE_MS)
  }

  private pump(): void {
    if (this.stopped || this.running || !this.pending || this.debounce) return
    if (this.recheck) { clearTimeout(this.recheck); this.recheck = null }
    const ready = this.ready()
    if (ready === 'hidden') return
    if (ready === 'later') {
      this.recheck = setTimeout(() => { this.recheck = null; this.pump() }, DEFER_RECHECK_MS)
      return
    }
    this.pending = false
    this.running = true
    const generation = this.generation
    void this.run().catch((): RunOutcome => 'failed').then(outcome => {
      this.running = false
      if (this.stopped) return
      if (generation === this.generation) {
        if (outcome === 'cancelled') this.pending = true
        else this.failed = outcome === 'failed'
      }
      // A trigger that came during the read (or the read an expand cancelled): read it, debounced.
      if (this.pending) this.schedule()
    })
  }
}

/** Follows a session's transcript and calls `ended` when the main agent's turn ends, for a turn end
 *  that arrives after it started watching. Events a load brings in (the first replay, an older page,
 *  a gap filled in) are history, not news. Only the new end of the transcript is read: a streaming
 *  delta or an unrelated event costs one comparison, and nothing renders. Returns the unsubscribe. */
export function watchTurnEnds(sessionId: string, ended: () => void): () => void {
  const first = useTranscriptStore.getState().sessions[sessionId]
  let held: AgentEvent[] | undefined = first?.events
  let through = first?.lastSeq ?? 0
  // Seq-less events (live ones the server has not numbered) already read.
  const seen = new Set<string>(first?.events.filter(ev => ev.seq == null).map(ev => ev.client_event_id))
  // The first load (before anything was held) is history: it only moves where news starts.
  let armed = !!first && (first.replayDone || first.events.length > 0)
  return useTranscriptStore.subscribe(state => {
    const t = state.sessions[sessionId]
    const events = t?.events
    if (!events || events === held) { armed ||= !!t?.replayDone; return }
    held = events
    let turn = false
    for (let i = events.length - 1; i >= 0; i--) {
      const ev = events[i]
      if (ev.seq != null) { if (ev.seq <= through) break }
      else if (seen.has(ev.client_event_id)) continue
      else seen.add(ev.client_event_id)
      const parent = (ev.payload as { parent_call_id?: unknown } | null)?.parent_call_id
      if (!parent && (ev.kind === 'turn_done' || isTurnError(ev))) turn = true
    }
    // News starts after the newest seq held now.
    for (let i = events.length - 1; i >= 0; i--) { const s = events[i].seq; if (s != null) { through = Math.max(through, s); break } }
    if (armed && turn) ended()
    armed ||= !!t.replayDone
  })
}
