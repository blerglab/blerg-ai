import { startTransition, useCallback, useEffect, useOptimistic, useState, createContext, useContext } from 'react'
import { isAutomated, startedByLabel } from '../lib/startedBy'
import { useNavigate, useLocation, Link } from 'react-router-dom'
import { apiFetch } from '../apiFetch'
import type { SessionInfo, SessionStatus, DaemonInfo } from '../types'
import { useSessionStore } from '../hooks/useSessionStore'
import SessionCard, { type SessionCardProps } from './SessionCard'
import { nestChildren, searchTree, type SessionTree } from '../lib/sessionTree'
import { childrenExpanded, setChildrenExpanded } from '../lib/sidebarPrefs'
import { getFocus, setFocus } from '../lib/sidebarPrefs'
import { NotificationsButton } from '../App'
import { ProposalsBadge } from './Proposals'
import { OpenSessionContext } from '../lib/openSession'
import { prefetchLaunchData } from '../lib/launchCache'
import { formatBuild } from '../lib/buildInfo'
import { setSettingsOpen } from '../lib/settingsDialog'
import UsageMeters from './UsageMeters'
import { displayState, waitingBannerText } from '../lib/displayState'

interface Props {
  onNewSession?: () => void
  variant?: 'page' | 'sidebar'
}

const Nesting = createContext<{ tree: SessionTree; activeId?: string; daemons: DaemonInfo[]; query: string } | null>(null)
function SessionBranch(props: SessionCardProps) {
  const context = useContext(Nesting)
  const [expanded, setExpanded] = useState(() => childrenExpanded(props.session.id))
  const children = context?.tree.childrenOf.get(props.session.id) ?? []
  const [searchChoice, setSearchChoice] = useState<{ query: string; open: boolean } | null>(null)
  const shown = context?.query ? searchChoice?.query === context.query ? searchChoice.open : true : expanded
  const toggle = () => { const next = !shown; setExpanded(next); if (context?.query) setSearchChoice({ query: context.query, open: next }); setChildrenExpanded(props.session.id, next) }
  return <div data-session-branch={props.session.id}>
    <SessionCard {...props} childrenCount={children.length} childrenExpanded={!!shown} onToggleChildren={toggle} />
    {shown && children.length > 0 && <div data-testid="nested-sessions" style={{ marginLeft: 12, paddingLeft: 8, borderLeft: '1px solid var(--stone)' }}>{children.map(child => <SessionBranch key={child.id} session={child} daemon={context?.daemons.find(d => d.id === child.daemon_id)} active={context?.activeId === child.id} nested />)}</div>}
  </div>
}

const ACTIVE_STATUSES = new Set<SessionStatus>(['starting', 'running', 'idle', 'waiting'])

// The persisted focus for "the cluster" (every other value is a daemon id).
const CLUSTER_FOCUS = 'cluster'

// The order of the active list: newest started first, and nothing else. A session's state (it
// flips between running and idle all day) never moves its card; the status glyph shows it.
function byNewestStarted(a: SessionInfo, b: SessionInfo): number {
  return new Date(b.started_at).getTime() - new Date(a.started_at).getTime()
}

// A pill of the "Where sessions run" row: compact, and selected when it is the focus.
function placePillStyle(selected: boolean, muted = false): React.CSSProperties {
  return {
    display: 'inline-flex',
    alignItems: 'center',
    gap: 5,
    background: selected ? 'color-mix(in srgb, var(--blaze) 22%, var(--basalt))' : 'none',
    color: selected ? 'var(--amber)' : muted ? 'var(--fog-dim)' : 'var(--fog)',
    border: `1px solid ${selected ? 'var(--blaze)' : 'var(--stone)'}`,
    borderRadius: 20,
    padding: '2px 9px',
    fontSize: '0.72rem',
    fontWeight: 600,
    lineHeight: '18px',
    whiteSpace: 'nowrap',
  }
}

function PlaceDot({ on }: { on: boolean }) {
  return <span style={{ width: 6, height: 6, borderRadius: '50%', background: on ? 'var(--lichen)' : 'var(--fog-dim)', display: 'inline-block', flexShrink: 0 }} />
}

function PlacePill({ testId, selected, disabled = false, title, onSelect, children }: {
  testId: string
  selected: boolean
  disabled?: boolean
  title: string
  onSelect: () => void
  children: React.ReactNode
}) {
  return (
    <button
      type="button"
      data-testid={testId}
      aria-pressed={selected}
      disabled={disabled}
      title={title}
      onClick={onSelect}
      style={{ ...placePillStyle(selected, disabled), cursor: disabled ? 'default' : 'pointer' }}
    >
      {children}
    </button>
  )
}

const AUTOMATED_OPEN_KEY = 'blerg.sidebar.automatedOpen'

function readAutomatedOpen(): boolean {
  try { return localStorage.getItem(AUTOMATED_OPEN_KEY) === '1' } catch { return false }
}

// Sessions started by a tool (an agent token), a cron or the operator key: one collapsible
// section, grouped by who started them, newest first, with a header that says how many are
// running so they are never out of sight. Collapsed by default; the choice is remembered.
function AutomatedSection({
  sessions,
  activeSessionId,
  isSidebar,
  daemons,
}: {
  sessions: SessionInfo[]
  activeSessionId: string | undefined
  isSidebar: boolean
  daemons: DaemonInfo[]
}) {
  const [expanded, setExpanded] = useState(readAutomatedOpen)
  function toggle() {
    setExpanded(e => {
      try { localStorage.setItem(AUTOMATED_OPEN_KEY, e ? '0' : '1') } catch { /* no storage */ }
      return !e
    })
  }
  const running = sessions.filter(s => ACTIVE_STATUSES.has(s.status)).length
  const recent = sessions.length - running
  const bySource = new Map<string, SessionInfo[]>()
  for (const s of [...sessions].sort((a, b) => (b.ended_at ?? b.started_at).localeCompare(a.ended_at ?? a.started_at))) {
    const key = startedByLabel(s)
    const list = bySource.get(key) ?? []
    list.push(s)
    bySource.set(key, list)
  }

  return (
    <div data-testid="automated-section" style={{ marginTop: 8, borderTop: '1px solid var(--basalt)', paddingTop: 10 }}>
      <button
        onClick={toggle}
        aria-expanded={expanded}
        style={{
          display: 'flex',
          alignItems: 'center',
          gap: 8,
          width: '100%',
          background: 'none',
          border: 'none',
          padding: '4px 0',
          cursor: 'pointer',
          marginBottom: expanded ? 10 : 0,
          textAlign: 'left',
        }}
      >
        <span style={{
          color: 'var(--stone)',
          fontSize: '0.65rem',
          display: 'inline-block',
          transform: expanded ? 'rotate(90deg)' : 'none',
          transition: 'transform 0.15s',
          flexShrink: 0,
        }}>▶</span>
        <span style={{
          color: 'var(--stone)',
          fontSize: '0.72rem',
          fontWeight: 700,
          letterSpacing: '0.1em',
          textTransform: 'uppercase',
          flex: 1,
        }}>
          Automated
        </span>
        <span data-testid="automated-summary" style={{ color: running > 0 ? 'var(--amber)' : 'var(--stone)', fontSize: '0.7rem' }}>
          {running > 0 ? `${running} running` : ''}{running > 0 && recent > 0 ? ' · ' : ''}{recent > 0 ? `${recent} recent` : ''}
        </span>
      </button>

      {expanded && (
        <div>
          {[...bySource.entries()].map(([source, list]) => (
            <div key={source} data-testid={`automated-group-${source}`} style={{ marginBottom: 10 }}>
              <div style={{
                color: 'var(--stone)',
                fontSize: '0.68rem',
                fontWeight: 700,
                letterSpacing: '0.08em',
                textTransform: 'uppercase',
                marginBottom: 5,
              }}>
                {source}
              </div>
              {list.map(session => (
                <SessionBranch
                  key={session.id}
                  session={session}
                  daemon={daemons.find(d => d.id === session.daemon_id)}
                  active={isSidebar && session.id === activeSessionId}
                />
              ))}
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

function HistorySection({
  sessions,
  activeSessionId,
  isSidebar,
  daemons,
}: {
  sessions: SessionInfo[]
  activeSessionId: string | undefined
  isSidebar: boolean
  daemons: DaemonInfo[]
}) {
  const [expanded, setExpanded] = useState(false)

  // One list in time order, most recently ended first: history is read as
  // "what happened lately", not per repo.
  const endedAt = (s: SessionInfo) => s.ended_at ?? s.archived_at ?? s.started_at
  const ordered = [...sessions].sort((a, b) => endedAt(b).localeCompare(endedAt(a)))

  return (
    <div style={{ marginTop: 8, borderTop: '1px solid var(--basalt)', paddingTop: 10 }}>
      <button
        onClick={() => setExpanded(e => !e)}
        style={{
          display: 'flex',
          alignItems: 'center',
          gap: 8,
          width: '100%',
          background: 'none',
          border: 'none',
          padding: '4px 0',
          cursor: 'pointer',
          marginBottom: expanded ? 10 : 0,
          textAlign: 'left',
        }}
      >
        <span style={{
          color: 'var(--stone)',
          fontSize: '0.65rem',
          display: 'inline-block',
          transform: expanded ? 'rotate(90deg)' : 'none',
          transition: 'transform 0.15s',
          flexShrink: 0,
        }}>▶</span>
        <span style={{
          color: 'var(--stone)',
          fontSize: '0.72rem',
          fontWeight: 700,
          letterSpacing: '0.1em',
          textTransform: 'uppercase',
          flex: 1,
        }}>
          History
        </span>
        <span style={{ color: 'var(--stone)', fontSize: '0.7rem' }}>
          {sessions.length} session{sessions.length !== 1 ? 's' : ''}
        </span>
      </button>

      {expanded && (
        <div style={{ opacity: 0.6 }}>
          {ordered.map(session => session.archived_at ? (
            <SessionCard key={session.id} session={session} daemon={daemons.find(d => d.id === session.daemon_id)} active={isSidebar && session.id === activeSessionId}/>
          ) : (
            <SessionBranch
              key={session.id}
              session={session}
              daemon={daemons.find(d => d.id === session.daemon_id)}
              active={isSidebar && session.id === activeSessionId}
            />
          ))}
        </div>
      )}
    </div>
  )
}

export default function SessionList({ onNewSession, variant = 'page' }: Props) {
  const { daemons, sessions, serverVersion, serverRelease, serverCommit, latestVersion, latestUrl } = useSessionStore()
  const build = formatBuild({ version: serverVersion, release: serverRelease, commit: serverCommit, latestVersion, latestUrl })
  const navigate = useNavigate()
  const location = useLocation()
  const routeSessionId = location.pathname.match(/^\/sessions\/([^/]+)/)?.[1]
  // The row clicked is highlighted at once: the switch to a session renders as a transition (the
  // router's), and until it commits the location still names the session being left.
  const [activeSessionId, setOptimisticActive] = useOptimistic(routeSessionId)
  const openSession = useCallback((id: string) => {
    startTransition(() => {
      setOptimisticActive(id)
      navigate(`/sessions/${id}`)
    })
  }, [navigate, setOptimisticActive])
  const isSidebar = variant === 'sidebar'
  const isCronsActive = location.pathname.startsWith('/crons')
  const isProposalsActive = location.pathname.startsWith('/proposals')
  const isInsightsActive = location.pathname.startsWith('/insights')

  // The cluster runtime: whether this server can run sessions as cluster pods,
  // and its session cap. Advisory only — any failure leaves it unconfigured,
  // which keeps the daemon install hint.
  const [cluster, setCluster] = useState<{ configured: boolean; max: number | null; active: number | null }>(
    { configured: false, max: null, active: null },
  )

  // The cluster's active sessions as the store sees them (live); the status
  // response's own count is only used when it carries one.
  const storeClusterActive = sessions.filter(s => s.runtime === 'cluster' && ACTIVE_STATUSES.has(s.status)).length

  useEffect(() => {
    let cancelled = false
    Promise.resolve()
      .then(() => apiFetch('/api/cluster/status'))
      .then(r => r.json())
      .then((s: { configured?: boolean; max_sessions?: number; active_sessions?: number }) => {
        if (cancelled) return
        setCluster({
          configured: !!s?.configured,
          max: typeof s?.max_sessions === 'number' ? s.max_sessions : null,
          active: typeof s?.active_sessions === 'number' ? s.active_sessions : null,
        })
      })
      .catch(() => { /* advisory only — keep the daemon install hint */ })
    return () => { cancelled = true }
    // Re-read when the number of cluster sessions changes, so the count in
    // the Cluster pill does not go stale.
  }, [storeClusterActive])

  // A cap saved on the Cluster page reaches the pill at once, not on the next reload.
  useEffect(() => {
    const onSaved = (e: Event) => {
      const max = (e as CustomEvent<{ max_sessions?: number }>).detail?.max_sessions
      if (typeof max === 'number') setCluster(c => ({ ...c, max }))
    }
    window.addEventListener('blerg:cluster-settings-saved', onSaved)
    return () => window.removeEventListener('blerg:cluster-settings-saved', onSaved)
  }, [])

  // Always the live count: the status response's own number is read the moment a session
  // changes, before the cluster has finished removing its pod, so it would go stale.
  const clusterActive = cluster.configured ? storeClusterActive : null

  // Workstation daemons only. Every cluster session pod connects as its own
  // ephemeral daemon with mode "runner"; current servers never send those, an
  // older one does, and they are not machines to show or pick.
  const workstations = daemons.filter(d => d.mode !== 'runner')
  const connectedWorkstations = workstations.filter(d => d.status === 'connected')
  // Places a session can run: the cluster (when configured) and each daemon.
  const places = (cluster.configured ? 1 : 0) + workstations.length

  // ── Focus state, seeded from localStorage on mount ─────────────────────────
  // The stored value is a workstation daemon id, or CLUSTER_FOCUS.
  const [focus, setFocusState] = useState<string | null>(getFocus)

  // A stored focus that points at nothing listed (a daemon that is gone, or
  // the cluster when it is not configured) means All.
  const effectiveFocus = focus === CLUSTER_FOCUS
    ? (cluster.configured ? CLUSTER_FOCUS : null)
    : focus && connectedWorkstations.some(d => d.id === focus)
      ? focus
      : null

  function handleSetFocus(id: string | null) {
    setFocusState(id)
    setFocus(id)
  }

  // Waiting for input: a session working in the background is not, whatever its count.
  const waitingCount = sessions.filter(s => !s.archived_at && displayState(s) === 'waiting').length

  // ── Search state ────────────────────────────────────────────────────────────
  const [query, setQuery] = useState('')
  const searchActive = query.trim().length > 0
  const q = query.trim().toLowerCase()

  // Sessions a tool, a cron or the operator key started are the person's to see, not the kind
  // they drive: they get a section of their own, active and ended alike, out of the repo
  // groups and out of History.
  const tree = searchTree(nestChildren(sessions), q)
  const listedIds = new Set(sessions.map(s => s.id))
  const automatedSessions = tree.top.filter(s => isAutomated(s, listedIds))
  const ownSessions = tree.top.filter(s => !isAutomated(s, listedIds))

  // Separate active from done sessions.
  const activeSessions = ownSessions.filter(s => ACTIVE_STATUSES.has(s.status))
  // Discarded children leave the active tree but remain directly accessible in History.
  const archivedChildren = sessions.filter(session => session.parent_session_id && session.archived_at && (!q || `${session.title} ${session.repo}`.toLowerCase().includes(q)))
  const doneSessions = [...ownSessions.filter(s => !ACTIVE_STATUSES.has(s.status)), ...archivedChildren]

  // When a query is active, filter active sessions by title or repo (case-insensitive).
  // An empty query leaves all sessions through so focus logic applies normally.
  const filteredActiveSessions = activeSessions

  // Starred active sessions — ignores focus, respects search.
  const starredActive = filteredActiveSessions
    .filter(s => s.starred)
    .sort(byNewestStarted)

  // A search spans every place to run; otherwise the focus narrows the list.
  const inFocus = (s: SessionInfo) => {
    if (searchActive || effectiveFocus === null) return true
    if (effectiveFocus === CLUSTER_FOCUS) return s.runtime === 'cluster'
    return s.daemon_id === effectiveFocus && s.runtime !== 'cluster'
  }

  // The active sessions, one card after the next across every runtime and repo (each card says
  // its own org/repo). Starred sessions are excluded — they render in the Starred section above.
  const listed = filteredActiveSessions
    .filter(s => !s.starred && inFocus(s))
    .sort(byNewestStarted)

  return (
    <OpenSessionContext.Provider value={openSession}>
    <Nesting.Provider value={{ tree, activeId: isSidebar ? activeSessionId : undefined, daemons, query: q }}>
    <div style={isSidebar ? {
      width: 300,
      flexShrink: 0,
      height: '100vh',
      overflowY: 'auto',
      background: 'var(--basalt)',
      color: 'var(--chalk)',
      borderRight: '1px solid var(--stone)',
      display: 'flex',
      flexDirection: 'column',
    } : {
      minHeight: '100vh',
      background: 'var(--basalt)',
      color: 'var(--chalk)',
    }}>
      {/* Top bar */}
      <div style={{
        display: 'flex',
        justifyContent: 'space-between',
        alignItems: 'center',
        padding: '12px 16px',
        borderBottom: '1px solid var(--stone)',
        position: 'sticky',
        top: 0,
        background: 'var(--basalt)',
        zIndex: 10,
      }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
          <span style={{
            fontFamily: 'var(--display)',
            color: 'var(--chalk)',
            fontWeight: 600,
            fontSize: '0.82rem',
            letterSpacing: '0.14em',
            textTransform: 'uppercase',
          }}>Sessions</span>
        </div>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
        <NotificationsButton />
        <button
          onClick={onNewSession}
          // About to open the sheet: warm what it opens on (lib/launchCache).
          onPointerEnter={prefetchLaunchData}
          onFocus={prefetchLaunchData}
          style={{
            background: 'color-mix(in srgb, var(--blaze) 22%, var(--basalt))',
            color: 'var(--amber)',
            border: '1px solid var(--blaze)',
            borderRadius: 6,
            padding: '6px 14px',
            fontWeight: 600,
            cursor: 'pointer',
            fontSize: '0.9rem',
            letterSpacing: '0.05em',
          }}
        >
          + New
        </button>
        </div>
      </div>

      {/* Where sessions run — ONE row of compact pills that show each place's status and are
          the focus filter too (All first, then the cluster when configured, then each workstation
          daemon; cluster session pods are never listed), with the usage meters beneath. */}
      <div
        data-testid="runtime-strip"
        style={{
          display: 'flex',
          flexDirection: 'column',
          gap: 6,
          padding: '8px 16px',
          borderBottom: '1px solid var(--stone)',
          background: 'var(--basalt)',
        }}
      >
        {!cluster.configured && workstations.length === 0 ? (
          <span style={{ color: 'var(--fog-dim)', fontSize: '0.8rem', lineHeight: '24px' }}>
            No daemon connected. Start it: ./daemon/install.sh status (from install/desktop), or see DAEMON.md.
          </span>
        ) : (
          <>
            <div
              data-testid="place-pills"
              role="group"
              aria-label="Where sessions run"
              style={{ display: 'flex', flexWrap: 'wrap', gap: 5, alignItems: 'center' }}
            >
              {places > 1 && (
                <PlacePill
                  testId="focus-pill-all"
                  selected={effectiveFocus === null}
                  title="Sessions everywhere"
                  onSelect={() => handleSetFocus(null)}
                >
                  All
                </PlacePill>
              )}
              {cluster.configured && (() => {
                const count = clusterActive !== null && cluster.max !== null
                  ? `${clusterActive}/${cluster.max}`
                  : clusterActive !== null ? `${clusterActive}` : ''
                const title = clusterActive !== null
                  ? `${clusterActive}${cluster.max !== null ? ` of ${cluster.max}` : ''} cluster sessions running`
                  : 'Cluster'
                const body = (
                  <>
                    <PlaceDot on />
                    Cluster{count && <span style={{ color: 'var(--fog-dim)', fontVariantNumeric: 'tabular-nums' }}> {count}</span>}
                  </>
                )
                // The only place: nothing to filter, so the pill opens the Cluster page.
                return places > 1 ? (
                  <PlacePill testId="focus-pill-cluster" selected={effectiveFocus === CLUSTER_FOCUS} title={`${title} · show only these`} onSelect={() => handleSetFocus(CLUSTER_FOCUS)}>
                    {body}
                  </PlacePill>
                ) : (
                  <Link to="/cluster" data-testid="focus-pill-cluster" title={`${title} · open the Cluster page`} style={{ ...placePillStyle(false), textDecoration: 'none' }}>
                    {body}
                  </Link>
                )
              })()}
              {workstations.map(d => {
                const connected = d.status === 'connected'
                const versionDiffers = !!d.version && !!serverVersion && d.version !== serverVersion
                const title = `${d.name} · ${d.mode}${d.version ? ` · ${d.version}` : ''}${connected ? '' : ' · not connected'}`
                const body = (
                  <>
                    <PlaceDot on={connected} />
                    {d.name}
                    {versionDiffers && <span style={{ color: 'var(--blaze)' }}>{d.version}</span>}
                  </>
                )
                return places > 1 ? (
                  <PlacePill key={d.id} testId={`focus-pill-${d.id}`} selected={effectiveFocus === d.id} disabled={!connected}
                    title={connected ? `${title} · show only these` : title} onSelect={() => handleSetFocus(d.id)}>
                    {body}
                  </PlacePill>
                ) : (
                  <span key={d.id} data-testid={`focus-pill-${d.id}`} title={title} style={placePillStyle(false, !connected)}>
                    {body}
                  </span>
                )
              })}
            </div>
            {/* The providers' usage limits, as the daemons report them (none: nothing drawn) */}
            <UsageMeters daemons={workstations} />
          </>
        )}
      </div>

      {/* Search input */}
      <div style={{
        padding: '6px 16px',
        borderBottom: '1px solid var(--basalt)',
        background: 'var(--basalt)',
      }}>
        <input
          data-testid="search-input"
          type="text"
          value={query}
          onChange={e => setQuery(e.target.value)}
          placeholder="Search sessions…"
          style={{
            width: '100%',
            background: 'var(--basalt)',
            border: `1px solid ${searchActive ? 'var(--fog-dim)' : 'var(--stone)'}`,
            borderRadius: 4,
            padding: '5px 8px',
            color: 'var(--chalk)',
            fontSize: '0.8rem',
            outline: 'none',
            boxSizing: 'border-box',
          }}
        />
      </div>

      {/* Orange waiting banner: sessions waiting for input */}
      {waitingCount > 0 && (
        <div data-testid="waiting-banner" style={{
          background: 'color-mix(in srgb, var(--blaze) 22%, var(--basalt))',
          borderBottom: '1px solid var(--blaze)',
          padding: '10px 16px',
          display: 'flex',
          alignItems: 'center',
          gap: 8,
          color: 'var(--amber)',
          fontSize: '0.9rem',
          fontWeight: 600,
          letterSpacing: '0.05em',
        }}>
          <span>⚡</span>
          <span style={{ flex: 1, minWidth: 0 }}>{waitingBannerText(waitingCount)}</span>
        </div>
      )}

      {/* Content */}
      <div style={{ padding: '12px 16px', flex: 1 }}>
        {/* ★ STARRED — pinned at top, always expanded, ignores focus, respects search */}
        {starredActive.length > 0 && (
          <div data-testid="starred-section" style={{ marginBottom: 20 }}>
            <div style={{
              display: 'flex',
              alignItems: 'center',
              gap: 8,
              padding: '4px 0',
              marginBottom: 8,
            }}>
              <span style={{
                color: 'var(--blaze)',
                fontSize: '0.75rem',
                fontWeight: 700,
                letterSpacing: '0.08em',
                textTransform: 'uppercase',
              }}>★ Starred</span>
            </div>
            {starredActive.map(session => (
              <SessionBranch
                key={session.id}
                session={session}
                daemon={daemons.find(d => d.id === session.daemon_id)}
                active={isSidebar && session.id === activeSessionId}
              />
            ))}
          </div>
        )}

        {listed.length > 0 && (
          <div data-testid="session-list" style={{ marginBottom: 12 }}>
            {listed.map(session => (
              <SessionBranch
                key={session.id}
                session={session}
                daemon={daemons.find(d => d.id === session.daemon_id)}
                active={isSidebar && session.id === activeSessionId}
              />
            ))}
          </div>
        )}

        {automatedSessions.length > 0 && (
          <AutomatedSection
            sessions={automatedSessions}
            activeSessionId={activeSessionId}
            isSidebar={isSidebar}
            daemons={daemons}
          />
        )}

        {/* History — all stopped/error sessions, collapsed by default.
            The tree search preserves a parent whose child matches in any section. */}
        {doneSessions.length > 0 && (
          <HistorySection
            sessions={doneSessions}
            activeSessionId={activeSessionId}
            isSidebar={isSidebar}
            daemons={daemons}
          />
        )}

        {/* Crons nav item (sidebar only) */}
        {isSidebar && (
          <div
            role="link"
            data-testid="crons-nav"
            onClick={() => navigate('/crons')}
            style={{
              display: 'flex',
              alignItems: 'center',
              gap: 10,
              padding: '10px 14px',
              marginTop: 4,
              borderRadius: 8,
              cursor: 'pointer',
              background: isCronsActive ? 'color-mix(in srgb, var(--blaze) 22%, var(--basalt))' : 'transparent',
              border: isCronsActive ? `1px solid var(--blaze)` : '1px solid transparent',
              color: isCronsActive ? 'var(--amber)' : 'var(--fog)',
              fontSize: '0.9rem',
              fontWeight: isCronsActive ? 700 : 500,
              letterSpacing: '0.05em',
            }}
          >
            <span style={{ fontSize: '1rem' }}>◷</span>
            <span>Crons</span>
          </div>
        )}

        {/* Proposals nav item (sidebar only), with the pending count */}
        {isSidebar && (
          <div
            role="link"
            data-testid="proposals-nav"
            onClick={() => navigate('/proposals')}
            style={{
              display: 'flex',
              alignItems: 'center',
              gap: 10,
              padding: '10px 14px',
              marginTop: 4,
              borderRadius: 8,
              cursor: 'pointer',
              background: isProposalsActive ? 'color-mix(in srgb, var(--blaze) 22%, var(--basalt))' : 'transparent',
              border: isProposalsActive ? `1px solid var(--blaze)` : '1px solid transparent',
              color: isProposalsActive ? 'var(--amber)' : 'var(--fog)',
              fontSize: '0.9rem',
              fontWeight: isProposalsActive ? 700 : 500,
              letterSpacing: '0.05em',
            }}
          >
            <span style={{ fontSize: '1rem' }}>✓</span>
            <span>Proposals</span>
            <ProposalsBadge style={{ marginLeft: 'auto' }} />
          </div>
        )}

        {/* Insights nav item (sidebar only): pod startup, sessions, tokens and estimated cost */}
        {isSidebar && (
          <div
            role="link"
            data-testid="insights-nav"
            onClick={() => navigate('/insights')}
            style={{
              display: 'flex',
              alignItems: 'center',
              gap: 10,
              padding: '10px 14px',
              marginTop: 4,
              borderRadius: 8,
              cursor: 'pointer',
              background: isInsightsActive ? 'color-mix(in srgb, var(--blaze) 22%, var(--basalt))' : 'transparent',
              border: isInsightsActive ? `1px solid var(--blaze)` : '1px solid transparent',
              color: isInsightsActive ? 'var(--amber)' : 'var(--fog)',
              fontSize: '0.9rem',
              fontWeight: isInsightsActive ? 700 : 500,
              letterSpacing: '0.05em',
            }}
          >
            <span style={{ fontSize: '1rem' }}>◔</span>
            <span>Insights</span>
          </div>
        )}

        {/* Settings (sidebar only): opens the Settings dialog over the page, no route of its own */}
        {isSidebar && (
          <button
            type="button"
            data-testid="settings-nav"
            onClick={() => setSettingsOpen(true)}
            style={{
              display: 'flex',
              alignItems: 'center',
              gap: 10,
              width: '100%',
              padding: '10px 14px',
              marginTop: 4,
              borderRadius: 8,
              cursor: 'pointer',
              background: 'transparent',
              border: '1px solid transparent',
              color: 'var(--fog)',
              fontFamily: 'inherit',
              fontSize: '0.9rem',
              fontWeight: 500,
              letterSpacing: '0.05em',
              textAlign: 'left',
            }}
          >
            <span style={{ fontSize: '1rem' }}>⚙</span>
            <span>Settings</span>
          </button>
        )}
      </div>

      {/* The build, at the foot of the list (the sidebar and the phone's list alike) */}
      {build.main && (
        <div
          data-testid="build-version"
          title={build.title || undefined}
          style={{
            marginTop: 'auto',
            // In the sidebar it stays in view as the list scrolls; on the phone it ends the page
            // (a fixed tab bar covers the bottom edge).
            ...(isSidebar ? { position: 'sticky' as const, bottom: 0 } : {}),
            padding: '8px 16px',
            borderTop: '1px solid var(--stone)',
            background: 'var(--basalt)',
            fontSize: '0.7rem',
            color: 'var(--fog-dim)',
            fontFamily: 'var(--mono)',
          }}
        >
          Blerg {build.main}
          {build.commit && <span style={{ opacity: 0.7 }}> · {build.commit}</span>}
          {build.update && (
            <>
              {' · '}
              <a
                data-testid="build-update"
                href={build.update.href}
                target="_blank"
                rel="noopener noreferrer"
                title={build.update.title}
                style={{ color: 'var(--accent)', textDecoration: 'none' }}
              >
                {build.update.label} ↗
              </a>
            </>
          )}
        </div>
      )}
    </div>
    </Nesting.Provider>
    </OpenSessionContext.Provider>
  )
}
