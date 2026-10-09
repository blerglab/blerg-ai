import { describe, expect, it } from 'vitest'
import { composeMarkupMessage, composeReviewMessage } from './feedback'
import type { ReviewFile, ReviewRequest } from './review'

const req = (id: string, text: string, anchor: ReviewRequest['anchor']): ReviewRequest =>
  ({ id, anchor, text, status: 'open', createdAt: '2026-10-06T10:00:00Z' })

const CLOSING_REPLY = 'Reply with `blerg-runner review reply <id> done|declined "<one line>"` per request, and publish the file again.'

describe('composeReviewMessage', () => {
  it('writes the header, the numbered requests, the diff and the closing paragraph', () => {
    const review: ReviewFile = {
      version: 1,
      file: { name: 'report.md', artifactId: 'a1', artifactVersion: 3 },
      requests: [
        req('ab12cd34', 'this is the median, not the mean', { kind: 'text', quote: 'the mean rose by 12%', heading: 'Results', page: 2, occurrence: 1 }),
        req('ef56gh78', 'say how many weeks', { kind: 'text', quote: 'we sampled weekly', heading: 'Method' }),
      ],
      edit: { artifactId: 'a2', diff: '@@ -14,7 +14,7 @@\n-We sampled weekly.\n+We sampled weekly for six weeks.\n' },
    }
    expect(composeReviewMessage(review, { name: 'report.md', version: 3 })).toBe([
      'Review of report.md (v3): 2 requests, 1 edit.',
      '',
      '1. [ab12cd34] Under "Results", page 2: "the mean rose by 12%" — this is the median, not the mean',
      '2. [ef56gh78] Under "Method": "we sampled weekly" — say how many weeks',
      '',
      'Edited copy attached as report.md (your copy, edited directly); the diff:',
      '```diff',
      '@@ -14,7 +14,7 @@',
      '-We sampled weekly.',
      '+We sampled weekly for six weeks.',
      '```',
      '',
      `Apply the diff first (it is the author's own wording), then each request, in the file this was published from. ${CLOSING_REPLY}`,
    ].join('\n'))
  })

  it('omits the version, heading, page and edit when absent, and counts one request', () => {
    const review: ReviewFile = {
      version: 1,
      file: { name: 'notes.md', artifactId: 'a1' },
      requests: [req('id000001', 'tighten this', { kind: 'text', quote: 'a loose sentence' })],
    }
    expect(composeReviewMessage(review, { name: 'notes.md' })).toBe([
      'Review of notes.md: 1 request.',
      '',
      '1. [id000001] "a loose sentence" — tighten this',
      '',
      `Apply each request in the file this was published from. ${CLOSING_REPLY}`,
    ].join('\n'))
  })

  it('names the page alone when there is no heading, and truncates a long quote', () => {
    const long = 'w'.repeat(200)
    const review: ReviewFile = {
      version: 1,
      file: { name: 'paper.pdf', artifactId: 'a1' },
      requests: [req('id000002', 'cut', { kind: 'text', quote: long, page: 4 })],
    }
    const msg = composeReviewMessage(review, { name: 'paper.pdf', version: 1 })
    expect(msg).toContain(`1. [id000002] Page 4: "${'w'.repeat(159)}…" — cut`)
    expect(msg.startsWith('Review of paper.pdf (v1): 1 request.\n')).toBe(true)
  })

  it('says 0 requests when only an edit is sent', () => {
    const review: ReviewFile = {
      version: 1,
      file: { name: 'r.md', artifactId: 'a1' },
      requests: [],
      edit: { artifactId: 'a2', diff: '@@ -1 +1 @@\n-a\n+b\n' },
    }
    const msg = composeReviewMessage(review, { name: 'r.md', version: 2 })
    expect(msg.startsWith('Review of r.md (v2): 0 requests, 1 edit.\n\nEdited copy attached as r.md')).toBe(true)
    expect(msg).toContain('```diff\n@@ -1 +1 @@\n-a\n+b\n```')
  })
})

describe('composeMarkupMessage', () => {
  it('lists each pin with where it sits', () => {
    expect(composeMarkupMessage('screenshot-3.png', [
      { n: 1, note: 'the title is clipped', x: 0.1, y: 0.1 },
      { n: 2, note: 'this button should be primary', x: 0.5, y: 0.5 },
      { n: 3, note: 'remove the footer here', x: 0.9, y: 0.95 },
    ])).toBe([
      'Marked up screenshot-3.png: 3 pins.',
      '',
      '1. (top left) the title is clipped',
      '2. (centre) this button should be primary',
      '3. (bottom right) remove the footer here',
    ].join('\n'))
  })

  it('counts one pin, and points at the image when there are none', () => {
    expect(composeMarkupMessage('s.png', [{ n: 1, note: 'here', x: 0.5, y: 0.1 }])).toBe('Marked up s.png: 1 pin.\n\n1. (top) here')
    expect(composeMarkupMessage('s.png', [])).toBe('Marked up s.png (see the attached image).')
  })
})
