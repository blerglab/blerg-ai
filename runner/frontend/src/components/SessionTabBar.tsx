import { useEffect, useRef, useState, type MouseEvent, type ReactNode } from 'react'
import type { ExplorerLayout } from '../hooks/useExplorerLayout'
import { tabLabel, type SessionTabs } from '../hooks/useSessionTabs'
import { sessionTabDragType } from './SessionGroups'
import { stripInsertion, type TabGroup } from '../lib/sessionGroups'
import { SHELL_LIMIT_REASON } from './ShellWorkspace'
import { FileIcon } from '@blerglab/chat'
import SessionTabStrip from './SessionTabStrip'
import './SessionTabBar.css'
export default function SessionTabBar({ tabs, explorer, shell, shellOpen: _shellOpen, board, panel, group, grouped = false, start = true, end = true }: { tabs: SessionTabs; explorer: ExplorerLayout; shell?: () => void; shellOpen: boolean; board?: ReactNode; panel?: ReactNode; group?: TabGroup; grouped?: boolean; start?: boolean; end?: boolean }) {
  // grouped: one strip of a split, numbered. start/end: this strip carries the workspace toggles
  // (explorer at its left end; shell, board and panel at its right end), so a split needs no extra row.
  const { triggerRef, id: explorerId, open: explorerOpen, toggle: toggleExplorer, mobile } = explorer
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
  const ids = (!mobile && group ? group.tabs : undefined) ?? ['chat', ...tabs.tabs.map(t => t.id)]
  const active = !mobile && group ? group.active : tabs.active
  const files = !mobile && group ? group.tabs.flatMap(id => tabs.tabs.filter(t => t.id === id)) : tabs.tabs
  const startDrag = (event: React.DragEvent, id: string) => { event.dataTransfer.effectAllowed = 'move'; event.dataTransfer.setData('text/plain', id); event.dataTransfer.setData(sessionTabDragType, JSON.stringify({ scope: tabs.scope, id })) }
  const openFiles = tabs.tabs.filter(t => !t.preview).length
  const hasShells = tabs.tabs.some(t => t.view.kind === 'shell')
  const filesLabel = `${openFiles} open ${hasShells ? openFiles === 1 ? 'tab' : 'tabs' : openFiles === 1 ? 'file' : 'files'}`
  const badge = tabs.unread > 0 && <span className="session-tab-unread" aria-label="New agent output">{tabs.unread}</span>
  const folder = <button ref={triggerRef} type="button" className="icon-btn" aria-label="Toggle explorer" aria-controls={explorerId} aria-expanded={explorerOpen} onClick={toggleExplorer} title="Toggle explorer"><svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="1.75" aria-hidden="true"><path d="M3 4h7l2 3h9v13H3z" /><path d="M8 7v13" /></svg>{mobile && <span>Explorer</span>}</button>
  const shellLimit = Math.max(tabs.shellCount, tabs.tabs.filter(t => t.view.kind === 'shell').length) >= 4
  const tooltip = (view: typeof tabs.tabs[number]['view']) => view.kind === 'shell' ? `${tabLabel(view)} · ${view.runtime === 'cluster' ? 'pod' : view.runtime} · ${view.folder}` : tabLabel(view)
  const icon = (view: typeof tabs.tabs[number]['view']) => view.kind === 'shell' ? <span aria-hidden="true">›_</span> : view.kind === 'commit' ? '±' : <FileIcon name={tabLabel(view)} />
  const shellButton = shell && <button type="button" className="icon-btn session-shell-toggle" aria-label="New shell" title={shellLimit ? SHELL_LIMIT_REASON : "New shell · Ctrl / ⌘ ` to focus shell"} aria-description={shellLimit ? SHELL_LIMIT_REASON : undefined} onClick={shell}>&gt;_</button>
  const exitFocus = tabs.tabFocused && (!grouped || group?.id === tabs.focused) && <button type="button" className="icon-btn" aria-label="Exit tab focus" title="Exit tab focus (Ctrl/⌘+Shift+Enter)" onClick={tabs.toggleTabFocus}><svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="1.75" aria-hidden="true"><path d="M3 9h6V3m6 0v6h6M3 15h6v6m6 0v-6h6" /></svg></button>
  const doubleClick = (id: string) => {
    if (tabs.tabs.find(tab => tab.id === id)?.preview) tabs.keep(id)
    else tabs.toggleTabFocus?.()
  }
  const activeFile = tabs.tabs.find(t => t.id === tabs.active)
  const groupNumber = group ? tabs.groups.findIndex(g => g.id === group.id) + 1 : 1
  const tabStrip = <SessionTabStrip active={active} items={ids.map(id => id === 'chat' ? { id, label: 'Chat', icon: <svg viewBox="0 0 16 16" width="14" height="14" fill="none" stroke="currentColor" aria-hidden="true"><path d="M3 2h10v8H6l-3 3z" /></svg> } : { id, label: tabLabel(tabs.tabs.find(t => t.id === id)!.view), preview: tabs.tabs.find(t => t.id === id)!.preview, icon: icon(tabs.tabs.find(t => t.id === id)!.view) })} label={grouped ? `Session tabs, group ${groupNumber}` : 'Session tabs'} onSelect={id => { tabs.select(id); if (id === active) tabs.targetFor(id).querySelector<HTMLElement>('.xterm-helper-textarea, .agent-composer textarea, .hosted-view:not([hidden]) [role="region"]')?.focus() }} onDragPosition={(x, strip) => setMarker(previous => { const left = stripInsertion(x, strip).left; return previous === left ? previous : left })} onDragLeave={e => { if (!e.currentTarget.contains(e.relatedTarget as Node)) setMarker(null) }} onDrop={e => {
    // One group: reorder here. In a split the groups' own drop listener handles the strip first.
    setMarker(null)
    try { const data = JSON.parse(e.dataTransfer.getData(sessionTabDragType)); if (data.scope !== tabs.scope || !ids.includes(data.id) || data.id === 'chat') return; e.preventDefault(); tabs.move(data.id, stripInsertion(e.clientX, e.currentTarget).before) } catch { /* External drag. */ }
  }} onKeyDown={e => {
    const focused = (e.target as HTMLElement).closest<HTMLElement>('[role="tab"]')?.dataset.tabId
    if (!focused) return
    const i = ids.indexOf(focused)
    if (e.altKey && e.shiftKey && focused !== 'chat' && ['ArrowLeft', 'ArrowRight'].includes(e.key)) { e.preventDefault(); tabs.move(focused, ids[e.key === 'ArrowLeft' ? Math.max(ids[0] === 'chat' ? 1 : 0, i - 1) : i + 2] ?? ''); return }
    const next = e.key === 'Home' ? 0 : e.key === 'End' ? ids.length - 1 : e.key === 'ArrowRight' ? (i + 1) % ids.length : e.key === 'ArrowLeft' ? (i + ids.length - 1) % ids.length : null
    if (next !== null) { e.preventDefault(); tabs.select(ids[next], true, 'tab'); e.currentTarget.querySelectorAll<HTMLButtonElement>('[role="tab"]')[next]?.focus() }
    if (e.key === 'Delete' && focused !== 'chat') { e.preventDefault(); tabs.close(focused, 'tab') }
  }}>
    {ids.includes('chat') && <div draggable onDoubleClick={() => doubleClick('chat')} onDragStart={e => startDrag(e, 'chat')} className={`session-tab${active === 'chat' ? ' active' : ''}`}><button role="tab" id="session-tab-chat" aria-controls="session-view-chat" aria-selected={active === 'chat'} tabIndex={active === 'chat' ? 0 : -1} data-tab-id="chat" onClick={() => tabs.select('chat')}><svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" aria-hidden="true"><path d="M3 2h10v8H6l-3 3z" /></svg> <span>Chat</span> <svg className="session-tab-pin" aria-label="Pinned" width="12" height="12" viewBox="0 0 16 16" fill="none" stroke="currentColor"><path d="m6 2 6 6-3 1-2 3-1-3-4-1 3-2zM6 10l-4 4" /></svg>{!mobile && badge}</button></div>}
    {files.map(t => <div key={t.id} draggable onAuxClick={event => middleClose(event, t.id)} className={`session-tab${active === t.id ? ' active' : ''}${t.preview ? ' preview' : ''}`} onDoubleClick={() => doubleClick(t.id)} onDragStart={e => startDrag(e, t.id)}>
      <button role="tab" id={`session-tab-${encodeURIComponent(t.id)}`} aria-controls={`session-view-${encodeURIComponent(t.id)}`} aria-selected={active === t.id} tabIndex={active === t.id ? 0 : -1} data-tab-id={t.id} title={`${tooltip(t.view)}${t.preview ? ' · Preview · Double-click to keep open' : ''}`} onClick={() => tabs.select(t.id)}>{icon(t.view)} <span>{tabLabel(t.view)}</span></button><button className="session-tab-close" tabIndex={-1} aria-label={`Close ${tabLabel(t.view)}`} onClick={() => tabs.close(t.id, 'tab')}>×</button>
    </div>)}
    {marker !== null && <span className="session-tab-insert" data-testid="tab-insert-marker" aria-hidden="true" style={{ left: marker }} />}
  </SessionTabStrip>
  if (mobile) return <nav className={`session-mobile-tools${activeFile ? ' has-file' : ''}`} aria-label="Session navigation" onMouseDown={preventMiddleDefault} onAuxClick={preventMiddleDefault}>
    <div className="session-mobile-controls">{folder}<button id={tabs.tabs.length ? undefined : "session-tab-chat"} className="session-back-chat" onClick={() => tabs.select('chat')}>← Chat {badge}</button>
    <>{openFiles > 0 && <button ref={keptButton} className="artifact-btn" aria-label={filesLabel} title={filesLabel} aria-haspopup="menu" aria-expanded={kept} onClick={() => setKept(!kept)}><svg aria-hidden="true" viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="1.75"><path d="M7 3h14v14M3 7h14v14H3z" /></svg> {openFiles}</button>}</>{exitFocus}{shellButton}{board}{panel}
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
    }}>{tabs.tabs.filter(t => !t.preview).map(t => <div key={t.id} onAuxClick={event => middleClose(event, t.id)}><button role="menuitem" onClick={() => { tabs.select(t.id); setKept(false) }}>{t.view.kind !== 'commit' && <FileIcon name={tabLabel(t.view)} />} {tabLabel(t.view)}</button><button aria-label={`Close ${tabLabel(t.view)}`} onClick={() => tabs.close(t.id, 'tab')}>×</button></div>)}</div>}
    </div>
    {tabs.tabs.length > 0 && tabStrip}
  </nav>
  return <nav className="session-tabbar" aria-label={grouped ? `Workspace group ${groupNumber}` : 'Workspace'} aria-current={grouped && tabs.focused === group?.id ? 'true' : undefined} onMouseDown={preventMiddleDefault} onAuxClick={preventMiddleDefault}>{start && folder}{tabStrip}{exitFocus}{end && shellButton}{end && board}{end && panel}</nav>
}
