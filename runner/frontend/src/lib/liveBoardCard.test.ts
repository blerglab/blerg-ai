import { beforeEach, describe, expect, it, vi } from 'vitest'
import { PINS_REFRESH_MS, checkAccount, readLiveBoardCard, resetLiveBoardCardsForTest, useLiveBoardCard, useLiveBoardCards } from './liveBoardCard'
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
