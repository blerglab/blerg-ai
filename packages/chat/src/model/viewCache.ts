// viewCache: what a session's views last read (its Files list, the contents of its open file,
// artifact and commit tabs), kept in memory so coming back to the session draws them at once
// (card #281). Never persisted.
//
// - One sign-in's: entries are filed under the transport's file operations object (`files`). A
//   host makes a new transport for another account or desktop authority, so an entry read under
//   the old one is never found again; clearViewCache() also drops everything at once.
// - Bounded: file contents by total bytes (VIEW_CACHE_BYTES, least recently viewed first; a file
//   over VIEW_CACHE_ENTRY_BYTES is not kept), Files lists by session count (VIEW_CACHE_LISTS).
// - A host forgets a session that ended, was archived or discarded (forgetViewCacheSession).

/** Bytes of file contents kept in all (per page). */
export const VIEW_CACHE_BYTES = 32 << 20
/** A file larger than this is not kept. */
export const VIEW_CACHE_ENTRY_BYTES = 8 << 20
/** How many sessions' Files lists are kept. */
export const VIEW_CACHE_LISTS = 10

const scopes = new WeakMap<object, number>()
let nextScope = 1
/** Bumped by clearViewCache: a read that began before it keeps nothing. */
let epoch = 0
/** Bumped for a session when it is forgotten: a read of it that began before keeps nothing. */
const sessionGen = new Map<string, number>()
/** Sessions that ended, were archived or discarded: nothing more is kept for them. */
const retired = new Set<string>()
/** Orders keepers: a slot keeps only a value asked for after the one it holds (#250's rule). */
let tickets = 0
const written = new Map<string, number>()
/** Whether a keeper made at (epoch, session generation, ticket) may still write `key`. */
function admit(key: string, sessionId: string, at: number, gen: number, ticket: number): boolean {
  if (at !== epoch || gen !== (sessionGen.get(sessionId) ?? 0) || retired.has(sessionId)) return false
  if (ticket < (written.get(key) ?? 0)) return false
  written.set(key, ticket)
  return true
}
function scopeOf(files: object): number {
  let id = scopes.get(files)
  if (!id) { id = nextScope++; scopes.set(files, id) }
  return id
}

interface Entry { session: string; bytes: number; value: unknown }
const contents = new Map<string, Entry>()
let total = 0
const lists = new Map<string, { session: string; value: unknown }>()

const contentKey = (files: object, sessionId: string, identity: string) => `${scopeOf(files)}\u0000${sessionId}\u0000${identity}`
const listKey = (files: object, sessionId: string) => `${scopeOf(files)}\u0000${sessionId}`

function drop(key: string) {
  const e = contents.get(key)
  if (!e) return
  total -= e.bytes
  contents.delete(key)
}

/** The kept contents of a file view, if any; counts as a view (least recently viewed goes first). */
export function peekContent<T>(files: object, sessionId: string, identity: string): T | undefined {
  const key = contentKey(files, sessionId, identity)
  const e = contents.get(key)
  if (!e) return undefined
  contents.delete(key); contents.set(key, e)
  return e.value as T
}

/** A keeper for contents about to be read: what it is given is kept only if nothing was cleared
 *  since it was made. `bytes`: what the value holds (a file over VIEW_CACHE_ENTRY_BYTES is not kept). */
export function contentKeeper<T>(files: object, sessionId: string, identity: string): (value: T, bytes: number) => void {
  const at = epoch, gen = sessionGen.get(sessionId) ?? 0, ticket = ++tickets
  return (value, bytes) => {
    const key = contentKey(files, sessionId, identity)
    if (!admit(key, sessionId, at, gen, ticket)) return
    drop(key)
    if (bytes > VIEW_CACHE_ENTRY_BYTES) return
    contents.set(key, { session: sessionId, bytes, value })
    total += bytes
    while (total > VIEW_CACHE_BYTES) drop(contents.keys().next().value!)
  }
}

/** The kept Files list of a session, if any. */
export function peekList<T>(files: object, sessionId: string): T | undefined {
  const key = listKey(files, sessionId)
  const e = lists.get(key)
  if (!e) return undefined
  lists.delete(key); lists.set(key, e)
  return e.value as T
}

/** A keeper for a Files list about to be read (see contentKeeper). */
export function listKeeper<T>(files: object, sessionId: string): (value: T) => void {
  const at = epoch, gen = sessionGen.get(sessionId) ?? 0, ticket = ++tickets
  return value => {
    const key = `list\u0000${listKey(files, sessionId)}`
    if (!admit(key, sessionId, at, gen, ticket)) return
    lists.delete(listKey(files, sessionId))
    lists.set(listKey(files, sessionId), { session: sessionId, value })
    while (lists.size > VIEW_CACHE_LISTS) lists.delete(lists.keys().next().value!)
  }
}
/** Forgets everything kept for one session (it was evicted; with `ended`, it ended, was archived or
 *  discarded, and nothing more is kept for it). A read of it that began before keeps nothing. */
export function forgetViewCacheSession(sessionId: string, opts: { ended?: boolean } = {}): void {
  sessionGen.set(sessionId, (sessionGen.get(sessionId) ?? 0) + 1)
  if (opts.ended) retired.add(sessionId)
  for (const [key, e] of [...contents]) if (e.session === sessionId) drop(key)
  for (const [key, e] of [...lists]) if (e.session === sessionId) lists.delete(key)
}

/** Forgets everything (sign-out, another account or desktop authority). */
export function clearViewCache(): void {
  contents.clear()
  lists.clear()
  written.clear()
  retired.clear()
  total = 0
  epoch++
}

/** Bytes of file contents held now (tests). */
export function viewCacheBytes(): number {
  return total
}
