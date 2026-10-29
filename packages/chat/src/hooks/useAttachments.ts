// Files upload immediately; local previews are bounded, inert thumbnails/excerpts, never source
// Files or resource URLs. Waiting files and unsent completed chips also survive page reloads.
//
// Across tabs (#231): a queued file is owned by the tab that chose it (model/attachmentQueue.ts).
// Only that tab uploads, retries or writes it; another tab shows it read-only ("Waiting in
// another tab") and takes it over only once the owner is gone. Uploads to one session go one at a
// time (a promise chain, and a Web Lock across tabs); under the lock the owner reads its stored
// entry again and stops if it was cleaned up or taken over. Cleanup (sign-out, discard, an ended
// session) deletes entries whoever owns them; an owner's later writes only update an entry that
// is still there, so nothing brings cleared bytes back.
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react'
import { formatLimit, MAX_ATTACHMENTS_PER_MESSAGE, MAX_ATTACHMENT_BYTES } from '../model/attachments'
import {createAttachmentPreview, type AttachmentPreview} from '../model/attachmentPreview'
import { attachmentQueue, attachmentQueueKey, holdTabLock, onAttachmentCleanup, ownerAlive, type StoredItem } from '../model/attachmentQueue'
import { uploadShortfall } from '../model/fileSpace'
import { fileErrorText } from '../transport/files'
import type { FileSpace, TransportFiles } from '../transport/types'

export interface AttachmentItem {
  key: string
  name: string
  size: number
  status: 'uploading' | 'waiting' | 'done' | 'error'
  /** Why a waiting file waits: the session has no room ('space', also when absent), or the
   *  server could not be reached to check ('offline'). */
  wait?: 'space' | 'offline'
  /** Owned by another open tab: shown read-only, never uploaded, written or sent from here. */
  foreign?: boolean
  file?: File
  loaded?: number
  id?: string
  error?: string
  errorId?: string
  preview?: AttachmentPreview
}
const kept = new Map<string, AttachmentItem[]>()
const previewJobs = new Set<AbortController>()
let counter = 0
const EMPTY: AttachmentItem[] = []
/** How often waiting files check for room (and another tab's files for their owner). */
export const WAITING_RETRY_MS = 5000
/** The message on a waiting file when its session ends. */
export const SESSION_ENDED_TEXT = 'This session has ended, so the file can’t be attached.'

function remember(sessionId: string, items: AttachmentItem[]) {
  kept.delete(sessionId)
  if (items.length) kept.set(sessionId, items)
  // Keep every unsent attachment's metadata; only expensive preview data may be evicted.
  let sessions = [...kept.values()].filter(list => list.some(item => item.preview)).length
  let count = [...kept.values()].reduce((total, list) => total + list.filter(item => item.preview).length, 0)
  for (const [sid, list] of kept) {
    if (sessions <= 16 && count <= 64) break
    const previews = list.filter(item => item.preview).length
    if (!previews) continue
    kept.set(sid, list.map(item => ({...item, preview: undefined})))
    count -= previews; sessions--
  }
}

// ── this tab ────────────────────────────────────────────────────────────────────────────────
// A random id in memory and sessionStorage (a reload of the same tab keeps it), alive while this
// tab holds the Web Lock named after it. A duplicated tab finds the lock held and takes a new id.
const TAB_KEY = 'blerg.attach.tab'
let tabReady: Promise<string> | null = null
let releaseTab: (() => void) | null = null
const randomId = () => (globalThis.crypto?.randomUUID?.() ?? `${Date.now()}-${Math.random()}`)
function thisTab(): Promise<string> {
  if (tabReady) return tabReady
  tabReady = (async () => {
    let id = ''
    try { id = sessionStorage.getItem(TAB_KEY) ?? '' } catch { /* no storage */ }
    if (!id || !(await holdTabLock(id, release => { releaseTab = release }))) {
      id = randomId()
      await holdTabLock(id, release => { releaseTab = release })
    }
    try { sessionStorage.setItem(TAB_KEY, id) } catch { /* no storage */ }
    return id
  })()
  return tabReady
}
/** Test seam: be another tab (a second instance of this module stands in for a second tab), or
 *  close this one (null releases its lock, as a closed tab would). */
let testTab = ''
export async function setAttachmentTabForTest(id: string | null) {
  if (id !== null && id === testTab && releaseTab) return // already this tab, lock held
  releaseTab?.(); releaseTab = null; testTab = ''
  if (id === null) { tabReady = Promise.resolve(`closed-${randomId()}`); return }
  testTab = id
  tabReady = holdTabLock(id, release => { releaseTab = release }).then(() => id)
  await tabReady
}

// Mounted hooks hear cleanup (here or in another tab) at once: they drop their chips and jobs.
/** Forgets remembered chips and cancels outstanding local preview work; by default also the files
 *  waiting on this device, in every tab (call it at sign-out). */
export function resetKeptAttachments(clearStored = true) {
  kept.clear()
  for (const ctl of previewJobs) ctl.abort()
  previewJobs.clear()
  if (clearStored) return attachmentQueue.clear().catch(() => {})
}
/** Forgets a session's unsent chips and waiting files on this device, in every tab (call it when
 *  the session is discarded). */
export function forgetSessionAttachments(sessionId: string) {
  for (const key of [...kept.keys()]) if (key === sessionId || key.endsWith(`\0${sessionId}`)) kept.delete(key)
  return attachmentQueue.forgetSession(sessionId).catch(() => {})
}

// One upload at a time per session: in this tab by a promise chain, across tabs by a Web Lock.
const chains = new Map<string, Promise<unknown>>()
function serial(key: string, run: () => Promise<void>): Promise<void> {
  const locks = typeof navigator !== 'undefined' ? (navigator as Navigator & {locks?: LockManager}).locks : undefined
  const locked = () => locks?.request ? locks.request(`blerg-attach:${key}`, run) as Promise<void> : run()
  const next = (chains.get(key) ?? Promise.resolve()).catch(() => {}).then(locked)
  chains.set(key, next)
  void next.catch(() => {}).finally(() => { if (chains.get(key) === next) chains.delete(key) })
  return next
}
let pruned = false
/** p, or a rejection as soon as signal aborts: a removed or abandoned upload never holds the
 *  session's queue, even when the transport ignores the signal. */
function untilAborted<T>(p: Promise<T>, signal: AbortSignal): Promise<T> {
  if (signal.aborted) return Promise.reject(new DOMException('The upload was canceled.', 'AbortError'))
  return new Promise<T>((resolve, reject) => {
    const stop = () => reject(new DOMException('The upload was canceled.', 'AbortError'))
    signal.addEventListener('abort', stop, {once: true})
    p.then(resolve, reject).finally(() => signal.removeEventListener('abort', stop))
  })
}
/** A stored file as a chip: this tab's own, or another tab's (read-only, no bytes). */
function fromStored(item: StoredItem, mine: boolean): AttachmentItem {
  const {owner: _owner, ...rest} = item; void _owner
  if (mine) return {...rest, foreign: undefined, status: rest.status === 'uploading' ? 'waiting' : rest.status}
  return {...rest, foreign: true, file: undefined, loaded: undefined}
}

type State = {sessionId: string; items: AttachmentItem[]}
type Job = {sessionId: string; ctl: AbortController}
const statusOf = (error: unknown) => (error as {status?: number} | null)?.status
const isNetworkFailure = (error: unknown) => error instanceof TypeError

export interface AttachmentOptions {
  /** The session has ended: nothing waits any more (no polling), and waiting files fail. */
  ended?: boolean
}

export function useAttachments(transportSessionId: string, files: TransportFiles | null, maxBytes: number = MAX_ATTACHMENT_BYTES, options: AttachmentOptions = {}) {
  const queueKey = attachmentQueueKey(transportSessionId, files)
  const sessionId = queueKey ?? transportSessionId
  const ended = options.ended === true
  const [state, setState] = useState<State>(() => ({sessionId, items: kept.get(sessionId) ?? EMPTY}))
  const [notice, setNotice] = useState<{sessionId: string; text: string} | null>(null)
  const [space, setSpace] = useState<{sessionId: string; value: FileSpace} | null>(null)
  const stateRef = useRef(state)
  const currentId = useRef(sessionId)
  useLayoutEffect(() => {currentId.current = sessionId}, [sessionId])
  const aborts = useRef(new Map<string, Job>())
  const previews = useRef(new Map<string, Job>())
  const removedKeys = useRef(new Set<string>())
  // Files chosen in this tab since mount: the only entries this tab stores anew (everything else
  // is updated only while it still exists).
  const freshKeys = useRef(new Set<string>())
  const clearEpoch = useRef(0)
  // Set by cleanup: this hook writes nothing more for the session.
  const forgotten = useRef(false)
  // Do not expose the previous session's chips for even the first render after switching.
  const items = state.sessionId === sessionId ? state.items : kept.get(sessionId) ?? EMPTY

  const applyLater = useRef<((sid: string, f: (cur: AttachmentItem[]) => AttachmentItem[]) => void) | null>(null)
  /** Stores what changed in this tab's own files; reacts when an entry is gone or taken over. */
  const persist = useCallback((sid: string, before: AttachmentItem[], after: AttachmentItem[]) => {
    const was = new Map(before.filter(i => !i.foreign).map(i => [i.key, i]))
    const now = new Map(after.filter(i => !i.foreign).map(i => [i.key, i]))
    const added = [...now.values()].filter(i => !was.has(i.key) && freshKeys.current.has(i.key) && i.status !== 'error')
    for (const i of added) freshKeys.current.delete(i.key)
    const gone = [...was.keys()].filter(k => !now.has(k) || now.get(k)!.status === 'error')
    const changed = [...now.values()].filter(i => {
      const b = was.get(i.key)
      return b && i.status !== 'error' && (i.status !== b.status || i.id !== b.id || i.file !== b.file || i.wait !== b.wait)
    })
    if (!added.length && !gone.length && !changed.length) return
    void thisTab().then(async me => {
      if (added.length) await attachmentQueue.add(sid, added, me)
      if (gone.length) await attachmentQueue.remove(sid, gone, me)
      for (const i of changed) {
        const {key, status, id, name, size, wait, file} = i
        const result = await attachmentQueue.update(sid, key, me, {status, id, name, size, wait, file})
        if (result === 'gone' || result === 'foreign') {
          aborts.current.get(key)?.ctl.abort()
          applyLater.current?.(sid, cur => result === 'gone' ? cur.filter(x => x.key !== key)
            : cur.map(x => x.key === key ? {...x, foreign: true, file: undefined, loaded: undefined} : x))
        }
      }
    }).catch(() => {
      if (currentId.current === sid) setNotice({sessionId: sid, text: 'Couldn’t save pending files on this device. Keep this page open until they upload.'})
    })
  }, [])
  const apply = useCallback((sid: string, f: (cur: AttachmentItem[]) => AttachmentItem[]) => {
    if (currentId.current !== sid || forgotten.current) return
    const cur = stateRef.current.sessionId === sid ? stateRef.current.items : kept.get(sid) ?? EMPTY
    stateRef.current = {sessionId: sid, items: f(cur)}
    setState(stateRef.current)
    remember(sid, stateRef.current.items)
    if (queueKey) persist(sid, cur, stateRef.current.items)
  }, [queueKey, persist])
  useEffect(() => {applyLater.current = apply}, [apply])
  const patch = useCallback((sid: string, key: string, value: Partial<AttachmentItem>) => {
    apply(sid, cur => cur.map(item => item.key === key ? {...item, ...value} : item))
  }, [apply])

  useEffect(() => {
    const uploads = aborts.current, local = previews.current
    removedKeys.current.clear()
    forgotten.current = false
    const off = onAttachmentCleanup(c => {
      if (c.type === 'forget' && c.sessionId !== transportSessionId) return
      forgotten.current = true
      for (const [key, job] of uploads) if (job.sessionId === sessionId) {job.ctl.abort(); uploads.delete(key)}
      for (const [key, job] of local) if (job.sessionId === sessionId) {job.ctl.abort(); previewJobs.delete(job.ctl); local.delete(key)}
      stateRef.current = {sessionId, items: EMPTY}
      setState(stateRef.current)
      kept.delete(sessionId)
    })
    stateRef.current = {sessionId, items: kept.get(sessionId) ?? EMPTY}
    setState(stateRef.current)
    if (queueKey && !pruned) {pruned = true; void attachmentQueue.prune().catch(() => {})}
    return () => {
      off()
      for (const [key, job] of uploads) if (job.sessionId === sessionId) {job.ctl.abort(); uploads.delete(key)}
      if (forgotten.current) return
      for (const [key, job] of local) if (job.sessionId === sessionId) {job.ctl.abort(); previewJobs.delete(job.ctl); local.delete(key)}
      // An upload cut short by leaving the session waits to be sent again, here or after a reload.
      const current = stateRef.current.sessionId === sessionId ? stateRef.current.items : kept.get(sessionId) ?? EMPTY
      const keep = current.flatMap(item => item.status === 'done' || item.status === 'waiting' ? [item]
        : item.status === 'uploading' && item.file ? [{...item, status: 'waiting' as const, wait: 'space' as const, loaded: undefined}] : [])
      remember(sessionId, keep)
      // Only an update of this tab's own entries, applied only while they still exist.
      if (queueKey) {
        const cut = current.filter(item => !item.foreign && item.status === 'uploading' && item.file)
        void thisTab().then(me => Promise.all(cut.map(item => attachmentQueue.update(sessionId, item.key, me, {status: 'waiting', wait: 'space'})))).catch(() => {})
      }
    }
  }, [sessionId, queueKey, transportSessionId])

  const cancelPreview = useCallback((key: string) => {
    const job = previews.current.get(key)
    if (job) {job.ctl.abort(); previewJobs.delete(job.ctl); previews.current.delete(key)}
  }, [])

  const fail = useCallback((sid: string, key: string, name: string, error: unknown) => {
    cancelPreview(key)
    const {message, errorId} = fileErrorText(error, 'Upload failed.')
    patch(sid, key, {status: 'error', wait: undefined, file: undefined, preview: undefined, error: `${name}: ${message}`, ...(errorId ? {errorId} : {})})
  }, [patch, cancelPreview])

  /** Reads the session's space; null when the server could not be reached (or said 5xx/429). */
  const readSpace = useCallback(async (sid: string): Promise<FileSpace | null> => {
    if (!files?.space) return null
    try {
      const value = await files.space(transportSessionId)
      if (currentId.current === sid) setSpace({sessionId: sid, value})
      return value
    } catch (error) {
      const status = statusOf(error)
      if (status && status >= 400 && status < 500 && status !== 429) throw error
      return null
    }
  }, [files, transportSessionId])

  const uploadNow = useCallback(async (sid: string, key: string, file: File, ctl: AbortController) => {
    if (!files || ctl.signal.aborted) return
    // Under the session's lock: is the entry still there, and still this tab's?
    if (queueKey && attachmentQueue.available()) {
      const me = await thisTab()
      const entry = (await attachmentQueue.read(sid).catch(() => undefined))?.find(item => item.key === key)
      if (ctl.signal.aborted) return
      if (!entry) { apply(sid, cur => cur.filter(item => item.key !== key)); return }
      if (entry.owner !== me) { apply(sid, cur => cur.map(item => item.key === key ? fromStored(entry, false) : item)); return }
      if (entry.status === 'done' && entry.id) { patch(sid, key, {status: 'done', wait: undefined, id: entry.id, name: entry.name, size: entry.size, file: undefined, loaded: undefined}); return }
    }
    try {
      // Read the quota before transmitting a large body: an early 409 can cut off an XHR.
      if (files.space) {
        const value = await untilAborted(readSpace(sid), ctl.signal)
        if (ctl.signal.aborted) return
        if (!value) { patch(sid, key, {status: 'waiting', wait: 'offline', loaded: undefined}); return }
        if (uploadShortfall(value, file.size) > 0) { patch(sid, key, {status: 'waiting', wait: 'space', loaded: undefined}); return }
      }
      patch(sid, key, {status: 'uploading', wait: undefined})
      const up = await untilAborted(files.upload(transportSessionId, file, loaded => {if (!ctl.signal.aborted) patch(sid, key, {loaded})}, ctl.signal), ctl.signal)
      if (ctl.signal.aborted) return
      patch(sid, key, {status: 'done', wait: undefined, id: up.id, name: up.name, size: up.size, file: undefined, loaded: undefined})
      if (queueKey) await attachmentQueue.flush()
    } catch (error) {
      if (ctl.signal.aborted) return
      // Only a total-size refusal waits; version limits and per-file limits still fail.
      if ((error as {code?: string} | null)?.code === 'file_space_required') {
        patch(sid, key, {status: 'waiting', wait: 'space', loaded: undefined})
        return
      }
      // A cut-off upload (the server refused the body before reading it, or the network dropped)
      // waits when the session is out of room or the server can't be reached.
      if (isNetworkFailure(error) && files.space) {
        let value: FileSpace | null = null
        try { value = await readSpace(sid) } catch { /* a refusal: fail below */ }
        if (ctl.signal.aborted) return
        if (!value) { patch(sid, key, {status: 'waiting', wait: 'offline', loaded: undefined}); return }
        if (uploadShortfall(value, file.size) > 0) { patch(sid, key, {status: 'waiting', wait: 'space', loaded: undefined}); return }
      }
      fail(sid, key, file.name, error)
    }
  }, [files, queueKey, transportSessionId, patch, apply, fail, readSpace])

  const upload = useCallback((sid: string, key: string, file: File, ctl: AbortController) => {
    if (!files) return Promise.resolve()
    if (file.size > maxBytes) {
      patch(sid, key, {status: 'error', file: undefined, error: `${file.name} is larger than ${formatLimit(maxBytes)} and can’t be attached.`})
      aborts.current.delete(key)
      return Promise.resolve()
    }
    return serial(sid, () => uploadNow(sid, key, file, ctl)).catch(error => {
      if (!ctl.signal.aborted) fail(sid, key, file.name, error)
    }).finally(() => { if (aborts.current.get(key)?.ctl === ctl) aborts.current.delete(key) })
  }, [files, maxBytes, patch, fail, uploadNow])

  /** Another tab's files: follow their stored state, and take over those whose tab is gone. */
  const followForeign = useCallback(async () => {
    if (!queueKey || forgotten.current) return
    const sid = sessionId
    if (!stateRef.current.items.some(item => item.foreign)) return
    const me = await thisTab()
    const stored = (await attachmentQueue.read(sid).catch(() => undefined)) ?? []
    const byKey = new Map(stored.map(item => [item.key, item]))
    const claimed: StoredItem[] = []
    for (const item of stateRef.current.items) {
      const entry = byKey.get(item.key)
      if (!item.foreign || !entry || entry.owner === me || await ownerAlive(entry.owner, me)) continue
      // Its tab is gone: take it over, atomically, under the session's lock.
      await serial(sid, async () => {
        const got = await attachmentQueue.claim(sid, item.key, entry.owner, me)
        if (got) claimed.push(got)
      })
    }
    for (const got of claimed) byKey.set(got.key, got)
    apply(sid, cur => cur.flatMap(item => {
      if (!item.foreign) return [item]
      const entry = byKey.get(item.key)
      if (!entry) return []
      return [fromStored(entry, entry.owner === me)]
    }))
  }, [queueKey, sessionId, apply])

  const retryWaiting = useCallback(async () => {
    const current = stateRef.current
    if (current.sessionId !== sessionId || ended) return
    await followForeign()
    const jobs: Promise<void>[] = []
    for (const queued of stateRef.current.items) {
      const item = stateRef.current.items.find(a => a.key === queued.key)
      if (!item || item.foreign || item.status !== 'waiting' || !item.file || aborts.current.has(item.key)) continue
      const ctl = new AbortController()
      aborts.current.set(item.key, {sessionId, ctl})
      jobs.push(upload(sessionId, item.key, item.file, ctl))
    }
    await Promise.all(jobs)
  }, [sessionId, ended, upload, followForeign])

  const retryRef = useRef(retryWaiting)
  useEffect(() => {retryRef.current = retryWaiting}, [retryWaiting])
  useEffect(() => {
    if (!queueKey) return
    let live = true
    const epoch = clearEpoch.current
    void Promise.all([thisTab(), attachmentQueue.read(sessionId)]).then(async ([me, saved]) => {
      if (!live || !saved?.length || epoch !== clearEpoch.current) return
      const alive = new Map<string, boolean>()
      for (const item of saved) if (item.owner !== me && item.owner !== undefined && !alive.has(item.owner)) alive.set(item.owner, await ownerAlive(item.owner, me))
      if (!live) return
      apply(sessionId, cur => {
        const keys = new Set(cur.map(item => item.key))
        return [...saved.filter(item => !keys.has(item.key) && !removedKeys.current.has(item.key))
          .map(item => fromStored(item, item.owner === me)), ...cur]
      })
      // Restored files check for room (and gone owners) at once, not only at the next poll.
      if (saved.some(item => item.status !== 'done')) window.setTimeout(() => {if (live) void retryRef.current()}, 0)
    }).catch(() => {})
    return () => {live = false}
  }, [sessionId, queueKey, apply])

  const pending = items.some(item => item.status === 'waiting' || (item.foreign && item.status !== 'done'))
  useEffect(() => {
    if (!pending || ended) return
    // Poll while files wait; also check at once after a local delete, refocus or reconnect.
    // (Not on every change to waiting: a refused retry would then retry again at once.)
    const retry = () => {void retryWaiting()}
    const timer = window.setInterval(retry, WAITING_RETRY_MS)
    for (const name of ['blerg-files-changed', 'focus', 'online']) window.addEventListener(name, retry)
    return () => {
      window.clearInterval(timer)
      for (const name of ['blerg-files-changed', 'focus', 'online']) window.removeEventListener(name, retry)
    }
  }, [pending, ended, retryWaiting])

  // An ended session takes no files: waiting ones fail and their bytes leave this device.
  useEffect(() => {
    if (!ended) return
    for (const [key, job] of aborts.current) if (job.sessionId === sessionId) {job.ctl.abort(); aborts.current.delete(key)}
    if (stateRef.current.items.some(item => item.status === 'waiting' || item.status === 'uploading'))
      apply(sessionId, cur => cur.map(item => item.status === 'waiting' || item.status === 'uploading'
        ? {...item, status: 'error' as const, wait: undefined, file: undefined, loaded: undefined, error: `${item.name}: ${SESSION_ENDED_TEXT}`} : item))
    // Whoever owns them: the ended session's waiting files leave this device.
    if (queueKey) void attachmentQueue.forgetSessionKey(sessionId).catch(() => {})
  }, [ended, sessionId, apply, queueKey])

  const addFiles = useCallback((list: Iterable<File> | ArrayLike<File>) => {
    if (!files) return
    const current = stateRef.current.sessionId === sessionId ? stateRef.current.items : kept.get(sessionId) ?? EMPTY
    let room = MAX_ATTACHMENTS_PER_MESSAGE - current.filter(item => item.status !== 'error' && !item.foreign).length
    setNotice(null)
    const added: AttachmentItem[] = []
    const starts: (() => void)[] = []
    for (const file of Array.from(list)) {
      const key = `att${Date.now()}-${++counter}`
      if (file.size > maxBytes) {
        added.push({key, name: file.name, size: file.size, status: 'error', error: `${file.name} is larger than ${formatLimit(maxBytes)} and can’t be attached.`})
        continue
      }
      if (room <= 0) {setNotice({sessionId, text: `You can attach up to ${MAX_ATTACHMENTS_PER_MESSAGE} files per message.`}); break}
      room--
      added.push({key, name: file.name, size: file.size, status: 'uploading', file})
      freshKeys.current.add(key)
      const ctl = new AbortController()
      aborts.current.set(key, {sessionId, ctl})
      const previewCtl = new AbortController()
      previews.current.set(key, {sessionId, ctl: previewCtl}); previewJobs.add(previewCtl)
      void createAttachmentPreview(file, previewCtl.signal).then(preview => {
        if (previewCtl.signal.aborted || !preview) return
        apply(sessionId, cur => cur.map(item => item.key === key && item.status !== 'error' ? {...item, preview} : item))
      }).finally(() => {previews.current.delete(key); previewJobs.delete(previewCtl)})
      starts.push(() => {void upload(sessionId, key, file, ctl)})
    }
    if (added.length) apply(sessionId, cur => [...cur, ...added])
    for (const start of starts) start()
  }, [sessionId, files, maxBytes, apply, upload])

  const remove = useCallback((key: string) => {
    const item = stateRef.current.sessionId === sessionId ? stateRef.current.items.find(item => item.key === key) : undefined
    if (!item || item.foreign) return
    removedKeys.current.add(key)
    aborts.current.get(key)?.ctl.abort(); aborts.current.delete(key); cancelPreview(key)
    apply(sessionId, cur => cur.filter(item => item.key !== key))
    setNotice(null)
    if (item.status === 'done' && item.id && files) void files.remove(transportSessionId, item.id).catch(() => {})
  }, [sessionId, transportSessionId, files, apply, cancelPreview])

  /** Sending clears the sent chips and their preview data; uploaded files stay with the session.
   *  Files still waiting for space (and other tabs' files) stay here. */
  const clear = useCallback(() => {
    clearEpoch.current++
    for (const [key, job] of aborts.current) if (job.sessionId === sessionId) {job.ctl.abort(); aborts.current.delete(key)}
    for (const [key, job] of previews.current) if (job.sessionId === sessionId) cancelPreview(key)
    apply(sessionId, cur => {
      const left = cur.filter(item => item.status === 'waiting' || item.foreign)
      return left.length ? left : EMPTY
    })
    setNotice(null)
    if (!stateRef.current.items.length) kept.delete(sessionId)
  }, [sessionId, apply, cancelPreview])

  const waitingItems = items.filter(item => item.status === 'waiting' && !item.foreign)
  return {
    items, retryWaiting,
    waitingBytes: waitingItems.reduce((n, item) => n + item.size, 0),
    /** How many of this tab's files wait, and whether any waits for room (not only the server). */
    waitingCount: waitingItems.length,
    waitingForSpace: waitingItems.some(item => item.wait !== 'offline'),
    /** The last space read for this session (limits, permissions), or null before one. */
    space: space?.sessionId === sessionId ? space.value : null,
    notice: notice?.sessionId === sessionId ? notice.text : null, addFiles, remove, clear,
    uploading: items.some(item => item.status === 'uploading' && !item.foreign),
    done: items.filter(item => item.status === 'done' && item.id && !item.foreign).map(item => ({id: item.id as string, name: item.name, size: item.size})),
  }
}
