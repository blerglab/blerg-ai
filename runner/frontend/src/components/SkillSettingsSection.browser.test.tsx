import { render, screen, waitFor } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import { expect, it, vi } from 'vitest'
import SettingsDialog from './SettingsDialog'
import CapabilitiesTab from './CapabilitiesTab'
import '../index.css'
import './SessionRail.css'
vi.mock('../apiFetch', () => ({ apiFetch: vi.fn(async (url: string) => new Response(JSON.stringify(url.endsWith('/skills') ? { 'test-driven-development': true, 'ux-design': false, coordinating: false } : { install: { 'ux-design': false }, account: {}, workspace: {}, effective: {}, admin: true, workspaces: [{ id: 'garden', title: 'Plan the garden' }], rules: [], limits: {} }))) }))
const shots = (import.meta.env as Record<string, string | undefined>).VITE_SKILL_SHOTS
it.each([390, 3840])('keeps skill settings and applied panel usable at %i px', async width => {
  await page.viewport(width, width === 390 ? 844 : 2160)
  const settings = render(<SettingsDialog onClose={() => {}} />)
  await userEvent.click(screen.getByText('Skills'))
  const control = screen.getByLabelText('Test-driven development')
  await waitFor(() => expect(control).toBeEnabled())
  const select = screen.getByLabelText('Apply to')
  expect(select.getBoundingClientRect().width).toBeLessThan(300)
  expect(screen.getByLabelText('UX design')).toBeDisabled()
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
  control.closest('details')!.scrollIntoView({ block: 'center' })
  if (shots) await page.screenshot({ path: `${shots}/skill-settings-${width}.png` })
  settings.unmount()
  render(<aside className="session-rail" style={{ maxWidth: 380, padding: 16 }}><CapabilitiesTab sessionId="worker" report={null} pendingToolChanges={0} /></aside>)
  await screen.findByText('test-driven-development')
  expect(screen.getByText('ux-design')).toBeInTheDocument()
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
  if (shots) await page.screenshot({ path: `${shots}/session-skills-${width}.png` })
})
