import { useQuestionItems, questionItemCount } from '../hooks/useQuestionItems'
import { useInboxUnread, openInbox } from '../hooks/useInbox'
import { requestDockTool } from '../hooks/useDockTabs'
import { InboxIcon } from './Inbox'
import { InsightsIcon } from './IconButton'
import DockRowMenu from './DockRowMenu'
import { BoardIcon } from './SessionBoardButton'
import { useEffect, useRef, useState, type MouseEvent, type ReactNode , useLayoutEffect} from 'react'
import type { ExplorerLayout } from '../hooks/useExplorerLayout'
import { tabLabel, useSessionTitles, type SessionTabs } from '../hooks/useSessionTabs'
import { sessionTabDragType } from './SessionGroups'
import { stripInsertion, type TabGroup } from '../lib/sessionGroups'
import { SHELL_LIMIT_REASON } from './ShellWorkspace'
import { FileIcon } from '@blerglab/chat'
import { useSessionStore } from '../hooks/useSessionStore'
import { suppressWorkerUnread } from '../lib/sessionPresentation'
import { sessionRole } from '../lib/sessionRole'
import SessionTabStrip from './SessionTabStrip'
import { nextTabIndex } from '../lib/tabStrip'
import ChatRoleIcon from './ChatRoleIcon'
import './SessionTabBar.css'
// `end` no longer draws anything of its own here (shell/board/panel moved to the session header,
// card BLG-450) but stays in the prop list: callers still pass it per strip, and SessionGroups still
// reads it to decide which strip of a split carries the shared toggles (start/end).
export default function SessionTabBar({ tabs, explorer, shellOpen: _shellOpen, group, grouped = false, start = true, end: _end = true, workspace = true }: { workspace?: boolean; tabs: SessionTabs; explorer: ExplorerLayout; shellOpen: boolean; group?: TabGroup; grouped?: boolean; start?: boolean; end?: boolean }) {
  const mainLabel = useSessionStore(s => { const session = s.sessions.find(session => session.id === tabs.sessionId); return session && sessionRole(session) !== 'workspace' ? session.title || 'Untitled chat' : 'Main' })
  const titles = useSessionTitles(tabs.tabs, tabs.labels)
  const workers = useSessionStore(state => JSON.stringify(tabs.tabs.flatMap(({ view }) => { if (view.kind !== 'session-chat') return []; return state.sessions.some(s => s.id === view.sessionId && sessionRole(s) === 'worker') ? [view.sessionId] : [] })))
  // Only session-chat tabs draw unread markers. Scanning every retained worker here also
  // walks its ancestry, once per strip and store broadcast, even for board/file-only tabs.
  const suppressed = useSessionStore(state => JSON.stringify(tabs.tabs.flatMap(({ view }) => {
    if (view.kind !== 'session-chat') return []
    const session = state.sessions.find(s => s.id === view.sessionId)
    return session && suppressWorkerUnread(session, state.sessions) ? [session.id] : []
  })))
  const suppressedIds = new Set<string>(JSON.parse(suppressed))
  const workerIds = new Set<string>(JSON.parse(workers))
  const needsYou = useQuestionItems(tabs.sessionId)
  const label = (view: SessionTabs['tabs'][number]['view']) => view.kind === 'needs-you' ? needsYou.items.length ? `Needs you · ${needsYou.items.length}` : 'Needs you' : tabLabel(view, titles)
  // grouped: one strip of a split, numbered. start/end: this strip carries the workspace toggles
  // (explorer at its left end; shell, board and panel at its right end), so a split needs no extra row.
  const { triggerRef, id: explorerId, open: explorerOpen, toggle: toggleExplorer, mobile } = explorer
  const [tabMenu, setTabMenu] = useState<{ id: string; x: number; y: number } | null>(null)
  const press = useRef<ReturnType<typeof setTimeout> | null>(null)
  useEffect(() => () => { if (press.current) clearTimeout(press.current) }, [])
  const cancelPress = () => { if (press.current) clearTimeout(press.current); press.current = null }
  const gestureTab = (target: EventTarget) => (target as HTMLElement).closest('.session-tab')?.querySelector<HTMLElement>('[data-tab-id]')
  const tabGestures = {
    onContextMenu: (event: React.MouseEvent) => {
      const tab = gestureTab(event.target)
      if (!tab?.dataset.tabId) return
      event.preventDefault(); cancelPress(); setTabMenu({ id: tab.dataset.tabId, x: event.clientX, y: event.clientY })
    },
    onKeyDown: (event: React.KeyboardEvent) => {
      const tab = gestureTab(event.target)
      if (!tab?.dataset.tabId || !(event.key === 'ContextMenu' || event.shiftKey && event.key === 'F10')) return
      event.preventDefault(); event.stopPropagation()
      const rect = tab.getBoundingClientRect(); setTabMenu({ id: tab.dataset.tabId, x: rect.left, y: rect.bottom })
    },
    onTouchStart: (event: React.TouchEvent) => {
      cancelPress()
      const tab = gestureTab(event.target), touch = event.touches[0]
      if (!tab?.dataset.tabId || !touch) return
      const id = tab.dataset.tabId, { clientX: x, clientY: y } = touch
      press.current = setTimeout(() => setTabMenu({ id, x, y }), 500)
    },
    onTouchEnd: cancelPress, onTouchMove: cancelPress, onTouchCancel: cancelPress,
  }
  const [kept, setKept] = useState(false)
  // Where a dragged tab would land in this strip: the insertion marker's left offset, as VS Code draws it.
  const [marker, setMarker] = useState<number | null>(null)
  useEffect(() => {
    if (marker === null) return
    const clear = () => setMarker(null)
    // Capture: a split's drop listener stops the drop from bubbling, and a moved tab's dragend has no document to reach.
    document.addEventListener('dragend', clear, true); document.addEventListener('drop', clear, true)
    return () => { document.removeEventListener('dragend', clear, true); document.removeEventListener('drop', clear, true) }
  }, [marker])
  const keptButton = useRef<HTMLButtonElement>(null)
  const keptMenu = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (!kept) return
    keptMenu.current?.querySelector<HTMLButtonElement>('[role=menuitem]')?.focus()
    const outside = (e: PointerEvent) => { if (!keptMenu.current?.contains(e.target as Node) && !keptButton.current?.contains(e.target as Node)) setKept(false) }
    document.addEventListener('pointerdown', outside)
    return () => document.removeEventListener('pointerdown', outside)
  }, [kept])
  const preventMiddleDefault = (event: MouseEvent) => { if (event.button === 1) event.preventDefault() }
  const middleClose = (event: MouseEvent, id: string) => {
    if (event.button !== 1) return
    event.preventDefault()
    tabs.close(id, 'tab')
    setKept(false)
  }
  const ids = (!mobile && group ? group.tabs : undefined) ?? tabs.groups.flatMap(g => g.tabs)
  const active = !mobile && group ? group.active : tabs.active
  const files = !mobile && group ? group.tabs.flatMap(id => tabs.tabs.filter(t => t.id === id)) : tabs.tabs
  const startDrag = (event: React.DragEvent, id: string) => { event.dataTransfer.effectAllowed = 'move'; event.dataTransfer.setData('text/plain', id); event.dataTransfer.setData(sessionTabDragType, JSON.stringify({ scope: tabs.scope, id })) }
  const openFiles = tabs.tabs.filter(t => !t.preview).length
  const hasOtherViews = tabs.tabs.some(t => t.view.kind === 'needs-you' || t.view.kind === 'inbox' || t.view.kind === 'ticket' || t.view.kind === 'question' || t.view.kind === 'blackboard' || t.view.kind === 'insights' || t.view.kind === 'test-performance' || t.view.kind === 'branch-graph' || t.view.kind === 'shell' || t.view.kind === 'session-chat' || t.view.kind === 'board')
  const filesLabel = `${openFiles} open ${hasOtherViews ? openFiles === 1 ? 'tab' : 'tabs' : openFiles === 1 ? 'file' : 'files'}`
  const badge = tabs.unread > 0 && <span className="session-tab-unread" aria-label="New agent output">{tabs.unread}</span>
  const folder = workspace && <div className="session-explorer-toggle"><button ref={triggerRef} type="button" className="icon-btn" aria-label="Toggle explorer" aria-controls={explorerId} aria-expanded={explorerOpen} onClick={toggleExplorer} title="Toggle explorer"><svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="1.75" aria-hidden="true"><path d="M3 4h7l2 3h9v13H3z" /><path d="M8 7v13" /></svg></button>{!explorerOpen && <CollapsedCounts sessionId={tabs.sessionId} toggleExplorer={toggleExplorer} />}</div>
  const tooltip = (view: typeof tabs.tabs[number]['view']) => view.kind === 'shell' ? `${label(view)} · ${view.runtime === 'cluster' ? 'pod' : view.runtime} · ${view.folder}` : view.kind === 'session-chat' ? `${workerIds.has(view.sessionId) ? 'Worker' : 'Channel'} · ${label(view)}` : view.kind === 'workspace' ? view.path : view.kind === 'artifact' ? view.artifact.name : label(view)
  const icon = (view: typeof tabs.tabs[number]['view']) => (view.kind === 'needs-you' || view.kind === 'question') ? <span aria-hidden="true">?</span> : view.kind === 'inbox' ? <InboxIcon /> : (view.kind === 'insights' || view.kind === 'test-performance') ? <InsightsIcon /> : (view.kind === 'ticket' || view.kind === 'blackboard' || view.kind === 'board') ? <BoardIcon /> : view.kind === 'session-chat' ? <ChatRoleIcon worker={workerIds.has(view.sessionId)} size={14} /> : view.kind === 'shell' ? <span aria-hidden="true">›_</span> : view.kind === 'commit' ? '±' : <FileIcon name={label(view)} />
  const exitFocus = tabs.tabFocused && (!grouped || group?.id === tabs.focused) && <button type="button" className="icon-btn" aria-label="Exit tab focus" title="Exit tab focus (Ctrl/⌘+Shift+Enter)" onClick={tabs.toggleTabFocus}>Exit focus</button>
  const doubleClick = (id: string) => {
    if (tabs.tabs.find(tab => tab.id === id)?.view.kind === 'shell') { tabs.requestShellRename(id); return }
    if (tabs.tabs.find(tab => tab.id === id)?.preview) tabs.keep(id)
    else tabs.toggleTabFocus?.()
  }
  const selectChat = (id: string) => {
    tabs.select(id)
    if (!mobile) tabs.targetFor(id).querySelector<HTMLElement>('.agent-composer textarea')?.focus()
  }
  const activeFile = tabs.tabs.find(t => t.id === tabs.active)
  const groupNumber = group ? tabs.groups.findIndex(g => g.id === group.id) + 1 : 1
  const mainTab = <div key="chat" draggable  onAuxClick={event => middleClose(event, 'chat')} onDoubleClick={() => doubleClick('chat')} onDragStart={e => startDrag(e, 'chat')} className={`session-tab${active === 'chat' ? ' active' : ''}`}><button role="tab" id="session-tab-chat" aria-controls="session-view-chat" aria-selected={active === 'chat'} tabIndex={active === 'chat' ? 0 : -1} data-tab-id="chat" onClick={() => selectChat('chat')}><ChatRoleIcon size={14} /> <span>{mainLabel}</span>{!mobile && badge}</button><button className="session-tab-close" tabIndex={-1} aria-label={`Close ${mainLabel}`} onClick={() => tabs.close('chat', 'tab')}>×</button></div>
  const tabStrip = <SessionTabStrip dragScope={tabs.scope} active={active} items={ids.map(id => id === 'chat' ? { id, label: mainLabel, icon: <ChatRoleIcon size={14} /> } : { id, label: label(tabs.tabs.find(t => t.id === id)!.view), preview: tabs.tabs.find(t => t.id === id)!.preview, icon: icon(tabs.tabs.find(t => t.id === id)!.view) })} label={grouped ? `Workspace tabs, group ${groupNumber}` : 'Workspace tabs'} onSelect={id => { tabs.select(id); if (id === active) tabs.targetFor(id).querySelector<HTMLElement>('[data-view-focus], .xterm-helper-textarea, .agent-composer textarea, .hosted-view:not([hidden]) [role="region"], .session-board-columns')?.focus() }} onDragPosition={(x, strip) => setMarker(previous => { const left = stripInsertion(x, strip).left; return previous === left ? previous : left })} onDragLeave={e => { if (!e.currentTarget.contains(e.relatedTarget as Node)) setMarker(null) }} onDrop={e => {
    // One group: reorder here. In a split the groups' own drop listener handles the strip first.
    setMarker(null)
    try { const data = JSON.parse(e.dataTransfer.getData(sessionTabDragType)); if (data.scope !== tabs.scope || !ids.includes(data.id)) return; e.preventDefault(); tabs.move(data.id, stripInsertion(e.clientX, e.currentTarget).before) } catch { /* External drag. */ }
  }} onKeyDown={e => {
    const focused = (e.target as HTMLElement).closest<HTMLElement>('[role="tab"]')?.dataset.tabId
    if (!focused) return
    const i = ids.indexOf(focused)
    if (e.altKey && e.shiftKey && ['ArrowLeft', 'ArrowRight'].includes(e.key)) { e.preventDefault(); tabs.move(focused, ids[e.key === 'ArrowLeft' ? Math.max(0, i - 1) : i + 2] ?? ''); return }
    const next = nextTabIndex(e.key, i, ids.length)
    if (next !== null) { e.preventDefault(); tabs.select(ids[next], true, 'tab'); e.currentTarget.querySelectorAll<HTMLButtonElement>('[role="tab"]')[next]?.focus() }
    if (e.key === 'Delete') { e.preventDefault(); tabs.close(focused, 'tab') }
  }}>
    {ids.flatMap(id => id === 'chat' ? [mainTab] : files.filter(t => t.id === id).map(t => <div key={t.id} draggable  onAuxClick={event => middleClose(event, t.id)} className={`session-tab${active === t.id ? ' active' : ''}${t.preview ? ' preview' : ''}`} onDoubleClick={() => doubleClick(t.id)} onDragStart={e => startDrag(e, t.id)}>
      <button role="tab" id={`session-tab-${encodeURIComponent(t.id)}`} aria-controls={`session-view-${encodeURIComponent(t.id)}`} aria-selected={active === t.id} tabIndex={active === t.id ? 0 : -1} data-tab-id={t.id} aria-description={t.view.kind === 'session-chat' ? workerIds.has(t.view.sessionId) ? 'Worker' : 'Channel' : undefined} title={`${tooltip(t.view)}${t.preview ? ' · Preview · Double-click to keep open' : ''}`} onClick={() => selectChat(t.id)}>{icon(t.view)} <span>{label(t.view)}</span>{t.unread && !(t.view.kind === 'session-chat' && suppressedIds.has(t.view.sessionId)) && <span className="session-tab-unread" aria-label={`New output in ${label(t.view)}`}>•</span>}</button><button className="session-tab-close" tabIndex={-1} aria-label={`Close ${label(t.view)}`} onClick={() => tabs.close(t.id, 'tab')}>×</button>
    </div>))}
    {tabMenu && <DockRowMenu label="Tab actions" x={tabMenu.x} y={tabMenu.y} close={(restore = true) => { const id = tabMenu.id; setTabMenu(null); if (restore) document.getElementById(`session-tab-${encodeURIComponent(id)}`)?.focus() }} actions={[
      { label: 'Close', run: () => tabs.close(tabMenu.id, 'tab') },
      { label: 'Close others', run: () => { tabs.closeMany(ids.filter(id => id !== tabMenu.id), 'tab') } },
      { label: 'Close tabs to the right', run: () => { tabs.closeMany(ids.slice(ids.indexOf(tabMenu.id) + 1), 'tab') } },
      ...(['blackboard', 'board', 'branch-graph', 'workspace', 'artifact', 'commit'].includes(tabs.tabs.find(t => t.id === tabMenu.id)?.view.kind ?? '') ? [{ label: 'Duplicate', run: () => tabs.duplicate(tabMenu.id) }] : []),
      ...(!mobile ? [{ label: 'Move to a split panel', run: () => tabs.drop(tabMenu.id, group?.id ?? tabs.focused, 'right') }] : []),
      ...(tabs.tabs.find(t => t.id === tabMenu.id)?.view.kind === 'shell' ? [{ label: 'Rename', run: () => tabs.requestShellRename(tabMenu.id) }] : []),
    ]} />}
    {marker !== null && <span className="session-tab-insert" data-testid="tab-insert-marker" aria-hidden="true" style={{ left: marker }} />}
  </SessionTabStrip>
  if (mobile) return <nav {...tabGestures} className={`session-mobile-tools${activeFile ? ' has-file' : ''}`} aria-label="Workspace navigation" onMouseDown={preventMiddleDefault} onAuxClick={preventMiddleDefault}>
    <div className="session-mobile-controls">{folder}{workspace && <button className="session-back-chat" onClick={() => selectChat('chat')}>← {mainLabel} {badge}</button>}
    <>{openFiles > 0 && <button ref={keptButton} className="artifact-btn" aria-label={filesLabel} title={filesLabel} aria-haspopup="menu" aria-expanded={kept} onClick={() => setKept(!kept)}><svg aria-hidden="true" viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="1.75"><path d="M7 3h14v14M3 7h14v14H3z" /></svg> {openFiles}</button>}</>{exitFocus}
    {kept && <div ref={keptMenu} className="session-kept" role="menu" aria-label="Kept open" onKeyDown={e => {
      if (e.key === 'Escape') { e.preventDefault(); setKept(false); keptButton.current?.focus() }
      if (e.key === 'Tab') setKept(false)
      if (['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(e.key)) {
        e.preventDefault()
        const items = [...e.currentTarget.querySelectorAll<HTMLButtonElement>('[role=menuitem]')]
        const current = items.indexOf(document.activeElement as HTMLButtonElement)
        const next = e.key === 'Home' ? 0 : e.key === 'End' ? items.length - 1 : (current + (e.key === 'ArrowDown' ? 1 : -1) + items.length) % items.length
        items[next]?.focus()
      }
    }}>{tabs.tabs.filter(t => !t.preview).map(t => <div key={t.id} onAuxClick={event => middleClose(event, t.id)}><button role="menuitem" onClick={() => { tabs.select(t.id); setKept(false) }}><span aria-hidden="true">{icon(t.view)}</span> {label(t.view)}</button><button aria-label={`Close ${label(t.view)}`} onClick={() => tabs.close(t.id, 'tab')}>×</button></div>)}</div>}
    </div>
    {ids.length > 0 && tabStrip}
  </nav>
  return <nav {...tabGestures} className="session-tabbar" aria-label={grouped ? `Workspace group ${groupNumber}` : 'Workspace'} aria-current={grouped && tabs.focused === group?.id ? 'true' : undefined} onMouseDown={preventMiddleDefault} onAuxClick={preventMiddleDefault}>{start && folder}{tabStrip}{exitFocus}</nav>
}

/** New shell: in the header's "opens a tab" group (moved out of the tab bar itself, card BLG-450),
 *  so it reuses the same shell-limit tooltip wherever it is placed. */
export function ShellToggleButton({ tabs, onOpen }: { tabs: SessionTabs; onOpen(): void }) {
  const shellLimit = Math.max(tabs.shellCount, tabs.tabs.filter(t => t.view.kind === 'shell').length) >= 4
  return <button type="button" className="icon-btn session-shell-toggle" aria-label="New shell" title={shellLimit ? SHELL_LIMIT_REASON : "New shell · Ctrl / ⌘ ` to focus shell"} aria-description={shellLimit ? SHELL_LIMIT_REASON : undefined} onClick={onOpen}>&gt;_</button>
}

/** Fixed tool inventory, using the workspace strip, insertion marks and group drop handlers. */
export function ToolTabBar({ tabs, group, mobile, onSelect, action, counts = {} }: { counts?: Record<string, number>; tabs: import('./SessionGroups').GroupTabs; group: TabGroup; mobile: boolean; onSelect(id: string): void; action?: ReactNode }) {
  const labels: Record<string, string> = { sessions: 'Channels', files: 'Files', repositories: 'Repositories', questions: 'Questions', inbox: 'Inbox' }
  const [marker, setMarker] = useState<number | null>(null)
  const nav = useRef<HTMLElement>(null)
  const tabKey = group.tabs.join('\0'), countKey = JSON.stringify(counts)
  // Labels step down only when they don't fit: all, then the selected tab's only, then glyphs.
  // Measured on the strip (a tab sized to its content can't measure itself).
  useLayoutEffect(() => {
    const bar = nav.current, strip = bar?.querySelector<HTMLElement>('.session-tabs')
    if (!bar || !strip) return
    const fit = () => {
      for (const level of ['all', 'active', 'none']) {
        bar.dataset.labels = level
        if (strip.scrollWidth <= strip.clientWidth + 1) return
      }
    }
    fit()
    // Measured next frame: changing labels inside the observer's own callback would loop it.
    let frame = 0
    const observer = new ResizeObserver(() => { cancelAnimationFrame(frame); frame = requestAnimationFrame(fit) })
    observer.observe(strip)
    return () => { cancelAnimationFrame(frame); observer.disconnect() }
  }, [tabKey, group.active, countKey])
  useEffect(() => {
    const clear = () => setMarker(null)
    document.addEventListener('dragend', clear, true); document.addEventListener('drop', clear, true)
    return () => { document.removeEventListener('dragend', clear, true); document.removeEventListener('drop', clear, true) }
  }, [])
  return <nav ref={nav} className="session-tabbar dock-tool-tabbar" aria-label="Dock tools" onKeyDown={event => {
    const modified = /Mac|iPhone|iPad|iPod/.test(navigator.platform) ? event.ctrlKey && !event.altKey : event.altKey && !event.ctrlKey
    if (!modified || event.metaKey || event.shiftKey || event.nativeEvent.isComposing) return
    const index = /^[1-9]$/.test(event.key) ? Number(event.key) - 1 : event.key === ']' ? (group.tabs.indexOf(group.active) + 1) % group.tabs.length : event.key === '[' ? (group.tabs.indexOf(group.active) + group.tabs.length - 1) % group.tabs.length : null
    if (index === null) return
    event.preventDefault(); event.stopPropagation()
    if (event.repeat || !group.tabs[index]) return
    onSelect(group.tabs[index]); document.getElementById(`session-tab-${group.tabs[index]}`)?.focus()
  }}>
    <SessionTabStrip dragScope={tabs.scope} disabledDrag={mobile} active={group.active} items={group.tabs.map(id => ({ id, label: labels[id], icon: null }))} label="Dock tools" onSelect={onSelect} onDragPosition={(x, strip) => { if (!mobile) setMarker(stripInsertion(x, strip).left) }} onDragLeave={() => setMarker(null)} onKeyDown={event => {
      const id = (event.target as HTMLElement).closest<HTMLElement>('[data-tab-id]')?.dataset.tabId
      if (!id) return
      const index = group.tabs.indexOf(id)
      const next = nextTabIndex(event.key, index, group.tabs.length)
      if (next !== null) { event.preventDefault(); onSelect(group.tabs[next]); event.currentTarget.querySelectorAll<HTMLButtonElement>('[role=tab]')[next]?.focus() }
    }}>
      {group.tabs.map(id => <div key={id} className={`session-tab dock-tool-tab${group.active === id ? ' active' : ''}`} draggable={!mobile} onDragStart={event => { event.dataTransfer.effectAllowed = 'move'; event.dataTransfer.setData(sessionTabDragType, JSON.stringify({ scope: tabs.scope, id })) }}><button type="button" role="tab" draggable={!mobile} id={`session-tab-${id}`} data-tab-id={id} aria-controls={`session-view-${id}`} aria-selected={group.active === id} tabIndex={group.active === id ? 0 : -1} onClick={() => onSelect(id)} title={`${labels[id]}${counts[id] ? ` · ${counts[id]} ${id === 'inbox' ? 'unread' : id === 'questions' ? 'open' : 'channels'}` : ''}`} aria-label={labels[id]} aria-description={counts[id] ? `${counts[id]} ${id === 'inbox' ? 'unread items' : id === 'questions' ? 'open questions' : 'channels'}` : undefined}><span className="dock-tool-glyph" aria-hidden="true">{id === 'inbox' ? <InboxIcon /> : id === 'questions' ? <QuestionsIcon /> : id === 'sessions' ? <ChatRoleIcon size={16} /> : id === 'files' ? <FileIcon name="document.txt" /> : <svg aria-hidden="true" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.75"><circle cx="6" cy="5" r="2" /><circle cx="6" cy="19" r="2" /><circle cx="18" cy="5" r="2" /><path d="M6 7v10m0-5h6a6 6 0 0 0 6-5" /></svg>}</span><span className="dock-tool-label" aria-hidden="true">{labels[id]}</span>{!!counts[id] && <span className={`dock-tool-count${id === 'inbox' || id === 'questions' ? ' attention' : ''}`} aria-hidden="true">{counts[id]}</span>}</button></div>)}
      {marker !== null && <span className="session-tab-insert" aria-hidden="true" style={{ left: marker }} />}
    </SessionTabStrip>{action}
  </nav>
}

// The collapsed-explorer counts subscribe on their own, so child streams and
// inbox refreshes re-render only these two buttons, never the whole tab bar.
function CollapsedCounts({ sessionId, toggleExplorer }: { sessionId: string; toggleExplorer(): void }) {
  const questions = questionItemCount(useQuestionItems(sessionId))
  const inboxUnread = useInboxUnread()
  return <>{questions > 0 && <button type="button" className="icon-btn" title={`${questions} open questions`} aria-label={`${questions} open questions`} onClick={() => { requestDockTool(sessionId, 'questions'); toggleExplorer() }}><span aria-hidden="true">?</span>{questions}</button>}{inboxUnread > 0 && <button type="button" className="icon-btn" title={`${inboxUnread} unread inbox items`} aria-label={`${inboxUnread} unread inbox items`} onClick={() => openInbox()}><InboxIcon />{inboxUnread}</button>}</>
}

// A question mark in a speech bubble: the dock's Questions tab, sized like its siblings.
function QuestionsIcon() {
  return <svg aria-hidden="true" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round"><path d="M21 12a8 8 0 0 1-11.6 7.1L4 20l1-4.6A8 8 0 1 1 21 12z" /><path d="M9.8 9.4a2.3 2.3 0 0 1 4.4.9c0 1.5-2.2 2-2.2 3.2" /><path d="M12 16.4h.01" /></svg>
}
