import { act, render, waitFor } from '@testing-library/react'
import { commands, page } from 'vitest/browser'
import { expect, it } from 'vitest'
import { ChatView } from '@blerglab/chat'
import { createFakeTransport } from '../../../../packages/chat/src/test/fakeTransport'
import { ev, resetChatState, seed, SESSION } from '../../../../packages/chat/src/test/chat'
import '@blerglab/chat/blerg.css'
import '../index.css'

// This file runs only on the isolated touch instance: touchScroll uses
// Emulation.setTouchEmulationEnabled, which changes Chromium's hover capability.
it('keeps a native touch reader still during streaming', async () => {
  await page.viewport(390, 900)
  resetChatState()
  seed(...Array.from({ length: 40 }, (_, i) => ev({ seq: i + 1, payload: { text: `Message ${i}: ${'A steady conversation. '.repeat(40)}` } })))
  const fake = createFakeTransport({ meta: { engine: 'codex', status: 'running' } })
  const view = render(<div style={{ height: 850 }}><ChatView session={SESSION} transport={fake.transport} /></div>)
  act(() => fake.replayDone({ lastSeq: 40 }))
  const root = view.container.querySelector<HTMLElement>('.agent-timeline')!
  const gap = () => root.scrollHeight - root.clientHeight - root.scrollTop
  await waitFor(() => expect(gap()).toBeLessThanOrEqual(1))
  const stream = () => act(() => fake.emit(ev({ kind: 'assistant_text', transient: true, payload: { text: ' token\n', done: false } })))
  stream()
  await commands.transcriptInput('touchScroll')
  await waitFor(() => expect(gap()).toBeGreaterThan(30))
  const row = Array.from(root.querySelectorAll<HTMLElement>('[data-event-id]')).find(el => el.getBoundingClientRect().top >= root.getBoundingClientRect().top)!
  const before = row.getBoundingClientRect().top
  for (let i = 0; i < 10; i++) {
    stream()
    for (let frame = 0; frame < 3; frame++) await new Promise(requestAnimationFrame)
    expect(row.getBoundingClientRect().top).toBeCloseTo(before, 0)
  }
})
