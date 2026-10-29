// The app outer scroller, sidebar and session together: long content and native background-task
// waiting banners stay bounded before and after opening a diff, including keyboard task details.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import '../index.css'
import { seedRichRail } from '../test/railFixture'
import { reelReply } from '../test/reelFixture'
import { RAIL_PREF_KEY, RAIL_TAB_KEY } from '../lib/sessionRail'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async (orig) => ({
  ...(await orig<typeof import('../authClient')>()),
  getAccessToken: () => 'tok',
  // Never let a test navigate the test page itself.
  redirectToRefresh: vi.fn(),
}))
// The chat's transport reads the files with that token, taken as fresh.
vi.mock('../claims', async (orig) => ({ ...(await orig<typeof import('../claims')>()), tokenExpired: () => false }))
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))

import SessionDetail from './SessionDetail'
import SessionList from './SessionList'
import { useSessionStore } from '../hooks/useSessionStore'
import { useTranscriptStore } from '@blerglab/chat'


const PATCH = 'diff --git a/fixture.txt b/fixture.txt\n--- a/fixture.txt\n+++ b/fixture.txt\n@@ -1 +1 @@\n-old\n+' + 'n'.repeat(2000) + '\n'
const FILES = [
  { id: 'f2', name: 'a-rather-long-file-name-for-the-release-plan-of-the-session-store.md', size: 6144, content_type: 'text/markdown', view: 'markdown', version: 2, latest_version: 2 },
  { id: 'f1', name: 'a-rather-long-file-name-for-the-release-plan-of-the-session-store.md', size: 4096, content_type: 'text/markdown', view: 'markdown', version: 1, latest_version: 2 },
  { id: 'f3', name: 'coverage.csv', size: 18432, content_type: 'text/csv', view: 'csv', version: 1, latest_version: 1 },
]
const CAPS = {
  seq: 3, ts: '2026-10-15T10:00:00Z',
  capabilities: { engine: 'claude', groups: [{ id: 'skills', label: 'Skills', items: [{ name: 'release-notes', description: 'Writes release notes from the merged pull requests since the last tag.' }] }] },
}

const json = (body: unknown) => Promise.resolve(new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } }))

beforeEach(() => {
  localStorage.removeItem(RAIL_PREF_KEY)
  localStorage.removeItem(RAIL_TAB_KEY)
  const { live } = seedRichRail()
  apiFetch.mockImplementation((url: string) => json(
    url.startsWith('/api/tool-changes') ? { tool_changes: [{ id: 't1', kind: 'changed', muted: false }] }
      : url.endsWith('/topics') ? reelReply()
        : url.endsWith('/live') ? live
          : url.endsWith('/capabilities') ? CAPS
            : {},
  ))
  vi.stubGlobal('fetch', vi.fn((url: string) => String(url).endsWith('/raw') ? Promise.resolve(new Response(PATCH)) : json(String(url).endsWith('/artifacts') ? { artifacts: FILES } : [])))
})
afterEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
  localStorage.removeItem(RAIL_PREF_KEY)
  localStorage.removeItem(RAIL_TAB_KEY)
})

// Match App.Layout's desktop flex ancestors, including the main pane's own scroller.
function renderPage() {
  render(<MemoryRouter initialEntries={['/sessions/sess-a']}>
    <div style={{display: 'flex', flexDirection: 'column', height: '100vh', overflow: 'hidden'}}>
      <div style={{display: 'flex', flexDirection: 'row', flex: 1, minHeight: 0}}>
        {innerWidth >= 900 && <SessionList variant="sidebar" />}
        <div data-testid="app-main" style={{flex: 1, minWidth: 0, overflow: 'auto'}}>
          <Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes>
        </div>
      </div>
    </div>
  </MemoryRouter>)
}

const noOverflow = (width: number) => {
  expect(document.documentElement.clientWidth).toBe(width)
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
}

describe('session page long-content width', () => {
  it.each([390, 1280, 2048])('contains unbroken messages and code at %ipx', async width => {
    await page.viewport(width, 800)
    const transcript = useTranscriptStore.getState()
    transcript.ingest({type: 'agent_event', session_id: 'sess-a', client_event_id: 'long-content', seq: 1000, ts: new Date().toISOString(), kind: 'assistant_text', payload: {text: 'Long identifier: ' + 'x'.repeat(2000) + '\n\n```text\n' + 'y'.repeat(2000) + '\n```'}})
    transcript.ingest({type: 'agent_event', session_id: 'sess-a', client_event_id: 'long-checklist', seq: 1001, ts: new Date().toISOString(), kind: 'checklist', payload: {op: 'set', items: [{text: 'Long checklist ' + 'w'.repeat(180), done: false}]}})
    transcript.ingest({type: 'agent_event', session_id: 'sess-a', client_event_id: 'long-command', seq: 1002, ts: new Date().toISOString(), kind: 'tool_call', payload: {tool: 'Bash', call_id: 'long-command', input: {command: 'printf ' + 'z'.repeat(2000)}}})
    transcript.ingest({type: 'agent_event', session_id: 'sess-a', client_event_id: 'long-artifact', seq: 1003, ts: new Date().toISOString(), kind: 'artifact', payload: {id: 'long-artifact', name: 'artifact-' + 'f'.repeat(180) + '.txt', size: 20, content_type: 'text/plain', view: 'text', version: 1}})
    transcript.ingest({type: 'agent_event', session_id: 'sess-a', client_event_id: 'diff-artifact', seq: 1004, ts: new Date().toISOString(), kind: 'artifact', payload: {id: 'diff-artifact', name: 'changes.patch', size: PATCH.length, content_type: 'text/x-diff', view: 'diff', version: 1}})
    renderPage()
    await waitFor(() => expect(screen.getByText(/Long identifier:/)).not.toBeNull())
    const shell = document.querySelector<HTMLButtonElement>('[data-call-id="long-command"] button')!
    await userEvent.click(shell)
    const card = screen.getAllByTestId('artifact-card').find(el => el.textContent?.includes('changes.patch'))!
    await userEvent.click(within(card).getByRole('button', {name: /^View$/}))
    await waitFor(() => expect(document.querySelector('.diff-main')).not.toBeNull())
    await userEvent.keyboard('{Escape}')
    await waitFor(() => expect(document.querySelector('.artifact-viewer')).not.toBeVisible())
    noOverflow(width)
    const scrollers = [...document.querySelectorAll<HTMLElement>('body *')].filter(el => /^(auto|scroll)$/.test(getComputedStyle(el).overflowX) && el.clientWidth > 0 && !el.matches('pre, .code-wrap, .rail-tabs, .tcg-out, .md-code-scroll, .attach-chips, .diff-table-wrap'))
    const wide = scrollers.filter(el => el.scrollWidth > el.clientWidth + 1).map(el => ({tag: el.tagName, className: el.className, width: el.clientWidth, scroll: el.scrollWidth}))
    expect(wide).toEqual([])
    const main = screen.getByTestId('app-main')
    expect(main.scrollWidth).toBeLessThanOrEqual(main.clientWidth)
    const timeline = document.querySelector<HTMLElement>('.agent-timeline')!
    expect(timeline.scrollWidth).toBeLessThanOrEqual(timeline.clientWidth)
    const overflow = [...document.querySelectorAll<HTMLElement>('body *')].filter(el => el.clientWidth > 0 && el.getBoundingClientRect().right > width + 1 && getComputedStyle(el).position !== 'fixed').map(el => el.className)
    expect(document.documentElement.scrollWidth, JSON.stringify(overflow)).toBeLessThanOrEqual(width)
  })
})


describe('native background command waiting banner', () => {
  it.each([{width:390,reduced:false},{width:1280,reduced:false},{width:2048,reduced:false},{width:390,reduced:true}])('keeps the summary compact and expands scrollable details by keyboard at $width px (reduced motion $reduced)', async ({width,reduced}) => {
    if (reduced) {
      const original = window.matchMedia.bind(window)
      vi.spyOn(window, 'matchMedia').mockImplementation(query => query === '(prefers-reduced-motion: reduce)' ? {matches: true, media: query, onchange: null, addListener() {}, removeListener() {}, addEventListener() {}, removeEventListener() {}, dispatchEvent: () => true} : original(query))
    }
    await page.viewport(width, 800)
    const command = "cat >> src/fixture.test.ts <<'EOF'\n" + ('const value = "' + 'x'.repeat(500) + '"\n').repeat(100) + 'EOF'
    useTranscriptStore.setState({sessions: {}})
    const transcript = useTranscriptStore.getState()
    let seq = 0
    const event = (kind: 'user_message' | 'background_task_started' | 'turn_done', payload: unknown) => transcript.ingest({type: 'agent_event', session_id: 'sess-a', client_event_id: `waiting-${++seq}`, seq, ts: new Date(Date.now() - 5000).toISOString(), kind, payload})
    event('user_message', {text: 'Add a test', source: 'chat'})
    event('background_task_started', {task_id: 'waiting-command', call_id: 'waiting-call', kind: 'bash', description: command})
    event('turn_done', {stop_reason: 'end_turn', model: 'fixture', usage: {}})
    transcript.ingestReplayDone('sess-a', {lastSeq: seq, hasMore: false})
    useSessionStore.setState(state => ({sessions: state.sessions.map(session => ({...session, status: 'idle' as const, background_tasks: 1}))}))
    renderPage()
    const banner = await screen.findByTestId('working')
    expect(banner.getBoundingClientRect().height).toBeLessThan(100)
    if (reduced) expect(getComputedStyle(banner.querySelector('.agent-dots i')!).animationName).toBe('none')
    expect(within(banner).getByTestId('working-words').textContent).toBe('Waiting on a background task')
    const details = banner.querySelector('details')!
    const summary = details.querySelector('summary')!
    summary.focus()
    await userEvent.keyboard('{Enter}')
    await waitFor(() => expect(details.open).toBe(true))
    const code = details.querySelector('pre')!
    expect(code.textContent).toBe(command)
    expect(code.scrollHeight).toBeGreaterThan(code.clientHeight)
    expect(code.scrollWidth).toBeGreaterThan(code.clientWidth)
    expect(code.getBoundingClientRect().height).toBeLessThanOrEqual(193)
    expect(banner.getBoundingClientRect().right).toBeLessThanOrEqual(width)
    noOverflow(width)
    expect(screen.getByTestId('app-main').scrollWidth).toBeLessThanOrEqual(screen.getByTestId('app-main').clientWidth)
    await userEvent.keyboard('{Enter}')
    await waitFor(() => expect(details.open).toBe(false))
    expect(document.activeElement).toBe(summary)
    expect(banner.getBoundingClientRect().height).toBeLessThan(100)
  })
})

describe('session list search spacing', () => {
  // The search box sits between the header's separator line and the first session card; the owner
  // asked for the same visible space on both sides (the smaller of the two).
  it('leaves the same space above and below the search box', async () => {
    await page.viewport(1440, 900)
    renderPage()
    const input = await screen.findByTestId('search-input')
    const header = input.parentElement!.previousElementSibling as HTMLElement
    const list = await screen.findByTestId('session-list')
    const firstCard = [...list.querySelectorAll<HTMLElement>('*')].find(el => getComputedStyle(el).borderTopWidth !== '0px' && el.getBoundingClientRect().height > 20)!
    const box = input.getBoundingClientRect()
    const above = box.top - header.getBoundingClientRect().bottom
    const below = firstCard.getBoundingClientRect().top - box.bottom
    console.log(`search gap above=${above} below=${below}`)
    expect(Math.abs(above - below)).toBeLessThanOrEqual(1)
  })
})
