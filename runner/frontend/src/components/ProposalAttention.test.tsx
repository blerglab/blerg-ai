import { fireEvent, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { expect, it, vi } from 'vitest'
import { PendingProposal } from './ProposalAttention'
import type { ProposalInfo } from '../types'

vi.mock('../ws', () => import('../test/wsMock'))
const apiFetch = vi.fn()
vi.mock('../apiFetch', () => ({ apiFetch: (...args: unknown[]) => apiFetch(...args) }))
const draft = { id: 'draft', kind: 'board_file', state: 'pending', tool: 'file_ticket', connection_name: 'Board', arguments: { title: 'Fix export', body: 'Full proposed body' }, arguments_raw: '{"title":"Fix export"}', expires_at: '2099-01-01T00:00:00Z', connection_id: '', agent_summary: '', session_id: null, cron_id: null, created_at: '2026-01-01T00:00:00Z', decided_at: null, decided_by: null, result: null } satisfies ProposalInfo
it('opens a compact filing row in the ticket view with draft, edit, decisions and exact request', () => {
  render(<MemoryRouter><PendingProposal proposal={draft} refresh={() => {}} /></MemoryRouter>)
  expect(screen.queryByText('Full proposed body')).toBeNull()
  expect(screen.getByText(/Expires/)).toBeTruthy()
  fireEvent.click(screen.getByRole('button', { name: /DRAFT Fix export/ }))
  expect(screen.getByRole('dialog')).toHaveTextContent('DRAFT')
  expect(screen.getByText('Full proposed body')).toBeTruthy()
  expect(screen.getByText('Show exact request')).toBeTruthy()
  expect(screen.getByRole('button', { name: 'Approve' })).toBeTruthy()
  expect(screen.getByRole('button', { name: 'Reject' })).toBeTruthy()
  expect(screen.getByRole('button', { name: 'Edit ticket draft' })).toBeTruthy()
  fireEvent.click(screen.getByRole('button', { name: 'Close' }))
  expect(screen.queryByRole('dialog')).toBeNull()
})
