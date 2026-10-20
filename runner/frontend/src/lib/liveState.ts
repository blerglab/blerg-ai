// The session panel's Live tab, read from the server: the reply of GET /api/sessions/{id}/live
// (runner/internal/livestate) and the events that came after it, folded in so the panel moves at
// once between two reads. Only what is trivial to fold is: a subagent's start, update and end (and a
// person's message or the turn's end ending a foreground one), a background task's start and end, an
// ask and its answer. Anything else (a turn's end, a status
// change) brings a new read. Pure.
import {
  STALE_RUNNING_MS, foldTicketEvent, parseTs, readChecklist, safeChecklistAssignments, readMcp, readMcpHold,
  type AgentEvent, type AskLive, type BackgroundTask, type ChecklistItem, type ChecklistLive, type PinnedLive, type McpHold, type McpLive, type PlanLive,
  type LiveState, type PlanStatus, type SubagentLive, type Ticket, type TicketLive, type TicketSummaryLive,
} from '@blerglab/chat'

/** Engine-neutral progress supplied by the server; no command-output inference. */
export interface LiveProgress { completed: number; total: number; label: string; updated_at?: string }
export type ProgressSubagent = SubagentLive & { progress?: LiveProgress }
export type SessionPanelLiveState = Omit<LiveState, 'subagents'> & { subagents: ProgressSubagent[]; progress?: LiveProgress | null }

export function readProgress(value: unknown): LiveProgress | null {
  const p = rec(value)
  if (!Number.isSafeInteger(p.completed) || !Number.isSafeInteger(p.total) ||
    (p.total as number) <= 0 || (p.completed as number) < 0 || (p.completed as number) > (p.total as number) ||
    typeof p.label !== 'string') return null
  return { completed: p.completed as number, total: p.total as number, label: p.label,
    ...(typeof p.updated_at === 'string' ? { updated_at: p.updated_at } : {}) }
}

export interface LiveAskReply { event_id: string; message_id: string; question: string; asked_at: string; seq: number }

export interface LiveSubagentReply {
  key: string
  child_id?: string
  title: string
  type: string
  model: string
  effort?: string
  progress?: LiveProgress
  tokens: number | null
  doing: string
  started_at: string
  /** Its newest sign of life: it stops counting STALE_RUNNING_MS after it. */
  active_at: string
  background: boolean
  /** A finished subagent the main agent resumed: it runs until its own end, past the turn's. */
  resumed?: boolean
  seq: number
}

export interface LiveTaskReply { task_id: string; call_id: string; kind: string; description: string; command?: string; started_at: string; seq: number }

export interface LiveChecklistReply { items: ChecklistItem[]; event_id: string; updated_at: string; commit_urls?: Record<string, string> }
export interface LivePlanReply { items: { text: string; status: PlanStatus; active?: string }[]; updated_at: string }
export interface LiveTicketSummaryReply { source: string; key: string; title: string; status: string; url: string; event_id: string; seq: number; updated_at: string }
export type LiveTicketReply = Ticket & { event_id: string; updated_at: string }

export interface LiveChildSession {
  id: string; title: string; engine: string; model: string; effort?: string; status: string
  started_at: string; ended_at?: string | null; archived_at?: string | null; asking: boolean; tokens: number | null; cost_usd?: number
  /** Reported current-turn telemetry, separate from the completed-turn billing ledger. */
  usage_status?: 'pending' | 'reported' | 'hidden'; tool_calls?: number; activity?: string
  activity_since?: string; last_activity_at?: string; local?: boolean
}
export interface LiveDelegationRequest { request_id: string; engine: string; title: string; expires_at: string }
/** A pending review request (blerg-runner review request): Needs you shows one row per request. */
export interface LiveReviewRequest { request_id: string; artifact_id: string; artifact_name: string; artifact_version: number; artifact_view: string; ask: string; created_at: string }
export interface LiveDelegationGrant { id: string; engine: string; runtime: string; daemon_id?: string; daemon_name?: string; repo?: string; no_repo?: boolean }
export const childIsRunning = (s: LiveChildSession) => !s.ended_at && ['starting', 'running', 'idle', 'waiting'].includes(s.status)

export interface LiveReply {
  children?: LiveChildSession[]
  delegation_requests?: LiveDelegationRequest[]
  delegation_grants?: LiveDelegationGrant[]
  review_requests?: LiveReviewRequest[]
  session_id: string
  through_seq: number
  generated_at: string
  tokens_hidden: boolean
  asks: LiveAskReply[]
  subagents: LiveSubagentReply[]
  tasks: LiveTaskReply[]
  tool_changes: number
  counts: { needs_you: number; running: number }
  /** The checklist, the engine's plan and the pins (a server older than them leaves them out). */
  checklist?: LiveChecklistReply | null
  plan?: LivePlanReply | null
  progress?: LiveProgress | null
  tickets?: LiveTicketSummaryReply[]
  ticket?: LiveTicketReply | null
  /** The session's MCP servers as the engine last reported them (mcp_status), and the held first
   *  prompt while it waits for them; the viewer may release it when they own the session. */
  mcp?: unknown
  mcp_hold?: { since: string; until: string } | null
  can_release_prompt?: boolean
}

/** The events that change the live state: each brings a new read (folded meanwhile when it can be).
 *  A subagent_update is not one (it comes every few seconds while a subagent works): its model,
 *  tokens and sign of life are folded, and the doing line it would refresh waits for the next read. */
export const LIVE_TRIGGER_KINDS: ReadonlySet<string> = new Set([
  'ask', 'subagent_started', 'subagent_done', 'background_task_started', 'background_task_done', 'turn_done',
  'checklist', 'ticket', 'mcp_status', 'plan_updated', 'progress_updated', 'children_changed', 'delegation_request', 'delegation_decided',
  'review_requested', 'review_decided',
])

/** The engine's task tools whose calls change the plan (the server's livestate.PlanTools). */
const PLAN_TOOLS: ReadonlySet<string> = new Set(['TodoWrite', 'TaskCreate', 'TaskUpdate'])

/** Plan tools can belong to the session or a child; results can assign newly created task IDs. */
export function isLiveTrigger(ev: AgentEvent): boolean {
  if (LIVE_TRIGGER_KINDS.has(ev.kind)) return true
  if (ev.kind === 'tool_result') return true
  if (ev.kind !== 'tool_call') return false
  const p = rec(ev.payload)
  return PLAN_TOOLS.has(str(p.tool))
}

/** The kinds foldLive reads: the triggers, a subagent's updates and a person's message (it can
 *  answer an ask, and it ends a foreground subagent). */
export const LIVE_FOLD_KINDS: ReadonlySet<string> = new Set([...LIVE_TRIGGER_KINDS, 'subagent_update', 'user_message'])

/** What the panel draws: the lists oldest first, and its counts (the header badge's). */
export interface LiveView {
  children?: LiveChildSession[]
  delegationRequests?: LiveDelegationRequest[]
  delegationGrants?: LiveDelegationGrant[]
  reviewRequests?: LiveReviewRequest[]
  asks: AskLive[]
  subagents: ProgressSubagent[]
  /** Background tasks still running (a background subagent's own task left out). */
  tasks: BackgroundTask[]
  toolChanges: number
  counts: { needsYou: number; running: number }
  /** The agent's checklist; null when it has none. */
  checklist: ChecklistLive | null
  /** The engine's plan (only the server reads it); null when there is none. */
  plan: PlanLive | null
  progress: LiveProgress | null
  /** The pins, the current one first, and the current one in full. */
  tickets: TicketSummaryLive[]
  ticket: TicketLive | null
  /** The MCP servers; null when the session has none the engine reported. */
  mcp: McpLive | null
  /** The first prompt is held for the servers; null when it is not (or the wait is over). */
  mcpHold: McpHold | null
  /** The viewer may release the held prompt. */
  canReleasePrompt: boolean
}

type Rec = Record<string, unknown>
const rec = (v: unknown): Rec => (typeof v === 'object' && v !== null && !Array.isArray(v) ? v as Rec : {})
const str = (v: unknown): string => (typeof v === 'string' ? v : '')

function firstLine(s: string): string {
  for (const line of s.split('\n')) {
    const t = line.trim()
    if (t) return t
  }
  return ''
}

// The chat's subagentTokens: every token of a usage object, or null when there is none.
function usageTotal(u: unknown): number | null {
  if (typeof u !== 'object' || u === null || Array.isArray(u)) return null
  const r = u as Rec
  let n = 0
  for (const k of ['input_tokens', 'output_tokens', 'cache_read_tokens', 'cache_write_tokens']) {
    const v = r[k]
    if (typeof v === 'number' && Number.isFinite(v)) n += v
  }
  return n
}

interface SubAcc extends ProgressSubagent { activeAt: number | null }

/** The index of the first event after the reply (the store holds them in seq order, live ones that
 *  carry no seq yet at the end). */
function firstAfter(events: readonly AgentEvent[], through: number): number {
  let i = events.length
  while (i > 0) {
    const s = events[i - 1].seq
    if (s !== undefined && s <= through) break
    i--
  }
  return i
}

/** foldLive: the reply with the events after it folded in, at server time now (ms): a subagent or
 *  task with no sign of life for STALE_RUNNING_MS is left out, as the server would. */
export function foldLive(reply: LiveReply, events: readonly AgentEvent[], now: number): LiveView {
  const assignmentFences = new Set<string>()
  const subs = new Map<string, SubAcc>()
  const promotedAliases = new Map<string, string>()
  for (const s of reply.subagents ?? []) {
    subs.set(s.key, {
      key: s.key, childId: s.child_id || s.key, title: s.title, type: s.type, model: s.model, ...(typeof s.effort === 'string' ? { effort: s.effort } : {}), tokens: s.tokens, doing: s.doing,
      startedAt: parseTs(s.started_at), background: s.background, activeAt: parseTs(s.active_at),
      ...(s.resumed ? { resumed: true } : {}),
      ...(readProgress(s.progress) ? { progress: readProgress(s.progress)! } : {}),
    })
  }
  const tasks = new Map<string, BackgroundTask>()
  for (const t of reply.tasks ?? []) {
    tasks.set(t.task_id, { taskId: t.task_id, callId: t.call_id, kind: t.kind, description: t.description, command: t.command ?? '', startedAt: parseTs(t.started_at), done: null })
  }
  const initialChecklist = reply.checklist ? readChecklist(reply.checklist, reply.checklist.event_id) : null
  let checklist: ChecklistLive | null = reply.checklist && initialChecklist?.items.length
    ? {
        items: initialChecklist.items, eventId: reply.checklist.event_id, updatedAt: parseTs(reply.checklist.updated_at),
        ...(reply.checklist.commit_urls ? { commitUrls: reply.checklist.commit_urls } : {}),
      }
    : null
  let mcp: McpLive | null = readMcp(reply.mcp)
  let mcpHold: McpHold | null = readMcpHold(reply.mcp_hold)
  const plan: PlanLive | null = reply.plan && reply.plan.items.length ? { items: reply.plan.items, updatedAt: parseTs(reply.plan.updated_at) } : null
  let pinned: PinnedLive = {
    tickets: (reply.tickets ?? []).map(t => ({ source: t.source, key: t.key, title: t.title, status: t.status, url: t.url, eventId: t.event_id, updatedAt: parseTs(t.updated_at) })),
    ticket: reply.ticket ? (({ event_id, updated_at, ...t }) => ({ ...t, eventId: event_id, updatedAt: parseTs(updated_at) }))(reply.ticket) : null,
  }
  const asks: AskLive[] = (reply.asks ?? []).map(a => ({ eventId: a.event_id, messageId: a.message_id, question: a.question, askedAt: parseTs(a.asked_at) }))

  const promote = (native: string, call: string) => {
    if (!native || !call || native === call || subs.has(native)) return
    const previous = subs.get(call)
    if (!previous || previous.childId) return
    subs.delete(call)
    if (assignmentFences.delete(call)) assignmentFences.add(native)
    previous.key = native
    previous.childId = native
    subs.set(native, previous)
    promotedAliases.set(call, native)
  }

  for (let i = firstAfter(events, reply.through_seq); i < events.length; i++) {
    const ev = events[i]
    const p = rec(ev.payload)
    const at = parseTs(ev.ts)
    switch (ev.kind) {
      case 'checklist': {
        // Each event holds the whole list: the latest wins.
        const c = readChecklist(ev.payload, ev.client_event_id)
        // The pages of the commits the reply knew stay; a newer commit has none until the next read.
        if (c) {
          assignmentFences.clear()
          checklist = c.items.length ? { items: c.items, eventId: ev.client_event_id, updatedAt: at, ...(checklist?.commitUrls ? { commitUrls: checklist.commitUrls } : {}) } : null
        }
        break
      }
      case 'mcp_status': {
        // Each snapshot holds all the servers: the latest wins, and it ends the hold when it settles.
        const m = readMcp(ev.payload)
        if (m) {
          mcp = m
          mcpHold = m.settled || m.released ? null : readMcpHold(p.hold)
        }
        break
      }
      case 'ticket':
        pinned = foldTicketEvent(pinned, ev)
        break
      case 'subagent_started': {
        const native = str(p.child_id)
        const key = native || str(p.call_id)
        promote(native, str(p.call_id))
        if (!key || subs.has(key)) break
        assignmentFences.add(key)
        // A resumed run (a finished subagent resumed with SendMessage) is not tied to a call: it
        // runs until its own end, in the background when its original did.
        const resumed = p.resumed === true
        subs.set(key, {
          key, ...(native ? {childId:native} : {}), title: firstLine(str(p.summary)) || 'Subagent · started earlier', type: str(p.agent_type), model: str(p.model), ...(typeof p.effort === 'string' ? { effort: p.effort } : {}),
          tokens: null, doing: '', startedAt: at, background: resumed && p.background === true, activeAt: at,
          ...(resumed ? { resumed: true } : {}),
        })
        break
      }
      case 'subagent_update':
      case 'subagent_done': {
        const native = str(p.child_id)
        const call = str(p.call_id)
        promote(native, call)
        const alias = promotedAliases.get(call)
        const ambiguous = !native && alias && alias !== call && subs.has(call)
        const s = ambiguous ? undefined : subs.get(native || alias || call)
        if (!s) break
        if (ev.kind === 'subagent_done' && p.background !== true) {
          subs.delete(s.key)
          for (const [key, target] of promotedAliases) if (target === s.key) promotedAliases.delete(key)
          break
        }
        if (ev.kind === 'subagent_done') s.background = true
        if (str(p.model)) {
          if (s.model !== str(p.model)) s.effort = ''
          s.model = str(p.model)
        }
        if (typeof p.effort === 'string') s.effort = p.effort
        else if (p.effort === null) s.effort = ''
        const tokens = usageTotal(p.usage)
        if (tokens !== null && !reply.tokens_hidden) s.tokens = tokens
        s.activeAt = at ?? s.activeAt
        break
      }
      case 'background_task_started': {
        const id = str(p.task_id)
        if (!id || tasks.has(id)) break
        tasks.set(id, { taskId: id, callId: str(p.call_id), kind: str(p.kind), description: str(p.description), command: str(p.command), startedAt: parseTs(str(p.started_at)) ?? at, done: null })
        break
      }
      case 'turn_done':
        // The turn is over: no call of it is in flight, a foreground subagent's included.
        for (const sub of [...subs.values()]) if (!sub.background && !sub.resumed) subs.delete(sub.key)
        break
      case 'background_task_done':
        tasks.delete(str(p.task_id))
        break
      case 'ask':
        if (!asks.some(a => a.eventId === ev.client_event_id)) {
          asks.push({ eventId: ev.client_event_id, messageId: str(p.message_id), question: str(p.body), askedAt: at })
        }
        break
      case 'user_message': {
        // An answer closes its ask (by message id, else the newest); a person's message closes the
        // newest ask that has no message id.
        const source = str(p.source)
        const id = str(p.message_id)
        let k = -1
        if (source === 'ask_answer') k = id ? asks.findIndex(a => a.messageId === id) : asks.length - 1
        else if (source !== 'system') k = asks.map(a => a.messageId === '').lastIndexOf(true)
        if (k >= 0) asks.splice(k, 1)
        // The chat's liveCalls (and the server's rule): a message that is not the system's ends the
        // calls in flight, so a foreground subagent stops counting; a background one runs on.
        if (source !== 'system') for (const sub of [...subs.values()]) if (!sub.background && !sub.resumed) subs.delete(sub.key)
        break
      }
    }
  }

  if (checklist) {
    const safe = safeChecklistAssignments(checklist.items, events)
    if (safe !== checklist.items) checklist = {...checklist, items: [...safe]}
  }
  if (checklist && assignmentFences.size) checklist = {...checklist, items: checklist.items.map(item => {
    const assignees = item.assignees?.filter(worker => worker.kind !== 'subagent' || !assignmentFences.has(worker.id))
    const next = {...item}
    if (assignees?.length) next.assignees = assignees
    else delete next.assignees
    return next
  })}

  const stale = (t: number | null) => t !== null && Number.isFinite(now) && now - t > STALE_RUNNING_MS
  const subagents: ProgressSubagent[] = []
  for (const s of subs.values()) {
    if (stale(s.activeAt)) continue
    subagents.push({
      key: s.key, ...(s.childId ? {childId:s.childId}:{}), title: s.title, type: s.type, model: s.model, ...(typeof s.effort === 'string' ? { effort: s.effort } : {}), tokens: s.tokens, doing: s.doing, startedAt: s.startedAt, background: s.background,
      ...(s.resumed ? { resumed: true } : {}),
      ...(s.progress ? { progress: s.progress } : {}),
    })
  }
  const listed = new Set(subagents.map(s => s.key))
  const running = [...tasks.values()].filter(t => !stale(t.startedAt) && !(t.kind === 'agent' && listed.has(t.callId)))
  const toolChanges = reply.tool_changes ?? 0
  return {
    asks, subagents, tasks: running, toolChanges,
    children: reply.children ?? [], delegationRequests: reply.delegation_requests ?? [], delegationGrants: reply.delegation_grants ?? [],
    reviewRequests: reply.review_requests ?? [],
    counts: { needsYou: asks.length + toolChanges + (reply.delegation_requests?.length ?? 0) + (reply.review_requests?.length ?? 0), running: subagents.length + running.length + (reply.children ?? []).filter(childIsRunning).length },
    checklist, plan, progress: readProgress(reply.progress), tickets: pinned.tickets, ticket: pinned.ticket,
    mcp: mcp && mcp.servers.length ? mcp : null,
    mcpHold: mcpHold && Number.isFinite(now) && mcpHold.until <= now ? null : mcpHold,
    canReleasePrompt: reply.can_release_prompt === true,
  }
}
