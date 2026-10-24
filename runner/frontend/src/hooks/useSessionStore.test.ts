import {useTranscriptStore} from '@blerglab/chat'
import ownershipEvent from '../test/desktopOwnership.json'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import * as wsMock from '../test/wsMock'
import { useSessionStore } from './useSessionStore'
import { useToastStore } from './useToastStore'
import { makeDaemon, makeSession } from '../test/fixtures'

vi.mock('../ws', () => import('../test/wsMock'))

const get = () => useSessionStore.getState()

const TAKING_LONGER_MSG = "This is taking longer than expected — it may have failed to start. Try again or check the sidebar."

describe('useSessionStore', () => {
  it('removes desktop source fields on reassignment and clears all lists on identity change', () => {
    useSessionStore.setState({ daemons: [makeDaemon({ id: 'desktop-a' })], sessions: [makeSession({ daemon_id: 'desktop-a', project_path: '/private-a', repo: 'private-a' })] })
    wsMock.emit(ownershipEvent as import('../types').DesktopOwnershipChanged)
    expect(get().daemons).toHaveLength(0)
    expect(get().sessions[0].project_path).toBe('')
    expect(get().sessions[0].repo).toBe('')
    useTranscriptStore.getState().ingest({type:'agent_event',session_id:'cached',client_event_id:'caps',seq:1,ts:'2026-01-01T00:00:00Z',kind:'capabilities',payload:{private:'old owner'}})
    window.dispatchEvent(new Event('blerg-identity-changed'))
    expect(get().sessions).toHaveLength(0)
    expect(useTranscriptStore.getState().sessions).toEqual({})
  })

  it('clears already delivered source and error state on a remote-replica invalidation', () => {
    useSessionStore.setState({daemons:[makeDaemon({id:'desktop-a'})],sessions:[makeSession({daemon_id:'desktop-a',repo:'secret',project_path:'/secret',error_reason:'private failure'})]})
    wsMock.emit({type:'desktop_inventory_invalidated'})
    expect(get().daemons).toHaveLength(0)
    expect(get().sessions[0]).toMatchObject({daemon_id:'',repo:'',project_path:''})
    expect(get().sessions[0].error_reason).toBeUndefined()
  })

  beforeEach(() => {
    localStorage.clear()
    vi.clearAllTimers()
    useSessionStore.setState({ daemons: [], sessions: [], statusChangedAt: {}, serverVersion: '', pendingSessionIds: {}, failedSessions: {} })
    useToastStore.setState({ toasts: [] })
  })

  it('marks an archived child without deleting its direct-URL/history record',()=>{
    const child=makeSession({id:'child',parent_session_id:'parent'})
    useSessionStore.setState({sessions:[child]})
    wsMock.emit({type:'session_archived',session_id:'child',parent_session_id:'parent',archived_at:'2026-10-19T12:00:00Z'})
    expect(get().sessions).toHaveLength(1)
    expect(get().sessions[0]).toMatchObject({...child,archived_at:'2026-10-19T12:00:00Z'})
    wsMock.emit({type:'session_state_changed',session_id:'child',status:'ended',unread:false})
    expect(get().sessions[0]?.archived_at).toBe('2026-10-19T12:00:00Z')
  })
  it('applies initial_state from the server', () => {
    wsMock.emit({
      type: 'initial_state',
      daemons: [makeDaemon()],
      sessions: [makeSession()],
      server_version: '2.1.0',
      server_release: '0.2.0',
      server_commit: '510d0c0',
    })
    expect(get().daemons).toHaveLength(1)
    expect(get().sessions).toHaveLength(1)
    expect(get().serverVersion).toBe('2.1.0')
    expect(get().serverRelease).toBe('0.2.0')
    expect(get().serverCommit).toBe('510d0c0')
    expect(get().maxFileBytes).toBeNull()
  })

  it('keeps the per-file limit the server advertises', () => {
    wsMock.emit({ type: 'initial_state', daemons: [], sessions: [], limits: { max_file_bytes: 104857600, max_session_upload_bytes: 524288000 } })
    expect(get().maxFileBytes).toBe(104857600)
    wsMock.emit({ type: 'initial_state', daemons: [], sessions: [], limits: { max_file_bytes: 0 } })
    expect(get().maxFileBytes).toBeNull()
  })

  it('updates a session status on session_state_changed', () => {
    wsMock.emit({ type: 'initial_state', daemons: [], sessions: [makeSession({ status: 'running' })] })
    wsMock.emit({ type: 'session_state_changed', session_id: 'sess-a', status: 'waiting', unread: false })
    expect(get().sessions[0].status).toBe('waiting')
  })

  it('shows the server notice as a toast, as given (regardless of activity)', () => {
    wsMock.emit({ type: 'initial_state', daemons: [], sessions: [makeSession({ status: 'running' })] })
    wsMock.emit({
      type: 'session_notice', session_id: 'sess-a', status: 'idle',
      title: 'Session idle', body: 'Build the widget is waiting for you.', url: '/sessions/sess-a',
    })
    const toasts = useToastStore.getState().toasts
    expect(toasts).toHaveLength(1)
    expect(toasts[0].title).toBe('Session idle')
    expect(toasts[0].body).toBe('Build the widget is waiting for you.')
    expect(toasts[0].url).toBe('/sessions/sess-a')
  })

  it('does not toast a state change by itself: the server says when the person is needed', () => {
    // A turn can end while background work runs; the server sends session_notice once it is done.
    wsMock.emit({ type: 'initial_state', daemons: [], sessions: [makeSession({ status: 'running' })] })
    wsMock.emit({ type: 'session_state_changed', session_id: 'sess-a', status: 'idle', unread: true })
    wsMock.emit({ type: 'session_state_changed', session_id: 'sess-a', status: 'running', unread: true })
    wsMock.emit({ type: 'session_state_changed', session_id: 'sess-a', status: 'waiting', unread: true })
    expect(get().sessions[0].status).toBe('waiting')
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

  it('shows a session whose runtime stopped as failed, with the reason', () => {
    wsMock.emit({ type: 'initial_state', daemons: [], sessions: [makeSession({ status: 'idle' })] })
    wsMock.emit({ type: 'session_ended', session_id: 'sess-a', exit_code: 1, message: 'Codex app-server repeatedly failed', end_reason: 'process_failed' })
    expect(get().sessions[0].status).toBe('error')
    expect(get().sessions[0].error_reason).toBe('Codex app-server repeatedly failed')
    expect(get().sessions[0].end_reason).toBe('process_failed')
  })

  it('follows a session\'s background-task count, and an end clears it', () => {
    wsMock.emit({ type: 'initial_state', daemons: [], sessions: [makeSession({ status: 'idle', background_tasks: 1 }), makeSession({ id: 'sess-b' })] })
    wsMock.emit({ type: 'session_background_changed', session_id: 'sess-a', background_tasks: 2 })
    expect(get().sessions.map(s => s.background_tasks)).toEqual([2, undefined])
    wsMock.emit({ type: 'session_state_changed', session_id: 'sess-a', status: 'running', unread: false })
    expect(get().sessions[0].background_tasks).toBe(2)
    wsMock.emit({ type: 'session_state_changed', session_id: 'sess-a', status: 'error', unread: false })
    expect(get().sessions[0].background_tasks).toBe(0)
    wsMock.emit({ type: 'session_background_changed', session_id: 'sess-a', background_tasks: 1 })
    wsMock.emit({ type: 'session_ended', session_id: 'sess-a', exit_code: 0 })
    expect(get().sessions[0].background_tasks).toBe(0)
  })

  it('follows a session paused by a restart until it runs again', () => {
    wsMock.emit({ type: 'initial_state', daemons: [], sessions: [makeSession({ status: 'idle' }), makeSession({ id: 'sess-b' })] })
    wsMock.emit({ type: 'session_paused_changed', session_id: 'sess-a', paused_reason: 'restart' })
    expect(get().sessions.map(s => s.paused_reason)).toEqual(['restart', undefined])
    wsMock.emit({ type: 'session_state_changed', session_id: 'sess-a', status: 'waiting', unread: false })
    expect(get().sessions[0].paused_reason).toBe('restart')
    wsMock.emit({ type: 'session_state_changed', session_id: 'sess-a', status: 'running', unread: false })
    expect(get().sessions[0].paused_reason).toBeUndefined()
    wsMock.emit({ type: 'session_paused_changed', session_id: 'sess-a', paused_reason: 'restart' })
    // A message accepted for it (from any tab) ends the pause everywhere.
    wsMock.emit({ type: 'session_paused_changed', session_id: 'sess-a', paused_reason: '' })
    expect(get().sessions[0].paused_reason).toBeUndefined()
    wsMock.emit({ type: 'session_paused_changed', session_id: 'sess-a', paused_reason: 'restart' })
    get().clearPaused('sess-a')
    expect(get().sessions[0].paused_reason).toBeUndefined()
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

  it('takes a daemon\'s usage limits from initial_state and replaces them on daemon_limits_changed', () => {
    const codex = { provider: 'codex', windows: [{ id: '5h', used_percent: 42, resets_at: '2026-10-16T14:00:00Z' }], fetched_at: '2026-10-16T12:00:00Z' }
    wsMock.emit({ type: 'initial_state', daemons: [makeDaemon({ usage_limits: [codex] }), makeDaemon({ id: 'd2' })], sessions: [] })
    expect(get().daemons[0].usage_limits).toEqual([codex])
    const claude = { provider: 'claude', windows: [{ id: 'capped', used_percent: 100, resets_at: '2026-10-16T15:00:00Z' }], fetched_at: '2026-10-16T12:05:00Z' }
    wsMock.emit({ type: 'daemon_limits_changed', daemon_id: 'd1', usage_limits: [claude, codex] })
    expect(get().daemons[0].usage_limits).toEqual([claude, codex])
    expect(get().daemons[1].usage_limits).toBeUndefined()
    wsMock.emit({ type: 'daemon_limits_changed', daemon_id: 'd1', usage_limits: [] })
    expect(get().daemons[0].usage_limits).toEqual([])
    // A daemon the page does not list is not invented.
    wsMock.emit({ type: 'daemon_limits_changed', daemon_id: 'gone', usage_limits: [codex] })
    expect(get().daemons).toHaveLength(2)
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
