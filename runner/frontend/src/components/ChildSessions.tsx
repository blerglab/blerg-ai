import { useContext, useState } from 'react'
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

export function ChildSessionRow({ child, now, compact = false }: { child: LiveChildSession; now: number; compact?: boolean }) {
  const open = useContext(OpenSessionContext)
  const navigate = useNavigate()
  const elapsed = now - Date.parse(child.started_at)
  return <li className="rail-item" data-testid="rail-child">
    <button type="button" className="rail-child-button" onClick={() => open ? open(child.id) : navigate(`/sessions/${encodeURIComponent(child.id)}`)}>
      <span className="rail-item-title"><span title={child.status} aria-label={child.status}>{childIsRunning(child) ? '↳' : child.status === 'error' ? '×' : child.status === 'ended' ? '✓' : child.status === 'disconnected' ? 'Ⅱ' : '■'}</span> <EngineBadge engine={child.engine} /> {child.title || 'Untitled session'}</span>
      <span className="rail-item-line">{!compact && <>{child.model && <span className="rail-chip">{child.model}</span>} {child.effort && <span className="rail-chip">{child.effort}</span>} {Number.isFinite(elapsed) && <span className="rail-clock">{formatDuration(Math.max(0, elapsed) / 1000)}</span>} </>}
        {child.tokens !== null && <span className="rail-chip">{tokenLabel(child.tokens)} tokens</span>}
        {child.cost_usd !== undefined && <span className="rail-chip">{formatUSD(child.cost_usd)}</span>}
        {child.asking && <span className="rail-chip attention">asking</span>}
      </span>
    </button>
  </li>
}

export function StartedSessions({ sessionId, children, grants, now }: { sessionId: string; children: LiveChildSession[]; grants: LiveDelegationGrant[]; now: number }) {
  const [all, setAll] = useState(false)
  const [busy, setBusy] = useState<string | null>(null)
  const [error, setError] = useState('')
  const ended = children.filter(c => !childIsRunning(c))
  if (!ended.length && !grants.length) return null
  const revoke = async (id: string) => {
    setBusy(id); setError('')
    try {
      const r = await apiFetch(`/api/delegation-grants/${encodeURIComponent(id)}/revoke`, { method: 'POST' })
      if (!r.ok) throw new Error()
      refreshChildren(sessionId)
    } catch { setError('Could not revoke permission. Try again.') }
    finally { setBusy(null) }
  }
  return <section className="rail-section" data-testid="rail-started" aria-label="Started sessions">
    <h3 className="rail-section-head">Started sessions <span className="rail-count">{ended.length}</span></h3>
    {grants.map(g => <button key={g.id} type="button" className="rail-link" disabled={busy !== null} title={`Permission for ${g.no_repo ? 'no repository' : g.repo || 'the approved repository'}`} onClick={() => { void revoke(g.id) }} aria-label={`Revoke ${ENGINE_LABELS[g.engine] ?? g.engine} ${RUNTIME_LABELS[g.runtime] ?? g.runtime}`}>Revoke{grants.length > 1 ? ` ${ENGINE_LABELS[g.engine] ?? g.engine} · ${RUNTIME_LABELS[g.runtime] ?? g.runtime}` : ''}</button>)}
    {error && <p role="alert">{error}</p>}
    <ul className="rail-list">{(all ? ended : ended.slice(0, 5)).map(c => <ChildSessionRow key={c.id} child={c} now={now} compact />)}</ul>
    {!all && ended.length > 5 && <button type="button" className="rail-link" onClick={() => setAll(true)}>Show all {ended.length}</button>}
  </section>
}
