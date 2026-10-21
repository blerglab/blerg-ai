import { beforeEach, describe, expect, it, vi } from 'vitest'
import { readLiveBoardCard, resetLiveBoardCardsForTest, useLiveBoardCard } from './liveBoardCard'
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
