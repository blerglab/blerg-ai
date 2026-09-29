import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import type { Board } from '../types'
import * as boardApi from '../lib/boardApi'

// Mock the boardApi module
vi.mock('../lib/boardApi', () => ({
  listBoards: vi.fn(),
  createBoard: vi.fn(),
  deleteBoard: vi.fn(),
}))

// Import after mock
import BoardsList from './BoardsList'

const BOARDS: Board[] = [
  {
    id: 'b1',
    name: 'Alpha Board',
    description: 'First board',
    repos: ['org/repo-one', 'org/repo-two'],
    default_daemon_id: null,
    created_at: '2024-01-01T00:00:00Z',
    updated_at: '2024-01-01T00:00:00Z',
  },
  {
    id: 'b2',
    name: 'Beta Board',
    description: null,
    repos: ['org/repo-three'],
    default_daemon_id: 'd1',
    created_at: '2024-01-02T00:00:00Z',
    updated_at: '2024-01-02T00:00:00Z',
  },
]

function buildFetchMock() {
  return vi.fn((url: string) => {
    if (url === '/api/repos') {
      return Promise.resolve({
        ok: true,
        json: () =>
          Promise.resolve({
            repos: [
              { name: 'repo-one', full_name: 'org/repo-one', checked_out: [], is_local: false },
              { name: 'repo-two', full_name: 'org/repo-two', checked_out: [], is_local: false },
            ],
          }),
      })
    }
    if (url === '/api/daemons') {
      return Promise.resolve({
        ok: true,
        json: () =>
          Promise.resolve([
            { id: 'd1', name: 'workstation', mode: 'local', repos_root: '/repos', status: 'connected' },
          ]),
      })
    }
    return Promise.resolve({ ok: true, json: () => Promise.resolve({}) })
  })
}

function renderBoardsList() {
  return render(
    <MemoryRouter initialEntries={['/boards']}>
      <BoardsList />
    </MemoryRouter>,
  )
}

describe('BoardsList', () => {
  beforeEach(() => {
    vi.mocked(boardApi.listBoards).mockResolvedValue(BOARDS)
    vi.mocked(boardApi.createBoard).mockResolvedValue({
      id: 'new-board',
      name: 'New Board',
      description: null,
      repos: ['org/repo-one'],
      default_daemon_id: null,
      created_at: '2024-01-03T00:00:00Z',
      updated_at: '2024-01-03T00:00:00Z',
      columns: [],
    })
    vi.mocked(boardApi.deleteBoard).mockResolvedValue(undefined)
    vi.stubGlobal('fetch', buildFetchMock())
  })

  afterEach(() => {
    vi.clearAllMocks()
    vi.unstubAllGlobals()
  })

  // ─── Render tests ───────────────────────────────────────────────────────────

  it('renders a list of boards returned by listBoards', async () => {
    renderBoardsList()
    expect(await screen.findByText('Alpha Board')).toBeInTheDocument()
    expect(screen.getByText('Beta Board')).toBeInTheDocument()
  })

  it('shows repo chips for each board', async () => {
    renderBoardsList()
    await screen.findByText('Alpha Board')
    expect(screen.getByText('org/repo-one')).toBeInTheDocument()
    expect(screen.getByText('org/repo-two')).toBeInTheDocument()
    expect(screen.getByText('org/repo-three')).toBeInTheDocument()
  })

  it('shows a "+" button to open the create sheet', async () => {
    renderBoardsList()
    await screen.findByText('Alpha Board')
    expect(screen.getByRole('button', { name: /\+/ })).toBeInTheDocument()
  })

  // ─── Delete with confirmation ───────────────────────────────────────────────

  it('calls window.confirm before deleting a board', async () => {
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false)
    renderBoardsList()
    await screen.findByText('Alpha Board')

    // Click the delete button for "Alpha Board"
    const deleteButtons = screen.getAllByRole('button', { name: /delete|✕|×|trash|remove/i })
    fireEvent.click(deleteButtons[0])

    expect(confirm).toHaveBeenCalled()
    expect(boardApi.deleteBoard).not.toHaveBeenCalled()
  })

  it('calls deleteBoard only after the user confirms deletion', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    renderBoardsList()
    await screen.findByText('Alpha Board')

    const deleteButtons = screen.getAllByRole('button', { name: /delete|✕|×|trash|remove/i })
    fireEvent.click(deleteButtons[0])

    await waitFor(() => expect(boardApi.deleteBoard).toHaveBeenCalledWith('b1'))
  })

  it('does NOT call deleteBoard when confirmation is cancelled', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(false)
    renderBoardsList()
    await screen.findByText('Alpha Board')

    const deleteButtons = screen.getAllByRole('button', { name: /delete|✕|×|trash|remove/i })
    fireEvent.click(deleteButtons[0])

    expect(boardApi.deleteBoard).not.toHaveBeenCalled()
  })

  it('refreshes the list after a board is deleted', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true)

    // After deletion, return only Beta Board
    vi.mocked(boardApi.listBoards)
      .mockResolvedValueOnce(BOARDS)
      .mockResolvedValueOnce([BOARDS[1]])

    renderBoardsList()
    await screen.findByText('Alpha Board')

    const deleteButtons = screen.getAllByRole('button', { name: /delete|✕|×|trash|remove/i })
    fireEvent.click(deleteButtons[0])

    await waitFor(() => expect(screen.queryByText('Alpha Board')).not.toBeInTheDocument())
    expect(screen.getByText('Beta Board')).toBeInTheDocument()
  })

  // ─── Create sheet ───────────────────────────────────────────────────────────

  it('opens the create sheet when the "+" button is clicked', async () => {
    renderBoardsList()
    await screen.findByText('Alpha Board')

    fireEvent.click(screen.getByRole('button', { name: /\+/ }))

    expect(await screen.findByText(/new board/i)).toBeInTheDocument()
  })

  it('submits the correct payload when creating a board', async () => {
    renderBoardsList()
    await screen.findByText('Alpha Board')

    // Open create sheet
    fireEvent.click(screen.getByRole('button', { name: /\+/ }))

    // Wait for the sheet to load repos
    await screen.findByText('repo-one')

    // Fill in the name
    const nameInput = screen.getByPlaceholderText(/board name/i)
    fireEvent.change(nameInput, { target: { value: 'My New Board' } })

    // Select a repo
    fireEvent.click(screen.getByText('repo-one'))

    // Submit
    fireEvent.click(screen.getByRole('button', { name: /create/i }))

    await waitFor(() =>
      expect(boardApi.createBoard).toHaveBeenCalledWith(
        expect.objectContaining({
          name: 'My New Board',
          repos: expect.arrayContaining(['org/repo-one']),
        }),
      ),
    )
  })

  it('does not submit when name is empty', async () => {
    renderBoardsList()
    await screen.findByText('Alpha Board')

    fireEvent.click(screen.getByRole('button', { name: /\+/ }))
    await screen.findByText('repo-one')

    // Select a repo but leave name empty
    fireEvent.click(screen.getByText('repo-one'))
    fireEvent.click(screen.getByRole('button', { name: /create/i }))

    expect(boardApi.createBoard).not.toHaveBeenCalled()
  })

  it('does not submit when no repos are selected', async () => {
    renderBoardsList()
    await screen.findByText('Alpha Board')

    fireEvent.click(screen.getByRole('button', { name: /\+/ }))
    await screen.findByText('repo-one')

    // Fill name but don't select any repo
    const nameInput = screen.getByPlaceholderText(/board name/i)
    fireEvent.change(nameInput, { target: { value: 'My Board' } })
    fireEvent.click(screen.getByRole('button', { name: /create/i }))

    expect(boardApi.createBoard).not.toHaveBeenCalled()
  })

  it('refreshes the list after creating a board', async () => {
    const updatedBoards = [...BOARDS, {
      id: 'new-board',
      name: 'New Board',
      description: null,
      repos: ['org/repo-one'],
      default_daemon_id: null,
      created_at: '2024-01-03T00:00:00Z',
      updated_at: '2024-01-03T00:00:00Z',
    }]

    vi.mocked(boardApi.listBoards)
      .mockResolvedValueOnce(BOARDS)
      .mockResolvedValueOnce(updatedBoards)

    renderBoardsList()
    await screen.findByText('Alpha Board')

    fireEvent.click(screen.getByRole('button', { name: /\+/ }))
    await screen.findByText('repo-one')

    const nameInput = screen.getByPlaceholderText(/board name/i)
    fireEvent.change(nameInput, { target: { value: 'New Board' } })
    fireEvent.click(screen.getByText('repo-one'))
    fireEvent.click(screen.getByRole('button', { name: /create/i }))

    await waitFor(() => expect(screen.getByText('New Board')).toBeInTheDocument())
  })
})
