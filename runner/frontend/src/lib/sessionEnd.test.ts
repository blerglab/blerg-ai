import { describe, it, expect } from 'vitest'
import { describeSessionEnd, END_REASON } from './sessionEnd'
import type { SessionInfo } from '../types'

type S = Pick<SessionInfo, 'status' | 'end_reason' | 'ended_by' | 'error_reason'>

function line(s: Partial<S>) {
  return describeSessionEnd({ status: 'stopped', ...s } as S)
}

describe('describeSessionEnd', () => {
  it.each([
    ['stopped_by_user', { kind: 'human', self: true }, 'Ended by you'],
    ['stopped_by_user', { kind: 'human', self: false }, 'Ended by another signed-in user'],
    ['stopped_by_user', { kind: 'human' }, 'Ended by another signed-in user'],
    ['stopped_by_agent', { kind: 'agent', self: true }, 'Ended by one of your agent tokens'],
    ['stopped_by_agent', { kind: 'agent' }, 'Ended by an agent token'],
    ['stopped_by_agent', { kind: 'service' }, 'Ended by a service'],
    ['stopped_by_agent', { kind: 'runner_key' }, 'Ended by an automated caller (runner key)'],
    ['stopped_by_agent', null, 'Stopped on request'],
    ['auto_stopped', null, 'Stopped automatically when its task finished'],
    ['process_exited', null, 'The process exited on its own'],
    ['process_failed', null, 'The process exited with an error'],
    ['start_failed', null, 'The session failed to start'],
    ['job_finished', null, 'The cluster job finished'],
    ['job_failed', null, 'The cluster job failed'],
    ['job_disappeared', null, 'The cluster job was removed outside Blerg'],
    ['not_resumed', null, 'Expired: nobody resumed it within a day'],
    ['daemon_lost', null, 'Its daemon disconnected and never came back'],
    ['daemon_unreported', null, 'Its daemon no longer had this session'],
  ] as const)('%s by %j → %s', (reason, by, want) => {
    expect(line({ end_reason: reason, ended_by: by })?.text).toBe(want)
  })

  it('covers every reason in the table', () => {
    // A new server reason needs a table entry; this keeps the test list honest.
    expect(Object.keys(END_REASON).sort()).toEqual([
      'auto_stopped', 'daemon_lost', 'daemon_unreported', 'job_disappeared', 'job_failed',
      'job_finished', 'not_resumed', 'process_exited', 'process_failed', 'start_failed',
      'stopped_by_agent', 'stopped_by_user',
    ])
  })

  it('says nothing for a session that ended before reasons were recorded', () => {
    expect(line({ end_reason: undefined, ended_by: undefined })).toBeNull()
    expect(line({ end_reason: '', ended_by: null })).toBeNull()
  })

  it('says nothing for a live session, even with a reason on it', () => {
    for (const status of ['running', 'idle', 'waiting', 'starting', 'disconnected'] as const) {
      expect(line({ status, end_reason: 'stopped_by_user', ended_by: { kind: 'human', self: true } })).toBeNull()
    }
  })

  it('treats the server-only "ended" status as terminal', () => {
    expect(line({ status: 'ended' as SessionInfo['status'], end_reason: 'auto_stopped' })?.text)
      .toBe('Stopped automatically when its task finished')
  })

  it('renders an unknown future reason as a plain "Ended"', () => {
    expect(line({ end_reason: 'something_new' })).toEqual({ text: 'Ended', coveredByError: false })
  })

  it('never resolves a code through the prototype', () => {
    for (const key of ['constructor', 'toString', '__proto__', 'hasOwnProperty']) {
      expect(line({ end_reason: key })).toEqual({ text: 'Ended', coveredByError: false })
    }
  })

  it('leaves a failure to the error banner when it has text', () => {
    expect(line({ status: 'error', end_reason: 'job_failed', error_reason: 'cluster job failed: DeadlineExceeded' })?.coveredByError).toBe(true)
    expect(line({ status: 'error', end_reason: 'job_failed', error_reason: '' })?.coveredByError).toBe(false)
    // A stop is not a failure: shown even beside an error banner.
    expect(line({ status: 'error', end_reason: 'stopped_by_user', ended_by: { kind: 'human', self: true }, error_reason: 'x' })?.coveredByError).toBe(false)
  })
})
