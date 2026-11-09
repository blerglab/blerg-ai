import { beforeEach, describe, expect, it, vi } from 'vitest'
import { moveLiveBoardCard, PINS_REFRESH_MS, checkAccount, readLiveBoardCard, resetLiveBoardCardsForTest, useLiveBoardCard, useLiveBoardCards } from './liveBoardCard'
import { renderHook, waitFor } from '@testing-library/react'

const apiFetch = vi.fn()
let token = ''
vi.mock('../apiFetch', () => ({ apiFetch: (...a: unknown[]) => apiFetch(...a) }))
vi.mock('../authClient', () => ({ getAccessToken: () => token }))

const jwt = (sub: string) => `h.${btoa(JSON.stringify({ sub })).replace(/=+$/, '')}.s`
const card = (title: string) => ({ ok: true, status: 200, json: async () => ({ card: { id: 'c', board_id: 'b', number: 1, title }, url: '' }) })

beforeEach(() => { apiFetch.mockReset(); resetLiveBoardCardsForTest() })

// A card read under one person's sign-in is never shown to another who signs in in the same tab:
// the reads are kept per account, and an account change forgets them.
describe('the live card cache is the person\'s', () => {
  it('does not hand a card read by one account to the next', async () => {
    token = jwt('ann')
    apiFetch.mockResolvedValueOnce(card('Ann\'s view'))
    await readLiveBoardCard('b', 1)
    token = jwt('bo')
    apiFetch.mockResolvedValueOnce({ ok: false, status: 403, json: async () => ({ error: 'no access' }) })
    const { result } = renderHook(() => useLiveBoardCard({ board: 'b', number: 1 }))
    expect(result.current.read).toBeNull()
    await waitFor(() => expect(result.current.state).toBe('failed'))
    expect(result.current.read).toBeNull()
    expect(apiFetch).toHaveBeenCalledTimes(2)
  })
})

// Card #127: the Live list reads its pins again while it is on screen, so a card moved to Done shows
// ✓ before the next pin; and an account switch forgets what hovercards read.
describe('useLiveBoardCards', () => {
  it('reads the pins again every PINS_REFRESH_MS while active, and not while inactive', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval', 'setTimeout', 'clearTimeout', 'Date'] })
    try {
      token = jwt('ann')
      apiFetch.mockResolvedValue(card('Live'))
      const refs = [{ board: 'b', number: 1 }]
      const { rerender } = renderHook(({ active }) => useLiveBoardCards(refs, 's', active), { initialProps: { active: false } })
      await vi.advanceTimersByTimeAsync(0)
      expect(apiFetch).toHaveBeenCalledTimes(1)
      await vi.advanceTimersByTimeAsync(PINS_REFRESH_MS * 2)
      expect(apiFetch).toHaveBeenCalledTimes(1)
      rerender({ active: true })
      await vi.advanceTimersByTimeAsync(PINS_REFRESH_MS)
      expect(apiFetch).toHaveBeenCalledTimes(2)
    } finally {
      vi.useRealTimers()
    }
  })

  it('an account switch clears the hovercards\' and previews\' caches', async () => {
    const chat = await import('@blerglab/chat')
    const hc = vi.spyOn(chat, 'clearHovercardCache')
    token = jwt('ann')
    checkAccount()
    expect(hc).not.toHaveBeenCalled()
    token = jwt('bo')
    checkAccount()
    expect(hc).toHaveBeenCalledTimes(1)
  })
})

it('reads a tab card under its workspace rule instead of reusing a pinned read', async () => {
 token = jwt('ann')
 apiFetch.mockResolvedValueOnce(card('Pinned card'))
 await readLiveBoardCard('b', 1, 's')
 apiFetch.mockResolvedValueOnce({ ok: false, status: 403, json: async () => ({ error: 'Workspace grant lowered' }) })
 const { result } = renderHook(() => useLiveBoardCard({ board: 'b', number: 1 }, 's', true, true))
 await waitFor(() => expect(result.current.state).toBe('failed'))
 expect(result.current.read).toBeNull()
 expect(apiFetch.mock.calls[1][0]).toBe('/api/board-cards/b/1?session=s&tab=1')
})

it('refreshes a conflict with the real upstream error shape before another person action', async () => {
 token = jwt('ann')
 apiFetch.mockResolvedValueOnce({ ok: true, status: 200, json: async () => ({ card: { id: 'c', board_id: 'b', number: 1, title: 'Card', version: 0 } }) })
 await readLiveBoardCard('b', 1, 's')
 apiFetch.mockResolvedValueOnce({ ok: false, status: 409, json: async () => ({ error: 'the board refused: {"error": "stale version"}' }) })
 apiFetch.mockResolvedValueOnce({ ok: true, status: 200, json: async () => ({ card: { id: 'c', board_id: 'b', number: 1, title: 'Card', version: 1 } }) })
 await expect(moveLiveBoardCard('b', 1, 'done', 's', 0)).rejects.toThrow('The card changed. Review its current status and try again.')
 expect(apiFetch).toHaveBeenCalledTimes(3)
 const { result } = renderHook(() => useLiveBoardCard({ board: 'b', number: 1 }, 's'))
 expect(result.current.read?.card.version).toBe(1)
 expect(apiFetch.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(1)
})

it('blocks another move when the conflict refresh fails', async () => {
 token = jwt('ann')
 apiFetch.mockResolvedValueOnce({ ok: false, status: 409, json: async () => ({ error: 'the board refused: {"error": "stale version"}' }) })
 apiFetch.mockResolvedValueOnce({ ok: false, status: 503, json: async () => ({ error: 'unavailable' }) })
 await expect(moveLiveBoardCard('b', 1, 'done', 's', 0)).rejects.toThrow('The card changed.')
 await expect(moveLiveBoardCard('b', 1, 'done', 's', 0)).rejects.toThrow('Read the current card')
 expect(apiFetch.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(1)
})
