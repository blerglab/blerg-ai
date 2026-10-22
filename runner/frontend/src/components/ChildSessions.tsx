import { useContext, type ReactNode } from 'react'
import {ChildDiscardAction} from './ChildDiscard'
import { useNavigate } from 'react-router-dom'
import { modelDisplayName, tokenLabel } from '@blerglab/chat'
import { OpenSessionContext } from '../lib/openSession'
import { ENGINE_LABELS, ENGINE_SHORT } from '../lib/runtimes'
import { childIsRunning, childIsWorking, type LiveChildSession } from '../lib/liveState'
import { formatDuration, formatUSD } from '../lib/insights'
import RunCard, { RunGlyph, RunStep } from './RunCard'
import { runTarget } from '../lib/runFacts'

export function EngineBadge({ engine }: { engine: string }) {
  const id = engine || 'claude'
  const name = ENGINE_LABELS[id] ?? id
  return <span className="rail-chip" title={name} aria-label={name}>{ENGINE_SHORT[id] ?? id}</span>
}

export function ChildSessionRow({ child, now, compact = false, parentId, assignment, work, card, variant }: { child: LiveChildSession; now: number; compact?: boolean; parentId?: string; assignment?: ReactNode; work?: ReactNode; card?: string; variant?: 'run' }) {
  const open = useContext(OpenSessionContext)
  const navigate = useNavigate()
  if (variant === 'run') return <RunningChildRow child={child} now={now} parentId={parentId} assignment={assignment} work={work} card={card} onOpen={() => open ? open(child.id) : navigate(`/sessions/${encodeURIComponent(child.id)}`)} />
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
    {parentId && <ChildDiscardAction parentId={parentId} childId={child.id} title={child.title || 'Untitled session'} running={childIsWorking(child)}/>}
  </li>
}

/** A started session at work, as a Running now card: two lines (RunCard). Its status, effort, tool
 *  calls, cost and endpoint are in the title's tooltip; a click on the card opens the session. */
function RunningChildRow({ child, now, parentId, assignment, work, card, onOpen }: { child: LiveChildSession; now: number; parentId?: string; assignment?: ReactNode; work?: ReactNode; card?: string; onOpen: () => void }) {
  const elapsed = Date.parse(child.started_at)
  const active = !child.ended_at && child.status === 'running'
  const activitySince = child.activity_since ? Date.parse(child.activity_since) : NaN
  const activityElapsed = active && Number.isFinite(activitySince) && activitySince <= now ? (now - activitySince) / 1000 : null
  const engine = ENGINE_LABELS[child.engine || 'claude'] ?? child.engine
  const model = child.model ? modelDisplayName(child.model) : ''
  const calls = Number.isSafeInteger(child.tool_calls) && child.tool_calls! >= 0 ? `${child.tool_calls} tool ${child.tool_calls === 1 ? 'call' : 'calls'}` : ''
  const tooltip = [child.title || 'Untitled session', [child.status, engine, model, child.effort ? `effort: ${child.effort}` : '', calls,
    child.usage_status === 'pending' ? 'usage pending' : '', child.cost_usd !== undefined ? formatUSD(child.cost_usd) : '', child.local ? 'local endpoint' : ''].filter(Boolean).join(' · ')].join('\n')
  const tokens = child.tokens !== null ? tokenLabel(child.tokens) : ''
  // The row's hovercard (card #129); a click on the row still opens the session.
  const hover = runTarget({
    title: child.title || 'Untitled session', kind: 'Child session', engine: child.engine ?? '', model: child.model ?? '',
    state: [child.status, child.asking ? 'asking' : '', child.effort ? `effort: ${child.effort}` : '', calls].filter(Boolean).join(' · '),
    step: child.activity, elapsed: Number.isFinite(elapsed) ? formatDuration(Math.max(0, now - elapsed) / 1000) : undefined,
    tokens: [tokens, child.cost_usd !== undefined ? formatUSD(child.cost_usd) : ''].filter(Boolean).join(' · ') || undefined, card, startedBy: 'this session',
  })
  return (
    <RunCard testId="rail-child" className={`rail-child-item rail-child-run${parentId ? ' has-discard' : ''}`} actionTestId="rail-child-open"
      state={active ? <span className="rail-spin" role="img" aria-label="Running" /> : <span role="img" aria-label={child.status}>↳</span>}
      title={child.title || 'Untitled session'} tooltip={tooltip} action="Open the session" onActivate={onOpen}
      elapsed={Number.isFinite(elapsed) ? <span className="rail-clock" title="Session age">{formatDuration(Math.max(0, now - elapsed) / 1000)}</span> : undefined}
      step={child.activity ? <><RunStep text={child.activity} testId="child-activity" />{activityElapsed !== null && <span className="rail-clock" title="Current activity elapsed">{formatDuration(activityElapsed)}</span>}</> : undefined}
      glyphs={<>
        {child.asking && <RunGlyph label="Asking" className="attention">?</RunGlyph>}
        <RunGlyph label={`Engine: ${engine}`}>{ENGINE_SHORT[child.engine || 'claude'] ?? engine.charAt(0)}</RunGlyph>
        {model && <RunGlyph label={`Model: ${model}`}>{model.charAt(0).toUpperCase()}</RunGlyph>}
        {tokens && <RunGlyph label={tokens} className="tokens">{tokens.replace(/ tokens?$/, '')}</RunGlyph>}
        {assignment}
      </>}
      work={work} hover={hover}
      after={parentId ? <ChildDiscardAction parentId={parentId} childId={child.id} title={child.title || 'Untitled session'} running={childIsWorking(child)}/> : undefined} />
  )
}

/** The session's started sessions. One that is at work is drawn once, in Running now (onShowRunning
 *  brings it into view): here it is a single "N running ↑" line. One that stopped (idle, done,
 *  failed) is listed here, to open, reuse or discard. Without onShowRunning (an archived session,
 *  which has no Running now) every child is listed. */
export function StartedSessions({ sessionId, children, now, assignmentFor, readOnly = false, onShowRunning }: { sessionId: string; children: LiveChildSession[]; now: number; readOnly?: boolean; assignmentFor?: (id: string) => ReactNode; onShowRunning?: () => void }) {
  const visible = children.filter(child => !child.archived_at).sort((a,b) => a.started_at.localeCompare(b.started_at) || a.id.localeCompare(b.id))
  const working = onShowRunning ? visible.filter(childIsWorking).length : 0
  const listed = onShowRunning ? visible.filter(child => !childIsWorking(child)) : visible
  return <>
    {visible.length > 0 && <section className="rail-section" data-testid="rail-started" aria-label="Started sessions">
      <h3 className="rail-section-head">Started sessions <span className="rail-count">{visible.length}</span></h3>
      {working > 0 && <button type="button" className="rail-link rail-started-running" data-testid="rail-started-running" onClick={onShowRunning} title="It is in Running now">{working} running ↑</button>}
      {listed.length > 0 && <ul className="rail-list">{listed.map(child => <ChildSessionRow key={child.id} child={child} now={now} parentId={readOnly ? undefined : sessionId} assignment={assignmentFor?.(child.id)}/>)}</ul>}
    </section>}
  </>
}
