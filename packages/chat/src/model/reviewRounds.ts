// A session's answers to a review are state, not documents: each `blerg-runner review reply`
// publishes `<file>.review.json` again, and a card per version would flood the chat with JSON. The
// transcript draws none of them; each round of answers (the publishes of one file's review between
// two of the person's messages) becomes one line at its newest publish, "11 requests answered".
import type { AgentEvent, ArtifactPayload } from '../types'
import type { ReviewFile } from './review'
import { userMessageShape } from './userMessages'

export const REVIEW_FILE_SUFFIX = '.review.json'

/** The reviewed file's name for a review-state file's name, or null for any other file. */
export function reviewedFileName(name: string): string | null {
  return name.length > REVIEW_FILE_SUFFIX.length && name.endsWith(REVIEW_FILE_SUFFIX)
    ? name.slice(0, -REVIEW_FILE_SUFFIX.length)
    : null
}

function reviewPayload(ev: AgentEvent): ArtifactPayload | null {
  if (ev.kind !== 'artifact') return null
  const p = ev.payload as Partial<ArtifactPayload> | null
  if (!p || typeof p.name !== 'string' || typeof p.id !== 'string' || p.origin === 'user') return null
  return reviewedFileName(p.name) === null ? null : (p as ArtifactPayload)
}

/** Whether an event is the session publishing a review-state file. */
export function isReviewStateEvent(ev: AgentEvent): boolean {
  return reviewPayload(ev) !== null
}

/** One round of answers to the review of one file. */
export interface ReviewRound {
  /** The reviewed file's name (report.md for report.md.review.json). */
  file: string
  /** How many times the review file was published in the round. */
  publishes: number
  /** The round's newest publish: the line sits at its event, and counts its answers. */
  latest: ArtifactPayload
  /** The session's last publish of this review before the round, when the transcript holds it. */
  baseline?: ArtifactPayload
  /** No earlier publish exists: the round's first is version 1 of the file. */
  first: boolean
}

export interface ReviewRounds {
  /** Review-state events that draw nothing: every publish of a round but its newest. */
  hidden: ReadonlySet<string>
  /** Each round, by the client_event_id of its newest publish. */
  rounds: ReadonlyMap<string, ReviewRound>
}

const NONE: ReviewRounds = { hidden: new Set(), rounds: new Map() }

/** The rounds of review answers in a transcript (oldest first). A round ends at a message the
 *  person can see (a new review submit, or any other message). */
export function reviewRounds(events: readonly AgentEvent[]): ReviewRounds {
  if (!events.some(isReviewStateEvent)) return NONE
  const hidden = new Set<string>()
  const rounds = new Map<string, ReviewRound>()
  // The open round of each file, and the last publish of each file seen so far.
  const open = new Map<string, { ev: AgentEvent; round: ReviewRound }>()
  const last = new Map<string, ArtifactPayload>()
  for (const ev of events) {
    if (ev.kind === 'user_message' && userMessageShape(ev.payload).as === 'person') {
      open.clear()
      continue
    }
    const p = reviewPayload(ev)
    if (!p) continue
    const file = reviewedFileName(p.name)!
    const current = open.get(file)
    if (current) {
      hidden.add(current.ev.client_event_id)
      rounds.delete(current.ev.client_event_id)
      current.round = { ...current.round, publishes: current.round.publishes + 1, latest: p }
      current.ev = ev
    } else {
      const baseline = last.get(file)
      open.set(file, { ev, round: { file, publishes: 1, latest: p, baseline, first: !baseline && (p.version ?? 0) === 1 } })
    }
    const now = open.get(file)!
    rounds.set(ev.client_event_id, now.round)
    last.set(file, p)
  }
  return { hidden, rounds }
}

/** How many requests a round answered: those settled in its newest review that were not settled,
 *  with the same answer, in the review before it. */
export function answeredInRound(latest: ReviewFile, baseline: ReviewFile | null): number {
  const before = new Map((baseline?.requests ?? []).map(r => [r.id, r]))
  let n = 0
  for (const r of latest.requests) {
    if (r.status === 'open') continue
    const b = before.get(r.id)
    if (b && b.status === r.status && b.reply === r.reply && (b.revision ?? 0) === (r.revision ?? 0) &&
      (b.replies?.length ?? 0) === (r.replies?.length ?? 0)) continue
    n++
  }
  return n
}
