// Files upload immediately; local previews are bounded, inert thumbnails/excerpts, never source
// Files or resource URLs. Completed chips survive session switches within a bounded page cache.
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react'
import { formatLimit, MAX_ATTACHMENTS_PER_MESSAGE, MAX_ATTACHMENT_BYTES } from '../model/attachments'
import {createAttachmentPreview, type AttachmentPreview} from '../model/attachmentPreview'
import { fileErrorText } from '../transport/files'
import type { TransportFiles } from '../transport/types'

export interface AttachmentItem {
  key: string
  name: string
  size: number
  status: 'uploading' | 'done' | 'error'
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
/** Forgets remembered chips and cancels outstanding local preview work. */
export function resetKeptAttachments() {
  kept.clear()
  for (const ctl of previewJobs) ctl.abort()
  previewJobs.clear()
}
type State = {sessionId: string; items: AttachmentItem[]}
type Job = {sessionId: string; ctl: AbortController}

export function useAttachments(sessionId: string, files: TransportFiles | null, maxBytes: number = MAX_ATTACHMENT_BYTES) {
  const [state, setState] = useState<State>(() => ({sessionId, items: kept.get(sessionId) ?? EMPTY}))
  const [notice, setNotice] = useState<{sessionId: string; text: string} | null>(null)
  const stateRef = useRef(state)
  const currentId = useRef(sessionId)
  useLayoutEffect(() => {currentId.current = sessionId}, [sessionId])
  const aborts = useRef(new Map<string, Job>())
  const previews = useRef(new Map<string, Job>())
  // Do not expose the previous session's chips for even the first render after switching.
  const items = state.sessionId === sessionId ? state.items : kept.get(sessionId) ?? EMPTY

  const apply = useCallback((sid: string, f: (cur: AttachmentItem[]) => AttachmentItem[]) => {
    if (currentId.current !== sid) return
    const cur = stateRef.current.sessionId === sid ? stateRef.current.items : kept.get(sid) ?? EMPTY
    stateRef.current = {sessionId: sid, items: f(cur)}
    setState(stateRef.current)
  }, [])
  const patch = useCallback((sid: string, key: string, value: Partial<AttachmentItem>) => {
    apply(sid, cur => cur.map(item => item.key === key ? {...item, ...value} : item))
  }, [apply])

  useEffect(() => {
    const uploads = aborts.current, local = previews.current
    stateRef.current = {sessionId, items: kept.get(sessionId) ?? EMPTY}
    setState(stateRef.current)
    return () => {
      for (const [key, job] of uploads) if (job.sessionId === sessionId) {job.ctl.abort(); uploads.delete(key)}
      for (const [key, job] of local) if (job.sessionId === sessionId) {job.ctl.abort(); previewJobs.delete(job.ctl); local.delete(key)}
      const done = stateRef.current.sessionId === sessionId ? stateRef.current.items.filter(item => item.status === 'done') : kept.get(sessionId) ?? EMPTY
      remember(sessionId, done)
    }
  }, [sessionId])

  const cancelPreview = useCallback((key: string) => {
    const job = previews.current.get(key)
    if (job) {job.ctl.abort(); previewJobs.delete(job.ctl); previews.current.delete(key)}
  }, [])

  const addFiles = useCallback((list: Iterable<File> | ArrayLike<File>) => {
    if (!files) return
    const current = stateRef.current.sessionId === sessionId ? stateRef.current.items : kept.get(sessionId) ?? EMPTY
    let room = MAX_ATTACHMENTS_PER_MESSAGE - current.filter(item => item.status !== 'error').length
    setNotice(null)
    const added: AttachmentItem[] = []
    for (const file of Array.from(list)) {
      const key = `att${++counter}`
      if (file.size > maxBytes) {
        added.push({key, name: file.name, size: file.size, status: 'error', error: `${file.name} is larger than ${formatLimit(maxBytes)} and can’t be attached.`})
        continue
      }
      if (room <= 0) {setNotice({sessionId, text: `You can attach up to ${MAX_ATTACHMENTS_PER_MESSAGE} files per message.`}); break}
      room--
      added.push({key, name: file.name, size: file.size, status: 'uploading'})
      const ctl = new AbortController()
      aborts.current.set(key, {sessionId, ctl})
      const previewCtl = new AbortController()
      previews.current.set(key, {sessionId, ctl: previewCtl}); previewJobs.add(previewCtl)
      void createAttachmentPreview(file, previewCtl.signal).then(preview => {
        if (previewCtl.signal.aborted || !preview) return
        apply(sessionId, cur => cur.map(item => item.key === key && item.status !== 'error' ? {...item, preview} : item))
      }).finally(() => {previews.current.delete(key); previewJobs.delete(previewCtl)})
      files.upload(sessionId, file, loaded => {if (!ctl.signal.aborted) patch(sessionId, key, {loaded})}, ctl.signal).then(
        up => {
          aborts.current.delete(key)
          if (!ctl.signal.aborted) patch(sessionId, key, {status: 'done', id: up.id, name: up.name, size: up.size})
        },
        (error: unknown) => {
          aborts.current.delete(key)
          cancelPreview(key)
          if (ctl.signal.aborted) return
          const {message, errorId} = fileErrorText(error, 'upload failed.')
          patch(sessionId, key, {status: 'error', preview: undefined, error: `${file.name}: ${message}`, ...(errorId ? {errorId} : {})})
        },
      )
    }
    if (added.length) apply(sessionId, cur => [...cur, ...added])
  }, [sessionId, files, maxBytes, patch, apply, cancelPreview])

  const remove = useCallback((key: string) => {
    const item = stateRef.current.sessionId === sessionId ? stateRef.current.items.find(item => item.key === key) : undefined
    if (!item) return
    aborts.current.get(key)?.ctl.abort(); aborts.current.delete(key); cancelPreview(key)
    apply(sessionId, cur => cur.filter(item => item.key !== key))
    setNotice(null)
    if (item.status === 'done' && item.id && files) void files.remove(sessionId, item.id).catch(() => {})
  }, [sessionId, files, apply, cancelPreview])

  /** Sending clears local chips and preview data; uploaded files stay with the session. */
  const clear = useCallback(() => {
    for (const [key, job] of aborts.current) if (job.sessionId === sessionId) {job.ctl.abort(); aborts.current.delete(key)}
    for (const [key, job] of previews.current) if (job.sessionId === sessionId) cancelPreview(key)
    apply(sessionId, () => EMPTY)
    setNotice(null); kept.delete(sessionId)
  }, [sessionId, apply, cancelPreview])

  return {
    items, notice: notice?.sessionId === sessionId ? notice.text : null, addFiles, remove, clear,
    uploading: items.some(item => item.status === 'uploading'),
    done: items.filter(item => item.status === 'done' && item.id).map(item => ({id: item.id as string, name: item.name, size: item.size})),
  }
}
