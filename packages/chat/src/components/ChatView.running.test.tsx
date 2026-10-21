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
    expect(screen.getByTestId('waiting-on').textContent).toBe(' · waiting on Bash · make a 4:12')
    tick(S)
    expect(screen.getByTestId('waiting-on').textContent).toBe(' · waiting on Bash · make a 4:13')
    // The first call ends: the line holds its label for 3 s from when it appeared, then names the next.
    arrive(t, result('a', NOW + 2 * S))
    expect(screen.getByTestId('waiting-on').textContent).toBe(' · waiting on Bash · make a 4:13')
    tick(2 * S)
    expect(screen.getByTestId('waiting-on').textContent).toContain('waiting on Bash · make b 1:')
  })

  it('does not name a call that lasts under 2 s', () => {
    seed(ev('user_message', { text: 'go' }, NOW - 10 * S), ev('status_changed', { status: 'running' }, NOW - 10 * S))
    const { t } = mount()
    arrive(t, call('a', NOW))
    tick(S)
    expect(screen.queryByTestId('waiting-on')).toBeNull()
    arrive(t, result('a', NOW + 500))
    tick(3 * S)
    expect(screen.queryByTestId('waiting-on')).toBeNull()
  })

  it('names a call once it has run 2 s, and keeps the label at least 3 s after it ends', () => {
    seed(ev('user_message', { text: 'go' }, NOW - 10 * S), ev('status_changed', { status: 'running' }, NOW - 10 * S))
    const { t } = mount()
    arrive(t, call('a', NOW))
    tick(S)
    expect(screen.queryByTestId('waiting-on')).toBeNull()
    tick(S)
    expect(screen.getByTestId('waiting-on').textContent).toBe(' · waiting on Bash · make a 0:02')
    arrive(t, result('a', NOW + 2500))
    tick(S)
    expect(screen.getByTestId('waiting-on')).not.toBeNull()
    tick(S)
    expect(screen.getByTestId('waiting-on')).not.toBeNull()
    tick(S)
    expect(screen.queryByTestId('waiting-on')).toBeNull()
  })

  it('does not change the suffix more than once per 3 s while calls alternate', () => {
    seed(ev('user_message', { text: 'go' }, NOW - 10 * S), ev('status_changed', { status: 'running' }, NOW - 10 * S))
    const { t } = mount()
    const seen: string[] = []
    const changes: number[] = []
    const look = (at: number) => {
      const s = screen.queryByTestId('waiting-on')?.textContent?.replace(/ \d+:\d+$/, '') ?? ''
      if (seen.length === 0) seen.push(s)
      else if (seen[seen.length - 1] !== s) { seen.push(s); changes.push(at) }
    }
    // Back-to-back calls of alternating tools, each 2.4 s long: one starts as the last ends.
    const plan = new Map<number, AgentEvent[]>()
    for (let k = 0; k < 6; k++) {
      const at = k * 2400
      const id = `x${k}`
      plan.set(at, [...(plan.get(at) ?? []), ev('tool_call', { tool: k % 2 ? 'Read' : 'Bash', call_id: id, input: {} }, NOW + at)])
      plan.set(at + 2400, [result(id, NOW + at + 2400)])
    }
    for (let at = 0; at <= 18000; at += 100) {
      const due = plan.get(at)
      if (due) arrive(t, ...due)
      look(at)
      tick(100)
    }
    for (let i = 1; i < changes.length; i++) expect(changes[i] - changes[i - 1]).toBeGreaterThanOrEqual(3000)
    // It did appear (the first call, once it had run 2 s), and changed more than once.
    expect(seen[1]).toBe(' · waiting on Bash')
    expect(changes[0]).toBe(2000)
    expect(changes.length).toBeGreaterThan(2)
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

describe('what the working line says the agent is on', () => {
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval', 'Date'] })
    vi.setSystemTime(NOW)
    resetChatState()
    seq = 0
    localStorage.setItem(KEY, '1')
  })
  afterEach(() => {
    vi.useRealTimers()
    localStorage.removeItem(KEY)
  })
  // A thinking block's start or end: live only, never held in the transcript.
  const live = (kind: string, payload: unknown = {}) => mk({ client_event_id: `live${++seq}`, ts: new Date(NOW).toISOString(), kind, payload, transient: true })
  const words = () => screen.getByTestId('working-words').textContent
  const startTurn = () => seed(ev('user_message', { text: 'go' }, NOW - 10 * S), ev('status_changed', { status: 'running' }, NOW - 10 * S))

  it('shows reported reasoning tokens live, then clears them on text and a new block', () => {
    startTurn()
    const { t } = mount()
    arrive(t, live('thinking_started'), live('thinking_progress', { tokens: 12 }))
    expect(screen.getByTestId('working')).toHaveTextContent('Thinking… 12 tokens')
    arrive(t, live('thinking_progress', { tokens: 21 }))
    expect(screen.getByTestId('working')).toHaveTextContent('Thinking… 21 tokens')
    arrive(t, live('thinking_progress', { tokens: 999, parent_call_id: 'child' }))
    expect(screen.getByTestId('working')).not.toHaveTextContent('999')
    arrive(t, live('assistant_text', { text: 'Ready', done: false }))
    expect(screen.getByTestId('working-compact')).not.toHaveTextContent('tokens')
    arrive(t, live('thinking_started'))
    expect(screen.getByTestId('working-compact')).not.toHaveTextContent('tokens')
  })

  it('shows the clock, not the count, to a viewer who may not see token counts', () => {
    startTurn()
    const t = createFakeTransport()
    render(<ChatView session={SESSION} transport={t.transport} meta={base} reasoningTokens={false} />)
    arrive(t, live('thinking_started'))
    tick(83 * S)
    arrive(t, live('thinking_progress', { tokens: 4321 }))
    expect(screen.getByTestId('working')).toHaveTextContent('Thinking… 1:23')
    expect(screen.getByTestId('working')).not.toHaveTextContent('4,321')
  })

  it('uses the reasoning phase clock when no token count is reported', () => {
    startTurn()
    const { t } = mount()
    arrive(t, live('thinking_started'))
    tick(83 * S)
    expect(screen.getByTestId('working')).toHaveTextContent('Thinking… 1:23')
    arrive(t, live('thinking_progress', { tokens: -4 }))
    expect(screen.getByTestId('working')).toHaveTextContent('Thinking… 1:23')
  })

  it('says "Thinking…" while a thinking block is open, until it closes', () => {
    startTurn()
    const { t } = mount()
    expect(words()).toBe('Working…')
    arrive(t, live('thinking_started'))
    expect(words()).toBe('Thinking…')
    tick(30 * S)
    expect(words()).toBe('Thinking…')
    arrive(t, live('thinking_done'))
    expect(words()).toBe('Working…')
    // A subagent thinking is not the main agent's: the line stays as it is.
    arrive(t, live('thinking_started', { parent_call_id: 'toolu_a' }))
    expect(words()).toBe('Working…')
  })

  it('ends "Thinking…" on the thinking itself, a tool call, or the turn end when the done is lost', () => {
    startTurn()
    const { t } = mount()
    arrive(t, live('thinking_started'))
    arrive(t, ev('assistant_thinking', { text: 'Look at the tests first.' }, NOW))
    expect(words()).toBe('Working…')
    arrive(t, live('thinking_started'))
    arrive(t, call('a', NOW))
    expect(words()).toBe('Working…')
    arrive(t, result('a', NOW + S), live('thinking_started'))
    expect(words()).toBe('Thinking…')
    arrive(t, ev('turn_done', { stop_reason: 'end_turn', model: 'm', usage: { input_tokens: 0, output_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0 } }, NOW + 2 * S), ev('status_changed', { status: 'idle' }, NOW + 2 * S))
    expect(screen.queryByTestId('working-words')?.textContent ?? '').not.toBe('Thinking…')
    // The next turn starts without it.
    arrive(t, ev('user_message', { text: 'more' }, NOW + 3 * S), ev('status_changed', { status: 'running' }, NOW + 3 * S))
    expect(words()).toBe('Working…')
  })

  it('says the model is being retried until the model answers, the notice card staying', () => {
    startTurn()
    const { t } = mount()
    arrive(t, ev('error', { message: 'retrying the API (attempt 2 of 10) after overloaded_error (529)…', retryable: true, code: 'api_retry', attempt: 2, max_retries: 10, retry_in_ms: 1200 }, NOW))
    expect(words()).toBe('Retrying the model (attempt 2)…')
    expect(screen.getByTestId('retry-notice')).toBeTruthy()
    // Thinking means the retry got through.
    arrive(t, live('thinking_started'))
    expect(words()).toBe('Thinking…')
    arrive(t, live('thinking_done'), ev('assistant_text', { text: 'Done.', done: true }, NOW + S))
    expect(words()).toBe('Working…')
    expect(screen.getByTestId('retry-notice')).toBeTruthy()
  })

  it('stops saying the model is being retried as soon as its answer starts streaming', () => {
    startTurn()
    const { t } = mount()
    arrive(t, ev('error', { message: 'retrying the API (attempt 3 of 10)…', retryable: true, code: 'api_retry', attempt: 3 }, NOW))
    expect(words()).toBe('Retrying the model (attempt 3)…')
    arrive(t, live('assistant_text', { text: 'Here is', done: false }))
    expect(words()).toBe('Working…')
    // Another retry later in the turn says so again.
    arrive(t, ev('error', { message: 'retrying the API (attempt 1 of 10)…', retryable: true, code: 'api_retry', attempt: 1 }, NOW + S))
    expect(words()).toBe('Retrying the model (attempt 1)…')
  })

  it("names a Bash call's description after its name, and the start of its command without one", () => {
    startTurn()
    const { t } = mount()
    arrive(t, ev('tool_call', { tool: 'Bash', call_id: 'a', input: { command: 'npm test', description: 'Run the unit tests' } }, NOW))
    tick(2 * S)
    expect(screen.getByTestId('waiting-on').textContent).toBe(' · waiting on Bash · Run the unit tests 0:02')
    expect(words()).toBe('Working…')
    arrive(t, result('a', NOW + 2 * S), ev('tool_call', { tool: 'Bash', call_id: 'b', input: { command: 'x'.repeat(80) } }, NOW + 2 * S))
    tick(3 * S)
    expect(screen.getByTestId('waiting-on').textContent).toBe(` · waiting on Bash · ${'x'.repeat(47)}… 0:03`)
  })
})
