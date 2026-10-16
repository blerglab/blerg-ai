import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, render, screen } from '@testing-library/react'
import ChatView from './ChatView'
import { useTranscriptStore } from '../hooks/useTranscriptStore'
import { createFakeTransport } from '../test/fakeTransport'
import { SESSION, meta, resetChatState, seed } from '../test/chat'
import type { AgentEvent } from '../types'

// Opening a session whose long transcript is already held (a revisit) paints the switch first —
// the skeleton — and draws the transcript on the next frame, in an ordinary update that store
// updates cannot hold up.

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

// Animation frames run when the test says so.
let frames: Array<FrameRequestCallback | null> = []
function nextFrame() {
  const due = frames
  frames = []
  due.forEach(cb => cb?.(0))
}

describe('ChatView paints the switch before a long held transcript', () => {
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

  it('a revisit to a long transcript commits the skeleton first, and the window on the next frame', () => {
    seed(...range(1, 1000))
    const t = createFakeTransport()
    const { container } = render(<ChatView session={SESSION} transport={t.transport} meta={meta()} />)
    expect(screen.getByTestId('history-loading')).toBeInTheDocument()
    expect(container.querySelectorAll('.agent-card.user')).toHaveLength(0)

    act(() => nextFrame())
    expect(screen.queryByTestId('history-loading')).toBeNull()
    expect(screen.getByText('message 1000')).toBeInTheDocument()
    expect(container.querySelectorAll('.agent-card.user')).toHaveLength(300)
  })

  it('a store update between the two paints does not hold the transcript up', () => {
    seed(...range(1, 1000))
    const t = createFakeTransport()
    const { container } = render(<ChatView session={SESSION} transport={t.transport} meta={meta()} />)
    // A live event lands before the frame: still the skeleton...
    act(() => { useTranscriptStore.getState().ingest(ev(1001)) })
    expect(screen.getByTestId('history-loading')).toBeInTheDocument()
    // ...and the next frame draws the window, the new event included.
    act(() => nextFrame())
    expect(screen.queryByTestId('history-loading')).toBeNull()
    expect(screen.getByText('message 1001')).toBeInTheDocument()
    expect(container.querySelectorAll('.agent-card.user')).toHaveLength(300)
  })

  it('a short transcript is drawn at once, with no skeleton and no frame to wait for', () => {
    seed(...range(1, 40))
    const t = createFakeTransport()
    render(<ChatView session={SESSION} transport={t.transport} meta={meta()} />)
    expect(screen.queryByTestId('history-loading')).toBeNull()
    expect(screen.getByText('message 40')).toBeInTheDocument()
    expect(frames).toHaveLength(0)
  })

  it('leaving before the frame cancels it', () => {
    seed(...range(1, 1000))
    const t = createFakeTransport()
    const { unmount } = render(<ChatView session={SESSION} transport={t.transport} meta={meta()} />)
    expect(frames.filter(Boolean)).toHaveLength(1)
    unmount()
    expect(frames.filter(Boolean)).toHaveLength(0)
  })
})
