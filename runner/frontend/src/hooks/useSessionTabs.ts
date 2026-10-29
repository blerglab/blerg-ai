import { useCallback, useContext, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { activateTab, groups, mapGroups, moveToGroup, normalizeLayout, removeTab, resizeSplit, type DropEdge, type TabLayout } from '../lib/sessionGroups'
import { PaneContext } from '../panes/context'
import { rememberViewScroll } from '../lib/moveSessionView'
import type { HostedView, ViewerHostValue } from '@blerglab/chat'
export type ShellTabView = { kind: 'shell'; shellId: string; label: string; runtime: 'host' | 'sandbox' | 'cluster'; folder: string }
export type SessionView = HostedView | ShellTabView
export type SessionTab = { id: string; view: SessionView; preview: boolean }
type State = { tabs: SessionTab[]; active: string; unread: number; layout: TabLayout; focused: string }
function settle(state: State, layout: TabLayout, focused = state.focused): State {
  const group = groups(layout).find(g => g.id === focused) ?? groups(layout)[0]
  const active = group.active
  return { ...state, layout, focused: group.id, active, unread: active === 'chat' ? 0 : state.unread }
}
let fallbackGroupId = 0
const newGroupId = () => globalThis.crypto?.randomUUID?.() ?? `group-${Date.now().toString(36)}-${++fallbackGroupId}`
export const sessionTabsKey = (account: string, session: string) => `blerg-runner.tabs.${encodeURIComponent(account)}.${encodeURIComponent(session)}`
export function viewId(view: SessionView): string {
  return view.kind === 'shell' ? `shell:${view.shellId}` : view.kind === 'workspace' ? `workspace:${view.path}` : view.kind === 'artifact' ? `artifact:${view.artifact.id}` : `commit:${view.sessionId ?? ''}:${view.sha}:${JSON.stringify(view.member ?? null)}`
}
export function tabLabel(view: SessionView): string { return view.kind === 'shell' ? view.label : view.kind === 'workspace' ? view.path.split('/').at(-1)! : view.kind === 'artifact' ? view.artifact.name : view.sha.slice(0, 8) }
function navigateTab(key: string, id: string) {
  const current = history.state?.sessionTab
  if (current?.key === key && current.id === id && !history.state?.blergSheet) return current.depth ?? 0
  const next = { ...history.state, blergSheet: undefined, sessionTab: { key, id, depth: (current?.key === key ? current.depth ?? 0 : 0) + 1 } }
  if (history.state?.blergSheet) history.replaceState(next, '')
  else history.pushState(next, '')
  return next.sessionTab.depth
}
function validView(value: unknown): value is SessionView {
  if (!value || typeof value !== 'object') return false
  const view = value as Partial<SessionView> & { shellId?: unknown; label?: unknown; runtime?: unknown; folder?: unknown } & { path?: unknown; sha?: unknown; artifact?: { id?: unknown; name?: unknown; view?: unknown; size?: unknown } }
  if (view.kind === 'shell') return typeof view.shellId === 'string' && typeof view.label === 'string' && ['host', 'sandbox', 'cluster'].includes(String(view.runtime)) && typeof view.folder === 'string'
  if (view.kind === 'workspace') return typeof view.path === 'string'
  if (view.kind === 'commit') return typeof view.sha === 'string'
  return view.kind === 'artifact' && !!view.artifact && typeof view.artifact.id === 'string' && typeof view.artifact.name === 'string' && typeof view.artifact.view === 'string' && typeof view.artifact.size === 'number'
}
function closedShells(key: string): Set<string> {
  try { const value: unknown = JSON.parse(localStorage.getItem(`${key}.closed-shells`) ?? '[]'); return new Set(Array.isArray(value) ? value.filter((id): id is string => typeof id === 'string') : []) } catch { return new Set() }
}
function read(key: string): State {
  try {
    const saved = JSON.parse(localStorage.getItem(key) ?? 'null') as State | null
    const closed = closedShells(key)
    const tabs = (saved?.tabs ?? []).filter(t => t && !closed.has(t.id) && typeof t.id === 'string' && validView(t.view) && viewId(t.view) === t.id)
    // Validate tab identities before reconciling the saved group tree.
    let restored = tabs.filter((t, i) => tabs.findIndex(x => x.id === t.id) === i).map(t => { const next = { ...t, view: t.view.kind === 'artifact' ? { ...t.view, autoplay: false } : t.view, preview: !!t.preview }; return next })
    const active = saved?.active === 'chat' || tabs.some(t => t.id === saved?.active) ? saved!.active : 'chat'
    const layout = normalizeLayout(saved?.layout, ['chat', ...restored.map(t => t.id)], active)
    const previews = new Set<string>()
    restored = restored.map(tab => { const group = groups(layout).find(g => g.tabs.includes(tab.id))!.id; const preview = tab.preview && !previews.has(group); if (preview) previews.add(group); return { ...tab, preview } })
    return settle({ tabs: restored, active, unread: 0, layout, focused: saved?.focused ?? groups(layout).find(g => g.active === active)?.id ?? 'main' }, layout)
  } catch { return { tabs: [], active: 'chat', unread: 0, layout: { type: 'group', id: 'main', tabs: ['chat'], active: 'chat' }, focused: 'main' } }
}
/** Imperative DOM ownership, separate from the serializable React layout state. */
class SessionViewTargets {
  readonly scope: string
  private elements = new Map<string, HTMLDivElement>()
  constructor(scope: string) { this.scope = scope }
  get = (id: string) => {
    let element = this.elements.get(id)
    if (!element) { element = document.createElement('div'); element.className = 'session-view-target'; element.dataset.viewId = id; this.elements.set(id, element) }
    return element
  }
  prune(tabs: SessionTab[]) {
    for (const [id, element] of this.elements) if (id !== 'chat' && !tabs.some(tab => tab.id === id)) { element.remove(); this.elements.delete(id) }
  }
}
export function useSessionTabs(account: string, session: string, phone: boolean) {
  const key = sessionTabsKey(account, session)
  const [owned, setOwned] = useState(() => ({ key, state: read(key) }))
  if (owned.key !== key) setOwned({ key, state: read(key) })
  const state = owned.key === key ? owned.state : read(key)
  const panes = useContext(PaneContext)
  const tabFocused = !!panes?.focused
  const focusSnapshot = useRef<{ key: string; focused: string } | null>(null)
  useLayoutEffect(() => {
    if (tabFocused) {
      if (focusSnapshot.current?.key !== key) focusSnapshot.current = { key, focused: state.focused }
    } else {
      const previous = focusSnapshot.current
      focusSnapshot.current = null
      if (previous?.key === key) setOwned(old => old.key === key ? { ...old, state: settle(old.state, old.state.layout) } : old)
    }
    // Snapshot only at the transition, never as the person selects another tab.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key, tabFocused])
  const targets = useMemo(() => new SessionViewTargets(key), [key])
  const targetFor = targets.get
  // A close must survive both an inventory still in flight and a failed DELETE/reload.
  // Only a successful inventory that no longer lists an identity clears its intent.
  const closed = useMemo(() => closedShells(key), [key])
  const persistClosed = useCallback(() => { try { localStorage.setItem(`${key}.closed-shells`, JSON.stringify([...closed])) } catch { /* Storage can be unavailable. */ } }, [key, closed])
  const closedShellIds = useCallback(() => [...closed], [closed])
  const isShellClosed = useCallback((id: string) => closed.has(id), [closed])
  const reconcileClosedShells = useCallback((ids: string[]) => {
    for (const id of closed) if (!ids.includes(id)) closed.delete(id)
    persistClosed()
  }, [closed, persistClosed])
  /** Records a shell as closed without a tab (the same intent a tab close records). */
  const rememberClosedShell = useCallback((id: string) => { closed.add(id); persistClosed() }, [closed, persistClosed])
  const [shellCount, setShellCount] = useState(0)
  const [requestedShellFocus, setShellFocus] = useState({ scope: targets, id: '', version: 0 })
  const shellFocus = requestedShellFocus.scope === targets ? requestedShellFocus : { id: '', version: 0 }
  const requestShellFocus = useCallback((id: string) => {
    if (!phone && id.startsWith('shell:')) setShellFocus(previous => ({ scope: targets, id, version: previous.version + 1 }))
  }, [targets, phone])
  const focusIntent = useRef<'content' | 'tab' | 'close'>('content')
  const takeFocusIntent = useCallback(() => {
    const intent = focusIntent.current
    focusIntent.current = 'content'
    return intent
  }, [])
  const position = useRef({ depth: 0, maxDepth: 0 })
  const [target, setTarget] = useState<HTMLDivElement | null>(null)
  const resizeOnly = useRef(false)
  const pendingSave = useRef<{ key: string; state: State } | null>(null)
  const saveTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const flushSave = useCallback(() => {
    if (saveTimer.current !== null) clearTimeout(saveTimer.current)
    saveTimer.current = null
    const pending = pendingSave.current
    pendingSave.current = null
    if (pending) { try { localStorage.setItem(pending.key, JSON.stringify(pending.state)) } catch { /* Storage can be unavailable. */ } }
  }, [])
  const update = useCallback((fn: (state: State) => State, resizing = false) => {
    resizeOnly.current = resizing
    setOwned(old => { if (old.key !== key) return old; const state = fn(old.state); return state === old.state ? old : { ...old, state } })
  }, [key])
  useEffect(() => {
    if (owned.key !== key) return
    const original = focusSnapshot.current
    // Reload and navigation always restore the normal focused group.
    const saved = original?.key === key ? settle(owned.state, owned.state.layout, original.focused) : owned.state
    pendingSave.current = { key, state: saved }
    if (!resizeOnly.current) flushSave()
    else if (saveTimer.current === null) saveTimer.current = setTimeout(flushSave, 200)
  }, [key, owned, flushSave])
  // Flush the last pointer position even when navigation happens before the throttle fires.
  useEffect(() => flushSave, [key, flushSave])
  const select = useCallback((id: string, push = true, focus: 'content' | 'tab' = 'content') => {
    focusIntent.current = focus
    if (focus === 'content') requestShellFocus(id)
    update(s => { const active = id === 'chat' || s.tabs.some(t => t.id === id) ? id : 'chat'; return settle(s, activateTab(s.layout, active), groups(s.layout).find(g => g.tabs.includes(active))?.id) })
    if (phone && push) { const depth = navigateTab(key, id); position.current = { depth, maxDepth: depth === position.current.depth ? position.current.maxDepth : depth } }
  }, [key, phone, update, requestShellFocus])
  useEffect(() => {
    if (!phone) return
    history.replaceState({ ...history.state, sessionTab: { key, id: state.active, depth: history.state?.sessionTab?.key === key ? history.state.sessionTab.depth ?? 0 : 0 } }, '')
    const depth = history.state.sessionTab.depth
    position.current = { depth, maxDepth: depth }
    // Establish the navigation owner only when its scope or breakpoint changes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key, phone])
  useEffect(() => {
    if (!phone) return
    const pop = (event: PopStateEvent) => {
      const tab = event.state?.sessionTab
      if (tab?.key !== key) return
      const previous = position.current.depth
      const direction = Math.sign(tab.depth - previous)
      const valid = tab.id === 'chat' || state.tabs.some(t => t.id === tab.id)
      // Browser history cannot delete individual entries. Traverse past closed/replaced
      // previews and duplicate destinations without exposing a stale view or a no-op Back.
      if (direction && (!valid || tab.id === state.active)) {
        const next = tab.depth + direction
        if (next >= 0 && next <= position.current.maxDepth) history.go(direction)
        else history.go(previous - tab.depth)
        return
      }
      position.current.depth = tab.depth
      select(valid ? tab.id : state.active, false)
    }
    window.addEventListener('popstate', pop)
    return () => window.removeEventListener('popstate', pop)
  }, [key, phone, select, state.active, state.tabs])
  const open = useCallback((view: SessionView, keep = false) => {
    focusIntent.current = 'content'
    const id = viewId(view)
    requestShellFocus(id)
    keep = keep || view.kind === 'shell'
    update(s => {
      const existing = s.tabs.find(t => t.id === id)
      const owner = existing ? groups(s.layout).find(g => g.tabs.includes(id))! : groups(s.layout).find(g => g.id === s.focused)!
      const replaced = view.kind !== 'shell' && !existing && s.tabs.find(t => t.preview && owner.tabs.includes(t.id) && t.id !== id)
      const tab = { id, view, preview: existing ? existing.preview && !keep : !keep }
      const tabs = existing ? s.tabs.map(t => t.id === id ? tab : t) : [...s.tabs.filter(t => t.id !== (replaced && replaced.id)), tab]
      const layout = mapGroups(s.layout, group => group.id === owner.id ? { ...group, tabs: existing ? group.tabs : [...group.tabs.filter(t => t !== (replaced && replaced.id)), id], active: id } : group)
      return settle({ ...s, tabs }, layout, owner.id)
    })
    if (phone) { const depth = navigateTab(key, id); position.current = { depth, maxDepth: depth === position.current.depth ? position.current.maxDepth : depth } }
  }, [key, phone, update, requestShellFocus])
  const keep = useCallback((id: string) => update(s => s.tabs.some(t => t.id === id && t.preview) ? { ...s, tabs: s.tabs.map(t => t.id === id ? { ...t, preview: false } : t) } : s), [update])
  const shellClosers = useRef(new Map<string, () => void>())
  const registerShell = useCallback((id: string, end: () => void) => { shellClosers.current.set(id, end); return () => { shellClosers.current.delete(id) } }, [])
  const close = useCallback((id: string, focus: 'tab' | 'close' = 'close') => {
    focusIntent.current = focus
    if (tabFocused && id === state.active) panes?.restore()
    if (id.startsWith('shell:')) { closed.add(id); persistClosed() }
    shellClosers.current.get(id)?.()
    update(s => {
      if (!s.tabs.some(t => t.id === id)) return s
      return settle({ ...s, tabs: s.tabs.filter(t => t.id !== id) }, removeTab(s.layout, id))
    })
  }, [update, closed, persistClosed, tabFocused, state.active, panes])
  /** Restores the listed shells' tabs; `kept` are tabs of shells that are gone, kept to say so. */
  const restoreShellTabs = useCallback((candidates: ShellTabView[], kept: string[] = []) => update(s => {
    const views = candidates.filter(view => !closed.has(viewId(view)))
    const live = new Set([...views.map(viewId), ...kept.filter(id => !closed.has(id))])
    let layout = s.layout
    for (const tab of s.tabs) if (tab.view.kind === 'shell' && !live.has(tab.id)) layout = removeTab(layout, tab.id)
    const tabs = s.tabs.filter(t => t.view.kind !== 'shell' || live.has(t.id)).map(t => {
      const view = views.find(v => viewId(v) === t.id)
      return view ? { ...t, view: { ...view, label: tabLabel(t.view) }, preview: false } : t
    })
    for (const view of views) if (!tabs.some(t => t.id === viewId(view))) {
      const id = viewId(view)
      tabs.push({ id, view, preview: false })
      layout = mapGroups(layout, g => g.id === groups(layout)[0].id ? { ...g, tabs: [...g.tabs, id] } : g)
    }
    return settle({ ...s, tabs }, layout)
  }), [update, closed])
  /**
   * A new shell takes a gone shell's tab: same position, same name, selected. The old shell is
   * recorded as closed, as a tab close records it, so an inventory that still lists it ends it
   * instead of bringing its tab back. The caller checks that the tab is still there.
   */
  const replaceShellTab = useCallback((oldId: string, view: ShellTabView) => {
    const id = viewId(view)
    closed.add(oldId); persistClosed()
    focusIntent.current = 'content'
    requestShellFocus(id)
    update(s => {
      const old = s.tabs.find(t => t.id === oldId)
      if (!old) return s
      const tabs = s.tabs.map(t => t.id === oldId ? { id, view: { ...view, label: tabLabel(old.view) }, preview: false } : t)
      const layout = mapGroups(s.layout, g => g.tabs.includes(oldId) ? { ...g, tabs: g.tabs.map(t => t === oldId ? id : t), active: id } : g)
      return settle({ ...s, tabs }, layout, groups(layout).find(g => g.tabs.includes(id))!.id)
    })
    // On a phone, the history entry follows the active tab in place (the effect below).
  }, [closed, persistClosed, update, requestShellFocus])
  useEffect(() => {
    // A close changes the displayed destination in place, rather than adding a visit.
    if (phone && history.state?.sessionTab?.key === key && !history.state?.blergSheet) {
      history.replaceState({ ...history.state, sessionTab: { ...history.state.sessionTab, id: state.active } }, '')
    }
  }, [key, phone, state.active])
  const back = useCallback(() => {
    if (history.state?.sessionTab?.key === key && history.state.sessionTab.depth > 0) history.back()
    else select('chat', false)
  }, [key, select])
  const move = useCallback((id: string, before: string) => update(s => {
    const tab = s.tabs.find(t => t.id === id)
    if (!tab || id === before) return s
    const tabs = s.tabs.filter(t => t.id !== id); const index = tabs.findIndex(t => t.id === before)
    tabs.splice(index < 0 ? tabs.length : index, 0, { ...tab, preview: false })
    const layout = mapGroups(s.layout, group => {
      if (!group.tabs.includes(id)) return group
      const ids = group.tabs.filter(t => t !== id), at = ids.indexOf(before)
      ids.splice(at < 0 ? ids.length : at, 0, id)
      return { ...group, tabs: ids }
    })
    return { ...s, tabs, layout }
  }), [update])
  const output = useRef({ key, signature: '' })
  const agentOutput = useCallback((signature: string) => {
    const previous = output.current
    output.current = { key, signature }
    if (previous.key === key && previous.signature && previous.signature !== signature) update(s => !((phone || tabFocused) ? [s.active] : groups(s.layout).map(g => g.active)).includes('chat') && !s.unread ? { ...s, unread: 1 } : s)
  }, [key, phone, tabFocused, update])
  const drop = useCallback((id: string, group: string, edge: DropEdge = 'center', before?: string) => {
    if (phone || tabFocused) return
    focusIntent.current = 'content'
    requestShellFocus(id)
    rememberViewScroll(targetFor(id))
    update(s => {
      const layout = moveToGroup(s.layout, id, group, edge, newGroupId(), before)
      if (layout === s.layout) return s
      return settle({ ...s, tabs: s.tabs.map(t => t.id === id ? { ...t, preview: false } : t) }, layout, groups(layout).find(g => g.tabs.includes(id))?.id)
    })
  }, [phone, tabFocused, update, targetFor, requestShellFocus])
  const split = useCallback((edge: 'right' | 'bottom') => drop(state.active, state.focused, edge), [drop, state.active, state.focused])
  const focusGroup = useCallback((id: string) => update(s => s.focused === id ? s : settle(s, s.layout, id)), [update])
  const nextGroup = useCallback((moveTab = false) => {
    const all = groups(state.layout), next = all[(all.findIndex(g => g.id === state.focused) + 1) % all.length]
    if (phone || all.length < 2) return
    if (moveTab) drop(state.active, next.id)
    else { focusIntent.current = 'content'; requestShellFocus(next.active); focusGroup(next.id) }
  }, [state.layout, state.focused, state.active, phone, drop, focusGroup, requestShellFocus])
  const resize = useCallback((id: string, ratio: number, dragging = false) => update(s => ({ ...s, layout: resizeSplit(s.layout, id, ratio) }), dragging), [update])
  // Stable portal containers preserve React state; TabMounts preserves scroll during DOM moves.
  useEffect(() => targets.prune(state.tabs), [targets, state.tabs])
  const allGroups = useMemo(() => groups(state.layout), [state.layout])
  const visible = useMemo(() => (phone || tabFocused) ? [state.active] : allGroups.map(g => g.active), [phone, tabFocused, state.active, allGroups])
  const chatTarget = targetFor('chat')
  const host = useMemo<ViewerHostValue>(() => ({ open, close, keep, active: state.active, views: state.tabs.filter((t): t is SessionTab & { view: HostedView } => t.view.kind !== 'shell'), target, targetFor, chatTarget, visible, agentOutput }), [open, close, keep, state.active, state.tabs, target, targetFor, chatTarget, visible, agentOutput])
  return { ...state, tabFocused, toggleTabFocus: panes?.toggle, shellFocus, shellCount, setShellCount, closedShellIds, isShellClosed, reconcileClosedShells, registerShell, restoreShellTabs, replaceShellTab, rememberClosedShell, scope: targets.scope, phone, groups: allGroups, visible, targetFor, drop, split, focusGroup, nextGroup, resize, takeFocusIntent, host, select, back, open, keep, close, move, setTarget }
}
export type SessionTabs = ReturnType<typeof useSessionTabs>
