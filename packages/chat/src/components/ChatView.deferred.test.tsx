import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, render, screen } from '@testing-library/react'
import ChatView from './ChatView'
import { useTranscriptStore } from '../hooks/useTranscriptStore'
import { createFakeTransport } from '../test/fakeTransport'
import { SESSION, meta, resetChatState, seed } from '../test/chat'
import type { AgentEvent } from '../types'

// Opening a session whose transcript is already held (a revisit) draws it in the first paint, long
// or short: no skeleton, no frame to wait for (#281; chat 0.5.0 to 1.30 painted a skeleton first).

function ev(seq: number): AgentEvent {
  return {
    type: 'agent_event',
    session_id: SESSION,
    client_event_id: `e${seq}`,
    seq,
    ts: '2026-10-13T00:00:00Z',
    kind: 'user_message',
    payload: { text: `message ${seq}`, source: 'chat' },
  }
}
const range = (from: number, to: number) => Array.from({ length: to - from + 1 }, (_, i) => ev(from + i))

// Animation frames never run unless a test runs them.
let frames: Array<FrameRequestCallback | null> = []

describe('ChatView draws a held transcript in the first paint', () => {
  beforeEach(() => {
    resetChatState()
    Element.prototype.scrollIntoView = vi.fn()
    frames = []
    vi.stubGlobal('requestAnimationFrame', (cb: FrameRequestCallback) => frames.push(cb))
    vi.stubGlobal('cancelAnimationFrame', (id: number) => { frames[id - 1] = null })
  })
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('a revisit to a long transcript draws its newest window at once, with no skeleton', () => {
    seed(...range(1, 1000))
    const t = createFakeTransport()
    const { container } = render(<ChatView session={SESSION} transport={t.transport} meta={meta()} />)
    expect(screen.queryByTestId('history-loading')).toBeNull()
    expect(screen.getByText('message 1000')).toBeInTheDocument()
    expect(container.querySelectorAll('.agent-card.user')).toHaveLength(300)
  })

  it('a live event after the first paint is added to what is drawn', () => {
    seed(...range(1, 1000))
    const t = createFakeTransport()
    const { container } = render(<ChatView session={SESSION} transport={t.transport} meta={meta()} />)
    act(() => { useTranscriptStore.getState().ingest(ev(1001)) })
    expect(screen.queryByTestId('history-loading')).toBeNull()
    expect(screen.getByText('message 1001')).toBeInTheDocument()
    expect(screen.getAllByText('message 1000')).toHaveLength(1)
    expect(container.querySelectorAll('.agent-card.user').length).toBeGreaterThanOrEqual(300)
  })

  it('a short transcript is drawn at once too', () => {
    seed(...range(1, 40))
    const t = createFakeTransport()
    render(<ChatView session={SESSION} transport={t.transport} meta={meta()} />)
    expect(screen.queryByTestId('history-loading')).toBeNull()
    expect(screen.getByText('message 40')).toBeInTheDocument()
  })
})
