import {describe, expect, it} from 'vitest'
import {parsePatch} from './patch'
import {reviewTimeline, submittedRounds, threadExcerpt, threadFreshness} from './reviewConversation'
import type {ReviewFile, ReviewRequest} from './review'
import type {ArtifactInfo} from '../transport/types'

const PATCH = 'diff --git a/a.ts b/a.ts\n--- a/a.ts\n+++ b/a.ts\n@@ -1,3 +1,3 @@\n keep\n-old\n+new\n tail\n'
const thread = (id: string, extra: Partial<ReviewRequest> = {}): ReviewRequest => ({id, text: id, status: 'open', createdAt: '2026-10-21T10:00:00Z',
  anchor: {kind: 'text', quote: 'new', diff: {artifactId: 'v1', file: 'a.ts', side: 'new', start: 2, end: 2, hunk: 1, fileIndex: 1}}, ...extra})
const upload = (id: string, version: number): ArtifactInfo => ({id, name: 'x.diff.review.json', size: 1, view: 'json', content_type: 'application/json', version, origin: 'user'})
const file = (requests: ReviewRequest[], submittedAt: string): ReviewFile => ({version: 1, file: {name: 'x.diff', artifactId: 'v1'}, requests, submittedAt})

describe('review conversation model', () => {
  it('finds which round started and which reopened each thread', () => {
    const rounds = submittedRounds([
      {artifact: upload('r2', 2), review: file([thread('a', {comments: [{text: 'again', createdAt: '2026-10-21T12:00:00Z', revision: 1}], revision: 1}), thread('b')], '2026-10-21T12:00:00Z')},
      {artifact: upload('r1', 1), review: file([thread('a')], '2026-10-21T11:00:00Z')},
    ])
    expect(rounds.map(r => [r.id, r.started, r.reopened])).toEqual([['r1', ['a'], []], ['r2', ['b'], ['a']]])
  })
  it("records the person's replies, resolves and rejects per round, with when", () => {
    const at = (h: number) => `2026-10-21T${h}:00:00Z`
    const rounds = submittedRounds([
      {artifact: upload('r1', 1), review: file([thread('a'), thread('b')], at(11))},
      {artifact: upload('r2', 2), review: file([
        thread('a', {revision: 1, comments: [{text: 'Why?', createdAt: at(12), revision: 1, action: 'reply'}]}),
        thread('b', {status: 'done', settledBy: 'person', revision: 1, comments: [{text: '', createdAt: at(12), revision: 1, action: 'resolve'}]}),
      ], at(12))},
    ])
    expect(rounds[1]).toMatchObject({started: [], reopened: [], actions: [{id: 'a', action: 'reply', at: at(12)}, {id: 'b', action: 'resolve', at: at(12)}]})
  })
  it('draws an excerpt from the quote alone when its patch is not at hand, and none for a general comment', () => {
    const excerpt = threadExcerpt(null, thread('a').anchor)!
    expect(excerpt).toMatchObject({file: 'a.ts', start: 2, exact: false, rows: [{text: 'new', new: 2, marked: true}]})
    expect(threadExcerpt(parsePatch(PATCH), {kind: 'general'})).toBeNull()
    expect(threadExcerpt(parsePatch(PATCH), thread('a').anchor)!.rows.map(r => r.text)).toEqual(['keep', 'old', 'new', 'tail'])
  })
  it('says Outdated only when the newest version changed the code of a file it still has', () => {
    const anchor = thread('a').anchor
    expect(threadFreshness(anchor, {id: 'v2', patch: parsePatch(PATCH)})).toBe('current')
    expect(threadFreshness(anchor, {id: 'v2', patch: parsePatch(PATCH.replace('+new', '+newer'))})).toBe('outdated')
    expect(threadFreshness(anchor, {id: 'v2', patch: parsePatch(PATCH.replace(/a\.ts/g, 'other.ts'))})).toBe('absent')
    expect(threadFreshness(anchor, null)).toBeNull()
    expect(threadFreshness({kind: 'general'}, {id: 'v2', patch: parsePatch(PATCH)})).toBeNull()
  })
  it('puts entries without a time last, in order, and keeps threads of a round inside it', () => {
    const entries = reviewTimeline({
      revisions: [{artifact: {id: 'v1', name: 'x.diff', size: 1, view: 'diff', content_type: '', created_at: '2026-10-21T09:00:00Z'}, commits: []}],
      rounds: [{id: 'r1', at: '2026-10-21T11:00:00Z', started: ['a'], reopened: [], actions: []}],
      threads: [thread('a'), thread('local', {createdAt: 'not a time'})],
      requests: [],
    })
    expect(entries.map(e => e.key)).toEqual(['revision:v1', 'round:r1', 'thread:local'])
  })
})
