import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, fireEvent, within, act } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import SessionList from './SessionList'
import { useSessionStore } from '../hooks/useSessionStore'
import { useMessageStore } from '../hooks/useMessageStore'
import { makeDaemon, makeSession } from '../test/fixtures'
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

describe('SessionList', () => {
  beforeEach(() => {
    localStorage.clear()
    useSessionStore.setState({ daemons: [], sessions: [], statusChangedAt: {}, serverVersion: '' })
    useMessageStore.setState({ messages: [], lastSeenAt: Date.now() + 9_999_999 })
  })

  it('carries the cron badge through to the list and links to the crons page in the sidebar', () => {
    seed([makeDaemon()], [
      makeSession({ id: 'a', status: 'running', title: 'From a cron', cron_id: 'c1' }),
      makeSession({ id: 'b', status: 'running', title: 'By hand' }),
    ])
    renderList({ variant: 'sidebar' })
    expect(screen.getAllByTestId('cron-badge')).toHaveLength(1)
    expect(screen.getByTestId('crons-nav')).toHaveTextContent('Crons')
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

  it('links to Insights in the sidebar, and only there', () => {
    const { unmount } = renderList({ variant: 'sidebar' })
    expect(screen.getByTestId('insights-nav')).toHaveTextContent('Insights')
    unmount()
    renderList()
    expect(screen.queryByTestId('insights-nav')).toBeNull()
  })

  it('renders active sessions grouped under their repo', () => {
    seed([makeDaemon()], [makeSession({ status: 'running', repo: 'widget', title: 'Task one' })])
    renderList()
    // Repo header is uppercased via CSS but the text node is the raw repo name.
    expect(screen.getByText('widget')).toBeInTheDocument()
    expect(screen.getByText('Task one')).toBeInTheDocument()
  })

  it('lists an active cluster session whose pod daemon is not in the daemon list', () => {
    seed([makeDaemon()], [
      makeSession({ id: 'c1', daemon_id: 'hidden-pod-daemon', status: 'idle', repo: 'widget', title: 'Cluster task' }),
    ])
    renderList()
    expect(screen.getByText('Cluster task')).toBeInTheDocument()
  })

  it('groups every no-repo session under "No repository", never a scratch folder name', () => {
    seed([makeDaemon()], [
      makeSession({ id: 's1', status: 'running', repo: '.scratch-granite-8000', title: 'Scratch one' }),
      makeSession({ id: 's2', status: 'running', repo: '.scratch-ember-01ab', title: 'Scratch two' }),
    ])
    renderList()
    expect(screen.getAllByText('No repository')).toHaveLength(1)
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

  it('tells the user how to start a daemon when none is connected', () => {
    seed([], [])
    renderList()
    expect(screen.getByText(/No daemon connected\. Start it:/)).toBeInTheDocument()
    expect(screen.getByText(/daemon\/install\.sh status/)).toBeInTheDocument()
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
    expect(screen.queryByTestId('focus-pill-row')).not.toBeInTheDocument()
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
    expect(screen.getByTestId('focus-pill-row')).toBeInTheDocument()
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

  it('non-matching repos and daemons are hidden during search', () => {
    seedSearch()
    renderList()
    fireEvent.change(screen.getByTestId('search-input'), { target: { value: 'backend' } })
    // widget repo section should be gone (no matching session)
    expect(screen.queryByText('widget')).not.toBeInTheDocument()
    // frontend repo section should be gone too
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

describe('SessionList — sort tiebreak', () => {
  beforeEach(() => {
    localStorage.clear()
    useSessionStore.setState({ daemons: [], sessions: [], statusChangedAt: {}, serverVersion: '' })
  })

  it('two same-status sessions sort newest-first by started_at', () => {
    const d = makeDaemon({ id: 'd1' })
    const sOld = makeSession({
      id: 'old', daemon_id: 'd1', repo: 'widget', title: 'Older task',
      status: 'running', started_at: '2026-01-01T00:00:00Z',
    })
    const sNew = makeSession({
      id: 'new', daemon_id: 'd1', repo: 'widget', title: 'Newer task',
      status: 'running', started_at: '2026-06-15T00:00:00Z',
    })
    useSessionStore.setState({
      daemons: [d], sessions: [sOld, sNew],
      statusChangedAt: { old: Date.now(), new: Date.now() }, serverVersion: 'test',
    })
    renderList()
    const body = document.body.textContent!
    expect(body.indexOf('Newer task')).toBeLessThan(body.indexOf('Older task'))
  })
})

describe('SessionList — starred section', () => {
  const d1 = makeDaemon({ id: 'd1', name: 'workstation', status: 'connected' })
  const d2 = makeDaemon({ id: 'd2', name: 'laptop', status: 'connected' })

  beforeEach(() => {
    localStorage.clear()
    useSessionStore.setState({ daemons: [], sessions: [], statusChangedAt: {}, serverVersion: '' })
  })

  it('starred active session appears inside starred-section and is absent from its repo group', () => {
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

    // Starred task appears exactly once (not also in the repo group)
    const allMatches = screen.getAllByText('Starred task')
    expect(allMatches).toHaveLength(1)

    // Normal task is NOT in the starred section, but IS in the repo group
    expect(within(starredSection).queryByText('Normal task')).not.toBeInTheDocument()
    expect(screen.getByText('Normal task')).toBeInTheDocument()
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

describe('SessionList — idle repos are always shown', () => {
  beforeEach(() => {
    localStorage.clear()
    useSessionStore.setState({ daemons: [], sessions: [], statusChangedAt: {}, serverVersion: '' })
  })

  it('all-idle repo shows its sessions immediately, with no collapse one-liner', () => {
    const d = makeDaemon({ id: 'd1' })
    const s1 = makeSession({ id: 's1', daemon_id: 'd1', repo: 'widget', title: 'Idle task', status: 'idle' })
    useSessionStore.setState({ daemons: [d], sessions: [s1], statusChangedAt: { s1: Date.now() }, serverVersion: 'test' })
    renderList()
    // Session is visible without any expand action — idle repos no longer auto-collapse.
    expect(screen.getByText('Idle task')).toBeInTheDocument()
    // The dashed collapse one-liner is gone entirely.
    expect(screen.queryByTestId('idle-repo-widget')).not.toBeInTheDocument()
  })

  it('active repo with a running session is shown', () => {
    const d = makeDaemon({ id: 'd1' })
    const s1 = makeSession({ id: 's1', daemon_id: 'd1', repo: 'widget', title: 'Running task', status: 'running' })
    useSessionStore.setState({ daemons: [d], sessions: [s1], statusChangedAt: { s1: Date.now() }, serverVersion: 'test' })
    renderList()
    expect(screen.getByText('Running task')).toBeInTheDocument()
  })

  it('a mix of idle and active repos all show their sessions', () => {
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
    expect(await screen.findByText(/Cluster · 2\/4 running/)).toBeInTheDocument()
    // one is killed: the pill drops at once, with no refetch needed
    act(() => {
      useSessionStore.setState({ sessions: [
        makeSession({ id: 'c1', runtime: 'cluster', status: 'stopped', daemon_id: 'pod-1' }),
        makeSession({ id: 'c2', runtime: 'cluster', status: 'running', daemon_id: 'pod-2' }),
      ] })
    })
    expect(await screen.findByText(/Cluster · 1\/4 running/)).toBeInTheDocument()
  })

  it('follows a cap saved on the Cluster page without a reload', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve({
      ok: true,
      json: () => Promise.resolve({ configured: true, max_sessions: 4, active_sessions: 0 }),
    })))
    seed([], [])
    renderList()
    expect(await screen.findByText(/Cluster · 0\/4 running/)).toBeInTheDocument()
    act(() => {
      window.dispatchEvent(new CustomEvent('blerg:cluster-settings-saved', { detail: { configured: true, max_sessions: 12 } }))
    })
    expect(await screen.findByText(/Cluster · 0\/12 running/)).toBeInTheDocument()
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
