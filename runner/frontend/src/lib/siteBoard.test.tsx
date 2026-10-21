import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { boardCardUrl, loadBoardOrigin, resetBoardOriginForTest, useBoardOrigin } from './boardLink'

const fetchSite = vi.fn()
beforeEach(() => { vi.stubGlobal('fetch', fetchSite); fetchSite.mockReset(); resetBoardOriginForTest() })
function Link() { const base = useBoardOrigin(); return <a href={boardCardUrl('board 1', 8, base)}>Open board card</a> }
describe('site board discovery', () => {
  it('reads the Board URL from the runner on its own origin, including a path prefix', async () => {
    fetchSite.mockResolvedValue(new Response(JSON.stringify({ board_url: 'https://example.com/apps/boards/' })))
    render(<Link />)
    await waitFor(() => expect(screen.getByRole('link')).toHaveAttribute('href', 'https://example.com/apps/boards/boards/board%201/cards/8'))
    // Same origin (a relative path): never a cross-origin read of core, which sends no CORS headers.
    expect(fetchSite.mock.calls[0][0]).toBe('/api/site-board')
  })

  it('remembers a failed lookup instead of asking again for every link', async () => {
    fetchSite.mockResolvedValue(new Response('{}', { status: 503 }))
    await loadBoardOrigin()
    await loadBoardOrigin()
    await loadBoardOrigin()
    expect(fetchSite).toHaveBeenCalledTimes(1)
  })
})
