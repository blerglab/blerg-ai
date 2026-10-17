import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, fireEvent, within, act, waitFor } from '@testing-library/react'
import { Suspense, use } from 'react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import SessionList from './SessionList'
import { useSessionStore } from '../hooks/useSessionStore'
import { useMessageStore } from '../hooks/useMessageStore'
import { makeDaemon, makeSession } from '../test/fixtures'
import { setSettingsOpen, settingsIsOpen } from '../lib/settingsDialog'
import { setFocus } from '../lib/sidebarPrefs'
import { usePendingProposals } from '../lib/proposals'

vi.mock('../ws', () => import('../test/wsMock'))

function seed(daemons = [makeDaemon()], sessions = [makeSession()]) {
  useSessionStore.setState({
    daemons,
    sessions,
    statusChangedAt: Object.fromEntries(sessions.map(s => [s.id, Date.now()])),
    serverVersion: 'test',
  })
}

function renderList(props = {}) {
  return render(
    <MemoryRouter initialEntries={['/']}>
      <SessionList {...props} />
    </MemoryRouter>,
  )
}

// The titles of the sessions in the store, in the order their cards sit on the page.
function cardTitles(): string[] {
  const body = document.body.textContent!
  return useSessionStore.getState().sessions
    .map(s => s.title)
    .filter(t => body.includes(t))
    .sort((x, y) => body.indexOf(x) - body.indexOf(y))
}

describe('SessionList', () => {
  beforeEach(() => {
    localStorage.clear()
    useSessionStore.setState({ daemons: [], sessions: [], statusChangedAt: {}, serverVersion: '' })
    useMessageStore.setState({ messages: [] })
  })

  it('carries the cron badge through to the list and links to the crons page in the sidebar', () => {
    seed([makeDaemon()], [
      makeSession({ id: 'a', status: 'running', title: 'From a cron', cron_id: 'c1' }),
      makeSession({ id: 'b', status: 'running', title: 'By hand' }),
    ])
    renderList({ variant: 'sidebar' })
    // A cron's session lives in the Automated section (collapsed until opened), with its badge.
    expect(screen.queryByTestId('cron-badge')).toBeNull()
    fireEvent.click(screen.getByText('Automated'))
    expect(screen.getAllByTestId('cron-badge')).toHaveLength(1)
    expect(screen.getByTestId('crons-nav')).toHaveTextContent('Crons')
  })

  it('shows the release version and commit at the foot of the sidebar and of the phone list', () => {
    seed()
    useSessionStore.setState({ serverVersion: '510d0c0', serverRelease: '0.2.0', serverCommit: '510d0c0' })
    const { unmount } = renderList({ variant: 'sidebar' })
    expect(screen.getByTestId('build-version')).toHaveTextContent('Blerg v0.2.0 · 510d0c0')
    expect(screen.getByTestId('build-version').title).toContain('release 0.2.0')
    unmount()
    renderList()
    expect(screen.getByTestId('build-version')).toHaveTextContent('Blerg v0.2.0 · 510d0c0')
  })

  it('falls back to the server version when the server reports no release', () => {
    seed()
    useSessionStore.setState({ serverVersion: 'dev', serverRelease: '', serverCommit: '' })
    renderList({ variant: 'sidebar' })
    expect(screen.getByTestId('build-version')).toHaveTextContent('Blerg dev')
  })

  it('links to Proposals in the sidebar with the pending count, hidden at zero', () => {
    usePendingProposals.setState({ count: 2 })
    const { unmount } = renderList({ variant: 'sidebar' })
    expect(screen.getByTestId('proposals-nav')).toHaveTextContent('Proposals')
    expect(screen.getByTestId('proposals-count-badge')).toHaveTextContent('2')
    unmount()
    usePendingProposals.setState({ count: 0 })
    renderList({ variant: 'sidebar' })
    expect(screen.queryByTestId('proposals-count-badge')).toBeNull()
  })

  it('highlights the clicked session at once, while the switch to it is still rendering', () => {
    seed([makeDaemon()], [
      makeSession({ id: 'a', status: 'running', title: 'Session A' }),
      makeSession({ id: 'b', status: 'running', title: 'Session B' }),
    ])
    // Opening b never finishes rendering (it suspends inside a transition), so the route stays on a.
    const never = new Promise<never>(() => {})
    function Opening(): null {
      use(never)
      return null
    }
    const { container } = render(
      <MemoryRouter initialEntries={['/sessions/a']}>
        <Suspense fallback={<div data-testid="fallback" />}>
          <SessionList variant="sidebar" />
          <Routes>
            <Route path="/sessions/a" element={<div data-testid="route-a" />} />
            <Route path="/sessions/b" element={<Opening />} />
          </Routes>
        </Suspense>
      </MemoryRouter>,
    )
    const active = () => Array.from(container.querySelectorAll('[data-active="true"]')).map(el => el.textContent)
    expect(active()).toHaveLength(1)
    expect(active()[0]).toContain('Session A')

    fireEvent.click(screen.getByText('Session B'))
    expect(screen.getByTestId('route-a')).toBeInTheDocument()
    expect(screen.queryByTestId('fallback')).toBeNull()
    expect(active()).toHaveLength(1)
    expect(active()[0]).toContain('Session B')
  })

  it('links to Insights in the sidebar, and only there', () => {
    const { unmount } = renderList({ variant: 'sidebar' })
    expect(screen.getByTestId('insights-nav')).toHaveTextContent('Insights')
    unmount()
    renderList()
    expect(screen.queryByTestId('insights-nav')).toBeNull()
  })

  it('has a Settings entry right under Insights that opens the dialog without leaving the page', () => {
    const { unmount } = renderList({ variant: 'sidebar' })
    const insights = screen.getByTestId('insights-nav')
    const settings = screen.getByTestId('settings-nav')
    expect(settings).toHaveTextContent('Settings')
    expect(insights.nextElementSibling).toBe(settings)
    expect(settingsIsOpen()).toBe(false)
    fireEvent.click(settings)
    expect(settingsIsOpen()).toBe(true)
    setSettingsOpen(false)
    unmount()
    renderList()
    expect(screen.queryByTestId('settings-nav')).toBeNull()
  })

  it('lists active sessions one after the next, each card saying its repo, with no repo groups', () => {
    seed([makeDaemon()], [
      makeSession({ id: 'a', status: 'running', repo: 'blerglab/widget', title: 'Task one' }),
      makeSession({ id: 'b', status: 'running', repo: 'blerglab/engine', title: 'Task two' }),
    ])
    renderList()
    expect(screen.getByText('Task one')).toBeInTheDocument()
    expect(screen.getAllByTestId('session-repo').map(r => r.textContent).sort()).toEqual(['blerglab/engine', 'blerglab/widget'])
    // No repo heading, no group toggle.
    expect(screen.queryByText('▶', { selector: 'span' })).toBeNull()
    expect(screen.queryByTestId(/^repo-group/)).toBeNull()
  })

  it('orders the list by start time, newest first, across repos and whatever the status', () => {
    seed([makeDaemon()], [
      makeSession({ id: 'i', status: 'idle', repo: 'a/alpha', title: 'Idle alpha', started_at: '2026-06-15T00:00:00Z' }),
      makeSession({ id: 'r1', status: 'running', repo: 'z/zulu', title: 'Running old', started_at: '2026-01-01T00:00:00Z' }),
      makeSession({ id: 'w', status: 'waiting', repo: 'm/mike', title: 'Waiting', started_at: '2026-02-01T00:00:00Z' }),
      makeSession({ id: 'r2', status: 'running', repo: 'a/alpha', title: 'Running new', started_at: '2026-06-01T00:00:00Z' }),
    ])
    renderList()
    expect(cardTitles()).toEqual(['Idle alpha', 'Running new', 'Waiting', 'Running old'])
  })

  it('does not move a card when its status changes', () => {
    seed([makeDaemon()], [
      makeSession({ id: 'a', status: 'running', title: 'Newest', started_at: '2026-06-03T00:00:00Z' }),
      makeSession({ id: 'b', status: 'running', title: 'Middle', started_at: '2026-06-02T00:00:00Z' }),
      makeSession({ id: 'c', status: 'idle', title: 'Oldest', started_at: '2026-06-01T00:00:00Z' }),
    ])
    renderList()
    const before = ['Newest', 'Middle', 'Oldest']
    expect(cardTitles()).toEqual(before)
    const setStatus = (id: string, status: 'running' | 'idle' | 'waiting', background_tasks = 0) => act(() => {
      useSessionStore.setState(st => ({
        sessions: st.sessions.map(s => (s.id === id ? { ...s, status, background_tasks } : s)),
      }))
    })
    setStatus('a', 'idle')
    expect(cardTitles()).toEqual(before)
    setStatus('c', 'running')
    expect(cardTitles()).toEqual(before)
    setStatus('b', 'idle', 2)
    expect(cardTitles()).toEqual(before)
    setStatus('c', 'waiting')
    expect(cardTitles()).toEqual(before)
  })

  it('puts a newly started session at the top of the list', () => {
    seed([makeDaemon()], [
      makeSession({ id: 'a', status: 'idle', title: 'Existing one', started_at: '2026-06-02T00:00:00Z' }),
      makeSession({ id: 'b', status: 'running', title: 'Existing two', started_at: '2026-06-01T00:00:00Z' }),
    ])
    renderList()
    act(() => {
      useSessionStore.setState(st => ({
        sessions: [...st.sessions, makeSession({ id: 'n', status: 'starting', title: 'Brand new', started_at: '2026-06-10T00:00:00Z' })],
      }))
    })
    expect(cardTitles()).toEqual(['Brand new', 'Existing one', 'Existing two'])
  })

  it('lists an active cluster session whose pod daemon is not in the daemon list', () => {
    seed([makeDaemon()], [
      makeSession({ id: 'c1', daemon_id: 'hidden-pod-daemon', status: 'idle', repo: 'widget', title: 'Cluster task' }),
    ])
    renderList()
    expect(screen.getByText('Cluster task')).toBeInTheDocument()
  })

  it('shows no repo line for a no-repo session, never a scratch folder name', () => {
    seed([makeDaemon()], [
      makeSession({ id: 's1', status: 'running', repo: '.scratch-granite-8000', title: 'Scratch one' }),
      makeSession({ id: 's2', status: 'running', repo: '.scratch-ember-01ab', title: 'Scratch two' }),
    ])
    renderList()
    expect(screen.queryByTestId('session-repo')).toBeNull()
    expect(screen.getByText('Scratch one')).toBeInTheDocument()
    expect(screen.getByText('Scratch two')).toBeInTheDocument()
    expect(screen.queryByText(/\.scratch-/)).not.toBeInTheDocument()
  })

  it('shows the waiting banner with a count when sessions are waiting', () => {
    seed([makeDaemon()], [
      makeSession({ id: 's1', status: 'waiting' }),
      makeSession({ id: 's2', status: 'waiting' }),
    ])
    renderList()
    expect(screen.getByText('2 sessions waiting for input')).toBeInTheDocument()
  })

  it('does not count a session working in the background as waiting for input', () => {
    seed([makeDaemon()], [
      makeSession({ id: 's1', status: 'waiting' }),
      makeSession({ id: 's2', status: 'idle', background_tasks: 3 }),
    ])
    renderList()
    expect(screen.getByText('1 session waiting for input')).toBeInTheDocument()
  })

  it('leaves sessions a restart left paused out of the banner, with no Resume all', () => {
    seed([makeDaemon()], [
      makeSession({ id: 's1', status: 'waiting' }),
      makeSession({ id: 'p1', status: 'idle', paused_reason: 'restart' }),
    ])
    renderList()
    expect(screen.getByTestId('waiting-banner')).toHaveTextContent(/^⚡1 session waiting for input$/)
    expect(screen.queryByText(/paused by a restart/)).toBeNull()
    expect(screen.queryByTestId('resume-all')).toBeNull()
  })

  it('shows no banner when the only session is one a restart left paused', () => {
    seed([makeDaemon()], [makeSession({ id: 'p1', status: 'idle', paused_reason: 'restart' })])
    renderList()
    expect(screen.queryByTestId('waiting-banner')).toBeNull()
  })

  it('shows no waiting banner when the only live sessions work in the background', () => {
    seed([makeDaemon()], [makeSession({ id: 's1', status: 'idle', background_tasks: 1 })])
    renderList()
    expect(screen.queryByText(/waiting for input/)).toBeNull()
  })

  it('tells the user how to start a daemon when none is connected', () => {
    seed([], [])
    renderList()
    expect(screen.getByText(/No daemon connected\. Start it:/)).toBeInTheDocument()
    expect(screen.getByText(/daemon\/install\.sh status/)).toBeInTheDocument()
  })

  it('offers notifications as a small bell beside + New, not as a row of its own', () => {
    vi.stubGlobal('Notification', { permission: 'default', requestPermission: vi.fn() })
    seed()
    renderList({ variant: 'sidebar' })
    const bell = screen.getByRole('button', { name: 'Enable notifications' })
    expect(bell.title).toBe('Enable notifications')
    expect(bell.textContent).toBe('')
    expect(bell.parentElement).toContainElement(screen.getByText('+ New'))
    expect(screen.queryByText('Enable notifications')).toBeNull()
    vi.unstubAllGlobals()
  })

  it('calls onNewSession when the New button is clicked', () => {
    const onNewSession = vi.fn()
    renderList({ onNewSession })
    fireEvent.click(screen.getByText('+ New'))
    expect(onNewSession).toHaveBeenCalledOnce()
  })
})

describe('SessionList — single daemon', () => {
  beforeEach(() => {
    localStorage.clear()
    useSessionStore.setState({ daemons: [], sessions: [], statusChangedAt: {}, serverVersion: '' })
  })

  it('does NOT render focus pills with one place to run', () => {
    seed(
      [makeDaemon({ id: 'd1', name: 'workstation' })],
      [makeSession({ id: 's1', daemon_id: 'd1', title: 'Widget task', status: 'running' })],
    )
    renderList()
    expect(screen.queryByTestId('focus-pill-all')).not.toBeInTheDocument()
    expect(screen.queryByTestId('collapse-all')).not.toBeInTheDocument()
  })
})

describe('SessionList — two daemons', () => {
  const d1 = makeDaemon({ id: 'd1', name: 'workstation', status: 'connected' })
  const d2 = makeDaemon({ id: 'd2', name: 'laptop', status: 'connected' })
  const s1 = makeSession({ id: 's1', daemon_id: 'd1', repo: 'widget', title: 'Widget task', status: 'running' })
  const s2 = makeSession({ id: 's2', daemon_id: 'd2', repo: 'backend', title: 'Backend task', status: 'running' })

  function seedTwo() {
    useSessionStore.setState({
      daemons: [d1, d2],
      sessions: [s1, s2],
      statusChangedAt: { s1: Date.now(), s2: Date.now() },
      serverVersion: 'test',
    })
  }

  beforeEach(() => {
    localStorage.clear()
    useSessionStore.setState({ daemons: [], sessions: [], statusChangedAt: {}, serverVersion: '' })
  })

  it('renders focus pill row when 2 daemons', () => {
    seedTwo()
    renderList()
    expect(screen.getByTestId('place-pills')).toBeInTheDocument()
    expect(screen.getByTestId('focus-pill-all')).toBeInTheDocument()
    expect(screen.getByTestId('focus-pill-d1')).toBeInTheDocument()
    expect(screen.getByTestId('focus-pill-d2')).toBeInTheDocument()
    expect(screen.queryByTestId('collapse-all')).not.toBeInTheDocument()
  })

  it('shows sessions from both daemons by default (All)', () => {
    seedTwo()
    renderList()
    expect(screen.getByText('Widget task')).toBeInTheDocument()
    expect(screen.getByText('Backend task')).toBeInTheDocument()
  })

  it('clicking a daemon pill hides the other daemon sessions', () => {
    seedTwo()
    renderList()
    fireEvent.click(screen.getByTestId('focus-pill-d1'))
    expect(screen.getByText('Widget task')).toBeInTheDocument()
    expect(screen.queryByText('Backend task')).not.toBeInTheDocument()
  })

  it('clicking All restores both daemon sessions', () => {
    seedTwo()
    renderList()
    fireEvent.click(screen.getByTestId('focus-pill-d1'))
    fireEvent.click(screen.getByTestId('focus-pill-all'))
    expect(screen.getByText('Widget task')).toBeInTheDocument()
    expect(screen.getByText('Backend task')).toBeInTheDocument()
  })

  it('clicking a daemon pill persists focus to localStorage', () => {
    seedTwo()
    renderList()
    fireEvent.click(screen.getByTestId('focus-pill-d1'))
    expect(localStorage.getItem('blerg-runner.sidebar.focus')).toBe(JSON.stringify('d1'))
  })

  it('clicking All clears persisted focus from localStorage', () => {
    seedTwo()
    setFocus('d1')
    renderList()
    fireEvent.click(screen.getByTestId('focus-pill-all'))
    expect(localStorage.getItem('blerg-runner.sidebar.focus')).toBeNull()
  })

  it('remount reads persisted focus and hides the unfocused daemon', () => {
    seedTwo()
    setFocus('d1')
    renderList()
    expect(screen.getByText('Widget task')).toBeInTheDocument()
    expect(screen.queryByText('Backend task')).not.toBeInTheDocument()
  })

  it('focused-but-disconnected daemon falls back to All', () => {
    // d2 is now disconnected
    useSessionStore.setState({
      daemons: [
        makeDaemon({ id: 'd1', name: 'workstation', status: 'connected' }),
        makeDaemon({ id: 'd2', name: 'laptop', status: 'disconnected' }),
      ],
      sessions: [s1, s2],
      statusChangedAt: { s1: Date.now(), s2: Date.now() },
      serverVersion: 'test',
    })
    setFocus('d2')
    renderList()
    // Since d2 is disconnected, effective focus = All → both sessions visible
    expect(screen.getByText('Widget task')).toBeInTheDocument()
    expect(screen.getByText('Backend task')).toBeInTheDocument()
  })

  it('has no Chat bubble in the top bar', () => {
    seed()
    renderList()
    expect(screen.queryByTestId('chat-bubble')).not.toBeInTheDocument()
  })
})

describe('SessionList — search', () => {
  const d1 = makeDaemon({ id: 'd1', name: 'workstation', status: 'connected' })
  const d2 = makeDaemon({ id: 'd2', name: 'laptop', status: 'connected' })
  const s1 = makeSession({ id: 's1', daemon_id: 'd1', repo: 'widget', title: 'Widget task', status: 'running' })
  const s2 = makeSession({ id: 's2', daemon_id: 'd2', repo: 'backend', title: 'Backend task', status: 'running' })
  const s3 = makeSession({ id: 's3', daemon_id: 'd1', repo: 'frontend', title: 'UI work', status: 'idle' })

  function seedSearch() {
    useSessionStore.setState({
      daemons: [d1, d2],
      sessions: [s1, s2, s3],
      statusChangedAt: { s1: Date.now(), s2: Date.now(), s3: Date.now() },
      serverVersion: 'test',
    })
  }

  beforeEach(() => {
    localStorage.clear()
    useSessionStore.setState({ daemons: [], sessions: [], statusChangedAt: {}, serverVersion: '' })
  })

  it('renders a search input', () => {
    seedSearch()
    renderList()
    expect(screen.getByTestId('search-input')).toBeInTheDocument()
  })

  it('typing a query narrows to sessions matching by title', () => {
    seedSearch()
    renderList()
    fireEvent.change(screen.getByTestId('search-input'), { target: { value: 'widget' } })
    expect(screen.getByText('Widget task')).toBeInTheDocument()
    expect(screen.queryByText('Backend task')).not.toBeInTheDocument()
    expect(screen.queryByText('UI work')).not.toBeInTheDocument()
  })

  it('typing a query narrows to sessions matching by repo', () => {
    seedSearch()
    renderList()
    fireEvent.change(screen.getByTestId('search-input'), { target: { value: 'backend' } })
    expect(screen.getByText('Backend task')).toBeInTheDocument()
    expect(screen.queryByText('Widget task')).not.toBeInTheDocument()
    expect(screen.queryByText('UI work')).not.toBeInTheDocument()
  })

  it('sessions that do not match are hidden during search, whatever their repo or daemon', () => {
    seedSearch()
    renderList()
    fireEvent.change(screen.getByTestId('search-input'), { target: { value: 'backend' } })
    // Neither the other cards nor their repo lines are left.
    expect(screen.queryByText('widget')).not.toBeInTheDocument()
    expect(screen.queryByText('frontend')).not.toBeInTheDocument()
    // only backend match is visible
    expect(screen.getByText('Backend task')).toBeInTheDocument()
  })

  it('with focus active, query surfaces matches in other daemons (focus overridden)', () => {
    seedSearch()
    setFocus('d1') // focus on workstation — only d1 sessions should show normally
    renderList()
    // Before search: d2's Backend task hidden due to focus
    expect(screen.queryByText('Backend task')).not.toBeInTheDocument()
    // Type a query that matches d2's session
    fireEvent.change(screen.getByTestId('search-input'), { target: { value: 'backend' } })
    // Now d2's session should be visible despite focus on d1
    expect(screen.getByText('Backend task')).toBeInTheDocument()
  })

  it('clearing the query restores focus-based filtering', () => {
    seedSearch()
    setFocus('d1') // focus on workstation
    renderList()
    // Type a query matching d2
    fireEvent.change(screen.getByTestId('search-input'), { target: { value: 'backend' } })
    expect(screen.getByText('Backend task')).toBeInTheDocument()
    // Clear the query
    fireEvent.change(screen.getByTestId('search-input'), { target: { value: '' } })
    // Focus on d1 restored: Backend task (d2) hidden again
    expect(screen.queryByText('Backend task')).not.toBeInTheDocument()
    expect(screen.getByText('Widget task')).toBeInTheDocument()
  })

  it('search is case-insensitive', () => {
    seedSearch()
    renderList()
    fireEvent.change(screen.getByTestId('search-input'), { target: { value: 'WIDGET' } })
    expect(screen.getByText('Widget task')).toBeInTheDocument()
    expect(screen.queryByText('Backend task')).not.toBeInTheDocument()
  })

})

describe('SessionList — starred section', () => {
  const d1 = makeDaemon({ id: 'd1', name: 'workstation', status: 'connected' })
  const d2 = makeDaemon({ id: 'd2', name: 'laptop', status: 'connected' })

  beforeEach(() => {
    localStorage.clear()
    useSessionStore.setState({ daemons: [], sessions: [], statusChangedAt: {}, serverVersion: '' })
  })

  it('starred active session appears inside starred-section and not again in the list', () => {
    const starred = makeSession({ id: 's1', daemon_id: 'd1', repo: 'widget', title: 'Starred task', status: 'running', starred: true })
    const normal = makeSession({ id: 's2', daemon_id: 'd1', repo: 'widget', title: 'Normal task', status: 'running', starred: false })
    useSessionStore.setState({
      daemons: [d1],
      sessions: [starred, normal],
      statusChangedAt: { s1: Date.now(), s2: Date.now() },
      serverVersion: 'test',
    })
    renderList()

    // Starred task is in the starred section
    const starredSection = screen.getByTestId('starred-section')
    expect(within(starredSection).getByText('Starred task')).toBeInTheDocument()

    // Starred task appears exactly once (not also in the list below)
    const allMatches = screen.getAllByText('Starred task')
    expect(allMatches).toHaveLength(1)

    // Normal task is NOT in the starred section, but IS in the list
    expect(within(starredSection).queryByText('Normal task')).not.toBeInTheDocument()
    expect(screen.getByText('Normal task')).toBeInTheDocument()
  })

  it('keeps starred sessions first, newest started first among them, and does not move them on a status change', () => {
    useSessionStore.setState({
      daemons: [d1],
      sessions: [
        makeSession({ id: 'n1', daemon_id: 'd1', title: 'Plain newest', status: 'running', started_at: '2026-06-09T00:00:00Z' }),
        makeSession({ id: 's1', daemon_id: 'd1', title: 'Star older', status: 'idle', starred: true, started_at: '2026-06-01T00:00:00Z' }),
        makeSession({ id: 's2', daemon_id: 'd1', title: 'Star newer', status: 'idle', starred: true, started_at: '2026-06-05T00:00:00Z' }),
        makeSession({ id: 'n2', daemon_id: 'd1', title: 'Plain older', status: 'waiting', started_at: '2026-06-02T00:00:00Z' }),
      ],
      statusChangedAt: {}, serverVersion: 'test',
    })
    renderList()
    const order = ['Star newer', 'Star older', 'Plain newest', 'Plain older']
    const titles = () => {
      const body = document.body.textContent!
      return [...order].sort((x, y) => body.indexOf(x) - body.indexOf(y))
    }
    expect(titles()).toEqual(order)
    act(() => {
      useSessionStore.setState(st => ({
        sessions: st.sessions.map(x => (x.id === 's1' ? { ...x, status: 'running' as const } : x.id === 's2' ? { ...x, status: 'waiting' as const } : x)),
      }))
    })
    expect(titles()).toEqual(order)
  })

  it('does not render starred-section when no active session is starred', () => {
    const normal = makeSession({ id: 's1', daemon_id: 'd1', repo: 'widget', title: 'Normal task', status: 'running' })
    useSessionStore.setState({
      daemons: [d1],
      sessions: [normal],
      statusChangedAt: { s1: Date.now() },
      serverVersion: 'test',
    })
    renderList()
    expect(screen.queryByTestId('starred-section')).not.toBeInTheDocument()
  })

  it('starred section shows starred sessions from non-focused daemon (ignores focus)', () => {
    const s1 = makeSession({ id: 's1', daemon_id: 'd1', repo: 'widget', title: 'Widget task', status: 'running' })
    const s2 = makeSession({ id: 's2', daemon_id: 'd2', repo: 'backend', title: 'Starred backend', status: 'running', starred: true })
    useSessionStore.setState({
      daemons: [d1, d2],
      sessions: [s1, s2],
      statusChangedAt: { s1: Date.now(), s2: Date.now() },
      serverVersion: 'test',
    })
    // Focus on d1 (workstation only) — d2's sessions normally hidden
    setFocus('d1')
    renderList()

    // Starred backend should appear in starred section despite d2 being unfocused
    const starredSection = screen.getByTestId('starred-section')
    expect(within(starredSection).getByText('Starred backend')).toBeInTheDocument()
  })

  it('search query that excludes a starred session removes it from the starred section', () => {
    const starred = makeSession({ id: 's1', daemon_id: 'd1', repo: 'widget', title: 'Starred task', status: 'running', starred: true })
    useSessionStore.setState({
      daemons: [d1],
      sessions: [starred],
      statusChangedAt: { s1: Date.now() },
      serverVersion: 'test',
    })
    renderList()

    // Starred section is visible initially
    expect(screen.getByTestId('starred-section')).toBeInTheDocument()

    // Type a query that does NOT match the starred session
    fireEvent.change(screen.getByTestId('search-input'), { target: { value: 'no-match-xyz' } })

    // Starred section should disappear
    expect(screen.queryByTestId('starred-section')).not.toBeInTheDocument()
  })
})

describe('SessionList — idle sessions are always shown', () => {
  beforeEach(() => {
    localStorage.clear()
    useSessionStore.setState({ daemons: [], sessions: [], statusChangedAt: {}, serverVersion: '' })
  })

  it('an idle session is listed with no expand step', () => {
    const d = makeDaemon({ id: 'd1' })
    const s1 = makeSession({ id: 's1', daemon_id: 'd1', repo: 'widget', title: 'Idle task', status: 'idle' })
    useSessionStore.setState({ daemons: [d], sessions: [s1], statusChangedAt: { s1: Date.now() }, serverVersion: 'test' })
    renderList()
    expect(screen.getByText('Idle task')).toBeInTheDocument()
  })

  it('a running session is listed', () => {
    const d = makeDaemon({ id: 'd1' })
    const s1 = makeSession({ id: 's1', daemon_id: 'd1', repo: 'widget', title: 'Running task', status: 'running' })
    useSessionStore.setState({ daemons: [d], sessions: [s1], statusChangedAt: { s1: Date.now() }, serverVersion: 'test' })
    renderList()
    expect(screen.getByText('Running task')).toBeInTheDocument()
  })

  it('idle and running sessions of different repos are all listed', () => {
    const d = makeDaemon({ id: 'd1' })
    const idle = makeSession({ id: 's1', daemon_id: 'd1', repo: 'widget', title: 'Idle task', status: 'idle' })
    const running = makeSession({ id: 's2', daemon_id: 'd1', repo: 'engine', title: 'Running task', status: 'running' })
    useSessionStore.setState({ daemons: [d], sessions: [idle, running], statusChangedAt: { s1: Date.now(), s2: Date.now() }, serverVersion: 'test' })
    renderList()
    expect(screen.getByText('Idle task')).toBeInTheDocument()
    expect(screen.getByText('Running task')).toBeInTheDocument()
  })
})

describe('SessionList — cluster pill count', () => {
  afterEach(() => { vi.unstubAllGlobals() })

  it('follows the sessions in the store, not the number fetched once from the server', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve({
      ok: true,
      json: () => Promise.resolve({ configured: true, max_sessions: 4, active_sessions: 3 }),
    })))
    seed([], [
      makeSession({ id: 'c1', runtime: 'cluster', status: 'idle', daemon_id: 'pod-1' }),
      makeSession({ id: 'c2', runtime: 'cluster', status: 'running', daemon_id: 'pod-2' }),
    ])
    renderList()
    // the server says 3 are in use but the store has 2 active cluster sessions: the pill shows the live 2
    await waitFor(() => expect(screen.getByTestId('focus-pill-cluster')).toHaveTextContent('Cluster 2/4'))
    // one is killed: the pill drops at once, with no refetch needed
    act(() => {
      useSessionStore.setState({ sessions: [
        makeSession({ id: 'c1', runtime: 'cluster', status: 'stopped', daemon_id: 'pod-1' }),
        makeSession({ id: 'c2', runtime: 'cluster', status: 'running', daemon_id: 'pod-2' }),
      ] })
    })
    await waitFor(() => expect(screen.getByTestId('focus-pill-cluster')).toHaveTextContent('Cluster 1/4'))
  })

  it('follows a cap saved on the Cluster page without a reload', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve({
      ok: true,
      json: () => Promise.resolve({ configured: true, max_sessions: 4, active_sessions: 0 }),
    })))
    seed([], [])
    renderList()
    await waitFor(() => expect(screen.getByTestId('focus-pill-cluster')).toHaveTextContent('Cluster 0/4'))
    act(() => {
      window.dispatchEvent(new CustomEvent('blerg:cluster-settings-saved', { detail: { configured: true, max_sessions: 12 } }))
    })
    await waitFor(() => expect(screen.getByTestId('focus-pill-cluster')).toHaveTextContent('Cluster 0/12'))
  })
})

describe('History order', () => {
  it('lists ended sessions most recent first across repos, with no repo headings', () => {
    useSessionStore.setState({
      sessions: [
        makeSession({ id: 'old', status: 'stopped', repo: '/r/alpha', title: 'Oldest', ended_at: '2026-10-01T10:00:00Z' }),
        makeSession({ id: 'new', status: 'stopped', repo: '/r/beta', title: 'Newest', ended_at: '2026-10-03T10:00:00Z' }),
        makeSession({ id: 'mid', status: 'stopped', repo: '/r/alpha', title: 'Middle', ended_at: '2026-10-02T10:00:00Z' }),
      ],
      daemons: [makeDaemon({ id: 'd1' })],
    } as never)
    renderList()
    fireEvent.click(screen.getByText('History'))
    const titles = ['Newest', 'Middle', 'Oldest'].map(t => screen.getByText(t))
    const pos = titles.map(el => el.compareDocumentPosition(titles[0]))
    expect(pos[1] & Node.DOCUMENT_POSITION_PRECEDING).toBeTruthy()
    expect(titles[2].compareDocumentPosition(titles[1]) & Node.DOCUMENT_POSITION_PRECEDING).toBeTruthy()
    expect(screen.queryByText('alpha')).toBeNull()
    expect(screen.queryByText('beta')).toBeNull()
  })
})

describe('Automated sessions', () => {
  it('keeps tool-, cron- and key-started sessions out of the list and History, in a collapsed section that counts them', () => {
    useSessionStore.setState({
      sessions: [
        makeSession({ id: 'mine', status: 'running', repo: '/r/widget', title: 'Mine' }),
        makeSession({ id: 'done', status: 'stopped', repo: '/r/widget', title: 'Mine, ended', ended_at: '2026-10-01T10:00:00Z' }),
        makeSession({ id: 't1', status: 'running', repo: '/r/widget', title: 'Smoke test', started_by: { kind: 'agent', name: 'literary-agent' } }),
        makeSession({ id: 't2', status: 'stopped', repo: '/r/widget', title: 'Nightly', started_by: { kind: 'agent', name: 'literary-agent' }, ended_at: '2026-10-02T10:00:00Z' }),
        makeSession({ id: 'c1', status: 'stopped', repo: '/r/widget', title: 'Cron run', cron_id: 'cron-1', ended_at: '2026-10-03T10:00:00Z' }),
        makeSession({ id: 'k1', status: 'running', repo: '/r/widget', title: 'Operator', started_by: { kind: 'runner_key' } }),
      ],
      daemons: [makeDaemon({ id: 'd1' })],
    } as never)
    renderList()
    // Collapsed: the section header counts, the cards are not drawn.
    expect(screen.getByTestId('automated-summary')).toHaveTextContent('2 running · 2 recent')
    expect(screen.queryByText('Smoke test')).toBeNull()
    expect(screen.getByText('Mine')).toBeInTheDocument()
    // History holds only the person's own ended session.
    fireEvent.click(screen.getByText('History'))
    expect(screen.getByText('Mine, ended')).toBeInTheDocument()
    expect(screen.queryByText('Nightly')).toBeNull()
    expect(screen.queryByText('Cron run')).toBeNull()
    // Opened: grouped by who started them, the token by its name.
    fireEvent.click(screen.getByText('Automated'))
    const tool = screen.getByTestId('automated-group-literary-agent')
    expect(tool).toHaveTextContent('Smoke test')
    expect(tool).toHaveTextContent('Nightly')
    expect(screen.getByTestId('automated-group-cron')).toHaveTextContent('Cron run')
    expect(screen.getByTestId('automated-group-operator key')).toHaveTextContent('Operator')
    expect(screen.getAllByTestId('started-by-chip').length).toBeGreaterThan(0)
  })
})
