import { it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import SessionInsightSummary from './SessionInsightSummary'
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))
it('shows measured lifetime without inventing test share or review rounds', async () => {
  apiFetch.mockResolvedValue({ ok: true, json: async () => ({ lifetime_seconds: 23400 }) })
  render(<SessionInsightSummary sessionId="sess-a" />)
  expect(await screen.findByText('6 h 30 min · test time and review rounds unavailable')).toBeInTheDocument()
  expect(apiFetch).toHaveBeenCalledWith('/api/sessions/sess-a/insights')
})
