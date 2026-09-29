// Grouping and summarising of tool activity for the agent transcript's
// compact view. Pure functions: nothing here touches React.
import type { AgentEvent, ToolCallPayload, ToolResultPayload } from '../types'

// Event kinds hidden from the timeline (raw context plumbing).
// start_stage renders as the start panel, capabilities as the Skills &
// plugins panel — not as timeline cards.
export const HIDDEN_KINDS = new Set(['provider_blocks', 'status_changed', 'start_stage', 'capabilities'])

// Kinds the timeline renders as a card of their own. Anything else renders
// nothing, so it must not split a run of tool calls either.
const CARD_KINDS = new Set([
  'user_message', 'assistant_text', 'check_in', 'ask', 'update', 'note',
  'model_changed', 'subagent_started', 'subagent_done', 'turn_done', 'error', 'compaction',
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

export type TimelineItem =
  | { type: 'event'; ev: AgentEvent }
  | { type: 'card'; entry: ToolEntry }
  | { type: 'tools'; key: string; entries: ToolEntry[] }

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v)
}

function toCall(ev: AgentEvent): ToolCallPayload {
  const p: Record<string, unknown> = isRecord(ev.payload) ? ev.payload : {}
  return {
    tool: typeof p.tool === 'string' && p.tool ? p.tool : 'tool',
    call_id: typeof p.call_id === 'string' && p.call_id ? p.call_id : ev.client_event_id,
    input: p.input,
  }
}

// isVisibleEvent: whether the timeline draws anything for this event (a card,
// a tool call, or a tool group member). Tool results ride on their calls.
export function isVisibleEvent(ev: AgentEvent): boolean {
  if (ev.kind === 'tool_call') return true
  if (HIDDEN_KINDS.has(ev.kind) || !CARD_KINDS.has(ev.kind)) return false
  return !(ev.kind === 'user_message' && isRecord(ev.payload) && ev.payload.source === 'system')
}

// buildTimeline folds the transcript into renderable items, merging each run
// of consecutive tool calls into one group and pairing results with calls.
export function buildTimeline(events: AgentEvent[]): TimelineItem[] {
  const results = new Map<string, ToolResultPayload>()
  for (const ev of events) {
    if (ev.kind === 'tool_result' && isRecord(ev.payload)) {
      const p = ev.payload as unknown as ToolResultPayload
      results.set(p.call_id, p)
    }
  }
  const items: TimelineItem[] = []
  let run: ToolEntry[] = []
  const flush = () => {
    if (run.length > 0) items.push({ type: 'tools', key: run[0].call.call_id, entries: run })
    run = []
  }
  for (const ev of events) {
    if (ev.kind === 'tool_result') continue
    if (ev.kind === 'tool_call') {
      const call = toCall(ev)
      const entry: ToolEntry = { ev, call, result: results.get(call.call_id) }
      if (PUBLISH_TOOLS.has(call.tool)) {
        flush()
        items.push({ type: 'card', entry })
      } else {
        run.push(entry)
      }
      continue
    }
    if (HIDDEN_KINDS.has(ev.kind) || !CARD_KINDS.has(ev.kind)) continue
    if (ev.kind === 'user_message' && isRecord(ev.payload) && ev.payload.source === 'system') continue
    flush()
    items.push({ type: 'event', ev })
  }
  flush()
  return items
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
