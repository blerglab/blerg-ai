import { beforeEach, describe, expect, it, vi } from 'vitest'
import { act, render, screen } from '@testing-library/react'
import ChatView from './ChatView'
import { useTranscriptStore } from '../hooks/useTranscriptStore'
import { createFakeTransport, type FakeTransport } from '../test/fakeTransport'
import { SESSION, meta, resetChatState } from '../test/chat'
import type { AgentEvent } from '../types'

// The transport pages a forward replay by itself (has_more → the next subscribe; see
// transport/blerg.test.ts). What the view owes is the skeleton: up until the replay is complete,
// gone the moment it is — also for a session that has no history at all.

const session = meta({ status: 'idle', started_at: '2026-09-30T00:00:00Z' })

function ev(seq: number, sessionId = SESSION): AgentEvent {
  return {
    type: 'agent_event',
    session_id: sessionId,
    client_event_id: `${sessionId}-e${seq}`,
    seq,
    ts: '2026-09-30T00:00:00Z',
    kind: 'user_message',
    payload: { text: `message ${seq}`, source: 'chat' },
  }
}

function mount(t: FakeTransport = createFakeTransport()) {
  return { t, ...render(<ChatView session={SESSION} transport={t.transport} meta={session} />) }
}

describe('ChatView transcript replay', () => {
  beforeEach(() => {
    resetChatState()
  })

  it('subscribes from seq 0 asking for the tail on a first visit', () => {
    const { t } = mount()
    expect(t.subscriptions).toEqual([{ sessionId: SESSION, afterSeq: 0, tail: true }])
  })

  it('shows a skeleton from the start, keeps the timeline out of sight while pages arrive, then shows it scrolled to the bottom', () => {
    const scroll = vi.fn()
    Element.prototype.scrollIntoView = scroll
    const { t, container } = mount()
    const timeline = () => container.querySelector('.agent-timeline')

    // Nothing has arrived yet: a skeleton, not a blank area.
    expect(screen.getByTestId('history-loading')).toBeTruthy()
    expect(screen.getByRole('status', { name: 'Loading conversation' })).toBeTruthy()
    expect(container.querySelectorAll('.sk-bubble').length).toBeGreaterThan(3)
    expect(timeline()?.classList.contains('loading')).toBe(true)

    // First page arrives, more to come: still the skeleton, no scrolling.
    act(() => {
      for (let i = 1; i <= 200; i++) t.emit(ev(i))
      t.replayDone({ lastSeq: 200, hasMore: true })
    })
    expect(screen.getByTestId('history-loading')).toBeTruthy()
    expect(timeline()?.classList.contains('loading')).toBe(true)
    expect(scroll).not.toHaveBeenCalled()

    // Last page: the real transcript replaces the skeleton, scrolled to the bottom.
    act(() => {
      for (let i = 201; i <= 304; i++) t.emit(ev(i))
      t.replayDone({ lastSeq: 304, hasMore: false })
    })
    expect(screen.queryByTestId('history-loading')).toBeNull()
    expect(timeline()?.classList.contains('loading')).toBe(false)
    expect(scroll).toHaveBeenCalled()
    expect(useTranscriptStore.getState().sessions.s1.events).toHaveLength(304)
    expect(screen.getByText('message 304')).toBeInTheDocument()
  })

  it('removes the skeleton for a session that has no history at all', () => {
    const { t } = mount()
    expect(screen.getByTestId('history-loading')).toBeTruthy()
    act(() => t.replayDone({ lastSeq: 0, hasMore: false }))
    expect(screen.queryByTestId('history-loading')).toBeNull()
  })

  it('keeps events of another session out of this transcript', () => {
    const { t } = mount()
    act(() => {
      t.emit(ev(1))
      t.emit(ev(2, 'other'))
      t.replayDone({ lastSeq: 1, hasMore: false })
    })
    expect(useTranscriptStore.getState().sessions.s1.events).toHaveLength(1)
    expect(useTranscriptStore.getState().sessions.other).toBeUndefined()
    expect(screen.queryByText('message 2')).toBeNull()
  })

  it('drops a resent event the transcript already holds', () => {
    const { t } = mount()
    act(() => {
      t.emit(ev(1))
      t.emit(ev(1))
      t.replayDone({ lastSeq: 1, hasMore: false })
    })
    expect(screen.getAllByText('message 1')).toHaveLength(1)
  })

  it('on a later visit resumes from the last seq held, without the skeleton or a tail request', () => {
    const t = createFakeTransport()
    const { unmount } = mount(t)
    act(() => {
      for (let i = 1; i <= 5; i++) t.emit(ev(i))
      t.replayDone({ lastSeq: 5, hasMore: false })
    })
    unmount()
    mount(t)
    expect(t.subscriptions[1]).toEqual({ sessionId: SESSION, afterSeq: 5, tail: false })
    expect(screen.queryByTestId('history-loading')).toBeNull()
    expect(screen.getByText('message 5')).toBeInTheDocument()
  })
})
