import { expect, it } from 'vitest'
import { sessionRole } from './sessionRole'
import { makeSession } from '../test/fixtures'
it('uses origin metadata rather than whether a chat was opened', () => {
  expect(sessionRole(makeSession({ parent_session_id: undefined }))).toBe('workspace')
  expect(sessionRole(makeSession({ parent_session_id: 'main' }))).toBe('channel')
  expect(sessionRole(makeSession({ parent_session_id: 'main', started_by: { kind: 'session' } }))).toBe('worker')
  expect(sessionRole(makeSession({ started_by: { kind: 'agent' } }))).toBe('worker')
  expect(sessionRole(makeSession({ cron_id: 'scheduled' }))).toBe('run')
})
