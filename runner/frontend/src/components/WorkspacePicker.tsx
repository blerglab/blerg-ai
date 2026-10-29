import { useEffect, useRef, useState, type KeyboardEvent, type ReactNode } from 'react'
import { FileIcon } from '@blerglab/chat'
import { apiFetch } from '../apiFetch'
import { providerLabel } from '../lib/repoRef'
import { currentAccount, type DaemonApiInfo, type RepoInfo } from '../lib/launchCache'
import '../../../../packages/settings/settings.css'
import './WorkspacePicker.css'

export interface SourceRef { path: string; source_identity: string }
export interface WorkspaceSelection {
  layout?: 'names' | 'structure'
  daemon_id?: string
  ownership_revision?: number
  connection_generation?: string
  root_identity?: string
  members: SourceRef[]
  folder?: SourceRef
  membership_snapshot?: string
  hosted?: { provider: string; full_name: string }[]
}
interface Entry extends SourceRef { name: string; kind: 'directory' | 'repository' }
interface Page { root_identity: string; root: Entry; entries: Entry[]; next_cursor?: string; truncated: boolean }
interface Props {
  daemon: DaemonApiInfo | null
  runtime: string
  repos: RepoInfo[]
  actions?: ReactNode
  hideTree?: boolean
  recentRepos?: string[]
  onChange: (selection: WorkspaceSelection | null, names: string[]) => void
}
const ref = (e: Entry): SourceRef => ({ path: e.path, source_identity: e.source_identity })
type Preview = { ok: boolean; code?: string; selection: WorkspaceSelection; members: { name: string }[]; structure_fallback?: boolean; member_paths?: string[] }
// A host folder with no Git repository resolves to zero members; older servers send
// them as null. Treat missing lists as empty so such a folder is a usable selection.
function preview(body: Preview | null): Preview {
  if (!body?.ok || !body.selection) return { ok: false, code: body?.code, selection: { members: [] }, members: [] }
  return { ...body, selection: { ...body.selection, members: body.selection.members ?? [] }, members: body.members ?? [] }
}

const desktopUpdateMessage = 'Update Blerg on this desktop to use several repositories (reinstall its daemon).'
const recheckMessage = 'The selection could not be checked. Try again.'
function rememberedLayout(key: string): boolean {
  try { return localStorage.getItem(key) === 'structure' } catch { return false }
}

// One tree for host and sandbox. Pages are lazy and never imply selection;
// folder checkboxes resolve a complete bounded membership snapshot separately.
export default function WorkspacePicker({ daemon, runtime, repos, onChange, actions, hideTree = false, recentRepos = [] }: Props) {
  // Like the launch sheet's engine/model defaults, this is a guarded browser
  // preference. Account scoping prevents one person's choice carrying to another.
  const layoutKey = `blerg.launch.workspace-layout.${encodeURIComponent(currentAccount())}`
  const [layoutPreference, setLayoutPreference] = useState(() => ({ key: layoutKey, keep: rememberedLayout(layoutKey) }))
  if (layoutPreference.key !== layoutKey) setLayoutPreference({ key: layoutKey, keep: rememberedLayout(layoutKey) })
  const keepStructure = layoutPreference.key === layoutKey ? layoutPreference.keep : rememberedLayout(layoutKey)
  const [memberPaths, setMemberPaths] = useState<string[]>([])
  const [needsRecheck, setNeedsRecheck] = useState(false)
  const [structureFallback, setStructureFallback] = useState(false)
  const [pages, setPages] = useState<Record<string, Page>>({})
  const [expanded, setExpanded] = useState<Set<string>>(new Set(['.']))
  const [searchExpanded, setSearchExpanded] = useState<Record<string, boolean>>({})
  const [focusedPath, setFocusedPath] = useState('.')
  const [selection, setSelection] = useState<WorkspaceSelection | null>(null)
  const [names, setNames] = useState<string[]>([])
  const [search, setSearch] = useState('')
  const [error, setError] = useState('')
  const [folderSelections, setFolderSelections] = useState<Record<string, SourceRef[]>>({})
  const [subsetFolder, setSubsetFolder] = useState<SourceRef | null>(null)
  const [busy, setBusy] = useState(false)
  const controller = useRef<AbortController | null>(null)
  const sequence = useRef(0)
  const change = useRef(onChange)
  useEffect(() => { change.current = onChange }, [onChange])
  const tree = useRef<HTMLDivElement>(null)
  const cluster = runtime === 'cluster'
  const root = pages['.']
  function publish(value: WorkspaceSelection | null, labels: string[], complete = false, fallback = false, paths: string[] = []) {
    void complete
    setNeedsRecheck(false); setMemberPaths(paths)
    setStructureFallback(fallback)
    setSelection(value); setNames(labels); change.current(value, labels)
  }
  function withLayout(value: WorkspaceSelection, keep = keepStructure): WorkspaceSelection {
    return { ...value, layout: !value.folder && value.members.length > 1 ? keep ? 'structure' : 'names' : undefined }
  }
  function restore() { change.current(needsRecheck ? null : selection, needsRecheck ? [] : names) }
  async function recheck(value: WorkspaceSelection | null, keep = keepStructure) {
    if (!value) { publish(null, []); return }
    const candidate = withLayout(value, keep)
    const labels = candidate.members.map(member => {
      const index = selection?.members.findIndex(old => old.source_identity === member.source_identity) ?? -1
      return names[index] ?? member.path.split('/').at(-1) ?? member.path
    })
    // Keep edits visible locally while withholding launch authority until checked.
    setSelection(candidate); setNames(labels); setMemberPaths([]); setNeedsRecheck(true)
    const operation = ++sequence.current
    let failureMessage = recheckMessage
    setBusy(true); setError(''); change.current(null, [])
    try {
      const res = await apiFetch('/api/workspace/preview', {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, signal: controller.current?.signal,
        body: JSON.stringify({ runtime: runtime === 'docker' ? 'sandbox' : 'host', selection: candidate, expand: false }),
      })
      const body = preview(await res.json() as Preview | null)
      if (operation !== sequence.current || controller.current?.signal.aborted) return
      if (!res.ok || !body.ok) {
        if (body.code === 'desktop_update_required') failureMessage = desktopUpdateMessage
        throw new Error('preview unavailable')
      }
      publish(body.selection, body.members.map(m => m.name), false, body.structure_fallback, body.member_paths)
    } catch {
      if (!controller.current?.signal.aborted && operation === sequence.current) {
        setError(failureMessage)
      }
    } finally { if (operation === sequence.current) setBusy(false) }
  }
  async function changeLayout(keep: boolean) {
    setLayoutPreference({ key: layoutKey, keep })
    try { localStorage.setItem(layoutKey, keep ? 'structure' : 'names') } catch { /* best-effort convenience */ }
    await recheck(selection, keep)
  }
  async function load(path: string, cursor = '', signal?: AbortSignal) {
    if (!daemon) return
    const query = new URLSearchParams({ daemon_id: daemon.id, path, cursor })
    const res = await apiFetch(`/api/workspace/discovery?${query}`, { signal })
    const body = await res.json() as { ok: boolean; page?: Page }
    if (!res.ok || !body.ok || !body.page) throw new Error('The folder changed or is unavailable. Refresh the tree.')
    const page = body.page
    if (signal?.aborted) return
    setPages(old => ({ ...old, [path]: cursor && old[path]
      ? { ...page, entries: [...old[path].entries, ...page.entries] } : page }))
  }
  useEffect(() => {
    const abort = new AbortController(); controller.current = abort
    if (!cluster && daemon?.discovery_version === 1) {
      void Promise.resolve().then(() => load('.', '', abort.signal)).catch(() => {
        if (!abort.signal.aborted) setError('The folder is unavailable. Reopen the picker to refresh it.')
      })
    }
    return () => { abort.abort() }
    // Parent keys the component by desktop connection, runtime and identity scope.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])
  async function toggle(entry: Entry) {
    if (!daemon || !root || busy) return
    setError('')
    const selected = selection?.members.some(m => m.source_identity === entry.source_identity)
    if (entry.kind === 'repository' && selected) {
      const index = selection!.members.findIndex(m => m.source_identity === entry.source_identity)
      const members = selection!.members.filter((_, i) => i !== index)
      await recheck(members.length ? { ...selection!, members, membership_snapshot: selection!.membership_snapshot?.startsWith('explicit:') ? undefined : selection!.membership_snapshot } : null)
      return
    }
    const priorFolder = folderSelections[entry.path]
    if (entry.kind === 'directory' && priorFolder && (priorFolder.length ? priorFolder.every(m => selection?.members.some(x => x.source_identity === m.source_identity)) : selection?.folder?.path === entry.path)) {
      const remaining = selection!.members.filter(m => !(entry.path === '.' || m.path.startsWith(`${entry.path}/`)))
      await recheck(remaining.length ? { ...selection!, members: remaining, folder: undefined, membership_snapshot: undefined } : null)
      setSubsetFolder(null); return
    }

    const current = selection ?? {
      daemon_id: daemon.id, ownership_revision: daemon.ownership_revision,
      connection_generation: daemon.connection_generation, root_identity: root.root_identity, members: [],
    }
    const members = entry.kind === 'repository' ? [...current.members, ref(entry)] : []
    if (members.length > 20) { setError('Select at most 20 repositories.'); return }
    const anchor = current.folder ?? subsetFolder
    const insideAnchor = anchor && (anchor.path === '.' || entry.path === anchor.path || entry.path.startsWith(`${anchor.path}/`))
    const candidate = entry.kind === 'directory'
      ? { ...current, members, folder: ref(entry), membership_snapshot: undefined }
      : { ...current, members, folder: insideAnchor ? anchor : undefined,
        membership_snapshot: insideAnchor && !current.membership_snapshot?.startsWith('explicit:') ? current.membership_snapshot : undefined }

    const operation = ++sequence.current
    setBusy(true)
    change.current(null, []) // A pending preview is not a launchable selection.
    try {
      const res = await apiFetch('/api/workspace/preview', {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, signal: controller.current?.signal,
        body: JSON.stringify({ runtime: runtime === 'docker' ? 'sandbox' : 'host', selection: withLayout(candidate), expand: entry.kind === 'directory' }),
      })
      const body = preview(await res.json() as Preview | null)
      if (operation !== sequence.current || controller.current?.signal.aborted) return
      if (!res.ok || !body.ok) {
        if (body.code === 'too_many_members' && entry.kind === 'directory' && !selection) { setSubsetFolder(ref(entry)); publish(null, []) } else { restore() }
        setError(body.code === 'desktop_update_required' ? desktopUpdateMessage : body.code === 'too_many_members' ? 'This folder contains more than 20 repositories. Expand it and select an explicit subset.'
          : body.code === 'overlapping_repositories' ? 'Choose a parent repository or its nested repository. Linked worktrees sharing Git data cannot be selected together.'
          : body.code === 'invalid_selection' && entry.kind === 'directory' && runtime === 'docker' ? 'This folder has no Git repositories, or it changed. A sandbox needs at least one repository; empty folders run on This machine.'
          : 'The selection changed or cannot be used safely. Select it again.'); return
      }
      if (entry.kind === 'directory') {
        const expandedMembers = body.selection.members
        const union = [...current.members]
        const labels = [...names]
        expandedMembers.forEach((m,i) => { if (!union.some(x => x.source_identity === m.source_identity)) { union.push(m); labels.push(body.members[i].name) } })
        if (union.length > 20) { setError('Select at most 20 repositories across all checked folders.'); restore(); return }
        if (current.members.some(m => !(entry.path === '.' || m.path.startsWith(`${entry.path}/`)))) {
          const united = { ...current, members: union, folder: undefined, membership_snapshot: undefined }
          const validation = await apiFetch('/api/workspace/preview', { method:'POST', headers:{'Content-Type':'application/json'}, signal:controller.current?.signal, body:JSON.stringify({runtime:runtime === 'docker' ? 'sandbox' : 'host',selection:withLayout(united),expand:false}) })
          const checked = preview(await validation.json() as Preview | null)
          if (operation !== sequence.current || controller.current?.signal.aborted) return
          if (!validation.ok || !checked.ok) { setError('The combined selection overlaps or changed. Select it again.');restore();return }
          publish(checked.selection,checked.members.map(m=>m.name),false,checked.structure_fallback,checked.member_paths)
          if (!expandedMembers.length) setError(`${entry.name} has no Git repositories, so nothing was added. To start in an empty folder, select it on its own.`)
        } else { publish(body.selection,body.members.map(m=>m.name),true,body.structure_fallback,body.member_paths) }
        setFolderSelections(old=>({...old,[entry.path]:expandedMembers}));setSubsetFolder(null)
      } else { publish(body.selection,body.members.map(m=>m.name),false,body.structure_fallback,body.member_paths) }
    } catch {
      if (!controller.current?.signal.aborted) setError('The selection could not be checked. Try again.')
    } finally { if (operation === sequence.current) setBusy(false) }
  }
  async function expand(entry: Entry) {
    const open = isOpen(entry)
    if (query) setSearchExpanded(old => ({ ...old, [entry.path]: !open }))
    else setExpanded(old => { const next = new Set(old); if (open) next.delete(entry.path); else next.add(entry.path); return next })
    if (open) return
    if (!pages[entry.path]) {
      try { await load(entry.path, '', controller.current?.signal) } catch { if (!controller.current?.signal.aborted) setError('The folder changed or is unavailable.') }
    }
  }
  const query = search.trim().toLowerCase()
  // Search can reveal loaded descendants without changing the saved expansion.
  // Explicit disclosure choices override that automatic expansion until the query changes.
  function isOpen(entry: Entry) {
    return query ? searchExpanded[entry.path] ?? (expanded.has(entry.path) || !!pages[entry.path]) : expanded.has(entry.path)
  }
  function setQuery(value: string) { setSearch(value); setSearchExpanded({}) }
  function matches(entry: Entry): boolean {
    return !query || entry.path.toLowerCase().includes(query) || entry.name.toLowerCase().includes(query)
      || !!pages[entry.path]?.entries.some(matches)
  }
  function recentRank(path: string, name: string, directory = false) {
    const rank = recentRepos.findIndex(repo => repo === path || repo === name || directory && repo.startsWith(`${path}/`))
    return rank < 0 ? Infinity : rank
  }
  function ordered(entries: Entry[]) {
    return [...entries].sort((a, b) => recentRank(a.path, a.name, a.kind === 'directory') - recentRank(b.path, b.name, b.kind === 'directory') || a.name.localeCompare(b.name))
  }
  function focusStep(step: number, edge?: 'first' | 'last') {
    const checks = Array.from(tree.current?.querySelectorAll<HTMLElement>(cluster ? 'input[type=checkbox]:not(:disabled)' : '[role=treeitem]') ?? [])
    const index = checks.indexOf(document.activeElement as HTMLElement)
    checks[edge === 'first' ? 0 : edge === 'last' ? checks.length - 1 : Math.max(0, Math.min(checks.length - 1, index + step))]?.focus()
  }
  function navigate(event: KeyboardEvent, entry?: Entry) {
    if (event.nativeEvent.isComposing) return
    if (['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) {
      event.preventDefault()
      focusStep(event.key === 'ArrowUp' ? -1 : 1, event.key === 'Home' ? 'first' : event.key === 'End' ? 'last' : undefined)
    } else if (entry && (event.key === 'ArrowRight' || event.key === 'ArrowLeft')) {
      event.preventDefault()
      if (event.key === 'ArrowRight') {
        if (!isOpen(entry)) void expand(entry)
        else tree.current?.querySelector<HTMLElement>(`[data-tree-path="${CSS.escape(entry.path)}"] > [role=group] > [role=treeitem]`)?.focus()
      } else if (isOpen(entry)) void expand(entry)
      else {
        const parent = entry.path.includes('/') ? entry.path.slice(0, entry.path.lastIndexOf('/')) : '.'
        Array.from(tree.current?.querySelectorAll<HTMLElement>('[role=treeitem]') ?? []).find(item => item.dataset.treePath === parent)?.focus()
      }
    } else if (entry && (event.key === 'Enter' || event.key === ' ')) {
      event.preventDefault(); void toggle(entry)
    }
  }
  const visiblePaths: string[] = []
  function collectVisible(entry: Entry) {
    visiblePaths.push(entry.path)
    if (isOpen(entry)) ordered(pages[entry.path]?.entries ?? []).filter(matches).forEach(collectVisible)
  }
  if (root) collectVisible(root.root)
  const tabPath = visiblePaths.includes(focusedPath) ? focusedPath : visiblePaths[0]
  function rows(entry: Entry, depth: number): React.ReactNode {
    const page = pages[entry.path]
    const checked = entry.kind === 'repository' ? !!selection?.members.some(m => m.source_identity === entry.source_identity)
      : !!folderSelections[entry.path] && (folderSelections[entry.path].length ? folderSelections[entry.path].every(m => selection?.members.some(x=>x.source_identity===m.source_identity)) : selection?.folder?.path===entry.path)
    const partial = entry.kind === 'directory' && !checked && !!selection?.members.some(m => entry.path === '.' || m.path.startsWith(`${entry.path}/`))
    const open = isOpen(entry)
    return <div key={entry.path} role="treeitem" data-tree-path={entry.path} aria-label={`${entry.name} ${entry.kind === 'directory' ? 'Folder' : 'Git repository'}`}
      aria-level={depth + 1} aria-expanded={open} aria-checked={partial ? 'mixed' : checked} aria-disabled={busy}
      tabIndex={entry.path === tabPath ? 0 : -1} onFocus={event => { if (event.target === event.currentTarget) setFocusedPath(entry.path) }}
      onKeyDown={event => { if ((event.target as HTMLElement).closest('[role=treeitem]') === event.currentTarget) navigate(event, entry) }}>
      <div onClick={event => event.currentTarget.parentElement?.focus()} className={`workspace-tree-row${checked ? ' is-selected' : ''}`} style={{ paddingInlineStart: depth * 18 + 8 }}>
        <button type="button" tabIndex={-1} aria-expanded={open} aria-label={`${open ? 'Collapse' : 'Expand'} ${entry.name}`} onClick={() => void expand(entry)}>{open ? '▾' : '▸'}</button>
        <label title={entry.path}><input type="checkbox" tabIndex={-1} data-path={entry.path} ref={input => { if (input) input.indeterminate = partial }} checked={checked} disabled={busy} onChange={() => void toggle(entry)} />
          {entry.kind === 'directory' ? <FileIcon name={entry.name} directory expanded={open} /> : <RepositoryGlyph />}
          <span>{entry.name}</span><small className="workspace-kind-hidden">{entry.kind === 'repository' ? 'Git repository' : 'Folder'}</small>
          {entry.kind === 'repository' && recentRank(entry.path, entry.name) < Infinity && <small aria-hidden="true">Recent</small>}
        </label>
      </div>
      {open && <div role="group">
        {page && ordered(page.entries).filter(matches).map(child => rows(child, depth + 1))}
        {page?.next_cursor && <button type="button" className="workspace-more" onClick={() => void load(entry.path, page.next_cursor, controller.current?.signal).catch(() => setError('The folder changed. Refresh the tree.'))}>Show more entries</button>}
        {page?.truncated && !page.next_cursor && <p className="workspace-note">Discovery reached its safety bound. Select a smaller folder.</p>}
      </div>}
    </div>
  }
  function toggleHosted(repo: RepoInfo) {
    const hosted = selection?.hosted ?? []
    const exists = hosted.some(x => x.provider === (repo.provider ?? 'github') && x.full_name === repo.full_name)
    if (!exists && hosted.length >= 20) { setError('Select at most 20 repositories.'); return }
    const next = exists ? hosted.filter(x => !(x.provider === (repo.provider ?? 'github') && x.full_name === repo.full_name)) : [...hosted, { provider: repo.provider ?? 'github', full_name: repo.full_name }]
    publish(next.length ? { members: [], hosted: next } : null, next.map(x => x.full_name))
  }
  return <section className="workspace-picker" aria-label="Workspace repositories">
    <input className="settings-field" aria-label="Search repositories" placeholder={cluster ? 'Search org/name' : 'Search folders and repositories'} value={search}
      onChange={e => setQuery(e.target.value)} onKeyDown={event => {
        if (event.key === 'ArrowDown') { event.preventDefault(); focusStep(1, 'first') }
        else if (event.key === 'Escape' && search) { event.preventDefault(); event.stopPropagation(); setQuery('') }
      }} />
    {actions}
    <div hidden={hideTree}>
    <div className="workspace-picker-heading"><strong>Selected repositories</strong><span>{cluster ? selection?.hosted?.length ?? 0 : selection?.members.length ?? 0} / 20</span></div>
    {!!names.length && <div className="workspace-chips">{names.map((name, i) => <span key={`${i}:${name}`} title={selection?.members[i]?.path ?? name}>{name}<button type="button" disabled={busy} aria-label={`Remove ${name}`} onClick={() => {
      if (cluster) { const hosted = selection!.hosted!.filter((_, n) => n !== i); publish(hosted.length ? { members: [], hosted } : null, names.filter((_, n) => n !== i)) }
      else { const members = selection!.members.filter((_, n) => n !== i); void recheck(members.length ? { ...selection!, members, membership_snapshot: selection!.membership_snapshot?.startsWith('explicit:') ? undefined : selection!.membership_snapshot } : null) }
    }}>×</button></span>)}</div>}
    {cluster ? <>
      <div ref={tree} className="workspace-tree" role="group" aria-label="Hosted repositories" onKeyDown={event => { if (event.target instanceof HTMLInputElement) { if (event.key === 'Enter') { event.preventDefault(); event.target.click() } else navigate(event) } }}>{[...repos].sort((a, b) => recentRank(a.full_name, a.full_name) - recentRank(b.full_name, b.full_name) || a.full_name.localeCompare(b.full_name)).filter(r => !r.is_local && r.full_name.includes('/') && r.full_name.toLowerCase().includes(query)).map(repo => <label className="workspace-hosted" key={`${repo.provider}:${repo.full_name}`}><input type="checkbox" checked={!!selection?.hosted?.some(x => x.provider === (repo.provider ?? 'github') && x.full_name === repo.full_name)} onChange={() => toggleHosted(repo)} /><RepositoryGlyph /><span title={repo.full_name}><b>{repo.name}</b><small>{repo.full_name}</small></span>{recentRank(repo.full_name, repo.full_name) < Infinity && <small aria-hidden="true">Recent</small>}<small data-testid={repo.provider && repo.provider !== 'github' ? 'repo-provider' : undefined}>{providerLabel(repo.provider ?? 'github')}</small></label>)}</div>
    </> : !daemon ? <p>Choose a desktop.</p> : daemon.discovery_version !== 1 ? <p>Update this desktop to browse folders safely.</p> : root ? <div ref={tree} className="workspace-tree" role="tree" aria-multiselectable="true" aria-label="Configured folder tree">{rows(root.root, 0)}{query && !root.entries.some(matches) && <p className="workspace-note">No loaded repositories or folders match.</p>}</div> : <p>Loading folders…</p>}

    {query && <p className="workspace-note">{cluster ? 'Searches hosted repositories.' : 'Searches loaded folders; expand a folder to include its contents.'}</p>}
    {selection?.folder && (selection.members.length === 0
      ? <p className="workspace-note" role="note" aria-label="Folder without repositories"><b title={selection.folder.path}>{selection.folder.path === '.' ? 'This folder' : selection.folder.path.split('/').pop()}</b> has no Git repositories. The session starts in it as a plain folder, on This machine only.</p>
      : <details className="workspace-note"><summary>Folder workspace: relative paths are preserved.</summary><p>New repositories are never added to this selection automatically.</p></details>)}
    {!cluster && !selection?.folder && (selection?.members.length ?? 0) > 1 && <>
      <label className="workspace-layout"><input type="checkbox" checked={keepStructure} disabled={busy} onChange={event => void changeLayout(event.target.checked)} aria-describedby="workspace-layout-help" />Keep folder structure</label>
      <p className="workspace-note" id="workspace-layout-help">Place repositories at their paths under the folder they share, instead of side by side by name.</p>
      {structureFallback && <p className="workspace-note" role="status">Folder structure is unavailable because a path component cannot be shown safely. Repositories will appear side by side by name.</p>}
    </>}
    {!cluster && !selection?.folder && names.length > 1 && <details className="workspace-note" role="note" aria-label="Workspace launch confirmation"><summary>Separate repositories use a virtual workspace.</summary><p>{keepStructure && !structureFallback ? 'Repositories appear at their paths under their deepest shared folder. Only selected repositories are included.' : 'Repositories appear at the workspace root under their names; cross-repository relative imports are not preserved.'} {runtime === 'daemon' && <>This machine uses a virtual tree. The shell starts in <b>{names[0]}</b> {memberPaths[0] && <>({memberPaths[0]})</>}, the first explicitly selected repository.</>}</p></details>}
    </div>
    {busy && <p role="status">Checking selection…</p>}
    {needsRecheck && !busy && <p className="workspace-note" role="status">The selection needs to be checked before launch. {error !== desktopUpdateMessage && <button type="button" className="btn small" onClick={() => void recheck(selection)}>Recheck selection</button>}</p>}
    {error && <p role="alert">{error}</p>}
  </section>
}

/** Same 16px canvas and stroke as the explorer's folder glyph. */
function RepositoryGlyph() {
  return <svg className="artifact-glyph" viewBox="0 0 16 16" width="16" height="16" aria-hidden="true"><path d="M4 2h8v12H4a2 2 0 0 1 0-4h8M4 2v8M7 4v4M9 5v3M7 6h2M4 12h3" fill="none" stroke="currentColor" strokeWidth="1" strokeLinejoin="round" /></svg>
}
