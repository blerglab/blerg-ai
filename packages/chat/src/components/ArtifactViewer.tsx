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
import { useEffect, useId, useRef, useState, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { HostedViewerRequest, useViewerHost } from './ViewerHost'
import { formatSize, parseCsv, prettyJson, withCsp } from '../model/artifacts'
import { langFromName } from '../model/codeLang'
import {
  ARTIFACT_VIEWS,
  CSV_MAX_COLS,
  CSV_MAX_ROWS,
  TEXT_PREVIEW_CAP,
  TEXT_VIEW_MAX,
  blobTypeFor,
  keptMediaUrl,
  mediaUrl,
  releaseMediaUrl,
  isViewable,
  tooBigToPreview,
} from '../model/artifactViews'
import { useBackdropClose } from '../hooks/useBackdropClose'
import { useSelectAllIn } from '../hooks/useSelectAllIn'
import { composeMarkupMessage } from '../model/feedback'
import { MAXIMIZED_KEY, isMaximizeShortcut, localStorageChatStorage, type ViewerLayoutKind as LayoutKind } from '../model/storage'
import { FILE_GONE_MESSAGE, FileRequestError, fileErrorText, isArtifactGone } from '../transport/files'
import type { ArtifactInfo, TransportFiles } from '../transport/types'
import { useChat } from './context'
import { contentKeeper, peekContent } from '../model/viewCache'
import CodeLines from './CodeLines'
import DataTable from './DataTable'
import ErrorId from './ErrorId'
import SourceCode from './SourceCode'
import ViewerToolbar, { ViewerAction, WrapToggle } from './ViewerToolbar'
import { WrapHostContext, WrapScopeContext, isWrapShortcut, toggleWrapIn, wrapKind, type WrapTarget } from '../model/lineWrap'
import { hasRenderedText } from '../model/rawView'
import DiffView from './DiffView'
import Markdown, { type MarkdownDocument } from './Markdown'
import VisualBlock from './VisualBlock'
import { visualFile } from '../model/visualKind'
import MarkupMode from './markup/MarkupMode'
import ImageView from './ImageView'
import ReviewMode from './review/ReviewMode'
import ReviewRequestBar from './ReviewRequestBar'

type Loaded =
  | { kind: 'text'; text: string }
  | { kind: 'blob'; url: string; blob: Blob }
  | { kind: 'error'; message?: string; errorId?: string }
  // The server still lists the file but no longer has its bytes (410, or the list said missing).
  | { kind: 'gone' }

/** What an artifact view keeps (model/viewCache): a version's bytes never change, so a kept one is
 *  shown and not read again. An SVG keeps the data: URL it drew from. */
type KeptArtifact = { kind: 'text'; text: string } | { kind: 'blob'; blob: Blob; dataUrl?: string }
const artifactIdentity = (a: ArtifactInfo) => `artifact:${a.id}:${a.version ?? ''}:${a.view}:${a.size}`

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
  /** Additional controls for a host-owned tab header. */
  headerActions?: ReactNode
  /** Render in place as a region, without a modal portal or size limits. */
  embedded?: boolean
  /** Open straight in review (a markdown or PDF file; a diff is always reviewed in place). */
  review?: boolean
}

export default function ArtifactViewer(props: Props) {
  const host = useViewerHost()
  if (host && !props.embedded) return <HostedViewerRequest view={{ kind: 'artifact', artifact: props.artifact, commit: props.commit, review: props.review, autoplay: props.autoplay }} consumed={props.onClose} />
  return <ArtifactViewerSession key={`${props.sessionId}:${props.artifact.id}:${props.artifact.view}:${props.artifact.version ?? ''}:${props.review ? 'review' : 'view'}`} {...props}/>
}

function ArtifactViewerSession({ sessionId, files, artifact, onClose, latest, onOpen, autoplay = false, commit = null, review = false, embedded = false, headerActions }: Props) {
  const viewerHost = useViewerHost()
  const titleId = useId()
  const dialogRef = useRef<HTMLDivElement | null>(null)
  useSelectAllIn(dialogRef)
  const backdrop = useBackdropClose(onClose)
  // The bytes last read for this version in this session show at once (model/viewCache).
  const [restored] = useState(() => {
    const kept = peekContent<KeptArtifact>(files, sessionId, artifactIdentity(artifact))
    if (!kept) return null
    if (kept.kind === 'text') return { identity: artifactIdentity(artifact), loaded: kept as Loaded }
    const url = keptMediaUrl(kept.blob, artifact.view, artifact.content_type, kept.dataUrl)
    return url ? { identity: artifactIdentity(artifact), loaded: { kind: 'blob', blob: kept.blob, url } as Loaded } : null
  })
  const [loaded, setLoaded] = useState<Loaded | null>(() => restored?.loaded ?? null)
  // An object URL made for a kept file, freed when the view goes.
  useEffect(() => () => { if (restored?.loaded.kind === 'blob') releaseMediaUrl(restored.loaded.url) }, [restored])
  const [raw, setRaw] = useState(false)
  // The kind of code on show, for the wrap toggle; null while the file is rendered, not code.
  const [codeWrap, setCodeWrap] = useState<WrapTarget | null>(null)
  const [showAll, setShowAll] = useState(false)
  const [downloadError, setDownloadError] = useState<{ message: string; errorId?: string } | null>(null)
  const [mode, setMode] = useState<Mode>(review && (artifact.view === 'markdown' || artifact.view === 'pdf' || artifact.view === 'diff') ? 'review' : 'view')
  // The feedback modes send through the chat; a viewer mounted alone has no send path.
  const chat = useChat()
  // A diff review's Conversation already shows the agent's ask as its body: the bar keeps only
  // its decision then.
  const [reviewTab, setReviewTab] = useState<'conversation' | 'files'>(commit ? 'files' : 'conversation')

  // "report.md · v2 of 3" once the file has more than one version; an older one says so.
  const lastVersion = Math.max(artifact.latest_version ?? 0, artifact.version ?? 0)
  const title = lastVersion > 1 && artifact.version ? `${artifact.name} · v${artifact.version} of ${lastVersion}` : artifact.name
  const isOlder = !!artifact.version && artifact.version < lastVersion

  const view = artifact.view
  const spec = isViewable(view) ? ARTIFACT_VIEWS[view] : null
  const tooBig = tooBigToPreview(view, artifact.size)
  const previewable = spec !== null && !tooBig

  useEffect(() => { if (!embedded || viewerHost?.autoFocus !== false) dialogRef.current?.focus() }, [embedded, viewerHost?.autoFocus])

  useEffect(() => {
    if (artifact.missing || !previewable || !spec) return // a file listed as missing is not fetched
    // A version's bytes never change: kept ones are not read again.
    if (restored?.identity === artifactIdentity(artifact)) return
    let cancelled = false
    let url: string | null = null
    const keep = contentKeeper<KeptArtifact>(files, sessionId, artifactIdentity(artifact))
    void (async () => {
      try {
        const blob = await files.raw(sessionId, artifact.id)
        if (spec.textual) {
          const text = await blob.text()
          keep({ kind: 'text', text }, text.length * 2)
          if (!cancelled) setLoaded({ kind: 'text', text })
          return
        }
        const buf = await blob.arrayBuffer()
        if (cancelled) return
        const typed = new Blob([buf], { type: blobTypeFor(view, artifact.content_type) })
        // An SVG draws from a data: URL, never a same-origin blob: one (mediaUrl).
        const made = await mediaUrl(buf, view, artifact.content_type)
        if (cancelled) { releaseMediaUrl(made); return }
        url = made
        keep({ kind: 'blob', blob: typed, dataUrl: made.startsWith('data:') ? made : undefined }, typed.size + (made.startsWith('data:') ? made.length * 2 : 0))
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
      releaseMediaUrl(url)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps -- artifact.version is read by artifactIdentity
  }, [sessionId, files, artifact.id, artifact.version, artifact.size, artifact.content_type, artifact.missing, view, previewable, spec, restored])

  async function download() {
    setDownloadError(null)
    try {
      await files.download(sessionId, artifact.id)
    } catch (e) {
      if (isArtifactGone(e)) setLoaded({ kind: 'gone' })
      else setDownloadError(fileErrorText(e, 'Download failed.'))
    }
  }

  // Which feedback mode this file takes, once its bytes are here.
  const gone = artifact.missing === true || loaded?.kind === 'gone'
  const ready = loaded !== null && loaded.kind !== 'error' && !gone
  // A file tab opens on the patch; its review conversation is an explicit toolbar action.
  const inlineReview = view === 'diff' && !!chat && ready && (!embedded || mode === 'review')

  // Maximize: edge to edge, remembered per kind of layout (a review, a diff, a draw-over, a file).
  const layoutKind: LayoutKind = view === 'diff' ? 'diff' : mode === 'review' ? 'review' : mode === 'markup' ? 'markup' : 'file'
  const [maximizedByKind, setMaximizedByKind] = useState<Partial<Record<LayoutKind, boolean>>>({})
  const maximized = maximizedByKind[layoutKind] ?? localStorageChatStorage.get(MAXIMIZED_KEY(layoutKind)) === '1'
  function toggleMaximized() {
    const next = !maximized
    setMaximizedByKind(m => ({ ...m, [layoutKind]: next }))
    localStorageChatStorage.set(MAXIMIZED_KEY(layoutKind), next ? '1' : null)
  }

  function onKeyDown(e: React.KeyboardEvent) {
    if (!embedded && e.key === 'Escape') {
      e.stopPropagation()
      onClose()
    } else if (isMaximizeShortcut(e)) {
      e.preventDefault()
      e.stopPropagation()
      toggleMaximized()
    } else if (isWrapShortcut(e) && toggleWrapIn(e.currentTarget)) {
      e.preventDefault()
      e.stopPropagation()
    }
  }
  const feedback: Exclude<Mode, 'view'> | null =
    !chat || !ready ? null : view === 'markdown' || view === 'pdf' || view === 'diff' ? 'review' : view === 'image' ? 'markup' : null

  const body = (
    <div className={embedded ? "embedded-viewer" : `artifact-overlay artifact-overlay-viewer${maximized ? ' is-maximized' : ''}`} data-testid="artifact-viewer-overlay" {...(embedded ? {} : backdrop)}>
      <div
        ref={dialogRef}
        className={`artifact-dialog artifact-viewer${mode !== 'view' || inlineReview ? ' is-review' : ''}${view === 'diff' ? ' is-diff' : ''}${maximized ? ' is-maximized' : ''}`}
        role={embedded ? "region" : "dialog"}
        aria-modal={embedded ? undefined : true}
        aria-labelledby={titleId}
        tabIndex={-1}
        onKeyDown={onKeyDown}
      >
        {embedded ? <ViewerToolbar titleId={titleId} title={title} copyValue={artifact.name} meta={['From Files', spec?.label, formatSize(artifact.size)].filter(Boolean).join(' · ')} toggle={loaded?.kind === 'text' && hasRenderedText(artifact.name, artifact.view, loaded.text) && <button type="button" className="artifact-btn" onClick={() => setRaw(!raw)}>{raw ? 'Rendered' : 'Raw'}</button>}>
          {inlineReview && !embedded ? null : mode !== 'view' ? <ViewerAction icon="back" label="Back" onClick={() => setMode('view')} />
            : feedback && <ViewerAction icon={feedback === 'review' ? 'review' : 'markup'} label={feedback === 'review' ? 'Review' : 'Mark up'} onClick={() => setMode(feedback)} />}
          {codeWrap && <WrapToggle kind={codeWrap.kind} available={codeWrap.available} />}
          {!gone && <ViewerAction icon="download" label="Download" onClick={() => void download()} />}
        </ViewerToolbar> : <header className="artifact-header">
          <div className="artifact-header-text">
            <h2 id={titleId}>{title}</h2>
            <p className="artifact-meta">{[spec?.label, formatSize(artifact.size)].filter(Boolean).join(' · ')}</p>
          </div>
          {inlineReview ? null : mode !== 'view' ? (
            <button type="button" className="artifact-btn" onClick={() => setMode('view')}>Back</button>
          ) : feedback === 'review' ? (
            <button type="button" className="artifact-btn" onClick={() => setMode('review')}>Review</button>
          ) : feedback === 'markup' ? (
            <button type="button" className="artifact-btn" onClick={() => setMode('markup')}>Mark up</button>
          ) : null}
          {!gone && <button type="button" className="artifact-btn" onClick={() => void download()}>Download</button>}
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
          {loaded?.kind === 'text' && hasRenderedText(artifact.name, artifact.view, loaded.text) && <button type="button" className="artifact-btn" onClick={() => setRaw(!raw)}>{raw ? 'Rendered' : 'Raw'}</button>}
          {codeWrap && <WrapToggle kind={codeWrap.kind} available={codeWrap.available} text />}
          {headerActions}
          <button type="button" className="artifact-close" aria-label="Close" onClick={onClose}>✕</button>
        </header>}
        {isOlder && (
          <p className="artifact-older" data-testid="artifact-older">
            This is an older version.
            {latest && onOpen && <> <button type="button" className="artifact-link" onClick={() => onOpen(latest)}>View latest</button></>}
          </p>
        )}
        <ReviewRequestBar artifact={artifact} hideAsk={inlineReview && reviewTab === 'conversation'} />
        {downloadError && <div className="artifact-error" role="alert">{downloadError.message}<ErrorId id={downloadError.errorId} /></div>}
        <div className="artifact-body">
          {gone ? (
            <p className="artifact-status" role="status" data-testid="artifact-gone">
              {FILE_GONE_MESSAGE} Its bytes were not kept on the server, so it can’t be viewed or downloaded.
            </p>
          ) : raw && loaded?.kind === 'text' ? (
            <SourceCode text={loaded.text} name={artifact.name} view={artifact.view} />
          ) : (mode === 'review' || inlineReview) && ready ? (
            <ReviewMode
              artifact={artifact}
              embedded={inlineReview}
              commit={commit}
              initialTab={commit ? 'files' : 'conversation'}
              onTabChange={setReviewTab}
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
    </div>
  )
  const scoped = <WrapScopeContext.Provider value={files.queueScope?.() ?? ''}><WrapHostContext.Provider value={setCodeWrap}>{body}</WrapHostContext.Provider></WrapScopeContext.Provider>
  return embedded ? scoped : createPortal(scoped, document.body)
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
  const viewerHost = useViewerHost()
  switch (artifact.view) {
    case 'image':
      return <ImageView key={`${artifact.id}:${artifact.version ?? ''}:${url}`} src={url} name={artifact.name} />
    case 'pdf':
      return (
        <div className="artifact-pdf">
          <iframe className="artifact-frame" title={artifact.name} src={url} />
          {!viewerHost && <a href={url} target="_blank" rel="noopener noreferrer">Open in new tab ↗</a>}
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

// A published file has no folder: its relative images stay placeholders (MarkdownDocument).
const PUBLISHED_DOCUMENT: MarkdownDocument = {}

export function TextView({ artifact, text, showAll, onShowAll, commit, document: doc = PUBLISHED_DOCUMENT }: { artifact: ArtifactInfo; text: string; showAll: boolean; onShowAll: () => void; commit?: string | null; document?: MarkdownDocument }) {
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

  const visual = visualFile(artifact.name, artifact.view, text)
  if (visual) return <VisualBlock kind={visual} source={text} />

  const body = artifact.view === 'json' ? prettyJson(text) : text
  // Code is coloured by its extension, json as json; no known language stays plain.
  const lang = artifact.view === 'json' ? 'json' : artifact.view === 'text' ? langFromName(artifact.name) : null
  const capped = !showAll && body.length > TEXT_PREVIEW_CAP
  const shown = capped ? body.slice(0, TEXT_PREVIEW_CAP) : body
  return (
    <>
      {artifact.view === 'markdown'
        ? <Markdown text={shown} className="artifact-markdown md-doc md-box" document={doc} html />
        // Code and plain text alike: numbered, with the line wrap toggle and the kind's default (#265).
        : <CodeLines text={shown} lang={lang} kind={artifact.view === 'json' ? 'code' : wrapKind(artifact.name)} />}
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
