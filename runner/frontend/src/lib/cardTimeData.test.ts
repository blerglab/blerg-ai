import { beforeEach, describe, expect, it, vi } from 'vitest'
import { readCardTime, resetCardTimeDataForTest, useCardTimeSummaries } from './cardTimeData'
import { renderHook, waitFor } from '@testing-library/react'

const apiFetch = vi.fn()
let token = ''
vi.mock('../apiFetch', () => ({ apiFetch: (...a: unknown[]) => apiFetch(...a) }))
vi.mock('../authClient', () => ({ getAccessToken: () => token }))

const jwt = (sub: string) => `h.${btoa(JSON.stringify({ sub })).replace(/=+$/, '')}.s`
const summary = (seconds: number) => ({ ok: true, json: async () => ({ seconds, rounds: 1 }) })

beforeEach(() => { apiFetch.mockReset(); resetCardTimeDataForTest() })

// H3: a card-time read taken under one person's sign-in is never handed to the next account that
// signs in in the same tab — the reads are kept per account, same as liveBoardCard.ts.
describe('the card-time cache is the person\'s', () => {
  it('does not hand a read by one account to the next', async () => {
    token = jwt('ann')
    apiFetch.mockResolvedValueOnce(summary(14400))
    const first = await readCardTime('b', 1)
    expect(first?.seconds).toBe(14400)

    token = jwt('bo')
    apiFetch.mockResolvedValueOnce({ ok: true, json: async () => ({}) }) // bo's own card is under budget
    const { result } = renderHook(() => useCardTimeSummaries([{ board: 'b', number: 1 }]))
    await waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(result.current.get('b#1')).toBeUndefined())
  })

  it('a read already in flight when the account switches is never cached under the new account', async () => {
    token = jwt('ann')
    let resolve!: (v: unknown) => void
    apiFetch.mockReturnValueOnce(new Promise(r => { resolve = r }))
    const read = readCardTime('b', 2) // starts under ann
    token = jwt('bo') // switches before ann's read resolves
    resolve(summary(7200))
    await read // the in-flight promise still settles for whoever awaits it directly...

    // ...but it must never land in the shared cache bo's own renders read from.
    apiFetch.mockResolvedValueOnce({ ok: true, json: async () => ({}) })
    const { result } = renderHook(() => useCardTimeSummaries([{ board: 'b', number: 2 }]))
    await waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(2))
    expect(result.current.get('b#2')).toBeUndefined()
  })
})

// Codex round 2 (cardTimeData.ts:88): a mounted panel must notice a crossing even with no edit to
// the pin/checklist list that would otherwise change `list`'s identity — poll on the same 60 s
// freshness window the cache itself uses.
describe('refetching on a schedule', () => {
  it('refetches a mounted ref on the existing 60s schedule with no other change', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval', 'setTimeout', 'clearTimeout', 'Date'] })
    try {
      token = jwt('ann')
      apiFetch.mockResolvedValue({ ok: true, json: async () => ({}) })
      renderHook(() => useCardTimeSummaries([{ board: 'b', number: 3 }]))
      await vi.advanceTimersByTimeAsync(0)
      expect(apiFetch).toHaveBeenCalledTimes(1)
      await vi.advanceTimersByTimeAsync(30_000)
      expect(apiFetch).toHaveBeenCalledTimes(1) // still fresh
      await vi.advanceTimersByTimeAsync(31_000) // past the 60s freshness window
      expect(apiFetch).toHaveBeenCalledTimes(2)
    } finally {
      vi.useRealTimers()
    }
  })

  // Codex round 2: account identity is now a memo/effect dependency, so an identity switch while
  // a hook is already mounted (no unmount/remount, unlike the tests above) must itself trigger a
  // fresh fetch and drop whatever was shown for the previous account.
  it('refetches immediately when the account changes under an already-mounted hook, with no external rerender', async () => {
    token = jwt('ann')
    apiFetch.mockResolvedValueOnce(summary(14400))
    const { result } = renderHook(() => useCardTimeSummaries([{ board: 'b', number: 4 }]))
    await waitFor(() => expect(result.current.get('b#4')?.seconds).toBe(14400))

    token = jwt('bo')
    apiFetch.mockResolvedValueOnce({ ok: true, json: async () => ({}) })
    // No rerender() call: the hook must notice the switch and update itself — the point of
    // notifying `listeners` from the blerg-identity-changed handler, not merely clearing state
    // that nothing would otherwise prompt a mounted hook to re-read.
    window.dispatchEvent(new Event('blerg-identity-changed'))

    await waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(2))
    expect(result.current.get('b#4')).toBeUndefined()
  })
})
