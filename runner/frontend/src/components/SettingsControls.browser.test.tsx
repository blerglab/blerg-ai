// Card #86 in a real browser: Settings' buttons and dropdowns are the app's (no native look), with a
// focus ring and a dimmed disabled state, and the dialog fits a phone and uses a 4K screen.
import { describe, expect, it, vi, afterEach } from 'vitest'
import { render, screen } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import '../index.css'
import SettingsDialog from './SettingsDialog'

const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))

const limits = { per_parent_running: 3, per_parent_starts_per_hour: 120, per_account_running: 6, per_account_starts_per_hour: 60 }
const rule = { id: 'rule', engine: 'codex', runtime: 'docker', daemon_id: 'host', daemon_name: 'Workstation', local_only: false, created_at: '2026-10-17T10:00:00Z' }
const json = (body: unknown) => Promise.resolve(new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } }))

afterEach(() => vi.restoreAllMocks())

describe('Settings controls', () => {
  it.each([390, 3840])('draws every button and select in the app style at %i px', async width => {
    await page.viewport(width, width === 3840 ? 2160 : 844)
    apiFetch.mockImplementation((path: string) => path === '/api/daemons'
      ? json([{ id: 'host', name: 'Workstation', mode: 'workstation', available_engines: ['codex', 'claude'] }])
      : path === '/api/cluster/status' ? json({ configured: false }) : json({ rules: [rule], limits }))
    render(<SettingsDialog onClose={() => {}} />)
    await screen.findByRole('button', { name: 'Add rule' })
    const dialog = screen.getByRole('dialog', { name: 'Settings' })
    const selects = [...dialog.querySelectorAll('select')]
    expect(selects.length).toBe(5)
    for (const s of selects) {
      const cs = getComputedStyle(s)
      expect(cs.appearance).toBe('none')
      expect(cs.borderTopLeftRadius).toBe('8px')
      expect(cs.borderTopWidth).toBe('1px')
      expect(cs.backgroundImage).toContain('linear-gradient')
      const r = s.getBoundingClientRect()
      expect(r.right).toBeLessThanOrEqual(width)
      expect(r.height).toBeGreaterThanOrEqual(width < 640 ? 40 : 32)
    }
    for (const b of dialog.querySelectorAll<HTMLButtonElement>('button.btn')) {
      const cs = getComputedStyle(b)
      expect(cs.borderTopLeftRadius).not.toBe('0px')
      expect(cs.borderTopWidth).toBe('1px')
      expect(cs.fontFamily).toBe(getComputedStyle(dialog).fontFamily)
    }
    // Disabled: Save colors with nothing to save is dimmed.
    const save = screen.getByRole('button', { name: 'Save colors' })
    expect(save).toBeDisabled()
    expect(Number(getComputedStyle(save).opacity)).toBeLessThan(1)
    // Focus: a select reached from the keyboard has the app's ring.
    const engine = screen.getByRole('combobox', { name: 'Rule engine' })
    for (let i = 0; i < 40 && document.activeElement !== engine; i++) await userEvent.keyboard('{Tab}')
    expect(document.activeElement).toBe(engine)
    expect(getComputedStyle(engine).outlineStyle).toBe('solid')
    // The dialog fits the phone and widens on the 4K screen; nothing scrolls sideways.
    const box = dialog.getBoundingClientRect()
    expect(box.right).toBeLessThanOrEqual(width)
    expect(dialog.scrollWidth).toBeLessThanOrEqual(dialog.clientWidth + 1)
    if (width === 3840) expect(box.width).toBeGreaterThan(1600)
  })
})
