import { afterEach, describe, expect, it, vi } from 'vitest'
import { act, renderHook } from '@testing-library/react'
import { useProposalsWaiting } from './useProposalsWaiting'
import type { ProposalInfo } from '../types'

describe('useProposalsWaiting', () => {
  afterEach(() => vi.useRealTimers())
  it('drops a proposal when its deadline passes, with no ticker', () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval', 'Date'] })
    vi.setSystemTime(Date.parse('2026-10-21T12:00:00Z'))
    const at = (ms: number) => new Date(Date.now() + ms).toISOString()
    const proposals = [
      { id: 'soon', state: 'pending', expires_at: at(60_000) },
      { id: 'later', state: 'pending', expires_at: at(86400_000) },
      { id: 'gone', state: 'pending', expires_at: at(-1) },
    ] as ProposalInfo[]
    const { result } = renderHook(() => useProposalsWaiting(proposals))
    expect(result.current.map(p => p.id)).toEqual(['soon', 'later'])
    act(() => { vi.advanceTimersByTime(59_000) })
    expect(result.current.map(p => p.id)).toEqual(['soon', 'later'])
    act(() => { vi.advanceTimersByTime(2_000) })
    expect(result.current.map(p => p.id)).toEqual(['later'])
    // One timer for the next deadline, not an interval.
    expect(vi.getTimerCount()).toBe(1)
  })
  it('counts by the server\'s clock: a browser running fast does not drop a proposal early', () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval', 'Date'] })
    vi.setSystemTime(Date.parse('2026-10-21T12:00:00Z'))
    const fast = 10 * 60_000 // client minus server
    const proposals = [{ id: 'p', state: 'pending', expires_at: new Date(Date.now() - 60_000).toISOString() }] as ProposalInfo[]
    const { result } = renderHook(() => useProposalsWaiting(proposals, fast))
    expect(result.current.map(p => p.id)).toEqual(['p'])
    act(() => { vi.advanceTimersByTime(8 * 60_000) })
    expect(result.current.map(p => p.id)).toEqual(['p'])
    act(() => { vi.advanceTimersByTime(2 * 60_000) })
    expect(result.current).toEqual([])
  })
})
