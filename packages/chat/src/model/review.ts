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
}

export interface ReviewComment {
  text: string
  createdAt: string
  revision: number
}

export interface ReviewRequest {
  id: string
  anchor: Anchor
  text: string
  status: 'open' | 'done' | 'declined'
  /** Incremented when a person reopens a thread; older answers cannot resolve it. */
  revision?: number
  suggestion?: {original: string; replacement: string}
  replies?: ReviewReply[]
  /** The person’s explanation for each reopened revision. */
  comments?: ReviewComment[]
  /** The session's one line. */
  reply?: string
  createdAt: string
}

export interface ReviewFile {
  version: 1
  file: { name: string; artifactId: string; artifactVersion?: number }
  requests: ReviewRequest[]
  /** In-browser edits of a markdown source: the edited copy's artifact id, and the diff. */
  edit?: { artifactId: string; diff: string }
  submittedAt?: string
}

const STATUSES: ReadonlySet<string> = new Set(['open', 'done', 'declined'])
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

function parseRequest(v: unknown): ReviewRequest | null {
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
      if (!isRecord(comment) || !isStr(comment.text) || !comment.text.trim() || comment.text.length > 32768 ||
        !isStr(comment.createdAt) || !Number.isFinite(Date.parse(comment.createdAt)) ||
        !positiveInt(comment.revision) || comment.revision > (r.revision ?? 0)) return null
      comments.push({text: comment.text, createdAt: comment.createdAt, revision: comment.revision})
    }
    r.comments = comments
  }
  if (v.replies !== undefined) {
    if (!Array.isArray(v.replies) || v.replies.length > 100) return null
    const replies: ReviewReply[] = []
    for (const reply of v.replies) {
      if (!isRecord(reply) || !isStr(reply.status) || !['done', 'declined'].includes(reply.status) || !isStr(reply.text) || !isStr(reply.createdAt) || !Number.isFinite(Date.parse(reply.createdAt))) return null
      if (reply.revision !== undefined && (!Number.isSafeInteger(reply.revision) || Number(reply.revision) < 0 || Number(reply.revision) > 1_000_000)) return null
      replies.push({status: reply.status as ReviewReply['status'], text: reply.text, createdAt: reply.createdAt,
        ...(reply.revision !== undefined ? {revision: Number(reply.revision)} : {})})
    }
    r.replies = replies
  }
  if (v.reply !== undefined) r.reply = v.reply
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
  for (const item of v.requests) {
    const r = parseRequest(item)
    if (!r) return null
    requests.push(r)
  }
  const out: ReviewFile = { version: 1, file: { name: f.name, artifactId: f.artifactId }, requests }
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

/** The person's file is the base (its requests, order, text, anchors, edit); for each request the
 *  agent's file also has, its status and reply win. Requests only the agent's file has are
 *  appended. Either side null: the other; both null: null. Neither input is changed. */
export function mergeReviews(person: ReviewFile | null, agent: ReviewFile | null): ReviewFile | null {
  if (!person) return agent
  if (!agent) return person
  const replies = new Map(agent.requests.map(r => [r.id, r]))
  const requests = person.requests.map(r => {
    const a = replies.get(r.id)
    if (!a) return r
    const merged: ReviewRequest = { ...r }
    if ((a.revision ?? 0) >= (r.revision ?? 0)) {
      merged.status = a.status
      if (a.revision !== undefined) merged.revision = a.revision
    }
    const comments = [...(r.comments ?? []), ...(a.comments ?? [])].filter(comment => comment.revision <= (merged.revision ?? 0))
    if (comments.length) merged.comments = [...new Map(comments.map(comment => [JSON.stringify([comment.revision, comment.createdAt, comment.text]), comment])).values()].sort((left, right) => left.revision - right.revision).slice(-100)
    const history = [...(r.replies ?? []), ...(a.replies ?? [])]
    if (history.length) merged.replies = [...new Map(history.map(reply => [JSON.stringify(reply), reply])).values()].slice(-100)
    if ((a.revision ?? 0) >= (r.revision ?? 0)) {
      if (a.reply !== undefined) merged.reply = a.reply
      else delete merged.reply
    }
    return merged
  })
  const seen = new Set(person.requests.map(r => r.id))
  for (const r of agent.requests) if (!seen.has(r.id)) requests.push(r)
  return { ...person, requests }
}
/** Reopen a settled thread without losing its answer or allowing an older answer to resolve it. */
export function reopenReviewRequest(request: ReviewRequest, explanation: string): ReviewRequest {
  const text = explanation.trim()
  if (!text || text.length > 32768 || request.status === 'open' || (request.revision ?? 0) >= 1_000_000) return request
  const replies = [...(request.replies ?? [])]
  if (request.reply && !replies.some(reply => reply.text === request.reply && reply.status === request.status && (reply.revision ?? 0) === (request.revision ?? 0))) {
    replies.push({
      text: request.reply,
      status: request.status,
      createdAt: Number.isFinite(Date.parse(request.createdAt)) ? request.createdAt : new Date().toISOString(),
      revision: request.revision ?? 0,
    })
  }
  return {
    ...request,
    status: 'open',
    revision: (request.revision ?? 0) + 1,
    comments: [...(request.comments ?? []), {text, createdAt: new Date().toISOString(), revision: (request.revision ?? 0) + 1}].slice(-100),
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
    replies.push({text: request.reply, status, createdAt: request.createdAt, revision: request.revision ?? 0})
  }
  return replies.slice(-100)
}
