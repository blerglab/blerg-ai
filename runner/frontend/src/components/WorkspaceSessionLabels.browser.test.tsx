import { browserWidths } from '../test/browserWidths'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { page } from 'vitest/browser'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { useTranscriptStore, type AgentEvent } from '@blerglab/chat'
import { useSessionStore } from '../hooks/useSessionStore'
import { makeSession, makeDaemon } from '../test/fixtures'
import { reelReply } from '../test/reelFixture'
import { invalidateChatTransport } from '../lib/chatTransport'
import SessionDetail from './SessionDetail'
import SessionCard from './SessionCard'
import CommandPaletteProvider from './CommandPalette'
import '../index.css'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async original => ({ ...await original<typeof import('../authClient')>(), getAccessToken: () => `x.${btoa(JSON.stringify({ sub: 'person-a', exp: 9999999999 }))}.x`, ensureFreshToken: async () => true }))
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))
const json = (body: unknown) => new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } })

beforeEach(() => {
  localStorage.clear()
  document.documentElement.setAttribute('data-theme', 'dark')
  useTranscriptStore.setState({ sessions: {} })
  const state = useTranscriptStore.getState()
  state.ingest({ session_id: 'sess-a', client_event_id: 'labels', seq: 1, ts: '2026-10-25T12:00:00Z', kind: 'assistant_text', payload: { text: 'Ready to review.', done: true } } as AgentEvent)
  state.ingestReplayDone('sess-a', { lastSeq: 1, hasMore: false })
  apiFetch.mockImplementation(async (url: string) => json(url.endsWith('/live') ? reelReply() : url.endsWith('/shell-capability') ? { available: false } : {}))
  vi.stubGlobal('fetch', vi.fn(async (url: string) => json(String(url).endsWith('/artifacts') ? { artifacts: [] } : [])))
})
afterEach(() => {
  invalidateChatTransport()
  vi.unstubAllGlobals()
  document.documentElement.removeAttribute('data-theme')
})

for (const runtime of ['daemon', 'cluster'] as const) for (const width of browserWidths([390, 3840])) for (const count of [1, 3]) {
  it(`names ${count} ${runtime} workspace repositories at ${width}px`, async () => {
    await page.viewport(width, width === 390 ? 844 : 2160)
    const names = ['blerg', 'board', 'chat'].slice(0, count)
    const session = makeSession({ title: 'Build session chrome', kind: 'agent', status: 'idle', runtime, model: 'model-next', effort: 'high',
      repo: runtime === 'cluster' ? '' : 'workspace', project_path: '/workspace/blerg', workspace: { members: names.map(name => ({ name, path: name })) },
    })
    useSessionStore.setState({ sessions: [session], daemons: [makeDaemon()] })
    render(<MemoryRouter initialEntries={['/sessions/sess-a']}><CommandPaletteProvider>
      <div style={{ height: '100vh' }}><Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes></div>
    </CommandPaletteProvider></MemoryRouter>)
    const label = count === 1 ? 'blerg' : 'blerg + 2'
    render(<MemoryRouter><div style={{ width: 280, maxWidth: '100%' }}><SessionCard session={session} daemon={makeDaemon()} /></div></MemoryRouter>)
    const repo = screen.getByTestId('session-repo')
    expect(repo.textContent).toBe(label)
    expect(repo.getAttribute('aria-label')).toBe(names.join(', '))
    expect(getComputedStyle(repo.parentElement!).flexWrap).toBe('nowrap')
    const ready = await screen.findByTestId('ready-repo')
    expect(ready.textContent).toBe(label)
    expect(ready.getAttribute('title')).toBe(names.join(', '))
    expect(screen.queryByText('workstation · /workspace/blerg')).toBeNull()
    const header = screen.getAllByLabelText(names.join(', ')).find(element => element.tagName === 'SPAN')!
    expect(header.textContent).toBe(label)
    for (const element of [header, ready, repo]) {
      const rect = element.getBoundingClientRect()
      // On phones the subtitle may collapse to preserve the title and BLG-587 controls.
      // Its full repository names remain available through the accessible label.
      if (element !== header || width > 390) expect(rect.width).toBeGreaterThan(0)
      expect(rect.right).toBeLessThanOrEqual(width)
      expect(rect.left).toBeGreaterThanOrEqual(0)
    }
  })
}
