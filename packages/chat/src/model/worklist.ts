// What a session works through: its checklist (`blerg-runner checklist`), the engine's own plan
// (the server's live state reads Claude Code's task tools) and the tickets the agent pinned
// (`blerg-runner pin`). Pure: reading the events' payloads defensively (an event is untrusted data:
// a field of the wrong type is dropped, never drawn), the words the chat rows say, and the fold
// of the events into what the session panel shows. The server stores the change in each event, so
// nothing here compares one event with another.
import type { AgentEvent, ChecklistAssignee, ChecklistItem, ChecklistPayload, Ticket, TicketComment, TicketField, TicketPayload } from '../types'
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
  /** A Blerg Board card's board (its key is "#<number>"). */
  boardId?: string
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

// IDs are data, never selectors or paths. Conservative legacy fallback stays unlinked when an
// old event ID cannot be represented; the server can normalize it with its hash-based fallback.
const WORK_ID = /^[A-Za-z0-9_-]{1,96}$/
const LEGACY_SEED = /^[A-Za-z0-9_-]{1,48}$/
const workerId = (v: unknown): v is string => typeof v === 'string' && v.length > 0 && new TextEncoder().encode(v).length <= 256 && v.trim() === v && !/[\p{Cc}\p{Cs}]/u.test(v)
const workId = (v: unknown): string => typeof v === 'string' && WORK_ID.test(v) ? v : ''
function readAssignees(value: unknown): ChecklistAssignee[] {
  const out: ChecklistAssignee[] = []
  if (Array.isArray(value)) for (const raw of value.slice(0, 32)) {
    const a = rec(raw)
    if (!a || (a.kind !== 'subagent' && a.kind !== 'child_session') || !workerId(a.id)) continue
    if (!out.some(x => x.kind === a.kind && x.id === a.id)) out.push({ kind: a.kind, id: a.id })
  }
  return out
}
function readItem(r: Rec): ChecklistItem {
  const commits = readCommits(r.commits)
  const diff = commits.length ? readDiff(r.diff) : ''
  const id = workId(r.id)
  const assignees = readAssignees(r.assignees)
  return { text: r.text as string, done: r.done === true,
    ...(id ? { id } : {}), ...(r.in_progress === true && r.done !== true ? { in_progress: true } : {}),
    ...(assignees.length ? { assignees } : {}), ...(commits.length ? { commits } : {}), ...(diff ? { diff } : {}) }
}

/** readChecklist: defensively read an event or /live list; old event IDs derive the same ordinary
 *  legacy identities as the server. No title/index matching across replacements. */
export function readChecklist(payload: unknown, eventId?: string): ChecklistPayload | null {
  const p = rec(payload)
  if (!p || !Array.isArray(p.items)) return null
  const items: ChecklistItem[] = []
  const used = new Set<string>()
  for (const [index, raw] of p.items.slice(0, 50).entries()) {
    const r = rec(raw)
    if (!r || typeof r.text !== 'string') continue
    const item = readItem(r)
    const fallback = eventId && LEGACY_SEED.test(eventId) ? `legacy-${eventId}-${index + 1}` : ''
    if (!item.id && fallback) item.id = fallback
    // Ambiguous duplicate IDs cannot safely be navigation targets.
    if (item.id && used.has(item.id)) {
      for (const previous of items) if (previous.id === item.id) delete previous.id
      delete item.id
    } else if (item.id) used.add(item.id)
    items.push(item)
  }
  const c = rec(p.changed)
  const changed = c && typeof c.text === 'string' && Number.isSafeInteger(c.index) && (c.index as number) > 0
    ? { ...readItem(c), index: c.index as number, ...(workId(c.from_item_id) ? { from_item_id: workId(c.from_item_id) } : {}) }
    : undefined
  return { items, op: str(p.op), ...(changed ? { changed } : {}) }
}

/** Explicit association only. An absent/ambiguous target must not navigate to another row. */
export function checklistAssignment(items: readonly ChecklistItem[], kind: ChecklistAssignee['kind'], id: string): ChecklistItem | undefined {
  if (!id) return undefined
  const found = items.filter(item => item.id && item.assignees?.some(a => a.kind === kind && a.id === id))
  return found.length === 1 ? found[0] : undefined
}

/** Resolve explicit native aliases from loaded events and suppress proven identity collisions.
 *  A host snapshot may know more than the loaded transcript: unknown IDs remain unchanged.
 *  Return the original references when unchanged so streaming tools/text don't churn the UI. */
export function safeChecklistAssignments(items: readonly ChecklistItem[], events: readonly AgentEvent[]): readonly ChecklistItem[] {
  const children = new Set<string>()
  const callsOnly = new Set<string>()
  const aliases = new Map<string, Set<string>>()
  for (const ev of events) {
    if (!['subagent_started', 'subagent_update', 'subagent_done'].includes(ev.kind)) continue
    const p = rec(ev.payload)
    if (!p) continue
    const child = workerId(p.child_id) ? p.child_id : ''
    const call = workerId(p.call_id) ? p.call_id : ''
    if (child) children.add(child)
    if (call && !child) callsOnly.add(call)
    if (call && child) {
      const targets = aliases.get(call) ?? new Set<string>()
      targets.add(child)
      aliases.set(call, targets)
    }
  }
  const ambiguous = new Set<string>()
  for (const id of new Set([...children, ...aliases.keys()])) {
    const targets = aliases.get(id)
    if ((targets && (targets.size > 1 || (children.has(id) && [...targets].some(target => target !== id)))) ||
      (children.has(id) && callsOnly.has(id) && !targets?.size)) ambiguous.add(id)
  }
  let changed = false
  const out = items.map(item => {
    if (!item.assignees?.length) return item
    let itemChanged = false
    const assignees: ChecklistAssignee[] = []
    for (const a of item.assignees) {
      if (a.kind !== 'subagent') { assignees.push(a); continue }
      const id = aliases.get(a.id)?.values().next().value ?? a.id
      if (ambiguous.has(a.id) || ambiguous.has(id)) { itemChanged = true; continue }
      if (id !== a.id) { itemChanged = true; assignees.push({ ...a, id }) }
      else assignees.push(a)
    }
    if (!itemChanged) return item
    changed = true
    return { ...item, assignees }
  })
  return changed ? out : items
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
  if (p.items.length === 0) return p.op === 'remove' && p.changed ? `Checklist · removed “${p.changed.text}”, none left` : 'Checklist cleared'
  const head = `Checklist · ${progressText(doneCount(p.items), p.items.length)}`
  const c = p.changed
  switch (p.op) {
    case 'tick': return c ? `${head} · ticked “${c.text}”` : head
    case 'doing': return c ? `${head} · doing “${c.text}”` : head
    case 'pending': return c ? `${head} · queued “${c.text}”` : head
    case 'assign': return c ? `${head} · assigned work on “${c.text}”` : head
    case 'unassign': return c ? `${head} · unassigned work on “${c.text}”` : head
    case 'rename': return c ? `${head} · renamed “${c.text}”` : head
    case 'move': return c ? `${head} · moved “${c.text}”` : head
    case 'untick': return c ? `${head} · unticked “${c.text}”` : head
    case 'remove': return c ? `${head} · removed “${c.text}”` : head
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
  const opt = ['url', 'board_id', 'status', 'type', 'priority', 'assignee', 'reporter', 'description'] as const
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
    const p = readChecklist(ev.payload, ev.client_event_id)
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
  // A pin is (source, key) and, for a Blerg Board card, its board: #42 on two boards is two pins.
  const board = p.ticket.board_id ?? ''
  const same = (s: { source: string; key: string; boardId?: string; board_id?: string }) =>
    s.source === p.ticket.source && s.key === p.ticket.key && (s.boardId ?? s.board_id ?? '') === board
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
    ...(t.board_id ? { boardId: t.board_id } : {}),
  }
  return { tickets: [summary, ...rest], ticket: { ...t, eventId: ev.client_event_id, updatedAt } }
}

function summaryTicket(s: TicketSummaryLive): TicketLive {
  return { source: s.source, key: s.key, title: s.title, ...(s.status ? { status: s.status } : {}), ...(s.url ? { url: s.url } : {}), ...(s.boardId ? { board_id: s.boardId } : {}), eventId: s.eventId, updatedAt: s.updatedAt }
}

/** pinnedFromEvents: what is pinned by the events held (the chat's own reading, on a server without
 *  the live state's tickets). */
export function pinnedFromEvents(events: readonly AgentEvent[]): PinnedLive {
  let st = NO_PINS
  for (const ev of events) st = foldTicketEvent(st, ev)
  return st
}

/** A status by what it means, for its chip's color: to do (and anything unknown) is neutral, then
 *  active (being worked), blocked, done, and canceled (the title struck through). */
export type StatusTone = 'neutral' | 'active' | 'blocked' | 'done' | 'canceled'

const TONES: Record<Exclude<StatusTone, 'neutral'>, string[]> = {
  active: ['in progress', 'in review', 'review', 'started', 'doing', 'active', 'testing', 'qa', 'in qa', 'in testing', 'in development', 'wip'],
  blocked: ['blocked', 'on hold', 'waiting', 'paused'],
  done: ['done', 'closed', 'resolved', 'fixed', 'complete', 'completed', 'merged', 'released', 'shipped'],
  canceled: ["won't do", "won't fix", 'wontfix', 'wontdo', 'canceled', 'cancelled', 'duplicate', 'invalid', 'rejected'],
}
const TONE_OF = new Map<string, StatusTone>(
  (Object.entries(TONES) as [StatusTone, string[]][]).flatMap(([tone, words]) => words.map(w => [w, tone] as [string, StatusTone])),
)

/** statusTone: a tracker's status word by meaning ("In Progress", "in_progress" and "IN-PROGRESS"
 *  are the same); a word not known is neutral. */
export function statusTone(status: string | undefined): StatusTone {
  const s = (status ?? '').trim().toLowerCase().replace(/[’‘`]/g, "'").replace(/[\s_-]+/g, ' ')
  return TONE_OF.get(s) ?? 'neutral'
}

/** priorityGlyph: ↑ for a high priority, → for a medium one, ↓ for a low one; '' for any other.
 *  Words (High, Minor…) and the P-scale (P0 and P1 high, P2 medium, P3 and P4 low, also as
 *  "P1 - Critical"); the priority itself is always shown as the tracker wrote it. */
export function priorityGlyph(priority: string | undefined): string {
  const p = (priority ?? '').trim().toLowerCase()
  const scale = /^p([0-4])(?![\w])/.exec(p)
  if (scale) return ['↑', '↑', '→', '↓', '↓'][Number(scale[1])]
  if (['highest', 'high', 'critical', 'urgent', 'blocker'].includes(p)) return '↑'
  if (['medium', 'normal', 'moderate', 'major'].includes(p)) return '→'
  if (['low', 'lowest', 'minor', 'trivial'].includes(p)) return '↓'
  return ''
}

/** ticketMeta: the card's one meta line, type · priority · assignee, the missing ones left out. */
export function ticketMeta(t: Partial<Ticket>): string[] {
  return [t.type, t.priority, t.assignee].filter((x): x is string => !!x)
}

// ── A pin's state as one glyph (#134): the chat's pin rows and the runner's panel share these ──

export type PinState = 'done' | 'doing' | 'todo'

/** More names for work in flight than the status words above know. */
const DOING = new Set(['review', 'code review', 'in dev', 'dev', 'implementing', 'wip', 'qa', 'verify', 'staging'])

/** A column name without its decoration: leading numbering ("2.", "2)"), emoji and symbols, and a
 *  trailing count ("(3)", "[3]", "· 3"). */
export function bareStatus(status: string | undefined): string {
  return (status ?? '')
    .replace(/^[\s\d.)\]:#-]*(?=\p{L})/u, '')
    .replace(/^[^\p{L}]+/u, '')
    .replace(/[\s·:-]*[([]?\s*\d+\s*[)\]]?\s*$/u, '')
    .trim()
}

/** The state a status word means (a board column's name, or another tracker's status). */
export function pinStateOfStatus(raw: string | undefined): PinState {
  const status = bareStatus(raw)
  if (DOING.has(status.toLowerCase().replace(/[\s_-]+/g, ' '))) return 'doing'
  const tone = statusTone(status)
  if (tone === 'done' || tone === 'canceled') return 'done'
  if (tone === 'active' || tone === 'blocked') return 'doing'
  return 'todo'
}

/** The state's glyph and its words. */
export const PIN_STATE_GLYPH: Record<PinState, { glyph: string; words: string }> = {
  done: { glyph: '✓', words: 'Done' },
  doing: { glyph: '◐', words: 'In progress' },
  todo: { glyph: '○', words: 'Not started' },
}

/** A run of pins (or of unpins) drawn as one row: the tickets, oldest first. */
export interface TicketRun { action: 'pin' | 'unpin'; tickets: Ticket[] }

/** The tickets a ticket event's row draws: its own, after any it folded (a run of pins or unpins). */
export function ticketRunOf(ev: AgentEvent): TicketRun | null {
  const p = readTicketEvent(ev.payload)
  if (!p || (p.action !== 'pin' && p.action !== 'unpin')) return null
  const earlier = Array.isArray((ev.payload as { earlier?: unknown }).earlier) ? (ev.payload as { earlier: unknown[] }).earlier : []
  const tickets = earlier.map(readTicket).filter((t): t is Ticket => !!t)
  return { action: p.action, tickets: [...tickets, p.ticket] }
}

/** foldTicketRun: two ticket events side by side in the transcript, a pin after a pin or an unpin after
 *  an unpin, as one: the later event, carrying the earlier tickets (`earlier`). Null when they do not
 *  fold (a refresh, a mixed pair, anything else): a refresh says what changed on a row of its own. */
export function foldTicketRun(prev: AgentEvent, ev: AgentEvent): AgentEvent | null {
  if (prev.kind !== 'ticket' || ev.kind !== 'ticket') return null
  const a = ticketRunOf(prev)
  const b = readTicketEvent(ev.payload)
  if (!a || !b || a.action !== b.action) return null
  return { ...ev, payload: { ...(ev.payload as object), earlier: a.tickets } }
}
