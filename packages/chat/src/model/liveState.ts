// What is live in a session right now, for a view beside the chat (the runner's side panel): the
// subagents still running and the agent's questions still waiting on an answer. Read from the same
// events the transcript draws, with the same rules as its subagent cards and ask cards. Pure.
import type { AgentEvent, MessagingPayload } from '../types'
import { askStates } from './asks'
import { liveCalls } from './liveCalls'
import { doingLine, inputField, resumedSubagent, subagentEffort, subagentModel, subagentStatus, subagentTitle, subagentTokens } from './subagents'
import { parseTs } from './timeLabel'
import { buildTimeline, parentCallOf, type SubagentEntry, type TimelineItem } from './toolGroups'

/** A subagent still at work, as the side panel shows it. */
export interface SubagentLive {
  /** Its card's key: the call id the subagent card carries (scrollToCall(key) shows it). */
  key: string
  /** Canonical native child identity, distinct from its transcript call key. */
  childId?: string
  /** The task's description, else the start of its prompt. */
  title: string
  /** Its type (general-purpose, Explore, …); '' when not reported. */
  type: string
  model: string
  /** Child-reported configured effort, absent/empty when unknown. */
  effort?: string
  /** Every token its API calls used so far, or null when none was reported. */
  tokens: number | null
  /** What it is doing now ("running Bash · npm test"); '' before its first step. */
  doing: string
  /** Server-time ms it started, when held. */
  startedAt: number | null
  /** It runs in the background (its call already returned its launch). */
  background: boolean
  /** A finished subagent the main agent resumed: it runs until its own end, not with a call. */
  resumed?: boolean
}

/** One of the agent's questions still waiting on an answer. */
export interface AskLive {
  /** The ask event's client_event_id. */
  eventId: string
  /** Its messages API id, when the daemon recorded one (an answer addresses it). */
  messageId: string
  question: string
  /** Server-time ms it was asked. */
  askedAt: number | null
  /** A question the agent does not wait on (`blerg-runner ask --no-wait`). */
  noWait?: boolean
}

/** What the chat showing the session says about it (see ChatView). */
export interface LiveReport {
  /** The session runs or waits on a turn: a call without a result is still in flight. */
  inFlight: boolean
  /** The session can still be running something (running, idle or waiting). */
  live: boolean
  /** The session is over (stopped, ended or failed): nothing it asked is still open. */
  ended: boolean
  /** Asks the server reported answered or closed, by message id, with their answer. */
  closedAsks: ReadonlyMap<string, string>
  /** The chat folds runs of tool calls (its timeline is then grouped; a view beside it reads the
   *  same one). */
  compact?: boolean
}

function hasSubagents(events: AgentEvent[]): boolean {
  return events.some(ev => ev.kind === 'subagent_started' || ev.kind === 'subagent_update' || ev.kind === 'subagent_done' || parentCallOf(ev) !== '')
}

// When a subagent started: its subagent_started, else its call; null when neither is held.
function startOf(sub: SubagentEntry): number | null {
  const ev = sub.started ?? sub.call?.ev
  return ev ? parseTs(ev.ts) : null
}

/** liveSubagents: the top-level subagents still running (a foreground one whose call is in flight,
 *  or a background one in a live session), oldest first. A subagent's own subagents stay inside its
 *  card. */
export function liveSubagents(
  events: AgentEvent[],
  report: Pick<LiveReport, 'inFlight' | 'live'>,
  /** Server-time ms now: a subagent with no event for STALE_RUNNING_MS and no end is not running. */
  now: number,
  /** The events' timeline when the caller has it (sharedTimeline); built here otherwise. */
  timeline?: (events: AgentEvent[]) => TimelineItem[],
): SubagentLive[] {
  if (!report.live || !hasSubagents(events)) return []
  const inFlight = new Set(liveCalls(events, report.inFlight).map(c => c.callId))
  const out: SubagentLive[] = []
  for (const item of (timeline ? timeline(events) : buildTimeline(events))) {
    if (item.type !== 'subagent') continue
    const sub = item.sub
    const status = subagentStatus(sub, inFlight, now)
    if (status !== 'running' && status !== 'background') continue
    out.push({
      key: sub.key,
      ...(sub.info.child_id ? { childId: sub.info.child_id } : {}),
      title: subagentTitle(sub),
      type: sub.info.agent_type || inputField(sub.call?.call.input, 'subagent_type'),
      model: subagentModel(sub),
      ...(subagentEffort(sub) ? { effort: subagentEffort(sub) } : {}),
      tokens: subagentTokens(sub),
      doing: doingLine(sub),
      // Only a start that is held: the first event held of a subagent that began on a page not
      // loaded yet (the edge of a history still coming in) is not when it started, and a time for
      // it would let the working line name it (subagentWaitingOn) as if it ran now.
      startedAt: startOf(sub),
      background: status === 'background',
      ...(resumedSubagent(sub) ? { resumed: true } : {}),
    })
  }
  return out
}

/** openAsks: the agent's questions still open, oldest first. */
export function openAsks(events: AgentEvent[], report: Pick<LiveReport, 'ended' | 'closedAsks'>): AskLive[] {
  const asks = askStates(events, report.closedAsks, report.ended)
  if (asks.open === null && !asks.anyOpen) return []
  const out: AskLive[] = []
  for (const ev of events) {
    if (ev.kind !== 'ask' || asks.byEvent.get(ev.client_event_id)?.open !== true) continue
    const p = (ev.payload ?? {}) as Partial<MessagingPayload>
    out.push({
      eventId: ev.client_event_id,
      messageId: typeof p.message_id === 'string' ? p.message_id : '',
      question: typeof p.body === 'string' ? p.body : '',
      askedAt: parseTs(ev.ts),
      ...(p.no_wait === true ? { noWait: true } : {}),
    })
  }
  return out
}
