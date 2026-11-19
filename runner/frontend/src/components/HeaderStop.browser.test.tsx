import { browserWidths } from '../test/browserWidths'
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
          <Routes><Route path="/sessions/:id" element={<SessionDetail />} /><Route path="/sessions" element={<div>Workspaces</div>} /></Routes>
        </div>
      </div>
    </MemoryRouter>,
  )
}

it.each(browserWidths([390, 3840]))('puts Stop in the header next to End session, not beside Send, at %i px', async width => {
  await page.viewport(width, width === 390 ? 844 : 2160)
  seed('running')
  mount()
  const group = await screen.findByTestId('session-header-controls')
  const stop = await within(group).findByRole('button', { name: "Stop the agent's current turn" })
  expect(stop).toHaveClass('icon-btn')
  expect(stop).toHaveAttribute('title', "Stop the agent's current turn")
  // Immediately left of End session, at every width now (card BLG-450: End always shows).
  expect(stop.nextElementSibling).toBe(within(group).getByRole('button', { name: 'End workspace' }))
  // One row with the header's other controls, inside the screen; the title keeps room.
  const s = stop.getBoundingClientRect()
  // The collapsed Open menu's items (card BLG-450) stay closed here, so skip them: a hidden
  // menuitem has no box of its own to compare.
  for (const b of group.querySelectorAll('button:not([role="menuitem"])')) expect(Math.abs(b.getBoundingClientRect().top - s.top)).toBeLessThan(1)
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
  expect(within(group).getByRole('button', { name: 'End workspace' })).toBeInTheDocument()
})

it.each([390, 3840])('shows one styled recovery notice above the composer at %i px', async width => {
 await page.viewport(width, width === 390 ? 844 : 2160)
 seed('running')
 mount()
 await screen.findByTestId('session-header-controls')
 act(() => wsMock.emit({type: 'session_paused_changed', session_id: 'sess-a', paused_reason: 'restart', paused_detail: 'Recovery paused: Its workspace was being stopped when the daemon went down, and the stop is not finished. It stays paused so no work is lost. Send a message to retry.'}))
 const notice = await screen.findByTestId('recovery-notice')
 expect(notice).toHaveClass('agent-notice')
 expect(notice).toHaveTextContent(/^Paused: Its workspace/)
 expect(notice).not.toHaveTextContent('Paused: Recovery paused:')
 expect(screen.getAllByText(/Its workspace was being stopped/)).toHaveLength(1)
 expect(screen.queryByTestId('waiting')).toBeNull()
 expect(screen.queryByText('Waiting for your answer')).toBeNull()
 const composer = screen.getByRole('textbox')
 expect(notice.closest('.chat-composer-surface')).toContainElement(composer)
 const bounds = notice.getBoundingClientRect()
 expect(bounds.bottom).toBeLessThanOrEqual(composer.getBoundingClientRect().top)
 expect(bounds.left).toBeGreaterThanOrEqual(0)
 expect(bounds.right).toBeLessThanOrEqual(width)
 expect(notice.scrollWidth).toBeLessThanOrEqual(notice.clientWidth)
 expect(getComputedStyle(notice).borderTopStyle).toBe('solid')
 expect(getComputedStyle(notice).backgroundColor).not.toBe('rgba(0, 0, 0, 0)')
 expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
 await expect.poll(() => screen.queryByTestId('working')).toBeNull()
 expect(screen.queryByTestId('stop-turn')).toBeNull()
 // Heartbeats reassert waiting/idle after a refusal; neither can restore Stop.
 for (const status of ['waiting', 'idle', 'waiting'] as const) {
   act(() => wsMock.emit({type: 'session_state_changed', session_id: 'sess-a', status, unread: false}))
   expect(screen.queryByTestId('stop-turn')).toBeNull()
 }
 expect(interrupts()).toBe(0)
 expect(useSessionStore.getState().sessions[0].status).toBe('waiting')
})

it('keeps an unrecovered session paused through End failure and lets End retry', async () => {
 await page.viewport(3840, 2160)
 seed('waiting')
 useSessionStore.setState(s => ({ sessions: s.sessions.map(session => ({...session, paused_reason: 'restart', paused_detail: 'Recovery paused: Workspace stop is unfinished. Send a message to retry.'})) }))
 apiFetch.mockImplementation((_url: string, options?: RequestInit) => options?.method === 'DELETE'
   ? Promise.resolve(new Response(JSON.stringify({error: 'Workspace cleanup is incomplete. All work is retained; retry End when the daemon is connected.'}), {status: 503, headers: {'Content-Type': 'application/json'}}))
   : json({}))
 mount()
 await screen.findByTestId('recovery-notice')
 expect(screen.queryByTestId('stop-turn')).toBeNull()
 expect(screen.queryByTestId('working')).toBeNull()
 const end = screen.getByRole('button', {name: 'End workspace'})
 await userEvent.click(end)
 await userEvent.click(within(screen.getByRole('alertdialog')).getByRole('button', {name: 'End workspace'}))
 const error = await screen.findByRole('alertdialog', {name: 'Ending the workspace failed'})
 expect(error).toHaveTextContent('Workspace cleanup is incomplete. All work is retained')
 expect(screen.queryByTestId('stop-turn')).toBeNull()
 expect(screen.getAllByTestId('recovery-notice')).toHaveLength(1)
 expect(useSessionStore.getState().sessions[0].paused_reason).toBe('restart')
 apiFetch.mockImplementation(() => Promise.resolve(new Response(null, {status: 204})))
 await userEvent.click(within(error).getByRole('button', {name: 'Retry'}))
 await expect.poll(() => screen.queryByTestId('recovery-notice')).toBeNull()
 expect(apiFetch.mock.calls.filter(([, options]) => options?.method === 'DELETE')).toHaveLength(2)
})
