// Tool changes: an MCP server changed a tool a session pinned, offers one the launch never saw,
// or stopped offering one (docs/design/mcp-tool-changes.md §6.1). The gateway turned the tool off
// and recorded what it saw; here the person reads the OLD and NEW definitions (the exact text that
// was hashed, hidden characters made visible) and accepts, which re-pins the session in place, or
// keeps the tool off. Rendered by the Proposals page under the proposals.
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { CSSProperties, ReactNode } from 'react'
import { Link } from 'react-router-dom'
import { apiFetch } from '../apiFetch'
import ToolSchemaGuidance from './ToolSchemaGuidance'
import { MCP_ALLOW_WARNING } from '../lib/mcpSelection'
import { PROPOSALS_POLL_MS, applyPendingChanges, beginChangesRequest } from '../lib/proposals'
import {
  acceptFailureMessage, escapeHidden, isPlainName, lineDiff, schemaText, unsafeReason, wordDiff, type DiffPart, type ToolChangesListBody,
} from '../lib/toolChanges'
import type { PinEventInfo, ToolChangeInfo } from '../types'
import { TimeLabel } from '@blerglab/chat'

// How many decided pages the connection history follows before it stops.
const HISTORY_PAGES = 10

const button: CSSProperties = {
  background: 'var(--scree)', border: '1px solid var(--stone)', borderRadius: 6, color: 'var(--fog)',
  padding: '5px 12px', cursor: 'pointer', fontSize: '0.85rem', fontFamily: 'inherit',
}
const primary: CSSProperties = {
  ...button, background: 'color-mix(in srgb, var(--blaze) 22%, var(--basalt))', border: '1px solid var(--blaze)', color: 'var(--amber)', fontWeight: 700,
}
const muted: CSSProperties = { color: 'var(--fog)', fontSize: 13 }
const pre: CSSProperties = {
  margin: 0, padding: '8px 10px', background: 'var(--basalt)', border: '1px solid var(--stone)', borderRadius: 6,
  color: 'var(--chalk)', fontFamily: 'var(--mono, monospace)', fontSize: 13, whiteSpace: 'pre-wrap', overflowWrap: 'anywhere', maxHeight: 360, overflowY: 'auto',
}
const badge: CSSProperties = { ...muted, border: '1px solid var(--stone)', borderRadius: 4, padding: '0 4px', fontSize: 11 }

async function errorText(res: Response): Promise<string> {
  try {
    const body = await res.json() as { error?: string }
    if (body.error) return body.error
  } catch { /* fall through */ }
  return `HTTP ${res.status}`
}

// Diff renders parts as plain text with colour; the text has already had hidden characters escaped.
function Diff({ parts, lines, testId }: { parts: DiffPart[]; lines: boolean; testId: string }) {
  return (
    <pre data-testid={testId} style={pre}>
      {parts.map((p, i) => {
        const style: CSSProperties = p.kind === 'add' ? { background: 'color-mix(in srgb, var(--success, #4caf50) 25%, transparent)' }
          : p.kind === 'del' ? { background: 'color-mix(in srgb, var(--danger) 25%, transparent)', textDecoration: 'line-through' } : {}
        const text = lines ? (p.kind === 'add' ? '+ ' : p.kind === 'del' ? '- ' : '  ') + p.text + '\n' : p.text
        return <span key={i} data-testid={p.kind === 'same' ? undefined : `diff-${p.kind}`} style={style}>{text}</span>
      })}
    </pre>
  )
}

const leadStyle: CSSProperties = { color: 'var(--chalk)', fontSize: 14, lineHeight: 1.4 }
const heading: CSSProperties = { ...muted, color: 'var(--chalk)', fontWeight: 700 }

// leadSentence says in plain words what happened and, while the change is pending, what each button does.
function leadSentence(c: ToolChangeInfo, hasOld: boolean): ReactNode {
  const conn = <b>{c.connection_name}</b>
  const tool = <b>{escapeHidden(c.tool).text}</b>
  const when = c.decided_at ? <> <TimeLabel ts={c.decided_at} /></> : null
  switch (c.state) {
    case 'pending':
      if (c.kind === 'new') {
        return <>{conn} now offers {tool}, which the session did not have when it started. It is off until you decide: Grant authorizes it in the mode you choose; Keep off leaves it off.</>
      }
      return <>{conn} changed what {tool} does after this session pinned it. Calls are blocked until you decide: Accept updates this session's grant to {hasOld ? 'the new definition below' : 'the definition below (the previous text was not kept)'}; Keep off leaves it blocked.</>
    case 'accepted':
      return <>You {c.kind === 'new' ? 'granted' : 'accepted'} this{when}; this session's grant now authorizes the current definition.</>
    case 'declined':
      return <>You kept this off{when}. Later changes to this tool in this session are listed without a badge.</>
    case 'expired':
      return <>Nobody decided within 7 days; the tool stays off.</>
    case 'superseded':
      return <>The server changed this tool again; see the newer change.</>
  }
}

interface CardProps {
  c: ToolChangeInfo
  busy: boolean
  error: string | undefined
  onAccept: (mode: 'allow' | 'propose') => void
  onDecline: () => void
}

function ToolChangeCard({ c, busy, error, onAccept, onDecline }: CardProps) {
  const [mode, setMode] = useState<'allow' | 'propose'>(c.kind === 'changed' ? c.mode : 'propose')
  // Escaping and diffing are keyed on the two definitions (stable objects per loaded card): a definition can be 256 KiB and the list
  // re-renders on every poll.
  const { oldDesc, newDesc, oldSchema, newSchema, descParts, schemaParts } = useMemo(() => {
    const oldDesc = escapeHidden(c.old_definition?.description ?? '')
    const newDesc = escapeHidden(c.new_definition?.description ?? '')
    const oldSchema = escapeHidden(schemaText(c.old_definition))
    const newSchema = escapeHidden(schemaText(c.new_definition))
    const descParts: DiffPart[] = c.old_definition ? wordDiff(oldDesc.text, newDesc.text) : [{ kind: 'same', text: newDesc.text }]
    const schemaParts: DiffPart[] = c.old_definition ? lineDiff(oldSchema.text, newSchema.text) : newSchema.text.split('\n').map(l => ({ kind: 'same' as const, text: l }))
    return { oldDesc, newDesc, oldSchema, newSchema, descParts, schemaParts }
  }, [c.old_definition, c.new_definition])
  const hidden = oldDesc.hidden || newDesc.hidden || oldSchema.hidden || newSchema.hidden
  const unsafe = unsafeReason(c.new_definition?.unsafe)
  const decidable = c.state === 'pending' && c.kind !== 'removed'
  const canAccept = decidable && !unsafe && !!c.new_definition
  const hasOld = !!c.old_definition
  const descChanged = oldDesc.text !== newDesc.text
  const schemaChanged = oldSchema.text !== newSchema.text
  const summary = descChanged && schemaChanged ? 'Description and input schema' : descChanged ? 'Description' : schemaChanged ? 'Input schema' : 'Nothing visible changed (whitespace or ordering)'
  const lead = leadSentence(c, hasOld)
  return (
    <div id={`tool-change-card-${c.id}`} data-testid={`tool-change-${c.id}`} style={{ border: '1px solid var(--stone)', borderRadius: 8, background: 'var(--scree)', padding: '10px 14px', display: 'flex', flexDirection: 'column', gap: 8 }}>
      <div style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
        <span style={{ color: 'var(--chalk)', fontWeight: 700, fontFamily: 'var(--mono, monospace)' }}>{escapeHidden(c.tool).text}</span>
        {!isPlainName(c.tool) && <span data-testid="odd-name-badge" style={{ ...badge, color: 'var(--danger)' }}>name has unusual characters</span>}
        <span style={muted}>on <span>{c.connection_name}</span></span>
        {hidden && <span data-testid="hidden-chars-badge" style={{ ...badge, color: 'var(--danger)' }}>contains hidden characters (shown as ⟨U+…⟩)</span>}
        <span style={{ marginLeft: 'auto', ...badge }}>{c.state}</span>
      </div>
      {c.kind === 'removed' && <div style={muted}>The server no longer offers this tool. Nothing to accept; it is off in this session.</div>}
      {c.kind !== 'removed' && <div data-testid="tool-change-lead" style={leadStyle}>{lead}</div>}
      {(c.state === 'pending' || c.state === 'accepted') && c.kind !== 'removed' && <ToolSchemaGuidance policy={c.tool_schema_refresh} />}
      {c.kind !== 'removed' && (
        <>
          <div style={heading}>{hasOld ? 'What changed' : 'Definition'}</div>
          {hasOld && <div data-testid="change-summary" style={muted}>{summary}</div>}
          <div style={{ ...muted, color: 'var(--chalk)' }}>Description{hasOld ? ' (before → after)' : ''}</div>
          <Diff testId="desc-diff" lines={false} parts={descParts} />
          <details open={hasOld && schemaChanged} data-testid="schema-details">
            <summary style={{ ...muted, color: 'var(--chalk)', cursor: 'pointer' }}>{hasOld ? 'Input schema (before → after)' : 'Input schema'}</summary>
            <div style={{ marginTop: 6 }}><Diff testId="schema-diff" lines parts={schemaParts} /></div>
          </details>
        </>
      )}
      <div style={{ ...muted, fontSize: 12, display: 'flex', gap: 10, flexWrap: 'wrap', alignItems: 'center' }}>
        <Link to={`/sessions/${c.session_id}`} style={{ color: 'var(--amber)' }}>Open session</Link>
        <span>Recorded <TimeLabel ts={c.created_at} /></span>
        {c.state === 'pending' && <span>Expires <TimeLabel ts={c.expires_at} /></span>}
        {c.decided_at && <span>Decided <TimeLabel ts={c.decided_at} /></span>}
      </div>
      {unsafe && <div role="alert" style={{ color: 'var(--danger)', fontSize: 13 }}>{unsafe}</div>}
      {decidable && c.kind === 'new' && (
        <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
          <label style={muted}>
            Mode{' '}
            <select aria-label={`${c.tool} mode`} value={mode} onChange={e => setMode(e.target.value as 'allow' | 'propose')}>
              <option value="propose">propose (queued for your approval)</option>
              {c.mode === 'allow' && <option value="allow">allow</option>}
            </select>
          </label>
          {mode === 'allow' && <div role="alert" style={{ color: 'var(--danger)', fontSize: 12 }}>Warning: {MCP_ALLOW_WARNING}.</div>}
        </div>
      )}
      {decidable && c.kind === 'changed' && c.mode === 'allow' && (
        <label style={muted}>
          <input type="checkbox" checked={mode === 'propose'} onChange={e => setMode(e.target.checked ? 'propose' : 'allow')} /> Tighten to propose (queue its calls for your approval)
        </label>
      )}
      {decidable && (
        <div style={{ display: 'flex', gap: 8 }}>
          {canAccept && (
            <button type="button" style={primary} disabled={busy} onClick={() => onAccept(mode)}>
              {c.kind === 'new' ? 'Grant' : 'Accept updated grant'}
            </button>
          )}
          <button type="button" style={button} disabled={busy} onClick={onDecline}>Keep off</button>
        </div>
      )}
      {error && <div role="alert" data-testid={`tool-change-error-${c.id}`} style={{ color: 'var(--danger)', fontSize: 13 }}>{error}</div>}
    </div>
  )
}

interface Props {
  focusId: string | null
  // The connection whose history is shown under the pending changes (core Settings links here
  // with ?connection=<id>); null shows pending changes only.
  connectionId: string | null
  /** Only this session's changes (the Proposals page's ?session=). */
  sessionId?: string | null
}

export default function ToolChanges({ focusId, connectionId, sessionId }: Props) {
  const [open, setOpen] = useState<ToolChangeInfo[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [showMuted, setShowMuted] = useState(false)
  const [busy, setBusy] = useState<string | null>(null)
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [history, setHistory] = useState<{ events: PinEventInfo[]; decided: ToolChangeInfo[] } | null>(null)
  const [historyError, setHistoryError] = useState<string | null>(null)
  const historySeq = useRef(0)
  const inFlight = useRef(new Set<string>())
  const seq = useRef(0)
  const scrolledFor = useRef<string | null>(null)

  // The connection's history: its pin events and its decided changes (filtered here; the route
  // filters by session, not connection, so pages are followed, up to HISTORY_PAGES).
  const loadHistory = useCallback(async () => {
    if (!connectionId) { setHistory(null); setHistoryError(null); return }
    const s = ++historySeq.current
    try {
      const er = await apiFetch(`/api/mcp/connections/${encodeURIComponent(connectionId)}/pin-events`)
      if (!er.ok) throw new Error(await errorText(er))
      const events = (await er.json() as { events?: PinEventInfo[] }).events ?? []
      const decided: ToolChangeInfo[] = []
      let cursor: string | null = null
      for (let page = 0; page < HISTORY_PAGES; page++) {
        const dr: Response = await apiFetch(`/api/tool-changes?scope=decided${cursor ? `&before=${encodeURIComponent(cursor)}` : ''}`)
        if (!dr.ok) throw new Error(await errorText(dr))
        const body = await dr.json() as ToolChangesListBody
        decided.push(...(body.tool_changes ?? []).filter(c => c.connection_id === connectionId))
        cursor = body.decided_next ?? null
        if (!cursor) break
      }
      if (s !== historySeq.current) return
      setHistory({ events, decided })
      setHistoryError(null)
    } catch (e: unknown) {
      if (s !== historySeq.current) return
      setHistoryError(e instanceof Error ? e.message : String(e))
    }
  }, [connectionId])

  useEffect(() => { void loadHistory() }, [loadHistory])

  const load = useCallback(async () => {
    const s = ++seq.current
    const ticket = beginChangesRequest()
    try {
      const res = await apiFetch('/api/tool-changes?scope=open')
      if (!res.ok) throw new Error(await errorText(res))
      const body = await res.json() as ToolChangesListBody
      if (s !== seq.current) return
      setOpen(body.tool_changes ?? [])
      if (typeof body.pending_count === 'number') applyPendingChanges(ticket, body.pending_count)
      setError(null)
    } catch (e: unknown) {
      if (s !== seq.current) return
      setError(e instanceof Error ? e.message : String(e))
      setOpen(prev => prev ?? [])
    }
  }, [])

  useEffect(() => {
    void load()
    const tick = () => { if (!document.hidden) void load() }
    const timer = setInterval(tick, PROPOSALS_POLL_MS)
    window.addEventListener('focus', tick)
    document.addEventListener('visibilitychange', tick)
    return () => {
      clearInterval(timer)
      window.removeEventListener('focus', tick)
      document.removeEventListener('visibilitychange', tick)
    }
  }, [load])

  // Scroll to the deep-linked card once it exists, once per focusId (polls must not yank the page).
  useEffect(() => {
    if (!focusId || scrolledFor.current === focusId) return
    const el = document.getElementById(`tool-change-card-${focusId}`)
    if (!el) return
    scrolledFor.current = focusId
    el.scrollIntoView?.({ block: 'center' })
  }, [focusId, open])

  async function act(id: string, path: 'accept' | 'decline', body?: unknown) {
    if (inFlight.current.has(id)) return
    inFlight.current.add(id)
    setBusy(id)
    setErrors(e => Object.fromEntries(Object.entries(e).filter(([k]) => k !== id)))
    try {
      const res = await apiFetch(`/api/tool-changes/${encodeURIComponent(id)}/${path}`, body === undefined ? { method: 'POST' } : {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
      })
      if (!res.ok) {
        const msg = await errorText(res)
        setErrors(e => ({ ...e, [id]: path === 'accept' ? acceptFailureMessage(res.status, msg) : msg }))
      }
    } catch (e: unknown) {
      setErrors(er => ({ ...er, [id]: e instanceof Error ? e.message : String(e) }))
    } finally {
      inFlight.current.delete(id)
      setBusy(null)
      await Promise.all([load(), loadHistory()])
    }
  }

  // Each card needs the two definitions; the list carries hashes only, so cards fetch their own.
  const items = (open ?? []).filter(c => !sessionId || c.session_id === sessionId)
  const visible = items.filter(c => showMuted || !c.muted)
  const mutedCount = items.filter(c => c.muted).length
  const groups = new Map<string, ToolChangeInfo[]>()
  for (const c of visible) {
    const k = `${c.session_id}|${c.connection_id}`
    groups.set(k, [...(groups.get(k) ?? []), c])
  }

  return (
    <div data-testid="tool-changes" style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
      <h3 style={{ margin: 0, color: 'var(--chalk)', fontSize: '1rem' }}>Tool changes</h3>
      {error && <div role="alert" style={{ color: 'var(--danger)', fontSize: 13 }}>Could not load tool changes: {error}</div>}
      {open === null && !error && <div style={muted}>Loading…</div>}
      {open !== null && !error && visible.length === 0 && (
        <div data-testid="tool-changes-empty" style={muted}>
          No tool changes waiting. When an MCP server changes a tool a session uses, or adds one, the tool is turned off and the change is listed here for you to accept or keep off.
        </div>
      )}
      {[...groups.entries()].map(([k, cs]) => (
        <section key={k} data-testid="tool-change-group" style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
          <div style={{ ...muted, fontWeight: 600 }}>{cs[0].connection_name} · <Link to={`/sessions/${cs[0].session_id}`} style={{ color: 'var(--amber)' }}>session</Link> · {cs.length} {cs.length === 1 ? 'tool' : 'tools'}</div>
          {cs.map(c => <LoadedCard key={c.id} summary={c} busy={busy === c.id} error={errors[c.id]} onAccept={mode => void act(c.id, 'accept', { mode })} onDecline={() => void act(c.id, 'decline')} />)}
        </section>
      ))}
      {mutedCount > 0 && (
        <button type="button" style={button} onClick={() => setShowMuted(s => !s)}>
          {showMuted ? 'Hide' : 'Show'} {mutedCount} declined earlier
        </button>
      )}
      {connectionId && historyError && <div role="alert" style={{ color: 'var(--danger)', fontSize: 13 }}>Could not load the history: {historyError}</div>}
      {connectionId && history && !historyError && (
        <section data-testid="connection-history" style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
          <h4 style={{ margin: 0, color: 'var(--chalk)', fontSize: '0.95rem' }}>History for this connection</h4>
          {history.events.length === 0 && history.decided.length === 0 && <div style={muted}>No pin events or decided changes yet.</div>}
          {history.events.map(e => (
            <div key={e.id} data-testid="pin-event" style={{ ...muted, fontSize: 12 }}>
              <TimeLabel ts={e.created_at} />: {e.action} <span style={{ fontFamily: 'var(--mono, monospace)' }}>{escapeHidden(e.tool).text}</span>
              {e.mode ? ` (${e.mode})` : ''} by {e.actor === 'policy:auto_accept' ? 'the connection policy' : e.actor === 'system' ? 'expiry' : 'you'}
            </div>
          ))}
          {history.decided.map(c => (
            <div key={c.id} style={{ ...muted, fontSize: 12 }}>
              <TimeLabel ts={c.decided_at ?? c.created_at} />: {c.kind} <span style={{ fontFamily: 'var(--mono, monospace)' }}>{escapeHidden(c.tool).text}</span> → {c.state}
            </div>
          ))}
        </section>
      )}
    </div>
  )
}

// LoadedCard fetches the change with its definitions once, then renders the card. A failed fetch
// shows the error with Retry; it never falls back to the list row, which has no definitions.
function LoadedCard(props: { summary: ToolChangeInfo; busy: boolean; error: string | undefined; onAccept: (m: 'allow' | 'propose') => void; onDecline: () => void }) {
  const [full, setFull] = useState<ToolChangeInfo | null>(props.summary.new_definition !== undefined ? props.summary : null)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [attempt, setAttempt] = useState(0)
  const summaryId = props.summary.id
  useEffect(() => {
    if (full) return
    let cancelled = false
    apiFetch(`/api/tool-changes/${encodeURIComponent(summaryId)}`)
      .then(async res => {
        if (!res.ok) throw new Error(await errorText(res))
        return await res.json() as ToolChangeInfo
      })
      .then(c => { if (!cancelled) setFull(c) })
      .catch((e: unknown) => { if (!cancelled) setLoadError(e instanceof Error ? e.message : String(e)) })
    return () => { cancelled = true }
  }, [full, summaryId, attempt])
  if (loadError !== null) {
    const s = props.summary
    return (
      <div id={`tool-change-card-${s.id}`} data-testid={`tool-change-${s.id}`} style={{ border: '1px solid var(--stone)', borderRadius: 8, background: 'var(--scree)', padding: '10px 14px', display: 'flex', flexDirection: 'column', gap: 8 }}>
        <div style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
          <span style={{ color: 'var(--chalk)', fontWeight: 700, fontFamily: 'var(--mono, monospace)' }}>{escapeHidden(s.tool).text}</span>
          <span style={muted}>on <span>{s.connection_name}</span></span>
        </div>
        <div role="alert" style={{ color: 'var(--danger)', fontSize: 13 }}>Could not load this definition: {loadError}</div>
        <div style={{ display: 'flex', gap: 8 }}>
          <button type="button" style={button} onClick={() => { setLoadError(null); setAttempt(n => n + 1) }}>Retry</button>
          {s.state === 'pending' && s.kind !== 'removed' && <button type="button" style={button} disabled={props.busy} onClick={props.onDecline}>Keep off</button>}
        </div>
        {props.error && <div role="alert" data-testid={`tool-change-error-${s.id}`} style={{ color: 'var(--danger)', fontSize: 13 }}>{props.error}</div>}
      </div>
    )
  }
  if (!full) return <div id={`tool-change-card-${props.summary.id}`} data-testid={`tool-change-${props.summary.id}`} style={muted}>Loading…</div>
  return <ToolChangeCard c={{ ...full, state: props.summary.state }} busy={props.busy} error={props.error} onAccept={props.onAccept} onDecline={props.onDecline} />
}
