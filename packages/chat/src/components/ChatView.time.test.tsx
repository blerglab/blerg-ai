import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, render, screen, within } from '@testing-library/react'
import ChatView from './ChatView'
import { useTranscriptStore } from '../hooks/useTranscriptStore'
import { absoluteLabel } from '../model/timeLabel'
import { createFakeTransport } from '../test/fakeTransport'
import { COMPACT, SESSION, ev as mk, ingest as ingestOpen, meta, resetChatState, seed } from '../test/chat'
import type { SessionMeta } from '../transport/types'
import type { AgentEvent } from '../types'

const KEY = COMPACT
const S = 1000
const M = 60 * S

// A local wall-clock time, so the tests hold in any time zone.
const local = (d: number, h: number, m: number, s = 0) => new Date(2026, 8, d, h, m, s).getTime()
const NOW = local(29, 12, 0)

const session: SessionMeta = meta({ status: 'idle', started_at: new Date(NOW - 3600_000).toISOString() })

let seq = 0
function ev(kind: string, payload: unknown, at: number | string): AgentEvent {
  seq++
  return mk({ client_event_id: `e${seq}`, seq, ts: typeof at === 'number' ? new Date(at).toISOString() : at, kind, payload })
}

const usage = { input_tokens: 1, output_tokens: 2, cache_read_tokens: 0, cache_write_tokens: 0 }
const turnDone = (at: number) =>
  ev('turn_done', { stop_reason: 'end_turn', model: 'claude-sonnet-5', usage }, at)
const bash = (id: string, at: number) => [
  ev('tool_call', { tool: 'Bash', call_id: id, input: { command: `echo ${id}` } }, at),
  ev('tool_result', { call_id: id, output: 'ok', is_error: false, duration_ms: 5 }, at + 1000),
]

function setHidden(hidden: boolean) {
  Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => (hidden ? 'hidden' : 'visible') })
  document.dispatchEvent(new Event('visibilitychange'))
}

function mount(m: SessionMeta = session) {
  const t = createFakeTransport()
  return { t, ...render(<ChatView session={SESSION} transport={t.transport} meta={m} />) }
}

describe('ChatView timestamps', () => {
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval', 'Date'] })
    vi.setSystemTime(NOW)
    resetChatState()
    seq = 0
  })

  afterEach(() => {
    setHidden(false)
    vi.useRealTimers()
    localStorage.removeItem(KEY)
  })

  function timesIn(el: HTMLElement) {
    return el.querySelectorAll('time')
  }

  for (const compact of [true, false]) {
    it(`labels every card kind with a <time> and the absolute time in its title (compact=${compact})`, () => {
      localStorage.setItem(KEY, compact ? '1' : '0')
      const at = NOW - 4 * M
      seed(
        ev('user_message', { text: 'do it', source: 'chat' }, at),
        ev('assistant_text', { text: 'on it', done: true }, at),
        ev('check_in', { phase: 'task_start', summary: 'planning' }, at),
        ev('ask', { body: 'which one?' }, at),
        ev('update', { body: 'halfway' }, at),
        ev('note', { body: 'fyi' }, at),
        ev('error', { message: 'boom' }, at),
        turnDone(at),
      )
      const { container } = mount()
      const times = Array.from(container.querySelectorAll<HTMLTimeElement>('.agent-timeline time.card-time'))
      expect(times).toHaveLength(8)
      for (const t of times) {
        expect(t.textContent).toContain('4 min ago')
        expect(t.getAttribute('datetime')).toBe(new Date(at).toISOString())
        expect(t.getAttribute('title')).toBe(absoluteLabel(at))
        expect(t.closest('[aria-live]')).toBeNull()
        expect(t.getAttribute('aria-live')).toBeNull()
      }
      expect(screen.getByTestId('checkin').querySelector('time')).toBeTruthy()
    })
  }

  it('shows no time, and never Invalid Date or NaN, for a missing or bad ts', () => {
    seed(
      ev('user_message', { text: 'no ts', source: 'chat' }, ''),
      ev('assistant_text', { text: 'bad ts', done: true }, 'garbage'),
      turnDone(NOW),
    )
    const { container } = mount()
    const timeline = container.querySelector('.agent-timeline') as HTMLElement
    expect(timeline.textContent).not.toMatch(/Invalid Date|NaN/)
    // Only the turn_done has a usable time.
    expect(timeline.querySelectorAll('time.card-time')).toHaveLength(1)
  })

  it('updates labels when the shared clock ticks, and uses one timer for all cards', () => {
    const spy = vi.spyOn(globalThis, 'setInterval')
    seed(
      ...Array.from({ length: 30 }, (_, i) => ev('assistant_text', { text: `m${i}`, done: true }, NOW - 2 * M)),
    )
    const { container } = mount()
    expect(spy).toHaveBeenCalledTimes(1)
    const first = () => container.querySelector('.agent-timeline time.card-time')!.textContent
    expect(first()).toBe('2 min ago')
    act(() => { vi.advanceTimersByTime(3 * M) })
    expect(first()).toBe('5 min ago')
    expect(Array.from(container.querySelectorAll('time.card-time')).every(t => t.textContent === '5 min ago')).toBe(true)
    spy.mockRestore()
  })

  it('pauses while the tab is hidden and refreshes the moment it is visible', () => {
    seed(ev('assistant_text', { text: 'hi', done: true }, NOW - M))
    const { container } = mount()
    const label = () => container.querySelector('.agent-timeline time.card-time')!.textContent
    expect(label()).toBe('1 min ago')
    act(() => { setHidden(true) })
    act(() => { vi.advanceTimersByTime(10 * M) })
    expect(label()).toBe('1 min ago')
    act(() => { setHidden(false) })
    expect(label()).toBe('11 min ago')
  })

  it('maps labels onto the server clock, not the browser one', () => {
    // The browser is two hours ahead of the server.
    const serverNow = NOW - 2 * 3600_000
    useTranscriptStore.getState().ingestReplayDone(SESSION, { lastSeq: 1, hasMore: false, serverTime: new Date(serverNow).toISOString() })
    ingestOpen(ev('assistant_text', { text: 'hi', done: true }, serverNow - 4 * M))
    const { container } = mount()
    expect(container.querySelector('.agent-timeline time.card-time')!.textContent).toBe('4 min ago')
  })

  it('takes the server clock from the replay the transport reports', () => {
    const serverNow = NOW - 2 * 3600_000
    const { t, container } = mount()
    act(() => {
      t.emit(ev('assistant_text', { text: 'hi', done: true }, serverNow - 4 * M))
      t.replayDone({ lastSeq: 1, hasMore: false, serverTime: new Date(serverNow).toISOString() })
    })
    expect(container.querySelector('.agent-timeline time.card-time')!.textContent).toBe('4 min ago')
  })

  it('inserts a divider where the local date changes, and not otherwise', () => {
    seed(
      ev('user_message', { text: 'late', source: 'chat' }, local(28, 23, 50)),
      ev('assistant_text', { text: 'reply', done: true }, local(28, 23, 55)),
      ev('assistant_text', { text: 'next day', done: true }, local(29, 0, 10)),
      ev('assistant_text', { text: 'same day', done: true }, local(29, 9, 0)),
    )
    mount()
    const dividers = screen.getAllByTestId('day-divider')
    expect(dividers).toHaveLength(1)
    expect(dividers[0].getAttribute('role')).toBe('separator')
    expect(dividers[0].textContent).toBe('Today')
    // The divider sits between the two days' cards.
    const cards = Array.from(document.querySelectorAll('.agent-timeline > *'))
    const at = cards.indexOf(dividers[0])
    expect(cards[at - 1].textContent).toContain('reply')
    expect(cards[at + 1].textContent).toContain('next day')
  })

  it('names the previous day "Yesterday" on its divider', () => {
    seed(
      ev('assistant_text', { text: 'a', done: true }, local(27, 10, 0)),
      ev('assistant_text', { text: 'b', done: true }, local(28, 10, 0)),
    )
    mount()
    expect(screen.getByTestId('day-divider').textContent).toBe('Yesterday')
  })

  it('keeps a group that spans midnight whole and shows its last call time', () => {
    const last = local(29, 0, 5)
    seed(
      ev('assistant_text', { text: 'before', done: true }, local(28, 23, 30)),
      ...bash('c1', local(28, 23, 58)),
      ...bash('c2', local(28, 23, 59)),
      ...bash('c3', last),
      ev('assistant_text', { text: 'after', done: true }, local(29, 0, 30)),
    )
    mount()
    const group = screen.getByTestId('tool-group')
    expect(screen.getAllByTestId('tool-group')).toHaveLength(1)
    // No divider inside the group; the day change is drawn before it.
    expect(within(group).queryByTestId('day-divider')).toBeNull()
    expect(screen.getAllByTestId('day-divider')).toHaveLength(1)
    const t = group.querySelector('time')!
    expect(t.getAttribute('datetime')).toBe(new Date(last).toISOString())
    expect(t.getAttribute('title')).toBe(absoluteLabel(last))
  })

  it('gives the streaming card no time', () => {
    const { t } = mount()
    act(() => t.emit({ ...ev('assistant_text', { text: 'typing', done: false }, ''), transient: true }))
    const card = screen.getByTestId('streaming')
    expect(card.textContent).toContain('typing')
    expect(timesIn(card)).toHaveLength(0)
  })

  it('appends time and turn duration to the turn-done marker', () => {
    seed(
      ev('user_message', { text: 'go', source: 'chat' }, NOW - 5 * M),
      turnDone(NOW - 5 * M + 42 * S),
      ev('user_message', { text: 'again', source: 'chat' }, NOW - 4 * M),
      ev('user_message', { text: '<reminder>', source: 'system' }, NOW - 3 * M),
      turnDone(NOW - 4 * M + 185 * S),
    )
    mount()
    const [a, b] = screen.getAllByTestId('turn-done')
    expect(a.textContent).toContain('took 42 s')
    expect(a.querySelector('time')!.textContent).toBe('4 min ago')
    // A system reminder in between does not restart the clock.
    expect(b.textContent).toContain('took 3 min 5 s')
  })

  it('measures a turn that absorbed messages sent mid-turn from the first of them', () => {
    seed(
      ev('user_message', { text: 'go', source: 'chat' }, NOW - 5 * M),
      ev('user_message', { text: 'also this', source: 'chat' }, NOW - 5 * M + 20 * S),
      turnDone(NOW - 5 * M + 100 * S),
    )
    mount()
    expect(screen.getByTestId('turn-done').textContent).toContain('took 1 min 40 s')
  })

  it('does not carry a message that never started a turn into the next turn\'s duration', () => {
    seed(
      ev('user_message', { text: 'lost', source: 'chat' }, NOW - 10 * M),
      ev('status_changed', { status: 'idle', reason: 'turn_done' }, NOW - 10 * M + 5 * S),
      ev('user_message', { text: 'real', source: 'chat' }, NOW - 2 * M),
      turnDone(NOW - 2 * M + 30 * S),
    )
    mount()
    expect(screen.getByTestId('turn-done').textContent).toContain('took 30 s')
  })

  it('omits the duration when the turn start is unknown', () => {
    seed(turnDone(NOW - M))
    mount()
    expect(screen.getByTestId('turn-done').textContent).not.toContain('took')
    expect(screen.getByTestId('turn-done').querySelector('time')).toBeTruthy()
  })

  it('carries the absolute time in the tooltips of expanded rows', () => {
    const at = local(29, 11, 0)
    seed(...bash('c1', at))
    mount()
    expect(screen.getByTestId('tool-row').querySelector('button')!.getAttribute('title')).toBe(absoluteLabel(at))
  })

  it('stays fast with timestamps on a 2,000-event transcript', () => {
    const events: AgentEvent[] = []
    for (let g = 0; g < 50; g++) {
      events.push(ev('assistant_text', { text: `step ${g}`, done: true }, NOW - (50 - g) * 3 * 3600_000))
      for (let i = 0; i < 20; i++) events.push(...bash(`g${g}c${i}`, NOW - (50 - g) * 3 * 3600_000 + i * S))
    }
    expect(events.length).toBeGreaterThan(2000)
    seed(...events)
    const t0 = performance.now()
    mount()
    const elapsed = performance.now() - t0
    expect(screen.getAllByTestId('tool-group')).toHaveLength(50)
    expect(document.querySelectorAll('time').length).toBeGreaterThan(100)
    expect(screen.getAllByTestId('day-divider').length).toBeGreaterThan(5)
    expect(elapsed).toBeLessThan(3000)
  })
})
