// #250: the per-session board reads are one account's: an identity change forgets them.
import { afterEach, expect, it, vi } from 'vitest'
import { renderHook, waitFor } from '@testing-library/react'
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))
import { resetSessionBoardsForTest, useLoadSessionBoard } from './sessionBoard'

afterEach(() => resetSessionBoardsForTest())

it('reads the session board again after an identity change instead of showing the old read', async () => {
  vi.useRealTimers()
  let board = 'b-person-a'
  apiFetch.mockImplementation(async () => new Response(JSON.stringify({ can_grant: true, reason: '', grants: [], mcp_tools: [], boards: [{ board_id: board, level: 'full' }] }), { headers: { 'Content-Type': 'application/json' } }))
  const first = renderHook(() => useLoadSessionBoard('s1', true))
  await waitFor(() => expect(first.result.current?.id).toBe('b-person-a'))
  first.unmount()
  window.dispatchEvent(new Event('blerg-identity-changed'))
  board = 'b-person-b'
  const second = renderHook(() => useLoadSessionBoard('s1', true))
  expect(second.result.current).toBeNull()
  await waitFor(() => expect(second.result.current?.id).toBe('b-person-b'))
  expect(apiFetch).toHaveBeenCalledTimes(2)
})
