import { describe, expect, it } from 'vitest'
import { isAutomated, startedByLabel } from './startedBy'
import { makeSession } from '../test/fixtures'
describe('started session attribution', () => {
  it('labels a named parent before generic source-name handling', () => {
    const child = makeSession({ started_by: { kind: 'session', name: 'Parent' }, parent_session_id: 'parent' })
    // "<who> from <where>": the parent's agent, from the parent session (never "started by from …").
    expect(startedByLabel(child)).toBe('an agent from Parent')
    expect(startedByLabel(child, makeSession({ engine: 'codex' }))).toBe('Codex agent from Parent')
    expect(startedByLabel(child, makeSession({ engine: '' }))).toBe('Claude agent from Parent')
    expect(startedByLabel(makeSession({ started_by: { kind: 'session' } }))).toBe('an agent from another session')
    expect(isAutomated(child, new Set(['parent']))).toBe(false)
    expect(isAutomated(child, new Set())).toBe(true)
  })
})
