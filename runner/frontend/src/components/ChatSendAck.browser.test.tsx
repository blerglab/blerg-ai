// BLG-455, real browser proof: a steered (mid-turn) message the server has acknowledged
// (input_accepted) never trips the "No response from the workspace" warning, however long the
// step runs — it stays a queued bubble ("the agent picks it up at its next step"), truthful and
// not alarming. A message truly never acknowledged (a dropped socket) still warns. Mounted the
// real ChatView over the fake transport, as NoticePlacement.browser.test.tsx does.
import { browserWidths } from '../test/browserWidths'
import { act, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import { page } from 'vitest/browser'
import { ChatView } from '@blerglab/chat'
import { createFakeTransport } from '../../../../packages/chat/src/test/fakeTransport'
import { ev, meta, resetChatState, seed, SESSION } from '../../../../packages/chat/src/test/chat'
import '@blerglab/chat/blerg.css'
import '../index.css'

const shotDir = (import.meta.env as Record<string, string | undefined>).VITE_SEND_ACK_SHOTS_DIR

function type(text: string) {
  const box = screen.getByRole('textbox') as HTMLTextAreaElement
  fireEvent.change(box, { target: { value: text } })
  return box
}
async function enter(box: HTMLElement) {
  await act(async () => { fireEvent.keyDown(box, { key: 'Enter' }) })
}

afterEach(() => { vi.useRealTimers() })

for (const width of browserWidths([390, 1024, 3840])) {
  it(`shows a delivered mid-turn message as queued, never a false warning, at ${width}px`, async () => {
    await page.viewport(width, 900)
    resetChatState()
    seed(ev({ seq: 1, payload: { text: 'start', source: 'chat' } }))
    const fake = createFakeTransport()
    render(<ChatView session={SESSION} transport={fake.transport} meta={meta({ status: 'idle' })} />)
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    try {
      const box = type('a long tool call, several minutes')
      await enter(box)
      const id = fake.sentWith[0].opts!.clientId!
      // The server confirms receipt right away; the step itself (a long tool call) is what takes
      // "several minutes" — nothing else is heard from the workspace in that time.
      act(() => fake.emit(ev({ seq: 6, kind: 'input_accepted', payload: { client_id: id, text: 'a long tool call, several minutes' } })))
      act(() => { vi.advanceTimersByTime(20_000) })
      expect(screen.queryByTestId('send-notice')).toBeNull()
      expect(screen.getByTestId('queued-message').textContent).not.toMatch(/may not have been delivered/i)
      if (shotDir) { await new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r))); await page.screenshot({ path: `${shotDir}/delivered-queued-${width}.png` }) }
    } finally {
      vi.useRealTimers()
    }
  })

  it(`warns when a message is never acknowledged at all, at ${width}px`, async () => {
    await page.viewport(width, 900)
    resetChatState()
    seed(ev({ seq: 1, payload: { text: 'start', source: 'chat' } }))
    const fake = createFakeTransport()
    render(<ChatView session={SESSION} transport={fake.transport} meta={meta({ status: 'idle' })} />)
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    try {
      const box = type('gone for good')
      await enter(box)
      // No input_accepted ever arrives: the socket dropped before the server could acknowledge it.
      act(() => { vi.advanceTimersByTime(20_000) })
      expect(screen.getByTestId('send-notice')).toBeInTheDocument()
      expect(screen.getByTestId('queued-message').textContent).toMatch(/may not have been delivered/i)
      if (shotDir) { await new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r))); await page.screenshot({ path: `${shotDir}/lost-warning-${width}.png` }) }
    } finally {
      vi.useRealTimers()
    }
  })
}
