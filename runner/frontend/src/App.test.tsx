import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, fireEvent, waitFor, act } from '@testing-library/react'
import { MemoryRouter, Routes, Route, useLocation } from 'react-router-dom'
import App, { TabBar, BrandHeader } from './App'
import { useSessionStore } from './hooks/useSessionStore'
import { useToastStore } from './hooks/useToastStore'
import { resetProposalWarnings } from './lib/proposalAttention'
import { makeDaemon, makeSession } from './test/fixtures'

vi.mock('./ws', () => import('./test/wsMock'))
import { PushSetting } from './components/ProposalNotifications'
import { useMessageStore } from './hooks/useMessageStore'
import { coreOrigin } from './authClient'
import { usePendingProposals } from './lib/proposals'
import { setSettingsOpen, settingsIsOpen } from './lib/settingsDialog'

// TabBar uses useNavigate and useLocation — must be inside a Router.
function renderTabBar(initialPath = '/') {
  return render(
    <MemoryRouter initialEntries={[initialPath]}>
      <TabBar />
    </MemoryRouter>,
  )
}

describe('TabBar Settings', () => {
  it('has a Settings tab that opens the dialog and does not navigate', () => {
    renderTabBar('/sessions')
    fireEvent.click(screen.getByTestId('settings-tab'))
    expect(settingsIsOpen()).toBe(true)
    setSettingsOpen(false)
  })
})

describe('TabBar open-questions badge', () => {
  beforeEach(() => {
    useMessageStore.setState({ messages: [] })
  })

  it('has no Chat tab and no count badge, even when there are open messages', () => {
    useMessageStore.setState({
      messages: [
        {
          id: 'msg-1',
          session_id: 'sess-a',
          kind: 'ask',
          body: 'Question?',
          status: 'open',
          created_at: '2026-06-23T10:00:00Z',
        },
      ],
    })
    renderTabBar()
    expect(screen.queryByText('Chat')).not.toBeInTheDocument()
    expect(screen.queryByTestId('open-count-badge')).not.toBeInTheDocument()
    expect(screen.getByText('Sessions')).toBeInTheDocument()
  })

  it('hides the badge when there are no open messages', () => {
    useMessageStore.setState({
      messages: [
        {
          id: 'msg-1',
          session_id: 'sess-a',
          kind: 'update',
          body: 'Done',
          status: 'answered',
          created_at: '2026-06-23T10:00:00Z',
        },
      ],
    })
    renderTabBar()
    expect(screen.queryByTestId('open-count-badge')).not.toBeInTheDocument()
  })

  it('hides the badge when message list is empty', () => {
    renderTabBar()
    expect(screen.queryByTestId('open-count-badge')).not.toBeInTheDocument()
  })
})

describe('BrandHeader wordmark link', () => {
  it('links to coreOrigin() (which keeps the port on a desktop install), not a port-dropped origin', () => {
    render(
      <MemoryRouter initialEntries={['/']}>
        <BrandHeader />
      </MemoryRouter>,
    )
    expect(screen.getByTitle('blerg control plane')).toHaveAttribute('href', coreOrigin())
  })
})

describe('TabBar cluster visibility', () => {
  beforeEach(() => {
    useMessageStore.setState({ messages: [] })
  })

  it('hides the Cluster tab when cluster status is not configured', () => {
    render(
      <MemoryRouter initialEntries={['/']}>
        <TabBar clusterConfigured={false} />
      </MemoryRouter>,
    )
    expect(screen.queryByText('Cluster')).not.toBeInTheDocument()
  })

  it('shows the Cluster tab when cluster status is configured', () => {
    render(
      <MemoryRouter initialEntries={['/']}>
        <TabBar clusterConfigured={true} />
      </MemoryRouter>,
    )
    expect(screen.getByText('Cluster')).toBeInTheDocument()
  })
})

function LocationProbe() {
  return <div data-testid="probe">{useLocation().pathname}</div>
}

describe('TabBar crons tab', () => {
  it('always offers the Crons tab and opens /crons', () => {
    render(
      <MemoryRouter initialEntries={['/']}>
        <TabBar />
        <Routes><Route path="*" element={<LocationProbe />} /></Routes>
      </MemoryRouter>,
    )
    fireEvent.click(screen.getByRole('button', { name: 'Crons' }))
    expect(screen.getByTestId('probe')).toHaveTextContent('/crons')
  })
})

describe('TabBar proposals tab', () => {
  beforeEach(() => usePendingProposals.setState({ count: 0 }))

  it('opens /proposals', () => {
    render(
      <MemoryRouter initialEntries={['/']}>
        <TabBar />
        <Routes><Route path="*" element={<LocationProbe />} /></Routes>
      </MemoryRouter>,
    )
    fireEvent.click(screen.getByRole('button', { name: 'Proposals' }))
    expect(screen.getByTestId('probe')).toHaveTextContent('/proposals')
  })

  it('shows the pending count and hides it at zero', () => {
    usePendingProposals.setState({ count: 3 })
    const { unmount } = renderTabBar()
    expect(screen.getByTestId('proposals-count-badge')).toHaveTextContent('3')
    unmount()
    usePendingProposals.setState({ count: 0 })
    renderTabBar()
    expect(screen.queryByTestId('proposals-count-badge')).toBeNull()
  })
})

describe('notifications are opt-in', () => {
  afterEach(() => {
    // navigator.serviceWorker is a property stub, not a vi.stubGlobal — clean
    // it up by hand so it never leaks into a later test's jsdom environment.
    // @ts-expect-error test-only cleanup of a property that may not exist
    delete navigator.serviceWorker
  })

  it('does not ask for permission on mount, only after the button is clicked', async () => {
    const requestPermission = vi.fn().mockResolvedValue('denied')
    vi.stubGlobal('Notification', { requestPermission, permission: 'default' })
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: true, json: () => Promise.resolve({ configured: false }), text: () => Promise.resolve('') }))
    vi.stubGlobal('PushManager', function () {})
    Object.defineProperty(navigator, 'serviceWorker', {
      configurable: true,
      value: { register: vi.fn().mockResolvedValue({ pushManager: { subscribe: vi.fn() } }) },
    })
    render(<MemoryRouter><PushSetting /></MemoryRouter>)
    expect(requestPermission).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: 'Turn on' }))
    await waitFor(() => expect(requestPermission).toHaveBeenCalledTimes(1))
    vi.unstubAllGlobals()
  })

  it('says the browser cannot receive push, and offers no button, when it has no Service Worker / Push support', async () => {
    // The default jsdom environment: no navigator.serviceWorker, no window.PushManager.
    const requestPermission = vi.fn().mockResolvedValue('granted')
    vi.stubGlobal('Notification', { requestPermission, permission: 'default' })
    render(<MemoryRouter><PushSetting /></MemoryRouter>)
    expect(screen.getByTestId('push-state')).toHaveTextContent('This browser cannot receive push notifications')
    expect(screen.queryByRole('button', { name: 'Turn on' })).toBeNull()
    expect(requestPermission).not.toHaveBeenCalled()
    vi.unstubAllGlobals()
  })

  it('turns push off: the server forgets the subscription, the browser drops it, and it is not restored', async () => {
    const unsubscribe = vi.fn().mockResolvedValue(true)
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, status: 204, json: () => Promise.resolve({}) })
    vi.stubGlobal('Notification', { requestPermission: vi.fn(), permission: 'granted' })
    vi.stubGlobal('fetch', fetchMock)
    vi.stubGlobal('PushManager', function () {})
    Object.defineProperty(navigator, 'serviceWorker', {
      configurable: true,
      value: { getRegistration: vi.fn().mockResolvedValue({ pushManager: { getSubscription: vi.fn().mockResolvedValue({ endpoint: 'https://push.example.com/sub/1', unsubscribe }) } }) },
    })
    localStorage.setItem('blerg_notifications_wanted', '1')
    render(<MemoryRouter><PushSetting /></MemoryRouter>)
    expect(screen.getByTestId('push-state')).toHaveTextContent('On for this browser.')
    fireEvent.click(screen.getByRole('button', { name: 'Turn off' }))
    await waitFor(() => expect(screen.getByTestId('push-state')).toHaveTextContent('Off for this browser.'))
    expect(fetchMock).toHaveBeenCalledWith('/api/push/subscribe', expect.objectContaining({ method: 'DELETE', body: JSON.stringify({ endpoint: 'https://push.example.com/sub/1' }) }))
    expect(unsubscribe).toHaveBeenCalledOnce()
    expect(localStorage.getItem('blerg_notifications_wanted')).toBeNull()
    expect(screen.getByRole('button', { name: 'Turn on' })).toBeInTheDocument()
    vi.unstubAllGlobals()
  })
})

describe('the app has one Notifications bell', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
    window.history.pushState({}, '', '/')
  })

  it.each(['/sessions/sess-a', '/sessions'])('shows exactly one bell at %s, in the sessions header beside + New', async path => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({}), text: () => Promise.resolve('') })))
    useSessionStore.setState({ daemons: [makeDaemon()], sessions: [makeSession({ id: 'sess-a', kind: 'agent', status: 'running', title: 'Busy work' })], statusChangedAt: {}, serverVersion: 'test' })
    window.history.pushState({}, '', path)
    render(<App />)
    await act(async () => { for (let i = 0; i < 6; i++) await Promise.resolve() })
    const bells = screen.getAllByRole('button', { name: /^Notifications/ })
    expect(bells).toHaveLength(1)
    expect(bells[0].parentElement!.parentElement).toContainElement(screen.getByText('+ New'))
    if (path.startsWith('/sessions/')) expect(screen.getByTestId('session-header-controls')).not.toContainElement(bells[0])
  })
})

describe('proposal expiry warnings come from the app layout', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
    window.history.pushState({}, '', '/')
  })

  it('warns on a phone page with no bell mounted', async () => {
    resetProposalWarnings()
    useToastStore.setState({ toasts: [] })
    vi.stubGlobal('matchMedia', vi.fn().mockImplementation((query: string) => ({
      matches: true, media: query, onchange: null,
      addEventListener: () => {}, removeEventListener: () => {}, addListener: () => {}, removeListener: () => {}, dispatchEvent: () => false,
    })))
    const expiring = { id: 'p1', state: 'pending', kind: 'board_file', connection_id: '', connection_name: 'Board', tool: 'file_ticket', arguments: { title: 'Card' }, arguments_raw: '{}', agent_summary: '', session_id: 'sess-a', cron_id: null, created_at: new Date().toISOString(), expires_at: new Date(Date.now() + 3600_000).toISOString(), decided_at: null, decided_by: null, result: null }
    vi.stubGlobal('fetch', vi.fn((url: string) => Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(String(url).startsWith('/api/proposals') ? { proposals: [expiring] } : {}), text: () => Promise.resolve('') })))
    useSessionStore.setState({ daemons: [makeDaemon()], sessions: [], statusChangedAt: {}, serverVersion: 'test' })
    window.history.pushState({}, '', '/crons')
    render(<App />)
    await waitFor(() => expect(useToastStore.getState().toasts.map(t => t.title)).toContain('Proposal expires soon'))
    expect(screen.queryAllByRole('button', { name: /^Notifications/ })).toHaveLength(0)
  })
})
