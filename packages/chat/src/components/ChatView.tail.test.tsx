import { beforeEach, describe, expect, it, vi } from 'vitest'
import { act, render, screen } from '@testing-library/react'
import ChatView from './ChatView'
import { useTranscriptStore } from '../hooks/useTranscriptStore'
import { createFakeTransport, type FakeTransport } from '../test/fakeTransport'
import { installIntersectionObserver, intersect } from '../test/intersection'
import { SESSION, meta, resetChatState } from '../test/chat'
import type { AgentEvent } from '../types'

const session = meta({ status: 'idle', started_at: '2026-09-30T00:00:00Z' })

function ev(seq: number): AgentEvent {
  return {
    type: 'agent_event',
    session_id: SESSION,
    client_event_id: `e${seq}`,
    seq,
    ts: '2026-09-30T00:00:00Z',
    kind: 'user_message',
    payload: { text: `message ${seq}`, source: 'chat' },
  }
}

const range = (from: number, to: number) => Array.from({ length: to - from + 1 }, (_, i) => ev(from + i))

// The server's answer to the first request: the newest 200 events of a 300-event transcript.
function deliverTail(t: FakeTransport) {
  act(() => {
    for (const e of range(101, 300)) t.emit(e)
    t.replayDone({ lastSeq: 300, hasMore: false, older: true, hasOlder: true, firstSeq: 101 })
  })
}

// The rest of it, as the answer to the older page the view asked for.
async function deliverOlder(t: FakeTransport) {
  await act(async () => {
    t.answerOlder({ events: range(1, 100), hasOlder: false, firstSeq: 1 })
  })
}

// A page is in flight: the timeline hides its scrollbar thumb (the loading pill itself shows only
// near the top and only for a slow page: Transcript.window.test.tsx).
const inFlight = () => !!document.querySelector('.agent-timeline.backfilling')

function mount(t: FakeTransport = createFakeTransport()) {
  return { t, ...render(<ChatView session={SESSION} transport={t.transport} meta={session} />) }
}

describe('ChatView loads a long transcript from its end', () => {
  beforeEach(() => {
    resetChatState()
    Element.prototype.scrollIntoView = vi.fn()
  })

  it('asks for the newest events first', () => {
    const { t } = mount()
    expect(t.subscriptions).toEqual([{ sessionId: SESSION, afterSeq: 0, tail: true }])
  })

  it('shows the conversation as soon as the newest events are in, and fetches the older ones behind it', () => {
    const { t, container } = mount()
    expect(screen.getByTestId('history-loading')).toBeTruthy()

    deliverTail(t)

    // No skeleton and a visible timeline, while older events remain to come.
    expect(screen.queryByTestId('history-loading')).toBeNull()
    expect(container.querySelector('.agent-timeline')?.classList.contains('loading')).toBe(false)
    expect(inFlight()).toBe(true)
    // the scrollbar thumb is not drawn while the transcript is still growing
    expect(container.querySelector('.agent-timeline')?.classList.contains('backfilling')).toBe(true)
    // ...and the next page is already being asked for, from just before the oldest one held.
    expect(t.older).toEqual([{ sessionId: SESSION, beforeSeq: 101, limit: 200 }])
    expect(screen.getByText('message 300')).toBeInTheDocument()
  })

  it('fetches one older page behind the tail and no more until asked, then drops the indicator', async () => {
    const { t, container } = mount()
    deliverTail(t)
    const timeline = () => container.querySelector('.agent-timeline')

    // One older page, put in front of what is held.
    await act(async () => {
      t.answerOlder({ events: range(51, 100), hasOlder: true, firstSeq: 51 })
    })
    const tr = useTranscriptStore.getState().sessions.s1
    expect(tr.events).toHaveLength(250)
    expect(tr.events[0].seq).toBe(51)
    expect(tr.events[249].seq).toBe(300)
    expect(tr.hasOlder).toBe(true)
    // Older events remain on the server, but none is being fetched: no request, no loading line,
    // and the scrollbar is drawn as usual.
    expect(t.older).toHaveLength(1)
    expect(t.pendingOlder).toBe(0)
    expect(screen.queryByTestId('older-loading')).toBeNull()
    expect(timeline()?.classList.contains('backfilling')).toBe(false)
  })

  it('"Loading earlier" and the hidden scrollbar follow the page in flight, not whether older events remain', async () => {
    const { t, container } = mount()
    deliverTail(t)
    const timeline = () => container.querySelector('.agent-timeline')
    expect(inFlight()).toBe(true)
    expect(timeline()?.classList.contains('backfilling')).toBe(true)
    await act(async () => {
      t.answerOlder({ events: range(51, 100), hasOlder: true, firstSeq: 51 })
    })
    expect(useTranscriptStore.getState().sessions.s1.hasOlder).toBe(true)
    expect(screen.queryByTestId('older-loading')).toBeNull()
    expect(timeline()?.classList.contains('backfilling')).toBe(false)
  })

  it('on a reconnect, asks again for the page the drop cut off', async () => {
    const { t } = mount()
    deliverTail(t)
    expect(t.older).toHaveLength(1)

    // The link drops while the older page was in flight: that request fails, and nothing is in
    // flight any more...
    await act(async () => {
      t.setConnected(false)
      t.failOlder()
    })
    expect(t.older).toHaveLength(1)
    expect(screen.queryByTestId('older-loading')).toBeNull()
    // ...and the link coming back asks for the same page again (the transport resubscribes
    // forward from the last seq by itself).
    act(() => t.setConnected(true))
    expect(t.older).toHaveLength(2)
    expect(t.older[1]).toEqual({ sessionId: SESSION, beforeSeq: 101, limit: 200 })
    expect(inFlight()).toBe(true)
    await deliverOlder(t)
    expect(screen.queryByTestId('older-loading')).toBeNull()
    expect(useTranscriptStore.getState().sessions.s1.events).toHaveLength(300)
  })

  it('on a later visit, picks the older pages up again where they were left', async () => {
    const t = createFakeTransport()
    const { unmount } = mount(t)
    deliverTail(t)
    await act(async () => { t.failOlder() })
    unmount()
    mount(t)
    expect(t.subscriptions[1]).toEqual({ sessionId: SESSION, afterSeq: 300, tail: false })
    expect(t.older).toHaveLength(2)
    expect(t.older[1]).toMatchObject({ beforeSeq: 101, limit: 200 })
    expect(inFlight()).toBe(true)
  })

  it('a transcript shorter than the tail has no older pages to fetch', () => {
    const { t } = mount()
    act(() => {
      for (const e of range(1, 5)) t.emit(e)
      t.replayDone({ lastSeq: 5, hasMore: false, older: true, hasOlder: false, firstSeq: 1 })
    })
    expect(screen.queryByTestId('history-loading')).toBeNull()
    expect(screen.queryByTestId('older-loading')).toBeNull()
    expect(t.older).toHaveLength(0)
  })

  it('draws an older page when the reader reaches the top, then asks for the next one exactly once', async () => {
    installIntersectionObserver()
    try {
      const { t } = mount()
      deliverTail(t)
      // The page fetched behind the tail lands in front, out of sight.
      await act(async () => {
        t.answerOlder({ events: range(51, 100), hasOlder: true, firstSeq: 51 })
      })
      expect(screen.queryByText('message 100')).toBeNull()
      expect(t.older).toHaveLength(1)

      // The reader reaches the top: the held page is drawn at once, without a request.
      act(() => intersect())
      expect(screen.getByText('message 51')).toBeInTheDocument()
      expect(t.older).toHaveLength(1)

      // At the top again with everything held drawn: one request, however often the top is seen.
      act(() => intersect())
      expect(t.older).toHaveLength(2)
      expect(t.older[1]).toEqual({ sessionId: SESSION, beforeSeq: 51, limit: 200 })
      expect(inFlight()).toBe(true)
      act(() => intersect())
      act(() => intersect())
      expect(t.older).toHaveLength(2)

      await act(async () => {
        t.answerOlder({ events: range(1, 50), hasOlder: false, firstSeq: 1 })
      })
      act(() => intersect())
      expect(screen.getByText('message 1')).toBeInTheDocument()
      expect(screen.queryByTestId('older-loading')).toBeNull()
      expect(t.older).toHaveLength(2)
    } finally {
      vi.unstubAllGlobals()
    }
  })

  it('after an older page fails, the top asks again once the reader scrolls away and back', async () => {
    installIntersectionObserver()
    try {
      const { t } = mount()
      deliverTail(t)
      await act(async () => {
        t.answerOlder({ events: range(51, 100), hasOlder: true, firstSeq: 51 })
      })
      act(() => intersect())
      act(() => intersect())
      expect(t.older).toHaveLength(2)
      // The page is refused (the link dropped and came back without a reconnect in between).
      await act(async () => { t.failOlder() })
      expect(screen.queryByTestId('older-loading')).toBeNull()
      act(() => intersect())
      expect(t.older).toHaveLength(2)
      // Away and back: exactly one more request.
      act(() => intersect(false))
      act(() => intersect())
      act(() => intersect())
      expect(t.older).toHaveLength(3)
      expect(t.older[2]).toEqual({ sessionId: SESSION, beforeSeq: 51, limit: 200 })
    } finally {
      vi.unstubAllGlobals()
    }
  })

  it('keeps a reader at the bottom when older events arrive', async () => {
    const { t, container } = mount()
    deliverTail(t)
    const timeline = container.querySelector('.agent-timeline') as HTMLElement
    Object.defineProperty(timeline, 'scrollHeight', {
      configurable: true,
      get: () => useTranscriptStore.getState().sessions.s1.events.length * 10,
    })
    Object.defineProperty(timeline, 'clientHeight', { configurable: true, value: 100 })
    // At the bottom: the view keeps following the end as the content grows.
    timeline.scrollTop = timeline.scrollHeight - timeline.clientHeight
    act(() => { timeline.dispatchEvent(new Event('scroll', { bubbles: true })) })
    act(() => t.emit(ev(301)))
    await deliverOlder(t)
    expect(timeline.scrollTop).toBe(timeline.scrollHeight - timeline.clientHeight)
  })
})
