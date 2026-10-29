import { describe, it, expect, vi, beforeEach } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
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

  it('lets a person dismiss every variant without navigating or running its action', () => {
    const action = vi.fn()
    useToastStore.setState({ toasts: [{ id: 1, title: 'Notice', body: 'Details', url: '/sessions/other', action: { label: 'Undo', onClick: action } }] })
    renderContainer()
    fireEvent.click(screen.getByRole('button', { name: 'Dismiss notification' }))
    expect(useToastStore.getState().toasts).toEqual([])
    expect(action).not.toHaveBeenCalled()
    expect(screen.queryByRole('region', { name: 'Notifications' })).toBeNull()
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

  it('session and message toasts use one surface', () => {
    useToastStore.setState({
      toasts: [
        { id: 1, title: 'Session', body: 'A', variant: 'session' },
        { id: 2, title: 'Message', body: 'B', variant: 'message' },
      ],
    })
    renderContainer()
    const sessionEl = screen.getByTestId('toast-session')
    const messageEl = screen.getByTestId('toast-message')
    expect(sessionEl.className).toBe('message-toast')
    expect(messageEl.className).toBe(sessionEl.className)
  })

  it('message and session titles use the popover typography', () => {
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
    expect(sessionTitle.className).toBe('confirm-popover-title')
    expect(messageTitle.className).toBe(sessionTitle.className)
  })
})
