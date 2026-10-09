import { useState, useEffect } from 'react'
import { isAutomated, startedByLabel } from '../lib/startedBy'
import { useNavigate, useLocation, Link } from 'react-router-dom'
import { apiFetch } from '../apiFetch'
import type { SessionInfo, SessionStatus, DaemonInfo } from '../types'
import { useSessionStore } from '../hooks/useSessionStore'
import SessionCard from './SessionCard'
import { getFocus, setFocus } from '../lib/sidebarPrefs'
import { NotificationsButton } from '../App'
import { repoLabel } from '../lib/noRepo'
import { ProposalsBadge } from './Proposals'

interface Props {
  onNewSession?: () => void
  variant?: 'page' | 'sidebar'
}

const ACTIVE_STATUSES = new Set<SessionStatus>(['starting', 'running', 'idle', 'waiting'])

// The persisted focus for "the cluster" (every other value is a daemon id).
const CLUSTER_FOCUS = 'cluster'

function statusSortKey(status: SessionStatus): number {
  if (status === 'waiting') return 0
  if (status === 'running') return 1
  if (status === 'starting') return 2
  if (status === 'idle') return 3
  return 4
}

// Returns true if a repo's sessions have anything the user should act on.
function isActiveRepo(sessions: SessionInfo[]): boolean {
  return sessions.some(s => s.status === 'waiting' || s.status === 'running' || s.status === 'starting')
}

function RepoSection({
  repo,
  sessions,
  activeSessionId,
  isSidebar,
  daemons,
}: {
  repo: string
  sessions: SessionInfo[]
  activeSessionId: string | undefined
  isSidebar: boolean
  daemons: DaemonInfo[]
}) {
  const counts = sessions.reduce<Record<string, number>>((acc, s) => {
    if (ACTIVE_STATUSES.has(s.status)) acc[s.status] = (acc[s.status] ?? 0) + 1
    return acc
  }, {})

  const STATUS_COLORS: Record<string, string> = {
    waiting: 'var(--amber)',
    running: 'var(--lichen)',
    starting: 'var(--batch)',
    idle: 'var(--fog-dim)',
  }

  const rollupParts = (['waiting', 'running', 'starting', 'idle'] as const)
    .filter(s => counts[s] > 0)
    .map(s => ({ status: s, count: counts[s] }))

  const sorted = [...sessions].sort((a, b) => {
    const statusDiff = statusSortKey(a.status) - statusSortKey(b.status)
    if (statusDiff !== 0) return statusDiff
    // most-recent-first tiebreak
    return new Date(b.started_at).getTime() - new Date(a.started_at).getTime()
  })

  return (
    <div style={{ marginBottom: 12 }}>
      <div
        style={{
          display: 'flex',
          alignItems: 'center',
          gap: 8,
          width: '100%',
          padding: '4px 0',
          marginBottom: 8,
          textAlign: 'left',
        }}
      >
        <span style={{
          color: 'var(--fog-dim)',
          fontSize: '0.65rem',
          transition: 'transform 0.15s',
          display: 'inline-block',
          transform: 'rotate(90deg)',
          flexShrink: 0,
        }}>▶</span>
        <span style={{
          color: 'var(--fog)',
          fontSize: '0.75rem',
          fontWeight: 700,
          letterSpacing: '0.08em',
          textTransform: 'uppercase',
          flex: 1,
          overflow: 'hidden',
          textOverflow: 'ellipsis',
          whiteSpace: 'nowrap',
        }}>
          {repo}
        </span>
        <span style={{ display: 'flex', gap: 5, flexShrink: 0, alignItems: 'center' }}>
          {rollupParts.map(({ status, count }) => (
            <span key={status} style={{ color: STATUS_COLORS[status], fontSize: '0.7rem', fontWeight: 600 }}>
              {count} {status}
            </span>
          ))}
        </span>
      </div>

      {sorted.map(session => (
        <SessionCard
          key={session.id}
          session={session}
          daemon={daemons.find(d => d.id === session.daemon_id)}
          active={isSidebar && session.id === activeSessionId}
        />
      ))}
    </div>
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
                <SessionCard
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
  const endedAt = (s: SessionInfo) => s.ended_at ?? s.started_at
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
          {ordered.map(session => (
            <SessionCard
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
  const { daemons, sessions, serverVersion } = useSessionStore()
  const navigate = useNavigate()
  const location = useLocation()
  const activeSessionId = location.pathname.match(/^\/sessions\/([^/]+)/)?.[1]
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

  const waitingCount = sessions.filter(s => s.status === 'waiting').length

  // ── Search state ────────────────────────────────────────────────────────────
  const [query, setQuery] = useState('')
  const searchActive = query.trim().length > 0
  const q = query.trim().toLowerCase()

  // Sessions a tool, a cron or the operator key started are the person's to see, not the kind
  // they drive: they get a section of their own, active and ended alike, out of the repo
  // groups and out of History.
  const automatedSessions = sessions.filter(isAutomated)
  const ownSessions = sessions.filter(s => !isAutomated(s))

  // Separate active from done sessions.
  const activeSessions = ownSessions.filter(s => ACTIVE_STATUSES.has(s.status))
  const doneSessions = ownSessions.filter(s => !ACTIVE_STATUSES.has(s.status))

  // When a query is active, filter active sessions by title or repo (case-insensitive).
  // An empty query leaves all sessions through so focus logic applies normally.
  const filteredActiveSessions = searchActive
    ? activeSessions.filter(s =>
        s.title.toLowerCase().includes(q) || repoLabel(s.repo).toLowerCase().includes(q)
      )
    : activeSessions

  // Starred active sessions — ignores focus, respects search.
  const starredActive = filteredActiveSessions
    .filter(s => s.starred)
    .sort((a, b) => {
      const statusDiff = statusSortKey(a.status) - statusSortKey(b.status)
      if (statusDiff !== 0) return statusDiff
      return new Date(b.started_at).getTime() - new Date(a.started_at).getTime()
    })

  // A search spans every place to run; otherwise the focus narrows the list.
  const inFocus = (s: SessionInfo) => {
    if (searchActive || effectiveFocus === null) return true
    if (effectiveFocus === CLUSTER_FOCUS) return s.runtime === 'cluster'
    return s.daemon_id === effectiveFocus && s.runtime !== 'cluster'
  }

  // Group the active sessions by repository, across every runtime. Starred
  // sessions are excluded — they render in the Starred section above. Where a
  // session runs is on its card (the runtime chip), not a grouping.
  const repoMap = new Map<string, SessionInfo[]>()
  for (const s of filteredActiveSessions) {
    if (s.starred || !inFocus(s)) continue
    // Every no-repo session groups under "No repository", not under its own
    // scratch folder name.
    const key = repoLabel(s.repo)
    const list = repoMap.get(key) ?? []
    list.push(s)
    repoMap.set(key, list)
  }

  // Sort repos: active ones first (have waiting/running sessions), then by name.
  const repoGroups = [...repoMap.entries()].sort(([aName, aSessions], [bName, bSessions]) => {
    const aActive = isActiveRepo(aSessions) ? 0 : 1
    const bActive = isActiveRepo(bSessions) ? 0 : 1
    if (aActive !== bActive) return aActive - bActive
    return aName.localeCompare(bName)
  })

  return (
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
        <button
          onClick={onNewSession}
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

      {/* Where sessions run — one compact, wrapping summary: the cluster (when
          configured) and each workstation daemon. Cluster session pods are
          never listed. */}
      <div
        data-testid="runtime-strip"
        style={{
          display: 'flex',
          flexWrap: 'wrap',
          gap: 6,
          padding: '8px 16px',
          borderBottom: '1px solid var(--stone)',
          background: 'var(--basalt)',
          alignItems: 'center',
        }}
      >
        {!cluster.configured && workstations.length === 0 ? (
          <span style={{ color: 'var(--fog-dim)', fontSize: '0.8rem', lineHeight: '24px' }}>
            No daemon connected. Start it: ./daemon/install.sh status (from install/desktop), or see DAEMON.md.
          </span>
        ) : (
          <>
            <span style={{ width: '100%', color: 'var(--fog-dim)', fontSize: '0.65rem', fontWeight: 700, letterSpacing: '0.1em', textTransform: 'uppercase' }}>
              Where sessions run
            </span>
            {cluster.configured && (
              <Link
                to="/cluster"
                data-testid="cluster-pill"
                title="Open the Cluster page"
                style={{
                  display: 'flex',
                  alignItems: 'center',
                  gap: 6,
                  background: 'color-mix(in srgb, var(--lichen) 22%, var(--basalt))',
                  border: '1px solid color-mix(in srgb, var(--lichen) 35%, var(--scree))',
                  borderRadius: 20,
                  padding: '3px 10px',
                  textDecoration: 'none',
                }}
              >
                <span style={{ width: 7, height: 7, borderRadius: '50%', background: 'var(--lichen)', display: 'inline-block', flexShrink: 0 }} />
                <span style={{ fontSize: '0.8rem', color: 'var(--chalk)' }}>
                  {clusterActive !== null && cluster.max !== null
                    ? `Cluster · ${clusterActive}/${cluster.max} running`
                    : clusterActive !== null
                      ? `Cluster · ${clusterActive} running`
                      : 'Cluster'}
                </span>
              </Link>
            )}
            {workstations.map(d => {
              const connected = d.status === 'connected'
              return (
                <div key={d.id} data-testid={`daemon-pill-${d.id}`} style={{
                  display: 'flex',
                  alignItems: 'center',
                  gap: 6,
                  background: connected ? 'color-mix(in srgb, var(--lichen) 22%, var(--basalt))' : 'var(--basalt)',
                  border: `1px solid ${connected ? 'color-mix(in srgb, var(--lichen) 35%, var(--scree))' : 'var(--stone)'}`,
                  borderRadius: 20,
                  padding: '3px 10px',
                }}>
                  <span style={{
                    width: 7,
                    height: 7,
                    borderRadius: '50%',
                    background: connected ? 'var(--lichen)' : 'var(--fog-dim)',
                    display: 'inline-block',
                    flexShrink: 0,
                  }} />
                  <span style={{ fontSize: '0.8rem', color: connected ? 'var(--chalk)' : 'var(--fog)' }}>
                    {d.name}
                  </span>
                  <span style={{ fontSize: '0.7rem', color: 'var(--fog-dim)' }}>
                    {d.mode}
                  </span>
                  {d.version && (
                    <span style={{ fontSize: '0.7rem', color: d.version && serverVersion && d.version !== serverVersion ? 'var(--blaze)' : 'var(--fog-dim)' }}>
                      {d.version}
                    </span>
                  )}
                </div>
              )
            })}
          </>
        )}
      </div>
      <div style={{ padding: '2px 16px 6px', fontSize: '0.7rem', color: 'var(--fog-dim)', display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 8 }}>
        <span>server {serverVersion || "dev"}</span>
        <NotificationsButton />
      </div>

      {/* Focus pill row — only when there is more than one place to run */}
      {places > 1 && (
        <div
          data-testid="focus-pill-row"
          style={{
            display: 'flex',
            gap: 6,
            padding: '6px 16px',
            borderBottom: '1px solid var(--basalt)',
            background: 'var(--basalt)',
            flexWrap: 'wrap',
            alignItems: 'center',
          }}
        >
          <button
            data-testid="focus-pill-all"
            onClick={() => handleSetFocus(null)}
            style={{
              background: effectiveFocus === null ? 'color-mix(in srgb, var(--blaze) 22%, var(--basalt))' : 'none',
              color: effectiveFocus === null ? 'var(--amber)' : 'var(--fog-dim)',
              border: effectiveFocus === null ? '1px solid var(--blaze)' : '1px solid var(--stone)',
              borderRadius: 20,
              padding: '2px 10px',
              fontSize: '0.72rem',
              fontWeight: 600,
              cursor: 'pointer',
            }}
          >
            All
          </button>

          {[
            ...(cluster.configured ? [{ key: CLUSTER_FOCUS, label: 'Cluster' }] : []),
            ...connectedWorkstations.map(d => ({ key: d.id, label: d.name })),
          ].map(({ key, label }) => (
            <button
              key={key}
              data-testid={`focus-pill-${key}`}
              onClick={() => handleSetFocus(key)}
              style={{
                background: effectiveFocus === key ? 'color-mix(in srgb, var(--lichen) 22%, var(--basalt))' : 'none',
                color: effectiveFocus === key ? 'var(--lichen)' : 'var(--fog-dim)',
                border: effectiveFocus === key ? '1px solid color-mix(in srgb, var(--lichen) 35%, var(--scree))' : '1px solid var(--stone)',
                borderRadius: 20,
                padding: '2px 10px',
                fontSize: '0.72rem',
                fontWeight: 600,
                cursor: 'pointer',
              }}
            >
              {label}
            </button>
          ))}
        </div>
      )}

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

      {/* Orange waiting banner */}
      {waitingCount > 0 && (
        <div style={{
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
          <span>{waitingCount} session{waitingCount !== 1 ? 's' : ''} waiting for input</span>
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
              <SessionCard
                key={session.id}
                session={session}
                daemon={daemons.find(d => d.id === session.daemon_id)}
                active={isSidebar && session.id === activeSessionId}
              />
            ))}
          </div>
        )}

        {repoGroups.map(([repo, repoSessions]) => (
          <RepoSection
            key={repo}
            repo={repo}
            sessions={repoSessions}
            activeSessionId={activeSessionId}
            isSidebar={isSidebar}
            daemons={daemons}
          />
        ))}

        {automatedSessions.length > 0 && (
          <AutomatedSection
            sessions={automatedSessions}
            activeSessionId={activeSessionId}
            isSidebar={isSidebar}
            daemons={daemons}
          />
        )}

        {/* History — all stopped/error sessions, collapsed by default.
            doneSessions are intentionally passed unfiltered: search is scoped
            to the active session tree and History is excluded from search. */}
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
      </div>
    </div>
  )
}
