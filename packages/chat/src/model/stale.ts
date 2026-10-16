// A run with no end recorded: a subagent or background task whose start (or newest event) is
// older than STALE_RUNNING_MS and that never reported an end. A daemon restart can cut one off
// before it says so, and a page of older history can bring its start back long after; it is not
// still running. Pure: the caller says what time it is (server time, as the events' are).

/** How long after its newest event a run with no end stops counting as running. */
export const STALE_RUNNING_MS = 12 * 60 * 60 * 1000

/** What a stale subagent's card says in place of how it went. */
export const NO_END_RECORDED = 'no end recorded'

/** staleSince: whether the newest sign of life (server-time ms; null when unknown) is older than
 *  STALE_RUNNING_MS at server time now. An unknown time is never stale. */
export function staleSince(newest: number | null, now: number): boolean {
  return newest !== null && Number.isFinite(now) && now - newest > STALE_RUNNING_MS
}
