import { act, fireEvent, renderHook } from '@testing-library/react'
import { beforeEach, expect, it, vi } from 'vitest'
import PaneProvider from '../components/PaneProvider'
import { usePaneLayout } from './usePaneLayout'
import { useContext } from 'react'
import { PaneContext } from '../panes/context'
import { sessionTabsKey, useSessionTabs } from './useSessionTabs'
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
