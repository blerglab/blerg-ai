import { selectSessionSections, useSessionListFilters, useSessionListSessions } from '../lib/sessionSelection'
import RuntimeIcon from './RuntimeIcon'
import { Hover } from '@blerglab/chat'
import { memo, useContext } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'
import { useSessionStore } from '../hooks/useSessionStore'
import { useSessionAggregate } from '../lib/sessionAggregate'
import { type SessionTree } from '../lib/sessionTree'
import { OpenSessionContext } from '../lib/openSession'
import { repoLabel } from '../lib/noRepo'
import { useChildrenExpanded, setChildrenExpanded } from '../lib/sidebarPrefs'
import { LiveSessionCard as SessionCard, SessionStatusGlyph } from './SessionCard'
import { projectTint, sessionMonogram } from '../lib/sessionTiles'
import type { DaemonInfo, SessionInfo } from '../types'
import './SessionTiles.css'

export default function SessionTiles() {
  const sessions = useSessionListSessions()
  const daemons = useSessionStore(s => s.daemons)
  const active = useLocation().pathname.split('/')[2]
  const { query, focus, clusterConfigured } = useSessionListFilters()
  const effectiveFocus = focus === 'cluster' ? (clusterConfigured ? focus : null) : daemons.some(d => d.id === focus && d.status === 'connected') ? focus : null
  const selection = selectSessionSections(sessions, query, effectiveFocus, active)
  const tree = selection.tree
  return <div className="session-tiles" aria-label="Sessions" role="group" onKeyDown={event => {
    if (!['ArrowUp', 'ArrowDown', 'Home', 'End'].includes(event.key)) return
    const buttons = [...event.currentTarget.querySelectorAll<HTMLButtonElement>('.session-tile')]
    const current = buttons.indexOf(document.activeElement as HTMLButtonElement)
    if (current < 0) return
    const next = event.key === 'Home' ? 0 : event.key === 'End' ? buttons.length - 1 : (current + (event.key === 'ArrowDown' ? 1 : -1) + buttons.length) % buttons.length
    event.preventDefault(); buttons[next]?.focus()
  }}>
    {selection.main.map(session => {
      const descendants = (id: string): string[] => (tree.childrenOf.get(id) ?? []).flatMap(child => [child.id, id, ...descendants(child.id)])
      return <SessionTile key={session.id} session={session} tree={tree} topology={JSON.stringify(descendants(session.id))} daemons={daemons} active={active} />
    })}
  </div>
}

// A streaming child may change this parent's aggregate glyph, but leaves sibling tiles alone.
const SessionTile = memo(function SessionTile({ session: snapshot, tree, daemons, active }: { session: SessionInfo; tree: SessionTree; topology: string; daemons: DaemonInfo[]; active?: string }) {
  const session = useSessionStore(s => s.sessions.find(current => current.id === snapshot.id)) ?? snapshot
  const aggregate = useSessionAggregate(session)
  const navigate = useNavigate()
  const open = useContext(OpenSessionContext)
  const below = (id: string): SessionInfo[] => (tree.childrenOf.get(id) ?? []).flatMap(s => [s, ...below(s.id)])
  const children = below(session.id)
  const title = session.title || repoLabel(session.repo)
  const current = active === session.id
  const containsActive = children.some(child => child.id === active)
  return <Hover target={{ kind: 'run', title, facts: {} }} content={
    <SessionTilePreview session={session} tree={tree} daemons={daemons} active={active} />
  }>
    {/* button-guard-ignore: the fill is the project tint from projectTint(); .session-tile (SessionTiles.css) draws the border. */}
    <button type="button" className={`session-tile${current || containsActive ? ' active' : ''}`} data-session-id={session.id}
    aria-label={`${title}, ${aggregate.label}${session.unread ? ', unread' : ''}${children.length ? `, ${children.length} started ${children.length === 1 ? 'session' : 'sessions'}` : ''}`} aria-current={current ? 'page' : undefined}
    style={{ background: projectTint(session) }} onClick={() => open ? open(session.id) : navigate(`/sessions/${session.id}`)}>
    <RuntimeIcon runtime={session.runtime} machine={daemons.find(d => d.id === session.daemon_id)?.name} /><span aria-hidden="true">{sessionMonogram(title)}</span>
    <span className="session-tile-state"><SessionStatusGlyph session={session} /></span>
    {session.unread && <span className="session-tile-unread" aria-hidden="true" />}
    {children.length > 0 && <span className="session-tile-children" aria-hidden="true">↳{children.length}</span>}
  </button></Hover>
}, (a, b) => a.session === b.session && a.daemons === b.daemons && a.active === b.active && a.topology === b.topology)

/** Keep the same cards and nesting controls as the expanded list, inside the shared popup. */
function SessionTilePreview({ session, tree, daemons, active, nested = false }: { session: SessionInfo; tree: SessionTree; daemons: DaemonInfo[]; active?: string; nested?: boolean }) {
  const shown = useChildrenExpanded(session.id)
  const children = tree.childrenOf.get(session.id) ?? []
  return <div data-session-preview>
    <SessionCard session={session} daemon={daemons.find(d => d.id === session.daemon_id)} active={session.id === active} nested={nested}
      childrenControl={children.length ? { total: children.length, shown, toggle: () => setChildrenExpanded(session.id, !shown) } : null} />
    {shown && children.length > 0 && <div className="session-tile-preview-children">{children.map(child =>
      <SessionTilePreview key={child.id} session={child} tree={tree} daemons={daemons} active={active} nested />,
    )}</div>}
  </div>
}
