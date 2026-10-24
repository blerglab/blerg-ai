import { useEffect, useRef, useState } from 'react'
import { apiFetch } from '../apiFetch'
import { providerLabel } from '../lib/repoRef'
import type { DaemonApiInfo, RepoInfo } from '../lib/launchCache'
import './WorkspacePicker.css'

export interface SourceRef { path: string; source_identity: string }
export interface WorkspaceSelection {
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
  onChange: (selection: WorkspaceSelection | null, names: string[]) => void
}
const ref = (e: Entry): SourceRef => ({ path: e.path, source_identity: e.source_identity })

// One tree for host and sandbox. Pages are lazy and never imply selection;
// folder checkboxes resolve a complete bounded membership snapshot separately.
export default function WorkspacePicker({ daemon, runtime, repos, onChange }: Props) {
  const [pages, setPages] = useState<Record<string, Page>>({})
  const [expanded, setExpanded] = useState<Set<string>>(new Set(['.']))
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
  const cluster = runtime === 'cluster'
  const root = pages['.']
  function publish(value: WorkspaceSelection | null, labels: string[], complete = false) {
    void complete
    setSelection(value); setNames(labels); change.current(value, labels)
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
      publish(members.length ? { ...selection!, members, membership_snapshot: selection!.membership_snapshot?.startsWith('explicit:') ? undefined : selection!.membership_snapshot } : null, names.filter((_, i) => i !== index))
      return
    }
    const priorFolder = folderSelections[entry.path]
    if (entry.kind === 'directory' && priorFolder && (priorFolder.length ? priorFolder.every(m => selection?.members.some(x => x.source_identity === m.source_identity)) : selection?.folder?.path === entry.path)) {
      const remaining = selection!.members.filter(m => !(entry.path === '.' || m.path.startsWith(`${entry.path}/`)))
      const labels = remaining.map(m => names[selection!.members.findIndex(x => x.source_identity === m.source_identity)])
      publish(remaining.length ? { ...selection!, members: remaining, folder: undefined, membership_snapshot: undefined } : null, labels)
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
        body: JSON.stringify({ runtime: runtime === 'docker' ? 'sandbox' : 'host', selection: candidate, expand: entry.kind === 'directory' }),
      })
      const body = await res.json() as { ok: boolean; code?: string; selection: WorkspaceSelection; members: { name: string }[] }
      if (operation !== sequence.current || controller.current?.signal.aborted) return
      if (!res.ok || !body.ok) {
        if (body.code === 'too_many_members' && entry.kind === 'directory' && !selection) { setSubsetFolder(ref(entry)); publish(null, []) } else { change.current(selection, names) }
        setError(body.code === 'too_many_members' ? 'This folder contains more than 20 repositories. Expand it and select an explicit subset.'
          : body.code === 'overlapping_repositories' ? 'Choose a parent repository or its nested repository. Linked worktrees sharing Git data cannot be selected together.'
          : 'The selection changed or cannot be used safely. Select it again.'); return
      }
      if (entry.kind === 'directory') {
        const expandedMembers = body.selection.members
        const union = [...current.members]
        const labels = [...names]
        expandedMembers.forEach((m,i) => { if (!union.some(x => x.source_identity === m.source_identity)) { union.push(m); labels.push(body.members[i].name) } })
        if (union.length > 20) { setError('Select at most 20 repositories across all checked folders.'); change.current(selection,names); return }
        if (current.members.some(m => !(entry.path === '.' || m.path.startsWith(`${entry.path}/`)))) {
          const united = { ...current, members: union, folder: undefined, membership_snapshot: undefined }
          const validation = await apiFetch('/api/workspace/preview', { method:'POST', headers:{'Content-Type':'application/json'}, signal:controller.current?.signal, body:JSON.stringify({runtime:runtime === 'docker' ? 'sandbox' : 'host',selection:united,expand:false}) })
          const checked = await validation.json() as {ok:boolean;selection:WorkspaceSelection;members:{name:string}[]}
          if (operation !== sequence.current || controller.current?.signal.aborted) return
          if (!validation.ok || !checked.ok) { setError('The combined selection overlaps or changed. Select it again.');change.current(selection,names);return }
          publish(checked.selection,checked.members.map(m=>m.name))
        } else { publish(body.selection,body.members.map(m=>m.name),true) }
        setFolderSelections(old=>({...old,[entry.path]:expandedMembers}));setSubsetFolder(null)
      } else { publish(body.selection,body.members.map(m=>m.name)) }
    } catch {
      if (!controller.current?.signal.aborted) setError('The selection could not be checked. Try again.')
    } finally { if (operation === sequence.current) setBusy(false) }
  }
  async function expand(entry: Entry) {
    if (expanded.has(entry.path)) { setExpanded(old => new Set([...old].filter(p => p !== entry.path))); return }
    setExpanded(old => new Set(old).add(entry.path))
    if (!pages[entry.path]) {
      try { await load(entry.path, '', controller.current?.signal) } catch { if (!controller.current?.signal.aborted) setError('The folder changed or is unavailable.') }
    }
  }
  function rows(entry: Entry, depth: number): React.ReactNode {
    const page = pages[entry.path]
    const checked = entry.kind === 'repository' ? !!selection?.members.some(m => m.source_identity === entry.source_identity)
      : !!folderSelections[entry.path] && (folderSelections[entry.path].length ? folderSelections[entry.path].every(m => selection?.members.some(x=>x.source_identity===m.source_identity)) : selection?.folder?.path===entry.path)
    return <div key={entry.path}>
      <div className={`workspace-tree-row${checked ? ' is-selected' : ''}`} style={{ paddingInlineStart: depth * 18 + 8 }}>
        <button type="button" aria-label={`${expanded.has(entry.path) ? 'Collapse' : 'Expand'} ${entry.name}`} onClick={() => void expand(entry)}>{expanded.has(entry.path) ? '▾' : '▸'}</button>
        <label><input type="checkbox" ref={input => { if (input) input.indeterminate = entry.kind === 'directory' && !checked && !!selection?.members.some(m => entry.path === '.' || m.path.startsWith(`${entry.path}/`)) }} checked={checked} disabled={busy} onChange={() => void toggle(entry)} /><span>{entry.name}</span><small className={entry.kind === 'directory' ? 'workspace-kind-hidden' : undefined}>{entry.kind === 'repository' ? 'Git' : 'Folder'}</small></label>
      </div>
      {expanded.has(entry.path) && <div role="group">
        {page?.entries.map(child => rows(child, depth + 1))}
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
    <div className="workspace-picker-heading"><strong>{cluster ? 'Repositories' : 'Folder tree'}</strong><span>{cluster ? selection?.hosted?.length ?? 0 : selection?.members.length ?? 0} / 20</span></div>
    {!!names.length && <div className="workspace-chips">{names.map((name, i) => <span key={`${i}:${name}`} title={selection?.members[i]?.path ?? name}>{name}<button type="button" disabled={busy} aria-label={`Remove ${name}`} onClick={() => {
      if (cluster) { const hosted = selection!.hosted!.filter((_, n) => n !== i); publish(hosted.length ? { members: [], hosted } : null, names.filter((_, n) => n !== i)) }
      else { const members = selection!.members.filter((_, n) => n !== i); publish(members.length ? { ...selection!, members, membership_snapshot: selection!.membership_snapshot?.startsWith('explicit:') ? undefined : selection!.membership_snapshot } : null, names.filter((_, n) => n !== i)) }
    }}>×</button></span>)}</div>}
    {cluster ? <>
      <input aria-label="Search repositories" placeholder="Search org/name" value={search} onChange={e => setSearch(e.target.value)} />
      <div className="workspace-tree" role="group" aria-label="Hosted repositories">{repos.filter(r => !r.is_local && r.full_name.includes('/') && r.full_name.toLowerCase().includes(search.toLowerCase())).map(repo => <label className="workspace-hosted" key={`${repo.provider}:${repo.full_name}`}><input type="checkbox" checked={!!selection?.hosted?.some(x => x.provider === (repo.provider ?? 'github') && x.full_name === repo.full_name)} onChange={() => toggleHosted(repo)} /><span><b>{repo.name}</b><small style={{display: 'block'}}>{repo.full_name}</small></span><small data-testid={repo.provider && repo.provider !== 'github' ? 'repo-provider' : undefined}>{providerLabel(repo.provider ?? 'github')}</small></label>)}</div>
    </> : !daemon ? <p>Choose a desktop.</p> : daemon.discovery_version !== 1 ? <p>Update this desktop to browse folders safely.</p> : root ? <div className="workspace-tree" role="group" aria-label="Configured folder tree">{rows(root.root, 0)}</div> : <p>Loading folders…</p>}

    {selection?.folder && <p className="workspace-note">Folder workspace: relative paths are preserved. {names.length === 0 ? 'Empty folders run on This machine only.' : 'New repositories are never added to this selection automatically.'}</p>}
    {!cluster && !selection?.folder && names.length > 1 && <p className="workspace-note" role="note" aria-label="Workspace launch confirmation">Repositories appear under members/&lt;id&gt;; cross-repository relative imports are not preserved. {runtime === 'daemon' && <>This machine uses a virtual tree. The shell starts in <b>{names[0]}</b> ({selection?.members[0]?.path}), the first explicitly selected repository.</>}</p>}
    {busy && <p role="status">Checking selection…</p>}
    {error && <p role="alert">{error}</p>}
  </section>
}
