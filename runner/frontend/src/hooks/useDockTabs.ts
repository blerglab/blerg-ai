import { signedInAccount } from '../lib/liveBoardCard'
import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { activateTab, groups, mapGroups, moveToGroup, normalizeLayout, resizeSplit, type DropEdge, type TabLayout } from '../lib/sessionGroups'
import { SessionViewTargets } from './useSessionTabs'
import type { GroupTabs } from '../components/SessionGroups'

export const dockTools = ['sessions', 'files', 'repositories', 'questions', 'inbox'] as const
export type DockTool = typeof dockTools[number]
const legacyKey = 'blerg-runner.session-dock.windows'
function read(key: string): { active: string; layout: TabLayout } {
  try {
    const saved = JSON.parse(localStorage.getItem(key) ?? localStorage.getItem(legacyKey) ?? 'null')
    const active = dockTools.includes(saved?.active) ? saved.active : 'sessions'
    if (saved?.layout) return { active, layout: normalizeLayout(saved.layout, [...dockTools], active) }
    // Older docks stored visible tools and absolute divider positions. Keep both.
    const panels: DockTool[] = Array.isArray(saved?.panels) && saved.panels.length >= 2 && saved.panels.length <= 3 && new Set(saved.panels).size === saved.panels.length && saved.panels.every((id: DockTool) => dockTools.includes(id)) ? saved.panels : []
    const leaf = (id: DockTool, index: number): TabLayout => ({ type: 'group', id: `dock-${index}`, tabs: dockTools.filter(t => t === id || index === 0 && !panels.includes(t)), active: id })
    const lower = Math.max(.3, Math.min(.85, Number(saved?.lower) || .7))
    const ratio = Math.max(.15, Math.min(panels.length === 3 ? lower - .15 : .85, Number(saved?.ratio) || .5))
    const layout: TabLayout = panels.length ? { type: 'split', id: 'dock-split', axis: 'horizontal', ratio, first: leaf(panels[0], 0), second: panels.length === 2 ? leaf(panels[1], 1) : { type: 'split', id: 'dock-lower', axis: 'horizontal', ratio: (lower - ratio) / (1 - ratio), first: leaf(panels[1], 1), second: leaf(panels[2], 2) } } : { type: 'group', id: 'dock-main', tabs: [...dockTools], active }
    return { active, layout }
  } catch { console.warn('Dock layout unavailable: invalid browser storage'); return { active: 'sessions', layout: { type: 'group', id: 'dock-main', tabs: [...dockTools], active: 'sessions' } } }
}
const requests = new Map<string, DockTool>()
export function requestDockTool(session: string, tool: DockTool) { requests.set(session, tool) }
export function pendingDockTool(session: string) { return requests.get(session) }
export function clearDockRequest(session: string) { requests.delete(session) }
export function useDockTabs(phone: boolean, initialTool?: DockTool, workspace = '') {
  const key = `${legacyKey}.${encodeURIComponent(signedInAccount())}.${encodeURIComponent(workspace)}`
  const [owner, setOwner] = useState(key)
  const [state, setState] = useState(() => { const state = read(key); return initialTool ? { active: initialTool, layout: activateTab(state.layout, initialTool) } : state })
  const [focused, setFocused] = useState(() => groups(state.layout).find(g => g.tabs.includes(state.active))?.id ?? groups(state.layout)[0].id)
  if (owner !== key) { const next = read(key); setOwner(key); setState(next); setFocused(groups(next.layout)[0].id) }
  const scope = 'sidebar-tools'
  const focusAfterDrop = useRef(false)
  const targets = useMemo(() => new SessionViewTargets(scope), [scope])
  const targetFor = targets.get
  useEffect(() => { try { localStorage.setItem(key, JSON.stringify(state)) } catch { console.warn('Dock layout save failed: browser storage unavailable') } }, [state, key])
  useEffect(() => {
    const capture = (event: Event) => { const detail = (event as CustomEvent).detail; if (detail.scope === workspace && detail.layout) detail.layout.dock = state }
    const apply = (event: Event) => { const detail = (event as CustomEvent).detail; const dock = detail.layout?.dock; if (detail.scope === workspace && dock) { const layout = normalizeLayout(dock.layout, [...dockTools], dock.active); setState({ layout, active: dockTools.includes(dock.active) ? dock.active : 'sessions' }); setFocused(groups(layout)[0].id) } }
    window.addEventListener('blerg-layout-capture', capture); window.addEventListener('blerg-layout-apply', apply)
    return () => { window.removeEventListener('blerg-layout-capture', capture); window.removeEventListener('blerg-layout-apply', apply) }
  }, [workspace, state])
  const select = (id: string) => {
    if (!dockTools.includes(id as DockTool)) return
    setState(s => ({ ...s, active: id, layout: phone ? s.layout : activateTab(s.layout, id) }))
    const group = groups(state.layout).find(g => g.tabs.includes(id))
    if (group) setFocused(group.id)
  }
  const move = (id: string, before: string) => setState(s => ({ ...s, layout: mapGroups(s.layout, group => {
    if (!group.tabs.includes(id) || id === before) return group
    const tabs = group.tabs.filter(t => t !== id), index = tabs.indexOf(before)
    tabs.splice(index < 0 ? tabs.length : index, 0, id)
    return { ...group, tabs }
  }) }))
  const drop = (id: string, group: string, edge: DropEdge = 'center', before?: string) => {
    if (phone) return
    focusAfterDrop.current = true
    setState(s => {
      const layout = moveToGroup(s.layout, id, group, edge, `dock-${crypto.randomUUID()}`, before)
      return layout === s.layout ? s : { active: id, layout }
    })
  }
  const layout: TabLayout = phone ? { type: 'group', id: 'dock-phone', tabs: groups(state.layout).flatMap(g => g.tabs), active: state.active } : state.layout
  useLayoutEffect(() => {
    if (!focusAfterDrop.current) return
    focusAfterDrop.current = false
    const group = groups(state.layout).find(g => g.tabs.includes(state.active))!
    setFocused(group.id)
    document.getElementById(`session-tab-${state.active}`)?.focus({ preventScroll: true })
  }, [state])
  const tabs: GroupTabs = { tabs: dockTools.map(id => ({ id })), dragDisabled: phone, scope, layout, active: state.active, focused: phone ? 'dock-phone' : focused, phone: false, tabFocused: false, groups: groups(layout), targetFor, focusGroup: id => {
    setFocused(id)
    if (!phone) setState(s => { const active = groups(s.layout).find(g => g.id === id)?.active ?? s.active; return active === s.active ? s : { ...s, active } })
  }, drop, move, resize: (id, ratio) => setState(s => ({ ...s, layout: resizeSplit(s.layout, id, ratio) })) }
  return { tabs, select, mobile: phone }
}
