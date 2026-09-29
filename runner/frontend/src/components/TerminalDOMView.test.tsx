import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, fireEvent, waitFor } from '@testing-library/react'
import * as wsMock from '../test/wsMock'
import TerminalDOMView from './TerminalDOMView'
import type { SessionOutput } from '../types'

vi.mock('../ws', () => import('../test/wsMock'))

// ── Manual requestAnimationFrame control ──────────────────────────────────────
// TerminalDOMView batches DOM renders through rAF. We replace rAF with a manual
// queue so a test can hold a frame "pending" (the exact state the black-terminal
// regression depends on) and flush it deterministically. xterm's own parsing
// completes on a microtask, so we only stub rAF, not setTimeout.
let rafCallbacks: Map<number, FrameRequestCallback>
let nextRafId: number

beforeEach(() => {
  wsMock.resetWsMock()
  rafCallbacks = new Map()
  nextRafId = 1
  vi.stubGlobal('requestAnimationFrame', (cb: FrameRequestCallback) => {
    const id = nextRafId++
    rafCallbacks.set(id, cb)
    return id
  })
  vi.stubGlobal('cancelAnimationFrame', (id: number) => {
    rafCallbacks.delete(id)
  })
})

afterEach(() => {
  vi.unstubAllGlobals()
})

function pendingFrames(): number {
  return rafCallbacks.size
}

function flushRaf(): void {
  const cbs = [...rafCallbacks.values()]
  rafCallbacks.clear()
  cbs.forEach(cb => cb(performance.now()))
}

function output(sessionId: string, text: string): SessionOutput {
  return { type: 'session_output', session_id: sessionId, data: btoa(text), seq: 0 }
}

describe('TerminalDOMView', () => {
  it('subscribes to the session on mount (socket already open)', () => {
    render(<TerminalDOMView sessionId="sess-a" />)
    expect(wsMock.send).toHaveBeenCalledWith({ type: 'subscribe_session', session_id: 'sess-a' })
  })

  it('unsubscribes on unmount', () => {
    const { unmount } = render(<TerminalDOMView sessionId="sess-a" />)
    wsMock.send.mockClear()
    unmount()
    expect(wsMock.send).toHaveBeenCalledWith({ type: 'unsubscribe_session', session_id: 'sess-a' })
  })

  it('renders replayed history output into the terminal', async () => {
    const { container } = render(<TerminalDOMView sessionId="sess-a" />)
    wsMock.emit(output('sess-a', 'ZXCVHISTORY'))
    await waitFor(() => expect(pendingFrames()).toBeGreaterThan(0))
    flushRaf()
    expect(container.textContent).toContain('ZXCVHISTORY')
  })

  it('ignores output addressed to a different session', async () => {
    const { container } = render(<TerminalDOMView sessionId="sess-a" />)
    wsMock.emit(output('sess-OTHER', 'WRONGSESSION'))
    // No render should be scheduled for a filtered message.
    await Promise.resolve()
    expect(pendingFrames()).toBe(0)
    expect(container.textContent).not.toContain('WRONGSESSION')
  })

  it('forwards a keypress as a send_input message', () => {
    const { container } = render(<TerminalDOMView sessionId="sess-a" />)
    const textarea = container.querySelector('textarea')!
    wsMock.send.mockClear()
    fireEvent.keyDown(textarea, { key: 'Enter' })
    expect(wsMock.send).toHaveBeenCalledWith({
      type: 'send_input',
      session_id: 'sess-a',
      data: btoa('\r'),
    })
  })

  // Regression: switching sessions while a render frame is still pending used to
  // leave a stale (cancelled) frame id in the shared rafRef, so the next
  // session's scheduleRender thought a frame was already queued and never
  // painted — a black terminal until refresh. See TerminalDOMView cleanup.
  it('renders the new session after a switch with a render frame still pending', async () => {
    const { container, rerender } = render(<TerminalDOMView sessionId="sess-a" />)

    // Produce output for A and let xterm schedule a render — but do NOT flush it.
    wsMock.emit(output('sess-a', 'AAAAOLD'))
    await waitFor(() => expect(pendingFrames()).toBeGreaterThan(0))

    // Switch to session B with A's frame still pending. Cleanup cancels that
    // frame; the fix also clears rafRef so B's scheduler can arm again.
    rerender(<TerminalDOMView sessionId="sess-b" />)

    // B's history arrives and must schedule + render. Without the fix, no frame
    // is ever queued here and this waitFor times out.
    wsMock.emit(output('sess-b', 'BBBBNEW'))
    await waitFor(() => expect(pendingFrames()).toBeGreaterThan(0))
    flushRaf()

    expect(container.textContent).toContain('BBBBNEW')
    expect(container.textContent).not.toContain('AAAAOLD')
  })
})
