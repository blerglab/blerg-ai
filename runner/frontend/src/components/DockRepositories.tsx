import DockHeading from './DockHeading'
import DockRowMenu from './DockRowMenu'
import { useDockRowMenu } from '../hooks/useDockRowMenu'
import { ConfirmPopover, IconButton } from '@blerglab/chat'
import { useCallback, useEffect, useId, useLayoutEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { useContext } from 'react'
import { GraphTabContext } from '../lib/graphTabContext'
import { visibleText } from '@blerglab/chat'
import RuntimeIcon from './RuntimeIcon'
import { ENGINE_LABELS } from '../lib/runtimes'
import { sessionNoun } from '../lib/sessionPresentation'
import { connected, onOpen as onConnectionOpen } from '../ws'
import { apiFetch } from '../apiFetch'
import { useSessionStore } from '../hooks/useSessionStore'
import { RefreshScheduler, watchTurnEnds } from '../lib/explorerAutoRefresh'
import type { SessionInfo } from '../types'
import type { TreeStatus } from '../lib/workspaceTree'
import { workspaceStatusReason } from '../lib/workspaceStatusReason'
import { fetchDaemons, fetchRepos, type DaemonApiInfo, type RepoInfo } from '../lib/launchCache'
import WorkspacePicker, { type WorkspaceSelection } from './WorkspacePicker'
import { RepositoryDecision } from './RepositoryRequest'
import './IconButton.css'
import './DockRepositories.css'

function RepositoryFullName({ full }: { full: string }) {
  const name = full.lastIndexOf('/') + 1
  return <span className="repository-name-text" title={full}><span className="repository-owner">{full.slice(0, name)}</span>{full.slice(name)}</span>
}

const shortBranch = (branch: string) => { const text = visibleText(branch); return text.length > 28 ? `${text.slice(0, 15)}…${text.slice(-10)}` : text }

type Member = NonNullable<SessionInfo['workspace']>['members'][number]
function usesMember(session: SessionInfo, member: Member, desktop: string): boolean {
  return session.workspace?.members.some(other => member.source_identity && other.source_identity
    ? member.source_identity === other.source_identity
    : session.daemon_id === desktop && member.name === other.name && member.path === other.path) ?? false
}
function workspaceSessions(sessions: SessionInfo[], main: SessionInfo | undefined): SessionInfo[] {
  if (!main) return []
  const children = new Map<string, SessionInfo[]>()
  for (const session of sessions) {
    if (!session.parent_session_id) continue
    const siblings = children.get(session.parent_session_id) ?? []
    siblings.push(session)
    children.set(session.parent_session_id, siblings)
  }
  const workspace = [main]
  const seen = new Set([main.id])
  for (let i = 0; i < workspace.length; i++) {
    for (const child of children.get(workspace[i].id) ?? []) {
      if (seen.has(child.id)) continue
      seen.add(child.id)
      workspace.push(child)
    }
  }
  return workspace
}
// Status shares a single reader slot with file browsing and file previews. Match the
// explorer's bounded busy retries instead of turning temporary contention into an error.
const BUSY_RETRY_MS = [150, 300, 600, 1000, 1500, 2000, 2500]
function busyDelay(signal: AbortSignal, delay: number) {
  return new Promise<void>((resolve, reject) => {
    const finish = () => { signal.removeEventListener('abort', abort); resolve() }
    const timer = window.setTimeout(finish, delay)
    const abort = () => { window.clearTimeout(timer); signal.removeEventListener('abort', abort); reject(new DOMException('Cancelled', 'AbortError')) }
    if (signal.aborted) abort()
    else signal.addEventListener('abort', abort, { once: true })
  })
}
async function readRepositoryStatus(sessionId: string, path: string, signal: AbortSignal) {
  const query = new URLSearchParams({ op: 'status', path: path === '.' ? '' : path })
  for (let attempt = 0; ; attempt++) {
    const response = await apiFetch(`/api/sessions/${encodeURIComponent(sessionId)}/workspace?${query}`, { signal })
    const reply = await response.json().catch(() => null)
    const busy = response.status === 409 && reply?.code === 'busy' || response.ok && reply?.result?.error_code === 'busy'
    if (busy && attempt < BUSY_RETRY_MS.length) {
      await busyDelay(signal, BUSY_RETRY_MS[attempt])
      continue
    }
    const status: TreeStatus | null = response.ok && reply?.result?.ok ? reply.result.status ?? null : null
    const error = status?.partial ? 'The state read was incomplete. Try again.' : !status ? busy ? 'Another workspace read is in progress. Try again when it finishes.' : workspaceStatusReason(reply?.result?.status_reason) || 'The repository state could not be read.' : ''
    return { status, error, generation: reply?.result?.generation as number | undefined, launchGeneration: reply?.result?.launch_generation as string | undefined }
  }
}

interface MembershipOperation { id: string; kind: 'add' | 'remove'; phase: string; requested_by: 'person' | 'main'; member_id: string; member_name: string; member_path?: string; proposed_digest: string; waiting?: string; waiting_code?: string; refusal?: string; local_files?: string[]; local_count?: number; local_digest?: string }
interface Membership { revision: number; unsupported?: string; add_blocked?: string; blockers: Record<string, string>; operation?: MembershipOperation; last?: MembershipOperation }
const MEMBERSHIP_POLL = 3000

// Reuse offline consent withdrawal: it journals denial on the server and
// requires actual cleanup before membership can continue. No timeout is proof.
function EndWorkspaceHostShells({ session }: { session: string }) {
  const trigger = useRef<HTMLButtonElement | null>(null)
  const [confirming, setConfirming] = useState(false)
  const [busy, setBusy] = useState(false)
  const [requested, setRequested] = useState(false)
  const [message, setMessage] = useState('')
  const end = async () => {
    setBusy(true)
    setMessage('')
    try {
      const ids = new Set<string>()
      const cursors = new Set<string>()
      let after = ''
      for (let pages = 0; ; pages++) {
        if (pages >= 128 || cursors.has(after)) throw new Error('pagination')
        cursors.add(after)
        const response = await apiFetch(`/api/sessions/${encodeURIComponent(session)}/shell-consents${after ? `?after=${encodeURIComponent(after)}` : ''}`)
        if (!response.ok) throw new Error('list')
        const page = await response.json() as { consents?: { id: string; withdrawn?: boolean }[]; next?: string }
        if (!Array.isArray(page.consents) || page.consents.length > 32 || page.consents.some(c => !c || typeof c.id !== 'string' || !c.id)) throw new Error('shape')
        for (const consent of page.consents) if (!consent.withdrawn) ids.add(consent.id)
        after = page.next ?? ''
        if (!after) break
      }
      if (!ids.size) {
        setMessage('No active host shell consent was found. Blerg will keep retrying the repository change.')
        return
      }
      for (const id of ids) {
        const response = await apiFetch(`/api/sessions/${encodeURIComponent(session)}/shell-consent/withdraw`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ consent_id: id }) })
        if (!response.ok) throw new Error('withdraw')
        const result = await response.json() as { withdrawn?: boolean; cleanup_pending?: boolean }
        if (result.withdrawn !== true || typeof result.cleanup_pending !== 'boolean') throw new Error('unconfirmed')
      }
      setMessage('Host shell consent withdrawn. The repository change will continue after the keeper confirms cleanup.')
      setRequested(true)
    } catch {
      setMessage('Host shell cleanup could not be requested. Try again or restore the keeper.')
    } finally { setBusy(false) }
  }
  return <div className="repository-pending">
    {!requested && <>
      <p className="repository-state">End the host shells you authorized for this workspace. Other sign-ins must end theirs. The agent keeps running.</p>
      <button ref={trigger} type="button" className="settings-action btn small" disabled={busy} onClick={() => setConfirming(true)}>{busy ? 'Ending host shells…' : 'End host shells and continue'}</button>
    </>}
    {confirming && <ConfirmPopover anchor={trigger}
      title="End host shells?"
      description="This ends the shells you opened on this desktop for this workspace, and anything still running in them. Other sign-ins keep theirs. The agent keeps running."
      confirmLabel="End host shells" cancelLabel="Cancel" danger
      onConfirm={() => { setConfirming(false); void end() }} onCancel={() => setConfirming(false)} />}
    {message && <p className="repository-notice" role="status">{message}</p>}
  </div>
}

function changeText(operation: MembershipOperation): string {
  const verb = operation.kind === 'add' ? 'Adding.' : 'Removing.'
  return `${verb} ${operation.waiting || 'Waiting for a safe moment.'}`
}
function refusalText(operation: MembershipOperation): string {
  return `Couldn't ${operation.kind} ${operation.member_name}. ${operation.refusal ?? ''}`.trim()
}

// The launch sheet's picker, one repository at a time. Choosing grants nothing:
// the server resolves and journals the choice, and the runtime proves it again.
function AddRepository({ session, onAdd, onCancel }: { session: SessionInfo; onAdd(selection: WorkspaceSelection): Promise<string | null>; onCancel(): void }) {
  const [daemon, setDaemon] = useState<DaemonApiInfo | null>(null)
  const [repos, setRepos] = useState<RepoInfo[]>([])
  const [selection, setSelection] = useState<WorkspaceSelection | null>(null)
  const [names, setNames] = useState<string[]>([])
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  useEffect(() => {
    let live = true
    fetchDaemons().then(list => { if (live) setDaemon(list.find(d => d.id === session.daemon_id) ?? null) }).catch(() => { if (live) setError('The desktop could not be reached.') })
    fetchRepos().then(part => { if (live) setRepos(part.repos ?? []) }).catch(() => undefined)
    return () => { live = false }
  }, [session.daemon_id])
  const count = selection?.members.length ?? 0
  return <div className="repository-add-panel" role="group" aria-label="Add a repository">
    {daemon ? <WorkspacePicker daemon={daemon} runtime={session.runtime === 'docker' ? 'docker' : 'daemon'} repos={repos} onChange={(next, picked) => { setSelection(next); setNames(picked); setError('') }} /> : !error && <p className="workspace-note">Loading repositories…</p>}
    {count > 1 && <p className="workspace-note">Choose one repository at a time.</p>}
    {error && <p className="repository-notice" role="alert">{error}</p>}
    <div className="repository-add-buttons">
      <button type="button" className="settings-action btn small" disabled={busy || count !== 1} onClick={() => {
        if (!selection) return
        setBusy(true)
        void onAdd(selection).then(problem => { setBusy(false); if (problem) setError(problem) })
      }}>{count === 1 ? `Add ${names[0] ?? 'repository'}` : 'Add'}</button>
      <button type="button" className="settings-action btn small" onClick={onCancel}>Cancel</button>
    </div>
  </div>
}

/** Membership comes from the authorized revision. Reading Git never discovers or adopts repositories. */
export default function DockRepositories({ sessionId, active, onOpen, focusedMember }: { focusedMember?: string; sessionId: string; active: boolean; onOpen?(session: SessionInfo, keep?: boolean): void }) {
  const rowMenu = useDockRowMenu<Member>()
  const openGraph = useContext(GraphTabContext)
  const root = useRef<HTMLElement>(null)
  const sessions = useSessionStore(s => s.sessions)
  const main = sessions.find(s => s.id === sessionId)
  const members = (main?.workspace?.members ?? []).filter(member => !focusedMember || member.id === focusedMember)
  const workspace = workspaceSessions(sessions, main)
  const [snapshot, setSnapshot] = useState<{ scope: string; statuses: Record<string, TreeStatus | null>; errors: Record<string, string>; results?: SessionInfo['workspace_results'] }>({ scope: '', statuses: {}, errors: {} })
  const [retryVersion, setRetryVersion] = useState(0)
  const [membership, setMembership] = useState<Membership | null>(null)
  const noticeKey = `blerg:repository-notice-dismissed:${sessionId}`
  const readDismissed = () => { try { return localStorage.getItem(noticeKey) } catch { return null } }
  const [dismissed, setDismissed] = useState(() => ({ sessionId, id: readDismissed() }))
  if (dismissed.sessionId !== sessionId) setDismissed({ sessionId, id: readDismissed() })
  const last = membership?.last?.phase !== 'denied' && membership?.last?.id !== dismissed.id ? membership?.last : undefined
  const dismissNotice = () => {
    if (!last) return
    setDismissed({ sessionId, id: last.id })
    try { localStorage.setItem(noticeKey, last.id) } catch { /* Still dismiss for this visit when storage is unavailable. */ }
  }
  const membershipRead = useRef(0)
  const [adding, setAdding] = useState(false)
  const [problem, setProblem] = useState('')
  const [menu, setMenu] = useState<string | null>(null)
  useEffect(() => {
    if (menu === null) return
    const close = (event: Event) => {
      if (event instanceof KeyboardEvent ? event.key === 'Escape' : !(event.target as Element).closest('.repository-menu')) setMenu(null)
    }
    document.addEventListener('pointerdown', close)
    document.addEventListener('keydown', close)
    return () => { document.removeEventListener('pointerdown', close); document.removeEventListener('keydown', close) }
  }, [menu])
  // A stable key avoids restarting reads for unrelated live inventory updates.
  const paths = JSON.stringify(members.map(m => m.path))
  const memberIds = JSON.stringify(members.map(m => ({ path: m.path, id: m.id })))
  const scope = sessionId + paths
  const watched = JSON.stringify(workspace.filter(s => s.id === sessionId || members.some(member => usesMember(s, member, main?.daemon_id ?? ''))).map(s => s.id).sort())
  useEffect(() => {
    if (!active || !focusedMember) return
    const aborter = new AbortController()
    const read = async () => {
      const next: Record<string, TreeStatus | null> = {}
      const errors: Record<string, string> = {}
      // The workspace API permits one read per person. Keep member reads serial.
      for (const path of JSON.parse(paths) as string[]) {
        try {
          const { status, error } = await readRepositoryStatus(sessionId, path, aborter.signal)
          next[path] = status
          if (error) errors[path] = error
        } catch { next[path] = null; errors[path] = 'The repository state could not be read.' }
        if (aborter.signal.aborted) return 'cancelled' as const

      }
      let results: SessionInfo['workspace_results']
      if (!aborter.signal.aborted && Object.keys(next).length) {
        try {
          const response = await apiFetch(`/api/sessions/${encodeURIComponent(sessionId)}/result`, { signal: aborter.signal })
          if (response.ok) results = (await response.json()).workspace_results
        } catch { /* Git state stays usable if delivery results are unavailable. */ }
      }
      if (!aborter.signal.aborted) { setSnapshot({ scope, statuses: next, errors, results }) }
      return 'done' as const
    }
    const shown = () => document.visibilityState !== 'hidden' && !!root.current && (typeof root.current.checkVisibility === 'function' ? root.current.checkVisibility() : !root.current.closest('[hidden]'))
    const scheduler = new RefreshScheduler(read, () => shown() ? true : 'hidden')
    let visible = shown()
    const observer = new ResizeObserver(() => {
      const next = shown()
      if (next && !visible) scheduler.request()
      visible = next
    })
    if (root.current) observer.observe(root.current)
    scheduler.request()
    const reconnect = onConnectionOpen(() => scheduler.request())
    const watchedIds = new Set(JSON.parse(watched) as string[])
    const stops = [...watchedIds].map(id => watchTurnEnds(id, () => scheduler.request()))
    // A worker's transcript may not be open. Its inventory transition still ends a turn.
    const stopInventory = useSessionStore.subscribe((next, previous) => {
      if (next.sessions === previous.sessions) return
      const statuses = new Map(previous.sessions.map(s => [s.id, s.status]))
      if (next.sessions.some(s => watchedIds.has(s.id) && statuses.get(s.id) !== s.status && ['idle', 'waiting', 'stopped', 'ended', 'error'].includes(s.status))) scheduler.request()
    })
    const back = () => { if (document.visibilityState !== 'hidden') scheduler.request() }
    window.addEventListener('focus', back)
    document.addEventListener('visibilitychange', back)
    return () => { aborter.abort(); observer.disconnect(); scheduler.stop(); stopInventory(); reconnect(); stops.forEach(stop => stop()); window.removeEventListener('focus', back); document.removeEventListener('visibilitychange', back) }
  }, [sessionId, active, paths, memberIds, scope, watched, retryVersion, focusedMember])

  const readMembership = useCallback(async (signal?: AbortSignal) => {
    const read = ++membershipRead.current
    try {
      const response = await apiFetch(`/api/sessions/${encodeURIComponent(sessionId)}/workspace/repositories`, { signal })
      if (response.ok) {
        const next = await response.json() as Membership
        if (read === membershipRead.current) setMembership(next)
      }
    } catch { /* The rows stay usable; changes stay offered from the last answer. */ }
  }, [sessionId])
  const operation = membership?.operation
  const open = !!operation
  useEffect(() => {
    if (!active) return
    const aborter = new AbortController()
    // eslint-disable-next-line react-hooks/set-state-in-effect -- Reads the authorized revision from the server; the state is set after the await.
    if (!connected()) void readMembership(aborter.signal)
    const poll = open ? window.setInterval(() => void readMembership(aborter.signal), MEMBERSHIP_POLL) : undefined
    const reconnect = onConnectionOpen(() => void readMembership(aborter.signal))
    const changed = () => void readMembership(aborter.signal)
    window.addEventListener('blerg:repository-requests-changed', changed)
    return () => { aborter.abort(); window.clearInterval(poll); reconnect(); window.removeEventListener('blerg:repository-requests-changed', changed) }
  }, [active, open, paths, readMembership, main?.repository_request?.id])
  const submit = useCallback(async (body: Record<string, unknown>, suffix = ''): Promise<string | null> => {
    const response = await apiFetch(`/api/sessions/${encodeURIComponent(sessionId)}/workspace/repositories${suffix}`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }).catch(() => null)
    await readMembership()
    if (response?.ok) { window.dispatchEvent(new Event('blerg:repository-requests-changed')); return null }
    const reply = await response?.json().catch(() => null) as { error?: string } | null | undefined
    return reply?.error || 'The change could not be sent. Try again.'
  }, [readMembership, sessionId])
  const change = useCallback(async (body: Record<string, unknown>) => submit({ operation_id: crypto.randomUUID(), expected_revision: membership?.revision ?? 0, ...body }), [membership?.revision, submit])

  if (!main) return <p className="workspace-note">Loading workspace…</p>
  const unsupported = membership?.unsupported
  const canChange = !!membership && !unsupported && !operation
  const header = <>
    {<DockHeading title={focusedMember ? members[0]?.name ?? 'Repository' : main?.title ? `${main.title} repositories` : 'Repositories'} action="Add repository" disabled={!canChange || !!membership?.add_blocked} reason={membership?.add_blocked} onAdd={membership && !unsupported ? () => { setAdding(value => !value); setProblem('') } : undefined} />}
    {unsupported && <span className="repository-unavailable" title={unsupported} aria-label={unsupported}>ⓘ</span>}
    {operation?.waiting_code === 'keeper' && <EndWorkspaceHostShells key={`${sessionId}:${operation.id}`} session={sessionId} />}
    {adding && canChange && <AddRepository session={main} onCancel={() => setAdding(false)} onAdd={async selection => {
      const failure = await change({ kind: 'add', selection })
      if (!failure) setAdding(false)
      return failure
    }} />}
    {problem && <p className="repository-notice" role="status">{problem}</p>}
    {last && <div className="repository-notice repository-notice-dismissable" role="status"><span>{refusalText(last)}</span><button type="button" className="icon-btn" title="Dismiss repository notice" aria-label="Dismiss repository notice" onClick={dismissNotice}>✕</button></div>}
  </>
  const decision = operation?.phase === 'requested' && <div className="repository-pending">
    <strong>Main asks to {operation.kind} {operation.member_name}</strong>
    <RepositoryDecision key={operation.id} sessionId={sessionId} request={operation} />
  </div>
  // A removal leaves its row at commit; it stays pending here until the runtime reopens.
  const addition = operation && operation.phase !== 'requested' && (operation.kind === 'add' || !members.some(member => member.id === operation.member_id)) && <div className="repository-pending">
    <strong>{operation.member_name}</strong>
    <p className="repository-state">{changeText(operation)}</p>
  </div>
  if (!focusedMember) return <section ref={root} aria-label="Workspace repositories">{header}{decision}{addition}<ul className="repository-list repository-name-list" aria-label="Repositories">{members.map(member => {
    const handlers = rowMenu.handlers(member)
    const pending = operation && operation.member_id === member.id ? changeText(operation) : ''
    return <li key={member.id || member.path}><button type="button" className="repository-name-link" onContextMenu={handlers.onContextMenu} onKeyDown={handlers.onKeyDown} onPointerDown={handlers.onPointerDown} onPointerMove={handlers.onPointerMove} onPointerUp={handlers.onPointerUp} onPointerCancel={handlers.onPointerCancel} onClickCapture={handlers.onClickCapture} onClick={() => member.id && openGraph?.(sessionId, member.id, member.name)} title={`Open branch history for ${member.name}`}><svg className="repository-glyph" aria-hidden="true" viewBox="0 0 16 16" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round"><circle cx="4.5" cy="3.5" r="1.75" /><circle cx="4.5" cy="12.5" r="1.75" /><circle cx="11.5" cy="5.5" r="1.75" /><path d="M4.5 5.25v5.5M11.5 7.25c0 2.5-3 2.75-5.5 3.75" /></svg><RepositoryFullName full={member.project || member.name} /></button>{pending && <p className="repository-state">{pending}</p>}</li>
  })}</ul>{!members.length && <p className="workspace-note">No repositories in this workspace.</p>}{rowMenu.menu && <DockRowMenu label="Repository actions" x={rowMenu.menu.x} y={rowMenu.menu.y} close={rowMenu.close} actions={[{ label: 'Remove repository', title: membership?.blockers?.[rowMenu.menu.data.id!], disabled: !canChange || !!membership?.blockers?.[rowMenu.menu.data.id!], run: () => { const member = rowMenu.menu!.data; rowMenu.close(); void change({ kind: 'remove', member_id: member.id }).then(failure => setProblem(failure ?? '')) } }]} />}</section>
  if (!members.length) return <section ref={root} className={focusedMember ? 'repository-details' : undefined} aria-label="Workspace repositories">{header}{decision}{addition}<p className="workspace-note">{main.workspace || !main.repo ? 'No repositories in this workspace.' : 'Repository details are unavailable.'}</p></section>
  return <section ref={root} className={focusedMember ? 'repository-details' : undefined} aria-label="Workspace repositories">{header}{decision}{addition}<ul className="repository-list" aria-label="Repositories">
    {members.map(member => {
      const status = snapshot.scope === scope ? snapshot.statuses[member.path] : undefined
      const result = (snapshot.scope === scope ? snapshot.results ?? main.workspace_results : main.workspace_results)?.find(r => r.member_id === member.id)
      const failure = snapshot.scope === scope ? snapshot.errors[member.path] : undefined
      const pending = result?.pending || result?.sync_error || result?.push_error
      const changes = status ? status.changed + status.untracked : 0
      const state = failure ? "Git status unavailable" : changes > 0 ? `${changes} uncommitted` : !status ? 'Reading state…' : pending ? 'Commits not delivered' : 'Clean'
      const branch = status?.branch === '(detached)' || status?.branch === 'HEAD' ? status.head?.slice(0, 7) || 'Detached' : status?.branch || result?.branch || '—'
      const owners = workspace.filter(session => !session.archived_at && !session.ended_at && !['stopped', 'ended', 'error'].includes(session.status) && usesMember(session, member, main.daemon_id)).flatMap(session => {
        const ownMember = session.workspace?.members.find(other => member.source_identity && other.source_identity ? member.source_identity === other.source_identity : member.name === other.name && member.path === other.path)
        const ownBranch = session.id === sessionId ? status?.branch || result?.branch : session.workspace_results?.find(item => item.member_id === ownMember?.id)?.branch
        return ownBranch && ownBranch !== 'HEAD' && ownBranch !== '(detached)' ? [{ session, branch: ownBranch }] : []
      })
      const removing = operation?.kind === 'remove' && operation.member_id === member.id && operation.phase !== 'requested'
      const blocker = (member.id ? membership?.blockers?.[member.id] : undefined) || (changes > 0 ? 'Commit or discard the changes first.' : pending ? 'Its commits have not been delivered yet.' : '')
      const note = removing ? changeText(operation) : blocker
      const refused = last?.local_digest && last.member_id === member.id ? last : undefined
      return <li className="repository-row" key={member.path} aria-label={`${member.name}, ${visibleText(branch)}, ${state}`}>
        <span className="repository-name" title={member.name}>{member.name}</span>
        <span className="repository-branch" title={visibleText(branch)}>{shortBranch(branch)}</span>
        <span className="repository-status" role="img" aria-label={state} title={failure || state}>{failure ? "!" : changes ? "●" : !status ? "◌" : pending ? "↑" : "✓"}</span>
        {pending && state !== 'Commits not delivered' && <span className="repository-status" role="img" aria-label="Commits not delivered" title="Commits not delivered">↑</span>}
        {failure && <IconButton label={`Retry state for ${member.name}`} onClick={() => setRetryVersion(version => version + 1)} />}
        {membership && !unsupported && <div className="repository-menu"><button type="button" className="icon-btn" aria-label={`Actions for ${member.name}`} aria-expanded={menu === member.path} onClick={() => setMenu(menu === member.path ? null : member.path)}>⋯</button>{menu === member.path && <div className="repository-menu-items"><button type="button" className="btn small" title={blocker || "Remove repository"} aria-label={`Remove ${member.name}`} disabled={!canChange || !!blocker} onClick={() => { setMenu(null); void change({ kind: 'remove', member_id: member.id }).then(failure => setProblem(failure ?? '')) }}>Remove</button></div>}</div>}
        {result?.sync_note && <p className="repository-state">{result.sync_note}</p>}
        {removing && <p className="repository-state">{note}</p>}
        {refused && <LocalFiles key={refused.id} operation={refused} disabled={!canChange} onDiscard={() => void change({ kind: 'remove', member_id: member.id, discard_local: refused.local_digest }).then(failure => setProblem(failure ?? ''))} />}
        {owners.length > 0 && <ul className="repository-branches" aria-label={`Current branches for ${member.name}`}>{owners.map(owner => <li key={owner.session.id}><RepositoryBranch session={owner.session} branch={owner.branch} onOpen={onOpen} /></li>)}</ul>}
      </li>
    })}
  </ul></section>
}

// Remove never deletes what it can't prove delivered. Files Git ignores go only
// after the person confirms these exact ones; the runtime refuses if they changed.
function LocalFiles({ operation, disabled, onDiscard }: { operation: MembershipOperation; disabled: boolean; onDiscard(): void }) {
  const [confirming, setConfirming] = useState(false)
  const names = operation.local_files ?? []
  const more = (operation.local_count ?? names.length) - names.length
  return <div className="repository-local" role="group" aria-label={`Local files in ${operation.member_name}`}>
    <ul>{names.map(name => <li key={name}>{name}</li>)}{more > 0 && <li>and {more} more</li>}</ul>
    {confirming ? <div className="repository-add-buttons">
      <button type="button" className="settings-action btn small" disabled={disabled} onClick={onDiscard}>Delete files and remove</button>
      <button type="button" className="settings-action btn small" onClick={() => setConfirming(false)}>Keep files</button>
    </div> : <button type="button" className="settings-action btn small" disabled={disabled} onClick={() => setConfirming(true)}>Discard files…</button>}
  </div>
}

/** Existing inventory only: hovering or holding a branch never starts another Git read. */
function RepositoryBranch({ session, branch, onOpen }: { session: SessionInfo; branch: string; onOpen?(session: SessionInfo, keep?: boolean): void }) {
  const button = useRef<HTMLButtonElement>(null)
  const timer = useRef<number | undefined>(undefined)
  const held = useRef(false)
  const origin = useRef({ x: 0, y: 0 })
  const id = useId()
  const [position, setPosition] = useState<{ left: number; top: number } | null>(null)
  const cancel = () => window.clearTimeout(timer.current)
  const close = () => { cancel(); setPosition(null) }
  const show = () => {
    const rect = button.current?.getBoundingClientRect()
    if (rect) setPosition({ left: Math.max(8, Math.min(rect.left, window.innerWidth - 288)), top: rect.bottom + 110 > window.innerHeight ? Math.max(8, rect.top - 110) : rect.bottom + 4 })
  }
  useEffect(() => () => window.clearTimeout(timer.current), [])
  useLayoutEffect(() => {
    if (!position) return
    const dismiss = (event: Event) => {
      if (event.type === 'keydown' && (event as KeyboardEvent).key !== 'Escape') return
      window.clearTimeout(timer.current); setPosition(null)
    }
    window.addEventListener('pointerdown', dismiss)
    window.addEventListener('keydown', dismiss)
    window.addEventListener('scroll', dismiss, true)
    window.addEventListener('resize', dismiss)
    return () => { window.removeEventListener('pointerdown', dismiss); window.removeEventListener('keydown', dismiss); window.removeEventListener('scroll', dismiss, true); window.removeEventListener('resize', dismiss) }
  }, [position])
  const title = session.title || `Untitled ${sessionNoun(session)}`
  return <>
    <button type="button" ref={button} className="repository-branch-link" aria-label={visibleText(branch)} aria-describedby={position ? id : undefined}
      onPointerEnter={event => { if (event.pointerType !== 'touch') { cancel(); timer.current = window.setTimeout(show, 250) } }}
      onPointerLeave={event => { if (event.pointerType !== 'touch') close() }} onFocus={event => { if (event.currentTarget.matches(':focus-visible')) { cancel(); timer.current = window.setTimeout(show, 250) } }} onBlur={close}
      onPointerDown={event => { held.current = false; if (event.pointerType === 'touch') { origin.current = { x: event.clientX, y: event.clientY }; cancel(); timer.current = window.setTimeout(() => { held.current = true; show() }, 500) } }}
      onPointerMove={event => { if (event.pointerType === 'touch' && Math.hypot(event.clientX - origin.current.x, event.clientY - origin.current.y) > 8) close() }}
      onPointerUp={cancel} onPointerCancel={close} onContextMenu={event => event.preventDefault()}
      onDoubleClick={() => { close(); onOpen?.(session, true) }}
      onClick={event => { if (held.current) { event.preventDefault(); return } close(); onOpen?.(session) }}>{shortBranch(branch)}</button>
    {position && createPortal(<div role="tooltip" id={id} className="repository-branch-preview" style={position}><strong>{title}</strong><span>{ENGINE_LABELS[session.engine || 'claude'] || session.engine}</span><span><RuntimeIcon runtime={session.runtime} /> {session.status === 'error' ? 'Failed' : session.status}</span></div>, document.body)}
  </>
}
