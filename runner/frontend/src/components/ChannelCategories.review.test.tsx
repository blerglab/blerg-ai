import { beforeEach, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import SessionDockWindows from './SessionDockWindows'
import { makeSession } from '../test/fixtures'
import { useSessionStore } from '../hooks/useSessionStore'
import { useDecisionAttention } from '../hooks/useSessionAttention'
import { useMessageStore } from '../hooks/useMessageStore'
import { apiFetch } from '../apiFetch'
vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../apiFetch', () => ({ apiFetch: vi.fn() }))
beforeEach(() => { localStorage.clear(); useDecisionAttention.setState({ counts: {}, live: {}, reviews: {}, plans: {} }); useMessageStore.setState({ messages: [] }) })
function fixture() {
 let categories = [{ id: 'research', name: 'Research', sessions: ['worker', 'channel'] }, { id: 'build', name: 'Build', sessions: [] as string[] }]
 vi.mocked(apiFetch).mockImplementation(async (url, init) => {
  if (!url.endsWith('/categories')) return new Response('{}')
  if (init?.method === 'POST') {
   const req = JSON.parse(init.body as string)
   if (req.action === 'move') {
    const item = categories.find(c => c.id === req.id)!
    categories = categories.filter(c => c.id !== req.id)
    const index = categories.findIndex(c => c.id === req.before)
    categories.splice(index < 0 ? categories.length : index, 0, item)
   }
  }
  return new Response(JSON.stringify({ categories }))
 })
 useSessionStore.setState({ sessions: [makeSession({ id: 'main' }), makeSession({ id: 'worker', title: 'Review', parent_session_id: 'main', started_by: { kind: 'session' } }), makeSession({ id: 'channel', title: 'Design', parent_session_id: 'main', unread: true })] })
 const view = (selected = 'main') => <MemoryRouter><SessionDockWindows sessionId="main" selectedSessionId={selected} mobile files={null} /></MemoryRouter>
 return { view }
}
const research = () => screen.getByRole('treeitem', { name: /^Research(?:,|$)/ })
it('reveals selected, failed and pending-decision workers, but never counts waiting alone', async () => {
 const { view } = fixture(); const mounted = render(view())
 await screen.findByRole('treeitem', { name: /^Research(?:,|$)/ })
 fireEvent.click(research())
 expect(screen.queryByRole('treeitem', { name: 'Review' })).toBeNull()
 expect(research()).toHaveAccessibleDescription('2 running, unread output')
 expect(research().querySelector('.dock-group-spinner')).toBeVisible()
 expect(screen.getByRole('img', { name: 'Unread category output' })).toBeVisible()
 mounted.rerender(view('worker'))
 expect(screen.getByRole('treeitem', { name: 'Review' })).toBeVisible()
 mounted.rerender(view())
 act(() => useSessionStore.setState(s => ({ sessions: s.sessions.map(x => x.id === 'worker' ? { ...x, status: 'error' } : x) })))
 expect(screen.getByRole('treeitem', { name: 'Review, failed' })).toBeVisible()
 expect(research()).toHaveAccessibleName(/Review: failed/)
 act(() => { useSessionStore.setState(s => ({ sessions: s.sessions.map(x => x.id === 'worker' ? { ...x, status: 'waiting' } : x) })); useDecisionAttention.setState({ live: { worker: { session_id: 'worker', proposals: 1 } } }) })
 expect(screen.getByRole('treeitem', { name: 'Review' })).toBeVisible()
 expect(research()).toHaveAccessibleName(/Review: needs you/)
 expect(screen.getByRole('img', { name: 'Needs you' })).toBeVisible()
 // Resolving the decision leaves a parent-handled wait, which is quiet everywhere.
 act(() => useDecisionAttention.setState({ live: { worker: { session_id: 'worker', proposals: 0 } } }))
 expect(screen.queryByRole('treeitem', { name: 'Review' })).toBeNull()
 expect(research()).not.toHaveAccessibleName(/needs you/)
 expect(screen.queryByRole('img', { name: 'Needs you' })).toBeNull()
})
it('keeps filing and category movement in keyboard-accessible row menus', async () => {
 const { view } = fixture(); render(view())
 await screen.findByRole('treeitem', { name: /^Research(?:,|$)/ })
 expect(screen.queryAllByRole('combobox')).toHaveLength(0)
 expect(screen.queryByRole('button', { name: 'New category' })).toBeNull()
 fireEvent.keyDown(screen.getByRole('treeitem', { name: /^Build(?:,|$)/ }), { key: 'F10', shiftKey: true })
 fireEvent.click(screen.getByRole('menuitem', { name: 'Move up' }))
 await waitFor(() => expect(screen.getAllByRole('treeitem')[1]).toHaveAccessibleName(/^Build,/))
 fireEvent.keyDown(screen.getAllByRole('treeitem')[1], { key: 'F10', shiftKey: true })
 expect(screen.getByRole('menuitem', { name: 'Move up' })).toBeDisabled()
 fireEvent.click(screen.getByRole('menuitem', { name: 'Move down' }))
 await waitFor(() => expect(screen.getAllByRole('treeitem')[1]).toHaveAccessibleName(/^Research,/))
 fireEvent.keyDown(screen.getByRole('treeitem', { name: 'Design' }), { key: 'F10', shiftKey: true })
 expect(screen.getByRole('menuitem', { name: 'File in Build' })).toBeVisible()
 expect(screen.queryAllByRole('combobox')).toHaveLength(0)
})
it('refreshes a stale category action with a clear message', async () => {
 let removed = false
 vi.mocked(apiFetch).mockImplementation(async (url, init) => {
  if (!url.endsWith('/categories')) return new Response('{}')
  if (init?.method === 'POST') { removed = true; return new Response('{"error":"category not found"}', { status: 404 }) }
  return new Response(JSON.stringify({ categories: removed ? [] : [{ id: 'research', name: 'Research', sessions: [] }] }))
 })
 useSessionStore.setState({ sessions: [makeSession({ id: 'main' })] })
 render(<MemoryRouter><SessionDockWindows sessionId="main" mobile files={null} /></MemoryRouter>)
 await screen.findByRole('treeitem', { name: /^Research(?:,|$)/ })
 fireEvent.contextMenu(research())
 fireEvent.click(screen.getByRole('menuitem', { name: 'Delete Research' }))
 await waitFor(() => expect(screen.queryByRole('treeitem', { name: /^Research(?:,|$)/ })).toBeNull())
 expect(screen.getByRole('alert')).toHaveTextContent('Categories changed. Review the refreshed list and try again.')
})
