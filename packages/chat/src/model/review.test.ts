import { describe, expect, it } from 'vitest'
import { emptyReview, mergeReviews, newRequestId, parseReviewFile, reviewFileName, type ReviewFile, type ReviewRequest } from './review'

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
