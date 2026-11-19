import { act, render, screen } from '@testing-library/react'
import { it, expect, vi } from 'vitest'
import { MemoryRouter } from 'react-router-dom'
import SessionTabBar from './SessionTabBar'
import { sessionTabsKey, useSessionTabs } from '../hooks/useSessionTabs'
import { useExplorerLayout } from '../hooks/useExplorerLayout'
import { useSessionStore } from '../hooks/useSessionStore'
import { makeSession } from '../test/fixtures'

vi.mock('../ws', () => import('../test/wsMock'))

function Strip() {
  const tabs = useSessionTabs('', 'main', false)
  const explorer = useExplorerLayout(false)
  return <SessionTabBar tabs={tabs} explorer={explorer} shellOpen={false} />
}

it('keeps unread channels and orphan workers while suppressing workers with a live parent', () => {
  localStorage.clear()
  const parent = makeSession({ id: 'main' })
  const worker = makeSession({ id: 'worker', title: 'Worker', parent_session_id: parent.id, started_by: { kind: 'session' } })
  const orphan = makeSession({ id: 'orphan', title: 'Orphan', parent_session_id: 'gone', started_by: { kind: 'session' } })
  const channel = makeSession({ id: 'channel', title: 'Channel', parent_session_id: parent.id })
  useSessionStore.setState({ sessions: [parent, worker, orphan, channel] })
  localStorage.setItem(sessionTabsKey('', parent.id), JSON.stringify({ active: 'chat', tabs: [worker, orphan, channel].map(session => ({ id: `session-chat:${session.id}`, view: { kind: 'session-chat', sessionId: session.id, label: session.title }, preview: false, unread: true })) }))
  render(<MemoryRouter><Strip /></MemoryRouter>)
  expect(screen.queryByLabelText('New output in Worker')).toBeNull()
  expect(screen.getByLabelText('New output in Orphan')).toBeTruthy()
  expect(screen.getByLabelText('New output in Channel')).toBeTruthy()
  act(() => useSessionStore.setState(s => ({ sessions: s.sessions.map(session => session.id === parent.id ? { ...session, archived_at: '2026-10-20T12:00:00Z' } : session) })))
  expect(screen.getByLabelText('New output in Worker')).toBeTruthy()
  act(() => useSessionStore.setState(s => ({ sessions: s.sessions.map(session => session.id === parent.id ? parent : session) })))
  expect(screen.queryByLabelText('New output in Worker')).toBeNull()
})
