import { describe, expect, it } from 'vitest'
import { isAutomated, startedByLabel } from './startedBy'
import { makeSession } from '../test/fixtures'
describe('started session attribution', () => {
  it('labels a named parent before generic source-name handling', () => {
    const child = makeSession({ started_by: { kind: 'session', name: 'Parent' }, parent_session_id: 'parent' })
    expect(startedByLabel(child)).toBe('from Parent')
    expect(isAutomated(child, new Set(['parent']))).toBe(false)
    expect(isAutomated(child, new Set())).toBe(true)
  })
})
