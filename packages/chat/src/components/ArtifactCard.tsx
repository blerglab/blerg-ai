// The transcript card for a published file: an inline preview when the file is an image (the
// picture itself), a video or audio file (a play tile), an HTML page (the page, small), markdown,
// text, code, JSON or a table (a short excerpt), a PDF (its first page) or a diff (its files and
// +/−), then 📎 name · v3 · size · [Download] [View]. The version shows from v2 on, and on a v1
// whose file has later versions; a lone v1 has none. Clicking the preview opens the viewer; a video or audio
// file then plays at once, and an HTML page becomes usable.
import { FileIcon } from './FileIcon'
import { useEffect, useRef, useState } from 'react'
import { useChat } from './context'
import { formatSize, withCsp } from '../model/artifacts'
import { EXCERPT_KINDS, HTML_PREVIEW_PAGE, type DiffPreviewData, loadDiffPreview, loadPdfThumb, loadPreviewExcerpt, loadPreviewText, loadPreviewUrl, previewKind } from '../model/artifactPreview'
import type { Excerpt } from '../model/previewExcerpt'
import { patchSummaryText } from '../model/patch'
import { useHighlight } from '../model/highlight'
import { isViewable } from '../model/artifactViews'
import { fileErrorText } from '../transport/files'
import type { TransportFiles } from '../transport/types'
import type { ArtifactPayload } from '../types'
import CodeText from './CodeText'
import DataTable from './DataTable'
import { InlineDiff } from './DiffView'
import ErrorId from './ErrorId'
import Markdown from './Markdown'
import VisualBlock from './VisualBlock'
import { visualFile } from '../model/visualKind'
import { TimeLabel } from './TimeLabel'
import './diff.css'

/** True once `ref` has come near the viewport (at once where IntersectionObserver is missing).
 *  One-shot: a preview is fetched when its card scrolls into view and never un-fetched. */
function useNear(ref: { current: Element | null }, enabled: boolean): boolean {
  const [near, setNear] = useState(() => typeof IntersectionObserver === 'undefined')
  useEffect(() => {
    if (!enabled || near || !ref.current || typeof IntersectionObserver === 'undefined') return
    const io = new IntersectionObserver(entries => {
      if (entries.some(e => e.isIntersecting)) {
        setNear(true)
        io.disconnect()
      }
    }, { rootMargin: '200px' })
    io.observe(ref.current)
    return () => io.disconnect()
  }, [ref, enabled, near])
  return near
}

/** The inline preview of a file: the image, or a play tile for video and audio. The image is
 *  fetched when the card scrolls into view (at once where IntersectionObserver is missing). */
export function MediaPreview({ sessionId, files, artifact, onOpen }: {
  sessionId: string
  files: TransportFiles
  artifact: ArtifactPayload
  onOpen: (autoplay: boolean) => void
}) {
  const kind = previewKind(artifact.view, artifact.size)
  const ref = useRef<HTMLButtonElement | null>(null)
  const [near, setNear] = useState(() => typeof IntersectionObserver === 'undefined')
  const [url, setUrl] = useState<string | null>(null)
  const [failed, setFailed] = useState(false)

  useEffect(() => {
    if (kind !== 'image' || near || !ref.current || typeof IntersectionObserver === 'undefined') return
    const io = new IntersectionObserver(entries => {
      if (entries.some(e => e.isIntersecting)) {
        setNear(true)
        io.disconnect()
      }
    }, { rootMargin: '200px' })
    io.observe(ref.current)
    return () => io.disconnect()
  }, [kind, near])

  useEffect(() => {
    if (kind !== 'image' || !near) return
    let live = true
    loadPreviewUrl(sessionId, artifact, id => files.raw(sessionId, id)).then(
      u => { if (live) setUrl(u) },
      () => { if (live) setFailed(true) },
    )
    return () => { live = false }
  }, [kind, near, sessionId, artifact, files])

  if (!kind || failed) return null
  if (kind === 'text' && visualFile(artifact.name, artifact.view, '') === 'mermaid') return <DiagramPreview sessionId={sessionId} files={files} artifact={artifact} />
  if (kind && EXCERPT_KINDS.has(kind)) return <ExcerptPreview sessionId={sessionId} files={files} artifact={artifact} onOpen={() => onOpen(false)} />
  if (kind === 'diff') return <DiffPreview sessionId={sessionId} files={files} artifact={artifact} onOpen={() => onOpen(false)} />
  if (kind === 'pdf') return <PdfPreview sessionId={sessionId} files={files} artifact={artifact} onOpen={() => onOpen(false)} />
  if (kind === 'html') return <HtmlPreview sessionId={sessionId} files={files} artifact={artifact} onOpen={() => onOpen(false)} />
  if (kind === 'media') {
    return (
      <button type="button" className="artifact-preview artifact-preview-tile" data-testid="artifact-preview-tile" onClick={() => onOpen(true)} aria-label={`Play ${artifact.name}`}>
        <span className="artifact-preview-play" aria-hidden="true">▶</span>
        <span className="artifact-preview-tile-name">{artifact.name}</span>
        <span className="artifact-preview-tile-meta">{artifact.view} · {formatSize(artifact.size)}</span>
      </button>
    )
  }
  return (
    <button ref={ref} type="button" className="artifact-preview" data-testid="artifact-preview" onClick={() => onOpen(false)} aria-label={`View ${artifact.name}`}>
      {url ? <img src={url} alt={artifact.name} /> : <span className="artifact-preview-loading" />}
    </button>
  )
}

/** A short excerpt of a markdown, text, code, JSON or table file: the first lines, faded at the
 *  end when there is more. Read from the transport when the card nears the viewport; a failed
 *  fetch draws nothing (the plain card). A cover button on top opens the viewer; the excerpt
 *  itself takes no pointer events, so its links are not followed from a preview. */
function ExcerptPreview({ sessionId, files, artifact, onOpen }: {
  sessionId: string
  files: TransportFiles
  artifact: ArtifactPayload
  onOpen: () => void
}) {
  const ref = useRef<HTMLDivElement | null>(null)
  const near = useNear(ref, true)
  const [excerpt, setExcerpt] = useState<Excerpt | null>(null)
  const [failed, setFailed] = useState(false)

  useEffect(() => {
    if (!near) return
    let live = true
    loadPreviewExcerpt(sessionId, artifact, id => files.raw(sessionId, id)).then(
      e => { if (live) setExcerpt(e) },
      () => { if (live) setFailed(true) },
    )
    return () => { live = false }
  }, [near, sessionId, artifact, files])

  const code = excerpt?.kind === 'text' ? excerpt : null
  const highlighted = useHighlight(code?.lang ?? null, code?.text ?? '', true)
  if (failed) return null
  return (
    <div ref={ref} className={`artifact-preview artifact-preview-excerpt${excerpt?.more ? ' has-more' : ''}`} data-testid="artifact-preview-excerpt">
      {excerpt?.kind === 'markdown' && <Markdown text={excerpt.text} className="artifact-preview-md md-doc" />}
      {code && (
        <div className="md-codeblock">
          <pre><code><CodeText text={code.text} highlighted={highlighted} /></code></pre>
        </div>
      )}
      {excerpt?.kind === 'csv' && (
        excerpt.rows.length === 0
          ? <p className="artifact-status">This file is empty.</p>
          : (
            <DataTable rows={excerpt.rows} preview testId="artifact-preview-table" />
          )
      )}
      {!excerpt && <span className="artifact-preview-loading" />}
      <button type="button" className="artifact-preview-cover" onClick={onOpen} aria-label={`Open ${artifact.name}`} />
    </div>
  )
}

/** A published Mermaid file (.mmd): the diagram itself, drawn when the card nears the viewport,
 *  or its source and the reason it was not drawn. The card's View opens it in the viewer. */
function DiagramPreview({ sessionId, files, artifact }: {
  sessionId: string
  files: TransportFiles
  artifact: ArtifactPayload
}) {
  const ref = useRef<HTMLDivElement | null>(null)
  const near = useNear(ref, true)
  const [source, setSource] = useState<string | null>(null)
  const [failed, setFailed] = useState(false)

  useEffect(() => {
    if (!near) return
    let live = true
    loadPreviewText(sessionId, artifact.id, id => files.raw(sessionId, id)).then(
      t => { if (live) setSource(t) },
      () => { if (live) setFailed(true) },
    )
    return () => { live = false }
  }, [near, sessionId, artifact.id, files])

  if (failed) return null
  return (
    <div ref={ref} className="artifact-preview-diagram" data-testid="artifact-preview-diagram">
      {source === null ? <span className="artifact-preview-loading" /> : <VisualBlock kind="mermaid" source={source.replace(/\n$/, '')} />}
    </div>
  )
}

/** A published diff: "3 files · +12 −4" and the first file names, read when the card nears the
 *  viewport. The whole tile opens the diff viewer. A failed read draws nothing (the plain card). */
function DiffPreview({ sessionId, files, artifact, onOpen }: {
  sessionId: string
  files: TransportFiles
  artifact: ArtifactPayload
  onOpen: () => void
}) {
  const ref = useRef<HTMLButtonElement | null>(null)
  const near = useNear(ref, true)
  const [data, setData] = useState<DiffPreviewData | null>(null)
  const [failed, setFailed] = useState(false)
  const summary = data?.summary ?? null

  useEffect(() => {
    if (!near) return
    let live = true
    loadDiffPreview(sessionId, artifact.id, id => files.raw(sessionId, id)).then(
      d => { if (live) setData(d) },
      () => { if (live) setFailed(true) },
    )
    return () => { live = false }
  }, [near, sessionId, artifact.id, files])

  if (failed || (summary && summary.files === 0)) return null
  const tile = (
    <button ref={ref} type="button" className="artifact-preview artifact-preview-diff" data-testid="artifact-preview-diff" onClick={onOpen} aria-label={`View the diff ${artifact.name}`}>
      {summary
        ? (
            <>
              <span className="artifact-preview-diff-counts" data-testid="artifact-preview-diff-counts">
                {patchSummaryText(summary).split(' · ')[0]} · <span className="diff-plus">+{summary.adds}</span> <span className="diff-minus">−{summary.dels}</span>
              </span>
              <ul className="artifact-preview-diff-files">
                {summary.names.map(n => <li key={n}>{n}</li>)}
                {summary.files > summary.names.length && <li>and {summary.files - summary.names.length} more</li>}
              </ul>
            </>
          )
        : <span className="artifact-preview-loading" />}
    </button>
  )
  // A small patch of one file shows its hunks under the summary line.
  if (!data?.inline) return tile
  return (
    <div className="artifact-preview-diff-stack" data-testid="artifact-preview-diff-inline">
      {tile}
      <div className="artifact-preview-diff-hunks"><InlineDiff file={data.inline} /></div>
    </div>
  )
}

/** The first page of a PDF, drawn small (pdf/thumb.ts) when the card nears the viewport. A
 *  failure (a damaged file, no canvas) draws nothing. */
function PdfPreview({ sessionId, files, artifact, onOpen }: {
  sessionId: string
  files: TransportFiles
  artifact: ArtifactPayload
  onOpen: () => void
}) {
  const ref = useRef<HTMLButtonElement | null>(null)
  const near = useNear(ref, true)
  const [url, setUrl] = useState<string | null>(null)
  const [failed, setFailed] = useState(false)

  useEffect(() => {
    if (!near) return
    let live = true
    loadPdfThumb(sessionId, artifact.id, id => files.raw(sessionId, id)).then(
      u => { if (live) setUrl(u) },
      () => { if (live) setFailed(true) },
    )
    return () => { live = false }
  }, [near, sessionId, artifact.id, files])

  if (failed) return null
  return (
    <button ref={ref} type="button" className="artifact-preview artifact-preview-pdf" data-testid="artifact-preview-pdf" onClick={onOpen} aria-label={`View ${artifact.name}`}>
      {url ? <img src={url} alt={`First page of ${artifact.name}`} /> : <span className="artifact-preview-loading" />}
    </button>
  )
}

/** The inline preview of an HTML page: the page itself, laid out at desktop width and scaled
 *  down to the card, under a cover that opens the viewer.
 *
 *  The page is somebody else's code (an agent wrote it, possibly from text it was fed), so it is
 *  kept where it can touch nothing:
 *    - the same sandbox as the viewer: `sandbox="allow-scripts"` with NO allow-same-origin, so it
 *      runs in an origin of its own — no cookies, no storage, no access to this page — and
 *      cannot open windows, show dialogs, navigate the tab or submit forms;
 *    - the same CSP ahead of its source (withCsp): no network at all, so nothing it reads can
 *      leave;
 *    - it cannot be clicked, scrolled, focused or tabbed into here: the cover is on top of it,
 *      the frame takes no pointer events and is inert and hidden from assistive technology. To
 *      use the page, open it;
 *    - it only exists while its card is near the viewport, so a transcript full of pages does
 *      not run them all, and a page that spins is gone once it is scrolled away. */
function HtmlPreview({ sessionId, files, artifact, onOpen }: {
  sessionId: string
  files: TransportFiles
  artifact: ArtifactPayload
  onOpen: () => void
}) {
  const ref = useRef<HTMLDivElement | null>(null)
  const noObserver = typeof IntersectionObserver === 'undefined'
  const [near, setNear] = useState(noObserver)
  const [html, setHtml] = useState<string | null>(null)
  const [failed, setFailed] = useState(false)
  const [width, setWidth] = useState(0)

  useEffect(() => {
    const el = ref.current
    if (!el || noObserver) return
    const io = new IntersectionObserver(entries => {
      for (const e of entries) setNear(e.isIntersecting)
    }, { rootMargin: '400px' })
    io.observe(el)
    return () => io.disconnect()
  }, [noObserver])

  useEffect(() => {
    const el = ref.current
    if (!el) return
    const measure = () => setWidth(el.clientWidth)
    measure()
    if (typeof ResizeObserver === 'undefined') return
    const ro = new ResizeObserver(measure)
    ro.observe(el)
    return () => ro.disconnect()
  }, [])

  useEffect(() => {
    if (!near || html !== null) return
    let live = true
    loadPreviewText(sessionId, artifact.id, id => files.raw(sessionId, id)).then(
      t => { if (live) setHtml(t) },
      () => { if (live) setFailed(true) },
    )
    return () => { live = false }
  }, [near, html, sessionId, artifact.id, files])

  if (failed) return null
  const scale = (width || 480) / HTML_PREVIEW_PAGE.width
  return (
    <div ref={ref} className="artifact-preview artifact-preview-html" data-testid="artifact-preview-html" style={{ height: Math.round(HTML_PREVIEW_PAGE.height * scale) }}>
      {near && html !== null && (
        <iframe
          title={`Preview of ${artifact.name}`}
          sandbox="allow-scripts"
          referrerPolicy="no-referrer"
          srcDoc={withCsp(html)}
          tabIndex={-1}
          aria-hidden="true"
          inert
          scrolling="no"
          style={{ width: HTML_PREVIEW_PAGE.width, height: HTML_PREVIEW_PAGE.height, transform: `scale(${scale})` }}
        />
      )}
      <button type="button" className="artifact-preview-cover" onClick={onOpen} aria-label={`Open ${artifact.name}`}>
        <span className="artifact-preview-open" aria-hidden="true">Open ⤢</span>
      </button>
    </div>
  )
}

export default function ArtifactCard({ payload, ts, eventId }: { payload: ArtifactPayload; ts?: string; eventId?: string }) {
  const chat = useChat()
  const files = chat?.files ?? null
  const [error, setError] = useState<{ message: string; errorId?: string } | null>(null)
  const [busy, setBusy] = useState(false)
  const version = payload.version
  const latest = chat?.latestVersion(payload) ?? payload.latest_version ?? version
  const showVersion = version !== undefined && (version > 1 || (latest ?? 1) > 1)

  async function download() {
    if (!chat || !files) return
    setBusy(true)
    setError(null)
    try {
      await files.download(chat.sessionId, payload.id)
    } catch (e) {
      setError(fileErrorText(e, 'Download failed.'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="agent-card artifact-card" data-testid="artifact-card" data-event-id={eventId}>
      {chat && files && <MediaPreview sessionId={chat.sessionId} files={files} artifact={payload} onOpen={autoplay => chat.view(payload, { autoplay })} />}
      <div className="artifact-card-row">
        <FileIcon name={payload.name} className="artifact-card-icon" />
        <span className="artifact-card-name">{payload.name}</span>
        {showVersion && <>{' '}<span className="artifact-card-version" data-testid="artifact-card-version">· v{version}</span></>}
        {' '}<span className="artifact-card-size">· {formatSize(payload.size)}</span>
        {chat && files && (
          <span className="artifact-card-actions">
            <button type="button" disabled={busy} onClick={() => void download()}>Download</button>
            {isViewable(payload.view) && (
              <button type="button" onClick={() => chat.view(payload)}>View</button>
            )}
          </span>
        )}
        <TimeLabel ts={ts} className="card-time-end" />
      </div>
      {error && <div className="artifact-error" role="alert">{error.message}<ErrorId id={error.errorId} /></div>}
    </div>
  )
}
