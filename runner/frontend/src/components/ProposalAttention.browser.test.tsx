import { afterEach, describe, expect, it, vi } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { page, userEvent } from 'vitest/browser'
import { PendingProposal } from './ProposalAttention'
import { ProposalBell } from './ProposalNotifications'
import type { ProposalInfo } from '../types'
import { useMessageStore } from '../hooks/useMessageStore'
import '@blerglab/chat/blerg.css'

const apiFetch = vi.fn()
vi.mock('../apiFetch', () => ({ apiFetch: (...args: unknown[]) => apiFetch(...args) }))
vi.mock('../ws', () => import('../test/wsMock'))
afterEach(() => apiFetch.mockReset())
const draft: ProposalInfo = { id: 'p1', state: 'pending', kind: 'board_file', connection_id: '', connection_name: 'Board', tool: 'file_ticket', arguments: { title: 'A complete readable proposal for a narrow session panel', type: 'bug', repos: ['example-org/example-repo'], body: '## Reproduction\n\n' + 'A detailed paragraph to read.\n\n'.repeat(60) + 'The final expected result.' }, arguments_raw: '{"large":12345678901234567890,"decimal":1.10}', agent_summary: '', session_id: 's1', cron_id: null, created_at: '2026-01-01T00:00:00Z', expires_at: new Date(Date.now() + 3600_000).toISOString(), decided_at: null, decided_by: null, result: null }

describe('readable proposal requests in Chromium', () => {
  it.each([390, 1280, 3840])('keeps the entire preview and exact request readable at %i px', async width => {
    const height = width === 3840 ? 2160 : 900
    await page.viewport(width, height)
    const refresh = vi.fn()
    apiFetch.mockResolvedValue({ ok: true, status: 200, json: async () => ({}) })
    const { container } = render(<MemoryRouter><div style={{ width: 'min(100%, 360px)', boxSizing: 'border-box', padding: 8 }}><PendingProposal proposal={draft} refresh={refresh} /></div></MemoryRouter>)
    const body = screen.getByRole('region', { name: 'Proposed card body' })
    expect(within(body).getByRole('heading', { name: 'Reproduction' })).toBeTruthy()
    expect(within(body).getByText('The final expected result.')).toBeTruthy()
    expect(body.scrollHeight).toBeGreaterThan(body.clientHeight)
    // The preview's height scales with the screen instead of a fixed cap.
    expect(parseFloat(getComputedStyle(body).maxHeight)).toBeCloseTo(Math.max(240, height * 0.6), 0)
    expect(container.querySelector('img,script')).toBeNull()
    expect(container.scrollWidth).toBeLessThanOrEqual(width)
    for (const button of container.querySelectorAll<HTMLElement>('button')) {
      const box = button.getBoundingClientRect()
      expect(box.right).toBeLessThanOrEqual(width)
      if (width === 390) expect(box.height).toBeGreaterThanOrEqual(44)
    }
    await userEvent.click(screen.getByText('Show exact request'))
    const exact = screen.getByTestId('proposal-args-p1')
    expect(exact.textContent).toContain('12345678901234567890')
    expect(exact.textContent).toContain('1.10')
    await userEvent.click(screen.getByRole('button', { name: 'Approve' }))
    expect(exact.closest('details')!.open).toBe(true)
    await userEvent.click(screen.getByRole('button', { name: 'Confirm approve' }))
    expect(apiFetch).toHaveBeenCalledWith('/api/proposals/p1/approve', expect.objectContaining({ method: 'POST', body: JSON.stringify({ expected_arguments_raw: draft.arguments_raw }) }))
    expect(refresh).toHaveBeenCalledOnce()
  })
  it.each([390, 3840])('sizes the notification list with the screen at %i px', async width => {
    await page.viewport(width, width === 3840 ? 2160 : 844)
    render(<MemoryRouter><div style={{ display: 'flex', justifyContent: 'flex-end' }}><ProposalBell inbox={{ pending: [draft], refresh: async () => {}, error: null }} /></div></MemoryRouter>)
    await userEvent.click(screen.getByRole('button', { name: /Notifications/ }))
    const list = screen.getByRole('region', { name: 'Notifications' }).getBoundingClientRect()
    expect(list.width).toBeLessThanOrEqual(width)
    if (width === 3840) expect(list.width).toBeGreaterThan(1400)
    else expect(list.width).toBeGreaterThan(300)
  })
  it.each([390, 3840])('lists an agent question in the bell, readable at %i px', async width => {
    await page.viewport(width, width === 3840 ? 2160 : 844)
    useMessageStore.setState({ messages: [{ id: 'q1', session_id: 's1', kind: 'ask', body: 'Keep the v1 endpoints for one more release, or remove them now? ' + 'Context. '.repeat(30), status: 'open', created_at: new Date().toISOString(), no_wait: true, answerable: true }] })
    try {
      render(<MemoryRouter><div style={{ display: 'flex', justifyContent: 'flex-end' }}><ProposalBell inbox={{ pending: [], refresh: async () => {}, error: null }} /></div></MemoryRouter>)
      await userEvent.click(screen.getByRole('button', { name: /1 question/ }))
      const row = screen.getByTestId('bell-question').getBoundingClientRect()
      expect(row.left).toBeGreaterThanOrEqual(0)
      expect(row.right).toBeLessThanOrEqual(width)
      expect(screen.getByRole('link', { name: 'Answer in the session' })).toBeTruthy()
    } finally {
      useMessageStore.setState({ messages: [] })
    }
  })
})
