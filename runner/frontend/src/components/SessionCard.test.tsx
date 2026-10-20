import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { MemoryRouter, useLocation } from 'react-router-dom'
import SessionCard from './SessionCard'
import { makeDaemon, makeSession } from '../test/fixtures'
import * as wsMock from '../test/wsMock'

const apiFetch=vi.hoisted(()=>vi.fn())
vi.mock('../apiFetch',()=>({apiFetch}))

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
    apiFetch.mockReset()
  })

  it('offers confirmed discard only in a child card menu and waits for authoritative data',async()=>{
    apiFetch.mockResolvedValue({ok:true})
    renderCard(makeSession({id:'child /',parent_session_id:'parent /',title:'Review child'}))
    fireEvent.click(screen.getByRole('button',{name:'Actions for Review child'}))
    fireEvent.click(screen.getByRole('menuitem',{name:'Discard child session'}))
    expect(screen.getByRole('dialog',{name:'Discard child session?'})).toBeInTheDocument()
    expect(screen.getByTestId('location')).toHaveTextContent('/')
    expect(apiFetch).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button',{name:'Discard child session'}))
    await waitFor(()=>expect(apiFetch).toHaveBeenCalledWith('/api/sessions/parent%20%2F/children/child%20%2F/discard',{method:'POST'}))
    expect(screen.getByText('Review child')).toBeInTheDocument()
    expect(screen.getByTestId('location')).toHaveTextContent('/')
  })
  it('offers a distinct confirmed parent discard and explains the cascade', async()=>{
    apiFetch.mockResolvedValue({ok: true,json:async()=>({status:'discarded',session_id:'parent /',archived_at:'2026-10-20T12:00:00Z'})})
    renderCard(makeSession({id:'parent /',title:'Parent'}))
    fireEvent.click(screen.getByRole('button',{name:'Actions for Parent'}))
    expect(screen.queryByRole('menuitem',{name:'Discard child session'})).toBeNull()
    fireEvent.click(screen.getByRole('menuitem',{name:'Discard session and children'}))
    expect(screen.getByRole('dialog',{name:'Discard session and all children?'})).toBeInTheDocument()
    expect(screen.getByText(/including children marked Keep running and work in progress/)).toBeInTheDocument()
    expect(screen.getByText(/conversations, files, history and insights remain available/)).toBeInTheDocument()
    expect(apiFetch).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button',{name:'Discard session and children'}))
    await waitFor(()=>expect(apiFetch).toHaveBeenCalledWith('/api/sessions/parent%20%2F/discard',{method:'POST'}))
    expect(screen.getByTestId('location')).toHaveTextContent('/')
    expect(screen.getByText('Parent')).toBeInTheDocument()
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
    renderCard(makeSession({ title: 'My task', status: 'idle', runtime: 'cluster', kind: 'tmux', model: 'claude-sonnet-5' }))
    const nameRow = screen.getByText('My task').parentElement!
    expect(nameRow).toContainElement(screen.getByTestId('status-dot'))
    expect(nameRow).toContainElement(screen.getByTestId('star-toggle'))
    for (const id of ['runtime-chip', 'kind-chip', 'model-chip']) {
      expect(nameRow).not.toContainElement(screen.getByTestId(id))
      expect(screen.getByTestId(id).parentElement).not.toBe(nameRow)
    }
    expect(screen.getByTestId('runtime-chip').parentElement).toBe(screen.getByTestId('model-chip').parentElement)
  })

  it('shows how many background tasks a running session has, on the chips row', () => {
    renderCard(makeSession({ title: 'My task', status: 'running', background_tasks: 2 }))
    const chip = screen.getByTestId('background-chip')
    expect(chip.textContent).toBe('\u23F12')
    expect(chip).toHaveAttribute('title', '2 background tasks running')
    expect(screen.getByText('My task').parentElement).not.toContainElement(chip)
  })

  it('an idle session with background tasks shows the background state: the ⏱ count in the status slot, once, words only in its name', () => {
    renderCard(makeSession({ title: 'My task', status: 'idle', background_tasks: 2 }))
    const glyph = screen.getByTestId('status-dot')
    expect(glyph).toHaveAttribute('data-status', 'background')
    expect(glyph.textContent).toBe('\u23F12')
    expect(glyph).toHaveAttribute('title', 'working in background, 2 tasks')
    expect(screen.getByRole('img', { name: 'working in background, 2 tasks' })).toBe(glyph)
    expect(screen.getByText('My task').parentElement).toContainElement(glyph)
    // Not idle's Zz, no second ⏱ chip, and no words on the card.
    expect(glyph).not.toHaveTextContent('Zz')
    expect(screen.queryByTestId('background-chip')).toBeNull()
    expect(screen.queryByText(/working in background/)).toBeNull()
  })

  it('gives every status glyph the same slot, so the names line up', () => {
    const widths: string[] = []
    for (const over of [{ status: 'idle' as const }, { status: 'waiting' as const }, { status: 'running' as const }, { status: 'idle' as const, background_tasks: 12 }, { status: 'stopped' as const }]) {
      const { unmount } = renderCard(makeSession(over))
      widths.push(screen.getByTestId('status-slot').style.width)
      unmount()
    }
    expect(new Set(widths)).toEqual(new Set(['22px']))
  })

  it('reads a count past nine as "9+", in the slot and on the chip', () => {
    const { unmount } = renderCard(makeSession({ status: 'idle', background_tasks: 12 }))
    expect(screen.getByTestId('status-dot').textContent).toBe('\u23F19+')
    expect(screen.getByTestId('status-dot')).toHaveAttribute('title', 'working in background, 12 tasks')
    unmount()
    renderCard(makeSession({ status: 'running', background_tasks: 12 }))
    expect(screen.getByTestId('background-chip').textContent).toBe('\u23F19+')
  })

  it('keeps the second line when a session with no chips goes into the background, so the card keeps its height', () => {
    const bare = { title: '', repo: 'widget', kind: undefined, runtime: undefined, model: undefined }
    const { container, rerender } = renderCard(makeSession({ ...bare, status: 'running', background_tasks: 1 }))
    const lines = () => (container.firstElementChild as HTMLElement).children.length
    expect(lines()).toBe(2)
    rerender(
      <MemoryRouter initialEntries={['/']}>
        <SessionCard session={makeSession({ ...bare, status: 'idle', background_tasks: 1 })} daemon={makeDaemon()} />
      </MemoryRouter>,
    )
    expect(screen.getByTestId('status-dot')).toHaveAttribute('data-status', 'background')
    expect(screen.queryByTestId('background-chip')).toBeNull()
    expect(lines()).toBe(2)
  })

  it('names one background task in the singular, and shows none for an ended session or a count of 0', () => {
    const { unmount } = renderCard(makeSession({ status: 'running', background_tasks: 1 }))
    expect(screen.getByTestId('background-chip')).toHaveAttribute('title', '1 background task running')
    unmount()
    const second = renderCard(makeSession({ status: 'stopped', background_tasks: 3 }))
    expect(screen.queryByTestId('background-chip')).toBeNull()
    second.unmount()
    renderCard(makeSession({ status: 'idle', background_tasks: 0 }))
    expect(screen.queryByTestId('background-chip')).toBeNull()
    expect(screen.getByTestId('status-dot')).toHaveTextContent('Zz')
  })

  it('renders a status dot', () => {
    renderCard(makeSession({ status: 'running' }))
    expect(screen.getByTestId('status-dot')).toBeInTheDocument()
  })

  it('status dot title carries the status name for accessibility', () => {
    renderCard(makeSession({ status: 'waiting' }))
    expect(screen.getByTestId('status-dot')).toHaveAttribute('title', 'waiting')
  })

  it('shows a session a restart left paused with the waiting "?", its tooltip saying why', () => {
    const { unmount } = renderCard(makeSession({ status: 'idle', paused_reason: 'restart' }))
    const glyph = screen.getByTestId('status-dot')
    expect(glyph).toHaveTextContent('?')
    expect(glyph).toHaveAttribute('title', 'paused by a restart')
    unmount()
    // Running again: its own glyph, whatever the row still says.
    renderCard(makeSession({ status: 'running', paused_reason: 'restart' }))
    expect(screen.getByTestId('status-dot')).not.toHaveAttribute('title', 'paused by a restart')
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

  it('says its org/repo in small muted text on the chips row, and nothing for a no-repo session', () => {
    const { unmount } = renderCard(makeSession({ title: 'My task', repo: 'blerglab/blerg', runtime: 'cluster' }))
    const repo = screen.getByTestId('session-repo')
    expect(repo).toHaveTextContent('blerglab/blerg')
    expect(repo.title).toBe('blerglab/blerg')
    expect(repo.parentElement).toBe(screen.getByTestId('runtime-chip').parentElement)
    expect(repo).toHaveStyle({ color: 'var(--fog-dim)' })
    // Two lines at most: the repo and the chips share one line that never wraps; the repo gives
    // way (ellipsis, full name in its title), the chips keep their size.
    expect(repo.parentElement).toHaveStyle({ flexWrap: 'nowrap', overflow: 'hidden' })
    expect(repo).toHaveStyle({ flex: '0 1 auto', minWidth: '0', textOverflow: 'ellipsis', whiteSpace: 'nowrap' })
    // The chips keep their size: only the repo gives way, so a chip is never cut at the card's edge.
    expect(screen.getByTestId('runtime-chip')).toHaveStyle({ flexShrink: '0' })
    unmount()
    for (const r of ['', '.scratch-granite-8000']) {
      const { unmount: u } = renderCard(makeSession({ title: 'My task', repo: r }))
      expect(screen.queryByTestId('session-repo')).toBeNull()
      u()
    }
    // An untitled session is named by its repo already: no second line saying it again.
    renderCard(makeSession({ title: '', repo: 'widget' }))
    expect(screen.getAllByText('widget')).toHaveLength(1)
    expect(screen.queryByTestId('session-repo')).toBeNull()
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
    it('running renders a spinner', async () => {
      renderCard(makeSession({ status: 'running' }))
      expect(screen.getByTestId('status-dot')).toHaveClass('blerg-status-spinner')
      // The class is styled: a running card with an unstyled spinner shows no glyph at all.
      // (The app's tsconfig has no Node types: the file system comes in untyped.)
      const fs = (await import(/* @vite-ignore */ 'node:' + 'fs')) as { readFileSync(p: string, enc: 'utf8'): string }
      const appCss = fs.readFileSync('src/index.css', 'utf8') // vitest runs from runner/frontend
      expect(appCss).toMatch(/\.blerg-status-spinner\s*\{[^}]*animation:\s*blerg-spin/)
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
      // An agent session is the default kind: no chip says so (Terminal, the exception, does).
      expect(screen.queryByTestId('kind-chip')).not.toBeInTheDocument()
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
  describe('cron badge', () => {
    it('marks a session a cron started', () => {
      renderCard(makeSession({ cron_id: 'c1' }))
      expect(screen.getByTestId('cron-badge')).toHaveTextContent('cron')
    })

    it('shows no badge on an ordinary session', () => {
      renderCard(makeSession())
      expect(screen.queryByTestId('cron-badge')).not.toBeInTheDocument()
    })
  })
})
