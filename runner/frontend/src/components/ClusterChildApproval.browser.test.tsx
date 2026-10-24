import { afterEach, expect, it, vi } from 'vitest'
import { cleanup, render, screen } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import { MemoryRouter } from 'react-router-dom'
import DelegationLiveReview from './DelegationLiveReview'
import '../index.css'

vi.mock('../apiFetch', () => ({ apiFetch: vi.fn(async () => ({ ok: true, json: async () => ({
  request_id: 'request', parent_session_id: 'parent', status: 'pending',
  expires_at: '2099-01-01T00:00:00Z', offered: ['once', 'deny'],
  request: { engine: 'codex', runtime: 'cluster', title: 'Review selected repositories', prompt: 'Review changes', repos: ['member-one', 'member-two'] },
}) })) }))
afterEach(cleanup)
it.each([390, 3840])('lists the exact cluster child subset on the approval card at %ipx', async width => {
  await page.viewport(width, 900)
  render(<MemoryRouter><DelegationLiveReview sessionId="parent" requestId="request" /></MemoryRouter>)
  await userEvent.click(screen.getByRole('button', { name: 'Review request' }))
  const members = await screen.findByText('member-one, member-two')
  expect(members).toBeVisible()
  expect(await screen.findByRole('button', { name: 'Allow once' })).toBeVisible()
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
})
