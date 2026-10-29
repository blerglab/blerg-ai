import { beforeEach, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { page } from 'vitest/browser'
import '../index.css'
import SettingsDialog from './SettingsDialog'
import DesktopEnrollNotice from './DesktopEnrollNotice'

const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))
beforeEach(() => {
  vi.clearAllMocks()
  apiFetch.mockImplementation(() => Promise.resolve(new Response(JSON.stringify({ rules: [], limits: { per_parent_running: 3, per_parent_starts_per_hour: 120, per_account_running: 6, per_account_starts_per_hour: 60 } }))))
})

const shots = (import.meta.env as Record<string, string | undefined>).VITE_SETTINGS_SHOTS

// #193: desktops are managed in core (Settings → Account → Your desktops). Runner keeps a small,
// styled link where people look today, and a one-line notice on sessions, never the old banner.
it.each([390, 3840])('links Runner Settings to core Your desktops at %i px', async width => {
  await page.viewport(width, width === 390 ? 844 : 2160)
  render(<SettingsDialog onClose={() => {}} />)
  const link = await screen.findByRole('link', { name: 'Manage your desktops' })
  expect(link.getAttribute('href')).toMatch(/\/settings#desktops$/)
  expect(link).toHaveClass('settings-action')
  const css = getComputedStyle(link)
  expect(css.borderTopLeftRadius).toBe('8px')
  expect(css.borderTopStyle).toBe('solid')
  expect(css.textDecorationLine).toBe('none')
  expect(screen.queryByRole('button', { name: 'Enroll' })).toBeNull()
  expect(screen.queryByText('Desktop ownership')).toBeNull()
  expect(link.getBoundingClientRect().right).toBeLessThanOrEqual(width)
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
  link.closest('section')!.scrollIntoView({ block: 'start' })
  if (shots) await page.screenshot({ path: `${shots}/runner-settings-desktops-${width}.png` })
})

it.each([390, 3840])('shows a compact enroll notice on a session at %i px', async width => {
  await page.viewport(width, 900)
  render(<div><DesktopEnrollNotice /><DesktopEnrollNotice refused reason="The server refused this desktop’s credential." /></div>)
  const notices = screen.getAllByTestId('desktop-enroll-notice')
  expect(notices).toHaveLength(2)
  expect(screen.getByText('This desktop isn’t enrolled to you yet.')).toBeVisible()
  for (const notice of notices) {
    const box = notice.getBoundingClientRect()
    expect(box.right).toBeLessThanOrEqual(width)
    // One line on a wide screen; at most two on a phone. Never a full panel.
    expect(box.height).toBeLessThan(width === 390 ? 72 : 44)
    expect(getComputedStyle(notice).borderLeftWidth).toBe('3px')
    const link = notice.querySelector('a')!
    expect(link.textContent).toBe('Enroll in Settings')
    expect(link.getAttribute('href')).toMatch(/\/settings#desktops$/)
    expect(link.getAttribute('target')).toBe('_blank')
  }
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
  if (shots) await page.screenshot({ path: `${shots}/runner-enroll-notice-${width}.png` })
})
