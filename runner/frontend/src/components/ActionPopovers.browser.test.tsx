import { beforeEach, expect, it, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import type { Board, MessageInfo } from '../types'
import * as boardApi from '../lib/boardApi'
import { apiFetch } from '../apiFetch'
import { useToastStore } from '../hooks/useToastStore'
import { useBoardStore } from '../hooks/useBoardStore'
import BoardsList from './BoardsList'
import BoardView from './BoardView'
import { MessageThread } from './MessageThread'
import '../index.css'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../apiFetch', () => ({ apiFetch: vi.fn() }))
vi.mock('../lib/boardApi', async original => ({
  ...await original<typeof import('../lib/boardApi')>(),
  listBoards: vi.fn(), deleteBoard: vi.fn(), getBoard: vi.fn(), deleteColumn: vi.fn(),
}))
const board: Board = { id: 'board', name: 'Release planning', description: null, repos: [], default_daemon_id: null, created_at: '', updated_at: '' }
beforeEach(() => {
  vi.resetAllMocks()
  useToastStore.setState({ toasts: [] })
  useBoardStore.setState({ board: null, boardId: null, columns: {}, tickets: {}, loading: false, pendingOpIds: new Set() })
  vi.mocked(boardApi.listBoards).mockResolvedValue([board])
})
async function contained(dialog: HTMLElement) {
  await waitFor(() => expect(dialog.getAnimations().length).toBe(0))
  const bounds = dialog.getBoundingClientRect()
  // Firefox can round a viewport edge a fraction of a CSS pixel outward.
  const tolerance = 1
  expect(bounds.left).toBeGreaterThanOrEqual(-tolerance)
  expect(bounds.top).toBeGreaterThanOrEqual(-tolerance)
  expect(bounds.right).toBeLessThanOrEqual(innerWidth + tolerance)
  expect(bounds.bottom).toBeLessThanOrEqual(innerHeight + tolerance)
}
it.each([390, 3840])('confirms and retries board deletion on the shared surface at %i', async width => {
  await page.viewport(width, 844)
  vi.mocked(boardApi.deleteBoard).mockRejectedValueOnce(new Error('Offline')).mockResolvedValueOnce(undefined)
  render(<MemoryRouter><BoardsList /></MemoryRouter>)
  const trigger = await screen.findByRole('button', { name: 'Delete board' })
  await userEvent.click(trigger)
  let dialog = screen.getByRole('alertdialog', { name: 'Delete board?' })
  await contained(dialog)
  expect(boardApi.deleteBoard).not.toHaveBeenCalled()
  await userEvent.keyboard('{Escape}')
  expect(trigger).toHaveFocus()
  await userEvent.click(trigger)
  dialog = screen.getByRole('alertdialog', { name: 'Delete board?' })
  await userEvent.click(within(dialog).getByRole('button', { name: 'Delete board' }))
  dialog = await screen.findByRole('alertdialog', { name: 'Board could not be deleted' })
  await contained(dialog)
  await userEvent.click(within(dialog).getByRole('button', { name: 'Retry' }))
  await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull())
  expect(boardApi.deleteBoard).toHaveBeenCalledTimes(2)
  expect(boardApi.deleteBoard).toHaveBeenLastCalledWith('board')
})
it('anchors column confirmation and failure to the surviving menu button', async () => {
  await page.viewport(3840, 1000)
  vi.mocked(boardApi.getBoard).mockResolvedValue({ ...board, tickets: [], columns: [{ id: 'todo', board_id: 'board', name: 'Todo', rank: 'a', is_terminal: false, created_at: '' }] })
  vi.mocked(boardApi.deleteColumn).mockRejectedValueOnce(new Error('Offline')).mockResolvedValueOnce(undefined)
  render(<MemoryRouter initialEntries={['/boards/board']}><Routes><Route path="/boards/:id" element={<BoardView />} /></Routes></MemoryRouter>)
  const trigger = await screen.findByTestId('column-menu-todo')
  await userEvent.click(trigger)
  await userEvent.click(screen.getByRole('button', { name: 'Delete column' }))
  const confirmation = screen.getByRole('alertdialog', { name: 'Delete column?' })
  await contained(confirmation)
  expect(boardApi.deleteColumn).not.toHaveBeenCalled()
  await userEvent.click(within(confirmation).getByRole('button', { name: 'Delete column' }))
  const error = await screen.findByRole('alertdialog', { name: 'Column could not be deleted' })
  await contained(error)
  await userEvent.click(within(error).getByRole('button', { name: 'Dismiss' }))
  expect(trigger).toHaveFocus()
})
it.each([390, 3840])('keeps a failed Reply anchored with its draft and retry at %i', async width => {
  await page.viewport(width, 844)
  vi.mocked(apiFetch).mockResolvedValueOnce(new Response('{}', { status: 409 })).mockResolvedValueOnce(new Response('{}'))
  const message: MessageInfo = { id: 'message', session_id: 'session', kind: 'ask', body: 'Ship this release?', status: 'open', created_at: '2026-10-25T12:00:00Z' }
  render(<MemoryRouter><MessageThread messages={[message]} /></MemoryRouter>)
  const input = screen.getByRole('textbox'), trigger = screen.getByRole('button', { name: 'Send' })
  await userEvent.fill(input, 'Ship after validation')
  await userEvent.click(trigger)
  const error = await screen.findByRole('alertdialog', { name: 'Reply failed' })
  await contained(error)
  expect(input).toHaveValue('Ship after validation')
  expect(useToastStore.getState().toasts).toHaveLength(0)
  await userEvent.click(within(error).getByRole('button', { name: 'Retry' }))
  await waitFor(() => expect(input).toHaveValue(''))
  expect(apiFetch).toHaveBeenCalledTimes(2)
})
