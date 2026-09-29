import { useState, useEffect } from 'react'
import { useNavigate, useLocation } from 'react-router-dom'
import { apiFetch } from '../apiFetch'
import type { SessionInfo, SessionStatus, DaemonInfo } from '../types'
import { useSessionStore } from '../hooks/useSessionStore'
import SessionCard from './SessionCard'
import { getFocus, setFocus, getCollapsed, setCollapsed } from '../lib/sidebarPrefs'
import ChatBubble from './ChatBubble'
import { NotificationsButton } from '../App'
import { repoLabel } from '../lib/noRepo'

interface Props {
  onNewSession?: () => void
  variant?: 'page' | 'sidebar'
}

const ACTIVE_STATUSES = new Set<SessionStatus>(['starting', 'running', 'idle', 'waiting'])

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

// DaemonSection is CONTROLLED: expanded + onToggle come from SessionList state.
function DaemonSection({
  daemon,
  repos,
  activeSessionId,
  isSidebar,
  daemons,
  expanded,
  onToggle,
}: {
  daemon: DaemonInfo
  repos: [string, SessionInfo[]][]
  activeSessionId: string | undefined
  isSidebar: boolean
  daemons: DaemonInfo[]
  expanded: boolean
  onToggle: () => void
}) {
  return (
    <div style={{ marginBottom: 20 }}>
      <button
        onClick={onToggle}
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
          fontSize: '0.6rem',
          display: 'inline-block',
          transform: expanded ? 'rotate(90deg)' : 'none',
          transition: 'transform 0.15s',
          flexShrink: 0,
        }}>▶</span>
        <span style={{
          width: 7,
          height: 7,
          borderRadius: '50%',
          background: daemon.status === 'connected' ? 'var(--lichen)' : 'var(--fog-dim)',
          display: 'inline-block',
          flexShrink: 0,
        }} />
        <span style={{
          color: 'var(--fog-dim)',
          fontSize: '0.7rem',
          fontWeight: 700,
          letterSpacing: '0.1em',
          textTransform: 'uppercase',
          flex: 1,
          overflow: 'hidden',
          textOverflow: 'ellipsis',
          whiteSpace: 'nowrap',
        }}>
          {daemon.name}
        </span>
      </button>

      {expanded && repos.map(([repo, repoSessions]) => (
        <RepoSection
          key={repo}
          repo={repo}
          sessions={repoSessions}
          activeSessionId={activeSessionId}
          isSidebar={isSidebar}
          daemons={daemons}
        />
      ))}
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

  // Group by repo, sorted by most recently ended.
  const byRepo = new Map<string, SessionInfo[]>()
  for (const s of sessions) {
    const key = repoLabel(s.repo)
    const list = byRepo.get(key) ?? []
    list.push(s)
    byRepo.set(key, list)
  }

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
          {[...byRepo.entries()].map(([repo, repoSessions]) => (
            <div key={repo} style={{ marginBottom: 10 }}>
              <div style={{
                color: 'var(--stone)',
                fontSize: '0.68rem',
                fontWeight: 700,
                letterSpacing: '0.08em',
                textTransform: 'uppercase',
                marginBottom: 5,
              }}>
                {repo}
              </div>
              {repoSessions.map(session => (
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

export default function SessionList({ onNewSession, variant = 'page' }: Props) {
  const { daemons, sessions, serverVersion } = useSessionStore()
  const navigate = useNavigate()
  const location = useLocation()
  const activeSessionId = location.pathname.match(/^\/sessions\/([^/]+)/)?.[1]
  const isSidebar = variant === 'sidebar'
  const isPreviewActive = location.pathname === '/preview'

  // Whether this server can run sessions as cluster pods. Only used to word
  // the empty daemon strip: with a cluster configured, "no daemon" is a
  // normal state, not something to go fix. Any failure leaves it false, which
  // keeps the original install hint.
  const [clusterConfigured, setClusterConfigured] = useState(false)

  useEffect(() => {
    let cancelled = false
    Promise.resolve()
      .then(() => apiFetch('/api/cluster/status'))
      .then(r => r.json())
      .then((s: { configured?: boolean }) => {
        if (!cancelled) setClusterConfigured(!!s?.configured)
      })
      .catch(() => { /* advisory only — keep the daemon install hint */ })
    return () => { cancelled = true }
  }, [])

  // ── Focus + collapse state, seeded from localStorage on mount ──────────────
  const [focusedDaemonId, setFocusedDaemonIdState] = useState<string | null>(getFocus)
  const [collapsedMap, setCollapsedMapState] = useState<Record<string, boolean>>(getCollapsed)

  // Connected daemons — used for focus-pill rendering and effective-focus check.
  const connectedDaemons = daemons.filter(d => d.status === 'connected')
  const connectedIds = new Set(connectedDaemons.map(d => d.id))

  // If the stored focus points at a non-connected daemon, treat as All.
  const effectiveFocusId = focusedDaemonId && connectedIds.has(focusedDaemonId)
    ? focusedDaemonId
    : null

  function handleSetFocus(id: string | null) {
    setFocusedDaemonIdState(id)
    setFocus(id)
  }

  function handleToggle(daemonId: string) {
    const next = { ...collapsedMap, [daemonId]: !(collapsedMap[daemonId] ?? false) }
    setCollapsedMapState(next)
    setCollapsed(next)
  }

  function handleCollapseAll() {
    const next: Record<string, boolean> = {}
    for (const d of daemons) next[d.id] = true
    setCollapsedMapState(next)
    setCollapsed(next)
  }

  const waitingCount = sessions.filter(s => s.status === 'waiting').length

  // ── Search state ────────────────────────────────────────────────────────────
  const [query, setQuery] = useState('')
  const searchActive = query.trim().length > 0
  const q = query.trim().toLowerCase()

  // Separate active from done sessions.
  const activeSessions = sessions.filter(s => ACTIVE_STATUSES.has(s.status))
  const doneSessions = sessions.filter(s => !ACTIVE_STATUSES.has(s.status))

  // When a query is active, filter active sessions by title or repo (case-insensitive).
  // An empty query leaves all sessions through so focus logic applies normally.
  const filteredActiveSessions = searchActive
    ? activeSessions.filter(s =>
        s.title.toLowerCase().includes(q) || repoLabel(s.repo).toLowerCase().includes(q)
      )
    : activeSessions

  // Starred active sessions — ignores daemon focus, respects search.
  const starredActive = filteredActiveSessions
    .filter(s => s.starred)
    .sort((a, b) => {
      const statusDiff = statusSortKey(a.status) - statusSortKey(b.status)
      if (statusDiff !== 0) return statusDiff
      return new Date(b.started_at).getTime() - new Date(a.started_at).getTime()
    })

  // Group filtered active sessions by repo within each daemon.
  // Starred sessions are excluded here — they render in the Starred section above.
  const daemonRepoMap = new Map<string, Map<string, SessionInfo[]>>()
  for (const d of daemons) {
    daemonRepoMap.set(d.id, new Map())
  }
  for (const s of filteredActiveSessions) {
    if (s.starred) continue
    if (!daemonRepoMap.has(s.daemon_id)) continue
    const repoMap = daemonRepoMap.get(s.daemon_id)!
    // Every no-repo session groups under "No repository", not under its own
    // scratch folder name.
    const key = repoLabel(s.repo)
    const list = repoMap.get(key) ?? []
    list.push(s)
    repoMap.set(key, list)
  }

  // Daemons to render — bypass focus when a search query is active so results
  // span all daemons; otherwise apply effective focus as Task 7 left it.
  const visibleDaemons = searchActive
    ? daemons
    : effectiveFocusId
      ? daemons.filter(d => d.id === effectiveFocusId)
      : daemons

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
          <ChatBubble />
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

      {/* Daemon status strip */}
      <div style={{
        display: 'flex',
        gap: 8,
        padding: '8px 16px',
        borderBottom: '1px solid var(--stone)',
        overflowX: 'auto',
        flexWrap: 'nowrap',
        background: 'var(--basalt)',
        scrollbarWidth: 'thin',
        scrollbarColor: 'var(--stone) var(--basalt)',
      }}>
        {daemons.length === 0 ? (
          <span style={{ color: 'var(--fog-dim)', fontSize: '0.8rem', lineHeight: '24px' }}>
            {clusterConfigured
              ? 'No workstation daemon connected — sessions will run as cluster pods.'
              : 'No daemon connected. Start it: ./daemon/install.sh status (from install/desktop), or see DAEMON.md.'}
          </span>
        ) : daemons.map(d => {
          const connected = d.status === 'connected'
          return (
            <div key={d.id} style={{
              display: 'flex',
              alignItems: 'center',
              gap: 6,
              background: connected ? 'color-mix(in srgb, var(--lichen) 22%, var(--basalt))' : 'var(--basalt)',
              border: `1px solid ${connected ? 'color-mix(in srgb, var(--lichen) 35%, var(--scree))' : 'var(--stone)'}`,
              borderRadius: 20,
              padding: '3px 10px',
              flexShrink: 0,
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
                  {d.version ?? ""}
                </span>
              )}
            </div>
          )
        })}
      </div>
      <div style={{ padding: '2px 16px 6px', fontSize: '0.7rem', color: 'var(--fog-dim)', display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 8 }}>
        <span>server {serverVersion || "dev"}</span>
        <NotificationsButton />
      </div>

      {/* Focus pill row — only when 2+ daemons */}
      {daemons.length > 1 && (
        <div
          data-testid="focus-pill-row"
          style={{
            display: 'flex',
            gap: 6,
            padding: '6px 16px',
            borderBottom: '1px solid var(--basalt)',
            background: 'var(--basalt)',
            overflowX: 'auto',
            flexWrap: 'nowrap',
            alignItems: 'center',
            scrollbarWidth: 'thin',
            scrollbarColor: 'var(--stone) var(--basalt)',
          }}
        >
          {/* All pill */}
          <button
            data-testid="focus-pill-all"
            onClick={() => handleSetFocus(null)}
            style={{
              background: effectiveFocusId === null ? 'color-mix(in srgb, var(--blaze) 22%, var(--basalt))' : 'none',
              color: effectiveFocusId === null ? 'var(--amber)' : 'var(--fog-dim)',
              border: effectiveFocusId === null ? '1px solid var(--blaze)' : '1px solid var(--stone)',
              borderRadius: 20,
              padding: '2px 10px',
              fontSize: '0.72rem',
              fontWeight: 600,
              cursor: 'pointer',
              flexShrink: 0,
            }}
          >
            All
          </button>

          {/* One pill per connected daemon */}
          {connectedDaemons.map(d => (
            <button
              key={d.id}
              data-testid={`focus-pill-${d.id}`}
              onClick={() => handleSetFocus(d.id)}
              style={{
                background: effectiveFocusId === d.id ? 'color-mix(in srgb, var(--lichen) 22%, var(--basalt))' : 'none',
                color: effectiveFocusId === d.id ? 'var(--lichen)' : 'var(--fog-dim)',
                border: effectiveFocusId === d.id ? '1px solid color-mix(in srgb, var(--lichen) 35%, var(--scree))' : '1px solid var(--stone)',
                borderRadius: 20,
                padding: '2px 10px',
                fontSize: '0.72rem',
                fontWeight: 600,
                cursor: 'pointer',
                flexShrink: 0,
              }}
            >
              {d.name}
            </button>
          ))}

          {/* Spacer pushes collapse-all to the right */}
          <span style={{ flex: 1 }} />

          {/* Collapse-all button */}
          <button
            data-testid="collapse-all"
            onClick={handleCollapseAll}
            title="Collapse all daemon sections"
            style={{
              background: 'none',
              color: 'var(--fog-dim)',
              border: '1px solid var(--stone)',
              borderRadius: 4,
              padding: '2px 6px',
              fontSize: '0.8rem',
              cursor: 'pointer',
              flexShrink: 0,
            }}
          >
            ⊟
          </button>
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

        {visibleDaemons.map(daemon => {
          const repoMap = daemonRepoMap.get(daemon.id) ?? new Map()
          if (repoMap.size === 0) return null

          // Sort repos: active ones first (have waiting/running sessions), then by name.
          const repos = [...repoMap.entries()].sort(([aName, aSessions], [bName, bSessions]) => {
            const aActive = isActiveRepo(aSessions) ? 0 : 1
            const bActive = isActiveRepo(bSessions) ? 0 : 1
            if (aActive !== bActive) return aActive - bActive
            return aName.localeCompare(bName)
          })

          if (daemons.length > 1) {
            return (
              <DaemonSection
                key={daemon.id}
                daemon={daemon}
                repos={repos}
                activeSessionId={activeSessionId}
                isSidebar={isSidebar}
                daemons={daemons}
                expanded={searchActive || !(collapsedMap[daemon.id] ?? false)}
                onToggle={() => handleToggle(daemon.id)}
              />
            )
          }

          return (
            <div key={daemon.id} style={{ marginBottom: 20 }}>
              {repos.map(([repo, repoSessions]) => (
                <RepoSection
                  key={repo}
                  repo={repo}
                  sessions={repoSessions}
                  activeSessionId={activeSessionId}
                  isSidebar={isSidebar}
                  daemons={daemons}
                />
              ))}
            </div>
          )
        })}

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

        {/* Preview nav item (sidebar only) */}
        {isSidebar && (
          <div
            onClick={() => navigate('/preview')}
            style={{
              display: 'flex',
              alignItems: 'center',
              gap: 10,
              padding: '10px 14px',
              marginTop: 8,
              borderRadius: 8,
              cursor: 'pointer',
              background: isPreviewActive ? 'color-mix(in srgb, var(--blaze) 22%, var(--basalt))' : 'transparent',
              border: isPreviewActive ? `1px solid var(--blaze)` : '1px solid transparent',
              color: isPreviewActive ? 'var(--amber)' : 'var(--fog)',
              fontSize: '0.9rem',
              fontWeight: isPreviewActive ? 700 : 500,
              letterSpacing: '0.05em',
            }}
          >
            <span style={{ fontSize: '1rem' }}>⊞</span>
            <span>Preview</span>
          </div>
        )}
      </div>
    </div>
  )
}
