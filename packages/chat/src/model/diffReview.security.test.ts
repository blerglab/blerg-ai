import { describe, expect, it } from 'vitest'
import { composeReviewMessage } from './feedback'
import { mergeReviews, parseReviewFile } from './review'
import { createDiffAnchor, diffAnchorContains } from './diffSelection'
import { parsePatch } from './patch'

const target = {
  file: 'src/example.ts', side: 'new', start: 11, end: 13,
  hunk: 2, fileIndex: 3, commit: 'abcdef01'.repeat(5),
}

function reviewJSON(diff: unknown = target, kind = 'text'): string {
  return JSON.stringify({
    version: 1,
    file: { name: 'changes.diff', artifactId: 'artifact-one', artifactVersion: 2 },
    requests: [{
      id: 'request-one', anchor: { kind, quote: 'selected code', diff },
      text: 'Please handle the empty input.', status: 'open', createdAt: '2026-01-01T12:00:00Z',
    }],
  })
}

describe('independent diff-anchor metadata security', () => {
  it('round-trips the full side/hunk/file/commit identity in version-1 review JSON', () => {
    const parsed = parseReviewFile(reviewJSON())
    expect(parsed).not.toBeNull()
    expect(parsed?.version).toBe(1)
    expect(parsed?.requests[0].anchor).toEqual({ kind: 'text', quote: 'selected code', diff: target })
    expect(parseReviewFile(JSON.stringify(parsed))).toEqual(parsed)
  })

  it.each([
    { ...target, side: 'old' },
    { ...target, hunk: 3 },
    { ...target, fileIndex: 4 },
    { ...target, commit: '12345678'.repeat(5) },
    { ...target, start: 12, end: 12 },
  ])('never collapses distinct targets that share a filename and selected quote', other => {
    const first = parseReviewFile(reviewJSON(target))
    const second = parseReviewFile(reviewJSON(other))
    expect(first?.requests[0].anchor).not.toEqual(second?.requests[0].anchor)
  })

  it.each(['start', 'end', 'hunk', 'fileIndex'])('requires %s to be a positive safe integer', key => {
    for (const bad of [0, -1, 1.5, '1', null, Number.MAX_SAFE_INTEGER + 1]) {
      expect(parseReviewFile(reviewJSON({ ...target, [key]: bad })), `${key}=${String(bad)}`).toBeNull()
    }
  })

  it.each([
    null, [], 'src/example.ts',
    { ...target, side: 'both' },
    { ...target, side: 'NEW' },
    { ...target, side: null },
    { ...target, start: 14, end: 13 },
    { ...target, file: '' },
    { ...target, file: 'x'.repeat(8193) },
    { ...target, file: 'src/\u0000example.ts' },
    { ...target, file: 'src/\u001b[2Jexample.ts' },
    { ...target, commit: 'not-a-commit' },
    { ...target, commit: 'a'.repeat(65) },
    { ...target, commit: 1234 },
    { ...target, commit: 'abcdef1' },
    { ...target, start: 1, end: 201 },
    { ...target, artifactId: '' },
    { ...target, artifactId: 'x'.repeat(201) },
    { ...target, artifactId: 1234 },
  ])('rejects malformed metadata without silently losing its diff identity', diff => {
    expect(parseReviewFile(reviewJSON(diff))).toBeNull()
  })

  it('does not accept diff identities attached to image-region anchors', () => {
    expect(parseReviewFile(reviewJSON(target, 'region'))).toBeNull()
  })

  it('does not carry arbitrary metadata keys into persisted review JSON', () => {
    const parsed = parseReviewFile(reviewJSON({ ...target, url: 'https://example.com/track', command: 'do not execute' }))
    expect(parsed?.requests[0].anchor).toEqual({ kind: 'text', quote: 'selected code', diff: target })
  })

  it('canonicalizes full uppercase commit hashes and preserves source-version identity', () => {
    const parsed = parseReviewFile(reviewJSON({ ...target, commit: target.commit.toUpperCase(), artifactId: 'source-version-two' }))
    expect(parsed?.requests[0].anchor).toMatchObject({ diff: { ...target, artifactId: 'source-version-two' } })
  })

  it('accepts full SHA-256 commit identities', () => {
    const commit = 'abcdef01'.repeat(8)
    const parsed = parseReviewFile(reviewJSON({ ...target, commit }))
    expect(parsed?.requests[0].anchor).toMatchObject({ diff: { ...target, commit } })
  })

  it('rejects excessive selected code instead of persisting an unbounded quote', () => {
    const review = JSON.parse(reviewJSON())
    review.requests[0].anchor.quote = 'x'.repeat(32_769)
    expect(parseReviewFile(JSON.stringify(review))).toBeNull()
  })

  it('keeps legitimate shell punctuation in a filename as inert display data', () => {
    const file = 'src/$(touch marker);`echo nope` [label](https://example.com).ts'
    const parsed = parseReviewFile(reviewJSON({ ...target, file }))
    expect(parsed?.requests[0].anchor).toMatchObject({ diff: { ...target, file } })
  })

  it('preserves an older person-authored target when merging the agent reply', () => {
    const person = parseReviewFile(reviewJSON(target))
    const agent = parseReviewFile(reviewJSON({ ...target, side: 'old', fileIndex: 8 }))
    expect(person).not.toBeNull()
    expect(agent).not.toBeNull()
    agent!.requests[0].status = 'done'
    agent!.requests[0].reply = 'Handled.'
    expect(mergeReviews(person, agent)?.requests[0]).toMatchObject({
      status: 'done', reply: 'Handled.',
      anchor: { kind: 'text', diff: target },
    })
    expect(person?.requests[0].status).toBe('open')
  })

  it('includes precise diff location and the existing reply instructions in feedback', () => {
    const review = parseReviewFile(reviewJSON())!
    expect(review).not.toBeNull()
    const message = composeReviewMessage(review, { name: 'changes.diff', version: 2 })
    expect(message).toContain('src/example.ts')
    expect(message).toContain('11')
    expect(message).toContain('13')
    expect(message).toContain('new')
    expect(message).toContain('abcdef0')
    expect(message).toContain('request-one')
    expect(message).toContain('blerg-runner review reply <id> done|declined')
  })
})

describe('independent diff target association', () => {
  const filePatch = 'diff --git a/src/example.ts b/src/example.ts\n--- a/src/example.ts\n+++ b/src/example.ts\n@@ -1 +1 @@\n-same old line\n+same new line\n'

  it('does not highlight another occurrence of the same path and quote in a multi-commit patch', () => {
    const patch = parsePatch(`commit ${'a'.repeat(40)}\n\n    First\n${filePatch}${filePatch}commit ${'b'.repeat(40)}\n\n    Second\n${filePatch}`)
    expect(patch.files).toHaveLength(3)
    const first = createDiffAnchor(patch, {fileIndex: 1, hunk: 1, side: 'new', line: 1})!
    expect(first).not.toBeNull()
    expect(diffAnchorContains(patch, first, {fileIndex: 1, hunk: 1, side: 'new', line: 1})).toBe(true)
    expect(diffAnchorContains(patch, first, {fileIndex: 2, hunk: 1, side: 'new', line: 1})).toBe(false)
    expect(diffAnchorContains(patch, first, {fileIndex: 3, hunk: 1, side: 'new', line: 1})).toBe(false)
  })

  it('never creates a path that the persisted review parser rejects for C1 controls', () => {
    const patch = parsePatch(filePatch)
    patch.files[0].newPath = 'src/\u0085example.ts'
    expect(createDiffAnchor(patch, {fileIndex: 1, hunk: 1, side: 'new', line: 1})).toBeNull()
  })

  it.each([Number.NaN, Number.POSITIVE_INFINITY, 0, -1, 1.5])('never associates a highlight with invalid line coordinate %s', line => {
    const patch = parsePatch(filePatch)
    const anchor = createDiffAnchor(patch, {fileIndex: 1, hunk: 1, side: 'new', line: 1})!
    expect(diffAnchorContains(patch, anchor, {fileIndex: 1, hunk: 1, side: 'new', line})).toBe(false)
  })
})
