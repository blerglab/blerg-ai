// The agent-reopen rule against the vectors blerg-runner (Python) and the runner (Go) test too.
import {describe, expect, it} from 'vitest'
import vectors from './testdata/agent-reopen-vectors.json'
import {agentReopenApplies, parseReviewFile, validReopenText, validRevision, type ReviewRequest} from './review'

const thread = (fields: Record<string, unknown>): ReviewRequest | undefined =>
  parseReviewFile(JSON.stringify({version: 1, file: {name: 'x.diff', artifactId: 'a'},
    requests: [{id: 't', anchor: {kind: 'general'}, text: 'Ask', createdAt: '2026-10-21T10:00:00Z', ...fields}]}))?.requests[0]

describe('the shared agent-reopen rule', () => {
  it.each(vectors.text.map(v => [JSON.stringify(v.text).slice(0, 30), v] as const))('text %s', (_name, v) => {
    expect(validReopenText(v.text.repeat(v.repeat ?? 1))).toBe(v.valid)
  })
  it.each(vectors.revision.map(v => [v.json, v] as const))('revision %s', (_name, v) => {
    expect(validRevision(JSON.parse(v.json))).toBe(v.valid)
  })
  it.each(vectors.applies.map(v => [v.name, v] as const))('applies: %s', (_name, v) => {
    const person = thread(v.person)!
    expect(person).toBeDefined()
    // A copy past the revision cap is not a review file at all: nothing of it applies.
    const agent = thread(v.agent)
    expect(agent ? agentReopenApplies(person, agent) : false).toBe(v.applies)
  })
  it.each(vectors.parse.map(v => [v.name, v] as const))('parse: %s', (_name, v) => {
    expect(thread(v.request) !== undefined).toBe(v.parses)
  })
})
