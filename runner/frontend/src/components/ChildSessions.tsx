import { useContext, useState } from 'react'
import {ChildDiscardAction} from './ChildDiscard'
import { useNavigate } from 'react-router-dom'
import { tokenLabel } from '@blerglab/chat'
import { apiFetch } from '../apiFetch'
import { OpenSessionContext } from '../lib/openSession'
import { ENGINE_LABELS, ENGINE_SHORT, RUNTIME_LABELS } from '../lib/runtimes'
import { childIsRunning, type LiveChildSession, type LiveDelegationGrant } from '../lib/liveState'
import { formatDuration, formatUSD } from '../lib/insights'
import { refreshChildren } from '../hooks/useChildrenSignal'

export function EngineBadge({ engine }: { engine: string }) {
  const id = engine || 'claude'
  const name = ENGINE_LABELS[id] ?? id
  return <span className="rail-chip" title={name} aria-label={name}>{ENGINE_SHORT[id] ?? id}</span>
}

export function ChildSessionRow({ child, now, compact = false, parentId }: { child: LiveChildSession; now: number; compact?: boolean; parentId?: string }) {
  const open = useContext(OpenSessionContext)
  const navigate = useNavigate()
  const finish = child.ended_at ? Date.parse(child.ended_at) : now
  const elapsed = finish - Date.parse(child.started_at)
  return <li className={`rail-item rail-child-item${parentId ? ' has-discard' : ''}`} data-testid="rail-child">
    <button type="button" className="rail-child-button" onClick={() => open ? open(child.id) : navigate(`/sessions/${encodeURIComponent(child.id)}`)}>
      <span className="rail-item-title"><span title={child.status} aria-label={child.status}>{childIsRunning(child) ? '↳' : child.status === 'error' ? '×' : child.status === 'ended' ? '✓' : child.status === 'disconnected' ? 'Ⅱ' : '■'}</span> <EngineBadge engine={child.engine} /> {child.title || 'Untitled session'}</span>
      <span className="rail-item-line"><span className="rail-chip">{child.status}</span> {!compact && <>{child.model && <span className="rail-chip">{child.model}</span>} {child.effort && <span className="rail-chip">{child.effort}</span>} {Number.isFinite(elapsed) && <span className="rail-clock">{formatDuration(Math.max(0, elapsed) / 1000)}</span>} </>}
        {child.tokens !== null && <span className="rail-chip">{tokenLabel(child.tokens)}</span>}
        {child.cost_usd !== undefined && <span className="rail-chip">{formatUSD(child.cost_usd)}</span>}
        {child.asking && <span className="rail-chip attention">asking</span>}
      </span>
    </button>
    {parentId && <ChildDiscardAction parentId={parentId} childId={child.id} title={child.title || 'Untitled session'}/>}
  </li>
}

export function StartedSessions({ sessionId, children, grants, now }: { sessionId: string; children: LiveChildSession[]; grants: LiveDelegationGrant[]; now: number }) {
  const [busy, setBusy] = useState<string | null>(null)
  const [error, setError] = useState('')
  const visible = children.filter(child => !child.archived_at).sort((a,b) => a.started_at.localeCompare(b.started_at) || a.id.localeCompare(b.id))
  if (!visible.length && !grants.length) return null
  const groups = new Map<string, {grant: LiveDelegationGrant; ids: string[]}>()
  for (const grant of grants) {
    const key = JSON.stringify([grant.engine, grant.runtime, grant.daemon_id ?? ''])
    const group = groups.get(key)
    if (group) group.ids.push(grant.id)
    else groups.set(key, {grant, ids: [grant.id]})
  }
  const multipleDaemons = new Set(grants.map(g => g.daemon_id ?? '')).size > 1
  const revoke = async (ids: string[]) => {
    setBusy(ids[0]); setError('')
    try {
      const results = await Promise.allSettled(ids.map(async id => {
        const r = await apiFetch(`/api/delegation-grants/${encodeURIComponent(id)}/revoke`, { method: 'POST' })
        if (!r.ok) throw new Error()
      }))
      if (results.some(result => result.status === 'rejected')) throw new Error()
    } catch { setError('Could not revoke permission. Try again.') }
    finally { refreshChildren(sessionId); setBusy(null) }
  }
  return <section className="rail-section" data-testid="rail-started" aria-label="Started sessions">
    <h3 className="rail-section-head">Started sessions <span className="rail-count">{visible.length}</span></h3>
    {[...groups].map(([key, {grant: g, ids}]) => {
      const label = `Revoke ${ENGINE_LABELS[g.engine] ?? g.engine} · ${RUNTIME_LABELS[g.runtime] ?? g.runtime}${multipleDaemons ? ` · ${g.daemon_name || g.daemon_id || 'Unknown desktop'}` : ''}`
      return <button key={key} type="button" className="rail-link" disabled={busy !== null} title="Permission for any repository or no repository" onClick={() => { void revoke(ids) }} aria-label={label}>{label}</button>
    })}
    {error && <p role="alert">{error}</p>}
    <ul className="rail-list">{visible.map(child => <ChildSessionRow key={child.id} child={child} now={now} parentId={sessionId}/>)}</ul>
  </section>
}
