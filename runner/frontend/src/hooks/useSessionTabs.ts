import { useCallback, useContext, useEffect, useLayoutEffect, useMemo, useRef, useState, useSyncExternalStore } from 'react'
import { activateTab, groups, mapGroups, moveToGroup, normalizeLayout, removeTab, resizeSplit, type DropEdge, type TabLayout } from '../lib/sessionGroups'
import { PaneContext } from '../panes/context'
import { rememberViewScroll } from '../lib/moveSessionView'
import type { Ticket, HostedView, ViewerHostValue } from '@blerglab/chat'
import { layoutIdentity, type WorkspaceLayout } from '../lib/workspaceLayouts'
import { useToastStore } from './useToastStore'
import { loadSnapshot, saveSnapshot } from '../lib/snapshots'
export type ShellTabView = { privileged?: boolean; kind: 'shell'; shellId: string; handle?: string; label: string; defaultLabel?: string; runtime: 'host' | 'sandbox' | 'cluster'; folder: string }
export type ChatTabView = { kind: 'session-chat'; sessionId: string; label: string }
export type GraphTabView = { kind: 'branch-graph'; sessionId: string; memberId: string; label: string }
export type BlackboardPath = { level: 'project' | 'milestone' | 'thrust'; milestoneId?: string; thrustId?: string }
export type BlackboardView = { kind: 'blackboard'; blackboardId: string; path: BlackboardPath; label: string }
export type TicketTabView = { kind: 'ticket'; ticket: Ticket; sessionId: string; label: string }
export type QuestionTabView = { kind: 'question'; questionId: string; sessionId: string; label: string }
export type SessionView = ( QuestionTabView | { kind: 'needs-you' } | { kind: 'inbox'; itemId?: string } | TicketTabView | { kind: 'insights'; workspace?: string } | { kind: 'test-performance'; workspace?: string } | BlackboardView | HostedView | ShellTabView | ChatTabView | GraphTabView | { kind: 'board'; boardId: string; label: string; sessionId?: string; instance?: string; copy?: number; focusedColumn?: string }) & { instance?: string }
export type SessionTab = { id: string; view: SessionView; preview: boolean; unread?: boolean }
export type SessionLabelSource = { subscribe(listener: () => void): () => void; title(sessionId: string): string | undefined }
const noLabelSubscription = () => () => {}
type State = { version?: number; mainClosed?: boolean; tabs: SessionTab[]; active: string; unread: number; layout: TabLayout; focused: string }
function settleVisible(state: State, layout: TabLayout, focused = state.focused, single = false): State {
  const group = groups(layout).find(g => g.id === focused) ?? groups(layout)[0]
  const active = group.active
  const visible = single ? [active] : groups(layout).map(group => group.active)
  const tabs = state.tabs.some(tab => tab.unread && visible.includes(tab.id))
    ? state.tabs.map(tab => tab.unread && visible.includes(tab.id) ? { ...tab, unread: false } : tab) : state.tabs
  return { ...state, tabs, layout, focused: group.id, active, unread: visible.includes('chat') ? 0 : state.unread }
}
let fallbackGroupId = 0
const newGroupId = () => globalThis.crypto?.randomUUID?.() ?? `group-${Date.now().toString(36)}-${++fallbackGroupId}`
export const sessionTabsKey = (account: string, session: string) => `blerg-runner.tabs.${encodeURIComponent(account)}.${encodeURIComponent(session)}`
export function viewId(view: SessionView): string {
  const base = baseViewId(view)
  return view.kind !== 'question' && view.kind !== 'board' && view.kind !== 'insights' && view.kind !== 'test-performance' && view.kind !== 'inbox' && view.kind !== 'needs-you' && view.instance ? `${base}:copy:${view.instance}` : base
}
function baseViewId(view: SessionView): string {
  if (view.kind === 'question') return `question:${view.questionId}`
  if (view.kind === 'needs-you') return 'needs-you'
  if (view.kind === 'inbox') return 'inbox-reader'
  if (view.kind === 'ticket') return `ticket:${view.sessionId}:${view.ticket.board_id}:${view.ticket.key}`
  return view.kind === 'blackboard' ? `blackboard:${view.blackboardId}` : view.kind === 'test-performance' ? 'test-performance' : view.kind === 'insights' ? 'insights' : view.kind === 'board' ? `board:${view.boardId}${view.instance ? `:${view.instance}` : ''}` : view.kind === 'branch-graph' ? `branch-graph:${view.sessionId}:${view.memberId}` : view.kind === 'session-chat' ? `session-chat:${view.sessionId}` : view.kind === 'shell' ? `shell:${view.shellId}` : view.kind === 'workspace' ? `workspace:${view.path}` : view.kind === 'artifact' ? `artifact:${view.artifact.id}` : view.kind === 'message' ? `message:${view.messageId}` : view.kind === 'visual' ? `visual:${view.id}` : `commit:${view.sessionId ?? ''}:${view.sha}:${JSON.stringify(view.member ?? null)}`
}
export function tabLabel(view: SessionView, titles?: Record<string, string>): string {
  if (view.kind === 'question') return view.label
  if (view.kind === 'needs-you') return 'Needs you'
  if (view.kind === 'inbox') return 'Inbox'
  if (view.kind === 'ticket') return view.label
  if (view.kind === 'blackboard') return 'Blackboard'
  if (view.kind === 'test-performance') return 'Test Performance'
  if (view.kind === 'insights') return 'Insights'
  if (view.kind === 'branch-graph') return view.label
  if (view.kind === 'board') return `${view.label}${view.focusedColumn ? ` · ${view.focusedColumn}` : ''}${view.copy && view.copy > 1 ? ` · ${view.copy}` : ''}`
  if (view.kind === 'session-chat') return titles && Object.hasOwn(titles, view.sessionId) ? titles[view.sessionId] || 'Untitled chat' : view.label
  if (view.kind === 'message') return view.label
  if (view.kind === 'visual') return view.label
  return view.kind === 'shell' ? `${view.label}${view.privileged ? ' · Privileged · Not frozen at turn end' : ''}` : view.kind === 'workspace' ? view.path.split('/').at(-1)! : view.kind === 'artifact' ? view.artifact.name.split('/').at(-1)! : view.sha.slice(0, 8)
}
export function useSessionTitles(tabs: readonly SessionTab[], source?: SessionLabelSource): Record<string, string> {
  const snapshot = useCallback(() => JSON.stringify(tabs.flatMap(tab => {
    if (tab.view.kind !== 'session-chat') return []
    const title = source?.title(tab.view.sessionId)
    return title === undefined ? [] : [[tab.view.sessionId, title]]
  })), [tabs, source])
  const signature = useSyncExternalStore(source?.subscribe ?? noLabelSubscription, snapshot, snapshot)
  return useMemo(() => Object.fromEntries(JSON.parse(signature)), [signature])
}
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
  if ('instance' in value && value.instance !== undefined && typeof value.instance !== 'string') return false
  const view = value as Partial<SessionView> & { shellId?: unknown; label?: unknown; runtime?: unknown; folder?: unknown } & { path?: unknown; sha?: unknown; artifact?: { id?: unknown; name?: unknown; view?: unknown; size?: unknown }; messageId?: unknown; text?: unknown; workspaceLinks?: unknown } & { id?: unknown; visualKind?: unknown; source?: unknown }
  if (view.kind === 'question') return typeof view.questionId === 'string' && typeof view.sessionId === 'string' && typeof view.label === 'string'
  if (view.kind === 'ticket') return typeof view.sessionId === 'string' && typeof view.label === 'string' && 'ticket' in view && !!view.ticket && typeof view.ticket === 'object' && typeof view.ticket.key === 'string' && typeof view.ticket.board_id === 'string' && view.ticket.source === 'blerg-board'
  if (view.kind === 'blackboard') return typeof view.blackboardId === 'string' && typeof view.label === 'string' && !!view.path && typeof view.path === 'object' && 'level' in view.path && ['project', 'milestone', 'thrust'].includes(String(view.path.level)) && (view.path.level === 'project' || 'milestoneId' in view.path && typeof view.path.milestoneId === 'string') && (view.path.level !== 'thrust' || 'thrustId' in view.path && typeof view.path.thrustId === 'string')
  if (view.kind === 'needs-you') return true
  if (view.kind === 'inbox') return !('itemId' in view) || typeof view.itemId === 'string'
  if (view.kind === 'insights' || view.kind === 'test-performance') return !('workspace' in view) || typeof view.workspace === 'string'
  if (view.kind === 'branch-graph') return typeof view.sessionId === 'string' && typeof view.memberId === 'string' && typeof view.label === 'string'
  if (view.kind === 'board') return 'boardId' in view && typeof view.boardId === 'string' && typeof view.label === 'string' && (view.sessionId === undefined || typeof view.sessionId === 'string')
  if (view.kind === 'session-chat') return typeof view.sessionId === 'string' && typeof view.label === 'string'
  if (view.kind === 'shell') return typeof view.shellId === 'string' && (view.handle === undefined || typeof view.handle === 'string') && typeof view.label === 'string' && ['host', 'sandbox', 'cluster'].includes(String(view.runtime)) && typeof view.folder === 'string'
  if (view.kind === 'workspace') return typeof view.path === 'string'
  if (view.kind === 'commit') return typeof view.sha === 'string'
  if (view.kind === 'message') return typeof view.messageId === 'string' && typeof view.text === 'string' && typeof view.label === 'string' && (view.workspaceLinks === undefined || typeof view.workspaceLinks === 'boolean')
  if (view.kind === 'visual') return typeof view.id === 'string' && (view.visualKind === 'mermaid' || view.visualKind === 'vega-lite' || view.visualKind === 'dashboard') && typeof view.source === 'string' && typeof view.label === 'string'
  return view.kind === 'artifact' && !!view.artifact && typeof view.artifact.id === 'string' && typeof view.artifact.name === 'string' && typeof view.artifact.view === 'string' && typeof view.artifact.size === 'number'
}
function closedShells(key: string): Set<string> {
  try { const value: unknown = JSON.parse(localStorage.getItem(`${key}.closed-shells`) ?? '[]'); return new Set(Array.isArray(value) ? value.filter((id): id is string => typeof id === 'string') : []) } catch { return new Set() }
}
function read(key: string, phone: boolean, account: string, session: string): State {
  try {
    const saved = JSON.parse(localStorage.getItem(key) ?? 'null') as State | null
    const closed = closedShells(key)
    // A message or chart/diagram tab's own body never lives in this saved blob (snapshots.ts,
    // BLG-449/BLG-451): it is resolved from the bounded snapshot store here, by the same id
    // Duplicate shares. One that was never saved (over the cap at open time) or has since been
    // evicted is dropped rather than restored empty — the same fate validView already gives any
    // other broken saved tab.
    const tabs = (saved?.tabs ?? [])
      .filter(t => t && !closed.has(t.id) && typeof t.id === 'string' && validView(t.view) && viewId(t.view) === t.id)
      .flatMap(t => {
        if (t.view.kind === 'message') {
          const body = loadSnapshot(account, session, t.view.messageId)
          return body === undefined ? [] : [{ ...t, view: { ...t.view, text: body, kept: true } }]
        }
        if (t.view.kind === 'visual') {
          const body = loadSnapshot(account, session, t.view.id)
          return body === undefined ? [] : [{ ...t, view: { ...t.view, source: body, kept: true } }]
        }
        return [t]
      })
    // Validate tab identities before reconciling the saved group tree.
    let restored = tabs.filter((t, i) => tabs.findIndex(x => x.id === t.id) === i).map(t => { const next = { ...t, view: t.view.kind === 'artifact' ? { ...t.view, autoplay: false } : t.view.kind === 'session-chat' && t.view.label === 'Untitled session' ? { ...t.view, label: 'Untitled chat' } : t.view, preview: t.view.kind === 'board' ? false : !!t.preview }; return next })
    const active = saved?.active === 'chat' || tabs.some(t => t.id === saved?.active) ? saved!.active : 'chat'
    // Older layouts always had Main; only an explicit new close intent removes it.
    const mainOpen = saved?.version !== 2 || saved.mainClosed !== true
    const layout = normalizeLayout(saved?.layout, [...(mainOpen ? ['chat'] : []), ...restored.map(t => t.id)], active)
    const previews = new Set<string>()
    restored = restored.map(tab => { const group = groups(layout).find(g => g.tabs.includes(tab.id))!.id; const preview = tab.preview && !previews.has(group); if (preview) previews.add(group); return { ...tab, preview } })
    return settleVisible({ tabs: restored, active, unread: 0, layout, focused: saved?.focused ?? groups(layout).find(g => g.active === active)?.id ?? 'main' }, layout, undefined, phone)
  } catch { return { tabs: [], active: 'chat', unread: 0, layout: { type: 'group', id: 'main', tabs: ['chat'], active: 'chat' }, focused: 'main' } }
}
/** Imperative DOM ownership, separate from the serializable React layout state. */
export class SessionViewTargets {
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
export function useSessionTabs(account: string, session: string, phone: boolean, labels?: SessionLabelSource, routerHistory?: { back(): boolean }) {
  const nativeHistory = phone && !routerHistory
  const historyReplacement = useRef(false)
  const consumeSheet = useCallback(() => {
    if (routerHistory && history.state?.blergSheet === 'explorer') {
      historyReplacement.current = true
      history.replaceState({ ...history.state, blergSheet: undefined }, '')
    }
  }, [routerHistory])
  const takeHistoryReplacement = useCallback(() => { const replace = historyReplacement.current; historyReplacement.current = false; return replace }, [])
  const key = sessionTabsKey(account, session)
  const [owned, setOwned] = useState(() => ({ key, state: read(key, phone, account, session) }))
  if (owned.key !== key) setOwned({ key, state: read(key, phone, account, session) })
  const state = owned.key === key ? owned.state : read(key, phone, account, session)
  const recovery = useRef<{ key: string; stack: { state: State; id: string }[] }>({ key, stack: [] })
  useLayoutEffect(() => { if (recovery.current.key !== key) recovery.current = { key, stack: [] } }, [key])
  const panes = useContext(PaneContext)
  const tabFocused = !!panes?.focused
  // Inventory consumers retain this callback across breakpoint and focus-mode changes.
  const singleView = useRef(phone || tabFocused)
  useLayoutEffect(() => { singleView.current = phone || tabFocused }, [phone, tabFocused])
  const settle = useCallback((state: State, layout: TabLayout, focused = state.focused) => settleVisible(state, layout, focused, singleView.current), [])
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
  const focusIntent = useRef<'content' | 'tab' | 'close' | 'preserve'>('content')
  const takeFocusIntent = useCallback(() => {
    const intent = focusIntent.current
    focusIntent.current = 'content'
    return intent
  }, [])
  const position = useRef({ depth: 0, maxDepth: 0 })
  const [target, setTarget] = useState<HTMLDivElement | null>(null)
  const resizeOnly = useRef(false)
  const pendingSave = useRef<{ key: string; session: string; state: State } | null>(null)
  const saveTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const flushSave = useCallback(() => {
    if (saveTimer.current !== null) clearTimeout(saveTimer.current)
    saveTimer.current = null
    const pending = pendingSave.current
    pendingSave.current = null
    if (pending) {
      // A message or chart/diagram tab's body lives in the bounded snapshot store, not here
      // (snapshots.ts): this blob must stay small regardless of how large a popped-out one was,
      // or a single multi-MB one would make this setItem throw and silently stop every tab from
      // persisting.
      const tabs = pending.state.tabs.map(t => t.view.kind === 'message' ? { ...t, view: { ...t.view, text: '', kept: undefined } } : t.view.kind === 'visual' ? { ...t, view: { ...t.view, source: '', kept: undefined } } : t)
      try {
        const previous = JSON.parse(localStorage.getItem(pending.key) ?? 'null')
        const panels: WorkspaceLayout['panels'] = {}
        window.dispatchEvent(new CustomEvent('blerg-layout-capture', { detail: { scope: pending.session, panels } }))
        const safeTabs = layoutIdentity(pending.state.tabs)
        const lastLayout = safeTabs.length ? { ...pending.state, tabs: safeTabs, panels, mainClosed: !groups(pending.state.layout).some(group => group.tabs.includes('chat')), savedAt: Date.now(), viewport: { width: window.innerWidth, height: window.innerHeight } } : previous?.lastLayout
        if (safeTabs.length) window.dispatchEvent(new CustomEvent('blerg-layout-capture', { detail: { scope: pending.session, panels, layout: lastLayout } }))
        localStorage.setItem(pending.key, JSON.stringify({ ...pending.state, tabs, version: 2, lastLayout, mainClosed: !groups(pending.state.layout).some(g => g.tabs.includes('chat')) }))
      } catch { console.warn('Workspace layout save failed: browser storage unavailable') }
    }
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
    pendingSave.current = { key, session, state: saved }
    if (!resizeOnly.current) flushSave()
    else if (saveTimer.current === null) saveTimer.current = setTimeout(flushSave, 200)
  }, [key, session, owned, flushSave, settle])
  // Flush the last pointer position even when navigation happens before the throttle fires.
  useEffect(() => flushSave, [key, flushSave])
  const select = useCallback((id: string, push = true, focus: 'content' | 'tab' = 'content') => {
    if (push) consumeSheet()
    focusIntent.current = focus
    if (focus === 'content') requestShellFocus(id)
    update(s => {
      const active = id === 'chat' || s.tabs.some(t => t.id === id) ? id : 'chat'
      const layout = active === 'chat' && !groups(s.layout).some(g => g.tabs.includes('chat'))
        ? mapGroups(s.layout, g => g.id === s.focused ? { ...g, tabs: [...g.tabs, 'chat'], active: 'chat' } : g)
        : activateTab(s.layout, active)
      return settle(s, layout, groups(layout).find(g => g.tabs.includes(active))?.id)
    })
    if (nativeHistory && push) { const depth = navigateTab(key, id); position.current = { depth, maxDepth: depth === position.current.depth ? position.current.maxDepth : depth } }
  }, [key, nativeHistory, update, requestShellFocus, settle, consumeSheet])
  useEffect(() => {
    if (!nativeHistory) return
    history.replaceState({ ...history.state, sessionTab: { key, id: state.active, depth: history.state?.sessionTab?.key === key ? history.state.sessionTab.depth ?? 0 : 0 } }, '')
    const depth = history.state.sessionTab.depth
    position.current = { depth, maxDepth: depth }
    // Establish the navigation owner only when its scope or breakpoint changes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key, nativeHistory])
  useEffect(() => {
    if (!nativeHistory) return
    const pop = (event: PopStateEvent) => {
      const tab = event.state?.sessionTab
      if (tab?.key !== key) return
      const previous = position.current.depth
      const direction = Math.sign(tab.depth - previous)
      const valid = groups(state.layout).some(g => g.tabs.includes(tab.id))
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
  }, [key, nativeHistory, select, state.active, state.tabs, state.layout])
  const open = useCallback((view: SessionView, keep = false) => {
    consumeSheet()
    focusIntent.current = 'content'
    if (view.kind === 'board' && !view.instance) view = { ...view, instance: newGroupId() }
    const id = viewId(view)
    requestShellFocus(id)
    // A popped-out message or chart/diagram is a snapshot the person asked to see by name: never
    // a silently replaced preview (BLG-449/BLG-451).
    keep = keep || view.kind === 'board' || view.kind === 'shell' || view.kind === 'message' || view.kind === 'visual' || phone && view.kind === 'session-chat'
    update(s => {
      const existing = s.tabs.find(t => t.id === id)
      const owner = existing ? groups(s.layout).find(g => g.tabs.includes(id))! : groups(s.layout).find(g => g.id === s.focused)!
      const replaced = view.kind !== 'shell' && !existing && s.tabs.find(t => t.preview && owner.tabs.includes(t.id) && t.id !== id)
      const boardId = view.kind === 'board' ? view.boardId : ''
      // Every snapshot tab already in the layout (message or chart/diagram alike — they share one
      // account-wide store and cap) is protected from eviction: losing one out from under an open
      // tab, just to make room for this one, would be worse than this one simply not surviving a
      // reload.
      const openSnapshotIds = new Set(s.tabs.flatMap(t => t.view.kind === 'message' ? [t.view.messageId] : t.view.kind === 'visual' ? [t.view.id] : []))
      const nextView = view.kind === 'board' && !view.copy ? { ...view, copy: Math.max(0, ...s.tabs.map(t => t.view.kind === 'board' && t.view.boardId === boardId ? t.view.copy ?? 1 : 0)) + 1 }
        : view.kind === 'message' ? { ...view, kept: saveSnapshot(account, session, view.messageId, view.text, openSnapshotIds) }
        : view.kind === 'visual' ? { ...view, kept: saveSnapshot(account, session, view.id, view.source, openSnapshotIds) }
        : view
      const tab = { id, view: nextView, preview: existing ? existing.preview && !keep : !keep }
      const tabs = existing ? s.tabs.map(t => t.id === id ? tab : t) : [...s.tabs.filter(t => t.id !== (replaced && replaced.id)), tab]
      const layout = mapGroups(s.layout, group => group.id === owner.id ? { ...group, tabs: existing ? group.tabs : [...group.tabs.filter(t => t !== (replaced && replaced.id)), id], active: id } : group)
      return settle({ ...s, tabs }, layout, owner.id)
    })
    if (nativeHistory) { const depth = navigateTab(key, id); position.current = { depth, maxDepth: depth === position.current.depth ? position.current.maxDepth : depth } }
  }, [key, phone, nativeHistory, update, requestShellFocus, settle, account, session, consumeSheet])
  const duplicate = useCallback((source: string) => {
    const tab = state.tabs.find(t => t.id === source)
    if (!tab || !['blackboard', 'board', 'branch-graph', 'workspace', 'artifact', 'commit', 'message', 'visual'].includes(tab.view.kind)) return
    const sourceBoard = tab.view.kind === 'board' ? tab.view.boardId : ''
    const view = { ...tab.view, instance: newGroupId(), ...(tab.view.kind === 'board' ? { copy: Math.max(0, ...state.tabs.map(t => t.view.kind === 'board' && t.view.boardId === sourceBoard ? t.view.copy ?? 1 : 0)) + 1 } : {}) }
    const id = viewId(view)
    if (view.kind === 'board') {
      const from = targets.scope + source, to = targets.scope + id
      try {
        for (const field of ['query', 'facets', 'release', 'card']) {
          const value = localStorage.getItem(`blerg-board:view:${from}:${field}`)
          if (value !== null) localStorage.setItem(`blerg-board:view:${to}:${field}`, value)
        }
        for (const prefix of ['blerg-board:focus:', 'blerg-board:tab-columns:']) {
          const value = localStorage.getItem(prefix + from)
          if (value !== null) localStorage.setItem(prefix + to, value)
        }
      } catch { /* A duplicate still opens without storage. */ }
    }
    open(view, true)
  }, [state.tabs, targets.scope, open])
  const keep = useCallback((id: string) => update(s => s.tabs.some(t => t.id === id && t.preview) ? { ...s, tabs: s.tabs.map(t => t.id === id ? { ...t, preview: false } : t) } : s), [update])
  const setShellLabel = useCallback((id: string, label: string) => update(s => ({ ...s, tabs: s.tabs.map(t => t.id === id && t.view.kind === 'shell' ? { ...t, view: { ...t.view, label } } : t) })), [update])
  // blackboardId is optional: most navigation (moving between project/milestone/thrust) keeps the
  // tab's existing blackboard; the empty-state screen (card BLG-450 phase 2) passes it once, to
  // switch the tab from its "no blackboard yet" sentinel to the one a person just created, without
  // opening a second tab.
  const navigateBlackboard = useCallback((id: string, path: BlackboardPath, label: string, blackboardId?: string) => update(s => ({ ...s, tabs: s.tabs.map(t => t.id === id && t.view.kind === 'blackboard' ? { ...t, view: { ...t.view, path, label, ...(blackboardId !== undefined ? { blackboardId } : {}) } } : t) })), [update])
  const setBoardLabel = useCallback((boardId: string, label: string) => update(s => s.tabs.some(t => t.view.kind === 'board' && t.view.boardId === boardId && t.view.label !== label) ? { ...s, tabs: s.tabs.map(t => t.view.kind === 'board' && t.view.boardId === boardId ? { ...t, view: { ...t.view, label } } : t) } : s), [update])
  const setQuestionLabel = useCallback((questionId: string, label: string) => update(s => s.tabs.some(t => t.view.kind === 'question' && t.view.questionId === questionId && t.view.label !== label) ? { ...s, tabs: s.tabs.map(t => t.view.kind === 'question' && t.view.questionId === questionId ? { ...t, view: { ...t.view, label } } : t) } : s), [update])
  const setBoardFocus = useCallback((id: string, focusedColumn: string) => update(s => s.tabs.some(t => t.id === id && t.view.kind === 'board' && t.view.focusedColumn !== focusedColumn) ? { ...s, tabs: s.tabs.map(t => t.id === id && t.view.kind === 'board' ? { ...t, view: { ...t.view, focusedColumn } } : t) } : s), [update])
  const requestShellRename = useCallback((id: string) => { window.dispatchEvent(new CustomEvent('blerg-rename-shell', { detail: { scope: targets.scope, id } })) }, [targets.scope])
  const shellClosers = useRef(new Map<string, () => void>())
  const registerShell = useCallback((id: string, end: () => void) => { shellClosers.current.set(id, end); return () => { shellClosers.current.delete(id) } }, [])
  const pendingShellEnds = useRef(new Map<string, { timer: ReturnType<typeof setTimeout>; finish(): void }>())
  useEffect(() => {
    const pending = pendingShellEnds.current
    const finish = () => { for (const entry of [...pending.values()]) entry.finish() }
    window.addEventListener('pagehide', finish)
    return () => { window.removeEventListener('pagehide', finish); finish() }
  }, [key])
  const cancelShellEnd = useCallback((id: string) => {
    const pending = pendingShellEnds.current.get(id)
    if (pending) { clearTimeout(pending.timer); pendingShellEnds.current.delete(id) }
  }, [])
  const closeTab = useCallback((id: string, focus: 'tab' | 'close' = 'close') => {
    focusIntent.current = focus
    if (tabFocused && id === state.active) panes?.restore()
    if (id.startsWith('shell:') && !pendingShellEnds.current.has(id)) {
      const end = shellClosers.current.get(id)
      const finish = () => {
        const pending = pendingShellEnds.current.get(id)
        if (!pending) return
        clearTimeout(pending.timer); pendingShellEnds.current.delete(id)
        closed.add(id); persistClosed(); (shellClosers.current.get(id) ?? end)?.()
      }
      pendingShellEnds.current.set(id, { timer: setTimeout(finish, 8000), finish })
    }
    update(s => {
      if (id !== 'chat' && !s.tabs.some(t => t.id === id)) return s
      return settle({ ...s, tabs: s.tabs.filter(t => t.id !== id) }, removeTab(s.layout, id))
    })
  }, [update, closed, persistClosed, tabFocused, state.active, panes, settle])
  // A toast owns only the tabs it closed. Later closes and new tabs remain independent.
  const restoreClosed = useCallback((previous: State, ids: string[]) => {
    if (recovery.current.key !== key) return
    for (const id of ids) { cancelShellEnd(id); closed.delete(id) }
    persistClosed()
    update(current => {
      const currentIds = groups(current.layout).flatMap(group => group.tabs)
      const allowed = [...new Set([...currentIds, ...ids])]
      const tabs = [...previous.tabs.filter(tab => allowed.includes(tab.id)).map(tab => current.tabs.find(t => t.id === tab.id) ?? { ...tab, preview: false }), ...current.tabs.filter(tab => !previous.tabs.some(t => t.id === tab.id))]
      const active = ids.includes(previous.active) ? previous.active : ids.at(-1) ?? current.active
      const layout = activateTab(normalizeLayout(previous.layout, allowed, active), active)
      return settle({ ...current, tabs }, layout, groups(layout).find(group => group.tabs.includes(active))?.id)
    })
  }, [key, cancelShellEnd, closed, persistClosed, update, settle])
  const reopenClosed = useCallback(() => {
    if (recovery.current.key !== key) return
    const entry = recovery.current.stack.pop()
    if (entry) restoreClosed(entry.state, [entry.id])
  }, [key, restoreClosed])
  const closeMany = useCallback((ids: string[], focus: 'tab' | 'close' = 'close', announce = true) => {
    const present = [...new Set(ids)].filter(id => groups(state.layout).some(group => group.tabs.includes(id)))
    if (!present.length) return
    const previous = state
    const entries: { state: State; id: string }[] = []
    let before = previous
    for (const id of present) {
      entries.push({ state: before, id })
      before = settle({ ...before, tabs: before.tabs.filter(tab => tab.id !== id) }, removeTab(before.layout, id))
      closeTab(id, focus)
    }
    recovery.current.stack = [...recovery.current.stack, ...entries].slice(-20)
    if (!announce) return
    useToastStore.getState().addToast({ title: present.length === 1 ? 'Tab closed' : `${present.length} tabs closed`, body: present.some(id => id.startsWith('shell:')) ? 'Shell closes in a few seconds.' : '', action: { label: 'Undo', onClick: () => {
      restoreClosed(previous, present)
      if (recovery.current.key === key) recovery.current.stack = recovery.current.stack.filter(entry => !entries.includes(entry))
    } }, ttlMs: 8000 })
  }, [state, closeTab, key, restoreClosed, settle])
  const close = useCallback((id: string, focus: 'tab' | 'close' = 'close') => closeMany([id], focus), [closeMany])
  const captureLayout = useCallback((): WorkspaceLayout => {
    const tabs = layoutIdentity(state.tabs)
    const panels: WorkspaceLayout['panels'] = {}
    const saved: WorkspaceLayout = { mainClosed: !groups(state.layout).some(group => group.tabs.includes('chat')), savedAt: Date.now(), viewport: { width: window.innerWidth, height: window.innerHeight }, tabs, layout: normalizeLayout(state.layout, [...(groups(state.layout).some(g => g.tabs.includes('chat')) ? ['chat'] : []), ...tabs.map(t => t.id)], state.active), active: state.active, focused: state.focused, panels }
    window.dispatchEvent(new CustomEvent('blerg-layout-capture', { detail: { scope: session, panels, layout: saved } }))
    return saved
  }, [state, session])
  const applyLayout = useCallback((saved: WorkspaceLayout, announce = true) => {
    try {
      const tabs = layoutIdentity(saved.tabs).filter(t => validView(t.view) && viewId(t.view) === t.id).flatMap(t => {
        const v = t.view
        if (v.kind === 'message' || v.kind === 'visual') {
          const body = loadSnapshot(account, session, v.kind === 'message' ? v.messageId : v.id)
          return body === undefined ? [] : [{ ...t, view: v.kind === 'message' ? { ...v, text: body } : { ...v, source: body } }]
        }
        return [t]
      })
      const mainOpen = groups(saved.layout).some(g => g.tabs.includes('chat'))
      const ids = [...(mainOpen ? ['chat'] : []), ...tabs.map(t => t.id)]
      const layout = normalizeLayout(saved.layout, ids, saved.active)
      closeMany(groups(state.layout).flatMap(g => g.tabs).filter(id => !ids.includes(id)), 'close', announce)
      for (const tab of tabs) { cancelShellEnd(tab.id); closed.delete(tab.id) }
      persistClosed()
      panes?.restore()
      window.dispatchEvent(new CustomEvent('blerg-layout-apply', { detail: { scope: session, panels: saved.panels, layout: saved } }))
      update(s => settle({ ...s, tabs }, layout, saved.focused))
      return true
    } catch {
      console.warn('Workspace layout restore failed: invalid layout data')
      useToastStore.getState().addToast({ title: 'Could not restore layout', body: 'The saved layout is invalid.' })
      return false
    }
  }, [panes, update, settle, session, account, state.layout, closeMany, cancelShellEnd, closed, persistClosed])
  const getLastLayout = useCallback((): WorkspaceLayout | undefined => {
    try { return JSON.parse(localStorage.getItem(key) ?? 'null')?.lastLayout }
    catch { console.warn('Workspace layout read failed: invalid browser storage'); return undefined }
  }, [key])
  const restoreLayout = useCallback(() => {
    const saved = getLastLayout()
    if (saved) return applyLayout(saved)
    useToastStore.getState().addToast({ title: 'No previous layout', body: 'Open more tabs to save an automatic layout.' })
    return false
  }, [getLastLayout, applyLayout])
  const resetLayout = useCallback(() => {
    const previous = captureLayout()
    if (!applyLayout({ tabs: [], layout: { type: 'group', id: 'main', tabs: ['chat'], active: 'chat' }, active: 'chat', focused: 'main', panels: { explorer: { open: false, width: 320 }, 'session-panel': { open: false, width: 340 } }, dock: { active: 'sessions', layout: { type: 'group', id: 'dock-main', tabs: ['sessions', 'files', 'repositories', 'questions', 'inbox'], active: 'sessions' } } }, false)) return
    useToastStore.getState().addToast({ title: 'Layout reset', body: 'Back to Main only.', action: { label: 'Undo', onClick: () => { if (recovery.current.key === key) applyLayout(previous, false) } }, ttlMs: 8000 })
  }, [captureLayout, applyLayout, key])
  /** Restores the listed shells' tabs; `kept` are tabs of shells that are gone, kept to say so. */
  const restoreShellTabs = useCallback((candidates: ShellTabView[], kept: string[] = []) => update(s => {
    const views = candidates.filter(view => !closed.has(viewId(view)) && !pendingShellEnds.current.has(viewId(view)))
    const live = new Set([...views.map(viewId), ...kept.filter(id => !closed.has(id))])
    let layout = s.layout
    for (const tab of s.tabs) if (tab.view.kind === 'shell' && !live.has(tab.id)) layout = removeTab(layout, tab.id)
    const tabs = s.tabs.filter(t => t.view.kind !== 'shell' || live.has(t.id)).map(t => {
      const view = views.find(v => viewId(v) === t.id)
      return view ? { ...t, view, preview: false } : t
    })
    for (const view of views) if (!tabs.some(t => t.id === viewId(view))) {
      const id = viewId(view)
      tabs.push({ id, view, preview: false })
      layout = mapGroups(layout, g => g.id === groups(layout)[0].id ? { ...g, tabs: [...g.tabs, id], active: g.active || id } : g)
    }
    return settle({ ...s, tabs }, layout)
  }), [update, closed, settle])
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
  }, [closed, persistClosed, update, requestShellFocus, settle])
  useEffect(() => {
    // A close changes the displayed destination in place, rather than adding a visit.
    if (nativeHistory && history.state?.sessionTab?.key === key && !history.state?.blergSheet) {
      history.replaceState({ ...history.state, sessionTab: { ...history.state.sessionTab, id: state.active } }, '')
    }
  }, [key, nativeHistory, state.active])
  const back = useCallback(() => {
    if (phone && routerHistory?.back()) return
    if (nativeHistory && history.state?.sessionTab?.key === key && history.state.sessionTab.depth > 0) history.back()
    else select('chat', false)
  }, [key, nativeHistory, select, phone, routerHistory])
  const move = useCallback((id: string, before: string) => update(s => {
    const tab = s.tabs.find(t => t.id === id)
    if ((!tab && id !== 'chat') || id === before) return s
    const tabs = s.tabs.filter(t => t.id !== id); const index = tabs.findIndex(t => t.id === before)
    if (tab) tabs.splice(index < 0 ? tabs.length : index, 0, { ...tab, preview: false })
    const layout = mapGroups(s.layout, group => {
      if (!group.tabs.includes(id)) return group
      const ids = group.tabs.filter(t => t !== id), at = ids.indexOf(before)
      ids.splice(at < 0 ? ids.length : at, 0, id)
      return { ...group, tabs: ids }
    })
    return { ...s, tabs, layout }
  }), [update])
  const chatOutputs = useRef(new Map<string, string>())
  const chatOutput = useCallback((id: string, signature: string) => {
    const identity = `${key}:${id}`, previous = chatOutputs.current.get(identity)
    chatOutputs.current.set(identity, signature)
    if (previous && previous !== signature) update(s => {
      const shown = (phone || tabFocused) ? [s.active] : groups(s.layout).map(group => group.active)
      return shown.includes(id) || !s.tabs.some(tab => tab.id === id && !tab.unread) ? s : { ...s, tabs: s.tabs.map(tab => tab.id === id ? { ...tab, unread: true } : tab) }
    })
  }, [key, update, phone, tabFocused])
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
  }, [phone, tabFocused, update, targetFor, requestShellFocus, settle])
  const split = useCallback((edge: 'right' | 'bottom') => drop(state.active, state.focused, edge), [drop, state.active, state.focused])
  const focusGroup = useCallback((id: string, intent: 'content' | 'preserve' = 'preserve') => {
    focusIntent.current = intent
    update(s => s.focused === id ? s : settle(s, s.layout, id))
  }, [update, settle])
  const nextGroup = useCallback((moveTab = false) => {
    const all = groups(state.layout), next = all[(all.findIndex(g => g.id === state.focused) + 1) % all.length]
    if (phone || all.length < 2) return
    if (moveTab) drop(state.active, next.id)
    else { focusIntent.current = 'content'; requestShellFocus(next.active); focusGroup(next.id, 'content') }
  }, [state.layout, state.focused, state.active, phone, drop, focusGroup, requestShellFocus])
  const resize = useCallback((id: string, ratio: number, dragging = false) => update(s => ({ ...s, layout: resizeSplit(s.layout, id, ratio) }), dragging), [update])
  // Stable portal containers preserve React state; TabMounts preserves scroll during DOM moves.
  useEffect(() => targets.prune(state.tabs), [targets, state.tabs])
  const allGroups = useMemo(() => groups(state.layout), [state.layout])
  const visible = useMemo(() => (phone || tabFocused) ? [state.active] : allGroups.map(g => g.active), [phone, tabFocused, state.active, allGroups])
  const chatTarget = targetFor('chat')
  const host = useMemo<ViewerHostValue>(() => ({ open, close, keep, active: state.active, views: state.tabs.filter((t): t is SessionTab & { view: HostedView } => t.view.kind !== 'inbox' && t.view.kind !== 'needs-you' && t.view.kind !== 'ticket' && t.view.kind !== 'question' && t.view.kind !== 'blackboard' && t.view.kind !== 'insights' && t.view.kind !== 'test-performance' && t.view.kind !== 'shell' && t.view.kind !== 'session-chat' && t.view.kind !== 'board' && t.view.kind !== 'branch-graph'), target, targetFor, chatTarget, visible, agentOutput }), [open, close, keep, state.active, state.tabs, target, targetFor, chatTarget, visible, agentOutput])
  return { ...state, takeHistoryReplacement, getLastLayout, captureLayout, applyLayout, restoreLayout, resetLayout, reopenClosed, closeMany, sessionId: session, navigateBlackboard, setBoardFocus, setBoardLabel, setQuestionLabel, setShellLabel, requestShellRename, labels, tabFocused, toggleTabFocus: panes?.toggle, shellFocus, shellCount, setShellCount, closedShellIds, isShellClosed, reconcileClosedShells, registerShell, restoreShellTabs, replaceShellTab, rememberClosedShell, scope: targets.scope, phone, chatOutput, groups: allGroups, visible, targetFor, drop, split, focusGroup, nextGroup, resize, takeFocusIntent, host, select, back, open, duplicate, keep, close, move, setTarget }
}
export type SessionTabs = ReturnType<typeof useSessionTabs>
