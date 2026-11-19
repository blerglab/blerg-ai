import { afterEach, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import WorkerPrioritySetting from './WorkerPrioritySetting'
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))
afterEach(() => vi.resetAllMocks())
it('loads the actual desktop policy and persists changes before showing success', async () => {
 apiFetch.mockResolvedValueOnce(new Response(JSON.stringify({ enabled: true, backend: 'nice + ionice' })))
 apiFetch.mockResolvedValueOnce(new Response(JSON.stringify({ enabled: false, backend: 'nice + ionice' })))
 render(<WorkerPrioritySetting daemonId="d1" />)
 const checkbox = screen.getByRole('checkbox', { name: 'Give your interactive work priority' })
 expect(checkbox).toBeDisabled()
 await waitFor(() => expect(checkbox).toBeChecked())
 fireEvent.click(checkbox)
 await waitFor(() => expect(checkbox).not.toBeChecked())
 expect(apiFetch).toHaveBeenLastCalledWith('/api/daemons/d1/worker-priority', expect.objectContaining({ method: 'PUT', body: JSON.stringify({ enabled: false }) }))
})
it('keeps the saved value when saving fails', async () => {
 apiFetch.mockResolvedValueOnce(new Response(JSON.stringify({ enabled: true, backend: 'nice' })))
 apiFetch.mockResolvedValueOnce(new Response('{}', { status: 500 }))
 render(<WorkerPrioritySetting daemonId="d1" />)
 const checkbox=screen.getByRole('checkbox')
 await waitFor(() => expect(checkbox).toBeChecked())
 fireEvent.click(checkbox)
 expect(await screen.findByRole('alert')).toHaveTextContent('Could not save')
 expect(checkbox).toBeChecked()
})
it('does not pretend an older or disconnected daemon supports the policy', async () => {
 apiFetch.mockResolvedValue(new Response('{}', { status: 502 }))
 render(<WorkerPrioritySetting daemonId="old" />)
 expect(await screen.findByRole('alert')).toHaveTextContent('Update or reconnect')
 expect(screen.getByRole('checkbox')).toBeDisabled()
})
