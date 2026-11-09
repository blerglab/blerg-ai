// A bounded, shared store for the full body a popped-out tab snapshots at open time: a message's
// text (BLG-449) and a chart or diagram's source (BLG-451) both go through this one store, hence
// "snapshot body", not "message text". Kept separate from a session's own tab layout
// (useSessionTabs), whose saved blob must never grow with a snapshot's size: an unbounded body
// inside that one localStorage entry would make its own setItem throw on a large message or
// diagram, silently stopping every tab from persisting, not just the large one. One shared index
// and one shared per-account cap across every kind of snapshot: a popped-out chart and a
// popped-out message draw against the same budget, not two separate ones.
//
// Duplicate shares a snapshot by giving its copy the same id (a message's messageId, or a chart's
// content-hash id) rather than writing a second copy: both tabs resolve the same stored body, so
// duplicating never costs storage.

/** A single snapshot body is never kept past this size. */
export const PER_SNAPSHOT_CAP = 1_000_000
/** Total kept per account; the oldest entries not in `protect` are evicted first to make room. */
export const PER_ACCOUNT_CAP = 8_000_000

interface IndexEntry {
  id: string
  session: string
  size: number
  savedAt: number
}

function indexKey(account: string): string {
  return `blerg-runner.snapshot-index.${encodeURIComponent(account)}`
}
function bodyKey(account: string, session: string, id: string): string {
  return `blerg-runner.snapshot.${encodeURIComponent(account)}.${encodeURIComponent(session)}.${encodeURIComponent(id)}`
}
function isIndexEntry(value: unknown): value is IndexEntry {
  const e = value as Partial<IndexEntry> | null
  return !!e && typeof e === 'object' && typeof e.id === 'string' && typeof e.session === 'string' && typeof e.size === 'number' && typeof e.savedAt === 'number'
}
function readIndex(account: string): IndexEntry[] {
  try {
    const parsed: unknown = JSON.parse(localStorage.getItem(indexKey(account)) ?? '[]')
    return Array.isArray(parsed) ? parsed.filter(isIndexEntry) : []
  } catch {
    return []
  }
}
function writeIndex(account: string, entries: IndexEntry[]) {
  try { localStorage.setItem(indexKey(account), JSON.stringify(entries)) } catch { /* best-effort: an index write never blocks a save */ }
}

/**
 * Saves a snapshot body, evicting the oldest entries not named in `protect` (ids of tabs
 * currently in the layout — open or not, but still referenced, so evicting one out from under
 * it would corrupt that tab on the next reload) until the account's total fits the cap. Returns
 * false — and logs why, but never throws — when the body alone exceeds the per-snapshot cap, or
 * there is no room to free without touching a protected entry: the caller then knows this
 * particular snapshot will not survive a reload, though it stays in memory for the rest of this
 * session regardless of what this function returns.
 */
export function saveSnapshot(account: string, session: string, id: string, body: string, protect: ReadonlySet<string> = new Set()): boolean {
  const size = body.length * 2 // UTF-16 code units, as JS strings and localStorage both keep them
  if (size > PER_SNAPSHOT_CAP) {
    console.warn('blerg-runner: snapshot too large to keep past this session', { session, id, size, cap: PER_SNAPSHOT_CAP })
    return false
  }
  // The index is keyed by (session, id), not id alone: a chart/diagram's id is a content hash
  // (visualSnapshotId), so the same chart opened in two different sessions shares one id. Filtering
  // or protecting by id alone would drop the other session's index entry out of bookkeeping
  // entirely (its body orphaned — uncounted, never evicted, missed by clearAccountSnapshots) and
  // let one session's protect set shield another session's entry it has no business protecting.
  const entries = readIndex(account).filter(e => !(e.id === id && e.session === session))
  entries.sort((a, b) => a.savedAt - b.savedAt)
  let total = entries.reduce((n, e) => n + e.size, 0) + size
  const kept: IndexEntry[] = []
  for (const e of entries) {
    if (total <= PER_ACCOUNT_CAP || e.session === session && protect.has(e.id)) { kept.push(e); continue }
    try { localStorage.removeItem(bodyKey(account, e.session, e.id)) } catch { /* best-effort */ }
    total -= e.size
  }
  if (total > PER_ACCOUNT_CAP) {
    console.warn('blerg-runner: no room to keep a new snapshot without evicting one still in use', { session, id, size })
    writeIndex(account, kept)
    return false
  }
  try {
    localStorage.setItem(bodyKey(account, session, id), body)
  } catch (e) {
    console.warn('blerg-runner: failed to save a snapshot', { session, id, error: String(e) })
    writeIndex(account, kept)
    return false
  }
  kept.push({ id, session, size, savedAt: Date.now() })
  writeIndex(account, kept)
  return true
}

/** The snapshot body kept for this id, or undefined when it was never saved or has since been
 *  evicted (over the cap, or forgotten at sign-out). */
export function loadSnapshot(account: string, session: string, id: string): string | undefined {
  try { return localStorage.getItem(bodyKey(account, session, id)) ?? undefined } catch { return undefined }
}

/** Forgets every snapshot kept for one account. */
export function clearAccountSnapshots(account: string): void {
  for (const e of readIndex(account)) { try { localStorage.removeItem(bodyKey(account, e.session, e.id)) } catch { /* ignore */ } }
  try { localStorage.removeItem(indexKey(account)) } catch { /* ignore */ }
}

/** Forgets every snapshot kept for every account (sign-out: the next person must start clean). */
export function clearAllSnapshots(): void {
  const keys: string[] = []
  try {
    for (let i = 0; i < localStorage.length; i++) {
      const k = localStorage.key(i)
      if (k && (k.startsWith('blerg-runner.snapshot-index.') || k.startsWith('blerg-runner.snapshot.'))) keys.push(k)
    }
  } catch { return }
  for (const k of keys) { try { localStorage.removeItem(k) } catch { /* ignore */ } }
}

if (typeof window !== 'undefined') {
  window.addEventListener('blerg-signed-out', clearAllSnapshots)
  window.addEventListener('blerg-identity-changed', clearAllSnapshots)
}
