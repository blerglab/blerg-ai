import { expect, it } from 'vitest'
import { clearViews } from './sessionViewCache'
import { revealSessionFile, sessionFileRevealRequest, sessionFileRevealVersion } from './openSessionFile'

it('forgets pending reveal paths when the account or workspace view cache is cleared', () => {
  revealSessionFile({ account: 'person-a', session: 'session', path: 'dev/private.txt' })
  expect(sessionFileRevealRequest('person-a', 'session')?.path).toBe('dev/private.txt')
  clearViews()
  expect(sessionFileRevealRequest('person-a', 'session')).toBeUndefined()
  expect(sessionFileRevealVersion('person-a', 'session')).toBe(0)
})
