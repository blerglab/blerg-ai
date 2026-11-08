import { renderHook } from '@testing-library/react'
import { expect, it, vi } from 'vitest'
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
