import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import DelegationRulesSection from './DelegationRulesSection'
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))
const limits = { per_parent_running: 3, per_parent_starts_per_hour: 120, per_account_running: 6, per_account_starts_per_hour: 60 }
const rule = { id: 'rule', engine: 'codex', runtime: 'docker', daemon_id: 'host', daemon_name: 'Workstation', local_only: false, created_at: '2026-10-17T10:00:00Z' }
const json = (body: unknown) => Promise.resolve({ ok: true, json: () => Promise.resolve(body) })
beforeEach(() => {
  apiFetch.mockReset()
  apiFetch.mockImplementation((path: string, init?: RequestInit) => init?.method ? json({}) : path === '/api/daemons' ? json([{ id: 'host', name: 'Workstation', mode: 'workstation', available_engines: ['codex', 'hermes', 'openclaw'] }]) : json({ rules: [rule], limits }))
})
describe('session-starting rules', () => {
  it('lists account rules and server limits, and removes the selected rule', async () => {
    render(<DelegationRulesSection />)
    expect(await screen.findByText(/Codex · desktop sandbox/)).toHaveTextContent('Workstation')
    expect(screen.getByText(/Each session can run 3/)).toHaveTextContent('start 60 an hour')
    fireEvent.click(screen.getByRole('button', { name: 'Remove' }))
    await waitFor(() => expect(apiFetch).toHaveBeenCalledWith('/api/delegation-rules/rule', { method: 'DELETE' }))
  })
  it('names a cluster rule by the cluster, not as an unsandboxed desktop', async () => {
    apiFetch.mockImplementation((path: string) => path === '/api/daemons' ? json([]) : json({ rules: [{ ...rule, runtime: 'cluster', daemon_id: 'cluster', daemon_name: 'cluster' }], limits }))
    render(<DelegationRulesSection />)
    const item = await screen.findByText(/Codex · on the cluster/)
    expect(item.textContent).not.toMatch(/unsandboxed|desktop/)
  })
  it('discovers available engines and defaults local-only on for Hermes', async () => {
    render(<DelegationRulesSection />)
    await screen.findByRole('button', { name: 'Add rule' })
    expect(apiFetch).toHaveBeenCalledWith('/api/daemons')
    expect(screen.queryByRole('option', { name: 'Claude' })).not.toBeInTheDocument()
    fireEvent.change(screen.getByLabelText('Rule engine'), { target: { value: 'hermes' } })
    expect(screen.getByLabelText('Local only')).toBeChecked()
    fireEvent.click(screen.getByRole('button', { name: 'Add rule' }))
    await waitFor(() => expect(apiFetch).toHaveBeenCalledWith('/api/delegation-rules', expect.objectContaining({ method: 'POST', body: JSON.stringify({ engine: 'hermes', runtime: 'docker', daemon_id: 'host', local_only: true }) })))
  })
  it('keeps OpenClaw host-only and explains unsandboxed access', async () => {
    render(<DelegationRulesSection />)
    await screen.findByRole('button', { name: 'Add rule' })
    fireEvent.change(screen.getByLabelText('Rule engine'), { target: { value: 'openclaw' } })
    expect(screen.getByLabelText('Rule runtime')).toHaveValue('daemon')
    expect(screen.getByRole('option', { name: 'desktop sandbox' })).toBeDisabled()
    expect(screen.getByText(/full filesystem access/)).toBeInTheDocument()
  })
  it('offers the cluster where the install has one, and adds a cluster rule that names no daemon', async () => {
    apiFetch.mockImplementation((path: string, init?: RequestInit) => init?.method ? json({}) : path === '/api/daemons' ? json([{ id: 'host', name: 'Workstation', mode: 'workstation', available_engines: ['codex', 'openclaw'] }])
      : path === '/api/cluster/status' ? json({ configured: true, available_engines: ['claude', 'codex'] }) : json({ rules: [rule], limits }))
    render(<DelegationRulesSection />)
    fireEvent.change(await screen.findByLabelText('Rule runtime'), { target: { value: 'cluster' } })
    expect(screen.queryByLabelText('Rule daemon')).not.toBeInTheDocument()
    expect(screen.queryByRole('option', { name: 'OpenClaw' })).not.toBeInTheDocument()
    fireEvent.change(screen.getByLabelText('Rule engine'), { target: { value: 'claude' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add rule' }))
    await waitFor(() => expect(apiFetch).toHaveBeenCalledWith('/api/delegation-rules', expect.objectContaining({ method: 'POST', body: JSON.stringify({ engine: 'claude', runtime: 'cluster', local_only: false }) })))
  })
  it('offers no cluster on a desktop-only install', async () => {
    render(<DelegationRulesSection />)
    await screen.findByRole('button', { name: 'Add rule' })
    expect(apiFetch).toHaveBeenCalledWith('/api/cluster/status')
    expect(screen.queryByRole('option', { name: 'on the cluster' })).not.toBeInTheDocument()
  })
  it('fails visibly on a missing or malformed rules response', async () => {
    apiFetch.mockImplementation(() => json({}))
    render(<DelegationRulesSection />)
    expect(await screen.findByRole('alert')).toHaveTextContent('Could not load')
    expect(screen.queryByRole('button', { name: 'Add rule' })).not.toBeInTheDocument()
  })
})
