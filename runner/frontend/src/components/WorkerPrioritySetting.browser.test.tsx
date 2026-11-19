import { expect, it, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { page, commands } from 'vitest/browser'
import '../index.css'
import SettingsDialog from './SettingsDialog'

const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))
vi.mock('./DelegationRulesSection', () => ({ default: () => null }))

for (const theme of ['dark', 'colorblind-dark']) for (const width of [390, 3840]) it(`keeps desktop priority in Settings at ${width}px in ${theme}`, async () => {
 document.documentElement.setAttribute('data-theme',theme)
 await page.viewport(width, width === 390 ? 844 : 2160)
 apiFetch.mockImplementation((path: string) => Promise.resolve(new Response(JSON.stringify(
  path === '/api/daemons' ? [{ id: 'fixture', name: 'Desktop', engine_statuses: [] }] : path.endsWith('/worker-priority') ? { enabled: true, backend: 'systemd-run + nice + ionice' } : {}
 ))))
 render(<SettingsDialog onClose={() => {}} />)
 const input=await screen.findByRole('checkbox', { name: 'Give your interactive work priority' })
 await vi.waitFor(() => expect((input as HTMLInputElement).checked).toBe(true))
 input.scrollIntoView({ block: 'center' })
 const dialog=screen.getByRole('dialog', { name: 'Settings' })
 expect(dialog.scrollWidth).toBeLessThanOrEqual(dialog.clientWidth+1)
 const rect=input.getBoundingClientRect()
 expect(rect.left).toBeGreaterThanOrEqual(0)
 expect(rect.right).toBeLessThanOrEqual(width)
 fireEvent.focus(input)
 const shots=import.meta.env.VITE_SETTINGS_SHOTS
 if (shots) await commands.settingsScreenshot(width, theme)
})
