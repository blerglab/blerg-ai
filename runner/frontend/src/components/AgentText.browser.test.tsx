import { beforeEach, expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'
import { page } from 'vitest/browser'
import { ChatView } from '@blerglab/chat'
import '@blerglab/chat/blerg.css'
import { ev, meta, resetChatState, seed, SESSION } from '../../../../packages/chat/src/test/chat'
import { createFakeTransport } from '../../../../packages/chat/src/test/fakeTransport'

beforeEach(resetChatState)
it.each([390, 3840].flatMap(width => [false, true].map(compact => ({ width, compact }))))('preserves pre-tool text at $width px (compact: $compact)', async ({ width, compact }) => {
  await page.viewport(width, width === 390 ? 844 : 2160)
  const text = 'I will inspect the timeline before changing the driver. Question 5: Should the report include the complete original message, including every detail after the question?'
  const thought = 'Compare the event kinds before selecting a fix.'
  seed(
    ev({ seq: 1, kind: 'user_message', payload: { text: 'Inspect the timeline.', source: 'chat' } }),
    ev({ seq: 2, kind: 'assistant_thinking', payload: { text: thought } }),
    ev({ seq: 3, kind: 'assistant_text', payload: { text, done: true } }),
    ev({ seq: 4, kind: 'tool_call', payload: { tool: 'Read', call_id: 'read', input: { file_path: 'example.ts' } } }),
    ev({ seq: 5, kind: 'tool_result', payload: { call_id: 'read', output: 'fixture', is_error: false } }),
  )
  const transport = createFakeTransport().transport
  const view = (thinking: 'shown' | 'hidden') => <div style={{ height: '100vh', width: '100%', minWidth: 0 }}><ChatView session={SESSION} transport={transport} meta={meta()} compact={compact} thinking={thinking} /></div>
  const { rerender } = render(view('shown'))
  const message = await screen.findByText(text)
  const card = message.closest<HTMLElement>('.agent-card')!
  expect(card.classList.contains('assistant')).toBe(true)
  expect(card.dataset.kind).not.toBe('thinking')
  expect(card.querySelector('.card-foot')!.textContent).not.toContain('thinking')
  expect(message.textContent).toBe(text)
  expect(message.getBoundingClientRect().height).toBeGreaterThan(0)
  expect(card.scrollWidth).toBeLessThanOrEqual(card.clientWidth)
  const thinking = screen.getByTestId('thinking')
  expect(thinking.textContent).toContain(thought)
  expect(thinking.querySelector('.card-foot')!.textContent).toContain('thinking')
  rerender(view('hidden'))
  expect(screen.queryByTestId('thinking')).toBeNull()
  expect(screen.getByText(text).textContent).toBe(text)
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
})
