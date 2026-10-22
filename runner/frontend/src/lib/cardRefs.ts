// Card numbers the session's text names (#153 in the chat, #157 in the checklist): "#N" is a card
// only on the session's one board (lib/sessionBoard.ts) and only when the card exists there, which
// its live read says (lib/liveBoardCard.ts: shared, cached). The tokenizer is the chat's splitRefs,
// so every surface (the chat, the checklist, #158's auto-tick) reads "#N" the same way.
import { splitRefs, type CardTarget } from '@blerglab/chat'
import { readLiveBoardCard } from './liveBoardCard'
import type { SessionBoard } from './sessionBoard'

/** The card numbers a text names, in order, once each (the chat's tokenizer). */
export function cardNumbers(text: string): number[] {
  const out: number[] = []
  for (const p of splitRefs(text)) {
    if ('key' in p && p.key.startsWith('card:')) {
      const n = Number(p.key.slice(5))
      if (n > 0 && !out.includes(n)) out.push(n)
    }
  }
  return out
}

/** Reads the cards the numbers name on the board, keeping those that exist (a batch for a ref store). */
export function boardCardsReader(board: SessionBoard, sessionId: string): (numbers: number[]) => Promise<Map<number, CardTarget>> {
  return async numbers => {
    const found = new Map<number, CardTarget>()
    await Promise.all(numbers.map(n => readLiveBoardCard(board.id, n, sessionId).then(
      r => { found.set(n, { kind: 'card', source: 'blerg-board', key: `#${n}`, boardId: board.id, title: r.card.title, ...(r.card.column_name ? { status: r.card.column_name } : {}) }) },
      () => { /* no such card here: the number stays text */ },
    )))
    return found
  }
}
