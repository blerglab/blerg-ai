import { expect, it } from 'vitest'
import { makeSession } from '../test/fixtures'
import { descendantsOf, sessionNoun, suppressWorkerUnread, descendantSummary, hasUnreadChannels } from './sessionPresentation'
const root = makeSession({ id: 'root', started_by: { kind: 'agent' } })
const channel = makeSession({ id: 'channel', parent_session_id: 'root', unread: true })
const worker = makeSession({ id: 'worker', parent_session_id: 'channel', started_by: { kind: 'session' }, unread: true })
it('distinguishes workspace containers, channels and delegated workers', () => {
  expect(sessionNoun(root)).toBe('workspace')
  expect(sessionNoun(channel)).toBe('channel')
  expect(sessionNoun(worker)).toBe('worker')
  expect(suppressWorkerUnread(root, [root])).toBe(false)
  expect(suppressWorkerUnread(worker, [worker])).toBe(false)
  expect(suppressWorkerUnread(worker, [root, channel, worker])).toBe(true)
})
it('counts nested workers and channels without depending on the search tree', () => {
  const sessions = [root, channel, worker, makeSession({ id: 'nested', parent_session_id: 'worker', started_by: { kind: 'session' } })]
  expect(descendantsOf('root', sessions)).toHaveLength(3)
  expect(descendantSummary(descendantsOf('root', sessions))).toBe('1 channel, 2 workers')
  expect(hasUnreadChannels('root', sessions)).toBe(true)
  expect(hasUnreadChannels('root', [root, worker])).toBe(false)
})
