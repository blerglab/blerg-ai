import { describe, it, expect, vi, beforeEach } from 'vitest'
import * as wsMock from '../test/wsMock'
import { useMessageStore, selectOpenCount, selectBySession, selectUnreadCount, markAllSeen } from './useMessageStore'
import { useToastStore } from './useToastStore'
import { useSessionStore } from './useSessionStore'
import { makeSession } from '../test/fixtures'
import type { MessageInfo } from '../types'

vi.mock('../ws', () => import('../test/wsMock'))

const get = () => useMessageStore.getState()

function makeMessage(over: Partial<MessageInfo> = {}): MessageInfo {
  return {
    id: 'msg-1',
    session_id: 'sess-a',
    kind: 'ask',
    body: 'What should I do?',
    status: 'open',
    answer: null,
    created_at: '2026-06-23T00:00:00Z',
    answered_at: null,
    ...over,
  }
}

describe('useMessageStore', () => {
  beforeEach(() => {
    useMessageStore.setState({ messages: [], lastSeenAt: 0 })
    localStorage.clear()
  })

  it('initial_state seeds messages from the server payload', () => {
    wsMock.emit({
      type: 'initial_state',
      daemons: [],
      sessions: [],
      messages: [makeMessage({ id: 'msg-1' }), makeMessage({ id: 'msg-2' })],
    })
    expect(get().messages).toHaveLength(2)
    expect(get().messages[0].id).toBe('msg-1')
    expect(get().messages[1].id).toBe('msg-2')
  })

  it('initial_state replaces existing messages (not appends)', () => {
    useMessageStore.setState({ messages: [makeMessage({ id: 'old' })] })
    wsMock.emit({
      type: 'initial_state',
      daemons: [],
      sessions: [],
      messages: [makeMessage({ id: 'new-1' })],
    })
    expect(get().messages).toHaveLength(1)
    expect(get().messages[0].id).toBe('new-1')
  })

  it('message_created prepends new message (newest-first order)', () => {
    useMessageStore.setState({ messages: [makeMessage({ id: 'existing' })] })
    wsMock.emit({
      type: 'message_created',
      message: makeMessage({ id: 'newest' }),
    })
    expect(get().messages[0].id).toBe('newest')
    expect(get().messages[1].id).toBe('existing')
  })

  it('message_created de-dupes by id (ignores if already present)', () => {
    useMessageStore.setState({ messages: [makeMessage({ id: 'dup' })] })
    wsMock.emit({
      type: 'message_created',
      message: makeMessage({ id: 'dup' }),
    })
    expect(get().messages).toHaveLength(1)
  })

  it('message_answered replaces the matching message by id', () => {
    useMessageStore.setState({
      messages: [makeMessage({ id: 'msg-a', status: 'open', answer: null })],
    })
    wsMock.emit({
      type: 'message_answered',
      message: makeMessage({ id: 'msg-a', status: 'answered', answer: 'Do X', answered_at: '2026-06-23T01:00:00Z' }),
    })
    expect(get().messages).toHaveLength(1)
    expect(get().messages[0].status).toBe('answered')
    expect(get().messages[0].answer).toBe('Do X')
    expect(get().messages[0].answered_at).toBe('2026-06-23T01:00:00Z')
  })

  it('message_answered is a no-op for an unknown id', () => {
    useMessageStore.setState({ messages: [makeMessage({ id: 'known' })] })
    wsMock.emit({
      type: 'message_answered',
      message: makeMessage({ id: 'unknown', status: 'answered' }),
    })
    expect(get().messages).toHaveLength(1)
    expect(get().messages[0].id).toBe('known')
  })

  // ── Selectors ────────────────────────────────────────────────────────────────

  it('selectOpenCount counts only status==="open" messages', () => {
    useMessageStore.setState({
      messages: [
        makeMessage({ id: '1', status: 'open' }),
        makeMessage({ id: '2', status: 'answered' }),
        makeMessage({ id: '3', status: 'open' }),
      ],
    })
    expect(selectOpenCount(get())).toBe(2)
  })

  it('selectOpenCount returns 0 when no open messages', () => {
    useMessageStore.setState({
      messages: [makeMessage({ id: '1', status: 'answered' })],
    })
    expect(selectOpenCount(get())).toBe(0)
  })

  it('selectBySession returns only messages for the given session id', () => {
    useMessageStore.setState({
      messages: [
        makeMessage({ id: '1', session_id: 'sess-a' }),
        makeMessage({ id: '2', session_id: 'sess-b' }),
        makeMessage({ id: '3', session_id: 'sess-a' }),
      ],
    })
    const forA = selectBySession('sess-a')(get())
    expect(forA).toHaveLength(2)
    expect(forA.every(m => m.session_id === 'sess-a')).toBe(true)
  })

  it('selectBySession returns empty array when no messages match', () => {
    useMessageStore.setState({
      messages: [makeMessage({ id: '1', session_id: 'sess-a' })],
    })
    expect(selectBySession('sess-z')(get())).toHaveLength(0)
  })
})

// ── selectUnreadCount and markAllSeen ──────────────────────────────────────────

describe('selectUnreadCount and markAllSeen', () => {
  beforeEach(() => {
    useMessageStore.setState({ messages: [], lastSeenAt: 0 })
    localStorage.clear()
  })

  it('selectUnreadCount counts messages created after lastSeenAt', () => {
    const cutoff = new Date('2026-06-23T09:00:00Z').getTime()
    useMessageStore.setState({
      messages: [
        makeMessage({ id: '1', created_at: '2026-06-23T10:00:00Z' }), // after cutoff → unread
        makeMessage({ id: '2', created_at: '2026-06-23T08:00:00Z' }), // before cutoff → read
      ],
      lastSeenAt: cutoff,
    })
    expect(selectUnreadCount(useMessageStore.getState())).toBe(1)
  })

  it('selectUnreadCount returns 0 when lastSeenAt is after all messages', () => {
    const future = new Date('2026-06-24T00:00:00Z').getTime()
    useMessageStore.setState({
      messages: [makeMessage({ id: '1', created_at: '2026-06-23T10:00:00Z' })],
      lastSeenAt: future,
    })
    expect(selectUnreadCount(useMessageStore.getState())).toBe(0)
  })

  it('markAllSeen sets lastSeenAt to now and clears unread count', () => {
    useMessageStore.setState({
      messages: [makeMessage({ id: '1', created_at: '2026-06-23T10:00:00Z' })],
      lastSeenAt: 0,
    })
    expect(selectUnreadCount(useMessageStore.getState())).toBe(1)
    markAllSeen()
    expect(selectUnreadCount(useMessageStore.getState())).toBe(0)
  })

  it('markAllSeen persists lastSeenAt to localStorage', () => {
    markAllSeen()
    const stored = localStorage.getItem('blerg-runner.messages.lastSeen')
    expect(stored).toBeTruthy()
    expect(parseInt(stored!, 10)).toBeGreaterThan(0)
  })
})

// ── message_created toast ──────────────────────────────────────────────────────

describe('message_created toast', () => {
  beforeEach(() => {
    useMessageStore.setState({ messages: [], lastSeenAt: 0 })
    useToastStore.setState({ toasts: [] })
    useSessionStore.setState({
      daemons: [],
      sessions: [],
      statusChangedAt: {},
      serverVersion: '',
      pendingSessionIds: {},
      failedSessions: {},
    })
  })

  it('fires a variant:message toast on live message_created', () => {
    useSessionStore.setState({
      daemons: [],
      sessions: [makeSession({ id: 'sess-a', title: 'My Project' })],
      statusChangedAt: {},
      serverVersion: '',
      pendingSessionIds: {},
      failedSessions: {},
    })
    wsMock.emit({
      type: 'message_created',
      message: makeMessage({ id: 'msg-new', session_id: 'sess-a', kind: 'ask', body: 'What next?' }),
    })
    const toasts = useToastStore.getState().toasts
    expect(toasts).toHaveLength(1)
    expect(toasts[0].variant).toBe('message')
    expect(toasts[0].url).toBe('/')
    expect(toasts[0].title).toContain('My Project')
    expect(toasts[0].title).toContain('ask')
    expect(toasts[0].body).toBe('What next?')
  })

  it('falls back to session_id in toast title when session is not in store', () => {
    wsMock.emit({
      type: 'message_created',
      message: makeMessage({ id: 'msg-new', session_id: 'unknown-xyz', kind: 'note', body: 'Hi' }),
    })
    const toasts = useToastStore.getState().toasts
    expect(toasts).toHaveLength(1)
    expect(toasts[0].title).toContain('unknown-xyz')
  })

  it('initial_state does NOT fire a toast', () => {
    wsMock.emit({
      type: 'initial_state',
      daemons: [],
      sessions: [],
      messages: [makeMessage({ id: 'msg-1' })],
    })
    expect(useToastStore.getState().toasts).toHaveLength(0)
  })

  it('message_created de-dupe does NOT fire a toast for already-present messages', () => {
    useMessageStore.setState({ messages: [makeMessage({ id: 'dup' })], lastSeenAt: 0 })
    wsMock.emit({
      type: 'message_created',
      message: makeMessage({ id: 'dup' }),
    })
    expect(useToastStore.getState().toasts).toHaveLength(0)
  })
})
