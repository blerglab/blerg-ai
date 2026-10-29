// The session panel's History tab: the reply of GET /api/sessions/{id}/topics (runner/internal/topics)
// and the words its cards use. Pure.
import type { SessionTranscript } from '@blerglab/chat'
import { lifetimeLabel } from './sessionRail'

export type AgentStatus = 'running' | 'background' | 'done' | 'error' | 'stopped'

export interface ReelUsage {
  input_tokens?: number
  output_tokens?: number
  cache_read_tokens?: number
  cache_write_tokens?: number
}

/** A commit the agent named on its done (`blerg-runner topic --done --commit <sha>`); url is its page
 *  in the session's repository, null when the server does not know the repository. */
export interface TopicCommit { sha: string; url: string | null }

/** missing: still listed, but the server no longer has its bytes (it cannot be opened). */
export interface TopicVersion { version: number; artifact_id: string; created_at: string; in_topic: boolean; deleted: boolean; missing?: boolean }

interface ItemBase { seq: number; event_id: string; ts: string }

export type TopicItem =
  | (ItemBase & { type: 'request'; text: string; marker?: boolean })
  | (ItemBase & {
      type: 'document'; name: string; view: string; mock: boolean; content_type: string; size: number
      /** versions lists the newest 20 plus every one this topic published; versions_total is the name's full count. */
      latest_version: number; versions_total: number; versions: TopicVersion[]
    })
  | (ItemBase & {
      type: 'agent'; call_id: string; title: string; agent_type: string; model: string; usage: ReelUsage | null
      tool_uses: number; duration_ms: number; status: AgentStatus
    })
  | (ItemBase & { type: 'done'; outcome: string; commits?: TopicCommit[] })
  | (ItemBase & { type: 'card'; card: TopicCard })
  | (ItemBase & { type: 'rename'; rename: TopicRename })

/** A change of the session's title: by its owner (person), the session that started it (parent), or
 *  its own agent. */
export interface TopicRename { from: string; to: string; by: 'person' | 'parent' | 'agent' }

/** A board card an approved proposal of the session created (never its body); auto: a session
 *  permission approved it. */
export interface TopicCard {
  proposal_id: string; id: string; board_id: string; board_name?: string; number: number
  title?: string; type?: string; priority?: string; column?: string; auto?: boolean
}

export type DocItem = Extract<TopicItem, { type: 'document' }>
export type AgentItem = Extract<TopicItem, { type: 'agent' }>
export type CardItem = Extract<TopicItem, { type: 'card' }>
export type RenameItem = Extract<TopicItem, { type: 'rename' }>
export type DoneItem = Extract<TopicItem, { type: 'done' }>

/** A piece of work the agent marked (`blerg-runner topic`): the only kind of topic. */
export interface Topic {
  id: string
  source: 'marker'
  title: string
  status: 'open' | 'done' | 'ended'
  outcome?: string
  start_seq: number
  end_seq: number
  started_at: string
  ended_at: string | null
  counts: { documents?: number; agents?: number; commits?: number; cards?: number }
  items: TopicItem[]
  /** The person's messages in the topic, oldest first; absent from an older server. */
  messages?: { seq: number; event_id: string; ts: string; text: string }[]
  /** How many older messages the topic left out (it lists the newest 50). */
  messages_truncated?: number
}

/** The work no marker holds, as one summary: how many subagents (each in subagents), each file at
 *  its newest version (a version's in_topic: published outside every topic), its span. */
export interface Unmarked {
  agents: number
  documents: DocItem[]
  subagents: AgentItem[]
  /** Absent from a server older than card chips. */
  cards?: CardItem[]
  /** Absent from a server older than renames. */
  renames?: RenameItem[]
  first_seq: number
  last_seq: number
  started_at: string
  ended_at: string
}

export interface TopicsReply {
  session_id: string
  through_seq: number
  generated_at: string
  tokens_hidden: boolean
  /** null when everything is inside a topic. */
  unmarked: Unmarked | null
  topics: Topic[]
  /** The History feed, newest first (at most the newest 300); absent from a server older than strokes. */
  strokes?: Stroke[]
  /** How many older strokes the server left out. */
  strokes_truncated?: number
}

/** decision: a ruling (the person's, or the agent's own); attention: what was asked of the person and
 *  how it was resolved; milestone: merged, deployed, released, a card done; finding: a problem that
 *  changed the plan. */
export type StrokeKind = 'decision' | 'attention' | 'milestone' | 'finding'
export const STROKE_KINDS: readonly StrokeKind[] = ['decision', 'attention', 'milestone', 'finding']

/** Where "Show in conversation" goes: the row of event_id, else the proposal card of proposal_id. */
export interface StrokeJump { event_id?: string; proposal_id?: string; seq: number }

/** A board card a stroke names: its number, its board when known (a link inside Blerg), never its body. */
export interface StrokeCard { board_id?: string; board_name?: string; number: number; title?: string }

/** One line of the feed (runner/internal/topics/strokes.go). Text is data, drawn as text. */
export interface Stroke {
  id: string
  kind: StrokeKind
  /** approvals: a run of engine command approvals folded into one line (count). */
  source: 'mark' | 'ask' | 'delegation' | 'proposal' | 'review' | 'review_round' | 'topic' | 'approvals'
  /** A mark's: person (only when the person posted it) | agent. An attention stroke's: who resolved
   *  it (person, parent, permission). */
  by?: 'person' | 'agent' | 'parent' | 'permission'
  /** "person" on the agent's line recording a ruling of the person's. */
  recorded_as?: 'person'
  /** A fold of repeated strokes: how many, they themselves (newest first), the oldest one's time and
   *  their cards (oldest first). */
  count?: number
  items?: Stroke[]
  first_at?: string
  cards?: StrokeCard[]
  /** How many older members and cards the fold left out (it carries the newest 50, and every waiting member). */
  items_truncated?: number
  cards_truncated?: number
  text: string
  /** An attention stroke's: open (waiting on you) | resolved | declined | expired | unanswered (the
   *  agent did not wait on it, or the session ended with it pending: nobody is held up by it). */
  state?: 'open' | 'resolved' | 'declined' | 'expired' | 'unanswered'
  outcome?: string
  at: string
  seq: number
  topic_id?: string
  jump: StrokeJump | null
  commits?: TopicCommit[]
  card?: StrokeCard
  backdated?: boolean
}

/** Transcript events that change the reel: each one the store takes makes History read it again. */
export const TRIGGER_KINDS: ReadonlySet<string> = new Set([
  'topic', 'subagent_started', 'subagent_done', 'artifact', 'turn_done', 'proposal_decided', 'session_renamed',
  // The feed's strokes: marks, questions, approvals, proposals, reviews.
  'mark', 'ask', 'delegation_request', 'delegation_decided', 'proposal_request', 'review_requested', 'review_decided',
])

const FEEDBACK_HEAD = /^(?:Review of .+?: \d+ requests?|Marked up )/

/** Whether an event changes the reel: a trigger kind, or, of the person's messages, only those that
 *  are strokes (an answer to a question, a review round); an ordinary chat message is not. */
export function isTrigger(ev: { kind: string; payload: unknown }): boolean {
  if (TRIGGER_KINDS.has(ev.kind)) return true
  if (ev.kind !== 'user_message' || typeof ev.payload !== 'object' || ev.payload === null) return false
  const p = ev.payload as { source?: unknown; text?: unknown }
  return p.source === 'ask_answer' || (typeof p.text === 'string' && FEEDBACK_HEAD.test(p.text.trimStart()))
}

/** The marker command, as the empty state shows it. */
export const TOPIC_COMMAND = 'blerg-runner topic "<title>"'
export const MARK_COMMAND = 'blerg-runner mark decision "<text>"'
export const EMPTY_HISTORY = `No topics yet. The agent marks a topic when it starts a piece of work: ${TOPIC_COMMAND}, and a decision, milestone or finding with ${MARK_COMMAND}.`

const pad = (n: number) => String(n).padStart(2, '0')

/** "09:05": hours and minutes, local time. */
export function hm(at: Date | string): string {
  const d = typeof at === 'string' ? new Date(at) : at
  return `${pad(d.getHours())}:${pad(d.getMinutes())}`
}

const sameDay = (a: Date, b: Date) => a.getFullYear() === b.getFullYear() && a.getMonth() === b.getMonth() && a.getDate() === b.getDate()

/** A fold's span: "17:53–18:30" (the date first when it began on another day). */
export function strokeSpan(from: string, to: string, now: Date): string {
  const a = new Date(from)
  const b = new Date(to)
  if (Number.isNaN(a.getTime()) || Number.isNaN(b.getTime())) return strokeTime(to, now)
  return `${strokeTime(from, now)}–${sameDay(a, b) ? hm(b) : strokeTime(to, now)}`
}

/** A stroke's time: "09:05" today, "Oct 21, 09:05" on another day. */
export function strokeTime(at: string, now: Date): string {
  const d = new Date(at)
  if (Number.isNaN(d.getTime())) return ''
  if (sameDay(d, now)) return hm(d)
  return `${d.toLocaleDateString([], { month: 'short', day: 'numeric' })}, ${hm(d)}`
}

/** "10:42–11:30 · 48 min"; "10:42–now · 18 min" while open; the date first when it was not today. */
export function spanLabel(t: Pick<Topic, 'started_at' | 'ended_at'>, now: Date): string {
  const start = new Date(t.started_at)
  if (Number.isNaN(start.getTime())) return ''
  const end = t.ended_at ? new Date(t.ended_at) : null
  const day = sameDay(start, now) ? '' : `${start.toLocaleDateString([], { month: 'short', day: 'numeric' })}, `
  return `${day}${hm(start)}–${end ? hm(end) : 'now'} · ${lifetimeLabel((end ?? now).getTime() - start.getTime())}`
}

/** "2 docs · 3 agents · 1 commit"; a zero is never shown (an older daemon's missing count is not a 0). */
export function countsLabel(c: Topic['counts']): string {
  const parts: string[] = []
  if (c.documents) parts.push(`${c.documents} ${c.documents === 1 ? 'doc' : 'docs'}`)
  if (c.agents) parts.push(`${c.agents} ${c.agents === 1 ? 'agent' : 'agents'}`)
  if (c.commits) parts.push(`${c.commits} ${c.commits === 1 ? 'commit' : 'commits'}`)
  if (c.cards) parts.push(`${c.cards} ${c.cards === 1 ? 'card' : 'cards'}`)
  return parts.join(' · ')
}

/** "Unmarked work · 42 agents · 8 files"; a zero is never shown. */
export function unmarkedLabel(u: Pick<Unmarked, 'agents' | 'documents' | 'cards' | 'renames'>): string {
  const parts = ['Unmarked work']
  if (u.agents) parts.push(`${u.agents} ${u.agents === 1 ? 'agent' : 'agents'}`)
  const files = u.documents.length
  if (files) parts.push(`${files} ${files === 1 ? 'file' : 'files'}`)
  const cards = u.cards?.length ?? 0
  if (cards) parts.push(`${cards} ${cards === 1 ? 'card' : 'cards'}`)
  const renames = u.renames?.length ?? 0
  if (renames) parts.push(`${renames} ${renames === 1 ? 'rename' : 'renames'}`)
  return parts.join(' · ')
}

/** The first seq the transcript store holds: every event from it on can be reached by "Show in
 *  conversation" (-Infinity when nothing is older than what is held, +Infinity when nothing is
 *  held). A number, so a store selector over it changes only when this does, not on every event. */
export function heldFrom(t: SessionTranscript | undefined): number {
  if (!t || t.events.length === 0) return Number.POSITIVE_INFINITY
  if (!t.hasOlder) return Number.NEGATIVE_INFINITY
  return t.firstSeq > 0 ? t.firstSeq : (t.events[0]?.seq ?? Number.POSITIVE_INFINITY)
}

/** Whether the transcript store holds the event at seq, so "Show in conversation" can reach it.
 *  Derived each render: the store can be reset. */
export function isHeld(t: SessionTranscript | undefined, seq: number): boolean {
  return seq >= heldFrom(t)
}

/** The version a document row shows: the newest its topic published that still exists, else the
 *  newest it published. */
export function shownVersion(doc: DocItem): TopicVersion | null {
  const inTopic = doc.versions.filter(v => v.in_topic)
  const live = inTopic.filter(v => !v.deleted)
  return live[live.length - 1] ?? inTopic[inTopic.length - 1] ?? null
}

export function usageTotal(u: ReelUsage | null | undefined): number {
  if (!u) return 0
  return (u.input_tokens ?? 0) + (u.output_tokens ?? 0) + (u.cache_read_tokens ?? 0) + (u.cache_write_tokens ?? 0)
}

/** "Renamed by the parent:": who renamed the session, as History's row starts. */
export function renamedBy(by: TopicRename['by']): string {
  if (by === 'parent') return 'Renamed by the parent:'
  if (by === 'agent') return 'Renamed by the agent:'
  return 'Renamed:'
}

/** The glyph of a stroke: ◆ the person's decision, ◇ the agent's own (the person can overrule it),
 *  ? attention, ✓ milestone, ⚠ finding. */
export function strokeGlyph(s: Pick<Stroke, 'kind' | 'by'>): string {
  switch (s.kind) {
    case 'decision': return s.by === 'person' ? '◆' : '◇'
    case 'attention': return '?'
    case 'milestone': return '✓'
    case 'finding': return '⚠'
  }
}

/** What the glyph means, in words: its tooltip and its accessible name. */
export function strokeWords(s: Pick<Stroke, 'kind' | 'by' | 'state' | 'source' | 'recorded_as'>): string {
  switch (s.kind) {
    case 'decision':
      if (s.by === 'person') return 'Your decision'
      if (s.recorded_as === 'person') return 'Your decision'
      return 'Decision (agent): its own; you can overrule it'
    case 'milestone':
      return 'Milestone'
    case 'finding':
      return 'Finding'
    case 'attention': {
      const what = s.source === 'review_round' ? 'Your review' : s.source === 'approvals' ? 'Command approvals' : 'Asked of you'
      if (s.state === 'open') return `${what} · waiting`
      if (s.state === 'expired' && s.by === 'person') return `${what} · dismissed by you`
      if (s.state === 'expired') return `${what} · closed`
      if (s.state === 'unanswered') return `${what} · unanswered`
      if (s.state === 'declined') return `${what} · declined`
      if (s.by === 'parent') return `${what} · answered by the parent session`
      if (s.by === 'permission') return `${what} · approved by a session permission`
      return s.source === 'review_round' ? what : `${what} · resolved`
    }
  }
}

/** The filter chips' words. */
export const KIND_WORDS: Record<StrokeKind, string> = {
  decision: 'Decisions', attention: 'Attention', milestone: 'Milestones', finding: 'Findings',
}
export const KIND_GLYPH: Record<StrokeKind, string> = { decision: '◆', attention: '?', milestone: '✓', finding: '⚠' }

/** How many strokes of each kind. */
export function strokeCounts(strokes: readonly Stroke[]): Record<StrokeKind, number> {
  const out: Record<StrokeKind, number> = { decision: 0, attention: 0, milestone: 0, finding: 0 }
  for (const s of strokes) if (s.kind in out) out[s.kind]++
  return out
}

/** One entry of the feed: a topic with its strokes (newest first), or strokes no topic holds. */
export type FeedEntry =
  | { type: 'topic'; topic: Topic; strokes: Stroke[] }
  | { type: 'loose'; key: string; strokes: Stroke[] }

const time = (iso: string | null | undefined, fallback: number) => {
  const t = iso ? Date.parse(iso) : NaN
  return Number.isNaN(t) ? fallback : t
}

/** The feed, newest first: each topic where it ended (an open one first), its strokes under it, and
 *  the strokes no topic holds between them, in runs. shown: the kinds the filters let through (all
 *  when empty); while filtering, a topic with nothing shown is left out. */
export function feedEntries(topics: readonly Topic[], strokes: readonly Stroke[], shown: ReadonlySet<StrokeKind>): FeedEntry[] {
  const pass = (s: Stroke) => shown.size === 0 || shown.has(s.kind)
  const byTopic = new Map<string, Stroke[]>()
  const known = new Set(topics.map(t => t.id))
  const loose: Stroke[] = []
  for (const s of strokes) {
    if (!pass(s)) continue
    if (s.topic_id && known.has(s.topic_id)) {
      const list = byTopic.get(s.topic_id) ?? []
      list.push(s)
      byTopic.set(s.topic_id, list)
    } else loose.push(s)
  }
  type Keyed = { key: number; order: number; entry: FeedEntry }
  const keyed: Keyed[] = []
  topics.forEach((t, i) => {
    const list = byTopic.get(t.id) ?? []
    if (shown.size > 0 && list.length === 0) return
    const newest = list.length ? time(list[0].at, 0) : 0
    const end = t.status === 'open' ? Number.POSITIVE_INFINITY : Math.max(time(t.ended_at, time(t.started_at, 0)), newest)
    keyed.push({ key: end, order: i, entry: { type: 'topic', topic: t, strokes: list } })
  })
  // Loose strokes in runs between the topics: a run ends where a topic falls between two of them.
  let run: Stroke[] = []
  let runKey = 0
  const flush = () => {
    if (run.length) keyed.push({ key: runKey, order: -1, entry: { type: 'loose', key: run[0].id, strokes: run } })
    run = []
  }
  const ends = keyed.map(k => k.key).sort((a, b) => b - a)
  let bucket = -1
  for (const s of loose) {
    const at = time(s.at, 0)
    const b = ends.findIndex(e => e <= at)
    const idx = b === -1 ? ends.length : b
    if (idx !== bucket) {
      flush()
      bucket = idx
      runKey = at
    }
    run.push(s)
  }
  flush()
  keyed.sort((a, b) => (b.key - a.key) || (a.order - b.order))
  return keyed.map(k => k.entry)
}

/** "📄 2 · ↳ 38": a topic's counts as glyphs, each with its words for the tooltip and aria. */
export function countGlyphs(c: Topic['counts']): { glyph: string; n: number; words: string }[] {
  const out: { glyph: string; n: number; words: string }[] = []
  if (c.documents) out.push({ glyph: '📄', n: c.documents, words: `${c.documents} ${c.documents === 1 ? 'file' : 'files'}` })
  if (c.agents) out.push({ glyph: '↳', n: c.agents, words: `${c.agents} ${c.agents === 1 ? 'subagent' : 'subagents'}` })
  if (c.commits) out.push({ glyph: '⎇', n: c.commits, words: `${c.commits} ${c.commits === 1 ? 'commit' : 'commits'}` })
  if (c.cards) out.push({ glyph: '▤', n: c.cards, words: `${c.cards} ${c.cards === 1 ? 'card' : 'cards'}` })
  return out
}

/** The waiting items themselves, newest first: a waiting stroke, and each waiting member of a fold
 *  (never the fold's line or its chips). What History pins above the feed. */
export function waitingItems(strokes: readonly Stroke[]): Stroke[] {
  const out: Stroke[] = []
  for (const s of strokes) {
    if ((s.items?.length ?? 0) > 1) out.push(...s.items!.filter(i => i.state === 'open'))
    else if (s.state === 'open') out.push(s)
  }
  return out
}

/** How long a set of runs took on the clock: the union of [start, start + duration] (parallel runs
 *  are not added up). */
export function wallClockMs(runs: readonly { ts: string; duration_ms: number }[]): number {
  const spans = runs
    .map(r => { const a = Date.parse(r.ts); return [a, a + r.duration_ms] as const })
    .filter(([a, b]) => Number.isFinite(a) && b > a)
    .sort((x, y) => x[0] - y[0])
  let total = 0
  let cur: [number, number] | null = null
  for (const [a, b] of spans) {
    if (cur && a <= cur[1]) cur[1] = Math.max(cur[1], b)
    else {
      if (cur) total += cur[1] - cur[0]
      cur = [a, b]
    }
  }
  if (cur) total += cur[1] - cur[0]
  return total
}

/** A message chip's label: its time, with the date when the topic's messages span days. */
export function messageLabel(ts: string, spansDays: boolean): string {
  const d = new Date(ts)
  if (Number.isNaN(d.getTime())) return ''
  return spansDays ? `${d.toLocaleDateString([], { month: 'short', day: 'numeric' })}, ${hm(d)}` : hm(d)
}
