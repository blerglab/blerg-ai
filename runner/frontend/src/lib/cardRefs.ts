// Card numbers the session's text names (#153 in the chat, #157 in the checklist): "#N" is a card
// only on the session's one board (lib/sessionBoard.ts) and only when the card exists there, which
// its live read says (lib/liveBoardCard.ts: shared, cached). The tokenizer is the chat's splitRefs,
// so every surface (the chat, the checklist, #158's auto-tick) reads "#N" the same way.
import { splitRefs, type CardTarget } from '@blerglab/chat'
import { readLiveBoardCard, signedInAccount } from './liveBoardCard'
import { apiFetch } from '../apiFetch'
import type { SessionBoard } from './sessionBoard'

/** The card numbers a text names, in order, once each (the chat's tokenizer). */
export function cardNumbers(text: string): number[] {
  const out: number[] = []
  for (const p of splitRefs(text)) {
    if ('cards' in p) { for (const n of p.cards) if (!out.includes(n)) out.push(n) }
    if ('key' in p && /^card:\d+$/.test(p.key)) {
      const n = Number(p.key.slice(5))
      if (n > 0 && !out.includes(n)) out.push(n)
    }
  }
  return out
}

let mapsFor = ''
const keyMaps = new Map<string, Promise<Map<string, string>>>()
window.addEventListener('blerg-identity-changed', () => keyMaps.clear())
export function resetBoardKeyMapsForTest(): void { keyMaps.clear(); mapsFor = '' }
async function boardForKey(key: string, session: string): Promise<string | undefined> {
  const who = signedInAccount()
  if (mapsFor !== who) { keyMaps.clear(); mapsFor = who }
  let pending = keyMaps.get(session)
  if (!pending) {
    pending = apiFetch('/api/session-boards').then(async r => {
      if (!r.ok) throw new Error('Board discovery failed')
      const { boards } = await r.json() as { boards: { id: string; key?: string; key_aliases?: string[] }[] }
      const map = new Map<string, string>()
      for (const board of boards) for (const k of [board.key, ...(board.key_aliases ?? [])]) if (k) map.set(k, board.id)
      return map
    })
    keyMaps.set(session, pending)
    void pending.catch(() => { if (keyMaps.get(session) === pending) keyMaps.delete(session) })
  }
  return (await pending).get(key)
}

/** Reads the cards the numbers name on the board, keeping those that exist (a batch for a ref store). */
export function boardCardsReader(board: SessionBoard, sessionId: string): (numbers: number[]) => Promise<Map<number, CardTarget>> {
  return async numbers => {
    const found = new Map<number, CardTarget>()
    let id = board.id
    if (/^[A-Z][A-Z0-9]{1,4}$/.test(id)) {
      try { id = await boardForKey(id, sessionId) ?? '' } catch { return found }
      if (!id) return found
    }
    await Promise.all(numbers.map(n => readLiveBoardCard(id, n, sessionId).then(
      r => { found.set(n, { kind: 'card', source: 'blerg-board', key: r.card.reference || `#${n}`, boardId: r.card.board_id, title: r.card.title, ...(r.card.column_name ? { status: r.card.column_name } : {}) }) },
      () => { /* no such card here: the number stays text */ },
    )))
    return found
  }
}
