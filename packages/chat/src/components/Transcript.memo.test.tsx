import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen } from '@testing-library/react'
import { useState } from 'react'
import Transcript, { type TranscriptProps } from './Transcript'
import ChatView from './ChatView'
import { createFakeTransport } from '../test/fakeTransport'
import { SESSION, ev, meta, resetChatState, seed } from '../test/chat'

// The timeline is the expensive part of the chat: a keystroke in the composer or the working
// clock's tick re-renders ChatView, and must not re-render the timeline with it.

// Counts the renders of the working line, which the timeline draws afresh each time it renders.
const working = vi.hoisted(() => ({ renders: 0 }))
vi.mock('./WorkingIndicator', () => ({
  default: (p: { compact?: boolean }) => {
    if (!p.compact) working.renders++
    return <div data-testid={p.compact ? 'working-compact' : 'working'} />
  },
}))

const NONE = new Set<string>()
const NO_QUEUE: TranscriptProps['queued'] = []
const READY = <div />

describe('Transcript is memoised', () => {
  beforeEach(() => {
    resetChatState()
    working.renders = 0
  })

  it('does not re-render when its parent re-renders with the same props', () => {
    const events = [ev({ seq: 1, payload: { text: 'hello', source: 'chat' } })]
    let bump: () => void = () => {}
    function Parent() {
      const [n, setN] = useState(0)
      bump = () => setN(n + 1)
      return (
        <>
          <span data-testid="n">{n}</span>
          <Transcript
            sessionId={SESSION}
            events={events}
            streaming=""
            compact
            liveIds={NONE}
            showSkeleton={false}
            loadingOlder={false}
            starting={false}
            attempt={null}
            clockOffset={0}
            readyCard={READY}
            working
            waiting={false}
            since={null}
            waitingOn={null}
            queued={NO_QUEUE}
            queueState="sending"
            steers
            pin={0}
          />
        </>
      )
    }
    render(<Parent />)
    expect(screen.getByTestId('working')).toBeInTheDocument()
    const before = working.renders
    act(() => bump())
    act(() => bump())
    expect(screen.getByTestId('n')).toHaveTextContent('2')
    expect(working.renders).toBe(before)
  })
})

describe('ChatView hands the timeline the same props when nothing in it changed', () => {
  beforeEach(() => {
    resetChatState()
    working.renders = 0
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval', 'Date'] })
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('a composer keystroke and the 1-second tick of a running turn leave the timeline alone', () => {
    seed(ev({ seq: 1, payload: { text: 'hello', source: 'chat' } }))
    const t = createFakeTransport()
    render(<ChatView session={SESSION} transport={t.transport} meta={meta({ status: 'running' })} />)
    expect(screen.getByTestId('working')).toBeInTheDocument()
    const before = working.renders

    fireEvent.change(screen.getByRole('textbox'), { target: { value: 'a' } })
    fireEvent.change(screen.getByRole('textbox'), { target: { value: 'ab' } })
    expect(working.renders).toBe(before)

    // ChatView's clock ticks (it re-renders); the timeline's working line ticks on its own clock,
    // once a second, and nothing else in the timeline re-renders.
    act(() => { vi.advanceTimersByTime(1000) })
    expect(working.renders).toBe(before + 1)
  })
})
