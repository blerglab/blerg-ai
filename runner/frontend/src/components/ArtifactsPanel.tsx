// ArtifactsPanel: every file the session holds, newest first. A file with several versions (the
// agent, or you, added the same name again) is ONE row showing its latest version, with the earlier
// ones behind an "Earlier versions" disclosure. Each version can be downloaded, viewed (when the app
// can show that type) or deleted on its own (after an in-page confirmation).
import { useEffect, useId, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { deleteArtifact, downloadArtifact, fileCount, formatSize, groupArtifacts, type ArtifactGroup, type ArtifactInfo } from '../lib/artifacts'
import { ARTIFACT_VIEWS, isViewable } from '../lib/artifactViews'
import { useBackdropClose } from '../hooks/useBackdropClose'
import type { ArtifactsStatus } from '../hooks/useArtifacts'
import { TimeLabel } from './TimeLabel'
import './Artifacts.css'

interface Props {
  sessionId: string
  items: ArtifactInfo[]
  status: ArtifactsStatus
  onClose: () => void
  onView: (artifact: ArtifactInfo) => void
  /** Called after a delete succeeded, so the list is read again. */
  onChanged: () => void
  /** Opens the file picker of the message box, so the empty state can offer it. */
  onAttach?: () => void
}

export default function ArtifactsPanel({ sessionId, items, status, onClose, onView, onChanged, onAttach }: Props) {
  const titleId = useId()
  const dialogRef = useRef<HTMLDivElement | null>(null)
  const backdrop = useBackdropClose(onClose)
  const [confirming, setConfirming] = useState<string | null>(null)
  const [busy, setBusy] = useState<string | null>(null)
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [expanded, setExpanded] = useState<Set<string>>(() => new Set())

  useEffect(() => dialogRef.current?.focus(), [])

  const fail = (id: string, msg: string) => setErrors(prev => ({ ...prev, [id]: msg }))
  const clear = (id: string) => setErrors(prev => {
    const { [id]: _gone, ...rest } = prev
    void _gone
    return rest
  })
  const toggle = (key: string) => setExpanded(prev => {
    const next = new Set(prev)
    if (!next.delete(key)) next.add(key)
    return next
  })

  async function download(a: ArtifactInfo) {
    clear(a.id)
    try {
      await downloadArtifact(sessionId, a)
    } catch (e) {
      fail(a.id, e instanceof Error ? e.message : 'Download failed.')
    }
  }

  async function remove(a: ArtifactInfo) {
    clear(a.id)
    setBusy(a.id)
    try {
      await deleteArtifact(sessionId, a.id)
      setConfirming(null)
      onChanged()
    } catch {
      fail(a.id, 'Couldn’t delete this file. Try again.')
    } finally {
      setBusy(null)
    }
  }

  function onKeyDown(e: React.KeyboardEvent) {
    if (e.key === 'Escape') {
      e.stopPropagation()
      onClose()
    }
  }

  // The name, size, kind and time of one version, and its Download / View / Delete.
  const details = (a: ArtifactInfo, g: ArtifactGroup, badge: boolean) => {
    const versioned = g.earlier.length > 0 || (a.version ?? 1) > 1
    return (
      <>
        <div className="artifact-row-main">
          <span className="artifact-row-name" data-testid="artifact-name" title={a.name}>{a.name}</span>
          {badge && <span className="artifact-version" data-testid="artifact-version">v{a.version}</span>}
          <span className="artifact-row-meta">
            {[formatSize(a.size), isViewable(a.view) ? ARTIFACT_VIEWS[a.view].label : 'Download only', ...(a.origin === 'user' ? ['uploaded by you'] : [])].join(' · ')}
            {a.created_at && <> · <TimeLabel ts={a.created_at} /></>}
          </span>
        </div>
        {confirming === a.id ? (
          <div className="artifact-row-actions">
            <span className="artifact-confirm">{versioned ? `Delete v${a.version ?? 1}?` : 'Delete this file?'}</span>
            <button type="button" className="artifact-btn danger" disabled={busy === a.id} onClick={() => void remove(a)}>Yes, delete</button>
            <button type="button" className="artifact-btn" onClick={() => setConfirming(null)}>Cancel</button>
          </div>
        ) : (
          <div className="artifact-row-actions">
            <button type="button" className="artifact-btn" onClick={() => void download(a)}>Download</button>
            {isViewable(a.view) && (
              <button type="button" className="artifact-btn" onClick={() => onView(a)}>View</button>
            )}
            <button type="button" className="artifact-btn" onClick={() => { clear(a.id); setConfirming(a.id) }}>Delete</button>
          </div>
        )}
        {errors[a.id] && <div className="artifact-error" role="alert">{errors[a.id]}</div>}
      </>
    )
  }

  const groupRow = (g: ArtifactGroup) => {
    const open = expanded.has(g.key)
    const listId = `${titleId}-${g.key.replace(/\W/g, '_')}`
    return (
      <li key={g.key} className="artifact-row" data-testid="artifact-row">
        {details(g.latest, g, g.earlier.length > 0 || (g.latest.version ?? 1) > 1)}
        {g.earlier.length > 0 && (
          <>
            <button
              type="button"
              className="artifact-earlier-toggle"
              aria-expanded={open}
              aria-controls={listId}
              onClick={() => toggle(g.key)}
            >
              <span className="artifact-earlier-caret" aria-hidden="true">{open ? '▾' : '▸'}</span>
              Earlier versions ({g.earlier.length})
            </button>
            {open && (
              <ul id={listId} className="artifact-versions" aria-label={`Earlier versions of ${g.name}`}>
                {g.earlier.map(a => (
                  <li key={a.id} className="artifact-version-row" data-testid="artifact-version-row">
                    {details(a, g, true)}
                  </li>
                ))}
              </ul>
            )}
          </>
        )}
      </li>
    )
  }

  let body: React.ReactNode
  if (status === 'loading' && items.length === 0) {
    body = <p className="artifact-status" data-testid="artifacts-loading">Loading…</p>
  } else if (status === 'error' && items.length === 0) {
    body = <p className="artifact-status" role="alert">Couldn’t load the files. Close this and try again.</p>
  } else if (items.length === 0) {
    body = (
      <div className="artifact-empty" data-testid="artifacts-empty">
        <svg className="artifact-empty-icon" viewBox="0 0 24 24" width="40" height="40" aria-hidden="true">
          <path
            d="M21 11.5 12.6 19.9a5 5 0 0 1-7.1-7.1l8.5-8.5a3.3 3.3 0 0 1 4.7 4.7l-8.5 8.5a1.7 1.7 0 0 1-2.4-2.4l7.8-7.8"
            fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round"
          />
        </svg>
        <h3 className="artifact-empty-title">No files yet</h3>
        <p className="artifact-empty-text">
          Files the agent publishes appear here. It can share one with <code>blerg-runner publish</code>.
        </p>
        <p className="artifact-empty-text">
          Files you attach to a message appear here too, and the agent can fetch them.
        </p>
        {onAttach && (
          <button type="button" className="artifact-empty-action" onClick={onAttach}>Attach files</button>
        )}
      </div>
    )
  } else {
    const groups = groupArtifacts(items)
    const uploaded = groups.filter(g => g.origin === 'user')
    const published = groups.filter(g => g.origin !== 'user')
    const list = (rows: ArtifactGroup[], label?: string) => (
      <ul className="artifact-list" aria-label={label}>{rows.map(groupRow)}</ul>
    )
    // Files you attached to a message get their own section; with none, the list is as it always was.
    body = uploaded.length === 0 ? list(groups) : (
      <>
        {published.length > 0 && <><h3 className="artifact-section">Published by the agent</h3>{list(published, 'Published by the agent')}</>}
        <h3 className="artifact-section" data-testid="uploaded-section">Uploaded by you</h3>
        {list(uploaded, 'Uploaded by you')}
      </>
    )
  }

  return createPortal(
    <div className="artifact-overlay" data-testid="artifacts-overlay" {...backdrop}>
      <div
        ref={dialogRef}
        className={`artifact-dialog artifact-panel${items.length === 0 ? ' is-empty' : ''}`}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        tabIndex={-1}
        data-testid="artifacts-panel"
        onKeyDown={onKeyDown}
      >
        <header className="artifact-header">
          <div className="artifact-header-text">
            <h2 id={titleId}>Files <span className="artifact-count">{fileCount(items)}</span></h2>
            <p className="artifact-meta">Published by the agent, or attached by you.</p>
          </div>
          <button type="button" className="artifact-close" aria-label="Close" onClick={onClose}>✕</button>
        </header>
        <div className="artifact-body">{body}</div>
      </div>
    </div>,
    document.body,
  )
}
