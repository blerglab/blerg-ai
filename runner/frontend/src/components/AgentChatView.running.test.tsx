import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, render, screen } from '@testing-library/react'
import AgentChatView from './AgentChatView'
import { useAgentTranscript } from '../hooks/useAgentTranscript'
import type { AgentEvent, SessionInfo } from '../types'

vi.mock('../ws', () => ({
  send: vi.fn(),
  onMessage: vi.fn(() => () => {}),
  onOpen: vi.fn(() => () => {}),
}))

const KEY = 'blerg.agent.compactTools'
const S = 1000
const M = 60 * S
const NOW = new Date(2026, 8, 29, 12, 0, 0).getTime()

const base: SessionInfo = {
  id: 's1',
  daemon_id: 'd1',
  status: 'running',
  project_path: '/r/proj',
  repo: 'proj',
  title: 'Test',
  model: 'claude-sonnet-5',
  started_at: new Date(NOW - 3600_000).toISOString(),
  kind: 'agent',
}

let seq = 0
function ev(kind: string, payload: unknown, at: number | string): AgentEvent {
  seq++
  return {
    type: 'agent_event', session_id: 's1', client_event_id: `e${seq}`, seq,
    ts: typeof at === 'number' ? new Date(at).toISOString() : at, kind, payload,
  } as AgentEvent
}
const ingest = (...events: AgentEvent[]) => events.forEach(e => useAgentTranscript.getState().ingest(e))
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

describe('running tool call clock', () => {
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval', 'Date'] })
    vi.setSystemTime(NOW)
    seq = 0
    localStorage.setItem(KEY, '1')
    useAgentTranscript.setState({ sessions: {} })
    vi.stubGlobal('fetch', vi.fn(() => Promise.reject(new Error('offline'))))
  })
  afterEach(() => {
    setHidden(false)
    vi.useRealTimers()
    vi.unstubAllGlobals()
    localStorage.removeItem(KEY)
  })

  it('ticks on a one-line row, then stops when the result arrives', () => {
    ingest(ev('user_message', { text: 'go' }, NOW - 10 * S), call('a', NOW - 4 * M - 12 * S))
    render(<AgentChatView session={base} />)
    expect(clockText()).toBe('running 4:12')
    tick(3 * S)
    expect(clockText()).toBe('running 4:15')
    act(() => ingest(result('a', NOW + 3 * S)))
    expect(screen.queryByTestId('tool-clock')).toBeNull()
  })

  it('shows the clock in a collapsed group preview and its summary, not a stale total', () => {
    ingest(
      ev('user_message', { text: 'go' }, NOW - 20 * M),
      ...finished('a', NOW - 10 * M), ...finished('b', NOW - 9 * M),
      call('c', NOW - 4 * M - 12 * S, 'make check'),
    )
    render(<AgentChatView session={base} />)
    const group = screen.getByTestId('tool-group')
    expect(screen.getByTestId('tool-preview').textContent).toBe('$ make check  ·  running 4:12')
    expect(group.textContent).toContain('4:12 running')
    expect(group.querySelector('.tcg-dur')).toBeNull()
    tick(S)
    expect(screen.getByTestId('tool-preview').textContent).toBe('$ make check  ·  running 4:13')
    act(() => ingest(result('c', NOW + 2 * S)))
    expect(screen.queryByTestId('tool-clock')).toBeNull()
    expect(group.querySelector('.tcg-dur')).not.toBeNull()
  })

  it('shows and updates the clock inside an expanded row', () => {
    ingest(
      ev('user_message', { text: 'go' }, NOW - 20 * M),
      ...finished('a', NOW - 10 * M), ...finished('b', NOW - 9 * M), call('c', NOW - 5 * S),
    )
    render(<AgentChatView session={base} />)
    act(() => screen.getByTestId('tool-group-toggle').click())
    const row = screen.getAllByTestId('tool-row')[2]
    const rowClock = () => row.querySelector('[data-testid="tool-clock"]')?.textContent
    expect(rowClock()).toBe('running 0:05')
    act(() => row.querySelector('button')!.click())
    tick(2 * S)
    expect(rowClock()).toBe('running 0:07')
    act(() => ingest(result('c', NOW + 2 * S)))
    expect(rowClock()).toBeUndefined()
    expect(row.querySelector('[data-testid="tool-detail"]')!.textContent).toContain('result')
  })

  it('emphasises at 2 minutes and hints at 10', () => {
    ingest(ev('user_message', { text: 'go' }, NOW - 20 * M), call('a', NOW - 119 * S))
    render(<AgentChatView session={base} />)
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
    ingest(
      ev('user_message', { text: 'go' }, NOW - 20 * M),
      ...finished('a', NOW - 15 * M), ...finished('b', NOW - 14 * M), call('c', NOW - 10 * M),
    )
    render(<AgentChatView session={base} />)
    expect(screen.getByTestId('tool-preview').textContent).toContain('still running — Stop interrupts the turn')
  })

  it('says what the toolbar is waiting on, oldest call first', () => {
    ingest(
      ev('user_message', { text: 'go' }, NOW - 6 * M),
      ev('status_changed', { status: 'running' }, NOW - 5 * M - 3 * S),
      call('a', NOW - 4 * M - 12 * S), call('b', NOW - 1 * M),
    )
    render(<AgentChatView session={base} />)
    expect(screen.getByTestId('waiting-on').textContent).toBe(' · waiting on Bash 4:12')
    tick(S)
    expect(screen.getByTestId('waiting-on').textContent).toBe(' · waiting on Bash 4:13')
    act(() => ingest(result('a', NOW + 2 * S)))
    expect(screen.getByTestId('waiting-on').textContent).toContain('waiting on Bash 1:')
  })

  it('shows no clock for an orphan call in an ended session or an earlier turn', () => {
    ingest(ev('user_message', { text: 'go' }, NOW - 20 * M), call('a', NOW - 10 * M))
    const { unmount } = render(<AgentChatView session={{ ...base, status: 'stopped' }} />)
    expect(screen.queryByTestId('tool-clock')).toBeNull()
    expect(screen.queryByTestId('waiting-on')).toBeNull()
    unmount()
    act(() => ingest(ev('turn_done', { stop_reason: 'end_turn', model: 'm', usage: { input_tokens: 1, output_tokens: 1, cache_read_tokens: 0, cache_write_tokens: 0 } }, NOW - 9 * M), ev('user_message', { text: 'again' }, NOW - S)))
    render(<AgentChatView session={base} />)
    expect(screen.queryByTestId('tool-clock')).toBeNull()
  })

  it('never shows NaN or a negative clock for a missing, bad or future ts', () => {
    ingest(
      ev('user_message', { text: 'go' }, NOW - 20 * M),
      call('a', ''), call('b', 'garbage'), call('c', NOW + 5 * M),
    )
    render(<AgentChatView session={base} />)
    // Three calls make a group; only the future-dated one has a usable start,
    // clamped to zero, in the summary and the preview.
    expect(screen.getAllByTestId('tool-clock').map(c => c.textContent)).toEqual(['0:00 running', 'running 0:00'])
    expect(document.body.textContent).not.toMatch(/NaN|-\d:\d\d/)
  })

  it('pauses ticking while the tab is hidden and resyncs on show', () => {
    ingest(ev('user_message', { text: 'go' }, NOW - 20 * M), call('a', NOW - 10 * S))
    render(<AgentChatView session={base} />)
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
      useAgentTranscript.setState({ sessions: {} })
      ingest(ev('user_message', { text: 'go' }, NOW - 20 * M), ...Array.from({ length: n }, (_, i) => call(`c${i}`, NOW - 30 * S)))
      const spy = vi.spyOn(globalThis, 'setInterval')
      const { unmount } = render(<AgentChatView session={base} />)
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
