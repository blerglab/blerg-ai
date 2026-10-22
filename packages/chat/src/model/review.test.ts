import { describe, expect, it } from 'vitest'
import { emptyReview, mergeReviews, reopenReviewRequest, replyToReviewRequest, settleReviewRequest, reviewReplyHistory, newRequestId, parseReviewFile, reviewFileName, type ReviewFile, type ReviewRequest } from './review'

const req = (over: Partial<ReviewRequest> = {}): ReviewRequest => ({
  id: 'abc12345',
  anchor: { kind: 'text', quote: 'the mean', heading: 'Results', occurrence: 1 },
  text: 'median, not mean',
  status: 'open',
  createdAt: '2026-10-06T10:00:00Z',
  ...over,
})

const file = (over: Partial<ReviewFile> = {}): ReviewFile => ({
  version: 1,
  file: { name: 'report.md', artifactId: 'a1', artifactVersion: 3 },
  requests: [req()],
  ...over,
})

describe('review ids and names', () => {
  it('makes 8 base36 chars, different each time', () => {
    const a = newRequestId()
    const b = newRequestId()
    expect(a).toMatch(/^[0-9a-z]{8}$/)
    expect(b).toMatch(/^[0-9a-z]{8}$/)
    expect(a).not.toBe(b)
  })

  it('names the review file beside the file', () => {
    expect(reviewFileName('report.md')).toBe('report.md.review.json')
  })

  it('starts an empty review', () => {
    expect(emptyReview({ name: 'r.md', artifactId: 'a1' })).toEqual({ version: 1, file: { name: 'r.md', artifactId: 'a1' }, requests: [] })
  })
})

describe('parseReviewFile', () => {
  it('accepts a well-formed file, keeping edit and submittedAt', () => {
    const f = file({ edit: { artifactId: 'a2', diff: '@@ -1 +1 @@\n-a\n+b\n' }, submittedAt: '2026-10-06T10:01:00Z' })
    expect(parseReviewFile(JSON.stringify(f))).toEqual(f)
  })

  it('accepts a request with a reply and a region anchor', () => {
    const f = file({ requests: [req({ status: 'done', reply: 'did it', anchor: { kind: 'region', rect: { x: 0.1, y: 0.2, w: 0.3, h: 0.4 }, pin: 1 } })] })
    expect(parseReviewFile(JSON.stringify(f))).toEqual(f)
  })

  it('rejects what is not a version-1 review', () => {
    expect(parseReviewFile('not json')).toBeNull()
    expect(parseReviewFile('null')).toBeNull()
    expect(parseReviewFile('[]')).toBeNull()
    expect(parseReviewFile(JSON.stringify({ ...file(), version: 2 }))).toBeNull()
    expect(parseReviewFile(JSON.stringify({ ...file(), file: { artifactId: 'a1' } }))).toBeNull()
    expect(parseReviewFile(JSON.stringify({ ...file(), requests: 'none' }))).toBeNull()
  })

  it('rejects a malformed request', () => {
    const bad = (r: unknown) => parseReviewFile(JSON.stringify({ ...file(), requests: [r] }))
    expect(bad({ ...req(), id: 7 })).toBeNull()
    expect(bad({ ...req(), status: 'maybe' })).toBeNull()
    expect(bad({ ...req(), anchor: { kind: 'blob' } })).toBeNull()
    expect(bad({ ...req(), anchor: null })).toBeNull()
    expect(bad({ ...req(), text: null })).toBeNull()
    expect(bad({ ...req(), createdAt: 5 })).toBeNull()
    expect(bad({ ...req(), reply: 5 })).toBeNull()
    expect(bad('x')).toBeNull()
  })

  it('rejects a malformed edit', () => {
    expect(parseReviewFile(JSON.stringify({ ...file(), edit: { diff: 'x' } }))).toBeNull()
    expect(parseReviewFile(JSON.stringify({ ...file(), edit: 'x' }))).toBeNull()
  })

  it('drops unknown fields rather than keeping them', () => {
    const parsed = parseReviewFile(JSON.stringify({ ...file(), extra: 1, requests: [{ ...req(), extra: 2 }] }))
    expect(parsed).toEqual(file())
  })
})

describe('mergeReviews', () => {
  it('passes a lone side through, and null for none', () => {
    expect(mergeReviews(null, null)).toBeNull()
    expect(mergeReviews(file(), null)).toEqual(file())
    expect(mergeReviews(null, file())).toEqual(file())
  })

  it('keeps the person’s base and takes status and reply from the agent per id', () => {
    const person = file({
      requests: [req({ id: 'one', text: 'first' }), req({ id: 'two', text: 'second' })],
      edit: { artifactId: 'a2', diff: 'd' },
      submittedAt: '2026-10-06T10:01:00Z',
    })
    const agent = file({
      file: { name: 'report.md', artifactId: 'a9' },
      requests: [
        req({ id: 'two', text: 'the agent’s copy of two', status: 'declined', reply: 'no' }),
        req({ id: 'one', text: 'the agent’s copy of one', status: 'done', reply: 'yes' }),
      ],
    })
    const merged = mergeReviews(person, agent)!
    expect(merged.file).toEqual(person.file)
    expect(merged.edit).toEqual(person.edit)
    expect(merged.submittedAt).toBe(person.submittedAt)
    expect(merged.requests.map(r => [r.id, r.text, r.status, r.reply])).toEqual([
      ['one', 'first', 'done', 'yes'],
      ['two', 'second', 'declined', 'no'],
    ])
  })

  it('drops requests only the agent’s file has (every request is the person’s), and leaves unanswered ones open', () => {
    const person = file({ requests: [req({ id: 'one' })] })
    const agent = file({ requests: [req({ id: 'extra', status: 'done', reply: 'added' })] })
    const merged = mergeReviews(person, agent)!
    expect(merged.requests.map(r => [r.id, r.status])).toEqual([['one', 'open']])
  })

  it('does not change its inputs', () => {
    const person = file({ requests: [req({ id: 'one' })] })
    const agent = file({ requests: [req({ id: 'one', status: 'done', reply: 'ok' })] })
    mergeReviews(person, agent)
    expect(person.requests[0].status).toBe('open')
    expect(person.requests[0].reply).toBeUndefined()
  })
})

describe('document anchor provenance', () => {
  const data = {version: 1, file: {name: 'document.md', artifactId: 'current'}, requests: [{id: 'comment', text: 'Keep this on its original passage', status: 'open', createdAt: '2026-01-01T00:00:00Z', anchor: {kind: 'text', quote: 'A passage', sourceArtifactId: 'original', unavailable: true}}]}
  it('roundtrips a source artifact and durable unavailable marker', () => {
    expect(parseReviewFile(JSON.stringify(data))?.requests[0].anchor).toEqual(data.requests[0].anchor)
  })
  it.each([{sourceArtifactId: '\nsecret'}, {sourceArtifactId: 42}, {unavailable: 'true'}, {kind: 'general'}, {sourceArtifactId: 'x'.repeat(201)}])('rejects malformed document provenance %j', overrides => {
    const bad = {...data, requests: [{...data.requests[0], anchor: {...data.requests[0].anchor, ...overrides}}]}
    expect(parseReviewFile(JSON.stringify(bad))).toBeNull()
  })
})


describe('reopenReviewRequest', () => {
  it('keeps provenance and previous answers immutable while advancing the revision', () => {
    const before = req({status: 'declined', reply: 'Original passage unavailable', revision: 2,
      anchor: {kind: 'text', quote: 'Original', sourceArtifactId: 'old', unavailable: true}})
    const reopened = reopenReviewRequest(before, 'Please reconsider')
    expect(reopened).toEqual({...before, status: 'open', revision: 3,
      comments: [{text: 'Please reconsider', createdAt: expect.any(String), revision: 3, action: 'reopen'}],
      replies: [{text: before.reply, status: 'declined', createdAt: before.createdAt, revision: 2, timeUnknown: true}]})
    expect(before.status).toBe('declined')
    expect(before.replies).toBeUndefined()
    expect(reopened.anchor).toEqual(before.anchor)
  })

  it('does not duplicate archived replies or exceed the bounded history', () => {
    const reply = {text: 'Fixed', status: 'done' as const, createdAt: '2026-10-06T10:00:00Z', revision: 4}
    const before = req({status: 'done', revision: 4, reply: 'Fixed', replies: [reply]})
    expect(reopenReviewRequest(before, 'Please reconsider').replies).toEqual([reply])
    const history = Array.from({length: 100}, (_, revision) => ({...reply, revision}))
    const full = req({status: 'done', revision: 100, reply: 'Newest', replies: history})
    const reopened = reopenReviewRequest(full, 'Please reconsider')
    expect(reopened.replies).toHaveLength(100)
    expect(reopened.replies?.[0].revision).toBe(1)
    expect(reopened.replies?.at(-1)?.text).toBe('Newest')
    expect(full.replies).toHaveLength(100)
  })

  it('leaves open threads and exhausted revisions alone', () => {
    const open = req()
    const exhausted = req({status: 'done', revision: 1_000_000})
    expect(reopenReviewRequest(open, 'Please reconsider')).toBe(open)
    expect(reopenReviewRequest(exhausted, 'Please reconsider')).toBe(exhausted)
  })
})


describe('reviewReplyHistory', () => {
  it('shows a current legacy answer alongside previous revision answers without mutation', () => {
    const old = {text: 'Earlier answer', status: 'done' as const, createdAt: '2026-10-06T10:00:00Z', revision: 0}
    const request = req({status: 'declined', revision: 1, reply: 'Latest legacy answer', replies: [old]})
    expect(reviewReplyHistory(request)).toEqual([old, {text: 'Latest legacy answer', status: 'declined', createdAt: request.createdAt, revision: 1, timeUnknown: true}])
    expect(request.replies).toEqual([old])
    expect(reviewReplyHistory(reopenReviewRequest(request, 'Please reconsider'))).toEqual(reviewReplyHistory(request))
  })

  it('does not repeat an archived current answer', () => {
    const reply = {text: 'Fixed', status: 'done' as const, createdAt: '2026-10-06T10:00:00Z', revision: 1}
    expect(reviewReplyHistory(req({status: 'done', reply: 'Fixed', revision: 1, replies: [reply]}))).toEqual([reply])
  })
})


describe('reopen explanations', () => {
  it('requires a bounded nonblank explanation before changing any thread state', () => {
    const request = req({status: 'done'})
    for (const explanation of ['', '  \n ', 'x'.repeat(32769)]) expect(reopenReviewRequest(request, explanation)).toBe(request)
    const reopened = reopenReviewRequest(request, '  Still incorrect  ')
    expect(reopened.text).toBe(request.text)
    expect(reopened.comments?.[0]).toMatchObject({text: 'Still incorrect', revision: 1})
    expect(parseReviewFile(JSON.stringify(file({requests: [reopened]})))?.requests[0]).toEqual(reopened)
    const answered = {...reopened, status: 'done' as const, reply: 'Fixed again'}
    expect(mergeReviews(file({requests: [reopened]}), file({requests: [answered]}))?.requests[0].comments).toEqual(reopened.comments)
  })

  it.each([null, [{text: ' ', createdAt: '2026-01-01', revision: 1}], [{text: 'why', createdAt: 'bad', revision: 1}], [{text: 'why', createdAt: '2026-01-01', revision: 3}], [{text: 'why', createdAt: '2026-01-01', revision: 0}], Array(101).fill({text: 'why', createdAt: '2026-01-01', revision: 1})])('rejects malformed explanation history %j', comments => {
    expect(parseReviewFile(JSON.stringify(file({requests: [{...req(), revision: 1, comments} as unknown as ReviewRequest]})))).toBeNull()
  })
})


it("never takes the person's comments from the agent's copy, only from the person's own file", () => {
  const comment = {text: 'Still broken', createdAt: '2026-01-02T00:00:00Z', revision: 1}
  const original = req({status: 'done'})
  const newer = {...original, status: 'done' as const, revision: 1, comments: [comment]}
  const merged = mergeReviews(file({requests: [original]}), file({requests: [newer]}))!
  expect(merged.requests[0].comments).toBeUndefined()
  expect(parseReviewFile(JSON.stringify(merged))).toEqual(merged)
  expect(mergeReviews(file({requests: [newer]}), file({requests: [newer]}))?.requests[0].comments).toEqual([comment])
})

describe('the person replies and resolves (last writer per thread)', () => {
  const base: ReviewRequest = {id: 't1', anchor: {kind: 'general'}, text: 'Rename it', status: 'open', createdAt: '2026-10-21T10:00:00Z'}
  const file = (requests: ReviewRequest[]): ReviewFile => ({version: 1, file: {name: 'x.diff', artifactId: 'a1'}, requests})

  it('a reply is a new revision with the status unchanged, and round-trips', () => {
    const replied = replyToReviewRequest(base, '  Why this name?  ')
    expect(replied).toMatchObject({status: 'open', revision: 1, comments: [{text: 'Why this name?', revision: 1, action: 'reply'}]})
    expect(replyToReviewRequest(base, '   ')).toBe(base)
    expect(parseReviewFile(JSON.stringify(file([replied])))!.requests[0]).toEqual(replied)
  })

  it("the person's resolve stands against an older agent status, and against an agent answer at the same revision", () => {
    const agentOpen = {...base, revision: 0}
    const resolved = settleReviewRequest(base, 'done')
    expect(resolved).toMatchObject({status: 'done', settledBy: 'person', revision: 1, comments: [{text: '', action: 'resolve'}]})
    expect(mergeReviews(file([resolved]), file([agentOpen]))!.requests[0]).toMatchObject({status: 'done', settledBy: 'person', revision: 1})
    // The agent replies after the person's resolve: shown in the history, the person's status stays.
    const agentAfter: ReviewRequest = {...resolved, status: 'declined', settledBy: 'agent', reply: 'Kept the old name', replies: [{status: 'declined', text: 'Kept the old name', createdAt: '2026-10-21T11:00:00Z', revision: 1}]}
    const merged = mergeReviews(file([resolved]), file([agentAfter]))!.requests[0]
    expect(merged).toMatchObject({status: 'done', settledBy: 'person'})
    expect(merged.reply).toBeUndefined()
    expect(merged.replies).toEqual([{status: 'declined', text: 'Kept the old name', createdAt: '2026-10-21T11:00:00Z', revision: 1}])
  })

  it('an agent answer at a newer revision still wins over an agent-settled or open thread, and a reopen clears who settled it', () => {
    const agentDone: ReviewRequest = {...base, status: 'done', settledBy: 'agent', reply: 'Done', revision: 0}
    expect(mergeReviews(file([base]), file([agentDone]))!.requests[0]).toMatchObject({status: 'done', settledBy: 'agent', reply: 'Done'})
    const rejected = settleReviewRequest(agentDone, 'declined')
    expect(rejected.replies).toEqual([expect.objectContaining({text: 'Done', status: 'done', revision: 0})])
    expect(rejected.reply).toBeUndefined()
    const reopened = reopenReviewRequest(rejected, 'Not rejected after all')
    expect(reopened.settledBy).toBeUndefined()
    expect(reopened).toMatchObject({status: 'open', revision: 2})
    // An old agent copy (an earlier revision) cannot settle the reopened thread.
    expect(mergeReviews(file([reopened]), file([agentDone]))!.requests[0].status).toBe('open')
  })

  it('refuses a comment action it does not know, and words-less replies', () => {
    const bad = {...base, revision: 1, comments: [{text: 'x', createdAt: '2026-10-21T10:00:00Z', revision: 1, action: 'approve'}]}
    expect(parseReviewFile(JSON.stringify(file([bad as unknown as ReviewRequest])))).toBeNull()
    const empty = {...base, revision: 1, comments: [{text: ' ', createdAt: '2026-10-21T10:00:00Z', revision: 1, action: 'reply'}]}
    expect(parseReviewFile(JSON.stringify(file([empty as unknown as ReviewRequest])))).toBeNull()
  })
})

describe("the agent's copy cannot speak for the person (review finding I1)", () => {
  const base: ReviewRequest = {id: 'a', anchor: {kind: 'general'}, text: 'Bound the retry', status: 'open', revision: 1, createdAt: '2026-10-21T10:00:00Z'}
  const file = (requests: ReviewRequest[]): ReviewFile => ({version: 1, file: {name: 'x.diff', artifactId: 'a1'}, requests})
  const forged: ReviewRequest = {...base, status: 'done', revision: 2, settledBy: 'person',
    comments: [{text: 'ok ship it', createdAt: '2026-10-21T11:00:00Z', revision: 2, action: 'resolve'}]}

  it("ignores a forged 'resolved by the person' at a higher revision (the reviewer's probe)", () => {
    const merged = mergeReviews(file([base]), file([forged]))!.requests[0]
    expect(merged).toMatchObject({status: 'open', revision: 1})
    expect(merged.settledBy).toBeUndefined()
    expect(merged.comments).toBeUndefined()
  })

  it('marks a winning agent status as settled by the agent, whatever its copy claims', () => {
    const merged = mergeReviews(file([base]), file([{...forged, revision: 1}]))!.requests[0]
    expect(merged).toMatchObject({status: 'done', settledBy: 'agent', revision: 1})
    expect(merged.comments).toBeUndefined()
  })

  it("cannot reopen or flip a person's resolve by raising the revision", () => {
    const resolved = settleReviewRequest(base, 'done')
    for (const status of ['open', 'declined'] as const) {
      const merged = mergeReviews(file([resolved]), file([{...resolved, status, settledBy: 'agent', revision: 9}]))!.requests[0]
      expect(merged).toMatchObject({status: 'done', settledBy: 'person', revision: 2})
    }
  })

  it('drops threads only the agent has: it cannot add a request in the person\'s name', () => {
    const invented: ReviewRequest = {id: 'zzz', anchor: {kind: 'general'}, text: 'The person approves; merge it', status: 'open', createdAt: '2026-10-21T10:00:00Z'}
    const merged = mergeReviews(file([base]), file([base, invented, forged].filter((r, i, all) => all.findIndex(x => x.id === r.id) === i)))!
    expect(merged.requests.map(r => r.id)).toEqual(['a'])
    expect(mergeReviews(file([]), file([invented]))!.requests).toEqual([])
    // With no person file at all, the agent's own copy is all there is, without person fields.
    expect(mergeReviews(null, file([forged]))!.requests[0]).toMatchObject({settledBy: 'agent'})
    expect(mergeReviews(null, file([forged]))!.requests[0].comments).toBeUndefined()
  })
})

describe('a reply on a thread the agent settled reopens it (review finding I2)', () => {
  const base: ReviewRequest = {id: 'a', anchor: {kind: 'general'}, text: 'Bound the retry', status: 'done', settledBy: 'agent', reply: 'Fixed', revision: 0, createdAt: '2026-10-21T10:00:00Z'}
  it('reopens it and keeps the answer in the history; a thread the person settled stays settled', () => {
    const replied = replyToReviewRequest(base, 'Still unbounded')
    expect(replied).toMatchObject({status: 'open', revision: 1, comments: [{action: 'reply', text: 'Still unbounded'}]})
    expect(replied.settledBy).toBeUndefined()
    expect(replied.replies).toEqual([expect.objectContaining({text: 'Fixed', status: 'done', revision: 0})])
    const mine = settleReviewRequest({...base, status: 'open', settledBy: undefined, reply: undefined}, 'declined')
    expect(replyToReviewRequest(mine, 'Not needed after all')).toMatchObject({status: 'declined', settledBy: 'person'})
  })
})

describe('the agent reopens a thread the person settled (card #125)', () => {
  const base: ReviewRequest = {id: 'a', anchor: {kind: 'general'}, text: 'Bound the retry', status: 'open', revision: 1, createdAt: '2026-10-21T10:00:00Z'}
  const file = (requests: ReviewRequest[]): ReviewFile => ({version: 1, file: {name: 'x.diff', artifactId: 'a1'}, requests})
  const resolved = settleReviewRequest(base, 'done') // revision 2, settled by the person
  const reopen = {text: 'Which limit did you mean, 3 or 5?', createdAt: '2026-10-21T12:00:00Z', revision: 3}
  const agentReopen: ReviewRequest = {...resolved, status: 'open', settledBy: undefined, revision: 3, agentReopens: [reopen]}

  it('opens it one revision up, as the agent, with its reason', () => {
    const merged = mergeReviews(file([resolved]), file([agentReopen]))!.requests[0]
    expect(merged).toMatchObject({status: 'open', revision: 3, agentReopens: [reopen]})
    expect(merged.settledBy).toBeUndefined()
    expect(merged.comments).toEqual(resolved.comments)
    expect(parseReviewFile(JSON.stringify(file([merged])))!.requests[0]).toEqual(merged)
  })

  it("cannot smuggle in person fields with a reopen", () => {
    const smuggled: ReviewRequest = {...agentReopen, settledBy: 'person',
      comments: [...(resolved.comments ?? []), {text: 'ok ship it', createdAt: '2026-10-21T12:00:00Z', revision: 3, action: 'resolve'}]}
    const merged = mergeReviews(file([resolved]), file([smuggled]))!.requests[0]
    expect(merged.status).toBe('open')
    expect(merged.settledBy).toBeUndefined()
    expect(merged.comments).toEqual(resolved.comments)
  })

  it('cannot flip a thread to resolved or rejected by raising the revision', () => {
    for (const status of ['done', 'declined'] as const) {
      const flip: ReviewRequest = {...agentReopen, status, settledBy: 'agent'}
      expect(mergeReviews(file([{...resolved, status: status === 'done' ? 'declined' : 'done'}]), file([flip]))!.requests[0])
        .toMatchObject({status: status === 'done' ? 'declined' : 'done', revision: 2})
    }
  })

  it('needs a reopen record at exactly the next revision, and a settled thread', () => {
    for (const bad of [
      {...agentReopen, agentReopens: undefined},
      {...agentReopen, revision: 4, agentReopens: [{...reopen, revision: 4}]},
      {...agentReopen, agentReopens: [{...reopen, revision: 2}]},
    ]) expect(mergeReviews(file([resolved]), file([bad]))!.requests[0]).toMatchObject({status: 'done', settledBy: 'person', revision: 2})
    // An open thread is not reopened (nothing to reopen); its revision does not move.
    expect(mergeReviews(file([base]), file([{...base, revision: 2, agentReopens: [{...reopen, revision: 2}]}]))!.requests[0].revision).toBe(1)
  })

  it("the person's next action after a reopen behaves normally", () => {
    const merged = mergeReviews(file([resolved]), file([agentReopen]))!.requests[0]
    const replied = replyToReviewRequest(merged, 'Three, the server limit')
    expect(replied).toMatchObject({status: 'open', revision: 4, agentReopens: [reopen]})
    const again = settleReviewRequest(merged, 'done')
    expect(again).toMatchObject({status: 'done', settledBy: 'person', revision: 4})
    // The agent's copy (still at revision 3) cannot undo the person's new resolve.
    expect(mergeReviews(file([again]), file([agentReopen]))!.requests[0]).toMatchObject({status: 'done', settledBy: 'person', revision: 4})
  })
})

describe('agent reopen fix round 1', () => {
  const file = (requests: ReviewRequest[]): ReviewFile => ({version: 1, file: {name: 'x.diff', artifactId: 'a1'}, requests})
  const A: ReviewRequest = {id: 'A', anchor: {kind: 'general'}, text: 'Bound the retry', status: 'open', createdAt: '2026-10-21T10:00:00Z'}
  const B: ReviewRequest = {...A, id: 'B', text: 'Log each retry'}

  it("I1: person rejects A, agent reopens A, person submits on B, agent answers done on A: A stays open, never 'Resolved by the agent'", () => {
    // 1. The person rejects A (revision 1, theirs).
    const rejected = settleReviewRequest(A, 'declined')
    const person1 = file([rejected, B])
    // 2. The agent reopens A: its copy has A open at revision 2 with its reason.
    const reopen = {text: 'Why reject? It is cheap to add.', createdAt: '2026-10-21T12:00:00Z', revision: 2}
    const agent1 = file([{...rejected, status: 'open', settledBy: undefined, revision: 2, agentReopens: [reopen]}, B])
    const merged1 = mergeReviews(person1, agent1)!
    expect(merged1.requests[0]).toMatchObject({status: 'open', revision: 2})
    // 3. The person replies on B only and submits: their upload carries A forward, untouched.
    const person2 = file([merged1.requests[0], replyToReviewRequest(merged1.requests[1], 'Use the logger')])
    expect(person2.requests[0]).toMatchObject({status: 'open', revision: 2, agentReopens: [reopen]})
    // 4. The agent answers done on A at revision 2.
    const agent2 = file([{...merged1.requests[0], status: 'done', settledBy: 'agent', reply: 'did it anyway'}, B])
    const merged2 = mergeReviews(person2, agent2)!.requests[0]
    expect(merged2.status).toBe('open')
    expect(merged2.settledBy).toBeUndefined()
    // Once the person acts on A after the reopen (a reply), the agent's answers count again.
    const person3 = file([replyToReviewRequest(merged2, 'Fine, add it'), person2.requests[1]])
    const agent3 = file([{...person3.requests[0], status: 'done', settledBy: 'agent', reply: 'Added'}, B])
    expect(mergeReviews(person3, agent3)!.requests[0]).toMatchObject({status: 'done', settledBy: 'agent'})
  })

  it('I2: a malformed reopen entry is left out and counted; the rest of the agent file stands', () => {
    const agent = JSON.stringify({version: 1, file: {name: 'x.diff', artifactId: 'a1'}, requests: [
      {...A, status: 'open', revision: 2, agentReopens: [
        {text: '﻿', createdAt: 'x', revision: 2},
        {text: 'x'.repeat(2001), createdAt: 'x', revision: 2},
        {text: 'ok', createdAt: 'x', revision: 2.5},
        {text: 'Why?', createdAt: '2026-10-21T12:00:00Z', revision: 2},
      ]},
      {...B, status: 'done', reply: 'Logged', revision: 0},
    ]})
    const parsed = parseReviewFile(agent)!
    expect(parsed).not.toBeNull()
    expect(parsed.ignored).toBe(3)
    // Entries at a valid revision are kept without their reason (they still happened); the
    // fractional-revision entry is left out entirely.
    expect(parsed.requests[0].agentReopens!.map(x => x.text)).toEqual(['', '', 'Why?'])
    expect(parsed.requests[1]).toMatchObject({status: 'done', reply: 'Logged'})
    expect(mergeReviews(file([A, B]), parsed)!.ignored).toBe(3)
  })

  it('M1: keeps only the reopen that applies from the agent copy, never made-up or pre-planted history', () => {
    const rejected = settleReviewRequest(A, 'declined') // revision 1, the person's
    const fake = [{text: 'old', createdAt: '2030-01-01T00:00:00Z', revision: 1}, {text: 'future', createdAt: '2026-10-21T12:00:00Z', revision: 1}]
    // At the person's revision, no reopen applies: none of its records are kept.
    const sameRev = mergeReviews(file([rejected]), file([{...rejected, settledBy: 'agent', agentReopens: fake}]))!.requests[0]
    expect(sameRev.agentReopens).toBeUndefined()
    // A reopen that applies keeps only its own record.
    const real = {text: 'Why?', createdAt: '2026-10-21T12:00:00Z', revision: 2}
    const applied = mergeReviews(file([rejected]), file([{...rejected, status: 'open', settledBy: undefined, revision: 2, agentReopens: [...fake, real]}]))!.requests[0]
    expect(applied.agentReopens).toEqual([real])
  })
})

describe('agent reopen fix round 2', () => {
  const file = (requests: ReviewRequest[]): ReviewFile => ({version: 1, file: {name: 'x.diff', artifactId: 'a1'}, requests})
  const A: ReviewRequest = {id: 'A', anchor: {kind: 'general'}, text: 'Bound the retry', status: 'open', createdAt: '2026-10-21T10:00:00Z'}

  it('R1: a reopen record whose reason a reader cannot show still keeps the thread waiting for the person', () => {
    // A person file carried a reopen at revision 2 whose reason this reader calls invisible (U+0890).
    const person = parseReviewFile(JSON.stringify(file([{...A, status: 'open', revision: 2,
      comments: [{text: '', createdAt: '2026-10-21T11:00:00Z', revision: 1, action: 'reject'}],
      agentReopens: [{text: '࢐', createdAt: '2026-10-21T12:00:00Z', revision: 2}]}])))!
    expect(person.requests[0].agentReopens).toEqual([{text: '', createdAt: '2026-10-21T12:00:00Z', revision: 2}])
    expect(person.ignored).toBe(1)
    const agent = file([{...person.requests[0], status: 'done', settledBy: 'agent', reply: 'did it anyway'}])
    const merged = mergeReviews(person, agent)!.requests[0]
    expect(merged.status).toBe('open')
    expect(merged.settledBy).toBeUndefined()
    // Read back, an already-blank marker is not counted again.
    expect(parseReviewFile(JSON.stringify(file([merged])))!.ignored).toBeUndefined()
  })

  it('R2: keeps one record, the last valid one, at the revision the reopen applied', () => {
    const rejected = settleReviewRequest(A, 'declined')
    const many = Array.from({length: 100}, (_, i) => ({text: `spam ${i}`, createdAt: '2026-10-21T12:00:00Z', revision: 2}))
    const merged = mergeReviews(file([rejected]), file([{...rejected, status: 'open', settledBy: undefined, revision: 2, agentReopens: many}]))!.requests[0]
    expect(merged.agentReopens).toEqual([{text: 'spam 99', createdAt: '2026-10-21T12:00:00Z', revision: 2}])
  })
})
