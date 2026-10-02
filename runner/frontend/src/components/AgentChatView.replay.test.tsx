import { beforeEach, describe, expect, it, vi } from 'vitest'
import { act, render, screen } from '@testing-library/react'
import AgentChatView from './AgentChatView'
import { useAgentTranscript } from '../hooks/useAgentTranscript'
import type { AgentEvent, SessionInfo } from '../types'

// A ws mock that keeps the handlers the view registers, so a test can play the
// server's part: connect, deliver a replay page, mark it done.
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

const subscribes = () =>
  vi.mocked(send).mock.calls
    .map(c => c[0] as { type: string; after_seq?: number })
    .filter(m => m.type === 'subscribe_agent_events')

describe('AgentChatView transcript replay', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    handlers.message = {}
    handlers.open = []
    useAgentTranscript.setState({ sessions: {} })
    vi.stubGlobal('fetch', vi.fn(() => Promise.reject(new Error('offline'))))
  })

  it('keeps asking for the next page while the server says there is more', () => {
    render(<AgentChatView session={session} />)
    act(() => { handlers.open.forEach(cb => cb()) })
    expect(subscribes().map(m => m.after_seq)).toEqual([0])

    // The server sends a page of 200 and says there is more.
    act(() => {
      for (let i = 1; i <= 200; i++) handlers.message.agent_event(ev(i))
      handlers.message.agent_events_replay_done({ session_id: 's1', last_seq: 200, has_more: true })
    })
    expect(subscribes().map(m => m.after_seq)).toEqual([0, 200])

    // The second page ends the transcript: no further request.
    act(() => {
      for (let i = 201; i <= 304; i++) handlers.message.agent_event(ev(i))
      handlers.message.agent_events_replay_done({ session_id: 's1', last_seq: 304, has_more: false })
    })
    expect(subscribes().map(m => m.after_seq)).toEqual([0, 200])
    expect(useAgentTranscript.getState().sessions.s1.events).toHaveLength(304)
  })

  it('ignores a replay-done for another session', () => {
    render(<AgentChatView session={session} />)
    act(() => { handlers.open.forEach(cb => cb()) })
    act(() => {
      handlers.message.agent_events_replay_done({ session_id: 'other', last_seq: 200, has_more: true })
    })
    expect(subscribes().map(m => m.after_seq)).toEqual([0])
  })

  it('shows a skeleton from the start, keeps the timeline out of sight while pages arrive, then shows it scrolled to the bottom', () => {
    const scroll = vi.fn()
    Element.prototype.scrollIntoView = scroll
    const { container } = render(<AgentChatView session={session} />)
    const timeline = () => container.querySelector('.agent-timeline')

    // Nothing has arrived yet: a skeleton, not a blank area.
    expect(screen.getByTestId('history-loading')).toBeTruthy()
    expect(screen.getByRole('status', { name: 'Loading conversation' })).toBeTruthy()
    expect(container.querySelectorAll('.sk-bubble').length).toBeGreaterThan(3)
    expect(timeline()?.classList.contains('loading')).toBe(true)

    act(() => { handlers.open.forEach(cb => cb()) })
    // First page arrives, more to come: still the skeleton, no scrolling.
    act(() => {
      for (let i = 1; i <= 200; i++) handlers.message.agent_event(ev(i))
      handlers.message.agent_events_replay_done({ session_id: 's1', last_seq: 200, has_more: true })
    })
    expect(screen.getByTestId('history-loading')).toBeTruthy()
    expect(timeline()?.classList.contains('loading')).toBe(true)
    expect(scroll).not.toHaveBeenCalled()

    // Last page: the real transcript replaces the skeleton, scrolled to the bottom.
    act(() => {
      for (let i = 201; i <= 304; i++) handlers.message.agent_event(ev(i))
      handlers.message.agent_events_replay_done({ session_id: 's1', last_seq: 304, has_more: false })
    })
    expect(screen.queryByTestId('history-loading')).toBeNull()
    expect(timeline()?.classList.contains('loading')).toBe(false)
    expect(scroll).toHaveBeenCalled()
  })

  it('removes the skeleton for a session that has no history at all', () => {
    render(<AgentChatView session={session} />)
    expect(screen.getByTestId('history-loading')).toBeTruthy()
    act(() => { handlers.open.forEach(cb => cb()) })
    act(() => { handlers.message.agent_events_replay_done({ session_id: 's1', last_seq: 0, has_more: false }) })
    expect(screen.queryByTestId('history-loading')).toBeNull()
  })
})
