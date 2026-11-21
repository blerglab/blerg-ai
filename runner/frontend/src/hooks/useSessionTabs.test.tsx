import { act, fireEvent, renderHook } from '@testing-library/react'
import { beforeEach, expect, it, vi } from 'vitest'
import { sessionTabLabels } from '../lib/sessionTabLabels'
import { useSessionStore } from './useSessionStore'
import { makeSession } from '../test/fixtures'
import PaneProvider from '../components/PaneProvider'
import { usePaneLayout } from './usePaneLayout'
import { useContext } from 'react'
import { PaneContext } from '../panes/context'
import { sessionTabsKey, tabLabel, useSessionTabs, useSessionTitles, type SessionTab } from './useSessionTabs'
import { loadSnapshot } from '../lib/snapshots'
beforeEach(() => localStorage.clear())
it('replaces just the preview, keeps interacted views, reorders and closes beside Chat', () => {
  const { result } = renderHook(() => useSessionTabs('person', 'session', false))
  act(() => result.current.open({ kind: 'workspace', path: 'one.ts' }))
  act(() => result.current.open({ kind: 'workspace', path: 'two.ts' }))
  expect(result.current.tabs.map(t => t.id)).toEqual(['workspace:two.ts'])
  act(() => result.current.keep('workspace:two.ts'))
  act(() => result.current.open({ kind: 'commit', sha: 'abcdef12' }, true))
  act(() => result.current.open({ kind: 'workspace', path: 'three.md' }, true))
  expect(result.current.tabs).toHaveLength(3)
  act(() => result.current.move('workspace:three.md', 'workspace:two.ts'))
  expect(result.current.tabs[0].id).toBe('workspace:three.md')
  act(() => result.current.close('workspace:three.md'))
  expect(result.current.active).toBe('chat')
})
it('restores tabs, order, previews and active choice only for the same person and session', () => {
  const hook = renderHook(({ account, session }) => useSessionTabs(account, session, false), { initialProps: { account: 'a', session: 'one' } })
  act(() => hook.result.current.open({ kind: 'workspace', path: 'README.md' }, true))
  act(() => hook.result.current.open({ kind: 'workspace', path: 'script.sh' }))
  hook.unmount()
  const next = renderHook(({ account, session }) => useSessionTabs(account, session, false), { initialProps: { account: 'a', session: 'one' } })
  expect(next.result.current.tabs).toHaveLength(2)
  expect(next.result.current.active).toBe('workspace:script.sh')
  next.rerender({ account: 'b', session: 'one' })
  expect(next.result.current.tabs).toEqual([])
  next.rerender({ account: 'a', session: 'two' })
  expect(next.result.current.active).toBe('chat')
  next.rerender({ account: 'a', session: 'one' })
  expect(next.result.current.tabs).toHaveLength(2)
  expect(JSON.parse(localStorage.getItem(sessionTabsKey('b', 'one'))!).tabs).toEqual([])
})
it('badges new output only away from Chat and clears it on return', () => {
  const { result } = renderHook(() => useSessionTabs('person', 'session', false))
  act(() => result.current.host.agentOutput!('loaded'))
  act(() => result.current.open({ kind: 'workspace', path: 'a' }))
  act(() => result.current.host.agentOutput!('new output'))
  expect(result.current.unread).toBe(1)
  act(() => result.current.host.agentOutput!('new output'))
  expect(result.current.unread).toBe(1)
  act(() => result.current.select('chat'))
  expect(result.current.unread).toBe(0)
  act(() => result.current.host.agentOutput!('visible output'))
  expect(result.current.unread).toBe(0)
})
it('ignores corrupt persisted preferences', () => {
  localStorage.setItem(sessionTabsKey('a', 's'), '{broken')
  expect(renderHook(() => useSessionTabs('a', 's', false)).result.current.active).toBe('chat')
})

it('rejects structurally invalid saved views before rendering a tab label', () => {
  localStorage.setItem(sessionTabsKey('a', 's'), JSON.stringify({ tabs: [{ id: 'workspace:123', view: { kind: 'workspace', path: 123 } }], active: 'workspace:123' }))
  const { result } = renderHook(() => useSessionTabs('a', 's', false))
  expect(result.current.tabs).toEqual([])
  expect(result.current.active).toBe('chat')
})
it('keeps Chat visible in another group, routes opens to focus and restores a nested layout on desktop after phone use', () => {
  const hook = renderHook(({ phone }) => useSessionTabs('person', 'split', phone), { initialProps: { phone: false } })
  const current = () => hook.result.current
  act(() => current().open({ kind: 'workspace', path: 'a' }, true))
  act(() => current().split('right'))
  expect(current().groups).toHaveLength(2)
  expect(current().visible).toEqual(['chat', 'workspace:a'])
  act(() => current().host.agentOutput!('first'))
  act(() => current().host.agentOutput!('new output'))
  expect(current().unread).toBe(0)
  act(() => current().open({ kind: 'workspace', path: 'b' }))
  act(() => current().split('bottom'))
  expect(current().groups).toHaveLength(3)
  const desktop = current().layout
  hook.rerender({ phone: true })
  expect(current().visible).toEqual(['workspace:b'])
  act(() => current().split('right'))
  expect(current().layout).toEqual(desktop)
  hook.rerender({ phone: false })
  expect(current().visible).toEqual(['chat', 'workspace:a', 'workspace:b'])
  act(() => current().close('workspace:b'))
  expect(current().groups).toHaveLength(2)
  hook.unmount()
  const restored = renderHook(() => useSessionTabs('person', 'split', false))
  expect(restored.result.current.groups).toHaveLength(2)
  expect(restored.result.current.visible).toEqual(['chat', 'workspace:a'])
})
it('uses one preview per group and preserves kept tabs and the other group when a preview is replaced', () => {
  const { result } = renderHook(() => useSessionTabs('person', 'previews', false))
  act(() => result.current.open({ kind: 'workspace', path: 'a' }))
  act(() => result.current.split('right'))
  act(() => result.current.open({ kind: 'workspace', path: 'b' }))
  act(() => result.current.focusGroup(result.current.groups[0].id))
  act(() => result.current.open({ kind: 'workspace', path: 'c' }))
  act(() => result.current.open({ kind: 'workspace', path: 'd' }))
  expect(result.current.tabs.map(tab => tab.id)).toEqual(['workspace:a', 'workspace:b', 'workspace:d'])
  expect(result.current.groups[1].tabs).toEqual(['workspace:a', 'workspace:b'])
  act(() => result.current.select('workspace:a'))
  expect(result.current.groups[1].tabs).toEqual(['workspace:a', 'workspace:b'])
  expect(result.current.tabs.some(tab => tab.id === 'workspace:b')).toBe(true)
})

it('splits on plain HTTP without crypto.randomUUID and creates distinct group ids', () => {
  const uuid = Object.getOwnPropertyDescriptor(crypto, 'randomUUID')
  Object.defineProperty(crypto, 'randomUUID', { configurable: true, value: undefined })
  try {
    const { result } = renderHook(() => useSessionTabs('person', 'http', false))
    act(() => result.current.open({ kind: 'workspace', path: 'a' }, true))
    act(() => result.current.split('right'))
    act(() => result.current.focusGroup('main'))
    act(() => result.current.open({ kind: 'workspace', path: 'b' }, true))
    act(() => result.current.split('bottom'))
    expect(result.current.groups).toHaveLength(3)
    expect(new Set(result.current.groups.map(g => g.id)).size).toBe(3)
  } finally { if (uuid) Object.defineProperty(crypto, 'randomUUID', uuid); else delete (crypto as Partial<Crypto>).randomUUID }
})

it('inserts an incoming tab at the requested tab position and keeps Chat pinned', () => {
  const { result } = renderHook(() => useSessionTabs('person', 'insert', false))
  for (const path of ['a', 'b', 'c']) act(() => result.current.open({ kind: 'workspace', path }, true))
  act(() => result.current.split('right'))
  act(() => result.current.drop('workspace:c', 'main', 'center', 'workspace:b'))
  expect(result.current.groups[0].tabs).toEqual(['chat', 'workspace:a', 'workspace:c', 'workspace:b'])
})

it('coalesces divider persistence and flushes the final ratio on unmount', () => {
  vi.useFakeTimers()
  // The storage actually in use: jsdom's, or the test setup's polyfill when Node's built-in Web
  // Storage leaves jsdom's unusable (Node 26 without --no-experimental-webstorage).
  const write = vi.spyOn(Object.getPrototypeOf(localStorage) as Storage, 'setItem')
  try {
    const { result, unmount } = renderHook(() => useSessionTabs('person', 'resize', false))
    act(() => result.current.open({ kind: 'workspace', path: 'a' }, true))
    act(() => result.current.split('right'))
    act(() => vi.advanceTimersByTime(250))
    write.mockClear()
    const id = result.current.layout.id
    for (let n = 0; n < 10; n++) act(() => result.current.resize(id, .51 + n / 100, true))
    expect(write).not.toHaveBeenCalled()
    act(() => vi.advanceTimersByTime(250))
    expect(write).toHaveBeenCalledTimes(1)
    act(() => result.current.resize(id, .7, true))
    unmount()
    expect(JSON.parse(localStorage.getItem(sessionTabsKey('person', 'resize'))!).layout.ratio).toBe(.7)
  } finally { write.mockRestore(); vi.useRealTimers() }
})
it('restores shell identities, ordering and split positions without exposing them to file viewers', () => {
  const shell = { kind: 'shell' as const, shellId: 'one', label: 'Shell', runtime: 'host' as const, folder: 'project' }
  const hook = renderHook(() => useSessionTabs('owner', 'shells', false))
  act(() => hook.result.current.open({ kind: 'workspace', path: 'notes.md' }))
  act(() => hook.result.current.open(shell))
  expect(hook.result.current.tabs).toHaveLength(2)
  expect(hook.result.current.tabs.find(t => t.id === 'shell:one')?.preview).toBe(false)
  expect(hook.result.current.host.views!.map(t => t.view.kind)).toEqual(['workspace'])
  act(() => hook.result.current.move('shell:one', 'workspace:notes.md'))
  act(() => hook.result.current.drop('shell:one', 'main', 'right'))
  const layout = hook.result.current.layout
  hook.unmount()
  const restored = renderHook(() => useSessionTabs('owner', 'shells', false))
  act(() => restored.result.current.restoreShellTabs([shell]))
  expect(restored.result.current.layout).toEqual(layout)
  expect(restored.result.current.active).toBe('shell:one')
  act(() => restored.result.current.restoreShellTabs([]))
  expect(restored.result.current.tabs.map(t => t.id)).toEqual(['workspace:notes.md'])
  expect(restored.result.current.groups).toHaveLength(1)
})
it('retains close intents through failed refresh/reload and clears them only after authoritative absence', () => {
  const shell = { kind: 'shell' as const, shellId: 'closed', label: 'Shell', runtime: 'host' as const, folder: 'project' }
  const hook = renderHook(() => useSessionTabs('owner', 'close-intent', false))
  act(() => hook.result.current.open(shell))
  act(() => hook.result.current.close('shell:closed'))
  hook.unmount()
  const restored = renderHook(() => useSessionTabs('owner', 'close-intent', false))
  expect(restored.result.current.isShellClosed('shell:closed')).toBe(true)
  act(() => restored.result.current.reconcileClosedShells(['shell:closed']))
  act(() => restored.result.current.restoreShellTabs([shell]))
  expect(restored.result.current.tabs).toHaveLength(0)
  act(() => restored.result.current.reconcileClosedShells([]))
  expect(restored.result.current.isShellClosed('shell:closed')).toBe(false)
  act(() => restored.result.current.restoreShellTabs([shell]))
  expect(restored.result.current.tabs).toHaveLength(1)
  const other = renderHook(() => useSessionTabs('other-owner', 'close-intent', false))
  expect(other.result.current.closedShellIds()).toEqual([])
})

it('temporarily focuses one tab, restores ratios and the last-used group, and never persists focus mode', () => {
  const hook = renderHook(() => {
    const tabs = useSessionTabs('person', 'focus', false)
    return { ...tabs, panes: useContext(PaneContext)! }
  }, { wrapper: ({ children }) => <PaneProvider>{children}</PaneProvider> })
  const current = () => hook.result.current
  act(() => current().open({ kind: 'workspace', path: 'a' }, true))
  act(() => current().split('right'))
  act(() => current().open({ kind: 'workspace', path: 'c' }, true))
  act(() => current().split('bottom'))
  const nested = current().layout
  if (nested.type === 'split') act(() => current().resize(nested.second.id, .41))
  act(() => current().resize(current().layout.id, .63))
  const before = current().layout, focused = current().focused
  act(() => current().panes.toggle())
  expect(current().tabFocused).toBe(true)
  expect(current().visible).toEqual(['workspace:c'])
  act(() => current().panes.toggle())
  expect(current().layout).toEqual(before)
  expect(current().focused).toBe(focused)
  act(() => current().panes.toggle())
  act(() => current().select('chat'))
  expect(current().visible).toEqual(['chat'])
  act(() => current().open({ kind: 'workspace', path: 'b' }, true))
  expect(current().visible).toEqual(['workspace:b'])
  const lastGroup = current().focused
  expect(lastGroup).not.toBe(focused)
  act(() => current().panes.toggle())
  expect(current().focused).toBe(lastGroup)
  expect(current().active).toBe('workspace:b')
  const restoredLayout = current().layout
  expect(restoredLayout.type === 'split' && restoredLayout.ratio).toBe(.63)
  expect(current().layout.id).toBe(before.id)
  act(() => current().select('workspace:b'))
  act(() => current().panes.toggle())
  act(() => current().close(current().active))
  expect(current().tabFocused).toBe(false)
  const normalGroup = current().focused
  act(() => current().panes.toggle())
  hook.unmount()
  const reload = renderHook(() => useSessionTabs('person', 'focus', false), { wrapper: ({ children }) => <PaneProvider>{children}</PaneProvider> })
  expect(reload.result.current.tabFocused).toBe(false)
  expect(reload.result.current.focused).toBe(normalGroup)
  expect(reload.result.current.layout.type === 'split' && reload.result.current.layout.ratio).toBe(.63)
})

it('keeps pane preferences through focus and scopes the keyboard action to the current session', () => {
  let route = '/sessions/one'
  const hook = renderHook(() => ({
    tabs: useSessionTabs('person', route, false),
    panes: useContext(PaneContext)!,
    pane: usePaneLayout('focus-test', 'Toggle test pane', { min: 200, max: 800, default: 300 }, true),
  }), { wrapper: ({ children }) => <PaneProvider scope={route}>{children}</PaneProvider> })
  const key = { key: 'Enter', ctrlKey: true, shiftKey: true }
  act(() => hook.result.current.pane.resize(427))
  fireEvent.keyDown(window, { ...key, isComposing: true })
  fireEvent.keyDown(window, { ...key, repeat: true })
  expect(hook.result.current.panes.focused).toBe(false)
  const dialog = document.createElement('div')
  dialog.setAttribute('aria-modal', 'true'); document.body.append(dialog)
  fireEvent.keyDown(window, key)
  expect(hook.result.current.panes.focused).toBe(false)
  dialog.hidden = true
  fireEvent.keyDown(window, key)
  expect(hook.result.current.panes.focused).toBe(true)
  expect(hook.result.current.pane.open).toBe(false)
  expect(hook.result.current.pane.width).toBe(427)
  dialog.remove()
  fireEvent.keyDown(window, key)
  expect(hook.result.current.pane.open).toBe(true)
  expect(hook.result.current.pane.width).toBe(427)
  fireEvent.keyDown(window, key)
  route = '/sessions/two'; hook.rerender()
  expect(hook.result.current.panes.focused).toBe(false)
  expect(hook.result.current.pane.open).toBe(true)
  route = '/sessions/one'; hook.rerender()
  expect(hook.result.current.panes.focused).toBe(false)
  expect(hook.result.current.pane.width).toBe(427)
})
it('keeps side chats through file previews, splits and reload, and closes only their views', () => {
  const hook = renderHook(() => useSessionTabs('person', 'main', false))
  act(() => hook.result.current.open({ kind: 'session-chat', sessionId: 'child', label: 'Review' }, true))
  act(() => hook.result.current.open({ kind: 'workspace', path: 'README.md' }))
  expect(hook.result.current.tabs.map(t => t.id)).toEqual(['session-chat:child', 'workspace:README.md'])
  act(() => hook.result.current.drop('session-chat:child', hook.result.current.focused, 'right'))
  expect(hook.result.current.groups).toHaveLength(2)
  expect(hook.result.current.host.views.map(t => t.id)).toEqual(['workspace:README.md'])
  hook.unmount()
  const restored = renderHook(() => useSessionTabs('person', 'main', false))
  expect(restored.result.current.tabs[0].view).toEqual({ kind: 'session-chat', sessionId: 'child', label: 'Review' })
  act(() => restored.result.current.close('session-chat:child'))
  expect(restored.result.current.tabs.map(t => t.id)).toEqual(['workspace:README.md'])
  act(() => restored.result.current.close('chat'))
  expect(restored.result.current.groups.flatMap(g => g.tabs)).not.toContain('chat')
})
it('review: label subscriptions ignore sessions with no open chat tab', () => {
  useSessionStore.setState({ sessions: [makeSession({ id: 'side', title: 'Side' }), makeSession({ id: 'other', title: 'Other' })] })
  const renders = vi.fn()
  const tabs: SessionTab[] = [{ id: 'session-chat:side', view: { kind: 'session-chat', sessionId: 'side', label: 'Old title' }, preview: false }]
  const hook = renderHook(() => { renders(); return useSessionTitles(tabs, sessionTabLabels) })
  const before = renders.mock.calls.length
  act(() => useSessionStore.setState({ sessions: useSessionStore.getState().sessions.map(s => s.id === 'other' ? { ...s, title: 'Unrelated update' } : s) }))
  expect(renders).toHaveBeenCalledTimes(before)
  act(() => useSessionStore.setState({ sessions: useSessionStore.getState().sessions.map(s => s.id === 'side' ? { ...s, title: 'Current side title' } : s) }))
  expect(hook.result.current.side).toBe('Current side title')
})

it('final review: clears unread in every visible split without replacing unchanged tabs', () => {
  const hook = renderHook(() => useSessionTabs('person', 'visible-unread', false))
  act(() => hook.result.current.open({ kind: 'session-chat', sessionId: 'side', label: 'Side' }))
  act(() => hook.result.current.split('right'))
  const tabs = hook.result.current.tabs
  act(() => hook.result.current.focusGroup(hook.result.current.groups[0].id))
  expect.soft(hook.result.current.tabs).toBe(tabs)
  hook.unmount()
  const key = sessionTabsKey('person', 'visible-unread')
  const saved = JSON.parse(localStorage.getItem(key)!)
  saved.tabs[0].unread = true
  localStorage.setItem(key, JSON.stringify(saved))
  const restored = renderHook(() => useSessionTabs('person', 'visible-unread', false))
  expect(restored.result.current.visible).toContain('session-chat:side')
  expect(restored.result.current.tabs[0].unread).toBe(false)
})

it('migrates saved automatic Untitled session labels without changing chat identifiers', () => {
  localStorage.setItem(sessionTabsKey('owner', 'legacy-label'), JSON.stringify({ tabs: [{ id: 'session-chat:child', view: { kind: 'session-chat', sessionId: 'child', label: 'Untitled session' }, preview: false }], active: 'chat' }))
  const { result } = renderHook(() => useSessionTabs('owner', 'legacy-label', false))
  expect(result.current.tabs[0].id).toBe('session-chat:child')
  expect(result.current.tabs[0].view).toEqual({ kind: 'session-chat', sessionId: 'child', label: 'Untitled chat' })
})

it('keeps graph tabs per repository and restores them outside hosted file viewers', () => {
  const hook = renderHook(() => useSessionTabs('person', 'main', false))
  act(() => hook.result.current.open({ kind: 'branch-graph', sessionId: 'main', memberId: 'one', label: 'App graph' }, true))
  act(() => hook.result.current.open({ kind: 'branch-graph', sessionId: 'main', memberId: 'two', label: 'Library graph' }, true))
  expect(hook.result.current.tabs.map(t => t.id)).toEqual(['branch-graph:main:one', 'branch-graph:main:two'])
  expect(hook.result.current.host.views).toEqual([])
  hook.unmount()
  const restored = renderHook(() => useSessionTabs('person', 'main', false))
  expect(restored.result.current.active).toBe('branch-graph:main:two')
  expect(restored.result.current.tabs).toHaveLength(2)
})
it('treats boards as independent kept tabs in the same group tree and restores kept boards', () => {
 const hook = renderHook(() => useSessionTabs('person', 'session', false))
 act(() => hook.result.current.open({ kind: 'workspace', path: 'one.ts' }))
 act(() => hook.result.current.open({ kind: 'board', boardId: 'board-a', label: 'Release' }))
 expect(hook.result.current.tabs).toHaveLength(1)
 const boardTabId = hook.result.current.tabs[0].id
 expect(hook.result.current.tabs[0].preview).toBe(false)
 expect(hook.result.current.host.views).toEqual([])
 act(() => hook.result.current.keep(boardTabId))
 act(() => hook.result.current.open({ kind: 'workspace', path: 'two.ts' }))
 act(() => hook.result.current.drop(boardTabId, hook.result.current.groups[0].id, 'right'))
 expect(hook.result.current.groups).toHaveLength(2)
 hook.unmount()
 const restored = renderHook(() => useSessionTabs('person', 'session', false))
 expect(restored.result.current.tabs.find(t => t.id === boardTabId)?.preview).toBe(false)
 expect(restored.result.current.groups).toHaveLength(2)
})

it('keeps a closed Main closed across reload and can reopen it into an empty panel', () => {
  const hook = renderHook(() => useSessionTabs('person', 'closed-main', false))
  act(() => hook.result.current.close('chat'))
  expect(hook.result.current.active).toBe('')
  expect(hook.result.current.groups[0].tabs).toEqual([])
  act(() => hook.result.current.host.agentOutput!('first'))
  act(() => hook.result.current.host.agentOutput!('second'))
  expect(hook.result.current.unread).toBe(1)
  hook.unmount()
  const restored = renderHook(() => useSessionTabs('person', 'closed-main', false))
  expect(restored.result.current.groups[0].tabs).toEqual([])
  act(() => restored.result.current.select('chat'))
  expect(restored.result.current.visible).toContain('chat')
})
it('migrates legacy layouts without dropping Main or file tabs', () => {
  localStorage.setItem(sessionTabsKey('person', 'legacy'), JSON.stringify({ tabs: [{ id: 'workspace:a', view: { kind: 'workspace', path: 'a' }, preview: false }], active: 'workspace:a' }))
  const hook = renderHook(() => useSessionTabs('person', 'legacy', false))
  expect(hook.result.current.groups[0].tabs).toEqual(['chat', 'workspace:a'])
})
it('persists Main at its ordinary position and keeps the other tabs when Main closes', () => {
  const hook = renderHook(() => useSessionTabs('person', 'main-order', false))
  act(() => hook.result.current.open({ kind: 'workspace', path: 'a' }, true))
  act(() => hook.result.current.move('chat', ''))
  expect(hook.result.current.groups[0].tabs).toEqual(['workspace:a', 'chat'])
  hook.unmount()
  const next = renderHook(() => useSessionTabs('person', 'main-order', false))
  expect(next.result.current.groups[0].tabs).toEqual(['workspace:a', 'chat'])
  act(() => next.result.current.close('chat'))
  next.unmount()
  const closed = renderHook(() => useSessionTabs('person', 'main-order', false))
  expect(closed.result.current.groups[0].tabs).toEqual(['workspace:a'])
})

it('keeps one Insights view across splits and restores it as an ordinary closeable tab', () => {
  const hook = renderHook(() => useSessionTabs('person', 'insights', false))
  act(() => hook.result.current.open({ kind: 'insights' }, true))
  act(() => hook.result.current.split('right'))
  act(() => hook.result.current.open({ kind: 'insights' }, true))
  expect(hook.result.current.tabs.map(t => t.id)).toEqual(['insights'])
  expect(hook.result.current.host.views).toEqual([])
  expect(hook.result.current.visible).toContain('insights')
  hook.unmount()
  const restored = renderHook(() => useSessionTabs('person', 'insights', false))
  expect(restored.result.current.tabs[0].view).toEqual({ kind: 'insights' })
  act(() => restored.result.current.close('insights'))
  expect(restored.result.current.tabs).toEqual([])
})
it('opens independent kept board instances and restores a legacy board beside them', () => {
  localStorage.setItem(sessionTabsKey('a', 's'), JSON.stringify({ tabs: [{ id: 'board:b', view: { kind: 'board', boardId: 'b', label: 'Work' }, preview: true }], active: 'board:b' }))
  const hook = renderHook(() => useSessionTabs('a', 's', false))
  act(() => hook.result.current.open({ kind: 'board', boardId: 'b', label: 'Work' }))
  act(() => hook.result.current.open({ kind: 'board', boardId: 'b', label: 'Work' }))
  expect(new Set(hook.result.current.tabs.map(t => t.id)).size).toBe(3)
  expect(hook.result.current.tabs.every(t => !t.preview)).toBe(true)
  const ids = hook.result.current.tabs.map(t => t.id)
  hook.unmount()
  expect(renderHook(() => useSessionTabs('a', 's', false)).result.current.tabs.map(t => t.id)).toEqual(ids)
})

it('duplicates a board with its saved view and omits conversation duplication', () => {
  const { result } = renderHook(() => useSessionTabs('a', 's', false))
  act(() => result.current.open({ kind: 'board', boardId: 'b', label: 'Work' }))
  const source = result.current.active
  localStorage.setItem('blerg-board:focus:' + result.current.scope + source, 'doing')
  localStorage.setItem(`blerg-board:view:${result.current.scope}${source}:facets`, '{"q":"","tag":[],"type":["bug"],"priority":[],"size":[],"risk":[],"complexity":[]}')
  localStorage.setItem('blerg-board:tab-columns:' + result.current.scope + source, '["done"]')
  act(() => result.current.duplicate(source))
  expect(result.current.tabs).toHaveLength(2)
  expect(localStorage.getItem('blerg-board:focus:' + result.current.scope + result.current.active)).toBe('doing')
  // Duplicate copies a tab's filter (facets and hidden columns), not just its focused column (BLG-465).
  expect(localStorage.getItem(`blerg-board:view:${result.current.scope}${result.current.active}:facets`)).toContain('"bug"')
  expect(localStorage.getItem('blerg-board:tab-columns:' + result.current.scope + result.current.active)).toBe('["done"]')
  act(() => result.current.duplicate('chat'))
  expect(result.current.tabs).toHaveLength(2)
})

it('keeps the board opener stable for mounted subscriptions as tabs change', () => {
 const { result } = renderHook(() => useSessionTabs('a', 's', false))
 const open = result.current.open
 act(() => open({ kind: 'board', boardId: 'b', label: 'Work' }))
 expect(result.current.open).toBe(open)
 act(() => result.current.setBoardFocus(result.current.active, 'In progress'))
 expect(result.current.open).toBe(open)
})
it('uses a filename on both workspace and published-file tabs while retaining the full view path', () => {
  const path = 'docs/releases/notes.md'
  const workspace = { kind: 'workspace' as const, path }
  const published = { kind: 'artifact' as const, artifact: { id: 'notes', name: path, view: 'markdown', size: 42, content_type: 'text/markdown' } }
  expect(tabLabel(workspace)).toBe('notes.md')
  expect(tabLabel(published)).toBe('notes.md')
  expect(workspace.path).toBe(path)
  expect(published.artifact.name).toBe(path)
})

// A message popped out of the chat (BLG-449): an ordinary tab, keyed by the event it came from so
// reopening the same message focuses the same tab rather than piling up duplicates.
it('opens a popped-out message as its own tab, named from its own label', () => {
  const { result } = renderHook(() => useSessionTabs('a', 's', false))
  act(() => result.current.open({ kind: 'message', messageId: 'e1', text: '## Plan\n\nDetails.', label: 'Plan' }))
  expect(result.current.tabs.map(t => t.id)).toEqual(['message:e1'])
  expect(result.current.active).toBe('message:e1')
  expect(tabLabel(result.current.tabs[0].view)).toBe('Plan')
})

// The keep-open guard (review L5): open() forces keep=true for a message tab even when its
// caller passes none, the same way it already does for board and shell tabs. Without it, a
// message tab opens as a replaceable preview: popping out a second message would silently
// replace the first tab instead of opening its own, and Duplicate's own open(view, true) call
// for the copy would do the same to whichever message tab is still a preview.
it('never opens a popped-out message as a replaceable preview, even with no second argument to open()', () => {
  const { result } = renderHook(() => useSessionTabs('a', 's', false))
  act(() => result.current.open({ kind: 'message', messageId: 'e1', text: 'x', label: 'x' }))
  expect(result.current.tabs[0].preview).toBe(false)
  // The guard's real consequence: a second pop-out opens beside the first, not in place of it.
  act(() => result.current.open({ kind: 'message', messageId: 'e2', text: 'y', label: 'y' }))
  expect(result.current.tabs.map(t => t.id)).toEqual(['message:e1', 'message:e2'])
})

it('reopens the same popped-out message in its existing tab instead of duplicating it', () => {
  const { result } = renderHook(() => useSessionTabs('a', 's', false))
  act(() => result.current.open({ kind: 'message', messageId: 'e1', text: 'first look', label: 'Chat · now' }, true))
  act(() => result.current.open({ kind: 'workspace', path: 'other.ts' }, true))
  act(() => result.current.open({ kind: 'message', messageId: 'e1', text: 'first look', label: 'Chat · now' }))
  expect(result.current.tabs.filter(t => t.id === 'message:e1')).toHaveLength(1)
  expect(result.current.active).toBe('message:e1')
})

it('closes a popped-out message tab like any other, back to Chat', () => {
  const { result } = renderHook(() => useSessionTabs('a', 's', false))
  act(() => result.current.open({ kind: 'message', messageId: 'e1', text: 'x', label: 'x' }))
  act(() => result.current.close('message:e1'))
  expect(result.current.tabs.map(t => t.id)).toEqual([])
  expect(result.current.active).toBe('chat')
})

it('duplicates a popped-out message tab as its own independent copy', () => {
  const { result } = renderHook(() => useSessionTabs('a', 's', false))
  act(() => result.current.open({ kind: 'message', messageId: 'e1', text: 'x', label: 'x' }))
  const source = result.current.active
  act(() => result.current.duplicate(source))
  expect(result.current.tabs).toHaveLength(2)
  expect(new Set(result.current.tabs.map(t => t.id)).size).toBe(2)
  expect(result.current.tabs.every(t => t.view.kind === 'message')).toBe(true)
})

it('keeps a popped-out message tab across a reload', () => {
  const hook = renderHook(() => useSessionTabs('a', 's', false))
  act(() => hook.result.current.open({ kind: 'message', messageId: 'e1', text: 'kept across reload', label: 'Kept' }, true))
  hook.unmount()
  const next = renderHook(() => useSessionTabs('a', 's', false))
  expect(next.result.current.tabs).toHaveLength(1)
  expect(next.result.current.tabs[0].view).toEqual({ kind: 'message', messageId: 'e1', text: 'kept across reload', label: 'Kept', kept: true })
  expect(next.result.current.active).toBe('message:e1')
})

it('rejects a saved message view missing its text or label', () => {
  localStorage.setItem(sessionTabsKey('a', 's'), JSON.stringify({
    tabs: [{ id: 'message:e1', view: { kind: 'message', messageId: 'e1' }, preview: false }],
    active: 'message:e1',
  }))
  expect(renderHook(() => useSessionTabs('a', 's', false)).result.current.tabs).toEqual([])
})

// The snapshot store (BLG-449 review M2): a message tab's body is bounded and kept apart from the
// tab layout's own saved blob, so an oversized message can never break every tab from persisting.
it('never puts a message body in the tab layout\'s own saved blob', () => {
  const { result } = renderHook(() => useSessionTabs('a', 's', false))
  const text = 'a distinctive body that must stay out of the layout blob entirely'
  act(() => result.current.open({ kind: 'message', messageId: 'e1', text, label: 'Note' }))
  expect(localStorage.getItem(sessionTabsKey('a', 's'))).not.toContain(text)
})

it('still opens an over-the-cap message for this session, marked as not kept past a reload', () => {
  const { result } = renderHook(() => useSessionTabs('a', 's', false))
  const huge = 'x'.repeat(1_100_000) // over the 1 MB per-snapshot cap (chars * 2 bytes)
  const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
  act(() => result.current.open({ kind: 'message', messageId: 'e1', text: huge, label: 'Huge' }))
  expect(result.current.tabs).toHaveLength(1)
  expect(result.current.tabs[0].view).toMatchObject({ kind: 'message', text: huge, kept: false })
  warn.mockRestore()
})

it('a message too large to keep does not survive a reload', () => {
  const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
  const hook = renderHook(() => useSessionTabs('a', 's', false))
  const huge = 'x'.repeat(1_100_000)
  act(() => hook.result.current.open({ kind: 'message', messageId: 'e1', text: huge, label: 'Huge' }, true))
  hook.unmount()
  const next = renderHook(() => useSessionTabs('a', 's', false))
  expect(next.result.current.tabs).toEqual([]) // dropped, like any other tab whose saved view no longer resolves
  warn.mockRestore()
})

it('Duplicate shares the one stored snapshot by id instead of writing a second copy', () => {
  const { result } = renderHook(() => useSessionTabs('a', 's', false))
  act(() => result.current.open({ kind: 'message', messageId: 'e1', text: 'shared body', label: 'Shared' }))
  act(() => result.current.duplicate('message:e1'))
  expect(result.current.tabs).toHaveLength(2)
  const bodyKeys = Array.from({ length: localStorage.length }, (_, i) => localStorage.key(i)).filter((k): k is string => !!k && k.startsWith('blerg-runner.snapshot.a.s.'))
  expect(bodyKeys).toHaveLength(1) // one stored body, not one per tab
})

// BLG-451: an inline chart/diagram's popped-out tab reuses the exact same bounded snapshot store
// as a popped-out message (snapshots.ts), not a second one.
it('never puts a chart/diagram source in the tab layout\'s own saved blob', () => {
  const { result } = renderHook(() => useSessionTabs('a', 's', false))
  const source = 'flowchart LR\n  A distinctive body that must stay out of the layout blob entirely'
  act(() => result.current.open({ kind: 'visual', id: 'v1', visualKind: 'mermaid', source, label: 'Diagram' }))
  expect(localStorage.getItem(sessionTabsKey('a', 's'))).not.toContain(source)
})

it('still opens an over-the-cap chart snapshot for this session, marked as not kept past a reload', () => {
  const { result } = renderHook(() => useSessionTabs('a', 's', false))
  const huge = 'x'.repeat(1_100_000) // over the 1 MB per-snapshot cap (chars * 2 bytes)
  const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
  act(() => result.current.open({ kind: 'visual', id: 'v1', visualKind: 'mermaid', source: huge, label: 'Huge' }))
  expect(result.current.tabs).toHaveLength(1)
  expect(result.current.tabs[0].view).toMatchObject({ kind: 'visual', source: huge, kept: false })
  warn.mockRestore()
})

it('a chart snapshot too large to keep does not survive a reload', () => {
  const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
  const hook = renderHook(() => useSessionTabs('a', 's', false))
  const huge = 'x'.repeat(1_100_000)
  act(() => hook.result.current.open({ kind: 'visual', id: 'v1', visualKind: 'mermaid', source: huge, label: 'Huge' }, true))
  hook.unmount()
  const next = renderHook(() => useSessionTabs('a', 's', false))
  expect(next.result.current.tabs).toEqual([]) // dropped, like any other tab whose saved view no longer resolves
  warn.mockRestore()
})

it('rejects a saved visual view missing its source or label', () => {
  localStorage.setItem(sessionTabsKey('a', 's'), JSON.stringify({
    tabs: [{ id: 'visual:v1', view: { kind: 'visual', id: 'v1', visualKind: 'mermaid' }, preview: false }],
    active: 'visual:v1',
  }))
  expect(renderHook(() => useSessionTabs('a', 's', false)).result.current.tabs).toEqual([])
})

it('Duplicate shares a chart snapshot\'s one stored body by id instead of writing a second copy', () => {
  const { result } = renderHook(() => useSessionTabs('a', 's', false))
  act(() => result.current.open({ kind: 'visual', id: 'v1', visualKind: 'mermaid', source: 'shared body', label: 'Shared' }))
  act(() => result.current.duplicate('visual:v1'))
  expect(result.current.tabs).toHaveLength(2)
  const bodyKeys = Array.from({ length: localStorage.length }, (_, i) => localStorage.key(i)).filter((k): k is string => !!k && k.startsWith('blerg-runner.snapshot.a.s.'))
  expect(bodyKeys).toHaveLength(1) // one stored body, not one per tab
})

// A message and a chart/diagram snapshot share one account-wide index and cap (snapshots.ts): the
// set of ids a save must never evict spans both kinds of currently open tab, not just the kind
// being saved right now -- an open chart tab must not be silently dropped to make room for a
// later message snapshot (or vice versa).
it('does not evict an open chart tab\'s snapshot to make room for later message snapshots', () => {
  const body = (n: number) => `${n}${'x'.repeat(449_990)}` // ~900,000 bytes (chars * 2), just under the 1 MB per-snapshot cap
  const { result } = renderHook(() => useSessionTabs('a', 's', false))
  act(() => result.current.open({ kind: 'visual', id: 'v1', visualKind: 'mermaid', source: body(0), label: 'Chart' }))
  expect(result.current.tabs[0].view).toMatchObject({ kind: 'visual', kept: true })
  // Seven more open tabs still leave room (8 * ~900 KB ~= 7.2 MB, under the 8 MB account cap); the
  // eighth message tips the account total over, with nothing left to evict but already-open tabs.
  for (let i = 1; i <= 7; i++) act(() => result.current.open({ kind: 'message', messageId: `m${i}`, text: body(i), label: `M${i}` }))
  act(() => result.current.open({ kind: 'message', messageId: 'm8', text: body(8), label: 'M8' }))
  // The eighth message legitimately doesn't survive a reload (no room without touching a
  // protected entry) -- but the chart tab opened first must still be exactly what was saved.
  expect(result.current.tabs.find(t => t.view.kind === 'message' && t.view.messageId === 'm8')?.view).toMatchObject({ kept: false })
  expect(loadSnapshot('a', 's', 'v1')).toBe(body(0))
})

it('opens one kept tab per question and focuses it again', () => {
 const { result } = renderHook(() => useSessionTabs('person', 'questions', false))
 const question = { kind: 'question' as const, questionId: 'q1', sessionId: 'questions', label: 'Question: Deploy to staging?' }
 expect(result.current.tabs).toHaveLength(0)
 act(() => result.current.open(question, true))
 act(() => result.current.open({ kind: 'workspace', path: 'other.md' }, true))
 act(() => result.current.open(question, true))
 expect(result.current.tabs.filter(tab => tab.view.kind === 'question')).toHaveLength(1)
 expect(result.current.active).toBe('question:q1')
 expect(result.current.tabs.find(tab => tab.id === 'question:q1')?.preview).toBe(false)
})

it('updates the question tab label after revision without creating another tab', () => {
 const {result}=renderHook(()=>useSessionTabs('person','revision',false))
 act(()=>result.current.open({kind:'question',questionId:'q1',sessionId:'revision',label:'Question: Old title'},true))
 act(()=>result.current.setQuestionLabel('q1','Question: Revised title'))
 expect(result.current.tabs).toHaveLength(1)
 expect(result.current.tabs[0].view).toMatchObject({label:'Question: Revised title'})
})

it('keeps and restores one Needs you workspace tab', () => {
 const { result, unmount } = renderHook(() => useSessionTabs('person', 'needs-tab', false))
 act(() => result.current.open({ kind: 'needs-you' }, true))
 act(() => result.current.open({ kind: 'needs-you' }, true))
 expect(result.current.tabs.filter(tab => tab.view.kind === 'needs-you')).toHaveLength(1)
 expect(result.current.active).toBe('needs-you')
 unmount()
 const restored = renderHook(() => useSessionTabs('person', 'needs-tab', false))
 expect(restored.result.current.tabs.find(tab => tab.id === 'needs-you')?.view).toEqual({ kind: 'needs-you' })
})

it('reopens bulk-closed tabs one at a time in reverse closing order', () => {
  const { result } = renderHook(() => useSessionTabs('owner', 'recover', false))
  act(() => result.current.open({ kind: 'insights' }, true))
  act(() => result.current.open({ kind: 'needs-you' }, true))
  act(() => result.current.closeMany(['insights', 'needs-you']))
  expect(result.current.tabs).toHaveLength(0)
  act(() => result.current.reopenClosed())
  expect(result.current.tabs.map(t => t.id)).toEqual(['needs-you'])
  act(() => result.current.reopenClosed())
  expect(result.current.tabs.map(t => t.id)).toEqual(['insights', 'needs-you'])
})

it('keeps a closing shell alive through Undo and ends it when the grace period expires', () => {
  vi.useFakeTimers()
  try {
    const { result } = renderHook(() => useSessionTabs('owner', 'shell-undo', false))
    const shell = { kind: 'shell' as const, shellId: 'one', label: 'Shell', runtime: 'host' as const, folder: '' }
    const end = vi.fn()
    act(() => { result.current.open(shell); result.current.registerShell('shell:one', end) })
    act(() => result.current.close('shell:one'))
    expect(end).not.toHaveBeenCalled()
    act(() => result.current.reopenClosed())
    act(() => vi.advanceTimersByTime(8000))
    expect(end).not.toHaveBeenCalled()
    expect(result.current.tabs.map(t => t.id)).toEqual(['shell:one'])
    act(() => result.current.close('shell:one'))
    act(() => vi.advanceTimersByTime(8000))
    expect(end).toHaveBeenCalledTimes(1)
  } finally { vi.useRealTimers() }
})

it('keeps the last nonempty layout through closing everything and a reload', () => {
  const first = renderHook(() => useSessionTabs('a', 'last', false))
  act(() => first.result.current.open({ kind: 'insights' }, true))
  act(() => first.result.current.split('right'))
  const layout = first.result.current.layout
  act(() => first.result.current.closeMany(['chat', 'insights']))
  expect(JSON.parse(localStorage.getItem(sessionTabsKey('a', 'last'))!).lastLayout.tabs.map((t: SessionTab) => t.id)).toEqual(['insights'])
  first.unmount()
  const next = renderHook(() => useSessionTabs('a', 'last', false))
  expect(next.result.current.tabs).toEqual([])
  act(() => next.result.current.restoreLayout())
  expect(next.result.current.tabs.map(t => t.id)).toEqual(['insights'])
  expect(next.result.current.layout).toEqual(layout)
})

it('saves explorer open state and width independently for each workspace', () => {
  const bounds = { min: 100, max: 800, default: 320 }
  const pane = renderHook(({ scope }) => usePaneLayout('explorer', 'Explorer', bounds, false, true, scope), { initialProps: { scope: 'one' } })
  act(() => pane.result.current.toggle())
  act(() => pane.result.current.resize(420))
  pane.rerender({ scope: 'two' })
  expect(pane.result.current.open).toBe(false)
  expect(pane.result.current.width).toBe(320)
  act(() => pane.result.current.resize(240))
  pane.rerender({ scope: 'one' })
  expect(pane.result.current.open).toBe(true)
  expect(pane.result.current.width).toBe(420)
})

it('an older close Undo restores only its own tabs and preserves newer opens', async () => {
  const { useToastStore } = await import('./useToastStore')
  useToastStore.setState({ toasts: [] })
  const { result } = renderHook(() => useSessionTabs('owner', 'independent-undo', false))
  act(() => result.current.open({ kind: 'insights' }, true))
  act(() => result.current.open({ kind: 'needs-you' }, true))
  act(() => result.current.close('insights'))
  const undo = useToastStore.getState().toasts.at(-1)!.action!.onClick
  act(() => result.current.close('needs-you'))
  act(() => result.current.open({ kind: 'inbox' }, true))
  act(undo)
  expect(result.current.tabs.map(t => t.id)).toEqual(['insights', 'inbox-reader'])
  act(() => result.current.reopenClosed())
  expect(result.current.tabs.map(t => t.id).sort()).toEqual(['inbox-reader', 'insights', 'needs-you'])
})

it('resets to Main with no panels open and offers one Undo that restores panel geometry', async () => {
  const { useToastStore } = await import('./useToastStore')
  useToastStore.setState({ toasts: [] })
  const { result } = renderHook(() => ({ tabs: useSessionTabs('owner', 'reset-panels', false), explorer: usePaneLayout('explorer', 'Explorer', { min: 100, max: 800, default: 320 }, true, true, 'reset-panels') }))
  act(() => result.current.explorer.resize(410))
  act(() => result.current.tabs.resetLayout())
  expect(result.current.explorer.open).toBe(false)
  expect(result.current.tabs.groups.flatMap(g => g.tabs)).toEqual(['chat'])
  expect(useToastStore.getState().toasts).toHaveLength(1)
  act(() => useToastStore.getState().toasts[0].action!.onClick())
  expect(result.current.explorer.open).toBe(true)
  expect(result.current.explorer.width).toBe(410)
})

it('keeps commit sides, commits, repositories and the working file in separate restorable tabs', () => {
  const first = renderHook(() => useSessionTabs('person', 'versions', false))
  const member = { member_id: 'repo', launch_generation: 'launch', generation: 2 }
  const base = { kind: 'workspace' as const, path: 'app.ts', ownerSessionId: 'channel' }
  const commit = { sha: 'abcdef1234567890', side: 'original' as const, sessionId: 'channel', member }
  act(() => {
    first.result.current.open(base, true)
    first.result.current.open({ ...base, commit }, true)
    first.result.current.open({ ...base, commit: { ...commit, side: 'changed' } }, true)
    first.result.current.open({ ...base, commit: { ...commit, sha: '123456abcdef7890' } }, true)
    first.result.current.open({ ...base, commit: { ...commit, member: { ...member, member_id: 'other' } } }, true)
  })
  expect(new Set(first.result.current.tabs.map(tab => tab.id)).size).toBe(5)
  const expected = first.result.current.tabs
  first.unmount()
  const restored = renderHook(() => useSessionTabs('person', 'versions', false))
  expect(restored.result.current.tabs).toEqual(expected)
  expect(tabLabel(expected[1].view)).toContain('original abcdef1')
  act(() => restored.result.current.close(expected[1].id))
  act(() => restored.result.current.reopenClosed())
  expect(restored.result.current.tabs.some(tab => tab.id === expected[1].id && tab.view.kind === 'workspace' && tab.view.commit?.side === 'original')).toBe(true)
})
