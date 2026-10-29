// Which tool calls are in flight right now, and how long they have been. Pure.
import type { AgentEvent } from '../types'
import { isTurnError } from './notices'
import { formatElapsed, parseTs } from './timeLabel'

/** After this a running call's clock is emphasised and explained. */
export const LONG_RUNNING_MS = 2 * 60_000
/** After this the preview line adds a hint about Stop. */
export const STILL_RUNNING_MS = 10 * 60_000

export interface LiveCall {
  callId: string
  tool: string
  /** What the call says it does, for the working line (callDetail); '' when it says nothing. */
  detail?: string
  /** Server-time ms the call started; null when its ts is missing or unparsable. */
  startedAt: number | null
}

/** What the working line names: a call (its tool, and what it does when it says) or a subagent. */
export interface WaitingOn {
  tool: string
  startedAt: number
  detail?: string
}

/** The longest a call's detail runs in the working line. */
export const WAITING_ON_DETAIL_MAX = 48

function firstLine(s: string): string {
  for (const line of s.split('\n')) {
    const t = line.trim()
    if (t) return t
  }
  return ''
}

function cutDetail(s: string): string {
  const chars = Array.from(s)
  return chars.length > WAITING_ON_DETAIL_MAX ? chars.slice(0, WAITING_ON_DETAIL_MAX - 1).join('') + '…' : s
}

/**
 * callDetail: what a call says it does, in one short line for the working line: a Bash call's
 * description (else the start of its command); an Agent's or a Monitor's description. Other tools
 * have no one-liner to give: ''.
 */
export function callDetail(tool: string, input: unknown): string {
  const field = (name: string): string => {
    const v = input && typeof input === 'object' ? (input as Record<string, unknown>)[name] : undefined
    return typeof v === 'string' ? firstLine(v) : ''
  }
  switch (tool) {
    case 'Bash':
      return cutDetail(field('description') || field('command'))
    case 'Agent':
    case 'Task':
    case 'Monitor':
      return cutDetail(field('description'))
  }
  return ''
}

function callIdOf(ev: AgentEvent): string {
  const p = ev.payload as { call_id?: unknown } | null
  return p && typeof p.call_id === 'string' && p.call_id ? p.call_id : ev.client_event_id
}

function statusOf(ev: AgentEvent): string {
  const s = (ev.payload as { status?: unknown } | null)?.status
  return typeof s === 'string' ? s : ''
}

/**
 * startsTurn: events[i] is where the turn now running begins, looking back from the newest event:
 * the end of the turn before it (a turn_done, an error that ends a turn), a message from the person
 * (not the system's, nor an answer to an ask: the native loop records the answer before it goes
 * back to running), or a status change other than an ask's (going to waiting, and running again
 * after the answer, are inside one turn).
 */
function startsTurn(events: AgentEvent[], i: number): boolean {
  const ev = events[i]
  if (ev.kind === 'turn_done' || isTurnError(ev)) return true
  if (ev.kind === 'user_message') {
    const source = (ev.payload as { source?: unknown } | null)?.source
    return source !== 'system' && source !== 'ask_answer' && source !== 'usage_limit'
  }
  if (ev.kind !== 'status_changed') return false
  const status = statusOf(ev)
  if (status === 'waiting') return false
  if (status !== 'running') return true
  for (let j = i - 1; j >= 0; j--) {
    if (events[j].kind === 'status_changed') return statusOf(events[j]) !== 'waiting'
  }
  return true
}

export interface LiveTurn {
  /** The unresolved calls after the newest turn start held (all those held when none is), oldest first. */
  calls: LiveCall[]
  /** A turn start was found in the events: the calls are the turn's own. Without one the turn began
   *  before the oldest event held (part of a history still loading), and a call whose result is not
   *  held, or an orphan of a turn long over, is among the calls. */
  bounded: boolean
  /** The client_event_id of the event that starts the turn; null when not bounded. */
  turnStart: string | null
}

const NO_TURN: LiveTurn = { calls: [], bounded: false, turnStart: null }

/**
 * liveTurn: the tool calls in flight in the turn now running (see startsTurn), and whether that
 * turn's start is held. Nothing unless the session is running or waiting: a call with no result in
 * an ended session, or in an earlier turn, is an orphan and must not tick.
 */
export function liveTurn(events: AgentEvent[], sessionActive: boolean): LiveTurn {
  if (!sessionActive) return NO_TURN
  const resolved = new Set<string>()
  const out: LiveCall[] = []
  let turnStart: string | null = null
  for (let i = events.length - 1; i >= 0; i--) {
    const ev = events[i]
    if (startsTurn(events, i)) {
      turnStart = ev.client_event_id
      break
    }
    if (ev.kind === 'tool_result') {
      resolved.add(callIdOf(ev))
    } else if (ev.kind === 'tool_call') {
      const id = callIdOf(ev)
      if (resolved.has(id)) continue
      const p = ev.payload as { tool?: unknown; input?: unknown } | null
      const tool = p && typeof p.tool === 'string' && p.tool ? p.tool : 'tool'
      out.push({ callId: id, tool, detail: callDetail(tool, p?.input), startedAt: parseTs(ev.ts) })
    }
  }
  return { calls: out.reverse(), bounded: turnStart !== null, turnStart }
}

/**
 * liveCalls: the unresolved tool calls of the turn in progress, oldest first (liveTurn's calls).
 * What the cards and the panel tick for; the working line names one only when the turn is bounded.
 */
export function liveCalls(events: AgentEvent[], sessionActive: boolean): LiveCall[] {
  return liveTurn(events, sessionActive).calls
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

/** A call must be in flight this long before the working line names it. */
export const WAITING_ON_AFTER_MS = 2_000
/** The working line's "waiting on" suffix changes (appears, goes, names another call) at most this often. */
export const WAITING_ON_HOLD_MS = 3_000

export interface WaitingOnState {
  /** The call the suffix names; null: no suffix. */
  shown: WaitingOn | null
  /** Server time it last changed (-Infinity: never). */
  since: number
}

export const WAITING_ON_INITIAL: WaitingOnState = { shown: null, since: -Infinity }

/**
 * nextWaitingOn: what the working line's suffix names at server time now, given the oldest call in
 * flight. Most calls last under a second, so a call is named only once it has run WAITING_ON_AFTER_MS,
 * and the suffix changes at most once per WAITING_ON_HOLD_MS: once shown it stays that long even if its
 * call ends, then names the call in flight by then, or goes. Returns state itself when nothing changes.
 */
export function nextWaitingOn(
  state: WaitingOnState, oldest: WaitingOn | null, now: number,
): WaitingOnState {
  const want = oldest && now - oldest.startedAt >= WAITING_ON_AFTER_MS ? oldest : null
  const cur = state.shown
  const same = want === null ? cur === null : cur !== null && cur.tool === want.tool && cur.startedAt === want.startedAt
  if (same || now - state.since < WAITING_ON_HOLD_MS) return state
  return { shown: want ? { tool: want.tool, startedAt: want.startedAt, ...(want.detail ? { detail: want.detail } : {}) } : null, since: now }
}
