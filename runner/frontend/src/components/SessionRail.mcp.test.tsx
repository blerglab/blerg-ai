import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act, render, screen, fireEvent, within } from '@testing-library/react'
import { MemoryRouter, Routes, Route } from 'react-router-dom'
import { useTranscriptStore, type AgentEvent } from '@blerglab/chat'
import SessionDetail from './SessionDetail'
import { useSessionStore } from '../hooks/useSessionStore'
import { useToastStore } from '../hooks/useToastStore'
import { useMessageStore } from '../hooks/useMessageStore'
import * as wsMock from '../test/wsMock'
import { makeDaemon, makeSession } from '../test/fixtures'
import { RAIL_PREF_KEY, RAIL_TAB_KEY } from '../lib/sessionRail'
import type { SessionInfo } from '../types'
import type { LiveReply } from '../lib/liveState'

vi.mock('../ws', () => import('../test/wsMock'))

// The session page's side panel: what needs you, what runs, the session's facts; a column beside
// the chat on a wide screen, a drawer on a narrow one.

const MIN = 60_000

// A page width: matchMedia answers (max-width: N px) queries for it.
function viewport(width: number) {
  window.matchMedia = vi.fn().mockImplementation((query: string) => {
    const m = /max-width:\s*(\d+)px/.exec(query)
    return {
      matches: m ? width <= Number(m[1]) : false, media: query, onchange: null,
      addEventListener: () => {}, removeEventListener: () => {}, addListener: () => {}, removeListener: () => {}, dispatchEvent: () => false,
    }
  }) as unknown as typeof window.matchMedia
}

function seedSession(over: Partial<SessionInfo> = {}) {
  const session = makeSession({ limit_resume_supported: true, kind: 'agent', status: 'running', title: 'Ship it', model: 'claude-opus-5-5', effort: 'high', runtime: 'docker', started_at: new Date(Date.now() - 135 * MIN).toISOString(), ...over })
  useSessionStore.setState({ daemons: [makeDaemon()], sessions: [session], statusChangedAt: { [session.id]: Date.now() }, serverVersion: 'test' })
  return session
}

let seq = 0
function ev(kind: string, payload: unknown, agoMs = 5 * MIN): AgentEvent {
  seq++
  return { session_id: 'sess-a', client_event_id: `r${seq}`, seq, ts: new Date(Date.now() - agoMs).toISOString(), kind, payload } as AgentEvent
}

const user = () => ev('user_message', { text: 'go', source: 'chat' }, 10 * MIN)

function seedEvents(...events: AgentEvent[]) {
  useTranscriptStore.setState({ sessions: {} })
  const s = useTranscriptStore.getState()
  for (const e of events) s.ingest(e)
  s.ingestReplayDone('sess-a', { lastSeq: seq, hasMore: false })
}

const iso = (offsetMs: number) => new Date(Date.now() + offsetMs).toISOString()

// GET /api/sessions/{id}/live's reply.
function liveReply(over: Partial<LiveReply> = {}): LiveReply {
  return {
    session_id: 'sess-a', through_seq: 100, generated_at: iso(0), tokens_hidden: false, asks: [], subagents: [], tasks: [],
    tool_changes: 0, counts: { needs_you: 0, running: 0 }, ...over,
  }
}

// GET /api/tool-changes answers `changes` pending changes on the session. GET /live answers `live`
// as JSON, or, without it, as a server that cannot (the page then reads the chat's loaded events, as
// it does on an older server). Anything else is empty.
let fetchMock: ReturnType<typeof vi.fn>
function stubFetch(changes = 0, live?: LiveReply) {
  fetchMock = vi.fn((url: string) => {
    if (String(url).includes('/live')) {
      return Promise.resolve(live
        ? { ok: true, status: 200, headers: new Headers({ 'Content-Type': 'application/json', ETag: '"1"' }), json: () => Promise.resolve(live) }
        : { ok: false, status: 404, headers: new Headers({ 'Content-Type': 'text/plain; charset=utf-8' }), json: () => Promise.reject(new SyntaxError('not JSON')) })
    }
    if (String(url).endsWith('/limit-resume')) return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({ enabled: false, state: 'disabled', reason: '', attempts: 0 }) })
    const body = String(url).includes('/api/tool-changes')
      ? { tool_changes: Array.from({ length: changes }, (_, i) => ({ id: `t${i}`, kind: 'changed', muted: false })) }
      : {}
    return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(body) })
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

// Renders the page and lets its first read of the live state answer.
async function renderPage() {
  const r = render(
    <MemoryRouter initialEntries={['/sessions/sess-a']}>
      <Routes>
        <Route path="/sessions/:id" element={<SessionDetail />} />
        <Route path="/proposals" element={<div>PROPOSALS</div>} />
      </Routes>
    </MemoryRouter>,
  )
  await act(async () => { for (let k = 0; k < 10; k++) await Promise.resolve() })
  return r
}


const desktop = window.matchMedia

describe('session panel: MCP servers, the held first message, checklist commits', () => {
  beforeEach(() => {
    seq = 0
    wsMock.resetWsMock()
    wsMock.send.mockReturnValue(true)
    useSessionStore.setState({ daemons: [], sessions: [], statusChangedAt: {}, serverVersion: '', pendingSessionIds: {}, failedSessions: {} })
    useToastStore.setState({ toasts: [] })
    useMessageStore.setState({ messages: [] })
    useTranscriptStore.setState({ sessions: {} })
    localStorage.removeItem(RAIL_PREF_KEY)
    localStorage.removeItem(RAIL_TAB_KEY)
    viewport(1280)
    Element.prototype.scrollIntoView = vi.fn() as unknown as Element['scrollIntoView']
  })
  afterEach(() => {
    vi.unstubAllGlobals()
    window.matchMedia = desktop
    delete (Element.prototype as { scrollIntoView?: unknown }).scrollIntoView
  })

  const srv = (name: string, status: string, tools = 0, source = 'user') => ({ name, status, source, tools })
  const mcp = (servers: unknown[], settled: boolean, extra: object = {}) => ({ servers, settled, since: iso(-12_000), engine: 'claude', ...extra })

  const snapshot = () => ev('capabilities', {
    engine: 'claude',
    groups: [{ id: 'mcp', label: 'MCP servers', items: [{ name: 'jira', status: 'pending', source: 'user' }, { name: 'docs', status: 'connected' }] }],
  })
  const openCaps = () => fireEvent.click(screen.getByRole('tab', { name: /Capabilities/ }))

  it('reviews a live approval without its transcript event or earlier-history loading', async () => {
    const request = { request_id: 'request-a', parent_session_id: 'sess-a', status: 'pending', expires_at: iso(60_000), offered: ['once', 'deny'], request: { engine: 'codex', runtime: 'docker', title: 'Offline fixture review', prompt: 'Public fixture instructions' } }
    stubFetch(0, liveReply({ delegation_requests: [{ request_id: 'request-a', engine: 'codex', title: 'Offline fixture review', expires_at: request.expires_at }] }))
    const fallback = fetchMock.getMockImplementation() as (url: string, ...args: unknown[]) => unknown
    fetchMock.mockImplementation((url: string, ...args: unknown[]) => String(url).endsWith('/api/delegation-requests/request-a')
      ? Promise.resolve({ ok: true, status: 200, json: async () => request }) : fallback(url, ...args))
    seedSession({ engine: 'codex', status: 'running' })
    seedEvents(user())
    await renderPage()
    fireEvent.click(await screen.findByRole('button', { name: 'Review request' }))
    const needs = screen.getByTestId('rail-needs')
    expect(await within(needs).findByRole('button', { name: 'Allow once' })).toBeVisible()
    expect(within(needs).getByText('Public fixture instructions')).toBeVisible()
    expect(screen.queryByText('Load earlier messages to review this request.')).toBeNull()
    expect(useTranscriptStore.getState().sessions['sess-a'].events.some(event => event.kind === 'delegation_request')).toBe(false)
  })

  it('Live has no MCP servers section', async () => {
    stubFetch(0, liveReply({ mcp: mcp([srv('jira', 'connected', 12), srv('docs', 'pending')], false) }))
    seedSession({ status: 'running' })
    seedEvents(user())
    await renderPage()
    await screen.findByTestId('session-rail')
    expect(screen.queryByTestId('rail-mcp')).toBeNull()
    expect(screen.getByText('MCP servers')).not.toBeVisible()
  })

  it('Capabilities shows each server with its live status, over the start-up snapshot', async () => {
    stubFetch(0, liveReply({ mcp: mcp([srv('jira', 'connected', 12), srv('docs', 'pending'), srv('db', 'failed'), srv('drive', 'needs-auth'), srv('old', 'disabled')], false, { released: 'timeout' }) }))
    seedSession({ status: 'running' })
    seedEvents(user(), snapshot())
    await renderPage()
    await screen.findByTestId('session-rail')
    openCaps()
    const group = await screen.findByTestId('caps-group-mcp')
    expect(within(group).getByText('jira').closest('li')).toHaveTextContent('connected')
    expect(within(group).getByText('docs').closest('li')).toHaveTextContent('connecting')
    expect(within(group).getByText('docs').closest('li')?.querySelector('.status-pending')).not.toBeNull()
    expect(within(group).getByText('db').closest('li')?.querySelector('.status-failed')).not.toBeNull()
    expect(within(group).getByText('drive').closest('li')).toHaveTextContent('needs sign-in')
    expect(within(group).getByText('old').closest('li')).toHaveTextContent('disabled')
    expect(group).toHaveTextContent('sent after the wait ran out')
  })

  it('shows an on-demand server without a connection claim or first-message hold', async () => {
    stubFetch(0, liveReply({ mcp: mcp([srv('docs', 'lazy')], true) }))
    seedSession({ status: 'running' })
    seedEvents(user())
    await renderPage()
    await screen.findByTestId('session-rail')
    openCaps()
    const group = await screen.findByTestId('caps-group-mcp')
    const row = within(group).getByText('docs').closest('li')!
    expect(row).toHaveTextContent('connects on demand')
    expect(row.querySelector('.status-connected')).toBeNull()
    expect(row.querySelector('.status-pending')).toBeNull()
    expect(screen.queryByTestId('mcp-hold')).toBeNull()
  })

  it('Capabilities falls back to the snapshot when the live state has no MCP servers', async () => {
    stubFetch(0, liveReply({ mcp: mcp([], true) }))
    seedSession({ status: 'running' })
    seedEvents(user(), snapshot())
    await renderPage()
    await screen.findByTestId('session-rail')
    openCaps()
    const group = await screen.findByTestId('caps-group-mcp')
    expect(within(group).getByText('jira').closest('li')).toHaveTextContent('connecting')
    expect(within(group).getByText('docs').closest('li')).toHaveTextContent('connected')
    expect(screen.queryByTestId('mcp-hold')).toBeNull()
  })

  it('the held first message shows above the composer; Send now releases it for the owner', async () => {
    stubFetch(0, liveReply({
      mcp: mcp([srv('jira', 'pending')], false), mcp_hold: { since: iso(-12_000), until: iso(18_000) }, can_release_prompt: true,
    }))
    seedSession({ status: 'running' })
    seedEvents(user())
    await renderPage()
    const line = await screen.findByTestId('mcp-hold')
    expect(line).toHaveTextContent('Waiting for MCP servers before sending your first message · 0:12 / 0:30')
    fireEvent.click(within(line).getByRole('button', { name: 'Send now' }))
    expect(wsMock.send).toHaveBeenCalledWith({ type: 'release_held_prompt', session_id: 'sess-a' })
  })

  it('a viewer who does not own the session sees the wait but no Send now', async () => {
    stubFetch(0, liveReply({ mcp: mcp([srv('jira', 'pending')], false), mcp_hold: { since: iso(-1000), until: iso(29_000) } }))
    seedSession({ status: 'running' })
    seedEvents(user())
    await renderPage()
    const line = await screen.findByTestId('mcp-hold')
    expect(within(line).queryByRole('button')).toBeNull()
  })

  it('an item of the checklist shows its commits as one chip, never linked out of Blerg even when the server knows the repository', async () => {
    stubFetch(0, liveReply({
      checklist: {
        items: [{ text: 'Fix', done: false, commits: ['abcdef1234', '1234567'] }, { text: 'Ship', done: false }],
        event_id: 'c1', updated_at: iso(0), commit_urls: { '1234567': 'https://forge.example.test/c/1234567' },
      },
    }))
    seedSession({ status: 'running' })
    seedEvents(user())
    await renderPage()
    fireEvent.click(await screen.findByRole('tab', { name: /Plan/ }))
    const sec = await screen.findByTestId('rail-checklist')
    const shas = within(sec).getAllByTestId('rail-plan-commit')
    expect(shas.map(s => s.textContent)).toEqual(['abcdef1+1'])
    expect(shas[0]).toHaveAccessibleName('Copy commits abcdef1, 1234567')
    expect(shas[0].tagName).toBe('BUTTON')
    expect(sec.querySelector('a')).toBeNull()
    expect(within(sec).queryByRole('button', { name: /View diff/ })).toBeNull()
  })
})
