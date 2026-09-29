import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import * as wsMock from '../test/wsMock'
import { useSessionStore } from './useSessionStore'
import { useToastStore } from './useToastStore'
import { makeDaemon, makeSession } from '../test/fixtures'

vi.mock('../ws', () => import('../test/wsMock'))

const get = () => useSessionStore.getState()

const TAKING_LONGER_MSG = "This is taking longer than expected — it may have failed to start. Try again or check the sidebar."

describe('useSessionStore', () => {
  beforeEach(() => {
    localStorage.clear()
    vi.clearAllTimers()
    useSessionStore.setState({ daemons: [], sessions: [], statusChangedAt: {}, serverVersion: '', pendingSessionIds: {}, failedSessions: {} })
    useToastStore.setState({ toasts: [] })
  })

  it('applies initial_state from the server', () => {
    wsMock.emit({
      type: 'initial_state',
      daemons: [makeDaemon()],
      sessions: [makeSession()],
      server_version: '2.1.0',
    })
    expect(get().daemons).toHaveLength(1)
    expect(get().sessions).toHaveLength(1)
    expect(get().serverVersion).toBe('2.1.0')
  })

  it('updates a session status on session_state_changed', () => {
    wsMock.emit({ type: 'initial_state', daemons: [], sessions: [makeSession({ status: 'running' })] })
    wsMock.emit({ type: 'session_state_changed', session_id: 'sess-a', status: 'waiting', unread: false })
    expect(get().sessions[0].status).toBe('waiting')
  })

  it('always shows a toast on a waiting/idle transition (regardless of activity)', () => {
    wsMock.emit({ type: 'initial_state', daemons: [], sessions: [makeSession({ status: 'running' })] })
    wsMock.emit({ type: 'session_state_changed', session_id: 'sess-a', status: 'idle', unread: false })
    const toasts = useToastStore.getState().toasts
    expect(toasts).toHaveLength(1)
    expect(toasts[0].title).toBe('Session idle')
    expect(toasts[0].url).toBe('/sessions/sess-a')
  })

  it('does not toast for a non-notifying transition (e.g. running)', () => {
    wsMock.emit({ type: 'initial_state', daemons: [], sessions: [makeSession({ status: 'waiting' })] })
    wsMock.emit({ type: 'session_state_changed', session_id: 'sess-a', status: 'running', unread: false })
    expect(useToastStore.getState().toasts).toHaveLength(0)
  })

  it('does not toast when the status did not actually change', () => {
    wsMock.emit({ type: 'initial_state', daemons: [], sessions: [makeSession({ status: 'idle' })] })
    wsMock.emit({ type: 'session_state_changed', session_id: 'sess-a', status: 'idle', unread: false })
    expect(useToastStore.getState().toasts).toHaveLength(0)
  })

  it('does not toast "finished its turn" when a starting session becomes ready and idle', () => {
    wsMock.emit({ type: 'initial_state', daemons: [], sessions: [makeSession({ status: 'starting' })] })
    wsMock.emit({ type: 'session_state_changed', session_id: 'sess-a', status: 'idle', unread: false })
    expect(get().sessions[0].status).toBe('idle')
    expect(useToastStore.getState().toasts).toHaveLength(0)
  })

  it('keeps the error reason that comes with an error broadcast', () => {
    wsMock.emit({ type: 'initial_state', daemons: [], sessions: [makeSession({ status: 'starting' })] })
    wsMock.emit({ type: 'session_state_changed', session_id: 'sess-a', status: 'error', message: 'cluster session cap reached (4)', unread: false })
    expect(get().sessions[0].error_reason).toBe('cluster session cap reached (4)')
  })

  it('takes why a session ended from the terminal broadcast, and drops it on a revive', () => {
    wsMock.emit({ type: 'initial_state', daemons: [], sessions: [makeSession({ status: 'running' })] })
    wsMock.emit({ type: 'session_state_changed', session_id: 'sess-a', status: 'stopped', unread: false, end_reason: 'stopped_by_user', ended_by: { kind: 'human', self: true } })
    expect(get().sessions[0].end_reason).toBe('stopped_by_user')
    expect(get().sessions[0].ended_by).toEqual({ kind: 'human', self: true })
    wsMock.emit({ type: 'session_state_changed', session_id: 'sess-a', status: 'running', unread: false })
    expect(get().sessions[0].end_reason).toBeUndefined()
    expect(get().sessions[0].ended_by).toBeNull()
  })

  it('takes why a session ended from session_ended', () => {
    wsMock.emit({ type: 'initial_state', daemons: [], sessions: [makeSession({ status: 'running' })] })
    wsMock.emit({ type: 'session_ended', session_id: 'sess-a', exit_code: 0, end_reason: 'process_exited' })
    expect(get().sessions[0].status).toBe('stopped')
    expect(get().sessions[0].end_reason).toBe('process_exited')
  })

  it('upserts a session announced twice (server at create, pod at start) without duplicating it', () => {
    wsMock.emit({ type: 'session_started', session: makeSession({ status: 'starting', daemon_id: 'cluster' }) })
    wsMock.emit({ type: 'session_started', session: makeSession({ status: 'starting', daemon_id: 'pod-daemon' }) })
    expect(get().sessions).toHaveLength(1)
    expect(get().sessions[0].daemon_id).toBe('pod-daemon')
  })

  it('a late "starting" announcement never drags a live session back', () => {
    wsMock.emit({ type: 'session_started', session: makeSession({ status: 'starting' }) })
    wsMock.emit({ type: 'session_state_changed', session_id: 'sess-a', status: 'idle', unread: false })
    wsMock.emit({ type: 'session_started', session: makeSession({ status: 'starting' }) })
    expect(get().sessions).toHaveLength(1)
    expect(get().sessions[0].status).toBe('idle')
  })

  it('adds a session on session_started', () => {
    wsMock.emit({ type: 'session_started', session: makeSession({ id: 'new-1' }) })
    expect(get().sessions.map(s => s.id)).toContain('new-1')
  })

  it('updates a title on session_title_changed', () => {
    wsMock.emit({ type: 'initial_state', daemons: [], sessions: [makeSession()] })
    wsMock.emit({ type: 'session_title_changed', session_id: 'sess-a', title: 'Renamed' })
    expect(get().sessions[0].title).toBe('Renamed')
  })

  it('marks affected sessions as error when a daemon disconnects', () => {
    wsMock.emit({
      type: 'initial_state',
      daemons: [makeDaemon()],
      sessions: [makeSession({ id: 'sess-a' })],
    })
    wsMock.emit({ type: 'daemon_disconnected', daemon_id: 'd1', affected_session_ids: ['sess-a'] })
    expect(get().sessions[0].status).toBe('error')
    expect(get().daemons).toHaveLength(0)
  })

  it('updateSessionStatus action mutates the matching session', () => {
    useSessionStore.setState({ sessions: [makeSession({ status: 'running' })] })
    get().updateSessionStatus('sess-a', 'idle')
    expect(get().sessions[0].status).toBe('idle')
  })

  // ── Pending / failed session state ───────────────────────────────────────────

  it('(a) session_started removes id from pendingSessionIds and adds to sessions', () => {
    get().addPendingSession('sess-new')
    expect(get().pendingSessionIds['sess-new']).toBe(true)
    wsMock.emit({ type: 'session_started', session: makeSession({ id: 'sess-new' }) })
    expect(get().pendingSessionIds['sess-new']).toBeUndefined()
    expect(get().sessions.some(s => s.id === 'sess-new')).toBe(true)
  })

  it('(b) session_state_changed error for pending id absent from sessions → moves to failedSessions', () => {
    get().addPendingSession('sess-err')
    wsMock.emit({
      type: 'session_state_changed',
      session_id: 'sess-err',
      status: 'error',
      message: 'daemon crashed',
      unread: false,
    })
    expect(get().pendingSessionIds['sess-err']).toBeUndefined()
    expect(get().failedSessions['sess-err']).toBe('daemon crashed')
  })

  it('(c) session_state_changed error for id already in sessions → updates status, failedSessions untouched', () => {
    wsMock.emit({ type: 'initial_state', daemons: [], sessions: [makeSession({ id: 'sess-existing', status: 'running' })], server_version: '' })
    wsMock.emit({ type: 'session_state_changed', session_id: 'sess-existing', status: 'error', message: 'oops', unread: false })
    expect(get().sessions.find(s => s.id === 'sess-existing')?.status).toBe('error')
    expect(Object.keys(get().failedSessions)).toHaveLength(0)
  })

  describe('timer-based tests', () => {
    beforeEach(() => vi.useFakeTimers())
    afterEach(() => vi.useRealTimers())

    it('(d) addPendingSession + 90s elapsed → failedSessions with "taking longer" copy', () => {
      get().addPendingSession('sess-timeout')
      vi.advanceTimersByTime(90_000)
      expect(get().pendingSessionIds['sess-timeout']).toBeUndefined()
      expect(get().failedSessions['sess-timeout']).toBe(TAKING_LONGER_MSG)
    })

    it('(e) addPendingSession → session_started → 90s → NOT marked failed (timer cancelled)', () => {
      get().addPendingSession('sess-ok')
      wsMock.emit({ type: 'session_started', session: makeSession({ id: 'sess-ok' }) })
      vi.advanceTimersByTime(90_000)
      expect(get().failedSessions['sess-ok']).toBeUndefined()
    })
  })

  it('removePendingSession is idempotent (safe to call for unknown id)', () => {
    expect(() => get().removePendingSession('nonexistent')).not.toThrow()
  })

  it('clearFailedSession removes the entry', () => {
    useSessionStore.setState({ failedSessions: { 'sess-x': 'some error' } })
    get().clearFailedSession('sess-x')
    expect(get().failedSessions['sess-x']).toBeUndefined()
  })

  it('clearFailedSession removes an entry whose stored message is the empty string', () => {
    useSessionStore.setState({ failedSessions: { 'sess-x': '' } })
    get().clearFailedSession('sess-x')
    expect('sess-x' in get().failedSessions).toBe(false)
  })

  // ── unread tracking ───────────────────────────────────────────────────────────

  it('session_state_changed with unread:true sets the session unread flag', () => {
    wsMock.emit({ type: 'initial_state', daemons: [], sessions: [makeSession({ status: 'running', unread: false })] })
    wsMock.emit({ type: 'session_state_changed', session_id: 'sess-a', status: 'waiting', unread: true })
    expect(get().sessions[0].unread).toBe(true)
  })

  it('session_state_changed with unread:false keeps unread false', () => {
    wsMock.emit({ type: 'initial_state', daemons: [], sessions: [makeSession({ status: 'running', unread: false })] })
    wsMock.emit({ type: 'session_state_changed', session_id: 'sess-a', status: 'idle', unread: false })
    expect(get().sessions[0].unread).toBe(false)
  })

  it('session_read_changed sets the matching session unread to false', () => {
    wsMock.emit({ type: 'initial_state', daemons: [], sessions: [makeSession({ status: 'waiting', unread: true })] })
    wsMock.emit({ type: 'session_read_changed', session_id: 'sess-a' })
    expect(get().sessions[0].unread).toBe(false)
  })

  it('initial_state seeds unread from session data', () => {
    wsMock.emit({ type: 'initial_state', daemons: [], sessions: [makeSession({ unread: true })] })
    expect(get().sessions[0].unread).toBe(true)
  })

  // ── starred tracking ──────────────────────────────────────────────────────────

  it('session_star_changed with starred:true sets the session starred flag', () => {
    wsMock.emit({ type: 'initial_state', daemons: [], sessions: [makeSession({ starred: false })] })
    wsMock.emit({ type: 'session_star_changed', session_id: 'sess-a', starred: true })
    expect(get().sessions[0].starred).toBe(true)
  })

  it('session_state_changed does NOT clear an existing starred flag', () => {
    wsMock.emit({ type: 'initial_state', daemons: [], sessions: [makeSession({ status: 'running', starred: true })] })
    wsMock.emit({ type: 'session_state_changed', session_id: 'sess-a', status: 'idle', unread: false })
    expect(get().sessions[0].starred).toBe(true)
  })
})
