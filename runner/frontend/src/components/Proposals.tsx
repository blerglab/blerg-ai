import ProposalPreview from './ProposalPreview'
import { ProposalExpiry } from './ProposalAttention'
import { proposalsChanged } from '../hooks/useProposalInbox'
import { actorLabel, permissionVia, serverClockOffset } from '../lib/proposalAttention'
import { useProposalsWaiting } from '../hooks/useProposalsWaiting'
// Proposals (/proposals): what agents asked to do through a tool in `propose` mode (spec 9). The
// gateway froze each call; nothing ran. Here the person reads the FROZEN ARGUMENTS (authoritative:
// approving sends exactly these), then approves or rejects. Built-in board filing drafts can be
// edited first; their saved arguments require a fresh confirmation. Ordinary MCP calls stay frozen.
// The summary line is built by the gateway and is only a convenience. Upstream results are untrusted: shown as plain text only.
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { CSSProperties } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { apiFetch } from '../apiFetch'
import ToolChanges from './ToolChanges'
import BoardFileDraft from './BoardFileDraft'
import {pluginApplication,pluginState,type PluginScope} from '../lib/sessionPlugins'
import {
  PROPOSALS_POLL_MS, applyPendingCount, applyPendingChanges, approveFailureMessage, beginCountRequest, beginChangesRequest, isAwaitingYou, resultText,
  usePendingProposals,
} from '../lib/proposals'
import type { ProposalInfo, ProposalState } from '../types'
import { TimeLabel } from '@blerglab/chat'

const button: CSSProperties = {
  background: 'var(--scree)', border: '1px solid var(--stone)', borderRadius: 6, color: 'var(--fog)',
  padding: '5px 12px', cursor: 'pointer', fontSize: '0.85rem', fontFamily: 'inherit',
}
const primary: CSSProperties = {
  ...button, background: 'color-mix(in srgb, var(--blaze) 22%, var(--basalt))', border: '1px solid var(--blaze)', color: 'var(--amber)', fontWeight: 700,
}
const muted: CSSProperties = { color: 'var(--fog)', fontSize: 13 }

const PILL: Record<ProposalState, string> = {
  pending: 'var(--amber)', executing: 'var(--amber)', unknown: 'var(--danger)', done: 'var(--success, #4caf50)',
  failed: 'var(--danger)', rejected: 'var(--fog-dim)', expired: 'var(--fog-dim)',
}

async function errorText(res: Response): Promise<string> {
  try {
    const body = await res.json() as { error?: string }
    if (body.error) return body.error
  } catch { /* fall through */ }
  return `HTTP ${res.status}`
}

const preStyle: CSSProperties = {
  margin: 0, padding: '8px 10px', background: 'var(--basalt)', border: '1px solid var(--stone)', borderRadius: 6,
  color: 'var(--chalk)', fontFamily: 'var(--mono, monospace)', fontSize: 13, whiteSpace: 'pre-wrap', overflowWrap: 'anywhere',
  maxHeight: 360, overflowY: 'auto',
}

interface CardProps {
  p: ProposalInfo
  focused: boolean
  busy: boolean
  confirming: boolean
  error: string | undefined
  onAsk: () => void
  onCancel: () => void
  onApprove: (scope?: PluginScope) => void
  onReject: () => void
  onResolve: (outcome: 'done' | 'failed') => void
  onSavedDraft: (updated: ProposalInfo) => void
}

function ProposalCard({ p, focused, busy, confirming, error, onAsk, onCancel, onApprove, onReject, onResolve, onSavedDraft }: CardProps) {
  const [editing, setEditing] = useState(false)
  const boardFile = p.kind === 'board_file'
  const [pluginScope, setPluginScope] = useState<PluginScope>(p.plugin_session_apply ? 'session' : 'always')
  const isPlugin = p.kind === 'plugin_request'
  const scope = p.plugin_session_apply ? pluginScope : 'always'
  const stateLabel = pluginState(p)
  const stateColor = stateLabel === 'queued' || stateLabel === 'installing' ? PILL.executing : stateLabel === 'failed' ? PILL.failed : PILL[p.state]
  const result = resultText(p.result)
  return (
    <div
      id={`proposal-card-${p.id}`} data-testid={`proposal-${p.id}`} data-focused={focused ? 'true' : undefined}
      style={{ border: focused ? '2px solid var(--blaze)' : '1px solid var(--stone)', borderRadius: 8, background: 'var(--scree)', padding: '10px 14px', display: 'flex', flexDirection: 'column', gap: 8 }}
    >
      <div style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
        <span style={{ color: 'var(--chalk)', fontWeight: 700, fontFamily: 'var(--mono, monospace)' }}>{p.tool}</span>
        <span style={muted}>on {p.connection_name}</span>
        <span data-testid="proposal-state" style={{ marginLeft: 'auto', color: stateColor, border: `1px solid ${stateColor}`, borderRadius: 10, padding: '0 8px', fontSize: 12 }}>{stateLabel === 'new_sessions' ? 'new sessions only' : stateLabel}</span>
      </div>
      {boardFile && <div>File a ticket on {typeof p.arguments.board_name === 'string' ? p.arguments.board_name : String(p.arguments.board_id ?? '')}</div>}
      <ProposalPreview proposal={p} complete={confirming} />
      <ProposalExpiry proposal={p} />
      {boardFile && p.state === 'pending' && (editing
        ? <BoardFileDraft proposal={p} onSaved={updated => { setEditing(false); onSavedDraft(updated) }} onCancel={() => setEditing(false)} />
        : <button type="button" style={button} disabled={busy || !p.arguments_raw} onClick={() => { onCancel(); setEditing(true) }}>Edit ticket draft</button>)}
      {isPlugin && <p role="status" style={muted}>{pluginApplication(p)}</p>}
      {p.agent_summary && (
        <div
          data-testid="proposal-summary" title="Written by the gateway for convenience; not checked against the arguments above"
          style={{ ...muted, fontSize: 12, color: 'var(--fog-dim)' }}
        >
          Summary (unverified): {p.agent_summary}
        </div>
      )}
      <div style={{ ...muted, fontSize: 12, display: 'flex', gap: 10, flexWrap: 'wrap', alignItems: 'center' }}>
        {p.session_id && <Link to={`/sessions/${p.session_id}`} style={{ color: 'var(--amber)' }}>Open session</Link>}
        {p.cron_id && <span data-testid="proposal-cron-badge" style={{ border: '1px solid var(--stone)', borderRadius: 4, padding: '0 4px' }}>cron</span>}
        <span>Proposed <TimeLabel ts={p.created_at} /></span>
        {p.state === 'pending' && <span>Expires <TimeLabel ts={p.expires_at} /></span>}
        {p.decided_at && <span>Decided <TimeLabel ts={p.decided_at} /></span>}
        {p.decided_by && <span>Decided by {actorLabel(p.decided_by_you)}</span>}
        {p.approved_by_grant && <span data-testid="proposal-by-grant">Approved automatically by a session permission</span>}
      </div>

      {p.approved_permission && <div className="proposal-permission-audit" data-testid={`proposal-permission-${p.id}`}>
        Permission: {p.approved_permission.board_name || p.approved_permission.board_id || p.approved_permission.connection_name}
        {' · '}{p.approved_permission.tool || p.approved_permission.level}
        {' · '}set by {actorLabel(p.approved_permission.granted_by_you)} {permissionVia(p.approved_permission.created_via)} <TimeLabel ts={p.approved_permission.created_at} />
        {p.approved_permission.revoked_at && <> · since revoked <TimeLabel ts={p.approved_permission.revoked_at} /></>}
      </div>}

      {(p.extensions?.length ?? 0) > 0 && <ul className="proposal-extension-history" aria-label="Deadline extensions" data-testid={`proposal-extensions-${p.id}`}>
        {p.extensions!.map(e => <li key={e.extended_at + e.expires_at}>
          Extended by {actorLabel(e.by_you)} <TimeLabel ts={e.extended_at} />: deadline moved to {new Date(e.expires_at).toLocaleString()}
        </li>)}
      </ul>}

      {result && (
        <div>
          <div style={{ ...muted, fontWeight: 600, marginBottom: 4 }}>{result.isError || p.state === 'failed' ? 'Error' : 'Result'}</div>
          <pre data-testid={`proposal-result-${p.id}`} style={preStyle}>{result.text}</pre>
        </div>
      )}

      {p.state === 'unknown' && (
        <div data-testid={`unknown-${p.id}`} style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
          <div role="status" style={{ color: 'var(--danger)', fontSize: 13 }}>Outcome unknown — check the target service, then mark what happened</div>
          <div style={{ display: 'flex', gap: 8 }}>
            <button type="button" style={button} disabled={busy} onClick={() => onResolve('done')}>Mark done</button>
            <button type="button" style={button} disabled={busy} onClick={() => onResolve('failed')}>Mark failed</button>
          </div>
        </div>
      )}

      {p.state === 'executing' && <div style={muted}>Running now. Refresh in a moment.</div>}

      {p.state === 'pending' && !confirming && !editing && (
        <div style={{ display: 'flex', gap: 8 }}>
          <button type="button" style={primary} disabled={busy || boardFile && !p.arguments_raw} onClick={onAsk}>Approve</button>
          <button type="button" style={button} disabled={busy} onClick={onReject}>Reject</button>
        </div>
      )}
      {p.state === 'pending' && confirming && !editing && (
        <div data-testid={`approve-confirm-${p.id}`} style={{ display: 'flex', flexDirection: 'column', gap: 6, borderTop: '1px solid var(--stone)', paddingTop: 8 }}>
          <div style={{ color: 'var(--chalk)', fontSize: 13 }}>
            {isPlugin ? 'Approve this exact plugin request. It will be installed only after your confirmation.' : <>This will run {p.tool} on {p.connection_name} with exactly these arguments, shown in full above. It can take up to a minute.</>}
          </div>
          {isPlugin && <>
            <label>Apply plugin<select aria-label="Apply plugin" value={scope} onChange={e=>setPluginScope(e.target.value as PluginScope)} disabled={busy}><option value="session" disabled={!p.plugin_session_apply}>This session only</option><option value="always">Always on</option></select></label>
            <p style={muted}>{scope === 'session' ? 'Loads between turns; account settings stay unchanged.' : p.plugin_session_apply ? 'Saves to account settings and loads between turns.' : 'Saves for new sessions; this session keeps its current plugins.'}</p>
          </>}
          <div style={{ display: 'flex', gap: 8 }}>
            <button type="button" style={primary} disabled={busy || (isPlugin && typeof p.arguments_raw !== 'string')} onClick={()=>onApprove(isPlugin ? scope : undefined)}>{busy ? 'Running…' : isPlugin ? 'Add plugin' : 'Run it'}</button>
            <button type="button" style={button} disabled={busy} onClick={onCancel}>Cancel</button>
          </div>
        </div>
      )}
      {error && <div role="alert" data-testid={`proposal-error-${p.id}`} style={{ color: 'var(--danger)', fontSize: 13 }}>{error}</div>}
    </div>
  )
}

// ProposalsBadge: the pending count (proposals plus tool changes) next to the Proposals nav entry; nothing at 0. It only reads the
// shared count (the app keeps it fresh with useProposalCountPoll).
export function ProposalsBadge({ style }: { style?: CSSProperties }) {
  const total = usePendingProposals(s => s.count + s.changes)
  if (total <= 0) return null
  return (
    <span
      data-testid="proposals-count-badge"
      aria-label={`${total} items awaiting your review`}
      style={{
        background: 'var(--danger)', color: 'var(--chalk)', borderRadius: 8, minWidth: 16, height: 16, fontSize: '0.65rem',
        fontWeight: 700, lineHeight: '16px', textAlign: 'center', padding: '0 4px', boxSizing: 'border-box', ...style,
      }}
    >
      {total}
    </span>
  )
}

type Tab = 'pending' | 'decided'

export default function Proposals() {
  const [params] = useSearchParams()
  const focusId = params.get('id')
  // ?session=<id> (the session panel's link): that session's proposals and tool changes only.
  const sessionFilter = params.get('session')
  const [open, setOpen] = useState<ProposalInfo[] | null>(null)
  // This client's clock minus the server's, from the open list's server_time.
  const [openClockOffset, setOpenClockOffset] = useState(0)
  const [decided, setDecided] = useState<ProposalInfo[] | null>(null)
  const [decidedNext, setDecidedNext] = useState<string | null>(null)
  const [loadingMore, setLoadingMore] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [tab, setTab] = useState<Tab>(() => ['id', 'session', 'change', 'connection'].some(key => params.has(key)) || params.get('view') === 'pending' ? 'pending' : 'decided')
  const [focused, setFocused] = useState<ProposalInfo | null>(null)
  const [focusError, setFocusError] = useState<string | null>(null)
  const [confirmedRaw, setConfirmedRaw] = useState<Record<string, string | undefined>>({})
  const [confirming, setConfirming] = useState<string | null>(null)
  const [busy, setBusy] = useState<string | null>(null)
  const [errors, setErrors] = useState<Record<string, string>>({})
  // Synchronous guard: a second click in the same tick cannot slip past a state update.
  const inFlight = useRef(new Set<string>())
  // Request ordering: a response applies only while it is still the newest request of its kind.
  const openSeq = useRef(0)
  const decidedSeq = useRef(0)
  const decidedPages = useRef(0)
  const tabRef = useRef<Tab>('pending')
  tabRef.current = tab
  const scrolledFor = useRef<string | null>(null)

  const loadOpen = useCallback(async () => {
    const seq = ++openSeq.current
    const ticket = beginCountRequest()
    const changesTicket = beginChangesRequest()
    try {
      const res = await apiFetch('/api/proposals?scope=open')
      if (!res.ok) throw new Error(await errorText(res))
      const body = await res.json() as { proposals?: ProposalInfo[]; pending_count?: number; tool_changes_pending?: number; server_time?: string }
      if (seq !== openSeq.current) return
      setOpen(body.proposals ?? [])
      setOpenClockOffset(serverClockOffset(body.server_time))
      if (typeof body.pending_count === 'number') applyPendingCount(ticket, body.pending_count)
      if (typeof body.tool_changes_pending === 'number') applyPendingChanges(changesTicket, body.tool_changes_pending)
      setError(null)
    } catch (e: unknown) {
      if (seq !== openSeq.current) return
      setError(e instanceof Error ? e.message : String(e))
      setOpen(prev => prev ?? [])
    }
  }, [])

  // The first page of decided proposals (replaces what was loaded).
  const loadDecided = useCallback(async () => {
    const seq = ++decidedSeq.current
    try {
      const res = await apiFetch('/api/proposals?scope=decided')
      if (!res.ok) throw new Error(await errorText(res))
      const body = await res.json() as { proposals?: ProposalInfo[]; decided_next?: string | null }
      if (seq !== decidedSeq.current) return
      decidedPages.current = 1
      setDecided(body.proposals ?? [])
      setDecidedNext(body.decided_next ?? null)
      setError(null)
    } catch (e: unknown) {
      if (seq !== decidedSeq.current) return
      setError(e instanceof Error ? e.message : String(e))
      setDecided(prev => prev ?? [])
    }
  }, [])

  async function loadMore() {
    if (!decidedNext || loadingMore) return
    const seq = ++decidedSeq.current
    setLoadingMore(true)
    try {
      const res = await apiFetch(`/api/proposals?scope=decided&before=${encodeURIComponent(decidedNext)}`)
      if (!res.ok) throw new Error(await errorText(res))
      const body = await res.json() as { proposals?: ProposalInfo[]; decided_next?: string | null }
      if (seq !== decidedSeq.current) return
      decidedPages.current += 1
      setDecided(prev => {
        const have = new Set((prev ?? []).map(p => p.id))
        return [...(prev ?? []), ...(body.proposals ?? []).filter(p => !have.has(p.id))]
      })
      setDecidedNext(body.decided_next ?? null)
    } catch (e: unknown) {
      if (seq === decidedSeq.current) setError(e instanceof Error ? e.message : String(e))
    } finally {
      setLoadingMore(false)
    }
  }

  // The deep-linked proposal, fetched by id (it may be decided and on a page that is not loaded).
  const loadFocused = useCallback(async (id: string, first: boolean) => {
    try {
      const res = await apiFetch(`/api/proposals/${encodeURIComponent(id)}`)
      if (res.status === 404) {
        if (first) setFocusError(`Proposal ${id} was not found. It may have been deleted (decided proposals are kept for 30 days) or it belongs to another account.`)
        return
      }
      if (!res.ok) throw new Error(await errorText(res))
      const p = await res.json() as ProposalInfo
      setFocused(p)
      setFocusError(null)
      if (first) setTab(isAwaitingYou(p) ? 'pending' : 'decided')
    } catch (e: unknown) {
      if (first) setFocusError(`Could not load proposal ${id}: ${e instanceof Error ? e.message : String(e)}`)
    }
  }, [])

  const load = useCallback(async () => {
    const jobs = [loadOpen()]
    // With more than the first page loaded a refresh would drop the person's place.
    if (tabRef.current === 'decided' && decidedPages.current <= 1) jobs.push(loadDecided())
    await Promise.all(jobs)
  }, [loadOpen, loadDecided])

  useEffect(() => {
    void load()
    const tick = () => { if (!document.hidden) void load() }
    const timer = setInterval(tick, PROPOSALS_POLL_MS)
    window.addEventListener('focus', tick)
    window.addEventListener('blerg:proposals-changed', tick)
    document.addEventListener('visibilitychange', tick)
    return () => {
      clearInterval(timer)
      window.removeEventListener('focus', tick)
      window.removeEventListener('blerg:proposals-changed', tick)
      document.removeEventListener('visibilitychange', tick)
    }
  }, [load])

  useEffect(() => {
    if (tab === 'decided' && decided === null) void loadDecided()
  }, [tab, decided, loadDecided])

  useEffect(() => {
    setFocused(null)
    setFocusError(null)
    scrolledFor.current = null
    if (focusId) void loadFocused(focusId, true)
  }, [focusId, loadFocused])

  async function act(id: string, path: string, body: unknown, failure: (status: number, msg: string) => string) {
    if (inFlight.current.has(id)) return
    inFlight.current.add(id)
    setBusy(id)
    setErrors(e => Object.fromEntries(Object.entries(e).filter(([k]) => k !== id)))
    try {
      const res = await apiFetch(`/api/proposals/${encodeURIComponent(id)}/${path}`, body === undefined ? { method: 'POST' } : {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
      })
      if (!res.ok) {
        const msg = failure(res.status, await errorText(res))
        setErrors(e => ({ ...e, [id]: msg }))
      }
    } catch (e: unknown) {
      setErrors(er => ({ ...er, [id]: e instanceof Error ? e.message : String(e) }))
    } finally {
      inFlight.current.delete(id)
      setBusy(null)
      setConfirming(c => (c === id ? null : c))
      // A decision moves the proposal to the decided list: start that list afresh.
      decidedPages.current = 0
      setDecided(null)
      setDecidedNext(null)
      if (focusId) void loadFocused(focusId, false)
      proposalsChanged()
      await loadOpen()
    }
  }

  const ofSession = (ps: ProposalInfo[]) => (sessionFilter ? ps.filter(p => p.session_id === sessionFilter) : ps)
  const pending = ofSession(open ?? [])
  // The tab counts what still waits on the person, as the bell and the badges do: a pending proposal
  // past its deadline (by the server's clock) is listed, as expired, but not counted.
  const pendingForCount = useMemo(() => (sessionFilter ? (open ?? []).filter(p => p.session_id === sessionFilter) : open ?? []), [open, sessionFilter])
  const waitingCount = useProposalsWaiting(pendingForCount, openClockOffset).length
  const listed = tab === 'pending' ? pending : ofSession(decided ?? [])
  // The deep-linked proposal shows on the tab it belongs to even when its page is not loaded.
  const focusedBelongs = focused !== null && isAwaitingYou(focused) === (tab === 'pending')
  const shown = focused && focusedBelongs && !listed.some(p => p.id === focused.id) ? [focused, ...listed] : listed
  const loadingList = tab === 'pending' ? open === null : decided === null
  const focusVisible = focused !== null && shown.some(p => p.id === focused.id)

  useEffect(() => {
    if (!focusId || !focusVisible || scrolledFor.current === focusId) return
    scrolledFor.current = focusId
    document.getElementById(`proposal-card-${focusId}`)?.scrollIntoView?.({ block: 'center' })
  }, [focusId, focusVisible])

  const tabButton = (id: Tab, label: string): React.ReactNode => (
    <button
      type="button" role="tab" aria-selected={tab === id} onClick={() => setTab(id)}
      style={{ ...button, ...(tab === id ? { color: 'var(--amber)', borderColor: 'var(--blaze)', fontWeight: 700 } : {}) }}
    >
      {label}
    </button>
  )

  return (
    <div style={{ padding: 16, display: 'flex', flexDirection: 'column', gap: 12, maxWidth: 820 }}>
      <h2 style={{ margin: 0, color: 'var(--chalk)', fontSize: '1.1rem' }}>Proposals</h2>
      <p style={muted}>Decision history and audit. Pending requests also appear in their session’s Needs you and in the notification bell.</p>
      <div role="tablist" style={{ display: 'flex', gap: 8 }}>
        {tabButton('pending', open === null ? 'Pending' : `Pending (${waitingCount})`)}
        {tabButton('decided', 'Decided')}
      </div>
      {sessionFilter && (
        <div data-testid="proposals-session-filter" style={muted}>
          Showing proposals for this session · <Link to="/proposals" style={{ color: 'var(--amber)' }}>Show all</Link>
        </div>
      )}
      {focusError && <div role="alert" data-testid="proposal-not-found" style={{ color: 'var(--danger)', fontSize: 13 }}>{focusError}</div>}
      {error && <div role="alert" style={{ color: 'var(--danger)', fontSize: 13 }}>Could not load proposals: {error}</div>}
      {loadingList && !error && <div style={muted}>Loading…</div>}
      {!loadingList && shown.length === 0 && (
        <div data-testid="proposals-empty" style={muted}>
          {tab === 'pending'
            ? 'Nothing waiting. When an agent uses a tool you set to "queued for your approval", its call is held here until you approve or reject it; when an MCP server changes a tool a session uses, the change is listed below.'
            : 'No decided proposals yet. Decided proposals are kept for 30 days.'}
        </div>
      )}
      {shown.map(p => (
        <ProposalCard
          key={p.id}
          p={p}
          focused={p.id === focusId}
          busy={busy === p.id}
          confirming={confirming === p.id && ((p.kind !== 'board_file' && p.kind !== 'plugin_request') || confirmedRaw[p.id] === p.arguments_raw)}
          error={errors[p.id]}
          onAsk={() => { setConfirmedRaw(previous => ({ ...previous, [p.id]: p.arguments_raw })); setConfirming(p.id) }}
          onCancel={() => setConfirming(null)}
          onApprove={scope => void act(p.id, 'approve', p.kind === 'plugin_request' ? {scope,expected_arguments_raw:confirmedRaw[p.id]} : p.kind === 'board_file' ? {expected_arguments_raw:confirmedRaw[p.id]} : undefined, p.kind === 'plugin_request' ? (_status, message) => `Plugin change was not accepted: ${message}` : approveFailureMessage)}
          onReject={() => void act(p.id, 'reject', undefined, (_s, m) => m)}
          onSavedDraft={updated => {
            ++openSeq.current
            setOpen(previous => previous?.map(item => item.id === updated.id ? updated : item) ?? null)
            setFocused(previous => previous?.id === updated.id ? updated : previous)
            setConfirming(null)
          }}
          onResolve={outcome => void act(p.id, 'resolve', { outcome }, (_s, m) => m)}
        />
      ))}
      {tab === 'pending' && <ToolChanges focusId={params.get('change')} connectionId={params.get('connection')} sessionId={sessionFilter} />}
      {tab === 'decided' && decidedNext && (
        <div>
          <button type="button" style={button} disabled={loadingMore} onClick={() => void loadMore()}>{loadingMore ? 'Loading…' : 'Load more'}</button>
        </div>
      )}
    </div>
  )
}
