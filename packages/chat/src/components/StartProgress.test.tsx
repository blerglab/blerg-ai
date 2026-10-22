import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, within } from '@testing-library/react'
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

  // Card #142: a confirmation anchored to the button, never a second press on the same button.
  it('asks in an alertdialog, focused on Cancel, and ends the session on End session', async () => {
    const onStop = vi.fn(() => Promise.resolve())
    render(<StartProgress attempt={attempt()} session={session} onStop={onStop} />)
    const stop = screen.getByRole('button', { name: 'Stop session' })
    fireEvent.click(stop)
    expect(onStop).not.toHaveBeenCalled()
    // The button itself never arms: same words, no second-press state.
    expect(stop).toHaveTextContent('Stop session')
    expect(stop.className).not.toContain('armed')
    const dialog = screen.getByRole('alertdialog', { name: 'End this session?' })
    expect(dialog).toHaveAttribute('aria-describedby')
    expect(document.getElementById(dialog.getAttribute('aria-describedby')!)).toHaveTextContent('its pod is removed')
    expect(within(dialog).getByRole('button', { name: 'Cancel' })).toHaveFocus()
    await act(async () => { fireEvent.click(within(dialog).getByRole('button', { name: 'End session' })) })
    expect(onStop).toHaveBeenCalledTimes(1)
    expect(screen.queryByRole('alertdialog')).toBeNull()
    expect(screen.queryByText(/Stop failed/)).toBeNull()
  })

  it('cancels on Esc, on Cancel and on a press outside, the focus back on the button', () => {
    const onStop = vi.fn(() => Promise.resolve())
    render(<StartProgress attempt={attempt()} session={session} onStop={onStop} />)
    const stop = screen.getByRole('button', { name: 'Stop session' })
    fireEvent.click(stop)
    fireEvent.keyDown(document.activeElement!, { key: 'Escape' })
    expect(screen.queryByRole('alertdialog')).toBeNull()
    expect(stop).toHaveFocus()
    fireEvent.click(stop)
    fireEvent.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: 'Cancel' }))
    expect(screen.queryByRole('alertdialog')).toBeNull()
    fireEvent.click(stop)
    fireEvent.pointerDown(document.body)
    expect(screen.queryByRole('alertdialog')).toBeNull()
    expect(stop).toHaveFocus()
    // Waiting never ends anything.
    act(() => { vi.advanceTimersByTime(10_000) })
    expect(onStop).not.toHaveBeenCalled()
  })

  // Fix round 1 (#142): a failed start ends like any other ("End session"), never "Delete"; the
  // words come from the session's runtime, and are neutral when it is not known.
  it('offers End session, not Delete, on a failed start, worded by the session runtime', () => {
    const failed = attempt({ runtime: undefined, failed: true, stages: [{ id: 'image', label: 'Pulling image', state: 'failed', detail: 'ImagePullBackOff' }] })
    const { rerender } = render(<StartProgress attempt={failed} session={{ id: 's1', status: 'error', runtime: 'docker' }} onStop={vi.fn(() => Promise.resolve())} />)
    expect(screen.queryByRole('button', { name: 'Delete session' })).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'End session' }))
    expect(screen.getByRole('alertdialog', { name: 'End this session?' })).toHaveAccessibleDescription('The agent stops and its sandbox container is removed. The conversation, files and commits are kept.')
    fireEvent.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: 'Cancel' }))
    rerender(<StartProgress attempt={failed} session={{ id: 's1', status: 'error' }} onStop={vi.fn(() => Promise.resolve())} />)
    fireEvent.click(screen.getByRole('button', { name: 'End session' }))
    expect(screen.getByRole('alertdialog')).toHaveAccessibleDescription('The agent stops. Work not yet saved may be lost; the conversation is kept.')
  })

  it('cannot send a stop twice while one is on its way, and gives the focus back after a failure', async () => {
    let fail: (e: Error) => void = () => {}
    const onStop = vi.fn(() => new Promise<void>((_, reject) => { fail = reject }))
    render(<StartProgress attempt={attempt()} session={session} onStop={onStop} />)
    const stop = screen.getByRole('button', { name: 'Stop session' })
    fireEvent.click(stop)
    await act(async () => { fireEvent.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: 'End session' })) })
    expect(stop).toBeDisabled()
    expect(stop).toHaveAttribute('aria-busy', 'true')
    fireEvent.click(stop)
    expect(screen.queryByRole('alertdialog')).toBeNull()
    expect(onStop).toHaveBeenCalledTimes(1)
    await act(async () => { fail(new Error('HTTP 500')) })
    expect(stop).toBeEnabled()
    expect(stop).toHaveFocus()
  })

  it('says when the stop failed', async () => {
    const onStop = vi.fn(() => Promise.reject(new Error('HTTP 500')))
    render(<StartProgress attempt={attempt()} session={session} onStop={onStop} />)
    fireEvent.click(screen.getByRole('button', { name: 'Stop session' }))
    await act(async () => { fireEvent.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: 'End session' })) })
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
