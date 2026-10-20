import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, render, screen } from '@testing-library/react'
import Transcript, { WINDOW, type TranscriptProps } from './Transcript'
import { installIntersectionObserver, intersect } from '../test/intersection'
import { SESSION } from '../test/chat'
import type { AgentEvent } from '../types'

// A long transcript draws only its newest WINDOW rows; reaching the top of what is drawn reveals
// WINDOW more, and once everything held is drawn it asks for the next older page.

function ev(seq: number, session = SESSION): AgentEvent {
  return {
    type: 'agent_event',
    session_id: session,
    client_event_id: `${session}-e${seq}`,
    seq,
    ts: '2026-10-13T00:00:00Z',
    kind: 'user_message',
    payload: { text: `message ${seq}`, source: 'chat' },
  }
}
const range = (from: number, to: number) => Array.from({ length: to - from + 1 }, (_, i) => ev(from + i))

const NONE = new Set<string>()
const NO_QUEUE: TranscriptProps['queued'] = []

function props(over: Partial<TranscriptProps> = {}): TranscriptProps {
  return {
    sessionId: SESSION,
    events: [],
    streaming: '',
    compact: true,
    liveIds: NONE,
    showSkeleton: false,
    loadingOlder: false,
    hasOlder: false,
    starting: false,
    attempt: null,
    clockOffset: 0,
    readyCard: <div data-testid="ready-card" />,
    working: false,
    waiting: false,
    since: null,
    waitingOn: null,
    queued: NO_QUEUE,
    queueState: 'sending',
    steers: true,
    pin: 0,
    ...over,
  }
}

const drawn = (c: HTMLElement) => c.querySelectorAll('.agent-card.user').length

// The reader scrolls up from where they are to `top` (following stops on an upward scroll).
function scrollUpTo(timeline: HTMLElement, top: number) {
  timeline.scrollTop = timeline.scrollHeight - timeline.clientHeight
  act(() => { timeline.dispatchEvent(new Event('scroll', { bubbles: true })) })
  timeline.scrollTop = top
  act(() => { timeline.dispatchEvent(new Event('scroll', { bubbles: true })) })
}

// jsdom has no layout, so a timeline reads as scrolled to its end; this one is scrolled to its top.
function scrolledUp(c: HTMLElement) {
  const timeline = c.querySelector('.agent-timeline') as HTMLElement
  Object.defineProperty(timeline, 'scrollHeight', { configurable: true, value: 100_000 })
  Object.defineProperty(timeline, 'clientHeight', { configurable: true, value: 100 })
  scrollUpTo(timeline, 0)
}

beforeEach(() => {
  installIntersectionObserver()
})
afterEach(() => {
  vi.unstubAllGlobals()
})

describe('Transcript draws a window of the newest rows', () => {
  beforeEach(() => {
    Element.prototype.scrollIntoView = vi.fn()
  })

  it('draws at most WINDOW rows of 1,000, the newest', () => {
    const { container } = render(<Transcript {...props({ events: range(1, 1000) })} />)
    expect(WINDOW).toBe(300)
    expect(drawn(container)).toBe(WINDOW)
    expect(screen.getByText('message 1000')).toBeInTheDocument()
    expect(screen.getByText('message 701')).toBeInTheDocument()
    expect(screen.queryByText('message 700')).toBeNull()
    // The ready card heads the transcript only once its start is drawn.
    expect(screen.queryByTestId('ready-card')).toBeNull()
  })

  it('reveals WINDOW more each time the sentinel above the first row comes into view', () => {
    const { container } = render(<Transcript {...props({ events: range(1, 1000) })} />)
    scrolledUp(container)
    const sentinel = screen.getByTestId('transcript-more')
    // The sentinel sits right above the first row drawn.
    expect(sentinel.nextElementSibling?.textContent).toContain('message 701')
    act(() => intersect())
    expect(drawn(container)).toBe(600)
    expect(screen.getByText('message 401')).toBeInTheDocument()
    act(() => intersect())
    act(() => intersect())
    expect(drawn(container)).toBe(1000)
    expect(screen.getByText('message 1')).toBeInTheDocument()
    // Everything drawn and nothing older: no sentinel, and the ready card heads it all.
    expect(screen.queryByTestId('transcript-more')).toBeNull()
    expect(screen.getByTestId('ready-card')).toBeInTheDocument()
  }, 30_000) // Drawing all 1000 rows may need the full unit watchdog under contention.

  it('with everything held drawn and older events on the server, asks for the next page exactly once', () => {
    const onLoadOlder = vi.fn()
    const p = props({ events: range(201, 600), hasOlder: true, onLoadOlder })
    const { container, rerender } = render(<Transcript {...p} />)
    act(() => intersect())
    expect(drawn(container)).toBe(400)
    expect(onLoadOlder).not.toHaveBeenCalled()

    // All held rows drawn: the sentinel asks for the older page...
    act(() => intersect())
    expect(onLoadOlder).toHaveBeenCalledTimes(1)
    // ...and not again while it is on its way, however often the sentinel is seen.
    act(() => intersect())
    rerender(<Transcript {...p} loadingOlder />)
    expect(screen.getByTestId('older-loading')).toBeInTheDocument()
    act(() => intersect())
    act(() => intersect())
    expect(onLoadOlder).toHaveBeenCalledTimes(1)

    // The page lands in front: it is not drawn until the reader reaches the top again.
    const more = range(1, 600)
    rerender(<Transcript {...p} events={more} />)
    expect(screen.queryByTestId('older-loading')).toBeNull()
    expect(drawn(container)).toBe(400)
    expect(screen.queryByText('message 200')).toBeNull()
    act(() => intersect())
    expect(drawn(container)).toBe(600)
    expect(screen.getByText('message 1')).toBeInTheDocument()
    // Still more on the server: the next page, once.
    act(() => intersect())
    expect(onLoadOlder).toHaveBeenCalledTimes(2)
  })

  it('does not ask again after a page that brought nothing, until something arrives', () => {
    const onLoadOlder = vi.fn()
    const p = props({ events: range(1, 100), hasOlder: true, onLoadOlder })
    const { rerender } = render(<Transcript {...p} />)
    act(() => intersect())
    expect(onLoadOlder).toHaveBeenCalledTimes(1)
    // The fetch failed (a drop): in flight, then not, and nothing new held.
    rerender(<Transcript {...p} loadingOlder />)
    rerender(<Transcript {...p} />)
    act(() => intersect())
    expect(onLoadOlder).toHaveBeenCalledTimes(1)
    // An event arrives (the reconnect brought the page): the sentinel may ask again.
    rerender(<Transcript {...p} events={range(1, 101)} />)
    act(() => intersect())
    expect(onLoadOlder).toHaveBeenCalledTimes(2)
  })

  it('after a fetch that failed, scrolling away and back to the top tries once more', () => {
    const onLoadOlder = vi.fn()
    const p = props({ events: range(1, 100), hasOlder: true, onLoadOlder })
    const { rerender } = render(<Transcript {...p} />)
    act(() => intersect())
    rerender(<Transcript {...p} loadingOlder />)
    rerender(<Transcript {...p} />)
    // Sitting at the top: no loop.
    act(() => intersect())
    expect(onLoadOlder).toHaveBeenCalledTimes(1)
    // The sentinel leaves view and comes back: exactly one more request.
    act(() => intersect(false))
    act(() => intersect())
    act(() => intersect())
    expect(onLoadOlder).toHaveBeenCalledTimes(2)
  })

  it('always draws live events appended at the bottom, without dropping the top row', () => {
    const events = range(1, 1000)
    const { container, rerender } = render(<Transcript {...props({ events })} />)
    expect(drawn(container)).toBe(300)
    rerender(<Transcript {...props({ events: [...events, ev(1001), ev(1002)] })} />)
    expect(drawn(container)).toBe(302)
    expect(screen.getByText('message 1002')).toBeInTheDocument()
    expect(screen.getByText('message 701')).toBeInTheDocument()
  })

  it('a reader following the bottom of a long live visit is drawn the newest WINDOW rows again', () => {
    const events = range(1, 300)
    const { container, rerender } = render(<Transcript {...props({ events })} />)
    expect(drawn(container)).toBe(300)
    // 700 live rows arrive while the reader stays at the bottom.
    rerender(<Transcript {...props({ events: [...events, ...range(301, 1000)] })} />)
    expect(drawn(container)).toBe(WINDOW)
    expect(screen.getByText('message 1000')).toBeInTheDocument()
    expect(screen.queryByText('message 700')).toBeNull()
  })

  it('a live window never grows past twice WINDOW for a reader at the bottom', () => {
    let events = range(1, 300)
    const { container, rerender } = render(<Transcript {...props({ events })} />)
    for (let next = 301; next <= 2000; next += 100) {
      events = [...events, ...range(next, next + 99)]
      rerender(<Transcript {...props({ events })} />)
      expect(drawn(container)).toBeLessThanOrEqual(2 * WINDOW)
      expect(drawn(container)).toBeGreaterThanOrEqual(WINDOW)
    }
  })

  it('a reader scrolled up keeps every row drawn while live rows arrive', () => {
    let events = range(1, 300)
    const { container, rerender } = render(<Transcript {...props({ events })} />)
    scrolledUp(container)
    events = [...events, ...range(301, 1000)]
    rerender(<Transcript {...props({ events })} />)
    expect(drawn(container)).toBe(1000)
  })

  it('goes back to the newest WINDOW rows when the session changes', () => {
    const { container, rerender } = render(<Transcript {...props({ events: range(1, 1000) })} />)
    act(() => intersect())
    expect(drawn(container)).toBe(600)
    const other = Array.from({ length: 1000 }, (_, i) => ev(i + 1, 'other'))
    rerender(<Transcript {...props({ sessionId: 'other', events: other })} />)
    expect(drawn(container)).toBe(300)
  })

  it('draws a short transcript whole, with no sentinel', () => {
    const { container } = render(<Transcript {...props({ events: range(1, 40) })} />)
    expect(drawn(container)).toBe(40)
    expect(screen.queryByTestId('transcript-more')).toBeNull()
    expect(screen.getByTestId('ready-card')).toBeInTheDocument()
  })

  it('counts a subagent card as one row, however many events it holds', () => {
    // 299 messages, then a subagent with 400 tool calls inside: 300 top-level rows, all drawn.
    const sub = (seq: number, kind: string, payload: unknown): AgentEvent => ({
      ...ev(seq), client_event_id: `${SESSION}-sub${seq}`, kind: kind as AgentEvent['kind'], payload,
    })
    const children = Array.from({ length: 400 }, (_, i) => sub(1000 + i, 'tool_call',
      { tool: 'Bash', call_id: `c${i}`, input: { command: 'ls' }, parent_call_id: 'A' }))
    for (const compact of [true, false]) {
      const events = [
        ...range(1, 299),
        sub(300, 'subagent_started', { child_id: 'A', agent_type: 'Explore', model: '', summary: 'Survey' }),
        ...children,
      ]
      const { container, unmount } = render(<Transcript {...props({ events, compact })} />)
      expect(drawn(container)).toBe(299)
      expect(screen.getByText('message 1')).toBeInTheDocument()
      expect(screen.getAllByTestId('subagent-card')).toHaveLength(1)
      expect(screen.queryByTestId('transcript-more')).toBeNull()
      unmount()
    }
  })

  it('keeps a subagent card open when it is drawn again', () => {
    const sub = (seq: number, kind: string, payload: unknown): AgentEvent => ({
      ...ev(seq), client_event_id: `${SESSION}-sub${seq}`, kind: kind as AgentEvent['kind'], payload,
    })
    const events = [
      ...range(1, 3),
      sub(4, 'subagent_started', { child_id: 'A', agent_type: 'Explore', model: '', summary: 'Survey' }),
      sub(5, 'tool_call', { tool: 'Bash', call_id: 'c1', input: { command: 'ls' }, parent_call_id: 'A' }),
    ]
    const { rerender } = render(<Transcript {...props({ events })} />)
    const row = () => screen.getByTestId('subagent-row')
    expect(row()).toHaveAttribute('aria-expanded', 'false')
    act(() => { row().click() })
    const first = screen.getByTestId('subagent-card')
    expect(row()).toHaveAttribute('aria-expanded', 'true')
    // The other view draws every row anew: a new card, still open.
    rerender(<Transcript {...props({ events, compact: false })} />)
    expect(screen.getByTestId('subagent-card')).not.toBe(first)
    expect(row()).toHaveAttribute('aria-expanded', 'true')
    expect(screen.getByTestId('subagent-body')).toBeInTheDocument()
    // Closed by hand, it stays closed when drawn again.
    act(() => { row().click() })
    rerender(<Transcript {...props({ events })} />)
    expect(row()).toHaveAttribute('aria-expanded', 'false')
  })

  it('forgets which subagent cards were open when another session is drawn', () => {
    const sub = (session: string, seq: number, kind: string, payload: unknown): AgentEvent => ({
      ...ev(seq, session), client_event_id: `${session}-sub${seq}`, kind: kind as AgentEvent['kind'], payload,
    })
    // The same call id in two sessions.
    const of = (session: string) => [
      ev(1, session),
      sub(session, 2, 'subagent_started', { child_id: 'A', agent_type: 'Explore', model: '', summary: 'Survey' }),
    ]
    const { rerender } = render(<Transcript {...props({ events: of(SESSION) })} />)
    act(() => { screen.getByTestId('subagent-row').click() })
    expect(screen.getByTestId('subagent-row')).toHaveAttribute('aria-expanded', 'true')
    rerender(<Transcript {...props({ sessionId: 'other', events: of('other'), compact: false })} />)
    expect(screen.getByTestId('subagent-row')).toHaveAttribute('aria-expanded', 'false')
  })

  it('windows the full-card (not compact) view too', () => {
    const { container } = render(<Transcript {...props({ events: range(1, 1000), compact: false })} />)
    expect(drawn(container)).toBe(300)
    act(() => intersect())
    expect(drawn(container)).toBe(600)
  })
})

describe('Transcript keeps the reader in place as rows are revealed above', () => {
  // jsdom has no layout: every child of the timeline is 10px tall, stacked in order.
  let timeline: HTMLElement
  const realRect = Element.prototype.getBoundingClientRect
  beforeEach(() => {
    Element.prototype.scrollIntoView = vi.fn()
    Element.prototype.getBoundingClientRect = function (this: Element) {
      const parent = this.parentElement
      if (parent && parent.classList.contains('agent-timeline')) {
        const i = Array.prototype.indexOf.call(parent.children, this)
        return { top: i * 10 - parent.scrollTop, bottom: i * 10 + 10 - parent.scrollTop, left: 0, right: 0, width: 0, height: 10, x: 0, y: 0, toJSON() {} } as DOMRect
      }
      return { top: 0, bottom: 0, left: 0, right: 0, width: 0, height: 0, x: 0, y: 0, toJSON() {} } as DOMRect
    }
  })
  afterEach(() => {
    Element.prototype.getBoundingClientRect = realRect
  })

  function layout(container: HTMLElement) {
    timeline = container.querySelector('.agent-timeline') as HTMLElement
    Object.defineProperty(timeline, 'scrollHeight', { configurable: true, get: () => timeline.children.length * 10 })
    Object.defineProperty(timeline, 'clientHeight', { configurable: true, value: 100 })
  }
  const viewTop = (el: Element) => el.getBoundingClientRect().top

  it('a reader scrolled up sees the same row at the same place after a reveal', () => {
    const { container } = render(<Transcript {...props({ events: range(1, 1000) })} />)
    layout(container)
    // Scrolled up near the top of what is drawn.
    scrollUpTo(timeline, 20)
    const first = screen.getByText('message 701').closest('.agent-card') as Element
    const before = viewTop(first)

    act(() => intersect())
    expect(drawn(container)).toBe(600)
    // 300 rows of 10px went in above it: the view moved down by exactly that.
    expect(timeline.scrollTop).toBe(3020)
    expect(viewTop(first)).toBe(before)
  })

  it('does not move the view again where the browser already kept the row in place', () => {
    const { container } = render(<Transcript {...props({ events: range(1, 1000) })} />)
    layout(container)
    scrollUpTo(timeline, 20)
    // A browser with scroll anchoring moves the scroll position by what went in above the row it
    // anchors to, as it lays the page out (here: on the first measurement after the reveal).
    const measure = Element.prototype.getBoundingClientRect
    let anchored = false
    Element.prototype.getBoundingClientRect = function (this: Element) {
      if (!anchored && this.parentElement === timeline && drawn(container) === 600) {
        anchored = true
        timeline.scrollTop += 3000
      }
      return measure.call(this)
    }
    act(() => intersect())
    expect(drawn(container)).toBe(600)
    expect(timeline.scrollTop).toBe(3020)
  })

  it('a reader at the bottom stays at the bottom', () => {
    const scroll = vi.fn()
    Element.prototype.scrollIntoView = scroll
    const { container } = render(<Transcript {...props({ events: range(1, 1000) })} />)
    layout(container)
    timeline.scrollTop = timeline.scrollHeight - 100
    act(() => { timeline.dispatchEvent(new Event('scroll', { bubbles: true })) })
    scroll.mockClear()
    act(() => intersect())
    expect(timeline.scrollTop).toBe(timeline.scrollHeight - timeline.clientHeight)
    expect(scroll).toHaveBeenCalled()
  })
})
