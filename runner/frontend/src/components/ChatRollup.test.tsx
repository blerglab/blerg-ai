import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { MemoryRouter, useLocation } from 'react-router-dom'
import ChatRollup, { MessageThread } from './ChatRollup'
import { useMessageStore, selectUnreadCount } from '../hooks/useMessageStore'
import { useSessionStore } from '../hooks/useSessionStore'
import { useToastStore } from '../hooks/useToastStore'
import { makeSession } from '../test/fixtures'
import type { MessageInfo } from '../types'

vi.mock('../ws', () => import('../test/wsMock'))

function makeMessage(over: Partial<MessageInfo> = {}): MessageInfo {
  return {
    id: 'msg-1',
    session_id: 'sess-a',
    kind: 'update',
    body: 'Task finished',
    status: 'answered',
    created_at: '2026-06-23T10:00:00Z',
    ...over,
  }
}

function renderRollup() {
  return render(
    <MemoryRouter initialEntries={['/']}>
      <ChatRollup />
    </MemoryRouter>,
  )
}

describe('ChatRollup', () => {
  beforeEach(() => {
    useMessageStore.setState({ messages: [], lastSeenAt: 0 })
    useSessionStore.setState({ daemons: [], sessions: [], statusChangedAt: {}, serverVersion: '' })
    useToastStore.setState({ toasts: [] })
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('renders empty state when no messages', () => {
    renderRollup()
    expect(screen.getByText(/no messages/i)).toBeInTheDocument()
  })

  it('renders messages newest-first, each tagged with session title', () => {
    useSessionStore.setState({
      daemons: [],
      sessions: [
        makeSession({ id: 'sess-a', title: 'Widget build', repo: 'widget' }),
        makeSession({ id: 'sess-b', title: 'Backend fix', repo: 'backend' }),
      ],
      statusChangedAt: {},
      serverVersion: '',
    })
    // Store is already newest-first; pass in that order
    useMessageStore.setState({
      messages: [
        makeMessage({ id: 'msg-2', session_id: 'sess-a', body: 'Newer message', created_at: '2026-06-23T11:00:00Z' }),
        makeMessage({ id: 'msg-1', session_id: 'sess-b', body: 'Older message', created_at: '2026-06-23T10:00:00Z' }),
      ],
    })
    renderRollup()

    expect(screen.getByText('Newer message')).toBeInTheDocument()
    expect(screen.getByText('Older message')).toBeInTheDocument()
    // Tagged with session title
    expect(screen.getAllByText('Widget build').length).toBeGreaterThan(0)
    expect(screen.getAllByText('Backend fix').length).toBeGreaterThan(0)
    // Newest-first: "Newer message" appears before "Older message" in DOM
    const body = document.body.textContent!
    expect(body.indexOf('Newer message')).toBeLessThan(body.indexOf('Older message'))
  })

  it('open ask message shows an inline reply box', () => {
    useSessionStore.setState({
      daemons: [],
      sessions: [makeSession({ id: 'sess-a', title: 'Widget build' })],
      statusChangedAt: {},
      serverVersion: '',
    })
    useMessageStore.setState({
      messages: [
        makeMessage({ id: 'msg-1', session_id: 'sess-a', kind: 'ask', status: 'open', body: 'What should I do?' }),
      ],
    })
    renderRollup()
    expect(screen.getByRole('textbox')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /send/i })).toBeInTheDocument()
  })

  it('open note message shows an inline reply box', () => {
    useSessionStore.setState({
      daemons: [],
      sessions: [makeSession({ id: 'sess-a', title: 'Widget build' })],
      statusChangedAt: {},
      serverVersion: '',
    })
    useMessageStore.setState({
      messages: [
        makeMessage({ id: 'msg-1', session_id: 'sess-a', kind: 'note', status: 'open', body: 'Side thought' }),
      ],
    })
    renderRollup()
    expect(screen.getByRole('textbox')).toBeInTheDocument()
  })

  it('submitting a reply POSTs to /api/messages/{id}/reply with the typed answer', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, json: async () => ({}) })
    vi.stubGlobal('fetch', fetchMock)

    useSessionStore.setState({
      daemons: [],
      sessions: [makeSession({ id: 'sess-a', title: 'Widget build' })],
      statusChangedAt: {},
      serverVersion: '',
    })
    useMessageStore.setState({
      messages: [
        makeMessage({ id: 'msg-42', session_id: 'sess-a', kind: 'ask', status: 'open', body: 'Question?' }),
      ],
    })
    renderRollup()

    const input = screen.getByRole('textbox')
    fireEvent.change(input, { target: { value: 'My answer here' } })
    fireEvent.click(screen.getByRole('button', { name: /send/i }))

    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        '/api/messages/msg-42/reply',
        expect.objectContaining({
          method: 'POST',
          // apiFetch (Task 18) adds Authorization on top of the caller's headers.
          headers: expect.objectContaining({ 'Content-Type': 'application/json' }),
          body: JSON.stringify({ answer: 'My answer here' }),
        }),
      )
    })
  })

  it('open ask is rendered above newer answered messages', () => {
    useSessionStore.setState({
      daemons: [],
      sessions: [makeSession({ id: 'sess-a', title: 'Widget build' })],
      statusChangedAt: {},
      serverVersion: '',
    })
    // Store order: newer answered messages listed first, old open ask last
    useMessageStore.setState({
      messages: [
        makeMessage({ id: 'msg-new2', session_id: 'sess-a', kind: 'update', status: 'answered', body: 'Newer answered 2', created_at: '2026-06-23T12:00:00Z' }),
        makeMessage({ id: 'msg-new1', session_id: 'sess-a', kind: 'update', status: 'answered', body: 'Newer answered 1', created_at: '2026-06-23T11:00:00Z' }),
        makeMessage({ id: 'msg-old', session_id: 'sess-a', kind: 'ask', status: 'open', body: 'Old open question', created_at: '2026-06-23T09:00:00Z' }),
      ],
    })
    renderRollup()
    const body = document.body.textContent!
    // Open ask must appear before answered messages regardless of arrival order
    expect(body.indexOf('Old open question')).toBeLessThan(body.indexOf('Newer answered 1'))
    expect(body.indexOf('Old open question')).toBeLessThan(body.indexOf('Newer answered 2'))
  })

  it('failed reply (non-2xx) keeps the input populated and shows an error toast', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: false, status: 409, json: async () => ({}) })
    vi.stubGlobal('fetch', fetchMock)

    useSessionStore.setState({
      daemons: [],
      sessions: [makeSession({ id: 'sess-a', title: 'Widget build' })],
      statusChangedAt: {},
      serverVersion: '',
    })
    useMessageStore.setState({
      messages: [
        makeMessage({ id: 'msg-42', session_id: 'sess-a', kind: 'ask', status: 'open', body: 'Question?' }),
      ],
    })
    renderRollup()

    const input = screen.getByRole('textbox')
    fireEvent.change(input, { target: { value: 'My answer' } })
    fireEvent.click(screen.getByRole('button', { name: /send/i }))

    await waitFor(() => expect(fetchMock).toHaveBeenCalled())
    expect((input as HTMLInputElement).value).toBe('My answer')
    expect(useToastStore.getState().toasts).toHaveLength(1)
  })

  it('successful reply clears the input', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, json: async () => ({}) })
    vi.stubGlobal('fetch', fetchMock)

    useSessionStore.setState({
      daemons: [],
      sessions: [makeSession({ id: 'sess-a', title: 'Widget build' })],
      statusChangedAt: {},
      serverVersion: '',
    })
    useMessageStore.setState({
      messages: [
        makeMessage({ id: 'msg-42', session_id: 'sess-a', kind: 'ask', status: 'open', body: 'Question?' }),
      ],
    })
    renderRollup()

    const input = screen.getByRole('textbox')
    fireEvent.change(input, { target: { value: 'My answer' } })
    fireEvent.click(screen.getByRole('button', { name: /send/i }))

    await waitFor(() => expect((input as HTMLInputElement).value).toBe(''))
    expect(useToastStore.getState().toasts).toHaveLength(0)
  })

  it('update message shows NO reply box', () => {
    useSessionStore.setState({
      daemons: [],
      sessions: [makeSession({ id: 'sess-a', title: 'Widget build' })],
      statusChangedAt: {},
      serverVersion: '',
    })
    useMessageStore.setState({
      messages: [
        makeMessage({ id: 'msg-1', session_id: 'sess-a', kind: 'update', status: 'answered', body: 'Task done' }),
      ],
    })
    renderRollup()
    expect(screen.queryByRole('textbox')).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /send/i })).not.toBeInTheDocument()
  })

  it('answered ask message shows NO reply box', () => {
    useSessionStore.setState({
      daemons: [],
      sessions: [makeSession({ id: 'sess-a', title: 'Widget build' })],
      statusChangedAt: {},
      serverVersion: '',
    })
    useMessageStore.setState({
      messages: [
        makeMessage({ id: 'msg-1', session_id: 'sess-a', kind: 'ask', status: 'answered', body: 'Question?' }),
      ],
    })
    renderRollup()
    expect(screen.queryByRole('textbox')).not.toBeInTheDocument()
  })

  it('falls back to session ID tag when session is unknown', () => {
    useSessionStore.setState({
      daemons: [],
      sessions: [],
      statusChangedAt: {},
      serverVersion: '',
    })
    useMessageStore.setState({
      messages: [
        makeMessage({ id: 'msg-1', session_id: 'unknown-sess-xyz', body: 'Hello', status: 'answered' }),
      ],
    })
    renderRollup()
    expect(screen.getByText('unknown-sess-xyz')).toBeInTheDocument()
  })

  it('marks all messages as seen on mount (clears unread count)', () => {
    useMessageStore.setState({
      messages: [makeMessage({ id: 'msg-1', created_at: '2026-06-23T10:00:00Z' })],
      lastSeenAt: 0, // all messages are unread
    })
    expect(selectUnreadCount(useMessageStore.getState())).toBe(1)
    renderRollup()
    expect(selectUnreadCount(useMessageStore.getState())).toBe(0)
  })

  it('marks messages seen again when the messages array changes while mounted', () => {
    useMessageStore.setState({
      messages: [makeMessage({ id: 'msg-1', created_at: '2026-06-23T10:00:00Z' })],
      lastSeenAt: new Date('2026-06-23T11:00:00Z').getTime(),
    })
    renderRollup()
    expect(selectUnreadCount(useMessageStore.getState())).toBe(0)

    // Simulate a new message arriving while roll-up is mounted.
    useMessageStore.setState((s) => ({
      messages: [makeMessage({ id: 'msg-2', created_at: '2026-06-23T12:00:00Z' }), ...s.messages],
      lastSeenAt: s.lastSeenAt,
    }))
    // Roll-up is mounted so it immediately re-fires markAllSeen → count goes back to 0.
    expect(selectUnreadCount(useMessageStore.getState())).toBe(0)
  })
})

// ─── MessageThread hideSessionLabel ──────────────────────────────────────────

describe('MessageThread hideSessionLabel', () => {
  beforeEach(() => {
    useMessageStore.setState({ messages: [] })
    useSessionStore.setState({
      daemons: [],
      sessions: [makeSession({ id: 'sess-a', title: 'Widget build', repo: 'widget' })],
      statusChangedAt: {},
      serverVersion: '',
    })
    useToastStore.setState({ toasts: [] })
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('per-session thread (hideSessionLabel=true) does NOT render session title on each bubble', () => {
    const msgs: MessageInfo[] = [
      makeMessage({ id: 'msg-1', session_id: 'sess-a', body: 'First update', status: 'answered' }),
      makeMessage({ id: 'msg-2', session_id: 'sess-a', body: 'Second update', status: 'answered', created_at: '2026-06-23T11:00:00Z' }),
    ]
    render(
      <MemoryRouter initialEntries={['/']}>
        <MessageThread messages={msgs} hideSessionLabel />
      </MemoryRouter>,
    )
    expect(screen.getByText('First update')).toBeInTheDocument()
    // The session title must NOT appear anywhere in the rendered output
    expect(screen.queryByText('Widget build')).not.toBeInTheDocument()
  })

  it('global roll-up (no hideSessionLabel) still shows session title on bubbles', () => {
    useMessageStore.setState({
      messages: [
        makeMessage({ id: 'msg-1', session_id: 'sess-a', body: 'Some update', status: 'answered' }),
      ],
    })
    render(
      <MemoryRouter initialEntries={['/']}>
        <ChatRollup />
      </MemoryRouter>,
    )
    expect(screen.getByText('Widget build')).toBeInTheDocument()
  })
})

// ─── MessageBubble navigation ─────────────────────────────────────────────────

function LocationProbe() {
  const loc = useLocation()
  return <div data-testid="location">{loc.pathname}</div>
}

describe('MessageBubble navigation', () => {
  beforeEach(() => {
    useMessageStore.setState({ messages: [], lastSeenAt: 0 })
    useSessionStore.setState({
      daemons: [],
      sessions: [makeSession({ id: 'sess-a', title: 'Widget build' })],
      statusChangedAt: {},
      serverVersion: '',
    })
    useToastStore.setState({ toasts: [] })
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('clicking the message bubble navigates to /sessions/<session_id>', () => {
    useMessageStore.setState({
      messages: [
        makeMessage({ id: 'msg-1', session_id: 'sess-a', kind: 'update', status: 'answered', body: 'Task done' }),
      ],
    })
    render(
      <MemoryRouter initialEntries={['/']}>
        <ChatRollup />
        <LocationProbe />
      </MemoryRouter>,
    )
    fireEvent.click(screen.getByText('Task done'))
    expect(screen.getByTestId('location')).toHaveTextContent('/sessions/sess-a')
  })

  it('clicking inside the reply input does NOT navigate', () => {
    useMessageStore.setState({
      messages: [
        makeMessage({ id: 'msg-1', session_id: 'sess-a', kind: 'ask', status: 'open', body: 'Question?' }),
      ],
    })
    render(
      <MemoryRouter initialEntries={['/']}>
        <ChatRollup />
        <LocationProbe />
      </MemoryRouter>,
    )
    fireEvent.click(screen.getByRole('textbox'))
    expect(screen.getByTestId('location')).toHaveTextContent('/')
  })

  it('clicking the send button does NOT navigate', () => {
    useMessageStore.setState({
      messages: [
        makeMessage({ id: 'msg-1', session_id: 'sess-a', kind: 'ask', status: 'open', body: 'Question?' }),
      ],
    })
    render(
      <MemoryRouter initialEntries={['/']}>
        <ChatRollup />
        <LocationProbe />
      </MemoryRouter>,
    )
    fireEvent.click(screen.getByRole('button', { name: /send/i }))
    expect(screen.getByTestId('location')).toHaveTextContent('/')
  })
})

// ─── MessageBubble reply bubble ───────────────────────────────────────────────

describe('MessageBubble reply bubble', () => {
  beforeEach(() => {
    useMessageStore.setState({ messages: [], lastSeenAt: 0 })
    useSessionStore.setState({
      daemons: [],
      sessions: [makeSession({ id: 'sess-a', title: 'Widget build' })],
      statusChangedAt: {},
      serverVersion: '',
    })
    useToastStore.setState({ toasts: [] })
  })

  it('answered ask with a real answer renders a You reply bubble with the answer text', () => {
    useMessageStore.setState({
      messages: [
        makeMessage({ id: 'msg-1', session_id: 'sess-a', kind: 'ask', status: 'answered', body: 'Question?', answer: 'My reply' }),
      ],
    })
    render(
      <MemoryRouter initialEntries={['/']}>
        <ChatRollup />
      </MemoryRouter>,
    )
    const reply = screen.getByTestId('message-reply')
    expect(reply).toBeInTheDocument()
    expect(reply).toHaveTextContent('My reply')
  })

  it('update message (answer null) renders NO reply bubble', () => {
    useMessageStore.setState({
      messages: [
        makeMessage({ id: 'msg-1', session_id: 'sess-a', kind: 'update', status: 'answered', body: 'Task done', answer: null }),
      ],
    })
    render(
      <MemoryRouter initialEntries={['/']}>
        <ChatRollup />
      </MemoryRouter>,
    )
    expect(screen.queryByTestId('message-reply')).not.toBeInTheDocument()
  })

  it('closed/expired message (answer empty string) renders NO reply bubble', () => {
    useMessageStore.setState({
      messages: [
        makeMessage({ id: 'msg-1', session_id: 'sess-a', kind: 'ask', status: 'answered', body: 'Expired?', answer: '' }),
      ],
    })
    render(
      <MemoryRouter initialEntries={['/']}>
        <ChatRollup />
      </MemoryRouter>,
    )
    expect(screen.queryByTestId('message-reply')).not.toBeInTheDocument()
  })

  it('open ask still shows the inline reply box and NO reply bubble', () => {
    useMessageStore.setState({
      messages: [
        makeMessage({ id: 'msg-1', session_id: 'sess-a', kind: 'ask', status: 'open', body: 'What next?' }),
      ],
    })
    render(
      <MemoryRouter initialEntries={['/']}>
        <ChatRollup />
      </MemoryRouter>,
    )
    expect(screen.getByRole('textbox')).toBeInTheDocument()
    expect(screen.queryByTestId('message-reply')).not.toBeInTheDocument()
  })

  it('shows when each message and its reply were sent, absolute time in the title', () => {
    const created = new Date(Date.now() - 270_000).toISOString()
    useMessageStore.setState({
      messages: [makeMessage({ id: 'msg-t', created_at: created, answer: 'ok', answered_at: created })],
    })
    renderRollup()
    const times = document.querySelectorAll('time')
    expect(times).toHaveLength(2)
    expect(times[0].textContent).toBe('4 min ago')
    expect(times[0].getAttribute('datetime')).toBe(created)
    expect(times[0].getAttribute('title')).toBeTruthy()
  })

  it('shows no time for a message with an unusable timestamp', () => {
    useMessageStore.setState({ messages: [makeMessage({ id: 'msg-bad', created_at: '' })] })
    renderRollup()
    expect(document.querySelector('time')).toBeNull()
    expect(document.body.textContent).not.toMatch(/Invalid Date|NaN/)
  })
})
