import { describe, expect, it } from 'vitest'
import { emptyReview, mergeReviews, reopenReviewRequest, reviewReplyHistory, newRequestId, parseReviewFile, reviewFileName, type ReviewFile, type ReviewRequest } from './review'

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

  it('appends requests only the agent’s file has, and leaves unanswered ones open', () => {
    const person = file({ requests: [req({ id: 'one' })] })
    const agent = file({ requests: [req({ id: 'extra', status: 'done', reply: 'added' })] })
    const merged = mergeReviews(person, agent)!
    expect(merged.requests.map(r => [r.id, r.status])).toEqual([['one', 'open'], ['extra', 'done']])
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
      comments: [{text: 'Please reconsider', createdAt: expect.any(String), revision: 3}],
      replies: [{text: before.reply, status: 'declined', createdAt: before.createdAt, revision: 2}]})
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
    expect(reviewReplyHistory(request)).toEqual([old, {text: 'Latest legacy answer', status: 'declined', createdAt: request.createdAt, revision: 1}])
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


it('recovers explanations from a newer agent copy without accepting comments beyond the merged revision', () => {
  const comment = {text: 'Still broken', createdAt: '2026-01-02T00:00:00Z', revision: 1}
  const original = req({status: 'done'})
  const newer = {...original, status: 'done' as const, revision: 1, comments: [comment]}
  const merged = mergeReviews(file({requests: [original]}), file({requests: [newer]}))!
  expect(merged.requests[0].comments).toEqual([comment])
  expect(parseReviewFile(JSON.stringify(merged))).toEqual(merged)
  expect(mergeReviews(file({requests: [newer]}), file({requests: [newer]}))?.requests[0].comments).toEqual([comment])
})
