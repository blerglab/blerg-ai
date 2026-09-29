import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { useMessageStore } from '../hooks/useMessageStore'
import type { MessageInfo } from '../types'

vi.mock('../ws', () => import('../test/wsMock'))

const mockNavigate = vi.fn()
vi.mock('react-router-dom', async (importOriginal) => {
  const actual = await importOriginal<typeof import('react-router-dom')>()
  return { ...actual, useNavigate: () => mockNavigate }
})

// Import AFTER the mock so the component picks up the mocked useNavigate.
import ChatBubble from './ChatBubble'

function makeMessage(over: Partial<MessageInfo> = {}): MessageInfo {
  return {
    id: 'msg-1',
    session_id: 'sess-a',
    kind: 'ask',
    body: 'Hello',
    status: 'open',
    answer: null,
    created_at: '2026-06-23T10:00:00Z',
    answered_at: null,
    ...over,
  }
}

function renderBubble() {
  return render(
    <MemoryRouter>
      <ChatBubble />
    </MemoryRouter>,
  )
}

describe('ChatBubble', () => {
  beforeEach(() => {
    // Set lastSeenAt far in the future so no messages are "unread" by default.
    useMessageStore.setState({ messages: [], lastSeenAt: Date.now() + 9_999_999 })
    mockNavigate.mockClear()
  })

  it('renders the chat bubble button', () => {
    renderBubble()
    expect(screen.getByTestId('chat-bubble')).toBeInTheDocument()
  })

  it('hides the badge when unread count is 0', () => {
    renderBubble()
    expect(screen.queryByTestId('chat-bubble-badge')).not.toBeInTheDocument()
  })

  it('shows the badge with unread count when count > 0', () => {
    useMessageStore.setState({
      messages: [makeMessage({ id: '1', created_at: '2026-06-23T10:00:00Z' })],
      lastSeenAt: 0, // all messages are unread
    })
    renderBubble()
    const badge = screen.getByTestId('chat-bubble-badge')
    expect(badge).toBeInTheDocument()
    expect(badge.textContent).toBe('1')
  })

  it('shows correct count for multiple unread messages', () => {
    useMessageStore.setState({
      messages: [
        makeMessage({ id: '1', created_at: '2026-06-23T10:00:00Z' }),
        makeMessage({ id: '2', created_at: '2026-06-23T10:01:00Z' }),
      ],
      lastSeenAt: 0,
    })
    renderBubble()
    expect(screen.getByTestId('chat-bubble-badge').textContent).toBe('2')
  })

  it('clicking the bubble navigates to /', () => {
    renderBubble()
    fireEvent.click(screen.getByTestId('chat-bubble'))
    expect(mockNavigate).toHaveBeenCalledWith('/')
  })
})
