// FilesTab: the session panel's Files — every file the session holds (FilesList: the chat's own list,
// with versions, Download, View and Delete), what was the Files dialog over the chat. The list is the
// chat's (useLiveState's `files`), so a delete here moves the chat's cards and History too; it is read
// again each time the tab comes on screen. View opens the file in the chat's viewer; Attach files
// opens the message box's file picker.
import InitialLoader from './InitialLoader'
import { useEffect, useRef, useState } from 'react'
import { FilesList, fileCount, type ChatFiles } from '@blerglab/chat'

export interface FilesTabProps {
  sessionId: string
  /** On screen: Files is the chosen tab and the panel is open. */
  active: boolean
  /** The chat's Files; absent until the chat reports them (or when it has none). */
  files?: ChatFiles
  /** Opens a file in the chat's viewer. */
  onOpenArtifact: (id: string) => void
  /** Opens the message box's file picker (the drawer goes first). */
  onAttach?: () => void
}

export default function FilesTab({ sessionId, active, files, onOpenArtifact, onAttach }: FilesTabProps) {
  // Not when the list first arrives (the chat has just read it): each later time the tab comes on screen.
  const refresh = files?.refresh
  const arrived = useRef(false)
  useEffect(() => {
    if (!refresh) return
    if (!arrived.current) {
      arrived.current = true
      return
    }
    if (active) refresh()
  }, [active, refresh])
  if (!files) return <MissingFiles key={sessionId} active={active} />
  if (files.status === 'loading' && files.items.length === 0) return <InitialLoader loading active={active} loadKey={sessionId} label="files" />
  const count = fileCount(files.items)
  return (
    <div className="rail-files" data-testid="rail-files">
      {files.items.length > 0 && (
        <div className="rail-files-head">
          <span data-testid="rail-files-count">{count} {count === 1 ? 'file' : 'files'}</span>
          {onAttach && <button type="button" className="rail-link" data-testid="rail-files-attach" onClick={onAttach}>Attach files</button>}
        </div>
      )}
      <FilesList
        sessionId={sessionId}
        files={files.ops}
        items={files.items}
        status={files.status}
        onView={a => onOpenArtifact(a.id)}
        onChanged={files.refresh}
        onAttach={onAttach}
        retryHint="It is read again when you come back to this tab."
      />
    </div>
  )
}

/** Some sessions never supply a Files source; stop waiting without preventing a late arrival. */
function MissingFiles({ active }: { active: boolean }) {
  const [expired, setExpired] = useState(false)
  useEffect(() => {
    if (!active || expired) return
    const timer = setTimeout(() => setExpired(true), 3000)
    return () => clearTimeout(timer)
  }, [active, expired])
  return <InitialLoader loading={!expired} active={active} label="files">
    <p className="rail-empty">The files show once the chat has loaded.</p>
  </InitialLoader>
}
