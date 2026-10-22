import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, render, screen } from '@testing-library/react'
import Transcript, { WINDOW, type TranscriptProps } from './Transcript'
import { installIntersectionObserver, intersect } from '../test/intersection'
import { inputClock } from '../model/clock'
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
    expect(container.querySelector('.agent-timeline')).toHaveClass('backfilling')
    act(() => intersect())
    act(() => intersect())
    expect(onLoadOlder).toHaveBeenCalledTimes(1)

    // The page lands in front of the top of what was drawn: it is drawn in the same render that
    // takes the loading line away (the content above the reader grows, never shrinks first).
    const more = range(1, 600)
    rerender(<Transcript {...p} events={more} />)
    expect(screen.queryByTestId('older-loading')).toBeNull()
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

  it('says where the session begins once nothing older is left, above everything else', () => {
    const p = props({ events: range(1, 400), hasOlder: false })
    const { container, rerender } = render(<Transcript {...p} hasOlder />)
    expect(screen.queryByTestId('history-start')).toBeNull()
    rerender(<Transcript {...p} />)
    // Rows held above the window are not drawn yet: not the beginning.
    expect(screen.queryByTestId('history-start')).toBeNull()
    act(() => intersect())
    const start = screen.getByTestId('history-start')
    expect(start).toHaveTextContent('Beginning of the session')
    expect(container.querySelector('.agent-timeline')!.firstElementChild).toBe(start)
    rerender(<Transcript {...p} loadingOlder />)
    expect(screen.queryByTestId('history-start')).toBeNull()
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

  it('draws a landed page at once only when the top was drawn', () => {
    const { container, rerender } = render(<Transcript {...props({ events: range(301, 1000), hasOlder: true })} />)
    layout(container)
    scrollUpTo(timeline, 20)
    // The newest WINDOW rows are drawn, not the top: a page landing in front draws nothing new.
    rerender(<Transcript {...props({ events: range(1, 1000), hasOlder: true })} />)
    expect(drawn(container)).toBe(WINDOW)
  })

  it('a page landing at the very top keeps the row in view where it was', () => {
    const onLoadOlder = vi.fn()
    const p = props({ events: range(901, 1000), hasOlder: true, onLoadOlder })
    const { container, rerender } = render(<Transcript {...p} />)
    layout(container)
    scrollUpTo(timeline, 0)
    act(() => intersect())
    expect(onLoadOlder).toHaveBeenCalledTimes(1)
    rerender(<Transcript {...p} loadingOlder />)
    const first = screen.getByText('message 901').closest('.agent-card') as Element
    const before = viewTop(first)
    // 100 rows of 10px go in above the row.
    rerender(<Transcript {...p} events={range(801, 1000)} />)
    expect(viewTop(first)).toBe(before)
    expect(timeline.scrollTop).toBe(1000)
  })

  it('after a page that moved the top out of reach, sitting still asks for nothing more; scrolling up again does', async () => {
    const onLoadOlder = vi.fn()
    const p = props({ events: range(981, 1000), hasOlder: true, onLoadOlder })
    const { container, rerender } = render(<Transcript {...p} />)
    layout(container)
    scrollUpTo(timeline, 0)
    act(() => intersect())
    expect(onLoadOlder).toHaveBeenCalledTimes(1)
    rerender(<Transcript {...p} loadingOlder />)
    // A long page lands (200 rows of 10px in a 100px view): the top is far above the view now.
    rerender(<Transcript {...p} events={range(781, 1000)} />)
    expect(timeline.scrollTop).toBe(2000)
    act(() => intersect())
    await act(() => new Promise(r => setTimeout(r, 500)))
    act(() => intersect())
    expect(onLoadOlder).toHaveBeenCalledTimes(1)
    // The reader scrolls up again: the next page.
    timeline.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowUp', bubbles: true }))
    await act(() => new Promise(r => setTimeout(r, 0)))
    expect(onLoadOlder).toHaveBeenCalledTimes(2)
  })

  it('after a page that left the reader at the top, asks for the next by itself (after the gap)', async () => {
    const onLoadOlder = vi.fn()
    const p = props({ events: range(981, 1000), hasOlder: true, onLoadOlder })
    const { container, rerender } = render(<Transcript {...p} />)
    layout(container)
    scrollUpTo(timeline, 0)
    act(() => intersect())
    expect(onLoadOlder).toHaveBeenCalledTimes(1)
    rerender(<Transcript {...p} loadingOlder />)
    // A short page: the top of what is drawn is still within reach.
    rerender(<Transcript {...p} events={range(976, 1000)} />)
    act(() => intersect())
    await act(() => new Promise(r => setTimeout(r, 500)))
    expect(onLoadOlder).toHaveBeenCalledTimes(2)
  })

  // A long page lands with the reader at the top: it moves the top out of reach (gated). The mock
  // IntersectionObserver then says the top is near again without any scroll, so only the input under
  // test can open the gate. Input timing runs on a hand-driven clock.
  async function gatedLanding() {
    const onLoadOlder = vi.fn()
    const p = props({ events: range(981, 1000), hasOlder: true, onLoadOlder })
    const { container, rerender } = render(<Transcript {...p} />)
    layout(container)
    scrollUpTo(timeline, 0)
    act(() => intersect())
    rerender(<Transcript {...p} loadingOlder />)
    const clock = { t: realNow() + 1000 }
    inputClock.now = () => clock.t
    rerender(<Transcript {...p} events={range(781, 1000)} />)
    act(() => intersect())
    await act(() => new Promise(r => setTimeout(r, 450)))
    expect(onLoadOlder).toHaveBeenCalledTimes(1)
    return { onLoadOlder, clock }
  }
  const realNow = inputClock.now
  const wheel = (deltaY = -120) => timeline.dispatchEvent(new WheelEvent('wheel', { deltaY, bubbles: true, cancelable: true }))
  const settle = (ms = 0) => act(() => new Promise(r => setTimeout(r, ms)))

  it('a wheel counts only when its gesture began after the page landed', async () => {
    try {
      const { onLoadOlder, clock } = await gatedLanding()
      // A gesture that began before the landing (a momentum tail, events 16 ms apart): nothing.
      clock.t -= 48
      for (let i = 0; i < 6; i++) {
        wheel()
        clock.t += 16
        await settle()
      }
      expect(onLoadOlder).toHaveBeenCalledTimes(1)
      // A pause, then a new gesture: the next page.
      clock.t += 200
      wheel()
      await settle()
      expect(onLoadOlder).toHaveBeenCalledTimes(2)
    } finally {
      inputClock.now = realNow
    }
  })

  it('a scroll a momentum wheel made is left to the wheel rule', async () => {
    try {
      const { onLoadOlder, clock } = await gatedLanding()
      // A momentum tail still coming (its gesture began before the landing), and the scroll it makes,
      // past the window: neither counts.
      clock.t -= 32
      for (let i = 0; i < 16; i++) {
        clock.t += 16
        wheel()
        timeline.scrollTop -= 10
        act(() => { timeline.dispatchEvent(new Event('scroll', { bubbles: true })) })
        await settle()
      }
      await settle(200)
      expect(onLoadOlder).toHaveBeenCalledTimes(1)
    } finally {
      inputClock.now = realNow
    }
  })

  it('a wheel just after a page went in is not a new gesture, however long since the last', async () => {
    try {
      const { onLoadOlder, clock } = await gatedLanding()
      // The first wheel event after drawing the page (held up behind it): 50 ms after it went in.
      clock.t += 50
      wheel()
      await settle()
      expect(onLoadOlder).toHaveBeenCalledTimes(1)
      // A pause, then a wheel: a new gesture.
      clock.t += 200
      wheel()
      await settle()
      expect(onLoadOlder).toHaveBeenCalledTimes(2)
    } finally {
      inputClock.now = realNow
    }
  })

  it('a scroll just after a landing counts once its window has passed', async () => {
    try {
      const { onLoadOlder, clock } = await gatedLanding()
      // A scroll the input listeners did not see (the scrollbar), 50 ms after the landing.
      clock.t += 50
      timeline.scrollTop -= 100
      act(() => { timeline.dispatchEvent(new Event('scroll', { bubbles: true })) })
      expect(onLoadOlder).toHaveBeenCalledTimes(1)
      await settle(150)
      expect(onLoadOlder).toHaveBeenCalledTimes(2)
    } finally {
      inputClock.now = realNow
    }
  })

  it('a key pressed within the window after a landing is not lost', async () => {
    try {
      const { onLoadOlder, clock } = await gatedLanding()
      clock.t += 20
      timeline.dispatchEvent(new KeyboardEvent('keydown', { key: 'PageUp', bubbles: true }))
      await settle()
      expect(onLoadOlder).toHaveBeenCalledTimes(2)
    } finally {
      inputClock.now = realNow
    }
  })

  it('tells a screen reader, politely, when an earlier page lands near the top', async () => {
    const wait = (ms: number) => act(() => new Promise(r => setTimeout(r, ms)))
    const p = props({ events: range(901, 1000), hasOlder: true })
    const { rerender } = render(<Transcript {...p} />)
    const status = screen.getByTestId('older-status')
    // A live region that is always there (not a second role=status beside the host's own).
    expect(status).toHaveAttribute('aria-live', 'polite')
    expect(status).toHaveTextContent('')
    act(() => intersect())
    rerender(<Transcript {...p} loadingOlder />)
    rerender(<Transcript {...p} events={range(801, 1000)} />)
    await wait(1100)
    expect(screen.getByTestId('older-status')).toHaveTextContent('Loaded earlier messages')
  })

  it('says one "Loading" and one "Loaded" for a run of slow pages, not both in turn for each', async () => {
    const wait = (ms: number) => act(() => new Promise(r => setTimeout(r, ms)))
    const p = props({ events: range(951, 1000), hasOlder: true })
    const { rerender } = render(<Transcript {...p} />)
    act(() => intersect())
    const heard: string[] = []
    const status = screen.getByTestId('older-status')
    const mo = new MutationObserver(() => {
      const t = status.textContent ?? ''
      if (heard[heard.length - 1] !== t) heard.push(t)
    })
    mo.observe(status, { childList: true, characterData: true, subtree: true })
    // Three slow pages, 400 ms apart, each drawing a few rows.
    for (let i = 0; i < 3; i++) {
      rerender(<Transcript {...p} events={range(951 - 10 * i, 1000)} loadingOlder />)
      await wait(350)
      await wait(100)
      rerender(<Transcript {...p} events={range(941 - 10 * i, 1000)} />)
      await wait(400)
    }
    await wait(1200)
    mo.disconnect()
    expect(heard).toEqual(['Loading earlier messages…', 'Loaded earlier messages'])
  })

  it('says nothing about loading when another session opens', async () => {
    const wait = (ms: number) => act(() => new Promise(r => setTimeout(r, ms)))
    const p = props({ events: range(901, 1000), hasOlder: true })
    const { rerender } = render(<Transcript {...p} />)
    act(() => intersect())
    // Session B arrives in one step (a cached switch).
    rerender(<Transcript {...p} sessionId="B" events={Array.from({ length: 30 }, (_, i) => ev(i + 1, 'B'))} />)
    await wait(1200)
    expect(screen.getByTestId('older-status')).toHaveTextContent('')
  })

  it('another session starts its timing afresh', async () => {
    const onLoadOlder = vi.fn()
    const p = props({ events: range(981, 1000), hasOlder: true, onLoadOlder })
    const { container, rerender } = render(<Transcript {...p} />)
    layout(container)
    scrollUpTo(timeline, 0)
    act(() => intersect())
    rerender(<Transcript {...p} loadingOlder />)
    rerender(<Transcript {...p} events={range(781, 1000)} />)
    expect(onLoadOlder).toHaveBeenCalledTimes(1)
    // Session B opens with its top already in reach: asked for at once, no scroll needed.
    const other = Array.from({ length: 30 }, (_, i) => ev(i + 1, 'B'))
    rerender(<Transcript {...p} sessionId="B" events={other} />)
    act(() => intersect())
    expect(onLoadOlder).toHaveBeenCalledTimes(2)
  })

  it('shows the loading pill only near the top, and only for a page slower than 300 ms', async () => {
    const wait = (ms: number) => act(() => new Promise(r => setTimeout(r, ms)))
    const p = props({ events: range(901, 1000), hasOlder: true })
    const { rerender } = render(<Transcript {...p} loadingOlder />)
    // Not near the top (the reader is at the end): no pill however long it takes.
    await wait(350)
    expect(screen.queryByTestId('older-loading')).toBeNull()
    rerender(<Transcript {...p} />)
    act(() => intersect())
    // Near the top, a fast page: none.
    rerender(<Transcript {...p} loadingOlder />)
    await wait(150)
    expect(screen.queryByTestId('older-loading')).toBeNull()
    rerender(<Transcript {...p} />)
    await wait(300)
    expect(screen.queryByTestId('older-loading')).toBeNull()
    // A slow page: after 300 ms.
    rerender(<Transcript {...p} loadingOlder />)
    await wait(200)
    expect(screen.queryByTestId('older-loading')).toBeNull()
    await wait(150)
    expect(screen.getByTestId('older-loading')).toHaveTextContent('Loading earlier messages')
    rerender(<Transcript {...p} />)
    expect(screen.queryByTestId('older-loading')).toBeNull()
  })

  it('a page landing while the reader follows the end is not drawn until they reach it', () => {
    const p = props({ events: range(901, 1000), hasOlder: true })
    const { container, rerender } = render(<Transcript {...p} loadingOlder />)
    expect(drawn(container)).toBe(100)
    rerender(<Transcript {...p} events={range(801, 1000)} />)
    expect(drawn(container)).toBe(100)
    act(() => intersect())
    expect(drawn(container)).toBe(200)
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
