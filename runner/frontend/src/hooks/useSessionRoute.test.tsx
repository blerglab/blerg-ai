import { renderHook } from '@testing-library/react'
import { beforeEach, expect, it, vi } from 'vitest'
import { useSessionRoute } from './useSessionRoute'
import type { SessionTabs } from './useSessionTabs'
import { makeSession } from '../test/fixtures'

const router = vi.hoisted(() => ({
  navigate: vi.fn(),
  location: { key: 'initial', pathname: '/sessions/root', state: null },
}))
vi.mock('react-router-dom', () => ({
  useNavigate: () => router.navigate,
  useLocation: () => router.location,
  useNavigationType: () => 'POP',
}))

beforeEach(() => { router.navigate.mockClear(); router.location = { key: 'initial', pathname: '/sessions/root', state: null } })

it('preserves distinct file history depths while router commits are pending', () => {
  const scope = 'workspace-tabs'
  const route = makeSession({ id: 'root' })
  const tabs = {
    scope, sessionId: 'root', active: 'chat',
    tabs: [], groups: [{ tabs: ['chat', 'workspace:README.md', 'workspace:package.json'] }],
    open: vi.fn(), select: vi.fn(), takeHistoryReplacement: () => true,
  } as unknown as SessionTabs
  const { rerender } = renderHook(() => useSessionRoute(tabs, route))
  tabs.active = 'workspace:README.md'
  rerender()
  tabs.active = 'workspace:package.json'
  rerender()
  expect(router.navigate.mock.calls.map(([, options]) => options.state.workspaceTab)).toEqual([
    { scope, id: 'workspace:README.md', depth: 1 },
    { scope, id: 'workspace:package.json', depth: 2 },
  ])
})

it.each(['session-chat:channel', 'workspace:README.md'])('restores %s when returning to its workspace', active => {
  router.navigate.mockClear()
  router.location = { key: 'other', pathname: '/sessions/other', state: null }
  const tabs = {
    scope: 'other-tabs', sessionId: 'other', active: 'chat', tabs: [],
    groups: [{ tabs: ['chat'] }], open: vi.fn(), select: vi.fn(), takeHistoryReplacement: () => false,
  } as unknown as SessionTabs
  let route = makeSession({ id: 'other' })
  const { rerender } = renderHook(() => useSessionRoute(tabs, route))
  Object.assign(tabs, { scope: 'root-tabs', sessionId: 'root', active,
    tabs: [{ id: active, view: active.startsWith('session-chat') ? { kind: 'session-chat', sessionId: 'channel', label: 'Channel' } : { kind: 'workspace', path: 'README.md' } }],
    groups: [{ tabs: ['chat', active] }],
  })
  route = makeSession({ id: 'root' })
  router.location = { key: 'return', pathname: '/sessions/root', state: null }
  rerender()
  expect(tabs.select).not.toHaveBeenCalled()
  expect(router.navigate).toHaveBeenLastCalledWith(active.startsWith('session-chat') ? '/sessions/channel' : '/sessions/root', {
    replace: true, state: { workspaceTab: { scope: 'root-tabs', id: active, depth: 0 } },
  })
})

it('keeps an explicit channel link authoritative over a remembered tab', () => {
  router.navigate.mockClear()
  router.location = { key: 'deep-link', pathname: '/sessions/linked', state: null }
  const tabs = { scope: 'root-tabs', sessionId: 'root', active: 'workspace:README.md',
    tabs: [{ id: 'workspace:README.md', view: { kind: 'workspace', path: 'README.md' } }],
    groups: [{ tabs: ['chat', 'workspace:README.md'] }], open: vi.fn(), select: vi.fn(),
  } as unknown as SessionTabs
  renderHook(() => useSessionRoute(tabs, makeSession({ id: 'linked', title: 'Linked channel' })))
  expect(tabs.open).toHaveBeenCalledWith({ kind: 'session-chat', sessionId: 'linked', label: 'Linked channel' }, true)
  expect(router.navigate).not.toHaveBeenCalled()
})

it('restores Main when the remembered channel is missing from a complete inventory', () => {
  const root = makeSession({ id: 'root' })
  const tabs = { scope: 'root-tabs', sessionId: 'root', active: 'session-chat:deleted',
    tabs: [{ id: 'session-chat:deleted', view: { kind: 'session-chat', sessionId: 'deleted', label: 'Deleted channel' } }],
    groups: [{ tabs: ['chat', 'session-chat:deleted'] }], open: vi.fn(), select: vi.fn(), closeMany: vi.fn(),
  } as unknown as SessionTabs
  renderHook(() => useSessionRoute(tabs, root, [root.id], true))
  expect(tabs.closeMany).toHaveBeenCalledWith(['session-chat:deleted'], 'close', false)
  expect(tabs.select).toHaveBeenCalledWith('chat')
  expect(router.navigate).toHaveBeenCalledWith('/sessions/root', { replace: true, state: { workspaceTab: { scope: 'root-tabs', id: 'chat', depth: 0 } } })
})

it('preserves unavailable background panes while Main is active', () => {
  const root = makeSession({ id: 'root' })
  const tabs = { scope: 'root-tabs', sessionId: 'root', active: 'chat',
    tabs: [{ id: 'session-chat:deleted', view: { kind: 'session-chat', sessionId: 'deleted', label: 'Deleted channel' } }],
    groups: [{ tabs: ['chat', 'session-chat:deleted'] }], open: vi.fn(), select: vi.fn(), closeMany: vi.fn(),
  } as unknown as SessionTabs
  renderHook(() => useSessionRoute(tabs, root, [root.id]))
  expect(tabs.closeMany).not.toHaveBeenCalled()
})

it.each([undefined, '2026-11-01T00:00:00Z'])('honors a deep link to a live or archived channel over a different remembered chat (%s)', archived_at => {
  const root = makeSession({ id: 'root' })
  const linked = makeSession({ id: 'linked', title: 'Linked channel', archived_at })
  router.location = { key: 'link', pathname: '/sessions/linked', state: null }
  const tabs = { scope: 'root-tabs', sessionId: 'root', active: 'session-chat:remembered',
    tabs: [{ id: 'session-chat:remembered', view: { kind: 'session-chat', sessionId: 'remembered', label: 'Remembered' } }],
    groups: [{ tabs: ['chat', 'session-chat:remembered', 'session-chat:linked'] }], open: vi.fn(), select: vi.fn(), closeMany: vi.fn(),
  } as unknown as SessionTabs
  const { rerender } = renderHook(() => useSessionRoute(tabs, linked, [root.id, linked.id, 'remembered'], true))
  expect(tabs.closeMany).not.toHaveBeenCalled()
  expect(tabs.open).toHaveBeenCalledWith({ kind: 'session-chat', sessionId: 'linked', label: 'Linked channel' }, true)
  tabs.active = 'session-chat:linked'
  tabs.tabs = [{ id: 'session-chat:linked', preview: false, view: { kind: 'session-chat', sessionId: 'linked', label: 'Linked channel' } }]
  rerender()
  expect(tabs.closeMany).not.toHaveBeenCalled()
  expect(tabs.select).not.toHaveBeenCalledWith('chat')
})

it.each([true, false])('waits for a complete inventory before dropping a remembered missing tab (arrives: %s)', arrives => {
  const root = makeSession({ id: 'root' })
  const child = makeSession({ id: 'child' })
  const tabs = { scope: 'root-tabs', sessionId: 'root', active: 'session-chat:child',
    tabs: [{ id: 'session-chat:child', view: { kind: 'session-chat', sessionId: 'child', label: 'Child' } }],
    groups: [{ tabs: ['chat', 'session-chat:child'] }], open: vi.fn(), select: vi.fn(), closeMany: vi.fn(),
  } as unknown as SessionTabs
  let complete = false
  let sessions = [root.id]
  const { rerender } = renderHook(() => useSessionRoute(tabs, root, sessions, complete))
  expect(tabs.closeMany).not.toHaveBeenCalled()
  expect(router.navigate).not.toHaveBeenCalled()
  sessions = arrives ? [root.id, child.id] : [root.id]
  complete = true
  rerender()
  if (arrives) {
    expect(tabs.closeMany).not.toHaveBeenCalled()
    expect(router.navigate).toHaveBeenCalledWith('/sessions/child', expect.objectContaining({ replace: true }))
  } else {
    expect(tabs.closeMany).toHaveBeenCalledWith(['session-chat:child'], 'close', false)
    expect(tabs.select).toHaveBeenCalledWith('chat')
  }
})
