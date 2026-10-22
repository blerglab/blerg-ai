// The review of one published file: requests anchored to places in it, the session's replies, an
// edited copy. Persisted as `<name>.review.json` beside the file — the person's upload is the
// base, the session's publish of the same name carries status and reply per request
// (docs/design/feedback-tools.md, "One model for both").

/** A position in one immutable published patch; indices and lines are 1-based. */
export interface DiffAnchor {
  file: string
  side: 'old' | 'new'
  start: number
  end: number
  hunk: number
  fileIndex: number
  commit?: string
  /** Binds a saved request to its source artifact, across later versions. */
  artifactId?: string
}

/** Where a request points: a passage (quote, heading, occurrence, page, line) or an image area. */
export interface Anchor {
  kind: 'text' | 'region' | 'general'
  /** Document provenance and a durable tombstone; never silently reattach a lost passage. */
  sourceArtifactId?: string
  unavailable?: boolean
  diff?: DiffAnchor
  quote?: string
  heading?: string
  /** PDF only, 1-based. */
  page?: number
  /** 1-based, among equal quotes on the page / in the file. */
  occurrence?: number
  /** Best-effort line in the markdown source. */
  line?: number
  /** An image area, in fractions of the image's width and height. */
  rect?: { x: number; y: number; w: number; h: number }
  /** The number drawn on the image. */
  pin?: number
}

export interface ReviewReply {
  status: 'done' | 'declined'
  text: string
  createdAt: string
  revision?: number
  /** Carried over from an older peer's one-line `reply`, whose time was never recorded:
   *  `createdAt` is a placeholder and no time is shown for it. */
  timeUnknown?: true
}

/** What a person's comment did to its thread. Absent: an older browser's reopen explanation. */
export type ReviewCommentAction = 'reply' | 'reopen' | 'resolve' | 'reject'

/** Something the person wrote on a thread. Each one is a new revision of the thread: whatever the
 *  agent last said about an older revision cannot change the thread's status any more. */
export interface ReviewComment {
  /** May be empty for a resolve or reject without a note. */
  text: string
  createdAt: string
  revision: number
  action?: ReviewCommentAction
}

export interface ReviewRequest {
  id: string
  anchor: Anchor
  text: string
  status: 'open' | 'done' | 'declined'
  /** Incremented by each thing the person does on the thread (reply, resolve, reject, reopen):
   *  the thread's clock. An agent answer at an older revision cannot change its status. */
  revision?: number
  /** Who set the current resolved/rejected status. A person's resolve or reject stands until the
   *  person changes it: an agent answer at the same revision is kept as history only. */
  settledBy?: 'person' | 'agent'
  /** The agent's reopens of a thread that was resolved or rejected (to ask for clarification, say),
   *  each with its reason and the revision it raised the thread to. The agent's own words. */
  agentReopens?: AgentReopen[]
  suggestion?: {original: string; replacement: string}
  replies?: ReviewReply[]
  /** What the person wrote on the thread: replies, and the note with each resolve, reject or reopen. */
  comments?: ReviewComment[]
  /** The session's one line. */
  reply?: string
  createdAt: string
}

/** The agent reopened a settled thread: its one line, when, and the revision it raised it to. */
export interface AgentReopen {
  text: string
  createdAt: string
  revision: number
}

export interface ReviewFile {
  version: 1
  file: { name: string; artifactId: string; artifactVersion?: number }
  requests: ReviewRequest[]
  /** In-browser edits of a markdown source: the edited copy's artifact id, and the diff. */
  edit?: { artifactId: string; diff: string }
  submittedAt?: string
  /** Entries left out when the file was read because they broke the shared rule (an agent reopen
   *  with an empty, invisible or too long reason, or a bad revision): the rest of the file stands. */
  ignored?: number
}

// ─── the agent-reopen rule, shared with blerg-runner and the runner (Go) ──────
// One definition in three languages, tested against the same vectors
// (src/model/testdata/agent-reopen-vectors.json): change all three together.

/** At most this many characters (Unicode code points) in an agent's reopen reason. */
export const AGENT_REOPEN_MAX = 2000
/** The highest revision a thread can have. */
export const REVISION_MAX = 1_000_000
// Cn (unassigned) counts as invisible too: a reader on newer Unicode tables may call a character
// Cf that an older writer does not know, and the writer must never be the more lenient one.
const INVISIBLE = /[\p{Cc}\p{Cf}\p{Cn}\p{Z}]/gu

/** A reopen reason: a string of at most AGENT_REOPEN_MAX code points with something left after
 *  removing control (Cc), format (Cf), unassigned (Cn) and separator/space (Z) characters. */
export function validReopenText(text: unknown): text is string {
  return typeof text === 'string' && [...text].length <= AGENT_REOPEN_MAX && text.replace(INVISIBLE, '') !== ''
}

/** A revision: a JSON number with an integral value from 1 to REVISION_MAX (3.0 is 3; 3.5, "3",
 *  true are not revisions). */
export function validRevision(v: unknown): v is number {
  return typeof v === 'number' && Number.isInteger(v) && v >= 1 && v <= REVISION_MAX
}

/** A reopen record that may stand in a thread at `threadRevision`: a valid reason and revision, a
 *  string time, and a revision no higher than the thread's own. */
export function validReopenRecord(x: unknown, threadRevision: number): x is AgentReopen {
  return isRecord(x) && validReopenText(x.text) && isStr(x.createdAt) && validRevision(x.revision) && x.revision <= threadRevision
}

const STATUSES: ReadonlySet<string> = new Set(['open', 'done', 'declined'])
const COMMENT_ACTIONS: ReadonlySet<string> = new Set(['reply', 'reopen', 'resolve', 'reject'])
const KINDS: ReadonlySet<string> = new Set(['text', 'region', 'general'])

/** 8 base36 characters from the browser's randomness. */
export function newRequestId(): string {
  const bytes = new Uint8Array(8)
  crypto.getRandomValues(bytes)
  let out = ''
  for (const b of bytes) out += (b % 36).toString(36)
  return out
}

export function reviewFileName(name: string): string {
  return `${name}.review.json`
}

export function emptyReview(file: ReviewFile['file']): ReviewFile {
  return { version: 1, file: { ...file }, requests: [] }
}

// ─── parsing ──────────────────────────────────────────────────────────────────

const isRecord = (v: unknown): v is Record<string, unknown> => typeof v === 'object' && v !== null && !Array.isArray(v)
const isStr = (v: unknown): v is string => typeof v === 'string'
const isNum = (v: unknown): v is number => typeof v === 'number' && Number.isFinite(v)
const optional = <T>(v: unknown, ok: (x: unknown) => x is T): v is T | undefined => v === undefined || ok(v)

const hasControl = (text: string) => [...text].some(char => { const n = char.charCodeAt(0); return n < 32 || (n >= 127 && n <= 159) })
const positiveInt = (v: unknown): v is number => typeof v === 'number' && Number.isSafeInteger(v) && v > 0

function parseDiffAnchor(v: unknown): DiffAnchor | null {
  if (!isRecord(v) || !isStr(v.file) || !v.file.trim() || v.file.length > 4096 || hasControl(v.file)) return null
  if (v.side !== 'old' && v.side !== 'new') return null
  if (!positiveInt(v.start) || !positiveInt(v.end) || v.end < v.start || v.end - v.start >= 200 || !positiveInt(v.hunk) || !positiveInt(v.fileIndex)) return null
  if (v.commit !== undefined && (!isStr(v.commit) || !/^(?:[a-f0-9]{40}|[a-f0-9]{64})$/i.test(v.commit))) return null
  if (v.artifactId !== undefined && (!isStr(v.artifactId) || !v.artifactId.trim() || v.artifactId.length > 200 || hasControl(v.artifactId))) return null
  return {file:v.file, side:v.side, start:v.start, end:v.end, hunk:v.hunk, fileIndex:v.fileIndex,
    ...(typeof v.commit === 'string' ? {commit:v.commit.toLowerCase()} : {}),
    ...(typeof v.artifactId === 'string' ? {artifactId:v.artifactId} : {})}
}

/** Plain descriptive text only: a diff path never becomes a URL or a filesystem operation. */
export function diffAnchorLabel(a: DiffAnchor): string {
  const lines = a.start === a.end ? `line ${a.start}` : `lines ${a.start}–${a.end}`
  return `${a.file}, ${a.side} ${lines}, hunk ${a.hunk}, file ${a.fileIndex}${a.commit ? `, commit ${a.commit.slice(0, 12)}` : ''}`
}

function parseAnchor(v: unknown): Anchor | null {
  if (!isRecord(v) || !isStr(v.kind) || !KINDS.has(v.kind)) return null
  if (!optional(v.quote, isStr) || !optional(v.heading, isStr)) return null
  if (!optional(v.page, isNum) || !optional(v.occurrence, isNum) || !optional(v.line, isNum) || !optional(v.pin, isNum)) return null
  let rect: Anchor['rect']
  if (v.rect !== undefined) {
    const r = v.rect
    if (!isRecord(r) || !isNum(r.x) || !isNum(r.y) || !isNum(r.w) || !isNum(r.h)) return null
    rect = { x: r.x, y: r.y, w: r.w, h: r.h }
  }
  if (v.sourceArtifactId !== undefined && (v.kind !== 'text' || v.diff !== undefined || !isStr(v.sourceArtifactId) || !v.sourceArtifactId.trim() || v.sourceArtifactId.length > 200 || hasControl(v.sourceArtifactId))) return null
  if (v.unavailable !== undefined && (v.kind !== 'text' || v.diff !== undefined || typeof v.unavailable !== 'boolean')) return null
  if (v.kind === 'general' && ['diff', 'quote', 'heading', 'page', 'line', 'rect', 'pin', 'occurrence'].some(key => v[key] !== undefined)) return null
  const a: Anchor = { kind: v.kind as Anchor['kind'] }
  if (typeof v.sourceArtifactId === 'string') a.sourceArtifactId = v.sourceArtifactId
  if (typeof v.unavailable === 'boolean') a.unavailable = v.unavailable
  if (v.diff !== undefined) {
    const diff = parseDiffAnchor(v.diff)
    if (v.kind !== 'text' || !diff || (typeof v.quote === 'string' && v.quote.length > 32768)) return null
    a.diff = diff
  }
  if (v.quote !== undefined) a.quote = v.quote
  if (v.heading !== undefined) a.heading = v.heading
  if (v.page !== undefined) a.page = v.page
  if (v.occurrence !== undefined) a.occurrence = v.occurrence
  if (v.line !== undefined) a.line = v.line
  if (rect) a.rect = rect
  if (v.pin !== undefined) a.pin = v.pin
  return a
}

function parseRequest(v: unknown, ignored: {count: number} = {count: 0}): ReviewRequest | null {
  if (!isRecord(v) || !isStr(v.id) || !isStr(v.text) || !isStr(v.createdAt)) return null
  if (!isStr(v.status) || !STATUSES.has(v.status) || !optional(v.reply, isStr)) return null
  const anchor = parseAnchor(v.anchor)
  if (!anchor) return null
  const r: ReviewRequest = { id: v.id, anchor, text: v.text, status: v.status as ReviewRequest['status'], createdAt: v.createdAt }
  if (v.revision !== undefined) {
    if (!Number.isSafeInteger(v.revision) || Number(v.revision) < 0 || Number(v.revision) > 1_000_000) return null
    r.revision = Number(v.revision)
  }
  if (v.suggestion !== undefined) {
    const suggestion = v.suggestion
    if (!isRecord(suggestion) || !isStr(suggestion.original) || !isStr(suggestion.replacement) ||
      anchor.kind !== 'text' || anchor.diff?.side !== 'new' || anchor.quote !== suggestion.original ||
      suggestion.original.length > 32768 || suggestion.replacement.length > 32768 ||
      suggestion.original.includes('\0') || suggestion.replacement.includes('\0')) return null
    r.suggestion = {original: suggestion.original, replacement: suggestion.replacement}
  }
  if (v.comments !== undefined) {
    if (!Array.isArray(v.comments) || v.comments.length > 100) return null
    const comments: ReviewComment[] = []
    for (const comment of v.comments) {
      if (!isRecord(comment) || !isStr(comment.text) || comment.text.length > 32768 ||
        !isStr(comment.createdAt) || !Number.isFinite(Date.parse(comment.createdAt)) ||
        !positiveInt(comment.revision) || comment.revision > (r.revision ?? 0)) return null
      if (comment.action !== undefined && !COMMENT_ACTIONS.has(comment.action as string)) return null
      const settles = comment.action === 'resolve' || comment.action === 'reject'
      // Only a resolve or reject may come without words.
      if (!settles && !comment.text.trim()) return null
      comments.push({text: comment.text, createdAt: comment.createdAt, revision: comment.revision,
        ...(comment.action !== undefined ? {action: comment.action as ReviewCommentAction} : {})})
    }
    r.comments = comments
  }
  if (v.replies !== undefined) {
    if (!Array.isArray(v.replies) || v.replies.length > 100) return null
    const replies: ReviewReply[] = []
    for (const reply of v.replies) {
      if (!isRecord(reply) || !isStr(reply.status) || !['done', 'declined'].includes(reply.status) || !isStr(reply.text) || !isStr(reply.createdAt) || !Number.isFinite(Date.parse(reply.createdAt))) return null
      if (reply.revision !== undefined && (!Number.isSafeInteger(reply.revision) || Number(reply.revision) < 0 || Number(reply.revision) > 1_000_000)) return null
      if (reply.timeUnknown !== undefined && reply.timeUnknown !== true) return null
      replies.push({status: reply.status as ReviewReply['status'], text: reply.text, createdAt: reply.createdAt,
        ...(reply.revision !== undefined ? {revision: Number(reply.revision)} : {}),
        ...(reply.timeUnknown === true ? {timeUnknown: true as const} : {})})
    }
    r.replies = replies
  }
  if (v.reply !== undefined) r.reply = v.reply
  if (v.agentReopens !== undefined) {
    // One bad reopen entry is left out (and counted), never the whole file: an agent's malformed
    // reopen must not cost the person every other answer in the review.
    const list = Array.isArray(v.agentReopens) ? v.agentReopens : []
    const reopens: AgentReopen[] = []
    for (const x of list.slice(-100)) {
      if (validReopenRecord(x, r.revision ?? 0)) reopens.push({text: x.text, createdAt: x.createdAt, revision: x.revision})
      else if (isRecord(x) && isStr(x.createdAt) && validRevision(x.revision) && x.revision <= (r.revision ?? 0)) {
        // A reopen whose reason cannot be shown still happened at its revision: keep it without the
        // reason, so the thread still waits for the person (it must not let the agent settle it).
        reopens.push({text: '', createdAt: x.createdAt, revision: x.revision})
        if (x.text !== '') ignored.count++
      } else ignored.count++
    }
    ignored.count += Array.isArray(v.agentReopens) ? Math.max(0, list.length - 100) : 1
    if (reopens.length) r.agentReopens = reopens
  }
  if (v.settledBy !== undefined) {
    if (v.settledBy !== 'person' && v.settledBy !== 'agent') return null
    if (r.status !== 'open') r.settledBy = v.settledBy
  }
  return r
}

/** A review file from its JSON text; null for anything that is not exactly a version-1 review
 *  (never throws). Unknown fields are dropped. */
export function parseReviewFile(text: string): ReviewFile | null {
  let v: unknown
  try {
    v = JSON.parse(text)
  } catch {
    return null
  }
  if (!isRecord(v) || v.version !== 1 || !Array.isArray(v.requests)) return null
  const f = v.file
  if (!isRecord(f) || !isStr(f.name) || !isStr(f.artifactId) || !optional(f.artifactVersion, isNum)) return null
  const requests: ReviewRequest[] = []
  const ignored = {count: 0}
  for (const item of v.requests) {
    const r = parseRequest(item, ignored)
    if (!r) return null
    requests.push(r)
  }
  const out: ReviewFile = { version: 1, file: { name: f.name, artifactId: f.artifactId }, requests }
  if (ignored.count) out.ignored = ignored.count
  if (f.artifactVersion !== undefined) out.file.artifactVersion = f.artifactVersion
  if (v.edit !== undefined) {
    const e = v.edit
    if (!isRecord(e) || !isStr(e.artifactId) || !isStr(e.diff)) return null
    out.edit = { artifactId: e.artifactId, diff: e.diff }
  }
  if (v.submittedAt !== undefined) {
    if (!isStr(v.submittedAt)) return null
    out.submittedAt = v.submittedAt
  }
  return out
}

// ─── merging ──────────────────────────────────────────────────────────────────

/** The person's file is the base (its requests, order, text, anchors, edit, comments), and the
 *  only source of requests: a thread only the agent's copy has is dropped. Per thread,
 *  the revision is the clock and only the person raises it. The agent's copy is trusted only for
 *  the agent's own fields: its status (and one-line reply) win at the person's current revision,
 *  unless the person resolved or rejected the thread themselves, and its answer history is kept, so
 *  an agent's reply after a person's resolve is still shown. Anything person-only in the agent's
 *  copy is ignored: its comments, a `settledBy: "person"` claim, and a revision above the person's,
 *  with one exception: the agent may reopen a resolved or rejected thread (`agentReopenApplies`),
 *  which raises it one revision, to open, with the agent's reason kept as the agent's.
 *  Either side null: the other (the agent's alone without person fields); both null: null. Neither
 *  input is changed. */
export function mergeReviews(person: ReviewFile | null, agent: ReviewFile | null): ReviewFile | null {
  if (!person) return agent ? { ...agent, requests: agent.requests.map(agentOnly) } : null
  if (!agent) return person
  const ignored = (person.ignored ?? 0) + (agent.ignored ?? 0)
  const replies = new Map(agent.requests.map(r => [r.id, r]))
  const requests = person.requests.map(r => {
    const a = replies.get(r.id)
    if (!a) return r
    const merged: ReviewRequest = { ...r }
    const reopened = agentReopenApplies(r, a)
    const agentWins = !reopened && agentStatusWins(r, a)
    if (reopened) {
      // The agent reopened the settled thread: one revision up, open, nobody's settle any more.
      merged.status = 'open'
      merged.revision = a.revision
      delete merged.settledBy
      delete merged.reply
    } else if (agentWins) {
      merged.status = a.status
      if (a.status === 'open') delete merged.settledBy
      else merged.settledBy = 'agent'
    }
    // The person's file keeps the reopens a merge accepted; from the agent's copy only the one that
    // applies now (no made-up or pre-planted history).
    // From the agent's copy, one record: the last valid one at the revision the reopen applied.
    const applied = reopened ? (a.agentReopens ?? []).filter(x => x.revision === a.revision && validReopenText(x.text)).slice(-1) : []
    const reopens = [...(r.agentReopens ?? []), ...applied]
      .filter(x => x.revision <= (merged.revision ?? 0))
    if (reopens.length) merged.agentReopens = [...new Map(reopens.map(x => [JSON.stringify([x.revision, x.createdAt, x.text]), x])).values()].sort((x, y) => x.revision - y.revision).slice(-100)
    else delete merged.agentReopens
    const history = [...(r.replies ?? []), ...(a.replies ?? [])]
    if (history.length) merged.replies = [...new Map(history.map(reply => [JSON.stringify(reply), reply])).values()].slice(-100)
    if (agentWins) {
      if (a.reply !== undefined) merged.reply = a.reply
      else delete merged.reply
    }
    return merged
  })
  // Threads only the agent's copy has are dropped, as blerg-runner does: every request is the
  // person's, so the agent cannot add one in their name (it would be saved in their next upload).
  return { ...person, requests, ...(ignored ? { ignored } : {}) }
}

/** A thread from the agent's copy when there is no person file: no person comments, and settled
 *  (if at all) by the agent. */
function agentOnly(r: ReviewRequest): ReviewRequest {
  const { comments: _comments, settledBy: _settledBy, ...rest } = r
  void _comments
  void _settledBy
  return r.status === 'open' ? rest : { ...rest, settledBy: 'agent' }
}
/** Whether the agent's copy of a thread sets its status: at the person's current revision only,
 *  and not over the person's own resolve or reject. */
/** Whether the agent's copy reopens a thread that is resolved or rejected (by either side): open,
 *  exactly one revision above the person's, with the agent's reopen record at that revision. The
 *  only way the agent raises a revision, and only ever to open: never to resolve or reject. */
export function agentReopenApplies(person: ReviewRequest, agent: ReviewRequest): boolean {
  const next = (person.revision ?? 0) + 1
  return person.status !== 'open' && agent.status === 'open' && next <= REVISION_MAX && (agent.revision ?? 0) === next &&
    (agent.agentReopens ?? []).some(x => x.revision === next && validReopenText(x.text))
}

/** The revision of the person's own last action on a thread: every reply, resolve, reject and
 *  reopen leaves a comment at its revision (0: none since it was asked). Submit carries a thread
 *  forward without adding one, so carrying it forward is never an action. */
export function personActionRevision(r: ReviewRequest): number {
  return Math.max(0, ...(r.comments ?? []).map(c => c.revision))
}

/** The agent reopened the thread and the person has not acted on it since: it stays open. */
export function awaitingPersonAfterReopen(r: ReviewRequest): boolean {
  const reopened = Math.max(0, ...(r.agentReopens ?? []).map(x => x.revision))
  return reopened > personActionRevision(r)
}

export function agentStatusWins(person: ReviewRequest, agent: ReviewRequest): boolean {
  // Only the person raises a thread's revision: an agent copy claiming a higher one is ignored,
  // and one at an older revision answers something the person has since changed. A thread the
  // agent reopened waits for the person: the agent cannot settle it again until they act on it.
  return (agent.revision ?? 0) === (person.revision ?? 0) && person.settledBy !== 'person' && !awaitingPersonAfterReopen(person)
}

/** The agent's current one-line answer, moved into the history (it belongs to the revision before
 *  the person's next action), and the `reply` field dropped. */
function archiveReply(request: ReviewRequest): {replies?: ReviewReply[]} {
  const replies = [...(request.replies ?? [])]
  if (request.reply && request.status !== 'open' && request.settledBy !== 'person' &&
    !replies.some(reply => reply.text === request.reply && reply.status === request.status && (reply.revision ?? 0) === (request.revision ?? 0))) {
    replies.push({
      text: request.reply,
      status: request.status,
      createdAt: Number.isFinite(Date.parse(request.createdAt)) ? request.createdAt : new Date().toISOString(),
      revision: request.revision ?? 0,
      timeUnknown: true,
    })
  }
  return replies.length ? {replies: replies.slice(-100)} : {}
}

/** A person's reply on a thread, as a new revision. A reply on a thread the agent resolved or
 *  rejected reopens it, as replying to a resolved conversation does on GitHub: the agent has
 *  something to answer, so it is open again everywhere (the counts, `review list`, a decision's
 *  comments). A thread the person settled themselves stays settled. The request unchanged when the
 *  text is empty, too long, or the thread is at its revision limit. */
export function replyToReviewRequest(request: ReviewRequest, text: string): ReviewRequest {
  const words = text.trim()
  if (!words || words.length > 32768 || (request.revision ?? 0) >= 1_000_000) return request
  const revision = (request.revision ?? 0) + 1
  const {reply: _reply, settledBy: _settledBy, ...rest} = request
  void _reply
  void _settledBy
  const keepsStatus = request.status === 'open' || request.settledBy === 'person'
  return {
    ...rest,
    ...(keepsStatus ? {status: request.status, ...(request.settledBy ? {settledBy: request.settledBy} : {})} : {status: 'open' as const}),
    revision,
    ...archiveReply(request),
    comments: [...(request.comments ?? []), {text: words, createdAt: new Date().toISOString(), revision, action: 'reply' as const}].slice(-100),
  }
}

/** The person resolves (done) or rejects (declined) a thread themselves, with an optional note.
 *  Their status stands until they change it, whatever the agent answered before or after. */
export function settleReviewRequest(request: ReviewRequest, status: 'done' | 'declined', note = ''): ReviewRequest {
  const words = note.trim()
  if (words.length > 32768 || (request.revision ?? 0) >= 1_000_000) return request
  if (request.status === status && request.settledBy === 'person') return request
  const revision = (request.revision ?? 0) + 1
  const {reply: _reply, ...rest} = request
  void _reply
  return {
    ...rest,
    status,
    settledBy: 'person',
    revision,
    ...archiveReply(request),
    comments: [...(request.comments ?? []), {text: words, createdAt: new Date().toISOString(), revision, action: status === 'done' ? 'resolve' as const : 'reject' as const}].slice(-100),
  }
}

/** Reopen a settled thread without losing its answer or allowing an older answer to resolve it. */
export function reopenReviewRequest(request: ReviewRequest, explanation: string): ReviewRequest {
  const text = explanation.trim()
  if (!text || text.length > 32768 || request.status === 'open' || (request.revision ?? 0) >= 1_000_000) return request
  const replies = [...(request.replies ?? [])]
  if (request.reply && request.settledBy !== 'person' && !replies.some(reply => reply.text === request.reply && reply.status === request.status && (reply.revision ?? 0) === (request.revision ?? 0))) {
    replies.push({
      text: request.reply,
      status: request.status,
      createdAt: Number.isFinite(Date.parse(request.createdAt)) ? request.createdAt : new Date().toISOString(),
      revision: request.revision ?? 0,
      timeUnknown: true,
    })
  }
  const {settledBy: _settledBy, ...rest} = request
  void _settledBy
  return {
    ...rest,
    status: 'open',
    revision: (request.revision ?? 0) + 1,
    comments: [...(request.comments ?? []), {text, createdAt: new Date().toISOString(), revision: (request.revision ?? 0) + 1, action: 'reopen' as const}].slice(-100),
    ...(replies.length ? {replies: replies.slice(-100)} : {}),
  }
}


/** Older peers may write only reply even when earlier answers already have a history. */
export function reviewReplyHistory(request: ReviewRequest): ReviewReply[] {
  const replies = [...(request.replies ?? [])]
  if (!request.reply) return replies
  // A reopened thread keeps its old reply, whose status belongs to the archived revision.
  if (request.status === 'open' && replies.length) return replies
  const status = request.status === 'declined' ? 'declined' : 'done'
  if (!replies.some(reply => reply.text === request.reply && reply.status === status &&
    (reply.revision ?? 0) === (request.revision ?? 0))) {
    replies.push({text: request.reply, status, createdAt: request.createdAt, revision: request.revision ?? 0, timeUnknown: true})
  }
  return replies.slice(-100)
}
