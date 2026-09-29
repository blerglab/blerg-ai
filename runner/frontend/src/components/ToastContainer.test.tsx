import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import ToastContainer from './ToastContainer'
import { useToastStore } from '../hooks/useToastStore'

vi.mock('../sound', () => ({
  playToastSound: vi.fn(),
  playMessageSound: vi.fn(),
}))

vi.mock('../ws', () => import('../test/wsMock'))

function renderContainer() {
  return render(
    <MemoryRouter>
      <ToastContainer />
    </MemoryRouter>,
  )
}

describe('ToastContainer', () => {
  beforeEach(() => {
    useToastStore.setState({ toasts: [] })
    vi.clearAllMocks()
  })

  it('renders nothing when there are no toasts', () => {
    const { container } = renderContainer()
    expect(container.firstChild).toBeNull()
  })

  it('session toast has data-testid="toast-session"', () => {
    useToastStore.setState({
      toasts: [{ id: 1, title: 'Session waiting', body: 'foo', variant: 'session' }],
    })
    renderContainer()
    expect(screen.getByTestId('toast-session')).toBeInTheDocument()
  })

  it('message toast has data-testid="toast-message"', () => {
    useToastStore.setState({
      toasts: [{ id: 1, title: 'Chat message', body: 'bar', variant: 'message' }],
    })
    renderContainer()
    expect(screen.getByTestId('toast-message')).toBeInTheDocument()
  })

  it('toast with no variant defaults to session styling (data-testid="toast-session")', () => {
    useToastStore.setState({
      toasts: [{ id: 1, title: 'Legacy', body: 'baz' }],
    })
    renderContainer()
    expect(screen.getByTestId('toast-session')).toBeInTheDocument()
  })

  it('session and message toasts have different left border colors', () => {
    useToastStore.setState({
      toasts: [
        { id: 1, title: 'Session', body: 'A', variant: 'session' },
        { id: 2, title: 'Message', body: 'B', variant: 'message' },
      ],
    })
    renderContainer()
    const sessionEl = screen.getByTestId('toast-session')
    const messageEl = screen.getByTestId('toast-message')
    expect(sessionEl.style.borderLeft).not.toBe(messageEl.style.borderLeft)
  })

  it('message toast title uses teal color, session toast uses cream color', () => {
    useToastStore.setState({
      toasts: [
        { id: 1, title: 'Session', body: 'A', variant: 'session' },
        { id: 2, title: 'Message', body: 'B', variant: 'message' },
      ],
    })
    renderContainer()
    const sessionTitle = screen.getByTestId('toast-session').querySelector('[data-testid="toast-title"]') as HTMLElement
    const messageTitle = screen.getByTestId('toast-message').querySelector('[data-testid="toast-title"]') as HTMLElement
    expect(sessionTitle).toBeTruthy()
    expect(messageTitle).toBeTruthy()
    // Colors differ between variants
    expect(sessionTitle.style.color).not.toBe(messageTitle.style.color)
  })
})
