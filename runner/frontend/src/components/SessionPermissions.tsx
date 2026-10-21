// SessionPermissions: the session panel's compact Permissions summary. A title, how many are
// active, a Manage button that opens the Permissions modal, then each active permission as a small
// chip (kind glyph and target) with × to revoke it at once. Permissions that no longer approve
// (their sign-in ended, or the tool changed) show apart, with why. A permission given from a chat
// card is just one more chip. Every state shown is the server's: nothing is optimistic, and a failed
// change says so. The modal holds its changes until Save, which goes through the same grant APIs.
import { useCallback, useEffect, useId, useMemo, useRef, useState } from 'react'
import { apiFetch } from '../apiFetch'
import { ENGINE_LABELS, RUNTIME_LABELS } from '../lib/runtimes'
import { useSessionBoards } from '../lib/sessionBoards'
import type { LiveDelegationGrant } from '../lib/liveState'
import { refreshChildren } from '../hooks/useChildrenSignal'
import { useChildApproval } from '../hooks/useChildApproval'
import { boardCaps, boardChip, boardDetail, boardName, boardScope, grantDetail, grantScope, grantTarget, inactiveLabel, ownLevel, readPermissions, type BoardAccess, type ProposalGrant, type PermissionsView } from '../lib/sessionPermissions'
import { CHILD_APPROVAL_NOTE, changeSummary, delegationKey, sessionChildOption, toolKey, type PermissionChanges } from '../lib/permissionsModal'
import PermissionsModal, { type ActiveEntry, type Loadable, type SaveOutcome } from './PermissionsModal'
import './SessionPermissions.css'

// Board and tool permissions could not be read: the modal still manages children, and says why
// the rest is missing.
const UNREADABLE: PermissionsView = { can_grant: false, reason: 'Board and tool permissions could not be loaded. Close this and use Refresh in the panel.', grants: [], mcp_tools: [], boards: [] }
const SIGNED_OUT = 'Needs you signed in: Renew it, or choose another level'

interface ChildGroup { key: string; label: string; chip: string; ids: string[] }

/** Child grants, one row per runtime (and desktop, when there are several), naming its engines: one
 *  permission a person recognizes, however many engines it covers. */
function childGroups(grants: LiveDelegationGrant[]): ChildGroup[] {
  const groups = new Map<string, ChildGroup & { engines: string[]; where: string }>()
  // The cluster is one place, named by its runtime; only desktops need their names told apart.
  const multipleDaemons = new Set(grants.filter(g => g.runtime !== 'cluster').map(g => g.daemon_id ?? '')).size > 1
  for (const g of grants) {
    const where = `${RUNTIME_LABELS[g.runtime] ?? g.runtime}${multipleDaemons && g.runtime !== 'cluster' ? ` · ${g.daemon_name || g.daemon_id || 'Unknown desktop'}` : ''}`
    const engine = ENGINE_LABELS[g.engine] ?? g.engine
    const key = delegationKey(g.runtime, where)
    const group = groups.get(key) ?? { key, label: '', chip: '', ids: [], engines: [], where }
    group.ids.push(g.id)
    if (!group.engines.includes(engine)) group.engines.push(engine)
    groups.set(key, group)
  }
  return [...groups.values()].map(({ engines, where, ...g }) => {
    const spoken = engines.length > 1 ? `${engines.slice(0, -1).join(', ')} and ${engines[engines.length - 1]}` : engines[0]
    return { ...g, label: `Start ${spoken} children · ${where}`, chip: `${engines.join(', ')} · ${where}` }
  })
}

async function failure(r: Response, fallback: string): Promise<string> {
  const body = (typeof r.json === 'function' ? await r.json().catch(() => ({})) : {}) as { error?: unknown }
  return typeof body.error === 'string' && body.error ? body.error : fallback
}

export default function SessionPermissions(props: {
  sessionId: string
  /** Live is on screen: the block reads the server (and keeps reading) only then. */
  active: boolean
  delegationGrants: LiveDelegationGrant[]
  /** Changes when the session's permissions changed elsewhere (a card in the chat). */
  refreshKey: string
  /** A new number opens the modal at the children (a chat card's "Change child approvals"). */
  openRequest?: number
  /** The request was taken (the modal opened): the page clears it, so a remount never reopens it. */
  onOpenRequestTaken?: () => void
}) {
  return <Summary key={props.sessionId} {...props} />
}

function Summary({ sessionId, active, delegationGrants, refreshKey, openRequest, onOpenRequestTaken }: { sessionId: string; active: boolean; delegationGrants: LiveDelegationGrant[]; refreshKey: string; openRequest?: number; onOpenRequestTaken?: () => void }) {
  const headId = useId()
  const [view, setView] = useState<PermissionsView | null>(null)
  const [state, setState] = useState<'loading' | 'ready' | 'hidden' | 'failed'>('loading')
  const [busy, setBusy] = useState<string | null>(null)
  const [error, setError] = useState('')
  const [status, setStatus] = useState('')
  const [modal, setModal] = useState<null | 'search' | 'children'>(null)
  const generation = useRef(0)
  const load = useCallback(async () => {
    const version = ++generation.current
    try {
      const r = await apiFetch(`/api/sessions/${encodeURIComponent(sessionId)}/permissions`)
      if (version !== generation.current) return
      if (r.status === 404 || r.status === 403) { setState('hidden'); return }
      if (!r.ok) throw new Error('load')
      const next = readPermissions(await r.json())
      if (version === generation.current) { setView(next); setState('ready') }
    } catch {
      if (version === generation.current) setState('failed')
    }
  }, [sessionId])
  useEffect(() => { if (active) void Promise.resolve().then(load) }, [active, load, refreshKey])
  useEffect(() => {
    if (!active || modal) return
    const t = window.setInterval(() => { if (!busy) void load() }, 15_000)
    return () => window.clearInterval(t)
  }, [active, busy, load, modal])
  useEffect(() => {
    if (!status) return
    const t = window.setTimeout(() => setStatus(''), 6000)
    return () => window.clearTimeout(t)
  }, [status])

  // A chat card asked for the children: open the modal there once the permissions have loaded.
  const [wanted, setWanted] = useState<number | undefined>(undefined)
  if (openRequest && openRequest !== wanted) setWanted(openRequest)
  const handled = useRef<number | undefined>(undefined)
  useEffect(() => {
    if (!wanted || handled.current === wanted || state === 'loading') return
    handled.current = wanted
    setModal('children')
    onOpenRequestTaken?.()
  }, [wanted, state, onOpenRequestTaken])

  async function change(key: string, run: () => Promise<Response>, fallback: string) {
    if (busy) return
    setBusy(key); setError(''); setStatus('')
    try {
      const r = await run()
      if (!r.ok) throw new Error(await failure(r, fallback))
    } catch (e) { setError(e instanceof Error ? e.message : fallback) }
    finally { await load(); setBusy(null) }
  }
  const revoke = (id: string) => change(id, () => apiFetch(`/api/proposal-grants/${encodeURIComponent(id)}/revoke`, { method: 'POST' }), 'Could not revoke the permission. Try again.')
  // A board's × lowers it to propose (every board's default), the launch board included.
  const revokeBoard = (b: BoardAccess) => change(`b:${b.board_id}`, () => apiFetch(`/api/sessions/${encodeURIComponent(sessionId)}/board-access`,
    { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ board_id: b.board_id, level: 'propose' }) }), 'Could not revoke the board access. Try again.')
  // Every grant of the row is revoked, and all of them are waited for before another try.
  const revokeChildren = (ids: string[]) => change(ids[0], async () => {
    const results = await Promise.allSettled(ids.map(id => apiFetch(`/api/delegation-grants/${encodeURIComponent(id)}/revoke`, { method: 'POST' })))
    refreshChildren(sessionId)
    const failed = results.some(r => r.status === 'rejected' || !r.value.ok)
    return { ok: !failed } as Response
  }, 'Could not revoke the permission. Try again.')

  const groups = useMemo(() => childGroups(delegationGrants), [delegationGrants])
  if (state === 'hidden') return null
  // Board permissions are listed by board (with their level); grants here are the MCP tools.
  const allGrants = (view?.grants ?? []).filter(g => g.kind === 'mcp_tool')
  const grants = allGrants.filter(g => g.active !== false)
  const dormant = allGrants.filter(g => g.active === false)
  const allBoards = view?.boards ?? []
  const boards = allBoards.filter(b => b.active !== false)
  const dormantBoards = allBoards.filter(b => b.active === false)
  const count = groups.length + boards.length + grants.length

  return (
    <section className="rail-section perm-block" aria-labelledby={headId} data-testid="rail-permissions">
      <div className="perm-head">
        <h3 className="rail-section-head" id={headId}><span>Permissions</span></h3>
        {state !== 'loading' && <span className="perm-active-count" data-testid="perm-count">{count} active</span>}
        <button type="button" className="perm-manage" data-perm-manage disabled={state === 'loading'} onClick={() => setModal('search')} aria-haspopup="dialog">Manage</button>
      </div>
      <div role="group" aria-label="Session permissions" className="perm-group">
        {state === 'loading' && !view && <p className="child-approval-note">Loading permissions…</p>}
        {view && !view.can_grant && view.reason && <p className="child-approval-note" data-testid="perm-reason">{view.reason}</p>}
        {state === 'ready' && count === 0 && dormant.length + dormantBoards.length === 0 && <p className="child-approval-note">None yet: this session asks you each time.</p>}
        {count > 0 && <ul className="perm-chips" data-testid="perm-active" aria-label="Active permissions">
          {groups.map(c => <Chip key={c.key} glyph="↳" text={c.chip} scope={c.label} title={`${c.label} without asking`} disabled={busy !== null} onRevoke={() => { void revokeChildren(c.ids) }} />)}
          {boards.map(b => <Chip key={`b:${b.board_id}`} testId="perm-board" glyph="▦" text={boardChip(b)} scope={boardScope(b)} title={`${boardScope(b)} · ${boardDetail(b)}`} disabled={busy !== null} onRevoke={() => { void revokeBoard(b) }} />)}
          {grants.map(g => <Chip key={g.id} testId="perm-grant" glyph="⚙" text={grantTarget(g)} scope={grantScope(g)} title={`${grantScope(g)} · ${grantDetail(g)}`} disabled={busy !== null} onRevoke={() => { void revoke(g.id) }} />)}
        </ul>}
        {dormant.length + dormantBoards.length > 0 && <div className="perm-dormant-list" data-testid="perm-inactive">
          <p className="perm-dormant-head">Not approving now</p>
          <ul className="perm-chips">
            {dormantBoards.map(b => <Chip key={`b:${b.board_id}`} dormant glyph="▦" text={boardChip(b)} scope={boardScope(b)} why="Needs you signed in"
              title={`${boardScope(b)}. ${SIGNED_OUT}`} disabled={busy !== null} onRevoke={() => { void revokeBoard(b) }} />)}
            {dormant.map(g => <Chip key={g.id} dormant glyph="⚙" text={grantTarget(g)} scope={grantScope(g)}
              why={g.inactive === 'tool_changed' ? 'tool changed' : 'Needs you signed in'} title={`${grantScope(g)}. ${inactiveLabel(g)}`} disabled={busy !== null} onRevoke={() => { void revoke(g.id) }} />)}
          </ul>
        </div>}
        {busy && <p role="status" className="child-approval-note">Saving…</p>}
        {status && !busy && <p role="status" className="child-approval-note perm-saved">{status}</p>}
        {(error || state === 'failed') && <p role="alert" className="child-approval-note perm-error">{error || 'Could not load the session permissions.'} <button type="button" className="perm-revoke" onClick={() => { setError(''); void load() }}>Refresh</button></p>}
      </div>
      {modal && <SessionPermissionsModal sessionId={sessionId} view={view ?? UNREADABLE} groups={groups} refreshKey={refreshKey} initialFocus={modal}
        onClose={() => setModal(null)} reload={load} onSaved={summary => setStatus(`Saved: ${summary.toLowerCase()}.`)} />}
    </section>
  )
}

function Chip({ glyph, text, scope, title, why, dormant, disabled, onRevoke, testId }: { glyph: string; text: string; scope: string; title: string; why?: string; dormant?: boolean; disabled: boolean; onRevoke: () => void; testId?: string }) {
  return <li className={`perm-chip${dormant ? ' perm-chip-dormant' : ''}`} title={title} data-testid={testId}>
    <span className="perm-chip-glyph" aria-hidden="true">{glyph}</span>
    <span className="perm-chip-text">{text}{why && <span className="perm-why"> · {why}</span>}</span>
    <button type="button" className="perm-chip-x" disabled={disabled} onClick={onRevoke} aria-label={`Revoke: ${scope}`}>×</button>
  </li>
}

/** The modal over a running session: reads children (and boards) only while open; Save applies
 *  the changes through the grant APIs, in order, and reports what did not apply. */
function SessionPermissionsModal({ sessionId, view, groups, refreshKey, initialFocus, onClose, reload, onSaved }: {
  sessionId: string; view: PermissionsView; groups: ChildGroup[]; refreshKey: string; initialFocus: 'search' | 'children'
  onClose: () => void; reload: () => Promise<void>; onSaved: (summary: string) => void
}) {
  const approval = useChildApproval(sessionId, true, refreshKey)
  const boardsRead = useSessionBoards(view.can_grant)
  const allGrants = view.grants.filter(g => g.kind === 'mcp_tool')
  const grants = allGrants.filter(g => g.active !== false)
  const dormant = allGrants.filter(g => g.active === false)
  const levels = view.boards

  const children: Loadable<{ options: ReturnType<typeof sessionChildOption>[]; note: string }> = approval.loading ? { state: 'loading' }
    : approval.value ? { state: 'ready', value: {
      options: approval.value.runtimes.map(sessionChildOption),
      note: CHILD_APPROVAL_NOTE,
    } }
      : approval.error ? { state: 'failed', message: approval.error, retry: approval.refresh } : { state: 'none' }
  // The person's boards, and any board with a level that their board discovery no longer lists.
  const boardRows = useMemo(() => {
    const listed = boardsRead.state === 'ready' ? boardsRead.boards : []
    const extra = levels.filter(b => !listed.some(x => x.id === b.board_id)).map(b => ({ id: b.board_id, name: boardName(b) }))
    return [...listed, ...extra]
  }, [boardsRead, levels])
  const nameOf = (b: BoardAccess) => b.board_name || boardRows.find(x => x.id === b.board_id)?.name || b.board_id
  const boardInfo = Object.fromEntries(levels.map(b => [b.board_id, { launch: b.launch, dormant: b.active === false ? SIGNED_OUT : undefined }]))
  const boards: Loadable<{ id: string; name: string }[]> = boardsRead.state === 'loading' ? { state: 'loading' }
    : boardsRead.state === 'failed' ? { state: 'failed', message: 'Boards are unavailable.', retry: boardsRead.retry } : { state: 'ready', value: boardRows }
  const tools = view.mcp_tools.map(t => ({ ...t, changed: dormant.some(g => g.kind === 'mcp_tool' && g.inactive === 'tool_changed' && g.connection_id === t.connection_id && g.tool === t.tool) }))
  // The modal keeps the person's changes as overrides on this, so a refresh under it keeps them.
  const initial = {
    // A started session's own level: Save changes it; the parent's level caps it.
    boards: Object.fromEntries(levels.map(b => [b.board_id, ownLevel(b)])),
    tools: grants.filter(g => g.kind === 'mcp_tool').map(g => toolKey(g.connection_id ?? '', g.tool ?? '')),
    revokeDelegations: [], revokeDormant: [],
  }
  const active: ActiveEntry[] = [
    ...groups.map(c => ({ key: c.key, kind: 'child' as const, label: c.label, detail: 'Allowed for this session', target: { delegation: c.key } })),
    ...levels.map(b => ({ key: `b:${b.board_id}`, kind: 'board' as const, label: boardChip({ ...b, board_name: nameOf(b) }), detail: boardDetail(b),
      dormant: b.active === false ? SIGNED_OUT : undefined, target: { board: b.board_id } })),
    ...grants.map(g => ({ key: g.id, kind: 'tool' as const, label: grantScope(g), detail: grantDetail(g),
      target: { tool: toolKey(g.connection_id ?? '', g.tool ?? '') } })),
    ...dormant.map(g => ({ key: g.id, kind: 'tool' as const, label: grantScope(g), detail: grantDetail(g),
      dormant: g.inactive === 'tool_changed' ? 'Tool changed: allow it again to approve the new version' : 'Needs you signed in: allow it again', target: { dormant: g.id } })),
  ]
  const dormantScopes = Object.fromEntries(dormant.map(g => [g.id, { tool: toolKey(g.connection_id ?? '', g.tool ?? '') }]))

  const caps = boardCaps(view.started_by)
  const boardCap = caps && view.started_by ? { by: view.started_by.title || 'the session that started it', levels: caps } : undefined

  async function save(_: unknown, c: PermissionChanges): Promise<SaveOutcome> {
    const errors: string[] = []
    // What did not apply, in the diff's own shape: the modal keeps exactly these as changes.
    const failed: PermissionChanges = { children: [], boardLevels: [], grantTools: [], revokeTools: [], revokeDelegations: [], revokeDormant: [] }
    const post = (url: string, body?: unknown) => apiFetch(url, body === undefined ? { method: 'POST' } : { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
    const run = async (r: () => Promise<Response>, fallback: string): Promise<boolean> => {
      try { const res = await r(); if (res.ok) return true; errors.push(await failure(res, fallback)) } catch { errors.push(fallback) }
      return false
    }
    // Children first, one runtime at a time, each against the revision the last one returned; after
    // a refusal the rest are not tried (the scope changed) and stay as changes.
    let revision = approval.value?.revision
    let stopped = false
    for (const ch of c.children) {
      if (stopped) { failed.children.push(ch); continue }
      const r = await approval.save(ch.runtime, ch.enabled, revision)
      if (r.ok) { revision = r.value.revision; continue }
      errors.push(r.conflict ? 'Child approval scope changed. Review the current engines and try again.' : 'Could not confirm child approval.')
      failed.children.push(ch); stopped = true
    }
    for (const key of c.revokeDelegations) {
      let ok = true
      for (const id of groups.find(g => g.key === key)?.ids ?? []) ok = await run(() => post(`/api/delegation-grants/${encodeURIComponent(id)}/revoke`), 'Could not revoke a child permission.') && ok
      if (!ok) failed.revokeDelegations.push(key)
    }
    const grantOf = (pred: (g: ProposalGrant) => boolean) => grants.find(pred)?.id
    const revokeGrant = (id: string | undefined) => id ? run(() => post(`/api/proposal-grants/${encodeURIComponent(id)}/revoke`), 'Could not revoke a permission.') : Promise.resolve(true)
    for (const id of c.revokeDormant) if (!await revokeGrant(id)) failed.revokeDormant.push(id)
    const put = (url: string, body: unknown) => apiFetch(url, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
    for (const b of c.boardLevels) {
      if (!await run(() => put(`/api/sessions/${encodeURIComponent(sessionId)}/board-access`, { board_id: b.board, level: b.level }), 'Could not set a board level.')) failed.boardLevels.push(b)
    }
    for (const t of c.revokeTools) if (!await revokeGrant(grantOf(g => g.kind === 'mcp_tool' && g.connection_id === t.connection_id && g.tool === t.tool))) failed.revokeTools.push(t)
    const grantUrl = `/api/sessions/${encodeURIComponent(sessionId)}/proposal-grants`
    for (const t of c.grantTools) if (!await run(() => post(grantUrl, { kind: 'mcp_tool', connection_id: t.connection_id, tool: t.tool }), 'Could not allow a tool.')) failed.grantTools.push(t)
    if (c.children.length || c.revokeDelegations.length) refreshChildren(sessionId)
    await reload()
    if (errors.length) return { message: `Not everything was saved: ${errors[0]}${errors.length > 1 ? ` (and ${errors.length - 1} more)` : ''}`, failed }
    onSaved(changeSummary(c))
  }

  return <PermissionsModal mode="session" onClose={onClose} initialFocus={initialFocus} childSessions={children} boards={boards} tools={tools}
    canGrant={view.can_grant} grantReason={view.reason} initial={initial} active={active} dormantScopes={dormantScopes} boardInfo={boardInfo} boardCap={boardCap} onSave={save} />
}
