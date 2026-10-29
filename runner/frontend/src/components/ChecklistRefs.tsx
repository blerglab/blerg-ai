// Card references in the checklist (card #157): "#155 Dismiss in Needs you" in a Plan item (and in
// Live's checklist line) gets the chat's treatment (#153). Once the card is confirmed on the
// session's one board, "#155" is an inline link with the card's hovercard, and a click opens the
// ticket dialog. Beside the item, the card's state glyph (✓ ◐ ○, as pins have them) shows when it
// differs from the item's own: an unticked item whose card is done is a stale plan, seen at a
// glance. Until confirmed the text is exactly as written. The lookups go through a ref store (the
// chat's: batched, each number asked once per session, kept); the card's state follows its live
// read, read again with the pins.
import { useContext, type ReactNode } from 'react'
import { Hover, splitRefs } from '@blerglab/chat'
import { cardNumbers } from '../lib/cardRefs'
import { ChecklistRefsContext } from '../lib/checklistRefs'
import { STATE_GLYPH, type PinState } from '../lib/pins'

/** An item's text, a confirmed "#N" an inline link with the card's hovercard. */
export function CardRefText({ text }: { text: string }) {
  const ctx = useContext(ChecklistRefsContext)
  if (!ctx || ctx.cards.size === 0 && !ctx.references?.size) return <>{text}</>
  const out: ReactNode[] = []
  splitRefs(text).forEach((p, i) => {
    const n = 'key' in p && p.key.startsWith('card:') ? Number(p.key.slice(5)) : 0
    const t = ('key' in p ? ctx.references?.get(p.key) : undefined) ?? (n ? ctx.cards.get(n) : undefined)
    if (!t) { out.push(p.text); return }
    out.push(
      <Hover key={i} target={t}>
        <button type="button" className="md-ref plan-ref" data-testid="plan-ref" data-ref={`card:${n}`}
          aria-label={`Open card ${t.key}${t.title ? `: ${t.title}` : ''}`}
          onClick={e => { e.stopPropagation(); ctx.open(t) }}>{t.key}</button>
      </Hover>,
    )
  })
  return <>{out}</>
}

/** The state glyph of each card the item names whose state differs from the item's own. */
export function CardRefStates({ text, state }: { text: string; state: PinState }) {
  const ctx = useContext(ChecklistRefsContext)
  if (!ctx) return null
  const shown = cardNumbers(text).flatMap(n => {
    const s = ctx.states.get(n)
    return ctx.cards.has(n) && s && s !== state ? [{ n, s }] : []
  })
  if (shown.length === 0) return null
  return (
    <>
      {shown.map(({ n, s }) => {
        const words = `Card #${n} is ${STATE_GLYPH[s].words.toLowerCase()}; this item is ${STATE_GLYPH[state].words.toLowerCase()}`
        return <span key={n} className={`rail-plan-card-state ${s}`} role="img" aria-label={words} title={words} data-testid="plan-card-state" data-card-state={s}>{STATE_GLYPH[s].glyph}</span>
      })}
    </>
  )
}
