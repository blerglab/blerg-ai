import { afterEach, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { page } from 'vitest/browser'
import { renderEvent, useTranscriptStore } from '@blerglab/chat'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import SessionDetail from './SessionDetail'
import CommandPaletteProvider from './CommandPalette'
import { useSessionStore } from '../hooks/useSessionStore'
import { makeDaemon, makeSession } from '../test/fixtures'
import { invalidateChatTransport } from '../lib/chatTransport'
import '../index.css'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async original => ({ ...await original<typeof import('../authClient')>(), getAccessToken: () => `x.${btoa(JSON.stringify({ sub: 'person-a', exp: 9999999999 }))}.x`, ensureFreshToken: async () => true }))
vi.mock('../shellTransport', async original => ({ ...await original<typeof import('../shellTransport')>(), restoreShells: async () => [] }))
vi.mock('../lib/engineModels', async original => ({ ...await original<typeof import('../lib/engineModels')>(), useEngineModels: () => ({ models: [], loading: false, fallback: false, source: 'live', engineEfforts: [] }) }))
const json = (value: unknown) => new Response(JSON.stringify(value), { headers: { 'Content-Type': 'application/json' } })
afterEach(() => { invalidateChatTransport(); vi.unstubAllGlobals() })
for (const width of [390, 3840]) it(`keeps session controls compact in the real settings panel at ${width}px`, async () => {
  await page.viewport(width, 900)
  localStorage.clear()
  useSessionStore.setState({ daemons: [makeDaemon()], sessions: [makeSession({ id: 'sess-a', kind: 'agent', status: 'idle', runtime: 'daemon' })] })
  useTranscriptStore.setState({ sessions: {} })
  useTranscriptStore.getState().ingest({ type: 'agent_event', session_id: 'sess-a', client_event_id: 'reply', seq: 1, ts: '2026-10-25T12:00:00Z', kind: 'assistant_text', payload: { text: 'The workspace is ready. Session options are in the workspace panel.', done: true } })
  useTranscriptStore.getState().ingestReplayDone('sess-a', { lastSeq: 1, hasMore: false })
  const fetcher = vi.fn(async (input: RequestInfo | URL) => {
    const url = new URL(String(input), location.origin)
    if (url.pathname.endsWith('/permissions')) return json({ can_grant: true, grants: [], boards: [], mcp_tools: [] })
    if (url.pathname.endsWith('/shells')) return json([])
    if (url.pathname.endsWith('/shell-capability')) return json({ available: false })
    if (url.pathname.endsWith('/live')) return json({ session_id: 'sess-a', through_seq: 0, generated_at: new Date().toISOString(), tokens_hidden: false, asks: [], subagents: [], tasks: [], tool_changes: 0, counts: { needs_you: 0, running: 0 }, can_release_prompt: true })
    return json({})
  })
  vi.stubGlobal('fetch', fetcher)
  invalidateChatTransport()
  render(<MemoryRouter initialEntries={['/sessions/sess-a']}><CommandPaletteProvider><Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes></CommandPaletteProvider></MemoryRouter>)
  const toggle = screen.getByRole('button', { name: 'Workspace panel' })
  if (toggle.getAttribute('aria-expanded') !== 'true') fireEvent.click(toggle)
  const follow = await screen.findByRole('checkbox', { name: 'Follow the agent in this workspace' })
  const verbosity = screen.getByRole('combobox', { name: 'Verbosity' })
  expect(follow.closest('.rail-options')).toBeTruthy()
  expect(verbosity.closest('.rail-options')).toBeTruthy()
  expect(verbosity.getBoundingClientRect().width).toBeLessThan(180)
  expect(document.querySelectorAll('[data-reveal-follow]')).toHaveLength(1)
  fireEvent.click(follow)
  expect(follow).toBeChecked()
  fireEvent.change(verbosity, { target: { value: 'terse' } })
  await waitFor(() => expect(screen.getByText('Applies on the next turn.')).toBeVisible())
  expect(fetcher.mock.calls.some(([input]) => String(input).endsWith('/sess-a/verbosity'))).toBe(true)
  // Closing the phone drawer must preserve opt-in and the explicit Show listener.
  const close = screen.queryByRole('button', { name: 'Close' })
  if (close) fireEvent.click(close)
  else fireEvent.click(toggle)
  const applied = vi.fn()
  window.addEventListener('blerg-ui-reveal-apply', applied)
  const suggestion = render(<>{renderEvent({ type: 'agent_event', session_id: 'sess-a', client_event_id: 'reveal', ts: new Date().toISOString(), kind: 'ui_reveal', payload: { action: 'panel', target: 'files' } }, new Map())}</>)
  fireEvent.click(screen.getByRole('button', { name: 'Show' }))
  await waitFor(() => expect(applied).toHaveBeenCalledOnce())
  window.removeEventListener('blerg-ui-reveal-apply', applied)
  suggestion.unmount()
  const panelToggle = screen.getByTestId('rail-toggle')
  if (panelToggle.getAttribute('aria-expanded') !== 'true') fireEvent.click(panelToggle)
  fireEvent.click(await screen.findByRole('tab', { name: /^Live/ }))
  const restored = await screen.findByRole('checkbox', { name: 'Follow the agent in this workspace' })
  expect(restored).toBeChecked()
  restored.scrollIntoView({ block: 'center' })
  if (import.meta.env.VITE_CHROME_SHOTS_DIR) await page.screenshot({ path: `${import.meta.env.VITE_CHROME_SHOTS_DIR}/quiet-session-controls-${width}.png` })
})
