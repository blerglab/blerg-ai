// Settings → Shells in a real browser: the administrator's limit controls fit a phone (stacked,
// nothing scrolling sideways) and use a 4K screen (the two limit groups side by side).
import { describe, expect, it, vi, afterEach } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import { page } from 'vitest/browser'
import '../index.css'
import SettingsDialog from './SettingsDialog'

const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))
vi.mock('./DelegationRulesSection', () => ({ default: () => null }))
vi.mock('./EngineStatusSection', () => ({ default: () => null }))

const defaults = { desktop: { detached_seconds: 0, idle_seconds: 0 }, cluster: { detached_seconds: 300, idle_seconds: 1800 } }
const json = (body: unknown, status = 200) => Promise.resolve(new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } }))

afterEach(() => vi.restoreAllMocks())

describe('Shell limits', () => {
  it.each([390, 3840])('fits and uses the space at %i px', async width => {
    await page.viewport(width, width === 3840 ? 2160 : 844)
    apiFetch.mockImplementation((path: string) => path === '/api/shells/settings'
      ? json({ ...defaults, defaults, stored: { desktop: { detached_seconds: null, idle_seconds: null }, cluster: { detached_seconds: null, idle_seconds: null } }, min_seconds: 600, max_seconds: 87600 * 3600 }) : json({}, 404))
    render(<SettingsDialog onClose={() => {}} />)
    const section = await screen.findByTestId('shell-limits')
    fireEvent.change(await screen.findByRole('combobox', { name: 'Desktop: close a shell with no typing after' }), { target: { value: 'after' } })
    const dialog = screen.getByRole('dialog', { name: 'Settings' })
    expect(dialog.scrollWidth).toBeLessThanOrEqual(dialog.clientWidth + 1)
    for (const control of section.querySelectorAll<HTMLElement>('select, input, button')) {
      const r = control.getBoundingClientRect()
      expect(r.left).toBeGreaterThanOrEqual(0)
      expect(r.right).toBeLessThanOrEqual(Math.min(width, dialog.getBoundingClientRect().right) + 1)
      expect(r.height).toBeGreaterThanOrEqual(width < 640 ? 40 : 32)
    }
    for (const s of section.querySelectorAll('select')) expect(getComputedStyle(s).appearance).toBe('none')
    const [detached, idle] = [...section.querySelectorAll<HTMLElement>('fieldset')].map(f => f.getBoundingClientRect())
    if (width === 3840) {
      expect(dialog.getBoundingClientRect().width).toBeGreaterThan(1600)
      expect(Math.abs(detached.top - idle.top)).toBeLessThan(2) // side by side
    } else {
      expect(idle.top).toBeGreaterThanOrEqual(detached.bottom) // stacked
    }
    expect(screen.getByText(/loses its network connection/).getBoundingClientRect().right).toBeLessThanOrEqual(width)
    const shots = (import.meta.env as Record<string, string | undefined>).VITE_SETTINGS_SHOTS
    if (shots) {
      section.scrollIntoView({ block: 'start' })
      await page.screenshot({ path: `${shots}/runner-shell-limits-${width}.png` })
    }
  })
})
