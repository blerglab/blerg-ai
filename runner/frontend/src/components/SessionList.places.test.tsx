import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, fireEvent, within } from '@testing-library/react'
import { MemoryRouter, Routes, Route } from 'react-router-dom'
import SessionList from './SessionList'
import { useSessionStore } from '../hooks/useSessionStore'
import { useMessageStore } from '../hooks/useMessageStore'
import { makeDaemon, makeSession } from '../test/fixtures'
import { setFocus } from '../lib/sidebarPrefs'

vi.mock('../ws', () => import('../test/wsMock'))

const ws = makeDaemon({ id: 'd1', name: 'workstation', mode: 'local', version: 'test' })
const pod = makeDaemon({ id: 'runner-1f3a9c', name: 'runner-1f3a9c', mode: 'runner', version: 'test' })

function seed(daemons = [ws], sessions = [makeSession()]) {
  useSessionStore.setState({
    daemons,
    sessions,
    statusChangedAt: Object.fromEntries(sessions.map(s => [s.id, Date.now()])),
    serverVersion: 'test',
  })
}

function stubCluster(status: Record<string, unknown> | null) {
  vi.stubGlobal('fetch', vi.fn((url: string) => {
    if (url === '/api/cluster/status' && status) {
      return Promise.resolve({ ok: true, json: () => Promise.resolve(status) })
    }
    return Promise.resolve({ ok: true, json: () => Promise.resolve({}) })
  }))
}

function renderList(props = {}) {
  return render(
    <MemoryRouter initialEntries={['/']}>
      <Routes>
        <Route path="/" element={<SessionList {...props} />} />
        <Route path="/cluster" element={<div data-testid="cluster-page" />} />
      </Routes>
    </MemoryRouter>,
  )
}

describe('SessionList — where sessions run', () => {
  beforeEach(() => {
    localStorage.clear()
    useSessionStore.setState({ daemons: [], sessions: [], statusChangedAt: {}, serverVersion: '' })
    useMessageStore.setState({ messages: [] })
  })
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('never lists a cluster session pod (daemon mode runner) as a machine', () => {
    seed([ws, pod, makeDaemon({ id: 'runner-b', name: 'runner-b', mode: 'runner' })], [])
    renderList()
    expect(screen.queryByText(/runner-1f3a9c/)).not.toBeInTheDocument()
    expect(screen.queryByText(/runner-b/)).not.toBeInTheDocument()
    expect(screen.getByTestId('focus-pill-d1')).toHaveTextContent('workstation')
    // A lone workstation daemon is one place to run: nothing to filter, so no All.
    expect(screen.queryByTestId('focus-pill-all')).not.toBeInTheDocument()
  })

  it('shows only the install hint when nothing is configured, even with pods in the store', () => {
    seed([pod], [])
    renderList()
    expect(screen.getByText(/No daemon connected\. Start it:/)).toBeInTheDocument()
    expect(screen.queryByText(/runner-1f3a9c/)).not.toBeInTheDocument()
  })

  it('shows one Cluster pill with active/max counts from the status', async () => {
    stubCluster({ configured: true, max_sessions: 5, active_sessions: 2, available_engines: [] })
    seed([], [
      makeSession({ id: 'c1', runtime: 'cluster', daemon_id: 'runner-x', status: 'running' }),
      makeSession({ id: 'c2', runtime: 'cluster', daemon_id: 'runner-y', status: 'idle' }),
    ])
    renderList()
    const pill = await screen.findByTestId('focus-pill-cluster')
    expect(pill).toHaveTextContent('Cluster 2/5')
    expect(pill.title).toContain('2 of 5 cluster sessions running')
    expect(screen.queryByText(/No daemon connected/)).not.toBeInTheDocument()
  })

  it('counts the active cluster sessions itself when the status has no active count', async () => {
    stubCluster({ configured: true, max_sessions: 4 })
    seed([], [
      makeSession({ id: 'c1', runtime: 'cluster', daemon_id: 'runner-x', status: 'running' }),
      makeSession({ id: 'c2', runtime: 'cluster', daemon_id: 'runner-y', status: 'idle' }),
      makeSession({ id: 'c3', runtime: 'cluster', daemon_id: 'runner-z', status: 'stopped' }),
      makeSession({ id: 'h1', runtime: 'daemon', daemon_id: 'd1', status: 'running' }),
    ])
    renderList()
    expect(await screen.findByTestId('focus-pill-cluster')).toHaveTextContent('Cluster 2/4')
  })

  it('the Cluster pill links to the Cluster page when it is the only place (nothing to filter)', async () => {
    stubCluster({ configured: true, max_sessions: 5, active_sessions: 0 })
    seed([], [])
    renderList()
    fireEvent.click(await screen.findByTestId('focus-pill-cluster'))
    expect(screen.getByTestId('cluster-page')).toBeInTheDocument()
  })

  it('lists the cluster pill and every workstation daemon, with a differing version highlighted', async () => {
    stubCluster({ configured: true, max_sessions: 5, active_sessions: 1 })
    seed([
      makeDaemon({ id: 'd1', name: 'workstation', mode: 'local', version: 'test' }),
      makeDaemon({ id: 'd2', name: 'laptop', mode: 'local', version: 'old' }),
      pod,
    ], [])
    renderList()
    await screen.findByTestId('focus-pill-cluster')
    const strip = screen.getByTestId('runtime-strip')
    expect(within(strip).getByTestId('focus-pill-d1')).toHaveTextContent('workstation')
    expect(within(strip).getByTestId('focus-pill-d1').title).toContain('local')
    // A version is shown only when it differs from the server's: then it warns.
    expect(within(strip).getByText('old')).toHaveStyle({ color: 'var(--blaze)' })
    expect(within(strip).queryByText('test')).not.toBeInTheDocument()
    expect(within(strip).queryByText(/runner-/)).not.toBeInTheDocument()
  })

  it('puts All, the cluster and every machine in ONE row of pills that show status and filter, with no separate label or focus row', async () => {
    stubCluster({ configured: true, max_sessions: 5, active_sessions: 1 })
    seed([ws, makeDaemon({ id: 'd2', name: 'laptop', status: 'disconnected' }), pod], [])
    renderList()
    await screen.findByTestId('focus-pill-cluster')
    const row = screen.getByTestId('place-pills')
    expect(row).toHaveAttribute('aria-label', 'Where sessions run')
    expect([...row.children].map(c => c.getAttribute('data-testid'))).toEqual(['focus-pill-all', 'focus-pill-cluster', 'focus-pill-d1', 'focus-pill-d2'])
    expect(screen.queryByText(/where sessions run/i)).not.toBeInTheDocument()
    expect(screen.queryByTestId('focus-pill-row')).not.toBeInTheDocument()
    expect(screen.getByTestId('focus-pill-all')).toHaveAttribute('aria-pressed', 'true')
    // A machine that is away shows its muted dot and is not a filter.
    expect(screen.getByTestId('focus-pill-d2')).toBeDisabled()
    fireEvent.click(screen.getByTestId('focus-pill-d1'))
    expect(screen.getByTestId('focus-pill-d1')).toHaveAttribute('aria-pressed', 'true')
    expect(screen.getByTestId('focus-pill-all')).toHaveAttribute('aria-pressed', 'false')
  })

  it('shows a meter row per reporting provider under the pills, and no strip of meters when none reports', () => {
    seed([ws], [])
    const { unmount } = renderList()
    expect(screen.queryByTestId('usage-meters')).toBeNull()
    unmount()
    seed([{ ...ws, usage_limits: [{ provider: 'codex', windows: [{ id: '5h', used_percent: 75 }, { id: '7d', used_percent: 12 }] }] }, pod], [])
    renderList()
    const strip = screen.getByTestId('runtime-strip')
    const meters = within(strip).getByTestId('usage-meters')
    // Directly beneath the row of pills.
    expect(screen.getByTestId('place-pills').nextElementSibling).toBe(meters)
    expect(within(meters).getByTestId('usage-row-codex')).toHaveTextContent('Codex5h75%week12%')
    expect(within(meters).getByTestId('usage-window-5h')).toHaveClass('usage-meter--warning')
  })

  it('the strip wraps instead of scrolling sideways', () => {
    seed([ws], [])
    renderList()
    const row = screen.getByTestId('place-pills')
    expect(row.style.flexWrap).toBe('wrap')
    expect(row.style.overflowX).toBe('')
  })
})

describe('SessionList — focus across runtimes', () => {
  const widgetHost = makeSession({ id: 'h1', daemon_id: 'd1', runtime: 'daemon', repo: 'widget', title: 'Widget on host', status: 'running' })
  const widgetCluster = makeSession({ id: 'c1', daemon_id: 'runner-1f3a9c', runtime: 'cluster', repo: 'widget', title: 'Widget on cluster', status: 'running' })
  const otherCluster = makeSession({ id: 'c2', daemon_id: 'runner-77', runtime: 'cluster', repo: 'backend', title: 'Backend on cluster', status: 'idle' })

  beforeEach(() => {
    localStorage.clear()
    useSessionStore.setState({ daemons: [], sessions: [], statusChangedAt: {}, serverVersion: '' })
    useMessageStore.setState({ messages: [] })
    stubCluster({ configured: true, max_sessions: 5, active_sessions: 2 })
  })
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('lists every runtime\'s sessions in one list, each card keeping its repo line and runtime chip', async () => {
    seed([ws, pod], [widgetHost, widgetCluster, otherCluster])
    renderList()
    await screen.findByTestId('focus-pill-cluster')
    expect(screen.getAllByTestId('session-repo').map(r => r.textContent).sort()).toEqual(['backend', 'widget', 'widget'])
    // No per-daemon section header is left behind.
    expect(screen.queryByText('workstation', { selector: 'span[style*="uppercase"]' })).not.toBeInTheDocument()
    expect(screen.getByText('Widget on host')).toBeInTheDocument()
    expect(screen.getByText('Widget on cluster')).toBeInTheDocument()
    const chips = screen.getAllByTestId('runtime-chip').map(c => c.textContent)
    expect(chips.sort()).toEqual(['Cluster', 'Cluster', 'Host'])
  })

  it('offers All / Cluster / each workstation daemon when there is more than one place', async () => {
    seed([ws, pod], [widgetHost, widgetCluster])
    renderList()
    await screen.findByTestId('focus-pill-cluster')
    expect(screen.getByTestId('focus-pill-all')).toBeInTheDocument()
    expect(screen.getByTestId('focus-pill-d1')).toBeInTheDocument()
    expect(screen.queryByTestId('focus-pill-runner-1f3a9c')).not.toBeInTheDocument()
  })

  it('the Cluster focus shows only cluster sessions and persists as "cluster"', async () => {
    seed([ws, pod], [widgetHost, widgetCluster, otherCluster])
    renderList()
    fireEvent.click(await screen.findByTestId('focus-pill-cluster'))
    expect(screen.queryByText('Widget on host')).not.toBeInTheDocument()
    expect(screen.getByText('Widget on cluster')).toBeInTheDocument()
    expect(screen.getByText('Backend on cluster')).toBeInTheDocument()
    expect(localStorage.getItem('blerg-runner.sidebar.focus')).toBe(JSON.stringify('cluster'))
  })

  it('a daemon focus shows that daemon\'s sessions and not the cluster ones', async () => {
    seed([ws, pod], [widgetHost, widgetCluster, otherCluster])
    renderList()
    // The daemon's pill is a filter once the cluster makes it one of two places.
    await screen.findByTestId('focus-pill-all')
    fireEvent.click(screen.getByTestId('focus-pill-d1'))
    expect(screen.getByText('Widget on host')).toBeInTheDocument()
    expect(screen.queryByText('Widget on cluster')).not.toBeInTheDocument()
    expect(screen.queryByText('Backend on cluster')).not.toBeInTheDocument()
  })

  it('a stored cluster focus is honoured on mount', async () => {
    setFocus('cluster')
    seed([ws], [widgetHost, widgetCluster])
    renderList()
    await screen.findByTestId('focus-pill-cluster')
    expect(screen.queryByText('Widget on host')).not.toBeInTheDocument()
    expect(screen.getByText('Widget on cluster')).toBeInTheDocument()
  })

  it('a stored cluster focus falls back to All when the cluster is not configured', () => {
    stubCluster({ configured: false })
    setFocus('cluster')
    seed([ws], [widgetHost, widgetCluster])
    renderList()
    expect(screen.getByText('Widget on host')).toBeInTheDocument()
    expect(screen.getByText('Widget on cluster')).toBeInTheDocument()
  })

  it('a stored focus on a daemon that is gone falls back to All', async () => {
    setFocus('vanished')
    seed([ws], [widgetHost, widgetCluster])
    renderList()
    await screen.findByTestId('focus-pill-cluster')
    expect(screen.getByText('Widget on host')).toBeInTheDocument()
    expect(screen.getByText('Widget on cluster')).toBeInTheDocument()
  })

  it('search spans every runtime regardless of the focus', async () => {
    setFocus('cluster')
    seed([ws], [widgetHost, widgetCluster])
    renderList()
    await screen.findByTestId('focus-pill-cluster')
    fireEvent.change(screen.getByTestId('search-input'), { target: { value: 'host' } })
    expect(screen.getByText('Widget on host')).toBeInTheDocument()
  })
})
