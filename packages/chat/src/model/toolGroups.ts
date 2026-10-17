// Grouping and summarising of tool activity for the agent transcript's
// compact view. Pure functions: nothing here touches React.
import type { AgentEvent, SubagentPayload, ToolCallPayload, ToolResultPayload } from '../types'
import { isShownUserMessage } from './userMessages'

// Event kinds hidden from the timeline (raw context plumbing).
// start_stage renders as the start panel, capabilities as the Skills &
// plugins panel — not as timeline cards.
// subagent_update only feeds its subagent's card, background_task_* only the background strip.
export const HIDDEN_KINDS = new Set([
  'provider_blocks', 'status_changed', 'start_stage', 'capabilities', 'subagent_update',
  'background_task_started', 'background_task_done', 'mcp_status',
])

// Kinds the timeline renders as a card of their own. Anything else renders
// nothing, so it must not split a run of tool calls either.
const CARD_KINDS = new Set([
  'user_message', 'assistant_text', 'assistant_thinking', 'check_in', 'ask', 'update', 'note',
  'model_changed', 'subagent_started', 'subagent_done', 'turn_done', 'error', 'compaction', 'artifact',
  'topic', 'checklist', 'ticket',
])

// Tools whose result is a link or image worth seeing at once: they stay full
// cards and end any run of compact rows.
const PUBLISH_TOOLS = new Set(['push_mockup', 'push_screenshot'])

export const OUTPUT_LINE_CAP = 40

export interface ToolEntry {
  ev: AgentEvent
  call: ToolCallPayload
  result?: ToolResultPayload
}

/** A subagent and everything it did: drawn as one card with its own rows inside. */
export interface SubagentEntry {
  /** The subagent's child_id (for Claude Code, the call id of the Agent call that started it). */
  key: string
  /** The event the card sits at: the first of its events held (the Agent call, as a rule). */
  anchor: AgentEvent
  /** Its subagent_started; absent while only later events are held (it started on an older page). */
  started?: AgentEvent
  /** Latest nonempty native model, paired with the folded effort below. */
  model?: string
  /** Folded configured effort, including explicit unknown resets. */
  effort?: string
  info: SubagentPayload
  /** The latest subagent_done: a background launch first, its real end later. */
  done?: AgentEvent
  /** The latest subagent_update: the model and running totals. */
  update?: AgentEvent
  /** The Agent tool call that started it, with its result: folded into the card. */
  call?: ToolEntry
  /** The subagent's own rows, built like the top level's. */
  children: TimelineItem[]
  /** The newest of its own events (a child tool call or message), for what it is doing now. */
  latest?: AgentEvent
  /** Every event the card holds (the anchor first), for the transcript's window. */
  ids: string[]
  toolCalls: number
  messages: number
  /** Still running after its call returned: a background subagent (its launch said so, or its
   *  events kept arriving after a call acknowledgement with no final end). */
  background: boolean
  /** It was ever a background subagent: its call's result is the launch, not the outcome. */
  launched: boolean
}

export type TimelineItem =
  | { type: 'event'; ev: AgentEvent }
  | { type: 'card'; entry: ToolEntry }
  | { type: 'tools'; key: string; entries: ToolEntry[] }
  | { type: 'subagent'; key: string; sub: SubagentEntry }

export interface TimelineOptions {
  /** Fold runs of consecutive tool calls into groups (the compact view); otherwise every call is a
   *  card of its own. Default true. */
  groupTools?: boolean
}

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v)
}

function toCall(ev: AgentEvent): ToolCallPayload {
  const p: Record<string, unknown> = isRecord(ev.payload) ? ev.payload : {}
  return {
    tool: typeof p.tool === 'string' && p.tool ? p.tool : 'tool',
    call_id: typeof p.call_id === 'string' && p.call_id ? p.call_id : ev.client_event_id,
    input: p.input,
    ...(typeof p.parent_call_id === 'string' && p.parent_call_id ? { parent_call_id: p.parent_call_id } : {}),
  }
}

function stringField(ev: AgentEvent, name: string): string {
  const v = isRecord(ev.payload) ? ev.payload[name] : undefined
  return typeof v === 'string' ? v : ''
}

/** parentCallOf: the call that started the subagent an event belongs to; '' for the main agent's. */
export function parentCallOf(ev: AgentEvent): string {
  return stringField(ev, 'parent_call_id')
}

// isVisibleEvent: whether the timeline draws anything for this event (a card,
// a tool call, or a tool group member). Tool results ride on their calls.
export function isVisibleEvent(ev: AgentEvent): boolean {
  if (ev.kind === 'tool_call') return true
  if (HIDDEN_KINDS.has(ev.kind) || !CARD_KINDS.has(ev.kind)) return false
  return ev.kind !== 'user_message' || isShownUserMessage(ev.payload)
}

// Whether two runs of tool entries show the same calls with the same results.
function sameEntries(a: ToolEntry[], b: ToolEntry[]): boolean {
  if (a.length !== b.length) return false
  for (let i = 0; i < a.length; i++) {
    if (a[i].ev !== b[i].ev || a[i].result !== b[i].result) return false
  }
  return true
}

// Whether two timelines draw the same thing (items compared by what they hold).
function sameItems(a: readonly TimelineItem[], b: readonly TimelineItem[]): boolean {
  if (a.length !== b.length) return false
  for (let i = 0; i < a.length; i++) {
    const x = a[i]
    const y = b[i]
    if (x === y) continue
    if (x.type === 'event' && y.type === 'event' && x.ev === y.ev) continue
    if (x.type === 'tools' && y.type === 'tools' && x.entries === y.entries) continue
    if (x.type === 'card' && y.type === 'card' && x.entry.ev === y.entry.ev && x.entry.result === y.entry.result) continue
    if (x.type === 'subagent' && y.type === 'subagent' && x.sub === y.sub) continue
    return false
  }
  return true
}

// The subagents the transcript holds a card for, and which events belong to each. A card exists
// for every child_id of a subagent_started, _update or _done held, and for every parent_call_id
// seen: a long subagent whose start is pages back still gets its card (a placeholder until the
// start arrives). An event belongs to a card when its parent_call_id is the card's key, or is a
// call that itself belongs to the card (a subagent's own subagent).
function indexSubagents(events: AgentEvent[]) {
  const keys = new Set<string>()
  const owner = new Map<string, string>() // call id → card key
  const childOf = new Map<string, string>() // client_event_id → card key
  for (const ev of events) {
    if (ev.kind !== 'subagent_started' && ev.kind !== 'subagent_update' && ev.kind !== 'subagent_done') continue
    const id = stringField(ev, 'child_id')
    if (id && !keys.has(id)) {
      keys.add(id)
      owner.set(id, id)
    }
  }
  for (const ev of events) {
    const parent = parentCallOf(ev)
    if (!parent) continue
    let key = owner.get(parent)
    if (key === undefined) {
      key = parent
      keys.add(key)
      owner.set(key, key)
    }
    childOf.set(ev.client_event_id, key)
    if (ev.kind === 'tool_call') {
      const id = stringField(ev, 'call_id')
      if (id && !owner.has(id)) owner.set(id, key)
    }
  }
  return { keys, childOf }
}

interface Pending {
  effort?: string
  model?: string
  key: string
  anchor: AgentEvent
  started?: AgentEvent
  call?: ToolEntry
  done?: AgentEvent
  update?: AgentEvent
  events: AgentEvent[]
  background: boolean
  launched: boolean
}

const NO_KEYS: ReadonlySet<string> = new Set()
const NO_CHILDREN: ReadonlyMap<string, string> = new Map()

function fold(
  events: AgentEvent[],
  results: Map<string, ToolResultPayload>,
  prev: readonly TimelineItem[] | undefined,
  groupTools: boolean,
  nest: boolean,
): TimelineItem[] {
  const before = new Map<string, ToolEntry[]>()
  const subsBefore = new Map<string, SubagentEntry>()
  if (prev) {
    for (const item of prev) {
      if (item.type === 'tools') before.set(item.key, item.entries)
      else if (item.type === 'subagent') subsBefore.set(item.key, item.sub)
    }
  }
  const { keys, childOf } = nest ? indexSubagents(events) : { keys: NO_KEYS, childOf: NO_CHILDREN }
  const pending = new Map<string, Pending>()
  const returned = new Set<string>()
  // A subagent's card is a slot until every event is read: its rows arrive after it is placed.
  const items: Array<TimelineItem | { type: 'slot'; key: string }> = []
  let run: ToolEntry[] = []
  const flush = () => {
    if (run.length > 0) {
      const key = run[0].call.call_id
      const held = before.get(key)
      items.push({ type: 'tools', key, entries: held && sameEntries(held, run) ? held : run })
    }
    run = []
  }
  // The card for key, put in the timeline where its first event is.
  const place = (key: string, anchor: AgentEvent): Pending => {
    let card = pending.get(key)
    if (!card) {
      flush()
      card = { key, anchor, events: [], background: false, launched: false }
      pending.set(key, card)
      items.push({ type: 'slot', key })
    }
    return card
  }
  for (const ev of events) {
    const owner = childOf.get(ev.client_event_id)
    if (owner !== undefined) {
      const card = place(owner, ev)
      card.events.push(ev)
      // A real end remains final even when a late report arrives. Older
      // engines may acknowledge a background launch only through its call's
      // result; subsequent activity proves it kept running, until its own end.
      if (card.started && !card.done && returned.has(owner)) {
        card.background = true
        card.launched = true
      }
      continue
    }
    if (ev.kind === 'tool_result') {
      returned.add(stringField(ev, 'call_id'))
      continue
    }
    if (ev.kind === 'tool_call') {
      const call = toCall(ev)
      const entry: ToolEntry = { ev, call, result: results.get(call.call_id) }
      if (keys.has(call.call_id)) {
        const card = place(call.call_id, ev)
        if (!card.call) card.call = entry
      } else if (PUBLISH_TOOLS.has(call.tool) || !groupTools) {
        flush()
        items.push({ type: 'card', entry })
      } else {
        run.push(entry)
      }
      continue
    }
    if (ev.kind === 'subagent_started' || ev.kind === 'subagent_update' || ev.kind === 'subagent_done') {
      const key = stringField(ev, 'child_id')
      if (keys.has(key)) {
        const card = place(key, ev)
        if (isRecord(ev.payload)) {
          const p = ev.payload
          if (typeof p.model === 'string' && p.model) {
            if (p.model !== card.model) card.effort = ''
            card.model = p.model
          }
          if (typeof p.effort === 'string') card.effort = p.effort
          else if (p.effort === null) card.effort = ''
        }
        if (ev.kind === 'subagent_started') {
          if (!card.started) card.started = ev
        } else if (ev.kind === 'subagent_update') {
          card.update = ev
        } else {
          // The latest end wins: a background launch, then its real end.
          card.done = ev
          const bg = isRecord(ev.payload) && ev.payload.background === true
          card.background = bg
          if (bg) card.launched = true
        }
        continue
      }
    }
    if (HIDDEN_KINDS.has(ev.kind) || !CARD_KINDS.has(ev.kind)) continue
    if (ev.kind === 'user_message' && !isShownUserMessage(ev.payload)) continue
    flush()
    // Consecutive checklist changes with nothing drawn between them are one row: the latest (its
    // payload holds the whole list).
    const last = items[items.length - 1]
    if (ev.kind === 'checklist' && last?.type === 'event' && last.ev.kind === 'checklist') {
      items[items.length - 1] = { type: 'event', ev }
      continue
    }
    items.push({ type: 'event', ev })
  }
  flush()
  return items.map(item => (item.type === 'slot'
    ? { type: 'subagent', key: item.key, sub: subagentEntry(pending.get(item.key)!, results, subsBefore.get(item.key), groupTools) }
    : item))
}

// subagentEntry: a placed card's entry, or the previous one when it draws the same thing.
function subagentEntry(
  card: Pending,
  results: Map<string, ToolResultPayload>,
  old: SubagentEntry | undefined,
  groupTools: boolean,
): SubagentEntry {
  const ids = [card.anchor.client_event_id]
  for (const ev of [card.started, card.call?.ev, card.update, card.done, ...card.events]) {
    if (ev && ev !== card.anchor) ids.push(ev.client_event_id)
  }
  const built = fold(card.events, results, old?.children, groupTools, false)
  const children = old && sameItems(old.children, built) ? old.children : built
  let latest: AgentEvent | undefined
  for (let i = card.events.length - 1; i >= 0 && !latest; i--) {
    const k = card.events[i].kind
    if (k === 'tool_call' || k === 'assistant_text') latest = card.events[i]
  }
  if (old && children === old.children && old.anchor === card.anchor && old.started === card.started &&
      old.done === card.done && old.update === card.update && old.latest === latest && old.effort === card.effort && old.model === card.model &&
      old.call?.ev === card.call?.ev && old.call?.result === card.call?.result &&
      old.ids.length === ids.length && old.background === card.background && old.launched === card.launched) {
    return old
  }
  const info = (card.started?.payload ?? { child_id: card.key, agent_type: '', model: '', summary: '' }) as SubagentPayload
  return {
    key: card.key,
    anchor: card.anchor,
    started: card.started,
    info,
    model: card.model,
    effort: card.effort,
    done: card.done,
    update: card.update,
    call: card.call,
    children,
    latest,
    ids,
    toolCalls: card.events.filter(ev => ev.kind === 'tool_call').length,
    messages: card.events.filter(ev => ev.kind === 'assistant_text').length,
    background: card.background,
    launched: card.launched,
  }
}

// buildTimeline folds the transcript into renderable items, merging each run
// of consecutive tool calls into one group and pairing results with calls.
// A subagent becomes one item holding its own rows (its events, the Agent call
// that started it and its end), so none of them is drawn at the top level.
// Given the previous timeline, a group whose calls and results are unchanged
// keeps its previous entries array, so the memoised row drawing it is skipped
// (and a subagent that did not change keeps its entry).
export function buildTimeline(events: AgentEvent[], prev?: readonly TimelineItem[], opts: TimelineOptions = {}): TimelineItem[] {
  const results = new Map<string, ToolResultPayload>()
  for (const ev of events) {
    if (ev.kind === 'tool_result' && isRecord(ev.payload)) {
      const p = ev.payload as unknown as ToolResultPayload
      results.set(p.call_id, p)
    }
  }
  return fold(events, results, prev, opts.groupTools ?? true, true)
}

// The last timeline built for each session (and grouping), with the events it was built from: the
// transcript and a view beside it (useLiveState's subagents) read the same events, and whichever asks
// first builds the timeline for both.
const shared = new Map<string, { events: AgentEvent[]; items: TimelineItem[] }>()

/** sharedTimeline: buildTimeline for a session's events, built once per events array however many
 *  views ask, and from the session's previous timeline (so unchanged groups keep their entries). */
export function sharedTimeline(sessionId: string, events: AgentEvent[], opts: TimelineOptions = {}): TimelineItem[] {
  const key = `${sessionId}\0${opts.groupTools ?? true}`
  const last = shared.get(key)
  if (last && last.events === events) return last.items
  const items = buildTimeline(events, last?.items, opts)
  shared.set(key, { events, items })
  return items
}

/** A buildTimeline that remembers the timeline it last returned and builds the next from it. */
export function timelineBuilder(opts: TimelineOptions = {}): (events: AgentEvent[]) => TimelineItem[] {
  let last: TimelineItem[] = []
  return events => {
    last = buildTimeline(events, last, opts)
    return last
  }
}

// ─── Summaries ──────────────────────────────────────────────────────────────

const PREFERRED_FIELDS = [
  'command', 'cmd', 'file_path', 'path', 'filepath', 'file', 'notebook_path', 'url',
  'pattern', 'query', 'name', 'prompt', 'question', 'body', 'description',
]

function firstLine(s: string): string {
  const head = s.length > 2000 ? s.slice(0, 2000) : s
  for (const line of head.split('\n')) {
    const t = line.trim()
    if (t) return t
  }
  return ''
}

function truncate(s: string, max: number): string {
  return s.length > max ? s.slice(0, max) + '…' : s
}

function asText(v: unknown): string | null {
  if (typeof v === 'string') return v
  if (Array.isArray(v) && v.length > 0 && v.every(x => typeof x === 'string')) return v.join(' ')
  return null
}

export interface CallDescription {
  summary: string
  shell: boolean
}

// describeInput: a short human summary of a call's input. Inputs are
// untrusted and of any shape; this never throws.
export function describeInput(input: unknown): CallDescription {
  if (typeof input === 'string') return { summary: truncate(firstLine(input), 100), shell: false }
  if (!isRecord(input)) return { summary: '', shell: false }
  for (const key of PREFERRED_FIELDS) {
    const text = asText(input[key])
    if (text === null) continue
    const line = firstLine(text)
    if (line) return { summary: truncate(line, 100), shell: key === 'command' || key === 'cmd' }
  }
  for (const v of Object.values(input)) {
    const text = asText(v)
    const line = text === null ? '' : firstLine(text)
    if (line) return { summary: truncate(line, 100), shell: false }
  }
  return { summary: '', shell: false }
}

// previewLine: what a call is doing, in one line — "$ npm test",
// "Read src/App.tsx".
export function previewLine(call: ToolCallPayload): string {
  const { summary, shell } = describeInput(call.input)
  if (shell) return `$ ${summary}`
  return summary ? `${call.tool} ${summary}` : call.tool
}

export interface GroupStats {
  total: number
  failed: number
  pending: number
  durationMs: number
  hasDuration: boolean
  byTool: Array<[string, number]>
}

export function groupStats(entries: ToolEntry[]): GroupStats {
  const counts = new Map<string, number>()
  let failed = 0
  let pending = 0
  let durationMs = 0
  let hasDuration = false
  for (const e of entries) {
    counts.set(e.call.tool, (counts.get(e.call.tool) ?? 0) + 1)
    if (!e.result) pending++
    else {
      if (e.result.is_error) failed++
      if (typeof e.result.duration_ms === 'number' && Number.isFinite(e.result.duration_ms)) {
        durationMs += e.result.duration_ms
        hasDuration = true
      }
    }
  }
  // Most-used first; ties keep the order the tools first appeared in.
  const byTool = Array.from(counts.entries()).sort((a, b) => b[1] - a[1])
  return { total: entries.length, failed, pending, durationMs, hasDuration, byTool }
}

const MAX_TOOL_NAMES = 4

export function summaryText(stats: GroupStats): string {
  const shown = stats.byTool.slice(0, MAX_TOOL_NAMES).map(([n, c]) => `${n} ×${c}`)
  const more = stats.byTool.length - shown.length
  if (more > 0) shown.push(`+${more} more`)
  return `Ran ${stats.total} tool calls · ${shown.join(', ')}`
}

export function formatDuration(ms: number): string {
  if (ms < 1000) return `${Math.round(ms)}ms`
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`
  const s = Math.round(ms / 1000)
  return `${Math.floor(s / 60)}m ${s % 60}s`
}

// failureLine: the first line of a failed call's output, for the row shown
// under a collapsed summary.
export function failureLine(result?: ToolResultPayload): string {
  const out = typeof result?.output === 'string' ? result.output : ''
  return truncate(firstLine(out), 200) || 'failed'
}

export function safeJSON(v: unknown): string {
  try {
    return JSON.stringify(v, null, 2) ?? ''
  } catch {
    return String(v)
  }
}

export function outputText(result: ToolResultPayload): string {
  const o: unknown = result.output
  return typeof o === 'string' ? o : o == null ? '' : safeJSON(o)
}
