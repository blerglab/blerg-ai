import { expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import '../index.css'
import SettingsDialog from './SettingsDialog'
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))
it.each([390, 3840])('assigns a desktop through Settings at %i px', async width => {
  await page.viewport(width, width === 390 ? 844 : 2160)
  apiFetch.mockImplementation((path: string, init?: RequestInit) => {
    const data = path === '/api/desktop-ownership' ? { desktops: [{ id: 'desktop', name: 'Workstation', owner_id: '', ownership_revision: 1 }], owners: [{ id: 'person', name: 'Person' }] }
      : init?.method ? {} : { rules: [], limits: { per_parent_running: 3, per_parent_starts_per_hour: 120, per_account_running: 6, per_account_starts_per_hour: 60 } }
    return Promise.resolve(new Response(JSON.stringify(data)))
  })
  render(<SettingsDialog onClose={() => {}} />)
  const owner = await screen.findByLabelText('Owner of Workstation')
  await userEvent.selectOptions(owner, 'person')
  await userEvent.click(screen.getByRole('button', { name: 'Assign desktop' }))
  await screen.findByRole('status')
  expect(apiFetch).toHaveBeenCalledWith('/api/desktop-ownership/desktop', expect.objectContaining({ method: 'PUT', body: JSON.stringify({ owner_id: 'person', ownership_revision: 1 }) }))
  const dialog = screen.getByRole('dialog', { name: 'Settings' }).getBoundingClientRect()
  const field = owner.getBoundingClientRect()
  expect(dialog.right).toBeLessThanOrEqual(width)
  expect(field.right).toBeLessThanOrEqual(width)
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
})
