// The Conversation view of a diff review, as data (GitHub's and GitLab's pull request page): the
// versions published under one diff's name, each review round the person submitted, the review
// requests and their decisions, and every thread with the few lines of code it is attached to.
// Everything is derived from what is already stored: the diff's versions (their bytes), the
// person's `<name>.review.json` uploads (one per submit), the merged review and the review requests.
// Pure functions; the patch text is untrusted and only ever drawn as text.
import type { ArtifactInfo } from '../transport/types'
import type { ReviewRequestPayload } from '../types'
import { diffAnchorContains } from './diffSelection'
import type { Patch, PatchCommit, PatchSummary } from './patch'
import { projectReviewAnchor } from './reviewProjection'
import type { Anchor, ReviewFile, ReviewRequest } from './review'

/** Lines of context drawn above and below a thread's code. */
export const EXCERPT_CONTEXT = 3
/** A long selection draws this many of its lines, then says how many more. */
export const EXCERPT_MAX_MARKED = 12

export interface ExcerptRow {
  kind: 'ctx' | 'add' | 'del'
  old?: number
  new?: number
  text: string
  /** One of the thread's own lines. */
  marked: boolean
}

export interface Excerpt {
  file: string
  side: 'old' | 'new'
  start: number
  end: number
  rows: ExcerptRow[]
  /** Marked lines not drawn (a long selection). */
  more: number
  /** false: drawn from the thread's saved quote alone (its patch was not at hand). */
  exact: boolean
}

/** The code a thread is attached to, with a few lines of context, from the patch it was written
 *  on. Without that patch (or when it does not hold the thread's code), the saved quote alone,
 *  numbered from the thread's first line. Null for a thread with no code (general, or a legacy
 *  passage without diff metadata). */
export function threadExcerpt(patch: Patch | null, anchor: Anchor, context = EXCERPT_CONTEXT): Excerpt | null {
  const range = anchor.diff
  if (!range || anchor.kind !== 'text') return null
  const base = { file: range.file, side: range.side, start: range.start, end: range.end }
  if (patch && diffAnchorContains(patch, anchor, { fileIndex: range.fileIndex, hunk: range.hunk, side: range.side, line: range.start })) {
    const lines = patch.files[range.fileIndex - 1].hunks[range.hunk - 1].lines
    const first = lines.findIndex(l => l.kind !== 'note' && l[range.side] === range.start)
    const last = lines.findIndex(l => l.kind !== 'note' && l[range.side] === range.end)
    const rows: ExcerptRow[] = []
    let more = 0
    let marked = 0
    for (let i = Math.max(0, first - context); i <= Math.min(lines.length - 1, last + context); i++) {
      const l = lines[i]
      if (l.kind === 'note') continue
      const own = l[range.side] !== undefined && l[range.side]! >= range.start && l[range.side]! <= range.end
      if (own && ++marked > EXCERPT_MAX_MARKED) {
        more++
        continue
      }
      rows.push({ kind: l.kind, old: l.old, new: l.new, text: l.text, marked: own })
    }
    return { ...base, rows, more, exact: true }
  }
  const quote = (anchor.quote ?? '').split('\n')
  const shown = quote.slice(0, EXCERPT_MAX_MARKED)
  return {
    ...base,
    rows: shown.map((text, i) => ({ kind: 'ctx' as const, [range.side]: range.start + i, text, marked: true })),
    more: quote.length - shown.length,
    exact: false,
  }
}

/** Where a thread's code stands in the newest version: 'current' (that version holds it), 'outdated'
 *  (its file is still in the diff but the code changed, or is now ambiguous), 'absent' (its file is
 *  not in the newest diff at all, e.g. a diff of only the fix commit: not a claim that it changed).
 *  null when that cannot be said (no code, or the newest version is not at hand). */
export type ThreadFreshness = 'current' | 'outdated' | 'absent'
export function threadFreshness(anchor: Anchor, newest: { id: string; patch: Patch } | null): ThreadFreshness | null {
  const range = anchor.diff
  if (!range || anchor.kind !== 'text' || !newest) return null
  if (range.artifactId === newest.id || projectReviewAnchor(newest.patch, anchor, newest.id)) return 'current'
  const hasFile = newest.patch.files.some(f => (range.side === 'new' ? f.newPath : f.oldPath) === range.file)
  return hasFile ? 'outdated' : 'absent'
}

/** One review the person submitted: one upload of `<name>.review.json`. */
export interface SubmittedRound {
  /** The upload's artifact id. */
  id: string
  at?: string
  /** The diff version it was written on. */
  artifactId?: string
  version?: number
  /** Threads first sent in this round, in the review's order. */
  started: string[]
  /** Threads reopened in this round. */
  reopened: string[]
  /** What else the person did on earlier threads in this round, in order: replied, resolved or
   *  rejected (one entry per thing done), with when. */
  actions: { id: string; action: 'reply' | 'resolve' | 'reject'; at: string }[]
}

/** The person's submitted reviews (oldest first), each with the threads it started or reopened.
 *  A review file holds every thread so far: a thread is new in the first round that has it, and
 *  reopened in a round where it has more of the person's comments than before. */
export function submittedRounds(uploads: readonly { artifact: ArtifactInfo; review: ReviewFile | null }[]): SubmittedRound[] {
  const seen = new Set<string>()
  const comments = new Map<string, number>()
  return [...uploads]
    .sort((a, b) => (a.artifact.version ?? 0) - (b.artifact.version ?? 0))
    .map(({ artifact, review }) => {
      const started: string[] = []
      const reopened: string[] = []
      const actions: SubmittedRound['actions'] = []
      for (const r of review?.requests ?? []) {
        const count = r.comments?.length ?? 0
        if (!seen.has(r.id)) {
          seen.add(r.id)
          started.push(r.id)
        } else if (count > (comments.get(r.id) ?? 0)) {
          for (const c of (r.comments ?? []).slice(comments.get(r.id) ?? 0)) {
            if (c.action === 'reply' || c.action === 'resolve' || c.action === 'reject') actions.push({ id: r.id, action: c.action, at: c.createdAt })
            else if (!reopened.includes(r.id)) reopened.push(r.id)
          }
        }
        comments.set(r.id, count)
      }
      return {
        id: artifact.id,
        at: review?.submittedAt ?? artifact.created_at,
        artifactId: review?.file.artifactId,
        version: review?.file.artifactVersion,
        started,
        reopened,
        actions,
      }
    })
}

export interface RevisionInfo {
  artifact: ArtifactInfo
  /** Absent until (or unless) its bytes are read. */
  summary?: PatchSummary
  commits: PatchCommit[]
}

export type TimelineEntry =
  | { kind: 'revision'; key: string; at?: string; revision: RevisionInfo }
  | { kind: 'requested'; key: string; at?: string; request: ReviewRequestPayload }
  | { kind: 'round'; key: string; at?: string; round: SubmittedRound }
  | { kind: 'thread'; key: string; at?: string; id: string }
  | { kind: 'decision'; key: string; at?: string; request: ReviewRequestPayload }

const RANK: Record<TimelineEntry['kind'], number> = { revision: 0, requested: 1, round: 2, thread: 3, decision: 4 }

function time(at: string | undefined): number {
  const t = at ? Date.parse(at) : NaN
  return Number.isFinite(t) ? t : Infinity
}

/** The timeline, oldest first: each version published, each review requested, each round the person
 *  submitted (its threads go with it), each thread no round holds (one not submitted yet, or from a
 *  review whose uploads are gone), and each decision. Entries without a time go last, in order. */
export function reviewTimeline(input: {
  revisions: readonly RevisionInfo[]
  rounds: readonly SubmittedRound[]
  threads: readonly ReviewRequest[]
  requests: readonly ReviewRequestPayload[]
}): TimelineEntry[] {
  const entries: TimelineEntry[] = []
  for (const revision of input.revisions) {
    entries.push({ kind: 'revision', key: `revision:${revision.artifact.id}`, at: revision.artifact.created_at, revision })
  }
  for (const request of input.requests) {
    entries.push({ kind: 'requested', key: `requested:${request.request_id}`, at: request.created_at, request })
    if (request.state !== 'pending') {
      entries.push({ kind: 'decision', key: `decision:${request.request_id}`, at: request.decided_at, request })
    }
  }
  const inRound = new Set<string>()
  for (const round of input.rounds) {
    if (!round.started.length && !round.reopened.length && !round.actions.length) continue
    round.started.forEach(id => inRound.add(id))
    entries.push({ kind: 'round', key: `round:${round.id}`, at: round.at, round })
  }
  for (const thread of input.threads) {
    if (!inRound.has(thread.id)) entries.push({ kind: 'thread', key: `thread:${thread.id}`, at: thread.createdAt, id: thread.id })
  }
  return entries
    .map((entry, index) => ({ entry, index }))
    .sort((a, b) => time(a.entry.at) - time(b.entry.at) || RANK[a.entry.kind] - RANK[b.entry.kind] || a.index - b.index)
    .map(({ entry }) => entry)
}
