import { Activity } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen } from '@testing-library/react'
import Transcript, { type TranscriptProps } from './Transcript'
import { installIntersectionObserver } from '../test/intersection'
import { SESSION } from '../test/chat'
import type { AgentEvent } from '../types'

// Following new output: any upward scroll by the reader stops it (even inside the newest card, so a
// phone reader is not pulled away from what they read); reaching the very end or sending resumes
// it. While it is off, new output below puts up a "New output" pill that takes them there.

function ev(seq: number): AgentEvent {
  return {
    type: 'agent_event',
    session_id: SESSION,
    client_event_id: `${SESSION}-e${seq}`,
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
    sessionId: SESSION, events: [], streaming: '', compact: true, liveIds: NONE,
    showSkeleton: false, loadingOlder: false, hasOlder: false, starting: false, attempt: null,
    clockOffset: 0, readyCard: null, working: false, waiting: false, since: null, waitingOn: null,
    queued: NO_QUEUE, queueState: 'sending', steers: true, pin: 0,
    ...over,
  }
}

// jsdom has no layout: the timeline is given a height of 1,000 px of content in a 100 px view.
function timelineOf(c: HTMLElement) {
  const el = c.querySelector('.agent-timeline') as HTMLElement
  Object.defineProperty(el, 'scrollHeight', { configurable: true, value: 1000 })
  Object.defineProperty(el, 'clientHeight', { configurable: true, value: 100 })
  return el
}
function scrollTo(el: HTMLElement, top: number, reader = top < el.scrollTop) {
  if (reader) fireEvent.pointerDown(el, { button: 0 })
  el.scrollTop = top
  act(() => { el.dispatchEvent(new Event('scroll', { bubbles: true })) })
  if (reader) fireEvent.pointerUp(window)
}

let into: ReturnType<typeof vi.fn>
beforeEach(() => {
  installIntersectionObserver()
  into = vi.fn()
  Element.prototype.scrollIntoView = into as unknown as typeof Element.prototype.scrollIntoView
})

describe('Transcript follows new output', () => {
  it('stops following on an upward scroll, even near the end, and resumes at the very end', () => {
    const { container, rerender } = render(<Transcript {...props({ events: range(1, 3) })} />)
    const tl = timelineOf(container)
    scrollTo(tl, 900) // at the end
    scrollTo(tl, 860) // 40 px up: once "near the bottom", now the reader reading
    into.mockClear()
    rerender(<Transcript {...props({ events: range(1, 4) })} />)
    expect(into).not.toHaveBeenCalled()
    // Down again, but not to the end: still not following.
    scrollTo(tl, 880)
    rerender(<Transcript {...props({ events: range(1, 5) })} />)
    expect(into).not.toHaveBeenCalled()
    // At the end (within 8 px): following again.
    scrollTo(tl, 895)
    rerender(<Transcript {...props({ events: range(1, 6) })} />)
    expect(into).toHaveBeenCalledTimes(1)
  })

  it('a send resumes following', () => {
    const { container, rerender } = render(<Transcript {...props({ events: range(1, 3) })} />)
    const tl = timelineOf(container)
    scrollTo(tl, 900)
    scrollTo(tl, 500)
    into.mockClear()
    rerender(<Transcript {...props({ events: range(1, 3), pin: 1 })} />)
    expect(into).toHaveBeenCalled()
    into.mockClear()
    rerender(<Transcript {...props({ events: range(1, 4), pin: 1 })} />)
    expect(into).toHaveBeenCalledTimes(1)
  })

  it.each(['send', 'session'])('resumes following for a real %s change while Activity is hidden', change => {
    const initial = props({ events: range(1, 3) })
    const view = (p: TranscriptProps, hidden = false) => <Activity mode={hidden ? 'hidden' : 'visible'}><Transcript {...p} /></Activity>
    const { container, rerender } = render(view(initial))
    const tl = timelineOf(container)
    scrollTo(tl, 900)
    scrollTo(tl, 500)
    rerender(view(initial, true))
    const changed = change === 'send' ? { ...initial, pin: 1 } : { ...initial, sessionId: 'second' }
    rerender(view(changed, true))
    into.mockClear()
    rerender(view(changed))
    expect(into).toHaveBeenCalled()
    expect(tl.scrollTop).toBe(900)
  })

  it('a shrink that moves the view (the browser, not the reader) does not stop following', () => {
    const { container, rerender } = render(<Transcript {...props({ events: range(1, 3) })} />)
    const tl = timelineOf(container)
    scrollTo(tl, 900)
    // The content under the view got 50 px shorter and the browser moved the view up with it.
    Object.defineProperty(tl, 'scrollHeight', { configurable: true, value: 950 })
    scrollTo(tl, 850, false)
    into.mockClear()
    rerender(<Transcript {...props({ events: range(1, 4) })} />)
    expect(into).toHaveBeenCalledTimes(1)
  })

  it('a one-notch upward scroll while the content changed height still stops following', () => {
    const { container, rerender } = render(<Transcript {...props({ events: range(1, 3) })} />)
    const tl = timelineOf(container)
    scrollTo(tl, 900)
    // An image loads (or a card opens): the content is 60 px taller, then the reader scrolls up once.
    Object.defineProperty(tl, 'scrollHeight', { configurable: true, value: 1060 })
    scrollTo(tl, 880)
    into.mockClear()
    rerender(<Transcript {...props({ events: range(1, 4) })} />)
    expect(into).not.toHaveBeenCalled()
    expect(screen.getByTestId('new-output')).toBeInTheDocument()
  })

  it('our own scroll does not read as the reader scrolling up', () => {
    const { container, rerender } = render(<Transcript {...props({ events: range(1, 3) })} />)
    const tl = timelineOf(container)
    scrollTo(tl, 900)
    // Trimming content reduces the maximum by the whole upward movement.
    // The browser is still at the end, so following goes on.
    rerender(<Transcript {...props({ events: range(1, 4) })} />)
    Object.defineProperty(tl, 'scrollHeight', { configurable: true, value: 800 })
    scrollTo(tl, 700, false)
    into.mockClear()
    rerender(<Transcript {...props({ events: range(1, 5) })} />)
    expect(into).toHaveBeenCalledTimes(1)
  })

  it('while following, the working line coming into view scrolls to it; while not, it does not', () => {
    const { container, rerender } = render(<Transcript {...props({ events: range(1, 3) })} />)
    into.mockClear()
    rerender(<Transcript {...props({ events: range(1, 3), working: true })} />)
    expect(into).toHaveBeenCalledTimes(1)
    const tl = timelineOf(container)
    scrollTo(tl, 900) // the follow's own scroll event
    scrollTo(tl, 500)
    into.mockClear()
    rerender(<Transcript {...props({ events: range(1, 3), working: false, waiting: true })} />)
    rerender(<Transcript {...props({ events: range(1, 3), waiting: false })} />)
    expect(into).not.toHaveBeenCalled()
    expect(screen.queryByTestId('new-output')).not.toBeInTheDocument()
  })
})

// The reader's input says they are going up before a scroll event can: a busy session scrolls to the
// end on almost every event, so their first upward scroll event often lands in the few milliseconds
// a scroll of ours is still taken as ours.
function wheel(el: Element, deltaY: number) {
  act(() => { el.dispatchEvent(Object.assign(new Event('wheel', { bubbles: true }), { deltaY })) })
}
function touch(el: Element, type: string, clientY: number, clientX = 100, identifier = 0) {
  const e = new Event(type, { bubbles: true })
  const changed = [{ identifier, clientY, clientX }]
  Object.defineProperty(e, 'changedTouches', { value: changed })
  Object.defineProperty(e, 'touches', { value: type === 'touchend' || type === 'touchcancel' ? [] : changed })
  act(() => { el.dispatchEvent(e) })
}
function key(el: Element, k: string, shiftKey = false) {
  act(() => { el.dispatchEvent(new KeyboardEvent('keydown', { key: k, shiftKey, bubbles: true })) })
}

describe('Transcript observes movement independently of input order', () => {
  let now = 1000
  beforeEach(() => {
    now = 1000
    vi.spyOn(performance, 'now').mockImplementation(() => now)
  })
  afterEach(() => {
    vi.restoreAllMocks()
    vi.useRealTimers()
  })

  it('a wheel up stops following even 10 ms after a scroll of ours', () => {
    const { container, rerender } = render(<Transcript {...props({ events: range(1, 3) })} />)
    const tl = timelineOf(container)
    scrollTo(tl, 900)
    rerender(<Transcript {...props({ events: range(1, 4) })} />) // we follow: a scroll of ours
    now += 10
    wheel(tl, -120)
    scrollTo(tl, 860) // inside the window, taken as ours by the scroll rule
    into.mockClear()
    rerender(<Transcript {...props({ events: range(1, 5) })} />)
    expect(into).not.toHaveBeenCalled()
    expect(screen.getByTestId('new-output')).toBeInTheDocument()
  })

  it('a wheel down does not stop following', () => {
    const { container, rerender } = render(<Transcript {...props({ events: range(1, 3) })} />)
    const tl = timelineOf(container)
    scrollTo(tl, 900)
    wheel(tl, 120)
    into.mockClear()
    rerender(<Transcript {...props({ events: range(1, 4) })} />)
    expect(into).toHaveBeenCalledTimes(1)
  })

  it('a view within 8 px of the end follows regardless of input', () => {
    const { container, rerender } = render(<Transcript {...props({ events: range(1, 3) })} />)
    const tl = timelineOf(container)
    scrollTo(tl, 900)
    wheel(tl, -3)
    scrollTo(tl, 897)
    into.mockClear()
    rerender(<Transcript {...props({ events: range(1, 4) })} />)
    expect(into).toHaveBeenCalledTimes(1)
    into.mockClear()
    // Back down to the end afterwards: following again.
    now += 500
    scrollTo(tl, 900)
    rerender(<Transcript {...props({ events: range(1, 5) })} />)
    expect(into).toHaveBeenCalledTimes(1)
  })

  it('a finger moving down the screen stops following', () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    const { container, rerender } = render(<Transcript {...props({ events: range(1, 3) })} />)
    const tl = timelineOf(container)
    scrollTo(tl, 900)
    touch(tl, 'touchstart', 300)
    touch(tl, 'touchmove', 308) // 8 px: a tap's wobble, not yet
    into.mockClear()
    rerender(<Transcript {...props({ events: range(1, 3), working: true })} />)
    touch(tl, 'touchmove', 315)
    scrollTo(tl, 870, false)
    touch(tl, 'touchend', 315)
    act(() => { vi.advanceTimersByTime(1000) })
    // The follow missed during the touch is not made up: the reader went up.
    expect(into).not.toHaveBeenCalled()
    rerender(<Transcript {...props({ events: range(1, 4), working: true })} />)
    expect(into).not.toHaveBeenCalled()
    expect(screen.getByTestId('new-output')).toBeInTheDocument()
  })

  it('a finger that goes up the screen, then down, counts from where it turned', () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    const { container, rerender } = render(<Transcript {...props({ events: range(1, 3) })} />)
    const tl = timelineOf(container)
    scrollTo(tl, 900)
    touch(tl, 'touchstart', 300)
    touch(tl, 'touchmove', 250)
    touch(tl, 'touchmove', 200)
    touch(tl, 'touchmove', 215) // 15 px down from the turn, still 85 px above the start
    scrollTo(tl, 870, false)
    touch(tl, 'touchend', 215)
    act(() => { vi.advanceTimersByTime(1000) })
    into.mockClear()
    rerender(<Transcript {...props({ events: range(1, 4) })} />)
    expect(into).not.toHaveBeenCalled()
  })

  it('a sideways swipe (on a code block, say) does not stop following', () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    const { container, rerender } = render(<Transcript {...props({ events: range(1, 3) })} />)
    const tl = timelineOf(container)
    scrollTo(tl, 900)
    touch(tl, 'touchstart', 300, 100)
    touch(tl, 'touchmove', 320, 160)
    touch(tl, 'touchend', 320, 160)
    act(() => { vi.advanceTimersByTime(1000) })
    into.mockClear()
    rerender(<Transcript {...props({ events: range(1, 4) })} />)
    expect(into).toHaveBeenCalledTimes(1)
  })

  it('input in a box inside that can still scroll up is for that box, not the timeline', () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    const { container, rerender } = render(<Transcript {...props({ events: range(1, 3) })} />)
    const tl = timelineOf(container)
    scrollTo(tl, 900)
    const box = document.createElement('pre')
    box.className = 'system-pre'
    const line = document.createElement('span')
    box.appendChild(line)
    tl.appendChild(box)
    Object.defineProperty(box, 'scrollHeight', { configurable: true, value: 500 })
    Object.defineProperty(box, 'clientHeight', { configurable: true, value: 200 })
    box.scrollTop = 120
    wheel(line, -120)
    key(line, 'PageUp')
    touch(line, 'touchstart', 300)
    touch(line, 'touchmove', 340)
    touch(line, 'touchend', 340)
    act(() => { vi.advanceTimersByTime(1000) })
    into.mockClear()
    rerender(<Transcript {...props({ events: range(1, 4) })} />)
    expect(into).toHaveBeenCalledTimes(1)
    // At its own top, the box has nowhere to go: the timeline scrolls, so following stops.
    box.scrollTop = 0
    wheel(line, -120)
    scrollTo(tl, 780, false)
    into.mockClear()
    rerender(<Transcript {...props({ events: range(1, 5) })} />)
    expect(into).not.toHaveBeenCalled()
  })

  it('a touch cancelled by the browser ends the touch like a lift', () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    const { container, rerender } = render(<Transcript {...props({ events: range(1, 3) })} />)
    const tl = timelineOf(container)
    scrollTo(tl, 900)
    into.mockClear()
    touch(tl, 'touchstart', 300)
    rerender(<Transcript {...props({ events: range(1, 4) })} />)
    touch(tl, 'touchcancel', 300)
    act(() => { vi.advanceTimersByTime(300) })
    expect(into).toHaveBeenCalledTimes(1)
    rerender(<Transcript {...props({ events: range(1, 5) })} />)
    expect(into).toHaveBeenCalledTimes(2)
  })

  it('a touch whose row was taken out from under it stops holding the view', () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    const { container, rerender } = render(<Transcript {...props({ events: range(1, 3) })} />)
    const tl = timelineOf(container)
    scrollTo(tl, 900)
    into.mockClear()
    const row = tl.querySelector('.agent-timeline > *:not([data-testid])') ?? tl.firstElementChild!
    touch(row, 'touchstart', 300)
    row.remove()
    rerender(<Transcript {...props({ events: range(1, 4) })} />)
    // Its touchend goes to the detached node and reaches neither the timeline nor the window.
    touch(row, 'touchend', 300)
    act(() => { vi.advanceTimersByTime(1499) })
    expect(into).not.toHaveBeenCalled()
    act(() => { vi.advanceTimersByTime(1) })
    expect(into).toHaveBeenCalledTimes(1)
    rerender(<Transcript {...props({ events: range(1, 5) })} />)
    expect(into).toHaveBeenCalledTimes(2)
  })

  it('a touch that ends outside the timeline still ends', () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    const { container, rerender } = render(<Transcript {...props({ events: range(1, 3) })} />)
    const tl = timelineOf(container)
    scrollTo(tl, 900)
    into.mockClear()
    touch(tl, 'touchstart', 300)
    rerender(<Transcript {...props({ events: range(1, 4) })} />)
    touch(document.body, 'touchend', 300)
    act(() => { vi.advanceTimersByTime(300) })
    expect(into).toHaveBeenCalledTimes(1)
  })

  it('a finger moving up the screen (scrolling down) does not', () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    const { container, rerender } = render(<Transcript {...props({ events: range(1, 3) })} />)
    const tl = timelineOf(container)
    scrollTo(tl, 900)
    touch(tl, 'touchstart', 300)
    touch(tl, 'touchmove', 250)
    touch(tl, 'touchend', 250)
    act(() => { vi.advanceTimersByTime(1000) })
    into.mockClear()
    rerender(<Transcript {...props({ events: range(1, 4) })} />)
    expect(into).toHaveBeenCalledTimes(1)
  })

  it('does not scroll while a finger is down or for 300 ms after, then catches up', () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    const { container, rerender } = render(<Transcript {...props({ events: range(1, 3) })} />)
    const tl = timelineOf(container)
    scrollTo(tl, 900)
    into.mockClear()
    touch(tl, 'touchstart', 300)
    rerender(<Transcript {...props({ events: range(1, 4) })} />)
    rerender(<Transcript {...props({ events: range(1, 4), streaming: 'Hel' })} />)
    expect(into).not.toHaveBeenCalled()
    touch(tl, 'touchend', 300)
    rerender(<Transcript {...props({ events: range(1, 5) })} />)
    act(() => { vi.advanceTimersByTime(299) })
    expect(into).not.toHaveBeenCalled()
    // A tap is not a scroll: still following, so once the touch settles the view goes to the end.
    act(() => { vi.advanceTimersByTime(1) })
    expect(into).toHaveBeenCalledTimes(1)
    rerender(<Transcript {...props({ events: range(1, 6) })} />)
    expect(into).toHaveBeenCalledTimes(2)
  })

  it('ArrowUp, PageUp, Home and Shift+Space stop following; typing in a box does not', () => {
    for (const [k, shift] of [['ArrowUp', false], ['PageUp', false], ['Home', false], [' ', true]] as const) {
      const { container, rerender, unmount } = render(<Transcript {...props({ events: range(1, 3) })} />)
      const tl = timelineOf(container)
      scrollTo(tl, 900)
      key(tl, k, shift)
      scrollTo(tl, 780, false)
      into.mockClear()
      rerender(<Transcript {...props({ events: range(1, 4) })} />)
      expect(into, k).not.toHaveBeenCalled()
      unmount()
    }
    const { container, rerender } = render(<Transcript {...props({ events: range(1, 3) })} />)
    const tl = timelineOf(container)
    scrollTo(tl, 900)
    const box = document.createElement('textarea')
    tl.appendChild(box)
    key(box, 'ArrowUp')
    key(tl, ' ')
    into.mockClear()
    rerender(<Transcript {...props({ events: range(1, 4) })} />)
    expect(into).toHaveBeenCalledTimes(1)
  })

  it('an upward scrollbar drag still stops following', () => {
    const { container, rerender } = render(<Transcript {...props({ events: range(1, 3) })} />)
    const tl = timelineOf(container)
    scrollTo(tl, 900)
    now += 100
    scrollTo(tl, 600)
    into.mockClear()
    rerender(<Transcript {...props({ events: range(1, 4) })} />)
    expect(into).not.toHaveBeenCalled()
  })

  it('listens once, not per render, and stops listening on unmount', () => {
    const add = vi.spyOn(HTMLElement.prototype, 'addEventListener')
    const remove = vi.spyOn(HTMLElement.prototype, 'removeEventListener')
    const addWin = vi.spyOn(window, 'addEventListener')
    const removeWin = vi.spyOn(window, 'removeEventListener')
    const addDoc = vi.spyOn(document, 'addEventListener')
    const removeDoc = vi.spyOn(document, 'removeEventListener')
    const { container, rerender, unmount } = render(<Transcript {...props({ events: range(1, 3) })} />)
    const tl = container.querySelector('.agent-timeline')
    const kinds = ['keydown', 'touchcancel', 'touchend', 'touchmove', 'touchstart', 'wheel']
    // React adds its own listeners too; only ours are counted (a touch's end is heard on the window).
    type Spy = typeof add | typeof addWin
    const names = (spy: Spy, at: unknown, tag: string) => spy.mock.calls
      .filter((c, i) => (at === null || spy.mock.contexts[i] === at) && kinds.includes(c[0] as string))
      .map(c => `${c[0] as string}${tag}`)
    const on = (el: Spy, win: Spy) => [...names(el, tl, ''), ...names(win, null, '@window')].sort()
    // Plus the long-press listeners (LongPress) on the timeline itself.
    const ours = ['keydown', 'touchcancel', 'touchcancel@window', 'touchend', 'touchend@window', 'touchmove', 'touchmove', 'touchstart', 'touchstart', 'wheel']
    const addAll = () => on(add, addWin)
    const removeAll = () => on(remove, removeWin)
    expect(addAll()).toEqual(ours)
    rerender(<Transcript {...props({ events: range(1, 4) })} />)
    rerender(<Transcript {...props({ events: range(1, 5), working: true })} />)
    expect(addAll()).toEqual(ours)
    expect(removeAll()).toEqual([])
    unmount()
    expect(removeAll()).toEqual(ours)
    expect(addDoc.mock.calls.filter(c => c[0] === 'keydown')).toHaveLength(1)
    expect(removeDoc.mock.calls.filter(c => c[0] === 'keydown')).toHaveLength(1)
  })
})

describe('the New output pill', () => {
  it('is not there while following', () => {
    const { rerender } = render(<Transcript {...props({ events: range(1, 3) })} />)
    rerender(<Transcript {...props({ events: range(1, 4) })} />)
    rerender(<Transcript {...props({ events: range(1, 4), streaming: 'Hel' })} />)
    expect(screen.queryByTestId('new-output')).not.toBeInTheDocument()
  })

  it('comes up when an event arrives while not following, and goes on reaching the end', () => {
    const { container, rerender } = render(<Transcript {...props({ events: range(1, 3) })} />)
    const tl = timelineOf(container)
    scrollTo(tl, 900)
    scrollTo(tl, 400)
    expect(screen.queryByTestId('new-output')).not.toBeInTheDocument()
    rerender(<Transcript {...props({ events: range(1, 4) })} />)
    const pill = screen.getByRole('button', { name: /New output/ })
    expect(pill).toHaveClass('chat-new-output')
    expect(pill).toHaveAttribute('aria-live', 'off')
    scrollTo(tl, 900)
    expect(screen.queryByTestId('new-output')).not.toBeInTheDocument()
  })

  it('names why following stopped (BLG-327): a wheel or scrollbar drag', () => {
    const first = render(<Transcript {...props({ events: range(1, 3) })} />)
    let tl = timelineOf(first.container)
    scrollTo(tl, 900)
    wheel(tl, -120)
    scrollTo(tl, 860)
    first.rerender(<Transcript {...props({ events: range(1, 4) })} />)
    expect(screen.getByTestId('new-output')).toHaveAttribute('data-unfollow', 'a scroll away from the end')
    expect(screen.getByTestId('new-output').title).toMatch(/^Stopped following: a scroll away from the end, at /)
    first.unmount()
    const second = render(<Transcript {...props({ events: range(1, 3) })} />)
    tl = timelineOf(second.container)
    scrollTo(tl, 900)
    scrollTo(tl, 400)
    second.rerender(<Transcript {...props({ events: range(1, 4) })} />)
    expect(screen.getByTestId('new-output').getAttribute('data-unfollow')).toMatch(/^a scroll away from the end$/)
  })

  it('comes up for a streaming reply too, and a tap goes to the end and follows again', () => {
    const { container, rerender } = render(<Transcript {...props({ events: range(1, 3) })} />)
    const tl = timelineOf(container)
    scrollTo(tl, 900)
    scrollTo(tl, 400)
    rerender(<Transcript {...props({ events: range(1, 3), streaming: 'Hel' })} />)
    into.mockClear()
    fireEvent.click(screen.getByTestId('new-output'))
    expect(into).toHaveBeenCalledTimes(1)
    expect(screen.queryByTestId('new-output')).not.toBeInTheDocument()
    into.mockClear()
    rerender(<Transcript {...props({ events: range(1, 3), streaming: 'Hello' })} />)
    expect(into).toHaveBeenCalledTimes(1)
    expect(screen.queryByTestId('new-output')).not.toBeInTheDocument()
  })

  it('an event that draws no row does not raise it; one that does, does', () => {
    const { container, rerender } = render(<Transcript {...props({ events: range(1, 3) })} />)
    const tl = timelineOf(container)
    scrollTo(tl, 900)
    scrollTo(tl, 400)
    const hidden = (seq: number, kind: AgentEvent['kind'], payload: unknown): AgentEvent => ({ ...ev(seq), kind, payload })
    const quiet = [
      ...range(1, 3),
      hidden(4, 'status_changed', { status: 'running' }),
      hidden(5, 'background_task_started', { task_id: 't1', call_id: 'c1', kind: 'bash', description: 'x' }),
    ]
    rerender(<Transcript {...props({ events: quiet })} />)
    expect(screen.queryByTestId('new-output')).not.toBeInTheDocument()
    rerender(<Transcript {...props({ events: [...quiet, ev(6)] })} />)
    expect(screen.getByTestId('new-output')).toBeInTheDocument()
  })

  it("a subagent's event folded into its card above is not new output", () => {
    const mk = (seq: number, kind: AgentEvent['kind'], payload: unknown): AgentEvent => ({ ...ev(seq), kind, payload })
    const base = [
      ev(1),
      mk(2, 'tool_call', { tool: 'Agent', call_id: 'A', input: { description: 'Survey', prompt: 'p', subagent_type: 'Explore' } }),
      mk(3, 'subagent_started', { child_id: 'A', agent_type: 'Explore', model: '', summary: 'Survey' }),
      mk(4, 'assistant_text', { text: 'Meanwhile, the main agent goes on.', done: true }),
    ]
    const { container, rerender } = render(<Transcript {...props({ events: base, liveIds: new Set(['A']) })} />)
    const tl = timelineOf(container)
    scrollTo(tl, 900)
    scrollTo(tl, 400)
    rerender(<Transcript {...props({ events: [...base, mk(5, 'tool_call', { tool: 'Bash', call_id: 's1', input: { command: 'ls' }, parent_call_id: 'A' })], liveIds: new Set(['A']) })} />)
    expect(screen.getAllByTestId('subagent-card')).toHaveLength(1)
    expect(screen.queryByTestId('new-output')).not.toBeInTheDocument()
  })

  it('an older page put in front is not new output', () => {
    const { container, rerender } = render(<Transcript {...props({ events: range(5, 8), hasOlder: true })} />)
    const tl = timelineOf(container)
    scrollTo(tl, 900)
    scrollTo(tl, 0)
    rerender(<Transcript {...props({ events: range(1, 8) })} />)
    expect(screen.queryByTestId('new-output')).not.toBeInTheDocument()
  })
})

it('saves the reader position after an upward scroll with unchanged geometry', () => {
  const onScrollLeave = vi.fn()
  const { container, unmount } = render(<Transcript {...props({ events: range(1, 3), onScrollLeave })} />)
  const tl = timelineOf(container)
  tl.scrollTop = 900
  act(() => { tl.dispatchEvent(new Event('scroll', { bubbles: true })) })
  scrollTo(tl, 0)
  unmount()
  expect(onScrollLeave).toHaveBeenLastCalledWith(expect.objectContaining({ follow: false, top: 0 }))
})

it('lets a scrollbar drag stop following during busy programmatic scrolling', () => {
  const { container, rerender } = render(<Transcript {...props({ events: range(1, 3) })} />)
  const tl = timelineOf(container)
  rerender(<Transcript {...props({ events: range(1, 3), streaming: 'Working' })} />)
  fireEvent.pointerDown(tl, { button: 0 })
  tl.scrollTop = 700
  act(() => { tl.dispatchEvent(new Event('scroll', { bubbles: true })) })
  into.mockClear()
  rerender(<Transcript {...props({ events: range(1, 3), streaming: 'Still working' })} />)
  expect(into).not.toHaveBeenCalled()
})

it('keeps following after movement without a reader gesture', () => {
  const onScrollLeave = vi.fn()
  const { container, rerender, unmount } = render(<Transcript {...props({ events: range(1, 3), onScrollLeave })} />)
  const tl = timelineOf(container)
  scrollTo(tl, 900, false)
  scrollTo(tl, 0, false)
  into.mockClear()
  rerender(<Transcript {...props({ events: range(1, 4), onScrollLeave })} />)
  expect(into).toHaveBeenCalledTimes(1)
  unmount()
  expect(onScrollLeave).toHaveBeenLastCalledWith({ follow: true })
})

it('preserves subpixel anchor corrections across repeated late row resizes', () => {
 const observers: {nodes: Set<Element>; notify: () => void}[] = []
 vi.stubGlobal('ResizeObserver', class {
  nodes = new Set<Element>()
  constructor(callback: ResizeObserverCallback) { observers.push({nodes: this.nodes, notify: () => callback([], this as unknown as ResizeObserver)}) }
  observe(node: Element) { this.nodes.add(node) } unobserve(node: Element) { this.nodes.delete(node) } disconnect() { this.nodes.clear() }
 })
 const view = render(<Transcript {...props({events: range(1, 10)})} />)
 try {
  const tl = timelineOf(view.container)
  let top = 0, growth = 0
  Object.defineProperty(tl, 'scrollTop', {configurable: true, get: () => top, set: value => { top = Math.round(value) }})
  const rect = (y: number, height: number) => ({x: 0, y, top: y, bottom: y + height, left: 0, right: 100, width: 100, height, toJSON() {}})
  vi.spyOn(tl, 'getBoundingClientRect').mockImplementation(() => rect(0, 100))
  const rows = [...tl.children]
  rows.forEach((row, i) => { vi.spyOn(row, 'getBoundingClientRect').mockImplementation(() => rect(i * 100 + growth - top, 100)) })
  scrollTo(tl, 900); scrollTo(tl, 400)
  const anchor = rows[4], before = anchor.getBoundingClientRect().top
  const observer = observers.find(item => item.nodes.has(tl))!
  expect(observer).toBeTruthy()
  for (let i = 0; i < 8; i++) {
   act(() => { growth += 0.25; observer.notify() })
   expect(Math.abs(anchor.getBoundingClientRect().top - before)).toBeLessThan(1)
  }
  expect(tl.scrollTop).toBe(402)
 } finally { view.unmount(); vi.unstubAllGlobals() }
})
