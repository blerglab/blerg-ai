import { describe, expect, it } from 'vitest'
import { answeredInRound, reviewRounds, reviewedFileName } from './reviewRounds'
import type { ReviewFile, ReviewRequest } from './review'
import type { AgentEvent } from '../types'

let n = 0
function ev(kind: string, payload: unknown): AgentEvent {
  n++
  return { type: 'agent_event', session_id: 's1', client_event_id: `e${n}`, ts: '2026-07-26T00:00:00Z', kind, payload } as AgentEvent
}
const publish = (name: string, version: number) =>
  ev('artifact', { id: `${name}-v${version}`, name, size: 10, content_type: 'application/json', view: 'json', origin: 'agent', version })
const says = (text: string) => ev('user_message', { text, source: 'human' })

describe('reviewRounds', () => {
  it('names the reviewed file of a review-state file only', () => {
    expect(reviewedFileName('plan.md.review.json')).toBe('plan.md')
    expect(reviewedFileName('plan.md')).toBeNull()
    expect(reviewedFileName('.review.json')).toBeNull()
  })

  it('folds a round of publishes into its newest, and a message of the person starts the next round', () => {
    const a = [publish('plan.md.review.json', 1), publish('plan.md.review.json', 2), publish('plan.md.review.json', 3)]
    const doc = publish('plan.md', 2)
    const msg = says('another round')
    const b = [publish('plan.md.review.json', 4), publish('plan.md.review.json', 5)]
    const { hidden, rounds } = reviewRounds([...a, doc, msg, ...b])
    expect([...hidden]).toEqual([a[0], a[1], b[0]].map(e => e.client_event_id))
    expect([...rounds.keys()]).toEqual([a[2].client_event_id, b[1].client_event_id])
    const first = rounds.get(a[2].client_event_id)!
    expect(first).toMatchObject({ file: 'plan.md', publishes: 3, first: true })
    expect(first.latest.version).toBe(3)
    const second = rounds.get(b[1].client_event_id)!
    expect(second).toMatchObject({ file: 'plan.md', publishes: 2, first: false })
    expect(second.baseline?.version).toBe(3)
  })

  it('keeps each file its own round, and leaves every other event alone', () => {
    const x = publish('a.md.review.json', 1)
    const y = publish('b.md.review.json', 1)
    const doc = publish('a.md', 1)
    const { hidden, rounds } = reviewRounds([x, doc, y])
    expect(hidden.size).toBe(0)
    expect(rounds.size).toBe(2)
    expect(rounds.has(doc.client_event_id)).toBe(false)
  })

  it('a round whose earlier publish is on a page not loaded has no baseline and is not a first', () => {
    const { rounds } = reviewRounds([publish('plan.md.review.json', 6)])
    expect([...rounds.values()][0]).toMatchObject({ first: false, baseline: undefined })
  })
})

const req = (id: string, status: ReviewRequest['status'], reply?: string): ReviewRequest =>
  ({ id, anchor: { kind: 'general' }, text: 'x', status, createdAt: '2026-07-26T00:00:00Z', ...(reply ? { reply } : {}) })
const review = (...requests: ReviewRequest[]): ReviewFile => ({ version: 1, file: { name: 'plan.md', artifactId: 'p' }, requests })

describe('answeredInRound', () => {
  it('counts the requests the round settled, not those settled before it', () => {
    const before = review(req('a', 'done', 'ok'), req('b', 'open'), req('c', 'open'))
    const after = review(req('a', 'done', 'ok'), req('b', 'declined', 'no'), req('c', 'done', 'yes'), req('d', 'open'))
    expect(answeredInRound(after, before)).toBe(2)
    expect(answeredInRound(after, null)).toBe(3)
  })

  it('counts a request answered again with a new line', () => {
    expect(answeredInRound(review(req('a', 'done', 'second')), review(req('a', 'done', 'first')))).toBe(1)
  })
})
