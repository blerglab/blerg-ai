import { browserWidths } from '../test/browserWidths'
import '../index.css'
import './ShellWorkspace.css'
import { render, screen, waitFor } from '@testing-library/react'
import { expect, it, vi } from 'vitest'
import { page, userEvent } from 'vitest/browser'
import HostShellConsentWithdrawal from './HostShellConsentWithdrawal'
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))

it.each(browserWidths([390, 3840]))('finds and withdraws consent without a live shell at %i px', async width => {
  await page.viewport(width, 900)
  apiFetch.mockReset()
  apiFetch.mockImplementation(async (url: string) => url.endsWith('/shell-consents')
    ? new Response(JSON.stringify({ consents: [{ id: 'original-acceptance', account: 'person', folder: `/project/${'long-folder-'.repeat(24)}` }], next: '' }), { status: 200 })
    : new Response(JSON.stringify({ withdrawn: true, cleanup_pending: true }), { status: 200 }))
  const view = render(<HostShellConsentWithdrawal session="offline-session" />)
  const button = await screen.findByRole('button', { name: 'Withdraw consent' })
  const bounds = button.getBoundingClientRect()
  expect(bounds.left).toBeGreaterThanOrEqual(0); expect(bounds.right).toBeLessThanOrEqual(width)
  await userEvent.click(button)
  await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('Consent withdrawn. Cleanup is pending.'))
  expect(apiFetch).toHaveBeenCalledWith('/api/sessions/offline-session/shell-consent/withdraw', expect.objectContaining({ method: 'POST', body: JSON.stringify({ consent_id: 'original-acceptance' }) }))
  expect(apiFetch.mock.calls.every(([url]) => !String(url).includes('/shells'))).toBe(true)
  view.unmount()
})
