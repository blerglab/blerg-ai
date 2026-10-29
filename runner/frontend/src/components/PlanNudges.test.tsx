import { afterEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { apiFetch } from '../apiFetch'
import PlanNudges from './PlanNudges'

vi.mock('../apiFetch', () => ({ apiFetch: vi.fn() }))
const request = vi.mocked(apiFetch)
function response(body: unknown, status = 200) {
  return Promise.resolve(new Response(JSON.stringify(body), { status }))
}

describe('PlanNudges', () => {
  afterEach(() => { vi.resetAllMocks() })

  it('is on by default and turns off through the owner endpoint', async () => {
    request.mockImplementationOnce(() => response({ enabled: true }))
      .mockImplementationOnce(() => response({ enabled: false }))
    render(<PlanNudges sessionId="s1" />)
    const checkbox = await screen.findByRole('checkbox', { name: 'Nudge when the plan drifts' })
    expect(checkbox).toBeChecked()
    fireEvent.click(checkbox)
    await waitFor(() => expect(checkbox).not.toBeChecked())
    expect(request).toHaveBeenNthCalledWith(1, '/api/sessions/s1/plan-nudges', { signal: expect.any(AbortSignal) })
    expect(request).toHaveBeenNthCalledWith(2, '/api/sessions/s1/plan-nudges', {
      method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: '{"enabled":false}',
    })
  })

  it('keeps the setting and says so when saving fails', async () => {
    request.mockImplementationOnce(() => response({ enabled: true }))
      .mockImplementationOnce(() => response({}, 500))
    render(<PlanNudges sessionId="s1" />)
    const checkbox = await screen.findByRole('checkbox')
    fireEvent.click(checkbox)
    expect(await screen.findByRole('alert')).toHaveTextContent('Could not update')
    expect(checkbox).toBeChecked()
  })

  it('shows nothing to someone who is not the owner', async () => {
    request.mockImplementation(() => response({}, 404))
    const { container } = render(<PlanNudges sessionId="s1" />)
    await waitFor(() => expect(request).toHaveBeenCalledOnce())
    expect(container).toBeEmptyDOMElement()
  })
})
