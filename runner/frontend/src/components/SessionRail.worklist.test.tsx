import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act, render, screen, fireEvent, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Routes, Route } from 'react-router-dom'
import { useTranscriptStore, type AgentEvent } from '@blerglab/chat'
import SessionDetail from './SessionDetail'
import SessionRail, { type SessionRailProps } from './SessionRail'
import { makeLive } from '../test/planTabFixture'
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

  it('opens the exact assigned plan item, focuses it, and never matches a title', async () => {
    stubFetch(0, liveReply({
      checklist: {items:[{id:'wrong',text:'Same title',done:false,assignees:[{kind:'subagent',id:'call-key'}]},{id:'native-work',text:'Assigned work',done:false,in_progress:true,assignees:[{kind:'subagent',id:'native-child'}]}],event_id:'c1',updated_at:iso(0)},
      subagents:[{key:'call-key',child_id:'native-child',title:'Same title',type:'reviewer',model:'',tokens:null,doing:'',started_at:iso(-MIN),active_at:iso(0),background:false,seq:1}],
    }))
    seedSession(); seedEvents(user()); await renderPage()
    const panel = screen.getByTestId('session-rail')
    const reference = within(within(panel).getByTestId('rail-subagent')).getByTestId('rail-checklist-reference')
    expect(reference).toHaveTextContent('Assigned work')
    fireEvent.click(reference)
    await waitFor(() => expect(document.activeElement).toHaveAttribute('data-checklist-id','native-work'))
    expect(within(panel).getByRole('tab',{name:/Plan/})).toHaveAttribute('aria-selected','true')
    const row = document.activeElement as HTMLElement
    expect(row).toHaveClass('checklist-highlight')
    expect(Element.prototype.scrollIntoView).toHaveBeenCalled()
    fireEvent.click(within(panel).getByRole('tab',{name:/Live/}))
    fireEvent.click(within(within(panel).getByTestId('rail-subagent')).getByTestId('rail-checklist-reference'))
    await waitFor(() => expect(row).toHaveClass('checklist-highlight'))
  })

  it('shows every doing item and explicit owned child assignment', async () => {
    stubFetch(0, liveReply({checklist:{items:[{id:'a',text:'Build',done:false,in_progress:true,assignees:[{kind:'child_session',id:'owned-child'}]},{id:'b',text:'Review',done:false,in_progress:true}],event_id:'c1',updated_at:iso(0)},children:[{id:'owned-child',title:'Worker',engine:'codex',model:'fixture',status:'running',started_at:iso(-MIN),asking:false,tokens:null}]}))
    seedSession();seedEvents(user());await renderPage()
    const panel=screen.getByTestId('session-rail')
    expect(within(panel).getByTestId('rail-plan-summary-text')).toHaveTextContent('Doing: Build; Review')
    // The running child has one card, under Running now, and it carries the reference.
    expect(within(within(panel).getByTestId('rail-started')).queryByTestId('rail-checklist-reference')).toBeNull()
    fireEvent.click(within(within(panel).getByTestId('rail-running')).getByTestId('rail-checklist-reference'))
    await waitFor(()=>expect(document.activeElement).toHaveAttribute('data-checklist-id','a'))
  })

  it.each([1280, 700])('opens the rail at %ipx from a keyboard transcript assignment and keeps a reordered identity stable', async width => {
    viewport(width)
    const entries=[{id:'a',text:'Assigned work',done:false,in_progress:true,assignees:[{kind:'subagent' as const,id:'native-a'}]},{id:'b',text:'Other',done:false}]
    stubFetch(0,liveReply({checklist:{items:entries,event_id:'c1',updated_at:iso(0)}}))
    seedSession();seedEvents(user(),ev('subagent_started',{child_id:'native-a',call_id:'call-a',summary:'Worker'}),ev('checklist',{op:'set',items:entries}))
    localStorage.setItem(RAIL_PREF_KEY,'0')
    await renderPage()
    const reference=within(screen.getByTestId('subagent-card')).getByRole('button',{name:'Plan · Assigned work'})
    reference.focus();await userEvent.setup().keyboard('{Enter}')
    await waitFor(()=>expect(document.activeElement).toHaveAttribute('data-checklist-id','a'))
    const original=document.activeElement
    seq=100
    act(()=>useTranscriptStore.getState().ingest(ev('checklist',{op:'set',items:[entries[1],entries[0]]})))
    expect((original as HTMLElement).isConnected).toBe(true)
    expect(original).toHaveAttribute('data-checklist-id','a')
    act(()=>useTranscriptStore.getState().ingest(ev('checklist',{op:'set',items:[entries[1],{id:'replacement',text:'Assigned work',done:false}]})))
    expect((original as HTMLElement).isConnected).toBe(false)
    expect(screen.getByTestId('rail-checklist').querySelector('.checklist-highlight')).toBeNull()
    expect(within(screen.getByTestId('subagent-card')).queryByRole('button',{name:'Plan · Assigned work'})).toBeNull()
  })

  it('uses steady scroll under reduced motion', async () => {
    const originalMatch=window.matchMedia
    window.matchMedia=vi.fn(query=>query==='(prefers-reduced-motion: reduce)'?{...originalMatch(query),matches:true}:originalMatch(query)) as typeof window.matchMedia
    stubFetch(0,liveReply({checklist:{items:[{id:'a',text:'Work',done:false,assignees:[{kind:'subagent',id:'native'}]}],event_id:'c1',updated_at:iso(0)},subagents:[{key:'native',title:'Worker',type:'',model:'',tokens:null,doing:'',started_at:iso(-MIN),active_at:iso(0),background:false,seq:1}]}))
    seedSession();seedEvents(user());await renderPage()
    fireEvent.click(within(screen.getByTestId('rail-subagent')).getByTestId('rail-checklist-reference'))
    await waitFor(()=>expect(document.activeElement).toHaveAttribute('data-checklist-id','a'))
    expect(Element.prototype.scrollIntoView).toHaveBeenLastCalledWith({block:'center',behavior:'auto'})
  })

  it('reports an unavailable target without focusing another item', async () => {
    stubFetch();const session=seedSession()
    render(<MemoryRouter><SessionRail id="rail" session={session} live={makeLive({checklist:{items:[{id:'different',text:'Same title',done:false}],eventId:'fresh',updatedAt:null}})} toolChanges={0} mode="panel" open onClose={()=>{}} onOpenArtifact={()=>{}} openChecklistItem={{id:'old',n:1}} /></MemoryRouter>)
    await waitFor(()=>expect(screen.getByText('This checklist item is no longer available.')).toHaveAttribute('role','status'))
    expect(vi.mocked(Element.prototype.scrollIntoView).mock.contexts.filter(element=>element instanceof HTMLElement&&element.dataset.checklistId)).toHaveLength(0)
  })

  it('uses the authoritative canonical live assignment for transcript chips', async () => {
    stubFetch(0,liveReply({checklist:{items:[{id:'stable',text:'Canonical work',done:false,assignees:[{kind:'subagent',id:'native-a'}]}],event_id:'new',updated_at:iso(0)}}))
    seedSession();seedEvents(user(),ev('subagent_started',{child_id:'native-a',call_id:'call-a',summary:'Worker'}),ev('checklist',{op:'assign',items:[{id:'stable',text:'Old alias',done:false,assignees:[{kind:'subagent',id:'call-a'}]}]}))
    await renderPage()
    const card=screen.getByTestId('subagent-card')
    expect(within(card).queryByRole('button',{name:'Plan · Old alias'})).toBeNull()
    fireEvent.click(within(card).getByRole('button',{name:'Plan · Canonical work'}))
    await waitFor(()=>expect(document.activeElement).toHaveAttribute('data-checklist-id','stable'))
  })

  it('scopes repeated request numbers and the same opaque ID to each session', async () => {
    stubFetch();seedSession()
    const props:SessionRailProps={id:'rail',session:makeSession({id:'session-a'}),live:makeLive({checklist:{items:[{id:'same-id',text:'A work',done:false}],eventId:'a',updatedAt:null}}),toolChanges:0,mode:'panel',open:true,onClose:()=>{},onOpenArtifact:()=>{},openChecklistItem:{id:'same-id',n:1}}
    const view=render(<MemoryRouter><SessionRail {...props}/></MemoryRouter>)
    await waitFor(()=>expect(document.activeElement).toHaveTextContent('A work'))
    const next={...props,session:makeSession({id:'session-b'}),live:makeLive({checklist:{items:[{id:'same-id',text:'B work',done:false}],eventId:'b',updatedAt:null}}),openChecklistItem:null}
    view.rerender(<MemoryRouter><SessionRail {...next}/></MemoryRouter>)
    expect(screen.getByTestId('rail-checklist').querySelector('.checklist-highlight')).toBeNull()
    view.rerender(<MemoryRouter><SessionRail {...next} openChecklistItem={{id:'same-id',n:1}}/></MemoryRouter>)
    await waitFor(()=>{expect(document.activeElement).toHaveTextContent('B work');expect(document.activeElement).toHaveClass('checklist-highlight')})
  })

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
