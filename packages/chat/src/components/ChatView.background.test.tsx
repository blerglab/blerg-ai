import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, fireEvent, render, screen, within } from '@testing-library/react'
import ChatView from './ChatView'
import { StatusGlyph, elapsedOf } from './BackgroundParts'
import { scrollToCall } from './scrollToCall'
import { useBackgroundTasks } from '../hooks/useBackgroundTasks'
import { createFakeTransport, type FakeTransport } from '../test/fakeTransport'
import { COMPACT, SESSION, ev as mk, meta, resetChatState, seed } from '../test/chat'
import type { SessionMeta } from '../transport/types'
import type { AgentEvent } from '../types'

// Background tasks: the chat draws no list of its own (the host's side panel lists them through
// useBackgroundTasks), the working line names them, scrollToCall finds the call a task started from,
// and an older daemon (no background events) changes nothing.

const S = 1000
const NOW = new Date(2026, 9, 14, 12, 0, 0).getTime()

let seq = 0
function ev(kind: string, payload: unknown, at: number): AgentEvent {
  seq++
  return mk({ client_event_id: `e${seq}`, seq, ts: new Date(at).toISOString(), kind, payload })
}
const userMsg = (at: number) => ev('user_message', { text: 'deploy it', source: 'chat' }, at)
const bashCall = (id: string, at: number, command = 'make deploy') =>
  ev('tool_call', { tool: 'Bash', call_id: id, input: { command, run_in_background: true } }, at)
const result = (id: string, at: number) => ev('tool_result', { call_id: id, output: 'ok', is_error: false, duration_ms: 1 }, at)
const started = (task: string, call: string, at: number, description: string, kind = 'bash') =>
  ev('background_task_started', { task_id: task, call_id: call, kind, description, started_at: new Date(at).toISOString() }, at)
const done = (task: string, call: string, at: number, status = 'completed', summary = `Background command completed`) =>
  ev('background_task_done', { task_id: task, call_id: call, kind: 'bash', status, summary }, at)
const turnDone = (at: number) => ev('turn_done', { stop_reason: 'end_turn', model: 'm', usage: {} }, at)

const LONG = 'Watch the build log of the release pipeline for the first error it reports'

function mount(status: SessionMeta['status'] = 'idle', t: FakeTransport = createFakeTransport()) {
  const r = render(<ChatView session={SESSION} transport={t.transport} meta={meta({ status })} />)
  return { t, ...r }
}
const arrive = (t: FakeTransport, ...events: AgentEvent[]) => act(() => { for (const e of events) t.emit(e) })

// A turn that left two tasks running: a background shell started 65 s ago and a monitor.
function twoRunning() {
  return [
    userMsg(NOW - 70 * S),
    bashCall('c1', NOW - 66 * S), result('c1', NOW - 65 * S), started('b1', 'c1', NOW - 65 * S, 'Wait for the deploy'),
    ev('tool_call', { tool: 'Monitor', call_id: 'c2', input: { command: 'tail -f log', description: LONG } }, NOW - 31 * S),
    result('c2', NOW - 30 * S), started('m1', 'c2', NOW - 30 * S, LONG, 'monitor'),
    ev('assistant_text', { text: 'Both are running.', done: true }, NOW - 29 * S), turnDone(NOW - 29 * S),
  ]
}

describe('background tasks in the chat', () => {
  let scrolled: ReturnType<typeof vi.fn>
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval', 'setTimeout', 'clearTimeout', 'Date'] })
    vi.setSystemTime(NOW)
    resetChatState()
    seq = 0
    scrolled = vi.fn()
    Element.prototype.scrollIntoView = scrolled as unknown as Element['scrollIntoView']
  })
  afterEach(() => {
    vi.useRealTimers()
    delete (Element.prototype as { scrollIntoView?: unknown }).scrollIntoView
    localStorage.removeItem(COMPACT)
  })

  it('draws no strip or list of its own; the working line names what runs', () => {
    seed(...twoRunning())
    const { container } = mount('idle')
    expect(screen.queryByTestId('background-strip')).toBeNull()
    expect(container.querySelector('.bg-strip, .bg-row, .bg-chip')).toBeNull()
    expect(screen.getByTestId('working').textContent).toBe('Waiting on 2 background tasks 1:05')
  })

  it('draws nothing for a session whose daemon sends no background events', () => {
    seed(userMsg(NOW - 10 * S), bashCall('c1', NOW - 9 * S), result('c1', NOW - 8 * S), turnDone(NOW - 7 * S))
    mount()
    expect(screen.queryByTestId('working')).toBeNull()
  })

  it('scrollToCall brings the call that started a task into view and flashes it', () => {
    seed(...twoRunning())
    mount()
    expect(scrollToCall('c1')).toBe(true)
    const target = scrolled.mock.contexts.at(-1) as HTMLElement
    expect(target.dataset.callId).toBe('c1')
    expect(target.classList.contains('bg-flash')).toBe(true)
    act(() => { vi.advanceTimersByTime(2 * S) })
    expect(target.classList.contains('bg-flash')).toBe(false)
    expect(scrollToCall('not-drawn')).toBe(false)
  })

  it('scrollToCall finds a call folded into a collapsed tool group', () => {
    localStorage.setItem(COMPACT, '1')
    seed(
      userMsg(NOW - 70 * S),
      ev('tool_call', { tool: 'Read', call_id: 'r1', input: { file_path: 'a' } }, NOW - 69 * S), result('r1', NOW - 68 * S),
      ev('tool_call', { tool: 'Read', call_id: 'r2', input: { file_path: 'b' } }, NOW - 67 * S), result('r2', NOW - 67 * S),
      bashCall('c1', NOW - 66 * S), result('c1', NOW - 65 * S), started('b1', 'c1', NOW - 65 * S, 'Wait for the deploy'),
      ev('tool_call', { tool: 'Read', call_id: 'r3', input: { file_path: 'c' } }, NOW - 64 * S), result('r3', NOW - 64 * S),
    )
    mount()
    // The background event does not split the run of calls.
    expect(screen.getAllByTestId('tool-group')).toHaveLength(1)
    scrollToCall('c1')
    expect((scrolled.mock.contexts.at(-1) as HTMLElement).dataset.testid).toBe('tool-group')
  })
})

// A view beside the chat (the host's side panel) listing the tasks through useBackgroundTasks.
function TaskList({ live }: { live?: boolean }) {
  const { running, recent, counts, now, dismiss } = useBackgroundTasks(SESSION, live === undefined ? {} : { live })
  return (
    <ul data-testid="tasks" data-running={counts.running} data-failed={counts.failed}>
      {[...running, ...recent].map(t => (
        <li key={t.taskId} data-task-id={t.taskId}>
          {t.description} {elapsedOf(t, now)}<StatusGlyph t={t} />
          <button type="button" onClick={() => dismiss(t.taskId)}>dismiss</button>
        </li>
      ))}
    </ul>
  )
}
const rows = () => Array.from(screen.getByTestId('tasks').querySelectorAll('li'))

describe('useBackgroundTasks', () => {
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval', 'setTimeout', 'clearTimeout', 'Date'] })
    vi.setSystemTime(NOW)
    resetChatState()
    seq = 0
  })
  afterEach(() => { vi.useRealTimers() })

  function page(status: SessionMeta['status'] = 'idle') {
    const t = createFakeTransport()
    render(<><TaskList /><ChatView session={SESSION} transport={t.transport} meta={meta({ status })} /></>)
    return t
  }

  it('lists the running tasks with a ticking clock, live as the session\'s ChatView says', () => {
    seed(...twoRunning())
    page()
    expect(rows().map(r => r.dataset.taskId)).toEqual(['b1', 'm1'])
    expect(rows()[0].textContent).toContain('Wait for the deploy 01:05')
    act(() => { vi.advanceTimersByTime(2 * S) })
    expect(rows()[0].textContent).toContain('01:07')
  })

  it('lists nothing for a session that has ended, and takes live from its host when given', () => {
    seed(...twoRunning())
    page('stopped')
    expect(rows()).toHaveLength(0)
    cleanup()
    resetChatState()
    seed(...twoRunning())
    render(<TaskList live />)
    expect(rows()).toHaveLength(2)
  })

  it('keeps a finished task with its check for 10 s', () => {
    seed(userMsg(NOW - 70 * S), bashCall('c1', NOW - 66 * S), result('c1', NOW - 65 * S), started('b1', 'c1', NOW - 65 * S, 'Wait for the deploy'))
    const t = page()
    arrive(t, done('b1', 'c1', NOW))
    expect(rows()[0].textContent).toContain('✓')
    act(() => { vi.advanceTimersByTime(5 * S) })
    expect(rows()).toHaveLength(1)
    act(() => { vi.advanceTimersByTime(5 * S) })
    expect(rows()).toHaveLength(0)
  })

  it('keeps a failure with its cross until it is dismissed', () => {
    seed(...twoRunning())
    const t = page()
    arrive(t, done('b1', 'c1', NOW, 'failed', 'Background command "make deploy" failed with exit code 2'))
    act(() => { vi.advanceTimersByTime(60 * S) })
    expect(screen.getByTestId('tasks').dataset.failed).toBe('1')
    // Running first, then what finished.
    expect(rows().map(r => r.dataset.taskId)).toEqual(['m1', 'b1'])
    expect(within(rows()[1]).getByRole('img', { name: 'failed' }).textContent).toBe('✕')
    fireEvent.click(within(rows()[1]).getByRole('button', { name: 'dismiss' }))
    expect(rows().map(r => r.dataset.taskId)).toEqual(['m1'])
  })
})

describe('working line while background tasks run', () => {
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval', 'Date'] })
    vi.setSystemTime(NOW)
    resetChatState()
    seq = 0
  })
  afterEach(() => { vi.useRealTimers() })

  it('says what an idle session is waiting on, with the oldest task\'s clock', () => {
    seed(...twoRunning())
    mount('idle')
    const line = screen.getByTestId('working')
    expect(line.dataset.background).toBe('true')
    expect(line.textContent).toBe('Waiting on 2 background tasks 1:05')
  })

  it('names the only task', () => {
    seed(userMsg(NOW - 70 * S), bashCall('c1', NOW - 66 * S), result('c1', NOW - 65 * S), started('b1', 'c1', NOW - 65 * S, 'Wait for the deploy'), turnDone(NOW - 60 * S))
    mount('idle')
    expect(screen.getByTestId('working').textContent).toBe('Waiting on a background task: Wait for the deploy 1:05')
  })

  it('replaces a bare "Working…" in a running turn, not one waiting on a call', () => {
    seed(...twoRunning(), ev('status_changed', { status: 'running', reason: 'turn' }, NOW - 10 * S))
    const { t } = mount('running')
    expect(screen.getByTestId('working').textContent).toBe('Waiting on 2 background tasks 0:10')
    arrive(t, ev('tool_call', { tool: 'Read', call_id: 'r9', input: { file_path: 'x' } }, NOW - 2 * S))
    expect(screen.getByTestId('working').textContent).toMatch(/^Working… 0:10 · waiting on Read/)
  })

  it('goes when the last task finishes', () => {
    seed(userMsg(NOW - 70 * S), bashCall('c1', NOW - 66 * S), result('c1', NOW - 65 * S), started('b1', 'c1', NOW - 65 * S, 'Wait for the deploy'), turnDone(NOW - 60 * S))
    const { t } = mount('idle')
    arrive(t, done('b1', 'c1', NOW))
    expect(screen.queryByTestId('working')).toBeNull()
  })
})
