// The messages a review or a draw-over sends the session: the human-readable form of the review
// file, and the pins' notes with where each sits (docs/design/feedback-tools.md, "The message
// the session gets"). The attachment note is added by the ordinary send path, not here.
import { describePlace } from './anchor'
import { diffAnchorLabel, reviewReplyHistory, type ReviewFile, type ReviewRequest } from './review'

const QUOTE_MAX = 160

const REPLY_HOW = 'Reply with `blerg-runner review reply <id> done|declined "<one line>"` per request, and publish the file again.'

const COMMENT_WORD = { reply: 'reply,', reopen: 'reopening', resolve: 'resolved it,', reject: 'rejected it,' } as const

/** The person's words for the agent, set off as a quote: each line starts "> ", so nothing they
 *  wrote can pass for a line of this message, and invisible format characters (bidi controls,
 *  zero-width) and control characters are dropped. '' when nothing is left. */
function quoted(text: string): string {
  // U+2028 and U+2029 are line breaks the agent may not read as such: lines like any other.
  const clean = [...text.replace(/[\p{Cf}]/gu, '').replace(/[\u2028\u2029]/g, '\n')].filter(ch => {
    const n = ch.charCodeAt(0)
    return ch === '\n' || ch === '\t' || !(n < 32 || (n >= 127 && n <= 159))
  }).join('').trim()
  return clean ? clean.split('\n').map(line => `> ${line}`).join('\n') : ''
}

/** Someone else's words on one line, between double quotes: format and control characters
 *  dropped, every run of whitespace (line breaks included) one space, inner double quotes single. */
function oneLineQuote(text: string): string {
  const words = [...text.replace(/[\p{Cf}]/gu, '')].map(ch => {
    const n = ch.charCodeAt(0)
    return n < 32 || (n >= 127 && n <= 159) ? ' ' : ch
  }).join('').split(/\s+/u).filter(Boolean).join(' ')
  return `"${words.replace(/"/g, "'")}"`
}

/** Where a thread stands after the person's latest action, for the agent. */
function threadState(r: ReviewRequest): string {
  if (r.status !== 'open' && r.settledBy === 'person') {
    return `${r.status === 'done' ? 'Resolved' : 'Rejected'} by the person (revision ${r.revision}): nothing to change unless their words ask for it; a reply is optional and does not change this status.`
  }
  if (r.status !== 'open') return `Resolved earlier by you; the person replied (revision ${r.revision}). Answer the reply.`
  const last = r.comments?.[r.comments.length - 1]
  const reopen = r.agentReopens?.[r.agentReopens.length - 1]
  if (reopen && reopen.revision >= (last?.revision ?? 0) && last?.action !== 'reply') {
    return `Open: you reopened it (revision ${reopen.revision}); the person has not answered yet.`
  }
  if (reopen && last?.action === 'reply' && last.revision > reopen.revision) {
    return `Open: the person answered your reopen (revision ${r.revision}).`
  }
  if (last?.action === 'reply') {
    return reviewReplyHistory(r).length
      ? `Open again: the person replied to your answer (revision ${r.revision}). Answer the reply.`
      : `Open thread; the person replied (revision ${r.revision}).`
  }
  return `Reopened thread, revision ${r.revision}.`
}

const plural = (n: number, word: string) => `${n} ${word}${n === 1 ? '' : 's'}`

function shortQuote(q: string): string {
  return q.length > QUOTE_MAX ? `${q.slice(0, QUOTE_MAX - 1)}…` : q
}

/** The review as the session reads it: a header, the numbered requests (each with its id, where
 *  it points and what it asks), the edit's diff, and what to do with them. */
export function composeReviewMessage(review: ReviewFile, file: { name: string; version?: number }): string {
  const isDiff = /\.(diff|patch)$/i.test(file.name) || review.requests.some(r => r.anchor.diff !== undefined)
  const version = file.version !== undefined ? ` (v${file.version})` : ''
  const counts = plural(review.requests.length, 'request') + (review.edit ? ', 1 edit' : '')
  const out: string[] = [`Review of ${file.name}${version}: ${counts}.`]
  if (review.requests.length > 0) {
    out.push('')
    review.requests.forEach((r, i) => {
      const { anchor } = r
      const where: string[] = []
      if (anchor.kind === 'general') where.push('General comment')
      if (anchor.diff) where.push(diffAnchorLabel(anchor.diff))
      if (anchor.heading) where.push(`Under "${anchor.heading}"`)
      if (anchor.page !== undefined) where.push(where.length ? `page ${anchor.page}` : `Page ${anchor.page}`)
      const ask = anchor.quote ? `"${shortQuote(anchor.quote)}" — ${r.text}` : r.text
      out.push(`${i + 1}. [${r.id}] ${where.length ? `${where.join(', ')}: ` : ''}${ask}`)
      if (r.revision) {
        out.push(threadState(r))
        // The person's comments and the agent's own reopens, in revision order.
        const said: Array<{revision: number; lines: string[]}> = [
          ...(r.comments ?? []).map(comment => {
            const head = `Person · ${COMMENT_WORD[comment.action ?? 'reopen']} revision ${comment.revision}`
            const words = quoted(comment.text)
            return {revision: comment.revision, lines: words ? [`${head}:`, words] : [`${head}.`]}
          }),
          ...(r.agentReopens ?? []).map(reopen => ({revision: reopen.revision, lines: [reopen.text ? `Your reopen, revision ${reopen.revision}: ${oneLineQuote(reopen.text)}` : `Your reopen, revision ${reopen.revision}.`]})),
        ]
        // A stable sort: at one revision the person's words come first.
        for (const item of said.sort((x, y) => x.revision - y.revision)) out.push(...item.lines)
        for (const reply of reviewReplyHistory(r)) out.push(`Previous agent answer · ${reply.status === 'done' ? 'Resolved' : 'Rejected'} · revision ${reply.revision ?? 0}:`, reply.text)
      }
      if (r.suggestion) {
        const runs = r.suggestion.replacement.match(/`+/g) ?? []
        const fence = '`'.repeat(Math.max(3, ...runs.map(run => run.length + 1)))
        out.push('Suggested replacement for the selected new-side lines:', `${fence}suggestion`, r.suggestion.replacement, fence)
      }
    })
  }
  if (review.edit) {
    out.push('', `Edited copy attached as ${file.name} (your copy, edited directly); the diff:`, '```diff')
    out.push(review.edit.diff.replace(/\n$/, ''), '```')
  }
  if (isDiff) {
    out.push('', 'Apply each anchored request in the source files named by its patch anchor. Address general comments as well. Suggestions describe replacement code; check the current source before applying them. Keep the published diff name unchanged so its threads carry forward. Then publish an updated diff with `blerg-runner diff <range>`. Reply with `blerg-runner review reply <id> done|declined "<one line>"` per request.')
    return out.join('\n')
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
