import {useEffect, useId, useRef, useState} from 'react'
import {createPortal} from 'react-dom'
import type {FileSpace, TransportFiles} from '../transport/types'
import {attachmentQueue, attachmentQueueKey} from '../model/attachmentQueue'
import {formatSize} from '../model/artifacts'
import {atStorageMaximum, oldestFiles, selectForSpace, uploadShortfall} from '../model/fileSpace'
import {fileActionError} from '../transport/files'
import {useBackdropClose} from '../hooks/useBackdropClose'

/** Tells waiting uploads and file lists that the session's files or space changed. */
export function announceFilesChanged() {
  window.dispatchEvent(new Event('blerg-files-changed'))
}

/** Shared by the waiting files under the message box and the host's Files list. */
export function FreeSpaceButton({sessionId, files, waitingBytes, onChanged}: {sessionId: string; files: TransportFiles; waitingBytes?: number; onChanged?: () => void}) {
  const [open, setOpen] = useState(false)
  if (!files.space) return null
  return <><button type="button" className="artifact-btn" onClick={() => setOpen(true)}>Free up space…</button>{open && <FreeSpaceDialog key={sessionId} sessionId={sessionId} files={files} waitingBytes={waitingBytes} onClose={() => setOpen(false)} onChanged={onChanged} />}</>
}

/** What the person can do about files waiting under the message box, shown once for all of them:
 *  delete older attachments, and (an administrator) raise this session's storage. */
export function WaitingSpaceActions({sessionId, files, waitingBytes, waitingForSpace, space, onChanged}: {
  sessionId: string; files: TransportFiles; waitingBytes: number; waitingForSpace: boolean; space: FileSpace | null; onChanged?: () => void
}) {
  const [busy, setBusy] = useState(false)
  // Keyed by session, so another session's chat never shows this one's outcome.
  const [said, setMessage] = useState<{sessionId: string; text: string; error: boolean} | null>(null)
  const message = said?.sessionId === sessionId ? said : null
  if (!waitingForSpace) {
    return <p className="attach-space-text">Can’t reach the server to check for space. Waiting files upload by themselves when it answers.</p>
  }
  async function increase() {
    if (busy || !files.increaseStorage) return
    setBusy(true); setMessage(null)
    try {
      await files.increaseStorage(sessionId)
      setMessage({sessionId, text: 'Storage increased. Waiting files upload now.', error: false})
      onChanged?.()
      announceFilesChanged()
    } catch (e) {
      setMessage({sessionId, text: fileActionError(e, 'Couldn’t increase the storage. Try again.').message, error: true})
    } finally {setBusy(false)}
  }
  const canIncrease = !!space?.can_increase && !!files.increaseStorage
  const atMax = space ? atStorageMaximum(space) : false
  return <>
    <p className="attach-space-text">
      This session’s attachments are full. You can delete older attachments to make room; waiting files upload by themselves.
      {' '}Send still sends your message without them.
    </p>
    <div className="attach-space-buttons">
      <FreeSpaceButton sessionId={sessionId} files={files} waitingBytes={waitingBytes} onChanged={onChanged} />
      {canIncrease && <button type="button" className="artifact-btn" disabled={busy} aria-busy={busy || undefined} onClick={() => void increase()}>{busy ? 'Increasing…' : 'Increase storage'}</button>}
    </div>
    {space && !canIncrease && <p className="attach-space-text">{atMax ? 'This session already has the most storage this install allows.' : 'Ask an admin to increase this session’s storage.'}</p>}
    {message && <p className={`attach-space-text${message.error ? ' is-error' : ''}`} role={message.error ? 'alert' : 'status'}>{message.text}</p>}
  </>
}

function FreeSpaceDialog({sessionId, files, waitingBytes, onClose, onChanged}: {sessionId: string; files: TransportFiles; waitingBytes?: number; onClose: () => void; onChanged?: () => void}) {
  const title = useId()
  const dialog = useRef<HTMLDivElement>(null)
  const [queuedBytes, setQueuedBytes] = useState(waitingBytes ?? 0)
  const [space, setSpace] = useState<FileSpace | null>(null)
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const backdrop = useBackdropClose(() => {if (!busy) onClose()})
  useEffect(() => {
    const previous = document.activeElement
    dialog.current?.focus()
    return () => {if (previous instanceof HTMLElement && previous.isConnected) previous.focus()}
  }, [])
  useEffect(() => {
    let live = true
    const queueKey = attachmentQueueKey(sessionId, files)
    Promise.all([files.space!(sessionId), waitingBytes === undefined && queueKey ? attachmentQueue.read(queueKey).catch(() => undefined) : Promise.resolve(undefined)]).then(([value, queued]) => {
      if (!live) return
      const bytes = waitingBytes ?? queued?.reduce((n, item) => n + (item.status === 'waiting' ? item.size : 0), 0) ?? 0
      setQueuedBytes(bytes)
      setSpace(value); setSelected(selectForSpace(value, bytes))
    }).catch(e => {if (live) setError(fileActionError(e, 'Couldn’t load the files.').message)})
    return () => {live = false}
  }, [sessionId, files, waitingBytes])
  const deletable = (id: string) => space?.artifacts.find(a => a.id === id)?.can_delete !== false
  async function remove() {
    if (!space || busy) return
    setBusy(true); setError(null)
    try {
      for (const a of oldestFiles(space.artifacts)) if (selected.has(a.id) && a.can_delete !== false) await files.remove(sessionId, a.id)
      onClose()
    } catch (e) {
      setError(fileActionError(e, 'Couldn’t delete the selected files. Try again.').message)
      // Keep the choices, but drop rows already deleted before a partial failure.
      try {setSpace(await files.space!(sessionId))} catch { /* retain the last list */ }
    } finally {
      setBusy(false)
      onChanged?.()
      announceFilesChanged()
    }
  }
  function keyDown(e: React.KeyboardEvent) {
    e.stopPropagation()
    if (e.key === 'Escape' && !busy) onClose()
    if (e.key !== 'Tab') return
    const buttons = dialog.current?.querySelectorAll<HTMLElement>('button:not(:disabled), input:not(:disabled)')
    if (!buttons?.length) {e.preventDefault(); return}
    const first = buttons[0]!, last = buttons[buttons.length - 1]!
    if (e.shiftKey && (document.activeElement === first || document.activeElement === dialog.current)) {e.preventDefault(); last.focus()}
    else if (!e.shiftKey && (document.activeElement === last || document.activeElement === dialog.current)) {e.preventDefault(); first.focus()}
  }
  const shortfall = space ? uploadShortfall(space, queuedBytes) : 0
  const anyDeletable = !!space?.artifacts.some(a => a.can_delete !== false)
  const someRefused = !!space?.artifacts.some(a => a.can_delete === false)
  return createPortal(<div className="artifact-overlay free-space-overlay" {...backdrop}>
    <div className="artifact-dialog free-space-dialog" role="dialog" aria-modal="true" aria-labelledby={title} tabIndex={-1} ref={dialog} onKeyDown={keyDown}>
      <header className="artifact-header"><h2 id={title}>Free up space</h2><button type="button" className="artifact-close" aria-label="Close cleanup" disabled={busy} onClick={onClose}>✕</button></header>
      <div className="artifact-body">
        <p>Files are listed oldest first. Deleting a file is permanent; published files keep a removed marker in History.</p>
        {shortfall > 0 && <p role="status">Free {formatSize(shortfall)} of attachments for the waiting files. Published files use separate space.</p>}
        {space && someRefused && <p role="status">{anyDeletable
          ? 'You can delete the files you attached. The session’s owner or an administrator can delete the others.'
          : 'Only the session’s owner or an administrator can delete these files.'}</p>}
        {!space && !error && <p role="status">Loading files…</p>}
        {error && <p role="alert">{error}</p>}
        {space && <ul className="free-space-list">{oldestFiles(space.artifacts).map(a => <li key={a.id}><label>
          <input type="checkbox" disabled={busy || a.can_delete === false} checked={selected.has(a.id) && a.can_delete !== false} onChange={() => setSelected(prev => {const next = new Set(prev); if (!next.delete(a.id)) next.add(a.id); return next})} />
          <span><strong>{a.name}</strong><small>{formatSize(a.size)} · {a.origin === 'user' ? 'Attachment' : 'Published'} · v{a.version ?? 1}{a.created_at ? ` · ${new Date(a.created_at).toLocaleDateString()}` : ''}</small></span>
        </label></li>)}</ul>}
        {space?.artifacts.length === 0 && <p>No files to delete.</p>}
      </div>
      <footer className="free-space-actions"><button type="button" className="artifact-btn" disabled={busy} onClick={onClose}>Cancel</button><button type="button" className="artifact-btn danger" disabled={busy || !anyDeletable || ![...selected].some(deletable)} onClick={() => void remove()}>{busy ? 'Deleting…' : 'Delete selected files'}</button></footer>
    </div>
  </div>, document.body)
}
