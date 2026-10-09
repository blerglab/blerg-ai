import { beforeEach, describe, expect, it, vi } from 'vitest'
import { act, render, screen } from '@testing-library/react'
import ChatView from './ChatView'
import { useTranscriptStore } from '../hooks/useTranscriptStore'
import { createFakeTransport, type FakeTransport } from '../test/fakeTransport'
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
    expect(screen.getByTestId('older-loading')).toBeTruthy()
    // the scrollbar thumb is not drawn while the transcript is still growing
    expect(container.querySelector('.agent-timeline')?.classList.contains('backfilling')).toBe(true)
    // ...and the next page is already being asked for, from just before the oldest one held.
    expect(t.older).toEqual([{ sessionId: SESSION, beforeSeq: 101, limit: 200 }])
    expect(screen.getByText('message 300')).toBeInTheDocument()
  })

  it('puts older pages in front, in order, keeps asking until the start, then drops the indicator', async () => {
    const { t } = mount()
    deliverTail(t)

    // Two older pages: the view asks again after the first says there is more.
    await act(async () => {
      t.answerOlder({ events: range(51, 100), hasOlder: true, firstSeq: 51 })
    })
    expect(t.older).toHaveLength(2)
    expect(t.older[1]).toEqual({ sessionId: SESSION, beforeSeq: 51, limit: 200 })
    expect(screen.getByTestId('older-loading')).toBeTruthy()
    await act(async () => {
      t.answerOlder({ events: range(1, 50), hasOlder: false, firstSeq: 1 })
    })

    const tr = useTranscriptStore.getState().sessions.s1
    expect(tr.events).toHaveLength(300)
    expect(tr.events[0].seq).toBe(1)
    expect(tr.events[299].seq).toBe(300)
    expect(tr.lastSeq).toBe(300)
    expect(screen.queryByTestId('older-loading')).toBeNull()
    expect(document.querySelector('.agent-timeline')?.classList.contains('backfilling')).toBe(false)
    // the chain stopped: two older pages, nothing after
    expect(t.older).toHaveLength(2)
    expect(t.pendingOlder).toBe(0)
    expect(screen.getByText('message 1')).toBeInTheDocument()
  })

  it('on a reconnect, resumes the older pages where the drop cut them off', async () => {
    const { t } = mount()
    deliverTail(t)
    expect(t.older).toHaveLength(1)

    // The link drops while the older page was in flight: that request fails...
    await act(async () => {
      t.setConnected(false)
      t.failOlder()
    })
    expect(t.older).toHaveLength(1)
    expect(screen.getByTestId('older-loading')).toBeTruthy()
    // ...and the link coming back asks for the same page again (the transport resubscribes
    // forward from the last seq by itself).
    act(() => t.setConnected(true))
    expect(t.older).toHaveLength(2)
    expect(t.older[1]).toEqual({ sessionId: SESSION, beforeSeq: 101, limit: 200 })
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
    expect(screen.getByTestId('older-loading')).toBeTruthy()
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

  it('R1: keeps the reader where they were when older events are put above them', async () => {
    const { t, container } = mount()
    deliverTail(t)
    const timeline = container.querySelector('.agent-timeline') as HTMLElement
    // jsdom has no layout: model a timeline whose height grows with its events (10px each),
    // scrolled up so the reader is not at the bottom.
    Object.defineProperty(timeline, 'scrollHeight', {
      configurable: true,
      get: () => useTranscriptStore.getState().sessions.s1.events.length * 10,
    })
    Object.defineProperty(timeline, 'clientHeight', { configurable: true, value: 100 })
    timeline.scrollTop = 500
    act(() => { timeline.dispatchEvent(new Event('scroll', { bubbles: true })) })
    // re-render so the view records the height it now has
    act(() => t.emit(ev(301)))
    timeline.scrollTop = 500

    await deliverOlder(t)
    // 100 events of 10px each were added above: the reader's view moves down by exactly that.
    expect(timeline.scrollTop).toBe(1500)
  })

  it('does not move a reader who is at the bottom when older events arrive', async () => {
    const { t, container } = mount()
    deliverTail(t)
    const timeline = container.querySelector('.agent-timeline') as HTMLElement
    Object.defineProperty(timeline, 'scrollHeight', {
      configurable: true,
      get: () => useTranscriptStore.getState().sessions.s1.events.length * 10,
    })
    Object.defineProperty(timeline, 'clientHeight', { configurable: true, value: 100 })
    // At the bottom (within 80px of it): the view keeps following the end.
    timeline.scrollTop = 1950
    act(() => { timeline.dispatchEvent(new Event('scroll', { bubbles: true })) })
    act(() => t.emit(ev(301)))
    timeline.scrollTop = 1950
    await deliverOlder(t)
    expect(timeline.scrollTop).toBe(1950)
  })
})
