import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { MemoryRouter, useLocation } from 'react-router-dom'
import SessionCard from './SessionCard'
import { makeDaemon, makeSession } from '../test/fixtures'
import * as wsMock from '../test/wsMock'

vi.mock('../ws', () => import('../test/wsMock'))

function LocationProbe() {
  const loc = useLocation()
  return <div data-testid="location">{loc.pathname}</div>
}

function renderCard(session = makeSession()) {
  return render(
    <MemoryRouter initialEntries={['/']}>
      <SessionCard session={session} daemon={makeDaemon()} />
      <LocationProbe />
    </MemoryRouter>,
  )
}

describe('SessionCard', () => {
  beforeEach(() => {
    wsMock.resetWsMock()
  })

  it('renders the session name and does NOT render the project path', () => {
    renderCard(makeSession({ title: 'My task', status: 'waiting', project_path: '/repos/foo' }))
    expect(screen.getByText('My task')).toBeInTheDocument()
    expect(screen.queryByText('/repos/foo')).not.toBeInTheDocument()
  })

  it('names an untitled no-repo session "No repository", not its scratch folder', () => {
    renderCard(makeSession({ title: '', status: 'idle', repo: '.scratch-granite-8000' }))
    expect(screen.getByText('No repository')).toBeInTheDocument()
    expect(screen.queryByText(/\.scratch-/)).not.toBeInTheDocument()
  })

  it('puts status + name on the first row and the chips on a second row', () => {
    renderCard(makeSession({ title: 'My task', status: 'idle', runtime: 'cluster', kind: 'agent', model: 'claude-sonnet-5' }))
    const nameRow = screen.getByText('My task').parentElement!
    expect(nameRow).toContainElement(screen.getByTestId('status-dot'))
    expect(nameRow).toContainElement(screen.getByTestId('star-toggle'))
    for (const id of ['runtime-chip', 'kind-chip', 'model-chip']) {
      expect(nameRow).not.toContainElement(screen.getByTestId(id))
      expect(screen.getByTestId(id).parentElement).not.toBe(nameRow)
    }
    expect(screen.getByTestId('runtime-chip').parentElement).toBe(screen.getByTestId('model-chip').parentElement)
  })

  it('renders a status dot', () => {
    renderCard(makeSession({ status: 'running' }))
    expect(screen.getByTestId('status-dot')).toBeInTheDocument()
  })

  it('status dot title carries the status name for accessibility', () => {
    renderCard(makeSession({ status: 'waiting' }))
    expect(screen.getByTestId('status-dot')).toHaveAttribute('title', 'waiting')
  })

  it('renders a model chip when model is set', () => {
    renderCard(makeSession({ model: 'claude-sonnet-4-6' }))
    expect(screen.getByTestId('model-chip')).toHaveTextContent('sonnet-4-6')
  })

  it('does not render effort text on the card', () => {
    renderCard(makeSession({ effort: 'low' } as Parameters<typeof makeSession>[0]))
    expect(screen.queryByText('low')).not.toBeInTheDocument()
  })

  it('shows the error reason on failed sessions', () => {
    renderCard(makeSession({ status: 'error', error_reason: 'claude not found on PATH of the daemon service' }))
    expect(screen.getByTestId('error-reason')).toHaveTextContent('claude not found on PATH')
  })

  it('applies active styling marker when active prop is set', () => {
    const { container } = render(
      <MemoryRouter initialEntries={['/']}>
        <SessionCard session={makeSession()} daemon={makeDaemon()} active />
      </MemoryRouter>,
    )
    expect(container.querySelector('[data-active="true"]')).toBeInTheDocument()
  })

  it('falls back to the repo name when there is no title', () => {
    renderCard(makeSession({ title: '', repo: 'widget' }))
    expect(screen.getByText('widget')).toBeInTheDocument()
  })

  it('navigates to the session on click', () => {
    renderCard(makeSession({ id: 'sess-xyz' }))
    fireEvent.click(screen.getByText('Build the widget'))
    expect(screen.getByTestId('location')).toHaveTextContent('/sessions/sess-xyz')
  })

  describe('status indicators', () => {
    it('running renders a spinner', () => {
      renderCard(makeSession({ status: 'running' }))
      expect(screen.getByTestId('status-dot')).toHaveClass('blerg-status-spinner')
    })

    it('waiting renders a question mark', () => {
      renderCard(makeSession({ status: 'waiting' }))
      expect(screen.getByTestId('status-dot')).toHaveTextContent('?')
    })

    it('idle renders a snoozing Zz', () => {
      renderCard(makeSession({ status: 'idle' }))
      expect(screen.getByTestId('status-dot')).toHaveTextContent('Zz')
    })
  })

  describe('unread dot', () => {
    it('an idle unread session renders the unread dot', () => {
      const { container } = renderCard(makeSession({ status: 'idle', unread: true }))
      expect(container.querySelector('[data-testid="unread-dot"]')).toBeInTheDocument()
    })

    it('a read idle session renders no unread dot', () => {
      const { container } = renderCard(makeSession({ status: 'idle', unread: false }))
      expect(container.querySelector('[data-testid="unread-dot"]')).not.toBeInTheDocument()
    })

    it('a waiting unread session has both the flash class and the unread dot', () => {
      const { container } = renderCard(makeSession({ status: 'waiting', unread: true }))
      expect(container.querySelector('.blerg-runner-waiting-flash')).toBeInTheDocument()
      expect(container.querySelector('[data-testid="unread-dot"]')).toBeInTheDocument()
    })

    it('a fully read card has neither flash class nor unread dot', () => {
      const { container } = renderCard(makeSession({ status: 'waiting', unread: false }))
      expect(container.querySelector('.blerg-runner-waiting-flash')).not.toBeInTheDocument()
      expect(container.querySelector('[data-testid="unread-dot"]')).not.toBeInTheDocument()
    })
  })

  describe('waiting flash', () => {
    it('a waiting unread card flashes', () => {
      const { container } = renderCard(makeSession({ status: 'waiting', unread: true }))
      expect(container.querySelector('.blerg-runner-waiting-flash')).toBeInTheDocument()
    })

    it('a non-waiting card never flashes', () => {
      const { container } = renderCard(makeSession({ status: 'running', unread: true }))
      expect(container.querySelector('.blerg-runner-waiting-flash')).not.toBeInTheDocument()
    })

    it('a waiting card with unread=false does not flash', () => {
      const { container } = renderCard(makeSession({ status: 'waiting', unread: false }))
      expect(container.querySelector('.blerg-runner-waiting-flash')).not.toBeInTheDocument()
    })
  })

  describe('star toggle', () => {
    it('a starred session shows the filled star glyph and data-starred="true"', () => {
      renderCard(makeSession({ starred: true }))
      const toggle = screen.getByTestId('star-toggle')
      expect(toggle).toHaveAttribute('data-starred', 'true')
      expect(toggle).toHaveTextContent('★')
    })

    it('an unstarred session shows the outline star glyph and data-starred="false"', () => {
      renderCard(makeSession({ starred: false }))
      const toggle = screen.getByTestId('star-toggle')
      expect(toggle).toHaveAttribute('data-starred', 'false')
      expect(toggle).toHaveTextContent('☆')
    })

    it('clicking the star toggle on an unstarred session sends set_session_star with starred:true', () => {
      renderCard(makeSession({ id: 'sess-xyz', starred: false }))
      fireEvent.click(screen.getByTestId('star-toggle'))
      expect(wsMock.send).toHaveBeenCalledWith({
        type: 'set_session_star',
        session_id: 'sess-xyz',
        starred: true,
      })
    })

    it('clicking the star toggle on a starred session sends set_session_star with starred:false', () => {
      renderCard(makeSession({ id: 'sess-xyz', starred: true }))
      fireEvent.click(screen.getByTestId('star-toggle'))
      expect(wsMock.send).toHaveBeenCalledWith({
        type: 'set_session_star',
        session_id: 'sess-xyz',
        starred: false,
      })
    })

    it('clicking the star toggle does NOT navigate away from the current page', () => {
      renderCard(makeSession({ id: 'sess-xyz', starred: false }))
      fireEvent.click(screen.getByTestId('star-toggle'))
      expect(screen.getByTestId('location')).toHaveTextContent('/')
    })
  })

  // Where a session runs is now an explicit choice at launch, so the list has
  // to say which one was made — "Host" in particular is the one a person must
  // be able to spot without opening the session.
  describe('runtime and kind chips', () => {
    it('labels a cluster session', () => {
      renderCard(makeSession({ runtime: 'cluster', kind: 'agent' }))
      expect(screen.getByTestId('runtime-chip')).toHaveTextContent('Cluster')
      expect(screen.getByTestId('kind-chip')).toHaveTextContent('Agent')
    })

    it('labels a sandboxed session', () => {
      renderCard(makeSession({ runtime: 'docker', kind: 'tmux' }))
      expect(screen.getByTestId('runtime-chip')).toHaveTextContent('Sandbox')
      expect(screen.getByTestId('kind-chip')).toHaveTextContent('Terminal')
    })

    it('labels an unsandboxed host session', () => {
      renderCard(makeSession({ runtime: 'daemon', kind: 'agent' }))
      expect(screen.getByTestId('runtime-chip')).toHaveTextContent('Host')
    })

    it('treats the legacy empty kind as a terminal session', () => {
      renderCard(makeSession({ kind: '' }))
      expect(screen.getByTestId('kind-chip')).toHaveTextContent('Terminal')
    })

    it('shows no runtime chip when the session predates the runtime column', () => {
      renderCard(makeSession())
      expect(screen.queryByTestId('runtime-chip')).not.toBeInTheDocument()
      expect(screen.queryByTestId('kind-chip')).not.toBeInTheDocument()
    })
  })
})
