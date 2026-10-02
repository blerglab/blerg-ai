import { beforeEach, describe, expect, it, vi } from 'vitest'
import { act, render, screen } from '@testing-library/react'
import AgentChatView from './AgentChatView'
import { useAgentTranscript } from '../hooks/useAgentTranscript'
import type { AgentEvent, SessionInfo } from '../types'

// Same idea as the replay test: a ws mock that lets the test play the server.
const handlers = vi.hoisted(() => ({
  message: {} as Record<string, (msg: unknown) => void>,
  open: [] as Array<() => void>,
}))

vi.mock('../ws', () => ({
  send: vi.fn(),
  onMessage: vi.fn((type: string, cb: (msg: unknown) => void) => {
    handlers.message[type] = cb
    return () => { delete handlers.message[type] }
  }),
  onOpen: vi.fn((cb: () => void) => {
    handlers.open.push(cb)
    return () => { handlers.open.splice(handlers.open.indexOf(cb), 1) }
  }),
}))

import { send } from '../ws'

const session: SessionInfo = {
  id: 's1',
  daemon_id: 'd1',
  status: 'idle',
  project_path: '/r/proj',
  repo: 'proj',
  title: 'Test',
  model: 'claude-sonnet-5',
  started_at: '2026-09-30T00:00:00Z',
  kind: 'agent',
}

function ev(seq: number): AgentEvent {
  return {
    type: 'agent_event',
    session_id: 's1',
    client_event_id: `e${seq}`,
    seq,
    ts: '2026-09-30T00:00:00Z',
    kind: 'user_message',
    payload: { text: `message ${seq}`, source: 'chat' },
  }
}

type Sub = { type: string; after_seq?: number; tail?: number; before_seq?: number; limit?: number }
const subscribes = () =>
  vi.mocked(send).mock.calls.map(c => c[0] as Sub).filter(m => m.type === 'subscribe_agent_events')

// The server's answer to the first request: the newest 200 events of a 300-event transcript.
function deliverTail() {
  act(() => {
    for (let i = 101; i <= 300; i++) handlers.message.agent_event(ev(i))
    handlers.message.agent_events_replay_done({
      session_id: 's1', last_seq: 300, has_more: false, older: true, has_older: true, first_seq: 101,
    })
  })
}

describe('AgentChatView loads a long transcript from its end', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    handlers.message = {}
    handlers.open = []
    useAgentTranscript.setState({ sessions: {} })
    vi.stubGlobal('fetch', vi.fn(() => Promise.reject(new Error('offline'))))
    Element.prototype.scrollIntoView = vi.fn()
  })

  it('asks for the newest events first', () => {
    render(<AgentChatView session={session} />)
    act(() => { handlers.open.forEach(cb => cb()) })
    expect(subscribes()).toEqual([{ type: 'subscribe_agent_events', session_id: 's1', after_seq: 0, tail: 200 }])
  })

  it('shows the conversation as soon as the newest events are in, and fetches the older ones behind it', () => {
    const { container } = render(<AgentChatView session={session} />)
    act(() => { handlers.open.forEach(cb => cb()) })
    expect(screen.getByTestId('history-loading')).toBeTruthy()

    deliverTail()

    // No skeleton and a visible timeline, while older events remain to come.
    expect(screen.queryByTestId('history-loading')).toBeNull()
    expect(container.querySelector('.agent-timeline')?.classList.contains('loading')).toBe(false)
    expect(screen.getByTestId('older-loading')).toBeTruthy()
    // the scrollbar thumb is not drawn while the transcript is still growing
    expect(container.querySelector('.agent-timeline')?.classList.contains('backfilling')).toBe(true)
    // ...and the next page is already being asked for, from just before the oldest one held.
    expect(subscribes()[1]).toMatchObject({ before_seq: 101, limit: 200 })
  })

  it('puts older pages in front, in order, keeps asking until the start, then drops the indicator', () => {
    render(<AgentChatView session={session} />)
    act(() => { handlers.open.forEach(cb => cb()) })
    deliverTail()

    act(() => {
      for (let i = 1; i <= 100; i++) handlers.message.agent_event(ev(i))
      handlers.message.agent_events_replay_done({
        session_id: 's1', last_seq: 0, has_more: false, older: true, has_older: false, first_seq: 1,
      })
    })

    const t = useAgentTranscript.getState().sessions.s1
    expect(t.events).toHaveLength(300)
    expect(t.events[0].seq).toBe(1)
    expect(t.events[299].seq).toBe(300)
    expect(t.lastSeq).toBe(300)
    expect(screen.queryByTestId('older-loading')).toBeNull()
    expect(document.querySelector('.agent-timeline')?.classList.contains('backfilling')).toBe(false)
    // the chain stopped: tail, the one older page, nothing after
    expect(subscribes()).toHaveLength(2)
  })

  it('on a reconnect, catches up forward from what it has and resumes the older pages', () => {
    render(<AgentChatView session={session} />)
    act(() => { handlers.open.forEach(cb => cb()) })
    deliverTail()
    vi.mocked(send).mockClear()

    // The socket drops and comes back while older pages were still outstanding.
    act(() => { handlers.open.forEach(cb => cb()) })
    const subs = subscribes()
    expect(subs).toContainEqual({ type: 'subscribe_agent_events', session_id: 's1', after_seq: 300 })
    expect(subs).toContainEqual({ type: 'subscribe_agent_events', session_id: 's1', before_seq: 101, limit: 200 })
    expect(subs.some(m => m.tail)).toBe(false)
  })

  it('a transcript shorter than the tail has no older pages to fetch', () => {
    render(<AgentChatView session={session} />)
    act(() => { handlers.open.forEach(cb => cb()) })
    act(() => {
      for (let i = 1; i <= 5; i++) handlers.message.agent_event(ev(i))
      handlers.message.agent_events_replay_done({
        session_id: 's1', last_seq: 5, has_more: false, older: true, has_older: false, first_seq: 1,
      })
    })
    expect(screen.queryByTestId('history-loading')).toBeNull()
    expect(screen.queryByTestId('older-loading')).toBeNull()
    expect(subscribes()).toHaveLength(1)
  })

  it('keeps the reader where they were when older events are put above them', () => {
    const { container } = render(<AgentChatView session={session} />)
    act(() => { handlers.open.forEach(cb => cb()) })
    deliverTail()
    const timeline = container.querySelector('.agent-timeline') as HTMLElement
    // jsdom has no layout: model a timeline whose height grows with its events (10px each),
    // scrolled up so the reader is not at the bottom.
    Object.defineProperty(timeline, 'scrollHeight', {
      configurable: true,
      get: () => useAgentTranscript.getState().sessions.s1.events.length * 10,
    })
    Object.defineProperty(timeline, 'clientHeight', { configurable: true, value: 100 })
    timeline.scrollTop = 500
    act(() => { timeline.dispatchEvent(new Event('scroll', { bubbles: true })) })
    // re-render so the view records the height it now has
    act(() => { handlers.message.agent_event(ev(301)) })
    timeline.scrollTop = 500

    act(() => {
      for (let i = 1; i <= 100; i++) handlers.message.agent_event(ev(i))
      handlers.message.agent_events_replay_done({
        session_id: 's1', last_seq: 0, has_more: false, older: true, has_older: false, first_seq: 1,
      })
    })
    // 100 events of 10px each were added above: the reader's view moves down by exactly that.
    expect(timeline.scrollTop).toBe(1500)
  })
})
