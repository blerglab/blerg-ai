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
import { RefContext, type RefContextValue } from './RefLinks'
import { createContext, useCallback, useContext, useEffect, useId, useMemo, useRef, useState, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { HostedViewerRequest, useViewerHost } from './ViewerHost'
import { useBackdropClose } from '../hooks/useBackdropClose'
import { useSelectAllIn } from '../hooks/useSelectAllIn'
import { MAXIMIZED_KEY, isMaximizeShortcut, localStorageChatStorage } from '../model/storage'
import type { TransportFiles, WorkspaceGitRef } from '../transport/types'
import DiffView from './DiffView'
import ViewerToolbar, { ViewerAction } from './ViewerToolbar'
import { WrapScopeContext, isWrapShortcut, toggleWrapIn } from '../model/lineWrap'
import { saveBlob } from '../transport/files'
import { repoName } from './Hovercard'
import { visibleText } from '../model/visibleText'
import { contentKeeper, peekContent } from '../model/viewCache'

/** The longest the spinner runs before the viewer gives up (the server answers within ~13 s). */
const GIVE_UP_MS = 20_000

type State = { kind: 'loading' } | { kind: 'ready'; text: string } | { kind: 'large' } | { kind: 'none' }
/** A commit's diff never changes: a kept one (model/viewCache) is shown and not read again. */
const commitIdentity = (sha: string, member?: WorkspaceGitRef) => `commit:${sha}:${member ? `${member.member_id}:${member.launch_generation}:${member.generation}` : ''}`

export interface CommitViewerProps {
  sessionId: string
  sha: string
  member?: WorkspaceGitRef
  files: TransportFiles
  /** The session's repository (a name or a path; its last part is shown). */
  repo?: string
  headerActions?: ReactNode
  embedded?: boolean
  onClose: () => void
}

export default function CommitViewer(props: CommitViewerProps) {
  const host = useViewerHost()
  if (host && !props.embedded) return <HostedViewerRequest view={{ kind: 'commit', sha: props.sha, sessionId: props.sessionId, member: props.member }} consumed={props.onClose} />
  return <CommitViewerBody {...props} />
}
function CommitViewerBody({ sessionId, sha, files, repo, member, onClose, embedded = false, headerActions }: CommitViewerProps) {
  const viewerHost = useViewerHost()
  const titleId = useId()
  const dialogRef = useRef<HTMLDivElement | null>(null)
  useSelectAllIn(dialogRef)
  const backdrop = useBackdropClose(onClose)
  // A host remounts the viewer for another commit (its key), so the first state is this commit's.
  const [state, setState] = useState<State>(() => (files.commitDiff ? peekContent<State>(files, sessionId, commitIdentity(sha, member)) ?? { kind: 'loading' } : { kind: 'none' }))
  const kept = useRef(state.kind === 'ready' || state.kind === 'large')
  const [copied, setCopied] = useState(false)
  const [maximized, setMaximized] = useState(() => localStorageChatStorage.get(MAXIMIZED_KEY('diff')) === '1')
  const short = sha.slice(0, 7)
  const name = visibleText(repoName(repo))

  useEffect(() => { if (!embedded || viewerHost?.autoFocus !== false) dialogRef.current?.focus() }, [embedded, viewerHost?.autoFocus])

  useEffect(() => {
    const read = files.commitDiff
    if (!read || kept.current) return
    let live = true
    const keep = contentKeeper<State>(files, sessionId, commitIdentity(sha, member))
    const ac = new AbortController()
    // Past the wait, the read is given up and the commit reads as not available.
    const timer = setTimeout(() => ac.abort(), GIVE_UP_MS)
    const reading = (member ? read.call(files, sessionId, sha, ac.signal, member) : read.call(files, sessionId, sha, ac.signal))
    reading.then(
      d => {
        const next: State = d === 'large' ? { kind: 'large' } : d ? { kind: 'ready', text: d.text } : { kind: 'none' }
        if (next.kind !== 'none') keep(next, next.kind === 'ready' ? next.text.length * 2 : 0)
        if (live) setState(next)
      },
      () => { if (live) setState({ kind: 'none' }) },
    ).finally(() => clearTimeout(timer))
    return () => { live = false; clearTimeout(timer); ac.abort() }
  }, [files, sessionId, sha, member])

  function toggleMaximized() {
    const next = !maximized
    setMaximized(next)
    localStorageChatStorage.set(MAXIMIZED_KEY('diff'), next ? '1' : null)
  }

  function copySha() {
    if (typeof navigator === 'undefined' || !navigator.clipboard) return
    void navigator.clipboard.writeText(sha).then(() => { setCopied(true); setTimeout(() => setCopied(false), 1200) }, () => {})
  }

  const body = (
    <div className={embedded ? "embedded-viewer" : `artifact-overlay artifact-overlay-viewer${maximized ? ' is-maximized' : ''}`} data-testid="commit-viewer-overlay" {...(embedded ? {} : backdrop)}>
      <div
        ref={dialogRef}
        className={`artifact-dialog artifact-viewer is-diff${maximized ? ' is-maximized' : ''}`}
        role={embedded ? "region" : "dialog"}
        aria-modal={embedded ? undefined : true}
        aria-labelledby={titleId}
        tabIndex={-1}
        onKeyDown={e => {
          if (!embedded && e.key === 'Escape') { e.stopPropagation(); onClose() }
          else if (isMaximizeShortcut(e)) { e.preventDefault(); e.stopPropagation(); toggleMaximized() }
          else if (isWrapShortcut(e) && toggleWrapIn(e.currentTarget)) { e.preventDefault(); e.stopPropagation() }
        }}
      >
        {embedded ? <ViewerToolbar titleId={titleId} title={`Commit ${short}`} copyValue={sha} copyLabel="Copy sha" meta={[sha, 'From the session’s workspace', name].filter(Boolean).join(' · ')}>
          {state.kind === 'ready' && <ViewerAction icon="download" label="Download" onClick={() => saveBlob(new Blob([state.text], { type: 'text/x-diff' }), `${short}.diff`)} />}
        </ViewerToolbar> : <header className="artifact-header">
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
          {headerActions}
          <button type="button" className="artifact-close" aria-label="Close" onClick={onClose}>✕</button>
        </header>}
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
            <DiffView text={shownHeader(state.text)} commit={sha} collapseLarge />
          )}
        </div>
      </div>
    </div>
  )
  const scoped = <WrapScopeContext.Provider value={files.queueScope?.() ?? ''}>{body}</WrapScopeContext.Provider>
  return embedded ? scoped : createPortal(scoped, document.body)
}

/** The patch with its commit header (sha, author, date and message: everything before the first
 *  file) drawn with hidden characters visible. The files' lines are left as they are: their paths
 *  are quoted by the daemon (core.quotePath), and their content is the code itself. */
export function shownHeader(text: string): string {
  const at = text.search(/^diff --git /m)
  return at < 0 ? visibleText(text) : visibleText(text.slice(0, at)) + text.slice(at)
}

/** Opens a commit's diff, from a chip. */
export type OpenCommit = (sessionId: string, sha: string, member?: WorkspaceGitRef, refs?: RefContextValue | null) => void

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
  const [open, setOpen] = useState<{ sessionId: string; sha: string; member?: WorkspaceGitRef; refs?: RefContextValue | null } | null>(null)
  const openedRef = useRef(opened)
  useEffect(() => { openedRef.current = opened }, [opened])
  const can = !!files?.commitDiff
  const openCommit = useCallback<OpenCommit>((sessionId, sha, member, refs) => {
    if (!member && openedRef.current?.(sessionId, sha)) return
    setOpen({ sessionId, sha, member, refs })
  }, [])
  const value = useMemo(() => (can ? openCommit : parent), [can, openCommit, parent])
  return (
    <OpenerContext.Provider value={value}>
      {children}
      {open && files && (
        <RefContext.Provider value={open.refs ?? null}><CommitViewer key={`${open.sessionId}/${open.sha}/${JSON.stringify(open.member)}`} sessionId={open.sessionId} sha={open.sha} member={open.member} files={files} repo={repo?.(open.sessionId)} onClose={() => setOpen(null)} /></RefContext.Provider>
      )}
    </OpenerContext.Provider>
  )
}
