// The working line's once-a-second clock re-renders only the line: ChatView and the composer are
// not drawn again by a tick (card #238).
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, render, screen } from '@testing-library/react'
import ChatView from './ChatView'
import { createFakeTransport } from '../test/fakeTransport'
import { SESSION, ev as mk, ingest, meta, resetChatState, seed } from '../test/chat'
import type { AgentEvent } from '../types'

const counts = vi.hoisted(() => ({ chatView: 0, composer: 0 }))

// ChatView creates the transcript element on every render; an unmemoized wrapper around it is
// called once per ChatView render, so counting its calls counts ChatView's renders.
vi.mock('./Transcript', async importOriginal => {
  const real = await importOriginal<typeof import('./Transcript')>()
  const Real = real.default
  return { ...real, default: (p: Parameters<typeof Real>[0]) => { counts.chatView++; return <Real {...p} /> } }
})
vi.mock('./Composer', async importOriginal => {
  const real = await importOriginal<typeof import('./Composer')>()
  const Real = real.default
  return { ...real, default: (p: Parameters<typeof Real>[0]) => { counts.composer++; return <Real {...p} /> } }
})

const S = 1000
// Mid-minute, so the 3 s below cross no minute boundary (ChatView's own clock is to the minute).
const NOW = new Date(2026, 8, 29, 12, 0, 20).getTime()

let seq = 0
function ev(kind: string, payload: unknown, at: number, transient = false): AgentEvent {
  seq++
  return mk({ client_event_id: `e${seq}`, seq, ts: new Date(at).toISOString(), kind, payload, transient })
}

const tick = (ms: number) => act(() => { vi.advanceTimersByTime(ms) })
const time = (id: string) => screen.getByTestId(id).querySelector('.agent-working-time')!.textContent

describe('ChatView clock ticks', () => {
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval', 'setTimeout', 'clearTimeout', 'Date'] })
    vi.setSystemTime(NOW)
    resetChatState()
    seq = 0
  })
  afterEach(() => { vi.useRealTimers() })

  for (const streaming of [false, true]) {
    it(`a running session's ticks re-render neither ChatView nor the composer (streaming=${streaming})`, () => {
      const turn = [
        ev('user_message', { text: 'go', source: 'chat' }, NOW - 42 * S),
        ev('status_changed', { status: 'running', reason: 'turn' }, NOW - 42 * S),
      ]
      if (streaming) ingest(ev('assistant_text', { text: 'part', done: false }, NOW, true))
      seed(...turn)
      const t = createFakeTransport()
      render(<ChatView session={SESSION} transport={t.transport} meta={meta({ status: 'running' })} />)
      const id = streaming ? 'working-compact' : 'working'
      expect(time(id)).toBe('0:42')
      expect(counts.composer).toBeGreaterThan(0)
      const before = { ...counts }
      for (let i = 1; i <= 3; i++) {
        tick(S)
        expect(time(id)).toBe(`0:${42 + i}`)
      }
      expect(counts.chatView - before.chatView).toBe(0)
      expect(counts.composer - before.composer).toBe(0)
    })
  }
})
