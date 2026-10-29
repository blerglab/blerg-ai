// Stop (interrupt the current turn) sits in the session header, immediately left of End session and
// far from Send, so it is not hit by accident (owner, 0.5.0; reverses #233's Stop beside Send). It shows
// only while the agent works, interrupts once, and waits while the interrupt is on its way.
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { act, render, screen, within } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { useChatReportStore } from '@blerglab/chat'
import '../index.css'
import { useSessionStore } from '../hooks/useSessionStore'
import { makeDaemon, makeSession } from '../test/fixtures'
import * as wsMock from '../test/wsMock'
import { invalidateChatTransport } from '../lib/chatTransport'
import type { SessionInfo } from '../types'

vi.mock('../ws', () => import('../test/wsMock'))
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))

import SessionDetail from './SessionDetail'

const json = (body: unknown) => Promise.resolve(new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } }))
const interrupts = () => wsMock.send.mock.calls.filter(([m]) => (m as { type: string }).type === 'interrupt_session').length

function seed(status: SessionInfo['status']) {
  useSessionStore.setState({
    daemons: [makeDaemon()],
    sessions: [makeSession({ id: 'sess-a', kind: 'agent', status, title: 'Ship the release notes' })],
    statusChangedAt: {}, serverVersion: 'test',
  })
}

beforeEach(() => {
  wsMock.resetWsMock()
  wsMock.send.mockReturnValue(true)
  apiFetch.mockImplementation(() => json({}))
  vi.stubGlobal('fetch', vi.fn(() => json([])))
})
afterEach(() => {
  invalidateChatTransport()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

function mount() {
  return render(
    <MemoryRouter initialEntries={['/sessions/sess-a']}>
      <div style={{ display: 'flex', height: '100vh' }}>
        <div style={{ flex: 1, minWidth: 0 }}>
          <Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes>
        </div>
      </div>
    </MemoryRouter>,
  )
}

it.each([390, 3840])('puts Stop in the header next to End session, not beside Send, at %i px', async width => {
  await page.viewport(width, width === 390 ? 844 : 2160)
  seed('running')
  mount()
  const group = await screen.findByTestId('session-header-controls')
  const stop = await within(group).findByRole('button', { name: "Stop the agent's current turn" })
  expect(stop).toHaveClass('icon-btn')
  expect(stop).toHaveAttribute('title', "Stop the agent's current turn")
  // Immediately left of End session (on a phone End session is in the palette: Stop ends the row).
  if (width > 1000) expect(stop.nextElementSibling).toBe(within(group).getByRole('button', { name: 'End session' }))
  else expect(stop).toBe(group.lastElementChild)
  // One row with the header's other controls, inside the screen; the title keeps room.
  const s = stop.getBoundingClientRect()
  for (const b of group.querySelectorAll('button')) expect(Math.abs(b.getBoundingClientRect().top - s.top)).toBeLessThan(1)
  expect(s.right).toBeLessThanOrEqual(width)
  expect(s.height).toBeGreaterThanOrEqual(28)
  // Far from Send: none in the message box, and only one Stop on the page.
  const send = screen.getByRole('button', { name: 'Send' })
  expect(send.parentElement!.querySelector('button[aria-label^="Stop"]')).toBeNull()
  expect(screen.getAllByRole('button', { name: /^Stop/ })).toHaveLength(1)
  expect(send.getBoundingClientRect().top - s.bottom).toBeGreaterThan(200)
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
  const shots = import.meta.env.VITE_CHROME_SHOTS_DIR
  if (shots) await page.screenshot({ path: `${shots}/header-stop-${width}.png` })
  // One click interrupts once; Stop waits (disabled) while the interrupt is on its way.
  await userEvent.click(stop)
  expect(interrupts()).toBe(1)
  expect(stop).toBeDisabled()
  expect(stop).toHaveAttribute('aria-busy', 'true')
  await userEvent.click(stop, { force: true })
  expect(interrupts()).toBe(1)
  // Esc does not interrupt.
  screen.getByRole('textbox').focus()
  await userEvent.keyboard('{Escape}')
  expect(interrupts()).toBe(1)
  // The turn ends: Stop goes.
  act(() => seed('idle'))
  await expect.poll(() => screen.queryByRole('button', { name: /^Stop/ })).toBeNull()
})

it('shows no Stop while the session is idle', async () => {
  await page.viewport(3840, 2160)
  seed('idle')
  mount()
  const group = await screen.findByTestId('session-header-controls')
  await expect.poll(() => useChatReportStore.getState().reports['sess-a']?.busy).toBe(false)
  expect(within(group).queryByRole('button', { name: /^Stop/ })).toBeNull()
  expect(within(group).getByRole('button', { name: 'End session' })).toBeInTheDocument()
})
