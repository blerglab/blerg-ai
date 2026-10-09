// useAttachments: the files attached to the message being written. Each file uploads as soon as it
// is added; a chip is 'uploading', 'done' (it will be named in the message) or 'error' (shown, not
// sent). Finished chips are kept per session for the life of the page, so leaving a session and
// coming back does not lose them (the draft text is what model/storage.ts stores; files are not).
import { useCallback, useEffect, useRef, useState } from 'react'
import { MAX_ATTACHMENTS_PER_MESSAGE, MAX_ATTACHMENT_BYTES } from '../model/attachments'
import type { TransportFiles } from '../transport/types'

export interface AttachmentItem {
  key: string
  name: string
  size: number
  status: 'uploading' | 'done' | 'error'
  /** Bytes sent so far while uploading (`size` is the total). */
  loaded?: number
  id?: string
  error?: string
}

const kept = new Map<string, AttachmentItem[]>()
let counter = 0

/** Forgets every session's remembered chips (for tests). */
export function resetKeptAttachments() {
  kept.clear()
}

export function useAttachments(sessionId: string, files: TransportFiles | null) {
  const [items, setItems] = useState<AttachmentItem[]>(() => kept.get(sessionId) ?? [])
  const [notice, setNotice] = useState<string | null>(null)
  // The list as the handlers see it right now; every change goes through apply() so it never lags.
  const itemsRef = useRef(items)
  const aborts = useRef(new Map<string, AbortController>())

  // Leaving: in-flight uploads are cancelled, finished ones are remembered.
  useEffect(() => {
    const live = aborts.current
    return () => {
      for (const c of live.values()) c.abort()
      live.clear()
      const done = itemsRef.current.filter(i => i.status === 'done')
      if (done.length > 0) kept.set(sessionId, done)
      else kept.delete(sessionId)
    }
  }, [sessionId])

  const apply = useCallback((f: (cur: AttachmentItem[]) => AttachmentItem[]) => {
    itemsRef.current = f(itemsRef.current)
    setItems(itemsRef.current)
  }, [])
  const patch = useCallback((key: string, p: Partial<AttachmentItem>) => {
    apply(cur => cur.map(i => (i.key === key ? { ...i, ...p } : i)))
  }, [apply])

  const addFiles = useCallback((list: Iterable<File> | ArrayLike<File>) => {
    if (!files) return
    const chosen = Array.from(list)
    if (chosen.length === 0) return
    let room = MAX_ATTACHMENTS_PER_MESSAGE - itemsRef.current.filter(i => i.status !== 'error').length
    setNotice(null)
    const added: AttachmentItem[] = []
    for (const f of chosen) {
      const key = `att${++counter}`
      if (f.size > MAX_ATTACHMENT_BYTES) {
        added.push({ key, name: f.name, size: f.size, status: 'error', error: `${f.name} is larger than 25 MiB and can’t be attached.` })
        continue
      }
      if (room <= 0) {
        setNotice(`You can attach up to ${MAX_ATTACHMENTS_PER_MESSAGE} files per message.`)
        break
      }
      room--
      added.push({ key, name: f.name, size: f.size, status: 'uploading' })
      const ctl = new AbortController()
      aborts.current.set(key, ctl)
      files.upload(sessionId, f, loaded => patch(key, { loaded }), ctl.signal).then(
        up => {
          aborts.current.delete(key)
          patch(key, { status: 'done', id: up.id, name: up.name, size: up.size })
        },
        (e: unknown) => {
          aborts.current.delete(key)
          if (ctl.signal.aborted) return
          patch(key, { status: 'error', error: `${f.name}: ${e instanceof Error ? e.message : 'upload failed.'}` })
        },
      )
    }
    if (added.length > 0) apply(cur => [...cur, ...added])
  }, [sessionId, files, patch, apply])

  /** Drops a chip: cancels an upload in flight, deletes one that finished. */
  const remove = useCallback((key: string) => {
    const it = itemsRef.current.find(i => i.key === key)
    if (!it) return
    aborts.current.get(key)?.abort()
    aborts.current.delete(key)
    apply(cur => cur.filter(i => i.key !== key))
    setNotice(null)
    if (it.status === 'done' && it.id && files) void files.remove(sessionId, it.id).catch(() => {})
  }, [sessionId, files, apply])

  /** After the message is sent: forget the chips (the files stay with the session). */
  const clear = useCallback(() => {
    apply(() => [])
    setNotice(null)
    kept.delete(sessionId)
  }, [sessionId, apply])

  return {
    items,
    notice,
    addFiles,
    remove,
    clear,
    uploading: items.some(i => i.status === 'uploading'),
    done: items.filter(i => i.status === 'done' && i.id).map(i => ({ id: i.id as string, name: i.name, size: i.size })),
  }
}
