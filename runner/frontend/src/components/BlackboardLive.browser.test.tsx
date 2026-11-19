import { afterEach, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { commands, page, userEvent } from 'vitest/browser'
import SessionDetail from './SessionDetail'
import CommandPaletteProvider from './CommandPalette'
import { connected } from '../ws'
import { useSessionStore } from '../hooks/useSessionStore'
import { makeDaemon, makeSession } from '../test/fixtures'
import { useTranscriptStore } from '@blerglab/chat'
import { resetBlackboardReadsForTest } from '../lib/blackboardReads'
import { invalidateChatTransport } from '../lib/chatTransport'
import { setTheme } from '../../../../packages/settings/theme'
import '../index.css'
import '@blerglab/chat/blerg.css'

const state = vi.hoisted(() => ({ token: 'x.' + btoa(JSON.stringify({ sub: 'fixture', exp: 9999999999 })).replace(/=+$/, '') + '.x', url: '' }))
vi.mock('../authClient', async original => ({ ...await original<typeof import('../authClient')>(), getAccessToken: () => state.token || 'x.' + btoa(JSON.stringify({ sub: 'fixture', exp: 9999999999 })).replace(/=+$/, '') + '.x', ensureFreshToken: async () => true }))
// Only unrelated runtime dependencies are canned. Blackboard requests, persistence,
// hub broadcasting, WebSocket delivery and the browser subscription are all real.
vi.mock('../apiFetch', () => ({ apiFetch: async (url: string, init?: RequestInit) => {
 if ((url.startsWith('/api/blackboards') || url.startsWith('/api/messages') || /\/api\/sessions\/[^/]+\/live/.test(url))) return fetch(state.url + url, { ...init, headers: { ...init?.headers, Authorization: 'Bearer ' + state.token } })
 const body = url.endsWith('/permissions') ? { can_grant: true, grants: [], boards: [], mcp_tools: [] }
  : url.endsWith('/live') ? { through_seq: 0, asks: [], subagents: [], tasks: [], children: [], counts: { needs_you: 0, running: 0 } }
  : url.startsWith('/api/session-boards') ? { configured: true, boards: [] }
  : {}
 return new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } })
} }))
afterEach(() => { invalidateChatTransport(); vi.unstubAllGlobals(); state.token = ''; state.url = '' })

for (const width of [390, 1024, 3840]) it(`updates an open tab through real runner HTTP and socket at ${width}px`, async () => {
 const fixture = await commands.startBlackboardFixture(location.origin)
 state.token = fixture.token; state.url = fixture.url
 const NativeSocket = globalThis.WebSocket
 const sockets = new Set<WebSocket>()
 vi.stubGlobal('WebSocket', class extends NativeSocket {
  constructor(url: string | URL, protocols?: string | string[]) {
   super(String(url).includes('/ws/browser') ? fixture.url.replace('http:', 'ws:') + '/ws/browser' : url, protocols)
   sockets.add(this)
  }
 })
 try {
  await page.viewport(width, width === 390 ? 844 : 2160)
  setTheme('dark'); resetBlackboardReadsForTest()
  window.dispatchEvent(new Event('blerg-identity-changed'))
  window.dispatchEvent(new Event('online'))
  await waitFor(() => expect(connected()).toBe(true))
  await waitFor(() => expect(useSessionStore.getState().sessions.some(s => s.id === fixture.sessionId)).toBe(true))
  useSessionStore.setState({ daemons: [makeDaemon()], sessions: [makeSession({ id: fixture.sessionId, kind: 'agent', runtime: 'daemon', status: 'idle', title: 'Plan the release' }), makeSession({ id: fixture.channelId, parent_session_id: fixture.sessionId, kind: 'agent' })] })
  useTranscriptStore.setState({ sessions: {} })
  useTranscriptStore.getState().ingestReplayDone(fixture.sessionId, { lastSeq: 0, hasMore: false })
  const app = render(<MemoryRouter initialEntries={['/sessions/' + fixture.sessionId]}><CommandPaletteProvider><div style={{ height: '100vh' }}><Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes></div></CommandPaletteProvider></MemoryRouter>)
  if (width === 1024) {
   const root = document.querySelector<HTMLElement>('[data-session-root]')!
   root.style.right = 'auto'; root.style.width = '1024px'
   expect(root.getBoundingClientRect().width).toBe(1024)
  }
  const rail = screen.queryByTestId('rail-toggle')
  if (rail?.getAttribute('aria-expanded') === 'true') fireEvent.click(rail)
  const menu = screen.queryByTestId('open-tab-menu')
  if (menu) fireEvent.click(menu)
  fireEvent.click(await screen.findByRole(menu ? 'menuitem' : 'button', { name: 'Open blackboard' }))
  await screen.findByRole('heading', { name: 'Give your next idea a clear path' })
  const tab = screen.getByRole('tab', { name: 'Blackboard' })
  const response = await fetch(fixture.url + '/api/sessions/' + fixture.sessionId + '/plan/draft', { method: 'POST', headers: { Authorization: 'Bearer ' + fixture.agentToken, 'Content-Type': 'application/json' }, body: JSON.stringify({ name: 'A clearer release loop', vision: 'Keep each iteration small, visible, and guided by evidence.', goals: ['Keep work moving', 'Make review easy', 'Recover reliably'], milestone: { name: 'Next release', target: '2027-01-15' }, thrusts: [{ name: 'Refresh the open plan', goal_index: 0, reason: 'Show new decisions where people are already working.' }, { name: 'Keep channels connected', goal_index: 0, reason: 'Share one workspace plan across Main and channels.' }, { name: 'Bring evidence into review', goal_index: 1, reason: 'Keep checks beside the proposed change.' }, { name: 'Make the next action clear', goal_index: 1, reason: 'Decide through Main’s question in chat or Needs you.' }, { name: 'Resume after a disconnect', goal_index: 2, reason: 'Catch up when the browser reconnects.' }, { name: 'Offer a quiet retry', goal_index: 2, reason: 'Explain failed refreshes without restoring discarded work.' }] }) })
  expect(response.status).toBe(201)
  const draft = await response.json()
  const name = draft.untrusted_data.name
  await screen.findByRole('heading', { name })
  expect(screen.getByRole('tab', { name: 'Blackboard' })).toBe(tab)
  expect(tab).toHaveAttribute('aria-selected', 'true')
  if (width === 1024) expect(document.querySelector('.blackboard')!.getBoundingClientRect().width).toBe(1024)
  const shots = import.meta.env.VITE_BLACKBOARD_SHOTS_DIR
  if (shots) {
   const frame = document.createElement('div')
   frame.style.cssText = `position:fixed;inset:0;width:${width}px;height:100vh;pointer-events:none`
   document.body.append(frame)
   try {
    expect(frame.getBoundingClientRect().width).toBe(width)
    await page.screenshot({ element: frame, path: `${shots}/real-blackboard-${width}.png` })
    if (width === 390) {
     setTheme('colorblind-dark')
     await page.screenshot({ element: frame, path: `${shots}/real-blackboard-colorblind-390.png` })
    }
   } finally { frame.remove() }
  }
  setTheme('dark')
  // The same server-bound subject is visible in chat and Needs you.
  const posted = await fetch(fixture.url + '/api/messages', { method: 'POST', headers: { Authorization: 'Bearer ' + fixture.agentToken, 'Content-Type': 'application/json' }, body: JSON.stringify({ kind: 'ask', subject: { kind: 'draft', draft_id: draft.id, revision: draft.revision } }) })
  expect(posted.status).toBe(201)
  const ask = await posted.json()
  fireEvent.click(screen.getByRole('tab', { name: 'Main' }))
  const chat = await screen.findByTestId('ask-card')
  fireEvent.click(chat.querySelector('button')!)
  let dialog = await screen.findByRole('dialog', { name: 'Create blackboard · 3 goals, 1 milestone, 6 thrusts' })
  expect(within(dialog).getByTestId('question-snapshot').textContent).toContain('Catch up when the browser reconnects.')
  expect(within(dialog).queryAllByRole('checkbox')).toHaveLength(0)
 expect(within(dialog).getByTestId('question-snapshot').textContent).not.toContain('Release:')
  const capture = async (name: string) => {
   const dir = import.meta.env.VITE_BOUND_ASK_SHOTS_DIR
   if (!dir) return
   const frame = document.createElement('div')
   frame.style.cssText = `position:fixed;inset:0;width:${width}px;height:100vh;pointer-events:none`
   document.body.append(frame)
   try { expect(frame.getBoundingClientRect().width).toBe(width); await page.screenshot({ element: frame, path: `${dir}/${name}-${width}.png` }) } finally { frame.remove() }
  }
  expect(within(dialog).queryByRole('textbox')).toBeNull()
  fireEvent.click(within(dialog).getByRole('button', { name: 'Ask Main' }))
  expect(within(dialog).getByRole('textbox')).toBeVisible()
  await capture('draft-chat')
  await userEvent.keyboard('{Escape}')
  if (!document.querySelector('[data-testid="rail-ask"]')) fireEvent.click(screen.getByTestId('rail-toggle'))
  await waitFor(() => expect(document.querySelector('[data-testid="rail-ask"]')).not.toBeNull())
  fireEvent.click(document.querySelector('[data-testid="rail-ask"] button')!)
  dialog = await screen.findByRole('dialog', { name: 'Create blackboard · 3 goals, 1 milestone, 6 thrusts' })
  // Needs you opens the shared question discussion, with the same bound draft
  // as chat. Its old rail-only kicker no longer exists (BLG-557).
  const discussion = within(dialog).getByRole('log', { name: 'Question discussion' })
  expect(within(discussion).getByTestId('question-snapshot')).toHaveTextContent('Catch up when the browser reconnects.')
  await capture('draft-needs-you')
  if (width === 390) { setTheme('colorblind-dark'); await capture('draft-needs-you-colorblind'); setTheme('dark') }
  if (width === 3840) {
   // Chat's targeted socket path must commit without a daemon too.
   await userEvent.keyboard('{Escape}')
   fireEvent.click(chat.querySelector('button')!)
   dialog = screen.getByRole('dialog', { name: 'Create blackboard · 3 goals, 1 milestone, 6 thrusts' })
  }
  expect(within(dialog).queryByRole('textbox')).toBeNull()
  expect(within(dialog).getByRole('button', { name: 'Decline' })).toBeEnabled()
  if (width === 3840) {
   expect(window.innerWidth).toBe(3840)
   expect(dialog.getBoundingClientRect().width).toBe(960)
  }
  fireEvent.click(within(dialog).getByRole('button', { name: 'Approve' }))
  if (width === 3840) {
   await waitFor(() => expect(chat).toHaveClass('closed'))
   expect(within(dialog).queryByRole('textbox')).toBeNull()
   await userEvent.keyboard('{Escape}')
  } else await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Create blackboard · 3 goals, 1 milestone, 6 thrusts' })).toBeNull())
  const receipt = await fetch(fixture.url + '/api/messages/' + ask.id + '/answer?wait=1', { headers: { Authorization: 'Bearer ' + fixture.agentToken } })
  expect(receipt.status).toBe(200)
  expect((await receipt.json()).decision_receipt).toMatchObject({ status: 'committed', draft_id: draft.id, revision: draft.revision })
  fireEvent.click(tab)
  await screen.findByRole('navigation', { name: 'Blackboard levels' })
  expect(screen.getByRole('tab', { name: 'Blackboard' })).toBe(tab)
  app.unmount()
 } finally {
  // Avoid a reconnect while the browser-owned server shuts down.
  for (const socket of sockets) { socket.onclose = null; socket.close() }
  await act(async () => { await commands.stopBlackboardFixture(fixture.token) })
 }
}, 120000)
