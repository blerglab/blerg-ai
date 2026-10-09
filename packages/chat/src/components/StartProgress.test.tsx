import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen } from '@testing-library/react'
import StartProgress from './StartProgress'
import { STILL_STARTING_MS, type StartAttempt } from '../model/startStages'

const session = { id: 's1', status: 'starting' }

function attempt(over: Partial<StartAttempt> = {}): StartAttempt {
  return {
    runtime: 'cluster',
    startedAt: Date.now(),
    ready: false,
    failed: false,
    stages: [
      { id: 'queued', label: 'Queued', state: 'done' },
      { id: 'schedule', label: 'Scheduling pod', state: 'done' },
      { id: 'image', label: 'Pulling image', state: 'active', detail: 'Pulling the image and creating the container' },
      { id: 'clone', label: 'Cloning repo', state: 'pending', detail: 'org/r' },
      { id: 'ready', label: 'Ready', state: 'pending' },
    ],
    ...over,
  }
}

describe('StartProgress', () => {
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval', 'setTimeout', 'clearTimeout', 'Date'] })
    vi.setSystemTime(new Date('2026-09-26T10:00:00Z'))
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('lists every stage with its state, and details only for the active one', () => {
    render(<StartProgress attempt={attempt()} session={session} onStop={vi.fn()} />)
    const items = screen.getAllByRole('listitem')
    expect(items.map(li => li.getAttribute('data-state'))).toEqual(['done', 'done', 'active', 'pending', 'pending'])
    expect(items[2]).toHaveAttribute('aria-current', 'step')
    expect(screen.getByText('Pulling the image and creating the container')).toBeInTheDocument()
    expect(screen.queryByText('org/r')).not.toBeInTheDocument()
    expect(screen.getByText('Starting session')).toBeInTheDocument()
  })

  it('ticks the elapsed time every second', () => {
    render(<StartProgress attempt={attempt()} session={session} />)
    expect(screen.getByTestId('start-elapsed')).toHaveTextContent('0:00')
    act(() => { vi.advanceTimersByTime(42_000) })
    expect(screen.getByTestId('start-elapsed')).toHaveTextContent('0:42')
  })

  it('shows the failure reason and the next step', () => {
    const a = attempt({
      failed: true,
      stages: [
        { id: 'queued', label: 'Queued', state: 'done' },
        { id: 'image', label: 'Pulling image', state: 'failed', detail: "Can't pull the agent image (ImagePullBackOff)", hint: 'Check BLERG_RUNNER_AGENT_IMAGE' },
      ],
    })
    render(<StartProgress attempt={a} session={{ ...session, status: 'error' }} />)
    expect(screen.getByText("Couldn't start the session")).toBeInTheDocument()
    expect(screen.getByText(/ImagePullBackOff/)).toBeInTheDocument()
    expect(screen.getByTestId('start-hint')).toHaveTextContent('Check BLERG_RUNNER_AGENT_IMAGE')
    // The session has ended: the clock stops.
    const before = screen.getByTestId('start-elapsed').textContent
    act(() => { vi.advanceTimersByTime(5000) })
    expect(screen.getByTestId('start-elapsed').textContent).toBe(before)
  })

  it('a failing stage the cluster keeps retrying is not the end: clock runs, stop offered', () => {
    const a = attempt({
      failed: true,
      stages: [
        { id: 'queued', label: 'Queued', state: 'done' },
        { id: 'schedule', label: 'Scheduling pod', state: 'failed', detail: 'No node can run the pod: not enough memory', hint: 'Free capacity' },
        { id: 'ready', label: 'Ready', state: 'pending' },
      ],
    })
    render(<StartProgress attempt={a} session={session} onStop={vi.fn()} />)
    expect(screen.queryByText("Couldn't start the session")).not.toBeInTheDocument()
    expect(screen.getAllByText(/still retrying/).length).toBeGreaterThan(0)
    expect(screen.getByText(/not enough memory/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Stop session' })).toBeInTheDocument()
    act(() => { vi.advanceTimersByTime(3000) })
    expect(screen.getByTestId('start-elapsed')).toHaveTextContent('0:03')
    act(() => { vi.advanceTimersByTime(STILL_STARTING_MS) })
    expect(screen.getByTestId('start-slow')).toBeInTheDocument()
  })

  it('measures elapsed time on the server clock', () => {
    // The browser runs 30 s fast: the attempt began at server 10:00:00, and
    // "now" on the server is 10:00:05 — not 10:00:35.
    vi.setSystemTime(new Date('2026-09-26T10:00:35Z'))
    render(<StartProgress attempt={attempt({ startedAt: Date.parse('2026-09-26T10:00:00Z') })} session={session} clockOffset={30_000} />)
    expect(screen.getByTestId('start-elapsed')).toHaveTextContent('0:05')
  })

  it('says it is taking too long after two minutes, with details and stop', () => {
    render(<StartProgress attempt={attempt()} session={session} onStop={vi.fn()} />)
    expect(screen.queryByTestId('start-slow')).not.toBeInTheDocument()
    act(() => { vi.advanceTimersByTime(STILL_STARTING_MS + 1000) })
    const slow = screen.getByTestId('start-slow')
    expect(slow).toHaveTextContent('Still starting after 2:01')
    expect(slow).toHaveAttribute('role', 'alert')
    // The ticking clock must not re-announce the alert every second.
    expect(slow.querySelector('[aria-hidden="true"]')).toHaveTextContent('2:01')
    expect(screen.getByTestId('start-slow')).toHaveTextContent('Pulling image')
    fireEvent.click(screen.getByRole('button', { name: 'View details' }))
    expect(screen.getByText('org/r')).toBeInTheDocument() // pending stage detail now visible
    expect(screen.getByText(/session: s1/)).toBeInTheDocument()
  })

  it('stops the session after a confirming second click', async () => {
    const onStop = vi.fn(() => Promise.resolve())
    render(<StartProgress attempt={attempt()} session={session} onStop={onStop} />)
    fireEvent.click(screen.getByRole('button', { name: 'Stop session' }))
    expect(onStop).not.toHaveBeenCalled()
    await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Confirm stop?' })) })
    expect(onStop).toHaveBeenCalledTimes(1)
    expect(screen.queryByText(/Stop failed/)).toBeNull()
  })

  it('disarms the stop after three seconds without the second click', () => {
    const onStop = vi.fn(() => Promise.resolve())
    render(<StartProgress attempt={attempt()} session={session} onStop={onStop} />)
    fireEvent.click(screen.getByRole('button', { name: 'Stop session' }))
    expect(screen.getByRole('button', { name: 'Confirm stop?' })).toBeInTheDocument()
    act(() => { vi.advanceTimersByTime(3000) })
    expect(screen.getByRole('button', { name: 'Stop session' })).toBeInTheDocument()
    expect(onStop).not.toHaveBeenCalled()
  })

  it('says when the stop failed', async () => {
    const onStop = vi.fn(() => Promise.reject(new Error('HTTP 500')))
    render(<StartProgress attempt={attempt()} session={session} onStop={onStop} />)
    fireEvent.click(screen.getByRole('button', { name: 'Stop session' }))
    await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Confirm stop?' })) })
    expect(screen.getByText('Stop failed: HTTP 500')).toBeInTheDocument()
  })

  it('offers no stop while there is no session yet, nor without a way to stop', () => {
    const { unmount } = render(<StartProgress attempt={attempt()} onStop={vi.fn()} />)
    expect(screen.queryByRole('button', { name: 'Stop session' })).not.toBeInTheDocument()
    unmount()
    render(<StartProgress attempt={attempt()} session={session} />)
    expect(screen.queryByRole('button', { name: 'Stop session' })).not.toBeInTheDocument()
  })

  it('renders a plugins stage from data, with its label and states', () => {
    const stages: StartAttempt['stages'] = [
      { id: 'queued', label: 'Queued', state: 'done' },
      { id: 'clone', label: 'Cloning repo', state: 'done' },
      { id: 'plugins', label: 'Installing plugins', state: 'active', detail: 'Installing 2 plugin(s)' },
      { id: 'engine', label: 'Starting agent', state: 'pending' },
      { id: 'ready', label: 'Ready', state: 'pending' },
    ]
    const { rerender } = render(<StartProgress attempt={attempt({ stages })} session={session} />)
    const item = () => screen.getByText('Installing plugins').closest('li')!
    expect(item()).toHaveAttribute('data-stage', 'plugins')
    expect(item()).toHaveAttribute('data-state', 'active')
    expect(screen.getByText('Installing 2 plugin(s)')).toBeInTheDocument()
    const doneStages = stages.map(s => s.id === 'plugins'
      ? { ...s, state: 'done' as const, detail: '1 of 2 installed — frontend-design failed' }
      : s.id === 'engine' ? { ...s, state: 'active' as const } : s)
    rerender(<StartProgress attempt={attempt({ stages: doneStages })} session={session} />)
    expect(item()).toHaveAttribute('data-state', 'done')
    fireEvent.click(screen.getByText('View details'))
    expect(screen.getByText('1 of 2 installed — frontend-design failed')).toBeInTheDocument()
  })

  it('shows a warning stage with its detail up front, distinct from done', () => {
    const stages: StartAttempt['stages'] = [
      { id: 'queued', label: 'Queued', state: 'done' },
      { id: 'plugins', label: 'Installing plugins', state: 'warning', detail: "plugins skipped: your plugin list couldn't be loaded" },
      { id: 'engine', label: 'Starting agent', state: 'active' },
      { id: 'ready', label: 'Ready', state: 'pending' },
    ]
    render(<StartProgress attempt={attempt({ stages })} session={session} />)
    const item = screen.getByText('Installing plugins').closest('li')!
    expect(item).toHaveAttribute('data-state', 'warning')
    expect(item).toHaveTextContent("plugins skipped: your plugin list couldn't be loaded")
    expect(item).toHaveTextContent('done with a warning')
    expect(screen.getByText('Starting session')).toBeInTheDocument() // not a failed start
  })
})
