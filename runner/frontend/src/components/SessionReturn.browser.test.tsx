// #281: coming back to a session draws its chat (a long held transcript included), its Files list
// and its open file tab in the first paint, with no loading state and no request in the way, then
// resumes the transcript where the kept one ends (no duplicates, no gap).
import { beforeEach, afterEach, expect, it, vi } from 'vitest'
import { useEffect } from 'react'
import { act, render, screen, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes, useNavigate } from 'react-router-dom'
import { page, userEvent } from 'vitest/browser'
import { useTranscriptStore, type AgentEvent } from '@blerglab/chat'
import SessionDetail from './SessionDetail'
import CommandPaletteProvider from './CommandPalette'
import { useSessionStore } from '../hooks/useSessionStore'
import { explorerPrefKey } from '../hooks/useExplorerLayout'
import { makeDaemon, makeSession } from '../test/fixtures'
import { RAIL_PREF_KEY, RAIL_TAB_KEY } from '../lib/sessionRail'
import { invalidateChatTransport } from '../lib/chatTransport'
import { resetSessionRetentionForTest } from '../lib/sessionRetention'
import { emit, send } from '../test/wsMock'
import '../index.css'
import '@blerglab/chat/blerg.css'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async original => ({ ...await original<typeof import('../authClient')>(), getAccessToken: () => `x.${btoa(JSON.stringify({ sub: 'person-a', exp: 9999999999 }))}.x`, ensureFreshToken: async () => true }))
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))
const shotDir = (import.meta.env as Record<string, string | undefined>).VITE_RETURN_SHOT_DIR

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
const LONG = 400
const ev = (session: string, seq: number): AgentEvent => ({ session_id: session, client_event_id: `${session}-${seq}`, seq, ts: '2026-10-27T12:00:00Z', kind: 'user_message', payload: { text: `${session} message ${seq}`, source: 'chat' } } as AgentEvent)

/** The browser-fetch server (Files list, file contents): it can hold every read. */
const server = { hold: false, held: [] as (() => void)[], fetches: [] as string[] }
beforeEach(() => {
  Object.assign(server, { hold: false, held: [], fetches: [] })
  resetSessionRetentionForTest()
  useTranscriptStore.setState({ sessions: {} })
  const transcript = useTranscriptStore.getState()
  for (const [session, n] of [['sess-a', LONG], ['sess-b', 3]] as const) {
    transcript.ingestMany(session, Array.from({ length: n }, (_, i) => ev(session, i + 1)))
    transcript.ingestReplayDone(session, { lastSeq: n, hasMore: false })
  }
  useSessionStore.setState({ daemons: [makeDaemon()], sessions: ['a', 'b'].map(x => makeSession({ id: `sess-${x}`, kind: 'agent', status: 'idle', title: `Session ${x.toUpperCase()}` })) })
  localStorage.setItem(RAIL_PREF_KEY, '1')
  localStorage.setItem(RAIL_TAB_KEY, 'files')
  localStorage.setItem(explorerPrefKey('person-a'), JSON.stringify({ open: true, width: 320 }))
  apiFetch.mockReset().mockImplementation(async (url: string) => {
    const id = /\/api\/sessions\/([^/]+)\//.exec(url)?.[1] ?? ''
    if (url.includes('/workspace?')) return json({ result: { ok: true, launch_generation: 'launch', generation: 1, entries: [{ name: `notes-${id}.md`, path: 'notes.md', directory: false }], truncated: false }, members: [], runtime: 'host' })
    if (url.endsWith('/permissions')) return json({ can_grant: true, reason: '', grants: [], mcp_tools: [], boards: [] })
    return json({})
  })
  vi.stubGlobal('fetch', vi.fn(async (input: string) => {
    const url = String(input)
    server.fetches.push(url)
    if (server.hold) await new Promise<void>(resolve => server.held.push(resolve))
    const id = /\/api\/sessions\/([^/]+)\//.exec(url)?.[1] ?? ''
    if (url.endsWith('/artifacts')) return json({ artifacts: [{ id: `art-${id}`, name: `report-${id}.md`, size: 12, view: 'markdown', content_type: 'text/markdown', version: 1, latest_version: 1 }] })
    if (url.includes('/workspace-file')) return json({ path: 'notes.md', view: 'text', content_type: 'text/plain', mtime: '2026-10-27T12:00:00Z', content_b64: btoa(`Workspace notes of ${id}`) })
    return json([])
  }))
})
afterEach(() => { invalidateChatTransport(); vi.unstubAllGlobals(); resetSessionRetentionForTest() })

const nav: { go: (path: string) => void } = { go: () => {} }
function Nav() { const navigate = useNavigate(); useEffect(() => { nav.go = navigate }, [navigate]); return null }
const go = (path: string) => nav.go(path)
function mount() {
  return render(<MemoryRouter initialEntries={['/sessions/sess-a']}><CommandPaletteProvider><Nav />
    <div style={{ display: 'flex', height: '100vh' }}><div style={{ flex: 1, minWidth: 0 }}><Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes></div></div>
  </CommandPaletteProvider></MemoryRouter>)
}
const panel = () => screen.getByTestId('session-rail')
async function release() {
  server.hold = false
  for (let i = 0; i < 20 && server.held.length; i++) await act(async () => { server.held.splice(0).forEach(r => r()); await new Promise(r => setTimeout(r, 20)) })
}

for (const [width, height] of [[1440, 900], [3840, 2160]] as const) {
  it(`A→B→A draws A's chat, Files and open file tab in the first paint at ${width}px`, async () => {
    await page.viewport(width, height)
    mount()
    await screen.findByText(`sess-a message ${LONG}`)
    // Open a workspace file in a tab, as a person does from the explorer.
    await userEvent.click(await screen.findByRole('treeitem', { name: /^notes-sess-a\.md/ }))
    await screen.findByText('Workspace notes of sess-a')
    await within(panel()).findByText('report-sess-a.md')
    await act(async () => go('/sessions/sess-b'))
    await screen.findByText('sess-b message 3')
    await act(async () => { await new Promise(r => setTimeout(r, 100)) })

    server.hold = true
    server.fetches.length = 0
    send.mockClear()
    await act(async () => go('/sessions/sess-a'))
    // First paint: the long transcript, the Files list and the file's contents, nothing loading.
    expect(screen.getByText(`sess-a message ${LONG}`)).toBeTruthy()
    expect(screen.queryByTestId('history-loading')).toBeNull()
    expect(screen.getByText('Workspace notes of sess-a')).toBeTruthy()
    expect(document.querySelector('[data-testid="workspace-loading"], .workspace-loading')).toBeNull()
    expect(within(panel()).getByText('report-sess-a.md')).toBeTruthy()
    if (shotDir) await page.screenshot({ path: `${shotDir}/return-${width}x${height}.png` })
    // Only background reads: the Files list and the open file once each, nothing else of theirs.
    expect(server.fetches.filter(u => u.endsWith('/artifacts'))).toHaveLength(1)
    expect(server.fetches.filter(u => u.includes('/workspace-file'))).toHaveLength(1)
    await release()

    // The transcript resumes where the kept one ends: asked from its last seq, no gap, no duplicate.
    const subscribe = send.mock.calls.map(c => c[0] as { type?: string; session_id?: string; after_seq?: number }).find(m => m.type === 'subscribe_agent_events' && m.session_id === 'sess-a')
    expect(subscribe?.after_seq).toBe(LONG)
    await act(async () => {
      emit({ type: 'agent_events_replay_done', session_id: 'sess-a', last_seq: LONG, has_more: false } as never)
      emit({ ...ev('sess-a', LONG + 1), type: 'agent_event' } as never)
    })
    await screen.findByText(`sess-a message ${LONG + 1}`)
    expect(screen.getAllByText(`sess-a message ${LONG}`)).toHaveLength(1)
    const seqs = (useTranscriptStore.getState().sessions['sess-a']?.events ?? []).map(e => e.seq)
    expect(seqs).toEqual(Array.from({ length: LONG + 1 }, (_, i) => i + 1))
  })
}
