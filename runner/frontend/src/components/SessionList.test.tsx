import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, fireEvent, within, act, waitFor } from '@testing-library/react'
import { Suspense, use } from 'react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import SessionList from './SessionList'
import { useSessionStore } from '../hooks/useSessionStore'
import { emit } from '../test/wsMock'
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

  it('links to a newer release only when the server reports one', () => {
    seed()
    useSessionStore.setState({ serverRelease: '0.4.0', latestVersion: '', latestUrl: '' })
    const { unmount } = renderList({ variant: 'sidebar' })
    expect(screen.queryByTestId('build-update')).toBeNull()
    unmount()
    useSessionStore.setState({ latestVersion: '0.4.1', latestUrl: 'https://github.com/blerglab/blerg-ai/releases/tag/v0.4.1' })
    renderList({ variant: 'sidebar' })
    const link = screen.getByTestId('build-update')
    expect(link).toHaveTextContent('v0.4.1 available')
    expect(link).toHaveAttribute('href', 'https://github.com/blerglab/blerg-ai/releases/tag/v0.4.1')
    expect(link).toHaveAttribute('target', '_blank')
    expect(link).toHaveAttribute('rel', 'noopener noreferrer')
    expect(link).toHaveAttribute('title', 'A newer Blerg release is available')
    expect(screen.queryByRole('link', { name: 'How to upgrade' })).toBeNull()
    useSessionStore.setState({ latestVersion: '', latestUrl: '' })
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

  it('has the app\'s one Notifications bell beside + New, an icon whose word is in its label, with push inside', () => {
    vi.stubGlobal('Notification', { permission: 'default', requestPermission: vi.fn() })
    seed()
    renderList({ variant: 'sidebar' })
    const bells = screen.getAllByRole('button', { name: /Notifications/ })
    expect(bells).toHaveLength(1)
    const bell = bells[0]
    expect(bell).toHaveClass('icon-btn')
    expect(bell.title).toMatch(/^Notifications/)
    expect(bell.textContent).toBe('')
    expect(bell.closest('div')!.parentElement).toContainElement(screen.getByText('+ New'))
    // The older push toggle is not a second bell: it is a setting inside this one.
    expect(screen.queryByRole('button', { name: 'Enable notifications' })).toBeNull()
    fireEvent.click(bell)
    expect(screen.getByTestId('push-setting')).toHaveTextContent('Push notifications')
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

  it.each(['stopped', 'error'] as const)('updates the Cluster pill when a nested child becomes %s', async status => {
    localStorage.clear()
    const fetch = vi.fn(() => Promise.resolve({
      ok: true,
      json: () => Promise.resolve({ configured: true, max_sessions: 4, active_sessions: 2 }),
    }))
    vi.stubGlobal('fetch', fetch)
    seed([], [
      makeSession({ id: 'parent', runtime: 'cluster', status: 'running' }),
      makeSession({ id: 'child', parent_session_id: 'parent', runtime: 'cluster', status: 'running' }),
    ])
    renderList()
    await waitFor(() => expect(screen.getByTestId('focus-pill-cluster')).toHaveTextContent('Cluster 2/4'))
    const reads = fetch.mock.calls.length
    // State broadcasts preserve ended_at and the other fields used by the structure selector.
    act(() => emit({ type: 'session_state_changed', session_id: 'child', status, unread: false }))
    expect(screen.getByTestId('focus-pill-cluster')).toHaveTextContent('Cluster 1/4')
    await waitFor(() => expect(fetch.mock.calls.length).toBeGreaterThan(reads))
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

describe('started-session sidebar nesting', () => {
  beforeEach(() => localStorage.clear())
  it.each(['archived', 'discarded', 'unknown'])('lets a child of an %s parent ask the person', parentState => {
    const parent = makeSession({ id: 'gone', title: 'Gone parent', status: parentState === 'discarded' ? 'stopped' : 'ended', archived_at: '2026-10-19T12:00:00Z' })
    const child = makeSession({ id: 'orphan', title: 'Orphan question', parent_session_id: 'gone', status: 'waiting' })
    seed([makeDaemon()], [...(parentState === 'unknown' ? [] : [parent]), child])
    renderList()
    expect(screen.getByTestId('status-dot')).toHaveTextContent('?')
    expect(screen.getByTestId('status-dot')).toHaveAccessibleName('waiting')
    expect(screen.getByTestId('waiting-banner')).toHaveTextContent('1 session waiting for input')
    act(() => useSessionStore.setState({ sessions: [...(parentState === 'unknown' ? [] : [parent]), { ...child, status: 'idle', paused_reason: 'restart' }] }))
    expect(screen.getByTestId('status-dot')).toHaveTextContent('?')
    expect(screen.getByTestId('status-dot')).toHaveAccessibleName('paused by a restart')
  })

  it.each([0, 2])('uses a neutral glyph for a child waiting on its parent with %i tasks, then asks when the parent is archived', background_tasks => {
    const parent = makeSession({ id: 'parent', title: 'Parent', status: 'idle' })
    seed([makeDaemon()], [parent, makeSession({ id: 'child', title: 'Child question', parent_session_id: 'parent', status: 'waiting', unread: true, background_tasks })])
    renderList()
    const childCard = screen.getByText('Child question').closest('[data-session-branch="child"]')! as HTMLElement
    const glyph = within(childCard).getByTestId('status-dot')
    expect(glyph).toHaveAccessibleName('Waiting for its parent session')
    expect(glyph).toHaveAttribute('data-status', 'waiting-parent')
    expect(glyph).toHaveStyle({ color: 'var(--fog-dim)' })
    expect(glyph).not.toHaveTextContent(/[?]|Zz/)
    expect(childCard.querySelector('.blerg-runner-waiting-flash')).toBeNull()
    expect(screen.queryByTestId('waiting-banner')).toBeNull()
    act(() => useSessionStore.setState(s => ({ sessions: s.sessions.map(session => session.id === 'parent' ? { ...session, archived_at: '2026-10-19T12:00:00Z' } : session) })))
    expect(screen.getByTestId('status-dot')).toHaveTextContent('?')
    expect(screen.getByTestId('waiting-banner')).toHaveTextContent('1 session waiting for input')
  })

  it('lists a child only under its parent, and a chevron control outside the card hides and shows it', () => {
    localStorage.removeItem('blerg-runner.sidebar.childrenClosed')
    seed([makeDaemon()], [makeSession({ id: 'parent', title: 'Parent' }), makeSession({ id: 'child', title: 'Child review', status: 'idle', parent_session_id: 'parent', started_by: { kind: 'session', name: 'Parent' } })])
    renderList()
    expect(screen.getAllByText('Child review')).toHaveLength(1)
    expect(screen.getByTestId('nested-sessions')).toHaveTextContent('Child review')
    // Nested under its parent, the child does not repeat "from Parent".
    expect(screen.getByTestId('nested-sessions').querySelector('[data-testid="started-by-chip"]')).toBeNull()
    // The control is a small round chevron button in the card's second line; beside the plain "↳ 1" chip.
    const toggle = screen.getByRole('button', { name: 'Hide 1 started session' })
    expect(toggle).toHaveAttribute('title', 'Hide 1 started session')
    expect(toggle).toHaveAttribute('aria-expanded', 'true')
    expect(toggle.closest('[data-testid="children-bar"]')).toBeNull()
    expect(screen.queryByTestId('children-count')).toBeNull()
    const chip = screen.getByTestId('hidden-children')
    expect(chip.tagName).toBe('SPAN')
    expect(chip).toHaveTextContent('↳ 1')
    expect(chip).toHaveAttribute('aria-label', '1 started session')
    fireEvent.click(toggle)
    expect(screen.queryByText('Child review')).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Show 1 started session' }))
    expect(screen.getByText('Child review')).toBeInTheDocument()
  })

  it('the chevron toggles the children without opening or selecting the card', () => {
    localStorage.removeItem('blerg-runner.sidebar.childrenClosed')
    seed([makeDaemon()], [makeSession({ id: 'parent', title: 'Parent' }), makeSession({ id: 'child', title: 'Child review', status: 'idle', parent_session_id: 'parent', started_by: { kind: 'session', name: 'Parent' } })])
    render(<MemoryRouter><Routes><Route path="/sessions/:id" element={<div data-testid="opened" />} /><Route path="*" element={<SessionList variant="sidebar" />} /></Routes></MemoryRouter>)
    fireEvent.click(screen.getByTestId('children-toggle'))
    expect(screen.queryByText('Child review')).not.toBeInTheDocument()
    fireEvent.click(screen.getByTestId('children-toggle'))
    expect(screen.getByText('Child review')).toBeInTheDocument()
    expect(screen.queryByTestId('opened')).toBeNull()
    fireEvent.click(screen.getByText('Parent'))
    expect(screen.getByTestId('opened')).toBeInTheDocument()
  })

  it('a collapsed parent hides a grandchild question and leaves its parent responsible', () => {
    localStorage.setItem('blerg-runner.sidebar.childrenClosed', JSON.stringify(['parent']))
    seed([makeDaemon()], [
      makeSession({ id: 'parent', title: 'Parent' }),
      makeSession({ id: 'mid', title: 'Idle middle', status: 'idle', parent_session_id: 'parent', started_by: { kind: 'session', name: 'Parent' } }),
      makeSession({ id: 'deep', title: 'Asking grandchild', status: 'waiting', parent_session_id: 'mid', started_by: { kind: 'session', name: 'Idle middle' } }),
    ])
    renderList()
    expect(screen.queryByText('Idle middle')).not.toBeInTheDocument()
    expect(screen.queryByText('Asking grandchild')).toBeNull()
    expect(screen.queryByTestId('kept-child')).toBeNull()
    expect(screen.getByTestId('hidden-children')).toHaveAttribute('aria-label', '1 started session hidden')
    expect(screen.queryByTestId('waiting-banner')).toBeNull()
  })

  it('collapsed, then reloaded: all children stay hidden while the parent glyph reflects their work', () => {
    localStorage.removeItem('blerg-runner.sidebar.childrenClosed')
    seed([makeDaemon()], [
      makeSession({ id: 'parent', title: 'Parent' }),
      makeSession({ id: 'idle', title: 'Idle worker', status: 'idle', parent_session_id: 'parent', started_by: { kind: 'session', name: 'Parent' } }),
      makeSession({ id: 'asking', title: 'Asking worker', status: 'waiting', parent_session_id: 'parent', started_by: { kind: 'session', name: 'Parent' } }),
      makeSession({ id: 'busy', title: 'Busy worker', status: 'running', parent_session_id: 'parent', started_by: { kind: 'session', name: 'Parent' } }),
    ])
    const first = renderList()
    fireEvent.click(screen.getByRole('button', { name: 'Hide 3 started sessions' }))
    first.unmount()
    // A reload: the choice is kept.
    renderList()
    expect(screen.getByRole('button', { name: 'Show 3 started sessions' })).toHaveAttribute('aria-expanded', 'false')
    const glyph = screen.getByTestId('hidden-children')
    expect(glyph).toHaveAttribute('aria-label', '3 started sessions hidden')
    expect(glyph).toHaveTextContent('↳ 3')
    expect(screen.getByTestId('status-dot')).toHaveAttribute('title', 'running')
    for (const title of ['Idle worker', 'Asking worker', 'Busy worker']) expect(screen.queryByText(title)).toBeNull()
    expect(screen.queryByTestId('kept-child')).toBeNull()
    expect(screen.queryByTestId('waiting-banner')).toBeNull()
    expect(screen.queryByTestId('nested-sessions')).toBeNull()
  })
  it('keeps the "from {parent}" chip on a child shown on its own', () => {
    seed([makeDaemon()], [makeSession({ id: 'orphan', title: 'Lone child', parent_session_id: 'gone', started_by: { kind: 'session', name: 'Parent' } })])
    renderList()
    fireEvent.click(screen.getByRole('button', { name: /Automated/ }))
    expect(screen.queryByTestId('nested-sessions')).toBeNull()
    expect(screen.getByTestId('started-by-chip')).toHaveTextContent('from Parent')
  })
  it('keeps an archived child out of cards and waiting banners without removing its stored record',()=>{
    seed([makeDaemon()],[makeSession({id:'parent',title:'Parent'}),makeSession({id:'archived',title:'Archived child',parent_session_id:'parent',status:'waiting',archived_at:'2026-10-19T12:00:00Z'})])
    renderList()
    expect(screen.queryByText('Archived child')).toBeNull()
    expect(screen.queryByTestId('waiting-banner')).toBeNull()
    expect(useSessionStore.getState().sessions.find(session=>session.id==='archived')).toBeDefined()
    fireEvent.click(screen.getByRole('button',{name:/History/}))
    expect(screen.getByText('Archived child')).toBeInTheDocument()
    expect(screen.queryByTestId('nested-sessions')).toBeNull()
    expect(screen.queryByRole('button',{name:'Actions for Archived child'})).toBeNull()
  })
  it('searches archived children as flat History records without pulling them into active nesting',()=>{
    const archive={parent_session_id:'parent',status:'ended' as const,archived_at:'2026-10-19T12:00:00Z'}
    seed([makeDaemon()],[makeSession({id:'parent',title:'Parent',repo:'parent-app'}),makeSession({...archive,id:'needle',title:'Needle archived review',repo:'worker'}),makeSession({...archive,id:'other',title:'Unrelated archive',repo:'other'})])
    renderList()
    fireEvent.change(screen.getByPlaceholderText(/search/i),{target:{value:'needle'}})
    expect(screen.queryByText('Needle archived review')).toBeNull()
    fireEvent.click(screen.getByRole('button',{name:/History/}))
    expect(screen.getByText('Needle archived review')).toBeInTheDocument()
    expect(screen.queryByText('Unrelated archive')).toBeNull()
    expect(screen.queryByText('Parent')).toBeNull()
    expect(screen.queryByTestId('nested-sessions')).toBeNull()
  })
  it('finds a matching child while keeping its parent', () => {
    seed([makeDaemon()], [makeSession({ id: 'parent', title: 'Unrelated parent', repo: 'app' }), makeSession({ id: 'child', title: 'Needle review', parent_session_id: 'parent', started_by: { kind: 'session' } })])
    renderList()
    fireEvent.change(screen.getByPlaceholderText(/search/i), { target: { value: 'needle' } })
    expect(screen.getByText('Unrelated parent')).toBeInTheDocument()
    expect(screen.getByText('Needle review')).toBeInTheDocument()
  })
})

describe('nested sessions in sidebar sections', () => {
  beforeEach(() => localStorage.clear())
  it.each(['history', 'automated', 'starred'])('keeps the child under a parent in %s', (section) => {
    const parent = makeSession({ id: 'parent', title: 'Parent container', ...(section === 'history' ? { status: 'stopped' as const } : {}), ...(section === 'automated' ? { started_by: { kind: 'agent', name: 'Automation' } } : {}), ...(section === 'starred' ? { starred: true } : {}) })
    seed([makeDaemon()], [parent, makeSession({ id: 'child', title: 'Nested child', parent_session_id: 'parent', started_by: { kind: 'session', name: 'Parent container' }, status: 'ended' })])
    renderList()
    if (section === 'history') fireEvent.click(screen.getByRole('button', { name: /History/ }))
    if (section === 'automated') fireEvent.click(screen.getByRole('button', { name: /Automated/ }))
    expect(screen.getAllByText('Nested child')).toHaveLength(1)
    expect(screen.getByTestId('nested-sessions')).toHaveTextContent('Nested child')
  })
})


it('keeps discarded parents and their children in History, including a stale running status', () => {
  localStorage.clear()
  seed([makeDaemon()], [makeSession({id:'parent', title:'Archived parent', status:'running', archived_at:'2026-10-20T12:00:00Z'}), makeSession({id:'child', title:'Archived worker', parent_session_id:'parent', status:'running', archived_at:'2026-10-20T12:00:00Z'})])
  renderList()
  expect(screen.queryByText('Archived parent')).toBeNull()
  expect(screen.queryByText('Archived worker')).toBeNull()
  fireEvent.click(screen.getByRole('button',{name:/History/}))
  expect(screen.getAllByText('Archived parent')).toHaveLength(1)
  expect(screen.getAllByText('Archived worker')).toHaveLength(1)
})

it('discarding a running session from its sidebar card asks first, and says it is running', () => {
  localStorage.clear()
  seed([makeDaemon()], [makeSession({ id: 'parent', title: 'Busy parent', status: 'running' })])
  renderList()
  fireEvent.click(screen.getByRole('button', { name: 'Actions for Busy parent' }))
  fireEvent.click(screen.getByRole('menuitem', { name: 'Discard session and children' }))
  expect(screen.getByRole('alertdialog', { name: 'Discard session and all children?' })).toBeInTheDocument()
  expect(screen.getByTestId('discard-running')).toHaveTextContent('It is running now.')
})

it('uses the same main sessions in the expanded sidebar and collapsed tiles, preserving place and search', async () => {
 seed([makeDaemon({id:'desktop'})],[
  makeSession({id:'active',title:'Desktop work',runtime:'daemon',daemon_id:'desktop',status:'idle'}),
  makeSession({id:'pod',title:'Cluster work',runtime:'cluster',status:'running'}),
  makeSession({id:'automated',title:'Nightly work',cron_id:'cron',status:'running'}),
  makeSession({id:'ended',title:'Previous work',status:'stopped'}),
  makeSession({id:'child',title:'Nested work',parent_session_id:'active',status:'running'}),
 ])
 render(<MemoryRouter initialEntries={['/sessions/active']}><SessionList variant="sidebar"/></MemoryRouter>)
 const expanded=await screen.findByTestId('session-list')
 expect(within(expanded).getByText('Desktop work')).toBeVisible()
 expect(within(expanded).getByText('Cluster work')).toBeVisible()
 expect(within(expanded).queryByText('Nightly work')).toBeNull()
 expect(within(expanded).queryByText('Previous work')).toBeNull()
 fireEvent.click(screen.getByRole('button',{name:'Collapse session list'}))
 const tiles=screen.getByRole('group',{name:'Sessions'})
 expect(within(tiles).getAllByRole('button').map(b=>b.dataset.sessionId).sort()).toEqual(['active','pod'])
 fireEvent.click(screen.getByRole('button',{name:'Toggle session list'}))
 fireEvent.change(screen.getByTestId('search-input'),{target:{value:'Desktop'}})
 fireEvent.click(screen.getByRole('button',{name:'Collapse session list'}))
 expect(within(screen.getByRole('group',{name:'Sessions'})).getAllByRole('button').map(b=>b.dataset.sessionId)).toEqual(['active'])
})

// The structure subscription must still follow children that become roots, and the collapsed
// branch must read current state instead of the structure snapshot's older card content.
it('moves a child of an archived parent to History when it stops', () => {
 localStorage.clear()
 seed([makeDaemon()], [
  makeSession({ id: 'archived-parent', title: 'Archived parent', archived_at: '2026-10-26T00:00:00Z' }),
  makeSession({ id: 'orphan', title: 'Independent child', parent_session_id: 'archived-parent', status: 'running' }),
 ])
 renderList()
 expect(screen.getByText('Independent child')).toBeInTheDocument()
 act(() => useSessionStore.getState().updateSessionStatus('orphan', 'stopped'))
 expect(screen.queryByText('Independent child')).toBeNull()
 fireEvent.click(screen.getByRole('button', { name: /History/ }))
 expect(screen.getByText('Independent child')).toBeInTheDocument()
})
it('keeps every collapsed child hidden across live work and attention changes', () => {
 localStorage.clear()
 localStorage.setItem('blerg-runner.sidebar.childrenClosed', JSON.stringify(['parent']))
 seed([makeDaemon()], [
  makeSession({ id: 'parent', title: 'Parent', status: 'idle' }),
  makeSession({ id: 'child', title: 'Child', parent_session_id: 'parent', status: 'idle' }),
 ])
 renderList()
 expect(screen.queryByTestId('kept-child')).toBeNull()
 act(() => useSessionStore.getState().updateSessionStatus('child', 'waiting'))
 expect(screen.queryByTestId('kept-child')).toBeNull()
 expect(screen.queryByText('Child')).toBeNull()
 expect(screen.getByTestId('hidden-children')).toHaveAttribute('aria-label', '1 started session hidden')
 expect(screen.getByTestId('status-dot')).toHaveAttribute('title', 'idle')
 expect(screen.queryByTestId('waiting-banner')).toBeNull()
 act(() => useSessionStore.getState().updateSessionStatus('child', 'running'))
 expect(screen.queryByText('Child')).toBeNull()
 expect(screen.getByTestId('status-dot')).toHaveAttribute('title', 'running')
 act(() => useSessionStore.getState().updateSessionStatus('child', 'idle'))
 expect(screen.queryByTestId('kept-child')).toBeNull()
 expect(screen.queryByTestId('waiting-banner')).toBeNull()
})
