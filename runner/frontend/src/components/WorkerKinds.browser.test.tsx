import { afterEach, expect, it, vi } from 'vitest'
import { render, screen, within, waitFor } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import App from '../App'
import { useTranscriptStore } from '@blerglab/chat'
import { useSessionStore } from '../hooks/useSessionStore'
import { useMessageStore } from '../hooks/useMessageStore'
import { makeDaemon, makeSession } from '../test/fixtures'
import { invalidateChatTransport } from '../lib/chatTransport'
import { setTheme } from '../../../../packages/settings/theme'
import '../index.css'
import '@blerglab/chat/blerg.css'
vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async original => ({ ...await original<typeof import('../authClient')>(), getAccessToken: () => `x.${btoa(JSON.stringify({ sub: 'person-a', exp: 9999999999 }))}.x`, ensureFreshToken: async () => true }))
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))
afterEach(() => { window.history.replaceState({}, '', '/'); invalidateChatTransport(); vi.unstubAllGlobals(); vi.restoreAllMocks() })
for (const [width, theme] of [[390, 'dark'], [1024, 'dark'], [3840, 'dark'], [3840, 'colorblind-dark']] as const) it(`shows worker kinds through the app at ${width} in ${theme}`, async () => {
 await page.viewport(width, width === 390 ? 1200 : 2160)
 localStorage.clear()
 if (width === 3840) localStorage.setItem('blerg-runner.explorer.person-a', JSON.stringify({ open: false, width: 1024 }))
 setTheme(theme)
 const kinds = ['card', 'role', 'review', 'research'] as const
 const states = ['running', 'idle', 'waiting', 'stopped'] as const
 const workers = kinds.flatMap(kind => states.map(status => makeSession({ id: `${kind}-${status}`, title: `${kind === 'card' ? 'BLG-123 Fix' : kind === 'role' ? 'Gates' : kind === 'review' ? 'Review changes' : 'Investigate'} · ${status === 'stopped' ? 'ended' : status}`, kind: 'agent', worker_kind: kind, parent_session_id: 'sess-a', runtime: 'docker', started_by: { kind: 'session' }, status, ended_at: status === 'stopped' ? '2026-10-31T19:01:00Z' : undefined })))
 useTranscriptStore.setState({ sessions: {} })
 useTranscriptStore.getState().ingestReplayDone('sess-a', { lastSeq: 0, hasMore: false })
 useMessageStore.setState({ messages: workers.filter(w => w.status === 'waiting').map(w => ({ id: `ask-${w.id}`, session_id: w.id, kind: 'ask' as const, status: 'open' as const, answerable: true, owner_only: true, body: 'Choose a direction', created_at: '2026-10-31T19:00:00Z' })) })
 useSessionStore.setState({ daemons: [makeDaemon()], sessions: [makeSession({ kind: 'agent', title: 'Coordinate worker kinds', status: 'idle' }), ...workers, makeSession({ id: 'unset', title: 'Unspecified worker', parent_session_id: 'sess-a', started_by: { kind: 'session' }, status: 'idle' })], statusChangedAt: {} })
 apiFetch.mockImplementation(async (url: string) => {
  const body = url.endsWith('/categories') ? { categories: [] } : url.endsWith('/topics') ? { topics: [], strokes: [] } : url.endsWith('/permissions') ? { can_grant: false, grants: [], boards: [], mcp_tools: [] } : url.endsWith('/live') ? { asks: [], subagents: [], tasks: [], children: [], counts: { needs_you: 0, running: 0 } } : url.endsWith('/messages') || url.endsWith('/children') || url.endsWith('/shells') || url.includes('/boards') ? [] : {}
  return new Response(JSON.stringify(body))
 })
 vi.stubGlobal('fetch', vi.fn(async () => new Response('[]')))
 window.history.replaceState({}, '', '/sessions/sess-a')
 render(<App />)
 await userEvent.click(await screen.findByRole('button', { name: 'Toggle explorer' }))
 await userEvent.click(await screen.findByRole('tab', { name: 'Channels' }))
 if (width === 3840) await waitFor(() => expect(screen.getByRole('region', { name: 'Explorer' }).getBoundingClientRect().width).toBe(1024))
 const group = await screen.findByRole('treeitem', { name: /^Workers, 17/ })
 expect(group).toHaveAttribute('aria-expanded', 'true')
 for (const worker of workers) {
  const row = screen.getByRole('treeitem', { name: worker.title })
  const label = worker.worker_kind === 'card' ? 'Card worker' : worker.worker_kind === 'role' ? 'Role worker' : worker.worker_kind === 'review' ? 'Reviewer' : 'Research'
  expect(within(row).getByRole('img', { name: label })).toBeVisible()
  expect(row.querySelector('[data-testid="status-dot"], .dock-needs-you')).not.toBeNull()
  expect(row.scrollWidth).toBeLessThanOrEqual(row.clientWidth)
 }
 const unset = screen.getByRole('treeitem', { name: 'Unspecified worker' })
 expect(within(unset).queryByRole('img', { name: /Card worker|Role worker|Reviewer|Research|Unknown runtime/ })).toBeNull()
 const blank = unset.querySelector('[data-worker-kind-slot]')!
 expect(blank).not.toBeNull()
 expect(blank.querySelector('svg')).toBeNull()
 expect(blank.getBoundingClientRect().width).toBe(16)
 const titleX = (row: HTMLElement) => row.querySelector('.workspace-name')!.getBoundingClientRect().x
 expect(titleX(unset)).toBe(titleX(screen.getByRole('treeitem', { name: workers[0].title })))
 await waitFor(() => expect(group).toBeVisible())
 const shots = import.meta.env.VITE_CHROME_SHOTS_DIR
 if (shots) {
  const frame = document.createElement('div')
  frame.style.cssText = 'position:fixed;inset:0;width:100vw;height:100vh;pointer-events:none'
  document.body.append(frame)
  expect(frame.getBoundingClientRect().width).toBe(width)
  try { await page.screenshot({ element: frame, path: `${shots}/worker-kinds-${width}-${theme}.png` }) } finally { frame.remove() }
 }
})
