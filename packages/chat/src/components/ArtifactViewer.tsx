import {liveFileSupport} from '../visual/liveFile'
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
import { useCallback, useEffect, useLayoutEffect, useId, useRef, useState, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { HostedViewerRequest, useViewerHost } from './ViewerHost'
import { formatSize, parseCsv, prettyJson, withCsp } from '../model/artifacts'
import { TimeLabel } from './TimeLabel'
import { langFromName } from '../model/codeLang'
import {
  ARTIFACT_VIEWS,
  CSV_MAX_COLS,
  CSV_MAX_ROWS,
  TEXT_PREVIEW_CAP,
  TEXT_VIEW_MAX,
  blobTypeFor,
  keptMediaUrl,
  isSvgImage,
  mediaUrl,
  releaseMediaUrl,
  isViewable,
  tooBigToPreview,
} from '../model/artifactViews'
import { useBackdropClose } from '../hooks/useBackdropClose'
import { useSelectAllIn } from '../hooks/useSelectAllIn'
import { composeMarkupMessage } from '../model/feedback'
import { MAXIMIZED_KEY, isMaximizeShortcut, localStorageChatStorage, type ViewerLayoutKind as LayoutKind } from '../model/storage'
import { FILE_GONE_MESSAGE, FileRequestError, fileErrorText, isArtifactGone, saveBlob } from '../transport/files'
import type { ArtifactInfo, TransportFiles } from '../transport/types'
import { useChat } from './context'
import { artifactContentIdentity as artifactIdentity, contentKeeper, peekContent } from '../model/viewCache'
import CodeLines from './CodeLines'
import DataTable from './DataTable'
import ErrorId from './ErrorId'
import SourceCode from './SourceCode'
import ViewerToolbar, { ViewerActionsContext, ViewerAction, WrapToggle } from './ViewerToolbar'
import { WrapHostContext, WrapScopeContext, isWrapShortcut, toggleWrapIn, wrapKind, type WrapTarget } from '../model/lineWrap'
import { hasRenderedText } from '../model/rawView'
import DiffView from './DiffView'
import Markdown, { type MarkdownDocument } from './Markdown'
import VisualBlock from './VisualBlock'
import { parseVegaLite } from '../visual/policy'
import { parseDashboard } from '../visual/dashboard'
import { renderVisual, type RenderedVisual } from '../visual/render'
import { DASHBOARD_EXPORT_BLOCKED, payloadExportAllowed, exportPng, exportSvg, exportedAtLowerResolution } from '../visual/visualExport'
import { readVisualTheme } from '../visual/theme'
import { visualFile, type VisualKind } from '../model/visualKind'
import MarkupMode from './markup/MarkupMode'
import ImageView from './ImageView'
import ReviewMode from './review/ReviewMode'
import ReviewRequestBar from './ReviewRequestBar'
import AnswerPage from './AnswerPage'
import VersionComparison from './VersionComparison'

type Loaded =
  | { kind: 'text'; text: string }
  | { kind: 'blob'; url: string; blob: Blob }
  | { kind: 'error'; message?: string; errorId?: string }
  // The server still lists the file but no longer has its bytes (410, or the list said missing).
  | { kind: 'gone' }

/** What an artifact view keeps (model/viewCache): a version's bytes never change, so a kept one is
 *  shown and not read again. An SVG keeps the data: URL it drew from. */
type KeptArtifact = { kind: 'text'; text: string } | { kind: 'blob'; blob: Blob; dataUrl?: string }
// A live file's version never moves, so its own updated_at (BLG-470) has to be part of what
// counts as "changed content" here too — otherwise an in-place republish that happens to keep the
// same byte size would not force the remount that picks up its freshly fetched bytes.

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
  if (props.embedded && !props.review) return <FollowingArtifact key={`${props.sessionId}:${/\.(?:vl|vega-lite|dash)\.json$/i.test(props.artifact.name) ? props.artifact.id : artifactIdentity(props.artifact)}`} {...props} />
  return <ArtifactViewerSession key={`${props.sessionId}:${props.artifact.id}:${props.artifact.view}:${props.artifact.version ?? ''}:${props.review ? 'review' : 'view'}`} {...props}/>
}

function FollowingArtifact(props: Props) {
  const isLive = props.artifact.live === true
  const followKey = `blerg.viewer.follow.${props.sessionId}.${props.artifact.origin === 'user' ? 'user' : 'agent'}.${props.artifact.name}`
  const [following, setFollowing] = useState(() => isLive || localStorageChatStorage.get(followKey) !== '0')
  const [shown, setShown] = useState<{ artifact: ArtifactInfo; text?: string }>({ artifact: props.artifact })
  const [failed, setFailed] = useState(false)
  const [snapshotting, setSnapshotting] = useState(false)
  const [snapshotMsg, setSnapshotMsg] = useState<string | null>(null)
  const candidate = props.latest ?? (isLive ? props.artifact : undefined)
  const latest = candidate?.name === props.artifact.name && (candidate.origin === 'user') === (props.artifact.origin === 'user') ? candidate : undefined
  const [versions, setVersions] = useState<ArtifactInfo[]>([])
  useEffect(() => {
    if (typeof props.files.list !== 'function') return
    let live = true
    void props.files.list(props.sessionId).then(items => {
      if (live) setVersions(items.filter(a => a.name === props.artifact.name && (a.origin === 'user') === (props.artifact.origin === 'user') && typeof a.version === 'number'))
    }, () => { /* Without the list, the toolbar still names the shown version when it can. */ })
    return () => { live = false }
  }, [props.files, props.sessionId, props.artifact.name, props.artifact.origin, latest?.id, shown.artifact.id])
  // A live file (BLG-470) keeps one id: its own republish is not a newer version, it is the same
  // artifact with a later updated_at, so the ordinary version check (which requires the id to
  // differ, or the version to have moved on) never fires for it. live.id === shown.id with a
  // moved updated_at is treated the same way a newer version would be.
  const liveUpdate = isLive && latest && latest.id === shown.artifact.id &&
    latest.updated_at !== undefined && latest.updated_at !== shown.artifact.updated_at
  useEffect(() => {
    if (!following || !latest ||
      (!liveUpdate && (latest.id === shown.artifact.id || (latest.version ?? 0) <= (shown.artifact.version ?? 0))) ||
      latest.name !== props.artifact.name ||
      (latest.origin === 'user') !== (props.artifact.origin === 'user') ||
      latest.view !== props.artifact.view) return
    let cancelled = false
    const controller = new AbortController()
    // Coalesce publication bursts; cleanup fences both reads and text conversion.
    const timer = setTimeout(() => {
      void (async () => {
        try {
          if (latest.missing || tooBigToPreview(latest.view, latest.size)) throw new Error()
          if (!isViewable(latest.view)) { setShown({ artifact: latest }); setFailed(false); return }
          const blob = await props.files.raw(props.sessionId, latest.id)
          const textual = isViewable(latest.view) && ARTIFACT_VIEWS[latest.view].textual
          if (textual && blob.size > TEXT_VIEW_MAX) throw new Error()
          const text = textual ? await blob.text() : undefined
          if (cancelled) return
          const kind = text === undefined ? null : visualFile(latest.name, latest.view, text)
          if (kind === 'vega-lite') parseVegaLite(text!)
          else if (kind === 'dashboard') parseDashboard(text!)
          else if (kind) {
            // Validate and draw charts away from the visible preview before replacing it.
            const target = document.createElement('div')
            target.style.cssText = 'position:fixed;visibility:hidden;pointer-events:none;width:800px'
            document.body.append(target)
            try {
              const visual = await renderVisual(kind, text!, target, undefined, controller.signal)
              visual.dispose()
            } finally { target.remove() }
          }
          const dataUrl = isSvgImage(latest.view, latest.content_type) ? await mediaUrl(blob, latest.view, latest.content_type) : undefined
          if (cancelled) return
          if (text !== undefined) contentKeeper<KeptArtifact>(props.files, props.sessionId, artifactIdentity(latest))({ kind: 'text', text }, text.length * 2)
          else contentKeeper<KeptArtifact>(props.files, props.sessionId, artifactIdentity(latest))({ kind: 'blob', blob, dataUrl }, blob.size + (dataUrl?.length ?? 0) * 2)
          setShown({ artifact: latest, text })
          setFailed(false)
        } catch (error) {
          if (!cancelled) {
            console.warn('Artifact replacement failed', {cause: error instanceof Error ? error.name : 'UnknownError'})
            setFailed(true)
          }
        }
      })()
    }, 250)
    return () => { cancelled = true; controller.abort(); clearTimeout(timer) }
  }, [following, latest, liveUpdate, props.artifact.name, props.artifact.origin, props.artifact.view, props.files, props.sessionId, shown.artifact])
  const options = [...versions.filter(v => v.id !== shown.artifact.id), shown.artifact].sort((a, b) => (a.version ?? 1) - (b.version ?? 1))
  const newest = Math.max(...options.map(v => v.version ?? 1), shown.artifact.latest_version ?? 0, latest?.version ?? 0)
  function pick(id: string) {
    const version = options.find(v => v.id === id)
    if (!version) return
    if ((version.version ?? 1) < newest) { localStorageChatStorage.set(followKey, '0'); setFollowing(false) }
    setFailed(false)
    setShown({ artifact: version })
  }
  async function snapshot() {
    if (!props.files.snapshot || snapshotting) return
    setSnapshotting(true)
    setSnapshotMsg(null)
    try {
      const saved = await props.files.snapshot(props.sessionId, shown.artifact.id)
      setSnapshotMsg(`Saved as ${saved.name}.`)
    } catch (e) {
      setSnapshotMsg(fileErrorText(e, 'Could not take a snapshot.').message)
    } finally {
      setSnapshotting(false)
    }
  }
  // A live file has no versions to pick or pin: it always follows its own content, and its
  // header offers Snapshot (a normal versioned copy of what it holds right now) instead.
  const actions = <>
    {!isLive && newest > 1 && <select className="viewer-version-select" aria-label="Version" title="Version" value={shown.artifact.id} onChange={e => pick(e.target.value)}>
      {options.map(v => <option key={v.id} value={v.id}>v{v.version ?? 1}{(v.version ?? 1) === newest ? ' (latest)' : ''}</option>)}
    </select>}
    {!isLive && <ViewerAction icon="follow" label={following ? 'Pin version' : 'Follow latest'} title={following ? 'Following latest · click to pin this version' : 'Follow latest'} pressed={following} onClick={() => { localStorageChatStorage.set(followKey, following ? '0' : '1'); setFollowing(!following); setFailed(false) }} />}
    {isLive && props.files.snapshot && <ViewerAction icon="save" label={snapshotting ? 'Saving…' : 'Snapshot'} title="Save this file's current content as a normal versioned file, named with the time" disabled={snapshotting} onClick={() => void snapshot()} />}
    {props.headerActions}
  </>
  return <>
    {failed && <div className="artifact-status" role="status">Could not update preview. The previous version is still shown.</div>}
    {snapshotMsg && <div className="artifact-status" role="status">{snapshotMsg}</div>}
    <ArtifactViewerSession key={/\.(?:vl|vega-lite|dash)\.json$/i.test(shown.artifact.name) ? shown.artifact.id : artifactIdentity(shown.artifact)} {...props} artifact={shown.artifact} initialText={shown.text} onFeedback={() => setFollowing(false)} headerActions={actions} versionUpdate={!isLive && !following && latest && (latest.version ?? 0) > (shown.artifact.version ?? 0) ? latest : undefined} onVersionUpdate={() => { if (latest) setShown({ artifact: latest }) }} />
  </>
}

function ArtifactViewerSession({ sessionId, files, artifact, onClose, latest, onOpen, autoplay = false, commit = null, review = false, embedded = false, headerActions, initialText, versionUpdate, onVersionUpdate, onFeedback }: Props & { initialText?: string; versionUpdate?: ArtifactInfo; onVersionUpdate?: () => void; onFeedback?: () => void }) {
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
  const [fetched, setLoaded] = useState<Loaded | null>(() => initialText !== undefined ? { kind: 'text', text: initialText } : restored?.loaded ?? null)
  const loaded: Loaded | null = initialText !== undefined ? {kind: 'text', text: initialText} : fetched
  // An object URL made for a kept file, freed when the view goes.
  useEffect(() => () => { if (restored?.loaded.kind === 'blob') releaseMediaUrl(restored.loaded.url) }, [restored])
  const [raw, setRaw] = useState(false)
  const [comparing, setComparing] = useState(false)
  const [comparisonHost, setComparisonHost] = useState<HTMLSpanElement | null>(null)
  // The kind of code on show, for the wrap toggle; null while the file is rendered, not code.
  const [codeWrap, setCodeWrap] = useState<WrapTarget | null>(null)
  const support = artifact.live && loaded?.kind === 'text' ? liveFileSupport(visualFile(artifact.name, artifact.view, loaded.text), loaded.text) : undefined
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
    if (initialText !== undefined || artifact.missing || !previewable || !spec) return // a file listed as missing is not fetched
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
      if (loaded?.kind === 'blob') {
        saveBlob(loaded.blob, artifact.name)
        return
      }
      if (loaded?.kind === 'text') {
        saveBlob(new Blob([loaded.text], {type: artifact.content_type || 'text/plain'}), artifact.name)
        return
      }
      await files.download(sessionId, artifact.id)
    } catch (e) {
      console.warn('content_download_failed', { cause: e instanceof FileRequestError ? 'file_request_rejected' : 'browser_export_rejected' })
      if (isArtifactGone(e)) setLoaded({ kind: 'gone' })
      else setDownloadError(fileErrorText(e, 'Download failed.'))
    }
  }

  // A chart or diagram's current drawing, fed by TextView/VisualBlock's onRendered — null while
  // drawing, on a theme-triggered redraw, or after a failure, so export never uses a stale one.
  const visualKind: VisualKind | null = loaded?.kind === 'text' ? visualFile(artifact.name, view, loaded.text) : null
  const [rendered, setRendered] = useState<RenderedVisual | null>(null)
  const revisionSource=loaded?.kind==='text'?loaded.text:undefined
  const revision = useRef({at:artifact.updated_at,source:revisionSource})
  useLayoutEffect(() => { revision.current = {at:artifact.updated_at,source:revisionSource} }, [artifact.updated_at,revisionSource])
  const [drawnAt,setDrawnAt] = useState(artifact.updated_at)
  const committedRevisionSource = useRef<string | undefined>(undefined)
  useLayoutEffect(() => {
    if (revisionSource !== undefined && revisionSource === committedRevisionSource.current) setDrawnAt(artifact.updated_at)
  }, [artifact.updated_at, revisionSource])
  const [runtimeUnsupported,setRuntimeUnsupported] = useState(false)
  useLayoutEffect(() => {
    const node=dialogRef.current
    const support=()=>{
      if(artifact.live){const unsupported=[...node!.querySelectorAll<HTMLElement>('.visual-surface')].some(surface=>surface.dataset.liveSupport!=='true');setRuntimeUnsupported(unsupported)}
    }
    const committed=(event:Event)=>{const source=(event as CustomEvent<{source:string}>).detail.source;queueMicrotask(()=>{if(source===revision.current.source){committedRevisionSource.current=source;setDrawnAt(revision.current.at)}})}
    node?.addEventListener('blerg-dashboard-committed',committed)
    node?.addEventListener('blerg-chart-live-support',support)
    return ()=>{node?.removeEventListener('blerg-chart-live-support',support);node?.removeEventListener('blerg-dashboard-committed',committed)}
  },[artifact.live])
  const onVisualRendered = useCallback((visual: RenderedVisual | null, committedSource?: string) => {
    setRendered(visual)
    if(visual){
      // Child layout commits can precede this viewer's revision layout effect.
      queueMicrotask(() => {if(committedSource===undefined || committedSource===revision.current.source){committedRevisionSource.current=revision.current.source;setDrawnAt(revision.current.at)}})
    }
  }, [setRendered, setDrawnAt])
  const [exportError, setExportError] = useState<string | null>(null)
  const [exportNotice, setExportNotice] = useState<string | null>(null)
  const exportName = artifact.name.replace(/\.[^./]+$/, '')

  function downloadSvg() {
    if (!rendered || !payloadExportAllowed(visualKind)) return
    setExportError(null)
    try { saveBlob(exportSvg(rendered), `${exportName}.svg`) }
    catch { console.warn('visual_export_failed', { cause: 'svg_export_rejected' }); setExportError('Could not export the SVG.') }
  }
  async function downloadPng() {
    if (!rendered || !payloadExportAllowed(visualKind)) return
    setExportError(null)
    setExportNotice(null)
    try {
      const theme = readVisualTheme(dialogRef.current ?? document.body)
      const blob = await exportPng(rendered, theme)
      saveBlob(blob, `${exportName}.png`)
      if (exportedAtLowerResolution(rendered)) setExportNotice('Exported at a lower resolution to stay within size limits.')
    } catch { console.warn('visual_export_failed', { cause: 'png_export_rejected' }); setExportError('Could not export the PNG.') }
  }

  // Which feedback mode this file takes, once its bytes are here.
  const gone = artifact.missing === true || loaded?.kind === 'gone'
  const ready = loaded !== null && loaded.kind !== 'error' && !gone
  // A file tab opens on the patch; its review conversation is an explicit toolbar action.
  const inlineReview = view === 'diff' && !!chat && ready && (!embedded || mode === 'review')

  // Maximize: edge to edge, remembered per kind of layout (a review, a diff, a draw-over, a file).
  const layoutKind: LayoutKind = view === 'diff' || comparing ? 'diff' : mode === 'review' ? 'review' : mode === 'markup' ? 'markup' : 'file'
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
    // button-guard-ignore: useBackdropClose supplies pointer handlers only; this overlay is not a button
    <div className={embedded ? "embedded-viewer" : `artifact-overlay artifact-overlay-viewer${maximized ? ' is-maximized' : ''}`} data-testid="artifact-viewer-overlay" {...(embedded ? {} : backdrop)}>
      <div
        ref={dialogRef}
        className={`artifact-dialog artifact-viewer${mode !== 'view' || inlineReview ? ' is-review' : ''}${view === 'diff' || comparing ? ' is-diff' : ''}${maximized ? ' is-maximized' : ''}`}
        role={embedded ? "region" : "dialog"}
        aria-modal={embedded ? undefined : true}
        aria-labelledby={titleId}
        tabIndex={-1}
        onKeyDown={onKeyDown}
      >
        <ViewerToolbar exports={<>
          {visualKind && visualKind !== 'dashboard' && <>
            <ViewerAction icon="downloadSvg" label="Download SVG" disabled={!rendered || !payloadExportAllowed(visualKind)} onClick={downloadSvg} />
            <ViewerAction icon="downloadPng" label="Download PNG" disabled={!rendered || !payloadExportAllowed(visualKind)} onClick={() => void downloadPng()} />
          </>}
          {!gone && <ViewerAction icon="download" label={visualKind ? 'Download source' : 'Download'} onClick={() => void download()} />}
        </>} primary={comparing || inlineReview && !embedded ? null : mode !== 'view' ? <ViewerAction icon="back" label="Back" onClick={() => setMode('view')} />
            : feedback && <ViewerAction icon={feedback === 'review' ? 'review' : 'markup'} label={feedback === 'review' ? 'Review' : 'Mark up'} onClick={() => { onFeedback?.(); setMode(feedback) }} />} printDisabled={gone || !loaded || visualKind === 'dashboard' || !!visualKind && !rendered} titleId={titleId} title={versionUpdate || embedded && artifact.name.includes('/') ? artifact.name : title} showPath={!embedded || artifact.name.includes("/")} copyValue={artifact.name} meta={['From Files', spec?.label, formatSize(artifact.size)].filter(Boolean).join(' · ')} toggle={!comparing && loaded?.kind === 'text' && hasRenderedText(artifact.name, artifact.view, loaded.text) && <ViewerAction icon="raw" label={raw ? 'Rendered' : 'Raw'} title={raw ? 'Show rendered file' : 'Show raw source'} pressed={raw} onClick={() => setRaw(!raw)} />}>
          {versionUpdate && <button type="button" className="viewer-version" title={`v${versionUpdate.version} is available`} aria-label={`v${versionUpdate.version} is available`} onClick={onVersionUpdate}><span className="viewer-version-dot" aria-hidden="true" /></button>}
          <span className="viewer-control-slot" ref={setComparisonHost} />
          {!comparing && codeWrap && <WrapToggle kind={codeWrap.kind} available={codeWrap.available} />}
          {headerActions}
          {!embedded && <>
            <ViewerAction icon={maximized ? 'restore' : 'maximize'} label={maximized ? 'Restore size' : 'Maximize'} title={`${maximized ? 'Restore size' : 'Maximize'} (Ctrl+Alt+F)`} pressed={maximized} onClick={toggleMaximized} />
            <ViewerAction icon="close" label="Close" onClick={onClose} />
          </>}
          {artifact.live && <span className="viewer-live-updated" data-testid="viewer-live-updated"><span title={support === 'unsupported-live' || runtimeUnsupported ? 'unsupported-live: native data updates unavailable; redraws each file revision' : undefined}>Live · updated </span><TimeLabel ts={visualKind === 'vega-lite' || visualKind === 'dashboard' ? drawnAt : artifact.updated_at} /></span>}
        </ViewerToolbar>
        {isOlder && !versionUpdate && !embedded && (
          <p className="artifact-older" data-testid="artifact-older">
            This is an older version.
            {latest && onOpen && <> <button type="button" className="artifact-link" onClick={() => onOpen(latest)}>View latest</button></>}
          </p>
        )}
        <ReviewRequestBar artifact={artifact} hideAsk={inlineReview && reviewTab === 'conversation'} />
        {downloadError && <div className="artifact-error" role="alert">{downloadError.message}<ErrorId id={downloadError.errorId} /></div>}
        {exportError && <div className="artifact-error" role="alert">{exportError}</div>}
        {visualKind === 'dashboard' && <p className="artifact-status" role="status">{DASHBOARD_EXPORT_BLOCKED}</p>}
        {exportNotice && <p className="artifact-status" role="status">{exportNotice}</p>}
        <VersionComparison artifact={artifact} files={files} sessionId={sessionId} currentText={loaded?.kind === 'text' ? loaded.text : undefined} active={comparing} onActive={setComparing} controlsHost={comparisonHost} />
        <ViewerActionsContext.Provider value={comparing ? null : comparisonHost}><div className="artifact-body" hidden={comparing}>
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
            artifact.view === 'html' && artifact.origin !== 'user' ? <AnswerPage key={artifact.id} sessionId={sessionId} artifactId={artifact.id} name={artifact.name} text={loaded.text} files={files} /> : <TextView artifact={artifact} text={loaded.text} showAll={showAll} onShowAll={() => setShowAll(true)} commit={commit} fileContext={{ sessionId, files }} onRendered={onVisualRendered} />
          )}
        </div></ViewerActionsContext.Provider>
      </div>
    </div>
  )
  const scoped = <WrapScopeContext.Provider value={files.queueScope?.() ?? ''}><WrapHostContext.Provider value={setCodeWrap}><ViewerActionsContext.Provider value={comparisonHost}>{body}</ViewerActionsContext.Provider></WrapHostContext.Provider></WrapScopeContext.Provider>
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

export function TextView({ artifact, text, showAll, onShowAll, commit, fileContext, document: doc = PUBLISHED_DOCUMENT, onRendered }: { fileContext?: { sessionId: string; files: TransportFiles }; artifact: ArtifactInfo; text: string; showAll: boolean; onShowAll: () => void; commit?: string | null; document?: MarkdownDocument; onRendered?: (visual: RenderedVisual | null, committedSource?:string) => void }) {
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
  if (artifact.view === 'diff') return <DiffView text={text} commit={commit} fileContext={fileContext} />

  const visual = visualFile(artifact.name, artifact.view, text)
  if (visual) return <VisualBlock kind={visual} source={text} fileContext={fileContext} onRendered={onRendered} hostedTab />

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
