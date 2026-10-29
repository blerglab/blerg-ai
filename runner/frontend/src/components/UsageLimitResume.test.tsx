import { afterEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { apiFetch } from '../apiFetch'
import UsageLimitResume from './UsageLimitResume'

vi.mock('../apiFetch', () => ({ apiFetch: vi.fn() }))
const request = vi.mocked(apiFetch)
const state = { enabled: false, state: '', reason: '', attempts: 0 }
function response(body: unknown = state, status = 200) {
  return Promise.resolve(new Response(JSON.stringify(body), { status }))
}

describe('UsageLimitResume', () => {
  afterEach(() => { vi.useRealTimers(); vi.resetAllMocks() })

  it('hides the control from nonowners without an error', async () => {
    request.mockImplementation(() => response({}, 404))
    const { container } = render(<UsageLimitResume sessionId="s1" />)
    await waitFor(() => expect(request).toHaveBeenCalledOnce())
    expect(container).toBeEmptyDOMElement()
  })

  it('enables through the authenticated endpoint and refetches authoritative state', async () => {
    request.mockImplementationOnce(() => response())
      .mockImplementationOnce(() => response())
      .mockImplementationOnce(() => response({ ...state, enabled: true, state: 'pending', reset_at: '2030-01-02T12:00:00Z' }))
    render(<UsageLimitResume sessionId="s1" />)
    const checkbox = await screen.findByRole('checkbox', { name: 'Resume when limit resets' })
    expect(checkbox).not.toBeChecked()
    fireEvent.click(checkbox)
    await waitFor(() => expect(checkbox).toBeChecked())
    expect(request).toHaveBeenNthCalledWith(2, '/api/sessions/s1/limit-resume', {
      method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: '{"enabled":true}',
    })
    expect(screen.getByRole('status')).toHaveTextContent('Resumes after')
  })

  it('cancels pending continuation by disabling and displays save failures', async () => {
    request.mockImplementationOnce(() => response({ ...state, enabled: true, state: 'blocked', reason: 'Sign in again.' }))
      .mockImplementationOnce(() => response({}, 500))
    render(<UsageLimitResume sessionId="s1" />)
    const checkbox = await screen.findByRole('checkbox')
    expect(screen.getByRole('status')).toHaveTextContent('Sign in again.')
    fireEvent.click(checkbox)
    expect(await screen.findByRole('alert')).toHaveTextContent('Could not update')
    expect(checkbox).toBeChecked()
    expect(JSON.parse(request.mock.calls[1][1]?.body as string)).toEqual({ enabled: false })
  })

  it('polls while mounted and stops polling when unmounted', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval', 'setTimeout', 'clearTimeout'] })
    request.mockImplementation(() => response({ ...state, enabled: true, state: 'unknown' }))
    const view = render(<UsageLimitResume sessionId="s1" />)
    await act(async () => { await vi.advanceTimersByTimeAsync(0) })
    expect(screen.getByRole('status')).toHaveTextContent('Reset time unknown; checking the usage meter.')
    await act(async () => { await vi.advanceTimersByTimeAsync(15_000) })
    expect(request).toHaveBeenCalledTimes(2)
    view.unmount()
    await vi.advanceTimersByTimeAsync(30_000)
    expect(request).toHaveBeenCalledTimes(2)
  })
})
