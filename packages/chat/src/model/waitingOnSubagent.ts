// The working line's suffix when the agent waits on a subagent rather than on a call: a resumed
// subagent's SendMessage call returns at once, so while it runs no call is in flight and the line
// would say a bare "Working…". It names the subagent instead ("· waiting on <its title>"), through the
// same hysteresis as a call (model/liveCalls nextWaitingOn). Pure.
import type { SubagentLive } from './liveState'

/** The longest a subagent's title runs in the working line. */
export const WAITING_ON_TITLE_MAX = 40

function cutTitle(s: string): string {
  const chars = Array.from(s.trim())
  return chars.length > WAITING_ON_TITLE_MAX ? chars.slice(0, WAITING_ON_TITLE_MAX - 1).join('') + '…' : chars.join('')
}

/** subagentWaitingOn: the oldest foreground subagent still running (one with a start time), as the
 *  working line names it; null when none runs (a background one is the background line's). */
export function subagentWaitingOn(subs: readonly SubagentLive[]): { tool: string; startedAt: number } | null {
  let best: SubagentLive | null = null
  for (const s of subs) {
    if (s.background || s.startedAt === null) continue
    if (best === null || s.startedAt < best.startedAt!) best = s
  }
  return best ? { tool: cutTitle(best.title), startedAt: best.startedAt! } : null
}
