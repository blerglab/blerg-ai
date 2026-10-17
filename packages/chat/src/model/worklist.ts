// What a session works through: its checklist (`blerg-runner checklist`), the engine's own plan
// (the server's live state reads Claude Code's task tools) and the tickets the agent pinned
// (`blerg-runner pin`). Pure: reading the events' payloads defensively (an event is untrusted data:
// a field of the wrong type is dropped, never drawn), the words the chat rows say, and the fold
// of the events into what the session panel shows. The server stores the change in each event, so
// nothing here compares one event with another.
import type { AgentEvent, ChecklistItem, ChecklistPayload, Ticket, TicketComment, TicketField, TicketPayload } from '../types'
import { parseTs } from './timeLabel'
import { safeUrl } from './safeUrl'

export interface ChecklistLive {
  items: ChecklistItem[]
  /** The event that set this list (the newest checklist event). */
  eventId: string
  updatedAt: number | null
  /** The web page of each commit the items hold, when the host's server knows the repository. */
  commitUrls?: Record<string, string>
}

export type PlanStatus = 'pending' | 'in_progress' | 'completed'
export interface PlanItemLive { text: string; status: PlanStatus; active?: string }
/** The engine's own plan (Claude Code's task list), when the agent set no checklist. */
export interface PlanLive { items: PlanItemLive[]; updatedAt: number | null }

export interface TicketSummaryLive {
  source: string
  key: string
  title: string
  status: string
  url: string
  /** The event that pinned or refreshed it last. */
  eventId: string
  updatedAt: number | null
}

/** The current pin in full. */
export interface TicketLive extends Ticket { eventId: string; updatedAt: number | null }

export interface PinnedLive {
  /** What is pinned, the current one (the newest pin or refresh) first. */
  tickets: TicketSummaryLive[]
  /** The current one in full; null when nothing is pinned. */
  ticket: TicketLive | null
}

type Rec = Record<string, unknown>
const rec = (v: unknown): Rec | null => (typeof v === 'object' && v !== null && !Array.isArray(v) ? v as Rec : null)
const str = (v: unknown): string => (typeof v === 'string' ? v : '')
const strs = (v: unknown): string[] => (Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string' && x !== '') : [])

const SHA = /^[0-9a-f]{7,40}$/
/** The commits an item holds: well-formed (7 to 40 hex characters), lower-cased, distinct. */
function readCommits(v: unknown): string[] {
  const out: string[] = []
  if (Array.isArray(v)) for (const x of v) {
    const sha = typeof x === 'string' ? x.toLowerCase() : ''
    if (SHA.test(sha) && !out.includes(sha)) out.push(sha)
  }
  return out
}

const ARTIFACT_ID = /^[0-9a-f]{32}$/
/** The id of an item's diff (a session artifact's id), or '' when it is not one. */
function readDiff(v: unknown): string {
  return typeof v === 'string' && ARTIFACT_ID.test(v) ? v : ''
}

/** "1 commit", "3 commits". */
export function commitsText(n: number): string {
  return `${n} commit${n === 1 ? '' : 's'}`
}

/** readChecklist: a checklist event's payload, its items checked; null when it is not one. */
export function readChecklist(payload: unknown): ChecklistPayload | null {
  const p = rec(payload)
  if (!p || !Array.isArray(p.items)) return null
  const items: ChecklistItem[] = []
  for (const it of p.items) {
    const r = rec(it)
    if (!r || typeof r.text !== 'string') continue
    const commits = readCommits(r.commits)
    const diff = commits.length ? readDiff(r.diff) : ''
    items.push({ text: r.text, done: r.done === true, ...(commits.length ? { commits } : {}), ...(diff ? { diff } : {}) })
  }
  const c = rec(p.changed)
  const cc = c ? readCommits(c.commits) : []
  const cd = c && cc.length ? readDiff(c.diff) : ''
  const changed = c && typeof c.text === 'string' && typeof c.index === 'number'
    ? { index: c.index, text: c.text, done: c.done === true, ...(cc.length ? { commits: cc } : {}), ...(cd ? { diff: cd } : {}) }
    : undefined
  return { items, op: str(p.op), ...(changed ? { changed } : {}) }
}

/** "3 of 7 done". */
export function progressText(done: number, total: number): string {
  return `${done} of ${total} done`
}

export function doneCount(items: readonly { done?: boolean; status?: string }[]): number {
  return items.filter(it => it.done === true || it.status === 'completed').length
}

/** checklistRowText: the chat's one-line row for a checklist event, e.g.
 *  `Checklist · 3 of 7 done · ticked "Write tests"`. */
export function checklistRowText(p: ChecklistPayload): string {
  if (p.items.length === 0) return 'Checklist cleared'
  const head = `Checklist · ${progressText(doneCount(p.items), p.items.length)}`
  const c = p.changed
  switch (p.op) {
    case 'tick': return c ? `${head} · ticked “${c.text}”` : head
    case 'untick': return c ? `${head} · unticked “${c.text}”` : head
    case 'add': return c ? `${head} · added “${c.text}”` : head
    case 'set': return `${head} · set`
    default: return head
  }
}

/** planLabel: the in-progress item and the count, e.g. "Writing tests · 3 of 7". */
export function planLabel(plan: PlanLive): string {
  const n = `${doneCount(plan.items)} of ${plan.items.length}`
  const now = plan.items.find(it => it.status === 'in_progress')
  return now ? `${now.active || now.text} · ${n}` : n
}

/** ticketHref: the ticket's link when it is an absolute http(s) address (not mailto, not anything
 *  else safeUrl would allow), else null. */
export function ticketHref(url: unknown): string | null {
  const u = safeUrl(typeof url === 'string' ? url : null)
  if (!u) return null
  return /^https?:/i.test(u) ? u : null
}

/** readTicket: a ticket as drawn, every field checked (a non-string dropped); null without a key. */
export function readTicket(v: unknown): Ticket | null {
  const t = rec(v)
  if (!t || !str(t.key)) return null
  const fields: TicketField[] = []
  if (Array.isArray(t.fields)) {
    for (const f of t.fields) {
      const r = rec(f)
      if (r && str(r.name)) fields.push({ name: str(r.name), value: str(r.value) })
    }
  }
  const comments: TicketComment[] = []
  if (Array.isArray(t.comments)) {
    for (const c of t.comments) {
      const r = rec(c)
      if (r && str(r.body)) comments.push({ author: str(r.author), at: str(r.at), body: str(r.body) })
    }
  }
  const out: Ticket = { source: str(t.source), key: str(t.key), title: str(t.title) }
  const opt = ['url', 'status', 'type', 'priority', 'assignee', 'reporter', 'description'] as const
  for (const k of opt) if (str(t[k])) out[k] = str(t[k])
  const labels = strs(t.labels)
  const acceptance = strs(t.acceptance)
  if (labels.length) out.labels = labels
  if (acceptance.length) out.acceptance = acceptance
  if (fields.length) out.fields = fields
  if (comments.length) out.comments = comments
  return out
}

/** readTicketEvent: a ticket event's payload, checked; null when it is not one. */
export function readTicketEvent(payload: unknown): TicketPayload | null {
  const p = rec(payload)
  const ticket = p ? readTicket(p.ticket) : null
  if (!p || !ticket) return null
  const prev = rec(p.previous)
  return { ticket, action: str(p.action), ...(prev ? { previous: { status: str(prev.status), title: str(prev.title) } } : {}) }
}

/** ticketChanges: what a re-pin changed, in words ("status Open → In Progress", "new title"). */
export function ticketChanges(p: TicketPayload): string[] {
  if (p.action !== 'update' || !p.previous) return []
  const out: string[] = []
  const before = p.previous.status
  const after = p.ticket.status ?? ''
  if (before !== after) out.push(before && after ? `status ${before} → ${after}` : after ? `status ${after}` : 'status cleared')
  if (p.previous.title !== p.ticket.title) out.push('new title')
  return out
}

/** The source as a badge says it: "JIRA", "Bugzilla", "GitHub", … (any other as it came). */
export function sourceLabel(source: string): string {
  const known: Record<string, string> = {
    jira: 'JIRA', bugzilla: 'Bugzilla', github: 'GitHub', gitlab: 'GitLab', linear: 'Linear', 'blerg-board': 'Board',
  }
  return known[source.toLowerCase()] ?? source
}

/** checklistFromEvents: the newest checklist held (null when there is none, or it was cleared). */
export function checklistFromEvents(events: readonly AgentEvent[]): ChecklistLive | null {
  for (let i = events.length - 1; i >= 0; i--) {
    const ev = events[i]
    if (ev.kind !== 'checklist') continue
    const p = readChecklist(ev.payload)
    if (!p) continue
    return p.items.length ? { items: p.items, eventId: ev.client_event_id, updatedAt: parseTs(ev.ts) } : null
  }
  return null
}

const NO_PINS: PinnedLive = { tickets: [], ticket: null }

/** foldTicketEvent: state with one ticket event applied (a pin or a refresh makes it current; an
 *  unpin takes it off). Returns state itself for any other event. */
export function foldTicketEvent(state: PinnedLive, ev: AgentEvent): PinnedLive {
  if (ev.kind !== 'ticket') return state
  const p = readTicketEvent(ev.payload)
  if (!p) return state
  const same = (s: { source: string; key: string }) => s.source === p.ticket.source && s.key === p.ticket.key
  const rest = state.tickets.filter(s => !same(s))
  if (p.action === 'unpin') {
    if (rest.length === state.tickets.length) return state
    // The current one went: the next is current, known only by its summary until a read.
    const current = state.ticket && !same(state.ticket) ? state.ticket : null
    return { tickets: rest, ticket: current ?? (rest[0] ? summaryTicket(rest[0]) : null) }
  }
  if (p.action !== 'pin' && p.action !== 'update') return state
  const updatedAt = parseTs(ev.ts)
  const t = p.ticket
  const summary: TicketSummaryLive = {
    source: t.source, key: t.key, title: t.title, status: t.status ?? '', url: t.url ?? '', eventId: ev.client_event_id, updatedAt,
  }
  return { tickets: [summary, ...rest], ticket: { ...t, eventId: ev.client_event_id, updatedAt } }
}

function summaryTicket(s: TicketSummaryLive): TicketLive {
  return { source: s.source, key: s.key, title: s.title, ...(s.status ? { status: s.status } : {}), ...(s.url ? { url: s.url } : {}), eventId: s.eventId, updatedAt: s.updatedAt }
}

/** pinnedFromEvents: what is pinned by the events held (the chat's own reading, on a server without
 *  the live state's tickets). */
export function pinnedFromEvents(events: readonly AgentEvent[]): PinnedLive {
  let st = NO_PINS
  for (const ev of events) st = foldTicketEvent(st, ev)
  return st
}
