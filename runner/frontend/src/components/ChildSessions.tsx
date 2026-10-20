import { useContext, type ReactNode } from 'react'
import {ChildDiscardAction} from './ChildDiscard'
import { useNavigate } from 'react-router-dom'
import { modelDisplayName, tokenLabel } from '@blerglab/chat'
import { OpenSessionContext } from '../lib/openSession'
import { ENGINE_LABELS, ENGINE_SHORT } from '../lib/runtimes'
import { childIsRunning, type LiveChildSession } from '../lib/liveState'
import { formatDuration, formatUSD } from '../lib/insights'

export function EngineBadge({ engine }: { engine: string }) {
  const id = engine || 'claude'
  const name = ENGINE_LABELS[id] ?? id
  return <span className="rail-chip" title={name} aria-label={name}>{ENGINE_SHORT[id] ?? id}</span>
}

export function ChildSessionRow({ child, now, compact = false, parentId, assignment }: { child: LiveChildSession; now: number; compact?: boolean; parentId?: string; assignment?: ReactNode }) {
  const open = useContext(OpenSessionContext)
  const navigate = useNavigate()
  const finish = child.ended_at ? Date.parse(child.ended_at) : now
  const elapsed = finish - Date.parse(child.started_at)
  const active = !child.ended_at && child.status === 'running'
  const activitySince = child.activity_since ? Date.parse(child.activity_since) : NaN
  const activityElapsed = active && Number.isFinite(activitySince) && activitySince <= now ? (now - activitySince) / 1000 : null
  const reportedCalls = Number.isSafeInteger(child.tool_calls) && child.tool_calls! >= 0 ? child.tool_calls : null
  return <li className={`rail-item rail-child-item${parentId ? ' has-discard' : ''}`} data-testid="rail-child">
    <button type="button" className="rail-child-button" onClick={() => open ? open(child.id) : navigate(`/sessions/${encodeURIComponent(child.id)}`)}>
      <span className="rail-item-title"><span title={child.status} aria-label={child.status}>{childIsRunning(child) ? '↳' : child.status === 'error' ? '×' : child.status === 'ended' ? '✓' : child.status === 'disconnected' ? 'Ⅱ' : '■'}</span> <EngineBadge engine={child.engine} /> {child.title || 'Untitled session'}</span>
      <span className="rail-item-line"><span className="rail-chip">{child.status}</span> {!compact && <>{child.model && <span className="rail-chip" title={child.model}>{modelDisplayName(child.model)}</span>} {child.effort && <span className="rail-chip rail-effort" title="Configured effort">{child.effort}</span>} {Number.isFinite(elapsed) && <span className="rail-clock" title="Session age">{formatDuration(Math.max(0, elapsed) / 1000)}</span>} </>}
        {child.tokens !== null && <span className="rail-chip" title="Reported tokens across model requests; not context size">{tokenLabel(child.tokens)}</span>}
        {child.usage_status === 'pending' && <span className="rail-chip" title="The current turn has not reported usage yet">Usage pending</span>}
        {reportedCalls !== null && <span className="rail-chip">{reportedCalls} tool {reportedCalls === 1 ? 'call' : 'calls'}</span>}
        {child.cost_usd !== undefined && <span className="rail-chip" title="Estimated from configured model prices">{formatUSD(child.cost_usd)}</span>}
        {child.local && <span className="rail-chip" title="The desktop reports this engine's endpoint as local; no monetary cost is inferred">Local endpoint</span>}
        {child.asking && <span className="rail-chip attention">asking</span>}
      </span>
      {child.activity && <span className={`rail-child-activity${active ? ' active' : ''}`} data-testid="child-activity" title={child.activity}>
        {active && <span className="rail-child-pulse" aria-hidden="true"/>}<span>{child.activity}</span>
        {activityElapsed !== null && <span className="rail-clock" title="Current activity elapsed">{formatDuration(activityElapsed)}</span>}
      </span>}
    </button>
    {assignment}
    {parentId && <ChildDiscardAction parentId={parentId} childId={child.id} title={child.title || 'Untitled session'}/>}
  </li>
}

export function StartedSessions({ sessionId, children, now, assignmentFor, readOnly = false }: { sessionId: string; children: LiveChildSession[]; now: number; readOnly?: boolean; assignmentFor?: (id: string) => ReactNode }) {
  const visible = children.filter(child => !child.archived_at).sort((a,b) => a.started_at.localeCompare(b.started_at) || a.id.localeCompare(b.id))
  return <>
    {visible.length > 0 && <section className="rail-section" data-testid="rail-started" aria-label="Started sessions">
      <h3 className="rail-section-head">Started sessions <span className="rail-count">{visible.length}</span></h3>
      <ul className="rail-list">{visible.map(child => <ChildSessionRow key={child.id} child={child} now={now} parentId={readOnly ? undefined : sessionId} assignment={assignmentFor?.(child.id)}/>)}</ul>
    </section>}
  </>
}
