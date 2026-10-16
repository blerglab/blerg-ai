// The session panel's Files and Capabilities tabs and its Settings section, and the toolbar they leave
// lean: the chat's toolbar keeps the model, effort, Rules and Stop; the Files and Skills & plugins
// dialogs are gone; Show thinking and Compact tool calls are the panel's, kept per browser.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act, render, screen, fireEvent, waitFor, within } from '@testing-library/react'
import { MemoryRouter, Routes, Route } from 'react-router-dom'
import { COMPACT_KEY, useTranscriptStore, type AgentEvent } from '@blerglab/chat'
import SessionDetail from './SessionDetail'
import { useSessionStore } from '../hooks/useSessionStore'
import * as wsMock from '../test/wsMock'
import { makeDaemon, makeSession } from '../test/fixtures'
import { RAIL_PREF_KEY, RAIL_TAB_KEY, THINKING_KEY, readRailTab } from '../lib/sessionRail'
import type { SessionInfo } from '../types'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async (orig) => ({
  ...(await orig<typeof import('../authClient')>()),
  getAccessToken: () => 'tok',
}))
// The token the chat's transport sends is taken as fresh (it is not a real JWT).
vi.mock('../claims', async (orig) => ({
  ...(await orig<typeof import('../claims')>()),
  tokenExpired: () => false,
}))

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

const report = { id: 'a1', name: 'report.md', size: 2048, content_type: 'text/markdown', view: 'markdown', created_at: '2026-10-15T10:00:00Z' }
const notes = { id: 'a2', name: 'notes.txt', size: 12, content_type: 'text/plain', view: 'text', created_at: '2026-10-15T09:00:00Z' }
const caps = {
  capabilities: { engine: 'claude', engine_name: 'Claude Code', groups: [{ id: 'skills', label: 'Skills', items: [{ name: 'release-notes', description: 'Writes release notes.' }] }] },
  seq: 3, ts: '2026-10-15T10:00:00Z',
}

let files: typeof report[] = []
let calls: string[] = []
// The server's GET /live answer, or null for a server without the route.
let liveAnswer: unknown = null
const json = (body: unknown) => Promise.resolve(new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } }))
function stubFetch() {
  calls = []
  vi.stubGlobal('fetch', vi.fn((url: string, init?: RequestInit) => {
    const u = String(url)
    calls.push(`${init?.method ?? 'GET'} ${u}`)
    if (u.endsWith('/artifacts')) return json({ artifacts: files })
    if (/\/artifacts\/[^/]+$/.test(u) && init?.method === 'DELETE') {
      files = files.filter(f => !u.endsWith(`/${f.id}`))
      return Promise.resolve(new Response(null, { status: 204 }))
    }
    if (u.endsWith('/raw')) return Promise.resolve(new Response('# Report', { status: 200, headers: { 'Content-Type': 'text/markdown' } }))
    if (u.includes('/capabilities')) return json(caps)
    if (u.includes('/live')) return liveAnswer ? json(liveAnswer) : Promise.resolve(new Response('no', { status: 404, headers: { 'Content-Type': 'text/plain' } }))
    return json({})
  }))
}
const reads = (part: string) => calls.filter(c => c.startsWith('GET') && c.includes(part)).length

let seq = 0
function ev(kind: string, payload: unknown): AgentEvent {
  seq++
  return { session_id: 'sess-a', client_event_id: `t${seq}`, seq, ts: new Date().toISOString(), kind, payload } as AgentEvent
}
// Three finished Bash calls: folded into one summary row while Compact tool calls is on.
const bashRun = () => [1, 2, 3].flatMap(i => [
  ev('tool_call', { tool: 'Bash', call_id: `c${i}`, input: { command: `echo ${i}` } }),
  ev('tool_result', { call_id: `c${i}`, output: 'ok', is_error: false, duration_ms: 1 }),
])

function seed(over: Partial<SessionInfo> = {}, ...events: AgentEvent[]) {
  const session = makeSession({ kind: 'agent', status: 'idle', title: 'Ship it', runtime: 'docker', ...over })
  useSessionStore.setState({ daemons: [makeDaemon()], sessions: [session], statusChangedAt: { [session.id]: Date.now() }, serverVersion: 'test' })
  useTranscriptStore.setState({ sessions: {} })
  const s = useTranscriptStore.getState()
  s.ingest(ev('user_message', { text: 'go', source: 'chat' }))
  for (const e of events) s.ingest(e)
  s.ingestReplayDone(session.id, { lastSeq: seq, hasMore: false })
}

async function renderPage() {
  const r = render(
    <MemoryRouter initialEntries={['/sessions/sess-a']}>
      <Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes>
    </MemoryRouter>,
  )
  await act(async () => { for (let k = 0; k < 10; k++) await Promise.resolve() })
  return r
}

const rail = () => screen.getByTestId('session-rail')
const tab = (name: string) => within(rail()).getByRole('tab', { name: ['Live', 'Files', 'Capabilities'].includes(name) ? new RegExp(`^${name}`) : name })
const toolbar = () => screen.getByTestId('agent-chat').querySelector('.agent-toolbar') as HTMLElement

describe('the session panel: Files, Capabilities and Settings', () => {
  beforeEach(() => {
    seq = 0
    files = [report, notes]
    liveAnswer = null
    wsMock.resetWsMock()
    wsMock.send.mockReturnValue(true)
    localStorage.removeItem(RAIL_TAB_KEY)
    localStorage.removeItem(THINKING_KEY)
    localStorage.removeItem(COMPACT_KEY)
    localStorage.setItem(RAIL_PREF_KEY, '1')
    stubFetch()
    viewport(1280)
  })
  afterEach(() => {
    vi.unstubAllGlobals()
    window.matchMedia = desktop
    document.body.style.overflow = ''
    localStorage.removeItem(RAIL_PREF_KEY)
    localStorage.removeItem(RAIL_TAB_KEY)
    localStorage.removeItem(COMPACT_KEY)
    localStorage.removeItem(THINKING_KEY)
  })

  it('the toolbar keeps model, effort, Rules and Stop: no Files, Skills & plugins, Show thinking or Compact tool calls', async () => {
    seed({ status: 'running', model: 'claude-opus-5-5', effort: 'high' })
    await renderPage()
    const bar = toolbar()
    expect(within(bar).getByRole('combobox', { name: 'Model' })).toBeInTheDocument()
    expect(within(bar).getByTestId('rules-toggle')).toBeInTheDocument()
    expect(within(bar).getByRole('button', { name: 'Stop' })).toBeInTheDocument()
    expect(within(bar).queryByTestId('files-toggle')).toBeNull()
    expect(within(bar).queryByTestId('capabilities-toggle')).toBeNull()
    expect(within(bar).queryByRole('checkbox', { name: 'Show thinking' })).toBeNull()
    expect(within(bar).queryByRole('checkbox', { name: 'Compact tool calls' })).toBeNull()
    expect(within(bar).queryByRole('checkbox', { name: 'Resume when limit resets' })).not.toBeInTheDocument()
    expect(within(screen.getByTestId('rail-settings')).getByRole('checkbox', { name: 'Resume when limit resets' })).toBeInTheDocument()
    // The header has no Skills & plugins button for an agent session either.
    expect(screen.queryByTestId('capabilities-toggle-terminal')).toBeNull()
  })

  it('Settings closes the Live tab with Show thinking and Compact tool calls, kept per browser', async () => {
    seed({}, ...bashRun())
    await renderPage()
    const live = within(rail()).getByRole('tabpanel', { name: /^Live/ })
    const sections = Array.from(live.querySelectorAll('section')).map(s => s.dataset.testid)
    expect(sections.at(-1)).toBe('rail-settings')
    expect(sections.at(-2)).toBe('rail-session')
    const settings = within(rail()).getByTestId('rail-settings')
    const compact = within(settings).getByRole('checkbox', { name: 'Compact tool calls' })
    const thinking = within(settings).getByRole('checkbox', { name: 'Show thinking' })
    expect(compact).toBeChecked()
    expect(thinking).toBeChecked()
    expect(screen.getByTestId('tool-group')).toBeInTheDocument()

    fireEvent.click(compact)
    expect(compact).not.toBeChecked()
    expect(screen.queryByTestId('tool-group')).toBeNull()
    expect(localStorage.getItem(COMPACT_KEY)).toBe('0')
    fireEvent.click(thinking)
    expect(localStorage.getItem(THINKING_KEY)).toBe('0')
  })

  it('reads both settings back as they were saved', async () => {
    localStorage.setItem(COMPACT_KEY, '0')
    localStorage.setItem(THINKING_KEY, '0')
    seed({}, ...bashRun())
    await renderPage()
    const settings = within(rail()).getByTestId('rail-settings')
    expect(within(settings).getByRole('checkbox', { name: 'Compact tool calls' })).not.toBeChecked()
    expect(within(settings).getByRole('checkbox', { name: 'Show thinking' })).not.toBeChecked()
    expect(screen.queryByTestId('tool-group')).toBeNull()
  })

  it('Files lists the chat’s files, read again when the tab comes on screen; View opens the viewer, Delete refreshes', async () => {
    seed()
    await renderPage()
    await waitFor(() => expect(reads('/artifacts')).toBe(1))
    fireEvent.click(tab('Files'))
    expect(localStorage.getItem(RAIL_TAB_KEY)).toBe('files')
    await waitFor(() => expect(reads('/artifacts')).toBe(2))
    const panel = within(rail()).getByTestId('rail-files')
    await waitFor(() => expect(within(panel).getAllByTestId('artifact-name').map(n => n.textContent)).toEqual(['report.md', 'notes.txt']))
    expect(within(panel).getByTestId('rail-files-count')).toHaveTextContent('2 files')
    // Not a dialog: the list is in the panel.
    expect(screen.queryByTestId('artifacts-panel')).toBeNull()

    fireEvent.click(within(within(panel).getAllByTestId('artifact-row')[0]).getByRole('button', { name: 'View' }))
    expect(await screen.findByRole('dialog', { name: /report\.md/ })).toBeInTheDocument()
    fireEvent.keyDown(screen.getByRole('dialog', { name: /report\.md/ }), { key: 'Escape' })

    const second = within(panel).getAllByTestId('artifact-row')[1]
    fireEvent.click(within(second).getByRole('button', { name: 'Delete' }))
    fireEvent.click(within(second).getByRole('button', { name: 'Yes, delete' }))
    await waitFor(() => expect(within(panel).getAllByTestId('artifact-name').map(n => n.textContent)).toEqual(['report.md']))
  })

  it('Files lists the chat’s files when the live state comes from the server too', async () => {
    liveAnswer = {
      session_id: 'sess-a', through_seq: 1, generated_at: new Date().toISOString(), tokens_hidden: false, asks: [], subagents: [], tasks: [],
      tool_changes: 0, counts: { needs_you: 0, running: 0 },
    }
    seed()
    await renderPage()
    await waitFor(() => expect(within(rail()).getByTestId('rail-open-files')).toBeInTheDocument())
    fireEvent.click(within(rail()).getByTestId('rail-open-files'))
    const panel = within(rail()).getByTestId('rail-files')
    await waitFor(() => expect(within(panel).getAllByTestId('artifact-name')).toHaveLength(2))
  })

  it('the Files tab carries the file count (names, not versions), hidden at zero, and follows a new file', async () => {
    // Two versions of one report and a note: two files.
    files = [{ ...report, version: 2, latest_version: 2 }, { ...report, id: 'a0', version: 1, latest_version: 2 }, notes] as typeof files
    seed()
    await renderPage()
    await waitFor(() => expect(within(tab('Files')).getByTestId('rail-files-tab-count')).toHaveTextContent('2'))
    expect(tab('Files')).toHaveAccessibleName('Files, 2 files')
    // The agent publishes a file: the count follows without the tab being opened.
    files = [{ id: 'a3', name: 'plan.md', size: 10, content_type: 'text/markdown', view: 'markdown', created_at: '2026-10-15T11:00:00Z' } as typeof report, ...files]
    act(() => { useTranscriptStore.getState().ingest(ev('artifact', { artifact_id: 'a3', name: 'plan.md' })) })
    await waitFor(() => expect(tab('Files')).toHaveAccessibleName('Files, 3 files'))
    // Everything deleted: no badge.
    files = []
    act(() => { useTranscriptStore.getState().ingest(ev('artifact', { artifact_id: 'a4', name: 'x.md' })) })
    await waitFor(() => expect(within(tab('Files')).queryByTestId('rail-files-tab-count')).toBeNull())
    expect(tab('Files')).toHaveAccessibleName('Files')
  })

  it('Files on screen from the start does not read again the list the chat has just read', async () => {
    localStorage.setItem(RAIL_TAB_KEY, 'files')
    seed()
    await renderPage()
    await within(rail()).findAllByTestId('artifact-row')
    await act(async () => { for (let k = 0; k < 10; k++) await Promise.resolve() })
    expect(reads('/artifacts')).toBe(1)
  })

  it('a switch to Capabilities reads the tool changes again, once', async () => {
    seed()
    await renderPage()
    await waitFor(() => expect(reads('/api/tool-changes')).toBe(1))
    fireEvent.click(tab('Capabilities'))
    await waitFor(() => expect(reads('/api/tool-changes')).toBe(2))
    await act(async () => { for (let k = 0; k < 10; k++) await Promise.resolve() })
    expect(reads('/api/tool-changes')).toBe(2)
  })

  it('Files offers the message box’s file picker', async () => {
    seed()
    await renderPage()
    fireEvent.click(tab('Files'))
    const input = screen.getByTestId('attach-input') as HTMLInputElement
    const picked = vi.fn()
    input.addEventListener('click', picked)
    fireEvent.click(await within(rail()).findByTestId('rail-files-attach'))
    expect(picked).toHaveBeenCalledTimes(1)
  })

  it('Capabilities shows what the session loaded, asking the server only once the tab is on screen', async () => {
    seed()
    await renderPage()
    expect(reads('/capabilities')).toBe(0)
    fireEvent.click(tab('Capabilities'))
    const panel = within(rail()).getByTestId('rail-capabilities')
    expect(await within(panel).findByText('release-notes')).toBeInTheDocument()
    expect(reads('/capabilities')).toBe(1)
    expect(within(panel).getByRole('searchbox', { name: 'Filter skills, plugins and tools' })).toBeInTheDocument()
    expect(screen.queryByTestId('capabilities-panel')).toBeNull()
    // Back on Live and on Capabilities again: read again (the dialog was read on each opening too).
    fireEvent.click(tab('Live'))
    fireEvent.click(tab('Capabilities'))
    await waitFor(() => expect(reads('/capabilities')).toBe(2))
  })

  it('the Capabilities tab carries the number of enabled capabilities, hidden at zero', async () => {
    // Two skills, a plugin enabled and one disabled, an MCP server connected and one that needs auth,
    // and commands and tools that do not count: 4 enabled.
    seed({}, ev('capabilities', {
      engine: 'claude',
      groups: [
        { id: 'skills', label: 'Skills', items: [{ name: 'a' }, { name: 'b' }] },
        { id: 'plugins', label: 'Plugins', items: [{ name: 'p1', status: 'enabled' }, { name: 'p2', status: 'disabled' }] },
        { id: 'mcp', label: 'MCP servers', items: [{ name: 'issues', status: 'connected' }, { name: 'drive', status: 'needs-auth' }] },
        { id: 'commands', label: 'Slash commands', items: [{ name: '/x' }, { name: '/y' }] },
        { id: 'tools', label: 'Tools', items: [{ name: 'Bash' }] },
      ],
    }))
    await renderPage()
    expect(within(tab('Capabilities')).getByTestId('rail-capabilities-tab-count')).toHaveTextContent('4')
    expect(tab('Capabilities')).toHaveAccessibleName('Capabilities, 4 enabled')
    // A report with nothing enabled: no badge.
    act(() => { useTranscriptStore.getState().ingest(ev('capabilities', { engine: 'claude', groups: [{ id: 'commands', label: 'Slash commands', items: [{ name: '/x' }] }] })) })
    await waitFor(() => expect(within(tab('Capabilities')).queryByTestId('rail-capabilities-tab-count')).toBeNull())
    expect(tab('Capabilities')).toHaveAccessibleName('Capabilities')
  })

  it('opens on the tab last chosen; a value it does not know is Live', async () => {
    localStorage.setItem(RAIL_TAB_KEY, 'capabilities')
    expect(readRailTab()).toBe('capabilities')
    // The name it was saved under before the tab was renamed.
    localStorage.setItem(RAIL_TAB_KEY, 'capabilities')
    expect(readRailTab()).toBe('capabilities')
    seed()
    await renderPage()
    expect(tab('Capabilities')).toHaveAttribute('aria-selected', 'true')
    localStorage.setItem(RAIL_TAB_KEY, 'settings')
    expect(readRailTab()).toBe('live')
  })

  it('shows Live for a new question from Files, without making it the choice', async () => {
    localStorage.setItem(RAIL_PREF_KEY, '0')
    localStorage.setItem(RAIL_TAB_KEY, 'files')
    seed({ status: 'waiting' })
    await renderPage()
    expect(screen.getByTestId('session-rail')).not.toBeVisible()
    act(() => {
      useTranscriptStore.getState().ingest({ ...ev('ask', { body: 'Merge now?', message_id: 'm9' }), ts: new Date(Date.now() + 60_000).toISOString() })
    })
    await waitFor(() => expect(tab('Live')).toHaveAttribute('aria-selected', 'true'))
    expect(localStorage.getItem(RAIL_TAB_KEY)).toBe('files')
  })

  describe('in the drawer (390 px)', () => {
    beforeEach(() => viewport(390))

    it('opening the drawer on Capabilities reads the tool changes again once, not twice', async () => {
      localStorage.setItem(RAIL_TAB_KEY, 'capabilities')
      seed()
      await renderPage()
      await waitFor(() => expect(reads('/api/tool-changes')).toBe(1))
      fireEvent.click(screen.getByTestId('rail-toggle'))
      await waitFor(() => expect(reads('/api/tool-changes')).toBe(2))
      await act(async () => { for (let k = 0; k < 10; k++) await Promise.resolve() })
      expect(reads('/api/tool-changes')).toBe(2)
    })

    it('has the same tabs; View closes the drawer and opens the file over the chat', async () => {
      localStorage.setItem(RAIL_TAB_KEY, 'files')
      seed()
      await renderPage()
      // The toolbar has no icon buttons for files or skills on a phone either.
      expect(within(toolbar()).queryByTestId('files-toggle')).toBeNull()
      expect(within(toolbar()).queryByTestId('capabilities-toggle')).toBeNull()
      fireEvent.click(screen.getByTestId('rail-toggle'))
      const drawer = screen.getByRole('dialog', { name: 'Session panel' })
      expect(within(drawer).getAllByRole('tab').map(t => t.textContent)).toEqual(['Live', 'History', 'Files2, 2 files', 'Capabilities'])
      const row = (await within(drawer).findAllByTestId('artifact-row'))[0]
      fireEvent.click(within(row).getByRole('button', { name: 'View' }))
      expect(screen.queryByRole('dialog', { name: 'Session panel' })).toBeNull()
      expect(await screen.findByRole('dialog', { name: /report\.md/ })).toBeInTheDocument()
    })
  })
})
