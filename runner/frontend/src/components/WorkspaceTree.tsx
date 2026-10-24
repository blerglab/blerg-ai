import { useEffect, useId, useRef, useState, type KeyboardEvent } from 'react'
import { WorkspaceFileViewer, FileGlyph, FolderGlyph } from '@blerglab/chat'
import { apiFetch } from '../apiFetch'
import { blergTransport } from '../lib/chatTransport'
import { signedInAccount } from '../lib/liveBoardCard'
import './WorkspaceTree.css'
import { inputClock } from '../../../../packages/chat/src/model/clock'

import { fileGlyphView, pathCodes, statusLabels, withDeletedEntries, workspaceChanges, type TreeEntry as Entry, type TreeStatus as Status } from '../lib/workspaceTree'
type Member = { id: string; name: string; path: string }
type Result = { ok: boolean; unavailable_members?: string[]; entries: Entry[]; cursor?: string; truncated: boolean; status?: Status; error_code?: string; launch_generation: string; generation: number }
type Reply = { result: Result; members: Member[]; runtime: string }
type Page = { entries: Entry[]; cursor?: string; truncated: boolean }

export default function WorkspaceTree({ sessionId, active }: { sessionId: string; active: boolean }) {
  return active ? <WorkspaceTreeBody key={`${sessionId}:${signedInAccount()}`} sessionId={sessionId} /> : null
}

function WorkspaceTreeBody({ sessionId }: { sessionId: string }) {
  const [pages, setPages] = useState<Record<string, Page>>({})
  const [expanded, setExpanded] = useState<Set<string>>(new Set(['']))
  const [statuses, setStatuses] = useState<Record<string, Status | null>>({})
  const [members, setMembers] = useState<Member[]>([])
  const [runtime, setRuntime] = useState('')
  const [unavailable, setUnavailable] = useState<string[]>([])
  const [filter, setFilter] = useState('')
  const [searchExpanded, setSearchExpanded] = useState(false)
  const [searchActive, setSearchActive] = useState<string | null>(null)
  const searchInput = useRef<HTMLInputElement>(null)
  const [hits, setHits] = useState<Page | null>(null)
  const [focusRoot, setFocusRoot] = useState('')
  const [message, setMessage] = useState('')
  const [busy, setBusy] = useState(false)
  const [viewing, setViewing] = useState<string | null>(null)
  const viewerTrigger = useRef<HTMLElement | null>(null)
  const [focusedPath, setFocusedPath] = useState('')
  const treeId = useId()
  const controller = useRef<AbortController | null>(null)
  const statusTimes = useRef<Record<string, number>>({})
  const binding = useRef('')
  const account = useRef(signedInAccount())
  const operations = blergTransport().files

  async function request(op: string, path: string, signal: AbortSignal, cursor?: string) {
    const query = new URLSearchParams({ op, path })
    if (op === 'search') query.set('filter', filter)
    if (cursor) query.set('cursor', cursor)
    const response = await apiFetch(`/api/sessions/${encodeURIComponent(sessionId)}/workspace?${query}`, { signal })
    if (!response.ok) throw new Error(response.status === 403 ? 'Only the session owner or an authorized administrator can browse this workspace.' : response.status === 409 ? 'Workspace unavailable or busy. Try Refresh.' : 'Could not read the workspace.')
    const reply = await response.json() as Reply
    if (signedInAccount() !== account.current) { setPages({}); setMembers([]); setStatuses({}); setHits(null); setViewing(null); throw new Error('Your sign-in changed. Refresh the workspace.') }
    if (signal.aborted) throw new DOMException('Cancelled', 'AbortError')
    const next = `${reply.result.launch_generation}:${reply.result.generation}`
    if (binding.current && binding.current !== next) {
      setPages({}); setStatuses({}); setHits(null); setMembers([])
      binding.current = next
      throw new Error('The runtime changed. Refresh to browse its workspace.')
    }
    binding.current = next
    if (!reply.result.ok) throw new Error(reply.result.error_code === 'stale' ? 'The directory changed. Refresh to load it again.' : 'The workspace could not be safely read.')
    setMembers(reply.members); setRuntime(reply.runtime)
    if (reply.result.unavailable_members) setUnavailable(reply.result.unavailable_members)
    return reply
  }

  async function load(path: string, cursor?: string, op = 'list') {
    controller.current?.abort()
    const ac = new AbortController(); controller.current = ac
    setBusy(true); setMessage('')
    try {
      const reply = await request(op, path, ac.signal, cursor)
      const result = reply.result
      const page = { entries: result.entries, cursor: result.cursor, truncated: result.truncated }
      if (op === 'status') { setStatuses(old => ({ ...old, [path]: result.status ?? null })); statusTimes.current[path] = inputClock.now() }
      else if (op === 'search') setHits(page)
      else {
        setPages(old => ({ ...old, [path]: { ...page, entries: cursor ? [...(old[path]?.entries ?? []), ...page.entries] : page.entries } }))
        // Status is requested only as this repository is expanded, after the
        // directory read releases its slot. No workspace-wide polling.
        const member = reply.members.find(m => m.path === (path || '.'))
        const discovered = pagesPathRepository(path)
        if (member || discovered) {
          try {
            const status = await request('status', path, ac.signal)
            setStatuses(old => ({ ...old, [path]: status.result.status ?? null })); statusTimes.current[path] = inputClock.now()
          } catch (error) {
            if (ac.signal.aborted) throw error
            setStatuses(old => ({ ...old, [path]: null })); statusTimes.current[path] = inputClock.now()
          }
        }
      }
    } catch (error) {
      if (!ac.signal.aborted && op === 'status') { setStatuses(old => ({ ...old, [path]: null })); statusTimes.current[path] = inputClock.now() }
      if (!ac.signal.aborted) setMessage(error instanceof Error ? error.message : 'Could not read the workspace.')
    } finally { if (!ac.signal.aborted) setBusy(false) }
  }
  function pagesPathRepository(path: string) { return hits?.entries.some(e => e.path === path && e.repository) || Object.values(pages).some(p => p.entries.some(e => e.path === path && e.repository)) }

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- Start the cancellable initial runtime read and its loading indicator on mount.
    void load('')
    return () => { controller.current?.abort() }
    // The component is remounted on refresh, session change or tab activation.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  function enterDirectory(entry: Entry) {
    if (entry.deleted) { setPages(old => ({ ...old, [entry.path]: { entries: [], truncated: false } })); return }
    if (!pages[entry.path]) void load(entry.path)
    else if (entry.repository && (statusTimes.current[entry.path] === undefined || inputClock.now() - statusTimes.current[entry.path] >= 5000)) void load(entry.path, undefined, 'status')
  }

  function toggle(entry: Entry) {
    if (!entry.directory) { viewerTrigger.current = document.activeElement as HTMLElement; setViewing(entry.path); return }
    if (hits) { setHits(null); setFilter(''); setFocusRoot(entry.path); enterDirectory(entry); return }
    const next = new Set(expanded)
    if (next.has(entry.path)) { next.delete(entry.path); controller.current?.abort(); setBusy(false) }
    else { next.add(entry.path); enterDirectory(entry) }
    setExpanded(next)
  }
  const badge = (key: string) => {
    const status = statuses[key]
    if (status === undefined) return null
    if (status === null) return <span className="workspace-status">status unavailable</span>
    return <span className="workspace-status">{status.branch || 'detached'} · {status.changed + status.untracked} changed{status.partial ? ' (partial)' : ''}</span>
  }
  const label = (entry: Entry) => members.find(m => m.id === entry.member_id)?.name ?? entry.name
  function treeKey(event: KeyboardEvent<HTMLUListElement>) {
    const item = (event.target as HTMLElement).closest<HTMLButtonElement>('[role="treeitem"]')
    if (!item) return
    const items = [...event.currentTarget.querySelectorAll<HTMLButtonElement>('[role="treeitem"]')]
    const index = items.indexOf(item)
    let next: HTMLButtonElement | undefined
    if (event.key === 'ArrowDown') next = items[Math.min(items.length - 1, index + 1)]
    else if (event.key === 'ArrowUp') next = items[Math.max(0, index - 1)]
    else if (event.key === 'Home') next = items[0]
    else if (event.key === 'End') next = items.at(-1)
    else if (event.key === 'ArrowRight') {
      if (item.getAttribute('aria-expanded') === 'false') item.click()
      else if (item.getAttribute('aria-expanded') === 'true') next = item.parentElement?.querySelector<HTMLButtonElement>(':scope > [role="group"] > li > [role="treeitem"]') ?? undefined
    } else if (event.key === 'ArrowLeft') {
      if (item.getAttribute('aria-expanded') === 'true') item.click()
      else next = item.parentElement?.parentElement?.closest('li')?.querySelector<HTMLButtonElement>('[role="treeitem"]') ?? undefined
    } else return
    event.preventDefault()
    if (next) {
      items.forEach(row => { row.tabIndex = row === next ? 0 : -1 })
      next.focus()
    }
  }
  const changes = workspaceChanges(statuses)
  const entriesAt = (directory: string) => withDeletedEntries(pages[directory]?.entries ?? [], directory, changes)
  function matches(entry: Entry): boolean {
    return !filter || label(entry).toLocaleLowerCase().includes(filter.toLocaleLowerCase()) || (entry.directory && entriesAt(entry.path).some(matches))
  }
  const visiblePaths: string[] = []
  function collectVisible(entries: Entry[]) {
    for (const entry of entries) {
      if (!matches(entry)) continue
      visiblePaths.push(entry.path)
      if (entry.directory && expanded.has(entry.path)) collectVisible(entriesAt(entry.path))
    }
  }
  collectVisible(hits?.entries ?? entriesAt(focusRoot))
  const tabPath = visiblePaths.includes(focusedPath) ? focusedPath : visiblePaths[0]
  const allLoaded: Entry[] = []
  function collectLoaded(entries: Entry[]) {
    for (const entry of entries) {
      allLoaded.push(entry)
      if (entry.directory) collectLoaded(entriesAt(entry.path))
    }
  }
  collectLoaded(entriesAt(focusRoot))
  const searchEntries = (hits?.entries ?? allLoaded).filter(entry => label(entry).toLocaleLowerCase().includes(filter.toLocaleLowerCase()))
  const activeResult = searchEntries.find(entry => entry.path === searchActive)
  const resultId = (entry: Entry) => `${treeId}-result-${encodeURIComponent(entry.path)}`
  const listboxId = `${treeId}-search-results`
  const activeId = searchExpanded && activeResult ? resultId(activeResult) : undefined
  useEffect(() => { if (activeId) document.getElementById(activeId)?.scrollIntoView?.({ block: 'nearest' }) }, [activeId])
  const announcedLabel = (entry: Entry, name = label(entry)) => [name, ...pathCodes(entry, changes).map(code => `${statusLabels[code].toLowerCase()}${entry.directory ? ' in this folder' : ''}`)].join(', ')
  function searchAll() {
    setSearchExpanded(true); setSearchActive(null); searchInput.current?.focus()
    void load('', undefined, 'search')
  }
  function chooseResult(entry: Entry) {
    setSearchExpanded(false); setSearchActive(null)
    if (entry.directory) {
      setHits(null); setFilter(''); setFocusRoot(entry.path); enterDirectory(entry)
    } else toggle(entry)
  }
  function searchKey(event: KeyboardEvent<HTMLInputElement>) {
    if (event.nativeEvent.isComposing) return
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      event.preventDefault(); setSearchExpanded(true)
      const current = searchEntries.findIndex(entry => entry.path === searchActive)
      const next = event.key === 'ArrowDown' ? Math.min(searchEntries.length - 1, current + 1) : current < 0 ? searchEntries.length - 1 : Math.max(0, current - 1)
      setSearchActive(searchEntries[next]?.path ?? null)
    } else if (event.key === 'Enter') {
      if (searchExpanded && activeResult) { event.preventDefault(); chooseResult(activeResult) }
      else if (filter.trim() && !busy) { event.preventDefault(); searchAll() }
    } else if (event.key === 'Escape' && searchExpanded) {
      event.preventDefault(); event.stopPropagation(); setSearchExpanded(false); setSearchActive(null)
    }
  }
  function rows(entries: Entry[], depth: number): React.ReactNode {
    return entries.filter(matches).map(entry => <li role="none" key={entry.path}>
      <button type="button" role="treeitem" aria-label={announcedLabel(entry)} aria-level={depth + 1} tabIndex={entry.path === tabPath ? 0 : -1} onFocus={() => setFocusedPath(entry.path)} aria-owns={entry.directory && expanded.has(entry.path) ? `${treeId}-${encodeURIComponent(entry.path)}` : undefined} className="workspace-row" style={{ paddingInlineStart: '6px' }} onClick={() => toggle(entry)} aria-expanded={entry.directory ? expanded.has(entry.path) : undefined}>
        <span className="workspace-chevron" aria-hidden="true">{entry.directory ? expanded.has(entry.path) ? '▾' : '▸' : ''}</span>
        {entry.directory ? <FolderGlyph expanded={expanded.has(entry.path)} /> : <FileGlyph view={fileGlyphView(entry.name)} label={fileGlyphView(entry.name)} />}
        <span className="workspace-name" title={label(entry)}>{label(entry)}</span>
        <span className="workspace-codes">{pathCodes(entry, changes).map(code => <span key={code} className={`workspace-code workspace-code-${code}`} title={`${statusLabels[code]}${entry.directory ? ' in this folder' : ''}`} aria-label={statusLabels[code]}>{code}</span>)}</span>
      </button>
      {badge(entry.path)}
      {entry.directory && expanded.has(entry.path) && <ul role="group" style={{ borderInlineStart: depth < 4 ? '1px solid var(--stone)' : '0', marginInlineStart: depth < 4 ? 16 : 0 }} id={`${treeId}-${encodeURIComponent(entry.path)}`}>{pages[entry.path] ? rows(entriesAt(entry.path), depth + 1) : <li className="workspace-note">{busy ? 'Loading…' : 'Expand again to retry.'}</li>}{pageEnd(entry.path)}</ul>}
    </li>)
  }
  function pageEnd(path: string) {
    const page = pages[path]
    if (!page?.truncated) return null
    return <li className="workspace-note">{page.cursor ? <button type="button" disabled={busy} onClick={() => void load(path, page.cursor)}>Load more</button> : 'Partial listing: this directory reached the read limit.'}</li>
  }
  return <section className="workspace-tree" aria-label="Workspace">
    <div className="workspace-tools">
      <input ref={searchInput} role="combobox" aria-label="Search workspace files" aria-autocomplete="list" aria-haspopup="listbox" aria-controls={listboxId} aria-expanded={searchExpanded} aria-activedescendant={activeId} placeholder="Search files…" value={filter} onChange={e => { setFilter(e.target.value); setHits(null); setSearchActive(null); setSearchExpanded(!!e.target.value.trim()) }} onBlur={() => { setSearchExpanded(false); setSearchActive(null) }} onKeyDown={searchKey} maxLength={256} />
      <button type="button" aria-disabled={busy} onClick={() => {
        if (busy) return
        setPages({}); setStatuses({}); setSearchExpanded(false); setSearchActive(null); setExpanded(new Set([''])); setHits(null); setFocusRoot(''); setFocusedPath(''); statusTimes.current = {}; binding.current = ''
        void load('')
      }} aria-label="Refresh workspace" title="Refresh workspace"><svg viewBox="0 0 16 16" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="1.3" aria-hidden="true"><path d="M13 6a5 5 0 1 0 .1 4M13 2v4H9" /></svg></button>
    </div>
    {filter.trim() && <button type="button" className="workspace-search-all" disabled={busy} onClick={searchAll}>Search all files <span aria-hidden="true">↵</span></button>}
    {runtime === 'sandbox' && <p className="workspace-note">Only selected repositories were copied.</p>}
    {members.filter(m => unavailable.includes(m.id)).map(m => <p className="workspace-note" key={m.id}>{m.name} · unavailable</p>)}
    {members.filter(m => m.path === '.').map(m => <p className="workspace-note" key={m.id}>{m.name} {badge('')}</p>)}
    {focusRoot && <p className="workspace-note"><button type="button" onClick={() => { controller.current?.abort(); setBusy(false); setFocusRoot(''); setHits(null) }}>Workspace</button> / {focusRoot} {badge(focusRoot)}</p>}
    {message && <p role="status" className="workspace-note">{message}</p>}
    {hits && <p className="workspace-note">Name matches{hits.truncated ? ' (partial)' : ''} <button type="button" onClick={() => { setHits(null); setSearchActive(null); setSearchExpanded(false); searchInput.current?.focus() }}>Back to tree</button></p>}
    <ul id={listboxId} role="listbox" aria-label="Workspace search results" hidden={!searchExpanded} aria-busy={busy}>
      {searchExpanded && searchEntries.map(entry => <li role="none" key={entry.path}>
        <button id={resultId(entry)} type="button" role="option" tabIndex={-1} aria-selected={entry.path === activeResult?.path} aria-label={announcedLabel(entry, entry.path)} className="workspace-row" onMouseDown={e => e.preventDefault()} onClick={() => chooseResult(entry)}>
          {entry.directory ? <FolderGlyph /> : <FileGlyph view={fileGlyphView(entry.name)} label={fileGlyphView(entry.name)} />}
          <span className="workspace-name" title={entry.path}>{entry.path}</span>
          <span className="workspace-codes">{pathCodes(entry, changes).map(code => <span key={code} className={`workspace-code workspace-code-${code}`} title={statusLabels[code]}>{code}</span>)}</span>
        </button>
      </li>)}
    </ul>
    {!searchExpanded && <ul role="tree" aria-label="Workspace files" onKeyDown={treeKey}>{rows(hits?.entries ?? entriesAt(focusRoot), 0)}{!hits && pageEnd(focusRoot)}</ul>}
    {searchExpanded && !busy && searchEntries.length === 0 && <p className="workspace-note">No loaded matches. Search all files to look across the workspace.</p>}
    {busy && <p className="workspace-note" role="status">Loading…</p>}
    {!busy && pages[focusRoot] && entriesAt(focusRoot).length === 0 && <p className="workspace-note">No visible files.</p>}
    {viewing && operations && <WorkspaceFileViewer key={`${sessionId}:${viewing}`} sessionId={sessionId} files={operations} path={viewing} onClose={() => { setViewing(null); viewerTrigger.current?.focus() }} />}
  </section>
}
