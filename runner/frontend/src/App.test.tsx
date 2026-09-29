import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { TabBar, NotificationsButton, BrandHeader } from './App'
import { useMessageStore } from './hooks/useMessageStore'
import { coreOrigin } from './authClient'

// TabBar uses useNavigate and useLocation — must be inside a Router.
function renderTabBar(initialPath = '/') {
  return render(
    <MemoryRouter initialEntries={[initialPath]}>
      <TabBar />
    </MemoryRouter>,
  )
}

describe('TabBar open-questions badge', () => {
  beforeEach(() => {
    useMessageStore.setState({ messages: [] })
  })

  it('shows a count badge on the Chat tab when there are open messages', () => {
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
        {
          id: 'msg-2',
          session_id: 'sess-b',
          kind: 'note',
          body: 'Side thought',
          status: 'open',
          created_at: '2026-06-23T10:01:00Z',
        },
        {
          id: 'msg-3',
          session_id: 'sess-a',
          kind: 'update',
          body: 'Done',
          status: 'answered',
          created_at: '2026-06-23T10:02:00Z',
        },
      ],
    })
    renderTabBar()
    const badge = screen.getByTestId('open-count-badge')
    expect(badge).toBeInTheDocument()
    expect(badge).toHaveTextContent('2')
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
    render(<MemoryRouter><NotificationsButton /></MemoryRouter>)
    expect(requestPermission).not.toHaveBeenCalled()
    fireEvent.click(screen.getByText('Enable notifications'))
    await waitFor(() => expect(requestPermission).toHaveBeenCalledTimes(1))
    vi.unstubAllGlobals()
  })

  it('does not ask for permission at all when the browser has no Service Worker / Push support', async () => {
    // No stubbing of navigator.serviceWorker / window.PushManager here — this
    // is the default jsdom environment, i.e. exactly the unsupported case the
    // capability check exists to guard: never prompt for a permission the
    // app could not act on anyway (no Service Worker to register, no push
    // subscription possible).
    const requestPermission = vi.fn().mockResolvedValue('granted')
    vi.stubGlobal('Notification', { requestPermission, permission: 'default' })
    render(<MemoryRouter><NotificationsButton /></MemoryRouter>)
    fireEvent.click(screen.getByText('Enable notifications'))
    // Give any (incorrect) async call a microtask turn to happen before asserting it didn't.
    await Promise.resolve()
    await Promise.resolve()
    expect(requestPermission).not.toHaveBeenCalled()
    vi.unstubAllGlobals()
  })
})
