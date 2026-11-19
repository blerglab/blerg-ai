import { expect, it, vi } from 'vitest'
import { act, render, screen } from '@testing-library/react'
import { page } from 'vitest/browser'
import { MemoryRouter } from 'react-router-dom'
import { MessageThread } from './MessageThread'
import '../index.css'
import { ChatView } from '@blerglab/chat'
import { createFakeTransport } from '../../../../packages/chat/src/test/fakeTransport'
import { SESSION, ev, meta, resetChatState, seed } from '../../../../packages/chat/src/test/chat'

vi.mock('../ws', () => import('../test/wsMock'))

it.each([[390, 844], [3840, 2160]])('shows a withdrawn question without an answer at %ipx', async (width, height) => {
  await page.viewport(width, height)
  render(<MemoryRouter><MessageThread messages={[{
    id: 'withdrawn-question', session_id: 'child', kind: 'ask', status: 'withdrawn',
    body: 'Which release should I use?', answer: null, created_at: '2026-10-30T18:00:00Z',
  }]} /></MemoryRouter>)
  expect(screen.getByText('Withdrawn · workspace discarded without an answer')).toBeVisible()
  expect(screen.queryByPlaceholderText('Type your reply…')).toBeNull()
  expect(screen.queryByTestId('message-reply')).toBeNull()
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
})

it.each([[390, 844], [3840, 2160]])('labels withdrawal on the question card at %ipx', async (width, height) => {
  await page.viewport(width, height)
  resetChatState()
  seed(ev({ kind: 'ask', seq: 1, payload: { message_id: 'question', body: 'Which release?', no_wait: true } }))
  const t = createFakeTransport()
  render(<div style={{ height: '100vh' }}><ChatView session={SESSION} transport={t.transport} meta={meta({ status: 'idle' })} /></div>)
  act(() => t.closeAsk('question', '', false, undefined, true))
  expect(screen.getByTestId('ask-closed').textContent).toBe('Withdrawn · workspace discarded without an answer')
  expect(screen.getByTestId('ask-closed')).toBeVisible()
  expect(screen.queryByText('Answered:')).toBeNull()
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
})
