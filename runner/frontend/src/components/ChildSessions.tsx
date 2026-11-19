import { longWhen } from '../lib/friendlyTime'
import RuntimeIcon from './RuntimeIcon'
import { sessionNoun } from '../lib/sessionPresentation'
import { useSessionStore } from '../hooks/useSessionStore'
import { memo, useContext, type ReactNode } from 'react'
import {ChildDiscardAction} from './ChildDiscard'
import { useNavigate } from 'react-router-dom'
import { modelDisplayName, reasoningLabel, tokenLabel, useTicker } from '@blerglab/chat'
import { OpenSessionContext } from '../lib/openSession'
import { ENGINE_LABELS, ENGINE_SHORT } from '../lib/runtimes'
import { childIsRunning, childIsWorking, type LiveChildSession } from '../lib/liveState'
import { formatDuration, formatUSD } from '../lib/insights'
import RunCard, { RunGlyph, RunStep } from './RunCard'
import { runTarget } from '../lib/runFacts'
import { compactTokens, contextMeter, readUsage, usageChip, withContextNote } from '../lib/liveUsage'
import { ContextGlyph } from './ContextMeter'
import { SessionStatusGlyph } from './SessionCard'

// A subagent-style "Thinking..." activity label advances by the second; everything else about a
// child row is driven by server pushes, not a clock. Moved from SessionRail.tsx (BLG-431) so the
// label can read a locally-ticked `now` instead of one computed at SessionRail's top.
function reasoningChild(child: LiveChildSession, now: number): LiveChildSession {
  if (child.activity !== 'Thinking…' || !child.activity_since) return child
  const since = Date.parse(child.activity_since)
  if (!Number.isFinite(since)) return child
  return { ...child, activity: reasoningLabel({ since, tokens: null }, now), activity_since: undefined }
}

function childDuration(child: LiveChildSession, now: number, elapsed: number) {
  const idle = !child.ended_at && child.status === 'idle'
  const turnEnd = child.last_turn_ended_at ? Date.parse(child.last_turn_ended_at) : NaN
  const idleSeconds = idle && Number.isFinite(turnEnd) && turnEnd <= now ? (now - turnEnd) / 1000 : null
  const idleText = idleSeconds === null ? '' : `idle ${Math.floor(idleSeconds / 3600) ? `${Math.floor(idleSeconds / 3600)}h ` : ''}${Math.floor(idleSeconds / 60) % 60}m`
  const duration = idle ? (idleText ? <span className="rail-clock" title={`Idle since ${child.last_turn_ended_at}`}>{idleText}</span> : undefined) : Number.isFinite(elapsed) ? <span className="rail-clock" title="Workspace age">{formatDuration(Math.max(0, elapsed) / 1000)}</span> : undefined
  return { duration, idleText }
}

function ChildRuntime({ id, runtime }: { id: string; runtime?: string }) {
  const session = useSessionStore(s => s.sessions.find(session => session.id === id))
  const machine = useSessionStore(s => s.daemons.find(d => d.id === session?.daemon_id)?.name)
  return <RuntimeIcon runtime={runtime ?? session?.runtime} machine={machine} />
}

export function EngineBadge({ engine }: { engine: string }) {
  const id = engine || 'claude'
  const name = ENGINE_LABELS[id] ?? id
  return <span className="rail-chip" title={name} aria-label={name}>{ENGINE_SHORT[id] ?? id}</span>
}

export const ChildSessionRow = memo(function ChildSessionRow({ child, clockOffset, ticking, compact = false, parentId, assignment, work, card, variant }: { child: LiveChildSession; clockOffset: number; ticking: boolean; compact?: boolean; parentId?: string; assignment?: ReactNode; work?: ReactNode; card?: string; variant?: 'run' }) {
  const noun = useSessionStore(s => { const session = s.sessions.find(item => item.id === child.id); return session ? sessionNoun(session) : 'worker' })
  const open = useContext(OpenSessionContext)
  const navigate = useNavigate()
  const now = useTicker(ticking) - clockOffset
  if (variant === 'run') return <RunningChildRow noun={noun} child={reasoningChild(child, now)} now={now} parentId={parentId} assignment={assignment} work={work} card={card} onOpen={() => open ? open(child.id) : navigate(`/sessions/${encodeURIComponent(child.id)}`)} />
  const finish = child.ended_at ? Date.parse(child.ended_at) : now
  const elapsed = finish - Date.parse(child.started_at)
  const { duration } = childDuration(child, now, elapsed)
  const active = !child.ended_at && child.status === 'running'
  const activitySince = child.activity_since ? Date.parse(child.activity_since) : NaN
  const activityElapsed = active && Number.isFinite(activitySince) && activitySince <= now ? (now - activitySince) / 1000 : null
  const reportedCalls = Number.isSafeInteger(child.tool_calls) && child.tool_calls! >= 0 ? child.tool_calls : null
  return <li className={`rail-item rail-child-item${parentId ? ' has-discard' : ''}`} data-testid="rail-child">
    <button type="button" className="rail-child-button" onClick={() => open ? open(child.id) : navigate(`/sessions/${encodeURIComponent(child.id)}`)}>
      <span className="rail-item-title"><span title={child.status} aria-label={child.status}>{childIsRunning(child) ? '↳' : child.status === 'error' ? '×' : child.status === 'ended' ? '✓' : child.status === 'disconnected' ? 'Ⅱ' : '■'}</span> <ChildRuntime id={child.id} runtime={child.runtime} /> <EngineBadge engine={child.engine} /> {child.title || 'Untitled worker'}</span>
      <span className="rail-item-line"><span className="rail-chip">{child.status}</span> {!compact && <>{child.model && <span className="rail-chip" title={child.model}>{modelDisplayName(child.model)}</span>} {child.effort && <span className="rail-chip rail-effort" title="Configured effort">{child.effort}</span>} {duration} </>}
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
    {parentId && <ChildDiscardAction noun={noun} parentId={parentId} childId={child.id} title={child.title || 'Untitled worker'} running={childIsWorking(child)}/>}
  </li>
})

/** A started session at work, as a Running now card: two lines (RunCard). Its status, effort, tool
 *  calls, cost and endpoint are in the title's tooltip; a click on the card opens the session. */
function RunningChildRow({ noun, child, now, parentId, assignment, work, card, onOpen }: { noun: ReturnType<typeof sessionNoun>; child: LiveChildSession; now: number; parentId?: string; assignment?: ReactNode; work?: ReactNode; card?: string; onOpen: () => void }) {
  const session = useSessionStore(s => s.sessions.find(item => item.id === child.id))
  const elapsed = (child.ended_at ? Date.parse(child.ended_at) : now) - Date.parse(child.started_at)
  const { duration, idleText } = childDuration(child, now, elapsed)
  const active = !child.ended_at && child.status === 'running'
  const activitySince = child.activity_since ? Date.parse(child.activity_since) : NaN
  const activityElapsed = active && Number.isFinite(activitySince) && activitySince <= now ? (now - activitySince) / 1000 : null
  const engine = ENGINE_LABELS[child.engine || 'claude'] ?? child.engine
  const model = child.model ? modelDisplayName(child.model) : ''
  const calls = Number.isSafeInteger(child.tool_calls) && child.tool_calls! >= 0 ? `${child.tool_calls} tool ${child.tool_calls === 1 ? 'call' : 'calls'}` : ''
  const tooltip = [child.title || 'Untitled worker', [child.status, engine, model, child.effort ? `effort: ${child.effort}` : '', calls,
    child.usage_status === 'pending' ? 'usage pending' : '', child.cost_usd !== undefined ? formatUSD(child.cost_usd) : '', child.local ? 'local endpoint' : ''].filter(Boolean).join(' · ')].join('\n')
  const tokens = child.tokens !== null ? tokenLabel(child.tokens) : ''
  // The token chip, as on the Main row: for whoever may see tokens (the server says "hidden"
  // otherwise). An older server sends the total without its breakdown.
  const usage = readUsage(child.usage)
  const seesTokens = child.usage_status === 'pending' || child.usage_status === 'reported' || child.tokens !== null
  const meter = usage ? contextMeter(usage.context, engine) : null
  const chip = !seesTokens ? null : usage || child.tokens === null ? withContextNote(usageChip(usage), usage, meter, engine) : { text: compactTokens(child.tokens), label: tokens }
  // The row's hovercard (card #129); a click on the row still opens the session.
  const hover = runTarget({
    title: child.title || 'Untitled worker', kind: 'Worker', engine: child.engine ?? '', model: child.model ?? '',
    state: [child.status, child.asking ? 'asking' : '', child.effort ? `effort: ${child.effort}` : '', calls].filter(Boolean).join(' · '),
    step: child.activity,
    tokens: [tokens, child.cost_usd !== undefined ? formatUSD(child.cost_usd) : '', meter ? meter.label.replace(/\n/g, ' · ') : ''].filter(Boolean).join(' · ') || undefined, card, startedBy: 'this workspace',
  })
  hover.details = [
    ...(Number.isFinite(elapsed) ? [{label: 'Started', value: `${longWhen(child.started_at, now)} · ${formatDuration(Math.max(0, elapsed) / 1000)} elapsed`}] : []),
    ...(idleText ? [{label: 'Idle since', value: `${longWhen(child.last_turn_ended_at!, now)} · ${idleText}`}] : []),
  ]
  return (
    <RunCard testId="rail-child" className={`rail-child-item rail-child-run${parentId ? ' has-discard' : ''}`} actionTestId="rail-child-open"
      state={session ? <SessionStatusGlyph session={session} /> : active ? <span className="rail-spin" role="img" aria-label="Running" /> : <span role="img" aria-label={child.status}>↳</span>}
      title={child.title || 'Untitled worker'} tooltip={tooltip} action="Open the worker" onActivate={onOpen}
      elapsed={duration}
      step={child.activity ? <><RunStep text={child.activity} testId="child-activity" />{activityElapsed !== null && <span className="rail-clock" title="Current activity elapsed">{formatDuration(activityElapsed)}</span>}</> : undefined}
      glyphsBelow glyphs={<>
        <ChildRuntime id={child.id} runtime={child.runtime} />
        {child.asking && <RunGlyph label="Asking" className="attention">?</RunGlyph>}
        <RunGlyph label={`Engine: ${engine}`}>{ENGINE_SHORT[child.engine || 'claude'] ?? engine.charAt(0)}</RunGlyph>
        {model && <RunGlyph label={`Model: ${model}`}>{model.charAt(0).toUpperCase()}</RunGlyph>}
        {meter && <ContextGlyph meter={meter} testId="rail-child-context" />}
        {chip && <RunGlyph label={chip.label} testId="rail-child-tokens" className="tokens">{chip.text}</RunGlyph>}
        {assignment}
      </>}
      work={work} hover={hover}
      after={parentId ? <ChildDiscardAction noun={noun} parentId={parentId} childId={child.id} title={child.title || 'Untitled worker'} running={childIsWorking(child)}/> : undefined} />
  )
}

/** The session's started sessions. One that is at work is drawn once, in Running now (onShowRunning
 *  brings it into view): here it is a single "N running ↑" line. One that stopped (idle, done,
 *  failed) is listed here, to open, reuse or discard. Without onShowRunning (an archived session,
 *  which has no Running now) every child is listed. */
export function StartedSessions({ sessionId, children, clockOffset, active, assignmentFor, readOnly = false, onShowRunning }: { sessionId: string; children: LiveChildSession[]; clockOffset: number; active: boolean; readOnly?: boolean; assignmentFor?: (id: string) => ReactNode; onShowRunning?: () => void }) {
  const visible = children.filter(child => !child.archived_at).sort((a,b) => a.started_at.localeCompare(b.started_at) || a.id.localeCompare(b.id))
  const working = onShowRunning ? visible.filter(childIsWorking).length : 0
  const listed = onShowRunning ? visible.filter(child => !childIsWorking(child)) : visible
  return <>
    {visible.length > 0 && <section className="rail-section" data-testid="rail-started" aria-label="Workers">
      <h3 className="rail-section-head">Workers <span className="rail-count">{visible.length}</span></h3>
      {working > 0 && <button type="button" className="rail-link rail-started-running" data-testid="rail-started-running" onClick={onShowRunning} title="It is in Running now">{working} running ↑</button>}
      {listed.length > 0 && <ul className="rail-list">{listed.map(child => <ChildSessionRow key={child.id} child={child} clockOffset={clockOffset} ticking={active} variant="run" parentId={readOnly ? undefined : sessionId} assignment={assignmentFor?.(child.id)}/>)}</ul>}
    </section>}
  </>
}
