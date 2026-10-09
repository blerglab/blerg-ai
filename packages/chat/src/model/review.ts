// The review of one published file: requests anchored to places in it, the session's replies, an
// edited copy. Persisted as `<name>.review.json` beside the file — the person's upload is the
// base, the session's publish of the same name carries status and reply per request
// (docs/design/feedback-tools.md, "One model for both").

/** Where a request points: a passage (quote, heading, occurrence, page, line) or an image area. */
export interface Anchor {
  kind: 'text' | 'region'
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

export interface ReviewRequest {
  id: string
  anchor: Anchor
  text: string
  status: 'open' | 'done' | 'declined'
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
const KINDS: ReadonlySet<string> = new Set(['text', 'region'])

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
  const a: Anchor = { kind: v.kind as Anchor['kind'] }
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
    const merged: ReviewRequest = { ...r, status: a.status }
    if (a.reply !== undefined) merged.reply = a.reply
    else delete merged.reply
    return merged
  })
  const seen = new Set(person.requests.map(r => r.id))
  for (const r of agent.requests) if (!seen.has(r.id)) requests.push(r)
  return { ...person, requests }
}
