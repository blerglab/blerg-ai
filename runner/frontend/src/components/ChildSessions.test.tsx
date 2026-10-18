import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { ChildSessionRow, StartedSessions } from './ChildSessions'
import { OpenSessionContext } from '../lib/openSession'
import type { LiveChildSession } from '../lib/liveState'
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))
vi.mock('../ws', () => import('../test/wsMock'))
const child: LiveChildSession = { id: 'child', title: 'Review runner', engine: 'codex', model: 'fixture-model', effort: 'high', status: 'running', started_at: '2026-10-17T10:00:00Z', ended_at: null, asking: true, tokens: 12000 }
const now = Date.parse('2026-10-17T10:02:00Z')
beforeEach(() => apiFetch.mockReset())
describe('started-session rows', () => {
  it('shows actual engine/model/effort/tokens/asking and opens the child in app', () => {
    const open = vi.fn()
    render(<MemoryRouter><OpenSessionContext.Provider value={open}><ul><ChildSessionRow child={child} now={now} /></ul></OpenSessionContext.Provider></MemoryRouter>)
    expect(screen.getByLabelText('Codex')).toHaveTextContent('Cx')
    expect(screen.getByText('fixture-model')).toBeInTheDocument()
    expect(screen.getByText('high')).toBeInTheDocument()
    expect(screen.getByText('asking')).toBeInTheDocument()
    expect(screen.getByText(/tokens/)).toHaveTextContent('12k')
    expect(screen.queryByText(/\$/)).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button'))
    expect(open).toHaveBeenCalledWith('child')
  })
  it('shows cost only when provided and never guesses hidden tokens', () => {
    render(<MemoryRouter><ul><ChildSessionRow child={{ ...child, tokens: null, cost_usd: 1.25 }} now={now} /></ul></MemoryRouter>)
    expect(screen.getByText(/1.25/)).toBeInTheDocument()
    expect(screen.queryByText(/tokens/)).not.toBeInTheDocument()
  })
  it('collapses ended children past five and expands the complete list', () => {
    const children = Array.from({ length: 7 }, (_, i) => ({ ...child, id: `c${i}`, title: `Child ${i}`, status: 'ended', ended_at: '2026-10-17T10:01:00Z' }))
    render(<MemoryRouter><StartedSessions sessionId="parent" children={children} grants={[]} now={now} /></MemoryRouter>)
    expect(screen.getAllByTestId('rail-child')).toHaveLength(5)
    fireEvent.click(screen.getByRole('button', { name: 'Show all 7' }))
    expect(screen.getAllByTestId('rail-child')).toHaveLength(7)
  })
  it('revokes the exact grant and reports a refusal without pretending success', async () => {
    apiFetch.mockResolvedValue({ ok: false })
    render(<MemoryRouter><StartedSessions sessionId="parent" children={[]} grants={[{ id: 'grant-id', engine: 'codex', runtime: 'docker' }]} now={now} /></MemoryRouter>)
    fireEvent.click(screen.getByRole('button', { name: /Revoke Codex/ }))
    await waitFor(() => expect(apiFetch).toHaveBeenCalledWith('/api/delegation-grants/grant-id/revoke', { method: 'POST' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Could not revoke')
  })
})
