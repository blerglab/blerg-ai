import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, render, screen } from '@testing-library/react'
import ChatView from './ChatView'
import { useTranscriptStore } from '../hooks/useTranscriptStore'
import { createFakeTransport, type FakeTransport } from '../test/fakeTransport'
import { COMPACT, SESSION, ev as mk, meta, resetChatState, seed } from '../test/chat'
import type { SessionMeta } from '../transport/types'
import type { AgentEvent } from '../types'

const KEY = COMPACT
const S = 1000
const M = 60 * S
const NOW = new Date(2026, 8, 29, 12, 0, 0).getTime()

const base: SessionMeta = meta({ status: 'running', started_at: new Date(NOW - 3600_000).toISOString() })

let seq = 0
function ev(kind: string, payload: unknown, at: number | string): AgentEvent {
  seq++
  return mk({ client_event_id: `e${seq}`, seq, ts: typeof at === 'number' ? new Date(at).toISOString() : at, kind, payload })
}
const call = (id: string, at: number | string, command = `make ${id}`) =>
  ev('tool_call', { tool: 'Bash', call_id: id, input: { command } }, at)
const result = (id: string, at: number) =>
  ev('tool_result', { call_id: id, output: 'ok', is_error: false, duration_ms: 5 }, at)
const finished = (id: string, at: number) => [call(id, at), result(id, at + S)]

function setHidden(hidden: boolean) {
  Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => (hidden ? 'hidden' : 'visible') })
  act(() => { document.dispatchEvent(new Event('visibilitychange')) })
}
const tick = (ms: number) => act(() => { vi.advanceTimersByTime(ms) })
const clockText = () => screen.getByTestId('tool-clock').textContent

function mount(m: SessionMeta = base, t: FakeTransport = createFakeTransport()) {
  const r = render(<ChatView session={SESSION} transport={t.transport} meta={m} />)
  return { t, ...r }
}
const arrive = (t: FakeTransport, ...events: AgentEvent[]) => act(() => { for (const e of events) t.emit(e) })

describe('running tool call clock', () => {
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval', 'Date'] })
    vi.setSystemTime(NOW)
    resetChatState()
    seq = 0
    localStorage.setItem(KEY, '1')
  })
  afterEach(() => {
    setHidden(false)
    vi.useRealTimers()
    localStorage.removeItem(KEY)
  })

  it('ticks on a one-line row, then stops when the result arrives', () => {
    seed(ev('user_message', { text: 'go' }, NOW - 10 * S), call('a', NOW - 4 * M - 12 * S))
    const { t } = mount()
    expect(clockText()).toBe('running 4:12')
    tick(3 * S)
    expect(clockText()).toBe('running 4:15')
    arrive(t, result('a', NOW + 3 * S))
    expect(screen.queryByTestId('tool-clock')).toBeNull()
  })

  it('shows the clock in a collapsed group preview and its summary, not a stale total', () => {
    seed(
      ev('user_message', { text: 'go' }, NOW - 20 * M),
      ...finished('a', NOW - 10 * M), ...finished('b', NOW - 9 * M),
      call('c', NOW - 4 * M - 12 * S, 'make check'),
    )
    const { t } = mount()
    const group = screen.getByTestId('tool-group')
    expect(screen.getByTestId('tool-preview').textContent).toBe('$ make check  ·  running 4:12')
    expect(group.textContent).toContain('4:12 running')
    expect(group.querySelector('.tcg-dur')).toBeNull()
    tick(S)
    expect(screen.getByTestId('tool-preview').textContent).toBe('$ make check  ·  running 4:13')
    arrive(t, result('c', NOW + 2 * S))
    expect(screen.queryByTestId('tool-clock')).toBeNull()
    expect(group.querySelector('.tcg-dur')).not.toBeNull()
  })

  it('shows and updates the clock inside an expanded row', () => {
    seed(
      ev('user_message', { text: 'go' }, NOW - 20 * M),
      ...finished('a', NOW - 10 * M), ...finished('b', NOW - 9 * M), call('c', NOW - 5 * S),
    )
    const { t } = mount()
    act(() => screen.getByTestId('tool-group-toggle').click())
    const row = screen.getAllByTestId('tool-row')[2]
    const rowClock = () => row.querySelector('[data-testid="tool-clock"]')?.textContent
    expect(rowClock()).toBe('running 0:05')
    act(() => row.querySelector('button')!.click())
    tick(2 * S)
    expect(rowClock()).toBe('running 0:07')
    arrive(t, result('c', NOW + 2 * S))
    expect(rowClock()).toBeUndefined()
    expect(row.querySelector('[data-testid="tool-detail"]')!.textContent).toContain('result')
  })

  it('emphasises at 2 minutes and hints at 10', () => {
    seed(ev('user_message', { text: 'go' }, NOW - 20 * M), call('a', NOW - 119 * S))
    mount()
    const clock = () => screen.getByTestId('tool-clock')
    expect(clock().className).not.toContain('tcg-clock-long')
    expect(clock().getAttribute('title')).toBeNull()
    tick(S)
    expect(clock().className).toContain('tcg-clock-long')
    expect(clock().getAttribute('title')).toBe(
      'This command has been running for 2 minutes. Claude Code reports output only when it finishes.')
    expect(screen.queryByText(/still running/)).toBeNull()
    tick(8 * M)
    expect(screen.getByText('still running — Stop interrupts the turn')).toBeTruthy()
  })

  it('puts the hint on a collapsed group preview line at 10 minutes', () => {
    seed(
      ev('user_message', { text: 'go' }, NOW - 20 * M),
      ...finished('a', NOW - 15 * M), ...finished('b', NOW - 14 * M), call('c', NOW - 10 * M),
    )
    mount()
    expect(screen.getByTestId('tool-preview').textContent).toContain('still running — Stop interrupts the turn')
  })

  it('says what the toolbar is waiting on, oldest call first', () => {
    seed(
      ev('user_message', { text: 'go' }, NOW - 6 * M),
      ev('status_changed', { status: 'running' }, NOW - 5 * M - 3 * S),
      call('a', NOW - 4 * M - 12 * S), call('b', NOW - 1 * M),
    )
    const { t } = mount()
    expect(screen.getByTestId('waiting-on').textContent).toBe(' · waiting on Bash 4:12')
    tick(S)
    expect(screen.getByTestId('waiting-on').textContent).toBe(' · waiting on Bash 4:13')
    arrive(t, result('a', NOW + 2 * S))
    expect(screen.getByTestId('waiting-on').textContent).toContain('waiting on Bash 1:')
  })

  it('shows no clock for an orphan call in an ended session or an earlier turn', () => {
    seed(ev('user_message', { text: 'go' }, NOW - 20 * M), call('a', NOW - 10 * M))
    const { unmount } = mount({ ...base, status: 'stopped' })
    expect(screen.queryByTestId('tool-clock')).toBeNull()
    expect(screen.queryByTestId('waiting-on')).toBeNull()
    unmount()
    seed(ev('turn_done', { stop_reason: 'end_turn', model: 'm', usage: { input_tokens: 1, output_tokens: 1, cache_read_tokens: 0, cache_write_tokens: 0 } }, NOW - 9 * M), ev('user_message', { text: 'again' }, NOW - S))
    mount()
    expect(screen.queryByTestId('tool-clock')).toBeNull()
  })

  it('never shows NaN or a negative clock for a missing, bad or future ts', () => {
    seed(
      ev('user_message', { text: 'go' }, NOW - 20 * M),
      call('a', ''), call('b', 'garbage'), call('c', NOW + 5 * M),
    )
    mount()
    // Three calls make a group; only the future-dated one has a usable start,
    // clamped to zero, in the summary and the preview.
    expect(screen.getAllByTestId('tool-clock').map(c => c.textContent)).toEqual(['0:00 running', 'running 0:00'])
    expect(document.body.textContent).not.toMatch(/NaN|-\d:\d\d/)
  })

  it('pauses ticking while the tab is hidden and resyncs on show', () => {
    seed(ev('user_message', { text: 'go' }, NOW - 20 * M), call('a', NOW - 10 * S))
    mount()
    expect(clockText()).toBe('running 0:10')
    setHidden(true)
    tick(60 * S)
    expect(clockText()).toBe('running 0:10')
    setHidden(false)
    expect(clockText()).toBe('running 1:10')
    tick(S)
    expect(clockText()).toBe('running 1:11')
  })

  it('uses one interval however many rows are running', () => {
    const count = (n: number) => {
      seq = 0
      useTranscriptStore.setState({ sessions: {} })
      seed(ev('user_message', { text: 'go' }, NOW - 20 * M), ...Array.from({ length: n }, (_, i) => call(`c${i}`, NOW - 30 * S)))
      const spy = vi.spyOn(globalThis, 'setInterval')
      const { unmount } = mount()
      const made = spy.mock.calls.filter(c => c[1] === 1000).length
      spy.mockRestore()
      unmount()
      return made
    }
    const one = count(1)
    expect(one).toBeGreaterThan(0)
    expect(count(8)).toBe(one)
  })
})
