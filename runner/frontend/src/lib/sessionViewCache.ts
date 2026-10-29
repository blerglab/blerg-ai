// sessionViewCache: what the explorer and the session panel last showed for a session, kept in
// memory so switching back to it draws at once (card #250); each part then reads again in the
// background. Never localStorage, never file contents.
//
// - One person's: every read and write names the signed-in account, and a different account (or an
//   identity or desktop scope change) forgets everything at once.
// - No late writers: every clear (another account, an identity or desktop scope change) moves the
//   cache to a new epoch, and a value is kept only under the epoch, account and order it was asked
//   for in (viewWriter): a reply that started before a clear, or before a newer read of the same
//   session and slot, is dropped.
// - Bounded: the last VIEW_CACHE_SESSIONS sessions viewed (least recently used goes first); the
//   explorer keeps at most EXPLORER_PAGE_CAP response pages per session (capPages).
// - Only sessions that can still change: the session store tells the cache which sessions are
//   live (syncViewCacheSessions); one that ended, was archived or is gone (discarded) is dropped,
//   and nothing is kept for a session the store does not list as live.
//
// The module imports nothing, so the test setup can clear it without loading the app.

/** How many sessions' views are kept. */
export const VIEW_CACHE_SESSIONS = 10
/** How many response pages (each listing read, Load more included) the explorer keeps per session. */
export const EXPLORER_PAGE_CAP = 200

/** How many response pages a listing holds. */
export const pagesIn = (page: { loaded?: number }) => Math.max(1, page.loaded ?? 1)
/** Response pages held in all. */
export const pageTotal = (pages: Record<string, { loaded?: number }>) => Object.values(pages).reduce((n, p) => n + pagesIn(p), 0)

/** At most `cap` response pages (EXPLORER_PAGE_CAP): the listings named in `keep` first (the root,
 *  where the tree is focused, the open folders, in tree order), then the others, most recently read
 *  first (`pages` is in read order). A listing that does not fit is left out whole. */
export function capPages<P extends { loaded?: number }>(pages: Record<string, P>, keep: readonly string[], cap = EXPLORER_PAGE_CAP): Record<string, P> {
  if (pageTotal(pages) <= cap) return pages
  const order = [...new Set([...keep.filter(p => p in pages), ...Object.keys(pages).reverse()])]
  const kept: Record<string, P> = {}
  let used = 0
  for (const path of order) {
    const n = pagesIn(pages[path])
    if (used + n > cap) continue
    kept[path] = pages[path]
    used += n
  }
  return kept
}

type Slots = Map<string, unknown>

let heldFor = ''
const entries = new Map<string, Slots>()
/** Moves on at every clear: a writer made before it keeps nothing. */
let epoch = 0
/** Orders writers: a slot keeps only a value asked for after the one it holds. */
let tickets = 0
const written = new Map<string, number>()
/** The sessions that may be cached; null until the session store has told us (then nothing is kept). */
let liveIds: ReadonlySet<string> | null = null

function forAccount(account: string): boolean {
  if (!account) return false
  if (account !== heldFor) {
    if (heldFor) forget()
    heldFor = account
  }
  return true
}

/** The slot's value for the account's session, or undefined. Reading counts as a view (LRU). */
export function readView<T>(account: string, sessionId: string, slot: string): T | undefined {
  if (!forAccount(account) || !liveIds?.has(sessionId)) return undefined
  const slots = entries.get(sessionId)
  if (!slots) return undefined
  entries.delete(sessionId)
  entries.set(sessionId, slots)
  return slots.get(slot) as T | undefined
}

/** The cache's epoch now: pass it to writeView to keep only while nothing was cleared since. */
export function viewEpoch(): number {
  return epoch
}

/** A keeper for a value about to be read, made before the read starts: bound to the account it is
 *  read under, the cache's epoch and an order ticket. The value is kept only if nothing was cleared
 *  since (no other account, identity or desktop scope), the session is live, and no read of the same
 *  session and slot asked later has been kept already. Returns whether it was kept. */
export function viewWriter<T>(account: string, sessionId: string, slot: string): (value: T) => boolean {
  const at = epoch
  const ticket = ++tickets
  return value => keep(account, at, ticket, sessionId, slot, value)
}

/** Keeps the slot's value now, if the cache is still at epoch `at` (default: now). */
export function writeView<T>(account: string, sessionId: string, slot: string, value: T, at = epoch): boolean {
  return keep(account, at, ++tickets, sessionId, slot, value)
}

function keep(account: string, at: number, ticket: number, sessionId: string, slot: string, value: unknown): boolean {
  if (at !== epoch || (heldFor && account !== heldFor)) return false
  if (!forAccount(account) || !liveIds?.has(sessionId)) return false
  const key = `${sessionId}\u0000${slot}`
  if (ticket < (written.get(key) ?? 0)) return false
  written.set(key, ticket)
  const slots = entries.get(sessionId) ?? new Map<string, unknown>()
  slots.set(slot, value)
  entries.delete(sessionId)
  entries.set(sessionId, slots)
  while (entries.size > VIEW_CACHE_SESSIONS) entries.delete(entries.keys().next().value!)
  return true
}

function forget(): void {
  entries.clear()
  written.clear()
  epoch++
}

/** Forgets one slot of a session, or the whole session when no slot is named. */
export function dropView(sessionId: string, slot?: string): void {
  if (slot === undefined) { entries.delete(sessionId); return }
  const slots = entries.get(sessionId)
  slots?.delete(slot)
  if (slots && slots.size === 0) entries.delete(sessionId)
}

/** Forgets everything (an identity change, sign-out, tests). */
export function clearViews(): void {
  forget()
  heldFor = ''
}

/** The session ids held now, least recently viewed first (tests). */
export function cachedViewSessions(): string[] {
  return [...entries.keys()]
}

interface SessionLike { id: string; status?: string; archived_at?: string | null }
const ENDED = new Set(['stopped', 'ended', 'error'])

/** The session store's list changed: only sessions still listed and not ended or archived stay. */
export function syncViewCacheSessions(sessions: readonly SessionLike[]): void {
  const live = new Set<string>()
  for (const s of sessions) if (!s.archived_at && !ENDED.has(s.status ?? '')) live.add(s.id)
  liveIds = live
  for (const id of [...entries.keys()]) if (!live.has(id)) entries.delete(id)
}

/** Tests: forget which sessions are live too. */
export function resetViewCacheForTest(): void {
  clearViews()
  liveIds = null
}

if (typeof window !== 'undefined') {
  window.addEventListener('blerg-identity-changed', clearViews)
  // Another desktop authority: what was read under the old one is not shown.
  window.addEventListener('blerg-desktop-scope-changed', clearViews)
}
