import { expect, it, vi } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import { page } from 'vitest/browser'
import { MemoryRouter } from 'react-router-dom'
import { useExplorerLayout } from '../hooks/useExplorerLayout'
import { sessionTabsKey, useSessionTabs } from '../hooks/useSessionTabs'
import { useSessionStore } from '../hooks/useSessionStore'
import { makeSession } from '../test/fixtures'
import SessionTabBar from './SessionTabBar'
import '../index.css'
vi.mock('../ws', () => import('../test/wsMock'))
function Tabs() {
  const explorer = useExplorerLayout(false)
  const state = useSessionTabs('owner', 'main', explorer.mobile)
  return <SessionTabBar tabs={{ ...state, tabs: state.tabs.map(tab => ({ ...tab, unread: true })) }} explorer={explorer} shellOpen={false} />
}
it('keeps worker tabs quiet and channel unread dots visible at 390 px', async () => {
  await page.viewport(390, 844)
  localStorage.clear()
  useSessionStore.setState({ sessions: [makeSession({ id: 'main' }), makeSession({ id: 'worker', title: 'Review worker', parent_session_id: 'main', started_by: { kind: 'session' } }), makeSession({ id: 'channel', title: 'Design channel', parent_session_id: 'main' })] })
  localStorage.setItem(sessionTabsKey('owner', 'main'), JSON.stringify({ tabs: ['worker', 'channel'].map(id => ({ id: `session-chat:${id}`, view: { kind: 'session-chat', sessionId: id, label: id === 'worker' ? 'Review worker' : 'Design channel' }, preview: false })), active: 'chat' }))
  render(<MemoryRouter><Tabs /></MemoryRouter>)
  expect(within(screen.getByRole('tab', { name: 'Review worker' })).queryByLabelText(/New output/)).toBeNull()
  expect(within(screen.getByRole('tab', { name: /Design channel/ })).getByLabelText('New output in Design channel')).toBeVisible()
})
