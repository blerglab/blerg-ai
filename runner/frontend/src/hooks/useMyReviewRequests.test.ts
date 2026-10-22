// Card #155 review M4: the bell's review list is read again as soon as this tab decides or dismisses
// one, and when the page comes back into view, not only at its next minute's read.
import { beforeEach, expect, it, vi } from 'vitest'
import { act, renderHook, waitFor } from '@testing-library/react'

vi.mock('../apiFetch', () => ({ apiFetch: vi.fn() }))
import { apiFetch } from '../apiFetch'
import { notifyReviewRequestsChanged, useMyReviewRequests } from './useMyReviewRequests'

const fetchMock = vi.mocked(apiFetch)
const reply = (ids: string[]) => new Response(JSON.stringify({ review_requests: ids.map(id => ({
  request_id: id, session_id: 's1', artifact_id: 'a1', artifact_name: 'plan.md', artifact_version: 1, ask: 'ok?', url: '',
})) }), { status: 200 })

beforeEach(() => { fetchMock.mockReset() })

it('reads again when a review request is decided or dismissed here', async () => {
  fetchMock.mockResolvedValueOnce(reply(['r1'])).mockResolvedValueOnce(reply([]))
  const { result } = renderHook(() => useMyReviewRequests(false))
  await waitFor(() => expect(result.current).toHaveLength(1))
  act(() => notifyReviewRequestsChanged())
  await waitFor(() => expect(result.current).toHaveLength(0))
  expect(fetchMock).toHaveBeenCalledTimes(2)
})

it('reads again when the page comes back into view', async () => {
  fetchMock.mockImplementation(() => Promise.resolve(reply([])))
  renderHook(() => useMyReviewRequests(false))
  await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1))
  act(() => { document.dispatchEvent(new Event('visibilitychange')) })
  await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2))
})
