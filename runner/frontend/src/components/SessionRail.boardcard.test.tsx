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
import { resetLiveBoardCardsForTest } from '../lib/liveBoardCard'

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
let column = 'col-inbox'
let boardStatus = 200
const names: Record<string, string> = { 'col-inbox': 'inbox', 'col-doing': 'In progress' }
const liveCard = () => ({
  card: {
    id: 'card-12', board_id: 'board-a', number: 12, title: 'Fix the flaky test (live)', body: 'Live **body**', type: 'bug', priority: 'high',
    column_id: column, column_name: names[column], tags: ['ci', 'flaky'], repos: ['example-org/app'],
    origin: { repo: 'example-org/app', engine: 'claude', session_url: 'https://runner.example.test/sessions/s0' },
    columns: [{ id: 'col-inbox', name: 'inbox' }, { id: 'col-doing', name: 'In progress' }],
    trail: [{ type: 'created', actor: 'agent', created_at: iso(-60 * MIN) }, { type: 'comment', actor: 'Ann', text: 'Seen on main', created_at: iso(-30 * MIN) }],
  },
  url: 'https://board.example.test/boards/board-a/cards/12',
})
function stubFetch(live: LiveReply) {
  fetchMock = vi.fn((url: string, init?: RequestInit) => {
    const u = String(url)
    if (u.includes('/api/board-cards/')) {
      if (u.includes('/move')) column = JSON.parse(String(init?.body)).column_id
      if (boardStatus !== 200) return Promise.resolve({ ok: false, status: boardStatus, json: () => Promise.resolve({ error: 'the board did not give your sign-in access to this card' }) })
      return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(liveCard()) })
    }
    if (u.includes('/live')) return Promise.resolve({ ok: true, status: 200, headers: new Headers({ 'Content-Type': 'application/json', ETag: '"1"' }), json: () => Promise.resolve(live) })
    if (u.endsWith('/limit-resume')) return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({ enabled: false, state: 'disabled', reason: '', attempts: 0 }) })
    return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({}) })
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

const boardPin = { source: 'blerg-board', key: '#12', board_id: 'board-a', title: 'Fix the flaky test', status: 'In progress', url: 'https://board.example.test/boards/board-a/cards/12', event_id: 't1', seq: 3, updated_at: iso(0) }
const checklist = { items: [{ text: 'Reproduce', done: false }], event_id: 'c1', updated_at: iso(0) }

describe('a pinned board card shows the live card', () => {
  beforeEach(() => {
    seq = 0
    column = 'col-inbox'
    boardStatus = 200
    resetLiveBoardCardsForTest()
    wsMock.resetWsMock()
    wsMock.send.mockReturnValue(true)
    useSessionStore.setState({ daemons: [], sessions: [], statusChangedAt: {}, serverVersion: '', pendingSessionIds: {}, failedSessions: {} })
    useToastStore.setState({ toasts: [] })
    useMessageStore.setState({ messages: [] })
    useTranscriptStore.setState({ sessions: {} })
    localStorage.removeItem(RAIL_PREF_KEY)
    localStorage.removeItem(RAIL_TAB_KEY)
    viewport(1280)
  })
  afterEach(() => {
    vi.unstubAllGlobals()
    window.matchMedia = desktop
  })

  it('the chip and the dialog show the card as the board holds it, and Move works in place', async () => {
    stubFetch(liveReply({ checklist, tickets: [boardPin], ticket: { ...boardPin, description: 'agent copy' } }))
    seedSession({ status: 'running' })
    seedEvents(user())
    await renderPage()
    const panel = await screen.findByTestId('session-rail')
    // The Live summary's chip: the card's column now, not the agent's "In progress".
    const chip = await within(panel).findByTestId('rail-plan-summary-ticket')
    await waitFor(() => expect(within(chip).getByTestId('ticket-status')).toHaveTextContent('inbox'))
    expect(within(chip).getByRole('img', { name: 'live from the board' })).toBeInTheDocument()
    expect(fetchMock.mock.calls.some(c => String(c[0]) === '/api/board-cards/board-a/12?session=sess-a')).toBe(true)
    fireEvent.click(within(panel).getByRole('tab', { name: /Plan/ }))
    const row = within(panel).getByTestId('rail-ticket-row')
    expect(row).toHaveAttribute('data-live', 'true')
    expect(row).toHaveTextContent('Fix the flaky test (live)')
    fireEvent.click(within(row).getByTestId('rail-ticket-open'))
    const modal = await screen.findByTestId('ticket-modal')
    const live = within(modal).getByTestId('board-card-live')
    expect(within(modal.querySelector('header')!).getByTestId('ticket-status')).toHaveTextContent('inbox')
    expect(live).toHaveTextContent('The agent pinned it as “In progress”')
    expect(within(live).getByTestId('ticket-modal-details')).toHaveTextContent('flaky')
    expect(within(live).getByTestId('board-card-origin')).toHaveTextContent('example-org/app')
    expect(within(live).getByTestId('board-card-repos')).toHaveTextContent('primary')
    expect(within(live).getByTestId('board-card-trail')).toHaveTextContent('Seen on main')
    expect(within(live).getByTestId('board-card-open')).toHaveAttribute('href', 'https://board.example.test/boards/board-a/cards/12')
    expect(live).not.toHaveTextContent('agent copy')
    // The filer wrote the origin's address: another host is shown as text, never a link.
    const originSession = within(live).getByTestId('board-card-origin-session')
    expect(originSession.querySelector('a')).toBeNull()
    expect(originSession).toHaveTextContent('https://runner.example.test/sessions/s0')
    const reads = () => fetchMock.mock.calls.filter(c => String(c[0]).startsWith('/api/board-cards/') && !String(c[0]).includes('/move')).length
    const before = reads()
    fireEvent.change(within(live).getByLabelText('Move to column'), { target: { value: 'col-doing' } })
    await act(async () => { fireEvent.click(within(live).getByRole('button', { name: 'Move' })) })
    const move = fetchMock.mock.calls.find(c => String(c[0]).includes('/move'))!
    expect(String(move[0])).toBe('/api/board-cards/board-a/12/move?session=sess-a')
    expect(JSON.parse(String((move[1] as RequestInit).body))).toEqual({ column_id: 'col-doing' })
    await waitFor(() => expect(within(modal.querySelector('header')!).getByTestId('ticket-status')).toHaveTextContent('In progress'))
    // One credential exchange for the move: the card held is moved in place, not read again.
    expect(reads()).toBe(before)
  })

  it('shows the agent\'s copy, and says so, when the board cannot be read', async () => {
    boardStatus = 403
    stubFetch(liveReply({ checklist, tickets: [boardPin], ticket: { ...boardPin, description: 'agent copy' } }))
    seedSession({ status: 'running' })
    seedEvents(user())
    await renderPage()
    const panel = await screen.findByTestId('session-rail')
    fireEvent.click(await within(panel).findByTestId('rail-plan-summary-ticket'))
    const modal = await screen.findByTestId('ticket-modal')
    await waitFor(() => expect(within(modal).getByTestId('board-card-failed')).toHaveTextContent('Your sign-in cannot read this card'))
    expect(modal).toHaveTextContent('agent copy')
  })

  it('a pin from another tracker keeps the pinned dialog, and reads nothing from a board', async () => {
    const jira = { source: 'jira', key: 'ABC-1', title: 'Crash on save', status: 'Open', url: 'https://jira.example.test/browse/ABC-1', event_id: 't2', seq: 5, updated_at: iso(0) }
    stubFetch(liveReply({ checklist, tickets: [jira], ticket: { ...jira, description: 'Steps', type: 'Bug' } }))
    seedSession({ status: 'running' })
    seedEvents(user())
    await renderPage()
    const panel = await screen.findByTestId('session-rail')
    fireEvent.click(await within(panel).findByTestId('rail-plan-summary-ticket'))
    const modal = await screen.findByTestId('ticket-modal')
    expect(modal).toHaveTextContent('Steps')
    expect(within(modal).getByTestId('ticket-modal-details')).toHaveTextContent('Type Bug')
    expect(modal.querySelector('.ticket-side')).toBeNull()
    expect(fetchMock.mock.calls.some(c => String(c[0]).includes('/api/board-cards/'))).toBe(false)
  })
})
