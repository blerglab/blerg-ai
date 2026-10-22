import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes, useNavigate } from 'react-router-dom'
import { useTranscriptStore, type AgentEvent } from '@blerglab/chat'
import SessionDetail from './SessionDetail'
import { useSessionStore } from '../hooks/useSessionStore'
import * as wsMock from '../test/wsMock'
import { makeDaemon, makeSession } from '../test/fixtures'
import { RAIL_PREF_KEY, RAIL_TAB_KEY } from '../lib/sessionRail'
import { reelReply } from '../test/reelFixture'
import { MIN_GAP_MS } from '../hooks/useTopics'

vi.mock('../ws', () => import('../test/wsMock'))
// The chat is the package's own (tested there). Here it shows which file it was asked to open, and a
// button that consumes the request the way its viewer does when it opens the file.
vi.mock('@blerglab/chat', async (importOriginal) => {
  const mod = await importOriginal<typeof import('@blerglab/chat')>()
  return {
    ...mod,
    ChatView: ({ linkedArtifact, onLinkedArtifactConsumed }: { linkedArtifact?: string | null; onLinkedArtifactConsumed?: () => void }) => (
      <div data-testid="chat-stub" data-linked={linkedArtifact ?? ''}>
        <button type="button" onClick={onLinkedArtifactConsumed}>viewer opened</button>
      </div>
    ),
  }
})

function viewport(width: number) {
  window.matchMedia = vi.fn().mockImplementation((query: string) => {
    const m = /max-width:\s*(\d+)px/.exec(query)
    return {
      matches: m ? width <= Number(m[1]) : false, media: query, onchange: null,
      addEventListener: () => {}, removeEventListener: () => {}, addListener: () => {}, removeListener: () => {}, dispatchEvent: () => false,
    }
  }) as unknown as typeof window.matchMedia
}

const desktop = window.matchMedia
let topicReads = 0

function stubFetch() {
  topicReads = 0
  vi.stubGlobal('fetch', vi.fn((url: string) => {
    if (String(url).includes('/topics')) {
      topicReads++
      return Promise.resolve(new Response(JSON.stringify(reelReply()), { status: 200, headers: { 'Content-Type': 'application/json', ETag: '"1"' } }))
    }
    return Promise.resolve({ ok: true, status: 200, headers: new Headers(), json: () => Promise.resolve({}) })
  }))
}

// Lets the page's first read of the live state answer (this stub's server has no such route: the
// page reads the chat's loaded events, as on an older server).
const liveRead = () => act(async () => { for (let k = 0; k < 10; k++) await Promise.resolve() })

function renderPage() {
  const session = makeSession({ kind: 'agent', status: 'idle', title: 'Ship it', runtime: 'docker', started_at: new Date(Date.now() - 3_600_000).toISOString() })
  useSessionStore.setState({ daemons: [makeDaemon()], sessions: [session], statusChangedAt: { [session.id]: Date.now() }, serverVersion: 'test' })
  useTranscriptStore.setState({ sessions: {} })
  const s = useTranscriptStore.getState()
  s.ingest({ type: 'agent_event', session_id: session.id, client_event_id: 'u1', seq: 1, ts: new Date().toISOString(), kind: 'user_message', payload: { text: 'go', source: 'chat' } } as AgentEvent)
  s.ingestReplayDone(session.id, { lastSeq: 1, hasMore: false })
  return render(
    <MemoryRouter initialEntries={[`/sessions/${session.id}`]}>
      <Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes>
    </MemoryRouter>,
  )
}

// Two sessions, A (sess-a, the page's) and B, with buttons that move the page between them.
function GoTo() {
  const navigate = useNavigate()
  return (
    <>
      <button type="button" onClick={() => navigate('/sessions/sess-a')}>to A</button>
      <button type="button" onClick={() => navigate('/sessions/sess-b')}>to B</button>
    </>
  )
}

function renderTwo() {
  const a = makeSession({ kind: 'agent', status: 'idle', title: 'Ship it', runtime: 'docker' })
  const b = makeSession({ id: 'sess-b', kind: 'agent', status: 'idle', title: 'Other', runtime: 'docker' })
  useSessionStore.setState({ daemons: [makeDaemon()], sessions: [a, b], statusChangedAt: { [a.id]: Date.now(), [b.id]: Date.now() }, serverVersion: 'test' })
  useTranscriptStore.setState({ sessions: {} })
  const s = useTranscriptStore.getState()
  for (const id of [a.id, b.id]) {
    s.ingest({ type: 'agent_event', session_id: id, client_event_id: `u-${id}`, seq: 1, ts: new Date().toISOString(), kind: 'user_message', payload: { text: 'go', source: 'chat' } } as AgentEvent)
    s.ingestReplayDone(id, { lastSeq: 1, hasMore: false })
  }
  return render(
    <MemoryRouter initialEntries={['/sessions/sess-a']}>
      <Routes><Route path="/sessions/:id" element={<><GoTo /><SessionDetail /></>} /></Routes>
    </MemoryRouter>,
  )
}

const rail = () => screen.queryByTestId('session-rail')
// Live's name carries what needs the person ("Live, 1 needs you").
const tab = (name: 'Live' | 'History' | 'Files' | 'Capabilities') => within(rail()!).getByRole('tab', { name: ['Live', 'Files', 'Capabilities'].includes(name) ? new RegExp(`^${name}`) : name })

function askNow(sessionId: string) {
  act(() => {
    useTranscriptStore.getState().ingest({
      type: 'agent_event', session_id: sessionId, client_event_id: 'q1', seq: 2, ts: new Date(Date.now() + 1000).toISOString(),
      kind: 'ask', payload: { body: 'Merge now?', message_id: 'm9' },
    } as AgentEvent)
  })
}

/** Under fake timers: runs every timer and promise the page set up, past useTopics' read gap, so a
 *  read that was going to happen has happened. */
const settle = () => act(async () => { await vi.advanceTimersByTimeAsync(MIN_GAP_MS * 2) })

describe('the session panel\'s History tab', () => {
  beforeEach(() => {
    wsMock.resetWsMock()
    localStorage.removeItem(RAIL_PREF_KEY)
    localStorage.removeItem(RAIL_TAB_KEY)
    stubFetch()
    viewport(1280)
  })
  afterEach(() => {
    vi.useRealTimers()
    vi.unstubAllGlobals()
    window.matchMedia = desktop
    document.body.style.overflow = ''
  })

  it('sits beside Live in a tablist, before Files and Capabilities, and the choice is remembered', async () => {
    localStorage.setItem(RAIL_PREF_KEY, '1')
    const { unmount } = renderPage()
    expect(within(rail()!).getAllByRole('tab').map(t => t.textContent)).toEqual(['Live', 'History', 'Files', 'Capabilities'])
    expect(tab('Live')).toHaveAttribute('aria-selected', 'true')
    expect(within(rail()!).getByTestId('rail-needs')).toBeInTheDocument()
    fireEvent.click(tab('History'))
    expect(tab('History')).toHaveAttribute('aria-selected', 'true')
    expect(localStorage.getItem(RAIL_TAB_KEY)).toBe('history')
    expect(within(rail()!).getByTestId('rail-needs')).not.toBeVisible()
    expect(await within(rail()!).findByTestId('reel-list')).toBeVisible()
    unmount()
    renderPage()
    expect(tab('History')).toHaveAttribute('aria-selected', 'true')
  })

  it('opens on Live for a new question, without changing the tab the person chose', async () => {
    localStorage.setItem(RAIL_PREF_KEY, '0')
    localStorage.setItem(RAIL_TAB_KEY, 'history')
    renderPage()
    await liveRead()
    expect(rail()).not.toBeVisible()
    askNow(useSessionStore.getState().sessions[0].id)
    expect(rail()).toBeVisible()
    expect(tab('Live')).toHaveAttribute('aria-selected', 'true')
    expect(within(rail()!).getByTestId('rail-needs')).toBeInTheDocument()
    expect(localStorage.getItem(RAIL_TAB_KEY)).toBe('history')
  })

  it('opens by itself on Live when a question waits and no choice was made', async () => {
    localStorage.setItem(RAIL_TAB_KEY, 'history')
    renderPage()
    await liveRead()
    askNow(useSessionStore.getState().sessions[0].id)
    expect(rail()).toBeVisible()
    expect(tab('Live')).toHaveAttribute('aria-selected', 'true')
    expect(localStorage.getItem(RAIL_TAB_KEY)).toBe('history')
  })

  it('shows Live once for an opening by itself: back from another session, the tab picked since stays', async () => {
    localStorage.setItem(RAIL_TAB_KEY, 'history')
    renderTwo()
    await liveRead()
    askNow('sess-a')
    expect(tab('Live')).toHaveAttribute('aria-selected', 'true')
    fireEvent.click(tab('History'))
    fireEvent.click(screen.getByRole('button', { name: 'to B' }))
    fireEvent.click(screen.getByRole('button', { name: 'to A' }))
    expect(tab('History')).toHaveAttribute('aria-selected', 'true')
  })

  it('reads nothing while the panel is open on Live from the first render', async () => {
    localStorage.setItem(RAIL_PREF_KEY, '1')
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    renderPage()
    expect(tab('Live')).toHaveAttribute('aria-selected', 'true')
    await settle()
    expect(topicReads).toBe(0)
  })

  it('counts what needs the person on the Live tab, so a question shows from History', async () => {
    localStorage.setItem(RAIL_PREF_KEY, '1')
    localStorage.setItem(RAIL_TAB_KEY, 'history')
    renderPage()
    await liveRead()
    expect(within(tab('Live')).queryByTestId('rail-tab-count')).toBeNull()
    askNow(useSessionStore.getState().sessions[0].id)
    expect(tab('History')).toHaveAttribute('aria-selected', 'true')
    expect(within(tab('Live')).getByTestId('rail-tab-count')).toHaveTextContent('1')
    expect(tab('Live')).toHaveAccessibleName('Live, 1 needs you')
  })

  it('keeps History\'s list and opened cards across a switch to Live and back, without reading again', async () => {
    localStorage.setItem(RAIL_PREF_KEY, '1')
    localStorage.setItem(RAIL_TAB_KEY, 'history')
    renderPage()
    await within(rail()!).findByTestId('reel-list')
    fireEvent.click(within(rail()!).getAllByTestId('reel-head')[1])
    fireEvent.click(tab('Live'))
    expect(within(rail()!).getByTestId('rail-needs')).toBeVisible()
    expect(within(rail()!).getByTestId('reel-list')).not.toBeVisible()
    fireEvent.click(tab('History'))
    expect(within(rail()!).getByTestId('reel-list')).toBeVisible()
    expect(within(rail()!).getAllByTestId('reel-head')[1]).toHaveAttribute('aria-expanded', 'true')
    expect(topicReads).toBe(1)
  })

  it('moves between the tabs with the arrow keys, Home and End', () => {
    localStorage.setItem(RAIL_PREF_KEY, '1')
    renderPage()
    fireEvent.keyDown(within(rail()!).getByRole('tablist'), { key: 'ArrowRight' })
    expect(tab('History')).toHaveAttribute('aria-selected', 'true')
    expect(tab('History')).toHaveFocus()
    fireEvent.keyDown(within(rail()!).getByRole('tablist'), { key: 'ArrowLeft' })
    expect(tab('Live')).toHaveAttribute('aria-selected', 'true')
    fireEvent.keyDown(within(rail()!).getByRole('tablist'), { key: 'End' })
    expect(tab('Capabilities')).toHaveAttribute('aria-selected', 'true')
    expect(tab('Capabilities')).toHaveFocus()
    fireEvent.keyDown(within(rail()!).getByRole('tablist'), { key: 'ArrowRight' })
    expect(tab('Live')).toHaveAttribute('aria-selected', 'true')
    fireEvent.keyDown(within(rail()!).getByRole('tablist'), { key: 'ArrowLeft' })
    expect(tab('Capabilities')).toHaveAttribute('aria-selected', 'true')
    fireEvent.keyDown(within(rail()!).getByRole('tablist'), { key: 'ArrowLeft' })
    expect(tab('Files')).toHaveAttribute('aria-selected', 'true')
    expect(tab('Files')).toHaveFocus()
    fireEvent.keyDown(within(rail()!).getByRole('tablist'), { key: 'Home' })
    expect(tab('Live')).toHaveAttribute('aria-selected', 'true')
    expect(tab('Live')).toHaveFocus()
  })

  it('reads the server only while History is on screen', async () => {
    localStorage.setItem(RAIL_PREF_KEY, '0')
    localStorage.setItem(RAIL_TAB_KEY, 'history')
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    renderPage()
    await settle()
    expect(topicReads).toBe(0)
    vi.useRealTimers()
    fireEvent.click(screen.getByTestId('rail-toggle'))
    await waitFor(() => expect(topicReads).toBe(1))
  })

  it('opens the same file again after its viewer was closed', async () => {
    localStorage.setItem(RAIL_PREF_KEY, '1')
    localStorage.setItem(RAIL_TAB_KEY, 'history')
    renderPage()
    await within(rail()!).findByTestId('reel-list')
    const done = within(rail()!).getAllByTestId('reel-card')[1]
    fireEvent.click(within(done).getByTestId('reel-head'))
    const v2 = () => within(done).getAllByTestId('reel-sum-file')[0]
    fireEvent.click(v2())
    expect(screen.getByTestId('chat-stub')).toHaveAttribute('data-linked', 'a2')
    fireEvent.click(screen.getByRole('button', { name: 'viewer opened' }))
    expect(screen.getByTestId('chat-stub')).toHaveAttribute('data-linked', '')
    fireEvent.click(v2())
    expect(screen.getByTestId('chat-stub')).toHaveAttribute('data-linked', 'a2')
  })

  it('on a phone the drawer opens on the chosen tab, a question or not: it never opened by itself', () => {
    viewport(390)
    localStorage.setItem(RAIL_TAB_KEY, 'history')
    renderPage()
    askNow(useSessionStore.getState().sessions[0].id)
    expect(rail()).toBeNull()
    fireEvent.click(screen.getByTestId('rail-toggle'))
    expect(tab('History')).toHaveAttribute('aria-selected', 'true')
  })

  it('on a phone a chip closes the drawer first', async () => {
    viewport(390)
    localStorage.setItem(RAIL_TAB_KEY, 'history')
    renderPage()
    fireEvent.click(screen.getByTestId('rail-toggle'))
    await within(rail()!).findByTestId('reel-list')
    const done = within(rail()!).getAllByTestId('reel-card')[1]
    fireEvent.click(within(done).getByTestId('reel-head'))
    fireEvent.click(within(done).getAllByTestId('reel-sum-file')[0])
    expect(rail()).toBeNull()
    expect(screen.getByTestId('chat-stub')).toHaveAttribute('data-linked', 'a2')
  })
})
