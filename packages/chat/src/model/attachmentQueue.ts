// IndexedDB stores the waiting bytes, not object URLs or executable previews.
//
// Each stored file has an owner: the browser tab that chose it (#231). Only the owner uploads,
// retries or writes it; any other tab shows it read-only ("waiting in another tab") and never
// writes its bytes. A tab is identified by a random id kept in memory and sessionStorage (so a
// reload of the same tab is still the owner), and is alive while it holds the Web Lock named
// after that id. When the owner is gone, another tab takes the file over atomically (claim).
// Every change is one IndexedDB transaction on the session's record, and an owner's updates apply
// only to a file that is still there and still its own: cleanup (sign-out, discard, a session
// that ended) deletes records whoever owns them, and nothing written afterwards brings them back.
// Records carry when they were written: stale ones (a session never reopened) are dropped.
import type {TransportFiles} from '../transport/types'
import type {AttachmentItem} from '../hooks/useAttachments'

/** A transport must identify its viewer before bytes may outlive the page. */
export function attachmentQueueKey(sessionId: string, files: TransportFiles | null): string | null {
  const scope = files?.queueScope?.()
  return scope ? `${scope}\0${sessionId}` : null
}
/** How long waiting files are kept on this device without the session being opened. */
export const QUEUE_MAX_AGE_MS = 7 * 24 * 60 * 60 * 1000
/** A stored file: the hook's item without its preview, plus the tab that owns it. */
export type StoredItem = Omit<AttachmentItem, 'preview'> & {owner?: string}
type Stored = {savedAt: number; items: StoredItem[]}

// ── tabs ────────────────────────────────────────────────────────────────────────────────────
const TAB_LOCK = 'blerg-attach-tab:'
const locks = () => (typeof navigator !== 'undefined' ? (navigator as Navigator & {locks?: LockManager}).locks : undefined)
/** Holds a tab's liveness lock until `release` is called (or the page goes). False when another
 *  open tab holds it already. Without Web Locks: true. */
export function holdTabLock(id: string, onRelease: (release: () => void) => void): Promise<boolean> {
  const lm = locks()
  if (!lm?.request) return Promise.resolve(true)
  return new Promise<boolean>(resolve => {
    void lm.request(TAB_LOCK + id, {ifAvailable: true}, lock => {
      if (!lock) { resolve(false); return undefined }
      resolve(true)
      return new Promise<void>(release => onRelease(release))
    })
  })
}
/** Whether the tab that owns a file is still open. Without Web Locks every other tab is assumed
 *  alive (nothing is ever taken over); a file with no owner (an older record) is free. */
export async function ownerAlive(owner: string | undefined, me: string): Promise<boolean> {
  if (!owner) return false
  if (owner === me) return true
  const lm = locks()
  if (!lm?.query) return true
  const held = await lm.query()
  return (held.held ?? []).some(l => l.name === TAB_LOCK + owner)
}

// ── storage ─────────────────────────────────────────────────────────────────────────────────
let operations: Promise<unknown> = Promise.resolve()
let now = () => Date.now()
/** Test seam: the clock records are stamped and aged with. */
export function setAttachmentQueueClock(clock: (() => number) | null) { now = clock ?? (() => Date.now()) }
async function database(): Promise<IDBDatabase | null> {
  if (typeof indexedDB === 'undefined') return null
  return new Promise((resolve, reject) => {
    const req = indexedDB.open('blerg-attachment-queue', 1)
    req.onupgradeneeded = () => req.result.createObjectStore('sessions')
    req.onsuccess = () => resolve(req.result)
    req.onerror = () => reject(req.error)
  })
}
function operation<T>(mode: IDBTransactionMode, run: (store: IDBObjectStore, done: (value: T) => void) => void): Promise<T | undefined> {
  const result = operations.catch(() => {}).then(async () => {
    const db = await database()
    if (!db) return undefined
    try {
      return await new Promise<T | undefined>((resolve, reject) => {
        let value: T | undefined
        const tx = db.transaction('sessions', mode)
        run(tx.objectStore('sessions'), v => { value = v })
        tx.oncomplete = () => resolve(value)
        tx.onerror = () => reject(tx.error)
        tx.onabort = () => reject(tx.error)
      })
    } finally { db.close() }
  })
  operations = result
  return result
}
function recordOf(value: unknown): Stored | undefined {
  if (Array.isArray(value)) return {savedAt: now(), items: value as StoredItem[]} // written before records were stamped
  const rec = value as Stored | undefined
  if (!rec || !Array.isArray(rec.items)) return undefined
  return now() - rec.savedAt > QUEUE_MAX_AGE_MS ? undefined : rec
}
const stored = ({preview: _preview, ...item}: AttachmentItem, owner: string): StoredItem => {void _preview; return {...item, owner}}
/** Read-modify-write of one session's record in a single transaction. */
function change<T>(sid: string, edit: (items: StoredItem[]) => {items: StoredItem[]; result: T}) {
  return operation<T>('readwrite', (store, done) => {
    const get = store.get(sid)
    get.onsuccess = () => {
      const {items, result} = edit(recordOf(get.result)?.items ?? [])
      done(result)
      if (items.length) store.put({savedAt: now(), items} satisfies Stored, sid)
      else store.delete(sid)
    }
  })
}
function deleteWhere(match: (key: string, value: unknown) => boolean) {
  return operation<void>('readwrite', store => {
    const cursor = store.openCursor()
    cursor.onsuccess = () => {
      const c = cursor.result
      if (!c) return
      if (match(String(c.key), c.value)) c.delete()
      c.continue()
    }
  })
}

// Another tab's cleanup reaches this one at once (its chats drop their chips and jobs).
type Cleanup = {type: 'reset'} | {type: 'forget'; sessionId: string}
const channel = typeof BroadcastChannel !== 'undefined' ? new BroadcastChannel('blerg-attachment-queue') : null
const cleanupListeners = new Set<(c: Cleanup) => void>()
if (channel) channel.onmessage = event => { for (const listen of cleanupListeners) listen(event.data as Cleanup) }
/** Hears cleanup done in this tab or another one. */
export function onAttachmentCleanup(listen: (c: Cleanup) => void): () => void {
  cleanupListeners.add(listen)
  return () => { cleanupListeners.delete(listen) }
}
function announceHere(c: Cleanup) { for (const listen of cleanupListeners) listen(c) }

export const attachmentQueue = {
  /** Whether this browser stores anything (IndexedDB). */
  available: () => typeof indexedDB !== 'undefined',
  read: async (sid: string): Promise<StoredItem[] | undefined> => {
    const items = await operation<StoredItem[] | undefined>('readonly', (store, done) => {
      const get = store.get(sid)
      get.onsuccess = () => done(recordOf(get.result)?.items)
    })
    return items?.length ? items : undefined
  },
  /** Stores files this tab just chose, owned by it. */
  add: (sid: string, items: AttachmentItem[], owner: string) => change(sid, cur => {
    const keys = new Set(items.map(i => i.key))
    return {items: [...cur.filter(i => !keys.has(i.key)), ...items.map(i => stored(i, owner))], result: undefined}
  }),
  /** Updates a file this tab owns. 'gone' when it is no longer stored (cleaned up or removed),
   *  'foreign' when another tab took it over; the caller then stops and writes nothing more. */
  update: (sid: string, key: string, owner: string, value: Partial<AttachmentItem>) => change<'ok' | 'gone' | 'foreign'>(sid, cur => {
    const at = cur.findIndex(i => i.key === key)
    if (at < 0) return {items: cur, result: 'gone'}
    if (cur[at]!.owner !== owner) return {items: cur, result: 'foreign'}
    const {preview: _preview, ...rest} = value; void _preview
    const next = [...cur]; next[at] = {...cur[at]!, ...rest, owner}
    return {items: next, result: 'ok'}
  }),
  /** Removes a file this tab owns (removed by the person, sent, or failed). */
  remove: (sid: string, keys: string[], owner: string) => change(sid, cur => ({items: cur.filter(i => !keys.includes(i.key) || i.owner !== owner), result: undefined})),
  /** Takes over a file whose owner is gone: only while it still names that owner. */
  claim: (sid: string, key: string, from: string | undefined, to: string) => change<StoredItem | null>(sid, cur => {
    const at = cur.findIndex(i => i.key === key)
    if (at < 0 || cur[at]!.owner !== from) return {items: cur, result: null}
    const next = [...cur]; next[at] = {...cur[at]!, owner: to}
    return {items: next, result: next[at]!}
  }),
  /** Deletes everything on this device, in every tab (sign-out). */
  clear: async () => {
    announceHere({type: 'reset'})
    await operation<void>('readwrite', store => { store.clear() })
    channel?.postMessage({type: 'reset'} satisfies Cleanup)
  },
  /** Drops a session's waiting files for every account and tab on this device (discarded). */
  forgetSession: async (sessionId: string) => {
    announceHere({type: 'forget', sessionId})
    await deleteWhere(key => key.endsWith(`\0${sessionId}`))
    channel?.postMessage({type: 'forget', sessionId} satisfies Cleanup)
  },
  /** Drops one session record (this account's) whoever owns its files: the session ended. */
  forgetSessionKey: (sid: string) => change(sid, () => ({items: [], result: undefined})),
  /** Drops records older than QUEUE_MAX_AGE_MS. */
  prune: () => deleteWhere((_key, value) => !recordOf(value)),
  /** Resolves once every operation queued so far has finished. */
  flush: () => operations.catch(() => {}).then(() => undefined),
  /** Test seam: replaces a session's record as given. */
  writeForTest: (sid: string, items: StoredItem[]) => change(sid, () => ({items, result: undefined})),
}
