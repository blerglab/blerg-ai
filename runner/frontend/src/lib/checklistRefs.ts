// Card references in the checklist (card #157): the confirmed cards a checklist names and their
// states, shared with the rows (components/ChecklistRefs.tsx draws them).
import { createContext, useContext, useEffect, useMemo, useSyncExternalStore } from 'react'
import { refStoreFor, type CardTarget, type RefKey } from '@blerglab/chat'
import { boardCardsReader, cardNumbers } from './cardRefs'
import { useLiveBoardCards } from './liveBoardCard'
import { cardState, type PinState } from './pins'
import type { SessionBoard } from './sessionBoard'

export interface ChecklistRefsValue {
  /** The confirmed cards, by number. */
  cards: ReadonlyMap<number, CardTarget>
  /** Their state now (their live read), by number. */
  states: ReadonlyMap<number, PinState>
  open: (t: CardTarget) => void
}

export const ChecklistRefsContext = createContext<ChecklistRefsValue | null>(null)

const NO_CARDS: ReadonlyMap<number, CardTarget> = new Map()

/** The cards the checklist's texts name, confirmed on the session's one board, and their states.
 *  `active`: the panel is on screen (the states are read again with the pins). */
export function useChecklistRefs(sessionId: string, texts: readonly string[], board: SessionBoard | null, active: boolean, open: (t: CardTarget) => void): ChecklistRefsValue {
  const store = refStoreFor(`checklist:${sessionId}`)
  const reader = useMemo(() => (board ? boardCardsReader(board, sessionId) : undefined), [board, sessionId])
  useEffect(() => { store.setResolvers({ cards: reader, commit: null }) }, [store, reader])
  const numbers = useMemo(() => [...new Set(texts.flatMap(cardNumbers))].sort((a, b) => a - b), [texts])
  const key = numbers.join(',')
  useEffect(() => { for (const n of numbers) store.want(`card:${n}` as RefKey) }, [store, key]) // eslint-disable-line react-hooks/exhaustive-deps
  // The confirmed set as a string, so a store change that confirms nothing new draws nothing again.
  const confirmedKey = useSyncExternalStore(store.subscribe, () => numbers.filter(n => store.get(`card:${n}`).state === 'card').join(','))
  const cards = useMemo(() => {
    if (!confirmedKey) return NO_CARDS
    const m = new Map<number, CardTarget>()
    for (const n of confirmedKey.split(',').map(Number)) {
      const s = store.get(`card:${n}`)
      if (s.state === 'card') m.set(n, s.target)
    }
    return m
  }, [store, confirmedKey])
  const refs = useMemo(() => [...cards.values()].map(t => ({ board: t.boardId!, number: Number(t.key.slice(1)) })), [cards])
  const live = useLiveBoardCards(refs, sessionId, active)
  const states = useMemo(() => {
    const m = new Map<number, PinState>()
    for (const r of refs) {
      const c = live.get(`${r.board}#${r.number}`)
      if (c) m.set(r.number, cardState(c))
    }
    return m
  }, [refs, live])
  return useMemo(() => ({ cards, states, open }), [cards, states, open])
}

/** Whether a text names a confirmed card (its title is then not a button: a link sits inside it). */
export function useHasCardRef(text: string): boolean {
  const ctx = useContext(ChecklistRefsContext)
  return !!ctx && cardNumbers(text).some(n => ctx.cards.has(n))
}

