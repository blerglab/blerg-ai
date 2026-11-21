// BLG-468 §4 (sequence): hover and keyboard focus on participants, messages, notes and blocks,
// plus chip activation — the same live-DOM-wired-onto-already-drawn-SVG discipline as
// blergFlow/accessibility.ts, extended to this diagram kind's own data attributes.
export function wireSequenceInteractivity(root: SVGSVGElement, onChipOpen?: (key: string) => void): HTMLElement {
  const live = document.createElement('div')
  live.setAttribute('aria-live', 'polite')
  live.className = 'visual-blerg-sequence-live'
  live.style.cssText = 'position:absolute;width:1px;height:1px;overflow:hidden;clip:rect(0,0,0,0)'

  const announce = (el: Element) => { live.textContent = el.getAttribute('aria-label') ?? '' }
  const selector = '[data-blerg-seq-participant], [data-blerg-seq-message], [data-blerg-seq-note], [data-blerg-seq-block]'
  for (const el of root.querySelectorAll(selector)) {
    el.addEventListener('focus', () => announce(el))
    el.addEventListener('mouseenter', () => announce(el))
    const chipKey = el.getAttribute('data-blerg-chip-key')
    if (!chipKey || !onChipOpen) continue
    el.addEventListener('click', () => onChipOpen(chipKey))
    el.addEventListener('keydown', event => {
      const key = (event as KeyboardEvent).key
      if (key === 'Enter' || key === ' ') { event.preventDefault(); onChipOpen(chipKey) }
    })
  }
  return live
}
