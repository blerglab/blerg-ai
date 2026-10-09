// The messages a review or a draw-over sends the session: the human-readable form of the review
// file, and the pins' notes with where each sits (docs/design/feedback-tools.md, "The message
// the session gets"). The attachment note is added by the ordinary send path, not here.
import { describePlace } from './anchor'
import type { ReviewFile } from './review'

const QUOTE_MAX = 160

const REPLY_HOW = 'Reply with `blerg-runner review reply <id> done|declined "<one line>"` per request, and publish the file again.'

const plural = (n: number, word: string) => `${n} ${word}${n === 1 ? '' : 's'}`

function shortQuote(q: string): string {
  return q.length > QUOTE_MAX ? `${q.slice(0, QUOTE_MAX - 1)}…` : q
}

/** The review as the session reads it: a header, the numbered requests (each with its id, where
 *  it points and what it asks), the edit's diff, and what to do with them. */
export function composeReviewMessage(review: ReviewFile, file: { name: string; version?: number }): string {
  const version = file.version !== undefined ? ` (v${file.version})` : ''
  const counts = plural(review.requests.length, 'request') + (review.edit ? ', 1 edit' : '')
  const out: string[] = [`Review of ${file.name}${version}: ${counts}.`]
  if (review.requests.length > 0) {
    out.push('')
    review.requests.forEach((r, i) => {
      const { anchor } = r
      const where: string[] = []
      if (anchor.heading) where.push(`Under "${anchor.heading}"`)
      if (anchor.page !== undefined) where.push(where.length ? `page ${anchor.page}` : `Page ${anchor.page}`)
      const ask = anchor.quote ? `"${shortQuote(anchor.quote)}" — ${r.text}` : r.text
      out.push(`${i + 1}. [${r.id}] ${where.length ? `${where.join(', ')}: ` : ''}${ask}`)
    })
  }
  if (review.edit) {
    out.push('', `Edited copy attached as ${file.name} (your copy, edited directly); the diff:`, '```diff')
    out.push(review.edit.diff.replace(/\n$/, ''), '```')
  }
  out.push('', review.edit
    ? `Apply the diff first (it is the author's own wording), then each request, in the file this was published from. ${REPLY_HOW}`
    : `Apply each request in the file this was published from. ${REPLY_HOW}`)
  return out.join('\n')
}

/** A numbered pin on an image: its note and its position as fractions of the image. */
export interface Pin { n: number; note: string; x: number; y: number }

/** The draw-over's message: each pin's number, place and note. */
export function composeMarkupMessage(fileName: string, pins: Pin[]): string {
  if (pins.length === 0) return `Marked up ${fileName} (see the attached image).`
  const out = [`Marked up ${fileName}: ${plural(pins.length, 'pin')}.`, '']
  for (const p of pins) {
    const place = describePlace({ x: p.x, y: p.y, w: 0, h: 0 })
    out.push(`${p.n}. (${place}) ${p.note}`.trimEnd())
  }
  return out.join('\n')
}
