// One ResizeObserver per chat pane, shared by every message's pop-out check (PopOutMessage,
// BLG-449) instead of two per message (one on the pane, one on the card): a long transcript would
// otherwise accumulate hundreds of redundant observers all watching the same pane element.
// ResizeObserver already delivers its entries in a single batched callback once per frame, so one
// shared instance per pane also gives every registered card's check "once per frame", for free.
interface PaneEntry {
  ro: ResizeObserver
  cards: Map<Element, Set<() => void>>
}

const panes = new WeakMap<Element, PaneEntry>()

/** Registers `onCheck` to run whenever `pane` or `card` resizes (and once immediately); returns
 *  the unsubscribe. Safe to call repeatedly for the same pane: the ResizeObserver is created once
 *  and reused, growing and shrinking only as cards register and unregister. */
export function watchPaneCard(pane: Element, card: Element, onCheck: () => void): () => void {
  if (typeof ResizeObserver === 'undefined') return () => {}
  let entry = panes.get(pane)
  if (!entry) {
    const cards = new Map<Element, Set<() => void>>()
    const ro = new ResizeObserver(() => {
      for (const callbacks of cards.values()) for (const cb of callbacks) cb()
    })
    ro.observe(pane)
    entry = { ro, cards }
    panes.set(pane, entry)
  }
  const { ro, cards } = entry
  let callbacks = cards.get(card)
  if (!callbacks) {
    callbacks = new Set()
    cards.set(card, callbacks)
    ro.observe(card)
  }
  callbacks.add(onCheck)
  onCheck()
  return () => {
    callbacks!.delete(onCheck)
    if (callbacks!.size > 0) return
    cards.delete(card)
    ro.unobserve(card)
    if (cards.size > 0) return
    ro.disconnect()
    panes.delete(pane)
  }
}
