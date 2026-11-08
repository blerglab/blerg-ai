import { render, screen, within, waitFor, fireEvent, act } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import { commands, page, userEvent } from 'vitest/browser'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import CommandPaletteProvider from './CommandPalette'
import SessionDetail from './SessionDetail'
import { useSessionStore } from '../hooks/useSessionStore'
import { makeSession, makeDaemon } from '../test/fixtures'
import { useTranscriptStore } from '@blerglab/chat'
import { invalidateChatTransport } from '../lib/chatTransport'
import { connected } from '../ws'
import { RAIL_PREF_KEY, RAIL_TAB_KEY } from '../lib/sessionRail'
import { setTheme } from '../../../../packages/settings/theme'
import '../index.css'
import '@blerglab/chat/blerg.css'

const state = vi.hoisted(() => ({ token: 'x.' + btoa(JSON.stringify({ sub: 'fixture', exp: 9999999999 })).replace(/=+$/, '') + '.x', url: '' }))
vi.mock('../authClient', async original => ({ ...await original<typeof import('../authClient')>(), getAccessToken: () => state.token, ensureFreshToken: async () => true }))
// The decision, transcript and Needs you use real runner HTTP and WebSocket paths.
// Only unrelated desktop/runtime views have canned data.
vi.mock('../apiFetch', () => ({ apiFetch: async (url: string, init?: RequestInit) => {
 if (url.startsWith('/api/blackboards') || url.startsWith('/api/messages') || /\/api\/sessions\/[^/]+\/live/.test(url)) return fetch(state.url + url, { ...init, headers: { ...init?.headers, Authorization: 'Bearer ' + state.token } })
 const body = url.endsWith('/topics') ? { topics: [], unmarked: null, strokes: [] }
  : url.includes('/workspace?') ? { result: { ok: true, launch_generation: 'launch', generation: 1, entries: [], truncated: false }, members: [], runtime: 'host' }
  : url.endsWith('/permissions') ? { can_grant: false, grants: [], mcp_tools: [], boards: [] } : {}
 return new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } })
} }))
const shotDir = (import.meta.env as Record<string, string | undefined>).VITE_BOUND_ASK_SHOTS_DIR
const title = 'Cut 0.6.0 · 1 archive, 1 move'
async function capture(name: string, width: number) {
 if (!shotDir) return
 const frame = document.createElement('div')
 frame.style.cssText = `position:fixed;inset:0;width:${width === 1024 ? 3840 : width}px;height:100vh;pointer-events:none`
 document.body.append(frame)
 expect(frame.getBoundingClientRect().width).toBe(width === 1024 ? 3840 : width)
 expect(document.querySelector('[data-testid="fixture-pane"]')?.getBoundingClientRect().width).toBe(width)
 try { await page.screenshot({ element: frame, path: `${shotDir}/${name}-${width}.png` }) } finally { frame.remove() }
}
afterEach(() => { invalidateChatTransport(); vi.unstubAllGlobals(); vi.restoreAllMocks(); document.documentElement.removeAttribute('data-theme'); state.token = ''; state.url = '' })
for (const width of [390, 1024, 3840]) it(`shows and commits the real bound question in chat and Needs you at ${width}px`, async () => {
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
 await page.viewport(width === 1024 ? 3840 : width, width === 390 ? 844 : 2160)
 localStorage.clear(); localStorage.setItem(RAIL_PREF_KEY, 'open'); localStorage.setItem(RAIL_TAB_KEY, 'live')
 document.documentElement.setAttribute('data-theme', 'dark')
 window.dispatchEvent(new Event('blerg-identity-changed')); window.dispatchEvent(new Event('online'))
 await waitFor(() => expect(connected()).toBe(true))
 await waitFor(() => expect(useSessionStore.getState().sessions.some(s => s.id === fixture.sessionId)).toBe(true))
 useSessionStore.setState({ daemons: [makeDaemon()], sessions: [makeSession({ id: fixture.sessionId, kind: 'agent', status: 'idle', title: 'Plan 0.6.0' })] })
 useTranscriptStore.setState({ sessions: {} })
 useTranscriptStore.getState().ingestReplayDone(fixture.sessionId, { lastSeq: 0, hasMore: false })
 const app = render(<MemoryRouter initialEntries={['/sessions/' + fixture.sessionId]}><CommandPaletteProvider><div data-testid="fixture-pane" style={{ display: 'flex', height: '100vh', width }}><div style={{ flex: 1, minWidth: 0 }}><Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes></div></div></CommandPaletteProvider></MemoryRouter>)
 const setup = await fetch(fixture.url + '/fixture/cut', { method: 'POST', headers: { Authorization: 'Bearer ' + fixture.token } })
 expect(setup.status).toBe(201)
 const subject = await setup.json()
 const posted = await fetch(fixture.url + '/api/messages', { method: 'POST', headers: { Authorization: 'Bearer ' + fixture.agentToken, 'Content-Type': 'application/json' }, body: JSON.stringify({ kind: 'ask', body: 'ignored Main prose', subject: { kind: 'cut', ...subject } }) })
 expect(posted.status).toBe(201)
 const ask = await posted.json()
 const chat = await screen.findByTestId('ask-card', undefined, { timeout: 10000 })
 fireEvent.click(chat.querySelector('button')!)
 let dialog = screen.getByRole('dialog', { name: title })
 expect(within(dialog).getByTestId('question-snapshot')).toHaveTextContent('Archive #2 Finished work')
 expect(within(dialog).getByTestId('question-snapshot')).toHaveTextContent('Move #1 Carry this forward')
 expect(within(dialog).getByTestId('question-snapshot')).toHaveTextContent('Next release: Next')
 expect(within(dialog).queryAllByRole('checkbox')).toHaveLength(0)
 expect(within(dialog).queryByRole('textbox')).toBeNull()
 expect(within(dialog).getByRole('button', { name: 'Decline' })).toBeEnabled()
 expect(dialog.scrollWidth).toBeLessThanOrEqual(dialog.clientWidth)
 if (width === 3840) {
  // BLG-527 intentionally bounds the question sheet, even on a 4K display.
  expect(window.innerWidth).toBe(3840)
  expect(dialog.getBoundingClientRect().width).toBe(960)
  const grid = dialog.querySelector<HTMLElement>('.question-plan-operations')!
  // Plan changes are grouped by thrust (25eb4877); a cut's archive and move have none, so they share one group.
  expect(grid.children).toHaveLength(1)
  expect(grid.querySelectorAll('.question-plan-operation')).toHaveLength(2)
  expect(getComputedStyle(grid).gridTemplateColumns.split(/\s+/).length).toBe(1)
 }
 if (width === 390) {
  fireEvent.click(within(dialog).getByRole('button', { name: 'Ask Main' }))
  await userEvent.fill(within(dialog).getByRole('textbox', { name: 'Your answer' }), 'yes')
  fireEvent.click(within(dialog).getByRole('button', { name: 'Ask Main' }))
  await within(dialog).findByText('yes')
  const pending = await fetch(fixture.url + '/api/messages/' + ask.id + '/answer?wait=1', { headers: { Authorization: 'Bearer ' + fixture.agentToken } })
  const discussion = await pending.json()
  expect(discussion.answered).toBe(false)
  expect(discussion.decision_receipt).toBeUndefined()
  await waitFor(() => expect(within(dialog).getByRole('button', { name: 'Approve' })).toBeEnabled())
 }
 await capture('cut-chat', width)
 await userEvent.keyboard('{Escape}')
 if (!document.querySelector('[data-testid="rail-ask"]')) fireEvent.click(screen.getByTestId('rail-toggle'))
 await waitFor(() => expect(document.querySelector('[data-testid="rail-ask"]')).not.toBeNull())
 fireEvent.click(document.querySelector('[data-testid="rail-ask"] button')!)
 dialog = screen.getByRole('dialog', { name: title })
 expect(within(dialog).getByTestId('question-snapshot')).toHaveTextContent('Archive #2 Finished work')
 expect(within(dialog).getByTestId('question-snapshot')).toHaveTextContent('Move #1 Carry this forward')
 expect(within(dialog).getByTestId('question-snapshot')).toHaveTextContent('Next release: Next')
 await capture('cut-needs-you', width)
 if (width === 390) {setTheme('colorblind-dark'); await capture('cut-needs-you-colorblind', width); setTheme('dark')}
 if (width === 3840) { await userEvent.keyboard('{Escape}'); fireEvent.click(chat.querySelector('button')!); dialog = screen.getByRole('dialog', { name: title }) }
 fireEvent.click(within(dialog).getByRole('button', { name: 'Approve' }))
 if (width === 3840) {
  await waitFor(() => expect(chat).toHaveClass('closed'))
  expect(within(dialog).queryByRole('textbox')).toBeNull()
 } else await waitFor(() => expect(screen.queryByRole('dialog', { name: title })).toBeNull())
 const result = await fetch(fixture.url + '/api/messages/' + ask.id + '/answer?wait=1', { headers: { Authorization: 'Bearer ' + fixture.agentToken } })
 expect(result.status).toBe(200)
 expect((await result.json()).decision_receipt).toMatchObject({ ask_id: ask.id, status: 'authorized' })
 await waitFor(async () => {
  const response = await fetch(fixture.url + '/api/blackboards/' + subject.blackboard_id + '?session=' + fixture.sessionId, { headers: { Authorization: 'Bearer ' + fixture.token } })
  const result = await response.json()
  expect(result.record.milestones[0].cut_receipt_id).toBeTruthy()
  expect(result.record.milestones[0].shipped_at).toBeTruthy()
 }, { timeout: 20000 })
 await userEvent.keyboard('{Escape}')
 if (screen.queryByRole('button', { name: 'Open blackboard' })) fireEvent.click(screen.getByRole('button', { name: 'Open blackboard' }))
 else {fireEvent.click(screen.getByRole('button', { name: 'Open' }));fireEvent.click(screen.getByRole('menuitem', { name: 'Open blackboard' }))}
 await screen.findByText(/Shipped /, undefined, { timeout: 10000 })
 for (const name of ['Final','Retry Final','Abort','Approve','Reject','Create','Edit','Discard']) expect(screen.queryByRole('button', {name})).toBeNull()
 await capture('cut-shipped', width)
 app.unmount()
 } finally {
  for (const socket of sockets) { socket.onclose = null; socket.close() }
  await act(async () => { await commands.stopBlackboardFixture(fixture.token) })
 }
}, 120000)
