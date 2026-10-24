// FilesList: every file the session holds, newest first — the body of the Files panel, and of a host's
// own place for it (the runner's session panel has a Files tab). Rows are compact: glyph, name, size ·
// version · time. With more than a few files a filter box, a sort and an origin choice appear. A file with several versions (the
// agent, or you, added the same name again) is ONE row showing its latest version, with the earlier
// ones behind an "Earlier versions" disclosure. Each version can be downloaded, viewed (when the app
// can show that type) or deleted on its own (after an in-page confirmation). A version the server
// still lists but no longer has the bytes of (`missing`) is drawn disabled, says "no longer
// available", and offers Delete only.
import { useId, useState, type ReactNode } from 'react'
import { localStorageChatStorage } from '../model/storage'
import { formatSize, groupArtifacts, type ArtifactGroup } from '../model/artifacts'
import { ARTIFACT_VIEWS, isViewable } from '../model/artifactViews'
import type { ArtifactsStatus } from '../hooks/useArtifacts'
import type { ArtifactInfo, TransportFiles } from '../transport/types'
import { fileErrorText } from '../transport/files'
import ErrorId from './ErrorId'
import { Hover } from './Hovercard'
import { TimeLabel } from './TimeLabel'
import { FileGlyph } from './FileGlyph'

const SORT_KEY = 'blerg.files.sort'
const ORIGIN_KEY = 'blerg.files.origin'
/** The list gets a filter box, sort and origin choice once it holds more files than this. */
const SEARCH_OVER = 6

type Sort = 'newest' | 'name'
type Origin = 'all' | 'agent' | 'user'

const DOWNLOAD_ICON = 'M8 2.5v8m-3-3 3 3 3-3M3 13h10'
const DELETE_ICON = 'M3.5 4.5h9M6.5 4.5v-2h3v2M5 4.5l.5 9h5l.5-9'

function IconButton({ label, path, danger, onClick, disabled }: { label: string; path: string; danger?: boolean; onClick: () => void; disabled?: boolean }) {
  return (
    <button type="button" className={`artifact-icon-btn${danger ? ' danger' : ''}`} aria-label={label} title={label} disabled={disabled} onClick={onClick}>
      <svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true">
        <path d={path} fill="none" stroke="currentColor" strokeWidth="1.3" strokeLinecap="round" strokeLinejoin="round" />
      </svg>
    </button>
  )
}

export interface FilesListProps {
  sessionId: string
  files: TransportFiles
  items: ArtifactInfo[]
  status: ArtifactsStatus
  onView: (artifact: ArtifactInfo) => void
  /** Called after a delete succeeded, so the list is read again. */
  onChanged: () => void
  /** Opens the file picker of the message box, so the empty state can offer it. */
  onAttach?: () => void
  /** What the empty state says the agent publishes with (the runner's `blerg-runner publish`). */
  publishHint?: string
  /** What a list that could not be read says after "Couldn’t load the files." */
  retryHint?: string
}

export default function FilesList({ sessionId, files, items, status, onView, onChanged, onAttach, publishHint = 'blerg-runner publish', retryHint = 'Close this and try again.' }: FilesListProps) {
  const listBase = useId()
  const [confirming, setConfirming] = useState<string | null>(null)
  const [busy, setBusy] = useState<string | null>(null)
  const [errors, setErrors] = useState<Record<string, { message: string; errorId?: string }>>({})
  const [expanded, setExpanded] = useState<Set<string>>(() => new Set())
  const [query, setQuery] = useState('')
  const [sort, setSortState] = useState<Sort>(() => (localStorageChatStorage.get(SORT_KEY) === 'name' ? 'name' : 'newest'))
  const [origin, setOriginState] = useState<Origin>(() => {
    const v = localStorageChatStorage.get(ORIGIN_KEY)
    return v === 'agent' || v === 'user' ? v : 'all'
  })
  const setSort = (v: Sort) => { setSortState(v); localStorageChatStorage.set(SORT_KEY, v) }
  const setOrigin = (v: Origin) => { setOriginState(v); localStorageChatStorage.set(ORIGIN_KEY, v) }

  const fail = (id: string, err: { message: string; errorId?: string }) => setErrors(prev => ({ ...prev, [id]: err }))
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
      await files.download(sessionId, a.id)
    } catch (e) {
      fail(a.id, fileErrorText(e, 'Download failed.'))
    }
  }

  async function remove(a: ArtifactInfo) {
    clear(a.id)
    setBusy(a.id)
    try {
      await files.remove(sessionId, a.id)
      setConfirming(null)
      onChanged()
    } catch {
      fail(a.id, { message: 'Couldn’t delete this file. Try again.' })
    } finally {
      setBusy(null)
    }
  }

  // One version: its glyph, name (the click opens it when the app can show it), size · version · time ·
  // "you", and Download / Delete (on hover or focus; always shown on touch).
  const details = (a: ArtifactInfo, g: ArtifactGroup, badge: boolean) => {
    const versioned = g.earlier.length > 0 || (a.version ?? 1) > 1
    const gone = a.missing === true
    const viewable = !gone && isViewable(a.view)
    const kind = isViewable(a.view) ? ARTIFACT_VIEWS[a.view].label : 'Download only'
    const mainProps = {
      className: `artifact-row-main${gone ? ' is-missing' : ''}`,
      'aria-disabled': gone || undefined,
      title: gone ? `${a.name}${badge ? ` v${a.version}` : ''} is no longer available` : a.name,
      'data-testid': gone ? 'artifact-missing' : undefined,
    }
    const main = (
      <>
        <span className="artifact-row-name" data-testid="artifact-name">{a.name}</span>
        <span className="artifact-row-meta">
          {gone ? 'no longer available' : formatSize(a.size)}
          {badge && <> · <span className="artifact-version" data-testid="artifact-version">v{a.version}</span></>}
          {a.created_at && <> · <TimeLabel ts={a.created_at} /></>}
          {a.origin === 'user' && <> · <span className="artifact-you" title="Attached by you">you</span></>}
        </span>
      </>
    )
    return (
      <>
        <FileGlyph view={a.view} label={gone ? 'No longer available' : kind} />
        {viewable
          ? <Hover target={{ kind: 'file', sessionId, id: a.id, artifact: { ...a, ...(g.latest.version ? { latest_version: g.latest.version } : {}) } }}>
              <button type="button" {...mainProps} aria-label={`View ${a.name}`} onClick={() => onView(a)}>{main}</button>
            </Hover>
          : <div {...mainProps}>{main}</div>}
        {confirming === a.id ? (
          <div className="artifact-row-actions is-confirming">
            <span className="artifact-confirm">{versioned ? `Delete v${a.version ?? 1}?` : 'Delete this file?'}</span>
            <button type="button" className="artifact-btn danger" disabled={busy === a.id} onClick={() => void remove(a)}>Yes, delete</button>
            <button type="button" className="artifact-btn" onClick={() => setConfirming(null)}>Cancel</button>
          </div>
        ) : (
          <div className="artifact-row-actions">
            {!gone && <IconButton label="Download" path={DOWNLOAD_ICON} onClick={() => void download(a)} />}
            <IconButton label="Delete" path={DELETE_ICON} onClick={() => { clear(a.id); setConfirming(a.id) }} />
          </div>
        )}
        {errors[a.id] && <div className="artifact-error" role="alert">{errors[a.id].message}<ErrorId id={errors[a.id].errorId} /></div>}
      </>
    )
  }

  const groupRow = (g: ArtifactGroup) => {
    const open = expanded.has(g.key)
    const listId = `${listBase}-${g.key.replace(/\W/g, '_')}`
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

  let body: ReactNode
  if (status === 'loading' && items.length === 0) {
    body = <p className="artifact-status" data-testid="artifacts-loading">Loading…</p>
  } else if (status === 'error' && items.length === 0) {
    body = <p className="artifact-status" role="alert">Couldn’t load the files. {retryHint}</p>
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
          Files the agent publishes appear here. It can share one with <code>{publishHint}</code>.
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
    const all = groupArtifacts(items)
    const tools = all.length > SEARCH_OVER // search, sort and origin only earn their room on a long list
    const needle = query.trim().toLowerCase()
    const shown = (tools ? all.filter(g => (origin === 'all' || g.origin === origin) && g.name.toLowerCase().includes(needle)) : all)
      .sort((x, y) => (tools && sort === 'name' ? x.name.localeCompare(y.name, undefined, { sensitivity: 'base' }) : 0))
    body = (
      <>
        {tools && (
          <div className="artifact-tools">
            <input
              type="search"
              className="artifact-search"
              placeholder="Filter files"
              aria-label="Filter files"
              value={query}
              onChange={e => setQuery(e.target.value)}
              onKeyDown={e => {
                if (e.key === 'Escape' && query) { e.stopPropagation(); setQuery('') }
              }}
            />
            <select className="artifact-select" aria-label="Sort files" value={sort} onChange={e => setSort(e.target.value as Sort)}>
              <option value="newest">Newest</option>
              <option value="name">Name</option>
            </select>
            <select className="artifact-select" aria-label="Show files from" value={origin} onChange={e => setOrigin(e.target.value as Origin)}>
              <option value="all">All</option>
              <option value="agent">From the agent</option>
              <option value="user">From you</option>
            </select>
            {shown.length !== all.length && <span className="artifact-shown" data-testid="artifacts-shown">{shown.length} of {all.length}</span>}
          </div>
        )}
        {shown.length === 0
          ? <p className="artifact-status" data-testid="artifacts-no-match">No files match</p>
          : <ul className="artifact-list">{shown.map(groupRow)}</ul>}
      </>
    )
  }
  return <>{body}</>
}
