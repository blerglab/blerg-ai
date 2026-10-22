// Card #142: End session asks in a small confirmation anchored to the header's ✕ (a bottom sheet on a
// phone), in real Chromium at 390 and 3840 px: the ✕ never arms for a second press, the focus starts
// on Cancel, Esc and a press outside cancel with the focus back on the ✕, End session ends, and its
// red is the only red while the question is open.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import '../index.css'
import '@blerglab/chat/blerg.css'
import { useSessionStore } from '../hooks/useSessionStore'
import { makeDaemon, makeSession } from '../test/fixtures'

vi.mock('../ws', () => import('../test/wsMock'))
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))

import SessionDetail from './SessionDetail'

const shotDir = (import.meta.env as Record<string, string | undefined>).VITE_END_SHOT_DIR
const json = (body: unknown) => Promise.resolve(new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } }))
const deletes = () => apiFetch.mock.calls.filter(([u, i]) => u === '/api/sessions/sess-a' && i?.method === 'DELETE')

beforeEach(() => {
  apiFetch.mockReset()
  apiFetch.mockImplementation((_url: string, init?: RequestInit) => init?.method === 'DELETE' ? Promise.resolve(new Response(null, { status: 204 })) : json({}))
  vi.stubGlobal('fetch', vi.fn(() => json([])))
  useSessionStore.setState({
    daemons: [makeDaemon()],
    sessions: [makeSession({ id: 'sess-a', kind: 'agent', runtime: 'daemon', status: 'running', title: 'Ship the release notes' })],
    statusChangedAt: {}, serverVersion: 'test',
  })
})
afterEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

function mount() {
  return render(
    <MemoryRouter initialEntries={['/sessions/sess-a']}>
      <div style={{ height: '100vh' }}>
        <Routes>
          <Route path="/sessions/:id" element={<SessionDetail />} />
          <Route path="/sessions" element={<p>HOME</p>} />
        </Routes>
      </div>
    </MemoryRouter>,
  )
}

const color = (value: string) => {
  const probe = document.createElement('span')
  probe.style.color = value
  document.body.appendChild(probe)
  const c = getComputedStyle(probe).color
  probe.remove()
  return c
}
const cssVar = (name: string) => getComputedStyle(document.documentElement).getPropertyValue(name).trim()

describe.each([
  { width: 390, height: 844 },
  { width: 3840, height: 2160 },
])('End session at $width px', ({ width, height }) => {
  beforeEach(async () => { await page.viewport(width, height) })

  it('asks in an alertdialog anchored to the ✕, never by arming the ✕', async () => {
    mount()
    const end = await screen.findByTestId('stop-session')
    await userEvent.click(end)
    const dialog = screen.getByRole('alertdialog', { name: 'End this session?' })
    expect(dialog.getAttribute('aria-describedby')).toBeTruthy()
    expect(document.getElementById(dialog.getAttribute('aria-describedby')!)!.textContent).toBe('The agent stops. The conversation and files are kept.')
    // No arming: the ✕ keeps its name, its look is the selected blue (not a filled red), nothing ended.
    expect(end.getAttribute('aria-label')).toBe('End session')
    expect(end.className).not.toContain('armed')
    // (after the button's 0.15 s color transition)
    const blue = color(cssVar('--selected-border'))
    await waitFor(() => expect(getComputedStyle(end).borderTopColor).toBe(blue))
    expect(deletes()).toHaveLength(0)
    // The focus starts on Cancel; End session is the only red.
    const cancel = within(dialog).getByRole('button', { name: 'Cancel' })
    const confirm = within(dialog).getByRole('button', { name: 'End session' })
    await waitFor(() => expect(document.activeElement).toBe(cancel))
    const red = color(cssVar('--danger'))
    expect(getComputedStyle(confirm).backgroundColor).toBe(red)
    expect(getComputedStyle(cancel).backgroundColor).not.toBe(red)
    expect(getComputedStyle(end).color).not.toBe(red)
    // Placed: under the ✕ and inside the screen on a wide one; a bottom sheet on a phone.
    const d = dialog.getBoundingClientRect()
    const a = end.getBoundingClientRect()
    expect(d.left).toBeGreaterThanOrEqual(0)
    expect(d.right).toBeLessThanOrEqual(width)
    // Above every other layer of the app (the bell's list 1500, Discard 1800), below hovercards.
    expect(Number(getComputedStyle(dialog).zIndex)).toBeGreaterThan(1800)
    // The rest of the page is inert while it asks.
    expect(end.closest('[inert]')).not.toBeNull()
    expect(dialog.closest('[inert]')).toBeNull()
    const c = cancel.getBoundingClientRect()
    const e = confirm.getBoundingClientRect()
    if (width <= 640) {
      expect(Math.round(d.bottom)).toBe(height)
      expect(Math.round(d.width)).toBe(width)
      // A phone's action sheet: End session on top, Cancel last, nearest the thumb; both full width.
      if (shotDir) await page.screenshot({ path: `${shotDir}/end-session-390.png` })
      expect(e.bottom).toBeLessThanOrEqual(c.top)
      expect(Math.round(e.width)).toBe(Math.round(c.width))
      expect(c.height).toBeGreaterThanOrEqual(44)
    } else {
      // Cancel, then End session on its right, as the app's other confirmations.
      expect(c.right).toBeLessThanOrEqual(e.left)
      expect(Math.abs(c.top - e.top)).toBeLessThan(1)
      expect(d.top).toBeGreaterThanOrEqual(a.bottom)
      expect(d.top - a.bottom).toBeLessThan(16)
      expect(Math.abs(d.right - a.right)).toBeLessThanOrEqual(1)
      expect(d.width).toBeLessThan(400)
    }
    // A second press where the ✕ is lands on the page outside the question, which cancels; it can
    // never reach the ✕ as a second, ending press.
    const hit = document.elementFromPoint(a.left + a.width / 2, a.top + a.height / 2)
    expect(hit?.className).toBe('confirm-popover-scrim')
    await userEvent.click(hit!)
    expect(screen.queryByRole('alertdialog')).toBeNull()
    expect(deletes()).toHaveLength(0)
  })

  it('Esc cancels and the focus goes back to the ✕', async () => {
    mount()
    const end = await screen.findByTestId('stop-session')
    await userEvent.click(end)
    // The focus starts on Cancel.
    await waitFor(() => expect(document.activeElement).toBe(within(screen.getByRole('alertdialog')).getByRole('button', { name: 'Cancel' })))
    await userEvent.keyboard('{Tab}')
    expect(document.activeElement).toBe(within(screen.getByRole('alertdialog')).getByRole('button', { name: 'End session' }))
    await userEvent.keyboard('{Tab}')
    expect(document.activeElement).toBe(within(screen.getByRole('alertdialog')).getByRole('button', { name: 'Cancel' }))
    await userEvent.keyboard('{Escape}')
    expect(screen.queryByRole('alertdialog')).toBeNull()
    expect(document.activeElement).toBe(end)
    expect(end.getAttribute('aria-expanded')).toBe('false')
    expect(end.closest('[inert]')).toBeNull()
    expect(deletes()).toHaveLength(0)
  })

  it('End session ends the session', async () => {
    mount()
    const end = await screen.findByTestId('stop-session')
    await userEvent.click(end)
    await userEvent.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: 'End session' }))
    await waitFor(() => expect(deletes()).toHaveLength(1))
    expect(await screen.findByText('HOME')).toBeTruthy()
  })
})
