// The session page's side panel in real Chromium: whether the toggle fits a phone's header, the
// drawer opens over the whole height and closes, and nothing is wider than the screen; and on a
// desktop, that the panel is a column beside the chat that the chat and its composer give way to.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import { MemoryRouter, Route, Routes, useNavigate } from 'react-router-dom'
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
import { useTranscriptStore } from '@blerglab/chat'
import { useSessionStore } from '../hooks/useSessionStore'

const PHONE = { width: 390, height: 700 }
// The toggle's counts as read: each count's text, its mark included (the subagent robot face, ↳).
const shownCounts = (toggle: HTMLElement) => [...toggle.querySelectorAll<HTMLElement>('.icon-btn-count')].map(el => el.textContent).join('')
const DESKTOP = { width: 1280, height: 800 }

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
  vi.stubGlobal('fetch', vi.fn((url: string) => json(String(url).endsWith('/artifacts') ? { artifacts: FILES } : [])))
})
afterEach(() => {
  vi.unstubAllGlobals()
  localStorage.removeItem(RAIL_PREF_KEY)
  localStorage.removeItem(RAIL_TAB_KEY)
})

function renderPage() {
  render(
    <div style={{ height: '100vh' }}>
      <MemoryRouter initialEntries={['/sessions/sess-a']}>
        <Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes>
      </MemoryRouter>
    </div>,
  )
}

const noOverflow = (width: number) => {
  expect(document.documentElement.clientWidth).toBe(width)
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
}

describe('the session panel on a phone (390 px)', () => {
  beforeEach(async () => { await page.viewport(PHONE.width, PHONE.height) })

  it('the toggle fits the header with its count, and the page is no wider than the screen', async () => {
    renderPage()
    const toggle = screen.getByTestId('rail-toggle')
    // 1 open ask + 1 tool change need you (not a toggle count); 2 subagents (A1 at work in a running turn, A2 in the
    // background, its task counted once) + 5 background tasks (3 the page holds, 2 that started
    // before them) run inside the session, from the server's live state. Main is not a count.
    await waitFor(() => expect(shownCounts(toggle)).toBe('🤖7'))
    expect(screen.getByTestId('rail-announce').textContent).toBe('Session panel: 7 subagents and background tasks')
    for (const count of toggle.querySelectorAll('.icon-btn-count')) expect(count.getBoundingClientRect().right).toBeLessThanOrEqual(PHONE.width)
    const r = toggle.getBoundingClientRect()
    expect(r.left).toBeGreaterThanOrEqual(0)
    expect(r.right).toBeLessThanOrEqual(PHONE.width)
    const header = toggle.parentElement!
    expect(header.scrollWidth).toBeLessThanOrEqual(header.clientWidth)
    // Closed, there is no drawer, and the chat has the whole width.
    expect(screen.queryByTestId('session-rail')).toBeNull()
    expect(screen.getByTestId('agent-chat').getBoundingClientRect().width).toBeGreaterThan(PHONE.width - 2)
    noOverflow(PHONE.width)
  })

  it('opens a full-height drawer from the toggle that Escape and the backdrop close', async () => {
    renderPage()
    const toggle = screen.getByTestId('rail-toggle')
    await userEvent.click(toggle)
    const drawer = await screen.findByRole('dialog', { name: 'Session panel' })
    // It slides in from the right edge; measured once it is in place.
    await waitFor(() => expect(drawer.getBoundingClientRect().right).toBe(PHONE.width))
    const d = drawer.getBoundingClientRect()
    expect(d.top).toBe(0)
    expect(d.bottom).toBe(PHONE.height)
    expect(d.left).toBeGreaterThan(0)
    expect(d.right).toBe(PHONE.width)
    noOverflow(PHONE.width)
    // Every row stays inside the drawer; the list scrolls inside it, not the page.
    for (const row of drawer.querySelectorAll('.rail-item')) {
      const rr = row.getBoundingClientRect()
      expect(rr.left).toBeGreaterThanOrEqual(d.left)
      expect(rr.right).toBeLessThanOrEqual(d.right)
    }
    const body = drawer.querySelector('.rail-body') as HTMLElement
    expect(body.scrollHeight).toBeGreaterThan(body.clientHeight)
    expect(getComputedStyle(document.body).overflow).toBe('hidden')

    await userEvent.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByTestId('session-rail')).toBeNull())
    expect(document.activeElement).toBe(toggle)
    expect(document.body.style.overflow).toBe('')

    await userEvent.click(toggle)
    await screen.findByRole('dialog', { name: 'Session panel' })
    // The strip of page left of the drawer is the backdrop.
    await userEvent.click(screen.getByTestId('session-rail-backdrop'), { position: { x: 10, y: 300 } })
    await waitFor(() => expect(screen.queryByTestId('session-rail')).toBeNull())
  })

  it('shows History in the drawer: cards as wide as the list, the opened topics\' chips wrapping inside them', async () => {
    localStorage.setItem(RAIL_TAB_KEY, 'history')
    renderPage()
    await userEvent.click(screen.getByTestId('rail-toggle'))
    const drawer = await screen.findByRole('dialog', { name: 'Session panel' })
    await waitFor(() => expect(drawer.getBoundingClientRect().right).toBe(PHONE.width))
    const list = await screen.findByTestId('reel-list')
    for (const head of screen.getAllByTestId('reel-head')) await userEvent.click(head)
    const pad = getComputedStyle(list)
    const inner = list.getBoundingClientRect().width - parseFloat(pad.paddingLeft) - parseFloat(pad.paddingRight)
    for (const card of screen.getAllByTestId('reel-card')) expect(Math.round(card.getBoundingClientRect().width)).toBe(Math.round(inner))
    const chips = drawer.querySelectorAll('.reel-summary .reel-chip')
    expect(chips.length).toBeGreaterThan(0)
    for (const chip of chips) {
      const c = chip.getBoundingClientRect()
      expect(c.right).toBeLessThanOrEqual(drawer.getBoundingClientRect().right)
    }
    noOverflow(PHONE.width)
  })

  it('shows Files in the drawer: every row and its buttons inside it, the page no wider than the screen', async () => {
    localStorage.setItem(RAIL_TAB_KEY, 'files')
    renderPage()
    // The chat's toolbar has no Files or Skills & plugins button.
    expect(screen.queryByTestId('files-toggle')).toBeNull()
    expect(screen.queryByTestId('capabilities-toggle')).toBeNull()
    await userEvent.click(screen.getByTestId('rail-toggle'))
    const drawer = await screen.findByRole('dialog', { name: 'Session panel' })
    await waitFor(() => expect(drawer.getBoundingClientRect().right).toBe(PHONE.width))
    const rows = await screen.findAllByTestId('artifact-row')
    expect(rows).toHaveLength(2)
    await userEvent.click(screen.getByRole('button', { name: /Earlier versions/ }))
    const d = drawer.getBoundingClientRect()
    for (const el of drawer.querySelectorAll('.artifact-row:not([hidden]), .artifact-version-row:not([hidden]), .artifact-btn')) {
      const r = el.getBoundingClientRect()
      if (!r.width) continue // Collapsed version rows retain their buttons in the DOM.
      expect(r.left).toBeGreaterThanOrEqual(d.left)
      expect(r.right).toBeLessThanOrEqual(d.right)
    }
    // The four tabs fit the drawer's width on one line.
    const tabs = within(drawer).getAllByRole('tab')
    expect(tabs.map(t => t.id.split('-tab-')[1])).toEqual(['live', 'history', 'files', 'capabilities'])
    expect(new Set(tabs.map(t => Math.round(t.getBoundingClientRect().top))).size).toBe(1)
    expect(tabs.at(-1)!.getBoundingClientRect().right).toBeLessThanOrEqual(d.right)
    noOverflow(PHONE.width)
  })
})

describe('the session panel on a desktop (1280 px)', () => {
  beforeEach(async () => { await page.viewport(DESKTOP.width, DESKTOP.height) })

  it('is a column beside the chat, the chat and its composer give way, and the toggle hides it', async () => {
    renderPage()
    const rail = screen.getByTestId('session-rail')
    // With no choice made, the panel is open.
    await waitFor(() => expect(rail.getBoundingClientRect().width).toBeGreaterThan(0))
    const r = rail.getBoundingClientRect()
    expect(r.width).toBeGreaterThanOrEqual(320)
    expect(r.width).toBeLessThanOrEqual(360)
    expect(r.right).toBe(DESKTOP.width)
    expect(r.bottom).toBe(DESKTOP.height)
    const chat = screen.getByTestId('agent-chat').getBoundingClientRect()
    expect(chat.right).toBeLessThanOrEqual(r.left)
    const composer = screen.getByPlaceholderText('Answer the agent’s question…').getBoundingClientRect()
    expect(composer.right).toBeLessThanOrEqual(r.left)
    expect(composer.left).toBeGreaterThanOrEqual(chat.left)
    noOverflow(DESKTOP.width)

    await userEvent.click(screen.getByTestId('rail-toggle'))
    await waitFor(() => expect(rail.getBoundingClientRect().width).toBe(0))
    const tools = screen.getByRole('navigation', { name: 'Workspace' }).getBoundingClientRect()
    const expandedChat = screen.getByTestId('agent-chat').getBoundingClientRect()
    expect(expandedChat.left).toBe(tools.left)
    expect(screen.queryByRole('toolbar', { name: 'Session panel tools' })).toBeNull()
    const panelRail = { width: 0 }
    expect(panelRail.width).toBe(0)
    expect(expandedChat.width).toBeGreaterThan(DESKTOP.width - panelRail.width - 2)
    expect(localStorage.getItem(RAIL_PREF_KEY)).toBe('0')
  })

  it('Capabilities fills the panel’s column, its filter and groups inside it; Session closes Live', async () => {
    renderPage()
    const rail = screen.getByTestId('session-rail')
    await waitFor(() => expect(rail.getBoundingClientRect().width).toBeGreaterThan(0))
    const r = rail.getBoundingClientRect()
    const settings = screen.getByTestId('rail-session')
    expect(settings.getBoundingClientRect().right).toBeLessThanOrEqual(r.right)
    await userEvent.click(screen.getByRole('tab', { name: /^Capabilities/ }))
    await screen.findByText('release-notes')
    const search = screen.getByRole('searchbox', { name: 'Filter skills, plugins and tools' }).getBoundingClientRect()
    expect(search.left).toBeGreaterThanOrEqual(r.left)
    expect(search.right).toBeLessThanOrEqual(r.right)
    for (const g of screen.getAllByTestId(/^caps-group-/)) expect(g.getBoundingClientRect().right).toBeLessThanOrEqual(r.right)
    noOverflow(DESKTOP.width)
  })
})


describe('session page long-content width', () => {
  it.each([390, 1280, 2048])('contains unbroken messages and code at %ipx', async width => {
    await page.viewport(width, 800)
    const transcript = useTranscriptStore.getState()
    transcript.ingest({type: 'agent_event', session_id: 'sess-a', client_event_id: 'long-content', seq: 1000, ts: new Date().toISOString(), kind: 'assistant_text', payload: {text: 'Long identifier: ' + 'x'.repeat(2000) + '\n\n```text\n' + 'y'.repeat(2000) + '\n```'}})
    transcript.ingest({type: 'agent_event', session_id: 'sess-a', client_event_id: 'long-checklist', seq: 1001, ts: new Date().toISOString(), kind: 'checklist', payload: {op: 'set', items: [{text: 'Long checklist ' + 'w'.repeat(180), done: false}]}})
    transcript.ingest({type: 'agent_event', session_id: 'sess-a', client_event_id: 'long-command', seq: 1002, ts: new Date().toISOString(), kind: 'tool_call', payload: {tool: 'Bash', call_id: 'long-command', input: {command: 'printf ' + 'z'.repeat(2000)}}})
    transcript.ingest({type: 'agent_event', session_id: 'sess-a', client_event_id: 'long-artifact', seq: 1003, ts: new Date().toISOString(), kind: 'artifact', payload: {id: 'long-artifact', name: 'artifact-' + 'f'.repeat(180) + '.txt', size: 20, content_type: 'text/plain', view: 'text', version: 1}})
    renderPage()
    await waitFor(() => expect(screen.getByText(/Long identifier:/)).not.toBeNull())
    const shell = document.querySelector<HTMLButtonElement>('[data-call-id="long-command"] button')!
    await userEvent.click(shell)
    noOverflow(width)
    const timeline = document.querySelector<HTMLElement>('.agent-timeline')!
    expect(timeline.scrollWidth).toBeLessThanOrEqual(timeline.clientWidth)
    const overflow = [...document.querySelectorAll<HTMLElement>('body *')].filter(el => el.clientWidth > 0 && el.getBoundingClientRect().right > width + 1 && getComputedStyle(el).position !== 'fixed').map(el => el.className)
    expect(document.documentElement.scrollWidth, JSON.stringify(overflow)).toBeLessThanOrEqual(width)
  })
})

it.each([[390,false],[1280,false],[390,true],[1280,true]] as const)('opens child approval from an allowed card at width %i, delayed %s', async (width, delayed) => {
  await page.viewport(width, 800)
  localStorage.setItem(RAIL_PREF_KEY, 'false')
  localStorage.setItem(RAIL_TAB_KEY, 'history')
  const base = apiFetch.getMockImplementation()!
  const runtime = (runtime: string) => ({runtime, enabled:false, partial:false, can_enable:true, reason:'', allowed_engines:['claude','codex','hermes'],granted_engines:[],standing_rule_engines:[],effective_allowed_engines:[]})
  let finishLoad!: () => void
  const approvalReply = () => json({revision:'revision-a',daemon_id:'desktop-a',allowed_engines:['claude','codex','hermes'],runtimes:[runtime('docker'),runtime('daemon')]})
  apiFetch.mockImplementation((url: string, ...rest: unknown[]) => url.endsWith('/permissions') ? json({can_grant:true,reason:'',grants:[],mcp_tools:[]}) : url.endsWith('/child-approval')
    ? delayed ? new Promise(resolve => { finishLoad = () => resolve(approvalReply()) }) : approvalReply()
    : base(url,...rest))
  for (const event of [
    {seq:100,kind:'delegation_request',payload:{request_id:'request-a',engine:'hermes',runtime:'docker',daemon_name:'Desktop',title:'Review fixture',prompt:'Review code',offered:['once','deny'],expires_at:'2099-01-01T00:00:00Z'}},
    {seq:101,kind:'delegation_decided',payload:{request_id:'request-a',status:'allowed',choice:'once'}},
  ]) useTranscriptStore.getState().ingest({...event,session_id:'sess-a',client_event_id:`child-approval-${event.seq}`,ts:new Date().toISOString()} as import('@blerglab/chat').AgentEvent)
  renderPage()
  await userEvent.click(await screen.findByRole('button',{name:'Change child approvals'}))
  // The Permissions modal opens at the children, and the first child toggle takes the focus once loaded.
  const dialog=await screen.findByRole('dialog',{name:'Permissions'})
  if (delayed) {
    await waitFor(()=>expect(document.activeElement).toBe(dialog))
    finishLoad()
  }
  const checkbox=await within(dialog).findByRole('checkbox',{name:'Start sandboxed children without asking'})
  await waitFor(()=>expect(document.activeElement).toBe(checkbox))
  expect(screen.getByRole('tab',{name:/Live/})).toHaveAttribute('aria-selected','true')
  const box=checkbox.getBoundingClientRect()
  expect(box.top).toBeGreaterThanOrEqual(0)
  expect(box.bottom).toBeLessThanOrEqual(800)
  noOverflow(width)
})

it('does not reopen the Permissions modal by itself after it was closed, across a session switch and back', async () => {
  await page.viewport(1280, 800)
  const base = apiFetch.getMockImplementation()!
  const runtime = (runtime: string) => ({runtime, enabled:false, partial:false, can_enable:true, reason:'', allowed_engines:['claude'],granted_engines:[],standing_rule_engines:[],effective_allowed_engines:[]})
  apiFetch.mockImplementation((url: string, ...rest: unknown[]) => url.endsWith('/permissions') ? json({can_grant:true,reason:'',grants:[],mcp_tools:[]})
    : url.endsWith('/child-approval') ? json({revision:'revision-a',daemon_id:'desktop-a',allowed_engines:['claude'],runtimes:[runtime('docker')]})
      : base(url,...rest))
  useSessionStore.setState(s => ({ sessions: [...s.sessions, { ...s.sessions[0], id: 'sess-b', name: 'Another session' }] }))
  for (const event of [
    {seq:100,kind:'delegation_request',payload:{request_id:'request-a',engine:'claude',runtime:'docker',daemon_name:'Desktop',title:'Review fixture',prompt:'Review code',offered:['once','deny'],expires_at:'2099-01-01T00:00:00Z'}},
    {seq:101,kind:'delegation_decided',payload:{request_id:'request-a',status:'allowed',choice:'once'}},
  ]) useTranscriptStore.getState().ingest({...event,session_id:'sess-a',client_event_id:`switch-${event.seq}`,ts:new Date().toISOString()} as import('@blerglab/chat').AgentEvent)
  function Nav() {
    const go = useNavigate()
    return <><button type="button" onClick={() => go('/sessions/sess-b')}>to B</button><button type="button" onClick={() => go('/sessions/sess-a')}>to A</button></>
  }
  render(
    <div style={{ height: '100vh' }}>
      <MemoryRouter initialEntries={['/sessions/sess-a']}>
        <Nav />
        <Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes>
      </MemoryRouter>
    </div>,
  )
  await userEvent.click(await screen.findByRole('button',{name:'Change child approvals'}))
  const dialog = await screen.findByRole('dialog',{name:'Permissions'})
  await userEvent.click(within(dialog).getByRole('button',{name:'Cancel'}))
  expect(screen.queryByRole('dialog')).toBeNull()
  await userEvent.click(screen.getByRole('button',{name:'to B'}))
  await userEvent.click(screen.getByRole('button',{name:'to A'}))
  await screen.findByRole('button',{name:'Manage'})
  await new Promise(resolve => setTimeout(resolve, 300))
  expect(screen.queryByRole('dialog')).toBeNull()
  // The card still opens it on purpose.
  await userEvent.click(await screen.findByRole('button',{name:'Change child approvals'}))
  expect(await screen.findByRole('dialog',{name:'Permissions'})).toBeInTheDocument()
})

// The plan nudge setting (card #158) sits in the panel's Session section and stays inside it, on a
// phone's drawer and on a 4K screen alike.
it.each([390, 3840])('shows the plan nudge setting inside the panel at %ipx', async width => {
  await page.viewport(width, 900)
  const fallback = apiFetch.getMockImplementation()!
  apiFetch.mockImplementation((url: string) => url.endsWith('/plan-nudges') ? json({ enabled: true }) : fallback(url))
  renderPage()
  if (width < 700) await userEvent.click(screen.getByTestId('rail-toggle'))
  const section = await screen.findByTestId('rail-session')
  const box = await within(section).findByRole('checkbox', { name: 'Nudge when the plan drifts' })
  expect(box).toBeChecked()
  const label = box.closest('label')!.getBoundingClientRect()
  const panel = screen.getByTestId('session-rail').getBoundingClientRect()
  expect(label.left).toBeGreaterThanOrEqual(panel.left)
  expect(label.right).toBeLessThanOrEqual(panel.right)
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
})

// Card #233 part 5: the toggle shows the work inside the session (🤖) and the child sessions at work
// (↳) as two counts, each hidden at zero, from the selector Running now lists with.
describe('the panel toggle counts work inside and child sessions apart', () => {
  const child = (id: string, engine: string) => ({ id, title: `Worker ${id}`, engine, model: 'fixture', status: 'running', started_at: new Date().toISOString(), ended_at: null, asking: false, tokens: null })
  const cases = [
    ['zero', { subagents: false, tasks: false, children: 0 }, '', 'Session panel'],
    ['children only', { subagents: false, tasks: false, children: 1 }, '↳1', 'Session panel: 1 child session'],
    ['both', { subagents: true, tasks: true, children: 4 }, '🤖7↳4', 'Session panel: 7 subagents and background tasks · 4 child sessions'],
  ] as const
  for (const width of [390, 3840]) {
    it.each(cases)(`at ${width} px: %s`, async (_name, o, shown, label) => {
      await page.viewport(width, width === 390 ? 844 : 2160)
      localStorage.setItem(RAIL_PREF_KEY, '0')
      const { live } = seedRichRail()
      const reply = { ...live, through_seq: 999, asks: [], tool_changes: 0, subagents: o.subagents ? live.subagents : [], tasks: o.tasks ? live.tasks : [],
        children: Array.from({ length: o.children }, (_, i) => child(`c${i}`, i % 2 ? 'codex' : 'claude')) }
      apiFetch.mockImplementation((url: string) => json(url.endsWith('/live') ? reply : url.endsWith('/topics') ? reelReply() : {}))
      useTranscriptStore.setState({ sessions: {} })
      renderPage()
      const toggle = screen.getByTestId('rail-toggle')
      await waitFor(() => expect(toggle).toHaveAccessibleName(label))
      expect(shownCounts(toggle)).toBe(shown)
      expect(toggle.querySelectorAll('.icon-btn-count')).toHaveLength(shown ? shown.split(/(?=🤖|↳)/u).length : 0)
      const r = toggle.getBoundingClientRect()
      expect(r.right).toBeLessThanOrEqual(width)
      for (const count of toggle.querySelectorAll('.icon-btn-count')) {
        const c = count.getBoundingClientRect()
        expect(c.right).toBeLessThanOrEqual(r.right + 0.5)
        expect(Math.abs(c.top + c.height / 2 - (r.top + r.height / 2))).toBeLessThan(3)
      }
      const shots = import.meta.env.VITE_CHROME_SHOTS_DIR
      if (shots && _name === 'both') await page.screenshot({ path: `${shots}/panel-toggle-${width}.png` })
      // Open, Running now lists the same numbers (Main is listed on its own).
      await userEvent.click(toggle)
      const running = await screen.findByTestId('rail-running')
      expect(within(running).queryAllByTestId('rail-subagent').length + within(running).queryAllByTestId('rail-task').length).toBe(o.subagents ? 7 : 0)
      expect(within(running).queryAllByTestId('rail-child')).toHaveLength(o.children)
      expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
    })
  }
})
