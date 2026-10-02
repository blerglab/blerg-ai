import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { MemoryRouter, Routes, Route, useLocation } from 'react-router-dom'
import { TabBar, NotificationsButton, BrandHeader } from './App'
import { useMessageStore } from './hooks/useMessageStore'
import { coreOrigin } from './authClient'
import { usePendingProposals } from './lib/proposals'

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
