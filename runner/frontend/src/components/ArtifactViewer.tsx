// ArtifactViewer: shows a published file inside the app. The bytes come from the authenticated
// /raw route (apiFetch, so with the bearer), and what is done with them depends on the file's
// `view` (lib/artifactViews.ts):
//
//   text / json / markdown / csv   read as TEXT and drawn by React (escaped): <pre>, the chat's
//                                  Markdown component (no raw HTML), a table. Capped.
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
import { apiFetch } from '../apiFetch'
import {
  artifactRawUrl,
  downloadArtifact,
  formatSize,
  parseCsv,
  prettyJson,
  withCsp,
  type ArtifactInfo,
} from '../lib/artifacts'
import {
  ARTIFACT_VIEWS,
  CSV_MAX_COLS,
  CSV_MAX_ROWS,
  TEXT_PREVIEW_CAP,
  TEXT_VIEW_MAX,
  blobTypeFor,
  isViewable,
  tooBigToPreview,
} from '../lib/artifactViews'
import { useBackdropClose } from '../hooks/useBackdropClose'
import Markdown from './Markdown'
import './Artifacts.css'

type Loaded =
  | { kind: 'text'; text: string }
  | { kind: 'blob'; url: string }
  | { kind: 'error' }

interface Props {
  sessionId: string
  artifact: ArtifactInfo
  onClose: () => void
  /** The newest version of this file, when `artifact` is an older one and the list has it. */
  latest?: ArtifactInfo
  /** Opens another version in the viewer ("View latest"). */
  onOpen?: (artifact: ArtifactInfo) => void
}

export default function ArtifactViewer({ sessionId, artifact, onClose, latest, onOpen }: Props) {
  const titleId = useId()
  const dialogRef = useRef<HTMLDivElement | null>(null)
  const backdrop = useBackdropClose(onClose)
  const [loaded, setLoaded] = useState<Loaded | null>(null)
  const [showAll, setShowAll] = useState(false)
  const [downloadError, setDownloadError] = useState<string | null>(null)

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
    if (!previewable || !spec) return
    let cancelled = false
    let url: string | null = null
    void (async () => {
      try {
        const r = await apiFetch(artifactRawUrl(sessionId, artifact.id))
        if (!r.ok) throw new Error(`HTTP ${r.status}`)
        if (spec.textual) {
          const text = await r.text()
          if (!cancelled) setLoaded({ kind: 'text', text })
          return
        }
        const buf = await r.arrayBuffer()
        if (cancelled) return
        url = URL.createObjectURL(new Blob([buf], { type: blobTypeFor(view, artifact.content_type) }))
        setLoaded({ kind: 'blob', url })
      } catch {
        if (!cancelled) setLoaded({ kind: 'error' })
      }
    })()
    return () => {
      cancelled = true
      if (url) URL.revokeObjectURL(url)
    }
  }, [sessionId, artifact.id, artifact.content_type, view, previewable, spec])

  async function download() {
    setDownloadError(null)
    try {
      await downloadArtifact(sessionId, artifact)
    } catch (e) {
      setDownloadError(e instanceof Error ? e.message : 'Download failed.')
    }
  }

  function onKeyDown(e: React.KeyboardEvent) {
    if (e.key === 'Escape') {
      e.stopPropagation()
      onClose()
    }
  }

  return createPortal(
    <div className="artifact-overlay artifact-overlay-viewer" data-testid="artifact-viewer-overlay" {...backdrop}>
      <div
        ref={dialogRef}
        className="artifact-dialog artifact-viewer"
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
          <button type="button" className="artifact-btn" onClick={() => void download()}>Download</button>
          <button type="button" className="artifact-close" aria-label="Close" onClick={onClose}>✕</button>
        </header>
        {isOlder && (
          <p className="artifact-older" data-testid="artifact-older">
            This is an older version.
            {latest && onOpen && <> <button type="button" className="artifact-link" onClick={() => onOpen(latest)}>View latest</button></>}
          </p>
        )}
        {downloadError && <div className="artifact-error" role="alert">{downloadError}</div>}
        <div className="artifact-body">
          {!previewable ? (
            <NoPreview artifact={artifact} tooBig={tooBig} />
          ) : loaded === null ? (
            <p className="artifact-status" data-testid="artifact-loading">Loading…</p>
          ) : loaded.kind === 'error' ? (
            <p className="artifact-status" data-testid="artifact-error">
              Couldn’t load this file. It may have been deleted — you can still try Download.
            </p>
          ) : loaded.kind === 'blob' ? (
            <BlobView artifact={artifact} url={loaded.url} />
          ) : (
            <TextView artifact={artifact} text={loaded.text} showAll={showAll} onShowAll={() => setShowAll(true)} />
          )}
        </div>
      </div>
    </div>,
    document.body,
  )
}

function NoPreview({ artifact, tooBig }: { artifact: ArtifactInfo; tooBig: boolean }) {
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

function BlobView({ artifact, url }: { artifact: ArtifactInfo; url: string }) {
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
      return <audio className="artifact-media" controls src={url} />
    case 'video':
      return <video className="artifact-media" controls src={url} />
    default:
      return null
  }
}

function TextView({ artifact, text, showAll, onShowAll }: { artifact: ArtifactInfo; text: string; showAll: boolean; onShowAll: () => void }) {
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

  const body = artifact.view === 'json' ? prettyJson(text) : text
  const capped = !showAll && body.length > TEXT_PREVIEW_CAP
  const shown = capped ? body.slice(0, TEXT_PREVIEW_CAP) : body
  return (
    <>
      {artifact.view === 'markdown'
        ? <Markdown text={shown} className="artifact-markdown" />
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
  const [head, ...rest] = rows
  return (
    <>
      <div className="artifact-table-wrap" tabIndex={0}>
        <table className="artifact-table" data-testid="artifact-table">
          <thead>
            <tr>{head.map((c, i) => <th key={i} scope="col">{c}</th>)}</tr>
          </thead>
          <tbody>
            {rest.map((r, i) => <tr key={i}>{r.map((c, j) => <td key={j}>{c}</td>)}</tr>)}
          </tbody>
        </table>
      </div>
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
