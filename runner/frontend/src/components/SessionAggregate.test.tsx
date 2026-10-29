import { act, render, screen, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, expect, it } from 'vitest'
import { LiveSessionCard as SessionCard } from './SessionCard'
import SessionTiles from './SessionTiles'
import { makeSession } from '../test/fixtures'
import { useSessionStore } from '../hooks/useSessionStore'
import { useSessionListFilters } from '../lib/sessionSelection'
import { selectSessionAggregate } from '../lib/sessionAggregate'

afterEach(() => useSessionStore.setState({ sessions: [] }))

it.each([
  ['running', 'waiting', 'running'],
  ['idle', 'waiting', 'idle'],
  ['idle', 'starting', 'starting'],
  ['waiting', 'running', 'waiting'],
  ['running', 'idle', 'running'],
  ['idle', 'idle', 'idle'],
] as const)('self %s and child %s aggregate as %s', (self, child, expected) => {
  const parent = makeSession({ id: 'parent', status: self })
  const descendant = makeSession({ id: 'child', parent_session_id: parent.id, status: child })
  expect(selectSessionAggregate(parent, [parent, descendant]).state).toBe(expected)
})

it('bounds traversal of malformed cyclic ancestry', () => {
  const parent = makeSession({ id: 'parent', status: 'idle', parent_session_id: 'child' })
  const child = makeSession({ id: 'child', status: 'running', parent_session_id: parent.id })
  expect(selectSessionAggregate(parent, [parent, child]).state).toBe('running')
})

it.each(['cards', 'tiles'])('%s aggregate the whole tree, even when descendants are hidden by search', mode => {
  const parent = makeSession({ id: 'aggregate-parent', title: 'Parent work', status: 'idle' })
  const child = makeSession({ id: 'aggregate-child', title: 'Child work', parent_session_id: parent.id, status: 'idle' })
  const grandchild = makeSession({ id: 'aggregate-grandchild', parent_session_id: child.id, status: 'running' })
  useSessionListFilters.setState({ query: 'Parent work', focus: null, clusterConfigured: false })
  useSessionStore.setState({ sessions: [parent, child, grandchild], daemons: [] })
  render(<MemoryRouter>{mode === 'cards' ? <SessionCard session={parent} daemon={undefined} /> : <SessionTiles />}</MemoryRouter>)
  const glyph = () => screen.getByTestId('status-dot')
  const assertState = (word: string, text: string) => {
    expect(glyph()).toHaveAttribute('title', word)
    expect(glyph()).toHaveAccessibleName(word)
    expect(glyph().textContent).toBe(text)
    if (mode === 'tiles') expect(screen.getByRole('button', { name: /Parent work/ })).toHaveAccessibleName(new RegExp(word))
  }
  assertState('running', '')
  act(() => useSessionStore.setState({ sessions: [parent, { ...child, status: 'waiting' }, grandchild] }))
  assertState('running', '')
  act(() => useSessionStore.setState({ sessions: [parent, child, { ...grandchild, status: 'idle' }] }))
  assertState('idle', 'Zz')
  act(() => useSessionStore.setState({ sessions: [parent, child, { ...grandchild, status: 'idle', background_tasks: 2 }] }))
  assertState('working in background, 2 tasks', '⏱2')
  act(() => useSessionStore.setState({ sessions: [parent, { ...child, paused_reason: 'restart' }, grandchild] }))
  assertState('running', '')
  act(() => useSessionStore.setState({ sessions: [{ ...parent, status: 'waiting' }, child, grandchild] }))
  assertState('waiting', '?')
  act(() => useSessionStore.setState({ sessions: [{ ...parent, paused_reason: 'restart' }, child, grandchild] }))
  assertState('paused by a restart', '?')
  act(() => useSessionStore.setState({ sessions: [parent, { ...child, status: 'waiting', archived_at: '2026-10-26' }] }))
  assertState('idle', 'Zz')
  if (mode === 'tiles') expect(within(screen.getByRole('button', { name: /Parent work/ })).getByTestId('status-dot')).toBe(glyph())
})

it.each(['waiting', 'idle'] as const)('keeps a %s child paused by restart from raising parent attention', status => {
  const parent = makeSession({ id: 'parent', status: 'idle' })
  const child = makeSession({ id: 'child', parent_session_id: parent.id, status, paused_reason: 'restart' })
  expect(selectSessionAggregate(parent, [parent, child])).toMatchObject({ state: 'idle', paused: false, label: 'idle' })
  expect(selectSessionAggregate(child, [parent, child])).toMatchObject(status === 'waiting'
    ? { state: 'waiting-parent', paused: false, label: 'Waiting for its parent session' }
    : { state: 'idle', paused: false, label: 'idle' })
})

it('shows a child question as waiting on its live parent', () => {
  const parent = makeSession({ id: 'parent', status: 'idle' })
  const child = makeSession({ id: 'child', parent_session_id: 'parent', status: 'waiting' })
  expect(selectSessionAggregate(child, [parent, child])).toMatchObject({ state: 'waiting-parent', paused: false, label: 'Waiting for its parent session' })
})
