import { afterEach, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { apiFetch } from '../apiFetch'
import UsageNoticeSettings from './UsageNoticeSettings'
vi.mock('../apiFetch', () => ({ apiFetch: vi.fn() }))
afterEach(() => vi.resetAllMocks())
it('loads and saves install-wide thresholds, and rejects unordered values', async () => {
  vi.mocked(apiFetch).mockResolvedValue(new Response(JSON.stringify({ thresholds: [80, 90, 95] })))
  render(<UsageNoticeSettings />)
  const first = await screen.findByRole('spinbutton', { name: 'Prefer other engines (%)' })
  fireEvent.change(first, { target: { value: '91' } })
  fireEvent.click(screen.getByRole('button', { name: 'Save usage thresholds' }))
  expect(await screen.findByRole('status')).toHaveTextContent('increasing whole percentages')
  expect(apiFetch).toHaveBeenCalledTimes(1)
  fireEvent.change(first, { target: { value: '75' } })
  fireEvent.click(screen.getByRole('button', { name: 'Save usage thresholds' }))
  await waitFor(() => expect(apiFetch).toHaveBeenCalledWith('/api/settings/usage-notices', expect.objectContaining({ method: 'PUT', body: JSON.stringify({ thresholds: [75, 90, 95] }) })))
  expect(await screen.findByRole('status')).toHaveTextContent('saved for this install')
})
it('hides administrator settings when access is refused', async () => {
  vi.mocked(apiFetch).mockResolvedValue(new Response('', { status: 403 }))
  const { container } = render(<UsageNoticeSettings />)
  await waitFor(() => expect(apiFetch).toHaveBeenCalled())
  expect(container).toBeEmptyDOMElement()
})
