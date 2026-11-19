// BLG-485: fills a plain DOM element (a chart's data-table cell or pointer-tracked tooltip —
// accessibility.ts, tooltip.ts, vega.ts) with the same text-and-reference split chat's own
// markdown step uses (model/refs.ts's `splitRefs`), never a second parser and never innerHTML —
// only text nodes and plain `<span>`s carrying the parser's own typed `RefKey`, so a label can
// never inject markup. A card range ("#210-#211") is left as plain text, same as chat's own
// RefLinks.tsx leaves an unconfirmed range: a second resolver for a multi-card chip isn't this
// ticket's scope.
import { splitRefs } from '../model/refs'

/** `interactive`: false for a pointer-tracked tooltip (tooltip.ts, vega.ts) — it hides the moment
 *  the pointer leaves the chart's own hit area, which a hovercard's own hover-intent needs to
 *  reach first; styled (the same quiet, underlined look) but not marked for ChipOverlay.tsx to
 *  find, so it never lays a proxy over an element about to disappear. A known, deliberate gap:
 *  these refs are visually distinguished and still readable as text, not clickable/hoverable. */
export function fillRefText(el: HTMLElement, value: string, chipClass: string, interactive = true): void {
  el.replaceChildren()
  for (const piece of splitRefs(value)) {
    if (!('key' in piece)) { el.append(document.createTextNode(piece.text)); continue }
    const span = document.createElement('span')
    if (interactive) span.setAttribute('data-blerg-chip-key', piece.key)
    span.className = chipClass
    span.textContent = piece.text
    el.append(span)
  }
}
