import { useQuestionItems, questionItemCount } from '../hooks/useQuestionItems'
import { CompactInbox } from './Inbox'
import { useInboxUnread } from '../hooks/useInbox'
import { ChannelPanelsContext } from '../lib/channelPanels'
import DockHeading from './DockHeading'
import QuestionQueue from './QuestionQueue'
import { useContext, useState, useEffect, useId, useRef, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import SessionGroups from './SessionGroups'
import { ToolTabBar } from './SessionTabBar'
import { dockTools, useDockTabs, pendingDockTool, clearDockRequest } from '../hooks/useDockTabs'
import { Link } from 'react-router-dom'
import { useSessionStore } from '../hooks/useSessionStore'
import type { SessionInfo } from '../types'
import ExplorerTreeRow from './ExplorerTreeRow'
import { explorerTreeKey } from '../lib/explorerTreeKey'
import { FileIcon } from '@blerglab/chat'
import { SessionStatusGlyph } from './SessionCard'
import RuntimeIcon from './RuntimeIcon'
import ChatRoleIcon from './ChatRoleIcon'
import WorkerKindIcon from './WorkerKindIcon'
import { sessionNoun } from '../lib/sessionPresentation'
import { rootSessionId } from '../lib/sessionTree'
import { useAttentionItems } from '../hooks/useSessionAttention'
import { sessionRole } from '../lib/sessionRole'
import { sessionRepos } from '../lib/sessionRepos'
import DockRepositories from './DockRepositories'
import { useChannelCategories, categoryDragType, channelDragType } from '../hooks/useChannelCategories'
import CategoryEditor from './CategoryEditor'
import NewChannel from './NewChannel'
import ChannelEmojiEditor from './ChannelEmojiEditor'
import DockSessionGroup from './DockSessionGroup'
import DockRowMenu, { type DockRowAction } from './DockRowMenu'
import { useDockRowMenu } from '../hooks/useDockRowMenu'
import './SessionDockWindows.css'

export default function SessionDockWindows({ sessionId, mobile, files, onOpen, onHistory, selectedSessionId }: { selectedSessionId?: string; sessionId: string; mobile: boolean; files: ReactNode; onOpen?(session: SessionInfo, keep?: boolean): void; onHistory?(): void }) {
  const [initialTool] = useState(() => pendingDockTool(sessionId))
  useEffect(() => { clearDockRequest(sessionId) }, [sessionId])
  const { tabs, select } = useDockTabs(mobile, initialTool, sessionId)
  const unread = useInboxUnread()
  const questionCount = questionItemCount(useQuestionItems(sessionId))
  const [creating, setCreating] = useState(false)
  const newChannelButton = useRef<HTMLButtonElement>(null)
  const sessions = useSessionStore(s => s.sessions)
  const main = sessions.find(s => s.id === (rootSessionId(sessions, sessionId) ?? sessionId))
  const [selection, setSelection] = useState({ route: sessionId, id: sessionId })
  const selectedSession = selectedSessionId ?? (selection.route === sessionId ? selection.id : sessionId)
  const setSelectedSession = (id: string) => setSelection({ route: sessionId, id })
  return <div className="session-dock-windows">
    <SessionGroups testId="dock-groups" tabs={tabs} alwaysShowBar bar={group => <ToolTabBar counts={{ sessions: sessions.filter(s => s.id === main?.id || sessionRole(s) === 'channel' && rootSessionId(sessions, s.id) === main?.id).length, inbox: unread, questions: questionCount }} tabs={tabs} group={group} mobile={mobile} onSelect={select} />} />
    {dockTools.map(tool => createPortal(<div className="session-dock-content">{tool === 'inbox' ? <CompactInbox /> : tool === 'questions' ? <QuestionQueue sessionId={sessionId} /> : tool === 'files' ? files : tool === 'repositories' ? <DockRepositories sessionId={sessionId} active={tabs.groups.some(g => g.active === tool)} onOpen={(session, keep) => { setSelectedSession(session.id); if (keep) onOpen?.(session, true); else onOpen?.(session) }} /> : <DockSessions heading={<DockHeading title={main?.title ? `${main.title} channels` : 'Channels'} action="New channel" buttonRef={newChannelButton} onAdd={main && sessionRole(main) === 'workspace' ? () => { select('sessions'); setCreating(true) } : undefined} />} creating={creating} closeChannelForm={(restoreFocus = true) => { setCreating(false); if (restoreFocus) requestAnimationFrame(() => newChannelButton.current?.focus()) }} mobile={mobile} sessionId={sessionId} selected={selectedSession} onSelect={setSelectedSession} onOpen={onOpen} onHistory={onHistory} />}</div>, tabs.targetFor(tool), tool))}
  </div>
}
function DockSessions({ heading, creating, closeChannelForm, mobile, sessionId, selected, onSelect, onOpen, onHistory }: { heading: ReactNode; creating: boolean; closeChannelForm(restoreFocus?: boolean): void; mobile: boolean; sessionId: string; selected: string; onSelect(id: string): void; onOpen?(session: SessionInfo, keep?: boolean): void; onHistory?(): void }) {
  const panels = useContext(ChannelPanelsContext)
  const sessions = useSessionStore(s => s.sessions)
  const rootId = rootSessionId(sessions, sessionId) ?? sessionId
  const main = sessions.find(s => s.id === rootId)
  const organization = useChannelCategories(rootId)
  const [emojiSession, setEmojiSession] = useState<SessionInfo | null>(null)
  const rowMenu = useDockRowMenu<{ kind: 'session' | 'category'; id: string }>()
  const menuHandlers = (kind: 'session' | 'category', id: string) => kind === 'session' || organization.ready ? rowMenu.handlers({ kind, id }) : {}
  const [editing, setEditing] = useState<{ root: string; id?: string; name: string } | null>(null)
  const [closedCategories, setClosedCategories] = useState<Set<string>>(new Set())
  const toggleCategory = (id: string) => setClosedCategories(previous => { const next = new Set(previous); if (next.has(id)) next.delete(id); else next.add(id); return next })
  const filed = new Set(organization.categories.flatMap(c => c.sessions))
  const dropOn = (event: React.DragEvent, category: string, before?: string) => {
    event.preventDefault(); event.stopPropagation()
    try {
      const channel = JSON.parse(event.dataTransfer.getData(channelDragType) || 'null')
      const group = JSON.parse(event.dataTransfer.getData(categoryDragType) || 'null')
      if (channel?.root === rootId) void organization.action({ action: 'file', id: category, session: channel.id, before })
      else if (group?.root === rootId && category !== group.id) void organization.action({ action: 'move', id: group.id, before: category })
    } catch { /* Ignore foreign drags. */ }
  }
  const acceptDrop = (event: React.DragEvent) => { if (event.dataTransfer.types.some(t => t === channelDragType || t === categoryDragType)) { event.preventDefault(); event.stopPropagation(); event.dataTransfer.dropEffect = 'move' } }
  const [disclosure, setDisclosure] = useState<{ selection: string; open: boolean | null }>({ selection: '', open: null })
  const treeId = useId()
  const [expanded, setExpanded] = useState<Set<string>>(new Set())
  const [focus, setFocus] = useState({ route: sessionId, id: selected })
  const focused = focus.route === sessionId ? focus.id : selected
  const setFocused = (id: string) => setFocus({ route: sessionId, id })
  const [finished, setFinished] = useState(false)
  const descendants: SessionInfo[] = []
  const childrenOf = new Map<string, SessionInfo[]>()
  for (const session of sessions) {
    if (!session.parent_session_id) continue
    const children = childrenOf.get(session.parent_session_id) ?? []
    children.push(session)
    childrenOf.set(session.parent_session_id, children)
  }
  const collect = (id: string, seen = new Set<string>()) => { if (seen.has(id)) return; seen.add(id); for (const child of childrenOf.get(id) ?? []) { if (seen.has(child.id)) continue; descendants.push(child); collect(child.id, seen) } }
  collect(rootId)
  const workers = descendants.filter(s => !s.archived_at && sessionRole(s) === 'worker' && !filed.has(s.id))
  // BLG-501: when the Workers folder mixes runtimes (a host and a cluster worker side by
  // side), every row needs its own visible glyph — not just the one differing from the
  // first worker's runtime, which left the "baseline" rows looking identical with the
  // actual runtime readable only in the tooltip.
  const workersMixedRuntimes = new Set(workers.map(s => s.runtime).filter(Boolean)).size > 1
  const channels = descendants.filter(s => !s.archived_at && sessionRole(s) === 'channel')
  const channelRoots = channels.filter(s => !filed.has(s.id) && !channels.some(parent => parent.id === s.parent_session_id))
  // The server identifies questions for this person; a bare wait can belong to Main.
  const allAttention = useAttentionItems(rootId)
  const attentionSessions = allAttention.map(item => sessions.find(s => s.id === item.session.id)).filter((s): s is SessionInfo => !!s)
  const attentionIds = new Set(attentionSessions.map(s => s.id))
  const workersOpen = disclosure.selection !== selected && workers.some(s => s.id === selected || s.id === sessionId) ? true : disclosure.open ?? workers.some(s => s.status === 'running')
  const setWorkersOpen = (open: boolean) => setDisclosure({ selection: selected, open })
  const membersFor = (ids: string[]) => {
    const result: SessionInfo[] = [], seen = new Set<string>()
    const visit = (id: string) => {
      const item = descendants.find(s => s.id === id && !s.archived_at)
      if (!item || seen.has(id)) return
      seen.add(id); result.push(item)
      if (sessionRole(item) !== 'worker') channels.filter(s => s.parent_session_id === id && !filed.has(s.id)).forEach(s => visit(s.id))
    }
    ids.forEach(visit)
    return result
  }
  const categoryOpen = (id: string, ids: string[]) => !closedCategories.has(id) || membersFor(ids).some(s => s.id === selected || s.id === sessionId || attentionIds.has(s.id))
  const revealAncestors = new Set<string>()
  for (const id of [selected, sessionId, ...attentionIds]) {
    let item = sessions.find(s => s.id === id)
    const seen = new Set<string>()
    while (item?.parent_session_id && !seen.has(item.id)) { seen.add(item.id); revealAncestors.add(item.parent_session_id); item = sessions.find(s => s.id === item!.parent_session_id) }
  }
  const visible = new Set([rootId])
  const visit = (session: SessionInfo) => {
    if (visible.has(session.id)) return
    visible.add(session.id)
    if (expanded.has(session.id) || revealAncestors.has(session.id)) channels.filter(s => s.parent_session_id === session.id && !filed.has(s.id)).forEach(visit)
  }
  channelRoots.forEach(visit)
  organization.categories.forEach(c => { visible.add(c.id); if (categoryOpen(c.id, c.sessions)) c.sessions.forEach(id => { const session = descendants.find(s => s.id === id && !s.archived_at); if (session) visit(session) }) })
  if (workers.length) visible.add('workers')
  if (workersOpen) workers.forEach(s => visible.add(s.id))
  if (descendants.some(s => s.archived_at)) visible.add('finished')
  if (finished) descendants.filter(s => s.archived_at).forEach(visit)
  const tabPath = visible.has(focused) ? focused : rootId
  const toggle = (id: string) => setExpanded(previous => { const next = new Set(previous); if (next.has(id)) next.delete(id); else next.add(id); return next })
  const row = (session: SessionInfo, depth: number, isMain = false): ReactNode => {
    const role = sessionRole(session)
    const worker = role === 'worker'
    const roleLabel = isMain && sessionNoun(session) === 'workspace' && worker ? 'Workspace' : worker ? 'Worker' : role === 'run' ? 'Run' : isMain && sessionNoun(session) === 'workspace' ? 'Main' : 'Channel'
    const groupRuntime = workers[0]?.runtime
    const showRuntime = worker && !isMain ? (workersMixedRuntimes ? !!session.runtime : !!session.runtime && session.runtime !== groupRuntime) : !isMain || worker
    const children = worker || session.archived_at ? [] : channels.filter(s => s.parent_session_id === session.id && !filed.has(s.id))
    const open = expanded.has(session.id) || revealAncestors.has(session.id)
    const name = isMain && (role === 'channel' || role === 'workspace') && sessionNoun(session) === 'workspace' ? 'Main' : session.title || `Untitled ${role}`
    return <li role="none" key={session.id} draggable={!mobile && !isMain && organization.ready} onDragStart={event => { if (isMain) return; event.stopPropagation(); event.dataTransfer.setData(channelDragType, JSON.stringify({ root: rootId, id: session.id })); event.dataTransfer.effectAllowed = 'move' }} onDragOver={!isMain ? acceptDrop : undefined} onDrop={!isMain ? event => dropOn(event, organization.categories.find(c => c.sessions.includes(session.id))?.id ?? '', session.id) : undefined} className={isMain ? 'dock-main' : worker ? 'dock-worker' : undefined}>
      <ExplorerTreeRow {...menuHandlers('session', session.id)} aria-label={session.status === 'error' ? `${name}, failed` : name} aria-description={`${roleLabel}${(!isMain || worker) ? `, ${session.runtime === 'daemon' ? 'Host' : session.runtime === 'docker' ? 'Sandbox' : session.runtime === 'cluster' ? 'Cluster' : 'Unknown runtime'}` : ''}`} aria-level={depth + 1} data-visible={panels?.visible.includes(session.id) || undefined} aria-selected={panels ? panels.focused === session.id : selected === session.id} tabIndex={tabPath === session.id ? 0 : -1} onFocus={() => setFocused(session.id)} title={[roleLabel, session.title, (!isMain || worker) ? session.runtime === 'daemon' ? 'Host' : session.runtime === 'docker' ? 'Sandbox' : session.runtime === 'cluster' ? 'Cluster' : 'Unknown runtime' : '', sessionRepos(session).label].filter(Boolean).join(' · ')} style={{ paddingInlineStart: '6px' }} expanded={!isMain && children.length ? open : undefined} onToggle={!isMain && children.length ? () => toggle(session.id) : undefined} onClick={() => { onSelect(session.id); onOpen?.(session) }} onDoubleClick={() => { onSelect(session.id); onOpen?.(session, true) }} icon={worker ? <WorkerKindIcon kind={session.worker_kind} /> : session.emoji ? <span className="dock-channel-emoji" aria-hidden="true">{session.emoji}</span> : <ChatRoleIcon worker={false} />} status={session.status !== 'error' && allAttention.some(item => item.session.id === session.id) && (!isMain || worker) ? <span className="dock-needs-you" role="img" aria-label="Needs you" title="Needs you">?</span> : <SessionStatusGlyph session={session} />}>
        <span className="workspace-name">{name}</span>{(!worker || isMain) && session.unread && <span className="dock-channel-unread" role="img" aria-label={`Unread ${isMain ? 'workspace' : 'channel'} output`} title={`Unread ${isMain ? 'workspace' : 'channel'} output`}>•</span>}{showRuntime && <RuntimeIcon runtime={session.runtime} />}
      </ExplorerTreeRow>

      {session.archived_at && <Link onClick={onHistory} className="session-history-link" to={`/sessions/${encodeURIComponent(session.id)}?panel=history`}>History</Link>}
      {!isMain && open && children.length > 0 && <ul role="group" style={{ borderInlineStart: depth < 4 ? '1px solid var(--stone)' : '0', marginInlineStart: depth < 4 ? 16 : 0 }}>{children.map(child => row(child, depth + 1))}</ul>}
    </li>
  }
  const menuActions: DockRowAction[] = []
  if (rowMenu.menu) {
    const { kind, id } = rowMenu.menu.data
    const menuSession = sessions.find(s => s.id === id)
    if (kind === 'session' && menuSession && sessionRole(menuSession) !== 'worker') menuActions.push({ label: 'Change emoji', run: () => setEmojiSession(menuSession) })
    if (kind === 'category') {
      const index = organization.categories.findIndex(c => c.id === id), category = organization.categories[index]
      if (category) menuActions.push(
        { label: `Rename ${category.name}`, run: () => setEditing({ root: rootId, id, name: category.name }) },
        { label: `Delete ${category.name}`, disabled: organization.busy, run: () => { void organization.action({ action: 'delete', id }) } },
        { label: 'Move up', disabled: organization.busy || index === 0, run: () => { void organization.action({ action: 'move', id, before: organization.categories[index - 1]?.id }) } },
        { label: 'Move down', disabled: organization.busy || index === organization.categories.length - 1, run: () => { void organization.action({ action: 'move', id, before: organization.categories[index + 2]?.id ?? '' }) } },
      )
    } else if (id !== rootId) {
      for (const category of organization.categories) menuActions.push({ label: `File in ${category.name}`, disabled: organization.busy, run: () => { void organization.action({ action: 'file', id: category.id, session: id }) } })
      if (filed.has(id)) menuActions.push({ label: 'Unfile', disabled: organization.busy, run: () => { void organization.action({ action: 'file', id: '', session: id }) } })
    }
    menuActions.push({ label: 'New category', disabled: organization.busy, run: () => setEditing({ root: rootId, name: '' }) })
  }
  const archived = descendants.filter(s => s.archived_at)
  return <section className="workspace-tree" aria-label="Channel explorer">
    {heading}
    {main && creating && <NewChannel key={rootId} workspace={main} close={closeChannelForm} />}

    {emojiSession && <ChannelEmojiEditor key={emojiSession.id} session={emojiSession} close={() => setEmojiSession(null)} />}
    {editing?.root === rootId && <CategoryEditor key={`${rootId}:${editing.id ?? 'new'}`} editing={editing} action={organization.action} busy={organization.busy} close={() => setEditing(null)} />}
    {organization.error && <p role="alert">{organization.error}</p>}
    <ul role="tree" aria-label="Channels" onKeyDown={explorerTreeKey}>
    {main && row(main, 0, true)}
    {organization.categories.map(category => <li role="none" key={category.id} className="dock-category-group" onDragOver={acceptDrop} onDrop={event => dropOn(event, category.id)} draggable={!mobile} onDragStart={event => { event.stopPropagation(); event.dataTransfer.setData(categoryDragType, JSON.stringify({ root: rootId, id: category.id })); event.dataTransfer.effectAllowed = 'move' }}>
        <DockSessionGroup {...menuHandlers('category', category.id)} name={category.name} category items={membersFor(category.sessions)} attention={attentionSessions} aria-level={1} aria-selected={false} tabIndex={tabPath === category.id ? 0 : -1} onFocus={() => setFocused(category.id)} expanded={categoryOpen(category.id, category.sessions)} onClick={() => toggleCategory(category.id)} onToggle={() => toggleCategory(category.id)} />
      {categoryOpen(category.id, category.sessions) && <ul role="group" aria-label={category.name}>{category.sessions.map(id => descendants.find(s => s.id === id && !s.archived_at)).filter((s): s is SessionInfo => !!s).map(session => row(session, 1))}</ul>}
    </li>)}
    {channelRoots.length > 0 && <li role="none" onDragOver={acceptDrop} onDrop={event => dropOn(event, '')}><ul role="group" aria-label="Channels">{channelRoots.map(child => row(child, 0))}</ul></li>}
    {workers.length > 0 && <li role="none" onDragOver={acceptDrop} onDrop={event => dropOn(event, '')}><DockSessionGroup name="Workers" items={workers} attention={attentionSessions} aria-level={1} aria-selected={false} tabIndex={tabPath === 'workers' ? 0 : -1} onFocus={() => setFocused('workers')} expanded={workersOpen} onToggle={() => setWorkersOpen(!workersOpen)} onClick={() => setWorkersOpen(!workersOpen)} />
      {workersOpen && <ul role="group">{workers.map(worker => row(worker, 1))}</ul>}
    </li>}
    {organization.categories.length > 0 && channelRoots.length === 0 && workers.length === 0 && <li role="none" onDragOver={acceptDrop} onDrop={event => dropOn(event, '')}><span className="dock-section-label">Drop here to unfile</span></li>}
    {archived.length > 0 && <li role="none"><ExplorerTreeRow aria-label="Finished" aria-level={1} aria-selected={false} tabIndex={tabPath === 'finished' ? 0 : -1} onFocus={() => setFocused('finished')} expanded={finished} onToggle={() => setFinished(!finished)} onClick={() => setFinished(!finished)} icon={<FileIcon name="Finished" directory expanded={finished} />}><span className="workspace-name">Finished</span></ExplorerTreeRow>
      {finished && <ul role="group" id={`${treeId}-finished`} style={{ borderInlineStart: '1px solid var(--stone)', marginInlineStart: 16 }}>{archived.map(child => row(child, 1))}</ul>}
    </li>}
  </ul>{rowMenu.menu && <DockRowMenu x={rowMenu.menu.x} y={rowMenu.menu.y} actions={menuActions} close={rowMenu.close} />}</section>
}
