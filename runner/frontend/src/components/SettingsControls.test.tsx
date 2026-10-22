// Card #86: Settings shows no browser-default button or dropdown. Every button and select in the
// dialog, the session-starting rules included, wears one of the app's styled classes.
import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import SettingsDialog from './SettingsDialog'

const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))

const limits = { per_parent_running: 3, per_parent_starts: 20, per_account_running: 6, per_account_starts_per_hour: 60 }
const rule = { id: 'rule', engine: 'codex', runtime: 'docker', daemon_id: 'host', daemon_name: 'Workstation', local_only: false, created_at: '2026-10-17T10:00:00Z' }
const json = (body: unknown, ok = true) => Promise.resolve({ ok, status: ok ? 200 : 500, json: () => Promise.resolve(body) })

// The app's styled classes for a button (the general .btn, and the dialog's own close ✕, preview
// segments and Reset links) and for a select.
const BUTTON_CLASSES = ['btn', 'caps-close', 'cc-seg', 'cc-reset']

function unstyled(root: HTMLElement): string[] {
  const out: string[] = []
  root.querySelectorAll('button').forEach(b => { if (!BUTTON_CLASSES.some(c => b.classList.contains(c))) out.push(`button "${b.textContent}"`) })
  root.querySelectorAll('select').forEach(s => { if (!s.classList.contains('select')) out.push(`select "${s.getAttribute('aria-label') ?? s.dataset.testid}"`) })
  return out
}

describe('Settings controls', () => {
  it('has no unstyled native button or select, with rules listed and a color customized', async () => {
    apiFetch.mockImplementation((path: string) => path === '/api/daemons'
      ? json([{ id: 'host', name: 'Workstation', mode: 'workstation', available_engines: ['codex', 'claude'] }])
      : path === '/api/cluster/status' ? json({ configured: false }) : json({ rules: [rule], limits }))
    render(<SettingsDialog onClose={() => {}} />)
    await screen.findByRole('button', { name: 'Add rule' })
    // A Reset appears once a color is changed.
    fireEvent.change(screen.getAllByLabelText(/ color$/)[0], { target: { value: '#123456' } })
    expect(screen.getByRole('button', { name: 'Reset' })).toBeInTheDocument()
    const dialog = screen.getByRole('dialog', { name: 'Settings' })
    expect(dialog.querySelectorAll('select').length).toBeGreaterThanOrEqual(4)
    expect(unstyled(dialog)).toEqual([])
    expect(screen.getByRole('button', { name: 'Remove' })).toHaveClass('btn', 'danger')
    expect(screen.getByRole('button', { name: 'Add rule' })).toHaveClass('btn', 'primary')
  })

  it('styles the Retry a failed rules load offers', async () => {
    apiFetch.mockImplementation(() => json({}, false))
    render(<SettingsDialog onClose={() => {}} />)
    await screen.findByRole('button', { name: 'Retry' })
    expect(unstyled(screen.getByRole('dialog', { name: 'Settings' }))).toEqual([])
  })
})
