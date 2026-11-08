import { act, render, waitFor } from '@testing-library/react'
import { expect, it } from 'vitest'
import { page } from 'vitest/browser'
import ChatView from '../../../../packages/chat/src/components/ChatView'
import { createFakeTransport } from '../../../../packages/chat/src/test/fakeTransport'
import { ev, ingest, meta, resetChatState, seed, SESSION } from '../../../../packages/chat/src/test/chat'
import '../index.css'

it.each([390, 3840])('follows a tall reply and post-mount growth through ChatView at %i px', async width => {
  await page.viewport(width, 800)
  resetChatState()
  seed(...Array.from({ length: 20 }, (_, i) => ev({ seq: i + 1, payload: { text: `Message ${i}: ${'Earlier text. '.repeat(30)}`, source: 'chat' } })))
  const host = document.createElement('div')
  host.style.cssText = 'position:fixed;inset:0;display:flex;flex-direction:column'
  document.body.append(host)
  render(<ChatView session={SESSION} transport={createFakeTransport().transport} meta={meta()} />, { container: host })
  const root = host.querySelector<HTMLElement>('.agent-timeline')!
  const gap = () => root.scrollHeight - root.clientHeight - root.scrollTop
  await waitFor(() => expect(gap()).toBeLessThanOrEqual(1))
  act(() => ingest(ev({ seq: 21, kind: 'assistant_text', payload: { text: Array.from({ length: 100 }, (_, i) => `Paragraph ${i}: ${'Long assistant output. '.repeat(15)}`).join('\n\n'), done: true } })))
  await waitFor(() => expect(gap()).toBeLessThanOrEqual(1))
  const reply = root.querySelector<HTMLElement>('.agent-card.assistant')!
  const oldHeight = root.scrollHeight
  // A late image/markdown renderer changes layout without another transport event.
  reply.style.height = '0px'
  reply.style.overflow = 'hidden'
  // Hydration replaces its placeholder in one task; the browser clamps during reflow.
  expect(root.scrollHeight).toBeLessThan(oldHeight)
  reply.style.minHeight = `${oldHeight + 2400}px`
  await waitFor(() => expect(root.scrollHeight).toBeGreaterThan(oldHeight + 2000))
  await waitFor(() => expect(gap()).toBeLessThanOrEqual(1))
  expect(host.querySelector('[data-testid="new-output"]')).toBeNull()
  reply.style.height = ''
  reply.style.minHeight = ''
  reply.style.overflow = ''
  act(() => ingest(ev({ seq: 22, kind: 'assistant_text', payload: { text: 'Still following after layout settles.', done: true } })))
  await waitFor(() => expect(gap()).toBeLessThanOrEqual(1))
  expect(host.querySelector('[data-testid="new-output"]')).toBeNull()
  await page.screenshot({ path: `chat-follow-${width}.png` })
})
