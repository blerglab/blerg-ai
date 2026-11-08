import { expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { page } from 'vitest/browser'
import { MemoryRouter } from 'react-router-dom'
import SessionDockWindows from './SessionDockWindows'
import { useSessionStore } from '../hooks/useSessionStore'
import { makeSession } from '../test/fixtures'
import { apiFetch } from '../apiFetch'
import '../index.css'
vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../apiFetch', () => ({ apiFetch: vi.fn() }))
for (const width of [390, 3840]) it(`organizes channels and workers at ${width}px`, async () => {
 await page.viewport(width, 900)
 document.documentElement.setAttribute('data-theme', 'dark')
 let categories = [{ id: 'research', name: 'Research', sessions: ['channel'] }, { id: 'build', name: 'Build', sessions: [] as string[] }]
 vi.mocked(apiFetch).mockImplementation(async (url, init) => {
  if (!url.endsWith('/categories')) return new Response('{}')
  if (init?.method === 'POST') {
   const request = JSON.parse(init.body as string)
   if (request.action === 'file') {
    categories.forEach(c => { c.sessions = c.sessions.filter(id => id !== request.session) })
    categories.find(c => c.id === request.id)?.sessions.push(request.session)
   }
   if (request.action === 'delete') categories = categories.filter(c => c.id !== request.id)
   if (request.action === 'move') categories.reverse()
  }
  return new Response(JSON.stringify({ categories }))
 })
 useSessionStore.setState({ sessions: [makeSession({ id: 'main' }), makeSession({ id: 'channel', title: 'Design', parent_session_id: 'main' }), makeSession({ id: 'worker', title: 'Implement', parent_session_id: 'main', started_by: { kind: 'session' } })] })
 render(<MemoryRouter><div style={{ width: width === 390 ? '100%' : 320, height: 750, display: 'flex' }}><SessionDockWindows sessionId="main" mobile={width === 390} files={null} /></div></MemoryRouter>)
 await screen.findByRole('treeitem', { name: /^Research,/ })
 expect(screen.getAllByRole('treeitem')[0]).toHaveAccessibleName('Main')
 if (width === 390) {
  fireEvent.contextMenu(screen.getByRole('treeitem', { name: /^Build,/ }))
  fireEvent.click(screen.getByRole('menuitem', { name: 'Move up' }))
 } else {
  const categoryData = new DataTransfer()
  await act(async () => { screen.getByRole('treeitem', { name: /^Build,/ }).closest('li')!.dispatchEvent(new DragEvent('dragstart', { bubbles: true, dataTransfer: categoryData })) })
  await act(async () => { screen.getByRole('treeitem', { name: /^Research,/ }).closest('li')!.dispatchEvent(new DragEvent('drop', { bubbles: true, dataTransfer: categoryData })) })
 }
 await waitFor(() => expect(screen.getAllByRole('treeitem')[1]).toHaveAccessibleName(/^Build,/))
 // Running workers start expanded; opening their group again would close it.
 expect(screen.getByRole('treeitem', { name: 'Workers, 1' })).toHaveAttribute('aria-expanded', 'true')
 if (width === 390) {
  fireEvent.contextMenu(screen.getByRole('treeitem', { name: 'Implement' }))
  fireEvent.click(screen.getByRole('menuitem', { name: 'File in Build' }))
 } else {
  const data = new DataTransfer()
  await act(async () => { screen.getByRole('treeitem', { name: 'Implement' }).closest('li')!.dispatchEvent(new DragEvent('dragstart', { bubbles: true, dataTransfer: data })) })
  expect(data.getData('application/x-blerg-channel')).toContain('worker')
  await act(async () => { screen.getByRole('treeitem', { name: /^Build,/ }).closest('li')!.dispatchEvent(new DragEvent('drop', { bubbles: true, dataTransfer: data })) })
 }
 await waitFor(() => expect(vi.mocked(apiFetch).mock.calls.some(([, init]) => init?.method === 'POST')).toBe(true))
 await waitFor(() => expect(screen.queryByRole('treeitem', { name: 'Workers, 1' })).toBeNull())
 expect(screen.getByRole('treeitem', { name: 'Implement' })).toBeVisible()
 for (const row of screen.getAllByRole('treeitem')) expect(row.scrollWidth).toBeLessThanOrEqual(row.clientWidth)
 const shots = import.meta.env.VITE_CHROME_SHOTS_DIR
 if (shots) await page.screenshot({ path: `${shots}/categories-${width}.png` })
 fireEvent.contextMenu(screen.getByRole('treeitem', { name: /^Build,/ }))
 fireEvent.click(screen.getByRole('menuitem', { name: 'Delete Build' }))
 await screen.findByRole('treeitem', { name: 'Workers, 1' })
 expect(useSessionStore.getState().sessions).toHaveLength(3)
})

for (const width of [390, 3840]) it(`keeps forty channels compact and supports touch menus at ${width}px`, async () => {
 await page.viewport(width, 900)
 let categories: { id: string; name: string; sessions: string[] }[] = []
 vi.mocked(apiFetch).mockImplementation(async (url, init) => {
  if (!url.endsWith('/categories')) return new Response('{}')
  if (init?.method === 'POST') {
   const request = JSON.parse(init.body as string)
   if (request.action === 'move') categories.reverse()
   if (request.action === 'file') categories.find(c => c.id === request.id)!.sessions.push(request.session)
  }
  return new Response(JSON.stringify({ categories }))
 })
 useSessionStore.setState({ sessions: [makeSession({ id: 'main' }), ...Array.from({ length: 40 }, (_, i) => makeSession({ id: `channel-${i}`, title: `Topic ${i}`, parent_session_id: 'main' }))] })
 render(<MemoryRouter><div style={{ width: width === 390 ? '100%' : 320, height: 750, display: 'flex' }}><SessionDockWindows sessionId="main" mobile={width === 390} files={null} /></div></MemoryRouter>)
 await act(async () => {})
 const tree = screen.getByRole('tree', { name: 'Channels' })
 const initialHeight = tree.getBoundingClientRect().height
 const main = screen.getByRole('treeitem', { name: 'Main' })
 expect(screen.queryByRole('button', { name: 'New category' })).toBeNull()
 expect(tree.querySelectorAll('select')).toHaveLength(0)
 categories = [{ id: 'research', name: 'Research', sessions: [] }, { id: 'build', name: 'Build', sessions: [] }]
 await act(async () => { window.dispatchEvent(new Event('focus')) })
 await screen.findByRole('treeitem', { name: /^Build,/ })
 expect(tree.getBoundingClientRect().height - initialHeight).toBeLessThan(90)
 expect(tree.querySelectorAll('select')).toHaveLength(0)
 expect(tree.querySelectorAll('[tabindex="0"]')).toHaveLength(1)
 // Touch long-press opens actions without a permanent button covering Main's status.
 await act(async () => { main.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, pointerType: 'touch', clientX: 50, clientY: 50 })) })
 await screen.findByRole('menuitem', { name: 'New category' })
 for (const item of screen.getAllByRole('menuitem')) {
  const box = item.getBoundingClientRect()
  expect(box.height).toBeGreaterThanOrEqual(44)
  expect(box.width).toBeGreaterThanOrEqual(44)
 }
 fireEvent.keyDown(screen.getByRole('menu'), { key: 'Escape' })
 expect(main).toHaveFocus()
 fireEvent.keyDown(screen.getByRole('treeitem', { name: /^Build,/ }), { key: 'F10', shiftKey: true })
 fireEvent.click(screen.getByRole('menuitem', { name: 'Move up' }))
 await waitFor(() => expect(screen.getAllByRole('treeitem')[1]).toHaveAccessibleName(/^Build,/))
 fireEvent.keyDown(screen.getAllByRole('treeitem')[1], { key: 'F10', shiftKey: true })
 fireEvent.click(screen.getByRole('menuitem', { name: 'Move down' }))
 await waitFor(() => expect(screen.getAllByRole('treeitem')[1]).toHaveAccessibleName(/^Research,/))
 const shots = import.meta.env.VITE_CHROME_SHOTS_DIR
 if (shots) await page.screenshot({ path: `${shots}/categories-compact-${width}.png` })
})
