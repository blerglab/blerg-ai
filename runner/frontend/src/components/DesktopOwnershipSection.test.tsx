import { afterEach, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import DesktopOwnershipSection from './DesktopOwnershipSection'
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))
afterEach(() => vi.resetAllMocks())
const view = { desktops: [{ id: 'desktop', name: 'Workstation', owner_id: '', ownership_revision: 1 }], owners: [{ id: 'person', name: 'Person' }] }
it('assigns a desktop with its current revision', async () => {
  apiFetch.mockImplementation((_path: string, init?: RequestInit) => Promise.resolve(new Response(JSON.stringify(init?.method ? {} : view))))
  render(<DesktopOwnershipSection />)
  fireEvent.change(await screen.findByLabelText('Owner of Workstation'), { target: { value: 'person' } })
  fireEvent.click(screen.getByRole('button', { name: 'Assign desktop' }))
  await waitFor(() => expect(apiFetch).toHaveBeenCalledWith('/api/desktop-ownership/desktop', expect.objectContaining({ method: 'PUT', body: JSON.stringify({ owner_id: 'person', ownership_revision: 1 }) })))
  expect(await screen.findByRole('status')).toHaveTextContent('Owner recorded')
})
it('hides administrative inventory from a member', async () => {
  apiFetch.mockResolvedValue(new Response('', { status: 403 }))
  render(<DesktopOwnershipSection />)
  await waitFor(() => expect(apiFetch).toHaveBeenCalled())
  expect(screen.queryByRole('region', { name: 'Desktop ownership' })).not.toBeInTheDocument()
})
