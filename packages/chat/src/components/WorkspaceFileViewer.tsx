// WorkspaceFileViewer: a file the agent named in its text, read from the session's folder (not a
// published artifact) and shown in the artifact viewer's frame. The bytes come through the
// transport's workspace-file read; they are drawn exactly as an artifact's are (the same
// renderers): text as escaped text, html in a sandboxed frame with no network, svg only through
// <img> on a blob URL. Markdown's raw HTML is the one exception, as on GitHub: an allowlist of
// elements and attributes rebuilt by React after DOMPurify (model/markdownHtml.ts). A markdown file's
// relative images are read through the same workspace-file route, resolved beside the file.
//
// A code file opens as numbered, highlighted lines; a line the text named is scrolled to and
// marked. "Save a copy to Files" uploads the bytes as the person's own file, so it is kept after
// the folder changes.
import { useEffect, useId, useMemo, useRef, useState, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { HostedViewerRequest, useViewerHost } from './ViewerHost'
import { useBackdropClose } from '../hooks/useBackdropClose'
import { useSelectAllIn } from '../hooks/useSelectAllIn'
import { formatSize } from '../model/artifacts'
import { ARTIFACT_VIEWS, blobTypeFor, isViewable, keptMediaUrl, mediaUrl, releaseMediaUrl, tooBigToPreview, TEXT_VIEW_MAX } from '../model/artifactViews'
import { queuedRead } from '../model/imageQueue'
import { contentKeeper, peekContent } from '../model/viewCache'
import { workspaceErrorText } from '../model/workspaceErrors'
import { fileErrorText, saveBlob, WorkspaceFileError } from '../transport/files'
import type { ArtifactInfo, TransportFiles, WorkspaceFile } from '../transport/types'
import ErrorId from './ErrorId'
import WorkspaceReview from './review/WorkspaceReview'
import { useChat } from './context'
import ViewerToolbar, { ViewerActionsContext, ViewerAction, WrapToggle } from './ViewerToolbar'
import { WrapHostContext, WrapScopeContext, isWrapShortcut, toggleWrapIn, type WrapTarget } from '../model/lineWrap'
import SourceCode from './SourceCode'
import { hasRenderedText } from '../model/rawView'
import { BlobView, NoPreview, TextView } from './ArtifactViewer'
import { visualFile } from '../model/visualKind'
import { renderVisual } from '../visual/render'
import type { MarkdownDocument } from './Markdown'

/** The longest the spinner runs before the viewer gives up (the server answers within ~11 s). */
const GIVE_UP_MS = 20_000

type State =
  | { kind: 'loading' }
  | { kind: 'error'; message: string; errorId?: string }
  | { kind: 'ready'; file: WorkspaceFile; text?: string; url?: string }

/** What a workspace file view keeps (model/viewCache): the file and its text, or for a picture or
 *  document the SVG data: URL it drew from (other media get a fresh object URL from the blob). */
interface KeptFile { file: WorkspaceFile; text?: string; media?: boolean; dataUrl?: string }
const keptIdentity = (path: string) => `workspace:${path}`
/** A kept file as a ready state, drawn at once. */
function fromKept(kept: KeptFile): { kind: 'ready'; file: WorkspaceFile; text?: string; url?: string } | null {
  if (!kept.media) return { kind: 'ready', file: kept.file, text: kept.text }
  const url = keptMediaUrl(kept.file.blob, kept.file.view, kept.file.content_type, kept.dataUrl)
  return url ? { kind: 'ready', file: kept.file, url } : null
}

interface Props {
  /** Commit blobs have no workspace folder and cannot be uploaded as workspace copies. */
  commitBlob?: boolean
  sessionId: string
  files: TransportFiles
  path: string
  /** The line to land on (text and code views). */
  line?: number
  onClose: () => void
  /** Additional controls for a host-owned tab header. */
  headerActions?: ReactNode
  /** Render in place as a region instead of a modal. */
  embedded?: boolean
  /** A copy was saved to the session's Files: the host reads the list again. */
  onSaved?: () => void
}

export default function WorkspaceFileViewer(props: Props) {
  const host = useViewerHost()
  if (host && !props.embedded) return <HostedViewerRequest view={{ kind: 'workspace', path: props.path, line: props.line }} consumed={props.onClose} />
  return <WorkspaceFileViewerBody key={`${props.sessionId}:${props.path}`} {...props} />
}
function WorkspaceFileViewerBody({ sessionId, files, path, line, onClose, onSaved, embedded = false, headerActions, commitBlob = false }: Props) {
  const viewerHost = useViewerHost()
  const chat = useChat()
  const [reviewing, setReviewing] = useState(false)
  const [utilityHost, setUtilityHost] = useState<HTMLSpanElement | null>(null)
  const titleId = useId()
  const dialogRef = useRef<HTMLDivElement | null>(null)
  useSelectAllIn(dialogRef)
  const backdrop = useBackdropClose(onClose)
  // The contents last read for this file in this session show at once (model/viewCache); the file is
  // read again behind them and replaces them only when its size or modification time changed.
  const [state, setState] = useState<State>(() => {
    const kept = peekContent<KeptFile>(files, sessionId, keptIdentity(path))
    return (kept && fromKept(kept)) || { kind: 'loading' }
  })
  const shown = useRef<State>(state)
  // Declared before the read below, so the read sees what is on screen.
  useEffect(() => { shown.current = state }, [state])
  // An object URL made for a kept picture, freed when the view goes.
  useEffect(() => () => { if (shown.current.kind === 'ready') releaseMediaUrl(shown.current.url) }, [])
  const [watching, setWatching] = useState(false)
  const watch = useRef(false)
  const watchRead = useRef<AbortController | null>(null)
  const refresh = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  const [watchError, setWatchError] = useState(false)
  function toggleWatch() {
    watch.current = !watch.current
    setWatching(watch.current)
    setWatchError(false)
    if (watch.current) setAttempt(value => value + 1)
    else { clearTimeout(refresh.current); watchRead.current?.abort() }
  }
  const [attempt, setAttempt] = useState(0)
  const [note, setNote] = useState<{ ok: boolean; message: string; errorId?: string } | null>(null)
  const [saving, setSaving] = useState(false)
  const [raw, setRaw] = useState(false)
  // The kind of code on show, for the wrap toggle; null while the file is rendered, not code.
  const [codeWrap, setCodeWrap] = useState<WrapTarget | null>(null)
  const name = path.slice(path.lastIndexOf('/') + 1)

  useEffect(() => { if (!embedded || viewerHost?.autoFocus !== false) dialogRef.current?.focus() }, [embedded, viewerHost?.autoFocus])

  useEffect(() => {
    const read = files.workspaceFile
    if (!read) return // ChatView opens the viewer only for a transport that has the read
    const ac = new AbortController()
    watchRead.current = ac
    let url: string | null = null
    let timedOut = false
    let cancelled = false
    const keep = contentKeeper<KeptFile>(files, sessionId, keptIdentity(path))
    // What is on screen before this read (kept contents): the read only checks it is still current.
    const before = shown.current.kind === 'ready' ? shown.current.file : null
    // The last refusal the queue tried again: on a timeout the person still sees the server's reason.
    let lastRefusal: unknown = null
    const timer = setTimeout(() => { timedOut = true; ac.abort() }, GIVE_UP_MS)
    void (async () => {
      try {
        // Through the page's workspace-read queue: two at a time, retried while the server is busy.
        const file = await queuedRead(signal => read.call(files, sessionId, path, signal), ac.signal, { onRefused: e => { lastRefusal = e } })
        clearTimeout(timer)
        if (ac.signal.aborted) return
        if (watch.current) {
          if (!before || file.view !== before.view || file.path !== before.path ||
            !(file.view === 'html' || visualFile(name, file.view, '')) ||
            file.size > TEXT_VIEW_MAX || file.blob.size > TEXT_VIEW_MAX) throw new Error()
          setWatchError(false)
        }
        // Unchanged since the kept contents were read: nothing on screen moves (scroll, selection).
        if (before && before.path === file.path && before.size === file.size && before.mtime === file.mtime && before.view === file.view) return
        const spec = isViewable(file.view) ? ARTIFACT_VIEWS[file.view] : null
        if (spec?.textual && !tooBigToPreview(file.view, file.size)) {
          const text = await file.blob.text()
          const kind = watch.current && visualFile(name, file.view, text)
          if (kind && !ac.signal.aborted) {
            const target = document.createElement('div')
            target.style.cssText = 'position:fixed;visibility:hidden;pointer-events:none;width:800px'
            document.body.append(target)
            try { const visual = await renderVisual(kind, text, target, undefined, ac.signal); visual.dispose() }
            finally { target.remove() }
          }
          if (!ac.signal.aborted) { setState({ kind: 'ready', file, text }); keep({ file, text }, file.size + text.length * 2) }
        } else if (spec && !spec.textual) {
          const buf = await file.blob.arrayBuffer()
          if (ac.signal.aborted) return
          const typed = new Blob([buf], { type: blobTypeFor(file.view, file.content_type) })
          // An SVG draws from a data: URL, never a same-origin blob: one (mediaUrl).
          const made = await mediaUrl(buf, file.view, file.content_type)
          if (ac.signal.aborted) { releaseMediaUrl(made); return }
          url = made
          const replaced = shown.current.kind === 'ready' ? shown.current.url : undefined
          setState({ kind: 'ready', file: { ...file, blob: typed }, url })
          if (replaced && replaced !== made) releaseMediaUrl(replaced)
          keep({ file: { ...file, blob: typed }, media: true, dataUrl: made.startsWith('data:') ? made : undefined }, file.size + (made.startsWith('data:') ? made.length * 2 : 0))
        } else {
          setState({ kind: 'ready', file })
          keep({ file }, file.size)
        }
      } catch (e) {
        clearTimeout(timer)
        if (ac.signal.aborted && !timedOut) return // cancelled by the person
        if (watch.current && before) { setWatchError(true); return }
        // Kept contents on screen: a read that could not reach the server leaves them as they are.
        if (before && !(e instanceof WorkspaceFileError)) return
        if (timedOut) {
          const why = lastRefusal instanceof WorkspaceFileError ? lastRefusal : null
          setState(why
            ? { kind: 'error', message: workspaceErrorText(why.status, why.serverText, why.code), errorId: why.errorId }
            : { kind: 'error', message: 'Opening the file took too long. Try again.' })
        }
        else if (e instanceof WorkspaceFileError) setState({ kind: 'error', message: workspaceErrorText(e.status, e.serverText, e.code), errorId: e.errorId })
        else setState({ kind: 'error', message: 'Couldn’t reach the server to open that file.' })
      } finally {
        // Schedule only after completion, so slow reads never overlap.
        if (!cancelled && watch.current && (timedOut || !ac.signal.aborted)) refresh.current = setTimeout(() => setAttempt(value => value + 1), 1000)
      }
    })()
    return () => {
      cancelled = true
      clearTimeout(refresh.current)
      clearTimeout(timer)
      ac.abort()
      releaseMediaUrl(url)
    }
  }, [sessionId, files, path, name, attempt])

  const ready = state.kind === 'ready' ? state : null
  // A markdown file's relative images and links resolve beside it, in the same folder.
  const mdDocument = useMemo<MarkdownDocument>(() => ({
    path: ready?.file.path ?? path,
    readWorkspace: !commitBlob && files.workspaceFile ? (p: string, signal?: AbortSignal) => files.workspaceFile!.call(files, sessionId, p, signal) : undefined,
  }), [files, sessionId, path, ready?.file.path, commitBlob])

  async function saveCopy() {
    if (!ready) return
    setSaving(true)
    setNote(null)
    try {
      await files.upload(sessionId, new File([ready.file.blob], name, { type: ready.file.blob.type }))
      setNote({ ok: true, message: 'Saved a copy to Files.' })
      onSaved?.()
    } catch (e) {
      setNote({ ok: false, ...fileErrorText(e, 'Couldn’t save a copy.') })
    } finally {
      setSaving(false)
    }
  }

  const meta = ['From the workspace’s folder', 'as of now', ready ? formatSize(ready.file.size) : null].filter(Boolean).join(' · ')

  const body = (
    // button-guard-ignore: useBackdropClose supplies pointer handlers only; this overlay is not a button
    <div className={embedded ? "embedded-viewer embedded-workspace" : "artifact-overlay artifact-overlay-viewer workspace-file-overlay"} data-testid="workspace-viewer-overlay" {...(embedded ? {} : backdrop)}>
      <div
        ref={dialogRef}
        className="artifact-dialog artifact-viewer"
        role={embedded ? "region" : "dialog"}
        aria-modal={embedded ? undefined : true}
        aria-labelledby={titleId}
        tabIndex={-1}
        onKeyDown={e => { if (!embedded && e.key === 'Escape') { e.stopPropagation(); onClose() } else if (isWrapShortcut(e) && toggleWrapIn(e.currentTarget)) { e.preventDefault(); e.stopPropagation() } }}
      >
        <ViewerToolbar exports={<>
          {ready && <ViewerAction icon="download" label="Download" onClick={() => saveBlob(ready.file.blob, name)} />}
        </>} titleId={titleId} title={path} showPath={!embedded || path !== name} meta={meta} primary={ready && chat?.sessionId === sessionId && <ViewerAction icon="review" label="Review" pressed={reviewing} onClick={() => setReviewing(value => !value)} />} toggle={ready?.text !== undefined && hasRenderedText(name, ready.file.view, ready.text) && <ViewerAction icon="raw" label={raw ? 'Rendered' : 'Raw'} title={raw ? 'Show rendered file' : 'Show raw source'} pressed={raw} onClick={() => setRaw(!raw)} />}>
          {codeWrap && <WrapToggle kind={codeWrap.kind} available={codeWrap.available} />}
          {ready && !commitBlob && <ViewerAction icon="save" label={saving ? 'Saving…' : 'Save a copy to Files'} disabled={saving} onClick={() => void saveCopy()} />}
          {ready && (ready.file.view === 'html' || visualFile(name, ready.file.view, '')) && <ViewerAction icon="follow" label={watching ? 'Stop watching' : 'Watch file'} pressed={watching} onClick={toggleWatch} />}
          <span className="viewer-control-slot" ref={setUtilityHost} />
          {headerActions}
          {!embedded && <ViewerAction icon="close" label="Close" onClick={onClose} />}
        </ViewerToolbar>
        {watchError && <div className="artifact-status" role="status">Could not update preview. The previous file is still shown.</div>}
        {watching && !watchError && <div className="artifact-status" role="status">Watching file</div>}
        {note && (
          <div className={note.ok ? 'artifact-status' : 'artifact-error'} role={note.ok ? 'status' : 'alert'} data-testid="workspace-note">
            {note.message}<ErrorId id={note.errorId} />
          </div>
        )}
        <div className="artifact-body">
          {reviewing && ready ? <WorkspaceReview sessionId={sessionId} files={files} path={ready.file.path} text={ready.text} blob={ready.file.blob} onClose={() => setReviewing(false)} /> : state.kind === 'loading' ? (
            <div className="artifact-status workspace-loading" role="status" data-testid="workspace-loading">
              <span className="workspace-spinner" aria-hidden="true" /> Opening the file…{' '}
              <button type="button" className="artifact-link" onClick={onClose}>Cancel</button>
            </div>
          ) : state.kind === 'error' ? (
            <div className="artifact-status" role="alert" data-testid="workspace-error">
              {state.message}<ErrorId id={state.errorId} />
              {' '}<button type="button" className="artifact-link" onClick={() => { setState({ kind: 'loading' }); setAttempt(n => n + 1) }}>Try again</button>
            </div>
          ) : (
            <Body raw={raw} state={state} name={name} line={line} document={mdDocument} />
          )}
        </div>
      </div>
    </div>
  )
  const scoped = <WrapScopeContext.Provider value={files.queueScope?.() ?? ''}><WrapHostContext.Provider value={setCodeWrap}><ViewerActionsContext.Provider value={utilityHost}>{body}</ViewerActionsContext.Provider></WrapHostContext.Provider></WrapScopeContext.Provider>
  return embedded ? scoped : createPortal(scoped, document.body)
}

function Body({ state, name, line, document: doc, raw }: { raw: boolean; state: Extract<State, { kind: 'ready' }>; name: string; line?: number; document: MarkdownDocument }) {
  const { file } = state
  const info: ArtifactInfo = useMemo(
    () => ({ id: file.path, name, size: file.size, content_type: file.content_type, view: file.view }),
    [file.path, name, file.size, file.content_type, file.view],
  )
  const [showAll, setShowAll] = useState(false)
  if (state.text !== undefined && (raw || !hasRenderedText(name, file.view, state.text))) return <SourceCode text={state.text} name={name} line={line} view={file.view} />
  if (state.text !== undefined) return <TextView artifact={info} text={state.text} showAll={showAll} onShowAll={() => setShowAll(true)} document={doc} />
  if (state.url) return <BlobView artifact={info} url={state.url} autoplay={false} />
  return <NoPreview artifact={info} tooBig={tooBigToPreview(file.view, file.size)} />
}
