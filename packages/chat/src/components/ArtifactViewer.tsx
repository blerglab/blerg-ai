// ArtifactViewer: shows a published file inside the app. The bytes come through the transport's
// authenticated `raw` read, and what is done with them depends on the file's `view`
// (model/artifactViews.ts):
//
//   text / json / markdown / csv   read as TEXT and drawn by React (escaped): <pre>, the chat's
//   / diff                         Markdown component (no raw HTML), a table, the diff viewer
//                                  (DiffView: parsed, file by file). Capped.
//   image (svg included)           <img src=blob:>. An SVG is never inlined: inside <img> its
//                                  scripts do not run.
//   pdf                            <iframe src=blob:> typed application/pdf (not sandboxed: the
//                                  browser's PDF viewer needs that; the server only calls a
//                                  file a PDF when its bytes start like one).
//   audio / video                  <audio>/<video controls src=blob:>.
//   html                           <iframe sandbox="allow-scripts" srcdoc=...>: scripts run, but
//                                  the frame has an opaque origin (no allow-same-origin, no forms,
//                                  popups or top navigation) and a CSP meta ahead of the page cuts
//                                  its network entirely.
//   anything else                  no preview: name, size, type and the Download button.
import { useEffect, useId, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { formatSize, parseCsv, prettyJson, withCsp } from '../model/artifacts'
import { langFromName } from '../model/codeLang'
import {
  ARTIFACT_VIEWS,
  CSV_MAX_COLS,
  CSV_MAX_ROWS,
  TEXT_PREVIEW_CAP,
  TEXT_VIEW_MAX,
  blobTypeFor,
  isViewable,
  tooBigToPreview,
} from '../model/artifactViews'
import { useBackdropClose } from '../hooks/useBackdropClose'
import { composeMarkupMessage } from '../model/feedback'
import { FILE_GONE_MESSAGE, FileRequestError, fileErrorText, isArtifactGone } from '../transport/files'
import type { ArtifactInfo, TransportFiles } from '../transport/types'
import { useChat } from './context'
import CodeLines from './CodeLines'
import DataTable from './DataTable'
import ErrorId from './ErrorId'
import DiffView from './DiffView'
import Markdown from './Markdown'
import MarkupMode from './markup/MarkupMode'
import ReviewMode from './review/ReviewMode'

type Loaded =
  | { kind: 'text'; text: string }
  | { kind: 'blob'; url: string; blob: Blob }
  | { kind: 'error'; message?: string; errorId?: string }
  // The server still lists the file but no longer has its bytes (410, or the list said missing).
  | { kind: 'gone' }

/** What the dialog's body shows: the file, its review (markdown, pdf) or a draw-over (image). */
type Mode = 'view' | 'review' | 'markup'

interface Props {
  sessionId: string
  files: TransportFiles
  artifact: ArtifactInfo
  onClose: () => void
  /** The newest version of this file, when `artifact` is an older one and the list has it. */
  latest?: ArtifactInfo
  /** Opens another version in the viewer ("View latest"). */
  onOpen?: (artifact: ArtifactInfo) => void
  /** Start a video or audio file playing at once (opened by a click on its preview). */
  autoplay?: boolean
  /** A diff opens at this commit's section (a checklist item's sha chip). */
  commit?: string | null
}

export default function ArtifactViewer({ sessionId, files, artifact, onClose, latest, onOpen, autoplay = false, commit = null }: Props) {
  const titleId = useId()
  const dialogRef = useRef<HTMLDivElement | null>(null)
  const backdrop = useBackdropClose(onClose)
  const [loaded, setLoaded] = useState<Loaded | null>(null)
  const [showAll, setShowAll] = useState(false)
  const [downloadError, setDownloadError] = useState<{ message: string; errorId?: string } | null>(null)
  const [mode, setMode] = useState<Mode>('view')
  // The feedback modes send through the chat; a viewer mounted alone has no send path.
  const chat = useChat()

  // "report.md · v2 of 3" once the file has more than one version; an older one says so.
  const lastVersion = Math.max(artifact.latest_version ?? 0, artifact.version ?? 0)
  const title = lastVersion > 1 && artifact.version ? `${artifact.name} · v${artifact.version} of ${lastVersion}` : artifact.name
  const isOlder = !!artifact.version && artifact.version < lastVersion

  const view = artifact.view
  const spec = isViewable(view) ? ARTIFACT_VIEWS[view] : null
  const tooBig = tooBigToPreview(view, artifact.size)
  const previewable = spec !== null && !tooBig

  useEffect(() => dialogRef.current?.focus(), [])

  useEffect(() => {
    if (artifact.missing || !previewable || !spec) return // a file listed as missing is not fetched
    let cancelled = false
    let url: string | null = null
    void (async () => {
      try {
        const blob = await files.raw(sessionId, artifact.id)
        if (spec.textual) {
          const text = await blob.text()
          if (!cancelled) setLoaded({ kind: 'text', text })
          return
        }
        const buf = await blob.arrayBuffer()
        if (cancelled) return
        const typed = new Blob([buf], { type: blobTypeFor(view, artifact.content_type) })
        url = URL.createObjectURL(typed)
        setLoaded({ kind: 'blob', url, blob: typed })
      } catch (e) {
        if (cancelled) return
        if (isArtifactGone(e)) setLoaded({ kind: 'gone' })
        // The server's own sentence when it refused for a reason of its own (its disk, say).
        else setLoaded(e instanceof FileRequestError ? { kind: 'error', message: e.message, errorId: e.errorId } : { kind: 'error' })
      }
    })()
    return () => {
      cancelled = true
      if (url) URL.revokeObjectURL(url)
    }
  }, [sessionId, files, artifact.id, artifact.content_type, artifact.missing, view, previewable, spec])

  async function download() {
    setDownloadError(null)
    try {
      await files.download(sessionId, artifact.id)
    } catch (e) {
      if (isArtifactGone(e)) setLoaded({ kind: 'gone' })
      else setDownloadError(fileErrorText(e, 'Download failed.'))
    }
  }

  function onKeyDown(e: React.KeyboardEvent) {
    if (e.key === 'Escape') {
      e.stopPropagation()
      onClose()
    }
  }

  // Which feedback mode this file takes, once its bytes are here.
  const gone = artifact.missing === true || loaded?.kind === 'gone'
  const ready = loaded !== null && loaded.kind !== 'error' && !gone
  const feedback: Exclude<Mode, 'view'> | null =
    !chat || !ready ? null : view === 'markdown' || view === 'pdf' ? 'review' : view === 'image' ? 'markup' : null

  return createPortal(
    <div className="artifact-overlay artifact-overlay-viewer" data-testid="artifact-viewer-overlay" {...backdrop}>
      <div
        ref={dialogRef}
        className={`artifact-dialog artifact-viewer${mode !== 'view' ? ' is-review' : ''}${view === 'diff' ? ' is-diff' : ''}`}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        tabIndex={-1}
        onKeyDown={onKeyDown}
      >
        <header className="artifact-header">
          <div className="artifact-header-text">
            <h2 id={titleId}>{title}</h2>
            <p className="artifact-meta">{[spec?.label, formatSize(artifact.size)].filter(Boolean).join(' · ')}</p>
          </div>
          {mode !== 'view' ? (
            <button type="button" className="artifact-btn" onClick={() => setMode('view')}>Back</button>
          ) : feedback === 'review' ? (
            <button type="button" className="artifact-btn" onClick={() => setMode('review')}>Review</button>
          ) : feedback === 'markup' ? (
            <button type="button" className="artifact-btn" onClick={() => setMode('markup')}>Mark up</button>
          ) : null}
          {!gone && <button type="button" className="artifact-btn" onClick={() => void download()}>Download</button>}
          <button type="button" className="artifact-close" aria-label="Close" onClick={onClose}>✕</button>
        </header>
        {isOlder && (
          <p className="artifact-older" data-testid="artifact-older">
            This is an older version.
            {latest && onOpen && <> <button type="button" className="artifact-link" onClick={() => onOpen(latest)}>View latest</button></>}
          </p>
        )}
        {downloadError && <div className="artifact-error" role="alert">{downloadError.message}<ErrorId id={downloadError.errorId} /></div>}
        <div className="artifact-body">
          {gone ? (
            <p className="artifact-status" role="status" data-testid="artifact-gone">
              {FILE_GONE_MESSAGE} Its bytes were not kept on the server, so it can’t be viewed or downloaded.
            </p>
          ) : mode === 'review' && ready ? (
            <ReviewMode
              artifact={artifact}
              text={loaded.kind === 'text' ? loaded.text : undefined}
              blob={loaded.kind === 'blob' ? loaded.blob : undefined}
              onClose={() => setMode('view')}
            />
          ) : mode === 'markup' && loaded?.kind === 'blob' && chat ? (
            <MarkupMode
              artifact={artifact}
              src={loaded.url}
              onSend={(file, pins) => chat.submitFeedback(composeMarkupMessage(artifact.name, pins), [file])}
              onClose={() => setMode('view')}
            />
          ) : !previewable ? (
            <NoPreview artifact={artifact} tooBig={tooBig} />
          ) : loaded === null ? (
            <p className="artifact-status" data-testid="artifact-loading">Loading…</p>
          ) : loaded.kind === 'error' ? (
            <p className="artifact-status" data-testid="artifact-error">
              {loaded.message
                ? <>{loaded.message}<ErrorId id={loaded.errorId} /></>
                : 'Couldn’t load this file. It may have been deleted — you can still try Download.'}
            </p>
          ) : loaded.kind === 'blob' ? (
            <BlobView artifact={artifact} url={loaded.url} autoplay={autoplay} />
          ) : (
            <TextView artifact={artifact} text={loaded.text} showAll={showAll} onShowAll={() => setShowAll(true)} commit={commit} />
          )}
        </div>
      </div>
    </div>,
    document.body,
  )
}

export function NoPreview({ artifact, tooBig }: { artifact: ArtifactInfo; tooBig: boolean }) {
  return (
    <div className="artifact-nopreview" data-testid="artifact-no-preview">
      <p className="artifact-nopreview-head">
        {tooBig
          ? `Too large to preview (over ${TEXT_VIEW_MAX / (1024 * 1024)} MB) — download it.`
          : 'No preview for this file type — download it.'}
      </p>
      <dl className="artifact-facts">
        <div><dt>Name</dt><dd>{artifact.name}</dd></div>
        <div><dt>Size</dt><dd>{formatSize(artifact.size)}</dd></div>
        <div><dt>Type</dt><dd>{artifact.content_type || 'unknown'}</dd></div>
      </dl>
    </div>
  )
}

export function BlobView({ artifact, url, autoplay }: { artifact: ArtifactInfo; url: string; autoplay: boolean }) {
  switch (artifact.view) {
    case 'image':
      return <img className="artifact-image" src={url} alt={artifact.name} />
    case 'pdf':
      return (
        <div className="artifact-pdf">
          <iframe className="artifact-frame" title={artifact.name} src={url} />
          <a href={url} target="_blank" rel="noopener noreferrer">Open in new tab ↗</a>
        </div>
      )
    case 'audio':
      return <audio className="artifact-media" controls autoPlay={autoplay} src={url} />
    case 'video':
      return <video className="artifact-media" controls autoPlay={autoplay} src={url} />
    default:
      return null
  }
}

export function TextView({ artifact, text, showAll, onShowAll, commit }: { artifact: ArtifactInfo; text: string; showAll: boolean; onShowAll: () => void; commit?: string | null }) {
  if (artifact.view === 'html') {
    return (
      <iframe
        className="artifact-frame artifact-html"
        title={artifact.name}
        sandbox="allow-scripts"
        referrerPolicy="no-referrer"
        srcDoc={withCsp(text)}
      />
    )
  }
  if (artifact.view === 'csv') return <CsvView artifact={artifact} text={text} />
  if (artifact.view === 'diff') return <DiffView text={text} commit={commit} />

  const body = artifact.view === 'json' ? prettyJson(text) : text
  // Code is coloured by its extension, json as json; no known language stays plain wrapped text.
  const lang = artifact.view === 'json' ? 'json' : artifact.view === 'text' ? langFromName(artifact.name) : null
  const capped = !showAll && body.length > TEXT_PREVIEW_CAP
  const shown = capped ? body.slice(0, TEXT_PREVIEW_CAP) : body
  return (
    <>
      {artifact.view === 'markdown'
        ? <Markdown text={shown} className="artifact-markdown md-doc" />
        : lang
          ? <CodeLines text={shown} lang={lang} />
          : <pre className="artifact-text" data-testid="artifact-text">{shown}</pre>}
      {capped && (
        <p className="artifact-status">
          Showing the first {formatSize(TEXT_PREVIEW_CAP)} of {formatSize(body.length)}.{' '}
          <button type="button" className="artifact-link" onClick={onShowAll}>Show all</button>
        </p>
      )}
    </>
  )
}

function CsvView({ artifact, text }: { artifact: ArtifactInfo; text: string }) {
  const tab = /\.tsv$/i.test(artifact.name) || artifact.content_type.includes('tab-separated')
  const { rows, moreRows, moreCols } = parseCsv(text, tab ? '\t' : ',', CSV_MAX_ROWS, CSV_MAX_COLS)
  if (rows.length === 0) return <p className="artifact-status">This file is empty.</p>
  return (
    <>
      <DataTable rows={rows} testId="artifact-table" />
      {(moreRows || moreCols) && (
        <p className="artifact-status">
          {moreRows && `Showing the first ${CSV_MAX_ROWS} rows. `}
          {moreCols && `Only the first ${CSV_MAX_COLS} columns are shown. `}
          Download the file for all of it.
        </p>
      )}
    </>
  )
}
