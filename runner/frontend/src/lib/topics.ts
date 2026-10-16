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

export type DocItem = Extract<TopicItem, { type: 'document' }>
export type AgentItem = Extract<TopicItem, { type: 'agent' }>

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
  counts: { documents?: number; agents?: number; commits?: number }
  items: TopicItem[]
}

/** The work no marker holds, as one summary: how many subagents (each in subagents), each file at
 *  its newest version (a version's in_topic: published outside every topic), its span. */
export interface Unmarked {
  agents: number
  documents: DocItem[]
  subagents: AgentItem[]
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
}

/** Transcript events that change the reel: each one the store takes makes History read it again. */
export const TRIGGER_KINDS: ReadonlySet<string> = new Set(['topic', 'subagent_started', 'subagent_done', 'artifact', 'turn_done'])

/** The marker command, as the empty state shows it. */
export const TOPIC_COMMAND = 'blerg-runner topic "<title>"'
export const EMPTY_HISTORY = `No topics yet. The agent marks a topic when it starts a piece of work: ${TOPIC_COMMAND}.`

const pad = (n: number) => String(n).padStart(2, '0')

/** "09:05": hours and minutes, local time. */
export function hm(at: Date | string): string {
  const d = typeof at === 'string' ? new Date(at) : at
  return `${pad(d.getHours())}:${pad(d.getMinutes())}`
}

const sameDay = (a: Date, b: Date) => a.getFullYear() === b.getFullYear() && a.getMonth() === b.getMonth() && a.getDate() === b.getDate()

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
  return parts.join(' · ')
}

/** "Unmarked work · 42 agents · 8 files"; a zero is never shown. */
export function unmarkedLabel(u: Pick<Unmarked, 'agents' | 'documents'>): string {
  const parts = ['Unmarked work']
  if (u.agents) parts.push(`${u.agents} ${u.agents === 1 ? 'agent' : 'agents'}`)
  const files = u.documents.length
  if (files) parts.push(`${files} ${files === 1 ? 'file' : 'files'}`)
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
