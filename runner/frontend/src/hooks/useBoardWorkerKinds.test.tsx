import { act, renderHook } from '@testing-library/react'
import { expect, it } from 'vitest'
import { useBoardWorkerKinds } from './useBoardWorkerKinds'
import { useSessionStore } from './useSessionStore'
import { makeSession } from '../test/fixtures'

it('selects only requested kinds, retains identity for telemetry and rebinds immediately', () => {
  useSessionStore.setState({ sessions: [makeSession({ id: 'worker', worker_kind: 'card' }), makeSession({ id: 'other', worker_kind: 'research' })] })
  let renders = 0
  const { result, rerender } = renderHook(({ children }) => { renders++; return useBoardWorkerKinds(children) }, { initialProps: { children: [{ id: 'worker' }] } })
  const initial = result.current, count = renders
  act(() => useSessionStore.setState(s => ({ sessions: s.sessions.map(session => ({ ...session, background_tasks: 3, title: 'Updated title' })) })))
  expect(result.current).toBe(initial)
  expect(renders).toBe(count)
  act(() => useSessionStore.setState(s => ({ sessions: s.sessions.map(session => session.id === 'worker' ? { ...session, worker_kind: 'review' } : session) })))
  expect(result.current).toEqual({ worker: 'review' })
  rerender({ children: [{ id: 'other' }] })
  expect(result.current).toEqual({ other: 'research' })
  act(() => useSessionStore.setState({ sessions: [] }))
  expect(result.current).toEqual({})
})
