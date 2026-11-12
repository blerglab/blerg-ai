import { browserWidths } from '../test/browserWidths'
import { expect, it, vi } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { page } from 'vitest/browser'
import BoardFileDraft from './BoardFileDraft'
import type { ProposalInfo } from '../types'

const apiFetch = vi.fn()
vi.mock('../apiFetch', () => ({ apiFetch: (...args: unknown[]) => apiFetch(...args) }))
vi.mock('../lib/sessionBoards', () => ({ useSessionBoards: () => ({ state: 'ready', configured: true, boards: [{ id: 'b1', name: 'Project board' }] }) }))

for (const width of browserWidths([390, 3840])) {
  it(`reviews and edits estimates at ${width}px`, async () => {
    await page.viewport(width, 900)
    const args = { board_id: 'b1', title: 'Review estimates', size: 'S', risk: 'low', complexity: 'high' }
    const proposal: ProposalInfo = { id: 'p1', kind: 'board_file', state: 'pending', arguments: args, arguments_raw: JSON.stringify(args), connection_id: '', connection_name: 'Board', tool: 'file_ticket', agent_summary: '', session_id: 's1', cron_id: null, created_at: '2026-01-01T00:00:00Z', expires_at: '2026-01-02T00:00:00Z', decided_at: null, decided_by: null, result: null }
    apiFetch.mockResolvedValue({ ok: true, json: async () => ({ ...proposal, arguments: { ...args, size: 'L' }, arguments_raw: JSON.stringify({ ...args, size: 'L' }) }) })
    const saved = vi.fn()
    render(<BoardFileDraft proposal={proposal} onSaved={saved} onCancel={() => {}} />)
    expect(screen.getByLabelText('Size')).toHaveValue('S')
    expect(screen.getByLabelText('Risk')).toHaveValue('low')
    expect(screen.getByLabelText('Complexity')).toHaveValue('high')
    fireEvent.change(screen.getByLabelText('Size'), { target: { value: 'L' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save draft' }))
    await waitFor(() => expect(saved).toHaveBeenCalledOnce())
    const sent = JSON.parse(apiFetch.mock.lastCall![1].body)
    expect(sent.draft).toMatchObject({ size: 'L', risk: 'low', complexity: 'high' })
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
    apiFetch.mockClear()
  })
}
