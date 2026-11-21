import { expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import SessionDockWindows from './SessionDockWindows'
import { makeSession } from '../test/fixtures'
import { useSessionStore } from '../hooks/useSessionStore'
vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../apiFetch', () => ({ apiFetch: vi.fn() }))
import { apiFetch } from '../apiFetch'
it('files a channel, preserves Main, renames and deletes without deleting sessions', async () => {
 const categories = [{ id: 'research', name: 'Research', sessions: ['child'] }]
 vi.mocked(apiFetch).mockImplementation(async (_url, init) => {
  if (init?.method === 'POST') {
   const action = JSON.parse(init.body as string)
   if (action.action === 'rename') categories[0].name = action.name
   if (action.action === 'delete') categories.splice(0)
  }
  return new Response(JSON.stringify({ categories }))
 })
 useSessionStore.setState({ sessions: [makeSession({ id: 'main' }), makeSession({ id: 'child', title: 'Design', parent_session_id: 'main' })] })
 render(<MemoryRouter><SessionDockWindows sessionId="main" mobile={false} files={null} /></MemoryRouter>)
 await screen.findByRole('treeitem', { name: /^Research,/ })
 expect(screen.getAllByRole('treeitem')[0]).toHaveAccessibleName('Main')
 expect(screen.getAllByRole('treeitem', { name: 'Design' })).toHaveLength(1)
 fireEvent.click(screen.getByRole('treeitem', { name: /^Research,/ }))
 expect(screen.queryByRole('treeitem', { name: 'Design' })).toBeNull()
 fireEvent.click(screen.getByRole('treeitem', { name: /^Research,/ }))
 expect(screen.getByRole('treeitem', { name: 'Design' })).toBeVisible()
 fireEvent.contextMenu(screen.getByRole('treeitem', { name: /^Research,/ }))
 fireEvent.click(screen.getByRole('menuitem', { name: 'Rename Research' }))
 fireEvent.change(screen.getByRole('textbox', { name: 'Category name' }), { target: { value: 'Reading' } })
 fireEvent.click(screen.getByRole('button', { name: 'Save category' }))
 await screen.findByRole('treeitem', { name: /^Reading,/ })
 fireEvent.contextMenu(screen.getByRole('treeitem', { name: /^Reading,/ }))
 fireEvent.click(screen.getByRole('menuitem', { name: 'Delete Reading' }))
 await waitFor(() => expect(screen.queryByRole('treeitem', { name: /^Reading,/ })).toBeNull())
 expect(screen.getByRole('treeitem', { name: 'Design' })).toBeVisible()
})

it('creates a category and files a channel through its row menu', async () => {
 const categories: { id: string; name: string; sessions: string[] }[] = []
 vi.mocked(apiFetch).mockImplementation(async (url, init) => {
  if (!url.endsWith('/categories')) return new Response('{}')
  if (init?.method === 'POST') {
   const request = JSON.parse(init.body as string)
   if (request.action === 'create') categories.push({ id: 'new', name: request.name, sessions: [] })
   if (request.action === 'file') categories[0].sessions.push(request.session)
  }
  return new Response(JSON.stringify({ categories }))
 })
 useSessionStore.setState({ sessions: [makeSession({ id: 'main' }), makeSession({ id: 'child', title: 'Design', parent_session_id: 'main' })] })
 render(<MemoryRouter><SessionDockWindows sessionId="main" mobile files={null} /></MemoryRouter>)
 await act(async () => {})
 fireEvent.contextMenu(screen.getByRole('treeitem', { name: 'Main' }))
 fireEvent.click(screen.getByRole('menuitem', { name: 'New category' }))
 expect(screen.getByRole('button', { name: 'Save category' })).toBeDisabled()
 fireEvent.change(screen.getByRole('textbox', { name: 'Category name' }), { target: { value: 'Design work' } })
 fireEvent.click(screen.getByRole('button', { name: 'Save category' }))
 await screen.findByRole('treeitem', { name: /^Design work,/ })
 fireEvent.contextMenu(screen.getByRole('treeitem', { name: 'Design' }))
 fireEvent.click(screen.getByRole('menuitem', { name: 'File in Design work' }))
 await waitFor(() => expect(screen.getByRole('group', { name: 'Design work' })).toContainElement(screen.getByRole('treeitem', { name: 'Design' })))
 expect(screen.getAllByRole('treeitem', { name: 'Design' })).toHaveLength(1)
})
