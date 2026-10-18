// WorkspaceFileViewer: a file the agent named in its text, read from the session's folder (not a
// published artifact) and shown in the artifact viewer's frame. The bytes come through the
// transport's workspace-file read; they are drawn exactly as an artifact's are (the same
// renderers): text as escaped text, html in a sandboxed frame with no network, svg only through
// <img> on a blob URL. Nothing the file holds is ever put into this page's DOM as markup.
//
// A code file opens as numbered, highlighted lines; a line the text named is scrolled to and
// marked. "Save a copy to Files" uploads the bytes as the person's own file, so it is kept after
// the folder changes.
import { useEffect, useId, useLayoutEffect, useMemo, useRef, useState, type CSSProperties } from 'react'
import { createPortal } from 'react-dom'
import { useBackdropClose } from '../hooks/useBackdropClose'
import { useSelectAllIn } from '../hooks/useSelectAllIn'
import { formatSize } from '../model/artifacts'
import { ARTIFACT_VIEWS, blobTypeFor, isViewable, tooBigToPreview } from '../model/artifactViews'
import { langFromName, langLabel } from '../model/codeLang'
import { useHighlightHead } from '../model/highlight'
import { countLines } from '../model/lineCap'
import { workspaceErrorText } from '../model/workspaceErrors'
import { fileErrorText, saveBlob, WorkspaceFileError } from '../transport/files'
import type { ArtifactInfo, TransportFiles, WorkspaceFile } from '../transport/types'
import CodeText from './CodeText'
import ErrorId from './ErrorId'
import { BlobView, NoPreview, TextView } from './ArtifactViewer'

/** The longest the spinner runs before the viewer gives up (the server answers within ~11 s). */
const GIVE_UP_MS = 20_000

type State =
  | { kind: 'loading' }
  | { kind: 'error'; message: string; errorId?: string }
  | { kind: 'ready'; file: WorkspaceFile; text?: string; url?: string }

interface Props {
  sessionId: string
  files: TransportFiles
  path: string
  /** The line to land on (text and code views). */
  line?: number
  onClose: () => void
  /** A copy was saved to the session's Files: the host reads the list again. */
  onSaved?: () => void
}

export default function WorkspaceFileViewer({ sessionId, files, path, line, onClose, onSaved }: Props) {
  const titleId = useId()
  const dialogRef = useRef<HTMLDivElement | null>(null)
  useSelectAllIn(dialogRef)
  const backdrop = useBackdropClose(onClose)
  const [state, setState] = useState<State>({ kind: 'loading' })
  const [attempt, setAttempt] = useState(0)
  const [note, setNote] = useState<{ ok: boolean; message: string; errorId?: string } | null>(null)
  const [saving, setSaving] = useState(false)
  const [copied, setCopied] = useState(false)
  const name = path.slice(path.lastIndexOf('/') + 1)

  useEffect(() => dialogRef.current?.focus(), [])

  useEffect(() => {
    const read = files.workspaceFile
    if (!read) return // ChatView opens the viewer only for a transport that has the read
    const ac = new AbortController()
    let url: string | null = null
    let timedOut = false
    const timer = setTimeout(() => { timedOut = true; ac.abort() }, GIVE_UP_MS)
    void (async () => {
      try {
        const file = await read.call(files, sessionId, path, ac.signal)
        clearTimeout(timer)
        if (ac.signal.aborted) return
        const spec = isViewable(file.view) ? ARTIFACT_VIEWS[file.view] : null
        if (spec?.textual && !tooBigToPreview(file.view, file.size)) {
          const text = await file.blob.text()
          if (!ac.signal.aborted) setState({ kind: 'ready', file, text })
        } else if (spec && !spec.textual) {
          const buf = await file.blob.arrayBuffer()
          if (ac.signal.aborted) return
          const typed = new Blob([buf], { type: blobTypeFor(file.view, file.content_type) })
          url = URL.createObjectURL(typed)
          setState({ kind: 'ready', file: { ...file, blob: typed }, url })
        } else {
          setState({ kind: 'ready', file })
        }
      } catch (e) {
        clearTimeout(timer)
        if (ac.signal.aborted && !timedOut) return // cancelled by the person
        if (timedOut) setState({ kind: 'error', message: 'Opening the file took too long. Try again.' })
        else if (e instanceof WorkspaceFileError) setState({ kind: 'error', message: workspaceErrorText(e.status, e.serverText), errorId: e.errorId })
        else setState({ kind: 'error', message: 'Couldn’t reach the server to open that file.' })
      }
    })()
    return () => {
      clearTimeout(timer)
      ac.abort()
      if (url) URL.revokeObjectURL(url)
    }
  }, [sessionId, files, path, attempt])

  const ready = state.kind === 'ready' ? state : null

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

  function copyPath() {
    if (typeof navigator === 'undefined' || !navigator.clipboard) return
    void navigator.clipboard.writeText(path).then(
      () => { setCopied(true); setTimeout(() => setCopied(false), 1200) },
      () => {},
    )
  }

  const meta = ['From the session’s folder', 'as of now', ready ? formatSize(ready.file.size) : null].filter(Boolean).join(' · ')

  return createPortal(
    <div className="artifact-overlay artifact-overlay-viewer" data-testid="workspace-viewer-overlay" {...backdrop}>
      <div
        ref={dialogRef}
        className="artifact-dialog artifact-viewer"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        tabIndex={-1}
        onKeyDown={e => { if (e.key === 'Escape') { e.stopPropagation(); onClose() } }}
      >
        <header className="artifact-header">
          <div className="artifact-header-text">
            <h2 id={titleId}>{path}</h2>
            <p className="artifact-meta" title={ready?.file.mtime ? `Last changed ${ready.file.mtime}` : undefined}>{meta}</p>
          </div>
          <button type="button" className="artifact-btn" onClick={copyPath}>{copied ? 'Copied' : 'Copy path'}</button>
          {ready && <button type="button" className="artifact-btn" onClick={() => saveBlob(ready.file.blob, name)}>Download</button>}
          {ready && (
            <button type="button" className="artifact-btn" disabled={saving} onClick={() => void saveCopy()}>
              {saving ? 'Saving…' : 'Save a copy to Files'}
            </button>
          )}
          <button type="button" className="artifact-close" aria-label="Close" onClick={onClose}>✕</button>
        </header>
        {note && (
          <div className={note.ok ? 'artifact-status' : 'artifact-error'} role={note.ok ? 'status' : 'alert'} data-testid="workspace-note">
            {note.message}<ErrorId id={note.errorId} />
          </div>
        )}
        <div className="artifact-body">
          {state.kind === 'loading' ? (
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
            <Body state={state} name={name} line={line} />
          )}
        </div>
      </div>
    </div>,
    document.body,
  )
}

function Body({ state, name, line }: { state: Extract<State, { kind: 'ready' }>; name: string; line?: number }) {
  const { file } = state
  const info: ArtifactInfo = useMemo(
    () => ({ id: file.path, name, size: file.size, content_type: file.content_type, view: file.view }),
    [file.path, name, file.size, file.content_type, file.view],
  )
  const [showAll, setShowAll] = useState(false)
  if (state.text !== undefined && file.view === 'text') return <CodeFile text={state.text} name={name} line={line} />
  if (state.text !== undefined) return <TextView artifact={info} text={state.text} showAll={showAll} onShowAll={() => setShowAll(true)} />
  if (state.url) return <BlobView artifact={info} url={state.url} autoplay={false} />
  return <NoPreview artifact={info} tooBig={tooBigToPreview(file.view, file.size)} />
}

// Numbered, highlighted lines (the code viewer's look); the named line is marked and scrolled to.
function CodeFile({ text, name, line }: { text: string; name: string; line?: number }) {
  const lang = langFromName(name)
  const { tokens: highlighted, tail } = useHighlightHead(lang, text)
  const lines = countLines(text)
  const at = line && line >= 1 && line <= lines ? line : undefined
  const scroller = useRef<HTMLDivElement | null>(null)
  const code = useRef<HTMLPreElement | null>(null)
  // The marked line sits where the stylesheet puts line N (10px padding, 1.5 line height), so the
  // band is drawn by CSS from the line number; the scroll position is measured.
  useLayoutEffect(() => {
    const box = scroller.current
    const pre = code.current
    if (!at || !box || !pre) return
    const cs = getComputedStyle(pre)
    const lh = parseFloat(cs.lineHeight) || (parseFloat(cs.fontSize) || 13) * 1.5
    box.scrollTop = Math.max(0, (parseFloat(cs.paddingTop) || 0) + (at - 1) * lh - box.clientHeight / 3)
  }, [at, text])
  return (
    <div className="code-viewer-scroll" data-testid="workspace-code" ref={scroller}>
      <pre className="code-viewer-gutter" aria-hidden="true">{Array.from({ length: lines }, (_, i) => i + 1).join('\n')}</pre>
      <pre className="code-viewer-code workspace-code" ref={code} data-lang={lang ? langLabel(lang) : undefined}>
        {at && <span className="workspace-line-band" data-testid="workspace-line" style={{ '--chat-ws-line': at } as CSSProperties} />}
        <code><CodeText text={text} highlighted={highlighted} tail={tail} /></code>
      </pre>
    </div>
  )
}
