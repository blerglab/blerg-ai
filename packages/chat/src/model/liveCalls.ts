// Which tool calls are in flight right now, and how long they have been. Pure.
import type { AgentEvent } from '../types'
import { formatElapsed, parseTs } from './timeLabel'

/** After this a running call's clock is emphasised and explained. */
export const LONG_RUNNING_MS = 2 * 60_000
/** After this the preview line adds a hint about Stop. */
export const STILL_RUNNING_MS = 10 * 60_000

export interface LiveCall {
  callId: string
  tool: string
  /** Server-time ms the call started; null when its ts is missing or unparsable. */
  startedAt: number | null
}

function callIdOf(ev: AgentEvent): string {
  const p = ev.payload as { call_id?: unknown } | null
  return p && typeof p.call_id === 'string' && p.call_id ? p.call_id : ev.client_event_id
}

/**
 * liveCalls: the unresolved tool calls of the turn in progress, oldest first.
 * Empty unless the session is running or waiting: a call with no result in an
 * ended session, or in an earlier turn, is an orphan and must not tick.
 */
export function liveCalls(events: AgentEvent[], sessionActive: boolean): LiveCall[] {
  if (!sessionActive) return []
  const resolved = new Set<string>()
  const out: LiveCall[] = []
  for (let i = events.length - 1; i >= 0; i--) {
    const ev = events[i]
    if (ev.kind === 'turn_done' || ev.kind === 'error') break
    if (ev.kind === 'user_message' && (ev.payload as { source?: unknown } | null)?.source !== 'system') break
    if (ev.kind === 'tool_result') {
      resolved.add(callIdOf(ev))
    } else if (ev.kind === 'tool_call') {
      const id = callIdOf(ev)
      if (resolved.has(id)) continue
      const p = ev.payload as { tool?: unknown } | null
      out.push({
        callId: id,
        tool: p && typeof p.tool === 'string' && p.tool ? p.tool : 'tool',
        startedAt: parseTs(ev.ts),
      })
    }
  }
  return out.reverse()
}

/** oldestLive: the longest-running live call with a usable start time. */
export function oldestLive(calls: LiveCall[]): (LiveCall & { startedAt: number }) | null {
  let best: (LiveCall & { startedAt: number }) | null = null
  for (const c of calls) {
    if (c.startedAt !== null && (best === null || c.startedAt < best.startedAt)) best = c as LiveCall & { startedAt: number }
  }
  return best
}

/** runningMs: ms a call has run, on the server clock; null without a start time; never negative. */
export function runningMs(startedAt: number | null, browserNow: number, clockOffset: number): number | null {
  if (startedAt === null || !Number.isFinite(browserNow)) return null
  return Math.max(0, browserNow - clockOffset - startedAt)
}

/** runningLabel: "4:12" (h:mm:ss past an hour). */
export function runningLabel(ms: number): string {
  return formatElapsed(ms)
}

export function longRunningTitle(ms: number): string {
  const n = Math.floor(ms / 60_000)
  return `This command has been running for ${n} ${n === 1 ? 'minute' : 'minutes'}. Claude Code reports output only when it finishes.`
}
