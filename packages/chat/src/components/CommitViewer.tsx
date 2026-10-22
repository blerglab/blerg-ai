// CommitViewer: a commit's diff, generated on demand from the session's own workspace (the
// transport's commitDiff) and drawn by the diff viewer every published diff uses (DiffView), in
// the artifact viewer's frame. It is what a commit chip opens when no published diff holds the
// commit: the commit stays inside Blerg, never a link out to a forge.
//
// A diff over the server's cap (2 MB) is not drawn: the viewer says it is too large. A commit that
// cannot be read (the session ended, its daemon is offline, the sha is unknown here, no access)
// gets one short, neutral line: there is nothing for the person to fix.
//
// CommitViewerHost holds the open commit for the chips under it; useCommitOpener is how a chip
// opens one (null where no host can, and the chip then copies its sha as before).
import { createContext, useCallback, useContext, useEffect, useId, useMemo, useRef, useState, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { useBackdropClose } from '../hooks/useBackdropClose'
import { useSelectAllIn } from '../hooks/useSelectAllIn'
import { MAXIMIZED_KEY, isMaximizeShortcut, localStorageChatStorage } from '../model/storage'
import type { TransportFiles } from '../transport/types'
import DiffView from './DiffView'
import { repoName } from './Hovercard'
import { visibleText } from '../model/visibleText'

/** The longest the spinner runs before the viewer gives up (the server answers within ~13 s). */
const GIVE_UP_MS = 20_000

type State = { kind: 'loading' } | { kind: 'ready'; text: string } | { kind: 'large' } | { kind: 'none' }

export interface CommitViewerProps {
  sessionId: string
  sha: string
  files: TransportFiles
  /** The session's repository (a name or a path; its last part is shown). */
  repo?: string
  onClose: () => void
}

export default function CommitViewer({ sessionId, sha, files, repo, onClose }: CommitViewerProps) {
  const titleId = useId()
  const dialogRef = useRef<HTMLDivElement | null>(null)
  useSelectAllIn(dialogRef)
  const backdrop = useBackdropClose(onClose)
  // A host remounts the viewer for another commit (its key), so the first state is this commit's.
  const [state, setState] = useState<State>(() => (files.commitDiff ? { kind: 'loading' } : { kind: 'none' }))
  const [copied, setCopied] = useState(false)
  const [maximized, setMaximized] = useState(() => localStorageChatStorage.get(MAXIMIZED_KEY('diff')) === '1')
  const short = sha.slice(0, 7)
  const name = visibleText(repoName(repo))

  useEffect(() => dialogRef.current?.focus(), [])

  useEffect(() => {
    const read = files.commitDiff
    if (!read) return
    let live = true
    const ac = new AbortController()
    // Past the wait, the read is given up and the commit reads as not available.
    const timer = setTimeout(() => ac.abort(), GIVE_UP_MS)
    read.call(files, sessionId, sha, ac.signal).then(
      d => { if (live) setState(d === 'large' ? { kind: 'large' } : d ? { kind: 'ready', text: d.text } : { kind: 'none' }) },
      () => { if (live) setState({ kind: 'none' }) },
    ).finally(() => clearTimeout(timer))
    return () => { live = false; clearTimeout(timer); ac.abort() }
  }, [files, sessionId, sha])

  function toggleMaximized() {
    const next = !maximized
    setMaximized(next)
    localStorageChatStorage.set(MAXIMIZED_KEY('diff'), next ? '1' : null)
  }

  function copySha() {
    if (typeof navigator === 'undefined' || !navigator.clipboard) return
    void navigator.clipboard.writeText(sha).then(() => { setCopied(true); setTimeout(() => setCopied(false), 1200) }, () => {})
  }

  return createPortal(
    <div className={`artifact-overlay artifact-overlay-viewer${maximized ? ' is-maximized' : ''}`} data-testid="commit-viewer-overlay" {...backdrop}>
      <div
        ref={dialogRef}
        className={`artifact-dialog artifact-viewer is-diff${maximized ? ' is-maximized' : ''}`}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        tabIndex={-1}
        onKeyDown={e => {
          if (e.key === 'Escape') { e.stopPropagation(); onClose() }
          else if (isMaximizeShortcut(e)) { e.preventDefault(); e.stopPropagation(); toggleMaximized() }
        }}
      >
        <header className="artifact-header">
          <div className="artifact-header-text">
            <h2 id={titleId}>Commit {short}</h2>
            <p className="artifact-meta" title={sha}>{['From the session’s workspace', name].filter(Boolean).join(' · ')}</p>
          </div>
          <button type="button" className="artifact-btn" onClick={copySha}>{copied ? 'Copied' : 'Copy sha'}</button>
          <button
            type="button"
            className="artifact-close artifact-maximize"
            aria-label={maximized ? 'Restore size' : 'Maximize'}
            aria-pressed={maximized}
            title={`${maximized ? 'Restore size' : 'Maximize'} (Ctrl+Alt+F)`}
            onClick={toggleMaximized}
          >
            <span aria-hidden="true">{maximized ? '⤡' : '⤢'}</span>
          </button>
          <button type="button" className="artifact-close" aria-label="Close" onClick={onClose}>✕</button>
        </header>
        <div className="artifact-body">
          {state.kind === 'loading' ? (
            <div className="artifact-status workspace-loading" role="status" data-testid="commit-loading">
              <span className="workspace-spinner" aria-hidden="true" /> Reading the commit…
            </div>
          ) : state.kind === 'large' ? (
            <p className="artifact-status" role="status" data-testid="commit-large">This commit’s diff is too large to open here.</p>
          ) : state.kind === 'none' ? (
            <p className="artifact-status" role="status" data-testid="commit-none">This commit can’t be read from the session’s workspace right now.</p>
          ) : (
            <DiffView text={shownHeader(state.text)} commit={sha} />
          )}
        </div>
      </div>
    </div>,
    document.body,
  )
}

/** The patch with its commit header (sha, author, date and message: everything before the first
 *  file) drawn with hidden characters visible. The files' lines are left as they are: their paths
 *  are quoted by the daemon (core.quotePath), and their content is the code itself. */
export function shownHeader(text: string): string {
  const at = text.search(/^diff --git /m)
  return at < 0 ? visibleText(text) : visibleText(text.slice(0, at)) + text.slice(at)
}

/** Opens a commit's diff, from a chip. */
export type OpenCommit = (sessionId: string, sha: string) => void

const OpenerContext = createContext<OpenCommit | null>(null)

/** How a chip opens a commit: null when nothing above it can (no host, or a transport without the
 *  commit read). */
export function useCommitOpener(): OpenCommit | null {
  return useContext(OpenerContext)
}

/** Lets the chips under it open a commit's diff on demand. `opened`: the host opens the commit
 *  itself when it can do better (a published diff that holds it) and says so with true. A host
 *  over a transport without the commit read passes the one above it through. */
export function CommitViewerHost({ files, repo, opened, children }: {
  files: TransportFiles | null | undefined
  repo?: (sessionId: string) => string | undefined
  opened?: (sessionId: string, sha: string) => boolean
  children: ReactNode
}) {
  const parent = useContext(OpenerContext)
  const [open, setOpen] = useState<{ sessionId: string; sha: string } | null>(null)
  const openedRef = useRef(opened)
  useEffect(() => { openedRef.current = opened }, [opened])
  const can = !!files?.commitDiff
  const openCommit = useCallback<OpenCommit>((sessionId, sha) => {
    if (openedRef.current?.(sessionId, sha)) return
    setOpen({ sessionId, sha })
  }, [])
  const value = useMemo(() => (can ? openCommit : parent), [can, openCommit, parent])
  return (
    <OpenerContext.Provider value={value}>
      {children}
      {open && files && (
        <CommitViewer key={`${open.sessionId}/${open.sha}`} sessionId={open.sessionId} sha={open.sha} files={files} repo={repo?.(open.sessionId)} onClose={() => setOpen(null)} />
      )}
    </OpenerContext.Provider>
  )
}
