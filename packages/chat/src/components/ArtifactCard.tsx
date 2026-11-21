import {liveFileSupport} from '../visual/liveFile'
// The transcript card for a published file: an inline preview when the file is an image (the
// picture itself), a video (a player), audio (a play tile), an HTML page (the page, small), markdown,
// text, code, JSON or a table (a short excerpt), a PDF (its first page) or a diff (its files and
// +/−), then 📎 name · v3 · size · [Download] [View]. The version shows from v2 on, and on a v1
// whose file has later versions; a lone v1 has none. Videos play here after Play; other previews
// open the viewer, where audio plays at once and an HTML page becomes usable.
import { FileIcon } from './FileIcon'
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { useChat } from './context'
import { formatSize, withCsp } from '../model/artifacts'
import { EXCERPT_KINDS, HTML_PREVIEW_PAGE, type DiffPreviewData, loadDiffPreview, loadPdfThumb, loadPreviewExcerpt, loadPreviewText, loadPreviewUrl, previewKind } from '../model/artifactPreview'
import type { Excerpt } from '../model/previewExcerpt'
import { patchSummaryText } from '../model/patch'
import { useHighlight } from '../model/highlight'
import { mediaUrl, releaseMediaUrl, isViewable } from '../model/artifactViews'
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
import { artifactContentIdentity, contentKeeper } from '../model/viewCache'
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

/** The inline preview of a file: an image, video player, or audio play tile. The image is
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
  if (kind === 'text' && visualFile(artifact.name, artifact.view, '')) return <VisualPreview key={artifact.id} sessionId={sessionId} files={files} artifact={artifact} />
  if (kind && EXCERPT_KINDS.has(kind)) return <ExcerptPreview sessionId={sessionId} files={files} artifact={artifact} onOpen={() => onOpen(false)} />
  if (kind === 'diff') return <DiffPreview sessionId={sessionId} files={files} artifact={artifact} onOpen={() => onOpen(false)} />
  if (kind === 'pdf') return <PdfPreview sessionId={sessionId} files={files} artifact={artifact} onOpen={() => onOpen(false)} />
  if (kind === 'html') return <HtmlPreview sessionId={sessionId} files={files} artifact={artifact} onOpen={() => onOpen(false)} />
  if (kind === 'video') return <VideoPreview key={artifact.id} sessionId={sessionId} files={files} artifact={artifact} />
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

/** The transport reads whole blobs, so defer the authenticated read until Play. */
function VideoPreview({ sessionId, files, artifact, onFailed }: {
  sessionId: string; files: TransportFiles; artifact: ArtifactPayload; onFailed?: () => void
}) {
  const ref = useRef<HTMLVideoElement | null>(null)
  const [requested, setRequested] = useState(false)
  const [url, setUrl] = useState<string | null>(null)
  const [failed, setFailed] = useState(false)
  const fail = () => { setFailed(true); onFailed?.() }
  useEffect(() => {
    if (!requested) return
    let live = true
    let made: string | null = null
    void files.raw(sessionId, artifact.id).then(bytes => mediaUrl(bytes, 'video', artifact.content_type)).then(u => {
      made = u
      if (live) setUrl(u)
      else releaseMediaUrl(u)
    }, () => { if (live) { setFailed(true); onFailed?.() } })
    return () => { live = false; releaseMediaUrl(made) }
  }, [requested, files, sessionId, artifact.id, artifact.content_type, onFailed])
  useEffect(() => {
    if (url) void ref.current?.play().catch(() => { /* Native controls allow a retry if play was blocked. */ })
  }, [url])
  if (failed) return null
  return <div className="artifact-preview artifact-preview-video" data-testid="artifact-preview-video">
    <video ref={ref} src={url ?? undefined} controls preload="none" playsInline aria-label={artifact.name} onError={fail} />
    {!url && <button type="button" className="artifact-btn artifact-video-play" disabled={requested} onClick={() => setRequested(true)} aria-label={`Play ${artifact.name}`}>{requested ? 'Loading…' : '▶ Play'}</button>}
  </div>
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

/** Published charts and diagrams use the inline renderer and policy, fetched near the viewport.
 *  Reserve the preview's height before reading; View opens the full file. */
function VisualPreview({ sessionId, files, artifact }: {
  sessionId: string
  files: TransportFiles
  artifact: ArtifactPayload
}) {
  const ref = useRef<HTMLDivElement | null>(null)
  const near = useNear(ref, true)
  const [source, setSource] = useState<string | null>(null)
  const [failed, setFailed] = useState(false)
  const [observed,setObserved] = useState(artifact.updated_at)
  const [runtimeUnsupported,setRuntimeUnsupported] = useState(false)
  const kind = visualFile(artifact.name, artifact.view, '')!
  const unsupported = artifact.live && (runtimeUnsupported || source !== null && liveFileSupport(kind,source)==='unsupported-live')


  useLayoutEffect(() => {
    const node=ref.current
    const support=()=>{if(artifact.live){const surfaces=[...node!.querySelectorAll<HTMLElement>('.visual-surface')];setRuntimeUnsupported(surfaces.some(surface=>surface.dataset.liveSupport!=='true'))}}
    node?.addEventListener('blerg-chart-live-support',support)
    return ()=>node?.removeEventListener('blerg-chart-live-support',support)
  },[artifact.live])
  const identity = artifactContentIdentity(artifact)
  useEffect(() => {
    if (!near) return
    let live = true
    const keep = contentKeeper(files, sessionId, identity)
    loadPreviewText(sessionId, artifact.id, id => files.raw(sessionId, id), artifact.updated_at).then(
      t => { if (live) { keep({kind: 'text', text: t}, t.length * 2); setSource(t); setObserved(artifact.updated_at); setFailed(false) } },
      () => { console.warn('artifact_visual_preview_failed', { cause: 'read_rejected' }); if (live) setFailed(true) },
    )
    return () => { live = false }
  }, [near, sessionId, artifact.id, artifact.updated_at, identity, files])

  return (
    <div ref={ref} className="artifact-preview-diagram" data-testid="artifact-preview-diagram">
      {artifact.live && <p className="artifact-status" title={unsupported ? 'unsupported-live: native data updates unavailable; redraws each file revision' : undefined}>Live · updated <TimeLabel ts={observed} /></p>}
      {failed && <p className="artifact-status" role="status">Couldn’t load this preview. Open the file to retry.</p>}
      {source === null ? failed ? null : <span className="artifact-preview-loading" /> : <VisualBlock kind={kind} source={source.replace(/\n$/, '')} fileContext={{sessionId, files}} hostedTab />}
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
    loadDiffPreview(sessionId, artifact.id, id => files.raw(sessionId, id), artifact.updated_at).then(
      d => { if (live) setData(d) },
      () => { if (live) setFailed(true) },
    )
    return () => { live = false }
  }, [near, sessionId, artifact.id, artifact.updated_at, files])

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
    loadPdfThumb(sessionId, artifact.id, id => files.raw(sessionId, id), artifact.updated_at).then(
      u => { if (live) setUrl(u) },
      () => { if (live) setFailed(true) },
    )
    return () => { live = false }
  }, [near, sessionId, artifact.id, artifact.updated_at, files])

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
  // Fetched once per revision, not once ever: a live file's id never changes, but its
  // updated_at does on every republish, and that must still trigger a fresh read (BLG-470).
  const fetchedRev = useRef<string | null>(null)

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
    const rev = `${artifact.id}:${artifact.updated_at ?? ''}`
    if (!near || fetchedRev.current === rev) return
    fetchedRev.current = rev
    let live = true
    setHtml(null)
    setFailed(false)
    loadPreviewText(sessionId, artifact.id, id => files.raw(sessionId, id), artifact.updated_at).then(
      t => { if (live) setHtml(t) },
      () => { if (live) setFailed(true) },
    )
    return () => { live = false }
  }, [near, sessionId, artifact.id, artifact.updated_at, files])

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
  const cardRef = useRef<HTMLDivElement | null>(null)
  const files = chat?.files ?? null
  const [error, setError] = useState<{ message: string; errorId?: string } | null>(null)
  const [busy, setBusy] = useState(false)
  const [videoFailed, setVideoFailed] = useState(false)
  const [videoGeneration, setVideoGeneration] = useState(0)
  const videoError = useCallback(() => setVideoFailed(true), [])
  // A live file's card is never re-created on republish (ChatView dedupes its events): refresh
  // its size, content type and updated_at from the session's current list instead of the stale
  // creation-event payload, and recompute only when those fields actually change.
  const live = payload.live ? chat?.liveArtifact?.(payload.id) : undefined
  const artifact = useMemo<ArtifactPayload>(
    () => live ? { ...payload, size: live.size, content_type: live.content_type, updated_at: live.updated_at } : payload,
    // eslint-disable-next-line react-hooks/exhaustive-deps -- only live's own fields, not its object identity: an unrelated list refresh must not force every live card's previews to refetch
    [payload, live?.size, live?.content_type, live?.updated_at],
  )
  const version = artifact.version
  const latest = chat?.latestVersion(artifact) ?? artifact.latest_version ?? version
  const showVideo = artifact.view === 'video' && (version ?? 1) >= (latest ?? 1) && !videoFailed
  const showVersion = !artifact.live && version !== undefined && (version > 1 || (latest ?? 1) > 1)

  function open() {
    if (artifact.view === 'video') {
      cardRef.current?.querySelector('video')?.pause()
      // Cancel pending inline playback too, before the viewer reads the same clip.
      setVideoGeneration(n => n + 1)
    }
    chat?.view(artifact)
  }

  async function download() {
    if (!chat || !files) return
    setBusy(true)
    setError(null)
    try {
      await files.download(chat.sessionId, artifact.id)
    } catch (e) {
      setError(fileErrorText(e, 'Download failed.'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div ref={cardRef} className="agent-card artifact-card" data-testid="artifact-card" data-event-id={eventId}>
      {chat && files && showVideo && <VideoPreview key={`${artifact.id}:${videoGeneration}`} sessionId={chat.sessionId} files={files} artifact={artifact} onFailed={videoError} />}
      {chat && files && artifact.view !== 'video' && <MediaPreview sessionId={chat.sessionId} files={files} artifact={artifact} onOpen={autoplay => chat.view(artifact, { autoplay })} />}
      <div className="artifact-card-row">
        <FileIcon name={artifact.name} className="artifact-card-icon" />
        <span className="artifact-card-name">{artifact.name}</span>
        {showVersion && <>{' '}<span className="artifact-card-version" data-testid="artifact-card-version">· v{version}</span></>}
        {' '}<span className="artifact-card-size">· {formatSize(artifact.size)}</span>
        {chat && files && (
          <span className="artifact-card-actions">
            <button type="button" disabled={busy} onClick={() => void download()}>Download</button>
            {isViewable(artifact.view) && (
              <button type="button" onClick={open}>{artifact.view === 'video' ? (showVideo ? 'Open larger' : 'Open') : 'View'}</button>
            )}
          </span>
        )}
        {artifact.live
          ? <span className="artifact-card-updated card-time card-time-end" data-testid="artifact-card-updated"><TimeLabel ts={artifact.updated_at ?? ts} /></span>
          : <TimeLabel ts={ts} className="card-time-end" />}
      </div>
      {error && <div className="artifact-error" role="alert">{error.message}<ErrorId id={error.errorId} /></div>}
    </div>
  )
}
