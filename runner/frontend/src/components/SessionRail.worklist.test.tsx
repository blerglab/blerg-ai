import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act, render, screen, fireEvent, waitFor, within } from '@testing-library/react'
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

describe('session panel: ticket, checklist and plan', () => {
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

  const items = (...xs: [string, boolean][]) => xs.map(([text, done]) => ({ text, done }))

  it('shows the pinned tickets first, then the checklist with its count and bar', async () => {
    stubFetch(0, liveReply({
      checklist: { items: items(['Read', true], ['Write tests', false], ['Ship', false]), event_id: 'c1', updated_at: iso(0) },
      plan: { items: [{ text: 'ignored', status: 'pending' }], updated_at: iso(0) },
      tickets: [
        { source: 'jira', key: 'ABC-1', title: 'Crash on save', status: 'In Progress', url: 'https://jira.example.test/browse/ABC-1', event_id: 't2', seq: 5, updated_at: iso(0) },
        { source: 'bugzilla', key: '77', title: 'Old bug', status: 'NEW', url: 'javascript:alert(1)', event_id: 't1', seq: 3, updated_at: iso(0) },
      ],
      ticket: { source: 'jira', key: 'ABC-1', title: 'Crash on save', status: 'In Progress', url: 'https://jira.example.test/browse/ABC-1', description: 'Steps to reproduce', event_id: 't2', updated_at: iso(0) },
    }))
    seedSession({ status: 'running' })
    seedEvents(user())
    await renderPage()
    const panel = await screen.findByTestId('session-rail')
    fireEvent.click(await within(panel).findByRole('tab', { name: /Plan/ }))
    const sections = within(panel).getAllByRole('region').map(r => r.getAttribute('data-testid'))
    // Plan holds the tickets then the list; Live keeps one summary line about it.
    expect(sections).toEqual(['rail-ticket', 'rail-checklist'])
    expect(within(panel).queryByTestId('rail-plan')).toBeNull()
    expect(within(panel).getByTestId('rail-checklist-meta')).toHaveTextContent('1 of 3 done')
    expect(within(panel).getByRole('progressbar')).toHaveAttribute('aria-valuenow', '1')
    const rows = within(panel).getAllByTestId('rail-ticket-row')
    expect(within(rows[0]).getByRole('link', { name: 'ABC-1' })).toHaveAttribute('href', 'https://jira.example.test/browse/ABC-1')
    expect(within(rows[1]).queryByRole('link')).toBeNull()
    fireEvent.click(within(rows[0]).getByTestId('rail-ticket-open'))
    const modal = await screen.findByTestId('ticket-modal')
    expect(modal).toHaveTextContent('Steps to reproduce')
    fireEvent.keyDown(modal, { key: 'Escape' })
    expect(screen.queryByTestId('ticket-modal')).toBeNull()
  })

  it('gives an item whose commits have a published diff a View diff, and none to an item without', async () => {
    const diff = 'd'.repeat(32)
    stubFetch(0, liveReply({
      checklist: { items: [{ text: 'Fix', done: true, commits: ['abcdef1'], diff }, { text: 'Ship', done: true, commits: ['1234567'] }], event_id: 'c1', updated_at: iso(0) },
    }))
    seedSession({ status: 'running' })
    seedEvents(user())
    await renderPage()
    const panel = await screen.findByTestId('session-rail')
    fireEvent.click(await within(panel).findByRole('tab', { name: /Plan/ }))
    const list = within(panel).getByTestId('rail-checklist')
    const buttons = within(list).getAllByRole('button', { name: /View diff/ })
    expect(buttons).toHaveLength(1)
    // The item with a diff opens it at a sha; the one without copies its sha.
    expect(within(list).getByRole('button', { name: 'Open commit abcdef1 in the diff' })).toBeTruthy()
    expect(within(list).getByRole('button', { name: 'Copy commit 1234567' })).toBeTruthy()
    expect(list.querySelector('a')).toBeNull()
    fireEvent.click(buttons[0])
  })

  it('shows the engine\'s plan when there is no checklist, labelled by what is in progress', async () => {
    stubFetch(0, liveReply({
      plan: { items: [{ text: 'Read', status: 'completed' }, { text: 'Write tests', status: 'in_progress', active: 'Writing tests' }, { text: 'Ship', status: 'pending' }], updated_at: iso(0) },
    }))
    seedSession({ status: 'running' })
    seedEvents(user())
    await renderPage()
    const panel = await screen.findByTestId('session-rail')
    expect(within(panel).getByTestId('rail-plan-meta')).toHaveTextContent('Writing tests · 1 of 3')
    expect(within(panel).queryByTestId('rail-ticket')).toBeNull()
  })

  it('on a server without the live route, reads the checklist and the pins from the loaded events', async () => {
    stubFetch(0)
    seedSession({ status: 'running' })
    seedEvents(user(),
      ev('checklist', { items: items(['a', true], ['b', false]), op: 'tick', changed: { index: 1, text: 'a', done: true } }),
      ev('ticket', { action: 'pin', ticket: { source: 'github', key: '12', title: 'Issue twelve' } }))
    await renderPage()
    const panel = await screen.findByTestId('session-rail')
    await waitFor(() => expect(within(panel).getByTestId('rail-checklist-meta')).toHaveTextContent('1 of 2 done'))
    expect(within(panel).getByTestId('rail-ticket')).toHaveTextContent('Issue twelve')
  })
})
